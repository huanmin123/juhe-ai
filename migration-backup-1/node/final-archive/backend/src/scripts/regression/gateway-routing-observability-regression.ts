import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import type { Logger } from 'pino'

import { runtimeConfig } from '../../config/runtime.js'
import { MemoryGatewayRoutingObservabilityStore } from '../../modules/gateway/observability/routing-observability-memory-store.js'
import { redisGatewayRoutingObservabilityRecordScript } from '../../modules/gateway/observability/routing-observability-redis-store.js'
import {
  getGatewayRoutingObservabilitySnapshot,
  getGatewayRoutingObservabilityStore,
  recordGatewayRoutingObservation,
  resetGatewayRoutingObservabilityForTest
} from '../../modules/gateway/observability/routing-observability.service.js'
import {
  gatewayRoutingObservationMetricKey,
  type GatewayRoutingObservation
} from '../../modules/gateway/observability/routing-observability-store.js'
import { withRequestContext, type RequestContext } from '../../shared/request-context.js'

const memory = new MemoryGatewayRoutingObservabilityStore()
await assert.rejects(
  memory.recordBatch([
    { observation: { kind: 'attempt', outcome: 'started' }, count: 1 },
    { observation: { kind: 'attempt', outcome: 'completed' }, count: 0 }
  ]),
  /正安全整数/u,
  '批量校验失败时不得留下前置计数'
)
assert.equal((await memory.snapshot()).recordedEvents, 0)
const fixedObservations: GatewayRoutingObservation[] = [
  { kind: 'circuit_transition', from: 'SUSPECT', to: 'OPEN', source: 'transport' },
  { kind: 'circuit_mutation', operation: 'acquire_confirmation', status: 'stale_generation', leaseKind: 'confirmation' },
  { kind: 'hot_quality_mutation', operation: 'terminal', status: 'idempotent' },
  { kind: 'exploration', outcome: 'reserved' },
  { kind: 'tier_escape', outcome: 'applied' },
  { kind: 'attempt', outcome: 'transport_failure' },
  { kind: 'budget', outcome: 'client_handoff' }
]
for (const [index, observation] of fixedObservations.entries()) await memory.record(observation, 1_000 + index)
await memory.recordBatch([
  { observation: { kind: 'attempt', outcome: 'started' }, count: 7 },
  { observation: { kind: 'attempt', outcome: 'started' }, count: 5 }
], 2_000)
await memory.record({ kind: 'attempt', outcome: 'completed' }, 1_500)
const memorySnapshot = await memory.snapshot()
assert.equal(memorySnapshot.recordedEvents, fixedObservations.length + 13)
assert.equal(memorySnapshot.updatedAtMs, 2_000, '乱序观测不得让 updatedAtMs 倒退')
assert.equal(Object.keys(memorySnapshot.counters).length, fixedObservations.length + 2)
for (const observation of fixedObservations) {
  assert.equal(memorySnapshot.counters[gatewayRoutingObservationMetricKey(observation)], 1)
}
assert.equal(memorySnapshot.counters['attempt.started'], 12, '批量写入必须按固定 key 合并计数')
assert.equal(memorySnapshot.counters['attempt.completed'], 1)

const logs: Array<{ level: string; fields: Record<string, unknown> }> = []
const requestLogger = {
  info(fields: Record<string, unknown>) { logs.push({ level: 'info', fields }) },
  warn(fields: Record<string, unknown>) { logs.push({ level: 'warn', fields }) },
  debug(fields: Record<string, unknown>) { logs.push({ level: 'debug', fields }) }
} as unknown as Logger
const context: RequestContext = {
  traceId: 'trace-routing-observability',
  requestId: 'request-routing-observability',
  startedAt: Date.now(),
  monotonicStartedAt: performance.now(),
  method: 'POST',
  path: '/v1/responses',
  originalUrl: '/v1/responses',
  logger: requestLogger
}

resetGatewayRoutingObservabilityForTest()
await withRequestContext(context, async () => {
  await recordGatewayRoutingObservation({ kind: 'circuit_transition', from: 'SUSPECT', to: 'OPEN', source: 'transport' })
  await recordGatewayRoutingObservation({ kind: 'circuit_transition', from: 'OPEN', to: 'OPEN', source: 'recovery' })
  await recordGatewayRoutingObservation({
    kind: 'circuit_mutation',
    operation: 'acquire_confirmation',
    status: 'stale_generation',
    leaseKind: 'confirmation'
  })
  await recordGatewayRoutingObservation({ kind: 'hot_quality_mutation', operation: 'terminal', status: 'idempotent' })
  await recordGatewayRoutingObservation({ kind: 'exploration', outcome: 'reserved' })
  await recordGatewayRoutingObservation({ kind: 'exploration', outcome: 'dispatched' })
  await recordGatewayRoutingObservation({ kind: 'tier_escape', outcome: 'applied' })
  await recordGatewayRoutingObservation({ kind: 'budget', outcome: 'wall_exhausted' })
  await recordGatewayRoutingObservation({ kind: 'budget', outcome: 'client_handoff' })
  for (let index = 0; index < 122; index += 1) {
    await recordGatewayRoutingObservation({ kind: 'attempt', outcome: 'started' })
  }
})

const summary = context.gatewayRoutingDispatchSummary
assert(summary)
assert.equal(summary.observedEvents, 128, '每请求 dispatch summary 必须有固定事件上限')
assert.equal(summary.droppedEvents, 3, '超过上限的观察只累计 dropped，不扩展摘要')
assert.equal(summary.circuitTransitions, 1, '同 phase 不得冒充状态转换')
assert.equal(summary.circuitCasConflicts, 1)
assert.equal(summary.circuitLeasesRejected, 1)
assert.equal(summary.hotQualityDeduplications, 1)
assert.equal(summary.explorationsReserved, 1)
assert.equal(summary.explorationsDispatched, 1)
assert.equal(summary.tierEscapes, 1)
assert.equal(summary.wallBudgetExhausted, 1)
assert.equal(summary.clientHandoffs, 1)
assert.equal(logs.filter((item) => item.fields.event === 'gateway_account_circuit_transition').length, 1)
assert.equal(logs.filter((item) => item.fields.event === 'gateway_account_circuit_dispatch_skipped').length, 1)
assert.doesNotMatch(JSON.stringify(summary), /authorization|api.?key|clientIp|response|body|credential/iu)

const sharedSnapshot = await getGatewayRoutingObservabilitySnapshot()
assert.equal(sharedSnapshot.recordedEvents, 131, '全局固定枚举计数不受单请求摘要截断影响')
assert.match(redisGatewayRoutingObservabilityRecordScript, /increment_saturated[\s\S]*HSET/u, 'Redis 事件计数与更新时间必须在同一 Lua 内提交')
assert.match(redisGatewayRoutingObservabilityRecordScript, /math\.max\(previous_updated_at_ms, tonumber\(now_ms\)\)/u, 'Redis 更新时间必须单调递增')

const requestContextSource = readFileSync(new URL('../../shared/request-context.ts', import.meta.url), 'utf8')
assert.match(requestContextSource, /dispatchSummary:\s*context\.gatewayRoutingDispatchSummary/u)
assert.match(requestContextSource, /!context\.stageSummaries\?\.length && !context\.gatewayRoutingDispatchSummary/u, '存在路由摘要时不得因没有阶段样本而跳过请求末尾摘要')
const upstreamDispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
assert.match(upstreamDispatchSource, /attemptTier[\s\S]*observeGatewayRouting\(\{ kind: 'tier_escape', outcome: 'applied' \}\)/u)
const statsRoutesSource = readFileSync(new URL('../../modules/stats/stats.routes.ts', import.meta.url), 'utf8')
assert.doesNotMatch(statsRoutesSource, /gatewayRoutingObservabilityAvailable|gatewayRoutingObservability:/u, '系统指标 runtime 不得附带页面未消费的路由观测大对象')

const originalRuntimeMode = runtimeConfig.runtimeMode
const originalStateDriver = runtimeConfig.runtimeStateDriver
try {
  runtimeConfig.runtimeMode = 'performance'
  runtimeConfig.runtimeStateDriver = 'memory'
  resetGatewayRoutingObservabilityForTest()
  assert.throws(
    () => getGatewayRoutingObservabilityStore(),
    /performance routing observability 要求 Redis runtime state/u,
    'performance 配置错误时不得静默退回本机 memory 指标'
  )
} finally {
  runtimeConfig.runtimeMode = originalRuntimeMode
  runtimeConfig.runtimeStateDriver = originalStateDriver
  resetGatewayRoutingObservabilityForTest()
}

console.log('gateway routing observability regression passed')
