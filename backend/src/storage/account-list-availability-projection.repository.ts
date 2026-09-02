import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../config/runtime.js'
import { isAccountStatus } from '../domain/account-status-classification.js'
import type { AccountListItem, AccountStatus } from '../domain/types.js'
import { accountNameSearchQueryTerms, escapeAccountNameSearchLike, normalizeAccountNameSearchText } from './account-name-search.repository.js'
import { accountStatusFilterValues, normalizeAccountListOptions, type AccountListOptions, type AccountListSchedulableFilter, type AccountListSort } from './account-list-options.js'
import type { DatabaseClient } from './database-client.js'
import { textPrefixUpperBound } from './query-utils.js'

export type AccountListAvailabilitySchedulableBucket = 'enabled' | 'disabled' | 'cooling'

export interface AccountListAvailabilityProjectionWrite {
  viewerSystemAccountId: string
  accountId: string
  /** The owner/source account that owns the distributed concurrency slots. */
  concurrencyAccountId: string
  currentConcurrency: number
  sourceAccountId?: string
  authorizationId?: string
  effectiveStatus: AccountStatus
  schedulableBucket: AccountListAvailabilitySchedulableBucket
  providerCode: string
  providerProtocolProfileId: string
  accountType: string
  boundGroupId?: string
  nameSortKey: string
  prioritySortKey: number
  superPrioritySortKey: number
  fallbackSortKey: number
  concurrencySortKey: number
  accountExpiresAtSortKey?: string
  lastUsedAtSortKey?: string
  createdAtSortKey: string
  payload: AccountListItem
  tagIds: string[]
  searchTerms?: string[]
  sourceGeneration: number
  nextTransitionAt?: string
  projectedAt?: string
}

export interface AccountListAvailabilityDirtyRecord {
  accountId: string
  viewerSystemAccountId: string
  generation: number
  appliedGeneration: number
  reason: string
  availableAtMs: number
  claimToken?: string
  claimedBy?: string
  claimUntilMs?: number
  attemptCount: number
  createdAtMs: number
  updatedAtMs: number
}

export interface AccountListAvailabilityDirtyClaim extends AccountListAvailabilityDirtyRecord {
  claimToken: string
  claimedBy: string
  claimUntilMs: number
}

export interface AccountListAvailabilityProjectionScope {
  accountId: string
  viewerSystemAccountId: string
  createdAt: string
}

export interface AccountListAvailabilityProjectionQuery {
  viewerSystemAccountId: string
  options?: AccountListOptions
  nowMs?: number
  /**
   * Dynamic usage, balance and concurrency fields are only needed by the
   * management list. Lightweight account options deliberately skip those
   * joins and JSON overlays while retaining the durable health guards.
   */
  includeDynamicOverlays?: boolean
}

export interface AccountListAvailabilityProjectionPage {
  items: AccountListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
  generatedAt: string
  projectedAt: string
}

export interface AccountListAvailabilityRuntimeOverlayWrite {
  accountId: string
  currentConcurrency: number
  observedAt: string
  nextReconcileAt?: string
}

export class AccountListAvailabilityProjectionUnavailableError extends Error {}

interface AccountListAvailabilityProjectionRow {
  projection_unavailable: number | boolean | string
  projection_now_iso?: string
  account_id: string
  payload_json: string
  projected_at: string
}

interface AccountListAvailabilityProjectionViewerHealthRow {
  projection_count: number | string
  oldest_projected_at?: string | null
  next_transition_at?: string | null
}

type AccountListAvailabilityProjectionDependencyState = 'healthy' | 'unavailable' | 'recovering'

interface AccountListAvailabilityProjectionDependencyHealthRow {
  state: AccountListAvailabilityProjectionDependencyState
  generation: number | string
  reason?: string | null
  updated_at: string
}

interface AccountListAvailabilityDirtyRow {
  account_id: string
  viewer_system_account_id: string
  generation: number | string
  applied_generation: number | string
  reason: string
  available_at_ms: number | string
  claim_token: string | null
  claimed_by: string | null
  claim_until_ms: number | string | null
  attempt_count: number | string
  created_at_ms: number | string
  updated_at_ms: number | string
}

interface AccountListAvailabilityProjectionScopeRow {
  account_id: string
  viewer_system_account_id: string
  created_at: string
}

const maximumDirtyClaimLimit = 500
const maximumDirtyLeaseMs = 60 * 60_000
const maximumDirtyRetryDelayMs = 24 * 60 * 60_000

export async function ensureAccountListAvailabilityProjectionRuntimeDependencyInClient(
  client: DatabaseClient,
  input: { updatedAt?: string }
): Promise<void> {
  const dependencyHealth = businessTable(client, 'account_list_availability_projection_dependency_health')
  const updatedAt = optionalText(input.updatedAt, 64, 'updatedAt') ?? new Date().toISOString()
  await client.execute(`
    INSERT INTO ${dependencyHealth} (dependency_name, state, generation, reason, updated_at)
    VALUES ('runtime_state', 'recovering', 1, 'initial_projection_bootstrap', ?)
    ON CONFLICT(dependency_name) DO NOTHING
  `, [updatedAt])
}

/** Refreshes the runtime dependency heartbeat only while the dependency is healthy. */
export async function touchAccountListAvailabilityProjectionRuntimeDependencyInClient(
  client: DatabaseClient,
  input: { updatedAt?: string }
): Promise<boolean> {
  const dependencyHealth = businessTable(client, 'account_list_availability_projection_dependency_health')
  const updatedAt = optionalText(input.updatedAt, 64, 'updatedAt') ?? new Date().toISOString()
  const result = await client.execute(`
    UPDATE ${dependencyHealth}
    SET updated_at = ?
    WHERE dependency_name = 'runtime_state'
      AND state = 'healthy'
  `, [updatedAt])
  return result.changes === 1
}

/**
 * A runtime-state read failure is a data-correctness failure, not an empty
 * snapshot. Readers inspect this row inside their single PostgreSQL query and
 * fail closed until a recovery replay has completed.
 */
export async function markAccountListAvailabilityProjectionRuntimeDependencyUnavailableInClient(
  client: DatabaseClient,
  input: { reason: string; updatedAt?: string }
): Promise<void> {
  const dependencyHealth = businessTable(client, 'account_list_availability_projection_dependency_health')
  const reason = requiredText(input.reason, 256, 'reason')
  const updatedAt = optionalText(input.updatedAt, 64, 'updatedAt') ?? new Date().toISOString()
  await client.execute(`
    INSERT INTO ${dependencyHealth} (dependency_name, state, generation, reason, updated_at)
    VALUES ('runtime_state', 'unavailable', 1, ?, ?)
    ON CONFLICT(dependency_name) DO UPDATE SET
      state = 'unavailable',
      generation = CASE
        WHEN ${dependencyHealth}.state = 'unavailable' THEN ${dependencyHealth}.generation
        ELSE ${dependencyHealth}.generation + 1
      END,
      reason = excluded.reason,
      updated_at = excluded.updated_at
  `, [reason, updatedAt])
}

/** Starts exactly one full replay after the runtime dependency becomes healthy. */
export async function beginAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient(
  client: DatabaseClient,
  input: { updatedAt?: string }
): Promise<boolean> {
  const dependencyHealth = businessTable(client, 'account_list_availability_projection_dependency_health')
  const updatedAt = optionalText(input.updatedAt, 64, 'updatedAt') ?? new Date().toISOString()
  return client.transaction(async (tx) => {
    const row = await tx.one<AccountListAvailabilityProjectionDependencyHealthRow>(`
      SELECT state, generation, reason, updated_at
      FROM ${dependencyHealth}
      WHERE dependency_name = 'runtime_state'
      ${tx.driver === 'postgres' ? 'FOR UPDATE' : ''}
    `)
    if (!row) {
      await tx.execute(`
        INSERT INTO ${dependencyHealth} (dependency_name, state, generation, reason, updated_at)
        VALUES ('runtime_state', 'recovering', 1, 'initial_projection_bootstrap', ?)
      `, [updatedAt])
      return true
    }
    if (row.state !== 'unavailable') return false
    await tx.execute(`
      UPDATE ${dependencyHealth}
      SET state = 'recovering', reason = 'runtime_state_recovery_replay', updated_at = ?
      WHERE dependency_name = 'runtime_state'
    `, [updatedAt])
    return true
  })
}

/** Recovery becomes readable only after every dirty row was acknowledged. */
export async function completeAccountListAvailabilityProjectionRuntimeDependencyRecoveryInClient(
  client: DatabaseClient,
  input: { updatedAt?: string }
): Promise<boolean> {
  const dependencyHealth = businessTable(client, 'account_list_availability_projection_dependency_health')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const updatedAt = optionalText(input.updatedAt, 64, 'updatedAt') ?? new Date().toISOString()
  const result = await client.execute(`
    UPDATE ${dependencyHealth}
    SET state = 'healthy', reason = NULL, updated_at = ?
    WHERE dependency_name = 'runtime_state'
      AND state = 'recovering'
      AND NOT EXISTS (SELECT 1 FROM ${dirty})
  `, [updatedAt])
  return result.changes === 1
}

/** Durable, idempotent bulk write for Redis-derived concurrency snapshots. */
export async function upsertAccountListAvailabilityRuntimeOverlaysInClient(
  client: DatabaseClient,
  entries: AccountListAvailabilityRuntimeOverlayWrite[]
): Promise<void> {
  const byAccountId = new Map<string, Required<AccountListAvailabilityRuntimeOverlayWrite>>()
  for (const entry of entries) {
    const accountId = requiredText(entry.accountId, 256, 'accountId')
    byAccountId.set(accountId, {
      accountId,
      currentConcurrency: nonNegativeInteger(entry.currentConcurrency, 'currentConcurrency'),
      observedAt: requiredText(entry.observedAt, 64, 'observedAt'),
      nextReconcileAt: optionalText(entry.nextReconcileAt, 64, 'nextReconcileAt') ?? ''
    })
  }
  const normalized = [...byAccountId.values()]
  if (!normalized.length) return
  const runtimeOverlays = businessTable(client, 'account_list_availability_runtime_overlays')
  await client.execute(`
    INSERT INTO ${runtimeOverlays} (
      account_id, current_concurrency, observed_at, next_reconcile_at
    ) VALUES ${normalized.map(() => '(?, ?, ?, ?)').join(', ')}
    ON CONFLICT(account_id) DO UPDATE SET
      current_concurrency = excluded.current_concurrency,
      observed_at = excluded.observed_at,
      next_reconcile_at = excluded.next_reconcile_at
  `, normalized.flatMap((entry) => [
    entry.accountId,
    entry.currentConcurrency,
    entry.observedAt,
    entry.nextReconcileAt || null
  ]))
}

/**
 * Writes one fully rendered management-list row. The source generation fence
 * prevents an expired worker lease from overwriting a newer projection.
 */
export async function upsertAccountListAvailabilityProjectionInClient(
  client: DatabaseClient,
  input: AccountListAvailabilityProjectionWrite
): Promise<boolean> {
  const value = normalizeProjectionWrite(input)
  return client.transaction((tx) => upsertAccountListAvailabilityProjectionInTransaction(tx, value))
}

/**
 * Returns false when a newer dirty generation already owns the projection.
 * Tags are updated in the same fenced transaction as the payload so an
 * expired worker cannot mutate part of a newer row.
 */
export async function upsertAccountListAvailabilityProjectionInTransaction(
  tx: DatabaseClient,
  input: AccountListAvailabilityProjectionWrite
): Promise<boolean> {
  const value = normalizeProjectionWrite(input)
  const projections = businessTable(tx, 'account_list_availability_projections')
  const projectionIndex = businessTable(tx, 'account_list_availability_projection_index')
  const projectionTags = businessTable(tx, 'account_list_availability_projection_tags')
  const projectionSearchTerms = businessTable(tx, 'account_list_availability_projection_search_terms')
  const runtimeOverlays = businessTable(tx, 'account_list_availability_runtime_overlays')
  const viewerHealth = businessTable(tx, 'account_list_availability_projection_viewer_health')
  const result = await tx.execute(`
      INSERT INTO ${projections} (
        viewer_system_account_id, account_id, source_account_id, authorization_id,
        effective_status, schedulable_bucket, provider_code, provider_protocol_profile_id,
        account_type, bound_group_id, name_sort_key, priority_sort_key,
        super_priority_sort_key, fallback_sort_key, concurrency_sort_key,
        account_expires_at_sort_key, last_used_at_sort_key, created_at_sort_key,
        payload_json, source_generation, next_transition_at, projected_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(viewer_system_account_id, account_id) DO UPDATE SET
        source_account_id = excluded.source_account_id,
        authorization_id = excluded.authorization_id,
        effective_status = excluded.effective_status,
        schedulable_bucket = excluded.schedulable_bucket,
        provider_code = excluded.provider_code,
        provider_protocol_profile_id = excluded.provider_protocol_profile_id,
        account_type = excluded.account_type,
        bound_group_id = excluded.bound_group_id,
        name_sort_key = excluded.name_sort_key,
        priority_sort_key = excluded.priority_sort_key,
        super_priority_sort_key = excluded.super_priority_sort_key,
        fallback_sort_key = excluded.fallback_sort_key,
        concurrency_sort_key = excluded.concurrency_sort_key,
        account_expires_at_sort_key = excluded.account_expires_at_sort_key,
        last_used_at_sort_key = excluded.last_used_at_sort_key,
        created_at_sort_key = excluded.created_at_sort_key,
        payload_json = excluded.payload_json,
        source_generation = excluded.source_generation,
        next_transition_at = excluded.next_transition_at,
        projected_at = excluded.projected_at
      WHERE ${projections}.source_generation <= excluded.source_generation
    `, [
      value.viewerSystemAccountId,
      value.accountId,
      value.sourceAccountId ?? null,
      value.authorizationId ?? null,
      value.effectiveStatus,
      value.schedulableBucket,
      value.providerCode,
      value.providerProtocolProfileId,
      value.accountType,
      value.boundGroupId ?? null,
      value.nameSortKey,
      value.prioritySortKey,
      value.superPrioritySortKey,
      value.fallbackSortKey,
      value.concurrencySortKey,
      value.accountExpiresAtSortKey ?? null,
      value.lastUsedAtSortKey ?? null,
      value.createdAtSortKey,
      JSON.stringify(value.payload),
      value.sourceGeneration,
      value.nextTransitionAt ?? null,
      value.projectedAt
  ])
  if (result.changes !== 1) return false
  await tx.execute(`
    INSERT INTO ${projectionIndex} (
      viewer_system_account_id, account_id, effective_status, schedulable_bucket,
      provider_code, provider_protocol_profile_id, account_type, bound_group_id,
      name_sort_key, priority_sort_key, super_priority_sort_key, fallback_sort_key,
      concurrency_sort_key, account_expires_at_sort_key, last_used_at_sort_key,
      created_at_sort_key, access_type_sort_key, search_index_complete, authorization_quota_exceeded
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(viewer_system_account_id, account_id) DO UPDATE SET
      effective_status = excluded.effective_status,
      schedulable_bucket = excluded.schedulable_bucket,
      provider_code = excluded.provider_code,
      provider_protocol_profile_id = excluded.provider_protocol_profile_id,
      account_type = excluded.account_type,
      bound_group_id = excluded.bound_group_id,
      name_sort_key = excluded.name_sort_key,
      priority_sort_key = excluded.priority_sort_key,
      super_priority_sort_key = excluded.super_priority_sort_key,
      fallback_sort_key = excluded.fallback_sort_key,
      concurrency_sort_key = excluded.concurrency_sort_key,
      account_expires_at_sort_key = excluded.account_expires_at_sort_key,
      last_used_at_sort_key = excluded.last_used_at_sort_key,
      created_at_sort_key = excluded.created_at_sort_key,
      access_type_sort_key = excluded.access_type_sort_key,
      search_index_complete = excluded.search_index_complete,
      authorization_quota_exceeded = excluded.authorization_quota_exceeded
  `, [
    value.viewerSystemAccountId,
    value.accountId,
    value.effectiveStatus,
    value.schedulableBucket,
    value.providerCode,
    value.providerProtocolProfileId,
    value.accountType,
    value.boundGroupId ?? null,
    value.nameSortKey,
    value.prioritySortKey,
    value.superPrioritySortKey,
    value.fallbackSortKey,
    value.concurrencySortKey,
    value.accountExpiresAtSortKey ?? null,
    value.lastUsedAtSortKey ?? null,
    value.createdAtSortKey,
    value.payload.accessType,
    value.searchIndexComplete ? 1 : 0,
    value.payload.authorizationQuotaExceeded ? 1 : 0
  ])
  await tx.execute(`
    INSERT INTO ${runtimeOverlays} (
      account_id, current_concurrency, observed_at, next_reconcile_at
    ) VALUES (?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      current_concurrency = excluded.current_concurrency,
      observed_at = excluded.observed_at,
      next_reconcile_at = excluded.next_reconcile_at
  `, [
    value.concurrencyAccountId,
    value.currentConcurrency,
    value.projectedAt,
    null
  ])
  await tx.execute(`
      DELETE FROM ${projectionTags}
      WHERE viewer_system_account_id = ? AND account_id = ?
    `, [value.viewerSystemAccountId, value.accountId])
  for (const tagId of value.tagIds) {
    await tx.execute(`
        INSERT INTO ${projectionTags} (viewer_system_account_id, account_id, tag_id)
        VALUES (?, ?, ?)
      `, [value.viewerSystemAccountId, value.accountId, tagId])
  }
  await tx.execute(`
      DELETE FROM ${projectionSearchTerms}
      WHERE viewer_system_account_id = ? AND account_id = ?
    `, [value.viewerSystemAccountId, value.accountId])
  for (const term of value.searchIndexComplete ? (value.searchTerms ?? []) : []) {
    await tx.execute(`
        INSERT INTO ${projectionSearchTerms} (
          viewer_system_account_id, account_id, term, name_sort_key, created_at_sort_key
        ) VALUES (?, ?, ?, ?, ?)
      `, [value.viewerSystemAccountId, value.accountId, term, value.nameSortKey, value.createdAtSortKey])
  }
  await markAccountListAvailabilityProjectionViewerHealthStaleInTransaction(
    tx,
    viewerHealth,
    value.viewerSystemAccountId,
    value.projectedAt
  )
  return true
}

/**
 * Recomputes the viewer-level O(1) freshness watermark after a worker has
 * applied every claim it owns for that viewer. It deliberately runs outside
 * request handling; readers only inspect this one row plus dirty records.
 */
export async function refreshAccountListAvailabilityProjectionViewerHealthInClient(
  client: DatabaseClient,
  input: { viewerSystemAccountId: string; updatedAt?: string }
): Promise<void> {
  const viewerSystemAccountId = requiredText(input.viewerSystemAccountId, 256, 'viewerSystemAccountId')
  const updatedAt = optionalText(input.updatedAt, 64, 'updatedAt') ?? new Date().toISOString()
  const projections = businessTable(client, 'account_list_availability_projections')
  const viewerHealth = businessTable(client, 'account_list_availability_projection_viewer_health')
  await client.transaction(async (tx) => {
    const aggregate = await tx.one<AccountListAvailabilityProjectionViewerHealthRow>(`
      SELECT COUNT(*) AS projection_count,
        MIN(projected_at) AS oldest_projected_at,
        MIN(next_transition_at) AS next_transition_at
      FROM ${projections}
      WHERE viewer_system_account_id = ?
    `, [viewerSystemAccountId])
    await tx.execute(`
      INSERT INTO ${viewerHealth} (
        viewer_system_account_id, projection_count, oldest_projected_at,
        next_transition_at, is_current, updated_at
      ) VALUES (?, ?, ?, ?, 1, ?)
      ON CONFLICT(viewer_system_account_id) DO UPDATE SET
        projection_count = excluded.projection_count,
        oldest_projected_at = excluded.oldest_projected_at,
        next_transition_at = excluded.next_transition_at,
        is_current = 1,
        updated_at = excluded.updated_at
    `, [
      viewerSystemAccountId,
      nonNegativeInteger(aggregate?.projection_count ?? 0, 'projection_count'),
      aggregate?.oldest_projected_at ?? null,
      aggregate?.next_transition_at ?? null,
      updatedAt
    ])
  })
}

/**
 * Backfills health rows for viewers that existed before the projection
 * tables/triggers were installed. A missing health row must never turn an
 * otherwise valid empty list into a permanent unavailable response.
 *
 * New rows deliberately begin non-current: the maintenance worker will mark
 * them current only after it has either materialized every visible account or
 * established that the viewer has no visible accounts.
 */
export async function ensureAccountListAvailabilityProjectionViewerHealthInClient(
  client: DatabaseClient,
  input: { limit: number; updatedAt?: string }
): Promise<number> {
  const limit = positiveInteger(input.limit, 'limit', maximumDirtyClaimLimit)
  const updatedAt = optionalText(input.updatedAt, 64, 'updatedAt') ?? new Date().toISOString()
  const systemAccounts = businessTable(client, 'system_accounts')
  const viewerHealth = businessTable(client, 'account_list_availability_projection_viewer_health')
  const result = await client.execute(`
    INSERT INTO ${viewerHealth} (
      viewer_system_account_id, projection_count, oldest_projected_at,
      next_transition_at, is_current, updated_at
    )
    SELECT system_accounts.id, 0, NULL, NULL, 0, ?
    FROM ${systemAccounts} system_accounts
    LEFT JOIN ${viewerHealth} health
      ON health.viewer_system_account_id = system_accounts.id
    WHERE health.viewer_system_account_id IS NULL
    ORDER BY system_accounts.id ASC
    LIMIT ?
    ON CONFLICT(viewer_system_account_id) DO NOTHING
  `, [updatedAt, limit])
  return result.changes
}

/** Finds health rows left stale by an interrupted worker or a cascaded delete. */
export async function listAccountListAvailabilityProjectionViewerHealthRefreshCandidatesInClient(
  client: DatabaseClient,
  input: { limit: number }
): Promise<string[]> {
  const limit = positiveInteger(input.limit, 'limit', maximumDirtyClaimLimit)
  const viewerHealth = businessTable(client, 'account_list_availability_projection_viewer_health')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const rows = await client.query<{ viewer_system_account_id: string }>(`
    SELECT health.viewer_system_account_id
    FROM ${viewerHealth} health
    WHERE health.is_current = 0
      AND NOT EXISTS (
        SELECT 1
        FROM ${dirty} dirty_accounts
        WHERE dirty_accounts.viewer_system_account_id = health.viewer_system_account_id
      )
    ORDER BY health.updated_at ASC, health.viewer_system_account_id ASC
    LIMIT ?
  `, [limit])
  return rows.map((row) => requiredText(row.viewer_system_account_id, 256, 'viewer_system_account_id'))
}

async function markAccountListAvailabilityProjectionViewerHealthStaleInTransaction(
  tx: DatabaseClient,
  viewerHealth: string,
  viewerSystemAccountId: string,
  updatedAt: string
): Promise<void> {
  await tx.execute(`
    INSERT INTO ${viewerHealth} (
      viewer_system_account_id, projection_count, oldest_projected_at,
      next_transition_at, is_current, updated_at
    ) VALUES (?, 0, NULL, NULL, 0, ?)
    ON CONFLICT(viewer_system_account_id) DO UPDATE SET
      is_current = 0,
      updated_at = excluded.updated_at
  `, [viewerSystemAccountId, updatedAt])
}

/**
 * Coalesces repeated writes for one account. A source account change is
 * expanded to its authorized instances by the caller before this function is
 * invoked, so the read path never has to discover dependent rows.
 */
export async function markAccountListAvailabilityDirtyInClient(
  client: DatabaseClient,
  input: { accountId: string; reason: string; availableAtMs?: number; nowMs?: number }
): Promise<AccountListAvailabilityDirtyRecord> {
  return client.transaction((tx) => markAccountListAvailabilityDirtyInTransaction(tx, input))
}

export async function markAccountListAvailabilityDirtyInTransaction(
  tx: DatabaseClient,
  input: { accountId: string; reason: string; availableAtMs?: number; nowMs?: number }
): Promise<AccountListAvailabilityDirtyRecord> {
  const accountId = requiredText(input.accountId, 256, 'accountId')
  const reason = requiredText(input.reason, 128, 'reason')
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const availableAtMs = nonNegativeInteger(input.availableAtMs ?? nowMs, 'availableAtMs')
  const dirty = businessTable(tx, 'account_list_availability_dirty')
  const projections = businessTable(tx, 'account_list_availability_projections')
  await tx.execute(`
    INSERT INTO ${dirty} (
      account_id, viewer_system_account_id, generation, applied_generation, reason, available_at_ms,
      claim_token, claimed_by, claim_until_ms, attempt_count,
      created_at_ms, updated_at_ms
    ) SELECT accounts.id, accounts.system_account_id, COALESCE((
      SELECT MAX(source_generation)
      FROM ${projections}
      WHERE account_id = ?
    ), 0) + 1, 0, ?, ?, NULL, NULL, NULL, 0, ?, ?
    FROM ${businessTable(tx, 'accounts')} accounts
    WHERE accounts.id = ? AND accounts.deleted_at IS NULL
    ON CONFLICT(account_id) DO UPDATE SET
      viewer_system_account_id = excluded.viewer_system_account_id,
      generation = ${dirty}.generation + 1,
      reason = excluded.reason,
      available_at_ms = CASE
        WHEN ${dirty}.available_at_ms < excluded.available_at_ms THEN ${dirty}.available_at_ms
        ELSE excluded.available_at_ms
      END,
      claim_token = NULL,
      claimed_by = NULL,
      claim_until_ms = NULL,
      updated_at_ms = excluded.updated_at_ms
  `, [accountId, reason, availableAtMs, nowMs, nowMs, accountId])
  const row = await tx.one<AccountListAvailabilityDirtyRow>(`
    SELECT account_id, viewer_system_account_id, generation, applied_generation, reason, available_at_ms,
      claim_token, claimed_by, claim_until_ms, attempt_count,
      created_at_ms, updated_at_ms
    FROM ${dirty}
    WHERE account_id = ?
  `, [accountId])
  if (!row) throw new Error(`账户列表投影脏标记 ${accountId} 未写入，账户可能已删除`)
  return mapDirtyRow(row)
}

/**
 * Expands a changed owner/source/authorization to every materialized account
 * instance that can expose the change. It is intended for the owning write
 * transaction, so a committed fact never leaves a silently stale list row.
 */
export async function markAccountListAvailabilityDirtyFamilyInTransaction(
  tx: DatabaseClient,
  input: {
    accountIds?: string[]
    sourceAccountIds?: string[]
    authorizationIds?: string[]
    reason: string
    availableAtMs?: number
    nowMs?: number
  }
): Promise<AccountListAvailabilityDirtyRecord[]> {
  const accountIds = normalizedIdList(input.accountIds, 'accountIds')
  const sourceAccountIds = normalizedIdList(input.sourceAccountIds, 'sourceAccountIds')
  const authorizationIds = normalizedIdList(input.authorizationIds, 'authorizationIds')
  if (!accountIds.length && !sourceAccountIds.length && !authorizationIds.length) {
    throw new Error('账户列表投影脏标记至少需要一个账户或授权标识')
  }
  const accounts = businessTable(tx, 'accounts')
  const clauses: string[] = []
  const params: string[] = []
  if (accountIds.length) {
    clauses.push(`accounts.id IN (${tx.dialect.bindPlaceholders(accountIds.length)})`)
    params.push(...accountIds)
  }
  if (sourceAccountIds.length) {
    clauses.push(`(
      accounts.id IN (${tx.dialect.bindPlaceholders(sourceAccountIds.length)})
      OR accounts.authorization_instance_source_account_id IN (${tx.dialect.bindPlaceholders(sourceAccountIds.length)})
    )`)
    params.push(...sourceAccountIds, ...sourceAccountIds)
  }
  if (authorizationIds.length) {
    clauses.push(`accounts.authorization_instance_authorization_id IN (${tx.dialect.bindPlaceholders(authorizationIds.length)})`)
    params.push(...authorizationIds)
  }
  const rows = await tx.query<{ id: string }>(`
    SELECT accounts.id
    FROM ${accounts} accounts
    WHERE ${clauses.map((clause) => `(${clause})`).join(' OR ')}
    ORDER BY accounts.id ASC
  `, params)
  return Promise.all(rows.map((row) => markAccountListAvailabilityDirtyInTransaction(tx, {
    accountId: row.id,
    reason: input.reason,
    availableAtMs: input.availableAtMs,
    nowMs: input.nowMs
  })))
}

/** Marks a runtime change before Redis publishes it to projection readers. */
export async function markAccountListAvailabilityDirtyFamilyInClient(
  client: DatabaseClient,
  input: Parameters<typeof markAccountListAvailabilityDirtyFamilyInTransaction>[1]
): Promise<AccountListAvailabilityDirtyRecord[]> {
  return client.transaction((tx) => markAccountListAvailabilityDirtyFamilyInTransaction(tx, input))
}

export async function listAccountListAvailabilityProjectionScopesInClient(
  client: DatabaseClient,
  accountIds: string[]
): Promise<AccountListAvailabilityProjectionScope[]> {
  const ids = normalizedIdList(accountIds, 'accountIds')
  if (!ids.length) return []
  const accounts = businessTable(client, 'accounts')
  const authorizations = businessTable(client, 'resource_authorizations')
  return client.query<AccountListAvailabilityProjectionScopeRow>(`
    SELECT accounts.id AS account_id, accounts.system_account_id AS viewer_system_account_id,
      accounts.created_at
    FROM ${accounts} accounts
    LEFT JOIN ${authorizations} authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    WHERE accounts.id IN (${client.dialect.bindPlaceholders(ids.length)})
      AND accounts.deleted_at IS NULL
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
    ORDER BY accounts.system_account_id ASC, accounts.id ASC
  `, ids).then((rows) => rows.map((row) => ({
    accountId: row.account_id,
    viewerSystemAccountId: row.viewer_system_account_id,
    createdAt: row.created_at
  })))
}

/**
 * Copies only terms backed by a completed source document. This is worker
 * work, never a list read, so an unfinished source index remains prefix-only
 * just as it does on the legacy endpoint.
 */
export async function loadAccountListAvailabilityProjectionSearchTermsInClient(
  client: DatabaseClient,
  accountIds: string[]
): Promise<Map<string, string[]>> {
  const ids = normalizedIdList(accountIds, 'accountIds')
  const output = new Map<string, string[]>()
  if (!ids.length) return output
  const searchTerms = businessTable(client, 'account_name_search_terms')
  const searchDocuments = businessTable(client, 'account_name_search_documents')
  const rows = await client.query<{ account_id: string; term: string }>(`
    SELECT search.account_id, search.term
    FROM ${searchTerms} search
    INNER JOIN ${searchDocuments} documents
      ON documents.account_id = search.account_id
    WHERE search.account_id IN (${client.dialect.bindPlaceholders(ids.length)})
    ORDER BY search.account_id ASC, search.term ASC
  `, ids)
  for (const row of rows) {
    output.set(row.account_id, [...(output.get(row.account_id) ?? []), row.term])
  }
  return output
}

export async function deleteAccountListAvailabilityProjectionForAccountInClient(
  client: DatabaseClient,
  input: { accountId: string; viewerSystemAccountId: string; sourceGeneration: number }
): Promise<boolean> {
  return client.transaction((tx) => deleteAccountListAvailabilityProjectionForAccountInTransaction(tx, input))
}

export async function deleteAccountListAvailabilityProjectionForAccountInTransaction(
  tx: DatabaseClient,
  input: { accountId: string; viewerSystemAccountId: string; sourceGeneration: number }
): Promise<boolean> {
  const accountId = requiredText(input.accountId, 256, 'accountId')
  const viewerSystemAccountId = requiredText(input.viewerSystemAccountId, 256, 'viewerSystemAccountId')
  const sourceGeneration = positiveInteger(input.sourceGeneration, 'sourceGeneration', Number.MAX_SAFE_INTEGER)
  const projections = businessTable(tx, 'account_list_availability_projections')
  const result = await tx.execute(`
    DELETE FROM ${projections}
    WHERE account_id = ? AND source_generation <= ?
  `, [accountId, sourceGeneration])
  if (result.changes > 0) {
    await markAccountListAvailabilityProjectionViewerHealthStaleInTransaction(
      tx,
      businessTable(tx, 'account_list_availability_projection_viewer_health'),
      viewerSystemAccountId,
      new Date().toISOString()
    )
  }
  return result.changes > 0
}

/**
 * Worker-only bootstrap. The bounded query discovers only rows absent from the
 * read model; requests treat them as unavailable instead of rebuilding them.
 */
export async function enqueueMissingAccountListAvailabilityProjectionsInClient(
  client: DatabaseClient,
  input: { limit: number; nowMs?: number }
): Promise<number> {
  const limit = positiveInteger(input.limit, 'limit', maximumDirtyClaimLimit)
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const accounts = businessTable(client, 'accounts')
  const authorizations = businessTable(client, 'resource_authorizations')
  const projections = businessTable(client, 'account_list_availability_projections')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  return client.transaction(async (tx) => {
    const rows = await tx.query<{ account_id: string }>(`
      SELECT accounts.id AS account_id
      FROM ${accounts} accounts
      LEFT JOIN ${authorizations} authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      LEFT JOIN ${projections} projections
        ON projections.viewer_system_account_id = accounts.system_account_id
       AND projections.account_id = accounts.id
      LEFT JOIN ${dirty} dirty_accounts
        ON dirty_accounts.account_id = accounts.id
      WHERE accounts.deleted_at IS NULL
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR authorizations.status IN ('active', 'paused', 'expired')
        )
        AND projections.account_id IS NULL
        AND dirty_accounts.account_id IS NULL
      ORDER BY accounts.created_at ASC, accounts.id ASC
      LIMIT ?
    `, [limit])
    await Promise.all(rows.map((row) => markAccountListAvailabilityDirtyInTransaction(tx, {
      accountId: row.account_id,
      reason: 'projection_missing',
      nowMs
    })))
    return rows.length
  })
}

/**
 * Runtime recovery must replay every visible row, rather than trusting the
 * previous Redis-derived availability payload. This is one set-based write;
 * it does not enumerate account IDs through the request path or Node loops.
 */
export async function enqueueAllAccountListAvailabilityProjectionsForRuntimeRecoveryInClient(
  client: DatabaseClient,
  input: { nowMs?: number }
): Promise<number> {
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const accounts = businessTable(client, 'accounts')
  const authorizations = businessTable(client, 'resource_authorizations')
  const projections = businessTable(client, 'account_list_availability_projections')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const viewerHealth = businessTable(client, 'account_list_availability_projection_viewer_health')
  return client.transaction(async (tx) => {
    const result = await tx.execute(`
      INSERT INTO ${dirty} (
        account_id, viewer_system_account_id, generation, applied_generation, reason,
        available_at_ms, claim_token, claimed_by, claim_until_ms, attempt_count,
        created_at_ms, updated_at_ms
      )
      SELECT accounts.id, accounts.system_account_id,
        COALESCE(projections.source_generation, 0) + 1,
        0, 'runtime_dependency_recovery', ?, NULL, NULL, NULL, 0, ?, ?
      FROM ${accounts} accounts
      LEFT JOIN ${authorizations} authorizations
        ON authorizations.id = accounts.authorization_instance_authorization_id
      LEFT JOIN ${projections} projections
        ON projections.viewer_system_account_id = accounts.system_account_id
       AND projections.account_id = accounts.id
      WHERE accounts.deleted_at IS NULL
        AND (
          accounts.authorization_instance_authorization_id IS NULL
          OR authorizations.status IN ('active', 'paused', 'expired')
        )
      ON CONFLICT(account_id) DO UPDATE SET
        viewer_system_account_id = excluded.viewer_system_account_id,
        generation = ${dirty}.generation + 1,
        reason = excluded.reason,
        available_at_ms = CASE
          WHEN ${dirty}.available_at_ms < excluded.available_at_ms THEN ${dirty}.available_at_ms
          ELSE excluded.available_at_ms
        END,
        claim_token = NULL,
        claimed_by = NULL,
        claim_until_ms = NULL,
        updated_at_ms = excluded.updated_at_ms
    `, [nowMs, nowMs, nowMs])
    await tx.execute(`
      UPDATE ${viewerHealth}
      SET is_current = 0, updated_at = ?
      WHERE viewer_system_account_id IN (
        SELECT DISTINCT accounts.system_account_id
        FROM ${accounts} accounts
        LEFT JOIN ${authorizations} authorizations
          ON authorizations.id = accounts.authorization_instance_authorization_id
        WHERE accounts.deleted_at IS NULL
          AND (
            accounts.authorization_instance_authorization_id IS NULL
            OR authorizations.status IN ('active', 'paused', 'expired')
          )
      )
    `, [new Date(nowMs).toISOString()])
    return result.changes
  })
}

/**
 * Natural expiry/cooldown transitions have no explicit writer. This bounded
 * worker scan turns them into ordinary dirty records before a request can use
 * an old availability decision.
 */
export async function enqueueDueAccountListAvailabilityProjectionsInClient(
  client: DatabaseClient,
  input: { limit: number; nowMs?: number }
): Promise<number> {
  const limit = positiveInteger(input.limit, 'limit', maximumDirtyClaimLimit)
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const projections = businessTable(client, 'account_list_availability_projections')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const now = new Date(nowMs).toISOString()
  return client.transaction(async (tx) => {
    const rows = await tx.query<{ account_id: string }>(`
      SELECT projections.account_id
      FROM ${projections} projections
      LEFT JOIN ${dirty} dirty_accounts
        ON dirty_accounts.account_id = projections.account_id
      WHERE projections.next_transition_at IS NOT NULL
        AND projections.next_transition_at <= ?
        AND dirty_accounts.account_id IS NULL
      ORDER BY projections.next_transition_at ASC, projections.account_id ASC
      LIMIT ?
    `, [now, limit])
    await Promise.all(rows.map((row) => markAccountListAvailabilityDirtyInTransaction(tx, {
      accountId: row.account_id,
      reason: 'projection_due_transition',
      nowMs
    })))
    return rows.length
  })
}

export async function claimAccountListAvailabilityDirtyInClient(
  client: DatabaseClient,
  input: { ownerId: string; limit: number; leaseMs: number; nowMs?: number }
): Promise<AccountListAvailabilityDirtyClaim[]> {
  const ownerId = requiredText(input.ownerId, 128, 'ownerId')
  const limit = positiveInteger(input.limit, 'limit', maximumDirtyClaimLimit)
  const leaseMs = positiveInteger(input.leaseMs, 'leaseMs', maximumDirtyLeaseMs)
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  return client.transaction(async (tx) => {
    const dirty = businessTable(tx, 'account_list_availability_dirty')
    const usesDatabaseClock = tx.driver === 'postgres'
    const databaseNowMs = 'FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint'
    const claimableNow = usesDatabaseClock ? databaseNowMs : '?'
    if (usesDatabaseClock) {
      const claimToken = randomUUID()
      const rows = await tx.query<AccountListAvailabilityDirtyRow>(`
        WITH candidates AS (
          SELECT account_id
          FROM ${dirty}
          WHERE available_at_ms <= ${databaseNowMs}
            AND (claim_until_ms IS NULL OR claim_until_ms <= ${databaseNowMs})
          ORDER BY available_at_ms ASC, created_at_ms ASC, account_id ASC
          LIMIT ?
          FOR UPDATE SKIP LOCKED
        )
        UPDATE ${dirty} dirty_accounts
        SET claim_token = ?,
            claimed_by = ?,
            claim_until_ms = ${databaseNowMs} + ?,
            attempt_count = dirty_accounts.attempt_count + 1,
            updated_at_ms = ${databaseNowMs}
        FROM candidates
        WHERE dirty_accounts.account_id = candidates.account_id
        RETURNING dirty_accounts.account_id, dirty_accounts.viewer_system_account_id,
          dirty_accounts.generation, dirty_accounts.applied_generation, dirty_accounts.reason,
          dirty_accounts.available_at_ms, dirty_accounts.claim_token, dirty_accounts.claimed_by,
          dirty_accounts.claim_until_ms, dirty_accounts.attempt_count,
          dirty_accounts.created_at_ms, dirty_accounts.updated_at_ms
      `, [limit, claimToken, ownerId, leaseMs])
      return rows.map((row) => ({
        ...mapDirtyRow(row),
        claimToken: requiredText(row.claim_token, 256, 'claim_token'),
        claimedBy: requiredText(row.claimed_by, 128, 'claimed_by'),
        claimUntilMs: nonNegativeInteger(row.claim_until_ms, 'claim_until_ms'),
        attemptCount: nonNegativeInteger(row.attempt_count, 'attempt_count'),
        updatedAtMs: nonNegativeInteger(row.updated_at_ms, 'updated_at_ms')
      }))
    }
    const rows = await tx.query<AccountListAvailabilityDirtyRow>(`
      SELECT account_id, viewer_system_account_id, generation, applied_generation, reason, available_at_ms,
        claim_token, claimed_by, claim_until_ms, attempt_count,
        created_at_ms, updated_at_ms
      FROM ${dirty}
      WHERE available_at_ms <= ${claimableNow}
        AND (claim_until_ms IS NULL OR claim_until_ms <= ${claimableNow})
      ORDER BY available_at_ms ASC, created_at_ms ASC, account_id ASC
      LIMIT ?
    `, [nowMs, nowMs, limit])
    const claimed: AccountListAvailabilityDirtyClaim[] = []
    for (const row of rows) {
      const claimToken = randomUUID()
      const claimUntilMs = nowMs + leaseMs
      const result = await tx.execute(`
        UPDATE ${dirty}
        SET claim_token = ?, claimed_by = ?, claim_until_ms = ?,
            attempt_count = attempt_count + 1, updated_at_ms = ?
        WHERE account_id = ? AND generation = ?
          AND available_at_ms <= ?
          AND (claim_until_ms IS NULL OR claim_until_ms <= ?)
      `, [claimToken, ownerId, claimUntilMs, nowMs, row.account_id, row.generation, nowMs, nowMs])
      if (result.changes !== 1) continue
      claimed.push({
        ...mapDirtyRow(row),
        claimToken,
        claimedBy: ownerId,
        claimUntilMs,
        attemptCount: nonNegativeInteger(row.attempt_count, 'attempt_count') + 1,
        updatedAtMs: nowMs
      })
    }
    return claimed
  })
}

export async function acknowledgeAccountListAvailabilityDirtyInClient(
  client: DatabaseClient,
  input: { accountId: string; generation: number; claimToken: string }
): Promise<boolean> {
  const accountId = requiredText(input.accountId, 256, 'accountId')
  const generation = positiveInteger(input.generation, 'generation', Number.MAX_SAFE_INTEGER)
  const claimToken = requiredText(input.claimToken, 256, 'claimToken')
  return client.transaction((tx) => acknowledgeAccountListAvailabilityDirtyInTransaction(tx, {
    accountId,
    generation,
    claimToken
  }))
}

export async function acknowledgeAccountListAvailabilityDirtyInTransaction(
  tx: DatabaseClient,
  input: { accountId: string; generation: number; claimToken: string }
): Promise<boolean> {
  const accountId = requiredText(input.accountId, 256, 'accountId')
  const generation = positiveInteger(input.generation, 'generation', Number.MAX_SAFE_INTEGER)
  const claimToken = requiredText(input.claimToken, 256, 'claimToken')
  const dirty = businessTable(tx, 'account_list_availability_dirty')
  const result = await tx.execute(`
    DELETE FROM ${dirty}
    WHERE account_id = ?
      AND generation = ?
      AND claim_token = ?
  `, [accountId, generation, claimToken])
  return result.changes === 1
}

/**
 * Commits a rendered row only while the exact dirty claim is current, then
 * marks that generation applied in the same transaction. This is the worker
 * boundary that prevents an expired claim from changing payload or tags.
 */
export async function applyAccountListAvailabilityProjectionDirtyClaimInClient(
  client: DatabaseClient,
  input: { claim: AccountListAvailabilityDirtyClaim; projection: AccountListAvailabilityProjectionWrite }
): Promise<boolean> {
  const claim = input.claim
  if (input.projection.sourceGeneration !== claim.generation) {
    throw new Error('账户列表投影 sourceGeneration 必须与 dirty claim generation 一致')
  }
  return client.transaction(async (tx) => {
    const dirty = businessTable(tx, 'account_list_availability_dirty')
    const current = await tx.one<{ account_id: string }>(`
      SELECT account_id
      FROM ${dirty}
      WHERE account_id = ?
        AND generation = ?
        AND claim_token = ?
      LIMIT 1
    `, [claim.accountId, claim.generation, claim.claimToken])
    if (!current) return false
    const written = await upsertAccountListAvailabilityProjectionInTransaction(tx, input.projection)
    if (!written) {
      throw new Error(`账户列表投影 ${claim.accountId} generation ${claim.generation} 被更高版本覆盖`)
    }
    const acknowledged = await acknowledgeAccountListAvailabilityDirtyInTransaction(tx, {
      accountId: claim.accountId,
      generation: claim.generation,
      claimToken: claim.claimToken
    })
    if (!acknowledged) {
      throw new Error(`账户列表投影 ${claim.accountId} generation ${claim.generation} 无法确认 dirty claim`)
    }
    return true
  })
}

/**
 * Commits one worker batch in a single transaction. Each row still retains
 * its own generation/claim fence, while avoiding one database transaction per
 * account during a large bootstrap or stale refresh.
 */
export async function applyAccountListAvailabilityProjectionDirtyClaimsInClient(
  client: DatabaseClient,
  input: Array<{ claim: AccountListAvailabilityDirtyClaim; projection: AccountListAvailabilityProjectionWrite }>
): Promise<Map<string, boolean>> {
  for (const entry of input) {
    if (entry.projection.sourceGeneration !== entry.claim.generation) {
      throw new Error('账户列表投影 sourceGeneration 必须与 dirty claim generation 一致')
    }
  }
  if (input.length === 0) return new Map()
  if (client.driver === 'postgres') {
    return applyAccountListAvailabilityProjectionDirtyClaimsPostgresInClient(client, input)
  }
  return client.transaction(async (tx) => {
    const dirty = businessTable(tx, 'account_list_availability_dirty')
    const results = new Map<string, boolean>()
    for (const entry of input) {
      const { claim, projection } = entry
      const current = await tx.one<{ account_id: string }>(`
        SELECT account_id
        FROM ${dirty}
        WHERE account_id = ?
          AND generation = ?
          AND claim_token = ?
        LIMIT 1
      `, [claim.accountId, claim.generation, claim.claimToken])
      if (!current) {
        results.set(claim.claimToken, false)
        continue
      }
      const written = await upsertAccountListAvailabilityProjectionInTransaction(tx, projection)
      if (!written) {
        throw new Error(`账户列表投影 ${claim.accountId} generation ${claim.generation} 被更高版本覆盖`)
      }
      const acknowledged = await acknowledgeAccountListAvailabilityDirtyInTransaction(tx, {
        accountId: claim.accountId,
        generation: claim.generation,
        claimToken: claim.claimToken
      })
      if (!acknowledged) {
        throw new Error(`账户列表投影 ${claim.accountId} generation ${claim.generation} 无法确认 dirty claim`)
      }
      results.set(claim.claimToken, true)
    }
    return results
  })
}

async function applyAccountListAvailabilityProjectionDirtyClaimsPostgresInClient(
  client: DatabaseClient,
  input: Array<{ claim: AccountListAvailabilityDirtyClaim; projection: AccountListAvailabilityProjectionWrite }>
): Promise<Map<string, boolean>> {
  const normalized = input.map(({ claim, projection }) => ({ claim, projection: normalizeProjectionWrite(projection) }))
  return client.transaction(async (tx) => {
    const dirty = businessTable(tx, 'account_list_availability_dirty')
    const projections = businessTable(tx, 'account_list_availability_projections')
    const projectionIndex = businessTable(tx, 'account_list_availability_projection_index')
    const projectionTags = businessTable(tx, 'account_list_availability_projection_tags')
    const projectionSearchTerms = businessTable(tx, 'account_list_availability_projection_search_terms')
    const runtimeOverlays = businessTable(tx, 'account_list_availability_runtime_overlays')
    const viewerHealth = businessTable(tx, 'account_list_availability_projection_viewer_health')
    const candidatesSql = normalized.map(() => '(CAST(? AS text), CAST(? AS integer), CAST(? AS text))').join(', ')
    const candidatesParams = normalized.flatMap(({ claim }) => [claim.accountId, claim.generation, claim.claimToken])
    const lockedRows = await tx.query<{ account_id: string; claim_token: string }>(`
      SELECT dirty_accounts.account_id, dirty_accounts.claim_token
      FROM ${dirty} dirty_accounts
      INNER JOIN (VALUES ${candidatesSql}) AS candidates(account_id, generation, claim_token)
        ON candidates.account_id = dirty_accounts.account_id
       AND candidates.generation = dirty_accounts.generation
       AND candidates.claim_token = dirty_accounts.claim_token
      FOR UPDATE OF dirty_accounts
    `, candidatesParams)
    const lockedTokens = new Set(lockedRows.map((row) => requiredText(row.claim_token, 256, 'claim_token')))
    const result = new Map(normalized.map(({ claim }) => [claim.claimToken, false]))
    const writes = normalized.filter(({ claim }) => lockedTokens.has(claim.claimToken))
    if (writes.length === 0) return result

    const projectionValuesSql = writes.map(() => `
      (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).join(', ')
    const projectionParams = writes.flatMap(({ projection }) => [
      projection.viewerSystemAccountId,
      projection.accountId,
      projection.sourceAccountId ?? null,
      projection.authorizationId ?? null,
      projection.effectiveStatus,
      projection.schedulableBucket,
      projection.providerCode,
      projection.providerProtocolProfileId,
      projection.accountType,
      projection.boundGroupId ?? null,
      projection.nameSortKey,
      projection.prioritySortKey,
      projection.superPrioritySortKey,
      projection.fallbackSortKey,
      projection.concurrencySortKey,
      projection.accountExpiresAtSortKey ?? null,
      projection.lastUsedAtSortKey ?? null,
      projection.createdAtSortKey,
      JSON.stringify(projection.payload),
      projection.sourceGeneration,
      projection.nextTransitionAt ?? null,
      projection.projectedAt
    ])
    const writtenRows = await tx.query<{ account_id: string }>(`
      INSERT INTO ${projections} (
        viewer_system_account_id, account_id, source_account_id, authorization_id,
        effective_status, schedulable_bucket, provider_code, provider_protocol_profile_id,
        account_type, bound_group_id, name_sort_key, priority_sort_key,
        super_priority_sort_key, fallback_sort_key, concurrency_sort_key,
        account_expires_at_sort_key, last_used_at_sort_key, created_at_sort_key,
        payload_json, source_generation, next_transition_at, projected_at
      ) VALUES ${projectionValuesSql}
      ON CONFLICT(viewer_system_account_id, account_id) DO UPDATE SET
        source_account_id = excluded.source_account_id,
        authorization_id = excluded.authorization_id,
        effective_status = excluded.effective_status,
        schedulable_bucket = excluded.schedulable_bucket,
        provider_code = excluded.provider_code,
        provider_protocol_profile_id = excluded.provider_protocol_profile_id,
        account_type = excluded.account_type,
        bound_group_id = excluded.bound_group_id,
        name_sort_key = excluded.name_sort_key,
        priority_sort_key = excluded.priority_sort_key,
        super_priority_sort_key = excluded.super_priority_sort_key,
        fallback_sort_key = excluded.fallback_sort_key,
        concurrency_sort_key = excluded.concurrency_sort_key,
        account_expires_at_sort_key = excluded.account_expires_at_sort_key,
        last_used_at_sort_key = excluded.last_used_at_sort_key,
        created_at_sort_key = excluded.created_at_sort_key,
        payload_json = excluded.payload_json,
        source_generation = excluded.source_generation,
        next_transition_at = excluded.next_transition_at,
        projected_at = excluded.projected_at
      WHERE ${projections}.source_generation <= excluded.source_generation
      RETURNING account_id
    `, projectionParams)
    const writtenAccountIds = new Set(writtenRows.map((row) => requiredText(row.account_id, 256, 'account_id')))
    if (writtenAccountIds.size !== writes.length || writes.some(({ projection }) => !writtenAccountIds.has(projection.accountId))) {
      throw new Error('账户列表投影批量写入被更高 generation 覆盖')
    }

    const indexValuesSql = writes.map(() => `
      (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `).join(', ')
    const indexParams = writes.flatMap(({ projection }) => [
      projection.viewerSystemAccountId,
      projection.accountId,
      projection.effectiveStatus,
      projection.schedulableBucket,
      projection.providerCode,
      projection.providerProtocolProfileId,
      projection.accountType,
      projection.boundGroupId ?? null,
      projection.nameSortKey,
      projection.prioritySortKey,
      projection.superPrioritySortKey,
      projection.fallbackSortKey,
      projection.concurrencySortKey,
      projection.accountExpiresAtSortKey ?? null,
      projection.lastUsedAtSortKey ?? null,
      projection.createdAtSortKey,
      projection.payload.accessType,
      projection.searchIndexComplete ? 1 : 0,
      projection.payload.authorizationQuotaExceeded ? 1 : 0
    ])
    await tx.execute(`
      INSERT INTO ${projectionIndex} (
        viewer_system_account_id, account_id, effective_status, schedulable_bucket,
        provider_code, provider_protocol_profile_id, account_type, bound_group_id,
        name_sort_key, priority_sort_key, super_priority_sort_key, fallback_sort_key,
        concurrency_sort_key, account_expires_at_sort_key, last_used_at_sort_key,
        created_at_sort_key, access_type_sort_key, search_index_complete, authorization_quota_exceeded
      ) VALUES ${indexValuesSql}
      ON CONFLICT(viewer_system_account_id, account_id) DO UPDATE SET
        effective_status = excluded.effective_status,
        schedulable_bucket = excluded.schedulable_bucket,
        provider_code = excluded.provider_code,
        provider_protocol_profile_id = excluded.provider_protocol_profile_id,
        account_type = excluded.account_type,
        bound_group_id = excluded.bound_group_id,
        name_sort_key = excluded.name_sort_key,
        priority_sort_key = excluded.priority_sort_key,
        super_priority_sort_key = excluded.super_priority_sort_key,
        fallback_sort_key = excluded.fallback_sort_key,
        concurrency_sort_key = excluded.concurrency_sort_key,
        account_expires_at_sort_key = excluded.account_expires_at_sort_key,
        last_used_at_sort_key = excluded.last_used_at_sort_key,
        created_at_sort_key = excluded.created_at_sort_key,
        access_type_sort_key = excluded.access_type_sort_key,
        search_index_complete = excluded.search_index_complete,
        authorization_quota_exceeded = excluded.authorization_quota_exceeded
    `, indexParams)

    // Several authorized rows can share one source account. Deduplicate before
    // UPSERT so PostgreSQL never attempts to update one overlay twice in the
    // same statement.
    const overlayByAccountId = new Map<string, typeof writes[number]['projection']>()
    for (const { projection } of writes) overlayByAccountId.set(projection.concurrencyAccountId, projection)
    const overlays = [...overlayByAccountId.entries()]
    await tx.execute(`
      INSERT INTO ${runtimeOverlays} (
        account_id, current_concurrency, observed_at, next_reconcile_at
      ) VALUES ${overlays.map(() => '(?, ?, ?, NULL)').join(', ')}
      ON CONFLICT(account_id) DO UPDATE SET
        current_concurrency = excluded.current_concurrency,
        observed_at = excluded.observed_at,
        next_reconcile_at = NULL
    `, overlays.flatMap(([accountId, projection]) => [
      accountId,
      projection.currentConcurrency,
      projection.projectedAt
    ]))

    const accountPairsSql = writes.map(() => '(?, ?)').join(', ')
    const accountPairsParams = writes.flatMap(({ projection }) => [projection.viewerSystemAccountId, projection.accountId])
    await tx.execute(`
      DELETE FROM ${projectionTags}
      WHERE (viewer_system_account_id, account_id) IN (VALUES ${accountPairsSql})
    `, accountPairsParams)
    const tagWrites = writes.flatMap(({ projection }) => projection.tagIds.map((tagId) => [
      projection.viewerSystemAccountId,
      projection.accountId,
      tagId
    ]))
    if (tagWrites.length > 0) {
      await tx.execute(`
        INSERT INTO ${projectionTags} (viewer_system_account_id, account_id, tag_id)
        VALUES ${tagWrites.map(() => '(?, ?, ?)').join(', ')}
        ON CONFLICT(viewer_system_account_id, account_id, tag_id) DO NOTHING
      `, tagWrites.flat())
    }
    await tx.execute(`
      DELETE FROM ${projectionSearchTerms}
      WHERE (viewer_system_account_id, account_id) IN (VALUES ${accountPairsSql})
    `, accountPairsParams)
    const searchWrites = writes.flatMap(({ projection }) => (projection.searchIndexComplete ? (projection.searchTerms ?? []) : []).map((term) => [
      projection.viewerSystemAccountId,
      projection.accountId,
      term,
      projection.nameSortKey,
      projection.createdAtSortKey
    ]))
    if (searchWrites.length > 0) {
      await tx.execute(`
        INSERT INTO ${projectionSearchTerms} (
          viewer_system_account_id, account_id, term, name_sort_key, created_at_sort_key
        ) VALUES ${searchWrites.map(() => '(?, ?, ?, ?, ?)').join(', ')}
        ON CONFLICT(viewer_system_account_id, account_id, term) DO NOTHING
      `, searchWrites.flat())
    }
    const viewerUpdates = new Map<string, string>()
    for (const { projection } of writes) {
      viewerUpdates.set(projection.viewerSystemAccountId, projection.projectedAt ?? new Date().toISOString())
    }
    for (const [viewerSystemAccountId, updatedAt] of viewerUpdates) {
      await markAccountListAvailabilityProjectionViewerHealthStaleInTransaction(tx, viewerHealth, viewerSystemAccountId, updatedAt)
    }
    const acknowledgedRows = await tx.query<{ claim_token: string }>(`
      DELETE FROM ${dirty} dirty_accounts
      USING (VALUES ${candidatesSql}) AS candidates(account_id, generation, claim_token)
      WHERE dirty_accounts.account_id = candidates.account_id
        AND dirty_accounts.generation = candidates.generation
        AND dirty_accounts.claim_token = candidates.claim_token
      RETURNING dirty_accounts.claim_token
    `, candidatesParams)
    if (acknowledgedRows.length !== writes.length) {
      throw new Error('账户列表投影批量确认 dirty claim 失败')
    }
    for (const { claim } of writes) result.set(claim.claimToken, true)
    return result
  })
}

/** Applies a current claim when an account instance is no longer visible. */
export async function applyAccountListAvailabilityProjectionDeletionDirtyClaimInClient(
  client: DatabaseClient,
  input: { claim: AccountListAvailabilityDirtyClaim }
): Promise<boolean> {
  const claim = input.claim
  return client.transaction(async (tx) => {
    const dirty = businessTable(tx, 'account_list_availability_dirty')
    const current = await tx.one<{ account_id: string }>(`
      SELECT account_id
      FROM ${dirty}
      WHERE account_id = ?
        AND generation = ?
        AND claim_token = ?
      LIMIT 1
    `, [claim.accountId, claim.generation, claim.claimToken])
    if (!current) return false
    await deleteAccountListAvailabilityProjectionForAccountInTransaction(tx, {
      accountId: claim.accountId,
      viewerSystemAccountId: claim.viewerSystemAccountId,
      sourceGeneration: claim.generation
    })
    const acknowledged = await acknowledgeAccountListAvailabilityDirtyInTransaction(tx, {
      accountId: claim.accountId,
      generation: claim.generation,
      claimToken: claim.claimToken
    })
    if (!acknowledged) {
      throw new Error(`账户列表投影 ${claim.accountId} generation ${claim.generation} 无法确认删除`)
    }
    return true
  })
}

export async function releaseAccountListAvailabilityDirtyForReplayInClient(
  client: DatabaseClient,
  input: { accountId: string; generation: number; claimToken: string; reason: string; retryDelayMs: number; nowMs?: number }
): Promise<boolean> {
  const accountId = requiredText(input.accountId, 256, 'accountId')
  const generation = positiveInteger(input.generation, 'generation', Number.MAX_SAFE_INTEGER)
  const claimToken = requiredText(input.claimToken, 256, 'claimToken')
  const reason = requiredText(input.reason, 128, 'reason')
  const retryDelayMs = nonNegativeInteger(input.retryDelayMs, 'retryDelayMs', maximumDirtyRetryDelayMs)
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  if (client.driver === 'postgres') {
    const databaseNowMs = 'FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint'
    const result = await client.execute(`
      UPDATE ${dirty}
      SET reason = ?, available_at_ms = ${databaseNowMs} + ?, claim_token = NULL,
          claimed_by = NULL, claim_until_ms = NULL, updated_at_ms = ${databaseNowMs}
      WHERE account_id = ? AND generation = ? AND claim_token = ?
    `, [reason, retryDelayMs, accountId, generation, claimToken])
    return result.changes === 1
  }
  const result = await client.execute(`
    UPDATE ${dirty}
    SET reason = ?, available_at_ms = ?, claim_token = NULL,
        claimed_by = NULL, claim_until_ms = NULL, updated_at_ms = ?
    WHERE account_id = ? AND generation = ? AND claim_token = ?
  `, [reason, nowMs + retryDelayMs, nowMs, accountId, generation, claimToken])
  return result.changes === 1
}

/**
 * This is the eventual request-path query: one indexed SQL statement obtains
 * both the durable-currentness guard and the page plus its look-ahead row. It
 * never discovers candidates or invokes runtime services. `projected_at` is
 * observability data, not a wall-clock cache TTL: periodically invalidating
 * every row would make a large viewer permanently unavailable while it was
 * being rematerialized.
 */
export async function listAccountListAvailabilityProjectionPageInClient(
  client: DatabaseClient,
  input: AccountListAvailabilityProjectionQuery
): Promise<AccountListAvailabilityProjectionPage> {
  const viewerSystemAccountId = requiredText(input.viewerSystemAccountId, 256, 'viewerSystemAccountId')
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const options = normalizeAccountListOptions(input.options)
  const includeDynamicOverlays = input.includeDynamicOverlays !== false

  const projections = businessTable(client, 'account_list_availability_projections')
  const projectionIndex = businessTable(client, 'account_list_availability_projection_index')
  const projectionTags = businessTable(client, 'account_list_availability_projection_tags')
  const projectionSearchTerms = businessTable(client, 'account_list_availability_projection_search_terms')
  const viewerHealth = businessTable(client, 'account_list_availability_projection_viewer_health')
  const dependencyHealth = businessTable(client, 'account_list_availability_projection_dependency_health')
  const runtimeOverlays = businessTable(client, 'account_list_availability_runtime_overlays')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const where: string[] = ['projections.viewer_system_account_id = ?']
  const params: unknown[] = [viewerSystemAccountId]
  let keyword: { normalized: string; terms: string[] } | undefined
  if (options.ids.length > 0) {
    where.push(`projections.account_id IN (${client.dialect.bindPlaceholders(options.ids.length)})`)
    params.push(...options.ids)
  }
  if (options.providerCode && options.providerCode !== 'all') {
    where.push('projections.provider_code = ?')
    params.push(options.providerCode)
  }
  if (options.providerProtocolProfileId && options.providerProtocolProfileId !== 'all') {
    where.push('projections.provider_protocol_profile_id = ?')
    params.push(options.providerProtocolProfileId)
  }
  if (options.type && options.type !== 'all') {
    where.push('projections.account_type = ?')
    params.push(options.type)
  }
  if (options.groupId) {
    where.push('projections.bound_group_id = ?')
    params.push(options.groupId)
  }
  if (options.keyword) {
    const normalizedKeyword = normalizeAccountNameSearchText(options.keyword)
    keyword = {
      normalized: normalizedKeyword,
      terms: accountNameSearchQueryTerms(options.keyword)
    }
  }
  appendStatusProjectionFilters(where, params, options.status, options.schedulable, client, includeDynamicOverlays)
  if (options.tagIds.length > 0) {
    where.push(`EXISTS (
      SELECT 1
      FROM ${projectionTags} projection_tags
      WHERE projection_tags.viewer_system_account_id = projections.viewer_system_account_id
        AND projection_tags.account_id = projections.account_id
        AND projection_tags.tag_id IN (${client.dialect.bindPlaceholders(options.tagIds.length)})
    )`)
    params.push(...options.tagIds)
  }

  const limit = options.pageSize + 1
  const offset = (options.page - 1) * options.pageSize
  const usesDatabaseClock = client.driver === 'postgres'
  const materializedCte = usesDatabaseClock ? ' MATERIALIZED' : ''
  const projectionOrder = projectionOrderClause(options.sorts, client, includeDynamicOverlays)
  const pageWindowOrder = projectionOrderClause(options.sorts, client, includeDynamicOverlays, 'page_window')
  const keywordSearchOrder = projectionOrderClause(options.sorts, client, includeDynamicOverlays, 'keyword_search')
  const pageWindowColumns = `
    projections.viewer_system_account_id, projections.account_id,
    projections.priority_sort_key, projections.super_priority_sort_key,
    projections.fallback_sort_key, projections.name_sort_key,
    projections.account_type, projections.provider_code,
    projections.concurrency_sort_key, projections.effective_status,
    projections.account_expires_at_sort_key, projections.last_used_at_sort_key,
    projections.created_at_sort_key, projections.access_type_sort_key`
  const supportsNameKeywordPaging = Boolean(
    keyword?.terms.length
    && options.sorts.length === 1
    && options.sorts[0]?.field === 'name'
  )
  const usesFastNameKeywordPaging = supportsNameKeywordPaging && where.length === 1
  const keywordMatchesCte = keyword?.terms.length && !usesFastNameKeywordPaging
    ? `keyword_matches AS${materializedCte} (
      SELECT projection_search.account_id
      FROM ${projectionSearchTerms} projection_search
      WHERE projection_search.viewer_system_account_id = ?
        AND projection_search.term = ?
    ),`
    : ''
  const keywordMatchesParams: unknown[] = keyword?.terms.length && !usesFastNameKeywordPaging
    ? [viewerSystemAccountId, keyword.terms[0]!]
    : []
  let keywordNamePagingCte = ''
  let keywordNamePagingParams: unknown[] = []
  if (keyword && usesFastNameKeywordPaging) {
    const pageCandidateLimit = limit + offset
    const upperBound = textPrefixUpperBound(keyword.normalized)
    const containsExpression = client.driver === 'postgres'
      ? 'keyword_search.name_sort_key LIKE ? ESCAPE \'\\\''
      : 'instr(keyword_search.name_sort_key, ?) > 0'
    const containsValue = client.driver === 'postgres'
      ? `%${escapeAccountNameSearchLike(keyword.normalized)}%`
      : keyword.normalized
    const baseWhere = where.join('\n        AND ')
    keywordNamePagingCte = `keyword_prefix AS${materializedCte} (
      SELECT projections.viewer_system_account_id, projections.account_id
      FROM ${projectionIndex} projections
      WHERE ${baseWhere}
        AND projections.name_sort_key >= ? AND projections.name_sort_key < ?
        AND projections.search_index_complete = 0
      ${projectionOrder}
      LIMIT ?
    ), keyword_contains AS${materializedCte} (
      SELECT keyword_search.viewer_system_account_id, keyword_search.account_id
      FROM ${projectionSearchTerms} keyword_search
      WHERE keyword_search.viewer_system_account_id = ?
        AND keyword_search.term = ?
        AND ${containsExpression}
      ${keywordSearchOrder}
      LIMIT ?
    ), keyword_candidates AS${materializedCte} (
      SELECT * FROM keyword_prefix
      UNION ALL
      SELECT * FROM keyword_contains
    ),`
    keywordNamePagingParams = [
      ...params, keyword.normalized, upperBound, pageCandidateLimit,
      viewerSystemAccountId, keyword.terms[0]!, containsValue, pageCandidateLimit
    ]
  } else if (keyword) {
    const keywordClauses = ['(projections.name_sort_key >= ? AND projections.name_sort_key < ?)']
    const keywordParams: unknown[] = [keyword.normalized, textPrefixUpperBound(keyword.normalized)]
    if (keyword.terms.length) {
      const containsExpression = client.driver === 'postgres'
        ? 'projections.name_sort_key LIKE ? ESCAPE \'\\\''
        : 'instr(projections.name_sort_key, ?) > 0'
      keywordClauses.push(`(
        ${containsExpression}
        AND EXISTS (
          SELECT 1
          FROM keyword_matches
          WHERE keyword_matches.account_id = projections.account_id
        )
      )`)
      keywordParams.push(client.driver === 'postgres'
        ? `%${escapeAccountNameSearchLike(keyword.normalized)}%`
        : keyword.normalized)
    }
    where.push(`(${keywordClauses.join(' OR ')})`)
    params.push(...keywordParams)
  }
  const pageKeysCte = `page_window AS${materializedCte} (
      SELECT ${pageWindowColumns}
      FROM ${projectionIndex} projections
      ${usesFastNameKeywordPaging ? `INNER JOIN keyword_candidates
        ON keyword_candidates.viewer_system_account_id = projections.viewer_system_account_id
        AND keyword_candidates.account_id = projections.account_id` : ''}
      WHERE ${where.join('\n        AND ')}
      ${projectionOrder}
      LIMIT ? OFFSET ?
    ), page_keys AS${materializedCte} (
      SELECT page_window.viewer_system_account_id, page_window.account_id,
        ROW_NUMBER() OVER (${pageWindowOrder}) AS page_order
      FROM page_window
    )`
  const pageKeysParams: unknown[] = [...params, limit, offset]
  const usageStatsDaily = usesDatabaseClock && includeDynamicOverlays
    ? client.dialect.qualifyTable('juhe_stats', 'usage_stats_daily')
    : ''
  const usageStatsTotals = usesDatabaseClock && includeDynamicOverlays
    ? client.dialect.qualifyTable('juhe_stats', 'usage_stats_totals')
    : ''
  const accountUsageSnapshots = usesDatabaseClock && includeDynamicOverlays
    ? client.dialect.qualifyTable('juhe_stats', 'account_usage_snapshots')
    : ''
  const systemSettings = usesDatabaseClock && includeDynamicOverlays
    ? businessTable(client, 'system_settings')
    : ''
  const projectionClockCte = usesDatabaseClock
    ? `projection_clock AS${materializedCte} (
      SELECT clock_timestamp() AS now_at
    ),`
    : ''
  // The displayed daily usage and balance snapshot are page-bounded dynamic
  // overlays. Keeping them in this statement avoids both stale JSON payloads
  // and a second request-time round trip to the stats database.
  const usageStatsContextCte = usesDatabaseClock && includeDynamicOverlays
    ? `usage_stats_context AS${materializedCte} (
      SELECT value_json::jsonb #>> '{}' AS timezone
      FROM ${systemSettings}
      WHERE system_account_id = 'sys_admin' AND key = 'usageStatsTimezone'
      LIMIT 1
    ),`
    : ''
  const projectionNow = usesDatabaseClock
    ? `to_char((SELECT now_at FROM projection_clock) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`
    : '?'
  const projectionHealthChecks = usesDatabaseClock
    ? `
        SELECT 1
        FROM ${dirty} dirty_accounts
        WHERE dirty_accounts.viewer_system_account_id = ?
        UNION ALL
        SELECT 1
        FROM ${viewerHealth} health
        WHERE health.viewer_system_account_id = ?
          AND (
            health.is_current = 0
            OR (health.next_transition_at IS NOT NULL AND health.next_transition_at <= ${projectionNow})
          )
        UNION ALL
        SELECT 1
        WHERE NOT EXISTS (
          SELECT 1
          FROM ${viewerHealth} missing_health
          WHERE missing_health.viewer_system_account_id = ?
        )
        UNION ALL
        SELECT 1
        FROM ${dependencyHealth} runtime_dependency
        WHERE runtime_dependency.dependency_name = 'runtime_state'
          AND (
            runtime_dependency.state <> 'healthy'
            OR runtime_dependency.updated_at::timestamptz <= (SELECT now_at FROM projection_clock)
              - (? * INTERVAL '1 millisecond')
          )
        UNION ALL
        SELECT 1
        WHERE NOT EXISTS (
          SELECT 1
          FROM ${dependencyHealth} missing_runtime_dependency
          WHERE missing_runtime_dependency.dependency_name = 'runtime_state'
        )
        ${includeDynamicOverlays ? `
        UNION ALL
        SELECT 1
        WHERE NOT EXISTS (
          SELECT 1
          FROM usage_stats_context
        )` : ''}`
    : `
        SELECT 1
        FROM ${dirty} dirty_accounts
        WHERE dirty_accounts.viewer_system_account_id = ?
        UNION ALL
        SELECT 1
        FROM ${viewerHealth} health
        WHERE health.viewer_system_account_id = ?
          AND (
            health.is_current = 0
            OR (health.next_transition_at IS NOT NULL AND health.next_transition_at <= ${projectionNow})
          )
        UNION ALL
        SELECT 1
        WHERE NOT EXISTS (
          SELECT 1
          FROM ${viewerHealth} missing_health
          WHERE missing_health.viewer_system_account_id = ?
        )`
  const pageRuntimeOverlaySelect = includeDynamicOverlays
    ? ', runtime_overlay.current_concurrency'
    : ''
  const pageRuntimeOverlayJoin = includeDynamicOverlays
    ? `
      LEFT JOIN ${runtimeOverlays} runtime_overlay
        ON runtime_overlay.account_id = COALESCE(projections.source_account_id, projections.account_id)`
    : ''
  const pageDynamicBase = usesDatabaseClock && includeDynamicOverlays
    ? `SELECT page.account_id, page.projected_at, page.page_order,
          (
            CASE
              WHEN COALESCE((page.payload_json::jsonb ->> 'balanceQueryEnabled')::boolean, false)
                AND balance_snapshot.snapshot_json IS NOT NULL
                AND balance_snapshot.next_refresh_after IS NOT DISTINCT FROM NULLIF(
                  page.payload_json::jsonb ->> 'balanceQueryNextRefreshAt', ''
                )::timestamptz
              THEN jsonb_set(
                jsonb_set(
                  jsonb_set(
                    page.payload_json::jsonb,
                    '{currentConcurrency}',
                    to_jsonb(COALESCE(page.current_concurrency, 0)),
                    true
                  ),
                  '{todayUsage}',
                  jsonb_build_object(
                    'requestCount', COALESCE(today_usage.request_count, 0),
                    'totalTokens', COALESCE(today_usage.input_tokens, 0) + COALESCE(today_usage.output_tokens, 0),
                    'totalCost', COALESCE(today_usage.total_cost_usd, 0)
                  ),
                  true
                ),
                '{balanceSnapshot}',
                balance_snapshot.snapshot_json::jsonb - 'keyBalances',
                true
              )
              ELSE jsonb_set(
                jsonb_set(
                  page.payload_json::jsonb,
                  '{currentConcurrency}',
                  to_jsonb(COALESCE(page.current_concurrency, 0)),
                  true
                ),
                '{todayUsage}',
                jsonb_build_object(
                  'requestCount', COALESCE(today_usage.request_count, 0),
                  'totalTokens', COALESCE(today_usage.input_tokens, 0) + COALESCE(today_usage.output_tokens, 0),
                  'totalCost', COALESCE(today_usage.total_cost_usd, 0)
                ),
                true
              ) - 'balanceSnapshot'
            END
          )::jsonb AS payload_json,
          page.payload_json::jsonb ->> 'accessType' AS access_type,
          authorization_total_usage.last_used_at AS authorization_last_used_at
        FROM page
        CROSS JOIN usage_stats_context
        LEFT JOIN ${usageStatsDaily} today_usage
          ON today_usage.system_account_id = page.viewer_system_account_id
          AND today_usage.scope_type = CASE
            WHEN page.payload_json::jsonb ->> 'accessType' = 'authorized' THEN 'account_authorization'
            ELSE 'account'
          END
          AND today_usage.scope_id = CASE
            WHEN page.payload_json::jsonb ->> 'accessType' = 'authorized'
              THEN page.payload_json::jsonb ->> 'accountAuthorizationId'
            ELSE page.account_id
          END
          AND today_usage.stat_date = to_char(
            (SELECT now_at FROM projection_clock) AT TIME ZONE usage_stats_context.timezone,
            'YYYY-MM-DD'
          )
        LEFT JOIN ${usageStatsTotals} authorization_total_usage
          ON authorization_total_usage.system_account_id = page.viewer_system_account_id
          AND authorization_total_usage.scope_type = 'account_authorization'
          AND authorization_total_usage.scope_id = page.payload_json::jsonb ->> 'accountAuthorizationId'
        LEFT JOIN ${accountUsageSnapshots} balance_snapshot
          ON balance_snapshot.kind = 'relay_balance'
          AND balance_snapshot.account_id = page.account_id`
    : includeDynamicOverlays
      ? `SELECT page.account_id, page.projected_at, page.page_order,
          json_set(page.payload_json, '$.currentConcurrency', COALESCE(page.current_concurrency, 0)) AS payload_json,
          NULL AS access_type,
          NULL AS authorization_last_used_at
        FROM page`
      : `SELECT page.account_id, page.projected_at, page.page_order,
          page.payload_json,
          NULL AS access_type,
          NULL AS authorization_last_used_at
        FROM page`
  const pageDynamicPayload = usesDatabaseClock && includeDynamicOverlays
    ? `CASE
              WHEN access_type = 'authorized' AND authorization_last_used_at IS NOT NULL THEN jsonb_set(
                payload_json,
                '{lastUsedAt}',
                to_jsonb(authorization_last_used_at),
                true
              )
              WHEN access_type = 'authorized' THEN payload_json - 'lastUsedAt'
              ELSE payload_json
            END::text`
    : 'payload_json'
  const rows = await client.query<AccountListAvailabilityProjectionRow>(`
    WITH ${projectionClockCte}${usageStatsContextCte}
    projection_health AS${materializedCte} (
      SELECT EXISTS (
        ${projectionHealthChecks}
      ) AS unavailable,
      ${projectionNow} AS projection_now_iso
    ), ${keywordMatchesCte}${keywordNamePagingCte}${pageKeysCte}, page AS (
      SELECT projections.viewer_system_account_id, projections.account_id, projections.payload_json, projections.projected_at,
        page_keys.page_order
        ${pageRuntimeOverlaySelect}
      FROM ${projections} projections
      INNER JOIN page_keys
        ON page_keys.viewer_system_account_id = projections.viewer_system_account_id
        AND page_keys.account_id = projections.account_id
      ${pageRuntimeOverlayJoin}
    ), page_dynamic_base AS (
      ${pageDynamicBase}
    ), page_dynamic AS (
      SELECT account_id, projected_at, page_order,
        ${pageDynamicPayload} AS payload_json
      FROM page_dynamic_base
    )
    SELECT projection_health.unavailable AS projection_unavailable,
      projection_health.projection_now_iso,
      page_dynamic.account_id,
      page_dynamic.payload_json,
      page_dynamic.projected_at
    FROM projection_health
    LEFT JOIN page_dynamic ON projection_health.unavailable = ${client.driver === 'postgres' ? 'false' : '0'}
    ORDER BY page_dynamic.page_order ASC NULLS FIRST
  `, [
    ...(usesDatabaseClock
      ? [
          viewerSystemAccountId,
          viewerSystemAccountId,
          viewerSystemAccountId,
          runtimeConfig.background.accountListAvailabilityProjectionRuntimeDependencyMaxAgeMs
        ]
      : [
          viewerSystemAccountId,
          viewerSystemAccountId,
          new Date(nowMs).toISOString(),
          viewerSystemAccountId,
          new Date(nowMs).toISOString()
        ]),
    ...keywordMatchesParams,
    ...keywordNamePagingParams,
    ...pageKeysParams
  ])
  if (rows.some((row) => booleanValue(row.projection_unavailable))) {
    throw new AccountListAvailabilityProjectionUnavailableError('账户列表投影正在更新，请稍后重试')
  }
  const pageRowsWithPayload = rows.filter((row) => Boolean(row.account_id))
  const hasMore = pageRowsWithPayload.length > options.pageSize
  const pageRows = hasMore ? pageRowsWithPayload.slice(0, options.pageSize) : pageRowsWithPayload
  const includesRuntimeStatusFilter = accountStatusFilterValues(options.status).length > 0 || options.schedulable !== 'all'
  const items = pageRows.map((row) => parseProjectionPayload(row, includesRuntimeStatusFilter))
  const projectedAt = pageRows.reduce((latest, row) => row.projected_at > latest ? row.projected_at : latest, '')
  return {
    items,
    total: (options.page - 1) * options.pageSize + items.length + (hasMore ? 1 : 0),
    hasMore,
    page: options.page,
    pageSize: options.pageSize,
    generatedAt: projectedAt || rows[0]?.projection_now_iso || new Date(nowMs).toISOString(),
    projectedAt
  }
}

function appendStatusProjectionFilters(
  where: string[],
  params: unknown[],
  rawStatus: string | undefined,
  schedulable: AccountListSchedulableFilter,
  client: Pick<DatabaseClient, 'dialect' | 'driver'>,
  includeDynamicOverlays: boolean
): void {
  const statuses = rawStatus
    ?.split(',')
    .map((value) => value.trim())
    .filter((value) => value && value !== 'all')
    .filter(isAccountStatus) ?? []
  if (rawStatus?.trim() && !statuses.length) {
    where.push('1 = 0')
  } else if (statuses.length) {
    where.push(`projections.effective_status IN (${client.dialect.bindPlaceholders(statuses.length)})`)
    params.push(...statuses)
  }
  if (schedulable === 'disabled' && !includeDynamicOverlays) {
    const authorizationQuotaExceeded = 'projections.authorization_quota_exceeded = 1'
    where.push(`(projections.schedulable_bucket = ? OR ${authorizationQuotaExceeded})`)
    params.push('disabled')
  } else if (schedulable === 'cooling' && !includeDynamicOverlays) {
    const authorizationQuotaExceeded = 'projections.authorization_quota_exceeded = 1'
    where.push(`projections.schedulable_bucket = ? AND NOT (${authorizationQuotaExceeded})`)
    params.push('cooling')
  } else if (schedulable !== 'all') {
    where.push('projections.schedulable_bucket = ?')
    params.push(schedulable)
  }
}

function projectionOrderClause(
  sorts: AccountListSort[],
  client: Pick<DatabaseClient, 'driver'>,
  includeDynamicOverlays: boolean,
  tableAlias = 'projections'
): string {
  return `ORDER BY ${projectionOrderParts(sorts, client, includeDynamicOverlays, tableAlias).join(', ')}`
}

function projectionOrderParts(
  sorts: AccountListSort[],
  client: Pick<DatabaseClient, 'driver'>,
  includeDynamicOverlays: boolean,
  tableAlias: string
): string[] {
  const textCollation = client.driver === 'postgres' ? ' COLLATE "C"' : ''
  const accessType = `${tableAlias}.access_type_sort_key`
  const columns: Record<AccountListSort['field'], string> = {
    priority: !includeDynamicOverlays
      ? `CASE WHEN ${accessType} = 'authorized' THEN 0 ELSE ${tableAlias}.priority_sort_key END`
      : `${tableAlias}.priority_sort_key`,
    superPriority: `${tableAlias}.super_priority_sort_key`,
    fallback: `${tableAlias}.fallback_sort_key`,
    name: `${tableAlias}.name_sort_key${textCollation}`,
    type: `${tableAlias}.account_type${textCollation}`,
    providerCode: `${tableAlias}.provider_code${textCollation}`,
    systemAccount: `${tableAlias}.viewer_system_account_id${textCollation}`,
    concurrency: `${tableAlias}.concurrency_sort_key`,
    status: `CASE ${tableAlias}.effective_status
      WHEN 'active' THEN 1
      WHEN 'temporary_unavailable' THEN 2
      WHEN 'rate_limited' THEN 3
      WHEN 'pending_test' THEN 4
      WHEN 'quality_isolated' THEN 5
      WHEN 'error' THEN 6
      WHEN 'disabled' THEN 7
      ELSE 8
    END`,
    accountExpiresAt: `${tableAlias}.account_expires_at_sort_key`,
    lastUsedAt: `${tableAlias}.last_used_at_sort_key`
  }
  const parts = sorts.flatMap((sort) => {
    const direction = sort.order === 'desc' ? 'DESC' : 'ASC'
    const column = columns[sort.field]
    return sort.field === 'lastUsedAt'
      ? [`CASE WHEN ${column} IS NULL THEN 1 ELSE 0 END ASC`, `${column} ${direction}`]
      : [`${column} ${direction}`]
  })
  return [...parts, `${tableAlias}.created_at_sort_key ASC`, `${tableAlias}.account_id ASC`]
}

function parseProjectionPayload(row: AccountListAvailabilityProjectionRow, includesRuntimeStatusFilter: boolean): AccountListItem {
  try {
    const value: unknown = JSON.parse(row.payload_json)
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      throw new Error('payload is not an object')
    }
    const item = value as AccountListItem
    if (includesRuntimeStatusFilter) return item
    return {
      ...item,
      // Preserve the legacy non-runtime page contract without issuing a tag
      // lookup after the one projection SQL statement.
      tags: Array.isArray(item.tags) ? item.tags.map(({ id, name }) => ({ id, name })) : []
    }
  } catch (error) {
    throw new AccountListAvailabilityProjectionUnavailableError(
      `账户列表投影 ${row.account_id} payload 无法读取: ${error instanceof Error ? error.message : String(error)}`
    )
  }
}

function booleanValue(value: number | boolean | string): boolean {
  return value === true || value === 1 || value === '1' || value === 'true' || value === 't'
}

function normalizeProjectionWrite(input: AccountListAvailabilityProjectionWrite): AccountListAvailabilityProjectionWrite & {
  projectedAt: string
  searchIndexComplete: boolean
} {
  if (!isAccountStatus(input.effectiveStatus)) {
    throw new Error(`账户列表投影状态无效: ${input.effectiveStatus}`)
  }
  if (!isSchedulableBucket(input.schedulableBucket)) {
    throw new Error(`账户列表投影可调度桶无效: ${input.schedulableBucket}`)
  }
  if (!input.payload || typeof input.payload !== 'object') {
    throw new Error('账户列表投影 payload 缺失')
  }
  // AccountListItem requires this field in production. Keep old fixture and
  // recovery writes compatible by treating an omitted value as an owner row.
  const payload = {
    ...input.payload,
    accessType: input.payload.accessType ?? 'owner'
  }
  const searchTerms = normalizedSearchTerms(input.searchTerms)
  return {
    ...input,
    payload,
    viewerSystemAccountId: requiredText(input.viewerSystemAccountId, 256, 'viewerSystemAccountId'),
    accountId: requiredText(input.accountId, 256, 'accountId'),
    concurrencyAccountId: requiredText(input.concurrencyAccountId, 256, 'concurrencyAccountId'),
    currentConcurrency: nonNegativeInteger(input.currentConcurrency, 'currentConcurrency'),
    sourceAccountId: optionalText(input.sourceAccountId, 256, 'sourceAccountId'),
    authorizationId: optionalText(input.authorizationId, 256, 'authorizationId'),
    providerCode: requiredText(input.providerCode, 128, 'providerCode'),
    providerProtocolProfileId: requiredText(input.providerProtocolProfileId, 256, 'providerProtocolProfileId'),
    accountType: requiredText(input.accountType, 64, 'accountType'),
    boundGroupId: optionalText(input.boundGroupId, 256, 'boundGroupId'),
    nameSortKey: normalizedAccountNameSortKey(input.nameSortKey),
    prioritySortKey: safeInteger(input.prioritySortKey, 'prioritySortKey'),
    superPrioritySortKey: safeInteger(input.superPrioritySortKey, 'superPrioritySortKey'),
    fallbackSortKey: safeInteger(input.fallbackSortKey, 'fallbackSortKey'),
    concurrencySortKey: safeInteger(input.concurrencySortKey, 'concurrencySortKey'),
    accountExpiresAtSortKey: optionalText(input.accountExpiresAtSortKey, 64, 'accountExpiresAtSortKey'),
    lastUsedAtSortKey: optionalText(input.lastUsedAtSortKey, 64, 'lastUsedAtSortKey'),
    createdAtSortKey: requiredText(input.createdAtSortKey, 64, 'createdAtSortKey'),
    tagIds: normalizedTagIds(input.tagIds),
    searchTerms,
    // A non-empty list originates from the completed business search document.
    // Keep direct/recovery writers compatible; they already pass exactly the
    // terms that are safe to use for contains matching.
    searchIndexComplete: searchTerms.length > 0,
    sourceGeneration: positiveInteger(input.sourceGeneration, 'sourceGeneration', Number.MAX_SAFE_INTEGER),
    nextTransitionAt: optionalText(input.nextTransitionAt, 64, 'nextTransitionAt'),
    projectedAt: optionalText(input.projectedAt, 64, 'projectedAt') ?? new Date().toISOString()
  }
}

function normalizedAccountNameSortKey(value: string): string {
  const normalized = normalizeAccountNameSearchText(requiredText(value, 2048, 'nameSortKey'))
  if (!normalized) throw new Error('nameSortKey 不能为空')
  return normalized
}

function mapDirtyRow(row: AccountListAvailabilityDirtyRow): AccountListAvailabilityDirtyRecord {
  return {
    accountId: requiredText(row.account_id, 256, 'account_id'),
    viewerSystemAccountId: requiredText(row.viewer_system_account_id, 256, 'viewer_system_account_id'),
    generation: positiveInteger(row.generation, 'generation', Number.MAX_SAFE_INTEGER),
    appliedGeneration: nonNegativeInteger(row.applied_generation, 'applied_generation'),
    reason: requiredText(row.reason, 128, 'reason'),
    availableAtMs: nonNegativeInteger(row.available_at_ms, 'available_at_ms'),
    ...(row.claim_token ? { claimToken: row.claim_token } : {}),
    ...(row.claimed_by ? { claimedBy: row.claimed_by } : {}),
    ...(row.claim_until_ms === null ? {} : { claimUntilMs: nonNegativeInteger(row.claim_until_ms, 'claim_until_ms') }),
    attemptCount: nonNegativeInteger(row.attempt_count, 'attempt_count'),
    createdAtMs: nonNegativeInteger(row.created_at_ms, 'created_at_ms'),
    updatedAtMs: nonNegativeInteger(row.updated_at_ms, 'updated_at_ms')
  }
}

function businessTable(client: Pick<DatabaseClient, 'driver' | 'dialect'>, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function isSchedulableBucket(value: string): value is AccountListAvailabilitySchedulableBucket {
  return value === 'enabled' || value === 'disabled' || value === 'cooling'
}

function normalizedTagIds(values: string[]): string[] {
  return [...new Set(values.map((value) => requiredText(value, 256, 'tagId')))].sort()
}

function normalizedSearchTerms(values: string[] | undefined): string[] {
  return [...new Set((values ?? []).map((value) => {
    if (typeof value !== 'string' || !value.trim() || value.length > 256) {
      throw new Error('searchTerm 必须是 1-256 位文本')
    }
    // Search grams intentionally retain leading/trailing spaces. They are
    // part of the source index key for phrase-boundary matching.
    return value
  }))].sort()
}

function normalizedIdList(values: string[] | undefined, name: string): string[] {
  return [...new Set((values ?? []).map((value) => requiredText(value, 256, name)))].sort()
}

function requiredText(value: unknown, maximumLength: number, name: string): string {
  if (typeof value !== 'string' || !value.trim() || value.trim().length > maximumLength) {
    throw new Error(`${name} 必须是 1-${maximumLength} 位文本`)
  }
  return value.trim()
}

function optionalText(value: unknown, maximumLength: number, name: string): string | undefined {
  if (value === undefined || value === null || value === '') return undefined
  return requiredText(value, maximumLength, name)
}

function safeInteger(value: unknown, name: string): number {
  if (!Number.isSafeInteger(value)) throw new Error(`${name} 必须是安全整数`)
  return Number(value)
}

function nonNegativeInteger(value: unknown, name: string, maximum = Number.MAX_SAFE_INTEGER): number {
  const number = Number(value)
  if (!Number.isSafeInteger(number) || number < 0 || number > maximum) {
    throw new Error(`${name} 必须是 0-${maximum} 的安全整数`)
  }
  return number
}

function positiveInteger(value: unknown, name: string, maximum: number): number {
  const number = nonNegativeInteger(value, name, maximum)
  if (number === 0) throw new Error(`${name} 必须大于 0`)
  return number
}
