import { strict as assert } from 'node:assert'

import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { createGatewayHotQualityAttemptLifecycle } from '../../modules/gateway/runtime/hot-quality-attempt-lifecycle.js'
import { MemoryHotQualityStore } from '../../modules/gateway/runtime/hot-quality-memory-store.js'
import {
  gatewayHotQualityModelFamily,
  onceGatewayHotQualityExplorationSettlement,
  type GatewayHotQualityRuntime
} from '../../modules/gateway/runtime/hot-quality-runtime.service.js'
import { MemorySameTierExplorationStore } from '../../modules/gateway/runtime/same-tier-exploration-memory-store.js'

const nowMs = Date.UTC(2026, 6, 23, 0, 0, 0)
const hotQualityStore = new MemoryHotQualityStore({ now: () => nowMs })
const runtime: GatewayHotQualityRuntime = {
  hotQualityStore,
  explorationStore: new MemorySameTierExplorationStore({ now: () => nowMs })
}
const account = {
  id: 'account-a',
  providerProtocolProfileId: 'profile-openai-responses',
  protocolCode: 'openai_v1',
  protocolVersion: 'responses'
} as unknown as UpstreamAccount

const attempt = createGatewayHotQualityAttemptLifecycle({
  runtime,
  attemptId: 'hotq:trace-a:audit-a',
  account,
  requestLane: 'text',
  model: 'gpt-sensitive-model-name',
  nowMs
})
attempt.markFirstByte(123.4)
await Promise.all([
  attempt.recordTerminal({ outcomeClass: 'completed_response', source: 'gateway_transport' }),
  attempt.recordTerminal({ outcomeClass: 'transport_failure', source: 'gateway_transport' })
])

const snapshot = await hotQualityStore.get(attempt.scope, nowMs)
assert.ok(snapshot)
assert.equal(snapshot.window5m.attempts, 1, '真实 HTTP attempt 只能写一次')
assert.equal(snapshot.window5m.completedResponses, 1, '并发 finalizer 只能提交第一个终态')
assert.equal(snapshot.window5m.localTransportFailures, 0)
assert.equal(snapshot.window5m.firstByteSampleCount, 1)
assert.equal(snapshot.window5m.firstByteSumMs, 123)
assert.equal(snapshot.window5m.qualityAttempts, 1)
assert.match(attempt.scope.modelFamily, /^model-bucket-[0-9a-f]{2}$/)
assert.equal(attempt.scope.modelFamily.includes('sensitive'), false, '热状态不得存原始模型名')
assert.equal(
  gatewayHotQualityModelFamily('gpt-sensitive-model-name'),
  attempt.scope.modelFamily,
  '候选读取和 attempt 写入必须使用同一个确定性 family'
)

// A replay/finalizer in another request object must observe the same terminal
// idempotency key rather than creating a second quality sample.
const replay = createGatewayHotQualityAttemptLifecycle({
  runtime,
  attemptId: attempt.attemptId,
  account,
  requestLane: 'text',
  model: 'gpt-sensitive-model-name',
  nowMs
})
await replay.recordTerminal({ outcomeClass: 'completed_response', source: 'gateway_transport', firstByteMs: 123 })
const afterReplay = await hotQualityStore.get(attempt.scope, nowMs)
assert.ok(afterReplay)
assert.equal(afterReplay.window5m.attempts, 1, '重放同一 attemptId 不得重复增加 attempts')
assert.equal(afterReplay.window5m.qualityAttempts, 1, '重放同一终态不得重复增加质量样本')

const cancelled = createGatewayHotQualityAttemptLifecycle({
  runtime,
  attemptId: 'hotq:trace-b:audit-b',
  account,
  requestLane: 'text',
  model: 'gpt-sensitive-model-name',
  nowMs
})
await cancelled.recordTerminal({ outcomeClass: 'client_cancellation', source: 'request_lifecycle' })
const afterCancellation = await hotQualityStore.get(cancelled.scope, nowMs)
assert.ok(afterCancellation)
assert.equal(afterCancellation.window5m.attempts, 2)
assert.equal(afterCancellation.window5m.clientCancellations, 1)
assert.equal(afterCancellation.window5m.qualityAttempts, 1, '客户端取消不得进入质量可靠性分母')

const unknown = createGatewayHotQualityAttemptLifecycle({
  runtime,
  attemptId: 'hotq:trace-c:audit-c',
  account,
  requestLane: 'text',
  model: 'gpt-sensitive-model-name',
  nowMs
})
await unknown.recordTerminal({ outcomeClass: 'unknown', source: 'request_lifecycle' })
const afterUnknown = await hotQualityStore.get(unknown.scope, nowMs)
assert.ok(afterUnknown)
assert.equal(afterUnknown.window5m.unknownOutcomes, 1)
assert.equal(afterUnknown.window5m.qualityAttempts, 1, '未知终态不得进入质量可靠性分母')

const explicitPolicy = createGatewayHotQualityAttemptLifecycle({
  runtime,
  attemptId: 'hotq:trace-d:audit-d',
  account,
  requestLane: 'text',
  model: 'gpt-sensitive-model-name',
  nowMs
})
await explicitPolicy.recordTerminal({ outcomeClass: 'explicit_policy_failure', failureScope: 'account', source: 'explicit_policy' })
const afterPolicy = await hotQualityStore.get(explicitPolicy.scope, nowMs)
assert.ok(afterPolicy)
assert.equal(afterPolicy.window5m.explicitPolicyFailures, 1)
assert.equal(afterPolicy.window5m.completedResponses, 1, '显式策略失败不得同时计入完整响应')

const settlementOutcomes: string[] = []
const settleOnce = onceGatewayHotQualityExplorationSettlement(async (outcome) => {
  settlementOutcomes.push(outcome)
})
await Promise.all([settleOnce('dispatched'), settleOnce('not_dispatched'), settleOnce('not_dispatched')])
assert.deepEqual(settlementOutcomes, ['dispatched'], 'dispatch、fallback 和 finally 竞争时只能结算一次')

console.log('gateway hot quality attempt lifecycle regression passed')
