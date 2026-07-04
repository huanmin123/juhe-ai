import { strict as assert } from 'node:assert'

import type { GatewaySettings } from '../../modules/gateway/policy/account-error-policy.service.js'
import { AnthropicStreamInspector } from '../../modules/gateway/protocols/anthropic-v1/stream-inspection.js'
import { GeminiStreamInspector } from '../../modules/gateway/protocols/gemini-v1beta/stream-inspection.js'
import { OpenAIStreamInspector } from '../../modules/gateway/protocols/openai-v1/stream-inspection.js'
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
const activeStreamStatus = {
  semanticResultReceived: true,
  pendingProtocolEvent: false,
  parserSkipped: false
}

const activeLifetimePlan = buildStreamReadPlan(settings, now - 61_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  ...activeStreamStatus
})
assert.equal(activeLifetimePlan.timeoutKind, 'stream_lifetime', '持续有心跳的活跃流超过最大存活时间时应按 lifetime 中断')
assert(activeLifetimePlan.timeoutMessage.includes('最大存活时间'), `最大存活时间文案不正确：${activeLifetimePlan.timeoutMessage}`)

const idlePlan = buildStreamReadPlan(settings, now - 10_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now - 31_000,
  upstreamChunkReceived: true,
  ...activeStreamStatus
})
assert.equal(idlePlan.timeoutKind, 'upstream_activity', '流式空闲比最大存活时间更早到达时仍应按 idle 中断')

const firstChunkLifetimePlan = buildStreamReadPlan(settings, now - 61_000, {
  waitingForFirstChunk: true,
  lastUpstreamActivityAt: now - 61_000,
  upstreamChunkReceived: false,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
})
assert.equal(firstChunkLifetimePlan.timeoutKind, 'stream_lifetime', '首段未返回但先达到最大存活时间时应按 lifetime 中断')

const disabledPlan = buildStreamReadPlan({ ...settings, streamCircuitBreakerEnabled: false }, now - 61_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  ...activeStreamStatus
})
assert.equal(disabledPlan.phase, 'no_circuit_breaker', '关闭流式熔断时最大存活时间也应随该开关关闭')

const semanticResultPlan = buildStreamReadPlan({ ...settings, streamMaxLifetimeSeconds: 300 }, now - 121_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
})
assert.equal(semanticResultPlan.timeoutKind, 'semantic_result', '只有心跳 raw chunk 但无语义结果时应按有效输出超时中断')
assert(semanticResultPlan.timeoutMessage.includes('有效输出'), `有效输出超时文案不正确：${semanticResultPlan.timeoutMessage}`)

const pendingEventPlan = buildStreamReadPlan({ ...settings, streamMaxLifetimeSeconds: 300 }, now - 121_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  semanticResultReceived: false,
  pendingProtocolEvent: true,
  parserSkipped: false
})
assert.equal(pendingEventPlan.timeoutKind, 'upstream_activity', '正在接收未闭合协议事件时应继续按 raw activity 保护，避免误杀大事件碎片')

assertProtocolPendingEvent(new OpenAIStreamInspector(), 'OpenAI')
assertProtocolPendingEvent(new AnthropicStreamInspector(), 'Anthropic')
assertProtocolPendingEvent(new GeminiStreamInspector(), 'Gemini')

console.log('stream max lifetime read plan regression passed')

function assertProtocolPendingEvent(
  inspector: {
    pushText(text: string): { pendingEvent: boolean }
  },
  label: string
): void {
  assert.equal(inspector.pushText(': keep-alive\n\n').pendingEvent, false, `${label} SSE comment 不应被视为未闭合协议事件`)
  assert.equal(inspector.pushText('event: message\n').pendingEvent, true, `${label} event 行应被视为未闭合协议事件`)
}
