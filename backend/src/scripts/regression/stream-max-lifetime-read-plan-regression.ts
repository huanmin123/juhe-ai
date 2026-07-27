import { strict as assert } from 'node:assert'

import type { GatewayTimeoutProfile } from '../../modules/gateway/policy/timeout-profile.js'
import { AnthropicStreamInspector } from '../../modules/gateway/protocols/anthropic-v1/stream-inspection.js'
import { GeminiStreamInspector } from '../../modules/gateway/protocols/gemini-v1beta/stream-inspection.js'
import { OpenAIStreamInspector } from '../../modules/gateway/protocols/openai-v1/stream-inspection.js'
import { buildGatewayStreamReadPlan, buildStreamReadPlan } from '../../modules/gateway/response/stream-read-plan.js'

const textProfile: GatewayTimeoutProfile = {
  firstResponseTimeoutMs: 120_000,
  firstByteTimeoutMs: 120_000,
  idleTimeoutMs: 30_000,
  uncommittedAttemptMaxLifetimeMs: 60_000,
  noAvailableAccountWaitMs: 270_000
}
const imageProfile: GatewayTimeoutProfile = {
  firstResponseTimeoutMs: 600_000,
  firstByteTimeoutMs: 600_000,
  idleTimeoutMs: 120_000,
  uncommittedAttemptMaxLifetimeMs: 3_600_000,
  noAvailableAccountWaitMs: 270_000
}
const compactProfile: GatewayTimeoutProfile = { ...textProfile, timeoutsDisabled: true }

const now = Date.now()
const activeStreamStatus = {
  semanticResultReceived: true,
  pendingProtocolEvent: false,
  parserSkipped: false
}

const activeLifetimePlan = buildStreamReadPlan(textProfile, now - 61_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  ...activeStreamStatus
}, now)
assert.equal(activeLifetimePlan.timeoutKind, 'upstream_activity', '已有语义输出的活跃流不应再受未提交尝试寿命限制')
assert.equal(activeLifetimePlan.streamLifetimeTimeoutMs, undefined, '已有语义输出后不得保留未提交尝试寿命')

const idlePlan = buildStreamReadPlan(textProfile, now - 10_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now - 31_000,
  upstreamChunkReceived: true,
  ...activeStreamStatus
}, now)
assert.equal(idlePlan.timeoutKind, 'upstream_activity', '流式空闲比最大存活时间更早到达时仍应按 idle 中断')

const firstChunkLifetimePlan = buildStreamReadPlan(textProfile, now - 61_000, {
  waitingForFirstChunk: true,
  lastUpstreamActivityAt: now - 61_000,
  upstreamChunkReceived: false,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
}, now)
assert.equal(firstChunkLifetimePlan.timeoutKind, 'stream_lifetime', '首段未返回但先达到最大存活时间时应按 lifetime 中断')

const semanticResultPlan = buildStreamReadPlan({ ...textProfile, uncommittedAttemptMaxLifetimeMs: 300_000 }, now - 121_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
}, now)
assert.equal(semanticResultPlan.timeoutKind, 'semantic_result', '只有心跳 raw chunk 但无语义结果时应按有效输出超时中断')
assert(semanticResultPlan.timeoutMessage.includes('有效输出'), `有效输出超时文案不正确：${semanticResultPlan.timeoutMessage}`)

const recentProtocolEventPlan = buildStreamReadPlan({ ...textProfile, uncommittedAttemptMaxLifetimeMs: 300_000 }, now - 121_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  lastSseEventActivityAt: now - 1_000,
  upstreamChunkReceived: true,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
}, now)
assert.equal(recentProtocolEventPlan.timeoutKind, 'upstream_activity', '最近完整协议事件应刷新有效输出等待窗口，避免碎片或非输出事件后误杀')

const pendingEventPlan = buildStreamReadPlan({ ...textProfile, uncommittedAttemptMaxLifetimeMs: 300_000 }, now - 121_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  semanticResultReceived: false,
  pendingProtocolEvent: true,
  parserSkipped: false
}, now)
assert.equal(pendingEventPlan.timeoutKind, 'upstream_activity', '正在接收未闭合协议事件时应继续按 raw activity 保护，避免误杀大事件碎片')

const imageFirstChunkPlan = buildStreamReadPlan(imageProfile, now, {
  waitingForFirstChunk: true,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: false,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
}, now)
assert.equal(imageFirstChunkPlan.timeoutMs, 600_000, '图像 lane 首段等待应使用 600 秒 profile')

const imageActivePlan = buildStreamReadPlan(imageProfile, now, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now,
  upstreamChunkReceived: true,
  semanticResultReceived: true,
  pendingProtocolEvent: false,
  parserSkipped: false
}, now)
assert.equal(imageActivePlan.timeoutMs, 120_000, '图像 lane 活动流应使用 120 秒 idle profile')

const compactFirstChunkPlan = buildGatewayStreamReadPlan(compactProfile, now - 600_000, {
  waitingForFirstChunk: true,
  lastUpstreamActivityAt: now - 600_000,
  upstreamChunkReceived: false,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
}, now)
assert.equal(compactFirstChunkPlan, undefined, '压缩请求首段和首个有效结果不得受文本首响应或 attempt 生命周期限制')

const compactIdlePlan = buildGatewayStreamReadPlan(compactProfile, now - 600_000, {
  waitingForFirstChunk: false,
  lastUpstreamActivityAt: now - 31_000,
  upstreamChunkReceived: true,
  semanticResultReceived: false,
  pendingProtocolEvent: false,
  parserSkipped: false
}, now)
assert.equal(compactIdlePlan?.timeoutKind, 'upstream_activity', '压缩流开始传输后仍应保留 raw idle 死连接保护')
assert.equal(compactIdlePlan?.deadlineExceeded, true, '压缩流无新数据超过 idle 时限后必须释放连接和并发槽')

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
