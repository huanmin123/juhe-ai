import { createHash, randomUUID } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { observeGatewayRouting } from '../observability/routing-observability.service.js'
import type { GatewayRoutingCircuitOperation } from '../observability/routing-observability-store.js'
import type { UpstreamAccount } from '../protocols/openai-v1/route-helpers.js'
import type { OpenAIGatewayRequestLane } from '../protocols/openai-v1/request-lane.js'
import { gatewayAccountRuntimeKey } from './account-runtime-keys.js'
import { MemoryAccountCircuitStore } from './account-circuit-memory-store.js'
import { RedisAccountCircuitStore } from './account-circuit-redis-store.js'
import { AccountCircuitControlPlaneBridge } from './account-circuit-control-plane-bridge.js'
import {
  accountCircuitScopeKey,
  closedAccountCircuitState,
  type AccountCircuitMutationResult,
  type AccountCircuitScope,
  type AccountCircuitState,
  type AccountCircuitStore
} from './account-circuit-store.js'

const gatewayAccountCircuitCapacity = 10_000
const gatewayAccountCircuitKnownModelLimit = 256
const gatewayAccountCircuitUnknownModelBucket = 'unknown'

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
  isRuntimeStateReady?: () => boolean
  ensureRuntimeStateReady?: () => Promise<boolean>
}

export interface PrepareGatewayAccountCircuitAttemptInput {
  account: UpstreamAccount
  requestLane: OpenAIGatewayRequestLane
  model: string | undefined
  confirmationLeaseDurationMs: number
  confirmation?: GatewayAccountCircuitConfirmation
}

export class GatewayAccountCircuitAttempt {
  readonly isConfirmation: boolean

  constructor(
    private readonly service: GatewayAccountCircuitService,
    readonly scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>,
    readonly dispatchRevision: string,
    readonly confirmationLeaseDurationMs: number,
    private readonly confirmation?: GatewayAccountCircuitConfirmation
  ) {
    this.isConfirmation = confirmation !== undefined
  }

  reportFramingComplete(): Promise<AccountCircuitMutationResult | undefined> {
    if (!this.confirmation) return Promise.resolve(undefined)
    return this.service.completeConfirmation(this.confirmation, 'framing_complete')
  }

  async reportTransportFailure(
    failure: GatewayAccountCircuitTransportFailure
  ): Promise<GatewayAccountCircuitFailureDecision> {
    if (this.confirmation) {
      const result = await this.service.completeConfirmation(
        this.confirmation,
        'transport_failure',
        requiredText(failure.reason, 'failure.reason')
      )
      return { outcome: 'blocked', state: result.state }
    }
    return this.service.suspectAndAcquireConfirmation({
      scope: this.scope,
      dispatchRevision: this.dispatchRevision,
      confirmationLeaseDurationMs: this.confirmationLeaseDurationMs,
      reason: `${failure.kind}:${requiredText(failure.reason, 'failure.reason')}`
    })
  }

  reportUnknown(): Promise<AccountCircuitMutationResult | undefined> {
    if (!this.confirmation) return Promise.resolve(undefined)
    return this.service.completeConfirmation(this.confirmation, 'unknown')
  }
}

export class GatewayAccountCircuitService {
  private readonly now: () => number
  private readonly createId: () => string
  private readonly onMutation?: GatewayAccountCircuitServiceOptions['onMutation']
  private readonly isRuntimeStateReady: () => boolean
  private readonly ensureRuntimeStateReady?: () => Promise<boolean>

  constructor(
    private readonly store: AccountCircuitStore,
    options: GatewayAccountCircuitServiceOptions = {}
  ) {
    this.now = options.now ?? Date.now
    this.createId = options.createId ?? randomUUID
    this.onMutation = options.onMutation
    this.isRuntimeStateReady = options.isRuntimeStateReady ?? (() => true)
    this.ensureRuntimeStateReady = options.ensureRuntimeStateReady
  }

  private async notifyMutation(
    operation: GatewayRoutingCircuitOperation,
    scope: AccountCircuitScope,
    result: AccountCircuitMutationResult,
    previousPhase?: AccountCircuitState['phase']
  ): Promise<void> {
    if (!this.onMutation || result.status === 'not_found') return
    await this.onMutation({ scope: { ...scope }, state: result.state, status: result.status, operation, previousPhase })
  }

  async prepareAttempt(input: PrepareGatewayAccountCircuitAttemptInput): Promise<GatewayAccountCircuitPrepareResult> {
    const scope = gatewayAccountProtocolModelScope(input.account, input.requestLane, input.model)
    const dispatchRevision = accountCircuitDispatchRevision(input.account)
    const leaseDurationMs = positiveDuration(input.confirmationLeaseDurationMs)
    const expectedScopeKey = accountCircuitScopeKey(scope)

    const runtimeStateReady = this.isRuntimeStateReady()
      || await this.ensureRuntimeStateReady?.()
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
    }
    if (accountState.phase !== 'CLOSED') {
      observeBlockedCircuitDispatch(accountState)
      return { outcome: 'blocked', state: accountState }
    }

    if (input.confirmation) {
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
          cloneConfirmation(input.confirmation)
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
    }
    if (state.phase === 'CLOSED') {
      return {
        outcome: 'dispatchable',
        attempt: new GatewayAccountCircuitAttempt(this, scope, dispatchRevision, leaseDurationMs)
      }
    }
    if (state.phase === 'SUSPECT' && state.dispatchRevision === dispatchRevision) {
      const decision = await this.acquireConfirmation(scope, state, leaseDurationMs)
      if (decision.outcome === 'confirmation_acquired') {
        return {
          outcome: 'dispatchable',
          attempt: new GatewayAccountCircuitAttempt(
            this,
            scope,
            dispatchRevision,
            leaseDurationMs,
            decision.confirmation
          )
        }
      }
      return decision
    }
    observeBlockedCircuitDispatch(state)
    return { outcome: 'blocked', state }
  }

  async suspectAndAcquireConfirmation(input: {
    scope: Extract<AccountCircuitScope, { kind: 'protocol_model' }>
    dispatchRevision: string
    confirmationLeaseDurationMs: number
    reason: string
  }): Promise<GatewayAccountCircuitFailureDecision> {
    const nowMs = this.now()
    const suspect = await this.store.suspect({
      scope: input.scope,
      dispatchRevision: requiredText(input.dispatchRevision, 'dispatchRevision'),
      transitionId: this.createId(),
      reason: requiredText(input.reason, 'reason'),
      nowMs
    })
    await this.notifyMutation('suspect', input.scope, suspect, 'CLOSED')
    if (
      suspect.state.phase !== 'SUSPECT'
      || suspect.state.dispatchRevision !== input.dispatchRevision
    ) {
      return { outcome: 'blocked', state: suspect.state }
    }
    return this.acquireConfirmation(
      input.scope,
      suspect.state,
      positiveDuration(input.confirmationLeaseDurationMs)
    )
  }

  async completeConfirmation(
    confirmation: GatewayAccountCircuitConfirmation,
    outcome: 'framing_complete' | 'transport_failure' | 'unknown',
    reason?: string
  ): Promise<AccountCircuitMutationResult> {
    const result = await this.completeAndNotify('complete_confirmation', confirmation.scope, 'SUSPECT', () => this.store.completeConfirmation({
      scope: confirmation.scope,
      generation: confirmation.generation,
      dispatchRevision: confirmation.dispatchRevision,
      transitionId: this.createId(),
      leaseId: confirmation.leaseId,
      outcome,
      reason,
      nowMs: this.now()
    }))
    if (outcome === 'transport_failure' && result.status === 'applied' && result.state.phase === 'OPEN') {
      const escalation = await this.store.recordProtocolModelOpenEvidence({
        scope: confirmation.scope,
        generation: result.state.generation,
        dispatchRevision: confirmation.dispatchRevision,
        evidenceId: `${confirmation.scopeKey}:${confirmation.generation}:${confirmation.leaseId}`,
        accountTransitionId: this.createId(),
        reason: requiredText(reason ?? 'protocol_model_transport_failure', 'reason'),
        confirmedFailureCount: 1,
        windowMs: 10 * 60_000,
        maxProtocolScopes: 8,
        nowMs: this.now()
      })
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
    leaseDurationMs: number
  ): Promise<GatewayAccountCircuitFailureDecision> {
    const leaseId = this.createId()
    const result = await this.store.acquireConfirmationLease({
      scope,
      generation: state.generation,
      dispatchRevision: state.dispatchRevision,
      transitionId: this.createId(),
      leaseId,
      leaseUntilMs: this.now() + leaseDurationMs,
      nowMs: this.now()
    })
    await this.notifyMutation('acquire_confirmation', scope, result, 'SUSPECT')
    if (result.status !== 'applied') {
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
    gatewayAccountCircuitBridgeSingleton = new AccountCircuitControlPlaneBridge({ store })
    void gatewayAccountCircuitBridgeSingleton.rebuild()
    gatewayAccountCircuitServiceSingleton = new GatewayAccountCircuitService(store, {
      isRuntimeStateReady: () => gatewayAccountCircuitBridgeSingleton?.isReady() === true,
      ensureRuntimeStateReady: async () => {
        const result = await gatewayAccountCircuitBridgeSingleton?.rebuild()
        return result?.blocked === false
      },
      onMutation: (input) => {
        if (input.status === 'applied') gatewayAccountCircuitBridgeSingleton?.observe(input)
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

export async function runGatewayAccountCircuitControlPlaneMaintenance(limit = 100): Promise<number> {
  if (!await ensureGatewayAccountCircuitRuntimeStateReady()) return 0
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
    credentials: account.credentials
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
    supportedModels: account.supportedModels,
    modelMappings: account.modelMappings,
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
