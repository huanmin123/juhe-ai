import { strict as assert } from 'node:assert'

import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import { buildStreamReadPlan } from '../../modules/gateway/response/stream-read-plan.js'

const settings: GatewaySettings = {
  gatewayTextRawBodyLimitMegabytes: 8,
  defaultTemporaryUnschedulableMinutes: 2,
  temporaryUnschedulableRetryIntervalSeconds: 3,
  temporaryUnschedulableRetryAttempts: 3,
  streamCircuitBreakerEnabled: true,
  streamRequestTimeoutSeconds: 120,
  streamIdleTimeoutSeconds: 30,
  streamClientTotalWaitTimeoutSeconds: 270,
  streamMaxLifetimeSeconds: 60,
  streamFailureThresholdCount: 3,
  streamFailureThresholdWindowMinutes: 5
}

const now = Date.now()

const activeLifetimePlan = buildStreamReadPlan(settings, now - 61_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true
})
assert.equal(activeLifetimePlan.timeoutKind, 'stream_lifetime', '持续有心跳的活跃流超过最大存活时间时应按 lifetime 中断')
assert(activeLifetimePlan.timeoutMessage.includes('最大存活时间'), `最大存活时间文案不正确：${activeLifetimePlan.timeoutMessage}`)

const idlePlan = buildStreamReadPlan(settings, now - 10_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now - 31_000,
  upstreamChunkReceived: true
})
assert.equal(idlePlan.timeoutKind, 'upstream_activity', '流式空闲比最大存活时间更早到达时仍应按 idle 中断')

const firstChunkLifetimePlan = buildStreamReadPlan(settings, now - 61_000, {
  waitingForFirstChunk: true,
  lastUpstreamActivityAt: now - 61_000,
  upstreamChunkReceived: false
})
assert.equal(firstChunkLifetimePlan.timeoutKind, 'stream_lifetime', '首段未返回但先达到最大存活时间时应按 lifetime 中断')

const disabledPlan = buildStreamReadPlan({ ...settings, streamCircuitBreakerEnabled: false }, now - 61_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true
})
assert.equal(disabledPlan.phase, 'no_circuit_breaker', '关闭流式熔断时最大存活时间也应随该开关关闭')

console.log('stream max lifetime read plan regression passed')
