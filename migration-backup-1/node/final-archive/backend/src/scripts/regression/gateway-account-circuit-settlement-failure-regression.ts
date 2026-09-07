import assert from 'node:assert/strict'

import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import { MemoryAccountCircuitStore } from '../../modules/gateway/runtime/account-circuit-memory-store.js'
import {
  GatewayAccountCircuitAttempt,
  GatewayAccountCircuitService,
  gatewayAccountProtocolModelScope,
  type GatewayAccountCircuitConfirmation
} from '../../modules/gateway/runtime/account-circuit.service.js'
import {
  accountCircuitScopeKey,
  type AccountCircuitMutationResult,
  type AccountCircuitScope,
  type AccountCircuitState,
  type AccountCircuitStore
} from '../../modules/gateway/runtime/account-circuit-store.js'

type CompleteConfirmationInput = Parameters<AccountCircuitStore['completeConfirmation']>[0]
type AcquireConfirmationInput = Parameters<AccountCircuitStore['acquireConfirmationLease']>[0]

class FaultInjectingAccountCircuitStore extends MemoryAccountCircuitStore {
  completeBeforeFailures = 0
  completeAfterFailures = 0
  acquireBeforeFailures = 0
  acquireAfterFailures = 0
  completeGate: Promise<void> | undefined
  completeEntered: (() => void) | undefined
  openParentAfterAcquire = false
  readonly completeInputs: CompleteConfirmationInput[] = []
  readonly acquireInputs: AcquireConfirmationInput[] = []

  override async completeConfirmation(input: CompleteConfirmationInput): Promise<AccountCircuitMutationResult> {
    this.completeInputs.push({ ...input, scope: { ...input.scope } })
    this.completeEntered?.()
    if (this.completeGate) await this.completeGate
    if (this.completeBeforeFailures > 0) {
      this.completeBeforeFailures -= 1
      throw new Error('injected complete failure before mutation')
    }
    const result = await super.completeConfirmation(input)
    if (this.completeAfterFailures > 0) {
      this.completeAfterFailures -= 1
      throw new Error('injected complete reply loss after mutation')
    }
    return result
  }

  override async acquireConfirmationLease(input: AcquireConfirmationInput): Promise<AccountCircuitMutationResult> {
    this.acquireInputs.push({ ...input, scope: { ...input.scope } })
    if (this.acquireBeforeFailures > 0) {
      this.acquireBeforeFailures -= 1
      throw new Error('injected acquire failure before mutation')
    }
    const result = await super.acquireConfirmationLease(input)
    if (result.status === 'applied' && this.openParentAfterAcquire) {
      this.openParentAfterAcquire = false
      await super.restore(openParentState(input.scope.accountRuntimeKey, input.dispatchRevision, input.nowMs ?? 0), input.nowMs)
    }
    if (this.acquireAfterFailures > 0) {
      this.acquireAfterFailures -= 1
      throw new Error('injected acquire reply loss after mutation')
    }
    return result
  }
}

await verifyFailedSettlementPinsUnknownAndCanRetry()
await verifyAppliedTransportSettlementCanRecoverItsReply()
await verifyConcurrentContradictorySettlementsUseFirstOutcome()
await verifyAcquireReplyLossRecoversOwnedLease()
await verifyParentRaceReleasesAcquiredChildLease()

console.log('gateway account circuit settlement failure regression passed')

async function verifyFailedSettlementPinsUnknownAndCanRetry(): Promise<void> {
  const fixture = await createConfirmationFixture({ confirmationFailuresRequired: 2 })
  fixture.store.completeBeforeFailures = 1

  await assert.rejects(fixture.attempt.reportUnknown(), /injected complete failure before mutation/)
  assert.equal(fixture.attempt.isConfirmation, true, '结算失败后 attempt 必须保留 confirmation 供同结果重试')
  assert.equal((await fixture.store.get(fixture.scope, fixture.now())).lease?.leaseId, fixture.confirmation.leaseId)

  const [framingCaller, unknownCaller] = await Promise.all([
    fixture.attempt.reportFramingComplete(),
    fixture.attempt.reportUnknown()
  ])
  assert.equal(framingCaller?.state.phase, 'SUSPECT')
  assert.equal(unknownCaller?.state.phase, 'SUSPECT')
  assert.deepEqual(
    fixture.store.completeInputs.map((input) => input.outcome),
    ['unknown', 'unknown'],
    '第一次 unknown 失败后，后来 framing 调用也只能重试已锁定的 unknown'
  )
  assert.equal(fixture.attempt.isConfirmation, false)
  const settled = await fixture.store.get(fixture.scope, fixture.now())
  assert.equal(settled.phase, 'SUSPECT')
  assert.equal(settled.lease, undefined)
  assert.equal(settled.confirmationFailureCount, 0)

  const lateFailure = await fixture.attempt.reportTransportFailure({ kind: 'transport', reason: 'late opposite result' })
  assert.equal(lateFailure.outcome, 'observer_neutral', '迟到相反结果不得重新修改已结算 confirmation')
  assert.equal(fixture.store.completeInputs.length, 2)
}

async function verifyAppliedTransportSettlementCanRecoverItsReply(): Promise<void> {
  const fixture = await createConfirmationFixture({ confirmationFailuresRequired: 1 })
  fixture.store.completeAfterFailures = 1

  await assert.rejects(
    fixture.attempt.reportTransportFailure({ kind: 'transport', reason: 'confirmed transport failure' }),
    /injected complete reply loss after mutation/
  )
  assert.equal((await fixture.store.get(fixture.scope, fixture.now())).phase, 'OPEN', '故障注入必须先真实提交 OPEN')
  assert.equal(fixture.attempt.isConfirmation, true, '响应丢失后 attempt 不得误以为结算完成')

  const recovered = await fixture.attempt.reportUnknown()
  assert.equal(recovered?.status, 'idempotent', '后来调用必须重放原 transport transition 并取回已提交结果')
  assert.equal(recovered?.state.phase, 'OPEN')
  assert.deepEqual(fixture.store.completeInputs.map((input) => input.outcome), ['transport_failure', 'transport_failure'])
  assert.equal(
    fixture.store.completeInputs[0]?.transitionId,
    fixture.store.completeInputs[1]?.transitionId,
    'confirmation 结算重试必须使用稳定 transitionId'
  )
  assert.equal((await fixture.store.get(fixture.scope, fixture.now())).confirmationFailureCount, 1, '重放不得重复累计失败')
}

async function verifyConcurrentContradictorySettlementsUseFirstOutcome(): Promise<void> {
  const fixture = await createConfirmationFixture({ confirmationFailuresRequired: 2 })
  const gate = deferred<void>()
  const entered = deferred<void>()
  fixture.store.completeGate = gate.promise
  fixture.store.completeEntered = () => entered.resolve()

  const transport = fixture.attempt.reportTransportFailure({ kind: 'timeout', reason: 'first terminal wins' })
  await entered.promise
  const framing = fixture.attempt.reportFramingComplete()
  const unknown = fixture.attempt.reportUnknown()
  gate.resolve()
  const [transportResult, framingResult, unknownResult] = await Promise.all([transport, framing, unknown])

  assert.equal(transportResult.outcome, 'blocked')
  assert.equal(framingResult?.state.phase, 'SUSPECT')
  assert.equal(unknownResult?.state.phase, 'SUSPECT')
  assert.equal(fixture.store.completeInputs.length, 1, '并发相反终态必须共享一个 store settlement')
  assert.equal(fixture.store.completeInputs[0]?.outcome, 'transport_failure')
  assert.equal((await fixture.store.get(fixture.scope, fixture.now())).confirmationFailureCount, 1)
}

async function verifyAcquireReplyLossRecoversOwnedLease(): Promise<void> {
  let now = 10_000
  const store = new FaultInjectingAccountCircuitStore({ capacity: 16, now: () => now })
  const account = accountFixture('acquire-reply-loss')
  const scope = gatewayAccountProtocolModelScope(account, 'text', 'gpt-5.4')
  const suspect = await store.suspect({
    scope,
    dispatchRevision: '1',
    transitionId: 'acquire-loss-suspect',
    reason: 'initial transport fact',
    confirmationFailuresRequired: 2,
    failureEvidenceKey: 'a'.repeat(64),
    nowMs: now
  })
  now = suspect.state.retryAtMs ?? now
  store.acquireAfterFailures = 2
  const service = serviceFor(store, () => now)
  const prepared = await service.prepareAttempt({
    account,
    requestLane: 'text',
    model: 'gpt-5.4',
    confirmationLeaseDurationMs: 60_000,
    confirmationFailuresRequired: 2,
    failureEvidenceKey: 'b'.repeat(64)
  })
  assert.equal(prepared.outcome, 'dispatchable', '两次 acquire 回复丢失后必须通过权威 store read 取回自有租约')
  if (prepared.outcome !== 'dispatchable') throw new Error('缺少 recovered confirmation attempt')
  assert.equal(prepared.attempt.isConfirmation, true)
  assert.equal(store.acquireInputs.length, 2)
  assert.equal(store.acquireInputs[0]?.transitionId, store.acquireInputs[1]?.transitionId)
  assert.equal(store.acquireInputs[0]?.leaseId, store.acquireInputs[1]?.leaseId)
  await prepared.attempt.reportUnknown()
  assert.equal((await store.get(scope, now)).lease, undefined)
}

async function verifyParentRaceReleasesAcquiredChildLease(): Promise<void> {
  let now = 20_000
  const store = new FaultInjectingAccountCircuitStore({ capacity: 16, now: () => now })
  const account = accountFixture('parent-race')
  const scope = gatewayAccountProtocolModelScope(account, 'text', 'gpt-5.4')
  const suspect = await store.suspect({
    scope,
    dispatchRevision: '1',
    transitionId: 'parent-race-suspect',
    reason: 'initial transport fact',
    confirmationFailuresRequired: 2,
    failureEvidenceKey: 'c'.repeat(64),
    nowMs: now
  })
  now = suspect.state.retryAtMs ?? now
  store.openParentAfterAcquire = true
  const service = serviceFor(store, () => now)
  const prepared = await service.prepareAttempt({
    account,
    requestLane: 'text',
    model: 'gpt-5.4',
    confirmationLeaseDurationMs: 86_405_000,
    confirmationFailuresRequired: 2,
    failureEvidenceKey: 'd'.repeat(64)
  })
  assert.equal(prepared.outcome, 'blocked')
  assert.equal((await store.get(scope, now)).lease, undefined, '父电路竞态阻断派发时必须立即释放超长 child confirmation 租约')
  assert.equal(store.completeInputs.at(-1)?.outcome, 'unknown')
}

async function createConfirmationFixture(input: { confirmationFailuresRequired: number }) {
  let now = 1_000
  const store = new FaultInjectingAccountCircuitStore({ capacity: 16, now: () => now })
  const scope = {
    kind: 'protocol_model' as const,
    accountRuntimeKey: `settlement-${input.confirmationFailuresRequired}-${Math.random().toString(16).slice(2)}`,
    protocolProfile: 'profile_openai_v1',
    requestLane: 'text' as const,
    modelBucket: 'gpt-5.4'
  }
  const suspect = await store.suspect({
    scope,
    dispatchRevision: '1',
    transitionId: 'seed-suspect',
    reason: 'initial transport fact',
    confirmationFailuresRequired: input.confirmationFailuresRequired,
    failureEvidenceKey: '1'.repeat(64),
    nowMs: now
  })
  now = suspect.state.retryAtMs ?? now
  const leaseId = 'confirmation-lease'
  const acquired = await store.acquireConfirmationLease({
    scope,
    generation: suspect.state.generation,
    dispatchRevision: '1',
    transitionId: 'seed-acquire',
    leaseId,
    leaseUntilMs: now + 60_000,
    expectedFailureEvidenceKey: '1'.repeat(64),
    confirmationEvidenceKey: '2'.repeat(64),
    nowMs: now
  })
  assert.equal(acquired.status, 'applied')
  const confirmation: GatewayAccountCircuitConfirmation = {
    scope,
    scopeKey: accountCircuitScopeKey(scope),
    accountRuntimeKey: scope.accountRuntimeKey,
    generation: acquired.state.generation,
    dispatchRevision: acquired.state.dispatchRevision,
    leaseId
  }
  const service = serviceFor(store, () => now)
  const attempt = new GatewayAccountCircuitAttempt(
    service,
    scope,
    '1',
    60_000,
    input.confirmationFailuresRequired,
    confirmation,
    '2'.repeat(64)
  )
  return { store, scope, confirmation, attempt, now: () => now }
}

function serviceFor(store: AccountCircuitStore, now: () => number): GatewayAccountCircuitService {
  let id = 0
  return new GatewayAccountCircuitService(store, {
    now,
    createId: () => `settlement-id-${++id}`,
    isRuntimeStateReady: () => true,
    escalationDistinctScopeThreshold: 3
  })
}

function accountFixture(id: string): UpstreamAccount {
  return {
    id,
    providerCode: 'openai',
    providerProtocolProfileId: 'profile_openai_v1',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    systemAccountId: 'system-a',
    accountOwnerSystemAccountId: 'system-a',
    groupOwnerSystemAccountId: 'system-a',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: id,
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
    apiKey: 'test-key',
    apiKeys: ['test-key'],
    refreshToken: 'test-refresh',
    streamFailureCount: 0,
    dispatchRevision: 1,
    credentials: { api_key: 'test-key', refresh_token: 'test-refresh' }
  }
}

function openParentState(accountRuntimeKey: string, dispatchRevision: string, nowMs: number): AccountCircuitState {
  const scope: AccountCircuitScope = { kind: 'account', accountRuntimeKey }
  return {
    scope,
    scopeKey: accountCircuitScopeKey(scope),
    phase: 'OPEN',
    generation: 1,
    dispatchRevision,
    transitionId: 'parent-race-open',
    incidentId: 'parent-race-incident',
    backoffAttempt: 1,
    recoverySuccessCount: 0,
    retryAtMs: nowMs + 60_000,
    openedAtMs: nowMs,
    updatedAtMs: nowMs
  }
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}
