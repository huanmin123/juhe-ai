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
