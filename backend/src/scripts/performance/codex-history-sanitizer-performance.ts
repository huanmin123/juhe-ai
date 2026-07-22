import assert from 'node:assert/strict'
import { performance } from 'node:perf_hooks'

import { sanitizeCodexResponseHistoryItems } from '../../modules/gateway/codex-responses/request-history-sanitizer.js'
import type { CodexHistorySanitizerContext } from '../../modules/gateway/codex-responses/request-history-types.js'

interface SanitizerBenchmarkResult {
  itemCount: number
  dirtyRate: number
  iterations: number
  p50Ms: number
  p95Ms: number
  copiedItemCount: number
  cleanReferenceReuseRate: number
}

const sizes = [10, 100, 1000, 10_000]
const results: SanitizerBenchmarkResult[] = []

for (const itemCount of sizes) {
  results.push(runCase(itemCount, 0))
  results.push(runCase(itemCount, 0.01))
}

for (const result of results) {
  if (result.dirtyRate === 0) {
    assert.equal(result.copiedItemCount, 0, 'clean 历史不得复制 item')
    assert.equal(result.cleanReferenceReuseRate, 1, 'clean 历史必须 100% 复用数组引用')
  } else {
    assert.equal(result.copiedItemCount, Math.max(1, Math.floor(result.itemCount * result.dirtyRate)), 'dirty 历史只应复制命中的 item')
  }
}

const clean = results.filter((result) => result.dirtyRate === 0)
const normalizedP95 = clean.map((result) => result.p95Ms / result.itemCount)
const minNormalized = Math.min(...normalizedP95.filter((value) => value > 0))
const maxNormalized = Math.max(...normalizedP95)
assert.ok(maxNormalized <= minNormalized * 20, `sanitizer 单 item p95 波动过大，疑似非线性：${JSON.stringify(normalizedP95)}`)

console.log(JSON.stringify({
  benchmark: 'codex-history-sanitizer',
  node: process.version,
  results
}, null, 2))

function runCase(itemCount: number, dirtyRate: number): SanitizerBenchmarkResult {
  const input = createItems(itemCount, dirtyRate)
  const context: CodexHistorySanitizerContext = {
    store: true,
    sourceScopeKey: 'account:a',
    targetScopeKey: 'account:a',
    targetPersistenceScope: 'account',
    contractRevision: 'codex-responses-2026-07-11-r1'
  }
  const iterations = Math.max(20, Math.floor(200_000 / itemCount))
  const timings: number[] = []
  let copiedItemCount = 0
  let reusedCount = 0

  for (let warmup = 0; warmup < 5; warmup += 1) {
    sanitizeCodexResponseHistoryItems(input, context)
  }
  for (let iteration = 0; iteration < iterations; iteration += 1) {
    const startedAt = performance.now()
    const result = sanitizeCodexResponseHistoryItems(input, context)
    timings.push(performance.now() - startedAt)
    if (result.items === input) reusedCount += 1
    if (iteration === 0) {
      copiedItemCount = result.items.reduce<number>(
        (count, item, index) => count + (item === input[index] ? 0 : 1),
        0
      )
    }
  }
  timings.sort((a, b) => a - b)
  return {
    itemCount,
    dirtyRate,
    iterations,
    p50Ms: percentile(timings, 0.5),
    p95Ms: percentile(timings, 0.95),
    copiedItemCount,
    cleanReferenceReuseRate: reusedCount / iterations
  }
}

function createItems(itemCount: number, dirtyRate: number): Array<Record<string, unknown>> {
  const dirtyCount = dirtyRate > 0 ? Math.max(1, Math.floor(itemCount * dirtyRate)) : 0
  return Array.from({ length: itemCount }, (_, index) => ({
    type: 'message',
    id: index < dirtyCount ? `fc_wrong_${index}` : `msg_clean_${index}`,
    role: 'assistant',
    content: [{ type: 'output_text', text: `message-${index}` }]
  }))
}

function percentile(sorted: number[], ratio: number): number {
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * ratio) - 1))
  return Number((sorted[index] ?? 0).toFixed(6))
}
