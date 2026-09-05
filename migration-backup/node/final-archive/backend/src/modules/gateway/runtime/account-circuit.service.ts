import { createHash, randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { accountCircuitCredentialOwnerIdentity } from '../../../domain/account-circuit-owner.js'
import { observeGatewayRouting } from '../observability/routing-observability.service.js'
import type { GatewayRoutingCircuitOperation } from '../observability/routing-observability-store.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { gatewayAccountRuntimeKey } from './account-runtime-keys.js'
import { MemoryAccountCircuitStore } from './account-circuit-memory-store.js'
import { RedisAccountCircuitStore } from './account-circuit-redis-store.js'
import { AccountCircuitControlPlaneBridge } from './account-circuit-control-plane-bridge.js'
import {
  accountCircuitDefaultConfirmationFailuresRequired,
  accountCircuitFailureEvidenceKeys,
  accountCircuitScopeKey,
  closedAccountCircuitState,
  normalizeAccountCircuitConfirmationFailuresRequired,
  normalizeAccountCircuitEscalationDistinctScopeThreshold,
  normalizeAccountCircuitEscalationWindowMs,
  normalizeAccountCircuitFailureEvidenceKey,
  type AccountCircuitMutationResult,
  type AccountCircuitScope,
  type AccountCircuitState,
  type AccountCircuitStore
} from './account-circuit-store.js'

const gatewayAccountCircuitCapacity = runtimeConfig.gateway.accountCircuitCapacity
const gatewayAccountCircuitKnownModelLimit = 256
const gatewayAccountCircuitUnknownModelBucket = 'unknown'
const gatewayAccountCircuitFailureEvidenceMarker = '|request_evidence_sha256='

export type GatewayAccountCircuitTransportFailureKind = 'transport' | 'timeout' | 'read_incomplete'

export interface GatewayAccountCircuitTransportFailure {
  kind: GatewayAccountCircuitTransportFailureKind
  reason: string
}

export interface GatewayAccountCircuitConfirmation {
  scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>
  scopeKey: string
  accountRuntimeKey: string
  generation: number
  dispatchRevision: string
  leaseId: string
}

export type GatewayAccountCircuitFailureDecision =
  | { outcome: 'confirmation_acquired'; confirmation: GatewayAccountCircuitConfirmation; state: AccountCircuitState }
  | { outcome: 'suspected'; state: AccountCircuitState }
  | { outcome: 'observer_neutral'; state: AccountCircuitState }
  | { outcome: 'blocked'; state: AccountCircuitState }

export type GatewayAccountCircuitPrepareResult =
  | { outcome: 'dispatchable'; attempt: GatewayAccountCircuitAttempt }
  | { outcome: 'blocked'; state: AccountCircuitState }

export interface GatewayAccountCircuitServiceOptions {
  now?: () => number
  createId?: () => string
  onMutation?: (input: {
    scope: AccountCircuitScope
    state: AccountCircuitState
    status: AccountCircuitMutationResult['status']
    operation: GatewayRoutingCircuitOperation
    previousPhase?: AccountCircuitState['phase']
  }) => Promise<void> | void
  isRuntimeStateReady?: (accountRuntimeKey: string) => boolean
  ensureRuntimeStateReady?: (accountRuntimeKey: string) => Promise<boolean>
  escalationDistinctScopeThreshold?: number
  escalationWindowMs?: number
}

export interface PrepareGatewayAccountCircuitAttemptInput {
  account: UpstreamAccount
  requestLane: OpenAIGatewayRequestLane
  model: string | undefined
  confirmationLeaseDurationMs: number
  confirmationEligible?: boolean
  confirmationFailuresRequired?: number
  confirmation?: GatewayAccountCircuitConfirmation
  failureEvidenceKey?: string
}

interface GatewayAccountCircuitObserver {
  generation: number
  dispatchRevision: string
  expectedFailureEvidenceKey: string
  observerEvidenceKey: string
  state: AccountCircuitState
}

type GatewayAccountCircuitConfirmationOutcome = 'framing_complete' | 'transport_failure' | 'unknown'

interface GatewayAccountCircuitConfirmationSettlementIntent {
  outcome: GatewayAccountCircuitConfirmationOutcome
  reason?: string
  failureEvidenceKey?: string
  framingCompleteDisposition?: 'recovering' | 'closed'
}

interface GatewayAccountCircuitConfirmationSettlement {
  outcome: GatewayAccountCircuitConfirmationOutcome
  result: AccountCircuitMutationResult
}

export class GatewayAccountCircuitAttempt {
  readonly isObserver: boolean

  private requestRecoveryGeneration: number | undefined
  private requestRecoveryEvidenceKey: string | undefined
  private confirmationKeyRotationFailureObserved = false
  private confirmationSettlementIntent: GatewayAccountCircuitConfirmationSettlementIntent | undefined
  private confirmationSettlementInFlight: Promise<GatewayAccountCircuitConfirmationSettlement> | undefined
  private confirmationSettlementResult: GatewayAccountCircuitConfirmationSettlement | undefined

  constructor(
    private readonly service: GatewayAccountCircuitService,
    readonly scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>,
    readonly dispatchRevision: string,
    readonly confirmationLeaseDurationMs: number,
    readonly confirmationFailuresRequired: number,
    private confirmation?: GatewayAccountCircuitConfirmation,
    private readonly failureEvidenceKey?: string,
    private readonly observer?: GatewayAccountCircuitObserver
  ) {
    this.isObserver = observer !== undefined
  }

  get isConfirmation(): boolean {
    return this.confirmation !== undefined
  }

  async reportFramingComplete(): Promise<AccountCircuitMutationResult | undefined> {
    const confirmationSettlement = this.settleConfirmation({
      outcome: 'framing_complete',
      framingCompleteDisposition: this.confirmationKeyRotationFailureObserved ? 'closed' : undefined
    })
    if (confirmationSettlement) {
      return (await confirmationSettlement).result
    }
    if (this.observer) {
      return this.service.completeObserverFraming({
        scope: this.scope,
        generation: this.observer.generation,
        dispatchRevision: this.observer.dispatchRevision,
        expectedFailureEvidenceKey: this.observer.expectedFailureEvidenceKey,
        observerEvidenceKey: this.observer.observerEvidenceKey
      })
    }
    if (this.requestRecoveryGeneration !== undefined) {
      const result = await this.service.completeRequestFramingAfterKeyRotation({
        scope: this.scope,
        generation: this.requestRecoveryGeneration,
        dispatchRevision: this.dispatchRevision,
        failureEvidenceKey: this.requestRecoveryEvidenceKey
      })
      this.requestRecoveryGeneration = undefined
      this.requestRecoveryEvidenceKey = undefined
      return result
    }
    if (this.isConfirmation) return undefined
    await this.service.clearAccountEscalationEvidenceAfterFramingComplete(
      this.scope,
      this.dispatchRevision
    )
    return undefined
  }

  async reportTransportFailure(
    failure: GatewayAccountCircuitTransportFailure
  ): Promise<GatewayAccountCircuitFailureDecision> {
    const failureReason = requiredText(failure.reason, 'failure.reason')
    const confirmationSettlement = this.settleConfirmation({
      outcome: 'transport_failure',
      reason: failureReason,
      failureEvidenceKey: this.failureEvidenceKey
    })
    if (confirmationSettlement) {
      const settlement = await confirmationSettlement
      if (settlement.outcome === 'transport_failure' && settlement.result.state.phase === 'SUSPECT') {
        this.requestRecoveryGeneration = settlement.result.state.generation
        this.requestRecoveryEvidenceKey = settlement.result.state.failureEvidenceKeys?.at(-1)
      }
      return {
        outcome: settlement.outcome === 'transport_failure' ? 'blocked' : 'observer_neutral',
        state: settlement.result.state
      }
    }
    if (this.observer) {
      return { outcome: 'observer_neutral', state: this.observer.state }
    }
    const decision = await this.service.suspectForegroundFailure({
      scope: this.scope,
      dispatchRevision: this.dispatchRevision,
      confirmationFailuresRequired: this.confirmationFailuresRequired,
      reason: `${failure.kind}:${failureReason}`,
      failureEvidenceKey: this.failureEvidenceKey
    })
    if (decision.outcome === 'suspected') {
      this.requestRecoveryGeneration = decision.state.generation
      this.requestRecoveryEvidenceKey = accountCircuitFailureEvidenceKeys(decision.state).at(-1)
    }
    return decision
  }

  deferConfirmationTransportFailureForKeyRotation(): boolean {
    if (!this.confirmation || this.confirmationSettlementIntent) return false
    this.confirmationKeyRotationFailureObserved = true
    return true
  }

  async reportUnknown(): Promise<AccountCircuitMutationResult | undefined> {
    const confirmationSettlement = this.settleConfirmation({ outcome: 'unknown' })
    return confirmationSettlement ? (await confirmationSettlement).result : undefined
  }

  private settleConfirmation(
    requestedIntent: GatewayAccountCircuitConfirmationSettlementIntent
  ): Promise<GatewayAccountCircuitConfirmationSettlement> | undefined {
    if (this.confirmationSettlementResult) {
      return Promise.resolve(this.confirmationSettlementResult)
    }
    const confirmation = this.confirmation
    if (!confirmation) return undefined

    // The first observed terminal outcome owns the lease. A store or control-
    // plane failure may retry that exact outcome, but a later code path must
    // never replace it with a contradictory success/failure observation.
    this.confirmationSettlementIntent ??= { ...requestedIntent }
    if (this.confirmationSettlementInFlight) return this.confirmationSettlementInFlight

    const intent = this.confirmationSettlementIntent
    const settlement = this.service.completeConfirmation(
      confirmation,
      intent.outcome,
      intent.reason,
      intent.failureEvidenceKey,
      intent.framingCompleteDisposition
    ).then((result) => {
      const completed = { outcome: intent.outcome, result }
      this.confirmationSettlementResult = completed
      this.confirmation = undefined
      this.confirmationKeyRotationFailureObserved = false
      return completed
    })
    this.confirmationSettlementInFlight = settlement
    void settlement.then(
      () => {
        if (this.confirmationSettlementInFlight === settlement) {
          this.confirmationSettlementInFlight = undefined
        }
      },
      () => {
        if (this.confirmationSettlementInFlight === settlement) {
          this.confirmationSettlementInFlight = undefined
        }
      }
    )
    return settlement
  }
}

export class GatewayAccountCircuitService {
  private readonly now: () => number
  private readonly createId: () => string
  private readonly onMutation?: GatewayAccountCircuitServiceOptions['onMutation']
  private readonly isRuntimeStateReady: (accountRuntimeKey: string) => boolean
  private readonly ensureRuntimeStateReady?: (accountRuntimeKey: string) => Promise<boolean>
  private readonly escalationDistinctScopeThreshold: number
  private readonly escalationWindowMs: number

  constructor(
    private readonly store: AccountCircuitStore,
    options: GatewayAccountCircuitServiceOptions = {}
  ) {
    this.now = options.now ?? Date.now
    this.createId = options.createId ?? randomUUID
    this.onMutation = options.onMutation
    this.isRuntimeStateReady = options.isRuntimeStateReady ?? (() => true)
    this.ensureRuntimeStateReady = options.ensureRuntimeStateReady
    this.escalationDistinctScopeThreshold = normalizeAccountCircuitEscalationDistinctScopeThreshold(
      options.escalationDistinctScopeThreshold,
      runtimeConfig.gateway.accountCircuitEscalationDistinctScopeThreshold
    )
    this.escalationWindowMs = normalizeAccountCircuitEscalationWindowMs(
      options.escalationWindowMs,
      runtimeConfig.gateway.accountCircuitEscalationWindowMs
    )
  }

  private async notifyMutation(
    operation: GatewayRoutingCircuitOperation,
    scope: AccountCircuitScope,
    result: AccountCircuitMutationResult,
    previousPhase?: AccountCircuitState['phase']
  ): Promise<void> {
    if (!this.onMutation || result.status === 'not_found') return
    for (const relatedState of result.relatedStates ?? []) {
      await this.onMutation({
        scope: { ...relatedState.scope },
        state: relatedState,
        status: 'applied',
        operation
      })
    }
    await this.onMutation({ scope: { ...scope }, state: result.state, status: result.status, operation, previousPhase })
  }

  async prepareAttempt(input: PrepareGatewayAccountCircuitAttemptInput): Promise<GatewayAccountCircuitPrepareResult> {
    const scope = gatewayAccountProtocolModelScope(input.account, input.requestLane, input.model)
    const dispatchRevision = accountCircuitDispatchRevision(input.account)
    const leaseDurationMs = positiveDuration(input.confirmationLeaseDurationMs)
    const confirmationFailuresRequired = boundedConfirmationFailuresRequired(
      input.confirmationFailuresRequired ?? accountCircuitDefaultConfirmationFailuresRequired
    )
    const expectedScopeKey = accountCircuitScopeKey(scope)

    const runtimeStateReady = this.isRuntimeStateReady(scope.accountRuntimeKey)
      || await this.ensureRuntimeStateReady?.(scope.accountRuntimeKey)
      || false
    if (!runtimeStateReady) {
      observeGatewayRouting({ kind: 'circuit_dispatch', outcome: 'rebuild_blocked', phase: 'SUSPECT' })
      return {
        outcome: 'blocked',
        state: {
          ...closedAccountCircuitState(scope, dispatchRevision, 0, 'runtime-state-rebuilding', this.now()),
          phase: 'SUSPECT',
          failureReason: 'runtime_state_rebuilding'
        }
      }
    }

    // A parent account incident shadows every protocol/model child. Read it
    // before creating a child confirmation so escalation takes effect on the
    // next request, including requests arriving on another process.
    const accountScope: AccountCircuitScope = {
      kind: 'account',
      accountRuntimeKey: scope.accountRuntimeKey
    }
    let accountState = await this.store.get(accountScope, this.now())
    if (accountState.dispatchRevision && accountState.dispatchRevision !== dispatchRevision) {
      const replaced = await this.store.replaceDispatchRevision({
        scope: accountScope,
        dispatchRevision,
        transitionId: this.createId(),
        nowMs: this.now()
      })
      await this.notifyMutation('replace_revision', accountScope, replaced, accountState.phase)
      accountState = replaced.state
      if (replaced.status === 'stale_dispatch_revision') {
        observeBlockedCircuitDispatch(accountState)
        return { outcome: 'blocked', state: accountState }
      }
    }
    if (accountState.phase !== 'CLOSED') {
      observeBlockedCircuitDispatch(accountState)
      return { outcome: 'blocked', state: accountState }
    }

    if (input.confirmation) {
      if (input.confirmationEligible === false) {
        await this.completeConfirmation(input.confirmation, 'unknown')
        const state = await this.store.get(scope, this.now())
        observeBlockedCircuitDispatch(state)
        return { outcome: 'blocked', state }
      }
      if (
        input.confirmation.scopeKey !== expectedScopeKey
        || input.confirmation.accountRuntimeKey !== scope.accountRuntimeKey
        || input.confirmation.dispatchRevision !== dispatchRevision
      ) {
        const state = await this.store.get(scope, this.now())
        observeBlockedCircuitDispatch(state)
        return { outcome: 'blocked', state }
      }
      const state = await this.store.get(scope, this.now())
      if (sameRequestFailureEvidence(state, input.failureEvidenceKey)) {
        observeBlockedCircuitDispatch(state)
        return { outcome: 'blocked', state }
      }
      if (
        state.phase !== 'SUSPECT'
        || state.generation !== input.confirmation.generation
        || state.dispatchRevision !== input.confirmation.dispatchRevision
        || state.lease?.kind !== 'confirmation'
        || state.lease.leaseId !== input.confirmation.leaseId
      ) {
        observeBlockedCircuitDispatch(state)
        return { outcome: 'blocked', state }
      }
      return {
        outcome: 'dispatchable',
        attempt: new GatewayAccountCircuitAttempt(
          this,
          scope,
          dispatchRevision,
          leaseDurationMs,
          confirmationFailuresRequired,
          cloneConfirmation(input.confirmation),
          normalizedFailureEvidenceKey(input.failureEvidenceKey)
        )
      }
    }

    let state = await this.store.get(scope, this.now())
    if (state.dispatchRevision && state.dispatchRevision !== dispatchRevision) {
      const replaced = await this.store.replaceDispatchRevision({
        scope,
        dispatchRevision,
        transitionId: this.createId(),
        nowMs: this.now()
      })
      await this.notifyMutation('replace_revision', scope, replaced, state.phase)
      state = replaced.state
      if (replaced.status === 'stale_dispatch_revision') {
        observeBlockedCircuitDispatch(state)
        return { outcome: 'blocked', state }
      }
    }
    if (state.phase === 'CLOSED') {
      return {
        outcome: 'dispatchable',
        attempt: new GatewayAccountCircuitAttempt(
          this,
          scope,
          dispatchRevision,
          leaseDurationMs,
          confirmationFailuresRequired,
          undefined,
          normalizedFailureEvidenceKey(input.failureEvidenceKey)
        )
      }
    }
    if (state.phase === 'SUSPECT' && state.dispatchRevision === dispatchRevision) {
      if (input.confirmationEligible === false) {
        observeBlockedCircuitDispatch(state)
        return { outcome: 'blocked', state }
      }
      const confirmationEvidenceKey = normalizedFailureEvidenceKey(input.failureEvidenceKey)
      if (!confirmationEvidenceKey || sameRequestFailureEvidence(state, confirmationEvidenceKey)) {
        observeBlockedCircuitDispatch(state)
        return { outcome: 'blocked', state }
      }
      const decision = await this.acquireConfirmation(
        scope,
        state,
        leaseDurationMs,
        confirmationEvidenceKey
      )
      const releaseAcquiredConfirmation = async () => {
        if (decision.outcome !== 'confirmation_acquired') return
        try {
          await this.completeConfirmation(decision.confirmation, 'unknown')
        } catch {
          await this.completeConfirmation(decision.confirmation, 'unknown')
        }
      }
      let currentParentState: AccountCircuitState
      try {
        currentParentState = await this.store.get(accountScope, this.now())
      } catch (error) {
        await releaseAcquiredConfirmation()
        throw error
      }
      if (
        currentParentState.phase !== 'CLOSED'
        || (currentParentState.dispatchRevision && currentParentState.dispatchRevision !== dispatchRevision)
      ) {
        await releaseAcquiredConfirmation()
        observeBlockedCircuitDispatch(currentParentState)
        return { outcome: 'blocked', state: currentParentState }
      }
      if (decision.outcome === 'confirmation_acquired') {
        return {
          outcome: 'dispatchable',
          attempt: new GatewayAccountCircuitAttempt(
            this,
            scope,
            dispatchRevision,
            leaseDurationMs,
            confirmationFailuresRequired,
            decision.confirmation,
            normalizedFailureEvidenceKey(input.failureEvidenceKey)
          )
        }
      }
      const observerState = decision.state
      const expectedFailureEvidenceKey = accountCircuitFailureEvidenceKeys(observerState).at(-1)
      if (
        observerState.phase === 'SUSPECT'
        && observerState.dispatchRevision === dispatchRevision
        && observerState.shadowedByIncidentId === undefined
        && expectedFailureEvidenceKey
        && !accountCircuitFailureEvidenceKeys(observerState).includes(confirmationEvidenceKey)
      ) {
        return {
          outcome: 'dispatchable',
          attempt: new GatewayAccountCircuitAttempt(
            this,
            scope,
            dispatchRevision,
            leaseDurationMs,
            confirmationFailuresRequired,
            undefined,
            confirmationEvidenceKey,
            {
              generation: observerState.generation,
              dispatchRevision: observerState.dispatchRevision,
              expectedFailureEvidenceKey,
              observerEvidenceKey: confirmationEvidenceKey,
              state: observerState
            }
          )
        }
      }
      observeBlockedCircuitDispatch(observerState)
      return { outcome: 'blocked', state: observerState }
    }
    observeBlockedCircuitDispatch(state)
    return { outcome: 'blocked', state }
  }

  async suspectForegroundFailure(input: {
    scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>
    dispatchRevision: string
    confirmationFailuresRequired?: number
    reason: string
    failureEvidenceKey?: string
  }): Promise<GatewayAccountCircuitFailureDecision> {
    const nowMs = this.now()
    const suspectTransitionId = this.createId()
    const suspect = await this.store.suspect({
      scope: input.scope,
      dispatchRevision: requiredText(input.dispatchRevision, 'dispatchRevision'),
      transitionId: suspectTransitionId,
      reason: failureReasonWithEvidence(input.reason, input.failureEvidenceKey),
      confirmationFailuresRequired: boundedConfirmationFailuresRequired(
        input.confirmationFailuresRequired ?? accountCircuitDefaultConfirmationFailuresRequired
      ),
      failureEvidenceKey: normalizeAccountCircuitFailureEvidenceKey(
        input.failureEvidenceKey,
        `suspect:${suspectTransitionId}`
      ),
      nowMs
    })
    await this.notifyMutation('suspect', input.scope, suspect, 'CLOSED')
    if (
      suspect.status === 'capacity_exhausted'
      ||
      suspect.state.phase !== 'SUSPECT'
      || suspect.state.dispatchRevision !== input.dispatchRevision
    ) {
      return { outcome: 'blocked', state: suspect.state }
    }
    return suspect.status === 'applied'
      ? { outcome: 'suspected', state: suspect.state }
      : { outcome: 'blocked', state: suspect.state }
  }

  async completeConfirmation(
    confirmation: GatewayAccountCircuitConfirmation,
    outcome: 'framing_complete' | 'transport_failure' | 'unknown',
    reason?: string,
    failureEvidenceKey?: string,
    framingCompleteDisposition?: 'recovering' | 'closed'
  ): Promise<AccountCircuitMutationResult> {
    const completionIdentity = sha256(stableSerialize({
      scopeKey: confirmation.scopeKey,
      generation: confirmation.generation,
      dispatchRevision: confirmation.dispatchRevision,
      leaseId: confirmation.leaseId,
      outcome
    }))
    const result = await this.completeAndNotify('complete_confirmation', confirmation.scope, 'SUSPECT', () => this.store.completeConfirmation({
      scope: confirmation.scope,
      generation: confirmation.generation,
      dispatchRevision: confirmation.dispatchRevision,
      transitionId: `confirmation:${completionIdentity}`,
      leaseId: confirmation.leaseId,
      outcome,
      reason,
      framingCompleteDisposition,
      ...(outcome === 'transport_failure'
        ? {
            failureEvidenceKey: normalizeAccountCircuitFailureEvidenceKey(
              failureEvidenceKey,
              `confirmation:${confirmation.leaseId}`
            )
          }
        : {}),
      nowMs: this.now()
    }))
    const appliedOrReplayed = result.status === 'applied' || result.status === 'idempotent'
    if (outcome === 'framing_complete' && appliedOrReplayed && result.state.phase === 'CLOSED') {
      await this.clearAccountEscalationEvidenceAfterFramingComplete(
        confirmation.scope,
        confirmation.dispatchRevision
      )
    }
    if (outcome === 'transport_failure' && appliedOrReplayed && result.state.phase === 'OPEN') {
      const escalation = await this.store.recordProtocolModelOpenEvidence({
        scope: confirmation.scope,
        generation: result.state.generation,
        dispatchRevision: confirmation.dispatchRevision,
        evidenceId: `${confirmation.scopeKey}:${confirmation.generation}:${confirmation.leaseId}`,
        accountTransitionId: `confirmation-parent:${completionIdentity}`,
        reason: requiredText(reason ?? 'protocol_model_transport_failure', 'reason'),
        confirmedFailureCount: 1,
        distinctScopeThreshold: this.escalationDistinctScopeThreshold,
        windowMs: this.escalationWindowMs,
        maxProtocolScopes: Math.max(8, this.escalationDistinctScopeThreshold),
        nowMs: this.now()
      })
      for (const relatedState of escalation.relatedStates ?? []) {
        await this.onMutation?.({
          scope: { ...relatedState.scope },
          state: relatedState,
          status: 'applied',
          operation: 'record_parent_evidence'
        })
      }
      await this.onMutation?.({
        scope: {
          kind: 'account',
          accountRuntimeKey: confirmation.accountRuntimeKey
        },
        state: escalation.accountState,
        status: escalationMutationStatus(escalation.status),
        operation: 'record_parent_evidence',
        ...(escalation.status === 'escalated' ? { previousPhase: 'CLOSED' as const } : {})
      })
    }
    return result
  }

  async clearAccountEscalationEvidenceAfterFramingComplete(
    scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>,
    dispatchRevision: string
  ): Promise<void> {
    await this.store.clearAccountEscalationEvidence({
      accountRuntimeKey: scope.accountRuntimeKey,
      dispatchRevision: requiredText(dispatchRevision, 'dispatchRevision'),
      evidenceId: this.createId(),
      nowMs: this.now()
    })
  }

  async completeObserverFraming(input: {
    scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>
    generation: number
    dispatchRevision: string
    expectedFailureEvidenceKey: string
    observerEvidenceKey: string
  }): Promise<AccountCircuitMutationResult> {
    const completed = await this.completeAndNotify(
      'complete_confirmation',
      input.scope,
      'SUSPECT',
      () => this.store.closeSuspectFromObserver({
        scope: input.scope,
        generation: input.generation,
        dispatchRevision: requiredText(input.dispatchRevision, 'dispatchRevision'),
        transitionId: this.createId(),
        expectedFailureEvidenceKey: input.expectedFailureEvidenceKey,
        observerEvidenceKey: input.observerEvidenceKey,
        nowMs: this.now()
      })
    )
    if (completed.status === 'applied' && completed.state.phase === 'CLOSED') {
      await this.clearAccountEscalationEvidenceAfterFramingComplete(input.scope, input.dispatchRevision)
    }
    return completed
  }

  /**
   * A request may fail on one physical API key and then receive a complete
   * response from another key of the same account. That proves the account's
   * protocol/model transport scope is usable. Reclaim only the SUSPECT
   * incident created by that request, fenced by generation, revision, and
   * the latest failure evidence. Closing invalidates any concurrent
   * confirmation lease so a late failure cannot reopen the incident.
   */
  async completeRequestFramingAfterKeyRotation(input: {
    scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>
    generation: number
    dispatchRevision: string
    failureEvidenceKey?: string
  }): Promise<AccountCircuitMutationResult> {
    const expectedEvidence = input.failureEvidenceKey
      ? normalizeAccountCircuitFailureEvidenceKey(input.failureEvidenceKey, 'request-key-rotation')
      : undefined
    if (!expectedEvidence) {
      return {
        status: 'state_mismatch',
        state: await this.store.get(input.scope, this.now())
      }
    }
    const completed = await this.completeAndNotify(
      'complete_confirmation',
      input.scope,
      'SUSPECT',
      () => this.store.closeSuspectFromKeyRotation({
        scope: input.scope,
        generation: input.generation,
        dispatchRevision: requiredText(input.dispatchRevision, 'dispatchRevision'),
        transitionId: this.createId(),
        expectedFailureEvidenceKey: expectedEvidence,
        nowMs: this.now()
      })
    )
    if (completed.status === 'applied' && completed.state.phase === 'CLOSED') {
      await this.clearAccountEscalationEvidenceAfterFramingComplete(
        input.scope,
        input.dispatchRevision
      )
    }
    return completed
  }

  private async completeAndNotify(
    operation: GatewayRoutingCircuitOperation,
    scope: AccountCircuitScope,
    previousPhase: AccountCircuitState['phase'],
    mutation: () => Promise<AccountCircuitMutationResult>
  ): Promise<AccountCircuitMutationResult> {
    const result = await mutation()
    await this.notifyMutation(operation, scope, result, previousPhase)
    return result
  }

  private async acquireConfirmation(
    scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>,
    state: AccountCircuitState,
    leaseDurationMs: number,
    confirmationEvidenceKey: string
  ): Promise<GatewayAccountCircuitFailureDecision> {
    const leaseId = this.createId()
    const nowMs = this.now()
    const expectedFailureEvidenceKey = accountCircuitFailureEvidenceKeys(state).at(-1)
    const acquireInput = {
      scope,
      generation: state.generation,
      dispatchRevision: state.dispatchRevision,
      transitionId: this.createId(),
      leaseId,
      leaseUntilMs: nowMs + leaseDurationMs,
      expectedFailureEvidenceKey,
      confirmationEvidenceKey,
      nowMs
    }
    let result: AccountCircuitMutationResult
    try {
      result = await this.store.acquireConfirmationLease(acquireInput)
    } catch {
      // A Redis reply can be lost after EVAL committed. Replaying the exact
      // transition is safe and lets the caller recover ownership of its lease.
      try {
        result = await this.store.acquireConfirmationLease(acquireInput)
      } catch (replayError) {
        const observed = await this.store.get(scope, this.now())
        const replayCommitted = observed.phase === 'SUSPECT'
          && observed.generation === state.generation
          && observed.dispatchRevision === state.dispatchRevision
          && observed.lease?.kind === 'confirmation'
          && observed.lease.leaseId === leaseId
        if (!replayCommitted) throw replayError
        result = { status: 'idempotent', state: observed }
      }
    }
    await this.notifyMutation('acquire_confirmation', scope, result, 'SUSPECT')
    const ownsLease = result.state.phase === 'SUSPECT'
      && result.state.generation === state.generation
      && result.state.dispatchRevision === state.dispatchRevision
      && result.state.lease?.kind === 'confirmation'
      && result.state.lease.leaseId === leaseId
    if ((result.status !== 'applied' && result.status !== 'idempotent') || !ownsLease) {
      return { outcome: 'blocked', state: result.state }
    }
    const confirmation: GatewayAccountCircuitConfirmation = {
      scope: { ...scope },
      scopeKey: accountCircuitScopeKey(scope),
      accountRuntimeKey: scope.accountRuntimeKey,
      generation: result.state.generation,
      dispatchRevision: result.state.dispatchRevision,
      leaseId
    }
    return { outcome: 'confirmation_acquired', confirmation, state: result.state }
  }
}

let gatewayAccountCircuitStoreSingleton: AccountCircuitStore | undefined
let gatewayAccountCircuitStoreIdentity = ''
let gatewayAccountCircuitServiceSingleton: GatewayAccountCircuitService | undefined
let gatewayAccountCircuitBridgeSingleton: AccountCircuitControlPlaneBridge | undefined

export function getGatewayAccountCircuitStore(): AccountCircuitStore {
  if (runtimeConfig.runtimeMode === 'standalone') {
    if (runtimeConfig.runtimeStateDriver !== 'memory') {
      throw new Error('standalone 账户电路要求 memory runtime state driver')
    }
    if (!gatewayAccountCircuitStoreSingleton || gatewayAccountCircuitStoreIdentity !== 'standalone:memory') {
      gatewayAccountCircuitStoreSingleton = new MemoryAccountCircuitStore({
        capacity: gatewayAccountCircuitCapacity
      })
      gatewayAccountCircuitStoreIdentity = 'standalone:memory'
      gatewayAccountCircuitServiceSingleton = undefined
    }
    return gatewayAccountCircuitStoreSingleton
  }

  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    throw new Error('performance 账户电路要求 redis runtime state driver')
  }
  const redisUrl = runtimeConfig.redis.stateUrl?.trim()
  if (!redisUrl) {
    throw new Error('performance 账户电路缺少 JUHE_AI_REDIS_STATE_URL')
  }
  const identity = `performance:redis:${sha256(redisUrl)}`
  if (!gatewayAccountCircuitStoreSingleton || gatewayAccountCircuitStoreIdentity !== identity) {
    gatewayAccountCircuitStoreSingleton = new RedisAccountCircuitStore({
      redisUrl,
      capacity: gatewayAccountCircuitCapacity
    })
    gatewayAccountCircuitStoreIdentity = identity
    gatewayAccountCircuitServiceSingleton = undefined
  }
  return gatewayAccountCircuitStoreSingleton
}

export function getGatewayAccountCircuitService(): GatewayAccountCircuitService {
  if (!gatewayAccountCircuitServiceSingleton) {
    const store = getGatewayAccountCircuitStore()
    gatewayAccountCircuitBridgeSingleton = new AccountCircuitControlPlaneBridge({
      store,
      rebuildPageTimeoutMs: runtimeConfig.gateway.accountCircuitRebuildPageTimeoutMs,
      rebuildTotalTimeoutMs: runtimeConfig.gateway.accountCircuitRebuildTotalTimeoutMs,
      rebuildMaxPages: runtimeConfig.gateway.accountCircuitRebuildMaxPages
    })
    void gatewayAccountCircuitBridgeSingleton.rebuild()
    gatewayAccountCircuitServiceSingleton = new GatewayAccountCircuitService(store, {
      isRuntimeStateReady: (accountRuntimeKey) => gatewayAccountCircuitBridgeSingleton?.isAccountReady(accountRuntimeKey) === true,
      ensureRuntimeStateReady: async (accountRuntimeKey) => await gatewayAccountCircuitBridgeSingleton?.ensureAccountReady(accountRuntimeKey) ?? false,
      onMutation: (input) => {
        projectGatewayAccountCircuitRuntimeMutation(input)
        observeGatewayRouting({
          kind: 'circuit_mutation',
          operation: input.operation,
          status: input.status,
          ...(input.state.lease?.kind ? { leaseKind: input.state.lease.kind } : {})
        })
        if (input.status === 'applied' && input.previousPhase && input.previousPhase !== input.state.phase) {
          observeGatewayRouting({
            kind: 'circuit_transition',
            from: input.previousPhase,
            to: input.state.phase,
            source: input.operation === 'replace_revision'
              ? 'configuration'
              : input.operation === 'complete_canary'
                ? 'recovery'
                : 'transport'
          })
        }
      }
    })
  }
  return gatewayAccountCircuitServiceSingleton
}

export function projectGatewayAccountCircuitRuntimeMutation(input: {
  scope: AccountCircuitScope
  state: AccountCircuitState
  status: AccountCircuitMutationResult['status']
}): void {
  if (input.status === 'applied') gatewayAccountCircuitBridgeSingleton?.observe(input)
}

export async function runGatewayAccountCircuitControlPlaneMaintenance(limit = 100): Promise<number> {
  // Project loaded/pending incidents even when a full rebuild is partial or
  // one unrelated scope has a persistence failure. Blocking this worker
  // globally would prevent recovery from releasing the capacity/readiness
  // condition that caused the partial rebuild.
  await ensureGatewayAccountCircuitRuntimeStateReady()
  const projected = await gatewayAccountCircuitBridgeSingleton?.projectPending(limit) ?? 0
  const reconciled = await gatewayAccountCircuitBridgeSingleton?.reconcileActive(limit) ?? 0
  return projected + reconciled
}

export async function ensureGatewayAccountCircuitRuntimeStateReady(): Promise<boolean> {
  if (!gatewayAccountCircuitBridgeSingleton) getGatewayAccountCircuitService()
  if (gatewayAccountCircuitBridgeSingleton?.isReady()) return true
  const rebuilt = await gatewayAccountCircuitBridgeSingleton?.rebuild()
  return rebuilt?.blocked === false
}

export function resetGatewayAccountCircuitStoreForTest(): void {
  gatewayAccountCircuitStoreSingleton = undefined
  gatewayAccountCircuitStoreIdentity = ''
  gatewayAccountCircuitServiceSingleton = undefined
  gatewayAccountCircuitBridgeSingleton = undefined
}

export function gatewayAccountProtocolModelScope(
  account: UpstreamAccount,
  requestLane: OpenAIGatewayRequestLane,
  model: string | undefined
): Extract<AccountCircuitScope, { kind: 'protocol_model' }> {
  return {
    kind: 'protocol_model',
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    protocolProfile: requiredText(
      account.providerProtocolProfileId || `${account.protocolCode}:${account.protocolVersion}`,
      'protocolProfile'
    ),
    requestLane,
    modelBucket: gatewayAccountCircuitModelBucket(account, model)
  }
}

export function accountCircuitDispatchRevision(account: UpstreamAccount): string {
  if (Number.isSafeInteger(account.dispatchRevision) && (account.dispatchRevision ?? 0) > 0) {
    return String(account.dispatchRevision)
  }
  const credentialMaterialDigest = sha256(stableSerialize({
    apiKey: account.apiKey,
    apiKeys: account.apiKeys,
    refreshToken: account.refreshToken,
    clientId: account.clientId,
    credentials: accountCircuitCredentialOwnerIdentity(account.credentials)
  }))
  const revisionPayload = {
    accountRuntimeKey: gatewayAccountRuntimeKey(account),
    credentialSourceAccountId: account.credentialSourceAccountId,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId,
    protocolCode: account.protocolCode,
    protocolVersion: account.protocolVersion,
    accountType: account.type,
    baseUrl: account.baseUrl,
    proxyProfileId: account.proxyProfileId,
    proxyUrl: account.proxyUrl,
    clientCompatibility: account.clientCompatibility,
    supportedEndpointModes: account.supportedEndpointModes,
    credentialMaterialDigest
  }
  return `v1:${sha256(stableSerialize(revisionPayload))}`
}

function gatewayAccountCircuitModelBucket(account: UpstreamAccount, model: string | undefined): string {
  const candidate = normalizeModelBucket(model)
  if (!candidate) return gatewayAccountCircuitUnknownModelBucket
  const known = new Set<string>()
  for (const configured of account.supportedModels ?? []) {
    const normalized = normalizeModelBucket(configured)
    if (normalized) known.add(normalized)
  }
  for (const mapping of account.modelMappings ?? []) {
    if (mapping.enabled === false) continue
    const source = normalizeModelBucket(mapping.sourceModel)
    const upstream = normalizeModelBucket(mapping.upstreamModel)
    if (source) known.add(source)
    if (upstream) known.add(upstream)
  }
  const boundedKnown = [...known].sort().slice(0, gatewayAccountCircuitKnownModelLimit)
  return boundedKnown.includes(candidate) ? candidate : gatewayAccountCircuitUnknownModelBucket
}

function normalizeModelBucket(value: string | undefined): string | undefined {
  const normalized = value?.trim().toLowerCase()
  if (!normalized || normalized.length > 128 || /[\u0000-\u001f\u007f]/.test(normalized)) return undefined
  return normalized
}

function cloneConfirmation(value: GatewayAccountCircuitConfirmation): GatewayAccountCircuitConfirmation {
  return { ...value, scope: { ...value.scope } }
}

function positiveDuration(value: number): number {
  if (!Number.isFinite(value) || value <= 0) throw new Error('confirmationLeaseDurationMs 必须是正有限数值')
  return Math.trunc(value)
}

function observeBlockedCircuitDispatch(state: AccountCircuitState): void {
  if (state.phase === 'CLOSED') return
  observeGatewayRouting({ kind: 'circuit_dispatch', outcome: 'blocked', phase: state.phase })
}

function escalationMutationStatus(
  status: Awaited<ReturnType<AccountCircuitStore['recordProtocolModelOpenEvidence']>>['status']
): AccountCircuitMutationResult['status'] {
  if (status === 'escalated' || status === 'already_active') return 'applied'
  if (status === 'recorded') return 'idempotent'
  return status
}

function requiredText(value: string, name: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error(`账户电路缺少 ${name}`)
  return normalized
}

function failureReasonWithEvidence(reason: string, evidenceKey: string | undefined): string {
  const normalizedReason = requiredText(reason, 'reason')
  const normalizedEvidence = normalizedFailureEvidenceKey(evidenceKey)
  return normalizedEvidence
    ? `${normalizedReason}${gatewayAccountCircuitFailureEvidenceMarker}${normalizedEvidence}`
    : normalizedReason
}

function sameRequestFailureEvidence(state: AccountCircuitState, evidenceKey: string | undefined): boolean {
  const normalizedEvidence = normalizedFailureEvidenceKey(evidenceKey)
  if (!normalizedEvidence) return false
  if (accountCircuitFailureEvidenceKeys(state).includes(normalizedEvidence)) return true
  const failureReason = state.failureReason
  if (!failureReason) return false
  const markerIndex = failureReason.lastIndexOf(gatewayAccountCircuitFailureEvidenceMarker)
  if (markerIndex < 0) return false
  return failureReason.slice(markerIndex + gatewayAccountCircuitFailureEvidenceMarker.length) === normalizedEvidence
}

function boundedConfirmationFailuresRequired(value: number): number {
  return normalizeAccountCircuitConfirmationFailuresRequired(value)
}

function normalizedFailureEvidenceKey(value: string | undefined): string | undefined {
  const normalized = value?.trim().toLowerCase()
  return normalized && /^[a-f0-9]{64}$/.test(normalized) ? normalized : undefined
}

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function stableSerialize(value: unknown, seen = new WeakSet<object>()): string {
  if (value === null) return 'null'
  if (value === undefined) return 'undefined'
  if (typeof value === 'string') return JSON.stringify(value)
  if (typeof value === 'number' || typeof value === 'boolean') return JSON.stringify(value)
  if (typeof value === 'bigint') return JSON.stringify(value.toString())
  if (Buffer.isBuffer(value)) return JSON.stringify({ bufferSha256: sha256(value.toString('base64')) })
  if (Array.isArray(value)) {
    if (seen.has(value)) return JSON.stringify('[Circular]')
    seen.add(value)
    const encoded = `[${value.map((item) => stableSerialize(item, seen)).join(',')}]`
    seen.delete(value)
    return encoded
  }
  if (typeof value !== 'object') return JSON.stringify(String(value))
  if (seen.has(value)) return JSON.stringify('[Circular]')
  seen.add(value)
  const record = value as Record<string, unknown>
  const encoded = `{${Object.keys(record).sort().map((key) => (
    `${JSON.stringify(key)}:${stableSerialize(record[key], seen)}`
  )).join(',')}}`
  seen.delete(value)
  return encoded
}
