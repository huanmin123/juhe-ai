import {
  accountCircuitBackoffDelayMs,
  accountCircuitRecoverySuccessThreshold,
  accountCircuitScopeKey,
  cloneAccountCircuitState,
  closedAccountCircuitState,
  type AccountCircuitLease,
  type AccountCircuitMutationResult,
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

export class MemoryAccountCircuitStore implements AccountCircuitStore {
  private readonly entries = new Map<string, MemoryAccountCircuitEntry>()
  private readonly capacity: number
  private readonly closedRetentionMs: number
  private readonly replayLimitPerScope: number
  private readonly now: () => number

  constructor(options: MemoryAccountCircuitStoreOptions) {
    this.capacity = positiveInteger(options.capacity, 'capacity')
    this.closedRetentionMs = positiveInteger(options.closedRetentionMs ?? 5 * 60_000, 'closedRetentionMs')
    this.replayLimitPerScope = positiveInteger(options.replayLimitPerScope ?? 64, 'replayLimitPerScope')
    this.now = options.now ?? Date.now
  }

  async get(scope: AccountCircuitScope, nowMs = this.now()): Promise<AccountCircuitState> {
    const entry = this.freshEntry(scope, nowMs)
    return cloneAccountCircuitState(entry?.state ?? closedAccountCircuitState(scope))
  }

  async suspect(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    reason: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult> {
    const now = normalizedNow(input.nowMs ?? this.now())
    const existing = this.freshEntry(input.scope, now)
    const replay = this.idempotentResult(existing, input.transitionId)
    if (replay) return replay
    if (existing && existing.state.phase !== 'CLOSED') return result('state_mismatch', existing.state)
    if (!existing && !this.reserveCapacity(now)) {
      return result('capacity_exhausted', closedAccountCircuitState(input.scope))
    }
    const generation = (existing?.state.generation ?? 0) + 1
    const state: AccountCircuitState = {
      scopeKey: accountCircuitScopeKey(input.scope),
      scope: { ...input.scope },
      phase: 'SUSPECT',
      generation,
      dispatchRevision: requiredValue(input.dispatchRevision, 'dispatchRevision'),
      transitionId: requiredValue(input.transitionId, 'transitionId'),
      backoffAttempt: 0,
      recoverySuccessCount: 0,
      failureReason: input.reason,
      updatedAtMs: now
    }
    const entry = existing ?? this.newEntry(state)
    return this.apply(entry, state, input.transitionId)
  }

  async acquireConfirmationLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
  }): Promise<AccountCircuitMutationResult> {
    return this.acquireLease(input, 'SUSPECT', 'confirmation')
  }

  async completeConfirmation(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
  }): Promise<AccountCircuitMutationResult> {
    const checked = this.checkedEntry(input, 'SUSPECT', 'confirmation', input.leaseId)
    if ('result' in checked) return checked.result
    const { entry, now } = checked
    if (input.outcome === 'framing_complete') {
      return this.close(entry, input.transitionId, now)
    }
    if (input.outcome === 'unknown') {
      return this.apply(entry, {
        ...entry.state,
        transitionId: input.transitionId,
        lease: undefined,
        updatedAtMs: now
      }, input.transitionId)
    }
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
    if (entry.state.phase === 'OPEN' && (entry.state.retryAtMs ?? Number.POSITIVE_INFINITY) > now) {
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
    const recoverySuccessCount = entry.state.recoverySuccessCount + 1
    if (recoverySuccessCount >= accountCircuitRecoverySuccessThreshold) {
      return this.close(entry, input.transitionId, now)
    }
    return this.apply(entry, {
      ...entry.state,
      phase: 'RECOVERING',
      transitionId: input.transitionId,
      recoverySuccessCount,
      lease: undefined,
      halfOpenOrigin: undefined,
      retryAtMs: now,
      updatedAtMs: now
    }, input.transitionId)
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
    if (!entry && !this.reserveCapacity(now)) {
      return result('capacity_exhausted', closedAccountCircuitState(input.scope))
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
    input: AccountCircuitTransitionIdentity & { leaseId: string; leaseUntilMs: number },
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
    if (entry.state.phase !== expectedPhase || entry.state.lease) return result('state_mismatch', entry.state)
    return this.apply(entry, {
      ...entry.state,
      transitionId: input.transitionId,
      lease: accountCircuitLease(kind, input.leaseId, input.leaseUntilMs, now),
      updatedAtMs: now
    }, input.transitionId)
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
      openedAtMs: now,
      retryAtMs: now + accountCircuitBackoffDelayMs(backoffAttempt),
      failureReason: reason ?? entry.state.failureReason,
      lease: undefined,
      halfOpenOrigin: undefined,
      updatedAtMs: now
    }, transitionId)
  }

  private close(entry: MemoryAccountCircuitEntry, transitionId: string, now: number): AccountCircuitMutationResult {
    return this.apply(entry, {
      ...closedAccountCircuitState(
        entry.state.scope,
        entry.state.dispatchRevision,
        entry.state.generation,
        transitionId,
        now
      )
    }, transitionId, now + this.closedRetentionMs)
  }

  private restoreCanaryOrigin(
    entry: MemoryAccountCircuitEntry,
    transitionId: string,
    now: number
  ): AccountCircuitMutationResult {
    const origin = entry.state.halfOpenOrigin ?? 'OPEN'
    return this.apply(entry, {
      ...entry.state,
      phase: origin,
      transitionId,
      lease: undefined,
      halfOpenOrigin: undefined,
      retryAtMs: now,
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
    return entry
  }

  private normalizeExpiredLease(entry: MemoryAccountCircuitEntry, now: number): void {
    const lease = entry.state.lease
    if (!lease || lease.leaseUntilMs > now) return
    if (lease.kind === 'confirmation') {
      entry.state = { ...entry.state, lease: undefined, updatedAtMs: now }
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
    if (this.entries.size < this.capacity) return true
    let oldestClosed: MemoryAccountCircuitEntry | undefined
    for (const entry of this.entries.values()) {
      if (entry.state.phase !== 'CLOSED') continue
      if (!oldestClosed || entry.state.updatedAtMs < oldestClosed.state.updatedAtMs) oldestClosed = entry
    }
    if (!oldestClosed) return false
    this.entries.delete(oldestClosed.state.scopeKey)
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

function result(status: AccountCircuitMutationResult['status'], state: AccountCircuitState): AccountCircuitMutationResult {
  return { status, state: cloneAccountCircuitState(state) }
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
  if (state.phase === 'OPEN' || state.phase === 'RECOVERING') {
    return state.retryAtMs ?? Number.POSITIVE_INFINITY
  }
  return state.updatedAtMs
}
