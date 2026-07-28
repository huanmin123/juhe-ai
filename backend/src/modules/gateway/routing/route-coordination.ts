import { randomUUID } from 'node:crypto'

import type { RouteStrategyMode } from '../../../domain/types.js'
import { observeGatewayRouting } from '../observability/routing-observability.service.js'

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
  | 'semantic_retry_already_attempted'
  | 'same_account_retry_not_registered'
  | 'same_account_retry_already_attempted'
  | 'same_account_retry_identity_mismatch'
  | 'same_account_retry_mode_conflict'
  | 'key_rotation_not_applicable'

export type GatewayDispatchAttemptRegistration =
  | { allowed: true }
  | { allowed: false; reason: GatewayDispatchAttemptRejectionReason }

export type GatewaySameAccountRetryReservation =
  | {
      reserved: true
      retryId: string
      retryNumber: number
      remaining: number
    }
  | {
      reserved: false
      reason: 'same_account_retry_budget_exhausted' | 'same_account_retry_not_applicable'
      remaining: number
    }

export interface GatewaySameAccountRetryReservationInput extends GatewayDispatchAttemptIdentity {
  maxRetries: number
}

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

/**
 * Request orchestration boundary. Lower-level candidate/preparation code may
 * ask for a route action through this owner instead of invoking a raw fallback
 * callback or changing groups by itself.
 */
export interface GatewayRouteFallbackDecision<TContext = unknown> {
  attempted: boolean
  context?: TContext
}

export interface GatewayRouteFinalFailure {
  statusCode: number
  message: string
  errorType: string
  errorCode?: string
  errorPhase: 'quota' | 'dispatch'
  failureAttribution?: 'gateway_capacity'
  retryAfterMs?: number
}

export interface GatewayRouteCoordinatorOwner<TContext = unknown> {
  requestFallback(reason: string): Promise<GatewayRouteFallbackDecision<TContext>>
  completeFailure(failure: GatewayRouteFinalFailure): Promise<void>
}

export interface GatewayRequestWallBudgetOptions {
  requestAcceptedAtMs: number
  budgetMs?: number
  unbounded?: boolean
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
  readonly unbounded: boolean
  private readonly now: () => number

  constructor(options: GatewayRequestWallBudgetOptions) {
    this.requestAcceptedAtMs = normalizedTimestamp(options.requestAcceptedAtMs)
    this.unbounded = options.unbounded === true
    this.budgetMs = this.unbounded
      ? Number.MAX_SAFE_INTEGER - this.requestAcceptedAtMs
      : normalizedPositiveMs(options.budgetMs, defaultGatewayRequestWallBudgetMs)
    this.deadlineAtMs = this.requestAcceptedAtMs + this.budgetMs
    this.now = options.now ?? Date.now
  }

  withMinimumBudgetMs(minimumBudgetMs: number): GatewayRequestWallBudget {
    if (this.unbounded) return this
    const normalizedMinimumBudgetMs = normalizedPositiveMs(minimumBudgetMs, this.budgetMs)
    if (normalizedMinimumBudgetMs <= this.budgetMs) return this
    return new GatewayRequestWallBudget({
      requestAcceptedAtMs: this.requestAcceptedAtMs,
      budgetMs: normalizedMinimumBudgetMs,
      now: this.now
    })
  }

  withoutLimit(): GatewayRequestWallBudget {
    if (this.unbounded) return this
    return new GatewayRequestWallBudget({
      requestAcceptedAtMs: this.requestAcceptedAtMs,
      unbounded: true,
      now: this.now
    })
  }

  elapsedMs(nowMs = this.now()): number {
    return Math.max(0, normalizedTimestamp(nowMs) - this.requestAcceptedAtMs)
  }

  remainingMs(nowMs = this.now()): number {
    if (this.unbounded) return Number.POSITIVE_INFINITY
    return Math.max(0, this.deadlineAtMs - normalizedTimestamp(nowMs))
  }

  availableDecisionMs(input: GatewayRequestWallBudgetDecision = {}): number {
    if (this.unbounded) return Number.POSITIVE_INFINITY
    const reserveMs = normalizedFinalResponseReserveMs(input.finalResponseReserveMs)
    return Math.max(0, this.remainingMs(input.nowMs) - reserveMs)
  }

  handoffRequired(input: GatewayRequestWallBudgetDecision = {}): boolean {
    if (this.unbounded) return false
    const meaningfulAttemptMs = normalizedNonNegativeMs(input.minimumMeaningfulAttemptMs)
    return this.availableDecisionMs(input) <= meaningfulAttemptMs
  }

  precommitRemainingMs(input: GatewayRequestPrecommitBudgetInput = {}): number {
    if (this.unbounded) return Number.POSITIVE_INFINITY
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
    if (this.unbounded) return normalizedNonNegativeMs(input.firstByteDeadlineMs)
    const nowMs = normalizedTimestamp(input.nowMs ?? this.now())
    const configuredFirstByteDeadlineMs = normalizedNonNegativeMs(input.firstByteDeadlineMs)
    const candidates = [
      configuredFirstByteDeadlineMs,
      this.precommitRemainingMs({ ...input, nowMs })
    ]
    const uncommittedAttemptDeadlineAtMs = normalizedOptionalTimestamp(input.uncommittedAttemptDeadlineAtMs)
    if (uncommittedAttemptDeadlineAtMs !== undefined) {
      candidates.push(Math.max(0, uncommittedAttemptDeadlineAtMs - nowMs))
    }
    const clipped = Math.max(0, Math.min(...candidates))
    if (clipped < configuredFirstByteDeadlineMs) {
      observeGatewayRouting({ kind: 'budget', outcome: 'precommit_clipped' }, nowMs)
    }
    return clipped
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

interface GatewaySameAccountRetryReservationRecord {
  readonly identity: GatewayDispatchAttemptIdentity
  readonly retryNumber: number
  consumed: boolean
}

export class GatewayRequestAttemptTracker {
  private readonly accountRuntimeKeys: Set<string>
  private readonly physicalCredentialKeys: Set<string>
  private readonly keyFingerprints: Set<string>
  private readonly protocolModelKeys: Set<string>
  private readonly physicalCredentialRuntimeKeys = new Map<string, Set<string>>()
  private readonly confirmationAttemptKeys = new Set<string>()
  private readonly confirmationPhysicalCredentialKeys = new Map<string, string>()
  private readonly semanticRetryAttemptKeys = new Set<string>()
  private readonly registeredDispatchIdentities = new Map<string, GatewayDispatchAttemptIdentity>()
  private readonly sameAccountRetryReservations = new Map<string, GatewaySameAccountRetryReservationRecord>()
  private sameAccountRetryLimit: number | undefined
  private sameAccountRetryReservationCount = 0

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

  tryReserveSameAccountRetry(
    input: GatewaySameAccountRetryReservationInput
  ): GatewaySameAccountRetryReservation {
    const maxRetries = normalizedSameAccountRetryMaxRetries(input.maxRetries)
    this.sameAccountRetryLimit = this.sameAccountRetryLimit === undefined
      ? maxRetries
      : Math.min(this.sameAccountRetryLimit, maxRetries)

    const identity = normalizedDispatchAttemptIdentity(input)
    const registeredIdentity = this.registeredDispatchIdentities.get(dispatchAttemptIdentityKey(identity))
    const remaining = this.sameAccountRetryRemaining()
    if (!registeredIdentity || !sameDispatchAttemptIdentity(registeredIdentity, identity)) {
      return { reserved: false, reason: 'same_account_retry_not_applicable', remaining }
    }
    if (remaining <= 0) {
      return { reserved: false, reason: 'same_account_retry_budget_exhausted', remaining: 0 }
    }

    const retryNumber = this.sameAccountRetryReservationCount + 1
    const retryId = `same-account-retry:${randomUUID()}`
    this.sameAccountRetryReservationCount = retryNumber
    this.sameAccountRetryReservations.set(retryId, {
      identity,
      retryNumber,
      consumed: false
    })
    return {
      reserved: true,
      retryId,
      retryNumber,
      remaining: this.sameAccountRetryRemaining()
    }
  }

  canAttemptAccount(input: Pick<GatewayDispatchAttemptIdentity, 'accountRuntimeKey' | 'physicalCredentialKey'> & {
    matchingConfirmation?: boolean
    semanticRetryId?: string
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
    if (input.semanticRetryId) {
      return this.semanticRetryAttemptKeys.has(semanticRetryAttemptKey(input.semanticRetryId, accountRuntimeKey, physicalCredentialKey))
        ? { allowed: false, reason: 'semantic_retry_already_attempted' }
        : { allowed: true }
    }
    if (input.matchingConfirmation) {
      const confirmationPhysicalCredentialKey = this.confirmationPhysicalCredentialKeys.get(accountRuntimeKey)
      if (
        confirmationPhysicalCredentialKey !== undefined
        && confirmationPhysicalCredentialKey !== physicalCredentialKey
      ) {
        return { allowed: false, reason: 'physical_credential_already_attempted' }
      }
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
    semanticRetryId?: string
    sameAccountRetryId?: string
  }): GatewayDispatchAttemptRegistration {
    const identity = normalizedDispatchAttemptIdentity(input)
    if (input.sameAccountRetryId !== undefined) {
      return this.tryRecordSameAccountRetry(identity, input)
    }
    const accountDecision = this.canAttemptAccount({
      accountRuntimeKey: identity.accountRuntimeKey,
      physicalCredentialKey: identity.physicalCredentialKey,
      matchingConfirmation: input.matchingConfirmation,
      semanticRetryId: input.semanticRetryId
    })

    if (input.semanticRetryId) {
      if (!accountDecision.allowed) return accountDecision
      this.semanticRetryAttemptKeys.add(semanticRetryAttemptKey(
        input.semanticRetryId,
        identity.accountRuntimeKey,
        identity.physicalCredentialKey
      ))
    } else if (input.matchingConfirmation) {
      if (accountDecision.allowed) {
        this.confirmationAttemptKeys.add(confirmationAttemptKey(identity.accountRuntimeKey, identity.physicalCredentialKey))
        this.confirmationPhysicalCredentialKeys.set(identity.accountRuntimeKey, identity.physicalCredentialKey)
      } else {
        if (!input.allowKeyRotation || accountDecision.reason !== 'confirmation_already_attempted') {
          return accountDecision
        }
        const keyRotationDecision = this.canRotateKey(identity)
        if (!keyRotationDecision.allowed) return keyRotationDecision
      }
    } else if (input.allowKeyRotation) {
      const keyRotationDecision = this.canRotateKey(identity)
      if (!keyRotationDecision.allowed) return keyRotationDecision
    } else {
      if (!accountDecision.allowed) return accountDecision
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
    this.registeredDispatchIdentities.set(dispatchAttemptIdentityKey(identity), identity)
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

  private sameAccountRetryRemaining(): number {
    return Math.max(0, (this.sameAccountRetryLimit ?? 0) - this.sameAccountRetryReservationCount)
  }

  private canRotateKey(identity: GatewayDispatchAttemptIdentity): GatewayDispatchAttemptRegistration {
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
    return { allowed: true }
  }

  private tryRecordSameAccountRetry(
    identity: GatewayDispatchAttemptIdentity,
    input: {
      matchingConfirmation?: boolean
      allowKeyRotation?: boolean
      semanticRetryId?: string
      sameAccountRetryId?: string
    }
  ): GatewayDispatchAttemptRegistration {
    if (input.matchingConfirmation || input.allowKeyRotation || input.semanticRetryId !== undefined) {
      return { allowed: false, reason: 'same_account_retry_mode_conflict' }
    }
    const retryId = normalizedRequiredKey(input.sameAccountRetryId ?? '')
    const reservation = this.sameAccountRetryReservations.get(retryId)
    if (!reservation) {
      return { allowed: false, reason: 'same_account_retry_not_registered' }
    }
    if (reservation.consumed) {
      return { allowed: false, reason: 'same_account_retry_already_attempted' }
    }
    if (!sameDispatchAttemptIdentity(reservation.identity, identity)) {
      return { allowed: false, reason: 'same_account_retry_identity_mismatch' }
    }
    reservation.consumed = true
    return { allowed: true }
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

function dispatchAttemptIdentityKey(identity: GatewayDispatchAttemptIdentity): string {
  return JSON.stringify([identity.accountRuntimeKey, identity.physicalCredentialKey])
}

function sameDispatchAttemptIdentity(
  left: GatewayDispatchAttemptIdentity,
  right: GatewayDispatchAttemptIdentity
): boolean {
  return left.protocolModelKey === right.protocolModelKey
    && left.accountRuntimeKey === right.accountRuntimeKey
    && left.physicalCredentialKey === right.physicalCredentialKey
    && left.keyFingerprint === right.keyFingerprint
}

function normalizedSameAccountRetryMaxRetries(value: number): number {
  if (!Number.isInteger(value) || value < 0 || value > 10) {
    throw new RangeError('same-account retry maxRetries must be an integer between 0 and 10')
  }
  return value
}

function confirmationAttemptKey(accountRuntimeKey: string, physicalCredentialKey: string): string {
  return JSON.stringify([accountRuntimeKey, physicalCredentialKey])
}

function semanticRetryAttemptKey(
  semanticRetryId: string,
  accountRuntimeKey: string,
  physicalCredentialKey: string
): string {
  return JSON.stringify([normalizedRequiredKey(semanticRetryId), accountRuntimeKey, physicalCredentialKey])
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
