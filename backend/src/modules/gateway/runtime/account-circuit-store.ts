import { createHash } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'

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
  confirmationFailuresRequired?: number
  confirmationFailureCount?: number
  failureEvidenceKeys?: string[]
  openedAtMs?: number
  retryAtMs?: number
  failureReason?: string
  lease?: AccountCircuitLease
  halfOpenOrigin?: 'OPEN' | 'RECOVERING'
  incidentId?: string
  shadowedByIncidentId?: string
  childIncidentIds?: string[]
  childScopeKeys?: string[]
  requiredRecoveryScopeKeys?: string[]
  recoveryEvidenceScopeKeys?: string[]
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
  relatedStates?: AccountCircuitState[]
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
    confirmationFailuresRequired?: number
    failureEvidenceKey?: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult>
  acquireConfirmationLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
    expectedFailureEvidenceKey?: string
    confirmationEvidenceKey?: string
  }): Promise<AccountCircuitMutationResult>
  closeSuspectFromObserver(input: AccountCircuitTransitionIdentity & {
    expectedFailureEvidenceKey: string
    observerEvidenceKey: string
  }): Promise<AccountCircuitMutationResult>
  closeSuspectFromKeyRotation(input: AccountCircuitTransitionIdentity & {
    expectedFailureEvidenceKey: string
  }): Promise<AccountCircuitMutationResult>
  completeConfirmation(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
    failureEvidenceKey?: string
    framingCompleteDisposition?: 'recovering' | 'closed'
  }): Promise<AccountCircuitMutationResult>
  acquireCanaryLease(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    leaseUntilMs: number
  }): Promise<AccountCircuitMutationResult>
  completeCanary(input: AccountCircuitTransitionIdentity & {
    leaseId: string
    outcome: 'framing_complete' | 'transport_failure' | 'unknown'
    reason?: string
    evidenceScopeKey?: string
  }): Promise<AccountCircuitMutationResult>
  recordProtocolModelOpenEvidence(input: AccountCircuitProtocolModelOpenEvidenceInput): Promise<AccountCircuitEscalationResult>
  clearAccountEscalationEvidence(input: {
    accountRuntimeKey: string
    dispatchRevision: string
    evidenceId: string
    nowMs?: number
  }): Promise<boolean>
  replaceDispatchRevision(input: {
    scope: AccountCircuitScope
    dispatchRevision: string
    transitionId: string
    nowMs?: number
  }): Promise<AccountCircuitMutationResult>
  restore(state: AccountCircuitState, nowMs?: number): Promise<AccountCircuitMutationResult>
  replaceAccountDispatchRevision(input: {
    accountRuntimeKey: string
    dispatchRevision: string
    transitionId: string
    nowMs?: number
  }): Promise<number>
  listDue(nowMs: number, limit: number): Promise<AccountCircuitState[]>
  size(): Promise<number>
}

export interface AccountCircuitProtocolModelOpenEvidenceInput {
  scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>
  generation: number
  dispatchRevision: string
  evidenceId: string
  accountTransitionId: string
  reason: string
  confirmedFailureCount: number
  distinctScopeThreshold: number
  windowMs: number
  maxProtocolScopes: number
  nowMs?: number
}

export type AccountCircuitEscalationStatus =
  | 'recorded'
  | 'escalated'
  | 'already_active'
  | 'idempotent'
  | 'not_found'
  | 'state_mismatch'
  | 'stale_generation'
  | 'stale_dispatch_revision'
  | 'capacity_exhausted'

export interface AccountCircuitEscalationResult {
  status: AccountCircuitEscalationStatus
  accountState: AccountCircuitState
  protocolScopeCount: number
  confirmedFailureCount: number
  relatedStates?: AccountCircuitState[]
}

export const accountCircuitBackoffMs = runtimeConfig.gateway.accountCircuitBackoffMs
export const accountCircuitRecoverySuccessThreshold = runtimeConfig.gateway.accountCircuitRecoverySuccessThreshold
export const accountCircuitRecoveryCanaryIntervalMs = runtimeConfig.gateway.accountCircuitRecoveryCanaryIntervalMs
export const accountCircuitSuspectConfirmationIntervalMs = runtimeConfig.gateway.accountCircuitSuspectConfirmationIntervalMs
export const accountCircuitDefaultConfirmationFailuresRequired = 2
export const accountCircuitLegacyConfirmationFailuresRequired = 1
export const accountCircuitConfirmationFailuresRequiredMin = 1
export const accountCircuitConfirmationFailuresRequiredMax = 5
export const accountCircuitEscalationDistinctScopeThresholdDefault = 3
export const accountCircuitEscalationDistinctScopeThresholdMin = 3
export const accountCircuitEscalationDistinctScopeThresholdMax = 64
export const accountCircuitEscalationWindowMsDefault = 10 * 60_000
export const accountCircuitEscalationWindowMsMin = 60_000
export const accountCircuitEscalationWindowMsMax = 24 * 60 * 60_000

export function accountCircuitBackoffDelayMs(attempt: number, jitterSeed?: string): number {
  const index = Math.min(accountCircuitBackoffMs.length - 1, Math.max(0, Math.trunc(attempt) - 1))
  const base = accountCircuitBackoffMs[index]!
  if (index < 4 || !jitterSeed?.trim()) return base
  const sample = Number.parseInt(createHash('sha1').update(jitterSeed).digest('hex').slice(0, 8), 16) / 0xffff_ffff
  return Math.max(1, Math.round(base * (0.8 + sample * 0.4)))
}

export function capacityExhaustedAccountCircuitState(
  scope: AccountCircuitScope,
  dispatchRevision = '',
  nowMs = Date.now()
): AccountCircuitState {
  return {
    ...closedAccountCircuitState(scope, dispatchRevision, 0, 'runtime-capacity-exhausted', nowMs),
    phase: 'SUSPECT',
    failureReason: 'runtime_state_capacity_exhausted',
    retryAtMs: nowMs + 1_000
  }
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

export function accountCircuitHierarchyTransitionId(input: {
  action: 'shadow' | 'unshadow'
  parentTransitionId: string
  parentIncidentId: string
  childScopeKey: string
  childGeneration: number
}): string {
  const parentTransitionId = requiredScopePart(input.parentTransitionId, 'parentTransitionId')
  const parentIncidentId = requiredScopePart(input.parentIncidentId, 'parentIncidentId')
  const childScopeKey = requiredScopePart(input.childScopeKey, 'childScopeKey')
  if (!Number.isSafeInteger(input.childGeneration) || input.childGeneration < 0) {
    throw new Error('账户电路 hierarchy childGeneration 无效')
  }
  const digest = createHash('sha1')
    .update(input.action)
    .update('\0')
    .update(parentTransitionId)
    .update('\0')
    .update(parentIncidentId)
    .update('\0')
    .update(childScopeKey)
    .update('\0')
    .update(String(input.childGeneration))
    .digest('hex')
  return `hierarchy:${input.action}:${digest}`
}

export function assertAccountCircuitStateScopeKey(state: Pick<AccountCircuitState, 'scope' | 'scopeKey'>): void {
  const expected = accountCircuitScopeKey(state.scope)
  if (state.scopeKey !== expected) {
    throw new Error('账户电路 scopeKey 与作用域字段不一致')
  }
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
    lease: state.lease ? { ...state.lease } : undefined,
    failureEvidenceKeys: cloneStringArray(state.failureEvidenceKeys),
    childIncidentIds: cloneStringArray(state.childIncidentIds),
    childScopeKeys: cloneStringArray(state.childScopeKeys),
    requiredRecoveryScopeKeys: cloneStringArray(state.requiredRecoveryScopeKeys),
    recoveryEvidenceScopeKeys: cloneStringArray(state.recoveryEvidenceScopeKeys)
  }
}

export function normalizeAccountCircuitConfirmationFailuresRequired(
  value: unknown,
  fallback = accountCircuitLegacyConfirmationFailuresRequired
): number {
  const normalized = value === undefined || value === null ? fallback : value
  if (
    typeof normalized !== 'number'
    || !Number.isSafeInteger(normalized)
    ||
    normalized < accountCircuitConfirmationFailuresRequiredMin
    || normalized > accountCircuitConfirmationFailuresRequiredMax
  ) {
    throw new Error(
      `账户电路 confirmationFailuresRequired 必须是 ${accountCircuitConfirmationFailuresRequiredMin}..${accountCircuitConfirmationFailuresRequiredMax} 的整数`
    )
  }
  return normalized
}

export function normalizeAccountCircuitEscalationDistinctScopeThreshold(
  value: unknown,
  fallback = accountCircuitEscalationDistinctScopeThresholdDefault
): number {
  const normalized = value === undefined || value === null ? fallback : value
  if (
    typeof normalized !== 'number'
    || !Number.isSafeInteger(normalized)
    || normalized < accountCircuitEscalationDistinctScopeThresholdMin
    || normalized > accountCircuitEscalationDistinctScopeThresholdMax
  ) {
    throw new Error(
      `账户电路 distinctScopeThreshold 必须是 ${accountCircuitEscalationDistinctScopeThresholdMin}..${accountCircuitEscalationDistinctScopeThresholdMax} 的整数`
    )
  }
  return normalized
}

export function normalizeAccountCircuitEscalationWindowMs(
  value: unknown,
  fallback = accountCircuitEscalationWindowMsDefault
): number {
  const normalized = value === undefined || value === null ? fallback : value
  if (
    typeof normalized !== 'number'
    || !Number.isSafeInteger(normalized)
    || normalized < accountCircuitEscalationWindowMsMin
    || normalized > accountCircuitEscalationWindowMsMax
  ) {
    throw new Error(
      `账户电路 escalationWindowMs 必须是 ${accountCircuitEscalationWindowMsMin}..${accountCircuitEscalationWindowMsMax} 的整数毫秒值`
    )
  }
  return normalized
}

export function normalizeAccountCircuitFailureEvidenceKey(value: unknown, fallbackSeed: string): string {
  const normalized = typeof value === 'string' ? value.trim().toLowerCase() : ''
  if (/^[a-f0-9]{64}$/.test(normalized)) return normalized
  const seed = fallbackSeed.trim()
  if (!seed) throw new Error('账户电路 failure evidence 缺少 fallbackSeed')
  return createHash('sha256').update(seed).digest('hex')
}

export function accountCircuitConfirmationFailureCount(state: AccountCircuitState): number {
  const value = state.confirmationFailureCount
  if (value === undefined) return 0
  if (!Number.isSafeInteger(value) || value < 0 || value > accountCircuitConfirmationFailuresRequiredMax) {
    throw new Error('账户电路 confirmationFailureCount 无效')
  }
  return value
}

export function accountCircuitFailureEvidenceKeys(state: AccountCircuitState): string[] {
  const required = normalizeAccountCircuitConfirmationFailuresRequired(state.confirmationFailuresRequired)
  const values = cloneStringArray(state.failureEvidenceKeys) ?? []
  const normalized = values
    .map((value) => value.trim().toLowerCase())
    .filter((value) => /^[a-f0-9]{64}$/.test(value))
  return [...new Set(normalized)].slice(-(required + 1))
}

function cloneStringArray(value: unknown): string[] | undefined {
  if (value === undefined || value === null) return undefined
  if (Array.isArray(value)) return value.filter((item): item is string => typeof item === 'string').slice()
  if (typeof value !== 'object') return undefined
  return Object.entries(value as Record<string, unknown>)
    .sort(([left], [right]) => Number(left) - Number(right))
    .map(([, item]) => item)
    .filter((item): item is string => typeof item === 'string')
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
