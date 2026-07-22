import assert from 'node:assert/strict'
import { spawnSync, type SpawnSyncReturns } from 'node:child_process'
import { performance } from 'node:perf_hooks'

import type { CodexProtocolGuardGlobalMode } from '../../config/runtime.js'
import {
  codexResponsesGuardDiagnosticLimit,
  createCodexResponsesGuardMarker,
  createCodexResponsesResponseGuard,
  type CodexResponsesGuardSseResult,
  type CodexResponsesResponseGuard
} from '../../modules/gateway/codex-responses/response-guard.js'
import { GatewayDownstreamCommitState } from '../../modules/gateway/response/downstream-commit-state.js'

type JsonRecord = Record<string, unknown>
type Protocol = 'json' | 'sse'
type Scenario = 'clean' | 'dirty_1pct'

interface StructureCounts {
  guardConstructionCount: number
  guardInputItemCount: number
  diagnosticCount: number
  omittedDiagnosticCount: number
  repairedItemCount: number
  copiedRootCount: number
  copiedArrayCount: number
  copiedItemCount: number
  idFactoryCallCount: number
  identityCount: number
  itemIdOwnerCount: number
}

interface BenchmarkResult {
  protocol: Protocol
  mode: CodexProtocolGuardGlobalMode
  scenario: Scenario
  itemCount: number
  dirtyItemCount: number
  iterations: number
  p50Ms: number
  p95Ms: number
  itemsPerSecond: number
  heapDeltaBytes: number
  structure: StructureCounts
}

interface TimedSample {
  elapsedMs: number
  structure: StructureCounts
}

interface SseMemoryGate {
  sameIdentityDeltaCount: number
  sameIdentityRetainedHeapBytes: number
  identityCountSmall: number
  identityCountLarge: number
  identityRetainedHeapBytesSmall: number
  identityRetainedHeapBytesLarge: number
  identityHeapGrowthLimitBytes: number
}

const modes = ['off', 'shadow', 'safe_repair'] as const
const sizes = [100, 1_000, 10_000] as const
const scenarios = ['clean', 'dirty_1pct'] as const

assert.equal(typeof global.gc, 'function', '必须通过 node --expose-gc 运行 Codex Responses guard 性能门禁')
assertRuntimeModeContract()

const results: BenchmarkResult[] = []
for (const protocol of ['json', 'sse'] as const) {
  for (const mode of modes) {
    for (const scenario of scenarios) {
      for (const itemCount of sizes) {
        results.push(protocol === 'json'
          ? runJsonCase(mode, scenario, itemCount)
          : runSseCase(mode, scenario, itemCount))
      }
    }
  }
}

assertStructureContracts(results)
const cpuSlopes = assertLinearCpuScaling(results)
const sseMemoryGate = assertSseMemoryBounds()

console.log(JSON.stringify({
  benchmark: 'codex-responses-contract-guard',
  node: process.version,
  runtimeModeDefault: 'shadow',
  results,
  cpuSlopes,
  sseMemoryGate
}, null, 2))

function runJsonCase(
  mode: CodexProtocolGuardGlobalMode,
  scenario: Scenario,
  itemCount: number
): BenchmarkResult {
  const dirtyItemCount = dirtyCount(itemCount, scenario)
  const document = createJsonDocument(itemCount, dirtyItemCount)
  const iterations = iterationCount(itemCount)
  const timings: number[] = []

  for (let index = 0; index < 3; index += 1) runJsonOnce(mode, document, dirtyItemCount)
  let firstStructure: StructureCounts | undefined
  for (let iteration = 0; iteration < iterations; iteration += 1) {
    const sample = runJsonOnce(mode, document, dirtyItemCount)
    timings.push(sample.elapsedMs)
    firstStructure ??= sample.structure
  }

  const heapDeltaBytes = measureRetainedHeap(() => {
    if (mode === 'off') return () => undefined
    const run = inspectJson(mode, document)
    return () => run.guard?.dispose()
  })
  return benchmarkResult('json', mode, scenario, itemCount, dirtyItemCount, iterations, timings, heapDeltaBytes, required(firstStructure))
}

function runJsonOnce(
  mode: CodexProtocolGuardGlobalMode,
  document: JsonRecord,
  dirtyItemCount: number
): TimedSample {
  const startedAt = performance.now()
  if (mode === 'off') {
    return {
      elapsedMs: performance.now() - startedAt,
      structure: emptyStructureCounts()
    }
  }
  const run = inspectJson(mode, document)
  const elapsedMs = performance.now() - startedAt
  try {
    const originalItems = document.output as JsonRecord[]
    const outputItems = run.result.value.output as JsonRecord[]
    const copiedRootCount = run.result.value === document ? 0 : 1
    const copiedArrayCount = outputItems === originalItems ? 0 : 1
    const copiedItemCount = outputItems.reduce(
      (count, item, index) => count + (item === originalItems[index] ? 0 : 1),
      0
    )
    const diagnosticCount = run.result.issues.length
    const omittedDiagnosticCount = run.result.omittedIssueCount
    const repairedItemCount = run.result.outcome === 'repaired_safe' || run.result.outcome === 'repaired_bridge'
      ? diagnosticCount + omittedDiagnosticCount
      : 0
    if (mode === 'safe_repair' && dirtyItemCount === 0) {
      assert.equal(run.idFactoryCallCount, 0, 'clean safe_repair 不得进入 ID repair factory')
    }
    return {
      elapsedMs,
      structure: {
        guardConstructionCount: run.guardConstructionCount,
        guardInputItemCount: originalItems.length,
        diagnosticCount,
        omittedDiagnosticCount,
        repairedItemCount,
        copiedRootCount,
        copiedArrayCount,
        copiedItemCount,
        idFactoryCallCount: run.idFactoryCallCount,
        identityCount: 0,
        itemIdOwnerCount: 0
      }
    }
  } finally {
    run.guard?.dispose()
  }
}

function inspectJson(
  mode: Exclude<CodexProtocolGuardGlobalMode, 'off'>,
  document: JsonRecord
): {
  result: ReturnType<CodexResponsesResponseGuard['inspectJson']>
  guard: CodexResponsesResponseGuard
  guardConstructionCount: number
  idFactoryCallCount: number
} {
  let idFactoryCallCount = 0
  const guard = createCodexResponsesResponseGuard({
    marker: createCodexResponsesGuardMarker('raw_upstream'),
    downstreamCommitState: new GatewayDownstreamCommitState(),
    mode,
    createItemId: ({ prefix, sequence, outputIndex }) => {
      idFactoryCallCount += 1
      return `${prefix}_bench_${outputIndex}_${sequence}`
    }
  })
  return {
    result: guard.inspectJson(document),
    guard,
    guardConstructionCount: 1,
    idFactoryCallCount
  }
}

function runSseCase(
  mode: CodexProtocolGuardGlobalMode,
  scenario: Scenario,
  itemCount: number
): BenchmarkResult {
  const dirtyItemCount = dirtyCount(itemCount, scenario)
  const events = createSseEvents(itemCount, dirtyItemCount)
  const iterations = iterationCount(itemCount)
  const timings: number[] = []

  for (let index = 0; index < 3; index += 1) runSseOnce(mode, events, dirtyItemCount)
  let firstStructure: StructureCounts | undefined
  for (let iteration = 0; iteration < iterations; iteration += 1) {
    const sample = runSseOnce(mode, events, dirtyItemCount)
    timings.push(sample.elapsedMs)
    firstStructure ??= sample.structure
  }

  const heapDeltaBytes = measureRetainedHeap(() => {
    const guard = mode === 'off' ? undefined : createGuard(mode)
    if (guard) for (const event of events) guard.inspectParsedSse({ responseResourceId: 'resp_bench', event })
    return () => guard?.dispose()
  })
  return benchmarkResult('sse', mode, scenario, itemCount, dirtyItemCount, iterations, timings, heapDeltaBytes, required(firstStructure))
}

function runSseOnce(
  mode: CodexProtocolGuardGlobalMode,
  events: readonly JsonRecord[],
  dirtyItemCount: number
): TimedSample {
  const startedAt = performance.now()
  let guardConstructionCount = 0
  let lastResult: CodexResponsesGuardSseResult | undefined
  const guard = mode === 'off' ? undefined : createGuard(mode)
  if (guard) {
    guardConstructionCount = 1
    for (const event of events) {
      lastResult = guard.inspectParsedSse({ responseResourceId: 'resp_bench', event })
    }
  }
  const elapsedMs = performance.now() - startedAt
  try {
    const snapshot = guard?.snapshot()
    const diagnosticCount = snapshot?.diagnostics.length ?? 0
    const omittedDiagnosticCount = snapshot?.omittedDiagnosticCount ?? 0
    if (mode !== 'off' && dirtyItemCount > 0) {
      assert(lastResult, 'dirty SSE 必须产生检查结果')
    }
    return {
      elapsedMs,
      structure: {
        guardConstructionCount,
        guardInputItemCount: mode === 'off' ? 0 : events.length,
        diagnosticCount,
        omittedDiagnosticCount,
        repairedItemCount: 0,
        copiedRootCount: 0,
        copiedArrayCount: 0,
        copiedItemCount: 0,
        idFactoryCallCount: 0,
        identityCount: snapshot?.stream.identityCount ?? 0,
        itemIdOwnerCount: snapshot?.stream.itemIdOwnerCount ?? 0
      }
    }
  } finally {
    guard?.dispose()
  }
}

function createGuard(mode: Exclude<CodexProtocolGuardGlobalMode, 'off'>): CodexResponsesResponseGuard {
  return createCodexResponsesResponseGuard({
    marker: createCodexResponsesGuardMarker('raw_upstream'),
    downstreamCommitState: new GatewayDownstreamCommitState(),
    mode
  })
}

function benchmarkResult(
  protocol: Protocol,
  mode: CodexProtocolGuardGlobalMode,
  scenario: Scenario,
  itemCount: number,
  dirtyItemCount: number,
  iterations: number,
  timings: number[],
  heapDeltaBytes: number,
  structure: StructureCounts
): BenchmarkResult {
  timings.sort((left, right) => left - right)
  const p50Ms = percentile(timings, 0.5)
  return {
    protocol,
    mode,
    scenario,
    itemCount,
    dirtyItemCount,
    iterations,
    p50Ms,
    p95Ms: percentile(timings, 0.95),
    itemsPerSecond: rounded(itemCount / Math.max(p50Ms / 1_000, Number.EPSILON), 2),
    heapDeltaBytes,
    structure
  }
}

function assertStructureContracts(allResults: readonly BenchmarkResult[]): void {
  for (const result of allResults) {
    const structure = result.structure
    if (result.mode === 'off') {
      assert.equal(structure.guardConstructionCount, 0, 'off 不得构造 response guard')
      assert.equal(structure.guardInputItemCount, 0, 'off 不得把 Responses items 交给 guard')
      assert.equal(structure.diagnosticCount, 0, 'off 不得产生 guard 诊断')
      continue
    }

    assert.equal(structure.guardConstructionCount, 1)
    assert.equal(structure.guardInputItemCount, result.itemCount, 'active guard 必须接收全部 item/event')
    if (result.scenario === 'clean') {
      assert.equal(structure.diagnosticCount, 0)
      assert.equal(structure.omittedDiagnosticCount, 0)
      assert.equal(structure.copiedRootCount, 0, 'clean JSON/SSE 必须复用根对象')
      assert.equal(structure.copiedArrayCount, 0, 'clean JSON/SSE 不得复制集合')
      assert.equal(structure.copiedItemCount, 0, 'clean JSON/SSE 不得复制 item')
      assert.equal(structure.idFactoryCallCount, 0, 'clean 路径不得进入 repair ID factory')
    } else {
      assert.equal(
        structure.diagnosticCount + structure.omittedDiagnosticCount,
        result.dirtyItemCount,
        '1% dirty 必须完整计数且只保留有界诊断'
      )
      assert(structure.diagnosticCount <= codexResponsesGuardDiagnosticLimit)
    }

    if (result.protocol === 'json') {
      const shouldRepair = result.mode === 'safe_repair' && result.scenario === 'dirty_1pct'
      assert.equal(structure.repairedItemCount, shouldRepair ? result.dirtyItemCount : 0)
      assert.equal(structure.copiedRootCount, shouldRepair ? 1 : 0)
      assert.equal(structure.copiedArrayCount, shouldRepair ? 1 : 0)
      assert.equal(structure.copiedItemCount, shouldRepair ? result.dirtyItemCount : 0, 'R0 必须只 copy-on-write 命中 item')
      assert.equal(structure.idFactoryCallCount, shouldRepair ? result.dirtyItemCount : 0)
    } else {
      assert.equal(structure.repairedItemCount, 0, 'SSE 已暴露 identity 不允许在线改写')
      assert.equal(structure.identityCount, result.itemCount)
      assert.equal(structure.itemIdOwnerCount, result.itemCount)
    }
  }
}

function assertLinearCpuScaling(allResults: readonly BenchmarkResult[]): Array<{
  protocol: Protocol
  mode: CodexProtocolGuardGlobalMode
  scenario: Scenario
  mediumToLargeExponent: number
  normalizedCostRatio: number
}> {
  const slopes: Array<{
    protocol: Protocol
    mode: CodexProtocolGuardGlobalMode
    scenario: Scenario
    mediumToLargeExponent: number
    normalizedCostRatio: number
  }> = []
  for (const protocol of ['json', 'sse'] as const) {
    for (const mode of modes) {
      for (const scenario of scenarios) {
        const group = allResults.filter((result) => result.protocol === protocol && result.mode === mode && result.scenario === scenario)
        const medium = group.find((result) => result.itemCount === 1_000)
        const large = group.find((result) => result.itemCount === 10_000)
        assert(medium && large)
        if (mode === 'off') {
          assert.equal(medium.structure.guardInputItemCount + large.structure.guardInputItemCount, 0)
          slopes.push({ protocol, mode, scenario, mediumToLargeExponent: 0, normalizedCostRatio: 0 })
          continue
        }
        const timeRatio = Math.max(large.p50Ms, Number.EPSILON) / Math.max(medium.p50Ms, Number.EPSILON)
        const exponent = Math.log(timeRatio) / Math.log(10)
        const normalizedCostRatio = (large.p50Ms / large.itemCount) / Math.max(medium.p50Ms / medium.itemCount, Number.EPSILON)
        assert(
          exponent <= 1.5,
          `${protocol}/${mode}/${scenario} 出现超线性 CPU 增长：exponent=${exponent}`
        )
        assert(
          normalizedCostRatio <= Math.sqrt(10),
          `${protocol}/${mode}/${scenario} 单 item 成本增长过大：ratio=${normalizedCostRatio}`
        )
        slopes.push({
          protocol,
          mode,
          scenario,
          mediumToLargeExponent: rounded(exponent, 4),
          normalizedCostRatio: rounded(normalizedCostRatio, 4)
        })
      }
    }
  }
  return slopes
}

function assertSseMemoryBounds(): SseMemoryGate {
  const sameIdentityDeltaCount = 100_000
  const sameIdentityRetainedHeapBytes = measureRetainedHeap(() => {
    const guard = createGuard('shadow')
    guard.inspectParsedSse({ responseResourceId: 'resp_delta', event: addedMessageEvent(0, false) })
    const delta = { type: 'response.output_text.delta', output_index: 0, delta: 'x' }
    for (let index = 0; index < sameIdentityDeltaCount; index += 1) {
      guard.inspectParsedSse({ responseResourceId: 'resp_delta', event: delta })
    }
    const snapshot = guard.snapshot()
    assert.equal(snapshot.stream.identityCount, 1, '重复 delta 不得累积 event identity')
    assert.equal(snapshot.stream.diagnostics.length, 0)
    return () => guard.dispose()
  })
  const sameIdentityHeapLimitBytes = 4 * 1024 * 1024
  assert(
    sameIdentityRetainedHeapBytes <= sameIdentityHeapLimitBytes,
    `单 identity 的 ${sameIdentityDeltaCount} 个 delta 不得被完整缓存：${sameIdentityRetainedHeapBytes}`
  )

  const identityCountSmall = 1_000
  const identityCountLarge = 10_000
  const identityRetainedHeapBytesSmall = measureSseIdentityHeap(identityCountSmall)
  const identityRetainedHeapBytesLarge = measureSseIdentityHeap(identityCountLarge)
  const identityHeapGrowthLimitBytes = identityRetainedHeapBytesSmall * 20 + 2 * 1024 * 1024
  assert(
    identityRetainedHeapBytesLarge <= identityHeapGrowthLimitBytes,
    `SSE identity 保留堆出现超线性增长：small=${identityRetainedHeapBytesSmall}, large=${identityRetainedHeapBytesLarge}`
  )
  return {
    sameIdentityDeltaCount,
    sameIdentityRetainedHeapBytes,
    identityCountSmall,
    identityCountLarge,
    identityRetainedHeapBytesSmall,
    identityRetainedHeapBytesLarge,
    identityHeapGrowthLimitBytes
  }
}

function measureSseIdentityHeap(itemCount: number): number {
  return measureRetainedHeap(() => {
    const guard = createGuard('shadow')
    for (let index = 0; index < itemCount; index += 1) {
      guard.inspectParsedSse({ responseResourceId: 'resp_heap', event: addedMessageEvent(index, false) })
    }
    const snapshot = guard.snapshot()
    assert.equal(snapshot.stream.identityCount, itemCount)
    assert.equal(snapshot.stream.itemIdOwnerCount, itemCount)
    return () => guard.dispose()
  })
}

function measureRetainedHeap(retain: () => () => void): number {
  forceGc()
  const before = process.memoryUsage().heapUsed
  const release = retain()
  forceGc()
  const retained = Math.max(0, process.memoryUsage().heapUsed - before)
  release()
  forceGc()
  return retained
}

function createJsonDocument(itemCount: number, dirtyItemCount: number): JsonRecord {
  return {
    id: 'resp_bench',
    object: 'response',
    output: Array.from({ length: itemCount }, (_, index) => messageItem(index, index < dirtyItemCount))
  }
}

function createSseEvents(itemCount: number, dirtyItemCount: number): JsonRecord[] {
  return Array.from({ length: itemCount }, (_, index) => addedMessageEvent(index, index < dirtyItemCount))
}

function addedMessageEvent(index: number, dirty: boolean): JsonRecord {
  return {
    type: 'response.output_item.added',
    output_index: index,
    item: messageItem(index, dirty)
  }
}

function messageItem(index: number, dirty: boolean): JsonRecord {
  return {
    id: dirty ? `fc_wrong_${index}` : `msg_bench_${index}`,
    type: 'message',
    role: 'assistant',
    content: []
  }
}

function emptyStructureCounts(): StructureCounts {
  return {
    guardConstructionCount: 0,
    guardInputItemCount: 0,
    diagnosticCount: 0,
    omittedDiagnosticCount: 0,
    repairedItemCount: 0,
    copiedRootCount: 0,
    copiedArrayCount: 0,
    copiedItemCount: 0,
    idFactoryCallCount: 0,
    identityCount: 0,
    itemIdOwnerCount: 0
  }
}

function assertRuntimeModeContract(): void {
  assert.equal(readRuntimeMode(undefined), 'shadow', '未配置时必须默认 shadow')
  for (const mode of modes) {
    assert.equal(readRuntimeMode(mode), mode, `${mode} 必须是合法全局模式`)
  }

  const invalid = runRuntimeProbe('repair_everything')
  assert.notEqual(invalid.status, 0, '非法 guard mode 必须令配置加载失败')
  assert.match(invalid.stderr, /JUHE_AI_CODEX_PROTOCOL_GUARD_MODE/)
  assert.match(invalid.stderr, /off.*shadow.*safe_repair/)
}

function readRuntimeMode(mode: CodexProtocolGuardGlobalMode | undefined): string {
  const result = runRuntimeProbe(mode)
  assert.equal(result.status, 0, `runtime mode probe 失败：${result.stderr}`)
  return result.stdout.trim()
}

function runRuntimeProbe(mode: string | undefined): SpawnSyncReturns<string> {
  const env: NodeJS.ProcessEnv = {
    ...process.env,
    NODE_ENV: 'test',
    JUHE_AI_DISABLE_BASE_ENV: 'true',
    JUHE_AI_RUNTIME_MODE: 'standalone',
    JUHE_AI_DATABASE_DRIVER: 'sqlite',
    JUHE_AI_CACHE_DRIVER: 'memory',
    JUHE_AI_RUNTIME_STATE_DRIVER: 'memory',
    JUHE_AI_QUEUE_DRIVER: 'memory'
  }
  delete env.JUHE_AI_POSTGRES_URL
  delete env.JUHE_AI_REDIS_CACHE_URL
  delete env.JUHE_AI_REDIS_STATE_URL
  delete env.JUHE_AI_REDIS_QUEUE_URL
  if (mode === undefined) delete env.JUHE_AI_CODEX_PROTOCOL_GUARD_MODE
  else env.JUHE_AI_CODEX_PROTOCOL_GUARD_MODE = mode

  return spawnSync(process.execPath, [
    '--import',
    'tsx',
    '--input-type=module',
    '--eval',
    "import { runtimeConfig } from './src/config/runtime.ts'; process.stdout.write(runtimeConfig.codexProtocolGuard.mode)"
  ], {
    cwd: process.cwd(),
    encoding: 'utf8',
    env
  })
}

function dirtyCount(itemCount: number, scenario: Scenario): number {
  return scenario === 'clean' ? 0 : Math.max(1, Math.floor(itemCount * 0.01))
}

function iterationCount(itemCount: number): number {
  if (itemCount === 100) return 100
  if (itemCount === 1_000) return 25
  return 9
}

function forceGc(): void {
  assert(global.gc)
  global.gc()
  global.gc()
}

function percentile(sorted: readonly number[], ratio: number): number {
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil(sorted.length * ratio) - 1))
  return rounded(sorted[index] ?? 0, 6)
}

function rounded(value: number, digits: number): number {
  return Number(value.toFixed(digits))
}

function required<T>(value: T | undefined): T {
  assert(value !== undefined)
  return value
}
