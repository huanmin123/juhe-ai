import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import type { RouteStrategySpeedFirstConfig } from '../../domain/types.js'

runtimeConfig.runtimeStateDriver = 'memory'

const {
  normalRouteLatencyDegradationScope,
  orderGatewayAccountsByNormalRouteLatencyDegradationAsync,
  clearNormalRouteLatencyDegradationForRouteStrategyAsync,
  listNormalRouteLatencyProbeCandidatesAsync,
  recordNormalRouteProbeFailureAsync,
  recordNormalRouteFirstByteSlowAsync,
  recordNormalRouteFirstByteSuccessAsync
} = await import('../../modules/gateway/runtime/normal-route-latency-degradation.service.js')

const config: RouteStrategySpeedFirstConfig = {
  firstByteThresholdMs: 30000,
  slowTriggerCount: 2,
  slowWindowSeconds: 120,
  recoverySuccessCount: 3,
  probeIntervalSeconds: 10,
  degradedTtlSeconds: 300,
  retryOnFirstByteTimeout: true,
  maxFirstByteRetriesPerRequest: 1
}

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

const secondSlow = await recordNormalRouteFirstByteSlowAsync(accounts[0]!, scope, config)
assert.equal(secondSlow?.slowCount, 2, '第二次慢速样本应达到触发次数')
assert.equal(secondSlow?.degraded, true, '达到触发次数后应进入速度降级')
const futureProbeAtMs = Date.now() + config.probeIntervalSeconds * 2000
const futureProbeCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10, futureProbeAtMs)
assert.equal(futureProbeCandidates.length, 1, '速度降级后应产生到期恢复探针候选')
assert.equal(futureProbeCandidates[0]?.accountId, accounts[0]!.id, '恢复探针候选应指向被降级账号')
const failedProbe = await recordNormalRouteProbeFailureAsync(futureProbeCandidates[0]!, '回归模拟探针仍然慢')
assert.equal(failedProbe?.degraded, true, '探针未达标后应继续保持速度降级')
const immediateProbeCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10)
assert.equal(immediateProbeCandidates.length, 0, '探针未达标后不应立即再次进入候选')
const dueProbeCandidates = await listNormalRouteLatencyProbeCandidatesAsync(10, Date.now() + config.probeIntervalSeconds * 2000)
assert.equal(dueProbeCandidates.length, 1, '探针未达标后应按探针间隔顺延下一次候选')
const degradedOrder = await orderGatewayAccountsByNormalRouteLatencyDegradationAsync(accounts, scope, config)
assert.equal(degradedOrder.applied, true, '存在未降级候选时应应用速度降级排序')
assert.deepEqual(degradedOrder.accounts.map((account) => account.id), [accounts[1]!.id, accounts[0]!.id], '速度降级账号应排到候选末尾')

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

console.log('普通路由速度优先运行态回归通过：慢速窗口、短 TTL 降级排序、后台探针候选和恢复成功次数均生效')
