import type { DatabaseSync } from 'node:sqlite'

import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, getRecordDatabase, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues } from './query-utils.js'
import { minuteKey, usageStatsTimezone } from './usage-stats-helpers.js'

export type AccountQualityState = 'fresh' | 'stale' | 'failed' | 'unknown'

export interface AccountQualityRealtimeRefreshResult {
  refreshed: number
  removed: number
  windowStartedAt: string
  windowEndedAt: string
}

interface AccountQualityRow {
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

const unknownQualityScore = 1_000_000
const failurePenaltyMs = 60_000
const stalePenaltyMs = 5_000

export function refreshAccountQualityFromUsage(windowMinutes = 10): AccountQualityRealtimeRefreshResult {
  const database = getRecordDatabase()
  const now = new Date()
  const timezone = usageStatsTimezone()
  const windowMs = Math.max(1, Math.min(Math.trunc(windowMinutes), 24 * 60)) * 60 * 1000
  const windowStartedAt = new Date(now.getTime() - windowMs).toISOString()
  const windowEndedAt = now.toISOString()
  const windowStartedMinute = minuteKey(new Date(now.getTime() - windowMs), timezone)
  const retentionCutoffMinute = minuteKey(new Date(now.getTime() - 24 * 60 * 60 * 1000), timezone)
  const updatedAt = nowIso()
  const activeAccounts = loadQualityAccountMetadata()
  const previousQualityByAccount = loadAccountQualityRows()

  const rows = database
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
      WHERE quality_stats.stat_minute >= ?
      GROUP BY quality_stats.account_id
    `)
    .all(windowStartedMinute, windowStartedMinute) as unknown as Array<{
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
    }>

  const activeAccountIds = new Set(activeAccounts.keys())
  const refreshedAccountIds = new Set<string>()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const upsertQuality = prepareAccountQualityUpsert(database)
    const activeIds = [...activeAccountIds]
    const deleteResult = activeIds.length > 0
      ? cleanupInactiveQualityRows(database, activeIds)
      : database.prepare('DELETE FROM account_quality_scores').run()
    if (activeIds.length > 0) {
      database.prepare(`
        DELETE FROM account_quality_minute_stats
        WHERE NOT EXISTS (
          SELECT 1
          FROM temp_active_quality_accounts active_accounts
          WHERE active_accounts.id = account_quality_minute_stats.account_id
        )
      `).run()
    } else {
      database.prepare('DELETE FROM account_quality_minute_stats').run()
    }
    database.prepare('DELETE FROM account_quality_minute_stats WHERE stat_minute < ?').run(retentionCutoffMinute)
    for (const row of rows) {
      const metadata = activeAccounts.get(row.account_id)
      if (!metadata) continue
      refreshedAccountIds.add(row.account_id)
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
      const qualityState = row.recent_first_token_sample_count > 0 ? 'fresh' : row.recent_success_count > 0 ? 'unknown' : 'failed'
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

    for (const [accountId, previous] of previousQualityByAccount) {
      if (!activeAccountIds.has(accountId) || refreshedAccountIds.has(accountId)) {
        continue
      }
      markAccountQualityStale(upsertQuality, previous, windowStartedAt, windowEndedAt, updatedAt)
    }

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

function loadQualityAccountMetadata(): Map<string, { systemAccountId: string; providerCode: string }> {
  const rows = getDatabase()
    .prepare('SELECT id, system_account_id, provider_code FROM accounts')
    .all() as unknown as Array<{ id: string; system_account_id: string; provider_code: string }>
  return new Map(rows.map((row) => [row.id, { systemAccountId: row.system_account_id, providerCode: row.provider_code }]))
}

function loadAccountQualityRows(): Map<string, AccountQualityRow> {
  const rows = getRecordDatabase()
    .prepare(`SELECT ${accountQualitySelectColumns()} FROM account_quality_scores`)
    .all() as unknown as AccountQualityRow[]
  return new Map(rows.map((row) => [row.account_id, row]))
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

function cleanupInactiveQualityRows(database: ReturnType<typeof getRecordDatabase>, activeIds: string[]): { changes?: number | bigint } {
  database.prepare('DROP TABLE IF EXISTS temp_active_quality_accounts').run()
  database.prepare('CREATE TEMP TABLE temp_active_quality_accounts (id TEXT PRIMARY KEY)').run()
  for (const chunk of chunkValues(activeIds, 500)) {
    database
      .prepare(`INSERT INTO temp_active_quality_accounts (id) VALUES ${chunk.map(() => '(?)').join(',')}`)
      .run(...chunk)
  }
  return database.prepare(`
    DELETE FROM account_quality_scores
    WHERE NOT EXISTS (
      SELECT 1
      FROM temp_active_quality_accounts active_accounts
      WHERE active_accounts.id = account_quality_scores.account_id
    )
  `).run()
}

function markAccountQualityStale(upsertQuality: ReturnType<DatabaseSync['prepare']>, previous: AccountQualityRow, windowStartedAt: string, windowEndedAt: string, updatedAt: string): void {
  const qualityState: AccountQualityState = previous.quality_state === 'fresh' ? 'stale' : previous.quality_state
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

function prepareAccountQualityUpsert(database: ReturnType<typeof getRecordDatabase>): ReturnType<DatabaseSync['prepare']> {
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

function upsertAccountQuality(upsertQuality: ReturnType<DatabaseSync['prepare']>, input: {
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
}): void {
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

function computeQualityScore(input: {
  ewmaFirstTokenMs?: number | null
  successRate?: number | null
  qualityState: AccountQualityState
  updatedAt: string
}): number {
  const latency = typeof input.ewmaFirstTokenMs === 'number' ? input.ewmaFirstTokenMs : unknownQualityScore
  const successRate = typeof input.successRate === 'number' ? input.successRate : 0.5
  const errorPenalty = Math.round((1 - successRate) * 20_000)
  const statePenalty = input.qualityState === 'failed'
    ? failurePenaltyMs
    : input.qualityState === 'stale'
      ? stalePenaltyMs
      : input.qualityState === 'unknown'
        ? 10_000
        : 0
  const agePenalty = agePenaltyMs(input.updatedAt)
  return Math.max(0, Math.trunc(latency + errorPenalty + statePenalty + agePenalty))
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
