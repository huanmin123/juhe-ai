import assert from 'node:assert/strict'

import { MemoryHotQualityStore } from '../../modules/gateway/runtime/hot-quality-memory-store.js'
import {
  HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS,
  HOT_QUALITY_KEY_TTL_MS,
  HOT_QUALITY_TERMINAL_TTL_MS,
  HOT_QUALITY_UNKNOWN_MODEL_FAMILY,
  createHotQualityModelFamilyCatalog,
  hotQualityScopeKey,
  type HotQualityScope,
  type HotQualityTerminalOutcomeClass
} from '../../modules/gateway/runtime/hot-quality-store.js'

let now = 10_000
const modelFamilies = createHotQualityModelFamilyCatalog(['gpt-5', 'gpt-4o'])
const gpt5 = modelFamilies.resolve('gpt-5')
const unknown = modelFamilies.resolve('unbounded-user-model-name')

assert.equal(unknown, HOT_QUALITY_UNKNOWN_MODEL_FAMILY, '目录外模型必须退化到协议级 unknown 桶')
assert.equal(modelFamilies.resolve(' GPT-5 '), gpt5, '模型目录 family 应稳定规范化')
assert.throws(
  () => createHotQualityModelFamilyCatalog(Array.from({ length: 257 }, (_, index) => `family-${index}`)),
  /最多允许 256 个模型 family/,
  '模型 family 目录自身也必须有界'
)

const scope: HotQualityScope = {
  accountRuntimeKey: 'acct:authorized:user:group:grant',
  protocolProfile: 'openai-responses',
  requestLane: 'text',
  modelFamily: gpt5
}
const imageScope: HotQualityScope = { ...scope, requestLane: 'image' }
const protocolScope: HotQualityScope = { ...scope, modelFamily: HOT_QUALITY_UNKNOWN_MODEL_FAMILY }

assert.notEqual(hotQualityScopeKey(scope), hotQualityScopeKey(imageScope), 'text 与 image lane 必须隔离')
assert.notEqual(hotQualityScopeKey(scope), hotQualityScopeKey(protocolScope), '已知 family 与协议 unknown 桶必须隔离')

const store = new MemoryHotQualityStore({
  keyCapacity: 8,
  attemptCapacity: 64,
  now: () => now
})

async function finish(
  target: MemoryHotQualityStore,
  input: {
    attemptId: string
    scope: HotQualityScope
    outcomeClass: HotQualityTerminalOutcomeClass
    terminalOutcomeId?: string
    firstByteMs?: number
    failureScope?: 'none' | 'key' | 'protocol_model' | 'account' | 'upstream_bucket'
    source?: 'gateway_transport' | 'explicit_policy' | 'request_lifecycle'
  }
): Promise<void> {
  const attempted = await target.recordAttempt({ attemptId: input.attemptId, scope: input.scope, nowMs: now })
  assert.ok(attempted.status === 'applied' || attempted.status === 'degraded_to_protocol')
  const completed = await target.recordTerminal({
    attemptId: input.attemptId,
    scope: input.scope,
    terminalOutcomeId: input.terminalOutcomeId ?? `terminal-${input.attemptId}`,
    outcomeClass: input.outcomeClass,
    failureScope: input.failureScope ?? (input.outcomeClass === 'completed_response' ? 'none' : 'protocol_model'),
    source: input.source ?? (input.outcomeClass === 'explicit_policy_failure' ? 'explicit_policy' : 'gateway_transport'),
    firstByteMs: input.firstByteMs,
    nowMs: now
  })
  assert.equal(completed.status, 'applied')
}

assert.equal((await store.recordAttempt({ attemptId: 'completed-1', scope, nowMs: now })).status, 'applied')
assert.equal(
  (await store.recordAttempt({ attemptId: 'completed-1', scope, nowMs: now })).status,
  'idempotent',
  '真实派发重放不得重复增加 attempts'
)
const terminalInput = {
  attemptId: 'completed-1',
  scope,
  terminalOutcomeId: 'terminal-completed-1',
  outcomeClass: 'completed_response' as const,
  failureScope: 'none' as const,
  source: 'gateway_transport' as const,
  firstByteMs: 800,
  nowMs: now
}
assert.equal((await store.recordTerminal(terminalInput)).status, 'applied')
assert.equal((await store.recordTerminal(terminalInput)).status, 'idempotent', 'finalizer 与队列重放必须终态幂等')
assert.equal((await store.recordTerminal({
  ...terminalInput,
  terminalOutcomeId: 'terminal-conflict'
})).status, 'terminal_conflict', '同一 attempt 不能提交第二个互斥终态')

await store.recordAttempt({ attemptId: 'outcome-id-conflict', scope: imageScope, nowMs: now })
assert.equal((await store.recordTerminal({
  ...terminalInput,
  attemptId: 'outcome-id-conflict',
  scope: imageScope
})).status, 'terminal_outcome_conflict', 'terminalOutcomeId 不能投影到另一个 attempt')
assert.equal((await store.recordTerminal({
  ...terminalInput,
  attemptId: 'missing-attempt',
  scope,
  terminalOutcomeId: 'missing-terminal'
})).status, 'attempt_not_found', '未真实派发的 attempt 不得写质量终态')

await store.recordAttempt({ attemptId: 'scope-conflict', scope: imageScope, nowMs: now })
assert.equal((await store.recordTerminal({
  ...terminalInput,
  attemptId: 'scope-conflict',
  terminalOutcomeId: 'terminal-scope-conflict'
})).status, 'attempt_conflict', '终态 scope 不得串写到另一个派发作用域')
assert.equal((await store.recordTerminal({
  ...terminalInput,
  attemptId: 'scope-conflict',
  scope: imageScope,
  terminalOutcomeId: 'terminal-scope-conflict'
})).status, 'applied', 'scope 冲突不得提前占用 terminalOutcomeId')

await store.recordAttempt({ attemptId: 'invalid-first-byte', scope: imageScope, nowMs: now })
await assert.rejects(
  store.recordTerminal({
    attemptId: 'invalid-first-byte',
    scope: imageScope,
    terminalOutcomeId: 'terminal-invalid-first-byte',
    outcomeClass: 'completed_response',
    failureScope: 'none',
    source: 'gateway_transport',
    firstByteMs: Number.NaN,
    nowMs: now
  }),
  /首字耗时必须是非负有限数值/,
  '非法首字样本必须在占用互斥终态前失败'
)
assert.equal((await store.recordTerminal({
  attemptId: 'invalid-first-byte',
  scope: imageScope,
  terminalOutcomeId: 'terminal-invalid-first-byte',
  outcomeClass: 'completed_response',
  failureScope: 'none',
  source: 'gateway_transport',
  firstByteMs: 900,
  nowMs: now
})).status, 'applied', '输入修正后必须仍能提交唯一终态')

await finish(store, { attemptId: 'explicit', scope, outcomeClass: 'explicit_policy_failure', firstByteMs: 1_500 })
await finish(store, { attemptId: 'timeout', scope, outcomeClass: 'timeout' })
await finish(store, { attemptId: 'read', scope, outcomeClass: 'read_interruption', firstByteMs: 5_000 })
await finish(store, { attemptId: 'incomplete', scope, outcomeClass: 'incomplete_response' })
await finish(store, { attemptId: 'transport', scope, outcomeClass: 'transport_failure' })
await finish(store, { attemptId: 'unknown', scope, outcomeClass: 'unknown', firstByteMs: 1 })
await finish(store, { attemptId: 'cancel', scope, outcomeClass: 'client_cancellation', firstByteMs: 1 })

const mutualSnapshot = await store.get(scope, now)
assert.ok(mutualSnapshot)
assert.equal(mutualSnapshot.window5m.attempts, 8)
assert.equal(mutualSnapshot.window5m.completedResponses, 1)
assert.equal(mutualSnapshot.window5m.explicitPolicyFailures, 1)
assert.equal(mutualSnapshot.window5m.localTransportFailures, 4, '本地失败总数不得与诊断子类重复累计')
assert.equal(mutualSnapshot.window5m.timeouts, 1)
assert.equal(mutualSnapshot.window5m.readInterruptions, 1)
assert.equal(mutualSnapshot.window5m.incompleteResponses, 1)
assert.equal(mutualSnapshot.window5m.unknownOutcomes, 1)
assert.equal(mutualSnapshot.window5m.clientCancellations, 1)
assert.equal(mutualSnapshot.window5m.qualityAttempts, 6, 'unknown 与 cancel 必须排除在完成率分母外')
assert.equal(mutualSnapshot.window5m.adjustedCompletionRate, 3 / 10)
assert.equal(mutualSnapshot.window5m.firstByteSampleCount, 3, 'unknown/cancel 的首字不得进入热速度样本')
assert.deepEqual(mutualSnapshot.window5m.firstByteHistogram, [1, 1, 1, 0, 0, 0, 0, 0])
assert.equal(mutualSnapshot.sampleState, 'known')
assert.equal(mutualSnapshot.reliabilityLevel, 'unhealthy')
assert.deepEqual(
  Object.keys((await store.getTerminal('completed-1', now)) ?? {}).sort(),
  ['createdAtMs', 'failureScope', 'outcomeClass', 'source', 'terminalOutcomeId'],
  '逻辑终态键只能保留设计允许的有界字段'
)

const concurrentStore = new MemoryHotQualityStore({ keyCapacity: 1, attemptCapacity: 128, now: () => now })
const concurrentAttempts = Array.from({ length: 100 }, (_, index) => `concurrent-${index}`)
const attemptResults = await Promise.all(concurrentAttempts.map((attemptId) => (
  concurrentStore.recordAttempt({ attemptId, scope, nowMs: now })
)))
assert.equal(attemptResults.filter((result) => result.status === 'applied').length, 100)
const terminalResults = await Promise.all(concurrentAttempts.map((attemptId) => (
  concurrentStore.recordTerminal({
    attemptId,
    scope,
    terminalOutcomeId: `terminal-${attemptId}`,
    outcomeClass: 'completed_response',
    failureScope: 'none',
    source: 'gateway_transport',
    nowMs: now
  })
)))
assert.equal(terminalResults.filter((result) => result.status === 'applied').length, 100)
const replayResults = await Promise.all(Array.from({ length: 32 }, () => concurrentStore.recordTerminal({
  attemptId: 'concurrent-0',
  scope,
  terminalOutcomeId: 'terminal-concurrent-0',
  outcomeClass: 'completed_response',
  failureScope: 'none',
  source: 'gateway_transport',
  nowMs: now
})))
assert.equal(replayResults.filter((result) => result.status === 'idempotent').length, 32)
const concurrentSnapshot = await concurrentStore.get(scope, now)
assert.ok(concurrentSnapshot)
assert.equal(concurrentSnapshot.window5m.attempts, 100, '同一分钟并发派发不得丢失 attempts 增量')
assert.equal(concurrentSnapshot.window5m.completedResponses, 100, '同一分钟并发终态不得丢失或重复完成增量')

const histogramStore = new MemoryHotQualityStore({ keyCapacity: 1, attemptCapacity: 16, now: () => now })
const histogramSamples = [1_000, 1_001, 5_000, 5_001, 20_000, 30_001, 60_000, 60_001]
for (const [index, firstByteMs] of histogramSamples.entries()) {
  await finish(histogramStore, {
    attemptId: `histogram-${index}`,
    scope,
    outcomeClass: 'completed_response',
    firstByteMs
  })
}
const histogramSnapshot = await histogramStore.get(scope, now)
assert.ok(histogramSnapshot)
assert.deepEqual(HOT_QUALITY_FIRST_BYTE_BUCKET_UPPER_BOUNDS_MS, [1_000, 2_000, 5_000, 10_000, 20_000, 30_000, 60_000, null])
assert.deepEqual(histogramSnapshot.window10m.firstByteHistogram, [1, 1, 1, 1, 1, 0, 2, 1])
assert.equal(histogramSnapshot.firstByteP95Bucket10m, 7, '10 分钟 P95 必须使用固定直方图近似')

const windowStore = new MemoryHotQualityStore({ keyCapacity: 1, attemptCapacity: 64, now: () => now })
for (let minute = 0; minute <= 30; minute += 1) {
  now = minute * 60_000 + 1
  await finish(windowStore, {
    attemptId: `minute-${minute}`,
    scope,
    outcomeClass: minute === 0 ? 'timeout' : 'completed_response'
  })
}
const ringSnapshot = await windowStore.get(scope, now)
assert.ok(ringSnapshot)
assert.equal(ringSnapshot.minuteBuckets.length, 30, '内存实现必须保持固定 30 个一分钟环形桶')
assert.equal(ringSnapshot.window5m.qualityAttempts, 5)
assert.equal(ringSnapshot.window10m.qualityAttempts, 10)
assert.equal(ringSnapshot.window30m.qualityAttempts, 30)
assert.equal(ringSnapshot.window30m.localTransportFailures, 0, '第 31 分钟必须淘汰最老桶')

now = 2_000_000
const capacityStore = new MemoryHotQualityStore({ keyCapacity: 2, attemptCapacity: 8, now: () => now })
await finish(capacityStore, { attemptId: 'protocol-seed', scope: protocolScope, outcomeClass: 'completed_response' })
await finish(capacityStore, { attemptId: 'known-seed', scope, outcomeClass: 'completed_response' })
const degraded = await capacityStore.recordAttempt({
  attemptId: 'known-overflow',
  scope: { ...scope, modelFamily: modelFamilies.resolve('gpt-4o') },
  nowMs: now
})
assert.equal(degraded.status, 'degraded_to_protocol', '容量满时新细分 family 应退化到已有协议 unknown 桶')
assert.equal(degraded.effectiveScope.modelFamily, HOT_QUALITY_UNKNOWN_MODEL_FAMILY)
assert.equal((await capacityStore.recordTerminal({
  attemptId: 'known-overflow',
  scope: { ...scope, modelFamily: modelFamilies.resolve('gpt-4o') },
  terminalOutcomeId: 'terminal-known-overflow',
  outcomeClass: 'completed_response',
  failureScope: 'none',
  source: 'gateway_transport',
  nowMs: now
})).status, 'applied')
const rejected = await capacityStore.recordAttempt({
  attemptId: 'other-account-overflow',
  scope: { ...scope, accountRuntimeKey: 'other-account' },
  nowMs: now
})
assert.equal(rejected.status, 'key_capacity_exhausted', '没有已有协议桶时不得无界创建或淘汰活跃 key')
const capacityStats = await capacityStore.stats(now)
assert.equal(capacityStats.keyCount, 2)
assert.equal(capacityStats.highCardinalityDegradations, 1)
assert.equal(capacityStats.keyCreationRefusals, 1)

const attemptCapacityStore = new MemoryHotQualityStore({ keyCapacity: 1, attemptCapacity: 1, now: () => now })
assert.equal((await attemptCapacityStore.recordAttempt({ attemptId: 'attempt-cap-1', scope, nowMs: now })).status, 'applied')
assert.equal(
  (await attemptCapacityStore.recordAttempt({ attemptId: 'attempt-cap-2', scope, nowMs: now })).status,
  'attempt_capacity_exhausted',
  'attempt 终态幂等索引必须有独立容量上限'
)

const atomicCapacityStore = new MemoryHotQualityStore({
  keyCapacity: 1,
  attemptCapacity: 4,
  keyTtlMs: 100,
  now: () => now
})
await atomicCapacityStore.recordAttempt({ attemptId: 'delayed-terminal', scope, nowMs: now })
now += 101
await atomicCapacityStore.recordAttempt({ attemptId: 'blocking-key', scope: imageScope, nowMs: now })
assert.equal((await atomicCapacityStore.recordTerminal({
  attemptId: 'delayed-terminal',
  scope,
  terminalOutcomeId: 'terminal-delayed',
  outcomeClass: 'completed_response',
  failureScope: 'none',
  source: 'gateway_transport',
  nowMs: now
})).status, 'quality_key_unavailable')
assert.equal(
  await atomicCapacityStore.getTerminal('delayed-terminal', now),
  undefined,
  '热 key 容量不足时不得只占终态却遗漏桶增量'
)
now += 101
assert.equal((await atomicCapacityStore.recordTerminal({
  attemptId: 'delayed-terminal',
  scope,
  terminalOutcomeId: 'terminal-delayed',
  outcomeClass: 'completed_response',
  failureScope: 'none',
  source: 'gateway_transport',
  nowMs: now
})).status, 'applied', '容量恢复后必须能原子补交尚未占用的终态')

const ttlStore = new MemoryHotQualityStore({ keyCapacity: 1, attemptCapacity: 2, now: () => now })
const ttlStartedAt = now
await finish(ttlStore, { attemptId: 'ttl-attempt', scope, outcomeClass: 'completed_response' })
now += HOT_QUALITY_KEY_TTL_MS + 1
assert.equal(await ttlStore.get(scope, now), undefined, '热质量 key 必须按 40 分钟 TTL 到期')
assert.equal((await ttlStore.stats(now)).keyCount, 0)
assert.equal((await ttlStore.recordTerminal({
  attemptId: 'ttl-attempt',
  scope,
  terminalOutcomeId: 'terminal-ttl-attempt',
  outcomeClass: 'completed_response',
  failureScope: 'none',
  source: 'gateway_transport',
  nowMs: now
})).status, 'idempotent', '热桶丢失后，一小时内终态幂等键仍必须生效')
now = ttlStartedAt + HOT_QUALITY_TERMINAL_TTL_MS + 1
assert.equal((await ttlStore.stats(now)).attemptIdentityCount, 0, '终态去重记录到期后必须清理')

console.log('hot-quality-memory-store-regression passed')
