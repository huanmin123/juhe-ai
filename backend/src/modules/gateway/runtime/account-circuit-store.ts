export type AccountCircuitPhase = 'CLOSED' | 'SUSPECT' | 'OPEN' | 'HALF_OPEN' | 'RECOVERING'

export type AccountCircuitScope =
  | { kind: 'account'; accountRuntimeKey: string }
  | { kind: 'key'; accountRuntimeKey: string; keyFingerprint: string }
  | {
      kind: 'protocol_model'
      accountRuntimeKey: string
      protocolProfile: string
      requestLane: 'text' | 'image'
      modelBucket: string
    }

export type AccountCircuitLeaseKind = 'confirmation' | 'half_open' | 'recovery'

export interface AccountCircuitLease {
  kind: AccountCircuitLeaseKind
  leaseId: string
  leaseUntilMs: number
}

export interface AccountCircuitState {
  scopeKey: string
  scope: AccountCircuitScope
  phase: AccountCircuitPhase
  generation: number
  dispatchRevision: string
  transitionId: string
  backoffAttempt: number
  recoverySuccessCount: number
  openedAtMs?: number
  retryAtMs?: number
  failureReason?: string
  lease?: AccountCircuitLease
  halfOpenOrigin?: 'OPEN' | 'RECOVERING'
  updatedAtMs: number
}

export type AccountCircuitMutationStatus =
  | 'applied'
  | 'idempotent'
  | 'not_found'
  | 'state_mismatch'
  | 'stale_generation'
  | 'stale_dispatch_revision'
  | 'lease_mismatch'
  | 'not_due'
  | 'capacity_exhausted'

export interface AccountCircuitMutationResult {
  status: AccountCircuitMutationStatus
  state: AccountCircuitState
}

export interface AccountCircuitTransitionIdentity {
  scope: AccountCircuitScope
  generation: number
  dispatchRevision: string
  transitionId: string
  nowMs?: number
}

export interface AccountCircuitStore {
  get(scope: AccountCircuitScope, nowMs?: number): Promise<AccountCircuitState>
  suspect(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    reason: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult>
  acquireConfirmationLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
  }): Promise<AccountCircuitMutationResult>
  completeConfirmation(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
  }): Promise<AccountCircuitMutationResult>
  acquireCanaryLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
  }): Promise<AccountCircuitMutationResult>
  completeCanary(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
  }): Promise<AccountCircuitMutationResult>
  replaceDispatchRevision(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult>
  listDue(nowMs: number, limit: number): Promise<AccountCircuitState[]>
  size(): Promise<number>
}

export const accountCircuitBackoffMs = [3_000, 5_000, 10_000, 30_000, 60_000] as const
export const accountCircuitRecoverySuccessThreshold = 3

export function accountCircuitBackoffDelayMs(attempt: number): number {
  const index = Math.min(accountCircuitBackoffMs.length - 1, Math.max(0, Math.trunc(attempt) - 1))
  return accountCircuitBackoffMs[index]!
}

export function accountCircuitScopeKey(scope: AccountCircuitScope): string {
  const accountRuntimeKey = requiredScopePart(scope.accountRuntimeKey, 'accountRuntimeKey')
  if (scope.kind === 'account') return encodedScopeKey(['account', accountRuntimeKey])
  if (scope.kind === 'key') {
    return encodedScopeKey(['key', accountRuntimeKey, requiredScopePart(scope.keyFingerprint, 'keyFingerprint')])
  }
  return encodedScopeKey([
    'protocol_model',
    accountRuntimeKey,
    requiredScopePart(scope.protocolProfile, 'protocolProfile'),
    requiredRequestLane(scope.requestLane),
    requiredScopePart(scope.modelBucket, 'modelBucket')
  ])
}

export function closedAccountCircuitState(
  scope: AccountCircuitScope,
  dispatchRevision = '',
  generation = 0,
  transitionId = '',
  updatedAtMs = 0
): AccountCircuitState {
  return {
    scopeKey: accountCircuitScopeKey(scope),
    scope: cloneAccountCircuitScope(scope),
    phase: 'CLOSED',
    generation,
    dispatchRevision,
    transitionId,
    backoffAttempt: 0,
    recoverySuccessCount: 0,
    updatedAtMs
  }
}

export function cloneAccountCircuitState(state: AccountCircuitState): AccountCircuitState {
  return {
    ...state,
    scope: cloneAccountCircuitScope(state.scope),
    lease: state.lease ? { ...state.lease } : undefined
  }
}

function cloneAccountCircuitScope(scope: AccountCircuitScope): AccountCircuitScope {
  return { ...scope }
}

function requiredScopePart(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`账户电路作用域缺少 ${name}`)
  return normalized
}

function requiredRequestLane(value: 'text' | 'image'): 'text' | 'image' {
  if (value !== 'text' && value !== 'image') throw new Error('账户电路作用域 requestLane 必须是 text 或 image')
  return value
}

function encodedScopeKey(parts: string[]): string {
  return parts.map((part) => `${Buffer.byteLength(part, 'utf8')}:${part}`).join('|')
}
