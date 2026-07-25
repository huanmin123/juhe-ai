import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'

import {
  GatewayRequestAttemptTracker,
  GatewayRequestWallBudget,
  RouteCoordinationBudget,
  advanceGatewayRoutePlanCursor,
  createGatewayRoutePlanSnapshot,
  defaultGatewayFinalResponseReserveMs,
  defaultGatewayRequestWallBudgetMs,
  defaultRouteCoordinationBudgetMs,
  gatewayAttemptProtocolModelKey,
  type GatewayRouteCoordinatorOwner,
  type RouteCoordinationResult
} from '../../modules/gateway/routing/route-coordination.js'

let nowMs = 1_000
const wallBudget = new GatewayRequestWallBudget({
  requestAcceptedAtMs: nowMs,
  now: () => nowMs
})
assert.equal(wallBudget.budgetMs, defaultGatewayRequestWallBudgetMs)
assert.equal(defaultGatewayFinalResponseReserveMs, 2_000)
assert.equal(wallBudget.deadlineAtMs, 271_000)
assert.equal(wallBudget.remainingMs(), 270_000)
assert.equal(wallBudget.handoffRequired({ finalResponseReserveMs: 2_000 }), false)

nowMs = 268_999
assert.equal(wallBudget.remainingMs(), 2_001)
assert.equal(wallBudget.handoffRequired({ finalResponseReserveMs: 2_000 }), false)
nowMs = 269_000
assert.equal(wallBudget.handoffRequired({ finalResponseReserveMs: 2_000 }), true, '预留最终响应时间后不得再启动新决策')
assert.equal(wallBudget.handoffRequired(), true, '未显式传参时也必须保留默认最终响应尾窗')
nowMs = 200_000
assert.equal(wallBudget.handoffRequired({ finalResponseReserveMs: 2_000, minimumMeaningfulAttemptMs: 70_000 }), true, '剩余墙钟不足一次有意义 attempt 时应 handoff')
assert.equal(wallBudget.precommitRemainingMs({
  nowMs,
  requestPrecommitDeadlineAtMs: 260_000,
  finalResponseReserveMs: 2_000
}), 58_000)
assert.equal(wallBudget.clipFirstByteDeadlineMs({
  nowMs,
  firstByteDeadlineMs: 90_000,
  requestPrecommitDeadlineAtMs: 260_000,
  finalResponseReserveMs: 2_000,
  uncommittedAttemptDeadlineAtMs: 245_000
}), 45_000, '首字 deadline 必须取配置、墙钟、precommit 和 attempt 尾窗中的最小值')
assert.equal(wallBudget.clipFirstByteDeadlineMs({
  nowMs: 269_500,
  firstByteDeadlineMs: 30_000,
  requestPrecommitDeadlineAtMs: 271_000,
  finalResponseReserveMs: 2_000
}), 0, '进入最终响应保留窗口后不得再启动未提交 attempt')

let coordinationNowMs = 10_000
const coordinationBudget = new RouteCoordinationBudget({
  requestId: 'request-a',
  budgetId: 'coordination-a',
  now: () => coordinationNowMs
})
assert.deepEqual(coordinationBudget.snapshot(), {
  requestId: 'request-a',
  budgetId: 'coordination-a',
  version: 0,
  remainingMs: defaultRouteCoordinationBudgetMs,
  activeSinceMs: undefined,
  lastWaitToken: undefined
})
const waitStarted = coordinationBudget.beginWait({ waitToken: 'wait-a', expectedVersion: 0 })
assert.equal(waitStarted.outcome, 'applied')
assert.equal(waitStarted.snapshot.version, 1)
assert.equal(waitStarted.snapshot.activeSinceMs, 10_000)

coordinationNowMs = 10_700
assert.equal(coordinationBudget.remainingMs(), 2_300)
const duplicateBegin = coordinationBudget.beginWait({ waitToken: 'wait-a', expectedVersion: 0 })
assert.equal(duplicateBegin.outcome, 'idempotent_replay', '重复 begin 不得重置 activeSince')
assert.equal(duplicateBegin.snapshot.activeSinceMs, 10_000)

const waitPaused = coordinationBudget.pauseWait({ waitToken: 'wait-a', expectedVersion: 1 })
assert.equal(waitPaused.outcome, 'applied')
assert.equal(waitPaused.snapshot.version, 2)
assert.equal(waitPaused.snapshot.remainingMs, 2_300)
assert.equal(waitPaused.snapshot.activeSinceMs, undefined)

coordinationNowMs = 20_000
assert.equal(coordinationBudget.remainingMs(), 2_300, 'attempt 和读取期间协调预算必须暂停')
const duplicatePause = coordinationBudget.pauseWait({ waitToken: 'wait-a', expectedVersion: 1 })
assert.equal(duplicatePause.outcome, 'idempotent_replay', '异步重复 pause 不得再次扣减')
assert.equal(duplicatePause.snapshot.remainingMs, 2_300)
const staleBegin = coordinationBudget.beginWait({ waitToken: 'wait-b', expectedVersion: 1 })
assert.equal(staleBegin.outcome, 'version_conflict')
assert.equal(staleBegin.snapshot.remainingMs, 2_300)

const secondWait = coordinationBudget.beginWait({ waitToken: 'wait-b', expectedVersion: 2 })
assert.equal(secondWait.outcome, 'applied')
coordinationNowMs = 20_500
const delayedDuplicatePause = coordinationBudget.pauseWait({ waitToken: 'wait-a', expectedVersion: 1 })
assert.equal(delayedDuplicatePause.outcome, 'idempotent_replay')
assert.equal(delayedDuplicatePause.snapshot.activeSinceMs, 20_000, '旧 wait 的异步重复回调不得暂停当前 wait')
assert.equal(coordinationBudget.remainingMs(), 1_800)
coordinationNowMs = 24_000
assert.equal(coordinationBudget.remainingMs(), 0)
const secondPause = coordinationBudget.pauseWait({ waitToken: 'wait-b', expectedVersion: 3 })
assert.equal(secondPause.outcome, 'applied')
assert.equal(secondPause.snapshot.remainingMs, 0)
assert.equal(coordinationBudget.exhausted(), true)

const attempts = new GatewayRequestAttemptTracker()
assert.equal(attempts.recordAccountRuntimeKey('runtime-a'), true)
assert.equal(attempts.recordAccountRuntimeKey('runtime-a'), false)
assert.equal(attempts.recordPhysicalCredentialKey('physical-a'), true)
assert.equal(attempts.recordKeyFingerprint('fingerprint-a'), true)
assert.equal(attempts.recordProtocolModelKey('openai-v1:gpt-5'), true)
assert.equal(attempts.hasAccountRuntimeKey('runtime-a'), true)
assert.equal(attempts.hasPhysicalCredentialKey('physical-a'), true)
assert.equal(attempts.hasKeyFingerprint('fingerprint-a'), true)
assert.equal(attempts.hasProtocolModelKey('openai-v1:gpt-5'), true)
assert.deepEqual(attempts.snapshot(), {
  attemptedAccountRuntimeKeys: ['runtime-a'],
  attemptedPhysicalCredentialKeys: ['physical-a'],
  attemptedKeyFingerprints: ['fingerprint-a'],
  attemptedProtocolModelKeys: ['openai-v1:gpt-5']
})

const dispatchAttempts = new GatewayRequestAttemptTracker()
const protocolModelKey = gatewayAttemptProtocolModelKey({
  accountRuntimeKey: 'runtime-owner',
  protocolCode: 'openai-v1',
  protocolVersion: '1',
  model: 'gpt-5'
})
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a'
}), { allowed: true })
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a'
}), { allowed: false, reason: 'physical_credential_already_attempted' })
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner:authorized:grantee:group-a:auth-a',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a'
}), { allowed: false, reason: 'physical_credential_already_attempted' })
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-b',
  allowKeyRotation: true
}), { allowed: true })
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-b',
  allowKeyRotation: true
}), { allowed: false, reason: 'key_fingerprint_already_attempted' })
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a',
  matchingConfirmation: true
}), { allowed: true })
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a',
  matchingConfirmation: true
}), { allowed: false, reason: 'confirmation_already_attempted' })
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-c',
  matchingConfirmation: true,
  allowKeyRotation: true
}), { allowed: true }, '同一 confirmation 必须允许轮换到同物理凭据下未尝试的新 Key')
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-c',
  matchingConfirmation: true,
  allowKeyRotation: true
}), { allowed: false, reason: 'key_fingerprint_already_attempted' }, '同一 confirmation 不得重复轮换到已尝试 Key')
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-other',
  keyFingerprint: 'key-d',
  matchingConfirmation: true,
  allowKeyRotation: true
}), { allowed: false, reason: 'physical_credential_already_attempted' }, 'confirmation Key 轮换不得跨物理凭据')
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-other',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-e',
  matchingConfirmation: true,
  allowKeyRotation: true
}), { allowed: false, reason: 'physical_credential_already_attempted' }, 'confirmation Key 轮换不得跨账户运行身份')
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a',
  semanticRetryId: 'hybrid-quality-repair-1'
}), { allowed: true }, '显式语义修复应允许同一物理凭据进入新的有界重试代次')
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a',
  semanticRetryId: 'hybrid-quality-repair-1'
}), { allowed: false, reason: 'semantic_retry_already_attempted' }, '同一语义重试代次仍不得重复命中物理凭据')
assert.deepEqual(dispatchAttempts.tryRecordDispatchAttempt({
  protocolModelKey,
  accountRuntimeKey: 'runtime-owner',
  physicalCredentialKey: 'physical-owner',
  keyFingerprint: 'key-a',
  semanticRetryId: 'hybrid-quality-repair-2'
}), { allowed: true }, '下一次用户策略授权的语义修复应使用新的独立代次')

const zeroRetryTracker = new GatewayRequestAttemptTracker()
const zeroRetryIdentity = {
  protocolModelKey: gatewayAttemptProtocolModelKey({
    accountRuntimeKey: 'runtime-zero-retry',
    protocolCode: 'openai-v1',
    protocolVersion: '1',
    model: 'gpt-5'
  }),
  accountRuntimeKey: 'runtime-zero-retry',
  physicalCredentialKey: 'physical-zero-retry',
  keyFingerprint: 'key-zero-retry'
}
assert.deepEqual(zeroRetryTracker.tryRecordDispatchAttempt(zeroRetryIdentity), { allowed: true })
const zeroRetrySnapshot = zeroRetryTracker.snapshot()
assert.deepEqual(zeroRetryTracker.tryReserveSameAccountRetry({
  ...zeroRetryIdentity,
  maxRetries: 0
}), {
  reserved: false,
  reason: 'same_account_retry_budget_exhausted',
  remaining: 0
}, 'maxRetries=0 必须禁用请求内原地重试')
assert.deepEqual(zeroRetryTracker.tryReserveSameAccountRetry({
  ...zeroRetryIdentity,
  maxRetries: 3
}), {
  reserved: false,
  reason: 'same_account_retry_budget_exhausted',
  remaining: 0
}, '首次见到 0 后，后续分组配置不得把请求级预算扩容到 3')
assert.deepEqual(zeroRetryTracker.snapshot(), zeroRetrySnapshot, '读取和拒绝原地重试预算不得污染尝试快照')
assert.throws(() => zeroRetryTracker.tryReserveSameAccountRetry({ ...zeroRetryIdentity, maxRetries: -1 }), /0 and 10/)
assert.throws(() => zeroRetryTracker.tryReserveSameAccountRetry({ ...zeroRetryIdentity, maxRetries: 11 }), /0 and 10/)
assert.throws(() => zeroRetryTracker.tryReserveSameAccountRetry({ ...zeroRetryIdentity, maxRetries: 1.5 }), /0 and 10/)

const oneRetryTracker = new GatewayRequestAttemptTracker()
const oneRetryIdentity = {
  protocolModelKey: gatewayAttemptProtocolModelKey({
    accountRuntimeKey: 'runtime-one-retry',
    protocolCode: 'openai-v1',
    protocolVersion: '1',
    model: 'gpt-5'
  }),
  accountRuntimeKey: 'runtime-one-retry',
  physicalCredentialKey: 'physical-one-retry',
  keyFingerprint: 'key-one-retry'
}
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt(oneRetryIdentity), { allowed: true })
const oneRetrySnapshot = oneRetryTracker.snapshot()
const oneRetryReservation = oneRetryTracker.tryReserveSameAccountRetry({
  ...oneRetryIdentity,
  maxRetries: 1
})
assert(oneRetryReservation.reserved, 'maxRetries=1 必须预留一次原地重试')
assert.equal(oneRetryReservation.retryNumber, 1)
assert.equal(oneRetryReservation.remaining, 0)
assert.match(oneRetryReservation.retryId, /^same-account-retry:/)
assert.deepEqual(oneRetryTracker.canAttemptAccount(oneRetryIdentity), {
  allowed: false,
  reason: 'physical_credential_already_attempted'
}, '普通 canAttemptAccount 不得因存在原地重试 token 而放行')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  sameAccountRetryId: 'same-account-retry:forged'
}), { allowed: false, reason: 'same_account_retry_not_registered' }, '伪造 retryId 必须被拒绝')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  accountRuntimeKey: 'runtime-new-account',
  physicalCredentialKey: 'physical-new-account',
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: false, reason: 'same_account_retry_identity_mismatch' }, '原地重试 token 不得放行新账户')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  accountRuntimeKey: 'runtime-shared-credential-alias',
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: false, reason: 'same_account_retry_identity_mismatch' }, '原地重试 token 不得放行共享物理凭据别名')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  keyFingerprint: 'key-one-retry-rotated',
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: false, reason: 'same_account_retry_identity_mismatch' }, '原地重试 token 不得绕过 Key rotation 更换指纹')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  matchingConfirmation: true,
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: false, reason: 'same_account_retry_mode_conflict' }, '原地重试不得与 confirmation 模式叠加')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  allowKeyRotation: true,
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: false, reason: 'same_account_retry_mode_conflict' }, '原地重试不得与 Key rotation 模式叠加')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  semanticRetryId: 'semantic-conflict',
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: false, reason: 'same_account_retry_mode_conflict' }, '原地重试不得与 semantic retry 模式叠加')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: true }, '匹配已登记账户和物理凭据的 retryId 必须恰好放行一次')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  sameAccountRetryId: oneRetryReservation.retryId
}), { allowed: false, reason: 'same_account_retry_already_attempted' }, '已消费 retryId 不得重复使用')
assert.deepEqual(oneRetryTracker.tryReserveSameAccountRetry({
  ...oneRetryIdentity,
  maxRetries: 3
}), {
  reserved: false,
  reason: 'same_account_retry_budget_exhausted',
  remaining: 0
}, '首次 maxRetries=1 后，后续更大的配置不得扩容')
assert.deepEqual(oneRetryTracker.snapshot(), oneRetrySnapshot, '预留、拒绝和成功的原地重试均不得污染普通尝试快照')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  matchingConfirmation: true
}), { allowed: true }, '原地重试不得预先消耗 confirmation 的独立登记')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  matchingConfirmation: true
}), { allowed: false, reason: 'confirmation_already_attempted' }, '原地重试后 confirmation 仍必须保持单次隔离')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  semanticRetryId: 'semantic-after-same-account-retry'
}), { allowed: true }, '原地重试不得预先消耗 semantic retry 的独立登记')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  semanticRetryId: 'semantic-after-same-account-retry'
}), { allowed: false, reason: 'semantic_retry_already_attempted' }, '原地重试后 semantic retry 仍必须保持同代次隔离')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  keyFingerprint: 'key-one-retry-rotated',
  allowKeyRotation: true
}), { allowed: true }, '原地重试不得阻断合法的新物理 Key rotation')
assert.deepEqual(oneRetryTracker.tryRecordDispatchAttempt({
  ...oneRetryIdentity,
  keyFingerprint: 'key-one-retry-rotated',
  allowKeyRotation: true
}), { allowed: false, reason: 'key_fingerprint_already_attempted' }, '原地重试后 Key rotation 仍必须按指纹隔离')

const sharedRetryTracker = new GatewayRequestAttemptTracker()
const sharedRetryIdentities = ['a', 'b', 'c'].map((suffix) => ({
  protocolModelKey: gatewayAttemptProtocolModelKey({
    accountRuntimeKey: `runtime-shared-retry-${suffix}`,
    protocolCode: 'openai-v1',
    protocolVersion: '1',
    model: 'gpt-5'
  }),
  accountRuntimeKey: `runtime-shared-retry-${suffix}`,
  physicalCredentialKey: `physical-shared-retry-${suffix}`,
  keyFingerprint: `key-shared-retry-${suffix}`
}))
for (const identity of sharedRetryIdentities) {
  assert.deepEqual(sharedRetryTracker.tryRecordDispatchAttempt(identity), { allowed: true })
}
const sharedRetrySnapshot = sharedRetryTracker.snapshot()
const sharedReservations = sharedRetryIdentities.map((identity) => sharedRetryTracker.tryReserveSameAccountRetry({
  ...identity,
  maxRetries: 3
}))
for (const [index, reservation] of sharedReservations.entries()) {
  assert(reservation.reserved, `第 ${index + 1} 次共享原地重试必须成功预留`)
  assert.equal(reservation.retryNumber, index + 1, '跨账户/分组不得重置请求级 retryNumber')
  assert.equal(reservation.remaining, 2 - index)
}
const sharedRetryIds = sharedReservations.map((reservation) => {
  assert(reservation.reserved)
  return reservation.retryId
})
assert.equal(new Set(sharedRetryIds).size, 3, '每次原地重试预留必须返回唯一 retryId')
for (const [index, identity] of sharedRetryIdentities.entries()) {
  assert.deepEqual(sharedRetryTracker.tryRecordDispatchAttempt({
    ...identity,
    sameAccountRetryId: sharedRetryIds[index]
  }), { allowed: true })
}
assert.deepEqual(sharedRetryTracker.tryReserveSameAccountRetry({
  ...sharedRetryIdentities[0]!,
  maxRetries: 10
}), {
  reserved: false,
  reason: 'same_account_retry_budget_exhausted',
  remaining: 0
}, '跨账户用完首次锁定的 3 次预算后，更大的后续配置不得重新发放额度')
assert.deepEqual(sharedRetryTracker.snapshot(), sharedRetrySnapshot, '跨账户共享重试也不得扩充普通尝试快照')

const conservativeRetryTracker = new GatewayRequestAttemptTracker()
for (const identity of sharedRetryIdentities.slice(0, 2)) {
  assert.deepEqual(conservativeRetryTracker.tryRecordDispatchAttempt(identity), { allowed: true })
}
const firstConservativeReservation = conservativeRetryTracker.tryReserveSameAccountRetry({
  ...sharedRetryIdentities[0]!,
  maxRetries: 3
})
assert(firstConservativeReservation.reserved)
assert.equal(firstConservativeReservation.remaining, 2)
assert.deepEqual(conservativeRetryTracker.tryReserveSameAccountRetry({
  ...sharedRetryIdentities[1]!,
  maxRetries: 1
}), {
  reserved: false,
  reason: 'same_account_retry_budget_exhausted',
  remaining: 0
}, '后续分组看到更小 maxRetries 时必须立即采用更保守的请求级上限')
assert.deepEqual(conservativeRetryTracker.tryReserveSameAccountRetry({
  ...sharedRetryIdentities[1]!,
  maxRetries: 10
}), {
  reserved: false,
  reason: 'same_account_retry_budget_exhausted',
  remaining: 0
}, '预算收紧后不得再被更大配置扩容')

const aliasRetryTracker = new GatewayRequestAttemptTracker()
assert.deepEqual(aliasRetryTracker.tryRecordDispatchAttempt(oneRetryIdentity), { allowed: true })
assert.deepEqual(aliasRetryTracker.tryReserveSameAccountRetry({
  ...oneRetryIdentity,
  accountRuntimeKey: 'runtime-shared-credential-alias',
  maxRetries: 1
}), {
  reserved: false,
  reason: 'same_account_retry_not_applicable',
  remaining: 1
}, '共享同一物理凭据的别名账户不得自行取得原地重试 token')

const targets = [{ groupId: 'group-a' }, { groupId: 'group-b' }] as const
const plan = createGatewayRoutePlanSnapshot({
  routePlanId: 'route-plan-a',
  mode: 'failover',
  requestAcceptedAtMs: 1_000,
  gatewayRequestWallBudgetMs: 270_000,
  firstByteDeadlineMs: 30_000,
  finalResponseReserveMs: 2_000,
  orderedAllowedTargets: targets,
  weightedDecisionToken: 'weight-a'
})
assert.equal(Object.isFrozen(plan), true)
assert.equal(Object.isFrozen(plan.orderedAllowedTargets), true)
assert.equal(plan.gatewayRequestWallDeadlineAtMs, 271_000)
assert.equal(plan.requestPrecommitDeadlineAtMs, 271_000)
assert.equal(plan.cursor, 0)
const advancedPlan = advanceGatewayRoutePlanCursor(plan)
assert.notEqual(advancedPlan, plan)
assert.equal(plan.cursor, 0, '推进 cursor 不得修改原始 route plan')
assert.equal(advancedPlan.cursor, 1)
assert.throws(() => advanceGatewayRoutePlanCursor(advancedPlan), /cursor/i, 'cursor 不得越过允许目标范围')
const defaultReservePlan = createGatewayRoutePlanSnapshot({
  routePlanId: 'route-plan-default-reserve',
  mode: 'normal',
  requestAcceptedAtMs: 1_000,
  orderedAllowedTargets: [{ groupId: 'group-a' }]
})
assert.equal(defaultReservePlan.finalResponseReserveMs, defaultGatewayFinalResponseReserveMs)

const results: RouteCoordinationResult<string>[] = [
  { outcome: 'dispatchable', accounts: ['account-a'] },
  {
    outcome: 'temporarily_blocked',
    reason: 'capacity_wait',
    blockedAccountIds: ['account-a'],
    confirmationInFlight: false,
    waitableByCurrentRequest: true,
    leaseSource: 'capacity_event',
    foreignLeaseInFlight: false
  },
  { outcome: 'hard_exhausted', reason: 'no_capable_account' },
  { outcome: 'request_exhausted', reason: 'all_candidates_attempted', attempts: attempts.snapshot() },
  {
    outcome: 'client_handoff',
    reason: 'gateway_request_wall_budget_exhausted',
    remainingUntriedCandidatesPossible: true,
    wallRemainingMs: 0,
    serverRetryRemainingMs: 100_000
  }
]
assert.deepEqual(results.map(result => result.outcome), [
  'dispatchable',
  'temporarily_blocked',
  'hard_exhausted',
  'request_exhausted',
  'client_handoff'
])

const fallbackReasons: string[] = []
const completedFailureCodes: string[] = []
const routeOwner: GatewayRouteCoordinatorOwner<{ groupId: string }> = {
  async requestFallback(reason) {
    fallbackReasons.push(reason)
    return { attempted: true, context: { groupId: 'group-b' } }
  },
  async completeFailure(failure) {
    completedFailureCodes.push(failure.errorCode ?? failure.errorType)
  }
}
assert.deepEqual(await routeOwner.requestFallback('group_capacity_busy'), {
  attempted: true,
  context: { groupId: 'group-b' }
})
assert.deepEqual(fallbackReasons, ['group_capacity_busy'], 'fallback 请求必须由 route owner 统一解释')
await routeOwner.completeFailure({
  statusCode: 503,
  message: 'temporary failure',
  errorType: 'service_unavailable',
  errorCode: 'temporarily_blocked',
  errorPhase: 'dispatch'
})
assert.deepEqual(completedFailureCodes, ['temporarily_blocked'], '最终 HTTP 失败也必须由 route owner 统一提交')

const preflightSource = readFileSync(new URL('../../modules/gateway/request/preflight.ts', import.meta.url), 'utf8')
const routesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
const dispatchSource = readFileSync(new URL('../../modules/gateway/dispatch/upstream-dispatch.ts', import.meta.url), 'utf8')
const candidateFilterSource = readFileSync(new URL('../../modules/gateway/dispatch/candidate-filter.ts', import.meta.url), 'utf8')
const fallbackCandidateSource = readFileSync(new URL('../../modules/gateway/dispatch/api-key-group-fallback-candidate.ts', import.meta.url), 'utf8')
const preparationSource = readFileSync(new URL('../../modules/gateway/dispatch/preparation.ts', import.meta.url), 'utf8')
const localSuppressionSource = readFileSync(new URL('../../modules/gateway/runtime/local-suppression-preflight.ts', import.meta.url), 'utf8')
const compactPreflightSource = readFileSync(new URL('../../modules/gateway/codex-responses/compact-preflight.ts', import.meta.url), 'utf8')
const auxiliaryDispatchSource = readFileSync(new URL('../../modules/gateway/hybrid/auxiliary-dispatch.service.ts', import.meta.url), 'utf8')
assert.match(preflightSource, /gatewayRequestWallBudget\?: GatewayRequestWallBudget/, 'preflight 必须显式携带整请求墙钟')
assert.match(preflightSource, /routeCoordinationBudget\?: RouteCoordinationBudget/, 'preflight 必须显式携带路由协调等待预算')
assert.match(preflightSource, /requestAttemptTracker\?: GatewayRequestAttemptTracker/, 'preflight 必须显式携带请求尝试集合')
assert.match(routesSource, /gatewayRequestWallBudget: currentPreflight\.gatewayRequestWallBudget/g, '分组和混合重入必须复用同一墙钟实例')
assert.match(routesSource, /routeCoordinationBudget: currentPreflight\.routeCoordinationBudget/g, '分组和混合重入必须复用同一路由协调预算')
assert.match(routesSource, /requestAttemptTracker: currentPreflight\.requestAttemptTracker/g, '分组和混合重入必须复用同一尝试集合')
assert.match(routesSource, /gateway_request_client_handoff/, '主循环必须在决策点执行墙钟 handoff')
assert.match(dispatchSource, /fetchFirstAvailableUpstream requires shared request coordination context/, '派发器不得为旁路请求静默创建独立预算')
assert.match(dispatchSource, /tryRecordDispatchAttempt/, '实际 HTTP attempt 前必须登记请求去重键')
assert.match(compactPreflightSource, /requestCoordination: GatewayUpstreamRequestCoordinationContext/, 'compact 预检必须接收主请求协调上下文')
assert.match(auxiliaryDispatchSource, /gateway_internal_request_coordination/, '独立 hybrid 辅助请求必须显式记录其独立预算原因')
assert.match(preflightSource, /const routeCoordinator: GatewayRouteCoordinatorOwner<OpenAIGatewayDispatchContext>/, 'preflight 必须为候选过滤和 preparation 建立共享 route owner')
assert.match(preflightSource, /createOpenAIGatewayRoutePlanSnapshot/, 'preflight 必须在真实主路径创建请求级 route plan snapshot')
assert.match(preflightSource, /routePlanSnapshot: candidate\.routePlanSnapshot/, '分组 fallback 预检必须传递推进后的 route plan snapshot')
assert.doesNotMatch(candidateFilterSource, /input\.attemptFallback\(/, 'candidate filter 不得直接解释 fallback callback')
assert.doesNotMatch(preparationSource, /input\.attemptFallback\(/, 'preparation 不得直接解释 fallback callback')
assert.match(candidateFilterSource, /input\.routeCoordinator\.requestFallback\(reason\)/, 'candidate filter 必须通过 route owner 请求 fallback')
assert.match(preparationSource, /input\.routeCoordinator\.requestFallback\(reason\)/, 'preparation 必须通过 route owner 请求 fallback')
assert.doesNotMatch(candidateFilterSource, /sendGatewayFailureResponse/, 'candidate filter 不得直接提交最终 HTTP 响应')
assert.doesNotMatch(preparationSource, /sendGatewayFailureResponse/, 'preparation 不得直接提交最终 HTTP 响应')
assert.doesNotMatch(localSuppressionSource, /sendGatewayFailureResponse/, 'local suppression preflight 不得直接提交最终 HTTP 响应')
assert.match(localSuppressionSource, /input\.routeCoordinator\.completeFailure/, 'local suppression 失败必须交给 route owner 提交')
assert.match(routesSource, /advanceGatewayRoutePlanCursor/, 'routes 必须单向推进 route plan cursor')
assert.doesNotMatch(routesSource, /allowCandidateWrap/, '主路由不得启用候选回绕')
assert.match(fallbackCandidateSource, /routePlanSnapshot/, 'fallback candidate 必须消费不可变 route plan snapshot')

console.log('gateway route coordination regression passed')
