import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import type { Request } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { gatewayForegroundAccountCircuitFailureEvidenceKey } from '../../modules/gateway/dispatch/upstream-dispatch.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import {
  accountCircuitScopeKey,
  closedAccountCircuitState,
  type AccountCircuitScope,
  type AccountCircuitState,
  type AccountCircuitStore
} from '../../modules/gateway/runtime/account-circuit-store.js'
import {
  GatewayAccountCircuitService,
  accountCircuitDispatchRevision,
  gatewayAccountProtocolModelScope,
  getGatewayAccountCircuitStore,
  resetGatewayAccountCircuitStoreForTest
} from '../../modules/gateway/runtime/account-circuit.service.js'

const account = accountFixture()
let now = 1_000
let id = 0
const store = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
const service = new GatewayAccountCircuitService(store, {
  now: () => now,
  createId: () => `id-${++id}`
})

const scope = gatewayAccountProtocolModelScope(account, 'text', 'gpt-5.4')
assert.equal(scope.accountRuntimeKey, account.id)
assert.equal(scope.protocolProfile, account.providerProtocolProfileId)
assert.equal(scope.requestLane, 'text')
assert.equal(scope.modelBucket, 'gpt-5.4')
assert.equal(gatewayAccountProtocolModelScope(account, 'text', 'arbitrary-user-model-a').modelBucket, 'unknown')
assert.equal(gatewayAccountProtocolModelScope(account, 'text', 'arbitrary-user-model-b').modelBucket, 'unknown')

const dispatchRevision = accountCircuitDispatchRevision(account)
assert.ok(dispatchRevision.startsWith('v1:'))
assert.doesNotMatch(dispatchRevision, /secret-api-key|refresh-secret/)
assert.notEqual(dispatchRevision, accountCircuitDispatchRevision({ ...account, apiKey: 'changed-secret' }))
assert.equal(dispatchRevision, accountCircuitDispatchRevision({ ...account, priority: 99, concurrencyLimit: 99 }), '优先级/并发变化不得创建新电路 owner revision')
assert.equal(dispatchRevision, accountCircuitDispatchRevision({ ...account, supportedModels: ['another-model'], modelMappings: [] }), '模型调度目录变化不得清空既有传输故障')
assert.equal(dispatchRevision, accountCircuitDispatchRevision({
  ...account,
  credentials: {
    ...account.credentials,
    error_handling_rules: [{ action: 'retry_next', status_codes: [429] }],
    service_tier_override: 'priority'
  }
}), '用户错误策略与请求偏好不得成为复活传输电路的旁路')
assert.notEqual(dispatchRevision, accountCircuitDispatchRevision({ ...account, proxyProfileId: 'proxy-b' }), '代理绑定变化必须创建新电路 owner revision')
assert.notEqual(dispatchRevision, accountCircuitDispatchRevision({ ...account, clientCompatibility: 'codex_responses' }), '协议兼容模式变化必须创建新电路 owner revision')
assert.equal(accountCircuitDispatchRevision({ ...account, dispatchRevision: 7 }), '7', 'DB dispatch revision 必须优先于兼容哈希')

const rebuiltAccount = { ...account, dispatchRevision: 7 }
const rebuiltScope = gatewayAccountProtocolModelScope(rebuiltAccount, 'text', 'gpt-5.4')
const rebuiltStore = new MemoryAccountCircuitStore({ capacity: 4, now: () => now })
await rebuiltStore.restore({
  scope: rebuiltScope,
  scopeKey: accountCircuitScopeKey(rebuiltScope),
  phase: 'OPEN',
  generation: 1,
  dispatchRevision: '7',
  transitionId: 'rebuild-open',
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  openedAtMs: now,
  retryAtMs: now + 30_000,
  updatedAtMs: now
})
const rebuiltDecision = await new GatewayAccountCircuitService(rebuiltStore, { now: () => now }).prepareAttempt({
  account: rebuiltAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 1
})
assert.equal(rebuiltDecision.outcome, 'blocked', '持久 OPEN 重建后的首次 prepare 不得因 revision 表示差异变成 CLOSED')

const initialAttempts = await Promise.all(Array.from({ length: 20 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 1
})))
assert.equal(initialAttempts.filter((result) => result.outcome === 'dispatchable').length, 20)
const firstInitialAttempt = initialAttempts[0]
assert.equal(firstInitialAttempt?.outcome, 'dispatchable')
if (firstInitialAttempt?.outcome !== 'dispatchable') throw new Error('缺少普通完整响应 attempt')
await firstInitialAttempt.attempt.reportFramingComplete()
assert.equal((await store.get(scope)).phase, 'CLOSED', '普通完整 HTTP framing 不得触发账户电路')

const transportDecisions = await Promise.all(initialAttempts.map(async (result, index) => {
  assert.equal(result.outcome, 'dispatchable')
  return result.attempt.reportTransportFailure({
    kind: 'transport',
    reason: `并发传输失败 ${index}`
  })
}))
assert.equal(transportDecisions.filter((decision) => decision.outcome === 'suspected').length, 1, '并发 CLOSED 失败只能有一个 SUSPECT 建立者')
assert.equal((await store.get(scope)).lease, undefined, '首个前台失败只建立 SUSPECT，不得顺带取得 confirmation 租约')

const blockedWhileSuspect = await Promise.all(Array.from({ length: 12 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})))
assert.equal(blockedWhileSuspect.filter((result) => result.outcome === 'dispatchable').length, 0)
assert.ok(blockedWhileSuspect.every((result) => result.outcome === 'blocked' && result.state.phase === 'SUSPECT'))

const notDueObservers = await Promise.all(Array.from({ length: 12 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: '1'.repeat(64)
})))
assert.equal(notDueObservers.filter((result) => result.outcome === 'dispatchable').length, 12, 'retryAt 前独立请求必须作为 observer 继续派发')
assert.ok(notDueObservers.every((result) => result.outcome === 'dispatchable' && result.attempt.isObserver))
const neutralObserver = notDueObservers[0]
if (!neutralObserver || neutralObserver.outcome !== 'dispatchable') throw new Error('缺少 observer attempt')
assert.equal((await neutralObserver.attempt.reportTransportFailure({ kind: 'timeout', reason: 'observer 超时' })).outcome, 'observer_neutral')
assert.equal((await store.get(scope)).confirmationFailureCount, 0, 'observer 失败不得累计 confirmation 失败')

now = (await store.get(scope)).retryAtMs ?? now
const dueAttempts = await Promise.all(Array.from({ length: 12 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 1,
  failureEvidenceKey: '1'.repeat(64)
})))
assert.equal(dueAttempts.filter((result) => result.outcome === 'dispatchable').length, 12, '租约被占用时独立请求仍必须作为 observer 派发')
const dueConfirmations = dueAttempts.filter((result) => result.outcome === 'dispatchable' && result.attempt.isConfirmation)
assert.equal(dueConfirmations.length, 1, '到期后同 generation 只能有一个 confirmation 租约赢家')
assert.equal(dueAttempts.filter((result) => result.outcome === 'dispatchable' && result.attempt.isObserver).length, 11)
const confirmationAttempt = dueConfirmations[0]
if (!confirmationAttempt || confirmationAttempt.outcome !== 'dispatchable') throw new Error('缺少 confirmation attempt')
await confirmationAttempt.attempt.reportTransportFailure({ kind: 'timeout', reason: 'confirmation 超时' })
assert.equal((await store.get(scope)).phase, 'OPEN')

const otherLane = await service.prepareAttempt({
  account,
  requestLane: 'image',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(otherLane.outcome, 'dispatchable', 'protocol-model 电路必须按 lane 隔离')

const recoveryAccount = accountFixture({ id: 'account-framing-complete' })
const recoveryAttempt = await service.prepareAttempt({
  account: recoveryAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(recoveryAttempt.outcome, 'dispatchable')
if (recoveryAttempt.outcome !== 'dispatchable') throw new Error('初始 attempt 不可派发')
const recoveryDecision = await recoveryAttempt.attempt.reportTransportFailure({ kind: 'read_incomplete', reason: '正文读取中断' })
assert.equal(recoveryDecision.outcome, 'suspected')
now = recoveryDecision.state.retryAtMs ?? now
const recoveryConfirmation = await service.prepareAttempt({
  account: recoveryAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: '2'.repeat(64)
})
assert.equal(recoveryConfirmation.outcome, 'dispatchable')
if (recoveryConfirmation.outcome !== 'dispatchable') throw new Error('recovery confirmation 不可派发')
await recoveryConfirmation.attempt.reportFramingComplete()
const recoveryState = await store.get(gatewayAccountProtocolModelScope(recoveryAccount, 'text', 'gpt-5.4'))
assert.equal(recoveryState.phase, 'RECOVERING', '首次 confirmation 完整成功必须进入 RECOVERING')
assert.equal(recoveryState.recoverySuccessCount, 0, 'confirmation 成功不得计入恢复 canary')

// One physical API key may fail at transport while another key on the same
// account completes framing. The latter must reclaim only the first request's
// child SUSPECT incident; it must not be a generic success-based reset.
const rotatedKeyAccount = accountFixture({
  id: 'account-rotated-key-framing',
  apiKeys: ['rotated-key-a', 'rotated-key-b'],
  apiKey: 'rotated-key-a'
})
const rotatedKeyScope = gatewayAccountProtocolModelScope(rotatedKeyAccount, 'text', 'gpt-5.4')
const rotatedKeyEvidence = 'd'.repeat(64)
const rotatedKeyInitial = await service.prepareAttempt({
  account: rotatedKeyAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: rotatedKeyEvidence
})
assert.equal(rotatedKeyInitial.outcome, 'dispatchable')
if (rotatedKeyInitial.outcome !== 'dispatchable') throw new Error('多 Key 首次 attempt 不可派发')
const rotatedKeyFailure = await rotatedKeyInitial.attempt.reportTransportFailure({
  kind: 'transport',
  reason: 'Key A 断流'
})
assert.equal(rotatedKeyFailure.outcome, 'suspected')
assert.equal((await store.get(rotatedKeyScope)).phase, 'SUSPECT')
const rotatedKeyRecovered = await rotatedKeyInitial.attempt.reportFramingComplete()
assert.equal(rotatedKeyRecovered?.status, 'applied', 'Key B 完整 framing 必须原子回收本次 SUSPECT')
assert.equal((await store.get(rotatedKeyScope)).phase, 'CLOSED')

const concurrentRotationAccount = accountFixture({ id: 'account-concurrent-key-rotation-success' })
const concurrentRotationScope = gatewayAccountProtocolModelScope(concurrentRotationAccount, 'text', 'gpt-5.4')
const concurrentRotationRevision = accountCircuitDispatchRevision(concurrentRotationAccount)
const concurrentRotationEvidence = '9'.repeat(64)
await store.suspect({
  scope: concurrentRotationScope,
  dispatchRevision: concurrentRotationRevision,
  transitionId: 'concurrent-key-rotation-suspect',
  reason: 'transport:first-key',
  confirmationFailuresRequired: 2,
  failureEvidenceKey: concurrentRotationEvidence,
  nowMs: now
})
const concurrentRotationResults = await Promise.all(Array.from({ length: 16 }, () => (
  service.completeRequestFramingAfterKeyRotation({
    scope: concurrentRotationScope,
    generation: 1,
    dispatchRevision: concurrentRotationRevision,
    failureEvidenceKey: concurrentRotationEvidence
  })
)))
assert.equal(concurrentRotationResults.filter((result) => result.status === 'applied').length, 1, '16 路并发健康 Key 只能有一个原子关闭赢家')
assert.equal((await store.get(concurrentRotationScope)).phase, 'CLOSED')

const observerRaceAccount = accountFixture({ id: 'account-observer-confirmation-race' })
const observerRaceScope = gatewayAccountProtocolModelScope(observerRaceAccount, 'text', 'gpt-5.4')
const observerRaceRevision = accountCircuitDispatchRevision(observerRaceAccount)
await store.suspect({
  scope: observerRaceScope,
  dispatchRevision: observerRaceRevision,
  transitionId: 'observer-race-suspect',
  reason: 'transport:initial',
  failureEvidenceKey: '3'.repeat(64),
  nowMs: now - 3_000
})
const observerRaceConfirmation = await service.prepareAttempt({
  account: observerRaceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 1,
  failureEvidenceKey: '4'.repeat(64)
})
assert.equal(observerRaceConfirmation.outcome, 'dispatchable')
if (observerRaceConfirmation.outcome !== 'dispatchable') throw new Error('observer 竞态缺少 confirmation')
assert.equal(observerRaceConfirmation.attempt.isConfirmation, true)
const observerRaceObserver = await service.prepareAttempt({
  account: observerRaceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: '5'.repeat(64)
})
assert.equal(observerRaceObserver.outcome, 'dispatchable')
if (observerRaceObserver.outcome !== 'dispatchable') throw new Error('租约占用时 observer 不可派发')
assert.equal(observerRaceObserver.attempt.isObserver, true)
const observerWon = await observerRaceObserver.attempt.reportFramingComplete()
assert.equal(observerWon?.status, 'applied', 'observer 完整 framing 必须能原子关闭带真实 confirmation 租约的 SUSPECT')
const lateConfirmation = await observerRaceConfirmation.attempt.reportTransportFailure({
  kind: 'timeout',
  reason: 'observer 成功后的迟到 confirmation 失败'
})
assert.equal(lateConfirmation.outcome, 'blocked')
assert.equal(lateConfirmation.state.phase, 'CLOSED', 'observer 关闭后迟到 confirmation 失败必须 state_mismatch，不能 OPEN')
assert.equal((await store.get(observerRaceScope)).lease, undefined)

const parentAcquireRaceAccount = accountFixture({ id: 'account-parent-opens-during-acquire' })
const parentAcquireRaceScope = gatewayAccountProtocolModelScope(parentAcquireRaceAccount, 'text', 'gpt-5.4')
const parentAcquireRaceRevision = accountCircuitDispatchRevision(parentAcquireRaceAccount)
const parentAcquireRaceStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
const parentAcquireRaceService = new GatewayAccountCircuitService(parentAcquireRaceStore, {
  now: () => now,
  createId: () => `parent-acquire-race-${++id}`
})
await parentAcquireRaceStore.suspect({
  scope: parentAcquireRaceScope,
  dispatchRevision: parentAcquireRaceRevision,
  transitionId: 'parent-acquire-race-suspect',
  reason: 'transport:initial',
  failureEvidenceKey: '6'.repeat(64),
  nowMs: now - 3_000
})
const acquireBeforeParentOpen = parentAcquireRaceStore.acquireConfirmationLease.bind(parentAcquireRaceStore)
parentAcquireRaceStore.acquireConfirmationLease = async (input) => {
  const acquired = await acquireBeforeParentOpen(input)
  if (acquired.status === 'applied') {
    await restoreOpenParentShadow(
      parentAcquireRaceStore,
      acquired.state,
      parentAcquireRaceRevision,
      'parent-acquire-race',
      now
    )
  }
  return acquired
}
const parentAcquireRaceResult = await parentAcquireRaceService.prepareAttempt({
  account: parentAcquireRaceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: '7'.repeat(64)
})
assert.equal(parentAcquireRaceResult.outcome, 'blocked', 'acquire await 期间父级 OPEN 后不得回退为 observer 派发')
if (parentAcquireRaceResult.outcome === 'blocked') assert.equal(parentAcquireRaceResult.state.scope.kind, 'account')
assert.ok((await parentAcquireRaceStore.get(parentAcquireRaceScope)).shadowedByIncidentId)

const parentObserverRaceAccount = accountFixture({ id: 'account-parent-opens-before-observer-close' })
const parentObserverRaceScope = gatewayAccountProtocolModelScope(parentObserverRaceAccount, 'text', 'gpt-5.4')
const parentObserverRaceRevision = accountCircuitDispatchRevision(parentObserverRaceAccount)
const parentObserverRaceStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
let parentObserverClearCount = 0
const clearParentObserverEvidence = parentObserverRaceStore.clearAccountEscalationEvidence.bind(parentObserverRaceStore)
parentObserverRaceStore.clearAccountEscalationEvidence = async (input) => {
  parentObserverClearCount += 1
  return clearParentObserverEvidence(input)
}
const parentObserverRaceService = new GatewayAccountCircuitService(parentObserverRaceStore, {
  now: () => now,
  createId: () => `parent-observer-race-${++id}`
})
const parentObserverSuspect = await parentObserverRaceStore.suspect({
  scope: parentObserverRaceScope,
  dispatchRevision: parentObserverRaceRevision,
  transitionId: 'parent-observer-race-suspect',
  reason: 'transport:initial',
  failureEvidenceKey: '8'.repeat(64),
  nowMs: now
})
const parentObserverAttempt = await parentObserverRaceService.prepareAttempt({
  account: parentObserverRaceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: '9'.repeat(64)
})
assert.equal(parentObserverAttempt.outcome, 'dispatchable')
if (parentObserverAttempt.outcome !== 'dispatchable') throw new Error('父级竞态 observer 不可派发')
assert.equal(parentObserverAttempt.attempt.isObserver, true)
await restoreOpenParentShadow(
  parentObserverRaceStore,
  parentObserverSuspect.state,
  parentObserverRaceRevision,
  'parent-observer-race',
  now
)
const shadowedObserverClose = await parentObserverAttempt.attempt.reportFramingComplete()
assert.equal(shadowedObserverClose?.status, 'state_mismatch', '父级 OPEN/shadow 后 observer 不得关闭 child')
assert.equal((await parentObserverRaceStore.get(parentObserverRaceScope)).phase, 'SUSPECT')
assert.deepEqual((await parentObserverRaceStore.get(parentObserverRaceScope)).failureEvidenceKeys, ['8'.repeat(64)])
assert.equal(parentObserverClearCount, 0, 'observer CAS 失败不得清除父级升级 evidence')

// A late success from the old request must not erase newer independent
// evidence in the same generation.
const rotatedKeyRaceAccount = accountFixture({ id: 'account-rotated-key-race' })
const rotatedKeyRaceScope = gatewayAccountProtocolModelScope(rotatedKeyRaceAccount, 'text', 'gpt-5.4')
const rotatedKeyRaceInitial = await service.prepareAttempt({
  account: rotatedKeyRaceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: 'e'.repeat(64)
})
assert.equal(rotatedKeyRaceInitial.outcome, 'dispatchable')
if (rotatedKeyRaceInitial.outcome !== 'dispatchable') throw new Error('多 Key 竞态首次 attempt 不可派发')
const rotatedKeyRaceFailure = await rotatedKeyRaceInitial.attempt.reportTransportFailure({ kind: 'timeout', reason: 'Key A timeout' })
assert.equal(rotatedKeyRaceFailure.outcome, 'suspected')
now = rotatedKeyRaceFailure.state.retryAtMs ?? now
const newerEvidenceAttempt = await service.prepareAttempt({
  account: rotatedKeyRaceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: 'f'.repeat(64)
})
assert.equal(newerEvidenceAttempt.outcome, 'dispatchable')
if (newerEvidenceAttempt.outcome !== 'dispatchable') throw new Error('新证据 confirmation 不可派发')
await newerEvidenceAttempt.attempt.reportTransportFailure({ kind: 'read_incomplete', reason: 'Key C 迟到断流' })
const staleRotationResult = await rotatedKeyRaceInitial.attempt.reportFramingComplete()
assert.equal(staleRotationResult?.status, 'state_mismatch', '新 evidence 出现后旧 Key 成功不得清除 SUSPECT')
assert.equal((await store.get(rotatedKeyRaceScope)).phase, 'SUSPECT')
const staleEvidenceLease = await store.acquireConfirmationLease({
  scope: rotatedKeyRaceScope,
  generation: (await store.get(rotatedKeyRaceScope)).generation,
  dispatchRevision: accountCircuitDispatchRevision(rotatedKeyRaceAccount),
  transitionId: 'stale-evidence-lease',
  leaseId: 'stale-evidence-lease',
  leaseUntilMs: now + 30_000,
  expectedFailureEvidenceKey: 'e'.repeat(64),
  nowMs: now
})
assert.equal(staleEvidenceLease.status, 'state_mismatch', 'Memory CAS 必须在租约获取时原子拒绝旧 evidence')

const badSessionEvidence = 'a'.repeat(64)
const healthySessionEvidence = 'b'.repeat(64)
const thirdSessionEvidence = 'c'.repeat(64)
const evidenceStore = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
const evidenceService = new GatewayAccountCircuitService(evidenceStore, {
  now: () => now,
  createId: () => `evidence-${++id}`
})
const evidenceAccount = accountFixture({ id: 'account-request-evidence' })

const sameCallerEvidenceKeys = Array.from({ length: 50 }, (_, index) => (
  gatewayForegroundAccountCircuitFailureEvidenceKey(
    accountCircuitRequestFixture({
      body: {
        model: 'gpt-5.4',
        input: `坏会话变化正文 ${index}`,
        previous_response_id: `逐请求变化但不是稳定会话 ${index}`
      },
      rawBody: Buffer.from(`raw-varying-body-${index}`),
      headers: { 'x-client-request-id': `request-${index}` }
    }),
    {
      systemAccountId: 'system-a',
      apiKeyId: `rotated-gateway-key-${index}`,
      clientIp: '203.0.113.10'
    }
  )
))
assert.equal(new Set(sameCallerEvidenceKeys).size, 1, '同一调用源轮换网关 API Key、正文、request id 或 previous response 不得制造独立熔断证据')

const sameExplicitSessionKeys = Array.from({ length: 50 }, (_, index) => (
  gatewayForegroundAccountCircuitFailureEvidenceKey(
    accountCircuitRequestFixture({
      body: { input: `变化正文 ${index}` },
      headers: { 'session-id': 'stable-explicit-session' }
    }),
    {
      systemAccountId: 'system-a',
      apiKeyId: `rotated-session-key-${index}`,
      clientIp: `203.0.113.${index + 1}`
    }
  )
))
assert.equal(new Set(sameExplicitSessionKeys).size, 1, '显式稳定会话必须优先于变化的网关 API Key、客户端地址和正文')

const sameCallerStormAccount = accountFixture({ id: 'account-varying-body-caller-storm' })
const sameCallerInitial = await evidenceService.prepareAttempt({
  account: sameCallerStormAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: sameCallerEvidenceKeys[0]
})
assert.equal(sameCallerInitial.outcome, 'dispatchable')
if (sameCallerInitial.outcome !== 'dispatchable') throw new Error('同源正文风暴首次 attempt 不可派发')
const sameCallerDecision = await sameCallerInitial.attempt.reportTransportFailure({ kind: 'transport', reason: '同源坏请求断流' })
assert.equal(sameCallerDecision.outcome, 'suspected')
const sameCallerStorm = await Promise.all(sameCallerEvidenceKeys.map((failureEvidenceKey) => evidenceService.prepareAttempt({
  account: sameCallerStormAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey
})))
assert.equal(sameCallerStorm.filter((result) => result.outcome === 'dispatchable').length, 0, '50 路变化正文的同源并发风暴不得取得独立 confirmation')
assert.equal((await evidenceStore.get(gatewayAccountProtocolModelScope(sameCallerStormAccount, 'text', 'gpt-5.4'))).phase, 'SUSPECT')
now += 3_000
const dueSameCallerProbe = await evidenceService.prepareAttempt({
  account: sameCallerStormAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: sameCallerEvidenceKeys[0]
})
assert.equal(dueSameCallerProbe.outcome, 'blocked', 'SUSPECT 到期后同一坏会话仍不得冒充独立 confirmation')
const sameCallerAfterDueFailure = await evidenceStore.get(gatewayAccountProtocolModelScope(sameCallerStormAccount, 'text', 'gpt-5.4'))
assert.equal(sameCallerAfterDueFailure.phase, 'SUSPECT')
assert.equal(sameCallerAfterDueFailure.confirmationFailureCount, 0, '到期同 evidence 再失败不得累计独立 confirmation')
assert.equal(sameCallerAfterDueFailure.lease, undefined, '被阻断的同 evidence 不得占用 confirmation 租约')

const independentRecoveryEvidenceKey = gatewayForegroundAccountCircuitFailureEvidenceKey(
  accountCircuitRequestFixture({ body: { input: '同一请求语义' } }),
  { systemAccountId: 'system-a', apiKeyId: 'gateway-key-a', clientIp: '203.0.113.250' }
)
assert.notEqual(independentRecoveryEvidenceKey, sameCallerEvidenceKeys[0], '独立恢复调用源必须产生不同证据')
const dueIndependentRecoveryProbe = await evidenceService.prepareAttempt({
  account: sameCallerStormAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: independentRecoveryEvidenceKey
})
assert.equal(dueIndependentRecoveryProbe.outcome, 'dispatchable', '不同调用源到期后必须能取得 single-flight confirmation')
if (dueIndependentRecoveryProbe.outcome !== 'dispatchable') throw new Error('独立调用源恢复探测不可派发')
assert.equal(dueIndependentRecoveryProbe.attempt.isConfirmation, true)
await dueIndependentRecoveryProbe.attempt.reportFramingComplete()
assert.equal(
  (await evidenceStore.get(gatewayAccountProtocolModelScope(sameCallerStormAccount, 'text', 'gpt-5.4'))).phase,
  'RECOVERING',
  '独立 confirmation 完整 framing 应推进恢复而不是维持误阻断'
)

const independentCallerEvidenceKeys = ['203.0.113.21', '203.0.113.22', '203.0.113.23'].map((clientIp) => (
  gatewayForegroundAccountCircuitFailureEvidenceKey(
    accountCircuitRequestFixture({ body: { input: '同一请求语义' } }),
    { systemAccountId: 'system-a', apiKeyId: 'gateway-key-a', clientIp }
  )
))
assert.equal(new Set(independentCallerEvidenceKeys).size, 3, '三个可观察客户端来源必须产生三份独立证据')
const independentCallerAccount = accountFixture({ id: 'account-three-independent-callers' })
const independentCallerInitial = await evidenceService.prepareAttempt({
  account: independentCallerAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: independentCallerEvidenceKeys[0]
})
assert.equal(independentCallerInitial.outcome, 'dispatchable')
if (independentCallerInitial.outcome !== 'dispatchable') throw new Error('独立调用源首次 attempt 不可派发')
const independentCallerDecision = await independentCallerInitial.attempt.reportTransportFailure({ kind: 'transport', reason: '第一来源断流' })
assert.equal(independentCallerDecision.outcome, 'suspected')
for (const [index, failureEvidenceKey] of independentCallerEvidenceKeys.slice(1).entries()) {
  now = (await evidenceStore.get(gatewayAccountProtocolModelScope(independentCallerAccount, 'text', 'gpt-5.4'))).retryAtMs ?? now
  const confirmationAttempt = await evidenceService.prepareAttempt({
    account: independentCallerAccount,
    requestLane: 'text',
    model: 'gpt-5.4',
    confirmationLeaseDurationMs: 30_000,
    confirmationFailuresRequired: 2,
    failureEvidenceKey
  })
  assert.equal(confirmationAttempt.outcome, 'dispatchable', `第 ${index + 2} 个独立来源必须能取得 confirmation`)
  if (confirmationAttempt.outcome !== 'dispatchable') throw new Error('独立调用源 confirmation 不可派发')
  assert.equal(confirmationAttempt.attempt.isConfirmation, true)
  await confirmationAttempt.attempt.reportTransportFailure({ kind: 'transport', reason: `第 ${index + 2} 来源断流` })
}
const independentCallerOpen = await evidenceStore.get(gatewayAccountProtocolModelScope(independentCallerAccount, 'text', 'gpt-5.4'))
assert.equal(independentCallerOpen.phase, 'OPEN', '首次失败加两个独立调用源 confirmation 失败后应 OPEN')
assert.deepEqual(independentCallerOpen.failureEvidenceKeys, independentCallerEvidenceKeys)

const unknownCallerEvidenceKeys = Array.from({ length: 50 }, (_, index) => (
  gatewayForegroundAccountCircuitFailureEvidenceKey(
    accountCircuitRequestFixture({
      body: { input: `未知来源变化正文 ${index}` },
      rawBody: Buffer.from(`unknown-caller-${index}`),
      headers: { 'x-client-request-id': `unknown-request-${index}` }
    }),
    {
      systemAccountId: 'system-a',
      apiKeyId: `rotated-gateway-key-${index}`,
      clientIp: undefined
    }
  )
))
assert.equal(new Set(unknownCallerEvidenceKeys).size, 1, '缺少客户端 IP 时不得凭轮换 API Key、正文或请求 id 推断来源独立')
const unknownCallerAccount = accountFixture({ id: 'account-unknown-caller-storm' })
const unknownCallerInitial = await evidenceService.prepareAttempt({
  account: unknownCallerAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey: unknownCallerEvidenceKeys[0]
})
assert.equal(unknownCallerInitial.outcome, 'dispatchable')
if (unknownCallerInitial.outcome !== 'dispatchable') throw new Error('未知来源首次 attempt 不可派发')
const unknownCallerDecision = await unknownCallerInitial.attempt.reportTransportFailure({ kind: 'transport', reason: '未知来源断流' })
assert.equal(unknownCallerDecision.outcome, 'suspected')
const unknownCallerStorm = await Promise.all(unknownCallerEvidenceKeys.map((failureEvidenceKey) => evidenceService.prepareAttempt({
  account: unknownCallerAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmationFailuresRequired: 2,
  failureEvidenceKey
})))
assert.equal(unknownCallerStorm.filter((result) => result.outcome === 'dispatchable').length, 0, '无 clientIp 的 50 路变化请求不得自证账户死亡')
assert.equal((await evidenceStore.get(gatewayAccountProtocolModelScope(unknownCallerAccount, 'text', 'gpt-5.4'))).phase, 'SUSPECT')

const evidenceInitial = await evidenceService.prepareAttempt({
  account: evidenceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: badSessionEvidence
})
assert.equal(evidenceInitial.outcome, 'dispatchable')
if (evidenceInitial.outcome !== 'dispatchable') throw new Error('坏会话首次 attempt 不可派发')
const evidenceDecision = await evidenceInitial.attempt.reportTransportFailure({ kind: 'transport', reason: '坏会话断流' })
assert.equal(evidenceDecision.outcome, 'suspected')
const sameEvidenceExplicitConfirmation = await evidenceService.prepareAttempt({
  account: evidenceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: badSessionEvidence
})
assert.equal(sameEvidenceExplicitConfirmation.outcome, 'blocked', '同一坏会话不得使用首错 confirmation 自证账户死亡')
const sameEvidenceStorm = await Promise.all(Array.from({ length: 24 }, () => evidenceService.prepareAttempt({
  account: evidenceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: badSessionEvidence
})))
assert.equal(sameEvidenceStorm.filter((result) => result.outcome === 'dispatchable').length, 0, '同一坏会话并发不得取得 confirmation')
assert.ok(sameEvidenceStorm.every((result) => result.outcome === 'blocked' && result.state.phase === 'SUSPECT'))
now = (await evidenceStore.get(gatewayAccountProtocolModelScope(evidenceAccount, 'text', 'gpt-5.4'))).retryAtMs ?? now
const independentEvidenceAttempts = await Promise.all(Array.from({ length: 12 }, () => evidenceService.prepareAttempt({
  account: evidenceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: healthySessionEvidence
})))
const independentEvidenceWinners = independentEvidenceAttempts.filter((result) => result.outcome === 'dispatchable')
assert.equal(independentEvidenceWinners.length, 12, '独立健康会话不得因 confirmation 租约被占用而饿死')
assert.equal(independentEvidenceAttempts.filter((result) => result.outcome === 'dispatchable' && result.attempt.isObserver).length, 11)
const independentEvidenceWinner = independentEvidenceAttempts.find((result) => result.outcome === 'dispatchable' && result.attempt.isConfirmation)
if (!independentEvidenceWinner || independentEvidenceWinner.outcome !== 'dispatchable') throw new Error('独立健康会话缺少 confirmation 赢家')
assert.equal(independentEvidenceWinner.attempt.isConfirmation, true)
await independentEvidenceWinner.attempt.reportFramingComplete()
assert.equal((await evidenceStore.get(gatewayAccountProtocolModelScope(evidenceAccount, 'text', 'gpt-5.4'))).phase, 'RECOVERING')

const confirmedDeadAccount = accountFixture({ id: 'account-independent-failures' })
const confirmedDeadInitial = await evidenceService.prepareAttempt({
  account: confirmedDeadAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: badSessionEvidence
})
assert.equal(confirmedDeadInitial.outcome, 'dispatchable')
if (confirmedDeadInitial.outcome !== 'dispatchable') throw new Error('真实死亡账户首次 attempt 不可派发')
const confirmedDeadDecision = await confirmedDeadInitial.attempt.reportTransportFailure({ kind: 'transport', reason: '第一会话断流' })
assert.equal(confirmedDeadDecision.outcome, 'suspected')
now = confirmedDeadDecision.state.retryAtMs ?? now
const confirmedDeadConfirmation = await evidenceService.prepareAttempt({
  account: confirmedDeadAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: healthySessionEvidence
})
assert.equal(confirmedDeadConfirmation.outcome, 'dispatchable')
  if (confirmedDeadConfirmation.outcome !== 'dispatchable') throw new Error('独立失败证据未取得 confirmation')
  await confirmedDeadConfirmation.attempt.reportTransportFailure({ kind: 'transport', reason: '第二会话也断流' })
  const afterFirstIndependentConfirmation = await evidenceStore.get(
    gatewayAccountProtocolModelScope(confirmedDeadAccount, 'text', 'gpt-5.4')
  )
  assert.equal(afterFirstIndependentConfirmation.phase, 'SUSPECT', '首次独立 confirmation 失败仍需切号并保持 SUSPECT')
  assert.equal(afterFirstIndependentConfirmation.confirmationFailureCount, 1)
  now = afterFirstIndependentConfirmation.retryAtMs ?? now
  const confirmedDeadSecondConfirmation = await evidenceService.prepareAttempt({
    account: confirmedDeadAccount,
    requestLane: 'text',
    model: 'gpt-5.4',
    confirmationLeaseDurationMs: 30_000,
    failureEvidenceKey: thirdSessionEvidence
  })
  assert.equal(confirmedDeadSecondConfirmation.outcome, 'dispatchable')
  if (confirmedDeadSecondConfirmation.outcome !== 'dispatchable') throw new Error('第二份独立 confirmation 未取得租约')
  await confirmedDeadSecondConfirmation.attempt.reportTransportFailure({ kind: 'transport', reason: '第三会话仍断流' })
  const confirmedDeadOpen = await evidenceStore.get(gatewayAccountProtocolModelScope(confirmedDeadAccount, 'text', 'gpt-5.4'))
  assert.equal(confirmedDeadOpen.phase, 'OPEN', '首次失败加两次独立 confirmation 失败后才应 OPEN')
  assert.equal(confirmedDeadOpen.confirmationFailureCount, 2)
  assert.deepEqual(confirmedDeadOpen.failureEvidenceKeys, [badSessionEvidence, healthySessionEvidence, thirdSessionEvidence])

const stormAccounts = Array.from({ length: 3 }, (_, index) => accountFixture({ id: `account-bad-session-storm-${index + 1}` }))
for (const stormAccount of stormAccounts) {
  const initial = await evidenceService.prepareAttempt({
    account: stormAccount,
    requestLane: 'text',
    model: 'gpt-5.4',
    confirmationLeaseDurationMs: 30_000,
    failureEvidenceKey: badSessionEvidence
  })
  assert.equal(initial.outcome, 'dispatchable')
  if (initial.outcome !== 'dispatchable') throw new Error('风暴账户首次 attempt 不可派发')
  const decision = await initial.attempt.reportTransportFailure({ kind: 'read_incomplete', reason: '同一坏会话缺少终态' })
  assert.equal(decision.outcome, 'suspected')
}
for (const stormAccount of stormAccounts) {
  const repeated = await Promise.all(Array.from({ length: 20 }, () => evidenceService.prepareAttempt({
    account: stormAccount,
    requestLane: 'text',
    model: 'gpt-5.4',
    confirmationLeaseDurationMs: 30_000,
    failureEvidenceKey: badSessionEvidence
  })))
  assert.equal(repeated.filter((result) => result.outcome === 'dispatchable').length, 0)
  assert.equal((await evidenceStore.get(gatewayAccountProtocolModelScope(stormAccount, 'text', 'gpt-5.4'))).phase, 'SUSPECT', '同一坏会话风暴不得把账户确认成 OPEN')
  const recovery = await evidenceService.prepareAttempt({
    account: stormAccount,
    requestLane: 'text',
    model: 'gpt-5.4',
    confirmationLeaseDurationMs: 30_000,
    failureEvidenceKey: healthySessionEvidence
  })
  assert.equal(recovery.outcome, 'dispatchable')
  if (recovery.outcome !== 'dispatchable') throw new Error('健康会话无法确认恢复风暴账户')
  assert.equal(recovery.attempt.isObserver, true, 'retryAt 前的健康会话应以 observer 身份继续服务')
  await recovery.attempt.reportFramingComplete()
  assert.equal((await evidenceStore.get(gatewayAccountProtocolModelScope(stormAccount, 'text', 'gpt-5.4'))).phase, 'CLOSED')
}

const framingResetAccount = accountFixture({
  id: 'account-framing-resets-parent-evidence',
  supportedModels: ['healthy-model', 'failed-model-a', 'failed-model-b', 'failed-model-c']
})
const framingResetStore = new MemoryAccountCircuitStore({ capacity: 16, now: () => now })
const framingResetService = new GatewayAccountCircuitService(framingResetStore, {
  now: () => now,
  createId: () => `framing-reset-${++id}`
})
const framingResetRevision = accountCircuitDispatchRevision(framingResetAccount)
const framingResetParentScope: AccountCircuitScope = {
  kind: 'account',
  accountRuntimeKey: framingResetAccount.id
}
const framingResetFailureScopes = ['failed-model-a', 'failed-model-b', 'failed-model-c'].map((model) => (
  gatewayAccountProtocolModelScope(framingResetAccount, 'text', model)
))
const healthyFramingAttempt = await framingResetService.prepareAttempt({
  account: framingResetAccount,
  requestLane: 'text',
  model: 'healthy-model',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(healthyFramingAttempt.outcome, 'dispatchable')
if (healthyFramingAttempt.outcome !== 'dispatchable') throw new Error('健康 framing attempt 不可派发')
for (const [index, failedScope] of framingResetFailureScopes.slice(0, 2).entries()) {
  await openProtocolModelCircuit(framingResetStore, failedScope, framingResetRevision, `framing-reset-${index}`, now)
  const evidence = await framingResetStore.recordProtocolModelOpenEvidence({
    scope: failedScope,
    generation: 1,
    dispatchRevision: framingResetRevision,
    evidenceId: `framing-reset-evidence-${index}`,
    accountTransitionId: 'framing-reset-parent-open',
    reason: '独立传输失败',
    confirmedFailureCount: 1,
    distinctScopeThreshold: 3,
    windowMs: 10 * 60_000,
    maxProtocolScopes: 8,
    nowMs: now
  })
  assert.equal(evidence.status, 'recorded')
}
await healthyFramingAttempt.attempt.reportFramingComplete()
await openProtocolModelCircuit(
  framingResetStore,
  framingResetFailureScopes[2]!,
  framingResetRevision,
  'framing-reset-2',
  now
)
const evidenceAfterHealthyFraming = await framingResetStore.recordProtocolModelOpenEvidence({
  scope: framingResetFailureScopes[2]!,
  generation: 1,
  dispatchRevision: framingResetRevision,
  evidenceId: 'framing-reset-evidence-2',
  accountTransitionId: 'framing-reset-parent-open',
  reason: '完整 framing 后的新传输失败',
  confirmedFailureCount: 1,
  distinctScopeThreshold: 3,
  windowMs: 10 * 60_000,
  maxProtocolScopes: 8,
  nowMs: now
})
assert.equal(evidenceAfterHealthyFraming.status, 'recorded', '普通 CLOSED attempt 的完整 framing 必须清除同 revision 的旧父级升级证据')
assert.equal(evidenceAfterHealthyFraming.protocolScopeCount, 1)
assert.equal((await framingResetStore.get(framingResetParentScope, now)).phase, 'CLOSED')

const lateHealthyFramingAttempt = await framingResetService.prepareAttempt({
  account: framingResetAccount,
  requestLane: 'image',
  model: 'healthy-model',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(lateHealthyFramingAttempt.outcome, 'dispatchable')
if (lateHealthyFramingAttempt.outcome !== 'dispatchable') throw new Error('迟到健康 framing attempt 不可派发')
for (const [index, failedScope] of framingResetFailureScopes.slice(0, 2).entries()) {
  const evidence = await framingResetStore.recordProtocolModelOpenEvidence({
    scope: failedScope,
    generation: 1,
    dispatchRevision: framingResetRevision,
    evidenceId: `framing-reset-reopen-${index}`,
    accountTransitionId: 'framing-reset-parent-open',
    reason: '并发独立传输失败',
    confirmedFailureCount: 1,
    distinctScopeThreshold: 3,
    windowMs: 10 * 60_000,
    maxProtocolScopes: 8,
    nowMs: now
  })
  assert.equal(evidence.status, index === 1 ? 'escalated' : 'recorded')
}
const parentBeforeLateFraming = await framingResetStore.get(framingResetParentScope, now)
assert.equal(parentBeforeLateFraming.phase, 'OPEN')
await lateHealthyFramingAttempt.attempt.reportFramingComplete()
const parentAfterLateFraming = await framingResetStore.get(framingResetParentScope, now)
assert.equal(parentAfterLateFraming.phase, 'OPEN', '父级已 OPEN 后的迟到普通 framing 只能清聚合证据，不得绕过 recovery 状态机')
assert.equal(parentAfterLateFraming.generation, parentBeforeLateFraming.generation)

const escalationAccount = accountFixture({
  id: 'account-parent-escalation',
  supportedModels: ['gpt-5.4', 'gpt-5.4-mini', 'gpt-5.4-nano']
})
const escalationMutations: Array<{
  scopeKind: string
  operation: string
  status: string
  phase: string
  previousPhase?: string
}> = []
const escalationStore = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
const escalationContributions: number[] = []
const escalationDistinctScopeThresholds: number[] = []
const escalationWindowsMs: number[] = []
const recordEscalationEvidence = escalationStore.recordProtocolModelOpenEvidence.bind(escalationStore)
escalationStore.recordProtocolModelOpenEvidence = async (input) => {
  escalationContributions.push(input.confirmedFailureCount)
  escalationDistinctScopeThresholds.push(input.distinctScopeThreshold)
  escalationWindowsMs.push(input.windowMs)
  return recordEscalationEvidence(input)
}
const escalationService = new GatewayAccountCircuitService(escalationStore, {
  now: () => now,
  createId: () => `escalation-${++id}`,
  onMutation: (mutation) => {
    escalationMutations.push({
      scopeKind: mutation.scope.kind,
      operation: mutation.operation,
      status: mutation.status,
      phase: mutation.state.phase,
      ...(mutation.previousPhase ? { previousPhase: mutation.previousPhase } : {})
    })
  }
})

const escalationTargets = [
  { requestLane: 'text' as const, model: 'gpt-5.4' },
  { requestLane: 'text' as const, model: 'gpt-5.4-mini' },
  { requestLane: 'text' as const, model: 'gpt-5.4-nano' },
  { requestLane: 'image' as const, model: 'gpt-5.4' }
]
const escalationInitialAttempts = await Promise.all(escalationTargets.map(async ({ requestLane, model }) => {
  const initial = await escalationService.prepareAttempt({
    account: escalationAccount,
    requestLane,
    model,
    confirmationLeaseDurationMs: 30_000,
    confirmationFailuresRequired: 1
  })
  assert.equal(initial.outcome, 'dispatchable')
  if (initial.outcome !== 'dispatchable') throw new Error(`${model} 初始 attempt 不可派发`)
  return initial.attempt
}))
const escalationDecisions = await Promise.all(escalationInitialAttempts.map(async (attempt, index) => {
  const target = escalationTargets[index]!
  const decision = await attempt.reportTransportFailure({
    kind: 'transport',
    reason: `${target.requestLane}/${target.model} 初始失败`
  })
  assert.equal(decision.outcome, 'suspected')
  return decision
}))
now = Math.max(...escalationDecisions.map((decision) => decision.state.retryAtMs ?? now))
const escalationConfirmationAttempts = await Promise.all(escalationDecisions.map(async (_decision, index) => {
  const target = escalationTargets[index]!
  const confirmationResult = await escalationService.prepareAttempt({
    account: escalationAccount,
    requestLane: target.requestLane,
    model: target.model,
    confirmationLeaseDurationMs: 30_000,
    confirmationFailuresRequired: 1,
    failureEvidenceKey: (index + 1).toString(16).repeat(64)
  })
  assert.equal(confirmationResult.outcome, 'dispatchable')
  if (confirmationResult.outcome !== 'dispatchable') throw new Error(`${target.model} confirmation 不可派发`)
  return confirmationResult.attempt
}))
await Promise.all(escalationConfirmationAttempts.slice(0, 3).map(async (attempt, index) => {
  await attempt.reportTransportFailure({ kind: 'timeout', reason: `${escalationTargets[index]!.model} confirmation 失败` })
}))
const escalatedGeneration = (await escalationStore.get({
  kind: 'account',
  accountRuntimeKey: escalationAccount.id
}, now)).generation
await escalationConfirmationAttempts[3]!.reportTransportFailure({
  kind: 'read_incomplete',
  reason: '父级已打开后的晚到 confirmation 失败'
})

const parentScope = { kind: 'account' as const, accountRuntimeKey: escalationAccount.id }
const parentState = await escalationStore.get(parentScope, now)
assert.equal(parentState.phase, 'OPEN', '三个独立协议/模型确认失败必须升级账户父级 OPEN')
assert.deepEqual(escalationContributions, [1, 1, 1, 1], '每个 child OPEN 对父级升级只能固定贡献 1，不能复用 confirmation 阈值或累计次数')
assert.deepEqual(escalationDistinctScopeThresholds, [3, 3, 3, 3], 'service 默认必须显式传递三个独立 scope 的升级阈值')
assert.deepEqual(escalationWindowsMs, [600_000, 600_000, 600_000, 600_000], 'service 默认升级窗口必须是 10 分钟')
assert.equal(parentState.childScopeKeys?.length, 4, '并发升级与晚到 confirmation 必须保留全部子作用域证据')
assert.equal(parentState.generation, escalatedGeneration, '父级已 OPEN 时晚到证据只能追加，不得重复升代')
assert.equal(
  escalationMutations.filter((mutation) => (
    mutation.scopeKind === 'account'
    && mutation.operation === 'record_parent_evidence'
    && mutation.status === 'applied'
    && mutation.previousPhase === 'CLOSED'
  )).length,
  1,
  '父级 CLOSED -> OPEN 只能持久化/观测一次'
)
const parentBlocked = await escalationService.prepareAttempt({
  account: escalationAccount,
  requestLane: 'image',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(parentBlocked.outcome, 'blocked', '账户父级 OPEN 必须阻断其他 lane/model')
if (parentBlocked.outcome === 'blocked') assert.equal(parentBlocked.state.scope.kind, 'account')

const configuredEscalationModels = ['configured-a', 'configured-b', 'configured-c', 'configured-d']
const configuredEscalationAccount = accountFixture({
  id: 'account-parent-escalation-configured',
  supportedModels: configuredEscalationModels
})
const configuredEscalationStore = new MemoryAccountCircuitStore({ capacity: 16, now: () => now })
const configuredEscalationService = new GatewayAccountCircuitService(configuredEscalationStore, {
  now: () => now,
  createId: () => `configured-escalation-${++id}`,
  escalationDistinctScopeThreshold: 4,
  escalationWindowMs: 60_000
})
const configuredParentScope = {
  kind: 'account' as const,
  accountRuntimeKey: configuredEscalationAccount.id
}
for (const [index, model] of configuredEscalationModels.entries()) {
  const initial = await configuredEscalationService.prepareAttempt({
    account: configuredEscalationAccount,
    requestLane: 'text',
    model,
    confirmationLeaseDurationMs: 30_000,
    confirmationFailuresRequired: 1
  })
  assert.equal(initial.outcome, 'dispatchable')
  if (initial.outcome !== 'dispatchable') throw new Error(`配置升级阈值 ${model} 首次 attempt 不可派发`)
  const decision = await initial.attempt.reportTransportFailure({ kind: 'transport', reason: `${model} 初次断流` })
  assert.equal(decision.outcome, 'suspected')
  now = decision.state.retryAtMs ?? now
  const confirmationAttempt = await configuredEscalationService.prepareAttempt({
    account: configuredEscalationAccount,
    requestLane: 'text',
    model,
    confirmationLeaseDurationMs: 30_000,
    confirmationFailuresRequired: 1,
    failureEvidenceKey: (index + 10).toString(16).repeat(64)
  })
  assert.equal(confirmationAttempt.outcome, 'dispatchable')
  if (confirmationAttempt.outcome !== 'dispatchable') throw new Error(`配置升级阈值 ${model} confirmation 不可派发`)
  await confirmationAttempt.attempt.reportTransportFailure({ kind: 'timeout', reason: `${model} confirmation 失败` })
  const phase = (await configuredEscalationStore.get(configuredParentScope, now)).phase
  assert.equal(phase, index === 3 ? 'OPEN' : 'CLOSED', '配置为 4 时只有第四个独立 child OPEN 才能升级父级')
}

const revisionAccount = accountFixture({ id: 'account-parent-revision', dispatchRevision: 2 })
const revisionStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => now })
const staleParentScope = { kind: 'account' as const, accountRuntimeKey: revisionAccount.id }
await revisionStore.restore({
  scope: staleParentScope,
  scopeKey: accountCircuitScopeKey(staleParentScope),
  phase: 'OPEN',
  generation: 4,
  dispatchRevision: '1',
  transitionId: 'old-parent-open',
  backoffAttempt: 2,
  recoverySuccessCount: 0,
  openedAtMs: now,
  retryAtMs: now + 30_000,
  updatedAtMs: now
})
const revisionMutations: Array<{ operation: string; status: string; scopeKind: string }> = []
const revisionService = new GatewayAccountCircuitService(revisionStore, {
  now: () => now,
  createId: () => `revision-${++id}`,
  onMutation: (mutation) => {
    revisionMutations.push({
      operation: mutation.operation,
      status: mutation.status,
      scopeKind: mutation.scope.kind
    })
  }
})
const revisionDecision = await revisionService.prepareAttempt({
  account: revisionAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(revisionDecision.outcome, 'dispatchable', '旧 revision 父级 OPEN 不得阻断新配置')
assert.ok(revisionMutations.some((mutation) => (
  mutation.operation === 'replace_revision'
  && mutation.status === 'applied'
  && mutation.scopeKind === 'account'
)), '父级 revision fence 必须进入 bridge mutation')
const replacedParent = await revisionStore.get(staleParentScope, now)
assert.equal(replacedParent.phase, 'CLOSED')
assert.equal(replacedParent.dispatchRevision, '2')

const unchangedOwnerScope = { kind: 'account' as const, accountRuntimeKey: 'account-owner-config-unchanged' }
const unchangedOwnerStore = new MemoryAccountCircuitStore({ capacity: 4, now: () => now })
await unchangedOwnerStore.restore({
  scope: unchangedOwnerScope,
  scopeKey: accountCircuitScopeKey(unchangedOwnerScope),
  phase: 'OPEN',
  generation: 3,
  dispatchRevision: '6',
  transitionId: 'owner-open-6',
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  openedAtMs: now,
  retryAtMs: now + 30_000,
  updatedAtMs: now
})
const unchangedOwnerService = new GatewayAccountCircuitService(unchangedOwnerStore, { now: () => now })
const unrelatedConfigDecision = await unchangedOwnerService.prepareAttempt({
  account: accountFixture({
    id: unchangedOwnerScope.accountRuntimeKey,
    dispatchRevision: 6,
    priority: 99,
    concurrencyLimit: 99
  }),
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(unrelatedConfigDecision.outcome, 'blocked', '优先级/并发变化后同 owner revision 的 OPEN 必须继续阻断')
const staleCandidateStore = new MemoryAccountCircuitStore({ capacity: 4, now: () => now })
const staleCandidateScope = { kind: 'account' as const, accountRuntimeKey: 'account-stale-candidate' }
await staleCandidateStore.restore({
  ...closedAccountCircuitState(staleCandidateScope, '8', 4, 'owner-8-closed', now),
  updatedAtMs: now
})
const staleCandidateDecision = await new GatewayAccountCircuitService(staleCandidateStore, { now: () => now }).prepareAttempt({
  account: accountFixture({ id: staleCandidateScope.accountRuntimeKey, dispatchRevision: 7 }),
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})
assert.equal(staleCandidateDecision.outcome, 'blocked', '迟到旧候选不得把新 owner CLOSED 反向换代后派发')
assert.equal((await staleCandidateStore.get(staleCandidateScope, now)).dispatchRevision, '8')

const failingStore = {
  ...store,
  get: async () => { throw new Error('redis unavailable') }
}
const failingService = new GatewayAccountCircuitService(failingStore as unknown as AccountCircuitStore)
await assert.rejects(() => failingService.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
}), /redis unavailable/, '运行态失败不得伪装成 CLOSED')

const previousMode = runtimeConfig.runtimeMode
const previousDriver = runtimeConfig.runtimeStateDriver
const previousStateUrl = runtimeConfig.redis.stateUrl
try {
  resetGatewayAccountCircuitStoreForTest()
  runtimeConfig.runtimeMode = 'standalone'
  runtimeConfig.runtimeStateDriver = 'memory'
  const firstMemoryStore = getGatewayAccountCircuitStore()
  const secondMemoryStore = getGatewayAccountCircuitStore()
  assert.equal(firstMemoryStore, secondMemoryStore, 'standalone 必须复用进程单例 memory store')

  resetGatewayAccountCircuitStoreForTest()
  runtimeConfig.runtimeMode = 'performance'
  runtimeConfig.runtimeStateDriver = 'redis'
  runtimeConfig.redis.stateUrl = undefined
  assert.throws(() => getGatewayAccountCircuitStore(), /JUHE_AI_REDIS_STATE_URL/, 'performance 缺少 Redis state URL 必须显式失败')
} finally {
  runtimeConfig.runtimeMode = previousMode
  runtimeConfig.runtimeStateDriver = previousDriver
  runtimeConfig.redis.stateUrl = previousStateUrl
  resetGatewayAccountCircuitStoreForTest()
}

const upstreamDispatchSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url)), 'utf8')
const routesSource = readFileSync(fileURLToPath(new URL('../../modules/gateway/routes.ts', import.meta.url)), 'utf8')
assert.match(upstreamDispatchSource, /getGatewayAccountCircuitService/, '上游候选派发必须接入账户短电路 service')
assert.match(upstreamDispatchSource, /retrySameAccount:\s*false/, '普通 same-account retry 不得参与短电路路径')
assert.doesNotMatch(upstreamDispatchSource, /function shouldRetrySameAccountAfterFailure/, '旧普通同号重试函数必须移除')
assert.doesNotMatch(upstreamDispatchSource, /SameAccountRetryBudget|createSameAccountRetryBudget|sameAccountRetryBudget/, '非租约同号重试预算必须移除')
assert.doesNotMatch(upstreamDispatchSource, /OpaqueFailoverBudget|maxOpaqueFailoverAccountsPerRequest|opaqueFailoverBudget/, '普通候选不得保留固定四账户预算')
assert.match(routesSource, /transportFailure[\s\S]*reportTransportFailure/, '响应读取未完成必须回报账户短电路')

console.log('gateway account circuit dispatch regression passed')

async function restoreOpenParentShadow(
  store: AccountCircuitStore,
  childState: AccountCircuitState,
  dispatchRevision: string,
  prefix: string,
  nowMs: number
): Promise<void> {
  const parentScope: AccountCircuitScope = {
    kind: 'account',
    accountRuntimeKey: childState.scope.accountRuntimeKey
  }
  const parentIncidentId = `${prefix}-parent-incident`
  const restored = await store.restore({
    scope: parentScope,
    scopeKey: accountCircuitScopeKey(parentScope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision,
    transitionId: `${prefix}-parent-open`,
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    incidentId: parentIncidentId,
    childScopeKeys: [childState.scopeKey],
    childIncidentIds: [childState.incidentId ?? `${childState.scopeKey}@${childState.generation}`],
    requiredRecoveryScopeKeys: [childState.scopeKey],
    openedAtMs: nowMs,
    retryAtMs: nowMs + 30_000,
    failureReason: 'parent concurrent open',
    updatedAtMs: nowMs
  }, nowMs)
  assert.equal(restored.status, 'applied')
}

async function openProtocolModelCircuit(
  store: AccountCircuitStore,
  scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>,
  dispatchRevision: string,
  prefix: string,
  nowMs: number
): Promise<void> {
  assert.equal((await store.suspect({
    scope,
    dispatchRevision,
    transitionId: `${prefix}-suspect`,
    reason: 'transport',
    confirmationFailuresRequired: 1,
    failureEvidenceKey: `${prefix}:initial`.padEnd(64, '0').slice(0, 64).replace(/[^a-f0-9]/g, 'a'),
    nowMs: nowMs - 3_000
  })).status, 'applied')
  assert.equal((await store.acquireConfirmationLease({
    scope,
    generation: 1,
    dispatchRevision,
    transitionId: `${prefix}-acquire`,
    leaseId: `${prefix}-lease`,
    leaseUntilMs: nowMs + 30_000,
    confirmationEvidenceKey: `${prefix}:confirmation`.padEnd(64, '1').slice(0, 64).replace(/[^a-f0-9]/g, 'b'),
    nowMs
  })).status, 'applied')
  const opened = await store.completeConfirmation({
    scope,
    generation: 1,
    dispatchRevision,
    transitionId: `${prefix}-open`,
    leaseId: `${prefix}-lease`,
    outcome: 'transport_failure',
    reason: 'transport',
    nowMs
  })
  assert.equal(opened.status, 'applied')
  assert.equal(opened.state.phase, 'OPEN')
}

function accountCircuitRequestFixture(input: {
  body?: unknown
  rawBody?: Buffer
  headers?: Record<string, string>
} = {}): Request {
  const headers = new Map(Object.entries(input.headers ?? {}).map(([name, value]) => [name.toLowerCase(), value]))
  return {
    body: input.body,
    rawBody: input.rawBody,
    header: (name: string) => headers.get(name.toLowerCase())
  } as unknown as Request
}

function accountFixture(overrides: Partial<UpstreamAccount> = {}): UpstreamAccount {
  return {
    id: 'account-circuit-a',
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'system-a',
    accountOwnerSystemAccountId: 'system-a',
    groupOwnerSystemAccountId: 'system-a',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: 'Circuit A',
    type: 'openai_api_key',
    status: 'active',
    concurrencyLimit: 4,
    priority: 1,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    supportedModels: ['gpt-5.4'],
    modelMappings: [],
    healthCheckModel: 'gpt-5.4',
    healthCheckEndpointMode: 'responses_json',
    baseUrl: 'https://api.example.com/v1',
    apiKey: 'secret-api-key',
    apiKeys: ['secret-api-key'],
    refreshToken: 'refresh-secret',
    streamFailureCount: 0,
    credentials: { api_key: 'secret-api-key', refresh_token: 'refresh-secret' },
    ...overrides
  }
}
