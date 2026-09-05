import {
  accountCircuitConfirmationFailureCount,
  accountCircuitDefaultConfirmationFailuresRequired,
  accountCircuitFailureEvidenceKeys,
  accountCircuitBackoffDelayMs,
  accountCircuitHierarchyTransitionId,
  accountCircuitRecoveryCanaryIntervalMs,
  accountCircuitRecoverySuccessThreshold,
  accountCircuitSuspectConfirmationIntervalMs,
  accountCircuitScopeKey,
  assertAccountCircuitStateScopeKey,
  capacityExhaustedAccountCircuitState,
  cloneAccountCircuitState,
  closedAccountCircuitState,
  normalizeAccountCircuitConfirmationFailuresRequired,
  normalizeAccountCircuitEscalationDistinctScopeThreshold,
  normalizeAccountCircuitFailureEvidenceKey,
  type AccountCircuitLease,
  type AccountCircuitEscalationResult,
  type AccountCircuitMutationResult,
  type AccountCircuitProtocolModelOpenEvidenceInput,
  type AccountCircuitScope,
  type AccountCircuitState,
  type AccountCircuitStore,
  type AccountCircuitTransitionIdentity
} from './account-circuit-store.js'

export interface MemoryAccountCircuitStoreOptions {
  capacity: number
  closedRetentionMs?: number
  replayLimitPerScope?: number
  now?: () => number
}

interface MemoryAccountCircuitEntry {
  state: AccountCircuitState
  closedExpiresAtMs?: number
  replayOrder: string[]
  replayIds: Set<string>
}

interface MemoryAccountCircuitEscalationScopeEvidence {
  scopeKey: string
  incidentId: string
  evidenceId: string
  confirmedFailureCount: number
  observedAtMs: number
}

interface MemoryAccountCircuitEscalationEvidence {
  dispatchRevision: string
  scopes: MemoryAccountCircuitEscalationScopeEvidence[]
}

export class MemoryAccountCircuitStore implements AccountCircuitStore {
  private readonly entries = new Map<string, MemoryAccountCircuitEntry>()
  private readonly escalationEvidence = new Map<string, MemoryAccountCircuitEscalationEvidence>()
  private readonly capacity: number
  private readonly closedRetentionMs: number
  private readonly replayLimitPerScope: number
  private readonly now: () => number
  private capacitySaturated = false

  constructor(options: MemoryAccountCircuitStoreOptions) {
    this.capacity = positiveInteger(options.capacity, 'capacity')
    this.closedRetentionMs = positiveInteger(options.closedRetentionMs ?? 5 * 60_000, 'closedRetentionMs')
    this.replayLimitPerScope = positiveInteger(options.replayLimitPerScope ?? 64, 'replayLimitPerScope')
    this.now = options.now ?? Date.now
  }

  async get(scope: AccountCircuitScope, nowMs = this.now()): Promise<AccountCircuitState> {
    const entry = this.freshEntry(scope, nowMs)
    if (entry) return cloneAccountCircuitState(entry.state)
    if (this.capacitySaturated && this.reserveCapacity(nowMs)) return closedAccountCircuitState(scope)
    return this.capacitySaturated
      ? capacityExhaustedAccountCircuitState(scope, '', nowMs)
      : closedAccountCircuitState(scope)
  }

  async suspect(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    reason: string
    confirmationFailuresRequired?: number
    failureEvidenceKey?: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const existing = this.freshEntry(input.scope, now)
    const replay = this.idempotentResult(existing, input.transitionId)
    if (replay) return replay
    if (existing?.state.dispatchRevision
      && existing.state.dispatchRevision !== input.dispatchRevision) {
      return result('stale_dispatch_revision', existing.state)
    }
    if (existing && existing.state.phase !== 'CLOSED') return result('state_mismatch', existing.state)
    if (!existing && !this.reserveCapacity(now)) {
      return result('capacity_exhausted', capacityExhaustedAccountCircuitState(input.scope, input.dispatchRevision, now))
    }
    const generation = (existing?.state.generation ?? 0) + 1
    const confirmationFailuresRequired = normalizeAccountCircuitConfirmationFailuresRequired(
      input.confirmationFailuresRequired,
      accountCircuitDefaultConfirmationFailuresRequired
    )
    const failureEvidenceKey = normalizeAccountCircuitFailureEvidenceKey(
      input.failureEvidenceKey,
      `suspect:${input.transitionId}`
    )
    const state: AccountCircuitState = {
      scopeKey: accountCircuitScopeKey(input.scope),
      scope: { ...input.scope },
      phase: 'SUSPECT',
      generation,
      dispatchRevision: requiredValue(input.dispatchRevision, 'dispatchRevision'),
      transitionId: requiredValue(input.transitionId, 'transitionId'),
      backoffAttempt: 0,
      recoverySuccessCount: 0,
      confirmationFailuresRequired,
      confirmationFailureCount: 0,
      failureEvidenceKeys: [failureEvidenceKey],
      incidentId: input.transitionId,
      failureReason: input.reason,
      retryAtMs: now + accountCircuitSuspectConfirmationIntervalMs,
      updatedAtMs: now
    }
    const entry = existing ?? this.newEntry(state)
    return this.apply(entry, state, input.transitionId)
  }

  async acquireConfirmationLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
    expectedFailureEvidenceKey?: string
    confirmationEvidenceKey?: string
  }): Promise<AccountCircuitMutationResult> {
    return this.acquireLease(input, 'SUSPECT', 'confirmation')
  }

  async closeSuspectFromObserver(input: AccountCircuitTransitionIdentity & {
    expectedFailureEvidenceKey: string
    observerEvidenceKey: string
  }): Promise<AccountCircuitMutationResult> {
    const checked = this.checkedSuspectClosure(input, input.expectedFailureEvidenceKey)
    if ('result' in checked) return checked.result
    const observerEvidenceKey = normalizeAccountCircuitFailureEvidenceKey(
      input.observerEvidenceKey,
      `observer-close:${input.transitionId}`
    )
    if (accountCircuitFailureEvidenceKeys(checked.entry.state).includes(observerEvidenceKey)) {
      return result('state_mismatch', checked.entry.state)
    }
    return this.close(checked.entry, input.transitionId, checked.now)
  }

  async closeSuspectFromKeyRotation(input: AccountCircuitTransitionIdentity & {
    expectedFailureEvidenceKey: string
  }): Promise<AccountCircuitMutationResult> {
    const checked = this.checkedSuspectClosure(input, input.expectedFailureEvidenceKey)
    if ('result' in checked) return checked.result
    return this.close(checked.entry, input.transitionId, checked.now)
  }

  async completeConfirmation(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
    failureEvidenceKey?: string
    framingCompleteDisposition?: 'recovering' | 'closed'
  }): Promise<AccountCircuitMutationResult> {
    const checked = this.checkedEntry(input, 'SUSPECT', 'confirmation', input.leaseId)
    if ('result' in checked) return checked.result
    const { entry, now } = checked
    if (input.outcome === 'framing_complete') {
      if (input.framingCompleteDisposition === 'closed') {
        return this.close(entry, input.transitionId, now)
      }
      return this.enterRecovering(entry, input.transitionId, now)
    }
    if (input.outcome === 'unknown') {
      const backoffAttempt = entry.state.backoffAttempt + 1
      return this.apply(entry, {
        ...entry.state,
        transitionId: input.transitionId,
        backoffAttempt,
        lease: undefined,
        retryAtMs: now + accountCircuitBackoffDelayMs(
          backoffAttempt,
          `${entry.state.scopeKey}:${entry.state.generation}:${backoffAttempt}:confirmation-unknown`
        ),
        updatedAtMs: now
      }, input.transitionId)
    }
    const confirmationFailuresRequired = normalizeAccountCircuitConfirmationFailuresRequired(
      entry.state.confirmationFailuresRequired
    )
    const previousEvidenceKeys = accountCircuitFailureEvidenceKeys(entry.state)
    const failureEvidenceKey = normalizeAccountCircuitFailureEvidenceKey(
      input.failureEvidenceKey,
      `confirmation:${input.leaseId}`
    )
    const isIndependentEvidence = !previousEvidenceKeys.includes(failureEvidenceKey)
    const failureEvidenceKeys = isIndependentEvidence
      ? [...previousEvidenceKeys, failureEvidenceKey].slice(-(confirmationFailuresRequired + 1))
      : previousEvidenceKeys
    const confirmationFailureCount = accountCircuitConfirmationFailureCount(entry.state)
      + (isIndependentEvidence ? 1 : 0)
    const confirmationState: AccountCircuitState = {
      ...entry.state,
      backoffAttempt: 0,
      confirmationFailuresRequired,
      confirmationFailureCount,
      failureEvidenceKeys,
      transitionId: input.transitionId,
      failureReason: input.reason ?? entry.state.failureReason,
      lease: undefined,
      retryAtMs: now + accountCircuitSuspectConfirmationIntervalMs,
      updatedAtMs: now
    }
    if (confirmationFailureCount < confirmationFailuresRequired) {
      return this.apply(entry, confirmationState, input.transitionId)
    }
    entry.state = confirmationState
    return this.open(entry, input.transitionId, now, input.reason)
  }

  async acquireCanaryLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
  }): Promise<AccountCircuitMutationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const entry = this.freshEntry(input.scope, now)
    const invalid = this.validateIdentity(entry, input)
    if (invalid) return invalid
    if (!entry) return result('not_found', closedAccountCircuitState(input.scope))
    const replay = this.idempotentResult(entry, input.transitionId)
    if (replay) return replay
    if (entry.state.phase !== 'OPEN' && entry.state.phase !== 'RECOVERING') {
      return result('state_mismatch', entry.state)
    }
    if (entry.state.lease) return result('state_mismatch', entry.state)
    if ((entry.state.phase === 'OPEN' || entry.state.phase === 'RECOVERING') && (entry.state.retryAtMs ?? Number.POSITIVE_INFINITY) > now) {
      return result('not_due', entry.state)
    }
    const origin = entry.state.phase
    const lease = accountCircuitLease(
      origin === 'OPEN' ? 'half_open' : 'recovery',
      input.leaseId,
      input.leaseUntilMs,
      now
    )
    return this.apply(entry, {
      ...entry.state,
      phase: 'HALF_OPEN',
      transitionId: input.transitionId,
      lease,
      halfOpenOrigin: origin,
      updatedAtMs: now
    }, input.transitionId)
  }

  async completeCanary(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
    evidenceScopeKey?: string
  }): Promise<AccountCircuitMutationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const entry = this.freshEntry(input.scope, now)
    const invalid = this.validateIdentity(entry, input)
    if (invalid) return invalid
    if (!entry) return result('not_found', closedAccountCircuitState(input.scope))
    const replay = this.idempotentResult(entry, input.transitionId)
    if (replay) return replay
    if (entry.state.phase !== 'HALF_OPEN') return result('state_mismatch', entry.state)
    if (!entry.state.lease || entry.state.lease.leaseId !== input.leaseId) {
      return result('lease_mismatch', entry.state)
    }
    if (input.outcome === 'transport_failure') {
      return this.open(entry, input.transitionId, now, input.reason)
    }
    if (input.outcome === 'unknown') {
      return this.restoreCanaryOrigin(entry, input.transitionId, now)
    }
    if (entry.state.halfOpenOrigin === 'OPEN') {
      return this.enterRecovering(entry, input.transitionId, now)
    }
    const recoveryEvidenceScopeKeys = this.nextRecoveryEvidenceScopeKeys(entry.state, input.evidenceScopeKey)
    const recoverySuccessCount = entry.state.recoverySuccessCount + 1
    if (recoverySuccessCount >= accountCircuitRecoverySuccessThreshold) {
      return this.close(entry, input.transitionId, now)
    }
    return this.apply(entry, {
      ...entry.state,
      phase: 'RECOVERING',
      transitionId: input.transitionId,
      recoverySuccessCount,
      recoveryEvidenceScopeKeys,
      lease: undefined,
      halfOpenOrigin: undefined,
      retryAtMs: now,
      updatedAtMs: now
    }, input.transitionId)
  }

  async recordProtocolModelOpenEvidence(
    input: AccountCircuitProtocolModelOpenEvidenceInput
  ): Promise<AccountCircuitEscalationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const scopeKey = accountCircuitScopeKey(input.scope)
    const child = this.freshEntry(input.scope, now)
    const accountScope: AccountCircuitScope = { kind: 'account', accountRuntimeKey: input.scope.accountRuntimeKey }
    const closedAccountState = closedAccountCircuitState(accountScope, input.dispatchRevision)
    if (!child) return escalationResult('not_found', closedAccountState, 0, 0)
    if (child.state.generation !== input.generation) {
      return escalationResult('stale_generation', closedAccountState, 0, 0)
    }
    if (child.state.dispatchRevision !== input.dispatchRevision) {
      return escalationResult('stale_dispatch_revision', closedAccountState, 0, 0)
    }
    if (child.state.phase !== 'OPEN') {
      return escalationResult('state_mismatch', closedAccountState, 0, 0)
    }

    const windowMs = positiveInteger(input.windowMs, 'windowMs')
    const maxProtocolScopes = positiveInteger(input.maxProtocolScopes, 'maxProtocolScopes')
    const distinctScopeThreshold = normalizeAccountCircuitEscalationDistinctScopeThreshold(input.distinctScopeThreshold)
    if (distinctScopeThreshold > maxProtocolScopes) {
      throw new Error('账户电路 distinctScopeThreshold 不能超过 maxProtocolScopes')
    }
    const confirmedFailureCount = positiveInteger(input.confirmedFailureCount, 'confirmedFailureCount')
    const cutoff = now - windowMs
    const previous = this.escalationEvidence.get(input.scope.accountRuntimeKey)
    const evidence: MemoryAccountCircuitEscalationEvidence = previous?.dispatchRevision === input.dispatchRevision
      ? {
          dispatchRevision: previous.dispatchRevision,
          scopes: previous.scopes.filter((item) => item.observedAtMs >= cutoff)
        }
      : { dispatchRevision: input.dispatchRevision, scopes: [] }
    if (evidence.scopes.some((item) => item.evidenceId === input.evidenceId)) {
      const accountState = await this.get(accountScope, now)
      return escalationResult('idempotent', accountState, evidence.scopes.length, totalConfirmedFailures(evidence.scopes))
    }
    const incidentId = child.state.incidentId ?? `${scopeKey}@${child.state.generation}`
    const existingIndex = evidence.scopes.findIndex((item) => item.scopeKey === scopeKey)
    const nextScopeEvidence: MemoryAccountCircuitEscalationScopeEvidence = {
      scopeKey,
      incidentId,
      evidenceId: requiredValue(input.evidenceId, 'evidenceId'),
      confirmedFailureCount,
      observedAtMs: now
    }
    if (existingIndex >= 0) evidence.scopes[existingIndex] = nextScopeEvidence
    else evidence.scopes.push(nextScopeEvidence)
    evidence.scopes.sort((left, right) => left.observedAtMs - right.observedAtMs)
    while (evidence.scopes.length > maxProtocolScopes) evidence.scopes.shift()
    this.escalationEvidence.set(input.scope.accountRuntimeKey, evidence)

    const failureTotal = totalConfirmedFailures(evidence.scopes)
    const accountEntry = this.freshEntry(accountScope, now)
    if (evidence.scopes.length < distinctScopeThreshold) {
      return escalationResult('recorded', accountEntry?.state ?? closedAccountState, evidence.scopes.length, failureTotal)
    }

    const childScopeKeys = evidence.scopes.map((item) => item.scopeKey)
    const childIncidentIds = evidence.scopes.map((item) => item.incidentId)
    if (accountEntry && accountEntry.state.phase !== 'CLOSED') {
      if (accountEntry.state.dispatchRevision !== input.dispatchRevision) {
        return escalationResult('stale_dispatch_revision', accountEntry.state, evidence.scopes.length, failureTotal)
      }
      const relatedStates = this.attachAccountShadow(
        accountEntry,
        childScopeKeys,
        childIncidentIds,
        input.accountTransitionId,
        now
      )
      return escalationResult('already_active', accountEntry.state, evidence.scopes.length, failureTotal, relatedStates)
    }
    if (!accountEntry && !this.reserveCapacity(now)) {
      return escalationResult(
        'capacity_exhausted',
        capacityExhaustedAccountCircuitState(accountScope, input.dispatchRevision, now),
        evidence.scopes.length,
        failureTotal
      )
    }
    const target = accountEntry ?? this.newEntry(closedAccountState)
    const accountIncidentId = requiredValue(input.accountTransitionId, 'accountTransitionId')
    const accountState: AccountCircuitState = {
      ...closedAccountState,
      phase: 'OPEN',
      generation: target.state.generation + 1,
      dispatchRevision: requiredValue(input.dispatchRevision, 'dispatchRevision'),
      transitionId: accountIncidentId,
      incidentId: accountIncidentId,
      backoffAttempt: 1,
      openedAtMs: now,
      retryAtMs: now + accountCircuitBackoffDelayMs(1, `${closedAccountState.scopeKey}:${target.state.generation + 1}:1`),
      failureReason: requiredValue(input.reason, 'reason'),
      childScopeKeys,
      childIncidentIds,
      requiredRecoveryScopeKeys: [...childScopeKeys],
      recoveryEvidenceScopeKeys: [],
      updatedAtMs: now
    }
    this.apply(target, accountState, accountIncidentId)
    const relatedStates = this.shadowChildren(
      childScopeKeys,
      childIncidentIds,
      accountIncidentId,
      input.dispatchRevision,
      accountIncidentId,
      now
    )
    return escalationResult('escalated', accountState, evidence.scopes.length, failureTotal, relatedStates)
  }

  async clearAccountEscalationEvidence(input: {
    accountRuntimeKey: string
    dispatchRevision: string
    evidenceId: string
    nowMs?: number
  }): Promise<boolean> {
    normalizedNow(input.nowMs ?? this.now())
    requiredValue(input.evidenceId, 'evidenceId')
    const evidence = this.escalationEvidence.get(requiredValue(input.accountRuntimeKey, 'accountRuntimeKey'))
    if (!evidence || evidence.dispatchRevision !== requiredValue(input.dispatchRevision, 'dispatchRevision')) return false
    this.escalationEvidence.delete(input.accountRuntimeKey)
    return true
  }

  async replaceDispatchRevision(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const entry = this.freshEntry(input.scope, now)
    const replay = this.idempotentResult(entry, input.transitionId)
    if (replay) return replay
    if (entry?.state.dispatchRevision === input.dispatchRevision) {
      return result('idempotent', entry.state)
    }
    if (entry && isOlderNumericDispatchRevision(input.dispatchRevision, entry.state.dispatchRevision)) {
      return result('stale_dispatch_revision', entry.state)
    }
    this.escalationEvidence.delete(input.scope.accountRuntimeKey)
    if (!entry && !this.reserveCapacity(now)) {
      return result('capacity_exhausted', capacityExhaustedAccountCircuitState(input.scope, input.dispatchRevision, now))
    }
    const target = entry ?? this.newEntry(closedAccountCircuitState(input.scope))
    return this.apply(target, {
      ...closedAccountCircuitState(
        input.scope,
        requiredValue(input.dispatchRevision, 'dispatchRevision'),
        target.state.generation + 1,
        requiredValue(input.transitionId, 'transitionId'),
        now
      )
    }, input.transitionId, now + this.closedRetentionMs)
  }

  async restore(rawState: AccountCircuitState, nowMs = this.now()): Promise<AccountCircuitMutationResult> {
    const now = normalizedNow(nowMs)
    const state = normalizeConfirmationState(cloneAccountCircuitState(rawState))
    assertAccountCircuitStateScopeKey(state)
    const existing = this.freshEntry(state.scope, now)
    if (existing && isOlderNumericDispatchRevision(state.dispatchRevision, existing.state.dispatchRevision)) {
      return result('stale_dispatch_revision', existing.state)
    }
    if (existing && (existing.state.generation > state.generation
      || (existing.state.generation === state.generation && existing.state.updatedAtMs >= state.updatedAtMs))) {
      const relatedStates = this.projectParentRelationship(existing.state)
      return result('idempotent', existing.state, relatedStates)
    }
    if (!existing && !this.reserveCapacity(now)) {
      return result('capacity_exhausted', capacityExhaustedAccountCircuitState(state.scope, state.dispatchRevision, now))
    }
    const entry = existing ?? this.newEntry(state)
    entry.state = state
    entry.closedExpiresAtMs = state.phase === 'CLOSED' ? now + this.closedRetentionMs : undefined
    if (!entry.replayIds.has(state.transitionId)) {
      entry.replayIds.add(state.transitionId)
      entry.replayOrder.push(state.transitionId)
    }
    this.entries.set(state.scopeKey, entry)
    const relatedStates = this.projectParentRelationship(state)
    return result('applied', state, relatedStates)
  }

  async replaceAccountDispatchRevision(input: {
    accountRuntimeKey: string
    dispatchRevision: string
    transitionId: string
    nowMs?: number
  }): Promise<number> {
    const now = normalizedNow(input.nowMs ?? this.now())
    for (const [runtimeKey, evidence] of this.escalationEvidence) {
      if (runtimeKeyMatchesDispatchRevisionTarget(runtimeKey, input.accountRuntimeKey)
        && evidence.dispatchRevision !== input.dispatchRevision
        && !isOlderNumericDispatchRevision(input.dispatchRevision, evidence.dispatchRevision)) {
        this.escalationEvidence.delete(runtimeKey)
      }
    }
    let changed = 0
    for (const entry of this.entries.values()) {
      if (!runtimeKeyMatchesDispatchRevisionTarget(entry.state.scope.accountRuntimeKey, input.accountRuntimeKey)) continue
      if (entry.state.dispatchRevision === input.dispatchRevision) continue
      if (isOlderNumericDispatchRevision(input.dispatchRevision, entry.state.dispatchRevision)) continue
      const state = closedAccountCircuitState(
        entry.state.scope,
        input.dispatchRevision,
        entry.state.generation + 1,
        input.transitionId,
        now
      )
      entry.state = state
      entry.closedExpiresAtMs = now + this.closedRetentionMs
      entry.replayIds.add(input.transitionId)
      entry.replayOrder.push(input.transitionId)
      changed++
    }
    return changed
  }

  async listDue(nowMs: number, limit: number): Promise<AccountCircuitState[]> {
    const now = normalizedNow(nowMs)
    this.cleanup(now)
    return [...this.entries.values()]
      .filter((entry) => accountCircuitDueAtMs(entry.state) <= now)
      .sort((left, right) => accountCircuitDueAtMs(left.state) - accountCircuitDueAtMs(right.state))
      .slice(0, positiveInteger(limit, 'limit'))
      .map((entry) => cloneAccountCircuitState(entry.state))
  }

  async size(): Promise<number> {
    this.cleanup(this.now())
    return this.entries.size
  }

  private acquireLease(
    input: AccountCircuitTransitionIdentity & {
      leaseId: string
      leaseUntilMs: number
      expectedFailureEvidenceKey?: string
      confirmationEvidenceKey?: string
    },
    expectedPhase: 'SUSPECT',
    kind: 'confirmation'
  ): AccountCircuitMutationResult {
    const now = normalizedNow(input.nowMs ?? this.now())
    const entry = this.freshEntry(input.scope, now)
    const invalid = this.validateIdentity(entry, input)
    if (invalid) return invalid
    if (!entry) return result('not_found', closedAccountCircuitState(input.scope))
    const replay = this.idempotentResult(entry, input.transitionId)
    if (replay) return replay
    if (entry.state.phase !== expectedPhase || entry.state.shadowedByIncidentId !== undefined) {
      return result('state_mismatch', entry.state)
    }
    if (
      input.expectedFailureEvidenceKey !== undefined
      && accountCircuitFailureEvidenceKeys(entry.state).at(-1) !== normalizeAccountCircuitFailureEvidenceKey(
        input.expectedFailureEvidenceKey,
        `confirmation-acquire:${input.transitionId}`
      )
    ) {
      return result('state_mismatch', entry.state)
    }
    if (
      input.confirmationEvidenceKey !== undefined
      && accountCircuitFailureEvidenceKeys(entry.state).includes(normalizeAccountCircuitFailureEvidenceKey(
        input.confirmationEvidenceKey,
        `confirmation-evidence:${input.transitionId}`
      ))
    ) {
      return result('state_mismatch', entry.state)
    }
    if (entry.state.lease) return result('state_mismatch', entry.state)
    if ((entry.state.retryAtMs ?? Number.POSITIVE_INFINITY) > now) {
      return result('not_due', entry.state)
    }
    return this.apply(entry, {
      ...entry.state,
      transitionId: input.transitionId,
      lease: accountCircuitLease(kind, input.leaseId, input.leaseUntilMs, now),
      updatedAtMs: now
    }, input.transitionId)
  }

  private checkedSuspectClosure(
    input: AccountCircuitTransitionIdentity,
    expectedFailureEvidenceKey: string
  ): { entry: MemoryAccountCircuitEntry; now: number } | { result: AccountCircuitMutationResult } {
    const now = normalizedNow(input.nowMs ?? this.now())
    const entry = this.freshEntry(input.scope, now)
    const invalid = this.validateIdentity(entry, input)
    if (invalid) return { result: invalid }
    if (!entry) return { result: result('not_found', closedAccountCircuitState(input.scope)) }
    const replay = this.idempotentResult(entry, input.transitionId)
    if (replay) return { result: replay }
    if (entry.state.phase !== 'SUSPECT' || entry.state.shadowedByIncidentId !== undefined) {
      return { result: result('state_mismatch', entry.state) }
    }
    const expectedEvidence = normalizeAccountCircuitFailureEvidenceKey(
      expectedFailureEvidenceKey,
      `suspect-close:${input.transitionId}`
    )
    if (accountCircuitFailureEvidenceKeys(entry.state).at(-1) !== expectedEvidence) {
      return { result: result('state_mismatch', entry.state) }
    }
    return { entry, now }
  }

  private checkedEntry(
    input: AccountCircuitTransitionIdentity,
    expectedPhase: 'SUSPECT',
    leaseKind: 'confirmation',
    leaseId: string
  ): { entry: MemoryAccountCircuitEntry; now: number } | { result: AccountCircuitMutationResult } {
    const now = normalizedNow(input.nowMs ?? this.now())
    const entry = this.freshEntry(input.scope, now)
    const invalid = this.validateIdentity(entry, input)
    if (invalid) return { result: invalid }
    if (!entry) return { result: result('not_found', closedAccountCircuitState(input.scope)) }
    const replay = this.idempotentResult(entry, input.transitionId)
    if (replay) return { result: replay }
    if (entry.state.phase !== expectedPhase) return { result: result('state_mismatch', entry.state) }
    if (entry.state.lease?.kind !== leaseKind || entry.state.lease.leaseId !== leaseId) {
      return { result: result('lease_mismatch', entry.state) }
    }
    return { entry, now }
  }

  private validateIdentity(
    entry: MemoryAccountCircuitEntry | undefined,
    input: AccountCircuitTransitionIdentity
  ): AccountCircuitMutationResult | undefined {
    if (!entry) return result('not_found', closedAccountCircuitState(input.scope))
    if (entry.state.generation !== input.generation) return result('stale_generation', entry.state)
    if (entry.state.dispatchRevision !== input.dispatchRevision) {
      return result('stale_dispatch_revision', entry.state)
    }
    return undefined
  }

  private open(
    entry: MemoryAccountCircuitEntry,
    transitionId: string,
    now: number,
    reason?: string
  ): AccountCircuitMutationResult {
    const backoffAttempt = entry.state.backoffAttempt + 1
    return this.apply(entry, {
      ...entry.state,
      phase: 'OPEN',
      transitionId,
      backoffAttempt,
      recoverySuccessCount: 0,
      recoveryEvidenceScopeKeys: [],
      incidentId: entry.state.incidentId ?? transitionId,
      openedAtMs: now,
      retryAtMs: now + accountCircuitBackoffDelayMs(
        backoffAttempt,
        `${entry.state.scopeKey}:${entry.state.generation}:${backoffAttempt}`
      ),
      failureReason: reason ?? entry.state.failureReason,
      lease: undefined,
      halfOpenOrigin: undefined,
      updatedAtMs: now
    }, transitionId)
  }

  private enterRecovering(
    entry: MemoryAccountCircuitEntry,
    transitionId: string,
    now: number
  ): AccountCircuitMutationResult {
    const backoffAttempt = entry.state.phase === 'SUSPECT' ? 0 : entry.state.backoffAttempt
    return this.apply(entry, {
      ...entry.state,
      phase: 'RECOVERING',
      transitionId,
      backoffAttempt,
      recoverySuccessCount: 0,
      recoveryEvidenceScopeKeys: [],
      confirmationFailureCount: 0,
      failureEvidenceKeys: [],
      failureReason: undefined,
      lease: undefined,
      halfOpenOrigin: undefined,
      retryAtMs: now + accountCircuitRecoveryCanaryIntervalMs,
      updatedAtMs: now
    }, transitionId)
  }

  private close(entry: MemoryAccountCircuitEntry, transitionId: string, now: number): AccountCircuitMutationResult {
    const childScopeKeys = entry.state.childScopeKeys ?? []
    const childIncidentIds = entry.state.childIncidentIds ?? []
    const incidentId = entry.state.incidentId
    const isAccountScope = entry.state.scope.kind === 'account'
    if (isAccountScope) {
      this.escalationEvidence.delete(entry.state.scope.accountRuntimeKey)
    }
    const closed = this.apply(entry, {
      ...closedAccountCircuitState(
        entry.state.scope,
        entry.state.dispatchRevision,
        entry.state.generation,
        transitionId,
        now
      ),
      incidentId,
      ...(isAccountScope && incidentId && childScopeKeys.length > 0
        ? {
            childScopeKeys: [...childScopeKeys],
            childIncidentIds: [...childIncidentIds]
          }
        : {})
    }, transitionId, now + this.closedRetentionMs)
    const relatedStates = incidentId
      ? this.unshadowChildren(childScopeKeys, childIncidentIds, incidentId, transitionId, now)
      : []
    return result(closed.status, closed.state, relatedStates)
  }

  private nextRecoveryEvidenceScopeKeys(
    state: AccountCircuitState,
    evidenceScopeKey: string | undefined
  ): string[] {
    if (state.scope.kind !== 'account') return state.recoveryEvidenceScopeKeys ?? []
    const requiredScopeKeys = state.requiredRecoveryScopeKeys ?? []
    if (requiredScopeKeys.length === 0) return state.recoveryEvidenceScopeKeys ?? []
    const normalized = evidenceScopeKey?.trim()
    if (!normalized || !requiredScopeKeys.includes(normalized)) return state.recoveryEvidenceScopeKeys ?? []
    return [...new Set([...(state.recoveryEvidenceScopeKeys ?? []), normalized])]
  }

  private attachAccountShadow(
    entry: MemoryAccountCircuitEntry,
    childScopeKeys: string[],
    childIncidentIds: string[],
    transitionId: string,
    now: number
  ): AccountCircuitState[] {
    const incidentId = entry.state.incidentId ?? entry.state.transitionId
    const nextScopeKeys = [...(entry.state.childScopeKeys ?? [])]
    const nextIncidentIds = [...(entry.state.childIncidentIds ?? [])]
    let relationshipChanged = entry.state.incidentId !== incidentId
    for (const [index, childScopeKey] of childScopeKeys.entries()) {
      const childIncidentId = childIncidentIds[index]
      if (!childIncidentId) continue
      const existingIndex = nextScopeKeys.indexOf(childScopeKey)
      if (existingIndex < 0) {
        nextScopeKeys.push(childScopeKey)
        nextIncidentIds.push(childIncidentId)
        relationshipChanged = true
      } else if (nextIncidentIds[existingIndex] !== childIncidentId) {
        nextIncidentIds[existingIndex] = childIncidentId
        relationshipChanged = true
      }
    }
    if (relationshipChanged) {
      entry.state = {
        ...entry.state,
        transitionId: requiredValue(transitionId, 'accountTransitionId'),
        incidentId,
        childScopeKeys: nextScopeKeys,
        childIncidentIds: nextIncidentIds,
        requiredRecoveryScopeKeys: [...new Set([...(entry.state.requiredRecoveryScopeKeys ?? []), ...nextScopeKeys])],
        updatedAtMs: now
      }
      this.rememberReplay(entry, transitionId)
    }
    return this.shadowChildren(
      childScopeKeys,
      childIncidentIds,
      incidentId,
      entry.state.dispatchRevision,
      transitionId,
      now
    )
  }

  private shadowChildren(
    scopeKeys: string[],
    childIncidentIds: string[],
    parentIncidentId: string,
    dispatchRevision: string,
    parentTransitionId: string,
    now: number
  ): AccountCircuitState[] {
    const relatedStates: AccountCircuitState[] = []
    for (const [index, scopeKey] of scopeKeys.entries()) {
      const child = this.entries.get(scopeKey)
      if (!child || child.state.phase === 'CLOSED' || child.state.dispatchRevision !== dispatchRevision) continue
      const childIncidentId = childIncidentIds[index]
      const currentIncidentId = child.state.incidentId ?? `${scopeKey}@${child.state.generation}`
      if (!childIncidentId || currentIncidentId !== childIncidentId || child.state.shadowedByIncidentId !== undefined) continue
      const transitionId = accountCircuitHierarchyTransitionId({
        action: 'shadow',
        parentTransitionId,
        parentIncidentId,
        childScopeKey: scopeKey,
        childGeneration: child.state.generation
      })
      child.state = { ...child.state, transitionId, shadowedByIncidentId: parentIncidentId, updatedAtMs: now }
      this.rememberReplay(child, transitionId)
      relatedStates.push(cloneAccountCircuitState(child.state))
    }
    return relatedStates
  }

  private unshadowChildren(
    scopeKeys: string[],
    childIncidentIds: string[],
    parentIncidentId: string,
    parentTransitionId: string,
    now: number
  ): AccountCircuitState[] {
    const relatedStates: AccountCircuitState[] = []
    for (const [index, scopeKey] of scopeKeys.entries()) {
      const child = this.entries.get(scopeKey)
      if (!child || child.state.shadowedByIncidentId !== parentIncidentId) continue
      const childIncidentId = childIncidentIds[index]
      const currentIncidentId = child.state.incidentId ?? `${scopeKey}@${child.state.generation}`
      if (!childIncidentId || currentIncidentId !== childIncidentId) continue
      const transitionId = accountCircuitHierarchyTransitionId({
        action: 'unshadow',
        parentTransitionId,
        parentIncidentId,
        childScopeKey: scopeKey,
        childGeneration: child.state.generation
      })
      child.state = { ...child.state, transitionId, shadowedByIncidentId: undefined, updatedAtMs: now }
      this.rememberReplay(child, transitionId)
      relatedStates.push(cloneAccountCircuitState(child.state))
    }
    return relatedStates
  }

  private projectParentRelationship(parentState: AccountCircuitState): AccountCircuitState[] {
    if (parentState.scope.kind !== 'account' || !parentState.incidentId) return []
    const childScopeKeys = parentState.childScopeKeys ?? []
    const childIncidentIds = parentState.childIncidentIds ?? []
    const relatedStates: AccountCircuitState[] = []
    for (const [index, scopeKey] of childScopeKeys.entries()) {
      const child = this.entries.get(scopeKey)
      if (!child || child.state.dispatchRevision !== parentState.dispatchRevision) continue
      const childIncidentId = childIncidentIds[index]
      const currentIncidentId = child.state.incidentId ?? `${scopeKey}@${child.state.generation}`
      if (
        !childIncidentId
        || currentIncidentId !== childIncidentId
        || child.state.updatedAtMs > parentState.updatedAtMs
      ) continue
      if (parentState.phase === 'CLOSED') {
        if (child.state.shadowedByIncidentId === parentState.incidentId) {
          const transitionId = accountCircuitHierarchyTransitionId({
            action: 'unshadow',
            parentTransitionId: parentState.transitionId,
            parentIncidentId: parentState.incidentId,
            childScopeKey: scopeKey,
            childGeneration: child.state.generation
          })
          child.state = {
            ...child.state,
            transitionId,
            shadowedByIncidentId: undefined,
            updatedAtMs: parentState.updatedAtMs
          }
          this.rememberReplay(child, transitionId)
          relatedStates.push(cloneAccountCircuitState(child.state))
        }
        continue
      }
      if (child.state.phase !== 'CLOSED' && child.state.shadowedByIncidentId === undefined) {
        const transitionId = accountCircuitHierarchyTransitionId({
          action: 'shadow',
          parentTransitionId: parentState.transitionId,
          parentIncidentId: parentState.incidentId,
          childScopeKey: scopeKey,
          childGeneration: child.state.generation
        })
        child.state = {
          ...child.state,
          transitionId,
          shadowedByIncidentId: parentState.incidentId,
          updatedAtMs: parentState.updatedAtMs
        }
        this.rememberReplay(child, transitionId)
        relatedStates.push(cloneAccountCircuitState(child.state))
      }
    }
    return relatedStates
  }

  private restoreCanaryOrigin(
    entry: MemoryAccountCircuitEntry,
    transitionId: string,
    now: number
  ): AccountCircuitMutationResult {
    const origin = entry.state.halfOpenOrigin ?? 'OPEN'
    const backoffAttempt = entry.state.backoffAttempt + 1
    return this.apply(entry, {
      ...entry.state,
      phase: origin,
      transitionId,
      backoffAttempt,
      lease: undefined,
      halfOpenOrigin: undefined,
      retryAtMs: now + accountCircuitBackoffDelayMs(
        backoffAttempt,
        `${entry.state.scopeKey}:${entry.state.generation}:${backoffAttempt}:unknown`
      ),
      updatedAtMs: now
    }, transitionId)
  }

  private freshEntry(scope: AccountCircuitScope, now: number): MemoryAccountCircuitEntry | undefined {
    const entry = this.entries.get(accountCircuitScopeKey(scope))
    if (!entry) return undefined
    if (entry.state.phase === 'CLOSED' && (entry.closedExpiresAtMs ?? 0) <= now) {
      this.entries.delete(entry.state.scopeKey)
      return undefined
    }
    this.normalizeExpiredLease(entry, now)
    entry.state = normalizeConfirmationState(entry.state)
    return entry
  }

  private normalizeExpiredLease(entry: MemoryAccountCircuitEntry, now: number): void {
    const lease = entry.state.lease
    if (!lease || lease.leaseUntilMs > now) return
    if (lease.kind === 'confirmation') {
      entry.state = { ...entry.state, lease: undefined, retryAtMs: now, updatedAtMs: now }
      return
    }
    entry.state = {
      ...entry.state,
      phase: entry.state.halfOpenOrigin ?? 'OPEN',
      lease: undefined,
      halfOpenOrigin: undefined,
      retryAtMs: now,
      updatedAtMs: now
    }
  }

  private reserveCapacity(now: number): boolean {
    this.cleanup(now)
    if (this.entries.size < this.capacity) {
      this.capacitySaturated = false
      return true
    }
    let oldestClosed: MemoryAccountCircuitEntry | undefined
    for (const entry of this.entries.values()) {
      if (entry.state.phase !== 'CLOSED') continue
      if (!oldestClosed || entry.state.updatedAtMs < oldestClosed.state.updatedAtMs) oldestClosed = entry
    }
    if (!oldestClosed) {
      this.capacitySaturated = true
      return false
    }
    this.entries.delete(oldestClosed.state.scopeKey)
    this.capacitySaturated = false
    return true
  }

  private cleanup(now: number): void {
    for (const [scopeKey, entry] of this.entries) {
      if (entry.state.phase === 'CLOSED' && (entry.closedExpiresAtMs ?? 0) <= now) {
        this.entries.delete(scopeKey)
      } else {
        this.normalizeExpiredLease(entry, now)
      }
    }
    if (this.entries.size < this.capacity) this.capacitySaturated = false
  }

  private newEntry(state: AccountCircuitState): MemoryAccountCircuitEntry {
    const entry: MemoryAccountCircuitEntry = {
      state,
      replayOrder: [],
      replayIds: new Set<string>()
    }
    this.entries.set(state.scopeKey, entry)
    return entry
  }

  private apply(
    entry: MemoryAccountCircuitEntry,
    state: AccountCircuitState,
    transitionId: string,
    closedExpiresAtMs?: number
  ): AccountCircuitMutationResult {
    requiredValue(transitionId, 'transitionId')
    entry.state = state
    entry.closedExpiresAtMs = closedExpiresAtMs
    this.rememberReplay(entry, transitionId)
    return result('applied', state)
  }

  private idempotentResult(
    entry: MemoryAccountCircuitEntry | undefined,
    transitionId: string
  ): AccountCircuitMutationResult | undefined {
    requiredValue(transitionId, 'transitionId')
    return entry?.replayIds.has(transitionId) ? result('idempotent', entry.state) : undefined
  }

  private rememberReplay(entry: MemoryAccountCircuitEntry, transitionId: string): void {
    if (entry.replayIds.has(transitionId)) return
    entry.replayIds.add(transitionId)
    entry.replayOrder.push(transitionId)
    while (entry.replayOrder.length > this.replayLimitPerScope) {
      const oldest = entry.replayOrder.shift()
      if (oldest) entry.replayIds.delete(oldest)
    }
  }
}

function runtimeKeyMatchesDispatchRevisionTarget(runtimeKey: string, target: string): boolean {
  return runtimeKey === target || (!target.includes(':authorized:') && runtimeKey.startsWith(`${target}:authorized:`))
}

function isOlderNumericDispatchRevision(candidate: string, current: string): boolean {
  const candidateNumber = Number(candidate)
  const currentNumber = Number(current)
  return Number.isSafeInteger(candidateNumber)
    && candidateNumber > 0
    && Number.isSafeInteger(currentNumber)
    && currentNumber > candidateNumber
}

function accountCircuitLease(
  kind: AccountCircuitLease['kind'],
  leaseId: string,
  leaseUntilMs: number,
  now: number
): AccountCircuitLease {
  const normalizedLeaseId = requiredValue(leaseId, 'leaseId')
  const normalizedLeaseUntilMs = normalizedNow(leaseUntilMs)
  if (normalizedLeaseUntilMs <= now) throw new Error('账户电路租约截止时间必须晚于当前时间')
  return { kind, leaseId: normalizedLeaseId, leaseUntilMs: normalizedLeaseUntilMs }
}

function result(
  status: AccountCircuitMutationResult['status'],
  state: AccountCircuitState,
  relatedStates: AccountCircuitState[] = []
): AccountCircuitMutationResult {
  return {
    status,
    state: cloneAccountCircuitState(state),
    ...(relatedStates.length > 0 ? { relatedStates: relatedStates.map(cloneAccountCircuitState) } : {})
  }
}

function escalationResult(
  status: AccountCircuitEscalationResult['status'],
  accountState: AccountCircuitState,
  protocolScopeCount: number,
  confirmedFailureCount: number,
  relatedStates: AccountCircuitState[] = []
): AccountCircuitEscalationResult {
  return {
    status,
    accountState: cloneAccountCircuitState(accountState),
    protocolScopeCount,
    confirmedFailureCount,
    ...(relatedStates.length > 0 ? { relatedStates: relatedStates.map(cloneAccountCircuitState) } : {})
  }
}

function totalConfirmedFailures(scopes: MemoryAccountCircuitEscalationScopeEvidence[]): number {
  return scopes.reduce((total, item) => total + item.confirmedFailureCount, 0)
}

function requiredValue(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`账户电路操作缺少 ${name}`)
  return normalized
}

function normalizedNow(value: number): number {
  if (!Number.isFinite(value)) throw new Error('账户电路时间必须是有限数值')
  return Math.max(0, Math.trunc(value))
}

function positiveInteger(value: number, name: string): number {
  if (!Number.isFinite(value) || value < 1) throw new Error(`账户电路 ${name} 必须是正整数`)
  return Math.trunc(value)
}

function accountCircuitDueAtMs(state: AccountCircuitState): number {
  if (state.phase === 'CLOSED') return Number.POSITIVE_INFINITY
  if (state.lease) return state.lease.leaseUntilMs
  if (state.phase === 'SUSPECT' || state.phase === 'OPEN' || state.phase === 'RECOVERING') {
    return state.retryAtMs ?? Number.POSITIVE_INFINITY
  }
  return Number.POSITIVE_INFINITY
}

function normalizeConfirmationState(state: AccountCircuitState): AccountCircuitState {
  if (state.phase === 'CLOSED') return state
  return {
    ...state,
    confirmationFailuresRequired: normalizeAccountCircuitConfirmationFailuresRequired(
      state.confirmationFailuresRequired
    ),
    confirmationFailureCount: accountCircuitConfirmationFailureCount(state),
    failureEvidenceKeys: accountCircuitFailureEvidenceKeys(state),
    ...(state.phase === 'SUSPECT' && state.retryAtMs === undefined
      ? { retryAtMs: state.lease?.leaseUntilMs ?? state.updatedAtMs }
      : {})
  }
}
