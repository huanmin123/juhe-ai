import type { RouteStrategyMode } from '../../../domain/types.js'

export const defaultGatewayRequestWallBudgetMs = 270_000
export const defaultGatewayFinalResponseReserveMs = 2_000
export const defaultRouteCoordinationBudgetMs = 3_000

export type ClientHandoffReason =
  | 'gateway_request_wall_budget_exhausted'
  | 'precommit_budget_exhausted'
  | 'server_retry_wait_budget_exhausted'

export interface GatewayRequestAttemptSnapshot {
  attemptedAccountRuntimeKeys: readonly string[]
  attemptedPhysicalCredentialKeys: readonly string[]
  attemptedKeyFingerprints: readonly string[]
  attemptedProtocolModelKeys: readonly string[]
}

export interface GatewayDispatchAttemptIdentity {
  protocolModelKey: string
  accountRuntimeKey: string
  physicalCredentialKey: string
  keyFingerprint?: string
}

export type GatewayDispatchAttemptRejectionReason =
  | 'account_runtime_already_attempted'
  | 'physical_credential_already_attempted'
  | 'key_fingerprint_already_attempted'
  | 'protocol_model_already_attempted'
  | 'confirmation_already_attempted'
  | 'key_rotation_not_applicable'

export type GatewayDispatchAttemptRegistration =
  | { allowed: true }
  | { allowed: false; reason: GatewayDispatchAttemptRejectionReason }

export type RouteCoordinationResult<TAccount> =
  | {
      outcome: 'dispatchable'
      accounts: readonly TAccount[]
    }
  | {
      outcome: 'temporarily_blocked'
      reason: string
      earliestRetryAtMs?: number
      confirmationInFlight: boolean
      blockedAccountIds: readonly string[]
      waitableByCurrentRequest: boolean
      leaseSource?: 'self_request' | 'capacity_event'
      wakeSource?: 'capacity_event'
      foreignLeaseInFlight: boolean
    }
  | {
      outcome: 'hard_exhausted'
      reason: string
    }
  | {
      outcome: 'request_exhausted'
      reason: string
      attempts: GatewayRequestAttemptSnapshot
    }
  | {
      outcome: 'client_handoff'
      reason: ClientHandoffReason
      remainingUntriedCandidatesPossible: boolean
      wallRemainingMs: number
      serverRetryRemainingMs: number
    }

export interface GatewayRequestWallBudgetOptions {
  requestAcceptedAtMs: number
  budgetMs?: number
  now?: () => number
}

export interface GatewayRequestWallBudgetDecision {
  nowMs?: number
  finalResponseReserveMs?: number
  minimumMeaningfulAttemptMs?: number
}

export interface GatewayRequestPrecommitBudgetInput {
  nowMs?: number
  requestPrecommitDeadlineAtMs?: number
  finalResponseReserveMs?: number
}

export interface GatewayFirstByteDeadlineClipInput extends GatewayRequestPrecommitBudgetInput {
  firstByteDeadlineMs: number
  uncommittedAttemptDeadlineAtMs?: number
}

export class GatewayRequestWallBudget {
  readonly requestAcceptedAtMs: number
  readonly budgetMs: number
  readonly deadlineAtMs: number
  private readonly now: () => number

  constructor(options: GatewayRequestWallBudgetOptions) {
    this.requestAcceptedAtMs = normalizedTimestamp(options.requestAcceptedAtMs)
    this.budgetMs = normalizedPositiveMs(options.budgetMs, defaultGatewayRequestWallBudgetMs)
    this.deadlineAtMs = this.requestAcceptedAtMs + this.budgetMs
    this.now = options.now ?? Date.now
  }

  elapsedMs(nowMs = this.now()): number {
    return Math.max(0, normalizedTimestamp(nowMs) - this.requestAcceptedAtMs)
  }

  remainingMs(nowMs = this.now()): number {
    return Math.max(0, this.deadlineAtMs - normalizedTimestamp(nowMs))
  }

  availableDecisionMs(input: GatewayRequestWallBudgetDecision = {}): number {
    const reserveMs = normalizedFinalResponseReserveMs(input.finalResponseReserveMs)
    return Math.max(0, this.remainingMs(input.nowMs) - reserveMs)
  }

  handoffRequired(input: GatewayRequestWallBudgetDecision = {}): boolean {
    const meaningfulAttemptMs = normalizedNonNegativeMs(input.minimumMeaningfulAttemptMs)
    return this.availableDecisionMs(input) <= meaningfulAttemptMs
  }

  precommitRemainingMs(input: GatewayRequestPrecommitBudgetInput = {}): number {
    const nowMs = normalizedTimestamp(input.nowMs ?? this.now())
    const reserveMs = normalizedFinalResponseReserveMs(input.finalResponseReserveMs)
    const precommitDeadlineAtMs = normalizedOptionalTimestamp(input.requestPrecommitDeadlineAtMs)
      ?? this.deadlineAtMs
    return Math.max(
      0,
      Math.min(this.deadlineAtMs, precommitDeadlineAtMs) - nowMs - reserveMs
    )
  }

  clipFirstByteDeadlineMs(input: GatewayFirstByteDeadlineClipInput): number {
    const nowMs = normalizedTimestamp(input.nowMs ?? this.now())
    const candidates = [
      normalizedNonNegativeMs(input.firstByteDeadlineMs),
      this.precommitRemainingMs({ ...input, nowMs })
    ]
    const uncommittedAttemptDeadlineAtMs = normalizedOptionalTimestamp(input.uncommittedAttemptDeadlineAtMs)
    if (uncommittedAttemptDeadlineAtMs !== undefined) {
      candidates.push(Math.max(0, uncommittedAttemptDeadlineAtMs - nowMs))
    }
    return Math.max(0, Math.min(...candidates))
  }
}

export interface RouteCoordinationBudgetSnapshot {
  requestId: string
  budgetId: string
  version: number
  remainingMs: number
  activeSinceMs?: number
  lastWaitToken?: string
}

export interface RouteCoordinationBudgetOptions {
  requestId: string
  budgetId?: string
  budgetMs?: number
  now?: () => number
}

export interface RouteCoordinationBudgetTransitionInput {
  waitToken: string
  expectedVersion: number
  nowMs?: number
}

export type RouteCoordinationBudgetTransitionResult = {
  outcome: 'applied' | 'idempotent_replay' | 'version_conflict' | 'invalid_transition'
  snapshot: RouteCoordinationBudgetSnapshot
}

export class RouteCoordinationBudget {
  readonly requestId: string
  readonly budgetId: string
  readonly budgetMs: number
  private version = 0
  private storedRemainingMs: number
  private activeSinceMs: number | undefined
  private lastWaitToken: string | undefined
  private readonly observedWaitTokens = new Set<string>()
  private readonly completedWaitTokens = new Set<string>()
  private readonly now: () => number

  constructor(options: RouteCoordinationBudgetOptions) {
    this.requestId = normalizedRequiredKey(options.requestId)
    this.budgetId = normalizedOptionalKey(options.budgetId) ?? `${this.requestId}:route-coordination`
    this.budgetMs = normalizedPositiveMs(options.budgetMs, defaultRouteCoordinationBudgetMs)
    this.storedRemainingMs = this.budgetMs
    this.now = options.now ?? Date.now
  }

  remainingMs(nowMs = this.now()): number {
    const now = normalizedTimestamp(nowMs)
    if (this.activeSinceMs === undefined) return this.storedRemainingMs
    return Math.max(0, this.storedRemainingMs - Math.max(0, now - this.activeSinceMs))
  }

  exhausted(nowMs = this.now()): boolean {
    return this.remainingMs(nowMs) <= 0
  }

  snapshot(nowMs = this.now()): RouteCoordinationBudgetSnapshot {
    return Object.freeze({
      requestId: this.requestId,
      budgetId: this.budgetId,
      version: this.version,
      remainingMs: this.remainingMs(nowMs),
      activeSinceMs: this.activeSinceMs,
      lastWaitToken: this.lastWaitToken
    })
  }

  beginWait(input: RouteCoordinationBudgetTransitionInput): RouteCoordinationBudgetTransitionResult {
    const waitToken = normalizedRequiredKey(input.waitToken)
    const nowMs = normalizedTimestamp(input.nowMs ?? this.now())
    if (this.observedWaitTokens.has(waitToken)) {
      return this.transitionResult('idempotent_replay', nowMs)
    }
    if (normalizedVersion(input.expectedVersion) !== this.version) {
      return this.transitionResult('version_conflict', nowMs)
    }
    if (this.activeSinceMs !== undefined || this.remainingMs(nowMs) <= 0) {
      return this.transitionResult('invalid_transition', nowMs)
    }

    this.observedWaitTokens.add(waitToken)
    this.lastWaitToken = waitToken
    this.activeSinceMs = nowMs
    this.version += 1
    return this.transitionResult('applied', nowMs)
  }

  pauseWait(input: RouteCoordinationBudgetTransitionInput): RouteCoordinationBudgetTransitionResult {
    const waitToken = normalizedRequiredKey(input.waitToken)
    const nowMs = normalizedTimestamp(input.nowMs ?? this.now())
    if (this.completedWaitTokens.has(waitToken)) {
      return this.transitionResult('idempotent_replay', nowMs)
    }
    if (normalizedVersion(input.expectedVersion) !== this.version) {
      return this.transitionResult('version_conflict', nowMs)
    }
    if (this.activeSinceMs === undefined || this.lastWaitToken !== waitToken) {
      return this.transitionResult('invalid_transition', nowMs)
    }

    this.storedRemainingMs = this.remainingMs(nowMs)
    this.activeSinceMs = undefined
    this.completedWaitTokens.add(waitToken)
    this.version += 1
    return this.transitionResult('applied', nowMs)
  }

  private transitionResult(
    outcome: RouteCoordinationBudgetTransitionResult['outcome'],
    nowMs: number
  ): RouteCoordinationBudgetTransitionResult {
    return { outcome, snapshot: this.snapshot(nowMs) }
  }
}

export class GatewayRequestAttemptTracker {
  private readonly accountRuntimeKeys: Set<string>
  private readonly physicalCredentialKeys: Set<string>
  private readonly keyFingerprints: Set<string>
  private readonly protocolModelKeys: Set<string>
  private readonly physicalCredentialRuntimeKeys = new Map<string, Set<string>>()
  private readonly confirmationAttemptKeys = new Set<string>()

  constructor(initial?: Partial<GatewayRequestAttemptSnapshot>) {
    this.accountRuntimeKeys = normalizedKeySet(initial?.attemptedAccountRuntimeKeys)
    this.physicalCredentialKeys = normalizedKeySet(initial?.attemptedPhysicalCredentialKeys)
    this.keyFingerprints = normalizedKeySet(initial?.attemptedKeyFingerprints)
    this.protocolModelKeys = normalizedKeySet(initial?.attemptedProtocolModelKeys)
  }

  recordAccountRuntimeKey(key: string): boolean {
    return recordKey(this.accountRuntimeKeys, key)
  }

  recordPhysicalCredentialKey(key: string): boolean {
    return recordKey(this.physicalCredentialKeys, key)
  }

  recordKeyFingerprint(key: string): boolean {
    return recordKey(this.keyFingerprints, key)
  }

  recordProtocolModelKey(key: string): boolean {
    return recordKey(this.protocolModelKeys, key)
  }

  hasAccountRuntimeKey(key: string): boolean {
    return hasKey(this.accountRuntimeKeys, key)
  }

  hasPhysicalCredentialKey(key: string): boolean {
    return hasKey(this.physicalCredentialKeys, key)
  }

  hasKeyFingerprint(key: string): boolean {
    return hasKey(this.keyFingerprints, key)
  }

  hasProtocolModelKey(key: string): boolean {
    return hasKey(this.protocolModelKeys, key)
  }

  canAttemptAccount(input: Pick<GatewayDispatchAttemptIdentity, 'accountRuntimeKey' | 'physicalCredentialKey'> & {
    matchingConfirmation?: boolean
  }): GatewayDispatchAttemptRegistration {
    const accountRuntimeKey = normalizedRequiredKey(input.accountRuntimeKey)
    const physicalCredentialKey = normalizedRequiredKey(input.physicalCredentialKey)
    const physicalRuntimeKeys = this.physicalCredentialRuntimeKeys.get(physicalCredentialKey)
    const physicalAttemptedByAnotherRuntime = physicalRuntimeKeys !== undefined
      ? !physicalRuntimeKeys.has(accountRuntimeKey)
      : this.physicalCredentialKeys.has(physicalCredentialKey) && !this.accountRuntimeKeys.has(accountRuntimeKey)
    if (physicalAttemptedByAnotherRuntime) {
      return { allowed: false, reason: 'physical_credential_already_attempted' }
    }
    if (input.matchingConfirmation) {
      return this.confirmationAttemptKeys.has(confirmationAttemptKey(accountRuntimeKey, physicalCredentialKey))
        ? { allowed: false, reason: 'confirmation_already_attempted' }
        : { allowed: true }
    }
    if (this.physicalCredentialKeys.has(physicalCredentialKey)) {
      return { allowed: false, reason: 'physical_credential_already_attempted' }
    }
    if (this.accountRuntimeKeys.has(accountRuntimeKey)) {
      return { allowed: false, reason: 'account_runtime_already_attempted' }
    }
    return { allowed: true }
  }

  tryRecordDispatchAttempt(input: GatewayDispatchAttemptIdentity & {
    matchingConfirmation?: boolean
    allowKeyRotation?: boolean
  }): GatewayDispatchAttemptRegistration {
    const identity = normalizedDispatchAttemptIdentity(input)
    const accountDecision = this.canAttemptAccount({
      accountRuntimeKey: identity.accountRuntimeKey,
      physicalCredentialKey: identity.physicalCredentialKey,
      matchingConfirmation: input.matchingConfirmation
    })
    if (!accountDecision.allowed && !input.allowKeyRotation) return accountDecision

    if (input.matchingConfirmation) {
      if (!accountDecision.allowed) return accountDecision
      this.confirmationAttemptKeys.add(confirmationAttemptKey(identity.accountRuntimeKey, identity.physicalCredentialKey))
    } else if (input.allowKeyRotation) {
      const runtimeKeys = this.physicalCredentialRuntimeKeys.get(identity.physicalCredentialKey)
      if (
        !identity.keyFingerprint
        || !this.accountRuntimeKeys.has(identity.accountRuntimeKey)
        || !this.physicalCredentialKeys.has(identity.physicalCredentialKey)
        || !runtimeKeys?.has(identity.accountRuntimeKey)
      ) {
        return { allowed: false, reason: 'key_rotation_not_applicable' }
      }
      if (this.keyFingerprints.has(identity.keyFingerprint)) {
        return { allowed: false, reason: 'key_fingerprint_already_attempted' }
      }
    } else {
      if (this.protocolModelKeys.has(identity.protocolModelKey)) {
        return { allowed: false, reason: 'protocol_model_already_attempted' }
      }
      if (identity.keyFingerprint && this.keyFingerprints.has(identity.keyFingerprint)) {
        return { allowed: false, reason: 'key_fingerprint_already_attempted' }
      }
    }

    this.accountRuntimeKeys.add(identity.accountRuntimeKey)
    this.physicalCredentialKeys.add(identity.physicalCredentialKey)
    this.protocolModelKeys.add(identity.protocolModelKey)
    if (identity.keyFingerprint) this.keyFingerprints.add(identity.keyFingerprint)
    const runtimeKeys = this.physicalCredentialRuntimeKeys.get(identity.physicalCredentialKey) ?? new Set<string>()
    runtimeKeys.add(identity.accountRuntimeKey)
    this.physicalCredentialRuntimeKeys.set(identity.physicalCredentialKey, runtimeKeys)
    return { allowed: true }
  }

  snapshot(): GatewayRequestAttemptSnapshot {
    return Object.freeze({
      attemptedAccountRuntimeKeys: Object.freeze([...this.accountRuntimeKeys]),
      attemptedPhysicalCredentialKeys: Object.freeze([...this.physicalCredentialKeys]),
      attemptedKeyFingerprints: Object.freeze([...this.keyFingerprints]),
      attemptedProtocolModelKeys: Object.freeze([...this.protocolModelKeys])
    })
  }
}

export function gatewayAttemptProtocolModelKey(input: {
  accountRuntimeKey: string
  protocolCode?: string
  protocolVersion?: string
  model?: string
}): string {
  return JSON.stringify([
    normalizedRequiredKey(input.accountRuntimeKey),
    normalizedOptionalKey(input.protocolCode) ?? 'unknown_protocol',
    normalizedOptionalKey(input.protocolVersion) ?? 'unknown_version',
    normalizedOptionalKey(input.model) ?? 'unknown_model'
  ])
}

export interface GatewayRoutePlanSnapshot<TTarget> {
  readonly routePlanId: string
  readonly mode: RouteStrategyMode
  readonly requestAcceptedAtMs: number
  readonly gatewayRequestWallBudgetMs: number
  readonly gatewayRequestWallDeadlineAtMs: number
  readonly firstByteDeadlineMs?: number
  readonly requestPrecommitDeadlineAtMs: number
  readonly finalResponseReserveMs: number
  readonly uncommittedAttemptDeadlineAtMs?: number
  readonly orderedAllowedTargets: readonly TTarget[]
  readonly cursor: number
  readonly weightedDecisionToken?: string
  readonly hybridScoreDecision?: unknown
}

export interface CreateGatewayRoutePlanSnapshotInput<TTarget> {
  routePlanId: string
  mode: RouteStrategyMode
  requestAcceptedAtMs: number
  gatewayRequestWallBudgetMs?: number
  firstByteDeadlineMs?: number
  requestPrecommitDeadlineAtMs?: number
  finalResponseReserveMs?: number
  uncommittedAttemptDeadlineAtMs?: number
  orderedAllowedTargets: readonly TTarget[]
  cursor?: number
  weightedDecisionToken?: string
  hybridScoreDecision?: unknown
}

export function createGatewayRoutePlanSnapshot<TTarget>(
  input: CreateGatewayRoutePlanSnapshotInput<TTarget>
): GatewayRoutePlanSnapshot<TTarget> {
  const routePlanId = normalizedRequiredKey(input.routePlanId)
  const requestAcceptedAtMs = normalizedTimestamp(input.requestAcceptedAtMs)
  const gatewayRequestWallBudgetMs = normalizedPositiveMs(
    input.gatewayRequestWallBudgetMs,
    defaultGatewayRequestWallBudgetMs
  )
  const gatewayRequestWallDeadlineAtMs = requestAcceptedAtMs + gatewayRequestWallBudgetMs
  const orderedAllowedTargets = Object.freeze([...input.orderedAllowedTargets])
  if (orderedAllowedTargets.length === 0) {
    throw new RangeError('route plan orderedAllowedTargets must not be empty')
  }
  const cursor = normalizedCursor(input.cursor ?? 0, orderedAllowedTargets.length)
  const requestPrecommitDeadlineAtMs = normalizedOptionalTimestamp(input.requestPrecommitDeadlineAtMs)
    ?? gatewayRequestWallDeadlineAtMs

  return Object.freeze({
    routePlanId,
    mode: input.mode,
    requestAcceptedAtMs,
    gatewayRequestWallBudgetMs,
    gatewayRequestWallDeadlineAtMs,
    firstByteDeadlineMs: normalizedOptionalNonNegativeMs(input.firstByteDeadlineMs),
    requestPrecommitDeadlineAtMs: Math.min(requestPrecommitDeadlineAtMs, gatewayRequestWallDeadlineAtMs),
    finalResponseReserveMs: normalizedFinalResponseReserveMs(input.finalResponseReserveMs),
    uncommittedAttemptDeadlineAtMs: normalizedOptionalTimestamp(input.uncommittedAttemptDeadlineAtMs),
    orderedAllowedTargets,
    cursor,
    weightedDecisionToken: normalizedOptionalKey(input.weightedDecisionToken),
    hybridScoreDecision: input.hybridScoreDecision
  })
}

export function advanceGatewayRoutePlanCursor<TTarget>(
  plan: GatewayRoutePlanSnapshot<TTarget>,
  nextCursor = plan.cursor + 1
): GatewayRoutePlanSnapshot<TTarget> {
  return createGatewayRoutePlanSnapshot({
    ...plan,
    orderedAllowedTargets: plan.orderedAllowedTargets,
    cursor: nextCursor
  })
}

function normalizedCursor(value: number, targetCount: number): number {
  const cursor = Number.isFinite(value) ? Math.trunc(value) : -1
  if (cursor < 0 || cursor >= targetCount) {
    throw new RangeError(`route plan cursor ${String(value)} is outside ordered target range`)
  }
  return cursor
}

function normalizedVersion(value: number): number {
  if (!Number.isInteger(value) || value < 0) {
    throw new RangeError('route coordination version must be a non-negative integer')
  }
  return value
}

function normalizedKeySet(values: readonly string[] | undefined): Set<string> {
  return new Set((values ?? []).map(normalizedRequiredKey))
}

function normalizedDispatchAttemptIdentity(input: GatewayDispatchAttemptIdentity): GatewayDispatchAttemptIdentity {
  return {
    protocolModelKey: normalizedRequiredKey(input.protocolModelKey),
    accountRuntimeKey: normalizedRequiredKey(input.accountRuntimeKey),
    physicalCredentialKey: normalizedRequiredKey(input.physicalCredentialKey),
    keyFingerprint: normalizedOptionalKey(input.keyFingerprint)
  }
}

function confirmationAttemptKey(accountRuntimeKey: string, physicalCredentialKey: string): string {
  return JSON.stringify([accountRuntimeKey, physicalCredentialKey])
}

function recordKey(keys: Set<string>, key: string): boolean {
  const normalized = normalizedRequiredKey(key)
  if (keys.has(normalized)) return false
  keys.add(normalized)
  return true
}

function hasKey(keys: Set<string>, key: string): boolean {
  return keys.has(normalizedRequiredKey(key))
}

function normalizedRequiredKey(value: string): string {
  const normalized = value.trim()
  if (!normalized) throw new TypeError('route coordination key must not be empty')
  return normalized
}

function normalizedOptionalKey(value: string | undefined): string | undefined {
  const normalized = value?.trim()
  return normalized || undefined
}

function normalizedPositiveMs(value: number | undefined, fallback: number): number {
  if (value === undefined) return fallback
  if (!Number.isFinite(value) || value <= 0) {
    throw new RangeError('route coordination duration must be a positive finite number')
  }
  return Math.trunc(value)
}

function normalizedNonNegativeMs(value: number | undefined): number {
  if (value === undefined) return 0
  if (!Number.isFinite(value) || value < 0) {
    throw new RangeError('route coordination duration must be a non-negative finite number')
  }
  return Math.trunc(value)
}

function normalizedFinalResponseReserveMs(value: number | undefined): number {
  return normalizedNonNegativeMs(value ?? defaultGatewayFinalResponseReserveMs)
}

function normalizedOptionalNonNegativeMs(value: number | undefined): number | undefined {
  return value === undefined ? undefined : normalizedNonNegativeMs(value)
}

function normalizedTimestamp(value: number): number {
  if (!Number.isFinite(value)) {
    throw new RangeError('route coordination timestamp must be finite')
  }
  return Math.trunc(value)
}

function normalizedOptionalTimestamp(value: number | undefined): number | undefined {
  return value === undefined ? undefined : normalizedTimestamp(value)
}
