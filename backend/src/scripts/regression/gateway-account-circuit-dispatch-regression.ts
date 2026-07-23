import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import { accountCircuitScopeKey, type AccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-store.js'
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
  confirmationLeaseDurationMs: 30_000
})
assert.equal(rebuiltDecision.outcome, 'blocked', '持久 OPEN 重建后的首次 prepare 不得因 revision 表示差异变成 CLOSED')

const initialAttempts = await Promise.all(Array.from({ length: 20 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
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
const confirmations = transportDecisions.filter((decision) => decision.outcome === 'confirmation_acquired')
assert.equal(confirmations.length, 1, '同 generation 只能有一个 confirmation 租约赢家')

const blockedWhileSuspect = await Promise.all(Array.from({ length: 12 }, () => service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000
})))
assert.equal(blockedWhileSuspect.filter((result) => result.outcome === 'dispatchable').length, 0)
assert.ok(blockedWhileSuspect.every((result) => result.outcome === 'blocked' && result.state.phase === 'SUSPECT'))

const confirmation = confirmations[0]
assert.equal(confirmation?.outcome, 'confirmation_acquired')
if (confirmation?.outcome !== 'confirmation_acquired') throw new Error('缺少 confirmation 租约')
const confirmationAttempt = await service.prepareAttempt({
  account,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmation: confirmation.confirmation
})
assert.equal(confirmationAttempt.outcome, 'dispatchable')
if (confirmationAttempt.outcome !== 'dispatchable') throw new Error('confirmation 租约无法派发')
assert.equal(confirmationAttempt.attempt.isConfirmation, true)
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
assert.equal(recoveryDecision.outcome, 'confirmation_acquired')
if (recoveryDecision.outcome !== 'confirmation_acquired') throw new Error('未取得 recovery confirmation')
const recoveryConfirmation = await service.prepareAttempt({
  account: recoveryAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmation: recoveryDecision.confirmation
})
assert.equal(recoveryConfirmation.outcome, 'dispatchable')
if (recoveryConfirmation.outcome !== 'dispatchable') throw new Error('recovery confirmation 不可派发')
await recoveryConfirmation.attempt.reportFramingComplete()
const recoveryState = await store.get(gatewayAccountProtocolModelScope(recoveryAccount, 'text', 'gpt-5.4'))
assert.equal(recoveryState.phase, 'RECOVERING', '首次 confirmation 完整成功必须进入 RECOVERING')
assert.equal(recoveryState.recoverySuccessCount, 0, 'confirmation 成功不得计入恢复 canary')

const badSessionEvidence = 'a'.repeat(64)
const healthySessionEvidence = 'b'.repeat(64)
const evidenceStore = new MemoryAccountCircuitStore({ capacity: 32, now: () => now })
const evidenceService = new GatewayAccountCircuitService(evidenceStore, {
  now: () => now,
  createId: () => `evidence-${++id}`
})
const evidenceAccount = accountFixture({ id: 'account-request-evidence' })
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
assert.equal(evidenceDecision.outcome, 'confirmation_acquired')
if (evidenceDecision.outcome !== 'confirmation_acquired') throw new Error('坏会话首次失败未取得 confirmation')
const sameEvidenceExplicitConfirmation = await evidenceService.prepareAttempt({
  account: evidenceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  confirmation: evidenceDecision.confirmation,
  failureEvidenceKey: badSessionEvidence
})
assert.equal(sameEvidenceExplicitConfirmation.outcome, 'blocked', '同一坏会话不得使用首错 confirmation 自证账户死亡')
await evidenceService.completeConfirmation(evidenceDecision.confirmation, 'unknown')
const sameEvidenceStorm = await Promise.all(Array.from({ length: 24 }, () => evidenceService.prepareAttempt({
  account: evidenceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: badSessionEvidence
})))
assert.equal(sameEvidenceStorm.filter((result) => result.outcome === 'dispatchable').length, 0, '同一坏会话并发不得取得 confirmation')
assert.ok(sameEvidenceStorm.every((result) => result.outcome === 'blocked' && result.state.phase === 'SUSPECT'))
const independentEvidenceAttempts = await Promise.all(Array.from({ length: 12 }, () => evidenceService.prepareAttempt({
  account: evidenceAccount,
  requestLane: 'text',
  model: 'gpt-5.4',
  confirmationLeaseDurationMs: 30_000,
  failureEvidenceKey: healthySessionEvidence
})))
const independentEvidenceWinners = independentEvidenceAttempts.filter((result) => result.outcome === 'dispatchable')
assert.equal(independentEvidenceWinners.length, 1, '独立健康会话并发只允许一个 confirmation 赢家')
const independentEvidenceWinner = independentEvidenceWinners[0]
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
assert.equal(confirmedDeadDecision.outcome, 'confirmation_acquired')
if (confirmedDeadDecision.outcome !== 'confirmation_acquired') throw new Error('真实死亡账户首次失败未取得 confirmation')
await evidenceService.completeConfirmation(confirmedDeadDecision.confirmation, 'unknown')
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
assert.equal((await evidenceStore.get(gatewayAccountProtocolModelScope(confirmedDeadAccount, 'text', 'gpt-5.4'))).phase, 'OPEN', '两个独立会话均传输失败应确认账户电路 OPEN')

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
  assert.equal(decision.outcome, 'confirmation_acquired')
  if (decision.outcome !== 'confirmation_acquired') throw new Error('风暴账户首次失败未取得 confirmation')
  await evidenceService.completeConfirmation(decision.confirmation, 'unknown')
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
  await recovery.attempt.reportFramingComplete()
  assert.equal((await evidenceStore.get(gatewayAccountProtocolModelScope(stormAccount, 'text', 'gpt-5.4'))).phase, 'RECOVERING')
}

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
    confirmationLeaseDurationMs: 30_000
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
  assert.equal(decision.outcome, 'confirmation_acquired')
  if (decision.outcome !== 'confirmation_acquired') throw new Error(`${target.model} 未取得 confirmation`)
  return decision.confirmation
}))
const escalationConfirmationAttempts = await Promise.all(escalationDecisions.map(async (confirmation, index) => {
  const target = escalationTargets[index]!
  const confirmationResult = await escalationService.prepareAttempt({
    account: escalationAccount,
    requestLane: target.requestLane,
    model: target.model,
    confirmationLeaseDurationMs: 30_000,
    confirmation
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
