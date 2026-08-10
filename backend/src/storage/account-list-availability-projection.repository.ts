import { randomUUID } from 'node:crypto'

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
  maximumProjectionAgeMs: number
  nowMs?: number
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
  const projectionTags = businessTable(tx, 'account_list_availability_projection_tags')
  const projectionSearchTerms = businessTable(tx, 'account_list_availability_projection_search_terms')
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
  for (const term of value.searchTerms ?? []) {
    await tx.execute(`
        INSERT INTO ${projectionSearchTerms} (viewer_system_account_id, account_id, term)
        VALUES (?, ?, ?)
      `, [value.viewerSystemAccountId, value.accountId, term])
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

/** Rebuilds old snapshots in bounded batches when no direct write event fired. */
export async function enqueueStaleAccountListAvailabilityProjectionsInClient(
  client: DatabaseClient,
  input: { limit: number; maximumProjectionAgeMs: number; nowMs?: number }
): Promise<number> {
  const limit = positiveInteger(input.limit, 'limit', maximumDirtyClaimLimit)
  const maximumProjectionAgeMs = positiveInteger(input.maximumProjectionAgeMs, 'maximumProjectionAgeMs', 24 * 60 * 60_000)
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const projections = businessTable(client, 'account_list_availability_projections')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const staleBefore = new Date(nowMs - maximumProjectionAgeMs).toISOString()
  return client.transaction(async (tx) => {
    const rows = await tx.query<{ account_id: string }>(`
      SELECT projections.account_id
      FROM ${projections} projections
      LEFT JOIN ${dirty} dirty_accounts
        ON dirty_accounts.account_id = projections.account_id
      WHERE projections.projected_at < ?
        AND dirty_accounts.account_id IS NULL
      ORDER BY projections.projected_at ASC, projections.account_id ASC
      LIMIT ?
    `, [staleBefore, limit])
    await Promise.all(rows.map((row) => markAccountListAvailabilityDirtyInTransaction(tx, {
      accountId: row.account_id,
      reason: 'projection_stale',
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
    const rows = await tx.query<AccountListAvailabilityDirtyRow>(`
      SELECT account_id, viewer_system_account_id, generation, applied_generation, reason, available_at_ms,
        claim_token, claimed_by, claim_until_ms, attempt_count,
        created_at_ms, updated_at_ms
      FROM ${dirty}
      WHERE available_at_ms <= ${claimableNow}
        AND (claim_until_ms IS NULL OR claim_until_ms <= ${claimableNow})
      ORDER BY available_at_ms ASC, created_at_ms ASC, account_id ASC
      LIMIT ?${tx.driver === 'postgres' ? ' FOR UPDATE SKIP LOCKED' : ''}
    `, usesDatabaseClock ? [limit] : [nowMs, nowMs, limit])
    const claimed: AccountListAvailabilityDirtyClaim[] = []
    for (const row of rows) {
      const claimToken = randomUUID()
      if (usesDatabaseClock) {
        const updated = await tx.query<{ claim_until_ms: number | string; updated_at_ms: number | string }>(`
          UPDATE ${dirty}
          SET claim_token = ?, claimed_by = ?, claim_until_ms = ${databaseNowMs} + ?,
              attempt_count = attempt_count + 1, updated_at_ms = ${databaseNowMs}
          WHERE account_id = ? AND generation = ?
            AND available_at_ms <= ${databaseNowMs}
            AND (claim_until_ms IS NULL OR claim_until_ms <= ${databaseNowMs})
          RETURNING claim_until_ms, updated_at_ms
        `, [claimToken, ownerId, leaseMs, row.account_id, row.generation])
        if (updated.length !== 1) continue
        claimed.push({
          ...mapDirtyRow(row),
          claimToken,
          claimedBy: ownerId,
          claimUntilMs: nonNegativeInteger(updated[0].claim_until_ms, 'claim_until_ms'),
          attemptCount: nonNegativeInteger(row.attempt_count, 'attempt_count') + 1,
          updatedAtMs: nonNegativeInteger(updated[0].updated_at_ms, 'updated_at_ms')
        })
        continue
      }
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
 * both the projection-health guard and the page plus its look-ahead row. It
 * never discovers candidates or invokes runtime services.
 */
export async function listAccountListAvailabilityProjectionPageInClient(
  client: DatabaseClient,
  input: AccountListAvailabilityProjectionQuery
): Promise<AccountListAvailabilityProjectionPage> {
  const viewerSystemAccountId = requiredText(input.viewerSystemAccountId, 256, 'viewerSystemAccountId')
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const maximumProjectionAgeMs = positiveInteger(input.maximumProjectionAgeMs, 'maximumProjectionAgeMs', 24 * 60 * 60_000)
  const options = normalizeAccountListOptions(input.options)

  const projections = businessTable(client, 'account_list_availability_projections')
  const projectionTags = businessTable(client, 'account_list_availability_projection_tags')
  const projectionSearchTerms = businessTable(client, 'account_list_availability_projection_search_terms')
  const viewerHealth = businessTable(client, 'account_list_availability_projection_viewer_health')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const where: string[] = ['projections.viewer_system_account_id = ?']
  const params: unknown[] = [viewerSystemAccountId]
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
    const keywordClauses = ['(projections.name_sort_key >= ? AND projections.name_sort_key < ?)']
    const keywordParams: unknown[] = [normalizedKeyword, textPrefixUpperBound(normalizedKeyword)]
    const terms = accountNameSearchQueryTerms(options.keyword)
    if (terms.length) {
      const containsExpression = client.driver === 'postgres'
        ? 'projections.name_sort_key LIKE ? ESCAPE \'\\\''
        : 'instr(projections.name_sort_key, ?) > 0'
      keywordClauses.push(`(
        ${containsExpression}
        AND EXISTS (
          SELECT 1
          FROM ${projectionSearchTerms} projection_search
          WHERE projection_search.viewer_system_account_id = projections.viewer_system_account_id
            AND projection_search.account_id = projections.account_id
            AND projection_search.term IN (${client.dialect.bindPlaceholders(terms.length)})
          GROUP BY projection_search.account_id
          HAVING COUNT(DISTINCT projection_search.term) = ?
        )
      )`)
      keywordParams.push(client.driver === 'postgres'
        ? `%${escapeAccountNameSearchLike(normalizedKeyword)}%`
        : normalizedKeyword, ...terms, terms.length)
    }
    where.push(`(${keywordClauses.join(' OR ')})`)
    params.push(...keywordParams)
  }
  appendStatusProjectionFilters(where, params, options.status, options.schedulable, client)
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
  const projectionClockCte = usesDatabaseClock
    ? `projection_clock AS${materializedCte} (
      SELECT clock_timestamp() AS now_at
    ),`
    : ''
  const staleBefore = usesDatabaseClock
    ? `to_char(
      (SELECT now_at FROM projection_clock) - (? * interval '1 millisecond'),
      'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'
    )`
    : '?'
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
            OR health.oldest_projected_at < ${staleBefore}
            OR (health.next_transition_at IS NOT NULL AND health.next_transition_at <= ${projectionNow})
          )
        UNION ALL
        SELECT 1
        WHERE NOT EXISTS (
          SELECT 1
          FROM ${viewerHealth} missing_health
          WHERE missing_health.viewer_system_account_id = ?
        )`
    : `
        SELECT 1
        FROM ${dirty} dirty_accounts
        WHERE dirty_accounts.viewer_system_account_id = ?
        UNION ALL
        SELECT 1
        FROM ${projections} stale_projections
        WHERE stale_projections.viewer_system_account_id = ?
          AND stale_projections.projected_at < ${staleBefore}
        UNION ALL
        SELECT 1
        FROM ${projections} due_projections
        WHERE due_projections.viewer_system_account_id = ?
          AND due_projections.next_transition_at IS NOT NULL
          AND due_projections.next_transition_at <= ${projectionNow}`
  const rows = await client.query<AccountListAvailabilityProjectionRow>(`
    WITH ${projectionClockCte}
    projection_health AS${materializedCte} (
      SELECT EXISTS (
        ${projectionHealthChecks}
      ) AS unavailable,
      ${projectionNow} AS projection_now_iso
    ), page AS (
      SELECT projections.account_id, projections.payload_json, projections.projected_at
      FROM ${projections} projections
      WHERE ${where.join('\n        AND ')}
      ${projectionOrderClause(options.sorts)}
      LIMIT ? OFFSET ?
    )
    SELECT projection_health.unavailable AS projection_unavailable,
      projection_health.projection_now_iso,
      page.account_id, page.payload_json, page.projected_at
    FROM projection_health
    LEFT JOIN page ON projection_health.unavailable = ${client.driver === 'postgres' ? 'false' : '0'}
  `, [
    viewerSystemAccountId,
    ...(usesDatabaseClock
      ? [viewerSystemAccountId, maximumProjectionAgeMs, viewerSystemAccountId]
      : [
          viewerSystemAccountId,
          new Date(nowMs - maximumProjectionAgeMs).toISOString(),
          viewerSystemAccountId,
          new Date(nowMs).toISOString(),
          new Date(nowMs).toISOString()
        ]),
    ...params,
    limit,
    offset
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

export async function assertAccountListAvailabilityProjectionFreshInClient(
  client: DatabaseClient,
  input: { viewerSystemAccountId: string; maximumProjectionAgeMs: number; nowMs?: number }
): Promise<void> {
  const viewerSystemAccountId = requiredText(input.viewerSystemAccountId, 256, 'viewerSystemAccountId')
  const nowMs = nonNegativeInteger(input.nowMs ?? Date.now(), 'nowMs')
  const maximumProjectionAgeMs = positiveInteger(input.maximumProjectionAgeMs, 'maximumProjectionAgeMs', 24 * 60 * 60_000)
  const projections = businessTable(client, 'account_list_availability_projections')
  const dirty = businessTable(client, 'account_list_availability_dirty')
  const accounts = businessTable(client, 'accounts')
  const authorizations = businessTable(client, 'resource_authorizations')
  const staleBefore = new Date(nowMs - maximumProjectionAgeMs).toISOString()
  const now = new Date(nowMs).toISOString()
  const stale = await client.one<{ account_id: string }>(`
    SELECT accounts.id AS account_id
    FROM ${accounts} accounts
    LEFT JOIN ${authorizations} authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${projections} projections
      ON projections.viewer_system_account_id = accounts.system_account_id
     AND projections.account_id = accounts.id
    LEFT JOIN ${dirty} dirty_accounts ON dirty_accounts.account_id = accounts.id
    WHERE accounts.system_account_id = ?
      AND accounts.deleted_at IS NULL
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
      AND (
        projections.account_id IS NULL
        OR dirty_accounts.account_id IS NOT NULL
        OR projections.projected_at < ?
        OR (projections.next_transition_at IS NOT NULL AND projections.next_transition_at <= ?)
      )
    ORDER BY accounts.id ASC
    LIMIT 1
  `, [viewerSystemAccountId, staleBefore, now])
  if (stale) {
    throw new AccountListAvailabilityProjectionUnavailableError('账户列表投影正在更新，请稍后重试')
  }
}

function appendStatusProjectionFilters(
  where: string[],
  params: unknown[],
  rawStatus: string | undefined,
  schedulable: AccountListSchedulableFilter,
  client: Pick<DatabaseClient, 'dialect'>
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
  if (schedulable !== 'all') {
    where.push('projections.schedulable_bucket = ?')
    params.push(schedulable)
  }
}

function projectionOrderClause(sorts: AccountListSort[]): string {
  const columns: Record<AccountListSort['field'], string> = {
    priority: 'projections.priority_sort_key',
    superPriority: 'projections.super_priority_sort_key',
    fallback: 'projections.fallback_sort_key',
    name: 'projections.name_sort_key',
    type: 'projections.account_type',
    providerCode: 'projections.provider_code',
    systemAccount: 'projections.viewer_system_account_id',
    concurrency: 'projections.concurrency_sort_key',
    status: 'projections.effective_status',
    accountExpiresAt: 'projections.account_expires_at_sort_key',
    lastUsedAt: 'projections.last_used_at_sort_key'
  }
  const parts = sorts.map((sort) => `${columns[sort.field]} ${sort.order === 'desc' ? 'DESC' : 'ASC'}`)
  return `ORDER BY ${[...parts, 'projections.created_at_sort_key ASC', 'projections.account_id ASC'].join(', ')}`
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

function normalizeProjectionWrite(input: AccountListAvailabilityProjectionWrite): AccountListAvailabilityProjectionWrite & { projectedAt: string } {
  if (!isAccountStatus(input.effectiveStatus)) {
    throw new Error(`账户列表投影状态无效: ${input.effectiveStatus}`)
  }
  if (!isSchedulableBucket(input.schedulableBucket)) {
    throw new Error(`账户列表投影可调度桶无效: ${input.schedulableBucket}`)
  }
  if (!input.payload || typeof input.payload !== 'object') {
    throw new Error('账户列表投影 payload 缺失')
  }
  return {
    ...input,
    viewerSystemAccountId: requiredText(input.viewerSystemAccountId, 256, 'viewerSystemAccountId'),
    accountId: requiredText(input.accountId, 256, 'accountId'),
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
    searchTerms: normalizedSearchTerms(input.searchTerms),
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
