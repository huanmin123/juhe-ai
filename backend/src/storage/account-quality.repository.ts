import type { DatabaseSync } from 'node:sqlite'

import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, getStatsDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues } from './query-utils.js'
import { minuteKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'

export type AccountQualityState = 'fresh' | 'stale' | 'failed' | 'unknown'

export interface AccountQualityRealtimeRefreshResult {
  refreshed: number
  removed: number
  windowStartedAt: string
  windowEndedAt: string
}

export interface AccountQualityFailurePrecheckCandidate {
  accountId: string
  systemAccountId: string
  providerCode: string
  recentRequestCount: number
  recentSuccessCount: number
  recentErrorCount: number
  successRate?: number
  lastErrorAt?: string
  lastErrorMessage?: string
  updatedAt: string
}

export interface AccountQualityRow {
  account_id: string
  system_account_id: string
  provider_code: string
  quality_score: number
  quality_state: AccountQualityState
  recent_request_count: number
  recent_success_count: number
  recent_error_count: number
  recent_first_token_sample_count: number
  recent_avg_first_token_ms: number | null
  ewma_first_token_ms: number | null
  success_rate: number | null
  window_started_at: string
  window_ended_at: string
  last_sample_at: string | null
  last_success_at: string | null
  last_error_at: string | null
  last_error_message: string | null
  updated_at: string
}

interface AccountQualityAggregateRow {
  account_id: string
  recent_request_count: number
  recent_success_count: number
  recent_error_count: number
  recent_first_token_sample_count: number
  recent_avg_first_token_ms: number | null
  last_sample_at: string | null
  last_success_at: string | null
  last_error_at: string | null
  last_error_message: string | null
}

interface AccountQualityUpsertInput {
  accountId: string
  systemAccountId: string
  providerCode: string
  qualityScore: number
  qualityState: AccountQualityState
  recentRequestCount: number
  recentSuccessCount: number
  recentErrorCount: number
  recentFirstTokenSampleCount: number
  recentAvgFirstTokenMs?: number | null
  ewmaFirstTokenMs?: number | null
  successRate?: number | null
  windowStartedAt: string
  windowEndedAt: string
  lastSampleAt?: string
  lastSuccessAt?: string
  lastErrorAt?: string
  lastErrorMessage?: string
  updatedAt: string
}

const unknownQualityScore = 1_000_000
const failurePenaltyMs = 60_000
const stalePenaltyMs = 5_000
const qualityCleanupBatchLimit = 1000
const qualityLookupChunkSize = 500
const accountQualityDirtyAccountBatchLimit = 500
const qualityFailurePrecheckMinRequests = 5
const qualityFailurePrecheckMinErrors = 2
const qualityFailurePrecheckFrequentErrors = 5
const qualityFailurePrecheckMaxSuccessRate = 0.5

export function refreshAccountQualityFromUsage(windowMinutes = 10, dirtyAccountLimit = accountQualityDirtyAccountBatchLimit): AccountQualityRealtimeRefreshResult {
  const database = getStatsDatabase()
  const now = new Date()
  const timezone = usageStatsTimezone()
  const windowMs = Math.max(1, Math.min(Math.trunc(windowMinutes), 24 * 60)) * 60 * 1000
  const windowStartedAt = new Date(now.getTime() - windowMs).toISOString()
  const windowEndedAt = now.toISOString()
  const windowStartedMinute = minuteKey(new Date(now.getTime() - windowMs), timezone)
  const retentionCutoffMinute = minuteKey(new Date(now.getTime() - 24 * 60 * 60 * 1000), timezone)
  const updatedAt = nowIso()

  const dirtyAccountIds = loadDirtyAccountQualityIds(database, dirtyAccountLimit)
  const rows = loadAccountQualityAggregates(database, dirtyAccountIds, windowStartedMinute)
  const sampledAccountIds = uniqueAccountQualityIds(rows.map((row) => row.account_id))
  const activeAccounts = loadQualityAccountMetadataByIds(sampledAccountIds)
  const previousQualityByAccount = loadAccountQualityRowsByAccountIds(sampledAccountIds)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const upsertQuality = prepareAccountQualityUpsert(database)
    const deleteResult = cleanupInactiveQualityRows(database, qualityCleanupBatchLimit)
    cleanupInactiveQualityMinuteRows(database, qualityCleanupBatchLimit)
    cleanupOldQualityMinuteRows(database, retentionCutoffMinute, qualityCleanupBatchLimit)
    createTempRefreshedQualityAccounts(database, sampledAccountIds)
    for (const previous of loadStaleAccountQualityRows(database, qualityCleanupBatchLimit)) {
      markAccountQualityStale(upsertQuality, previous, windowStartedAt, windowEndedAt, updatedAt)
    }
    for (const row of rows) {
      const metadata = activeAccounts.get(row.account_id)
      if (!metadata) continue
      const previous = previousQualityByAccount.get(row.account_id)
      const recentAvg = integerOrNull(row.recent_avg_first_token_ms)
      const previousEwma = previous?.ewma_first_token_ms ?? null
      const ewmaFirstTokenMs = recentAvg === null
        ? previousEwma
        : previousEwma === null
          ? recentAvg
          : Math.round(previousEwma * 0.6 + recentAvg * 0.4)
      const successRate = row.recent_request_count > 0
        ? Math.max(0, Math.min(1, row.recent_success_count / row.recent_request_count))
        : previous?.success_rate ?? null
      const qualityState = row.recent_first_token_sample_count > 0 ? 'fresh' : 'unknown'
      const qualityScore = computeQualityScore({
        ewmaFirstTokenMs,
        successRate,
        qualityState,
        updatedAt
      })
      upsertAccountQuality(upsertQuality, {
        accountId: row.account_id,
        systemAccountId: metadata.systemAccountId,
        providerCode: metadata.providerCode,
        qualityScore,
        qualityState,
        recentRequestCount: row.recent_request_count,
        recentSuccessCount: row.recent_success_count,
        recentErrorCount: row.recent_error_count,
        recentFirstTokenSampleCount: row.recent_first_token_sample_count,
        recentAvgFirstTokenMs: recentAvg,
        ewmaFirstTokenMs,
        successRate,
        windowStartedAt,
        windowEndedAt,
        lastSampleAt: row.last_sample_at ?? previous?.last_sample_at ?? undefined,
        lastSuccessAt: row.last_success_at ?? previous?.last_success_at ?? undefined,
        lastErrorAt: row.last_error_at ?? previous?.last_error_at ?? undefined,
        lastErrorMessage: row.last_error_message ?? previous?.last_error_message ?? undefined,
        updatedAt
      })
    }
    deleteDirtyAccountQualityRows(database, dirtyAccountIds)

    commitDatabaseTransaction(database, transactionStarted)
    return { refreshed: rows.length, removed: Number(deleteResult.changes ?? 0), windowStartedAt, windowEndedAt }
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export async function refreshAccountQualityFromUsageAsync(windowMinutes = 10, dirtyAccountLimit = accountQualityDirtyAccountBatchLimit): Promise<AccountQualityRealtimeRefreshResult> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const now = new Date()
  const timezone = await usageStatsTimezoneAsync()
  const windowMs = Math.max(1, Math.min(Math.trunc(windowMinutes), 24 * 60)) * 60 * 1000
  const windowStartedAt = new Date(now.getTime() - windowMs).toISOString()
  const windowEndedAt = now.toISOString()
  const windowStartedMinute = minuteKey(new Date(now.getTime() - windowMs), timezone)
  const retentionCutoffMinute = minuteKey(new Date(now.getTime() - 24 * 60 * 60 * 1000), timezone)
  const updatedAt = nowIso()

  const dirtyAccountIds = await loadDirtyAccountQualityIdsAsync(client, dirtyAccountLimit)
  const rows = await loadAccountQualityAggregatesAsync(client, dirtyAccountIds, windowStartedMinute)
  const sampledAccountIds = uniqueAccountQualityIds(rows.map((row) => row.account_id))
  const activeAccounts = await loadQualityAccountMetadataByIdsAsync(client, sampledAccountIds)
  const previousQualityByAccount = await loadAccountQualityRowsByAccountIdsAsync(client, sampledAccountIds)
  const removed = await client.transaction(async (tx) => {
    const deleteResult = await cleanupInactiveQualityRowsAsync(tx, qualityCleanupBatchLimit)
    await cleanupInactiveQualityMinuteRowsAsync(tx, qualityCleanupBatchLimit)
    await cleanupOldQualityMinuteRowsAsync(tx, retentionCutoffMinute, qualityCleanupBatchLimit)
    for (const previous of await loadStaleAccountQualityRowsAsync(tx, sampledAccountIds, qualityCleanupBatchLimit)) {
      await markAccountQualityStaleAsync(tx, previous, windowStartedAt, windowEndedAt, updatedAt)
    }
    for (const row of rows) {
      const metadata = activeAccounts.get(row.account_id)
      if (!metadata) continue
      const previous = previousQualityByAccount.get(row.account_id)
      const recentAvg = integerOrNull(row.recent_avg_first_token_ms)
      const previousEwma = previous?.ewma_first_token_ms ?? null
      const ewmaFirstTokenMs = recentAvg === null
        ? previousEwma
        : previousEwma === null
          ? recentAvg
          : Math.round(previousEwma * 0.6 + recentAvg * 0.4)
      const successRate = row.recent_request_count > 0
        ? Math.max(0, Math.min(1, row.recent_success_count / row.recent_request_count))
        : previous?.success_rate ?? null
      const qualityState = row.recent_first_token_sample_count > 0 ? 'fresh' : 'unknown'
      const qualityScore = computeQualityScore({ ewmaFirstTokenMs, successRate, qualityState, updatedAt })
      await upsertAccountQualityAsync(tx, {
        accountId: row.account_id,
        systemAccountId: metadata.systemAccountId,
        providerCode: metadata.providerCode,
        qualityScore,
        qualityState,
        recentRequestCount: row.recent_request_count,
        recentSuccessCount: row.recent_success_count,
        recentErrorCount: row.recent_error_count,
        recentFirstTokenSampleCount: row.recent_first_token_sample_count,
        recentAvgFirstTokenMs: recentAvg,
        ewmaFirstTokenMs,
        successRate,
        windowStartedAt,
        windowEndedAt,
        lastSampleAt: row.last_sample_at ?? previous?.last_sample_at ?? undefined,
        lastSuccessAt: row.last_success_at ?? previous?.last_success_at ?? undefined,
        lastErrorAt: row.last_error_at ?? previous?.last_error_at ?? undefined,
        lastErrorMessage: row.last_error_message ?? previous?.last_error_message ?? undefined,
        updatedAt
      })
    }
    await deleteDirtyAccountQualityRowsAsync(tx, dirtyAccountIds)
    return deleteResult
  })
  return { refreshed: rows.length, removed, windowStartedAt, windowEndedAt }
}

export function listAccountQualityFailurePrecheckCandidates(limit = 20): AccountQualityFailurePrecheckCandidate[] {
  const normalizedLimit = Math.max(1, Math.min(Math.trunc(limit), 100))
  const rows = getStatsDatabase()
    .prepare(`
      SELECT
        account_id,
        system_account_id,
        provider_code,
        recent_request_count,
        recent_success_count,
        recent_error_count,
        success_rate,
        last_error_at,
        last_error_message,
        updated_at
      FROM account_quality_scores INDEXED BY idx_account_quality_scores_failure_precheck
      WHERE recent_request_count >= ${qualityFailurePrecheckMinRequests}
        AND recent_error_count >= ${qualityFailurePrecheckMinErrors}
        AND (
          recent_error_count >= ${qualityFailurePrecheckFrequentErrors}
          OR (success_rate IS NOT NULL AND success_rate <= ${qualityFailurePrecheckMaxSuccessRate})
        )
      ORDER BY recent_error_count DESC,
        COALESCE(success_rate, 1) ASC,
        updated_at DESC,
        account_id ASC
      LIMIT ?
    `)
    .all(normalizedLimit) as unknown as Array<{
      account_id?: string | null
      system_account_id?: string | null
      provider_code?: string | null
      recent_request_count?: number | null
      recent_success_count?: number | null
      recent_error_count?: number | null
      success_rate?: number | null
      last_error_at?: string | null
      last_error_message?: string | null
      updated_at?: string | null
    }>
  return rows
    .map((row): AccountQualityFailurePrecheckCandidate | undefined => {
      const accountId = row.account_id?.trim()
      const systemAccountId = row.system_account_id?.trim()
      const providerCode = row.provider_code?.trim()
      const updatedAt = row.updated_at?.trim()
      if (!accountId || !systemAccountId || !providerCode || !updatedAt) {
        return undefined
      }
      const successRate = nullableRate(row.success_rate)
      const candidate: AccountQualityFailurePrecheckCandidate = {
        accountId,
        systemAccountId,
        providerCode,
        recentRequestCount: Math.max(0, Math.trunc(Number(row.recent_request_count ?? 0))),
        recentSuccessCount: Math.max(0, Math.trunc(Number(row.recent_success_count ?? 0))),
        recentErrorCount: Math.max(0, Math.trunc(Number(row.recent_error_count ?? 0))),
        lastErrorAt: row.last_error_at?.trim() || undefined,
        lastErrorMessage: row.last_error_message?.trim() || undefined,
        updatedAt
      }
      if (successRate !== null) {
        candidate.successRate = successRate
      }
      return candidate
    })
    .filter((row): row is AccountQualityFailurePrecheckCandidate => Boolean(row))
}

export async function listAccountQualityFailurePrecheckCandidatesAsync(limit = 20): Promise<AccountQualityFailurePrecheckCandidate[]> {
  const normalizedLimit = Math.max(1, Math.min(Math.trunc(limit), 100))
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await client.query<{
    account_id?: string | null
    system_account_id?: string | null
    provider_code?: string | null
    recent_request_count?: number | string | null
    recent_success_count?: number | string | null
    recent_error_count?: number | string | null
    success_rate?: number | string | null
    last_error_at?: string | null
    last_error_message?: string | null
    updated_at?: string | null
  }>(`
    SELECT
      account_id,
      system_account_id,
      provider_code,
      recent_request_count,
      recent_success_count,
      recent_error_count,
      success_rate,
      last_error_at,
      last_error_message,
      updated_at
    FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_scores')}
    WHERE recent_request_count >= ${qualityFailurePrecheckMinRequests}
      AND recent_error_count >= ${qualityFailurePrecheckMinErrors}
      AND (
        recent_error_count >= ${qualityFailurePrecheckFrequentErrors}
        OR (success_rate IS NOT NULL AND success_rate <= ${qualityFailurePrecheckMaxSuccessRate})
      )
    ORDER BY recent_error_count DESC,
      COALESCE(success_rate, 1) ASC,
      updated_at DESC,
      account_id ASC
    LIMIT ?
  `, [normalizedLimit])
  return rows
    .map((row): AccountQualityFailurePrecheckCandidate | undefined => {
      const accountId = row.account_id?.trim()
      const systemAccountId = row.system_account_id?.trim()
      const providerCode = row.provider_code?.trim()
      const updatedAt = row.updated_at?.trim()
      if (!accountId || !systemAccountId || !providerCode || !updatedAt) return undefined
      const successRate = nullableRate(row.success_rate)
      const candidate: AccountQualityFailurePrecheckCandidate = {
        accountId,
        systemAccountId,
        providerCode,
        recentRequestCount: Math.max(0, Math.trunc(Number(row.recent_request_count ?? 0))),
        recentSuccessCount: Math.max(0, Math.trunc(Number(row.recent_success_count ?? 0))),
        recentErrorCount: Math.max(0, Math.trunc(Number(row.recent_error_count ?? 0))),
        lastErrorAt: row.last_error_at?.trim() || undefined,
        lastErrorMessage: row.last_error_message?.trim() || undefined,
        updatedAt
      }
      if (successRate !== null) candidate.successRate = successRate
      return candidate
    })
    .filter((row): row is AccountQualityFailurePrecheckCandidate => Boolean(row))
}

function loadDirtyAccountQualityIds(database: ReturnType<typeof getStatsDatabase>, limit: number): string[] {
  const rows = database
    .prepare(`
      SELECT account_id
      FROM account_quality_dirty_accounts INDEXED BY idx_account_quality_dirty_accounts_updated
      ORDER BY updated_at ASC, account_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as unknown as Array<{ account_id?: string | null }>
  return uniqueAccountQualityIds(rows.map((row) => row.account_id))
}

function loadAccountQualityAggregates(
  database: ReturnType<typeof getStatsDatabase>,
  accountIds: string[],
  windowStartedMinute: string
): AccountQualityAggregateRow[] {
  const rows: AccountQualityAggregateRow[] = []
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    rows.push(...database
      .prepare(`
        SELECT
          quality_stats.account_id,
          SUM(quality_stats.request_count) AS recent_request_count,
          SUM(quality_stats.success_count) AS recent_success_count,
          SUM(quality_stats.error_count) AS recent_error_count,
          SUM(quality_stats.first_token_ms_count) AS recent_first_token_sample_count,
          CASE
            WHEN SUM(quality_stats.first_token_ms_count) > 0
            THEN SUM(quality_stats.first_token_ms_sum) * 1.0 / SUM(quality_stats.first_token_ms_count)
            ELSE NULL
          END AS recent_avg_first_token_ms,
          MAX(quality_stats.last_sample_at) AS last_sample_at,
          MAX(quality_stats.last_success_at) AS last_success_at,
          MAX(quality_stats.last_error_at) AS last_error_at,
          (
            SELECT latest_error.last_error_message
            FROM account_quality_minute_stats latest_error
            WHERE latest_error.account_id = quality_stats.account_id
              AND latest_error.stat_minute >= ?
              AND latest_error.last_error_at IS NOT NULL
            ORDER BY latest_error.last_error_at DESC, latest_error.stat_minute DESC
            LIMIT 1
          ) AS last_error_message
        FROM account_quality_minute_stats quality_stats
        WHERE quality_stats.account_id IN (${chunk.map(() => '?').join(',')})
          AND quality_stats.stat_minute >= ?
        GROUP BY quality_stats.account_id
        ORDER BY quality_stats.account_id ASC
      `)
      .all(windowStartedMinute, ...chunk, windowStartedMinute) as unknown as AccountQualityAggregateRow[])
  }
  return rows
}

async function loadDirtyAccountQualityIdsAsync(client: DatabaseClient, limit: number): Promise<string[]> {
  const rows = await client.query<{ account_id?: string | null }>(`
    SELECT account_id
    FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_dirty_accounts')}
    ORDER BY updated_at ASC, account_id ASC
    LIMIT ?
  `, [Math.max(1, Math.trunc(limit))])
  return uniqueAccountQualityIds(rows.map((row) => row.account_id))
}

async function loadAccountQualityAggregatesAsync(
  client: DatabaseClient,
  accountIds: string[],
  windowStartedMinute: string
): Promise<AccountQualityAggregateRow[]> {
  const rows: AccountQualityAggregateRow[] = []
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    rows.push(...await client.query<AccountQualityAggregateRow>(`
      SELECT
        quality_stats.account_id,
        SUM(quality_stats.request_count) AS recent_request_count,
        SUM(quality_stats.success_count) AS recent_success_count,
        SUM(quality_stats.error_count) AS recent_error_count,
        SUM(quality_stats.first_token_ms_count) AS recent_first_token_sample_count,
        CASE
          WHEN SUM(quality_stats.first_token_ms_count) > 0
          THEN SUM(quality_stats.first_token_ms_sum)::double precision / SUM(quality_stats.first_token_ms_count)
          ELSE NULL
        END AS recent_avg_first_token_ms,
        MAX(quality_stats.last_sample_at) AS last_sample_at,
        MAX(quality_stats.last_success_at) AS last_success_at,
        MAX(quality_stats.last_error_at) AS last_error_at,
        (
          SELECT latest_error.last_error_message
          FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_minute_stats')} latest_error
          WHERE latest_error.account_id = quality_stats.account_id
            AND latest_error.stat_minute >= ?
            AND latest_error.last_error_at IS NOT NULL
          ORDER BY latest_error.last_error_at DESC, latest_error.stat_minute DESC
          LIMIT 1
        ) AS last_error_message
      FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_minute_stats')} quality_stats
      WHERE quality_stats.account_id IN (${chunk.map(() => '?').join(', ')})
        AND quality_stats.stat_minute >= ?
      GROUP BY quality_stats.account_id
      ORDER BY quality_stats.account_id ASC
    `, [windowStartedMinute, ...chunk, windowStartedMinute]))
  }
  return rows.map((row) => ({
    ...row,
    recent_request_count: Number(row.recent_request_count ?? 0),
    recent_success_count: Number(row.recent_success_count ?? 0),
    recent_error_count: Number(row.recent_error_count ?? 0),
    recent_first_token_sample_count: Number(row.recent_first_token_sample_count ?? 0),
    recent_avg_first_token_ms: row.recent_avg_first_token_ms === null ? null : Number(row.recent_avg_first_token_ms)
  }))
}

function deleteDirtyAccountQualityRows(database: ReturnType<typeof getStatsDatabase>, accountIds: string[]): void {
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    database
      .prepare(`DELETE FROM account_quality_dirty_accounts WHERE account_id IN (${chunk.map(() => '?').join(',')})`)
      .run(...chunk)
  }
}

async function deleteDirtyAccountQualityRowsAsync(client: DatabaseClient, accountIds: string[]): Promise<void> {
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    await client.execute(`
      DELETE FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_dirty_accounts')}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
    `, chunk)
  }
}

function loadQualityAccountMetadataByIds(accountIds: string[]): Map<string, { systemAccountId: string; providerCode: string }> {
  if (accountIds.length === 0) return new Map()
  const rows: Array<{ id: string; system_account_id: string; provider_code: string }> = []
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    rows.push(...database
      .prepare(`
        SELECT id, system_account_id, provider_code
        FROM accounts
        WHERE id IN (${chunk.map(() => '?').join(',')})
      `)
      .all(...chunk) as unknown as Array<{ id: string; system_account_id: string; provider_code: string }>)
  }
  return new Map(rows.map((row) => [row.id, { systemAccountId: row.system_account_id, providerCode: row.provider_code }]))
}

async function loadQualityAccountMetadataByIdsAsync(client: DatabaseClient, accountIds: string[]): Promise<Map<string, { systemAccountId: string; providerCode: string }>> {
  if (!accountIds.length) return new Map()
  const rows: Array<{ id: string; system_account_id: string; provider_code: string }> = []
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    rows.push(...await client.query<Array<{ id: string; system_account_id: string; provider_code: string }>[number]>(`
      SELECT id, system_account_id, provider_code
      FROM ${accountQualityTable(client, 'juhe_business', 'accounts')}
      WHERE id IN (${chunk.map(() => '?').join(', ')})
    `, chunk))
  }
  return new Map(rows.map((row) => [row.id, { systemAccountId: row.system_account_id, providerCode: row.provider_code }]))
}

export function loadAccountQualityRowsByAccountIds(accountIds: string[]): Map<string, AccountQualityRow> {
  if (accountIds.length === 0) return new Map()
  const rows: AccountQualityRow[] = []
  const database = getStatsDatabase()
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    rows.push(...database
      .prepare(`
        SELECT ${accountQualitySelectColumns()}
        FROM account_quality_scores
        WHERE account_id IN (${chunk.map(() => '?').join(',')})
      `)
      .all(...chunk) as unknown as AccountQualityRow[])
  }
  return new Map(rows.map((row) => [row.account_id, row]))
}

export async function loadAccountQualityRowsByAccountIdsAsync(client: DatabaseClient, accountIds: string[]): Promise<Map<string, AccountQualityRow>> {
  if (!accountIds.length) return new Map()
  const rows: AccountQualityRow[] = []
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    rows.push(...await client.query<AccountQualityRow>(`
      SELECT ${accountQualitySelectColumns()}
      FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_scores')}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
    `, chunk))
  }
  return new Map(rows.map((row) => [row.account_id, normalizeAccountQualityRow(row)]))
}

function accountQualitySelectColumns(): string {
  return [
    'account_id',
    'system_account_id',
    'provider_code',
    'quality_score',
    'quality_state',
    'recent_request_count',
    'recent_success_count',
    'recent_error_count',
    'recent_first_token_sample_count',
    'recent_avg_first_token_ms',
    'ewma_first_token_ms',
    'success_rate',
    'window_started_at',
    'window_ended_at',
    'last_sample_at',
    'last_success_at',
    'last_error_at',
    'last_error_message',
    'updated_at'
  ].join(', ')
}

function cleanupInactiveQualityRows(database: ReturnType<typeof getStatsDatabase>, limit: number): { changes?: number | bigint } {
  const candidateRows = database
    .prepare(`
      SELECT account_id
      FROM account_quality_scores
      ORDER BY updated_at ASC, account_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as unknown as Array<{ account_id?: string | null }>
  const candidateIds = uniqueAccountQualityIds(candidateRows.map((row) => row.account_id))
  if (candidateIds.length === 0) {
    return { changes: 0 }
  }
  const activeIds = new Set(loadQualityAccountMetadataByIds(candidateIds).keys())
  const inactiveIds = candidateIds.filter((accountId) => !activeIds.has(accountId))
  if (inactiveIds.length === 0) {
    return { changes: 0 }
  }
  let changes = 0
  for (const chunk of chunkValues(inactiveIds, qualityLookupChunkSize)) {
    const result = database
      .prepare(`DELETE FROM account_quality_scores WHERE account_id IN (${chunk.map(() => '?').join(',')})`)
      .run(...chunk)
    changes += Number(result.changes ?? 0)
  }
  return { changes }
}

async function cleanupInactiveQualityRowsAsync(client: DatabaseClient, limit: number): Promise<number> {
  const candidateRows = await client.query<{ account_id?: string | null }>(`
    SELECT account_id
    FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_scores')}
    ORDER BY updated_at ASC, account_id ASC
    LIMIT ?
  `, [Math.max(1, Math.trunc(limit))])
  const candidateIds = uniqueAccountQualityIds(candidateRows.map((row) => row.account_id))
  if (candidateIds.length === 0) return 0

  const activeIds = new Set((await loadQualityAccountMetadataByIdsAsync(client, candidateIds)).keys())
  const inactiveIds = candidateIds.filter((accountId) => !activeIds.has(accountId))
  let changes = 0
  for (const chunk of chunkValues(inactiveIds, qualityLookupChunkSize)) {
    const result = await client.execute(`
      DELETE FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_scores')}
      WHERE account_id IN (${chunk.map(() => '?').join(', ')})
    `, chunk)
    changes += result.changes
  }
  return changes
}

function cleanupInactiveQualityMinuteRows(database: ReturnType<typeof getStatsDatabase>, limit: number): { changes?: number | bigint } {
  const candidateRows = database
    .prepare(`
      SELECT account_id
      FROM account_quality_minute_stats
      ORDER BY stat_minute ASC, rowid ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as unknown as Array<{ account_id?: string | null }>
  const candidateIds = uniqueAccountQualityIds(candidateRows.map((row) => row.account_id))
  if (candidateIds.length === 0) {
    return { changes: 0 }
  }
  const activeIds = new Set(loadQualityAccountMetadataByIds(candidateIds).keys())
  const inactiveIds = candidateIds.filter((accountId) => !activeIds.has(accountId))
  if (inactiveIds.length === 0) {
    return { changes: 0 }
  }
  return database.prepare(`
    DELETE FROM account_quality_minute_stats
    WHERE rowid IN (
      SELECT rowid
      FROM account_quality_minute_stats
      WHERE account_id IN (${inactiveIds.map(() => '?').join(',')})
      ORDER BY stat_minute ASC, rowid ASC
      LIMIT ?
    )
  `).run(...inactiveIds, Math.max(1, Math.trunc(limit)))
}

async function cleanupInactiveQualityMinuteRowsAsync(client: DatabaseClient, limit: number): Promise<number> {
  const normalizedLimit = Math.max(1, Math.trunc(limit))
  const minuteStatsTable = accountQualityTable(client, 'juhe_stats', 'account_quality_minute_stats')
  const candidateRows = await client.query<{ account_id?: string | null }>(`
    SELECT account_id
    FROM ${minuteStatsTable}
    ORDER BY stat_minute ASC, ctid ASC
    LIMIT ?
  `, [normalizedLimit])
  const candidateIds = uniqueAccountQualityIds(candidateRows.map((row) => row.account_id))
  if (candidateIds.length === 0) return 0

  const activeIds = new Set((await loadQualityAccountMetadataByIdsAsync(client, candidateIds)).keys())
  const inactiveIds = candidateIds.filter((accountId) => !activeIds.has(accountId))
  if (inactiveIds.length === 0) return 0

  const result = await client.execute(`
    DELETE FROM ${minuteStatsTable}
    WHERE ctid IN (
      SELECT ctid
      FROM ${minuteStatsTable}
      WHERE account_id IN (${inactiveIds.map(() => '?').join(', ')})
      ORDER BY stat_minute ASC, ctid ASC
      LIMIT ?
    )
  `, [...inactiveIds, normalizedLimit])
  return result.changes
}

function cleanupOldQualityMinuteRows(database: ReturnType<typeof getStatsDatabase>, cutoffMinute: string, limit: number): { changes?: number | bigint } {
  return database.prepare(`
    DELETE FROM account_quality_minute_stats
    WHERE rowid IN (
      SELECT rowid
      FROM account_quality_minute_stats
      WHERE stat_minute < ?
      ORDER BY stat_minute ASC, rowid ASC
      LIMIT ?
    )
  `).run(cutoffMinute, Math.max(1, Math.trunc(limit)))
}

async function cleanupOldQualityMinuteRowsAsync(client: DatabaseClient, cutoffMinute: string, limit: number): Promise<number> {
  const minuteStatsTable = accountQualityTable(client, 'juhe_stats', 'account_quality_minute_stats')
  const result = await client.execute(`
    DELETE FROM ${minuteStatsTable}
    WHERE ctid IN (
      SELECT ctid
      FROM ${minuteStatsTable}
      WHERE stat_minute < ?
      ORDER BY stat_minute ASC, ctid ASC
      LIMIT ?
    )
  `, [cutoffMinute, Math.max(1, Math.trunc(limit))])
  return result.changes
}

function createTempRefreshedQualityAccounts(database: ReturnType<typeof getStatsDatabase>, accountIds: string[]): void {
  database.prepare('DROP TABLE IF EXISTS temp_refreshed_quality_accounts').run()
  database.prepare('CREATE TEMP TABLE temp_refreshed_quality_accounts (id TEXT PRIMARY KEY)').run()
  for (const chunk of chunkValues(accountIds, qualityLookupChunkSize)) {
    database
      .prepare(`INSERT INTO temp_refreshed_quality_accounts (id) VALUES ${chunk.map(() => '(?)').join(',')}`)
      .run(...chunk)
  }
}

function loadStaleAccountQualityRows(database: ReturnType<typeof getStatsDatabase>, limit: number): AccountQualityRow[] {
  return database
    .prepare(`
      SELECT ${accountQualitySelectColumns()}
      FROM account_quality_scores
      WHERE quality_state IN ('fresh', 'failed')
        AND NOT EXISTS (
          SELECT 1
          FROM temp_refreshed_quality_accounts refreshed_accounts
          WHERE refreshed_accounts.id = account_quality_scores.account_id
        )
      ORDER BY updated_at ASC, account_id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as unknown as AccountQualityRow[]
}

async function loadStaleAccountQualityRowsAsync(client: DatabaseClient, refreshedAccountIds: string[], limit: number): Promise<AccountQualityRow[]> {
  const excludeClause = refreshedAccountIds.length > 0
    ? 'AND NOT (account_id = ANY(?::text[]))'
    : ''
  const params: unknown[] = refreshedAccountIds.length > 0
    ? [refreshedAccountIds, Math.max(1, Math.trunc(limit))]
    : [Math.max(1, Math.trunc(limit))]
  const rows = await client.query<AccountQualityRow>(`
    SELECT ${accountQualitySelectColumns()}
    FROM ${accountQualityTable(client, 'juhe_stats', 'account_quality_scores')}
    WHERE quality_state IN ('fresh', 'failed')
      ${excludeClause}
    ORDER BY updated_at ASC, account_id ASC
    LIMIT ?
  `, params)
  return rows.map(normalizeAccountQualityRow)
}

function uniqueAccountQualityIds(values: Array<string | null | undefined>): string[] {
  return [...new Set(values.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
}

function markAccountQualityStale(upsertQuality: ReturnType<DatabaseSync['prepare']>, previous: AccountQualityRow, windowStartedAt: string, windowEndedAt: string, updatedAt: string): void {
  const qualityState: AccountQualityState = previous.quality_state === 'fresh'
    ? 'stale'
    : previous.quality_state === 'failed'
      ? 'unknown'
      : previous.quality_state
  const qualityScore = computeQualityScore({
    ewmaFirstTokenMs: previous.ewma_first_token_ms,
    successRate: previous.success_rate,
    qualityState,
    updatedAt
  })
  upsertAccountQuality(upsertQuality, {
    accountId: previous.account_id,
    systemAccountId: previous.system_account_id,
    providerCode: previous.provider_code,
    qualityScore,
    qualityState,
    recentRequestCount: 0,
    recentSuccessCount: 0,
    recentErrorCount: 0,
    recentFirstTokenSampleCount: 0,
    recentAvgFirstTokenMs: undefined,
    ewmaFirstTokenMs: previous.ewma_first_token_ms ?? undefined,
    successRate: previous.success_rate ?? undefined,
    windowStartedAt,
    windowEndedAt,
    lastSampleAt: previous.last_sample_at ?? undefined,
    lastSuccessAt: previous.last_success_at ?? undefined,
    lastErrorAt: previous.last_error_at ?? undefined,
    lastErrorMessage: previous.last_error_message ?? undefined,
    updatedAt
  })
}

async function markAccountQualityStaleAsync(client: DatabaseClient, previous: AccountQualityRow, windowStartedAt: string, windowEndedAt: string, updatedAt: string): Promise<void> {
  const qualityState: AccountQualityState = previous.quality_state === 'fresh'
    ? 'stale'
    : previous.quality_state === 'failed'
      ? 'unknown'
      : previous.quality_state
  const qualityScore = computeQualityScore({
    ewmaFirstTokenMs: previous.ewma_first_token_ms,
    successRate: previous.success_rate,
    qualityState,
    updatedAt
  })
  await upsertAccountQualityAsync(client, {
    accountId: previous.account_id,
    systemAccountId: previous.system_account_id,
    providerCode: previous.provider_code,
    qualityScore,
    qualityState,
    recentRequestCount: 0,
    recentSuccessCount: 0,
    recentErrorCount: 0,
    recentFirstTokenSampleCount: 0,
    recentAvgFirstTokenMs: undefined,
    ewmaFirstTokenMs: previous.ewma_first_token_ms ?? undefined,
    successRate: previous.success_rate ?? undefined,
    windowStartedAt,
    windowEndedAt,
    lastSampleAt: previous.last_sample_at ?? undefined,
    lastSuccessAt: previous.last_success_at ?? undefined,
    lastErrorAt: previous.last_error_at ?? undefined,
    lastErrorMessage: previous.last_error_message ?? undefined,
    updatedAt
  })
}

function prepareAccountQualityUpsert(database: ReturnType<typeof getStatsDatabase>): ReturnType<DatabaseSync['prepare']> {
  return database.prepare(`
    INSERT INTO account_quality_scores (
      account_id, system_account_id, provider_code, quality_score, quality_state,
      recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
      recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
      window_started_at, window_ended_at, last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      system_account_id = excluded.system_account_id,
      provider_code = excluded.provider_code,
      quality_score = excluded.quality_score,
      quality_state = excluded.quality_state,
      recent_request_count = excluded.recent_request_count,
      recent_success_count = excluded.recent_success_count,
      recent_error_count = excluded.recent_error_count,
      recent_first_token_sample_count = excluded.recent_first_token_sample_count,
      recent_avg_first_token_ms = excluded.recent_avg_first_token_ms,
      ewma_first_token_ms = excluded.ewma_first_token_ms,
      success_rate = excluded.success_rate,
      window_started_at = excluded.window_started_at,
      window_ended_at = excluded.window_ended_at,
      last_sample_at = excluded.last_sample_at,
      last_success_at = excluded.last_success_at,
      last_error_at = excluded.last_error_at,
      last_error_message = excluded.last_error_message,
      updated_at = excluded.updated_at
  `)
}

function upsertAccountQuality(upsertQuality: ReturnType<DatabaseSync['prepare']>, input: AccountQualityUpsertInput): void {
  upsertQuality.run(
      input.accountId,
      input.systemAccountId,
      input.providerCode,
      input.qualityScore,
      input.qualityState,
      Math.max(0, Math.trunc(input.recentRequestCount)),
      Math.max(0, Math.trunc(input.recentSuccessCount)),
      Math.max(0, Math.trunc(input.recentErrorCount)),
      Math.max(0, Math.trunc(input.recentFirstTokenSampleCount)),
      nullableInteger(input.recentAvgFirstTokenMs),
      nullableInteger(input.ewmaFirstTokenMs),
      nullableRate(input.successRate),
      input.windowStartedAt,
      input.windowEndedAt,
      input.lastSampleAt ?? null,
      input.lastSuccessAt ?? null,
      input.lastErrorAt ?? null,
      input.lastErrorMessage ?? null,
      input.updatedAt
    )
}

async function upsertAccountQualityAsync(client: DatabaseClient, input: AccountQualityUpsertInput): Promise<void> {
  await client.execute(`
    INSERT INTO ${accountQualityTable(client, 'juhe_stats', 'account_quality_scores')} (
      account_id, system_account_id, provider_code, quality_score, quality_state,
      recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
      recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
      window_started_at, window_ended_at, last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(account_id) DO UPDATE SET
      system_account_id = EXCLUDED.system_account_id,
      provider_code = EXCLUDED.provider_code,
      quality_score = EXCLUDED.quality_score,
      quality_state = EXCLUDED.quality_state,
      recent_request_count = EXCLUDED.recent_request_count,
      recent_success_count = EXCLUDED.recent_success_count,
      recent_error_count = EXCLUDED.recent_error_count,
      recent_first_token_sample_count = EXCLUDED.recent_first_token_sample_count,
      recent_avg_first_token_ms = EXCLUDED.recent_avg_first_token_ms,
      ewma_first_token_ms = EXCLUDED.ewma_first_token_ms,
      success_rate = EXCLUDED.success_rate,
      window_started_at = EXCLUDED.window_started_at,
      window_ended_at = EXCLUDED.window_ended_at,
      last_sample_at = EXCLUDED.last_sample_at,
      last_success_at = EXCLUDED.last_success_at,
      last_error_at = EXCLUDED.last_error_at,
      last_error_message = EXCLUDED.last_error_message,
      updated_at = EXCLUDED.updated_at
  `, [
    input.accountId,
    input.systemAccountId,
    input.providerCode,
    Math.max(0, Math.trunc(input.qualityScore)),
    input.qualityState,
    Math.max(0, Math.trunc(input.recentRequestCount)),
    Math.max(0, Math.trunc(input.recentSuccessCount)),
    Math.max(0, Math.trunc(input.recentErrorCount)),
    Math.max(0, Math.trunc(input.recentFirstTokenSampleCount)),
    nullableInteger(input.recentAvgFirstTokenMs),
    nullableInteger(input.ewmaFirstTokenMs),
    nullableRate(input.successRate),
    input.windowStartedAt,
    input.windowEndedAt,
    input.lastSampleAt ?? null,
    input.lastSuccessAt ?? null,
    input.lastErrorAt ?? null,
    input.lastErrorMessage ?? null,
    input.updatedAt
  ])
}

function computeQualityScore(input: {
  ewmaFirstTokenMs?: number | null
  successRate?: number | null
  qualityState: AccountQualityState
  updatedAt: string
}): number {
  const latency = typeof input.ewmaFirstTokenMs === 'number' ? input.ewmaFirstTokenMs : unknownQualityScore
  const statePenalty = input.qualityState === 'failed'
    ? failurePenaltyMs
    : input.qualityState === 'stale'
      ? stalePenaltyMs
      : input.qualityState === 'unknown'
        ? 10_000
        : 0
  const agePenalty = agePenaltyMs(input.updatedAt)
  return Math.max(0, Math.trunc(latency + statePenalty + agePenalty))
}

function agePenaltyMs(updatedAt: string): number {
  const updatedTime = Date.parse(updatedAt)
  if (!Number.isFinite(updatedTime)) {
    return stalePenaltyMs
  }
  const ageMinutes = Math.max(0, Math.floor((Date.now() - updatedTime) / 60_000))
  return Math.min(10_000, ageMinutes * 100)
}

function integerOrNull(value: unknown): number | null {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.max(0, Math.round(number)) : null
}

function nullableInteger(value: unknown): number | null {
  return integerOrNull(value)
}

function nullableRate(value: unknown): number | null {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? Math.max(0, Math.min(1, number)) : null
}

function normalizeAccountQualityRow(row: AccountQualityRow): AccountQualityRow {
  return {
    ...row,
    quality_score: integerOrNull(row.quality_score) ?? unknownQualityScore,
    quality_state: normalizeAccountQualityState(row.quality_state),
    recent_request_count: integerOrNull(row.recent_request_count) ?? 0,
    recent_success_count: integerOrNull(row.recent_success_count) ?? 0,
    recent_error_count: integerOrNull(row.recent_error_count) ?? 0,
    recent_first_token_sample_count: integerOrNull(row.recent_first_token_sample_count) ?? 0,
    recent_avg_first_token_ms: integerOrNull(row.recent_avg_first_token_ms),
    ewma_first_token_ms: integerOrNull(row.ewma_first_token_ms),
    success_rate: nullableRate(row.success_rate)
  }
}

function normalizeAccountQualityState(value: unknown): AccountQualityState {
  if (value === 'fresh' || value === 'stale' || value === 'failed' || value === 'unknown') {
    return value
  }
  return 'unknown'
}

function accountQualityTable(client: DatabaseClient, schemaName: 'juhe_business' | 'juhe_stats', tableName: string): string {
  return client.dialect.qualifyTable(schemaName, tableName)
}
