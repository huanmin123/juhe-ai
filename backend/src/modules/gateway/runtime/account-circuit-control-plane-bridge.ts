import { randomUUID } from 'node:crypto'
import { performance } from 'node:perf_hooks'

import {
  type AccountCircuitIncidentRecord,
  type AccountCircuitIncidentRebuildPage,
  type AccountCircuitIncidentState,
  type CompareAndSetAccountCircuitIncidentInput,
  type CompareAndSetAccountCircuitIncidentResult
} from '../../../storage/account-circuit-control-plane.repository.js'
import {
  accountCircuitConfirmationFailureCount,
  accountCircuitFailureEvidenceKeys,
  accountCircuitScopeKey,
  normalizeAccountCircuitConfirmationFailuresRequired,
  type AccountCircuitMutationResult,
  type AccountCircuitScope,
  type AccountCircuitState,
  type AccountCircuitStore
} from './account-circuit-store.js'
import { requestGatewayDbService } from './gateway-db-service-request.js'

export interface AccountCircuitControlPlaneBridgeOptions {
  store: AccountCircuitStore
  requestDb?: typeof requestGatewayDbService
  ownerId?: string
  retryDelayMs?: number
  maxPersistAttempts?: number
  closedRetentionMs?: number
  rebuildPageSize?: number
  rebuildMaxPages?: number
  rebuildPageTimeoutMs?: number
  rebuildTotalTimeoutMs?: number
  now?: () => number
  monotonicNow?: () => number
  persistIncident?: (
    input: CompareAndSetAccountCircuitIncidentInput
  ) => Promise<CompareAndSetAccountCircuitIncidentResult>
  loadRebuildPage?: (input: {
    nowMs: number
    afterUpdatedAtMs?: number
    afterCircuitScopeKey?: string
    limit: number
  }) => Promise<AccountCircuitIncidentRebuildPage>
  loadAccountIncidents?: (accountRuntimeKey: string) => Promise<AccountCircuitIncidentRecord[]>
}

export interface AccountCircuitControlPlaneRebuildResult {
  loaded: number
  blocked: boolean
  reason?:
    | 'runtime_state_rebuilding'
    | 'runtime_state_rebuild_failed'
    | 'runtime_state_rebuild_timeout'
    | 'runtime_state_rebuild_invalid_cursor'
    | 'runtime_state_rebuild_capacity_exhausted'
}

export interface PublicAccountCircuitSummary {
  status: 'normal' | 'verifying' | 'avoided' | 'recovering'
  reason?: 'connect_failed' | 'timeout_before_complete' | 'read_interrupted' | 'incomplete_response' | 'explicit_policy'
  since?: string
  nextCheckAt?: string
}

export async function loadPublicAccountCircuitSummaries(
  accountRuntimeKeys: string[]
): Promise<Record<string, PublicAccountCircuitSummary>> {
  const keys = [...new Set(accountRuntimeKeys.map((key) => key.trim()).filter(Boolean))].slice(0, 100)
  const incidents = await requestGatewayDbService({
    type: 'list_account_circuit_incidents_by_runtime_keys',
    accountRuntimeKeys: keys
  })
  return publicAccountCircuitSummariesFromIncidents(keys, incidents)
}

/**
 * Converts durable circuit rows to the public, per-runtime-key shape. The
 * projection worker can reuse this pure reducer with a direct PostgreSQL
 * read, while request hydration retains its DB-service boundary.
 */
export function publicAccountCircuitSummariesFromIncidents(
  accountRuntimeKeys: string[],
  incidents: AccountCircuitIncidentRecord[]
): Record<string, PublicAccountCircuitSummary> {
  const keys = [...new Set(accountRuntimeKeys.map((key) => key.trim()).filter(Boolean))].slice(0, 100)
  const grouped = new Map<string, typeof incidents>()
  for (const incident of incidents) {
    const values = grouped.get(incident.accountRuntimeKey) ?? []
    values.push(incident)
    grouped.set(incident.accountRuntimeKey, values)
  }
  return Object.fromEntries(keys.map((key) => [key, publicAccountCircuitSummary(grouped.get(key) ?? [])]))
}

/**
 * Coalesces runtime transitions to the latest state per scope and serializes
 * bounded persistence attempts into the DB ledger. Runtime state remains the
 * fast path; the outbox projector is the only component that acknowledges
 * durable projection progress.
 */
export class AccountCircuitControlPlaneBridge {
  private readonly store: AccountCircuitStore
  private readonly requestDb: typeof requestGatewayDbService
  private readonly ownerId: string
  private readonly retryDelayMs: number
  private readonly maxPersistAttempts: number
  private readonly closedRetentionMs: number
  private readonly rebuildPageSize: number
  private readonly rebuildMaxPages: number
  private readonly rebuildPageTimeoutMs: number
  private readonly rebuildTotalTimeoutMs: number
  private readonly now: () => number
  private readonly monotonicNow: () => number
  private readonly persistIncident: NonNullable<AccountCircuitControlPlaneBridgeOptions['persistIncident']>
  private readonly loadRebuildPage: NonNullable<AccountCircuitControlPlaneBridgeOptions['loadRebuildPage']>
  private readonly loadAccountIncidents: NonNullable<AccountCircuitControlPlaneBridgeOptions['loadAccountIncidents']>
  private readonly pending = new Map<string, { scope: AccountCircuitScope; state: AccountCircuitState }>()
  private readonly workers = new Map<string, Promise<void>>()
  private readonly retryTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private readonly retryBackoffs = new Map<string, number>()
  private readonly persistenceFailures = new Map<string, string>()
  private readonly ledgerRevisions = new Map<string, number>()
  private readonly dispatchRevisions = new Map<string, number>()
  private rebuilding = false
  private globallyReady = false
  private readonly readyAccountRuntimeKeys = new Set<string>()
  private readonly accountLoadPromises = new Map<string, Promise<boolean>>()
  private rebuildPromise?: Promise<AccountCircuitControlPlaneRebuildResult>
  private reconcileCursor?: { updatedAtMs: number; circuitScopeKey: string }

  constructor(options: AccountCircuitControlPlaneBridgeOptions) {
    this.store = options.store
    this.requestDb = options.requestDb ?? requestGatewayDbService
    this.ownerId = options.ownerId?.trim() || `circuit-bridge:${randomUUID()}`
    this.retryDelayMs = positive(options.retryDelayMs ?? 1_000)
    this.maxPersistAttempts = positive(options.maxPersistAttempts ?? 3)
    this.closedRetentionMs = positive(options.closedRetentionMs ?? 5 * 60_000)
    this.rebuildPageSize = positive(options.rebuildPageSize ?? 500)
    this.rebuildMaxPages = positive(options.rebuildMaxPages ?? 200)
    this.rebuildPageTimeoutMs = positive(options.rebuildPageTimeoutMs ?? 2_000)
    this.rebuildTotalTimeoutMs = positive(options.rebuildTotalTimeoutMs ?? 15_000)
    this.now = options.now ?? Date.now
    this.monotonicNow = options.monotonicNow ?? performance.now.bind(performance)
    this.persistIncident = options.persistIncident ?? (async (input) => await this.requestDb({
      type: 'compare_and_set_account_circuit_incident',
      input
    }, { timeoutMs: this.rebuildPageTimeoutMs }))
    this.loadRebuildPage = options.loadRebuildPage ?? (async (input) => await this.requestDb({
      type: 'list_account_circuit_incidents_for_rebuild',
      ...input
    }, { timeoutMs: this.rebuildPageTimeoutMs }))
    this.loadAccountIncidents = options.loadAccountIncidents ?? (async (accountRuntimeKey) => await this.requestDb({
      type: 'list_account_circuit_incidents_by_runtime_keys',
      accountRuntimeKeys: [accountRuntimeKey],
      includeRetainedClosed: true,
      nowMs: this.now()
    }, { timeoutMs: this.rebuildPageTimeoutMs }))
  }

  /** Global cold-start gate; scope persistence failures are account-local. */
  isReady(): boolean { return this.globallyReady }

  isAccountReady(accountRuntimeKey: string): boolean {
    const normalized = requiredRuntimeKey(accountRuntimeKey)
    return (this.globallyReady || this.readyAccountRuntimeKeys.has(normalized))
      && !this.hasAccountPersistenceFailure(normalized)
  }

  async ensureAccountReady(accountRuntimeKey: string): Promise<boolean> {
    const normalized = requiredRuntimeKey(accountRuntimeKey)
    if (this.isAccountReady(normalized)) return true
    const active = this.accountLoadPromises.get(normalized)
    if (active) return active
    const load = this.performAccountLoad(normalized).finally(() => {
      if (this.accountLoadPromises.get(normalized) === load) this.accountLoadPromises.delete(normalized)
    })
    this.accountLoadPromises.set(normalized, load)
    return load
  }

  observe(input: { scope: AccountCircuitScope; state: AccountCircuitState }): void {
    const scopeKey = input.state.scopeKey
    this.pending.set(scopeKey, input)
    this.startScopeWorker(scopeKey)
  }

  async rebuild(): Promise<AccountCircuitControlPlaneRebuildResult> {
    if (this.rebuildPromise) return this.rebuildPromise
    const rebuild = this.performRebuild()
    this.rebuildPromise = rebuild
    try {
      return await rebuild
    } finally {
      if (this.rebuildPromise === rebuild) this.rebuildPromise = undefined
    }
  }

  private async performRebuild(): Promise<AccountCircuitControlPlaneRebuildResult> {
    this.rebuilding = true
    let loaded = 0
    let cursor: { updatedAtMs: number; circuitScopeKey: string } | undefined
    const hierarchyScopeKeys = new Map<string, string>()
    const deferredParents: AccountCircuitIncidentRecord[] = []
    const startedAt = this.monotonicNow()
    try {
      for (let pageNumber = 1; ; pageNumber++) {
        if (pageNumber > this.rebuildMaxPages) {
          throw new AccountCircuitRebuildError('runtime_state_rebuild_invalid_cursor')
        }
        const remainingMs = this.rebuildTotalTimeoutMs - (this.monotonicNow() - startedAt)
        if (remainingMs <= 0) throw new AccountCircuitRebuildError('runtime_state_rebuild_timeout')
        const page = await withinTimeout(this.loadRebuildPage({
          nowMs: this.now(),
          ...(cursor ? { afterUpdatedAtMs: cursor.updatedAtMs, afterCircuitScopeKey: cursor.circuitScopeKey } : {}),
          limit: this.rebuildPageSize
        }), Math.min(this.rebuildPageTimeoutMs, remainingMs), 'runtime_state_rebuild_timeout')
        for (const incident of page.items) {
          hierarchyScopeKeys.set(incidentHierarchyKey(incident.accountRuntimeKey, incident.incidentId), incident.circuitScopeKey)
        }
        for (const incident of page.items) {
          this.ledgerRevisions.set(incident.circuitScopeKey, incident.ledgerRevision)
          this.dispatchRevisions.set(incident.accountId, incident.dispatchRevision)
          if (incident.childIncidentIds.length > 0) {
            deferredParents.push(incident)
            continue
          }
          const restored = await this.store.restore(incidentToRuntimeState(incident, hierarchyScopeKeys), this.now())
          this.observeRestoredRelationships(restored)
          if (restored.status === 'capacity_exhausted') {
            throw new AccountCircuitRebuildError('runtime_state_rebuild_capacity_exhausted')
          }
          loaded++
        }
        const nextCursor = page.nextCursor
        if (!nextCursor) break
        if (cursor && compareCursor(nextCursor, cursor) <= 0) {
          throw new AccountCircuitRebuildError('runtime_state_rebuild_invalid_cursor')
        }
        cursor = nextCursor
      }
      for (const incident of deferredParents) {
        const restored = await this.store.restore(incidentToRuntimeState(incident, hierarchyScopeKeys), this.now())
        this.observeRestoredRelationships(restored)
        if (restored.status === 'capacity_exhausted') {
          throw new AccountCircuitRebuildError('runtime_state_rebuild_capacity_exhausted')
        }
        loaded++
      }
      const remainingMs = this.rebuildTotalTimeoutMs - (this.monotonicNow() - startedAt)
      if (remainingMs <= 0) throw new AccountCircuitRebuildError('runtime_state_rebuild_timeout')
      await withinTimeout(
        this.retryPendingImmediately(),
        remainingMs,
        'runtime_state_rebuild_timeout'
      )
      this.globallyReady = true
      return { loaded, blocked: false }
    } catch (error) {
      return {
        loaded,
        blocked: true,
        reason: error instanceof AccountCircuitRebuildError
          ? error.reason
          : 'runtime_state_rebuild_failed'
      }
    } finally {
      this.rebuilding = false
    }
  }

  /**
   * Replays one bounded ledger page without closing the readiness gate. This
   * repairs evicted runtime keys and missing due-index members while keeping
   * request dispatch available after the initial rebuild has completed.
   */
  async reconcileActive(limit = 100): Promise<number> {
    if (this.rebuilding || !this.globallyReady) return 0
    const page = await withinTimeout(this.loadRebuildPage({
      nowMs: this.now(),
      ...(this.reconcileCursor
        ? {
            afterUpdatedAtMs: this.reconcileCursor.updatedAtMs,
            afterCircuitScopeKey: this.reconcileCursor.circuitScopeKey
          }
        : {}),
      limit: positive(limit)
    }), this.rebuildPageTimeoutMs, 'runtime_state_rebuild_timeout')
    let repaired = 0
    const hierarchyScopeKeys = incidentScopeKeyMap(page.items)
    for (const incident of page.items) {
      if (hasUnresolvedChildIncident(incident, hierarchyScopeKeys)) {
        addIncidentScopeKeys(hierarchyScopeKeys, await this.loadAccountIncidents(incident.accountRuntimeKey))
      }
      const restored = await this.store.restore(incidentToRuntimeState(incident, hierarchyScopeKeys), this.now())
      this.observeRestoredRelationships(restored)
      if (restored.status === 'capacity_exhausted') {
        throw new Error('账户 circuit runtime store 对账容量不足')
      }
      this.ledgerRevisions.set(incident.circuitScopeKey, incident.ledgerRevision)
      this.dispatchRevisions.set(incident.accountId, incident.dispatchRevision)
      repaired++
    }
    this.reconcileCursor = page.nextCursor
    return repaired
  }

  private async performAccountLoad(accountRuntimeKey: string): Promise<boolean> {
    try {
      const incidents = await withinTimeout(
        this.loadAccountIncidents(accountRuntimeKey),
        this.rebuildPageTimeoutMs,
        'runtime_state_rebuild_timeout'
      )
      const hierarchyScopeKeys = incidentScopeKeyMap(incidents)
      const orderedIncidents = [
        ...incidents.filter((incident) => incident.childIncidentIds.length === 0),
        ...incidents.filter((incident) => incident.childIncidentIds.length > 0)
      ]
      for (const incident of orderedIncidents) {
        if (incident.accountRuntimeKey !== accountRuntimeKey) return false
        const restored = await this.store.restore(incidentToRuntimeState(incident, hierarchyScopeKeys), this.now())
        this.observeRestoredRelationships(restored)
        if (restored.status === 'capacity_exhausted') return false
        this.ledgerRevisions.set(incident.circuitScopeKey, incident.ledgerRevision)
        this.dispatchRevisions.set(incident.accountId, incident.dispatchRevision)
      }
      this.readyAccountRuntimeKeys.add(accountRuntimeKey)
      return !this.hasAccountPersistenceFailure(accountRuntimeKey)
    } catch {
      return false
    }
  }

  private hasAccountPersistenceFailure(accountRuntimeKey: string): boolean {
    for (const failedAccountRuntimeKey of this.persistenceFailures.values()) {
      if (failedAccountRuntimeKey === accountRuntimeKey) return true
    }
    return false
  }

  async projectPending(limit = 100): Promise<number> {
    const claims = await this.requestDb({
      type: 'claim_account_circuit_outbox',
      ownerId: this.ownerId,
      nowMs: this.now(),
      leaseMs: 30_000,
      limit: positive(limit)
    })
    let acknowledged = 0
    for (const event of claims) {
      try {
        if (event.eventType === 'dispatch_revision_changed') {
          await this.store.replaceAccountDispatchRevision({
            accountRuntimeKey: event.accountRuntimeKey,
            dispatchRevision: String(event.dispatchRevision),
            transitionId: event.transitionId,
            nowMs: this.now()
          })
        } else {
          if (!event.circuitScopeKey) throw new Error('incident outbox 缺少 circuitScopeKey')
          const incident = await this.requestDb({
            type: 'get_account_circuit_incident_by_scope_key',
            circuitScopeKey: event.circuitScopeKey
          })
          if (!incident) throw new Error('incident outbox 对应 ledger 缺失')
          const hierarchyScopeKeys = incidentScopeKeyMap([incident])
          if (hasUnresolvedChildIncident(incident, hierarchyScopeKeys)) {
            addIncidentScopeKeys(hierarchyScopeKeys, await this.loadAccountIncidents(incident.accountRuntimeKey))
          }
          const projected = await this.store.restore(incidentToRuntimeState(incident, hierarchyScopeKeys), this.now())
          this.observeRestoredRelationships(projected)
          if (projected.status === 'capacity_exhausted') throw new Error('runtime circuit projection capacity exhausted')
        }
        if ((await this.requestDb({
          type: 'ack_account_circuit_outbox',
          eventId: event.eventId,
          projectionKey: event.projectionKey,
          claimToken: event.claimToken!,
          acknowledgedAtMs: this.now()
        })).acknowledged) acknowledged++
      } catch (error) {
        await this.requestDb({
          type: 'release_account_circuit_outbox_for_replay',
          eventId: event.eventId,
          claimToken: event.claimToken!,
          errorClass: classifyError(error),
          nowMs: this.now(),
          retryDelayMs: this.retryDelayMs
        })
      }
    }
    return acknowledged
  }

  private async persistWithRetry(scope: AccountCircuitScope, state: AccountCircuitState): Promise<void> {
    let delay = this.retryDelayMs
    const accountId = accountIdFromRuntimeKey(scope.accountRuntimeKey)
    let desiredState = state
    for (let attempt = 1; attempt <= this.maxPersistAttempts; attempt++) {
      try {
        const refreshed = await this.refreshDesiredState(scope, desiredState)
        if (!refreshed) return
        desiredState = refreshed
        const stateDispatchRevision = Number(desiredState.dispatchRevision)
        const dispatchRevision = Number.isSafeInteger(stateDispatchRevision) && stateDispatchRevision > 0
          ? stateDispatchRevision
          : this.dispatchRevisions.get(accountId) ?? 1
        this.dispatchRevisions.set(accountId, dispatchRevision)
        const transitionId = desiredState.transitionId || `rebuild:${desiredState.scopeKey}:${desiredState.generation}`
        const persisted = await this.persistIncident({
          accountId,
          accountRuntimeKey: scope.accountRuntimeKey,
          circuitScopeKey: desiredState.scopeKey,
          scopeKind: scope.kind,
          ...(scope.kind === 'key' ? { keyFingerprint: scope.keyFingerprint } : {}),
          ...(scope.kind === 'protocol_model' ? {
            protocolCode: scope.protocolProfile,
            requestLane: scope.requestLane,
            modelFamily: scope.modelBucket
          } : {}),
          incidentId: durableIncidentId(desiredState),
          ...(desiredState.shadowedByIncidentId
            ? { parentIncidentId: desiredState.shadowedByIncidentId }
            : {}),
          childIncidentIds: desiredState.childIncidentIds ?? [],
          state: desiredState.phase as AccountCircuitIncidentState,
          generation: desiredState.generation,
          dispatchRevision,
          expectedLedgerRevision: this.ledgerRevisions.get(desiredState.scopeKey) ?? null,
          transitionId,
          nextTransitionAtMs: desiredState.retryAtMs,
          openUntilMs: desiredState.retryAtMs,
          ...(desiredState.lease ? {
            leaseId: desiredState.lease.leaseId,
            leasePurpose: desiredState.lease.kind,
            leaseOwnerRunId: this.ownerId,
            leaseUntilMs: desiredState.lease.leaseUntilMs
          } : {}),
          backoffLevel: desiredState.backoffAttempt,
          consecutiveFailures: accountCircuitConfirmationFailureCount(desiredState),
          confirmationFailuresRequired: normalizeAccountCircuitConfirmationFailuresRequired(
            desiredState.confirmationFailuresRequired
          ),
          confirmationFailureEvidenceKeys: accountCircuitFailureEvidenceKeys(desiredState),
          recoveringSuccesses: desiredState.recoverySuccessCount,
          upstreamAttemptObserved: true,
          ...(desiredState.failureReason ? { lastFailureClass: classifyFailure(desiredState.failureReason) } : {}),
          ...(desiredState.phase === 'CLOSED' ? { retainedUntilMs: this.now() + this.closedRetentionMs } : {}),
          stateUpdatedAtMs: desiredState.updatedAtMs,
          nowMs: this.now()
        })
        // Physical account cleanup is a terminal outcome for late runtime
        // observations; do not retain a pending item or schedule retries.
        if (persisted.status === 'account_not_found') return
        this.dispatchRevisions.set(accountId, persisted.currentDispatchRevision)
        if (persisted.incident) this.ledgerRevisions.set(desiredState.scopeKey, persisted.incident.ledgerRevision)
        if (persisted.status === 'stale_dispatch_revision') return
        if (persisted.status === 'cas_conflict') {
          if (persisted.incident) {
            const runtimeState = await this.refreshDesiredState(scope, desiredState)
            if (!runtimeState) return
            if (incidentMatchesRuntimeState(persisted.incident, runtimeState)) return
            if (incidentIsNewerThanRuntimeState(persisted.incident, runtimeState)) {
              const restored = await this.store.restore(
                incidentToRuntimeState(persisted.incident, incidentScopeKeyMapFromRuntimeState(runtimeState)),
                this.now()
              )
              this.observeRestoredRelationships(restored)
              if (restored.status === 'capacity_exhausted') {
                throw new Error('账户 circuit runtime store 对账容量不足')
              }
              if (incidentMatchesRuntimeState(persisted.incident, restored.state)) return
              desiredState = restored.state
            } else {
              desiredState = runtimeState
            }
          }
          if (attempt === this.maxPersistAttempts) break
          await wait(Math.min(delay, 250))
          delay = Math.min(delay * 2, 30_000)
          continue
        }
        return
      } catch {
        if (attempt === this.maxPersistAttempts) break
        await wait(delay)
        delay = Math.min(delay * 2, 30_000)
      }
    }
    throw new Error('账户 circuit control-plane 持久化重试耗尽')
  }

  private async refreshDesiredState(
    scope: AccountCircuitScope,
    observedState: AccountCircuitState
  ): Promise<AccountCircuitState | undefined> {
    const runtimeState = await this.store.get(scope, this.now())
    if (!runtimeState.dispatchRevision || runtimeState.generation < observedState.generation) return observedState
    if (runtimeState.dispatchRevision !== observedState.dispatchRevision) {
      const runtimeRevision = Number(runtimeState.dispatchRevision)
      const observedRevision = Number(observedState.dispatchRevision)
      if (Number.isSafeInteger(runtimeRevision) && Number.isSafeInteger(observedRevision)) {
        return runtimeRevision > observedRevision ? undefined : observedState
      }
      return undefined
    }
    if (runtimeState.generation > observedState.generation) return runtimeState
    if (runtimeState.generation === observedState.generation
      && (runtimeState.updatedAtMs > observedState.updatedAtMs
        || runtimeState.transitionId !== observedState.transitionId)) {
      return runtimeState
    }
    return observedState
  }

  private startScopeWorker(scopeKey: string): Promise<void> | undefined {
    if (this.workers.has(scopeKey) || this.retryTimers.has(scopeKey)) return this.workers.get(scopeKey)
    const worker = this.drainScope(scopeKey).finally(() => {
      if (this.workers.get(scopeKey) === worker) this.workers.delete(scopeKey)
      if (this.pending.has(scopeKey)) this.scheduleScopeRetry(scopeKey)
    })
    this.workers.set(scopeKey, worker)
    return worker
  }

  private observeRestoredRelationships(result: AccountCircuitMutationResult): void {
    for (const state of result.relatedStates ?? []) {
      this.observe({ scope: state.scope, state })
    }
  }

  private async drainScope(scopeKey: string): Promise<void> {
    for (;;) {
      const current = this.pending.get(scopeKey)
      if (!current) return
      this.pending.delete(scopeKey)
      try {
        await this.persistWithRetry(current.scope, current.state)
        this.retryBackoffs.delete(scopeKey)
        if (!this.pending.has(scopeKey)) this.persistenceFailures.delete(scopeKey)
      } catch {
        if (!this.pending.has(scopeKey)) this.pending.set(scopeKey, current)
        this.persistenceFailures.set(scopeKey, current.scope.accountRuntimeKey)
        return
      }
    }
  }

  private scheduleScopeRetry(scopeKey: string): void {
    if (this.retryTimers.has(scopeKey)) return
    const delay = this.retryBackoffs.get(scopeKey) ?? this.retryDelayMs
    this.retryBackoffs.set(scopeKey, Math.min(delay * 2, 30_000))
    const timer = setTimeout(() => {
      if (this.retryTimers.get(scopeKey) === timer) this.retryTimers.delete(scopeKey)
      this.startScopeWorker(scopeKey)
    }, delay)
    timer.unref()
    this.retryTimers.set(scopeKey, timer)
  }

  private async retryPendingImmediately(): Promise<void> {
    for (const [scopeKey, timer] of this.retryTimers) {
      clearTimeout(timer)
      this.retryTimers.delete(scopeKey)
    }
    const workers = [...this.pending.keys()]
      .map((scopeKey) => this.startScopeWorker(scopeKey))
      .filter((worker): worker is Promise<void> => worker !== undefined)
    await Promise.all(workers)
  }
}

function incidentToRuntimeState(
  incident: AccountCircuitIncidentRecord,
  hierarchyScopeKeys: ReadonlyMap<string, string> = new Map()
): AccountCircuitState {
  const scope: AccountCircuitScope = incident.scopeKind === 'account'
    ? { kind: 'account', accountRuntimeKey: incident.accountRuntimeKey }
    : incident.scopeKind === 'key'
      ? {
          kind: 'key',
          accountRuntimeKey: incident.accountRuntimeKey,
          keyFingerprint: required(incident.keyFingerprint, 'keyFingerprint')
        }
      : {
          kind: 'protocol_model',
          accountRuntimeKey: incident.accountRuntimeKey,
          protocolProfile: required(incident.protocolCode, 'protocolCode'),
          requestLane: requiredRequestLane(incident.requestLane),
          modelBucket: required(incident.modelFamily, 'modelFamily')
        }
  if (incident.circuitScopeKey !== accountCircuitScopeKey(scope)) {
    throw new Error('持久化账户 circuit scopeKey 与作用域字段不一致')
  }
  const leaseKind = incident.leasePurpose === 'confirmation'
    ? 'confirmation'
    : incident.leasePurpose === 'half_open'
      ? 'half_open'
      : incident.leasePurpose === 'recovery'
        ? 'recovery'
        : undefined
  const childScopeKeys = incident.childIncidentIds
    .map((incidentId) => hierarchyScopeKeys.get(incidentHierarchyKey(incident.accountRuntimeKey, incidentId)))
    .filter((scopeKey): scopeKey is string => scopeKey !== undefined)
  return {
    scopeKey: incident.circuitScopeKey,
    scope,
    phase: runtimePhase(incident.state),
    generation: incident.generation,
    dispatchRevision: String(incident.dispatchRevision),
    transitionId: incident.transitionId,
    incidentId: incident.incidentId,
    ...(incident.parentIncidentId ? { shadowedByIncidentId: incident.parentIncidentId } : {}),
    ...(incident.childIncidentIds.length > 0 ? { childIncidentIds: [...incident.childIncidentIds] } : {}),
    ...(childScopeKeys.length > 0
      ? {
          childScopeKeys,
          requiredRecoveryScopeKeys: [...childScopeKeys]
        }
      : {}),
    backoffAttempt: incident.backoffLevel,
    recoverySuccessCount: incident.recoveringSuccesses,
    confirmationFailuresRequired: incident.confirmationFailuresRequired,
    confirmationFailureCount: incident.consecutiveFailures,
    failureEvidenceKeys: [...incident.confirmationFailureEvidenceKeys],
    ...(incident.openUntilMs !== undefined ? { openedAtMs: incident.updatedAtMs } : {}),
    ...(incident.nextTransitionAtMs !== undefined ? { retryAtMs: incident.nextTransitionAtMs } : {}),
    ...(incident.lastFailureClass ? { failureReason: incident.lastFailureClass } : {}),
    ...(leaseKind && incident.leaseId && incident.leaseUntilMs !== undefined
      ? { lease: { kind: leaseKind, leaseId: incident.leaseId, leaseUntilMs: incident.leaseUntilMs } }
      : {}),
    ...(incident.state === 'HALF_OPEN' && leaseKind === 'half_open' ? { halfOpenOrigin: 'OPEN' as const } : {}),
    ...(incident.state === 'HALF_OPEN' && leaseKind === 'recovery' ? { halfOpenOrigin: 'RECOVERING' as const } : {}),
    updatedAtMs: incident.updatedAtMs
  }
}

function incidentMatchesRuntimeState(
  incident: AccountCircuitIncidentRecord,
  state: AccountCircuitState
): boolean {
  return incident.dispatchRevision === Number(state.dispatchRevision)
    && incident.generation === state.generation
    && runtimePhase(incident.state) === state.phase
    && incident.transitionId === state.transitionId
    && incident.incidentId === durableIncidentId(state)
    && (incident.parentIncidentId ?? '') === (state.shadowedByIncidentId ?? '')
    && sameStringSet(incident.childIncidentIds, state.childIncidentIds ?? [])
    && incident.updatedAtMs === state.updatedAtMs
}

function durableIncidentId(state: AccountCircuitState): string {
  return state.incidentId?.trim() || state.transitionId
}

function incidentHierarchyKey(accountRuntimeKey: string, incidentId: string): string {
  return `${accountRuntimeKey.length}:${accountRuntimeKey}|${incidentId}`
}

function incidentScopeKeyMap(incidents: AccountCircuitIncidentRecord[]): Map<string, string> {
  const result = new Map<string, string>()
  addIncidentScopeKeys(result, incidents)
  return result
}

function addIncidentScopeKeys(
  target: Map<string, string>,
  incidents: AccountCircuitIncidentRecord[]
): void {
  for (const incident of incidents) {
    target.set(incidentHierarchyKey(incident.accountRuntimeKey, incident.incidentId), incident.circuitScopeKey)
  }
}

function incidentScopeKeyMapFromRuntimeState(state: AccountCircuitState): Map<string, string> {
  const result = new Map<string, string>()
  for (const [index, incidentId] of (state.childIncidentIds ?? []).entries()) {
    const scopeKey = state.childScopeKeys?.[index]
    if (scopeKey) result.set(incidentHierarchyKey(state.scope.accountRuntimeKey, incidentId), scopeKey)
  }
  return result
}

function hasUnresolvedChildIncident(
  incident: AccountCircuitIncidentRecord,
  hierarchyScopeKeys: ReadonlyMap<string, string>
): boolean {
  return incident.childIncidentIds.some((incidentId) => (
    !hierarchyScopeKeys.has(incidentHierarchyKey(incident.accountRuntimeKey, incidentId))
  ))
}

function sameStringSet(left: string[], right: string[]): boolean {
  if (left.length !== right.length) return false
  const values = new Set(left)
  return values.size === right.length && right.every((value) => values.has(value))
}

function incidentIsNewerThanRuntimeState(
  incident: AccountCircuitIncidentRecord,
  state: AccountCircuitState
): boolean {
  const runtimeDispatchRevision = Number(state.dispatchRevision)
  if (Number.isSafeInteger(runtimeDispatchRevision)
    && incident.dispatchRevision !== runtimeDispatchRevision) {
    return incident.dispatchRevision > runtimeDispatchRevision
  }
  return incident.generation > state.generation
}

function runtimePhase(state: AccountCircuitIncidentState): AccountCircuitState['phase'] {
  if (state === 'PERSISTING' || state === 'SHADOWED_BY_PERSISTENT') return 'OPEN'
  return state
}

function required(value: string | undefined, name: string): string {
  if (!value?.trim()) throw new Error(`账户 circuit incident 缺少 ${name}`)
  return value.trim()
}

function requiredRequestLane(value: string | undefined): 'text' | 'image' {
  if (value !== 'text' && value !== 'image') throw new Error('持久化账户 circuit requestLane 无效')
  return value
}

export function publicAccountCircuitSummary(incidents: AccountCircuitIncidentRecord[]): PublicAccountCircuitSummary {
  if (incidents.length === 0) return { status: 'normal' }
  const selected = [...incidents].sort((left, right) => {
    const priority = (state: AccountCircuitIncidentState): number => {
      if (state === 'OPEN' || state === 'PERSISTING' || state === 'SHADOWED_BY_PERSISTENT') return 3
      if (state === 'HALF_OPEN' || state === 'SUSPECT') return 2
      if (state === 'RECOVERING') return 1
      return 0
    }
    return priority(right.state) - priority(left.state) || left.updatedAtMs - right.updatedAtMs
  })[0]!
  const status = selected.state === 'OPEN' || selected.state === 'PERSISTING' || selected.state === 'SHADOWED_BY_PERSISTENT'
    ? 'avoided'
    : selected.state === 'RECOVERING'
      ? 'recovering'
      : 'verifying'
  const nextCheckMs = incidents
    .map((item) => item.nextTransitionAtMs)
    .filter((value): value is number => value !== undefined)
    .sort((left, right) => left - right)[0]
  return {
    status,
    ...(selected.lastFailureClass ? { reason: selected.lastFailureClass } : {}),
    since: new Date(selected.updatedAtMs).toISOString(),
    ...(nextCheckMs !== undefined ? { nextCheckAt: new Date(nextCheckMs).toISOString() } : {})
  }
}

function accountIdFromRuntimeKey(runtimeKey: string): string {
  const accountId = runtimeKey.split(':', 1)[0]?.trim()
  if (!accountId) throw new Error('账户 circuit runtime key 缺少 accountId')
  return accountId
}

function classifyFailure(reason: string): 'connect_failed' | 'timeout_before_complete' | 'read_interrupted' | 'incomplete_response' | 'explicit_policy' {
  const value = reason.toLowerCase()
  if (value.includes('timeout')) return 'timeout_before_complete'
  if (value.includes('connect')) return 'connect_failed'
  if (value.includes('read')) return 'read_interrupted'
  if (value.includes('policy')) return 'explicit_policy'
  return 'incomplete_response'
}

function classifyError(error: unknown): string {
  return error instanceof Error ? error.name.slice(0, 64) : 'projector_error'
}

function positive(value: number): number {
  if (!Number.isFinite(value) || value <= 0) throw new Error('control-plane 数值必须为正')
  return Math.trunc(value)
}

function requiredRuntimeKey(value: string): string {
  const normalized = value.trim()
  if (!normalized) throw new Error('账户 circuit runtime key 不能为空')
  return normalized
}

function compareCursor(
  left: { updatedAtMs: number; circuitScopeKey: string },
  right: { updatedAtMs: number; circuitScopeKey: string }
): number {
  return left.updatedAtMs - right.updatedAtMs || left.circuitScopeKey.localeCompare(right.circuitScopeKey)
}

class AccountCircuitRebuildError extends Error {
  constructor(readonly reason: NonNullable<AccountCircuitControlPlaneRebuildResult['reason']>) {
    super(reason)
    this.name = 'AccountCircuitRebuildError'
  }
}

async function withinTimeout<T>(
  operation: Promise<T>,
  timeoutMs: number,
  reason: NonNullable<AccountCircuitControlPlaneRebuildResult['reason']>
): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => reject(new AccountCircuitRebuildError(reason)), Math.max(1, Math.trunc(timeoutMs)))
  })
  try {
    return await Promise.race([operation, timeout])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

function wait(ms: number): Promise<void> { return new Promise((resolve) => setTimeout(resolve, ms)) }
