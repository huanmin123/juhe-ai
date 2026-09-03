import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../config/runtime.js'
import type { AccountStatus } from '../domain/types.js'
import { buildSystemAccountScopeClause, type AccessScope } from './access-scope.js'
import { canManageResourceOwner } from './resource-authorization-helpers.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'

export const defaultAccountLockDeathTimeoutSeconds = 300
export const defaultAccountLockRetryIntervalSeconds = 5

export type AccountLockStateName = 'UNLOCKED' | 'LOCKED_IDLE' | 'ENGAGED' | 'DEAD_CONFIRMED'

export interface AccountLockState {
  accountId: string
  enabled: boolean
  lockState: AccountLockStateName
  lockDeathTimeoutSeconds: number
  lockRetryIntervalSeconds: number
  incidentId?: string
  generation: number
  incidentStartedAt?: string
  deadlineAt?: string
  originalStatus?: AccountStatus
  provenance?: 'lock_policy'
  nextRetryAtMs?: number
  leaseId?: string
  leaseUntilMs?: number
  updatedAt: string
}

export interface AccountLockRetryLease {
  allowed: boolean
  waitMs: number
  leaseId?: string
}

export interface AccountLockObservation {
  generation: number
  incidentId?: string
  /**
   * When present (including null), the attempt observed the dispatch lease
   * ownership. This fences a late terminal event from clearing a newer lease.
   * Omitted is retained only for legacy callers that did not observe a lease.
   */
  leaseId?: string | null
}

// A crashed attempt must eventually become reclaimable, while normal attempts
// release this lease explicitly after their transport lifecycle settles.
export const accountLockDispatchLeaseDurationMs = 300_000
export const accountLockReservationLeaseDurationMs = 60_000

/**
 * A due time that is already in the past must not be used as the start of a
 * newly reserved lease: doing so makes the lease expired before it can be
 * consumed. Preserve future due times, but claim an overdue retry now.
 */
export function accountLockRetryReservationDueAtMs(nextRetryAtMs: number | undefined, nowMs: number): number | undefined {
  if (nextRetryAtMs === undefined) return undefined
  return Math.max(nextRetryAtMs, nowMs)
}

interface AccountLockRow {
  account_id: string
  enabled: number | boolean | string
  lock_state: AccountLockStateName
  lock_death_timeout_seconds: number | string
  lock_retry_interval_seconds: number | string
  incident_id: string | null
  generation: number | string
  incident_started_at: string | null
  deadline_at: string | null
  original_status: AccountStatus | null
  provenance: 'lock_policy' | null
  next_retry_at_ms: number | string | null
  lease_id: string | null
  lease_until_ms: number | string | null
  updated_at: string
}

export function normalizeAccountLockDeathTimeoutSeconds(value: unknown): number {
  return normalizeInteger(value, defaultAccountLockDeathTimeoutSeconds, 30, 3600, '锁死死亡窗口')
}

export function normalizeAccountLockRetryIntervalSeconds(value: unknown): number {
  return normalizeInteger(value, defaultAccountLockRetryIntervalSeconds, 5, 30, '锁死重试间隔')
}

export async function findAccountLockStateAsync(accountId: string): Promise<AccountLockState | undefined> {
  const id = accountId.trim()
  if (!id) return undefined
  const client = await accountLockClient()
  const row = await client.one<AccountLockRow>(`
    SELECT account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
      incident_id, generation, incident_started_at, deadline_at, original_status, provenance,
      next_retry_at_ms, lease_id, lease_until_ms, updated_at
    FROM ${table(client, 'account_lock_states')}
    WHERE account_id = ?
  `, [id])
  if (!row) return undefined
  const state = stateFromRow(row)
  if (state.enabled && state.lockState === 'DEAD_CONFIRMED') {
    const account = await client.one<{ status: AccountStatus; schedulable: number | boolean | string }>(`
      SELECT status, schedulable FROM ${table(client, 'accounts')} WHERE id = ? AND deleted_at IS NULL
    `, [id])
    if (account?.status === 'active' && Boolean(Number(account.schedulable))) {
      const recovered = { ...state, lockState: 'LOCKED_IDLE' as const, incidentId: undefined, incidentStartedAt: undefined, deadlineAt: undefined, provenance: undefined, originalStatus: undefined, updatedAt: nowIso() }
      const recoveryResult = await client.execute(`
        UPDATE ${table(client, 'account_lock_states')}
        SET lock_state = 'LOCKED_IDLE', incident_id = NULL, incident_started_at = NULL, deadline_at = NULL,
            original_status = NULL, provenance = NULL, next_retry_at_ms = NULL, lease_id = NULL,
            lease_until_ms = NULL, updated_at = ?
        WHERE account_id = ? AND lock_state = 'DEAD_CONFIRMED' AND generation = ?
      `, [recovered.updatedAt, id, state.generation])
      return recoveryResult.changes === 1 ? recovered : findAccountLockStateAsync(id)
    }
  }
  return state
}

export async function listAccountLockStatesAsync(accountIds: readonly string[]): Promise<Map<string, AccountLockState>> {
  const ids = [...new Set(accountIds.map((id) => id.trim()).filter(Boolean))]
  const result = new Map<string, AccountLockState>()
  await Promise.all(ids.map(async (id) => {
    const state = await findAccountLockStateAsync(id)
    if (state) result.set(id, state)
  }))
  return result
}

export async function setAccountLockAsync(input: {
  accountId: string
  enabled: boolean
  expectedConfigRevision?: number
  lockDeathTimeoutSeconds?: unknown
  lockRetryIntervalSeconds?: unknown
  access?: AccessScope
  expectedLockGeneration?: number
}): Promise<AccountLockState | undefined> {
  const id = input.accountId.trim()
  if (!id) throw new Error('账户不存在')
  const client = await accountLockClient()
  const updated = await client.transaction(async (tx) => {
    const scope = buildSystemAccountScopeClause(input.access, 'accounts.system_account_id')
    const account = await tx.one<{ id: string; system_account_id: string; status: AccountStatus; config_revision: number | string }>(`
      SELECT id, system_account_id, status, config_revision FROM ${table(tx, 'accounts')}
      WHERE id = ? AND deleted_at IS NULL${scope.clause}
    `, [id, ...scope.params])
    if (!account || !canManageResourceOwner(account.system_account_id, input.access)) return undefined
    if (input.expectedConfigRevision !== undefined && Number(account.config_revision) !== input.expectedConfigRevision) {
      throw new Error('账户配置已发生并发变更，请刷新列表后重试')
    }
    const now = nowIso()
    const existing = await tx.one<AccountLockRow>(`
      SELECT account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
        incident_id, generation, incident_started_at, deadline_at, original_status, provenance,
        next_retry_at_ms, lease_id, lease_until_ms, updated_at
      FROM ${table(tx, 'account_lock_states')} WHERE account_id = ?
    `, [id])
    const previous = existing ? stateFromRow(existing) : undefined
    if (input.expectedLockGeneration !== undefined && Number(previous?.generation ?? 0) !== input.expectedLockGeneration) {
      throw new Error('账户锁死状态已发生并发变更，请刷新列表后重试')
    }
    const timeout = input.lockDeathTimeoutSeconds === undefined
      ? previous?.lockDeathTimeoutSeconds ?? defaultAccountLockDeathTimeoutSeconds
      : normalizeAccountLockDeathTimeoutSeconds(input.lockDeathTimeoutSeconds)
    const interval = input.lockRetryIntervalSeconds === undefined
      ? previous?.lockRetryIntervalSeconds ?? defaultAccountLockRetryIntervalSeconds
      : normalizeAccountLockRetryIntervalSeconds(input.lockRetryIntervalSeconds)
    const preserveActiveIncident = input.enabled && previous?.enabled && previous.lockState === 'ENGAGED'
    const retryConfigChanged = previous !== undefined && previous.lockRetryIntervalSeconds !== interval
    const deathConfigChanged = previous !== undefined && previous.lockDeathTimeoutSeconds !== timeout
    const preservedIncidentStartedAt = preserveActiveIncident ? previous?.incidentStartedAt ?? null : null
    const preservedDeadlineAt = preserveActiveIncident
      ? deathConfigChanged && preservedIncidentStartedAt
        ? new Date(Date.parse(preservedIncidentStartedAt) + timeout * 1000).toISOString()
        : previous?.deadlineAt ?? null
      : null
    const nextState: AccountLockStateName = input.enabled
      ? preserveActiveIncident ? 'ENGAGED' : 'LOCKED_IDLE'
      : 'UNLOCKED'
    const generation = Math.max(1, Number(previous?.generation ?? 0) + 1)
    const upsertResult = await tx.execute(`
      INSERT INTO ${table(tx, 'account_lock_states')} (
        account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
        incident_id, generation, incident_started_at, deadline_at, original_status, provenance,
        next_retry_at_ms, lease_id, lease_until_ms, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT(account_id) DO UPDATE SET
        enabled = excluded.enabled,
        lock_state = excluded.lock_state,
        lock_death_timeout_seconds = excluded.lock_death_timeout_seconds,
        lock_retry_interval_seconds = excluded.lock_retry_interval_seconds,
        incident_id = excluded.incident_id, incident_started_at = excluded.incident_started_at,
        deadline_at = excluded.deadline_at, original_status = excluded.original_status,
        provenance = excluded.provenance, next_retry_at_ms = excluded.next_retry_at_ms,
         lease_id = excluded.lease_id, lease_until_ms = excluded.lease_until_ms, generation = excluded.generation,
         updated_at = excluded.updated_at
       WHERE ${table(tx, 'account_lock_states')}.generation = ?
    `, [
      id, input.enabled ? 1 : 0, nextState, timeout, interval,
      preserveActiveIncident ? previous?.incidentId ?? null : null,
      generation,
       preservedIncidentStartedAt,
       preservedDeadlineAt,
      preserveActiveIncident ? previous?.originalStatus ?? null : null,
      preserveActiveIncident ? previous?.provenance ?? null : null,
       preserveActiveIncident && !retryConfigChanged && !deathConfigChanged ? previous?.nextRetryAtMs ?? null : null,
       preserveActiveIncident && !retryConfigChanged && !deathConfigChanged ? previous?.leaseId ?? null : null,
       preserveActiveIncident && !retryConfigChanged && !deathConfigChanged ? previous?.leaseUntilMs ?? null : null,
       now,
       Number(previous?.generation ?? 0)
     ])
    if (upsertResult.changes !== 1) {
      throw new Error('账户锁死状态已发生并发变更，请刷新列表后重试')
    }
    const revisionUpdated = await tx.execute(`
      UPDATE ${table(tx, 'accounts')}
      SET config_revision = config_revision + 1, updated_at = ?
      WHERE id = ? AND deleted_at IS NULL AND config_revision = ?
    `, [now, id, Number(account.config_revision)])
    if (revisionUpdated.changes !== 1) {
      throw new Error('账户配置已发生并发变更，请刷新列表后重试')
    }
    return {
      accountId: id,
      enabled: input.enabled,
      lockState: nextState,
      lockDeathTimeoutSeconds: timeout,
      lockRetryIntervalSeconds: interval,
      generation,
      ...(preserveActiveIncident && previous?.incidentId ? { incidentId: previous.incidentId } : {}),
      ...(preserveActiveIncident && previous?.incidentStartedAt ? { incidentStartedAt: previous.incidentStartedAt } : {}),
      ...(preserveActiveIncident && preservedDeadlineAt ? { deadlineAt: preservedDeadlineAt } : {}),
      ...(preserveActiveIncident && previous?.originalStatus ? { originalStatus: previous.originalStatus } : {}),
      ...(preserveActiveIncident && previous?.provenance ? { provenance: previous.provenance } : {}),
      ...(preserveActiveIncident && !retryConfigChanged && !deathConfigChanged && previous?.nextRetryAtMs ? { nextRetryAtMs: previous.nextRetryAtMs } : {}),
      ...(preserveActiveIncident && !retryConfigChanged && !deathConfigChanged && previous?.leaseId ? { leaseId: previous.leaseId } : {}),
      ...(preserveActiveIncident && !retryConfigChanged && !deathConfigChanged && previous?.leaseUntilMs ? { leaseUntilMs: previous.leaseUntilMs } : {}),
      updatedAt: now
    } satisfies AccountLockState
  })
  if (!updated) return undefined
  // Do not overwrite a concurrently advanced incident/lease with a stale
  // configuration snapshot. The database row is authoritative after commit.
  const clientAfterCommit = await accountLockClient()
  const rowAfterCommit = await clientAfterCommit.one<AccountLockRow>(`
    SELECT account_id, enabled, lock_state, lock_death_timeout_seconds, lock_retry_interval_seconds,
      incident_id, generation, incident_started_at, deadline_at, original_status, provenance,
      next_retry_at_ms, lease_id, lease_until_ms, updated_at
    FROM ${table(clientAfterCommit, 'account_lock_states')} WHERE account_id = ?
  `, [id])
  const persisted = rowAfterCommit ? stateFromRow(rowAfterCommit) : undefined
  if (!persisted) return updated
  return persisted
}

export async function updateAccountLockConfigAsync(input: {
  accountId: string
  expectedConfigRevision?: number
  lockDeathTimeoutSeconds?: unknown
  lockRetryIntervalSeconds?: unknown
  access?: AccessScope
}): Promise<AccountLockState | undefined> {
  if (input.lockDeathTimeoutSeconds === undefined && input.lockRetryIntervalSeconds === undefined) {
    throw new Error('请至少提交一项锁死配置')
  }
  const current = await findAccountLockStateAsync(input.accountId)
  return setAccountLockAsync({
    ...input,
    enabled: current?.enabled ?? false,
    expectedLockGeneration: current?.generation
  })
}

export async function recordAccountLockFailureAsync(accountId: string, reason = 'gateway_failure', observation?: AccountLockObservation): Promise<AccountLockState | undefined> {
  const current = await findAccountLockStateAsync(accountId)
  if (!current?.enabled || current.lockState === 'DEAD_CONFIRMED') return current
  if (observation && !sameAccountLockObservation(current, observation)) return current
  if (current.lockState === 'ENGAGED') return current
  const nowMs = Date.now()
  const client = await accountLockClient()
  const account = await client.one<{ status: AccountStatus }>(`
    SELECT status FROM ${table(client, 'accounts')} WHERE id = ? AND deleted_at IS NULL
  `, [accountId])
  const next: AccountLockState = {
    ...current,
    lockState: 'ENGAGED',
    incidentId: `${accountId}:${current.generation + 1}:${randomUUID()}`,
    generation: current.generation + 1,
    incidentStartedAt: new Date(nowMs).toISOString(),
    deadlineAt: new Date(nowMs + current.lockDeathTimeoutSeconds * 1000).toISOString(),
    originalStatus: account?.status,
    provenance: undefined,
    nextRetryAtMs: undefined,
    leaseId: undefined,
    leaseUntilMs: undefined,
    updatedAt: new Date(nowMs).toISOString()
  }
  const leaseFence = accountLockLeaseFence(observation, nowMs)
  const result = await client.execute(`
    UPDATE ${table(client, 'account_lock_states')}
    SET lock_state = ?, incident_id = ?, generation = ?, incident_started_at = ?, deadline_at = ?,
        original_status = ?, provenance = NULL, next_retry_at_ms = NULL, lease_id = NULL,
        lease_until_ms = NULL, updated_at = ?
    WHERE account_id = ? AND enabled = 1 AND lock_state = 'LOCKED_IDLE' AND generation = ?${leaseFence.sql}
  `, [
    next.lockState, next.incidentId ?? null, next.generation, next.incidentStartedAt ?? null,
    next.deadlineAt ?? null, next.originalStatus ?? null, next.updatedAt, accountId, current.generation,
    ...leaseFence.params
  ])
  return result.changes === 1 ? next : findAccountLockStateAsync(accountId)
}

export async function completeAccountLockSuccessAsync(accountId: string, observation?: AccountLockObservation): Promise<AccountLockState | undefined> {
  const current = await findAccountLockStateAsync(accountId)
  if (!current?.enabled || current.lockState !== 'ENGAGED') return current
  if (observation && !sameAccountLockObservation(current, observation)) return current
  const completionNowMs = Date.now()
  const next: AccountLockState = { ...current, lockState: 'LOCKED_IDLE', incidentId: undefined, incidentStartedAt: undefined, deadlineAt: undefined, nextRetryAtMs: undefined, leaseId: undefined, leaseUntilMs: undefined, updatedAt: new Date(completionNowMs).toISOString() }
  const leaseFence = accountLockLeaseFence(observation, completionNowMs)
  const client = await accountLockClient()
  const result = await client.execute(`
    UPDATE ${table(client, 'account_lock_states')}
    SET lock_state = 'LOCKED_IDLE', incident_id = NULL, incident_started_at = NULL, deadline_at = NULL,
        original_status = NULL, provenance = NULL, next_retry_at_ms = NULL, lease_id = NULL,
        lease_until_ms = NULL, updated_at = ?
    WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ? AND incident_id = ?${leaseFence.sql}
  `, [next.updatedAt, accountId, current.generation, current.incidentId ?? null, ...leaseFence.params])
  return result.changes === 1 ? next : findAccountLockStateAsync(accountId)
}

export async function settleAccountLockDeadlineAsync(accountId: string, nowMs = Date.now(), observation?: AccountLockObservation): Promise<AccountLockState | undefined> {
  const current = await findAccountLockStateAsync(accountId)
  if (!current?.enabled || current.lockState !== 'ENGAGED' || !current.deadlineAt || Date.parse(current.deadlineAt) > nowMs) return current
  if (observation && !sameAccountLockObservation(current, observation)) return current
  const leaseFence = accountLockLeaseFence(observation, nowMs)
  const client = await accountLockClient()
  const settled = await client.transaction(async (tx) => {
    const row = await tx.one<{ lock_state: AccountLockStateName; generation: number | string; deadline_at: string | null; incident_id: string | null; original_status: AccountStatus | null; lease_id: string | null }>(`
      SELECT lock_state, generation, deadline_at, incident_id, original_status, lease_id
      FROM ${table(tx, 'account_lock_states')}
      WHERE account_id = ?
    `, [accountId])
    if (!row || row.lock_state !== 'ENGAGED' || !row.deadline_at || Date.parse(row.deadline_at) > nowMs) return false
    if (observation && (Number(row.generation) !== observation.generation || row.incident_id !== observation.incidentId || (Object.prototype.hasOwnProperty.call(observation, 'leaseId') && (row.lease_id ?? null) !== observation.leaseId))) return false
    const account = await tx.one<{ status: AccountStatus }>(`
      SELECT status FROM ${table(tx, 'accounts')} WHERE id = ? AND deleted_at IS NULL
    `, [accountId])
    const originalStatus = row.original_status ?? account?.status
    const transition = await tx.execute(`
      UPDATE ${table(tx, 'account_lock_states')}
      SET lock_state = 'DEAD_CONFIRMED', original_status = ?, provenance = 'lock_policy',
          lease_id = NULL, lease_until_ms = NULL, next_retry_at_ms = NULL, updated_at = ?
      WHERE account_id = ? AND lock_state = 'ENGAGED' AND generation = ? AND incident_id = ?${leaseFence.sql}
    `, [originalStatus ?? null, nowIso(), accountId, Number(row.generation), row.incident_id, ...leaseFence.params])
    if (transition.changes !== 1) return false
    if (originalStatus === 'active') {
      await tx.execute(`
        UPDATE ${table(tx, 'accounts')}
        SET status = 'temporary_unavailable', cooldown_until = ?, updated_at = ?
        WHERE id = ? AND status = 'active' AND schedulable = 1
      `, [new Date(nowMs).toISOString(), nowIso(), accountId])
    }
    return true
  })
  if (!settled) return findAccountLockStateAsync(accountId)
  const next: AccountLockState = {
    ...current,
    lockState: 'DEAD_CONFIRMED',
    provenance: 'lock_policy',
    nextRetryAtMs: undefined,
    leaseId: undefined,
    leaseUntilMs: undefined,
    updatedAt: nowIso()
  }
  return next
}

export function accountLockBlocksCrossAccount(state: AccountLockState | undefined, nowMs = Date.now()): boolean {
  return Boolean(state?.enabled && state.lockState === 'ENGAGED' && (!state.deadlineAt || Date.parse(state.deadlineAt) > nowMs))
}

export async function acquireAccountLockRetryLeaseAsync(accountId: string, globalDelayMs: number): Promise<AccountLockRetryLease> {
  const current = await findAccountLockStateAsync(accountId)
  if (!current || !accountLockBlocksCrossAccount(current)) return { allowed: true, waitMs: 0 }
  const active = current
  const now = Date.now()
  if (active.leaseId && active.leaseUntilMs && active.leaseUntilMs > now) {
    const waitUntil = active.nextRetryAtMs && active.nextRetryAtMs > now ? active.nextRetryAtMs : active.leaseUntilMs
    return { allowed: false, waitMs: waitUntil - now }
  }
  // Preserve a previously scheduled due time. Once due, claim immediately;
  // only a new retry window samples jitter.
  const desiredAt = accountLockRetryReservationDueAtMs(active.nextRetryAtMs, now)
    ?? (now + Math.max(Math.max(0, Math.trunc(globalDelayMs)), sampleLockDelayMs(active.lockRetryIntervalSeconds)))
  if (active.nextRetryAtMs && active.nextRetryAtMs > now) return { allowed: false, waitMs: active.nextRetryAtMs - now }
  const leaseId = randomUUID()
  const next: AccountLockState = { ...active, nextRetryAtMs: desiredAt, leaseId, leaseUntilMs: desiredAt + accountLockReservationLeaseDurationMs, updatedAt: new Date(now).toISOString() }
  const client = await accountLockClient()
  const result = await client.execute(`
    UPDATE ${table(client, 'account_lock_states')}
    SET next_retry_at_ms = ?, lease_id = ?, lease_until_ms = ?, updated_at = ?
    WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ?
      AND incident_id = ? AND (lease_until_ms IS NULL OR lease_until_ms <= ?)
      AND (next_retry_at_ms IS NULL OR next_retry_at_ms <= ?)
  `, [desiredAt, leaseId, desiredAt + accountLockReservationLeaseDurationMs, next.updatedAt, accountId, active.generation, active.incidentId ?? null, now, now])
  return result.changes === 1
    ? { allowed: true, waitMs: Math.max(0, desiredAt - now), leaseId }
    : { allowed: false, waitMs: Math.max(1, desiredAt - now) }
}

export async function consumeAccountLockRetryLeaseAsync(accountId: string, leaseId: string | undefined): Promise<boolean> {
  if (!leaseId) return false
  const current = await findAccountLockStateAsync(accountId)
  if (!current || !accountLockBlocksCrossAccount(current)) return false
  const now = Date.now()
  if (
    current.leaseId !== leaseId
    || !current.nextRetryAtMs
    || current.nextRetryAtMs > now
    || !current.leaseUntilMs
    || current.leaseUntilMs <= now
  ) return false
  const client = await accountLockClient()
  const result = await client.execute(`
    UPDATE ${table(client, 'account_lock_states')}
    SET next_retry_at_ms = ?, lease_until_ms = ?, updated_at = ?
    WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ?
      AND incident_id = ? AND lease_id = ? AND next_retry_at_ms <= ? AND lease_until_ms > ?
  `, [
    now, now + accountLockDispatchLeaseDurationMs, new Date(now).toISOString(), accountId, current.generation, current.incidentId ?? null, leaseId, now, now
  ])
  return result.changes === 1
}

export async function releaseAccountLockRetryLeaseAsync(input: {
  accountId: string
  leaseId?: string
  globalDelayMs?: number
  completedAtMs?: number
  scheduleNextRetry?: boolean
}): Promise<boolean> {
  if (!input.leaseId) return true
  const completedAtMs = input.completedAtMs ?? Date.now()
  const client = await accountLockClient()
  const current = await findAccountLockStateAsync(input.accountId)
  if (!current || !accountLockBlocksCrossAccount(current) || current.leaseId !== input.leaseId) return false
  const nextRetryAtMs = input.scheduleNextRetry === false
    ? null
    : completedAtMs + Math.max(Math.max(0, Math.trunc(input.globalDelayMs ?? 0)), sampleLockDelayMs(current.lockRetryIntervalSeconds))
  const result = await client.execute(`
    UPDATE ${table(client, 'account_lock_states')}
    SET next_retry_at_ms = ?, lease_id = NULL, lease_until_ms = NULL, updated_at = ?
    WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED' AND generation = ?
      AND incident_id = ? AND lease_id = ? AND lease_until_ms > ?
  `, [nextRetryAtMs, new Date(completedAtMs).toISOString(), input.accountId, current.generation, current.incidentId ?? null, input.leaseId, completedAtMs])
  return result.changes === 1
}

/** Release a waiting reservation after handoff/abort, preserving its shared due time. */
export async function abandonAccountLockRetryReservationAsync(input: {
  accountId: string
  leaseId?: string
}): Promise<boolean> {
  if (!input.leaseId) return true
  const client = await accountLockClient()
  const result = await client.execute(`
    UPDATE ${table(client, 'account_lock_states')}
    SET lease_id = NULL, lease_until_ms = NULL, updated_at = ?
    WHERE account_id = ? AND enabled = 1 AND lock_state = 'ENGAGED'
      AND lease_id = ? AND next_retry_at_ms IS NOT NULL
      AND lease_until_ms = next_retry_at_ms + ?
  `, [nowIso(), input.accountId, input.leaseId, accountLockReservationLeaseDurationMs])
  return result.changes === 1
}

export function sampleLockDelayMs(intervalSeconds: number, seed: string = randomUUID()): number {
  const base = normalizeAccountLockRetryIntervalSeconds(intervalSeconds) * 1000
  const jitter = base <= 10_000 ? 2_000 : 5_000
  let hash = 0
  for (const char of seed) hash = (hash * 31 + char.charCodeAt(0)) | 0
  const offset = Math.abs(hash) % (jitter * 2 + 1) - jitter
  return Math.max(2_000, Math.min(30_000, base + offset))
}

function stateFromRow(row: AccountLockRow): AccountLockState {
  return {
    accountId: row.account_id,
    enabled: Boolean(Number(row.enabled)),
    lockState: row.lock_state,
    lockDeathTimeoutSeconds: normalizeAccountLockDeathTimeoutSeconds(Number(row.lock_death_timeout_seconds)),
    lockRetryIntervalSeconds: normalizeAccountLockRetryIntervalSeconds(Number(row.lock_retry_interval_seconds)),
    ...(row.incident_id ? { incidentId: row.incident_id } : {}),
    generation: Number(row.generation),
    ...(row.incident_started_at ? { incidentStartedAt: row.incident_started_at } : {}),
    ...(row.deadline_at ? { deadlineAt: row.deadline_at } : {}),
    ...(row.original_status ? { originalStatus: row.original_status } : {}),
    ...(row.provenance ? { provenance: row.provenance } : {}),
    ...(row.next_retry_at_ms !== null ? { nextRetryAtMs: Number(row.next_retry_at_ms) } : {}),
    ...(row.lease_id ? { leaseId: row.lease_id } : {}),
    ...(row.lease_until_ms !== null ? { leaseUntilMs: Number(row.lease_until_ms) } : {}),
    updatedAt: row.updated_at
  }
}

function sameAccountLockObservation(current: AccountLockState, observation: AccountLockObservation): boolean {
  return current.generation === observation.generation
    && current.incidentId === observation.incidentId
    && (!Object.prototype.hasOwnProperty.call(observation, 'leaseId')
      || (current.leaseId ?? null) === observation.leaseId)
}

function accountLockLeaseFence(observation?: AccountLockObservation, validAtMs = Date.now()): { sql: string; params: unknown[] } {
  if (!observation || !Object.prototype.hasOwnProperty.call(observation, 'leaseId')) {
    return { sql: '', params: [] }
  }
  if (observation.leaseId === null || observation.leaseId === undefined) {
    return { sql: ' AND lease_id IS NULL', params: [] }
  }
  return { sql: ' AND lease_id = ? AND lease_until_ms > ?', params: [observation.leaseId, validAtMs] }
}

function normalizeInteger(value: unknown, fallback: number, min: number, max: number, label: string): number {
  const normalized = value === undefined || value === null ? fallback : value
  if (typeof normalized !== 'number' || !Number.isSafeInteger(normalized) || normalized < min || normalized > max) throw new Error(`${label}必须是 ${min}..${max} 的整数`)
  return normalized
}

async function accountLockClient(): Promise<DatabaseClient> {
  return runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
}

function table(client: DatabaseClient, name: string): string {
  return client.driver === 'postgres' ? client.dialect.qualifyTable('juhe_business', name) : client.dialect.quoteIdentifier(name)
}
