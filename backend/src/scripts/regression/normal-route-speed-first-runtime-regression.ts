import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import type { NormalRouteSpeedFirstRuntimeConfig } from '../../modules/gateway/runtime/normal-route-latency-degradation.service.js'
import { createRuntimeStateStore } from '../../shared/runtime-state-store.js'

runtimeConfig.runtimeStateDriver = 'memory'

const {
  normalRouteLatencyDegradationScope,
  orderGatewayAccountsByNormalRouteLatencyDegradationAsync,
  clearNormalRouteLatencyDegradationForRouteStrategyAsync,
  clearAllNormalRouteLatencyDegradationAsync,
  clearNormalRouteLatencyDegradationForAccountBindingAsync,
  deferNormalRouteLatencyProbeCandidateAsync,
  discardNormalRouteLatencyProbeCandidateAsync,
  listNormalRouteLatencyProbeCandidatesAsync,
  recordNormalRouteProbeFailureAsync,
  recordNormalRouteFirstByteSlowAsync,
  recordNormalRouteFirstByteSuccessAsync
} = await import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js')
const config: NormalRouteSpeedFirstRuntimeConfig = {
  firstByteDeadlineMs: 30000,
  slowTriggerCount: 2,
  slowWindowSeconds: 120,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 300,
  maxFirstByteRetriesPerRequest: 2
}
const clearAllForRuntimeEvent = clearAllNormalRouteLatencyDegradationAsync as unknown as (
  event: { version: string; publishedAt: string }
) => Promise<void | false>

const scope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_runtime',
  routeStrategyId: `route_strategy_speed_first_runtime_${Date.now()}`,
  groupId: 'group_speed_first_runtime'
})
assert(scope, '速度优先运行态回归需要有效 scope')

const accounts = [
  { id: 'account_speed_first_a', name: '速度优先账号 A' },
  { id: 'account_speed_first_b', name: '速度优先账号 B' }
]

const initialOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.equal(initialOrder.applied, false, '初始状态不应应用速度降级排序')

const firstSlow = await recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config)
assert.equal(firstSlow?.slowCount, 1, '第一次慢速样本应只记录观察')
assert.equal(firstSlow?.degraded, false, '未达到触发次数前不应降级')
const observedOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.deepEqual(observedOrder.accounts.map((account) => account.id), accounts.map((account) => account.id), '未达到触发次数前应保持原排序')

const observationClearScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_runtime',
  routeStrategyId: `route_strategy_speed_first_observation_clear_${Date.now()}`,
  groupId: 'group_speed_first_runtime'
})
const observationRetainedScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_runtime',
  routeStrategyId: `route_strategy_speed_first_observation_retained_${Date.now()}`,
  groupId: 'group_speed_first_runtime'
})
assert(observationClearScope, '速度优先观察态清理回归需要有效 scope')
assert(observationRetainedScope, '速度优先精确清理保留回归需要有效 scope')
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, observationClearScope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[1]!, observationRetainedScope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[1]!, observationRetainedScope, config)
const observationClearCount = await clearNormalRouteLatencyDegradationForRouteStrategyAsync(observationClearScope.routeStrategyId)
assert.equal(observationClearCount >= 1, true, '按路由策略清理应删除未确认降级的慢样本观察态')
const observationAfterClear = await recordNormalRouteFirstByteSlowAsync(accounts[0]!, observationClearScope, config)
assert.equal(observationAfterClear?.slowCount, 1, '观察态清理后下一次慢样本应重新从 1 计数')
assert.equal(
  await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, observationRetainedScope, config).then((result) => result.applied),
  true,
  '按路由策略精确清理不能删除其他策略的速度降级状态'
)
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(observationClearScope.routeStrategyId)
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(observationRetainedScope.routeStrategyId)

const transientRecovery = await recordNormalRouteFirstByteSuccessAsync(accounts[0]!, scope, config, 100)
assert.equal(transientRecovery?.cleared, true, '未确认慢前的快首字应清理慢样本窗口')
const transientSlow = await recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config)
assert.equal(transientSlow?.slowCount, 1, '快首字清理后下一次慢样本应重新从 1 计数')
assert.equal(transientSlow?.degraded, false, '偶发慢后恢复快不应被累计成速度降级')

const secondSlow = await recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config)
assert.equal(secondSlow?.slowCount, 2, '第二次慢速样本应达到触发次数')
assert.equal(secondSlow?.degraded, true, '达到触发次数后应进入速度降级')
const futureProbeAtMs = Date.now() + config.probeIntervalSeconds * 2000
const futureProbeCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10, futureProbeAtMs)
  assert.equal(futureProbeCandidates.length, 1, '速度降级后应产生到期恢复探针候选')
  assert.equal(futureProbeCandidates[0]?.accountId, accounts[0]!.id, '恢复探针候选应指向被降级账号')
  assert.equal(await deferNormalRouteLatencyProbeCandidateAsync(futureProbeCandidates[0]!), true, '中性恢复探针应只顺延下一次检查')
  assert.equal((await listNormalRouteLatencyProbeCandidatesAsync(10)).length, 0, '中性恢复探针不得立即重复入队')
  const neutralDeferredCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10, Date.now() + config.probeIntervalSeconds * 2000)
  assert.equal(neutralDeferredCandidates.length, 1, '中性恢复探针顺延后应保持原降级候选')
  assert.equal(neutralDeferredCandidates[0]?.recoverySuccessCount, 0, '中性恢复探针不得增加或清空恢复成功证据')
  const failedProbe = await recordNormalRouteProbeFailureAsync(neutralDeferredCandidates[0]!, '回归模拟探针仍然慢')
assert.equal(failedProbe?.degraded, true, '探针未达标后应继续保持速度降级')
const immediateProbeCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10)
assert.equal(immediateProbeCandidates.length, 0, '探针未达标后不应立即再次进入候选')
const dueProbeCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10, Date.now() + config.probeIntervalSeconds * 2000)
assert.equal(dueProbeCandidates.length, 1, '探针未达标后应按探针间隔顺延下一次候选')
const degradedOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.equal(degradedOrder.applied, true, '存在未降级候选时应应用速度降级排序')
assert.deepEqual(degradedOrder.accounts.map((account) => account.id), [accounts[1]!.id, accounts[0]!.id], '速度降级账号应排到候选末尾')

const priorityScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_runtime',
  routeStrategyId: `route_strategy_speed_first_priority_override_${Date.now()}`,
  groupId: 'group_speed_first_runtime'
})
assert(priorityScope, '速度优先账户偏好覆盖回归需要有效 scope')
const priorityAccounts = [
  { id: 'account_speed_first_super', name: '速度优先超级优先账号', superPriorityEnabled: true, priority: 0 },
  { id: 'account_speed_first_normal', name: '速度优先普通优先级账号', priority: 10 }
]
await recordNormalRouteFirstByteSlowAsync(priorityAccounts[0]!, priorityScope, config)
await recordNormalRouteFirstByteSlowAsync(priorityAccounts[0]!, priorityScope, config)
const priorityOverrideOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(priorityAccounts, priorityScope, config)
assert.equal(priorityOverrideOrder.applied, true, '超级优先账号确认慢后应应用速度优先覆盖排序')
assert.deepEqual(
  priorityOverrideOrder.accounts.map((account) => account.id),
  [priorityAccounts[1]!.id, priorityAccounts[0]!.id],
  '速度优先应允许未降级普通优先级账号临时越过已降级超级优先账号'
)
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(priorityScope.routeStrategyId)

for (let index = 1; index <= 2; index += 1) {
  const recovery = await recordNormalRouteFirstByteSuccessAsync(accounts[0]!, scope, config, 100)
  assert.equal(recovery?.cleared, false, `第 ${index} 次恢复成功不应立即清理降级`)
  assert.equal(recovery?.recoverySuccessCount, index, '恢复成功次数应递增')
}
const finalRecovery = await recordNormalRouteFirstByteSuccessAsync(accounts[0]!, scope, config, 100)
assert.equal(finalRecovery?.cleared, true, '达到恢复成功次数后应清理速度降级')
const recoveredProbeCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10, Date.now() + config.probeIntervalSeconds * 2000)
assert.equal(recoveredProbeCandidates.length, 0, '恢复后应从后台探针候选索引中清理')
const recoveredOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.equal(recoveredOrder.applied, false, '恢复后不应继续应用速度降级排序')
assert.deepEqual(recoveredOrder.accounts.map((account) => account.id), accounts.map((account) => account.id), '恢复后应回到原候选顺序')

await recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[1]!, scope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[1]!, scope, config)
const allDegradedOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.equal(allDegradedOrder.applied, false, '所有候选都降级时不应重排')
assert.equal(allDegradedOrder.bypassedAllDegraded, true, '所有候选都降级时应标记 bypassedAllDegraded')
assert.deepEqual(allDegradedOrder.accounts.map((account) => account.id), accounts.map((account) => account.id), '所有候选都降级时应保留原顺序兜底')
const clearedCount = await clearNormalRouteLatencyDegradationForRouteStrategyAsync(scope.routeStrategyId)
assert.equal(clearedCount >= 2, true, '按路由策略清理应删除当前策略下的速度降级状态')
const clearedOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.equal(clearedOrder.applied, false, '清理速度优先运行态后不应继续应用降级排序')

const concurrentSlowResults = await Promise.all([
  recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config),
  recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config)
])
assert.equal(
  concurrentSlowResults.some((result) => result?.degraded === true),
  true,
  '并发慢样本达到触发次数时应稳定进入速度降级'
)
const concurrentOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.equal(concurrentOrder.applied, true, '并发慢样本触发后应应用速度降级排序')
assert.deepEqual(concurrentOrder.accounts.map((account) => account.id), [accounts[1]!.id, accounts[0]!.id], '并发慢样本后应把慢账号后置')
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(scope.routeStrategyId)

const latencyStateStore = createRuntimeStateStore('gateway-normal-route-latency-degradation')
const generationScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_generation',
  routeStrategyId: `route_strategy_speed_first_generation_${Date.now()}`,
  groupId: 'group_speed_first_generation'
})
const oldOnlyScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_generation',
  routeStrategyId: `route_strategy_speed_first_old_only_${Date.now()}`,
  groupId: 'group_speed_first_old_only'
})
assert(generationScope, 'generation 回归需要有效主 scope')
assert(oldOnlyScope, 'generation 回归需要有效旧代保留 scope')
for (const generationTestScope of [generationScope, oldOnlyScope]) {
  await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationTestScope, config)
  await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationTestScope, config)
}
const firstGenerationEvent = {
  version: 'runtime-generation-002',
  publishedAt: '2026-07-12T11:00:00.000+09:00'
}
const firstGenerationEventCanonical = {
  ...firstGenerationEvent,
  publishedAt: '2026-07-12T02:00:00.000Z'
}
const delayedOlderGenerationEvent = {
  version: 'runtime-generation-999',
  publishedAt: '2026-07-12T10:59:59.000+09:00'
}
const sameTimeOlderGenerationEvent = {
  version: 'runtime-generation-001',
  publishedAt: '2026-07-12T10:00:00.000+08:00'
}
const newerGenerationEvent = {
  version: 'runtime-generation-003',
  publishedAt: '2026-07-12T02:00:01.000Z'
}
const generationAllIndexBefore = (await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? []
const generationProbeIndexBefore = (await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? []
const generationStateKey = generationAllIndexBefore.find((key) => key.includes(generationScope.routeStrategyId))
const oldOnlyStateKey = generationAllIndexBefore.find((key) => key.includes(oldOnlyScope.routeStrategyId))
assert(generationStateKey, 'generation 回归应找到主 state key')
assert(oldOnlyStateKey, 'generation 回归应找到旧代 state key')
const oldGenerationState = await latencyStateStore.getJson<Record<string, unknown>>(generationStateKey)
const oldOnlyState = await latencyStateStore.getJson<Record<string, unknown>>(oldOnlyStateKey)
assert.equal(typeof oldGenerationState?.generation, 'string', 'latency state 必须持久化 generation marker token')
assert.equal(typeof oldOnlyState?.generation, 'string', '旧代保留 state 必须持久化 generation marker token')
for (const publishedAt of ['2026-07-12T02:00:00.000', '2026-07-12 02:00:00.000', 'bad']) {
  await assert.rejects(
    clearAllForRuntimeEvent({ version: `runtime-generation-invalid-${publishedAt}`, publishedAt }),
    /普通路由速度优先 generation event publishedAt必须是带 Z 或数值 offset 的 RFC3339 时间/,
    `提供的非法 generation publishedAt 必须失败：${publishedAt}`
  )
}
await latencyStateStore.setJson('v1:generation', {
  version: 'runtime-generation-invalid-runtime-state',
  publishedAt: '2026-07-12T02:00:00.000'
}, 48 * 60 * 60 * 1000)
await assert.rejects(
  clearAllForRuntimeEvent({ version: 'runtime-generation-valid-after-invalid-runtime-state', publishedAt: '2026-07-12T02:00:00.000Z' }),
  /普通路由速度优先 runtime-state generation event publishedAt 必须是带 Z 或数值 offset 的 RFC3339 时间/,
  'Redis/runtime-state 中提供的裸 generation publishedAt 必须显式失败，不能当作缺失 marker'
)
await latencyStateStore.delete('v1:generation')
const oldProbeCandidate = (await listNormalRouteLatencyProbeCandidatesAsync(
  100,
  Date.now() + config.probeIntervalSeconds * 2000
)).find((candidate) => candidate.stateKey === generationStateKey)
assert(oldProbeCandidate, 'generation event 前应生成旧代 probe candidate')
assert.equal(typeof oldProbeCandidate.generation, 'string', 'probe candidate 必须携带 generation marker token')

const originalIncr = latencyStateStore.incr.bind(latencyStateStore)
const originalDelete = latencyStateStore.delete.bind(latencyStateStore)
const originalGetJson = latencyStateStore.getJson.bind(latencyStateStore)
const originalSetJson = latencyStateStore.setJson.bind(latencyStateStore)
const originalCompareSetJson = latencyStateStore.compareSetJson.bind(latencyStateStore)
let fullClearIncrCount = 0
let fullClearDeleteCount = 0
let fullClearStateOrIndexReadCount = 0
let fullClearGenerationSetCount = 0
let fullClearGenerationCasCount = 0
latencyStateStore.incr = async (key, options) => {
  fullClearIncrCount += 1
  return originalIncr(key, options)
}
latencyStateStore.delete = async (key) => {
  fullClearDeleteCount += 1
  return originalDelete(key)
}
latencyStateStore.getJson = async <T>(key: string) => {
  if (key !== 'v1:generation') {
    fullClearStateOrIndexReadCount += 1
  }
  return originalGetJson<T>(key)
}
latencyStateStore.setJson = async (key, value, ttlMs) => {
  if (key === 'v1:generation') {
    fullClearGenerationSetCount += 1
  }
  return originalSetJson(key, value, ttlMs)
}
latencyStateStore.compareSetJson = async (key, expectedValue, nextValue, ttlMs) => {
  if (key === 'v1:generation') {
    fullClearGenerationCasCount += 1
  }
  return originalCompareSetJson(key, expectedValue, nextValue, ttlMs)
}
let generationClearResult: void | false
try {
  generationClearResult = await clearAllForRuntimeEvent(firstGenerationEvent)
} finally {
  latencyStateStore.incr = originalIncr
  latencyStateStore.delete = originalDelete
  latencyStateStore.getJson = originalGetJson
  latencyStateStore.setJson = originalSetJson
  latencyStateStore.compareSetJson = originalCompareSetJson
}
assert.notEqual(generationClearResult, false, '首次 generation event 应成功应用')
assert.equal(fullClearIncrCount, 0, 'runtime-state generation marker 不能按消费进程执行 incr')
assert.equal(fullClearDeleteCount, 0, 'O(1) full clear 不能删除任何 state/index key')
assert.equal(fullClearStateOrIndexReadCount, 0, 'O(1) full clear 不能读取 state/all-index/probe-index')
assert.equal(fullClearGenerationSetCount, 0, 'generation marker 不能通过无条件 setJson 更新')
assert.equal(fullClearGenerationCasCount, 1, '首次较新 runtime-state event 应通过一次 CAS 更新 generation marker')
assert.deepEqual(
  await latencyStateStore.getJson('v1:generation'),
  firstGenerationEventCanonical,
  'generation marker 应保存 canonical UTC runtime-state version 和 publishedAt'
)
assert.deepEqual(
  (await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [],
  generationAllIndexBefore,
  'generation marker 更新后应保留旧 all-index'
)
assert.deepEqual(
  (await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? [],
  generationProbeIndexBefore,
  'generation marker 更新后应保留旧 probe-index'
)
assert.equal(
  await isLatencyDegraded(accounts[0]!, generationScope),
  false,
  'generation marker 更新后旧降级 state 应立即不可见'
)
assert.equal(
  (await listNormalRouteLatencyProbeCandidatesAsync(100, Date.now() + config.probeIntervalSeconds * 2000))
    .some((candidate) => candidate.stateKey === generationStateKey),
  false,
  'generation marker 更新后旧 probe candidate 应立即不可见'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? [])
    .includes(generationStateKey),
  true,
  'generation mismatch 不能移除旧 probe-index key'
)

const exhaustedGenerationEvent = {
  version: 'runtime-generation-cas-exhausted',
  publishedAt: '2026-07-12T02:00:00.500Z'
}
let exhaustedGenerationCasCount = 0
let exhaustedGenerationResult: void | false
latencyStateStore.compareSetJson = async (key, expectedValue, nextValue, ttlMs) => {
  if (key === 'v1:generation' && exhaustedGenerationCasCount < 20) {
    exhaustedGenerationCasCount += 1
    return false
  }
  return originalCompareSetJson(key, expectedValue, nextValue, ttlMs)
}
try {
  exhaustedGenerationResult = await clearAllForRuntimeEvent(exhaustedGenerationEvent)
} finally {
  latencyStateStore.compareSetJson = originalCompareSetJson
  await latencyStateStore.setJson('v1:generation', firstGenerationEventCanonical, 48 * 60 * 60 * 1000)
}
assert.equal(exhaustedGenerationResult, false, 'generation CAS 重试耗尽应返回 deferred')
assert(
  exhaustedGenerationCasCount > 0 && exhaustedGenerationCasCount <= 10,
  'generation CAS 重试必须使用小的有界上限'
)

await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationScope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationScope, config)
const currentGenerationState = await latencyStateStore.getJson<Record<string, unknown>>(generationStateKey)
const currentGenerationCandidate = (await listNormalRouteLatencyProbeCandidatesAsync(
  100,
  Date.now() + config.probeIntervalSeconds * 2000
)).find((candidate) => candidate.stateKey === generationStateKey)
assert(currentGenerationState, '首次 generation event 后应可写入新代 state')
assert(currentGenerationCandidate, '首次 generation event 后应发现新代 probe candidate')

assert.notEqual(
  await clearAllForRuntimeEvent(firstGenerationEvent),
  false,
  '同一 runtime-state event 重复消费应成功 no-op'
)
assert.equal(
  await isLatencyDegraded(accounts[0]!, generationScope),
  true,
  '同一 event 被其他消费进程重复处理不能让首次事件后的新写 state 失效'
)
assert.notEqual(
  await clearAllForRuntimeEvent(delayedOlderGenerationEvent),
  false,
  '延迟旧 event 应成功 no-op'
)
assert.notEqual(
  await clearAllForRuntimeEvent(sameTimeOlderGenerationEvent),
  false,
  '同 publishedAt 的较旧 version 应成功 no-op'
)
assert.deepEqual(
  await latencyStateStore.getJson('v1:generation'),
  firstGenerationEventCanonical,
  '延迟旧 event 或同时间较旧 version 不能回滚 generation marker'
)
assert.equal(
  await isLatencyDegraded(accounts[0]!, generationScope),
  true,
  '旧 event no-op 后当前 generation state 应保持可见'
)

assert.notEqual(
  await clearAllForRuntimeEvent(newerGenerationEvent),
  false,
  '较新 runtime-state event 应成功更新 generation marker'
)
assert.deepEqual(
  await latencyStateStore.getJson('v1:generation'),
  newerGenerationEvent,
  '较新 event 应替换 generation marker'
)
assert.equal(
  await isLatencyDegraded(accounts[0]!, generationScope),
  false,
  '较新 event 应立即失效前一 generation state'
)
await latencyStateStore.setJson(generationStateKey, currentGenerationState, config.degradedTtlSeconds * 1000)
assert.equal(
  await isLatencyDegraded(accounts[0]!, generationScope),
  false,
  '较新 event 后完成的旧 generation 并发写必须保持不可见'
)

await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationScope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationScope, config)
const newestGenerationState = await latencyStateStore.getJson<Record<string, unknown>>(generationStateKey)
assert.notEqual(
  newestGenerationState?.generation,
  currentGenerationState.generation,
  '新写 state 应使用较新 event 对应的 generation marker token'
)
assert.equal(
  await isLatencyDegraded(accounts[0]!, generationScope),
  true,
  '较新 event 后新 generation write 应正常进入降级'
)
const newestGenerationCandidate = (await listNormalRouteLatencyProbeCandidatesAsync(
  100,
  Date.now() + config.probeIntervalSeconds * 2000
)).find((candidate) => candidate.stateKey === generationStateKey)
assert(newestGenerationCandidate, '新 generation state 应通过保留的 probe-index 被发现')

await recordNormalRouteProbeFailureAsync(currentGenerationCandidate, '旧代 probe failure 不得覆盖新代 state')
await discardNormalRouteLatencyProbeCandidateAsync(currentGenerationCandidate)
assert.equal(
  (await latencyStateStore.getJson<Record<string, unknown>>(generationStateKey))?.generation,
  newestGenerationState?.generation,
  '旧 generation probe candidate 不能删除或覆盖新 generation state'
)

const fencedOlderEvent = {
  version: 'runtime-generation-004',
  publishedAt: '2026-07-12T02:00:02.000Z'
}
const fencedNewerEvent = {
  version: 'runtime-generation-005',
  publishedAt: '2026-07-12T02:00:03.000Z'
}
let releaseFencedOlderCas: (() => void) | undefined
let resolveFencedOlderCasPaused: (() => void) | undefined
const fencedOlderCasPaused = new Promise<void>((resolve) => {
  resolveFencedOlderCasPaused = resolve
})
const fencedOlderCasGate = new Promise<void>((resolve) => {
  releaseFencedOlderCas = resolve
})
let pauseFencedOlderCas = true
latencyStateStore.compareSetJson = async (key, expectedValue, nextValue, ttlMs) => {
  const nextEvent = nextValue as { version?: unknown }
  if (
    key === 'v1:generation'
    && nextEvent?.version === fencedOlderEvent.version
    && pauseFencedOlderCas
  ) {
    pauseFencedOlderCas = false
    resolveFencedOlderCasPaused?.()
    await fencedOlderCasGate
  }
  return originalCompareSetJson(key, expectedValue, nextValue, ttlMs)
}
let fencedOlderApply: Promise<void | false> | undefined
let fencedOlderCasHookObserved = false
try {
  fencedOlderApply = clearAllForRuntimeEvent(fencedOlderEvent)
  fencedOlderCasHookObserved = await Promise.race([
    fencedOlderCasPaused.then(() => true),
    delay(100).then(() => false)
  ])
  if (fencedOlderCasHookObserved) {
    assert.notEqual(
      await clearAllForRuntimeEvent(fencedNewerEvent),
      false,
      '旧 owner CAS 暂停时，较新 event 应能先成功更新 marker'
    )
  }
} finally {
  releaseFencedOlderCas?.()
  await fencedOlderApply?.catch(() => undefined)
  latencyStateStore.compareSetJson = originalCompareSetJson
}
assert.equal(
  fencedOlderCasHookObserved,
  true,
  'generation 更新必须经过可暂停验证的 compareSetJson CAS'
)
assert.deepEqual(
  await latencyStateStore.getJson('v1:generation'),
  fencedNewerEvent,
  '旧 owner CAS 恢复后不能覆盖已提交的较新 generation marker'
)

const markerRefreshScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_marker_refresh',
  routeStrategyId: `route_strategy_speed_first_marker_refresh_${Date.now()}`,
  groupId: 'group_speed_first_marker_refresh'
})
assert(markerRefreshScope, 'generation marker 条件续期回归需要有效 scope')
await latencyStateStore.setJson('v1:generation', fencedNewerEvent, 100)
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, markerRefreshScope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, markerRefreshScope, config)
await delay(150)
assert.equal(
  await isLatencyDegraded(accounts[0]!, markerRefreshScope),
  true,
  'marker 临近过期后写 state/index 应通过同值 CAS 续期，不能早于新状态过期'
)
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(markerRefreshScope.routeStrategyId)

const oldOnlyIndexBeforeExact = (await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? []
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(oldOnlyScope.routeStrategyId)
await clearNormalRouteLatencyDegradationForAccountBindingAsync({
  systemAccountId: oldOnlyScope.systemAccountId,
  accountId: accounts[0]!.id,
  groupIds: [oldOnlyScope.groupId]
})
assert.deepEqual(
  await latencyStateStore.getJson(oldOnlyStateKey),
  oldOnlyState,
  'route/account exact clear 不能删除 generation mismatch 的旧 state'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
    .includes(oldOnlyStateKey),
  oldOnlyIndexBeforeExact.includes(oldOnlyStateKey),
  'route/account exact clear 不能移除 generation mismatch 的旧 index key'
)

const exactGenerationScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_generation',
  routeStrategyId: `route_strategy_speed_first_generation_exact_${Date.now()}`,
  groupId: 'group_speed_first_generation_exact'
})
assert(exactGenerationScope, '当前 generation exact clear 回归需要有效 scope')
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, exactGenerationScope, config)
const exactGenerationKey = ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
  .find((key) => key.includes(exactGenerationScope.routeStrategyId))
assert(exactGenerationKey, '当前 generation exact clear 回归应找到 state key')
const exactGenerationLockKey = `v1:mutation-lock:${exactGenerationKey}`
const exactGenerationLockToken = `generation-exact-lock-${Date.now()}`
assert.equal(
  await latencyStateStore.acquireLock(exactGenerationLockKey, {
    ttlMs: 5000,
    token: exactGenerationLockToken
  }),
  true,
  '当前 generation exact clear 回归应先持有 mutation lock'
)
let exactGenerationClearSettled = false
const exactGenerationClear = clearNormalRouteLatencyDegradationForRouteStrategyAsync(
  exactGenerationScope.routeStrategyId
).finally(() => {
  exactGenerationClearSettled = true
})
await delay(30)
assert.equal(exactGenerationClearSettled, false, 'route exact clear 必须等待同 key mutation lock')
await latencyStateStore.releaseLock(exactGenerationLockKey, exactGenerationLockToken)
await exactGenerationClear
assert.equal(
  await latencyStateStore.getJson(exactGenerationKey),
  undefined,
  'route exact clear 应删除当前 generation state'
)

const accountExactScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_generation_account_exact',
  routeStrategyId: `route_strategy_speed_first_generation_account_exact_${Date.now()}`,
  groupId: 'group_speed_first_generation_account_exact'
})
assert(accountExactScope, '当前 generation account exact clear 回归需要有效 scope')
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, accountExactScope, config)
const accountExactKey = ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
  .find((key) => key.includes(accountExactScope.routeStrategyId))
assert(accountExactKey, '当前 generation account exact clear 回归应找到 state key')
const accountExactLockKey = `v1:mutation-lock:${accountExactKey}`
const accountExactLockToken = `generation-account-exact-lock-${Date.now()}`
assert.equal(
  await latencyStateStore.acquireLock(accountExactLockKey, {
    ttlMs: 5000,
    token: accountExactLockToken
  }),
  true,
  '当前 generation account exact clear 回归应先持有 mutation lock'
)
let accountExactClearSettled = false
const accountExactClear = clearNormalRouteLatencyDegradationForAccountBindingAsync({
  systemAccountId: accountExactScope.systemAccountId,
  accountId: accounts[0]!.id,
  groupIds: [accountExactScope.groupId]
}).finally(() => {
  accountExactClearSettled = true
})
await delay(30)
assert.equal(accountExactClearSettled, false, 'account exact clear 必须等待同 key mutation lock')
await latencyStateStore.releaseLock(accountExactLockKey, accountExactLockToken)
await accountExactClear
assert.equal(
  await latencyStateStore.getJson(accountExactKey),
  undefined,
  'account exact clear 应删除当前 generation state'
)

await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationScope, config)
await recordNormalRouteFirstByteSlowAsync(accounts[0]!, generationScope, config)
const currentProbeDiscardCandidate = (await listNormalRouteLatencyProbeCandidatesAsync(
  100,
  Date.now() + config.probeIntervalSeconds * 2000
)).find((candidate) => candidate.stateKey === generationStateKey)
assert(currentProbeDiscardCandidate, '当前 generation probe discard 回归应生成候选')
const probeDiscardLockKey = `v1:mutation-lock:${generationStateKey}`
const probeDiscardLockToken = `generation-probe-discard-lock-${Date.now()}`
assert.equal(
  await latencyStateStore.acquireLock(probeDiscardLockKey, {
    ttlMs: 5000,
    token: probeDiscardLockToken
  }),
  true,
  '当前 generation probe discard 回归应先持有 mutation lock'
)
let probeDiscardSettled = false
const probeDiscard = discardNormalRouteLatencyProbeCandidateAsync(
  currentProbeDiscardCandidate
).finally(() => {
  probeDiscardSettled = true
})
await delay(30)
assert.equal(probeDiscardSettled, false, '当前 generation probe discard 必须等待同 key mutation lock')
await latencyStateStore.releaseLock(probeDiscardLockKey, probeDiscardLockToken)
await probeDiscard
assert.equal(
  await latencyStateStore.getJson(generationStateKey),
  undefined,
  '当前 generation probe discard 应安全删除目标 state'
)

const exactContentionScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_exact_contention',
  routeStrategyId: `route_strategy_speed_first_exact_contention_${Date.now()}`,
  groupId: 'group_speed_first_exact_contention'
})
assert(exactContentionScope, '逐 key exact clear 竞争回归需要有效 scope')
const exactContentionAccounts = [
  { id: 'account_exact_contention_a', name: '逐 key 清理账号 A' },
  { id: 'account_exact_contention_b', name: '逐 key 清理账号 B' },
  { id: 'account_exact_contention_z', name: '逐 key 清理账号 Z' }
]
for (const account of exactContentionAccounts) {
  await recordNormalRouteFirstByteSlowAsync(account, exactContentionScope, config)
  await recordNormalRouteFirstByteSlowAsync(account, exactContentionScope, config)
}
const exactContentionKeys = ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
  .filter((key) => key.includes(exactContentionScope.routeStrategyId))
  .sort()
assert.equal(exactContentionKeys.length, exactContentionAccounts.length, '逐 key exact clear 回归应找到三个 state key')
const rebuiltKey = exactContentionKeys[0]!
const blockedKey = exactContentionKeys.at(-1)!
const rebuiltAccount = exactContentionAccounts.find((account) => rebuiltKey.includes(account.id))
assert(rebuiltAccount, '逐 key exact clear 回归应找到待并发重建账号')
const blockedLockKey = `v1:mutation-lock:${blockedKey}`
const blockedLockToken = `generation-exact-contention-lock-${Date.now()}`
assert.equal(
  await latencyStateStore.acquireLock(blockedLockKey, {
    ttlMs: 5000,
    token: blockedLockToken
  }),
  true,
  '逐 key exact clear 回归应先长期占用最后一个目标 key'
)
const exactContentionClear = clearNormalRouteLatencyDegradationForRouteStrategyAsync(
  exactContentionScope.routeStrategyId
)
await delay(2200)
await recordNormalRouteFirstByteSlowAsync(rebuiltAccount, exactContentionScope, config)
await recordNormalRouteFirstByteSlowAsync(rebuiltAccount, exactContentionScope, config)
await latencyStateStore.releaseLock(blockedLockKey, blockedLockToken)
await exactContentionClear
const rebuiltState = await latencyStateStore.getJson<Record<string, unknown>>(rebuiltKey)
assert(rebuiltState, '等待其他 key 超过 mutation TTL 时，已清 key 的并发重建不能被 exact clear 误删')
assert.equal(typeof rebuiltState.generation, 'string', '并发重建 state 应保持当前 generation token')
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? []).includes(rebuiltKey),
  true,
  '并发重建后 all-index 必须保留目标 key'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? []).includes(rebuiltKey),
  true,
  '并发重建后 probe-index 必须保留目标 key'
)
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(exactContentionScope.routeStrategyId)

const exactIndexContentionScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_exact_index_contention',
  routeStrategyId: `route_strategy_speed_first_exact_index_contention_${Date.now()}`,
  groupId: 'group_speed_first_exact_index_contention'
})
assert(exactIndexContentionScope, 'exact clear index lock 竞争回归需要有效 scope')
const exactIndexContentionAccount = {
  id: 'account_exact_index_contention',
  name: '精确清理索引竞争账号'
}
await recordNormalRouteFirstByteSlowAsync(exactIndexContentionAccount, exactIndexContentionScope, config)
await recordNormalRouteFirstByteSlowAsync(exactIndexContentionAccount, exactIndexContentionScope, config)
const exactIndexContentionKey = ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
  .find((key) => key.includes(exactIndexContentionScope.routeStrategyId))
assert(exactIndexContentionKey, 'exact clear index lock 竞争回归应找到 state key')
const probeIndexLockToken = `generation-probe-index-contention-${Date.now()}`
assert.equal(
  await latencyStateStore.acquireLock('v1:probe-index-lock', {
    ttlMs: 5000,
    token: probeIndexLockToken
  }),
  true,
  'exact clear index lock 竞争回归应先占用 probe index lock'
)
const exactIndexContentionClear = clearNormalRouteLatencyDegradationForRouteStrategyAsync(
  exactIndexContentionScope.routeStrategyId
)
for (let attempt = 0; attempt < 100; attempt += 1) {
  if (await latencyStateStore.getJson(exactIndexContentionKey) === undefined) break
  await delay(10)
}
assert.equal(
  await latencyStateStore.getJson(exactIndexContentionKey),
  undefined,
  'exact clear 应先删除 state，再进入 probe index lock 等待'
)
await delay(2200)
let exactIndexRebuildSettled = false
const exactIndexRebuild = (async () => {
  await recordNormalRouteFirstByteSlowAsync(exactIndexContentionAccount, exactIndexContentionScope, config)
  await recordNormalRouteFirstByteSlowAsync(exactIndexContentionAccount, exactIndexContentionScope, config)
})().finally(() => {
  exactIndexRebuildSettled = true
})
await delay(50)
assert.equal(
  exactIndexRebuildSettled,
  false,
  'exact clear 等待 index lock 超过 2 秒时，同 key record 仍应被专用 mutation lock 阻塞'
)
await latencyStateStore.releaseLock('v1:probe-index-lock', probeIndexLockToken)
await Promise.all([exactIndexContentionClear, exactIndexRebuild])
assert(
  await latencyStateStore.getJson(exactIndexContentionKey),
  'index lock 竞争结束后并发 record state 应存在'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
    .includes(exactIndexContentionKey),
  true,
  'index lock 竞争结束后并发 record all-index 应保持一致'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? [])
    .includes(exactIndexContentionKey),
  true,
  'index lock 竞争结束后并发 record probe-index 应保持一致'
)
await clearNormalRouteLatencyDegradationForRouteStrategyAsync(exactIndexContentionScope.routeStrategyId)

const recordIndexContentionScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_record_index_contention',
  routeStrategyId: `route_strategy_speed_first_record_index_contention_${Date.now()}`,
  groupId: 'group_speed_first_record_index_contention'
})
assert(recordIndexContentionScope, 'record/exact clear index lock 竞争回归需要有效 scope')
const recordIndexContentionAccount = {
  id: 'account_record_index_contention',
  name: '记录索引竞争账号'
}
await recordNormalRouteFirstByteSlowAsync(recordIndexContentionAccount, recordIndexContentionScope, config)
const recordIndexContentionKey = ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
  .find((key) => key.includes(recordIndexContentionScope.routeStrategyId))
assert(recordIndexContentionKey, 'record/exact clear index lock 竞争回归应找到 state key')
const allIndexLockToken = `generation-all-index-contention-${Date.now()}`
assert.equal(
  await latencyStateStore.acquireLock('v1:all-index-lock', {
    ttlMs: 5000,
    token: allIndexLockToken
  }),
  true,
  'record/exact clear index lock 竞争回归应先占用 all-index lock'
)
const blockedRecord = recordNormalRouteFirstByteSlowAsync(
  recordIndexContentionAccount,
  recordIndexContentionScope,
  config
)
for (let attempt = 0; attempt < 100; attempt += 1) {
  const state = await latencyStateStore.getJson<{ slowCount?: number }>(recordIndexContentionKey)
  if ((state?.slowCount ?? 0) >= 2) break
  await delay(10)
}
assert.equal(
  (await latencyStateStore.getJson<{ slowCount?: number }>(recordIndexContentionKey))?.slowCount,
  2,
  'record 应已写 state 并在 all-index lock 上等待'
)
await delay(2200)
const concurrentExactClear = clearNormalRouteLatencyDegradationForRouteStrategyAsync(
  recordIndexContentionScope.routeStrategyId
)
await delay(50)
await latencyStateStore.releaseLock('v1:all-index-lock', allIndexLockToken)
await Promise.all([blockedRecord, concurrentExactClear])
const recordContentionStateExists = Boolean(
  await latencyStateStore.getJson(recordIndexContentionKey)
)
const recordContentionAllIndexExists =
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
    .includes(recordIndexContentionKey)
const recordContentionProbeIndexExists =
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? [])
    .includes(recordIndexContentionKey)
assert.equal(
  recordContentionAllIndexExists,
  recordContentionStateExists,
  'record 与 exact clear 都 fulfilled 后 all-index 必须与 state 一致'
)
assert.equal(
  recordContentionProbeIndexExists,
  recordContentionStateExists,
  'record 与 exact clear 都 fulfilled 后 probe-index 必须与 state 一致'
)

const indexCasExhaustionScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_index_cas_exhaustion',
  routeStrategyId: `route_strategy_speed_first_index_cas_exhaustion_${Date.now()}`,
  groupId: 'group_speed_first_index_cas_exhaustion'
})
assert(indexCasExhaustionScope, 'index CAS 耗尽回归需要有效 scope')
const indexCasExhaustionAccount = {
  id: 'account_index_cas_exhaustion',
  name: '索引 CAS 耗尽账号'
}
let indexCasExhaustionStateKey: string | undefined
let indexCasExhaustionAttempts = 0
latencyStateStore.setJson = async (key, value, ttlMs) => {
  if ((value as { accountId?: unknown })?.accountId === indexCasExhaustionAccount.id) {
    indexCasExhaustionStateKey = key
  }
  return originalSetJson(key, value, ttlMs)
}
latencyStateStore.compareSetJson = async (key, expectedValue, nextValue, ttlMs) => {
  if (key === 'v1:all-index') {
    indexCasExhaustionAttempts += 1
    return false
  }
  return originalCompareSetJson(key, expectedValue, nextValue, ttlMs)
}
let indexCasExhaustionResult: PromiseSettledResult<unknown>
try {
  ;[indexCasExhaustionResult] = await Promise.allSettled([
    recordNormalRouteFirstByteSlowAsync(
      indexCasExhaustionAccount,
      indexCasExhaustionScope,
      config
    )
  ])
} finally {
  latencyStateStore.setJson = originalSetJson
  latencyStateStore.compareSetJson = originalCompareSetJson
}
assert.equal(indexCasExhaustionResult.status, 'rejected', 'index CAS 重试耗尽时 record 不能报告成功')
assert(
  indexCasExhaustionAttempts > 0 && indexCasExhaustionAttempts <= 10,
  'index CAS 冲突重试必须使用小的有界上限'
)
assert(indexCasExhaustionStateKey, 'index CAS 耗尽回归应捕获本次 state key')
assert.equal(
  await latencyStateStore.getJson(indexCasExhaustionStateKey),
  undefined,
  'index add 失败后必须原子撤销本次新写 state'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
    .includes(indexCasExhaustionStateKey),
  false,
  'index add 失败后不能残留本次 state 的 all-index 成员'
)

const probeIndexCasExhaustionScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_probe_index_cas_exhaustion',
  routeStrategyId: `route_strategy_speed_first_probe_index_cas_exhaustion_${Date.now()}`,
  groupId: 'group_speed_first_probe_index_cas_exhaustion'
})
assert(probeIndexCasExhaustionScope, 'probe index CAS 耗尽回归需要有效 scope')
const probeIndexCasExhaustionAccount = {
  id: 'account_probe_index_cas_exhaustion',
  name: '探针索引 CAS 耗尽账号'
}
await recordNormalRouteFirstByteSlowAsync(
  probeIndexCasExhaustionAccount,
  probeIndexCasExhaustionScope,
  config
)
const probeIndexCasExhaustionStateKey = (
  (await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? []
).find((key) => key.includes(probeIndexCasExhaustionScope.routeStrategyId))
assert(probeIndexCasExhaustionStateKey, 'probe index CAS 耗尽回归应找到观察态 state key')
let probeIndexCasExhaustionAttempts = 0
latencyStateStore.compareSetJson = async (key, expectedValue, nextValue, ttlMs) => {
  if (key === 'v1:probe-index') {
    probeIndexCasExhaustionAttempts += 1
    return false
  }
  return originalCompareSetJson(key, expectedValue, nextValue, ttlMs)
}
let probeIndexCasExhaustionResult: PromiseSettledResult<unknown>
try {
  ;[probeIndexCasExhaustionResult] = await Promise.allSettled([
    recordNormalRouteFirstByteSlowAsync(
      probeIndexCasExhaustionAccount,
      probeIndexCasExhaustionScope,
      config
    )
  ])
} finally {
  latencyStateStore.compareSetJson = originalCompareSetJson
}
assert.equal(
  probeIndexCasExhaustionResult.status,
  'rejected',
  'probe-index CAS 重试耗尽时降级 record 不能报告成功'
)
assert(
  probeIndexCasExhaustionAttempts > 0 && probeIndexCasExhaustionAttempts <= 10,
  'probe-index CAS 冲突重试必须使用小的有界上限'
)
const probeIndexRollbackState = await latencyStateStore.getJson<{
  slowCount?: number
  degradedUntilMs?: number
}>(probeIndexCasExhaustionStateKey)
assert.equal(probeIndexRollbackState?.slowCount, 1, 'probe-index 失败后应恢复写入前观察态')
assert.equal(
  probeIndexRollbackState?.degradedUntilMs,
  undefined,
  'probe-index 失败后不能保留未索引的降级 state'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
    .includes(probeIndexCasExhaustionStateKey),
  true,
  'probe-index 失败回滚后既有 all-index 成员应保留'
)
assert.equal(
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:probe-index'))?.keys ?? [])
    .includes(probeIndexCasExhaustionStateKey),
  false,
  'probe-index 失败回滚后不能残留 probe-index 成员'
)

const staleRecordScopeA = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_stale_record_index',
  routeStrategyId: `route_strategy_speed_first_stale_record_a_${Date.now()}`,
  groupId: 'group_speed_first_stale_record_index'
})
const staleRecordScopeB = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_stale_record_index',
  routeStrategyId: `route_strategy_speed_first_stale_record_b_${Date.now()}`,
  groupId: 'group_speed_first_stale_record_index'
})
assert(staleRecordScopeA, 'stale record index fencing 回归需要有效 A scope')
assert(staleRecordScopeB, 'stale record index fencing 回归需要有效 B scope')
const staleRecordAccountA = { id: 'account_stale_record_a', name: '陈旧 record A' }
const staleRecordAccountB = { id: 'account_stale_record_b', name: '陈旧 record B' }
const originalAcquireLock = latencyStateStore.acquireLock.bind(latencyStateStore)
let staleRecordStateKeyA: string | undefined
let staleRecordStateKeyB: string | undefined
let releaseStaleRecordIndexRead: (() => void) | undefined
let resolveStaleRecordIndexReadPaused: (() => void) | undefined
const staleRecordIndexReadPaused = new Promise<void>((resolve) => {
  resolveStaleRecordIndexReadPaused = resolve
})
const staleRecordIndexReadGate = new Promise<void>((resolve) => {
  releaseStaleRecordIndexRead = resolve
})
let pauseStaleRecordIndexRead = true
latencyStateStore.acquireLock = async (key, options) => originalAcquireLock(
  key,
  key === 'v1:all-index-lock' ? { ...options, ttlMs: 40 } : options
)
latencyStateStore.setJson = async (key, value, ttlMs) => {
  const accountId = (value as { accountId?: unknown })?.accountId
  if (accountId === staleRecordAccountA.id) staleRecordStateKeyA = key
  if (accountId === staleRecordAccountB.id) staleRecordStateKeyB = key
  return originalSetJson(key, value, ttlMs)
}
latencyStateStore.getJson = async <T>(key: string) => {
  const value = await originalGetJson<T>(key)
  if (key === 'v1:all-index' && pauseStaleRecordIndexRead) {
    pauseStaleRecordIndexRead = false
    resolveStaleRecordIndexReadPaused?.()
    await staleRecordIndexReadGate
  }
  return value
}
let staleRecordA: Promise<unknown> | undefined
let staleRecordB: Promise<unknown> | undefined
let staleRecordResults: PromiseSettledResult<unknown>[] = []
try {
  staleRecordA = recordNormalRouteFirstByteSlowAsync(
    staleRecordAccountA,
    staleRecordScopeA,
    config
  )
  assert.equal(
    await Promise.race([
      staleRecordIndexReadPaused.then(() => true),
      delay(1000).then(() => false)
    ]),
    true,
    'record A 应暂停在 all-index 旧快照读取后'
  )
  await delay(80)
  staleRecordB = recordNormalRouteFirstByteSlowAsync(
    staleRecordAccountB,
    staleRecordScopeB,
    config
  )
  await staleRecordB
  releaseStaleRecordIndexRead?.()
  staleRecordResults = await Promise.allSettled([staleRecordA, staleRecordB])
} finally {
  releaseStaleRecordIndexRead?.()
  await Promise.allSettled([staleRecordA, staleRecordB].filter(Boolean))
  latencyStateStore.acquireLock = originalAcquireLock
  latencyStateStore.setJson = originalSetJson
  latencyStateStore.getJson = originalGetJson
}
assert(
  staleRecordResults.every((result) => result.status === 'fulfilled'),
  '两个不同 key 的 record 均应成功完成'
)
assert(staleRecordStateKeyA, 'stale record index fencing 回归应捕获 A state key')
assert(staleRecordStateKeyB, 'stale record index fencing 回归应捕获 B state key')
assert(await latencyStateStore.getJson(staleRecordStateKeyA), 'record A state 应存在')
assert(await latencyStateStore.getJson(staleRecordStateKeyB), 'record B state 应存在')
const staleRecordAllIndex = (await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? []
assert(staleRecordAllIndex.includes(staleRecordStateKeyA), 'record A state 应保留在 all-index')
const staleRecordBIndexed = staleRecordAllIndex.includes(staleRecordStateKeyB)

const staleExactClearScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_stale_exact_clear',
  routeStrategyId: `route_strategy_speed_first_stale_exact_clear_${Date.now()}`,
  groupId: 'group_speed_first_stale_exact_clear'
})
const staleExactRecordScope = normalRouteLatencyDegradationScope({
  systemAccountId: 'sys_speed_first_stale_exact_clear',
  routeStrategyId: `route_strategy_speed_first_stale_exact_record_${Date.now()}`,
  groupId: 'group_speed_first_stale_exact_clear'
})
assert(staleExactClearScope, 'stale exact clear fencing 回归需要有效 A scope')
assert(staleExactRecordScope, 'stale exact clear fencing 回归需要有效 B scope')
const staleExactClearAccount = { id: 'account_stale_exact_clear_a', name: '陈旧 exact clear A' }
const staleExactRecordAccount = { id: 'account_stale_exact_record_b', name: '并发 record B' }
await recordNormalRouteFirstByteSlowAsync(staleExactClearAccount, staleExactClearScope, config)
const staleExactClearStateKey = (
  (await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? []
).find((key) => key.includes(staleExactClearScope.routeStrategyId))
assert(staleExactClearStateKey, 'stale exact clear fencing 回归应找到 A state key')
await latencyStateStore.setJson('v1:all-index', { keys: [staleExactClearStateKey] }, 24 * 60 * 60 * 1000)
await latencyStateStore.setJson('v1:probe-index', { keys: [] }, 24 * 60 * 60 * 1000)
let staleExactRecordStateKey: string | undefined
let staleExactAllIndexReadCount = 0
let releaseStaleExactIndexRead: (() => void) | undefined
let resolveStaleExactIndexReadPaused: (() => void) | undefined
const staleExactIndexReadPaused = new Promise<void>((resolve) => {
  resolveStaleExactIndexReadPaused = resolve
})
const staleExactIndexReadGate = new Promise<void>((resolve) => {
  releaseStaleExactIndexRead = resolve
})
latencyStateStore.acquireLock = async (key, options) => originalAcquireLock(
  key,
  key === 'v1:all-index-lock' ? { ...options, ttlMs: 40 } : options
)
latencyStateStore.setJson = async (key, value, ttlMs) => {
  if ((value as { accountId?: unknown })?.accountId === staleExactRecordAccount.id) {
    staleExactRecordStateKey = key
  }
  return originalSetJson(key, value, ttlMs)
}
latencyStateStore.getJson = async <T>(key: string) => {
  const value = await originalGetJson<T>(key)
  if (key === 'v1:all-index') {
    staleExactAllIndexReadCount += 1
    if (staleExactAllIndexReadCount === 2) {
      resolveStaleExactIndexReadPaused?.()
      await staleExactIndexReadGate
    }
  }
  return value
}
let staleExactClear: Promise<number> | undefined
let staleExactRecord: Promise<unknown> | undefined
let staleExactResults: PromiseSettledResult<unknown>[] = []
try {
  staleExactClear = clearNormalRouteLatencyDegradationForRouteStrategyAsync(
    staleExactClearScope.routeStrategyId
  )
  assert.equal(
    await Promise.race([
      staleExactIndexReadPaused.then(() => true),
      delay(1000).then(() => false)
    ]),
    true,
    'exact clear A 应暂停在 all-index 旧快照读取后'
  )
  await delay(80)
  staleExactRecord = recordNormalRouteFirstByteSlowAsync(
    staleExactRecordAccount,
    staleExactRecordScope,
    config
  )
  await staleExactRecord
  releaseStaleExactIndexRead?.()
  staleExactResults = await Promise.allSettled([staleExactClear, staleExactRecord])
} finally {
  releaseStaleExactIndexRead?.()
  await Promise.allSettled([staleExactClear, staleExactRecord].filter(Boolean))
  latencyStateStore.acquireLock = originalAcquireLock
  latencyStateStore.setJson = originalSetJson
  latencyStateStore.getJson = originalGetJson
}
assert(
  staleExactResults.every((result) => result.status === 'fulfilled'),
  'exact clear A 与 record B 均应成功完成'
)
assert(staleExactRecordStateKey, 'stale exact clear fencing 回归应捕获 B state key')
assert(await latencyStateStore.getJson(staleExactRecordStateKey), 'record B state 应存在')
const staleExactRecordBIndexed =
  ((await latencyStateStore.getJson<{ keys?: string[] }>('v1:all-index'))?.keys ?? [])
    .includes(staleExactRecordStateKey)
assert.deepEqual(
  {
    twoRecordsPreserveB: staleRecordBIndexed,
    exactClearPreservesRecordB: staleExactRecordBIndexed
  },
  {
    twoRecordsPreserveB: true,
    exactClearPreservesRecordB: true
  },
  '陈旧 all-index owner 不能覆盖其他 key 的 record/index 更新'
)

console.log('普通路由速度优先运行态回归通过：基础降级、事件 generation marker、旧事件幂等、probe 安全和逐 key 精确清理均生效')

async function isLatencyDegraded(
  account: { id: string; name: string },
  latencyScope: NonNullable<ReturnType<typeof normalRouteLatencyDegradationScope>>
): Promise<boolean> {
  const ordered = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(
    [account, { id: `${account.id}_fallback`, name: 'generation fallback' }],
    latencyScope,
    config
  )
  return ordered.degradedAccountIds.includes(account.id)
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}
