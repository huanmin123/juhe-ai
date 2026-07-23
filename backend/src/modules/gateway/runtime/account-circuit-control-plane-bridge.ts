import { randomUUID } from 'node:crypto'

import {
  type AccountCircuitIncidentRecord,
  type AccountCircuitIncidentRebuildPage,
  type AccountCircuitIncidentState,
  type CompareAndSetAccountCircuitIncidentInput,
  type CompareAndSetAccountCircuitIncidentResult
} from '../../../storage/account-circuit-control-plane.repository.js'
import { requestDbService } from '../../db-service/db-service-ipc.js'
import type { AccountCircuitScope, AccountCircuitState, AccountCircuitStore } from './account-circuit-store.js'

export interface AccountCircuitControlPlaneBridgeOptions {
  store: AccountCircuitStore
  ownerId?: string
  retryDelayMs?: number
  maxPersistAttempts?: number
  closedRetentionMs?: number
  now?: () => number
  persistIncident?: (
    input: CompareAndSetAccountCircuitIncidentInput
  ) => Promise<CompareAndSetAccountCircuitIncidentResult>
  loadRebuildPage?: (input: {
    nowMs: number
    afterUpdatedAtMs?: number
    afterCircuitScopeKey?: string
    limit: number
  }) => Promise<AccountCircuitIncidentRebuildPage>
}

export interface AccountCircuitControlPlaneRebuildResult {
  loaded: number
  blocked: boolean
  reason?: 'runtime_state_rebuilding' | 'runtime_state_rebuild_failed'
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
  const incidents = await requestDbService({
    type: 'list_account_circuit_incidents_by_runtime_keys',
    accountRuntimeKeys: keys
  })
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
  private readonly ownerId: string
  private readonly retryDelayMs: number
  private readonly maxPersistAttempts: number
  private readonly closedRetentionMs: number
  private readonly now: () => number
  private readonly persistIncident: NonNullable<AccountCircuitControlPlaneBridgeOptions['persistIncident']>
  private readonly loadRebuildPage: NonNullable<AccountCircuitControlPlaneBridgeOptions['loadRebuildPage']>
  private readonly pending = new Map<string, { scope: AccountCircuitScope; state: AccountCircuitState }>()
  private readonly workers = new Map<string, Promise<void>>()
  private readonly retryTimers = new Map<string, ReturnType<typeof setTimeout>>()
  private readonly retryBackoffs = new Map<string, number>()
  private readonly persistenceFailures = new Set<string>()
  private readonly ledgerRevisions = new Map<string, number>()
  private readonly dispatchRevisions = new Map<string, number>()
  private rebuilding = true
  private rebuildPromise?: Promise<AccountCircuitControlPlaneRebuildResult>
  private reconcileCursor?: { updatedAtMs: number; circuitScopeKey: string }

  constructor(options: AccountCircuitControlPlaneBridgeOptions) {
    this.store = options.store
    this.ownerId = options.ownerId?.trim() || `circuit-bridge:${randomUUID()}`
    this.retryDelayMs = positive(options.retryDelayMs ?? 1_000)
    this.maxPersistAttempts = positive(options.maxPersistAttempts ?? 3)
    this.closedRetentionMs = positive(options.closedRetentionMs ?? 5 * 60_000)
    this.now = options.now ?? Date.now
    this.persistIncident = options.persistIncident ?? (async (input) => await requestDbService({
      type: 'compare_and_set_account_circuit_incident',
      input
    }))
    this.loadRebuildPage = options.loadRebuildPage ?? (async (input) => await requestDbService({
      type: 'list_account_circuit_incidents_for_rebuild',
      ...input
    }))
  }

  isReady(): boolean { return !this.rebuilding && this.persistenceFailures.size === 0 }

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
    try {
      for (;;) {
        const page = await this.loadRebuildPage({
          nowMs: this.now(),
          ...(cursor ? { afterUpdatedAtMs: cursor.updatedAtMs, afterCircuitScopeKey: cursor.circuitScopeKey } : {}),
          limit: 500
        })
        for (const incident of page.items) {
          const restored = await this.store.restore(incidentToRuntimeState(incident), this.now())
          if (restored.status === 'capacity_exhausted') {
            throw new Error('账户 circuit runtime store 重建容量不足')
          }
          loaded++
          this.ledgerRevisions.set(incident.circuitScopeKey, incident.ledgerRevision)
          this.dispatchRevisions.set(incident.accountId, incident.dispatchRevision)
        }
        cursor = page.nextCursor
        if (!cursor) break
      }
      await this.retryPendingImmediately()
      if (this.persistenceFailures.size > 0) {
        throw new Error('账户 circuit control-plane 仍有未持久化状态')
      }
      this.rebuilding = false
      return { loaded, blocked: false }
    } catch {
      // Missing/failed durable state must remain fail-closed; callers should keep
      // routing blocked and retry startup reconciliation.
      this.rebuilding = true
      return { loaded, blocked: true, reason: 'runtime_state_rebuild_failed' }
    }
  }

  /**
   * Replays one bounded ledger page without closing the readiness gate. This
   * repairs evicted runtime keys and missing due-index members while keeping
   * request dispatch available after the initial rebuild has completed.
   */
  async reconcileActive(limit = 100): Promise<number> {
    if (this.rebuilding) return 0
    const page = await this.loadRebuildPage({
      nowMs: this.now(),
      ...(this.reconcileCursor
        ? {
            afterUpdatedAtMs: this.reconcileCursor.updatedAtMs,
            afterCircuitScopeKey: this.reconcileCursor.circuitScopeKey
          }
        : {}),
      limit: positive(limit)
    })
    let repaired = 0
    for (const incident of page.items) {
      const restored = await this.store.restore(incidentToRuntimeState(incident), this.now())
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

  async projectPending(limit = 100): Promise<number> {
    const claims = await requestDbService({
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
          const incident = await findActiveIncident(event.circuitScopeKey)
          if (!incident) throw new Error('incident outbox 对应 ledger 缺失')
          const projected = await this.store.restore(incidentToRuntimeState(incident), this.now())
          if (projected.status === 'capacity_exhausted') throw new Error('runtime circuit projection capacity exhausted')
        }
        if ((await requestDbService({
          type: 'ack_account_circuit_outbox',
          eventId: event.eventId,
          projectionKey: event.projectionKey,
          claimToken: event.claimToken!,
          acknowledgedAtMs: this.now()
        })).acknowledged) acknowledged++
      } catch (error) {
        await requestDbService({
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
    const stateDispatchRevision = Number(state.dispatchRevision)
    const dispatchRevision = Number.isSafeInteger(stateDispatchRevision) && stateDispatchRevision > 0
      ? stateDispatchRevision
      : this.dispatchRevisions.get(accountId) ?? 1
    this.dispatchRevisions.set(accountId, dispatchRevision)
    for (let attempt = 1; attempt <= this.maxPersistAttempts; attempt++) {
      try {
        const transitionId = state.transitionId || `rebuild:${state.scopeKey}:${state.generation}`
        const persisted = await this.persistIncident({
          accountId,
          accountRuntimeKey: scope.accountRuntimeKey,
          circuitScopeKey: state.scopeKey,
          scopeKind: scope.kind,
          ...(scope.kind === 'key' ? { keyFingerprint: scope.keyFingerprint } : {}),
          ...(scope.kind === 'protocol_model' ? {
            protocolCode: scope.protocolProfile,
            requestLane: scope.requestLane,
            modelFamily: scope.modelBucket
          } : {}),
          incidentId: `${state.scopeKey}:${state.generation}`,
          state: state.phase as AccountCircuitIncidentState,
          generation: state.generation,
          dispatchRevision,
          expectedLedgerRevision: this.ledgerRevisions.get(state.scopeKey) ?? null,
          transitionId,
          nextTransitionAtMs: state.retryAtMs,
          openUntilMs: state.retryAtMs,
          ...(state.lease ? {
            leaseId: state.lease.leaseId,
            leasePurpose: state.lease.kind,
            leaseOwnerRunId: this.ownerId,
            leaseUntilMs: state.lease.leaseUntilMs
          } : {}),
          backoffLevel: state.backoffAttempt,
          recoveringSuccesses: state.recoverySuccessCount,
          upstreamAttemptObserved: true,
          ...(state.failureReason ? { lastFailureClass: classifyFailure(state.failureReason) } : {}),
          ...(state.phase === 'CLOSED' ? { retainedUntilMs: this.now() + this.closedRetentionMs } : {}),
          nowMs: this.now()
        })
        this.dispatchRevisions.set(accountId, persisted.currentDispatchRevision)
        if (persisted.incident) this.ledgerRevisions.set(state.scopeKey, persisted.incident.ledgerRevision)
        if (persisted.status === 'stale_dispatch_revision') return
        if (persisted.status === 'cas_conflict') {
          if (persisted.incident && persisted.incident.generation >= state.generation) return
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

  private startScopeWorker(scopeKey: string): Promise<void> | undefined {
    if (this.workers.has(scopeKey) || this.retryTimers.has(scopeKey)) return this.workers.get(scopeKey)
    const worker = this.drainScope(scopeKey).finally(() => {
      if (this.workers.get(scopeKey) === worker) this.workers.delete(scopeKey)
      if (this.pending.has(scopeKey)) this.scheduleScopeRetry(scopeKey)
    })
    this.workers.set(scopeKey, worker)
    return worker
  }

  private async drainScope(scopeKey: string): Promise<void> {
    for (;;) {
      const current = this.pending.get(scopeKey)
      if (!current) return
      this.pending.delete(scopeKey)
      try {
        await this.persistWithRetry(current.scope, current.state)
        this.persistenceFailures.delete(scopeKey)
        this.retryBackoffs.delete(scopeKey)
      } catch {
        if (!this.pending.has(scopeKey)) this.pending.set(scopeKey, current)
        this.persistenceFailures.add(scopeKey)
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

async function loadAllActiveIncidents(): Promise<AccountCircuitIncidentRecord[]> {
  const items: AccountCircuitIncidentRecord[] = []
  let cursor: { updatedAtMs: number; circuitScopeKey: string } | undefined
  for (;;) {
    const page = await requestDbService({
      type: 'list_account_circuit_incidents_for_rebuild',
      ...(cursor ? { afterUpdatedAtMs: cursor.updatedAtMs, afterCircuitScopeKey: cursor.circuitScopeKey } : {}),
      limit: 500
    })
    items.push(...page.items)
    cursor = page.nextCursor
    if (!cursor) return items
  }
}

async function findActiveIncident(scopeKey: string): Promise<AccountCircuitIncidentRecord | undefined> {
  return (await loadAllActiveIncidents()).find((incident) => incident.circuitScopeKey === scopeKey)
}

function incidentToRuntimeState(incident: AccountCircuitIncidentRecord): AccountCircuitState {
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
          requestLane: incident.requestLane === 'image' ? 'image' : 'text',
          modelBucket: required(incident.modelFamily, 'modelFamily')
        }
  const leaseKind = incident.leasePurpose === 'confirmation'
    ? 'confirmation'
    : incident.leasePurpose === 'half_open'
      ? 'half_open'
      : incident.leasePurpose === 'recovery'
        ? 'recovery'
        : undefined
  return {
    scopeKey: incident.circuitScopeKey,
    scope,
    phase: runtimePhase(incident.state),
    generation: incident.generation,
    dispatchRevision: String(incident.dispatchRevision),
    transitionId: incident.transitionId,
    backoffAttempt: incident.backoffLevel,
    recoverySuccessCount: incident.recoveringSuccesses,
    ...(incident.openUntilMs !== undefined ? { openedAtMs: incident.updatedAtMs } : {}),
    ...(incident.nextTransitionAtMs !== undefined ? { retryAtMs: incident.nextTransitionAtMs } : {}),
    ...(leaseKind && incident.leaseId && incident.leaseUntilMs !== undefined
      ? { lease: { kind: leaseKind, leaseId: incident.leaseId, leaseUntilMs: incident.leaseUntilMs } }
      : {}),
    updatedAtMs: incident.updatedAtMs
  }
}

function runtimePhase(state: AccountCircuitIncidentState): AccountCircuitState['phase'] {
  if (state === 'PERSISTING' || state === 'SHADOWED_BY_PERSISTENT') return 'OPEN'
  return state
}

function required(value: string | undefined, name: string): string {
  if (!value?.trim()) throw new Error(`账户 circuit incident 缺少 ${name}`)
  return value.trim()
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

function wait(ms: number): Promise<void> { return new Promise((resolve) => setTimeout(resolve, ms)) }
