import assert from 'node:assert/strict'
import { performance } from 'node:perf_hooks'

import {
  createRedisPageDataChangeStore,
  pageDataScope,
  type PageDataDomain,
  type PageDataRedisClient,
  type PageDataRevisionToken
} from '../../modules/page-data/page-data-change.service.js'

interface CommandCounts {
  eval: number
  get: number
  set: number
  sendCommand: number
}

interface CountingRedisClient extends PageDataRedisClient {
  commandCounts(): CommandCounts
  resetCommandCounts(): void
}

interface BenchmarkResult {
  name: string
  domains: number
  concurrency: number
  confirms: number
  p50Ms: number
  p95Ms: number
  p99Ms: number
  redisCommandsPerConfirm: CommandCounts
}

const iterations = positiveInteger(process.env.JUHE_PAGE_DATA_CONFIRM_BENCHMARK_ITERATIONS, 2_000)
const scope = pageDataScope({ viewerSystemAccountId: 'benchmark-user', viewScope: 'self' })
const scenarios = [
  { name: '1-domain-sequential', domains: ['accounts.runtime'] as PageDataDomain[], concurrency: 1 },
  { name: '3-domain-sequential', domains: ['accounts.static', 'accounts.runtime', 'usage.records'] as PageDataDomain[], concurrency: 1 },
  { name: '4-domain-sequential', domains: ['accounts.static', 'accounts.runtime', 'usage.records', 'announcements.public'] as PageDataDomain[], concurrency: 1 },
  { name: '1-domain-concurrent-20', domains: ['accounts.runtime'] as PageDataDomain[], concurrency: 20 },
  { name: '3-domain-concurrent-20', domains: ['accounts.static', 'accounts.runtime', 'usage.records'] as PageDataDomain[], concurrency: 20 },
  { name: '4-domain-concurrent-20', domains: ['accounts.static', 'accounts.runtime', 'usage.records', 'announcements.public'] as PageDataDomain[], concurrency: 20 }
]

const results: BenchmarkResult[] = []
for (const scenario of scenarios) results.push(await runScenario(scenario))
console.log(JSON.stringify({ mode: 'fake', iterationsPerScenario: iterations, results }, null, 2))

async function runScenario(input: { name: string; domains: PageDataDomain[]; concurrency: number }): Promise<BenchmarkResult> {
  const client = createBenchmarkRedisClient()
  const store = createRedisPageDataChangeStore({
    client,
    keyPrefix: `benchmark:page-data:${input.name}`,
    epoch: 'benchmark-epoch',
    now: () => new Date('2026-07-18T00:00:00.000Z')
  })
  const initial = await store.confirm(scope, Object.fromEntries(input.domains.map((domain) => [domain, undefined])))
  const tokens = Object.fromEntries(input.domains.map((domain) => {
    const token = initial.domains[domain]?.token
    assert(token)
    return [domain, token]
  })) as Record<string, PageDataRevisionToken>

  for (let index = 0; index < 100; index += 1) await store.confirm(scope, tokens)
  client.resetCommandCounts()
  const latencies: number[] = []
  let nextIndex = 0
  await Promise.all(Array.from({ length: input.concurrency }, async () => {
    while (nextIndex < iterations) {
      nextIndex += 1
      const startedAt = performance.now()
      const result = await store.confirm(scope, tokens)
      latencies.push(performance.now() - startedAt)
      assert(input.domains.every((domain) => result.domains[domain]?.action === 'unchanged'))
    }
  }))

  const counts = client.commandCounts()
  const redisCommandsPerConfirm = {
    eval: counts.eval / iterations,
    get: counts.get / iterations,
    set: counts.set / iterations,
    sendCommand: counts.sendCommand / iterations
  }
  assert.deepEqual(redisCommandsPerConfirm, { eval: 1, get: 0, set: 0, sendCommand: 0 })
  latencies.sort((left, right) => left - right)
  return {
    name: input.name,
    domains: input.domains.length,
    concurrency: input.concurrency,
    confirms: iterations,
    p50Ms: percentile(latencies, 50),
    p95Ms: percentile(latencies, 95),
    p99Ms: percentile(latencies, 99),
    redisCommandsPerConfirm
  }
}

function createBenchmarkRedisClient(): CountingRedisClient {
  const strings = new Map<string, string>()
  const counts: CommandCounts = { eval: 0, get: 0, set: 0, sendCommand: 0 }
  return {
    commandCounts: () => ({ ...counts }),
    resetCommandCounts: () => { counts.eval = 0; counts.get = 0; counts.set = 0; counts.sendCommand = 0 },
    async get(key) { counts.get += 1; return strings.get(key) ?? null },
    async set(key, value, options) {
      counts.set += 1
      if (options?.NX === true && strings.has(key)) return null
      strings.set(key, value)
      return 'OK'
    },
    async eval(script, options) {
      counts.eval += 1
      if (!script.includes('page_data_confirm_v1')) throw new Error('benchmark fake 仅支持 confirm Lua')
      const [epochKey, ...streamKeys] = options.keys
      const [proposedEpoch, domainCountText] = options.arguments
      assert(epochKey && proposedEpoch && domainCountText)
      if (!strings.has(epochKey)) strings.set(epochKey, proposedEpoch)
      const result: unknown[] = [strings.get(epochKey)!]
      for (let index = 0; index < Number(domainCountText); index += 1) {
        const sequenceKey = streamKeys[index * 3]
        const resetSequenceKey = streamKeys[index * 3 + 2]
        assert(sequenceKey && resetSequenceKey)
        result.push([Number(strings.get(sequenceKey) ?? 0), Number(strings.get(resetSequenceKey) ?? 0), []])
      }
      return result
    },
    async sendCommand() { counts.sendCommand += 1; throw new Error('benchmark confirm 不允许 sendCommand') }
  }
}

function percentile(sorted: number[], value: number): number {
  const index = Math.max(0, Math.ceil(sorted.length * value / 100) - 1)
  return Number((sorted[index] ?? 0).toFixed(4))
}

function positiveInteger(value: string | undefined, fallback: number): number {
  const parsed = Number(value)
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : fallback
}
