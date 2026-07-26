import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  AccountCircuitControlPlaneBridge,
  publicAccountCircuitSummary,
  type AccountCircuitControlPlaneBridgeOptions
} from '../../modules/gateway/runtime/account-circuit-control-plane-bridge.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import { GatewayAccountCircuitService } from '../../modules/gateway/runtime/account-circuit.service.js'
import {
  accountCircuitScopeKey,
  type AccountCircuitScope,
  type AccountCircuitState
} from '../../modules/gateway/runtime/account-circuit-store.js'
import type {
  AccountCircuitIncidentRecord,
  AccountCircuitOutboxRecord
} from '../../storage/account-circuit-control-plane.repository.js'

const bridgeSource = readFileSync(
  new URL('../../modules/gateway/runtime/account-circuit-control-plane-bridge.ts', import.meta.url),
  'utf8'
)
assert.match(
  bridgeSource,
  /import \{ requestGatewayDbService \} from '\.\/gateway-db-service-request\.js'/,
  'control-plane bridge 必须通过角色感知封装访问 DB service'
)
assert.doesNotMatch(
  bridgeSource,
  /from '\.\.\/\.\.\/db-service\/db-service-ipc\.js'/,
  'control-plane bridge 不得绕过 worker IPC 直接依赖本地 DB service'
)

const scope = {
  kind: 'protocol_model' as const,
  accountRuntimeKey: 'account-1',
  protocolProfile: 'openai-v1',
  requestLane: 'text' as const,
  modelBucket: 'unknown'
}
const confirmationEvidenceA = 'a'.repeat(64)
const confirmationEvidenceB = 'b'.repeat(64)
const confirmationEvidenceC = 'c'.repeat(64)
const store = new MemoryAccountCircuitStore({ capacity: 8, now: () => 10_000 })
const open: AccountCircuitState = {
  scope,
  scopeKey: accountCircuitScopeKey(scope),
  phase: 'OPEN',
  generation: 2,
  dispatchRevision: '7',
  transitionId: 'transition-open',
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  openedAtMs: 9_000,
  retryAtMs: 15_000,
  updatedAtMs: 9_000
}

assert.equal((await store.restore(open, 10_000)).status, 'applied')
assert.equal((await store.get(scope, 10_001)).phase, 'OPEN', '重启恢复后活动电路不能按缺失 key 解释成 CLOSED')
assert.equal((await store.restore({ ...open, generation: 1, updatedAtMs: 8_000 }, 10_002)).status, 'idempotent')
assert.equal((await store.get(scope, 10_003)).generation, 2, '延迟 projector 不得覆盖更新 generation')

const baseIncident: AccountCircuitIncidentRecord = {
  circuitScopeKey: open.scopeKey,
  accountId: 'account-1',
  accountRuntimeKey: 'account-1',
  scopeKind: 'protocol_model',
  protocolCode: 'openai-v1',
  requestLane: 'text',
  modelFamily: 'unknown',
  incidentId: 'incident-1',
  childIncidentIds: [],
  state: 'OPEN',
  generation: 2,
  dispatchRevision: 7,
  ledgerRevision: 2,
  projectedLedgerRevision: 1,
  transitionId: 'transition-open',
  cooldownObservationGeneration: 0,
  nextTransitionAtMs: 15_000,
  upstreamAttemptObserved: true,
  backoffLevel: 1,
  consecutiveFailures: 1,
  confirmationFailuresRequired: 1,
  confirmationFailureEvidenceKeys: [],
  recoveringSuccesses: 0,
  lastFailureClass: 'timeout_before_complete',
  createdAtMs: 8_000,
  updatedAtMs: 9_000
}

const expiredClosedIncident: AccountCircuitIncidentRecord = {
  ...baseIncident,
  state: 'CLOSED',
  transitionId: 'expired-closed-transition',
  ledgerRevision: 4,
  projectedLedgerRevision: 3,
  consecutiveFailures: 0,
  confirmationFailureEvidenceKeys: [],
  retainedUntilMs: 10_000,
  updatedAtMs: 9_500
}
const expiredClosedOutbox: AccountCircuitOutboxRecord = {
  eventId: 'expired-closed-event',
  projectionKey: 'account_circuit_runtime_v1',
  dedupeKey: 'incident:expired-closed-transition',
  eventType: 'incident_changed',
  accountId: expiredClosedIncident.accountId,
  accountRuntimeKey: expiredClosedIncident.accountRuntimeKey,
  circuitScopeKey: expiredClosedIncident.circuitScopeKey,
  incidentId: expiredClosedIncident.incidentId,
  transitionId: expiredClosedIncident.transitionId,
  dispatchRevision: expiredClosedIncident.dispatchRevision,
  generation: expiredClosedIncident.generation,
  ledgerRevision: expiredClosedIncident.ledgerRevision,
  status: 'processing',
  availableAtMs: 9_500,
  claimToken: 'expired-closed-claim',
  claimedBy: 'expired-closed-projector',
  claimUntilMs: 50_000,
  attemptCount: 1,
  createdAtMs: 9_500,
  updatedAtMs: 20_000
}
const projectionOperations: string[] = []
let projectionClaimed = false
let projectionReleased = false
const projectionStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 20_000 })
const projectionRequestDb = (async (operation: { type: string }) => {
  projectionOperations.push(operation.type)
  if (operation.type === 'claim_account_circuit_outbox') {
    if (projectionClaimed) return []
    projectionClaimed = true
    return [expiredClosedOutbox]
  }
  if (operation.type === 'get_account_circuit_incident_by_scope_key') return expiredClosedIncident
  if (operation.type === 'ack_account_circuit_outbox') return { acknowledged: true }
  if (operation.type === 'release_account_circuit_outbox_for_replay') {
    projectionReleased = true
    return { released: true }
  }
  throw new Error(`未预期的 DB service 操作：${operation.type}`)
}) as unknown as NonNullable<AccountCircuitControlPlaneBridgeOptions['requestDb']>
const projectionBridge = new AccountCircuitControlPlaneBridge({
  store: projectionStore,
  ownerId: 'expired-closed-projector',
  now: () => 20_000,
  requestDb: projectionRequestDb
})
assert.equal(await projectionBridge.projectPending(1), 1, '过期 CLOSED outbox 必须完成投影和 ACK')
assert.deepEqual(projectionOperations, [
  'claim_account_circuit_outbox',
  'get_account_circuit_incident_by_scope_key',
  'ack_account_circuit_outbox'
], '单条 incident outbox 只能按 scope 直查，不得扫描 active incident 全集')
assert.equal(projectionReleased, false, '可直查的过期 CLOSED ledger 不得进入永久 replay')
assert.equal(
  (await projectionStore.get(scope, 20_000)).transitionId,
  expiredClosedIncident.transitionId,
  '过期 CLOSED tombstone 必须先投影到 runtime 再 ACK'
)

const partialSuspect: AccountCircuitState = {
  scope,
  scopeKey: accountCircuitScopeKey(scope),
  phase: 'SUSPECT',
  generation: 3,
  dispatchRevision: '7',
  transitionId: 'partial-confirmation-before-restart',
  backoffAttempt: 0,
  recoverySuccessCount: 0,
  confirmationFailuresRequired: 2,
  confirmationFailureCount: 1,
  failureEvidenceKeys: [confirmationEvidenceA, confirmationEvidenceB],
  retryAtMs: 13_000,
  updatedAtMs: 10_000
}

let persistedPartial: Parameters<NonNullable<ConstructorParameters<typeof AccountCircuitControlPlaneBridge>[0]['persistIncident']>>[0] | undefined
const persistBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    persistedPartial = input
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await persistBridge.rebuild()).blocked, false)
persistBridge.observe({ scope, state: partialSuspect })
await waitUntil(() => persistedPartial !== undefined, '部分确认状态必须进入 durable persistence')
assert.equal(persistedPartial?.confirmationFailuresRequired, 2)
assert.equal(persistedPartial?.consecutiveFailures, 1)
assert.deepEqual(persistedPartial?.confirmationFailureEvidenceKeys, [confirmationEvidenceA, confirmationEvidenceB])
assert.equal(persistedPartial?.nextTransitionAtMs, 13_000, 'SUSPECT 后台确认时间必须持久化到 control-plane')

const realEscalationStore = new MemoryAccountCircuitStore({ capacity: 16, now: () => 30_000 })
type PersistIncidentInput = Parameters<NonNullable<ConstructorParameters<typeof AccountCircuitControlPlaneBridge>[0]['persistIncident']>>[0]
const realEscalationPersisted = new Map<string, PersistIncidentInput>()
const realEscalationHistory: PersistIncidentInput[] = []
const realEscalationBridge = new AccountCircuitControlPlaneBridge({
  store: realEscalationStore,
  now: () => 30_000,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    realEscalationHistory.push(input)
    realEscalationPersisted.set(input.circuitScopeKey, input)
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await realEscalationBridge.rebuild()).blocked, false)
let realEscalationId = 0
const realEscalationService = new GatewayAccountCircuitService(realEscalationStore, {
  now: () => 30_000,
  createId: () => `real-escalation-${++realEscalationId}`,
  escalationDistinctScopeThreshold: 3,
  escalationWindowMs: 60_000,
  onMutation: (input) => realEscalationBridge.observe(input)
})
const realEscalationScopes: Array<Extract<AccountCircuitScope, { kind: 'protocol_model' }>> = ['a', 'b', 'c'].map((modelBucket) => ({
  kind: 'protocol_model',
  accountRuntimeKey: 'real-escalation-account',
  protocolProfile: 'openai-v1',
  requestLane: 'text',
  modelBucket
}))
async function acquireDueRealEscalationConfirmation(
  childScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>,
  initialEvidenceKey: string,
  confirmationEvidenceKey: string,
  suffix: string
) {
  const suspect = await realEscalationStore.suspect({
    scope: childScope,
    dispatchRevision: '1',
    transitionId: `real-escalation-suspect-${suffix}`,
    reason: `real-escalation-initial-${suffix}`,
    confirmationFailuresRequired: 1,
    failureEvidenceKey: initialEvidenceKey,
    nowMs: 27_000
  })
  assert.equal(suspect.status, 'applied')
  const leaseId = `real-escalation-lease-${suffix}`
  const acquired = await realEscalationStore.acquireConfirmationLease({
    scope: childScope,
    generation: suspect.state.generation,
    dispatchRevision: suspect.state.dispatchRevision,
    transitionId: `real-escalation-acquire-${suffix}`,
    leaseId,
    leaseUntilMs: 35_000,
    expectedFailureEvidenceKey: initialEvidenceKey,
    confirmationEvidenceKey,
    nowMs: 30_000
  })
  assert.equal(acquired.status, 'applied', '测试夹具必须在 SUSPECT 到期后取得真实 confirmation 租约')
  return {
    scope: { ...childScope },
    scopeKey: accountCircuitScopeKey(childScope),
    accountRuntimeKey: childScope.accountRuntimeKey,
    generation: acquired.state.generation,
    dispatchRevision: acquired.state.dispatchRevision,
    leaseId
  }
}
for (const [index, childScope] of realEscalationScopes.entries()) {
  const confirmationEvidenceKey = String(index + 11).repeat(64).slice(0, 64)
  const confirmation = await acquireDueRealEscalationConfirmation(
    childScope,
    'a'.repeat(64),
    confirmationEvidenceKey,
    String(index)
  )
  await realEscalationService.completeConfirmation(
    confirmation,
    'transport_failure',
    `real-escalation-confirmed-${index}`,
    confirmationEvidenceKey
  )
  if (index < 2) {
    await waitUntil(
      () => realEscalationPersisted.get(accountCircuitScopeKey(childScope))?.state === 'OPEN',
      `真实升级前第 ${index + 1} 个子 incident 必须先独立落盘`
    )
  }
}
const realEscalationParentScope = {
  kind: 'account' as const,
  accountRuntimeKey: 'real-escalation-account'
}
const realEscalationParentScopeKey = accountCircuitScopeKey(realEscalationParentScope)
await waitUntil(
  () => realEscalationPersisted.get(realEscalationParentScopeKey)?.state === 'OPEN',
  '真实升级产生的父 incident 必须落盘'
)
const realEscalationParent = realEscalationPersisted.get(realEscalationParentScopeKey)
assert.equal(realEscalationParent?.childIncidentIds?.length, 3, '真实升级父 incident 必须持久化全部 child incident ID')
await waitUntil(
  () => realEscalationScopes.every((childScope) => (
    realEscalationPersisted.get(accountCircuitScopeKey(childScope))?.parentIncidentId === realEscalationParent?.incidentId
  )),
  '真实升级后的全部子 incident 必须完成父关系持久化'
)
for (const childScope of realEscalationScopes) {
  assert.equal(
    realEscalationPersisted.get(accountCircuitScopeKey(childScope))?.parentIncidentId,
    realEscalationParent?.incidentId,
    '真实升级后每个子 incident 必须持久化父 incident ID，不能只在 runtime 内存中 shadow'
  )
}
for (const [index, childScope] of realEscalationScopes.entries()) {
  const childHistory = realEscalationHistory.filter((input) => input.circuitScopeKey === accountCircuitScopeKey(childScope))
  if (index < 2) {
    assert.ok(childHistory.some((input) => input.parentIncidentId === undefined), '前两个子 incident 应先有独立 OPEN 持久化事实')
  }
  assert.match(childHistory.at(-1)?.transitionId ?? '', /^hierarchy:shadow:[a-f0-9]{40}$/)
}
const realEscalationParentTransitionBeforeIncrement = realEscalationParent?.transitionId
const incrementalChildScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
  ...realEscalationScopes[0]!,
  modelBucket: 'd'
}
const incrementalConfirmation = await acquireDueRealEscalationConfirmation(
  incrementalChildScope,
  'a'.repeat(64),
  'e'.repeat(64),
  'incremental'
)
await realEscalationService.completeConfirmation(
  incrementalConfirmation,
  'transport_failure',
  'real-escalation-incremental-confirmed',
  'e'.repeat(64)
)
await waitUntil(
  () => realEscalationPersisted.get(realEscalationParentScopeKey)?.childIncidentIds?.length === 4,
  '父 incident 已 OPEN 后的新子 scope 必须增量持久化'
)
const incrementedParent = realEscalationPersisted.get(realEscalationParentScopeKey)
assert.notEqual(
  incrementedParent?.transitionId,
  realEscalationParentTransitionBeforeIncrement,
  'already_active 扩展 child 集时必须使用新 transitionId，不能被旧 outbox dedupe 吞掉'
)
await waitUntil(
  () => realEscalationPersisted.get(accountCircuitScopeKey(incrementalChildScope))?.parentIncidentId === incrementedParent?.incidentId,
  'already_active 新子 incident 必须持久化父关系'
)

const hierarchyParentScope = { kind: 'account' as const, accountRuntimeKey: scope.accountRuntimeKey }
const hierarchyChildScopes = [
  { ...scope, modelBucket: 'hierarchy-a' },
  { ...scope, modelBucket: 'hierarchy-b' }
]
const hierarchyParentState: AccountCircuitState = {
  scope: hierarchyParentScope,
  scopeKey: accountCircuitScopeKey(hierarchyParentScope),
  phase: 'OPEN',
  generation: 4,
  dispatchRevision: '7',
  transitionId: 'hierarchy-parent-open',
  incidentId: 'hierarchy-parent-incident',
  childIncidentIds: ['hierarchy-child-incident-a', 'hierarchy-child-incident-b'],
  childScopeKeys: hierarchyChildScopes.map(accountCircuitScopeKey),
  requiredRecoveryScopeKeys: hierarchyChildScopes.map(accountCircuitScopeKey),
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  retryAtMs: 15_000,
  updatedAtMs: 9_000
}
let persistedHierarchy: Parameters<NonNullable<ConstructorParameters<typeof AccountCircuitControlPlaneBridge>[0]['persistIncident']>>[0] | undefined
const hierarchyPersistBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    persistedHierarchy = input
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await hierarchyPersistBridge.rebuild()).blocked, false)
hierarchyPersistBridge.observe({ scope: hierarchyParentScope, state: hierarchyParentState })
await waitUntil(() => persistedHierarchy !== undefined, '父级 incident 必须进入 durable persistence')
assert.equal(persistedHierarchy?.incidentId, hierarchyParentState.incidentId, 'ledger 必须保留稳定 runtime incidentId，不能另造 scope/generation ID')
assert.deepEqual(persistedHierarchy?.childIncidentIds, hierarchyParentState.childIncidentIds)

let hierarchyNow = 20_000
let hierarchyRebuildPage = 0
const hierarchyChildIncidents: AccountCircuitIncidentRecord[] = hierarchyChildScopes.map((childScope, index) => ({
  ...baseIncident,
  circuitScopeKey: accountCircuitScopeKey(childScope),
  modelFamily: childScope.modelBucket,
  incidentId: hierarchyParentState.childIncidentIds![index]!,
  childIncidentIds: [],
  generation: 4,
  transitionId: `hierarchy-child-open-${index}`,
  updatedAtMs: 8_000 + index
}))
const hierarchyParentIncident: AccountCircuitIncidentRecord = {
  ...baseIncident,
  circuitScopeKey: hierarchyParentState.scopeKey,
  scopeKind: 'account',
  incidentId: hierarchyParentState.incidentId!,
  childIncidentIds: [...hierarchyParentState.childIncidentIds!],
  generation: hierarchyParentState.generation,
  transitionId: hierarchyParentState.transitionId,
  updatedAtMs: hierarchyParentState.updatedAtMs
}
const hierarchyRebuildStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => hierarchyNow })
const hierarchyRelationshipPersistence = new Map<string, PersistIncidentInput>()
const hierarchyRebuildBridge = new AccountCircuitControlPlaneBridge({
  store: hierarchyRebuildStore,
  now: () => hierarchyNow,
  loadRebuildPage: async () => {
    hierarchyRebuildPage++
    return hierarchyRebuildPage === 1
      ? {
          items: [hierarchyParentIncident],
          nextCursor: { updatedAtMs: hierarchyParentIncident.updatedAtMs, circuitScopeKey: hierarchyParentIncident.circuitScopeKey }
        }
      : { items: hierarchyChildIncidents, nextCursor: undefined }
  },
  persistIncident: async (input) => {
    hierarchyRelationshipPersistence.set(input.circuitScopeKey, input)
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.deepEqual(await hierarchyRebuildBridge.rebuild(), { loaded: 3, blocked: false })
const rebuiltParent = await hierarchyRebuildStore.get(hierarchyParentScope, hierarchyNow)
assert.deepEqual(rebuiltParent.childIncidentIds, hierarchyParentState.childIncidentIds)
assert.deepEqual(rebuiltParent.childScopeKeys, hierarchyParentState.childScopeKeys, '跨分页冷启动必须由 child incidentId 重建父级 scope 关系')
for (const childScope of hierarchyChildScopes) {
  assert.equal(
    (await hierarchyRebuildStore.get(childScope, hierarchyNow)).shadowedByIncidentId,
    hierarchyParentState.incidentId,
    '父 ledger 已落盘但子 parentIncidentId 尚未落盘时，冷重建必须从父 childIds 修复缺失 shadow'
  )
}
await waitUntil(
  () => hierarchyChildScopes.every((childScope) => (
    hierarchyRelationshipPersistence.get(accountCircuitScopeKey(childScope))?.parentIncidentId === hierarchyParentState.incidentId
  )),
  '冷重建派生的 child parentId 必须继续持久化，不能只修 runtime'
)
const hierarchyDurableOpenChildren = hierarchyChildIncidents.map((incident) => {
  const persisted = hierarchyRelationshipPersistence.get(incident.circuitScopeKey)
  assert.ok(persisted)
  return {
    ...incident,
    ...(persisted.parentIncidentId ? { parentIncidentId: persisted.parentIncidentId } : {}),
    transitionId: persisted.transitionId,
    updatedAtMs: persisted.stateUpdatedAtMs ?? incident.updatedAtMs,
    ledgerRevision: incident.ledgerRevision + 1
  }
})
const hierarchySecondOpenStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => hierarchyNow })
const hierarchySecondOpenBridge = new AccountCircuitControlPlaneBridge({
  store: hierarchySecondOpenStore,
  now: () => hierarchyNow,
  loadRebuildPage: async () => ({ items: hierarchyDurableOpenChildren, nextCursor: undefined }),
  persistIncident: async (input) => ({ status: 'idempotent', currentDispatchRevision: input.dispatchRevision })
})
assert.equal((await hierarchySecondOpenBridge.rebuild()).blocked, false)
for (const childScope of hierarchyChildScopes) {
  assert.equal(
    (await hierarchySecondOpenStore.get(childScope, hierarchyNow)).shadowedByIncidentId,
    hierarchyParentState.incidentId,
    '父 ledger 后续缺失时，第二次冷启动也必须保留已持久化 shadow'
  )
}
const relationshipRepairScope: Extract<AccountCircuitScope, { kind: 'protocol_model' }> = {
  ...scope,
  accountRuntimeKey: 'relationship-repair-account',
  modelBucket: 'repair-child'
}
const relationshipRepairParentScope = {
  kind: 'account' as const,
  accountRuntimeKey: relationshipRepairScope.accountRuntimeKey
}
const relationshipRepairStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 40_000 })
const relationshipRepairChild: AccountCircuitState = {
  scope: relationshipRepairScope,
  scopeKey: accountCircuitScopeKey(relationshipRepairScope),
  phase: 'OPEN',
  generation: 5,
  dispatchRevision: '7',
  transitionId: 'relationship-repair-child-open',
  incidentId: 'relationship-repair-child-incident',
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  retryAtMs: 50_000,
  updatedAtMs: 35_000
}
assert.equal((await relationshipRepairStore.restore(relationshipRepairChild, 40_000)).status, 'applied')
const relationshipRepairParent: AccountCircuitState = {
  scope: relationshipRepairParentScope,
  scopeKey: accountCircuitScopeKey(relationshipRepairParentScope),
  phase: 'OPEN',
  generation: 1,
  dispatchRevision: '7',
  transitionId: 'relationship-repair-parent-open',
  incidentId: 'relationship-repair-parent-incident',
  childScopeKeys: [relationshipRepairChild.scopeKey],
  childIncidentIds: [relationshipRepairChild.incidentId!],
  requiredRecoveryScopeKeys: [relationshipRepairChild.scopeKey],
  backoffAttempt: 1,
  recoverySuccessCount: 0,
  retryAtMs: 50_000,
  updatedAtMs: 36_000
}
assert.equal((await relationshipRepairStore.restore(relationshipRepairParent, 40_000)).status, 'applied')
const repairedChild = await relationshipRepairStore.get(relationshipRepairScope, 40_000)
assert.equal(repairedChild.shadowedByIncidentId, relationshipRepairParent.incidentId)
assert.match(repairedChild.transitionId, /^hierarchy:shadow:[a-f0-9]{40}$/, '冷重建派生 shadow 必须产生可持久化的独立 transition')
assert.equal(repairedChild.updatedAtMs, relationshipRepairParent.updatedAtMs)
const relationshipRepairReplay = await relationshipRepairStore.restore(relationshipRepairParent, 40_000)
assert.equal(relationshipRepairReplay.status, 'idempotent')
assert.equal(relationshipRepairReplay.relatedStates, undefined, '相同父关系重复重建不得重复生成子 transition')
assert.equal(
  (await relationshipRepairStore.get(relationshipRepairScope, 40_000)).shadowedByIncidentId,
  relationshipRepairParent.incidentId,
  '父 incident 幂等重放仍应修复缺失关系'
)
const relationshipClosedParent: AccountCircuitState = {
  ...relationshipRepairParent,
  phase: 'CLOSED',
  transitionId: 'relationship-repair-parent-closed',
  backoffAttempt: 0,
  retryAtMs: undefined,
  updatedAtMs: 37_000
}
assert.equal((await relationshipRepairStore.restore(relationshipClosedParent, 40_000)).status, 'applied')
const unshadowedAfterCrash = await relationshipRepairStore.get(relationshipRepairScope, 40_000)
assert.equal(unshadowedAfterCrash.shadowedByIncidentId, undefined, '父 CLOSED tombstone 必须修复父先落盘的 unshadow 崩溃窗口')
assert.match(unshadowedAfterCrash.transitionId, /^hierarchy:unshadow:[a-f0-9]{40}$/)

const staleRelationshipStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 40_000 })
const newerIndependentChild: AccountCircuitState = {
  ...relationshipRepairChild,
  generation: 6,
  dispatchRevision: '8',
  transitionId: 'relationship-newer-child-open',
  incidentId: 'relationship-newer-child-incident',
  shadowedByIncidentId: 'relationship-newer-parent-incident',
  updatedAtMs: 39_000
}
assert.equal((await staleRelationshipStore.restore(newerIndependentChild, 40_000)).status, 'applied')
assert.equal((await staleRelationshipStore.restore(relationshipRepairParent, 40_000)).status, 'applied')
const childAfterStaleParentReplay = await staleRelationshipStore.get(relationshipRepairScope, 40_000)
assert.equal(childAfterStaleParentReplay.generation, newerIndependentChild.generation)
assert.equal(childAfterStaleParentReplay.dispatchRevision, newerIndependentChild.dispatchRevision)
assert.equal(childAfterStaleParentReplay.incidentId, newerIndependentChild.incidentId)
assert.equal(childAfterStaleParentReplay.transitionId, newerIndependentChild.transitionId)
assert.equal(
  childAfterStaleParentReplay.shadowedByIncidentId,
  newerIndependentChild.shadowedByIncidentId,
  '旧父记录不得覆盖更新 generation/revision/incident 的独立子状态'
)
const differentParentStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 40_000 })
const childOwnedByDifferentParent: AccountCircuitState = {
  ...relationshipRepairChild,
  shadowedByIncidentId: 'relationship-different-parent-incident'
}
assert.equal((await differentParentStore.restore(childOwnedByDifferentParent, 40_000)).status, 'applied')
assert.equal((await differentParentStore.restore(relationshipRepairParent, 40_000)).status, 'applied')
assert.equal(
  (await differentParentStore.get(relationshipRepairScope, 40_000)).shadowedByIncidentId,
  childOwnedByDifferentParent.shadowedByIncidentId,
  '父关系重建只能填补缺失字段，不得覆盖同 revision/incident 的不同 parent'
)
const hierarchyIdentity = {
  scope: hierarchyParentScope,
  generation: hierarchyParentState.generation,
  dispatchRevision: hierarchyParentState.dispatchRevision
}
assert.equal((await hierarchyRebuildStore.acquireCanaryLease({
  ...hierarchyIdentity,
  transitionId: 'hierarchy-rebuilt-half-open',
  leaseId: 'hierarchy-rebuilt-half-open-lease',
  leaseUntilMs: hierarchyNow + 1_000,
  nowMs: hierarchyNow
})).status, 'applied')
await hierarchyRebuildStore.completeCanary({
  ...hierarchyIdentity,
  transitionId: 'hierarchy-rebuilt-half-open-success',
  leaseId: 'hierarchy-rebuilt-half-open-lease',
  outcome: 'framing_complete',
  nowMs: hierarchyNow
})
let hierarchyClosedResult: Awaited<ReturnType<typeof hierarchyRebuildStore.completeCanary>> | undefined
for (const index of [0, 1, 2]) {
  hierarchyNow += 3_000
  const leaseId = `hierarchy-rebuilt-recovery-${index}`
  assert.equal((await hierarchyRebuildStore.acquireCanaryLease({
    ...hierarchyIdentity,
    transitionId: `${leaseId}-acquire`,
    leaseId,
    leaseUntilMs: hierarchyNow + 1_000,
    nowMs: hierarchyNow
  })).status, 'applied')
  const completed = await hierarchyRebuildStore.completeCanary({
    ...hierarchyIdentity,
    transitionId: `${leaseId}-complete`,
    leaseId,
    outcome: 'framing_complete',
    nowMs: hierarchyNow
  })
  if (index === 2) hierarchyClosedResult = completed
}
assert.equal(hierarchyClosedResult?.state.phase, 'CLOSED')
for (const childState of hierarchyClosedResult?.relatedStates ?? []) {
  hierarchyRebuildBridge.observe({ scope: childState.scope, state: childState })
}
if (hierarchyClosedResult) {
  hierarchyRebuildBridge.observe({ scope: hierarchyClosedResult.state.scope, state: hierarchyClosedResult.state })
}
await waitUntil(
  () => hierarchyChildScopes.every((childScope) => (
    hierarchyRelationshipPersistence.get(accountCircuitScopeKey(childScope))?.parentIncidentId === undefined
  )),
  '父 CLOSED 派生的 child unshadow 必须持久化'
)
assert.equal((await hierarchyRebuildStore.get(hierarchyParentScope, hierarchyNow)).phase, 'CLOSED')
for (const childScope of hierarchyChildScopes) {
  const childState = await hierarchyRebuildStore.get(childScope, hierarchyNow)
  assert.equal(childState.shadowedByIncidentId, undefined, '冷启动后父级关闭必须解除自身 shadow')
  assert.equal(childState.phase, 'OPEN', '父级关闭不得隐式关闭子 circuit')
}
const hierarchyDurableClosedChildren = hierarchyChildIncidents.map((incident) => {
  const persisted = hierarchyRelationshipPersistence.get(incident.circuitScopeKey)
  assert.ok(persisted)
  return {
    ...incident,
    transitionId: persisted.transitionId,
    updatedAtMs: persisted.stateUpdatedAtMs ?? incident.updatedAtMs,
    ledgerRevision: incident.ledgerRevision + 2
  }
})
const hierarchySecondClosedStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => hierarchyNow })
const hierarchySecondClosedBridge = new AccountCircuitControlPlaneBridge({
  store: hierarchySecondClosedStore,
  now: () => hierarchyNow,
  loadRebuildPage: async () => ({ items: hierarchyDurableClosedChildren, nextCursor: undefined }),
  persistIncident: async (input) => ({ status: 'idempotent', currentDispatchRevision: input.dispatchRevision })
})
assert.equal((await hierarchySecondClosedBridge.rebuild()).blocked, false)
for (const childScope of hierarchyChildScopes) {
  assert.equal(
    (await hierarchySecondClosedStore.get(childScope, hierarchyNow)).shadowedByIncidentId,
    undefined,
    '父 CLOSED tombstone 已清理后，第二次冷启动不能从 durable child 复活旧 parentId'
  )
}

const rebuildStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 11_000 })
const rebuildBridge = new AccountCircuitControlPlaneBridge({
  store: rebuildStore,
  loadRebuildPage: async () => ({
    items: [{
      ...baseIncident,
      state: 'SUSPECT',
      generation: partialSuspect.generation,
      transitionId: partialSuspect.transitionId,
      nextTransitionAtMs: undefined,
      backoffLevel: 0,
      consecutiveFailures: 1,
      confirmationFailuresRequired: 2,
      confirmationFailureEvidenceKeys: [confirmationEvidenceA, confirmationEvidenceB],
      updatedAtMs: partialSuspect.updatedAtMs
    }],
    nextCursor: undefined
  })
})
assert.equal((await rebuildBridge.rebuild()).blocked, false)
const rebuiltIdentity = { scope, generation: 3, dispatchRevision: '7' }
assert.equal((await rebuildStore.get(scope, 11_000)).confirmationFailureCount, 1)
assert.equal((await rebuildStore.listDue(11_000, 10))[0]?.scopeKey, partialSuspect.scopeKey, '旧 ledger 缺少 nextTransitionAtMs 时也必须用 updatedAt 恢复低流量 SUSPECT due 队列')
assert.equal((await rebuildStore.acquireConfirmationLease({
  ...rebuiltIdentity,
  transitionId: 'duplicate-evidence-acquire',
  leaseId: 'duplicate-evidence-lease',
  leaseUntilMs: 12_000,
  expectedFailureEvidenceKey: confirmationEvidenceB,
  confirmationEvidenceKey: confirmationEvidenceB,
  nowMs: 11_000
})).status, 'state_mismatch', '重启后重复 evidence 必须在取得租约前被阻断')
assert.equal((await rebuildStore.acquireConfirmationLease({
  ...rebuiltIdentity,
  transitionId: 'independent-evidence-acquire',
  leaseId: 'independent-evidence-lease',
  leaseUntilMs: 12_100,
  expectedFailureEvidenceKey: confirmationEvidenceB,
  confirmationEvidenceKey: confirmationEvidenceC,
  nowMs: 11_100
})).status, 'applied')
const independentResult = await rebuildStore.completeConfirmation({
  ...rebuiltIdentity,
  transitionId: 'independent-evidence-complete',
  leaseId: 'independent-evidence-lease',
  outcome: 'transport_failure',
  failureEvidenceKey: confirmationEvidenceC,
  nowMs: 11_101
})
assert.equal(independentResult.state.phase, 'OPEN', '重建后第二个独立 confirmation 失败必须继续阈值并 OPEN')
assert.equal(independentResult.state.confirmationFailureCount, 2)
assert.deepEqual(independentResult.state.failureEvidenceKeys, [confirmationEvidenceA, confirmationEvidenceB, confirmationEvidenceC])

const recoveryLeaseScope = { ...scope, modelBucket: 'recovery-lease-after-restart' }
const recoveryLeaseIncident: AccountCircuitIncidentRecord = {
  ...baseIncident,
  circuitScopeKey: accountCircuitScopeKey(recoveryLeaseScope),
  modelFamily: recoveryLeaseScope.modelBucket,
  state: 'HALF_OPEN',
  generation: 5,
  transitionId: 'recovery-lease-before-restart',
  incidentId: 'recovery-lease-incident',
  leaseId: 'recovery-lease-id',
  leasePurpose: 'recovery',
  leaseOwnerRunId: 'old-worker',
  leaseUntilMs: 30_000,
  recoveringSuccesses: 1,
  nextTransitionAtMs: 30_000,
  updatedAtMs: 20_000
}
const recoveryLeaseStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 21_000 })
const recoveryLeaseBridge = new AccountCircuitControlPlaneBridge({
  store: recoveryLeaseStore,
  now: () => 21_000,
  loadRebuildPage: async () => ({ items: [recoveryLeaseIncident], nextCursor: undefined })
})
assert.equal((await recoveryLeaseBridge.rebuild()).blocked, false)
const rebuiltRecoveryLease = await recoveryLeaseStore.get(recoveryLeaseScope, 21_000)
assert.equal(rebuiltRecoveryLease.halfOpenOrigin, 'RECOVERING', '恢复期租约冷启动后不能被误还原成 OPEN origin')
const releasedRecoveryLease = await recoveryLeaseStore.completeCanary({
  scope: recoveryLeaseScope,
  generation: recoveryLeaseIncident.generation,
  dispatchRevision: String(recoveryLeaseIncident.dispatchRevision),
  transitionId: 'recovery-lease-unknown-after-restart',
  leaseId: recoveryLeaseIncident.leaseId!,
  outcome: 'unknown',
  nowMs: 21_000
})
assert.equal(releasedRecoveryLease.state.phase, 'RECOVERING', '冷启动后的 unknown 只能回到原 RECOVERING，不能放大成 OPEN')
assert.equal(releasedRecoveryLease.state.recoverySuccessCount, 1)

const corruptScopeBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  loadRebuildPage: async () => ({
    items: [{
      ...baseIncident,
      circuitScopeKey: 'durable-key-does-not-match-scope',
      requestLane: 'typo'
    }],
    nextCursor: undefined
  })
})
const corruptScopeRebuild = await corruptScopeBridge.rebuild()
assert.equal(corruptScopeRebuild.blocked, true, '非法 lane/scopeKey 的 ledger 行必须阻断冷启动，不能假装 loaded/ready')
assert.equal(corruptScopeBridge.isReady(), false)

assert.deepEqual(publicAccountCircuitSummary([]), { status: 'normal' })
assert.deepEqual(publicAccountCircuitSummary([baseIncident]), {
  status: 'avoided',
  reason: 'timeout_before_complete',
  since: new Date(9_000).toISOString(),
  nextCheckAt: new Date(15_000).toISOString()
})
assert.equal(publicAccountCircuitSummary([
  { ...baseIncident, state: 'RECOVERING', updatedAtMs: 12_000 },
  { ...baseIncident, state: 'HALF_OPEN', updatedAtMs: 13_000 }
]).status, 'verifying')

let rebuildLoads = 0
const coldStartBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8, now: () => 10_000 }),
  loadRebuildPage: async () => {
    rebuildLoads++
    await new Promise((resolve) => setTimeout(resolve, 1))
    return { items: [], nextCursor: undefined }
  }
})
const coldStartResults = await Promise.all([
  coldStartBridge.rebuild(),
  coldStartBridge.rebuild(),
  coldStartBridge.rebuild()
])
assert.equal(rebuildLoads, 1, '冷启动并发请求必须共享同一个 rebuild 单飞 promise')
assert.equal(coldStartResults.every((result) => result.blocked === false), true)
assert.equal(coldStartBridge.isReady(), true)

let revisionReconcileLoads = 0
const revisionReconcileStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 20_000 })
const revisionReconcileBridge = new AccountCircuitControlPlaneBridge({
  store: revisionReconcileStore,
  loadRebuildPage: async () => {
    revisionReconcileLoads++
    if (revisionReconcileLoads === 1) {
      return {
        items: [{
          ...baseIncident,
          generation: 8,
          dispatchRevision: 8,
          transitionId: 'owner-revision-8-open',
          ledgerRevision: 8,
          projectedLedgerRevision: 8,
          updatedAtMs: 18_000
        }],
        nextCursor: undefined
      }
    }
    return {
      items: [{
        ...baseIncident,
        generation: 99,
        dispatchRevision: 7,
        transitionId: 'late-owner-revision-7',
        ledgerRevision: 99,
        projectedLedgerRevision: 99,
        updatedAtMs: 19_000
      }],
      nextCursor: undefined
    }
  }
})
assert.equal((await revisionReconcileBridge.rebuild()).blocked, false)
assert.equal((await revisionReconcileStore.get(scope, 20_000)).dispatchRevision, '8')
assert.equal(await revisionReconcileBridge.reconcileActive(), 1)
const afterLateRevisionReconcile = await revisionReconcileStore.get(scope, 20_000)
assert.equal(afterLateRevisionReconcile.dispatchRevision, '8', 'bridge 对账不得把迟到旧 owner revision 写回运行态')
assert.equal(afterLateRevisionReconcile.phase, 'OPEN')
assert.equal(afterLateRevisionReconcile.generation, 8, '旧 revision 即使伪带更大 generation 也必须被 revision fence 拒绝')

const failedBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  loadRebuildPage: async () => { throw new Error('durable state unavailable') }
})
const failedRebuild = await failedBridge.rebuild()
assert.equal(failedRebuild.blocked, true)
assert.equal(failedBridge.isReady(), false, '持久状态不可用时必须保持 fail-closed gate')
assert.equal(await failedBridge.reconcileActive(), 0)

const unrelatedAccountRuntimeKey = 'account-2'
let isolatedPersistCalls = 0
const accountIsolatedBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 60_000,
  maxPersistAttempts: 1,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async () => {
    isolatedPersistCalls++
    throw new Error('account-1 durable state unavailable')
  }
})
assert.equal((await accountIsolatedBridge.rebuild()).blocked, false)
accountIsolatedBridge.observe({ scope, state: open })
await waitUntil(
  () => !accountIsolatedBridge.isAccountReady(scope.accountRuntimeKey),
  '单账户持久化失败必须关闭该账户 readiness'
)
assert.equal(accountIsolatedBridge.isReady(), true, '单账户持久化失败不得关闭全局冷启动 readiness')
assert.equal(
  accountIsolatedBridge.isAccountReady(unrelatedAccountRuntimeKey),
  true,
  '单账户持久化失败不得阻塞无关账户'
)
const accountIsolatedRebuild = await accountIsolatedBridge.rebuild()
assert.equal(accountIsolatedRebuild.blocked, false, '维护入口重建不得把局部持久化失败升级为全局 blocked')
assert.equal(accountIsolatedBridge.isAccountReady(scope.accountRuntimeKey), false)
assert.equal(accountIsolatedBridge.isAccountReady(unrelatedAccountRuntimeKey), true)
assert.equal(isolatedPersistCalls, 2, '重建只能对失败 scope 发起一次有界立即重试')

let persistenceCall = 0
let signalFirstRecoveryStarted!: () => void
const firstRecoveryStarted = new Promise<void>((resolve) => { signalFirstRecoveryStarted = resolve })
let releaseFirstRecovery!: () => void
const firstRecoveryGate = new Promise<void>((resolve) => { releaseFirstRecovery = resolve })
let signalQueuedRecoveryStarted!: () => void
const queuedRecoveryStarted = new Promise<void>((resolve) => { signalQueuedRecoveryStarted = resolve })
let releaseQueuedRecovery!: () => void
const queuedRecoveryGate = new Promise<void>((resolve) => { releaseQueuedRecovery = resolve })
const inFlightFailureBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 1,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    persistenceCall++
    if (persistenceCall === 1) throw new Error('initial persistence failure')
    if (persistenceCall === 2) {
      signalFirstRecoveryStarted()
      await firstRecoveryGate
    } else if (persistenceCall === 3) {
      signalQueuedRecoveryStarted()
      await queuedRecoveryGate
    }
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await inFlightFailureBridge.rebuild()).blocked, false)
inFlightFailureBridge.observe({ scope, state: open })
await waitUntil(
  () => !inFlightFailureBridge.isAccountReady(scope.accountRuntimeKey),
  '首次持久化失败必须关闭故障账户 readiness'
)
await firstRecoveryStarted
inFlightFailureBridge.observe({
  scope,
  state: {
    ...open,
    generation: open.generation + 1,
    transitionId: 'queued-while-recovering',
    updatedAtMs: open.updatedAtMs + 1
  }
})
releaseFirstRecovery()
await queuedRecoveryStarted
assert.equal(
  inFlightFailureBridge.isAccountReady(scope.accountRuntimeKey),
  false,
  '旧状态恢复成功后，新状态仍在持久化时不得短暂放行故障账户'
)
assert.equal(inFlightFailureBridge.isAccountReady(unrelatedAccountRuntimeKey), true)
releaseQueuedRecovery()
await waitUntil(
  () => inFlightFailureBridge.isAccountReady(scope.accountRuntimeKey),
  '同 scope 待写队列全部排空后才能恢复账户 readiness'
)

let durableAvailable = false
const persistedTransitions: string[] = []
const boundedBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    persistedTransitions.push(input.transitionId)
    if (!durableAvailable) throw new Error('durable state unavailable')
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await boundedBridge.rebuild()).blocked, false)
boundedBridge.observe({ scope, state: open })
await waitUntil(
  () => !boundedBridge.isAccountReady(scope.accountRuntimeKey),
  '持久化重试耗尽后必须关闭故障账户就绪门'
)
assert.equal(boundedBridge.isReady(), true)
assert.equal(boundedBridge.isAccountReady(unrelatedAccountRuntimeKey), true)

for (let generation = 3; generation <= 102; generation++) {
  boundedBridge.observe({
    scope,
    state: {
      ...open,
      generation,
      transitionId: `transition-${generation}`,
      updatedAtMs: open.updatedAtMs + generation
    }
  })
}
durableAvailable = true
await waitUntil(
  () => boundedBridge.isAccountReady(scope.accountRuntimeKey),
  'DB 恢复后必须自动持久化并重新打开故障账户就绪门'
)
assert.equal(persistedTransitions.length, 3, '持续 DB 故障时每 scope 只能保留一个有限重试批次和最新待写状态')
assert.equal(persistedTransitions.at(-1), 'transition-102', '恢复时必须持久化故障期间的最新状态')

let conflictActive = true
const conflictExpectedRevisions: Array<number | null> = []
const conflictBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    conflictExpectedRevisions.push(input.expectedLedgerRevision)
    if (conflictActive) {
      return {
        status: 'cas_conflict',
        currentDispatchRevision: input.dispatchRevision,
        incident: { ...baseIncident, generation: 1, ledgerRevision: 11 }
      }
    }
    return { status: 'applied', currentDispatchRevision: input.dispatchRevision }
  }
})
assert.equal((await conflictBridge.rebuild()).blocked, false)
conflictBridge.observe({ scope, state: open })
await waitUntil(
  () => !conflictBridge.isAccountReady(scope.accountRuntimeKey),
  '持续 CAS 冲突耗尽批次后必须关闭故障账户就绪门'
)
assert.equal(conflictBridge.isReady(), true)
assert.equal(conflictBridge.isAccountReady(unrelatedAccountRuntimeKey), true)
assert.deepEqual(conflictExpectedRevisions, [null, 11], 'CAS 冲突重试必须使用 ledger 返回的最新 revision')
conflictActive = false
await waitUntil(
  () => conflictBridge.isAccountReady(scope.accountRuntimeKey),
  'CAS 冲突解除后必须自动恢复故障账户 readiness'
)
assert.equal(conflictExpectedRevisions.length, 3, 'CAS 冲突期间不得形成无界同步重试')

const sharedRaceStore = new MemoryAccountCircuitStore({ capacity: 8, now: () => 20_000 })
const sameGenerationSuspect: AccountCircuitState = {
  ...partialSuspect,
  generation: 20,
  transitionId: 'race-suspect',
  updatedAtMs: 16_000
}
const sameGenerationOpen: AccountCircuitState = {
  ...open,
  generation: 20,
  transitionId: 'race-open',
  updatedAtMs: 17_000
}
const sameGenerationRecovering: AccountCircuitState = {
  ...sameGenerationOpen,
  phase: 'RECOVERING',
  transitionId: 'race-recovering',
  recoverySuccessCount: 1,
  retryAtMs: 18_500,
  updatedAtMs: 18_000
}
const sameGenerationClosed: AccountCircuitState = {
  ...sameGenerationRecovering,
  phase: 'CLOSED',
  transitionId: 'race-closed',
  backoffAttempt: 0,
  recoverySuccessCount: 0,
  retryAtMs: undefined,
  updatedAtMs: 19_000
}
assert.equal((await sharedRaceStore.restore(sameGenerationSuspect, 16_000)).status, 'applied')

let durableRaceIncident: AccountCircuitIncidentRecord | undefined
const durableRaceTransitions: string[] = []
let releaseDelayedSuspect!: () => void
const delayedSuspectRelease = new Promise<void>((resolve) => { releaseDelayedSuspect = resolve })
let signalDelayedSuspectStarted!: () => void
const delayedSuspectStarted = new Promise<void>((resolve) => { signalDelayedSuspectStarted = resolve })
const persistRaceIncident: NonNullable<ConstructorParameters<typeof AccountCircuitControlPlaneBridge>[0]['persistIncident']> = async (input) => {
  durableRaceTransitions.push(input.transitionId)
  const currentLedgerRevision = durableRaceIncident?.ledgerRevision
  let response: Awaited<ReturnType<NonNullable<ConstructorParameters<typeof AccountCircuitControlPlaneBridge>[0]['persistIncident']>>>
  if ((input.expectedLedgerRevision === null && durableRaceIncident)
    || (input.expectedLedgerRevision !== null && input.expectedLedgerRevision !== currentLedgerRevision)) {
    response = {
      status: 'cas_conflict',
      currentDispatchRevision: input.dispatchRevision,
      incident: durableRaceIncident
    }
  } else {
    const ledgerRevision = (currentLedgerRevision ?? 0) + 1
    durableRaceIncident = {
      ...baseIncident,
      state: input.state,
      generation: input.generation,
      dispatchRevision: input.dispatchRevision,
      ledgerRevision,
      transitionId: input.transitionId,
      nextTransitionAtMs: input.nextTransitionAtMs,
      openUntilMs: input.openUntilMs,
      backoffLevel: input.backoffLevel ?? 0,
      consecutiveFailures: input.consecutiveFailures ?? 0,
      confirmationFailuresRequired: input.confirmationFailuresRequired ?? 1,
      confirmationFailureEvidenceKeys: input.confirmationFailureEvidenceKeys ?? [],
      recoveringSuccesses: input.recoveringSuccesses ?? 0,
      updatedAtMs: input.stateUpdatedAtMs ?? input.nowMs ?? 0
    }
    response = {
      status: 'applied',
      currentDispatchRevision: input.dispatchRevision,
      incident: durableRaceIncident
    }
  }
  if (input.transitionId === sameGenerationSuspect.transitionId) {
    signalDelayedSuspectStarted()
    await delayedSuspectRelease
  }
  return response
}
const delayedRaceBridge = new AccountCircuitControlPlaneBridge({
  store: sharedRaceStore,
  ownerId: 'race-delayed',
  retryDelayMs: 1,
  maxPersistAttempts: 4,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: persistRaceIncident
})
const currentRaceBridge = new AccountCircuitControlPlaneBridge({
  store: sharedRaceStore,
  ownerId: 'race-current',
  retryDelayMs: 1,
  maxPersistAttempts: 4,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: persistRaceIncident
})
assert.equal((await delayedRaceBridge.rebuild()).blocked, false)
assert.equal((await currentRaceBridge.rebuild()).blocked, false)
delayedRaceBridge.observe({ scope, state: sameGenerationSuspect })
await delayedSuspectStarted
assert.equal(durableRaceIncident?.transitionId, sameGenerationSuspect.transitionId, '乱序场景必须先令 durable 停在 SUSPECT')
assert.equal((await sharedRaceStore.restore(sameGenerationOpen, 17_000)).status, 'applied')
currentRaceBridge.observe({ scope, state: sameGenerationOpen })
await waitUntil(() => durableRaceIncident?.transitionId === sameGenerationOpen.transitionId, '并发 bridge 必须先持久化 OPEN')
assert.equal((await sharedRaceStore.restore(sameGenerationRecovering, 18_000)).status, 'applied')
currentRaceBridge.observe({ scope, state: sameGenerationRecovering })
await waitUntil(() => durableRaceIncident?.transitionId === sameGenerationRecovering.transitionId, '同 generation RECOVERING 必须可持久化')
assert.equal((await sharedRaceStore.restore(sameGenerationClosed, 19_000)).status, 'applied')
currentRaceBridge.observe({ scope, state: sameGenerationClosed })
await waitUntil(() => durableRaceIncident?.transitionId === sameGenerationClosed.transitionId, '同 generation CLOSED 必须可持久化')
releaseDelayedSuspect()
await waitUntil(
  () => durableRaceTransitions.filter((transitionId) => transitionId === sameGenerationSuspect.transitionId).length === 1,
  '迟到 SUSPECT 应仅发生一次 CAS 冲突'
)
await new Promise((resolve) => setTimeout(resolve, 20))
assert.equal(durableRaceIncident?.state, 'CLOSED', '迟到 bridge 不得用同 generation SUSPECT 覆盖 CLOSED')
assert.equal(durableRaceIncident?.transitionId, sameGenerationClosed.transitionId)
assert.equal(durableRaceIncident?.updatedAtMs, sameGenerationClosed.updatedAtMs, '持久层必须保留 runtime 状态时间')
assert.equal(delayedRaceBridge.isReady(), true)
assert.equal(currentRaceBridge.isReady(), true)

let staleGenerationCalls = 0
const staleGenerationBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    staleGenerationCalls++
    return {
      status: 'cas_conflict',
      currentDispatchRevision: input.dispatchRevision,
      incident: { ...baseIncident, generation: input.generation + 1, ledgerRevision: 12 }
    }
  }
})
assert.equal((await staleGenerationBridge.rebuild()).blocked, false)
staleGenerationBridge.observe({ scope, state: open })
await waitUntil(() => staleGenerationCalls === 1, '较新持久 generation 应吸收晚到旧状态')
await new Promise((resolve) => setTimeout(resolve, 20))
assert.equal(staleGenerationCalls, 1, '晚到旧 generation 不得用新 ledger revision 反向覆盖持久状态')
assert.equal(staleGenerationBridge.isReady(), true)

let staleCalls = 0
const staleBridge = new AccountCircuitControlPlaneBridge({
  store: new MemoryAccountCircuitStore({ capacity: 8 }),
  retryDelayMs: 5,
  maxPersistAttempts: 2,
  loadRebuildPage: async () => ({ items: [], nextCursor: undefined }),
  persistIncident: async (input) => {
    staleCalls++
    return { status: 'stale_dispatch_revision', currentDispatchRevision: input.dispatchRevision + 1 }
  }
})
assert.equal((await staleBridge.rebuild()).blocked, false)
staleBridge.observe({ scope, state: open })
await waitUntil(() => staleCalls === 1, 'stale dispatch revision 应立即完成旧状态收口')
await new Promise((resolve) => setTimeout(resolve, 20))
assert.equal(staleCalls, 1, 'stale dispatch revision 不得进入永久重试')
assert.equal(staleBridge.isReady(), true)

for (const forbidden of ['scopeKey', 'lease', 'generation', 'revision', 'count', 'ip']) {
  assert.equal(JSON.stringify(publicAccountCircuitSummary([baseIncident])).toLowerCase().includes(forbidden.toLowerCase()), false)
}

console.log(JSON.stringify({ message: '账户 circuit control-plane bridge 回归通过' }))

async function waitUntil(predicate: () => boolean, message: string): Promise<void> {
  const deadline = Date.now() + 2_000
  while (!predicate()) {
    if (Date.now() >= deadline) assert.fail(message)
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
