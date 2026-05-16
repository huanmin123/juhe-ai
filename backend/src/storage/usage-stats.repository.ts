import type { DatabaseSync } from 'node:sqlite'

import type {
  AiPerformanceAccount,
  AiPerformanceAccountOption,
  AiPerformanceOverview,
  AiPerformanceWindowDefinition,
  AiPerformanceWindowKey,
  AccountUsageStatsRange,
  UsageOverviewWindowDefinition,
  UsageOverviewWindowKey
} from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { averageFromSum, dateKey, hourKey, monthKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { emptyStatsAggregateMathRow, mapSystemMetricsHourly, mapSystemMetricsLatest, usageSummaryWithMath } from './usage-stats-mappers.js'
import { aggregateAccountQualityMinuteStatsRecord, aggregateCallerAccountUsageStatsRecord, aggregateUsageStatsRecord } from './usage-stats-writers.js'
import {
  GLOBAL_STATS_SCOPE_ID,
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
  USAGE_STATS_RECORD_SELECT_COLUMNS,
  type AccountUsageAggregateRow,
  type StatsAggregateMathRow,
  type StatsJobStateRow,
  type SystemMetricsOverview,
  type SystemMetricsSampleInput,
  type UsageStatsOverview,
  type UsageStatsRecordRow
} from './usage-stats-types.js'

export type { SystemMetricsOverview, SystemMetricsSampleInput, UsageStatsOverview } from './usage-stats-types.js'
export type { AiPerformanceWindowKey, UsageOverviewWindowKey } from '../domain/types.js'

const USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS = 5
const CALLER_ACCOUNT_USAGE_STATS_BACKFILL_JOB_NAME = 'caller_account_usage_stats_backfill'
const ACCOUNT_QUALITY_MINUTE_STATS_BACKFILL_JOB_NAME = 'account_quality_minute_stats_backfill'
const AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT = 20
const AI_PERFORMANCE_ACCOUNT_OPTION_DEFAULT_LIMIT = 30
const AI_PERFORMANCE_ACCOUNT_OPTION_MAX_LIMIT = 50
const HOUR_MS = 60 * 60 * 1000
const DAY_MS = 24 * HOUR_MS
const FIXED_RANGE_WINDOW_DAYS = 31

const USAGE_OVERVIEW_WINDOWS: UsageOverviewWindowDefinition[] = [
  { key: 'last1d', label: '近一天', hours: 24 },
  { key: 'last3d', label: '近三天', hours: 72 },
  { key: 'last7d', label: '近一周', hours: 168 },
  { key: 'last30d', label: '近一月', hours: 720 }
]

const USAGE_OVERVIEW_TREND_BUCKET_HOURS: Record<UsageOverviewWindowKey, number> = {
  last1d: 1,
  last3d: 6,
  last7d: 24,
  last30d: 24
}

const AI_PERFORMANCE_WINDOWS: AiPerformanceWindowDefinition[] = [
  { key: 'last1d', label: '近一天', hours: 24 },
  { key: 'last3d', label: '近三天', hours: 72 },
  { key: 'last7d', label: '近一周', hours: 168 }
]

const QUOTA_HOURLY_WINDOW_HOURS = [1, 3, 6, 12, 24, 72, 168, 720] as const

export function aggregateUsageStatsBatch(limit = 2000): number {
  const database = getRecordDatabase()
  const batchLimit = Math.max(1, limit)
  const callerAccountBackfill = ensureCallerAccountUsageStatsBackfill(database, batchLimit)
  if (!callerAccountBackfill.complete || callerAccountBackfill.processed > 0) {
    return callerAccountBackfill.processed
  }
  const accountQualityBackfill = ensureAccountQualityMinuteStatsBackfill(database, batchLimit)
  if (!accountQualityBackfill.complete || accountQualityBackfill.processed > 0) {
    return accountQualityBackfill.processed
  }
  const state = usageStatsJobState(database)
  const safeCreatedBefore = usageStatsSafeCreatedBefore()
  const rows = database
    .prepare(`
      SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
      FROM usage_records
      WHERE created_at <= ?
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(safeCreatedBefore, state.cursorCreatedAt, state.cursorCreatedAt, state.cursorId, batchLimit) as unknown as UsageStatsRecordRow[]

  if (!rows.length) {
    updateStatsJobState(database, {
      lastSuccessAt: nowIso(),
      lagSeconds: latestUsageRecordLagSeconds(database, safeCreatedBefore, state.cursorCreatedAt, state.cursorId)
    })
    return 0
  }

  const updatedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const row of rows) {
      aggregateUsageStatsRecord(database, row, updatedAt)
    }
    const last = rows[rows.length - 1]
    updateStatsJobState(database, {
      cursorCreatedAt: last.created_at,
      cursorId: last.id,
      lastSuccessAt: updatedAt,
      lagSeconds: statsLagSecondsFromCursor(last.created_at)
    })
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    updateStatsJobState(database, {
      lastErrorMessage: error instanceof Error ? error.message : '用量统计聚合失败',
      lagSeconds: latestUsageStatsLagSeconds()
    })
    throw error
  }

  return rows.length
}

function ensureCallerAccountUsageStatsBackfill(database: DatabaseSync, limit: number): { complete: boolean; processed: number } {
  const backfillState = database
    .prepare("SELECT cursor_created_at, cursor_id, last_success_at FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(CALLER_ACCOUNT_USAGE_STATS_BACKFILL_JOB_NAME) as unknown as { cursor_created_at?: string | null; cursor_id?: string | null; last_success_at?: string | null } | undefined
  if (backfillState?.last_success_at) {
    return { complete: true, processed: 0 }
  }

  const usageStatsState = usageStatsJobState(database)
  if (!usageStatsState.cursorCreatedAt) {
    recordCallerAccountUsageStatsBackfill(database, 'skipped')
    return { complete: true, processed: 0 }
  }

  const cursorCreatedAt = backfillState?.cursor_created_at ?? ''
  const cursorId = backfillState?.cursor_id ?? ''
  const rows = database
    .prepare(`
      SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
      FROM usage_records
      WHERE account_id IS NOT NULL
        AND (created_at > ? OR (created_at = ? AND id > ?))
        AND (created_at < ? OR (created_at = ? AND id <= ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(cursorCreatedAt, cursorCreatedAt, cursorId, usageStatsState.cursorCreatedAt, usageStatsState.cursorCreatedAt, usageStatsState.cursorId, limit) as unknown as UsageStatsRecordRow[]
  const updatedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const row of rows) {
      aggregateCallerAccountUsageStatsRecord(database, row, updatedAt)
    }
    const last = rows[rows.length - 1]
    const complete = !last || rows.length < limit || last.created_at > usageStatsState.cursorCreatedAt || (last.created_at === usageStatsState.cursorCreatedAt && last.id >= usageStatsState.cursorId)
    if (complete) {
      recordCallerAccountUsageStatsBackfill(database, `processed:${rows.length}`, updatedAt)
    } else {
      recordCallerAccountUsageStatsBackfillProgress(database, last.created_at, last.id, updatedAt)
    }
    commitDatabaseTransaction(database, transactionStarted)
    return { complete, processed: rows.length }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    recordCallerAccountUsageStatsBackfillFailure(database, error)
    throw error
  }
}

function recordCallerAccountUsageStatsBackfillProgress(database: DatabaseSync, cursorCreatedAt: string, cursorId: string, updatedAt = nowIso()): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, ?, ?, NULL, NULL, 0, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = NULL,
      last_error_message = NULL,
      lag_seconds = 0,
      updated_at = excluded.updated_at
  `).run(CALLER_ACCOUNT_USAGE_STATS_BACKFILL_JOB_NAME, cursorCreatedAt, cursorId, updatedAt)
}

function recordCallerAccountUsageStatsBackfill(database: DatabaseSync, cursorId: string, updatedAt = nowIso()): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, '', ?, ?, NULL, 0, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      lag_seconds = 0,
      updated_at = excluded.updated_at
  `).run(CALLER_ACCOUNT_USAGE_STATS_BACKFILL_JOB_NAME, cursorId, updatedAt, updatedAt)
}

function recordCallerAccountUsageStatsBackfillFailure(database: DatabaseSync, error: unknown): void {
  const updatedAt = nowIso()
  const message = error instanceof Error ? error.message : 'caller_account 用量统计回填失败'
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, '', 'failed', NULL, ?, 0, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_id = excluded.cursor_id,
      last_success_at = NULL,
      last_error_message = excluded.last_error_message,
      lag_seconds = 0,
      updated_at = excluded.updated_at
  `).run(CALLER_ACCOUNT_USAGE_STATS_BACKFILL_JOB_NAME, message, updatedAt)
}

function ensureAccountQualityMinuteStatsBackfill(database: DatabaseSync, limit: number): { complete: boolean; processed: number } {
  const backfillState = database
    .prepare("SELECT cursor_created_at, cursor_id, last_success_at FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = ?")
    .get(ACCOUNT_QUALITY_MINUTE_STATS_BACKFILL_JOB_NAME) as unknown as { cursor_created_at?: string | null; cursor_id?: string | null; last_success_at?: string | null } | undefined
  if (backfillState?.last_success_at) {
    return { complete: true, processed: 0 }
  }

  const usageStatsState = usageStatsJobState(database)
  if (!usageStatsState.cursorCreatedAt) {
    recordAccountQualityMinuteStatsBackfill(database, 'skipped')
    return { complete: true, processed: 0 }
  }

  const cursorCreatedAt = backfillState?.cursor_created_at ?? ''
  const cursorId = backfillState?.cursor_id ?? ''
  const rows = database
    .prepare(`
      SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
      FROM usage_records
      WHERE account_id IS NOT NULL
        AND api_key_id IS NOT NULL
        AND (created_at > ? OR (created_at = ? AND id > ?))
        AND (created_at < ? OR (created_at = ? AND id <= ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all(cursorCreatedAt, cursorCreatedAt, cursorId, usageStatsState.cursorCreatedAt, usageStatsState.cursorCreatedAt, usageStatsState.cursorId, limit) as unknown as UsageStatsRecordRow[]
  const updatedAt = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const row of rows) {
      aggregateAccountQualityMinuteStatsRecord(database, row, updatedAt)
    }
    const last = rows[rows.length - 1]
    const complete = !last || rows.length < limit || last.created_at > usageStatsState.cursorCreatedAt || (last.created_at === usageStatsState.cursorCreatedAt && last.id >= usageStatsState.cursorId)
    if (complete) {
      recordAccountQualityMinuteStatsBackfill(database, `processed:${rows.length}`, updatedAt)
    } else {
      recordAccountQualityMinuteStatsBackfillProgress(database, last.created_at, last.id, updatedAt)
    }
    commitDatabaseTransaction(database, transactionStarted)
    return { complete, processed: rows.length }
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    recordAccountQualityMinuteStatsBackfillFailure(database, error)
    throw error
  }
}

function recordAccountQualityMinuteStatsBackfillProgress(database: DatabaseSync, cursorCreatedAt: string, cursorId: string, updatedAt = nowIso()): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, ?, ?, NULL, NULL, 0, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = excluded.cursor_created_at,
      cursor_id = excluded.cursor_id,
      last_success_at = NULL,
      last_error_message = NULL,
      lag_seconds = 0,
      updated_at = excluded.updated_at
  `).run(ACCOUNT_QUALITY_MINUTE_STATS_BACKFILL_JOB_NAME, cursorCreatedAt, cursorId, updatedAt)
}

function recordAccountQualityMinuteStatsBackfill(database: DatabaseSync, cursorId: string, updatedAt = nowIso()): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, '', ?, ?, NULL, 0, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_id = excluded.cursor_id,
      last_success_at = excluded.last_success_at,
      last_error_message = NULL,
      lag_seconds = 0,
      updated_at = excluded.updated_at
  `).run(ACCOUNT_QUALITY_MINUTE_STATS_BACKFILL_JOB_NAME, cursorId, updatedAt, updatedAt)
}

function recordAccountQualityMinuteStatsBackfillFailure(database: DatabaseSync, error: unknown): void {
  const updatedAt = nowIso()
  const message = error instanceof Error ? error.message : '账号质量分钟缓存回填失败'
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', ?, '', 'failed', NULL, ?, 0, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_id = excluded.cursor_id,
      last_success_at = NULL,
      last_error_message = excluded.last_error_message,
      lag_seconds = 0,
      updated_at = excluded.updated_at
  `).run(ACCOUNT_QUALITY_MINUTE_STATS_BACKFILL_JOB_NAME, message, updatedAt)
}

export function refreshGroupAccountStatsCache(): void {
  const database = getRecordDatabase()
  const businessDatabase = getDatabase()
  const updatedAt = nowIso()
  const groups = businessDatabase.prepare('SELECT id, system_account_id FROM groups').all() as unknown as Array<{ id: string; system_account_id: string }>
  const groupAccountRows = businessDatabase.prepare(`
    SELECT
      group_accounts.group_id,
      group_accounts.account_id,
      group_accounts.account_authorization_id,
      groups.system_account_id AS group_system_account_id,
      accounts.system_account_id AS account_system_account_id,
      accounts.status,
      accounts.schedulable,
      accounts.cooldown_until,
      accounts.concurrency_limit,
      account_authorizations.status AS authorization_status,
      account_authorizations.expires_at AS authorization_expires_at
    FROM group_accounts
    INNER JOIN groups ON groups.id = group_accounts.group_id
    LEFT JOIN accounts ON accounts.id = group_accounts.account_id
    LEFT JOIN resource_authorizations account_authorizations
      ON account_authorizations.id = group_accounts.account_authorization_id
    WHERE group_accounts.enabled = 1
  `).all() as unknown as Array<{
    group_id: string
    account_id: string | null
    account_authorization_id: string | null
    group_system_account_id: string
    account_system_account_id: string | null
    status: string | null
    schedulable: number | null
    cooldown_until: string | null
    concurrency_limit: number | null
    authorization_status: string | null
    authorization_expires_at: string | null
  }>
  const statsByGroup = new Map<string, GroupAccountStatsAccumulator>()
  for (const group of groups) {
    statsByGroup.set(group.id, emptyGroupAccountStatsAccumulator(group.id, group.system_account_id))
  }
  for (const row of groupAccountRows) {
    const stats = statsByGroup.get(row.group_id) ?? emptyGroupAccountStatsAccumulator(row.group_id, row.group_system_account_id)
    statsByGroup.set(row.group_id, stats)
    if (!row.account_id || !row.account_system_account_id) continue
    const authorized = row.account_system_account_id === row.group_system_account_id
      || (row.authorization_status === 'active' && (!row.authorization_expires_at || row.authorization_expires_at > updatedAt))
    if (!authorized) continue
    stats.total += 1
    stats.concurrencyLimit += Number(row.concurrency_limit ?? 0)
    if (row.status === 'active') {
      stats.active += 1
      if (row.schedulable === 1 && (!row.cooldown_until || row.cooldown_until <= updatedAt)) {
        stats.available += 1
      }
    } else if (row.status === 'disabled') {
      stats.disabled += 1
    } else {
      stats.error += 1
    }
    if (row.status === 'rate_limited') {
      stats.rateLimited += 1
    }
  }
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM group_account_stats').run()
    const insert = database.prepare(`
      INSERT INTO group_account_stats (
        system_account_id, group_id, total, available, active, disabled, error,
        rate_limited, current_concurrency, concurrency_limit, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
    `)
    for (const stats of statsByGroup.values()) {
      insert.run(
        stats.systemAccountId,
        stats.groupId,
        stats.total,
        stats.available,
        stats.active,
        stats.disabled,
        stats.error,
        stats.rateLimited,
        stats.concurrencyLimit,
        updatedAt
      )
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

interface GroupAccountStatsAccumulator {
  groupId: string
  systemAccountId: string
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rateLimited: number
  concurrencyLimit: number
}

function emptyGroupAccountStatsAccumulator(groupId: string, systemAccountId: string): GroupAccountStatsAccumulator {
  return {
    groupId,
    systemAccountId,
    total: 0,
    available: 0,
    active: 0,
    disabled: 0,
    error: 0,
    rateLimited: 0,
    concurrencyLimit: 0
  }
}

export function refreshUsageRankSnapshots(): void {
  const database = getRecordDatabase()
  const timezone = usageStatsTimezone()
  const updatedAt = nowIso()
  const snapshotAt = updatedAt
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    refreshAccountLast7dRequestRankSnapshot(database, snapshotAt, updatedAt, timezone)
    refreshCallerAccountLast7dRequestRankSnapshot(database, snapshotAt, updatedAt, timezone)
    refreshApiKeyCurrentMonthCostRankSnapshot(database, snapshotAt, updatedAt, timezone)
    refreshAuthorizationCurrentMonthCostRankSnapshot(database, 'account_authorization', snapshotAt, updatedAt, timezone)
    refreshAuthorizationCurrentMonthCostRankSnapshot(database, 'group_authorization', snapshotAt, updatedAt, timezone)
    refreshUsageOverviewWindowSnapshots(database, updatedAt, timezone)
    refreshUsageQuotaHourlyWindowSnapshots(database, updatedAt, timezone)
    refreshUsageScopeRangeWindowSnapshots(database, updatedAt, timezone)
    refreshAuthorizationUsageRangeWindowSnapshots(database, updatedAt, timezone)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

function refreshUsageOverviewWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  database.prepare('DELETE FROM usage_overview_summary_windows').run()
  database.prepare('DELETE FROM usage_overview_trend_windows').run()
  database.prepare('DELETE FROM usage_model_rank_windows').run()
  database.prepare('DELETE FROM usage_error_rank_windows').run()
  database.prepare('DELETE FROM ai_performance_summary_windows').run()
  database.prepare('DELETE FROM system_metrics_trend_windows').run()

  const scopes = usageOverviewSnapshotScopes(database)
  for (const scope of scopes) {
    for (const window of USAGE_OVERVIEW_WINDOWS) {
      const sinceHour = hourKey(new Date(Date.now() - window.hours * HOUR_MS), timezone)
      refreshUsageOverviewSummaryWindow(database, scope, window.key, sinceHour, updatedAt)
      refreshUsageOverviewTrendWindow(database, scope, window.key, sinceHour, trendBucketHours(window.key), updatedAt)
      refreshUsageModelRankWindow(database, scope.systemAccountId, window.key, sinceHour, updatedAt)
      refreshUsageErrorRankWindow(database, scope.systemAccountId, window.key, sinceHour, updatedAt)
    }
  }
  const systemAccountIds = scopes
    .map((scope) => scope.systemAccountId)
    .filter((id) => id !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID)
  for (const systemAccountId of systemAccountIds) {
    for (const window of AI_PERFORMANCE_WINDOWS) {
      const sinceHour = hourKey(new Date(Date.now() - window.hours * HOUR_MS), timezone)
      refreshAiPerformanceSummaryWindow(database, systemAccountId, window.key, sinceHour, updatedAt)
    }
  }
  for (const window of USAGE_OVERVIEW_WINDOWS) {
    const sinceHour = hourKey(new Date(Date.now() - window.hours * HOUR_MS), timezone)
    refreshSystemMetricsTrendWindow(database, window.key, sinceHour, trendBucketHours(window.key), updatedAt)
  }
}

function usageOverviewSnapshotScopes(database: DatabaseSync): Array<{ systemAccountId: string; scopeId: string }> {
  const rows = database.prepare(`
    SELECT DISTINCT system_account_id, scope_id
    FROM usage_stats_totals
    WHERE scope_type = 'system_account'
  `).all() as unknown as Array<{ system_account_id?: string | null; scope_id?: string | null }>
  const scopes = rows
    .map((row) => ({ systemAccountId: row.system_account_id ?? '', scopeId: row.scope_id ?? '' }))
    .filter((row) => row.systemAccountId && row.scopeId)
  if (!scopes.some((scope) => scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID && scope.scopeId === GLOBAL_STATS_SCOPE_ID)) {
    scopes.push({ systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeId: GLOBAL_STATS_SCOPE_ID })
  }
  return scopes
}

function refreshUsageOverviewSummaryWindow(
  database: DatabaseSync,
  scope: { systemAccountId: string; scopeId: string },
  windowKey: UsageOverviewWindowKey,
  sinceHour: string,
  updatedAt: string
): void {
  database.prepare(`
    INSERT INTO usage_overview_summary_windows (
      system_account_id, window_key, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count,
      last_used_at, updated_at
    )
    SELECT ?, ?,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(success_count), 0),
      COALESCE(SUM(error_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(total_cost_usd), 0),
      COALESCE(SUM(duration_ms_sum), 0),
      COALESCE(SUM(duration_ms_count), 0),
      COALESCE(SUM(first_token_ms_sum), 0),
      COALESCE(SUM(first_token_ms_count), 0),
      MAX(last_used_at),
      ?
    FROM usage_stats_hourly
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_hour >= ?
  `).run(scope.systemAccountId, windowKey, updatedAt, scope.systemAccountId, scope.scopeId, sinceHour)
}

function refreshUsageOverviewTrendWindow(
  database: DatabaseSync,
  scope: { systemAccountId: string; scopeId: string },
  windowKey: UsageOverviewWindowKey,
  sinceHour: string,
  bucketHours: number,
  updatedAt: string
): void {
  const rows = database.prepare(`
    SELECT stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      total_cost_usd, duration_ms_sum, duration_ms_count
    FROM usage_stats_hourly
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND stat_hour >= ?
    ORDER BY stat_hour ASC
  `).all(scope.systemAccountId, scope.scopeId, sinceHour) as unknown as Array<{
    stat_hour: string
    request_count: number
    error_count: number
    input_tokens: number
    output_tokens: number
    cache_read_tokens: number
    total_cost_usd: number
    duration_ms_sum: number
    duration_ms_count: number
  }>
  const buckets = new Map<string, {
    requestCount: number
    errorCount: number
    inputTokens: number
    outputTokens: number
    cacheReadTokens: number
    totalCostUsd: number
    durationMsSum: number
    durationMsCount: number
  }>()
  for (const row of rows) {
    const key = trendBucketKey(row.stat_hour, bucketHours)
    const bucket = buckets.get(key) ?? {
      requestCount: 0,
      errorCount: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      totalCostUsd: 0,
      durationMsSum: 0,
      durationMsCount: 0
    }
    bucket.requestCount += Number(row.request_count ?? 0)
    bucket.errorCount += Number(row.error_count ?? 0)
    bucket.inputTokens += Number(row.input_tokens ?? 0)
    bucket.outputTokens += Number(row.output_tokens ?? 0)
    bucket.cacheReadTokens += Number(row.cache_read_tokens ?? 0)
    bucket.totalCostUsd += Number(row.total_cost_usd ?? 0)
    bucket.durationMsSum += Number(row.duration_ms_sum ?? 0)
    bucket.durationMsCount += Number(row.duration_ms_count ?? 0)
    buckets.set(key, bucket)
  }

  const insert = database.prepare(`
    INSERT INTO usage_overview_trend_windows (
      system_account_id, window_key, bucket_key, request_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd,
      duration_ms_sum, duration_ms_count, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const [bucketKey, bucket] of buckets) {
    insert.run(
      scope.systemAccountId,
      windowKey,
      bucketKey,
      bucket.requestCount,
      bucket.errorCount,
      bucket.inputTokens,
      bucket.outputTokens,
      bucket.cacheReadTokens,
      bucket.totalCostUsd,
      bucket.durationMsSum,
      bucket.durationMsCount,
      updatedAt
    )
  }
}

function refreshUsageModelRankWindow(database: DatabaseSync, systemAccountId: string, windowKey: UsageOverviewWindowKey, sinceHour: string, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_model_rank_windows (
      system_account_id, window_key, rank, provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, updated_at
    )
    SELECT ?, ?, rank, provider_code, model, request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, ?
    FROM (
      SELECT
        provider_code,
        model,
        SUM(request_count) AS request_count,
        SUM(input_tokens) AS input_tokens,
        SUM(output_tokens) AS output_tokens,
        SUM(cache_read_tokens) AS cache_read_tokens,
        SUM(total_cost_usd) AS total_cost_usd,
        ROW_NUMBER() OVER (ORDER BY SUM(request_count) DESC, provider_code ASC, model ASC) AS rank
      FROM usage_model_hourly
      WHERE system_account_id = ?
        AND stat_hour >= ?
      GROUP BY provider_code, model
    )
    WHERE rank <= 10
  `).run(systemAccountId, windowKey, updatedAt, systemAccountId, sinceHour)
}

function refreshUsageErrorRankWindow(database: DatabaseSync, systemAccountId: string, windowKey: UsageOverviewWindowKey, sinceHour: string, updatedAt: string): void {
  database.prepare(`
    INSERT INTO usage_error_rank_windows (
      system_account_id, window_key, rank, provider_code, error_code,
      status_code, error_message, error_count, updated_at
    )
    SELECT ?, ?, rank, provider_code, error_code, status_code, error_message, error_count, ?
    FROM (
      SELECT
        provider_code,
        error_code,
        MAX(status_code) AS status_code,
        MAX(error_message) AS error_message,
        SUM(error_count) AS error_count,
        ROW_NUMBER() OVER (
          ORDER BY SUM(error_count) DESC, provider_code ASC, error_code ASC, error_group ASC
        ) AS rank
      FROM usage_error_hourly
      WHERE system_account_id = ?
        AND stat_hour >= ?
      GROUP BY error_group, provider_code, error_code
    )
    WHERE rank <= 10
  `).run(systemAccountId, windowKey, updatedAt, systemAccountId, sinceHour)
}

function refreshAiPerformanceSummaryWindow(database: DatabaseSync, systemAccountId: string, windowKey: AiPerformanceWindowKey, sinceHour: string, updatedAt: string): void {
  database.prepare(`
    INSERT INTO ai_performance_summary_windows (
      system_account_id, window_key, request_count, duration_ms_sum, duration_ms_count,
      duration_ms_max, first_token_ms_sum, first_token_ms_count, first_token_ms_max, updated_at
    )
    SELECT ?, ?,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(duration_ms_sum), 0),
      COALESCE(SUM(duration_ms_count), 0),
      COALESCE(MAX(duration_ms_max), 0),
      COALESCE(SUM(first_token_ms_sum), 0),
      COALESCE(SUM(first_token_ms_count), 0),
      COALESCE(MAX(first_token_ms_max), 0),
      ?
    FROM usage_stats_hourly
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND stat_hour >= ?
  `).run(systemAccountId, windowKey, updatedAt, systemAccountId, sinceHour)
}

function refreshSystemMetricsTrendWindow(database: DatabaseSync, windowKey: UsageOverviewWindowKey, sinceHour: string, bucketHours: number, updatedAt: string): void {
  const rows = database.prepare('SELECT * FROM system_metrics_hourly WHERE stat_hour >= ? ORDER BY stat_hour ASC').all(sinceHour) as unknown as Array<Record<string, unknown>>
  const buckets = aggregateSystemMetricsRows(rows, bucketHours)
  const insert = database.prepare(`
    INSERT INTO system_metrics_trend_windows (
      window_key, bucket_key, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const row of buckets) {
    insert.run(
      windowKey,
      String(row.stat_hour ?? ''),
      Number(row.sample_count ?? 0),
      Number(row.cpu_percent_sum ?? 0),
      nullableNumber(row.cpu_percent_max),
      Number(row.memory_used_percent_sum ?? 0),
      nullableNumber(row.memory_used_percent_max),
      Number(row.process_rss_bytes_sum ?? 0),
      nullableNumber(row.process_rss_bytes_max),
      Number(row.process_heap_used_bytes_sum ?? 0),
      nullableNumber(row.process_heap_used_bytes_max),
      Number(row.event_loop_lag_ms_sum ?? 0),
      nullableNumber(row.event_loop_lag_ms_max),
      Number(row.network_rx_bytes_per_sec_sum ?? 0),
      nullableNumber(row.network_rx_bytes_per_sec_max),
      Number(row.network_rx_bytes_per_sec_count ?? 0),
      Number(row.network_tx_bytes_per_sec_sum ?? 0),
      nullableNumber(row.network_tx_bytes_per_sec_max),
      Number(row.network_tx_bytes_per_sec_count ?? 0),
      nullableNumber(row.network_rx_total_bytes_max),
      nullableNumber(row.network_tx_total_bytes_max),
      nullableNumber(row.db_file_bytes_max),
      nullableNumber(row.stats_lag_seconds_max),
      updatedAt
    )
  }
}

function refreshUsageQuotaHourlyWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const windows = usageQuotaHourlyWindows()
  database.prepare('DELETE FROM usage_quota_hourly_windows').run()
  const insert = database.prepare(`
    INSERT INTO usage_quota_hourly_windows (
      system_account_id, scope_type, scope_id, window_hours, total_cost_usd, updated_at
    )
    SELECT system_account_id, scope_type, scope_id, ?, COALESCE(SUM(total_cost_usd), 0), ?
    FROM usage_stats_hourly
    WHERE stat_hour >= ?
    GROUP BY system_account_id, scope_type, scope_id
    HAVING COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (const hours of windows) {
    insert.run(hours, updatedAt, hourKey(new Date(Date.now() - hours * HOUR_MS), timezone))
  }
}

function usageQuotaHourlyWindows(): number[] {
  const windows = new Set<number>(QUOTA_HOURLY_WINDOW_HOURS)
  for (const row of quotaLimitRows()) {
    const limits = parseRequestQuotaLimitsJson(row.limits_json)
    if (limits.hourly?.enabled) {
      windows.add(limits.hourly.hours)
    }
  }
  return [...windows].filter((value) => Number.isInteger(value) && value > 0).sort((left, right) => left - right)
}

function quotaLimitRows(): Array<{ limits_json: string | null }> {
  const database = getDatabase()
  return [
    ...database.prepare('SELECT quota_limits_json AS limits_json FROM api_keys WHERE quota_limits_json IS NOT NULL').all(),
    ...database.prepare('SELECT limits_json FROM resource_authorizations WHERE limits_json IS NOT NULL').all(),
    ...database.prepare('SELECT limits_json FROM resource_authorization_grants WHERE limits_json IS NOT NULL').all()
  ] as unknown as Array<{ limits_json: string | null }>
}

function refreshUsageScopeRangeWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const todayKey = dateKey(new Date(), timezone)
  const endDate = parseDateKeyStrict(todayKey)
  if (!endDate) return
  const earliestDate = addDays(endDate, -(FIXED_RANGE_WINDOW_DAYS - 1))
  const dates = Array.from({ length: FIXED_RANGE_WINDOW_DAYS }, (_, index) => localDateKey(addDays(earliestDate, index)))
  database.prepare('DELETE FROM usage_scope_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)
  const insert = database.prepare(`
    INSERT INTO usage_scope_range_windows (
      system_account_id, scope_type, scope_id, start_date, end_date,
      request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      scope_type,
      scope_id,
      ?,
      ?,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM usage_stats_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, scope_type, scope_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      insert.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
    }
  }
}

function refreshAuthorizationUsageRangeWindowSnapshots(database: DatabaseSync, updatedAt: string, timezone: string): void {
  const todayKey = dateKey(new Date(), timezone)
  const endDate = parseDateKeyStrict(todayKey)
  if (!endDate) return
  const earliestDate = addDays(endDate, -(FIXED_RANGE_WINDOW_DAYS - 1))
  const dates = Array.from({ length: FIXED_RANGE_WINDOW_DAYS }, (_, index) => localDateKey(addDays(earliestDate, index)))
  database.prepare('DELETE FROM authorization_team_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)
  database.prepare('DELETE FROM authorization_user_usage_range_windows WHERE end_date >= ? AND end_date <= ?').run(dates[0], todayKey)

  const insertTeamRange = database.prepare(`
    INSERT INTO authorization_team_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      ?,
      ?,
      team_filter_id,
      resource_filter_type,
      resource_filter_id,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM authorization_team_usage_summary_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, team_filter_id, resource_filter_type, resource_filter_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  const insertUserRange = database.prepare(`
    INSERT INTO authorization_user_usage_range_windows (
      system_account_id, start_date, end_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
      request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd, last_used_at, updated_at
    )
    SELECT
      system_account_id,
      ?,
      ?,
      team_filter_id,
      grantee_filter_system_account_id,
      resource_filter_type,
      resource_filter_id,
      COALESCE(SUM(request_count), 0),
      COALESCE(SUM(input_tokens), 0),
      COALESCE(SUM(output_tokens), 0),
      COALESCE(SUM(cache_read_tokens), 0),
      COALESCE(SUM(total_cost_usd), 0),
      MAX(last_used_at),
      ?
    FROM authorization_user_usage_summary_daily
    WHERE stat_date >= ?
      AND stat_date <= ?
    GROUP BY system_account_id, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id
    HAVING COALESCE(SUM(request_count), 0) > 0
      OR COALESCE(SUM(input_tokens), 0) > 0
      OR COALESCE(SUM(output_tokens), 0) > 0
      OR COALESCE(SUM(cache_read_tokens), 0) > 0
      OR COALESCE(SUM(total_cost_usd), 0) > 0
  `)
  for (let startIndex = 0; startIndex < dates.length; startIndex += 1) {
    for (let endIndex = startIndex; endIndex < dates.length; endIndex += 1) {
      const startDate = dates[startIndex]
      const rangeEndDate = dates[endIndex]
      insertTeamRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
      insertUserRange.run(startDate, rangeEndDate, updatedAt, startDate, rangeEndDate)
    }
  }
}

export function fixedUsageStatsRangeWindow(timezone = usageStatsTimezone()): AccountUsageStatsRange {
  const todayKey = dateKey(new Date(), timezone)
  const endDate = parseDateKeyStrict(todayKey) ?? new Date()
  const startDate = localDateKey(addDays(endDate, -(FIXED_RANGE_WINDOW_DAYS - 1)))
  return {
    startDate,
    endDate: todayKey,
    days: FIXED_RANGE_WINDOW_DAYS,
    maxDays: FIXED_RANGE_WINDOW_DAYS
  }
}

export function isFixedUsageStatsRangeWindow(range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>, timezone = usageStatsTimezone()): boolean {
  const fixed = fixedUsageStatsRangeWindow(timezone)
  return range.startDate === fixed.startDate && range.endDate === fixed.endDate
}

export interface UsageStatsConsistencyIssue {
  systemAccountId: string
  scopeType: string
  scopeId: string
  statDate: string
  metric: 'request_count' | 'success_count' | 'error_count' | 'input_tokens' | 'output_tokens' | 'cache_read_tokens' | 'total_cost_usd'
  dailyValue: number
  hourlyValue: number
}

export function checkUsageStatsConsistency(sampleLimit = 20): UsageStatsConsistencyIssue[] {
  const database = getRecordDatabase()
  const samples = database.prepare(`
    SELECT system_account_id, scope_type, scope_id, stat_date,
      request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd
    FROM usage_stats_daily
    WHERE stat_date < ?
    ORDER BY updated_at DESC, stat_date DESC, system_account_id ASC, scope_type ASC, scope_id ASC
    LIMIT ?
  `).all(dateKey(new Date(), usageStatsTimezone()), boundedConsistencySampleLimit(sampleLimit)) as unknown as Array<Record<string, unknown>>
  const issues: UsageStatsConsistencyIssue[] = []
  for (const sample of samples) {
    const daily = consistencyStatsRow(sample)
    const hourly = database.prepare(`
      SELECT
        COALESCE(SUM(request_count), 0) AS request_count,
        COALESCE(SUM(success_count), 0) AS success_count,
        COALESCE(SUM(error_count), 0) AS error_count,
        COALESCE(SUM(input_tokens), 0) AS input_tokens,
        COALESCE(SUM(output_tokens), 0) AS output_tokens,
        COALESCE(SUM(cache_read_tokens), 0) AS cache_read_tokens,
        COALESCE(SUM(total_cost_usd), 0) AS total_cost_usd
      FROM usage_stats_hourly
      WHERE system_account_id = ?
        AND scope_type = ?
        AND scope_id = ?
        AND stat_hour >= ?
        AND stat_hour < ?
    `).get(
      daily.systemAccountId,
      daily.scopeType,
      daily.scopeId,
      `${daily.statDate}T00`,
      `${nextDateKey(daily.statDate)}T00`
    ) as unknown as Record<string, unknown> | undefined
    issues.push(...compareConsistencyRows(daily, consistencyStatsRow(hourly ?? {})))
  }
  return issues
}

export function insertSystemMetricsSample(input: SystemMetricsSampleInput): void {
  const database = getRecordDatabase()
  const sampledAt = nowIso()
  const statHour = hourKey(new Date(sampledAt), usageStatsTimezone())
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database
      .prepare(`
        INSERT INTO system_metrics_samples (
          sampled_at, cpu_percent, memory_used_percent, memory_total_bytes, memory_free_bytes,
          process_rss_bytes, process_heap_used_bytes, process_heap_total_bytes, event_loop_lag_ms,
          network_rx_bytes_per_sec, network_tx_bytes_per_sec, network_rx_total_bytes, network_tx_total_bytes,
          db_file_bytes, stats_lag_seconds, id, created_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        sampledAt,
        input.cpuPercent ?? null,
        input.memoryUsedPercent ?? null,
        input.memoryTotalBytes ?? null,
        input.memoryFreeBytes ?? null,
        input.processRssBytes ?? null,
        input.processHeapUsedBytes ?? null,
        input.processHeapTotalBytes ?? null,
        input.eventLoopLagMs ?? null,
        input.networkRxBytesPerSecond ?? null,
        input.networkTxBytesPerSecond ?? null,
        input.networkRxTotalBytes ?? null,
        input.networkTxTotalBytes ?? null,
        input.dbFileBytes ?? null,
        input.statsLagSeconds ?? null,
        newId('metric'),
        sampledAt
      )
    upsertSystemMetricsHourly(database, statHour, input, sampledAt)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
}

export function latestUsageStatsLagSeconds(): number {
  const row = getRecordDatabase()
    .prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { lag_seconds?: number } | undefined
  return Number(row?.lag_seconds ?? 0)
}

export function getUsageStatsOverview(access?: AccessScope, windowKey: UsageOverviewWindowKey = 'last1d'): UsageStatsOverview {
  const database = getRecordDatabase()
  const statsScope = usageOverviewStatsScope(access)
  const window = usageOverviewWindow(windowKey)

  const summaryRow = database.prepare(`
    SELECT ? AS account_id, request_count, success_count, error_count,
      input_tokens, output_tokens, cache_read_tokens, total_cost_usd AS total_cost,
      duration_ms_sum, duration_ms_count, first_token_ms_sum, first_token_ms_count, last_used_at
    FROM usage_overview_summary_windows
    WHERE system_account_id = ? AND window_key = ?
  `).get(statsScope.scopeId, statsScope.systemAccountId, window.key) as unknown as AccountUsageAggregateRow & StatsAggregateMathRow | undefined

  const hourlyRows = database.prepare(`
    SELECT bucket_key AS stat_hour, request_count, error_count, input_tokens, output_tokens, cache_read_tokens,
      total_cost_usd AS total_cost, duration_ms_sum, duration_ms_count
    FROM usage_overview_trend_windows
    WHERE system_account_id = ? AND window_key = ?
    ORDER BY bucket_key ASC
  `).all(statsScope.systemAccountId, window.key) as unknown as Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; total_cost: number }>

  const modelRows = database.prepare(`
    SELECT provider_code, model,
      request_count, input_tokens, output_tokens, cache_read_tokens, total_cost_usd AS total_cost
    FROM usage_model_rank_windows
    WHERE system_account_id = ? AND window_key = ?
    ORDER BY rank ASC
  `).all(statsScope.systemAccountId, window.key) as unknown as Array<{ provider_code: string; model: string; request_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; total_cost: number }>

  const errorRows = database.prepare(`
    SELECT provider_code, error_code, status_code, error_message, error_count
    FROM usage_error_rank_windows
    WHERE system_account_id = ? AND window_key = ?
    ORDER BY rank ASC
  `).all(statsScope.systemAccountId, window.key) as unknown as Array<{ provider_code: string; error_code: string; status_code: number; error_message: string | null; error_count: number }>

  return {
    window,
    summary: usageSummaryWithMath(summaryRow ?? emptyStatsAggregateMathRow()),
    hourlyTrend: mapUsageTrendRows(hourlyRows),
    modelDistribution: modelRows.map((row) => ({
      providerCode: row.provider_code,
      model: row.model,
      requestCount: Number(row.request_count ?? 0),
      totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0),
      totalCost: Number(row.total_cost ?? 0)
    })),
    errors: errorRows.map((row) => ({
      providerCode: row.provider_code,
      errorCode: row.error_code,
      statusCode: row.status_code || undefined,
      errorMessage: row.error_message ?? undefined,
      errorCount: Number(row.error_count ?? 0)
    })),
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

export function getAiPerformanceOverview(access?: AccessScope, windowKey: AiPerformanceWindowKey = 'last1d', accountIds: string[] = []): AiPerformanceOverview {
  const database = getRecordDatabase()
  const timezone = usageStatsTimezone()
  const systemAccountId = currentSystemAccountId(access)
  const window = aiPerformanceWindow(windowKey)
  const now = Date.now()
  const hourBuckets = hourBucketsUntilNow(window.hours, now, timezone)
  const windowSinceHour = hourBuckets[0] ?? hourKey(new Date(now), timezone)
  const activeSinceHour = hourKey(new Date(now - (AI_PERFORMANCE_WINDOWS[2].hours - 1) * HOUR_MS), timezone)
  const selectedAccountIds = uniqueNonEmpty(accountIds).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)

  const defaultRows = loadDefaultAiPerformanceAccounts(database, systemAccountId)
  const selectedRows = selectedAccountIds.length
    ? loadSelectedAiPerformanceAccounts(database, systemAccountId, activeSinceHour, selectedAccountIds)
    : []
  const defaultIds = new Set(defaultRows.map((row) => row.id))
  const selectedIds = new Set(selectedRows.map((row) => row.id))
  const orderedRows = dedupeAiPerformanceAccountRows([...defaultRows, ...selectedRows])
  const accounts = orderedRows.map((row) => mapAiPerformanceAccount(row, defaultIds, selectedIds))
  const hourlyRows = accounts.length
    ? loadAiPerformanceHourlyRows(database, systemAccountId, accounts.map((account) => account.id), windowSinceHour)
    : []
  const hourlyRowsByAccountHour = new Map(hourlyRows.map((row) => [`${row.scope_id}\n${row.stat_hour}`, row]))
  const summaryRow = loadAiPerformanceSummaryRow(database, systemAccountId, window.key)

  const hourlySeries = accounts.map((account) => ({
    accountId: account.id,
    accountName: account.name,
    systemAccountId: account.systemAccountId,
    points: hourBuckets.map((statHour) => {
      const row = hourlyRowsByAccountHour.get(`${account.id}\n${statHour}`)
      const requestCount = Number(row?.request_count ?? 0)
      const firstTokenCount = Number(row?.first_token_ms_count ?? 0)
      const durationCount = Number(row?.duration_ms_count ?? 0)
      return {
        statHour,
        requestCount,
        firstTokenCount,
        averageFirstTokenMs: averageFromSum(row?.first_token_ms_sum, row?.first_token_ms_count),
        maxFirstTokenMs: maxFromCountedMetric(row?.first_token_ms_max, firstTokenCount),
        durationCount,
        averageDurationMs: averageFromSum(row?.duration_ms_sum, row?.duration_ms_count),
        maxDurationMs: maxFromCountedMetric(row?.duration_ms_max, durationCount)
      }
    })
  }))

  return {
    window,
    defaultAccounts: accounts.filter((account) => account.defaultVisible),
    selectedAccounts: accounts.filter((account) => account.selected),
    accounts,
    hourlySeries,
    summary: {
      requestCount: Number(summaryRow?.request_count ?? 0),
      firstTokenCount: Number(summaryRow?.first_token_ms_count ?? 0),
      averageFirstTokenMs: averageFromSum(summaryRow?.first_token_ms_sum, summaryRow?.first_token_ms_count),
      maxFirstTokenMs: maxFromCountedMetric(summaryRow?.first_token_ms_max, Number(summaryRow?.first_token_ms_count ?? 0)),
      durationCount: Number(summaryRow?.duration_ms_count ?? 0),
      averageDurationMs: averageFromSum(summaryRow?.duration_ms_sum, summaryRow?.duration_ms_count),
      maxDurationMs: maxFromCountedMetric(summaryRow?.duration_ms_max, Number(summaryRow?.duration_ms_count ?? 0))
    },
    statsLagSeconds: latestUsageStatsLagSeconds()
  }
}

function loadAiPerformanceSummaryRow(database: DatabaseSync, systemAccountId: string, windowKey: AiPerformanceWindowKey): {
  request_count: number
  first_token_ms_sum: number
  first_token_ms_count: number
  first_token_ms_max: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
} | undefined {
  return database.prepare(`
    SELECT request_count, first_token_ms_sum, first_token_ms_count, first_token_ms_max, duration_ms_sum, duration_ms_count, duration_ms_max
    FROM ai_performance_summary_windows
    WHERE system_account_id = ? AND window_key = ?
  `).get(systemAccountId, windowKey) as unknown as {
    request_count: number
    first_token_ms_sum: number
    first_token_ms_count: number
    first_token_ms_max: number
    duration_ms_sum: number
    duration_ms_count: number
    duration_ms_max: number
  } | undefined
}

export function listAiPerformanceAccountOptions(
  access?: AccessScope,
  options: { keyword?: string; accountIds?: string[]; limit?: number } = {}
): AiPerformanceAccountOption[] {
  const database = getRecordDatabase()
  const timezone = usageStatsTimezone()
  const systemAccountId = currentSystemAccountId(access)
  const activeSinceHour = hourKey(new Date(Date.now() - (AI_PERFORMANCE_WINDOWS[2].hours - 1) * HOUR_MS), timezone)
  const selectedAccountIds = uniqueNonEmpty(options.accountIds ?? []).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)
  const searchLimit = boundedAccountOptionLimit(options.limit)
  const searchRows = loadAiPerformanceAccountOptionRows(database, systemAccountId, activeSinceHour, {
    keyword: options.keyword?.trim(),
    limit: searchLimit
  })
  const selectedRows = selectedAccountIds.length
    ? loadSelectedAiPerformanceAccounts(database, systemAccountId, activeSinceHour, selectedAccountIds)
    : []
  const rows = dedupeAiPerformanceAccountRows([...searchRows, ...selectedRows])
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    status: row.status,
    providerCode: row.provider_code,
    systemAccountId: row.system_account_id,
    requestCountLast7d: Number(row.request_count_last_7d ?? 0)
  }))
}

export function getSystemMetricsOverview(windowKey: UsageOverviewWindowKey = 'last1d'): SystemMetricsOverview {
  const database = getRecordDatabase()
  const latest = database.prepare('SELECT * FROM system_metrics_samples ORDER BY sampled_at DESC, id DESC LIMIT 1').get() as unknown as Record<string, unknown> | undefined
  const window = usageOverviewWindow(windowKey)
  const rows = database.prepare(`
    SELECT bucket_key AS stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max
    FROM system_metrics_trend_windows
    WHERE window_key = ?
    ORDER BY bucket_key ASC
  `).all(window.key) as unknown as Array<Record<string, unknown>>
  return {
    latest: latest ? mapSystemMetricsLatest(latest) : undefined,
    hourlyTrend: rows.map(mapSystemMetricsHourly)
  }
}

function usageOverviewWindow(windowKey: UsageOverviewWindowKey): UsageOverviewWindowDefinition {
  return USAGE_OVERVIEW_WINDOWS.find((window) => window.key === windowKey) ?? USAGE_OVERVIEW_WINDOWS[0]
}

function refreshAccountLast7dRequestRankSnapshot(database: DatabaseSync, snapshotAt: string, updatedAt: string, timezone: string): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType: 'account',
    windowKey: 'last7d',
    metric: 'request_count',
    metricColumn: 'request_count',
    sourceTable: 'usage_stats_daily',
    timeWhere: 'stat_date >= ?',
    timeParams: [dateKey(new Date(Date.now() - 6 * DAY_MS), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

function refreshCallerAccountLast7dRequestRankSnapshot(database: DatabaseSync, snapshotAt: string, updatedAt: string, timezone: string): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType: 'caller_account',
    windowKey: 'last7d',
    metric: 'request_count',
    metricColumn: 'request_count',
    sourceTable: 'usage_stats_daily',
    timeWhere: 'stat_date >= ?',
    timeParams: [dateKey(new Date(Date.now() - 6 * DAY_MS), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

function refreshApiKeyCurrentMonthCostRankSnapshot(database: DatabaseSync, snapshotAt: string, updatedAt: string, timezone: string): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType: 'api_key',
    windowKey: 'current_month',
    metric: 'total_cost_usd',
    metricColumn: 'total_cost_usd',
    sourceTable: 'usage_stats_monthly',
    timeWhere: 'stat_month = ?',
    timeParams: [monthKey(new Date(), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

function refreshAuthorizationCurrentMonthCostRankSnapshot(
  database: DatabaseSync,
  scopeType: 'account_authorization' | 'group_authorization',
  snapshotAt: string,
  updatedAt: string,
  timezone: string
): void {
  refreshUsageRankSnapshotFromStats(database, {
    scopeType,
    windowKey: 'current_month',
    metric: 'total_cost_usd',
    metricColumn: 'total_cost_usd',
    sourceTable: 'usage_stats_monthly',
    timeWhere: 'stat_month = ?',
    timeParams: [monthKey(new Date(), timezone)],
    snapshotAt,
    updatedAt,
    limit: 50
  })
}

function refreshUsageRankSnapshotFromStats(database: DatabaseSync, input: {
  scopeType: string
  windowKey: string
  metric: string
  metricColumn: 'request_count' | 'total_cost_usd'
  sourceTable: 'usage_stats_daily' | 'usage_stats_monthly'
  timeWhere: string
  timeParams: string[]
  snapshotAt: string
  updatedAt: string
  limit: number
}): void {
  database.prepare(`
    DELETE FROM usage_rank_snapshots
    WHERE scope_type = ?
      AND window_key = ?
      AND metric = ?
  `).run(input.scopeType, input.windowKey, input.metric)
  database.prepare(`
    INSERT INTO usage_rank_snapshots (system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at)
    SELECT system_account_id, scope_type, window_key, metric, snapshot_at, rank, scope_id, metric_value, updated_at
    FROM (
      SELECT
        system_account_id,
        ? AS scope_type,
        ? AS window_key,
        ? AS metric,
        ? AS snapshot_at,
        ROW_NUMBER() OVER (
          PARTITION BY system_account_id
          ORDER BY metric_value DESC, last_used_at DESC, scope_id ASC
        ) AS rank,
        scope_id,
        metric_value,
        ? AS updated_at
      FROM (
        SELECT
          system_account_id,
          scope_id,
          SUM(${input.metricColumn}) AS metric_value,
          MAX(last_used_at) AS last_used_at
        FROM ${input.sourceTable}
        WHERE scope_type = ?
          AND ${input.timeWhere}
        GROUP BY system_account_id, scope_id
        HAVING SUM(${input.metricColumn}) > 0
      )
    )
    WHERE rank <= ?
  `).run(input.scopeType, input.windowKey, input.metric, input.snapshotAt, input.updatedAt, input.scopeType, ...input.timeParams, input.limit)
}

function aiPerformanceWindow(windowKey: AiPerformanceWindowKey): AiPerformanceWindowDefinition {
  return AI_PERFORMANCE_WINDOWS.find((window) => window.key === windowKey) ?? AI_PERFORMANCE_WINDOWS[0]
}

interface AiPerformanceAccountRow {
  id: string
  name: string
  status: AiPerformanceAccount['status']
  provider_code: string
  system_account_id: string
  system_account_name: string | null
  request_count_last_7d: number
  last_stat_hour: string | null
}

interface AiPerformanceHourlyRow {
  scope_id: string
  stat_hour: string
  request_count: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
  first_token_ms_sum: number
  first_token_ms_count: number
  first_token_ms_max: number
}

function loadDefaultAiPerformanceAccounts(database: DatabaseSync, systemAccountId: string, limit = 10): AiPerformanceAccountRow[] {
  return loadDefaultAiPerformanceAccountsFromRankSnapshot(database, systemAccountId, limit)
}

function loadDefaultAiPerformanceAccountsFromRankSnapshot(database: DatabaseSync, systemAccountId: string, limit: number): AiPerformanceAccountRow[] {
  const rows = database.prepare(`
    SELECT scope_id, metric_value AS request_count_last_7d, snapshot_at AS last_stat_hour, rank
    FROM usage_rank_snapshots
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND window_key = 'last7d'
      AND metric = 'request_count'
      AND snapshot_at = (
        SELECT MAX(snapshot_at)
        FROM usage_rank_snapshots
        WHERE system_account_id = ?
          AND scope_type = 'account'
          AND window_key = 'last7d'
          AND metric = 'request_count'
      )
    ORDER BY rank ASC
    LIMIT ?
  `).all(systemAccountId, systemAccountId, limit) as unknown as Array<{ scope_id: string; request_count_last_7d: number; last_stat_hour: string | null; rank: number }>
  return mergeAiPerformanceStatsWithAccounts(rows.map((row) => ({
    id: row.scope_id,
    requestCountLast7d: Number(row.request_count_last_7d ?? 0),
    lastStatHour: row.last_stat_hour ?? null,
    rank: Number(row.rank ?? 0)
  })), systemAccountId)
}

function loadSelectedAiPerformanceAccounts(database: DatabaseSync, systemAccountId: string, activeSinceHour: string, accountIds: string[]): AiPerformanceAccountRow[] {
  void activeSinceHour
  const rows = loadUsageRankMetricsByScopeIds(database, systemAccountId, 'account', 'last7d', 'request_count', accountIds)
  const merged = mergeAiPerformanceStatsWithAccounts(accountIds.map((id) => {
    const row = rows.get(id)
    return {
      id,
      requestCountLast7d: Number(row?.metricValue ?? 0),
      lastStatHour: row?.snapshotAt ?? null
    }
  }), systemAccountId)
  const order = new Map(accountIds.map((id, index) => [id, index]))
  return merged.sort((left, right) => (order.get(left.id) ?? 0) - (order.get(right.id) ?? 0))
}

function loadUsageRankMetricsByScopeIds(
  database: DatabaseSync,
  systemAccountId: string,
  scopeType: string,
  windowKey: string,
  metric: string,
  scopeIds: string[]
): Map<string, { metricValue: number; snapshotAt: string | null }> {
  const result = new Map<string, { metricValue: number; snapshotAt: string | null }>()
  const uniqueIds = [...new Set(scopeIds.filter(Boolean))]
  if (!uniqueIds.length) return result
  for (const idChunk of chunkValues(uniqueIds, 400)) {
    const rows = database.prepare(`
      SELECT scope_id, metric_value, snapshot_at
      FROM usage_rank_snapshots
      WHERE system_account_id = ?
        AND scope_type = ?
        AND window_key = ?
        AND metric = ?
        AND scope_id IN (${sqlPlaceholders(idChunk.length)})
        AND snapshot_at = (
          SELECT MAX(snapshot_at)
          FROM usage_rank_snapshots
          WHERE system_account_id = ?
            AND scope_type = ?
            AND window_key = ?
            AND metric = ?
        )
    `).all(systemAccountId, scopeType, windowKey, metric, ...idChunk, systemAccountId, scopeType, windowKey, metric) as unknown as Array<{ scope_id: string; metric_value: number; snapshot_at: string | null }>
    for (const row of rows) {
      result.set(row.scope_id, {
        metricValue: Number(row.metric_value ?? 0),
        snapshotAt: row.snapshot_at ?? null
      })
    }
  }
  return result
}

function loadAiPerformanceHourlyRows(database: DatabaseSync, systemAccountId: string, accountIds: string[], sinceHour: string): AiPerformanceHourlyRow[] {
  const placeholders = sqlPlaceholders(accountIds.length)
  return database.prepare(`
    SELECT
      scope_id,
      stat_hour,
      request_count,
      duration_ms_sum,
      duration_ms_count,
      duration_ms_max,
      first_token_ms_sum,
      first_token_ms_count,
      first_token_ms_max
    FROM usage_stats_hourly
    WHERE system_account_id = ?
      AND scope_type = 'account'
      AND scope_id IN (${placeholders})
      AND stat_hour >= ?
    ORDER BY stat_hour ASC
  `).all(systemAccountId, ...accountIds, sinceHour) as unknown as AiPerformanceHourlyRow[]
}

function loadAiPerformanceAccountOptionRows(
  database: DatabaseSync,
  systemAccountId: string,
  activeSinceHour: string,
  options: { keyword?: string; limit: number }
): AiPerformanceAccountRow[] {
  const keyword = options.keyword?.trim()
  if (!keyword) {
    return loadDefaultAiPerformanceAccounts(database, systemAccountId, options.limit)
  }

  const likeKeyword = `%${keyword}%`
  const accountRows = getDatabase().prepare(`
    SELECT accounts.id
    FROM accounts
    WHERE accounts.system_account_id = ?
      AND (accounts.name LIKE ? OR accounts.id LIKE ? OR accounts.provider_code LIKE ?)
    ORDER BY lower(accounts.name) ASC, accounts.id ASC
    LIMIT ?
  `).all(systemAccountId, likeKeyword, likeKeyword, likeKeyword, options.limit) as unknown as Array<{ id: string }>
  const accountIds = accountRows.map((row) => row.id)
  return accountIds.length
    ? loadSelectedAiPerformanceAccounts(database, systemAccountId, activeSinceHour, accountIds)
    : []
}

function mergeAiPerformanceStatsWithAccounts(
  statsRows: Array<{ id: string; requestCountLast7d: number; lastStatHour: string | null; rank?: number }>,
  systemAccountId: string
): AiPerformanceAccountRow[] {
  const ids = [...new Set(statsRows.map((row) => row.id).filter(Boolean))]
  if (!ids.length) return []
  const placeholders = sqlPlaceholders(ids.length)
  const accounts = getDatabase().prepare(`
    SELECT
      accounts.id,
      accounts.name,
      accounts.status,
      accounts.provider_code,
      accounts.system_account_id,
      system_accounts.display_name AS system_account_name
    FROM accounts
    LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id
    WHERE accounts.system_account_id = ?
      AND accounts.id IN (${placeholders})
  `).all(systemAccountId, ...ids) as unknown as Array<{
    id: string
    name: string
    status: AiPerformanceAccount['status']
    provider_code: string
    system_account_id: string
    system_account_name: string | null
  }>
  const statsById = new Map(statsRows.map((row, index) => [row.id, { ...row, index }]))
  return accounts.map((account) => {
    const stats = statsById.get(account.id)
    return {
      ...account,
      request_count_last_7d: stats?.requestCountLast7d ?? 0,
      last_stat_hour: stats?.lastStatHour ?? null
    }
  }).sort((left, right) => {
    const leftStats = statsById.get(left.id)
    const rightStats = statsById.get(right.id)
    const leftRank = leftStats?.rank ?? Number.POSITIVE_INFINITY
    const rightRank = rightStats?.rank ?? Number.POSITIVE_INFINITY
    if (leftRank !== rightRank) return leftRank - rightRank
    if (right.request_count_last_7d !== left.request_count_last_7d) return right.request_count_last_7d - left.request_count_last_7d
    return left.name.localeCompare(right.name, 'zh-CN') || left.id.localeCompare(right.id)
  })
}

function dedupeAiPerformanceAccountRows(rows: AiPerformanceAccountRow[]): AiPerformanceAccountRow[] {
  const seen = new Set<string>()
  return rows.filter((row) => {
    if (seen.has(row.id)) return false
    seen.add(row.id)
    return true
  })
}

function mapAiPerformanceAccount(row: AiPerformanceAccountRow, defaultIds: Set<string>, selectedIds: Set<string>): AiPerformanceAccount {
  return {
    id: row.id,
    name: row.name,
    status: row.status,
    providerCode: row.provider_code,
    systemAccountId: row.system_account_id,
    systemAccountName: row.system_account_name ?? undefined,
    requestCountLast7d: Number(row.request_count_last_7d ?? 0),
    selected: selectedIds.has(row.id),
    defaultVisible: defaultIds.has(row.id)
  }
}

function hourBucketsUntilNow(hours: number, now = Date.now(), timezone?: string): string[] {
  const size = Math.max(1, Math.trunc(hours))
  return Array.from({ length: size }, (_, index) => hourKey(new Date(now - (size - 1 - index) * HOUR_MS), timezone))
}

function uniqueNonEmpty(values: string[]): string[] {
  const seen = new Set<string>()
  const result: string[] = []
  for (const value of values) {
    const text = value.trim()
    if (!text || seen.has(text)) continue
    seen.add(text)
    result.push(text)
  }
  return result
}

function boundedAccountOptionLimit(value?: number): number {
  const number = Number(value ?? AI_PERFORMANCE_ACCOUNT_OPTION_DEFAULT_LIMIT)
  if (!Number.isFinite(number)) return AI_PERFORMANCE_ACCOUNT_OPTION_DEFAULT_LIMIT
  return Math.min(AI_PERFORMANCE_ACCOUNT_OPTION_MAX_LIMIT, Math.max(1, Math.trunc(number)))
}

function trendBucketHours(windowKey: UsageOverviewWindowKey): number {
  return USAGE_OVERVIEW_TREND_BUCKET_HOURS[windowKey] ?? 1
}

function maxFromCountedMetric(value: unknown, count: number): number | undefined {
  const number = Number(value ?? 0)
  return count > 0 && Number.isFinite(number) ? Math.max(0, Math.round(number)) : undefined
}

function mapUsageTrendRows(
  rows: Array<StatsAggregateMathRow & { stat_hour: string; error_count: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; total_cost: number }>,
): UsageStatsOverview['hourlyTrend'] {
  return rows.map((row) => ({
    statHour: row.stat_hour,
    requestCount: Number(row.request_count ?? 0),
    totalTokens: Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0),
    totalCost: Number(row.total_cost ?? 0),
    averageDurationMs: averageFromSum(row.duration_ms_sum, row.duration_ms_count),
    errorCount: Number(row.error_count ?? 0)
  }))
}

function aggregateSystemMetricsRows(rows: Array<Record<string, unknown>>, bucketHours: number): Array<Record<string, unknown>> {
  const buckets = new Map<string, Record<string, unknown>>()
  for (const row of rows) {
    const key = trendBucketKey(String(row.stat_hour ?? ''), bucketHours)
    const bucket = buckets.get(key) ?? { stat_hour: key, sample_count: 0 }
    addMetric(bucket, row, 'sample_count')
    addMetric(bucket, row, 'cpu_percent_sum')
    maxMetric(bucket, row, 'cpu_percent_max')
    addMetric(bucket, row, 'memory_used_percent_sum')
    maxMetric(bucket, row, 'memory_used_percent_max')
    addMetric(bucket, row, 'process_rss_bytes_sum')
    maxMetric(bucket, row, 'process_rss_bytes_max')
    addMetric(bucket, row, 'process_heap_used_bytes_sum')
    maxMetric(bucket, row, 'process_heap_used_bytes_max')
    addMetric(bucket, row, 'event_loop_lag_ms_sum')
    maxMetric(bucket, row, 'event_loop_lag_ms_max')
    addMetric(bucket, row, 'network_rx_bytes_per_sec_sum')
    maxMetric(bucket, row, 'network_rx_bytes_per_sec_max')
    addMetric(bucket, row, 'network_rx_bytes_per_sec_count')
    addMetric(bucket, row, 'network_tx_bytes_per_sec_sum')
    maxMetric(bucket, row, 'network_tx_bytes_per_sec_max')
    addMetric(bucket, row, 'network_tx_bytes_per_sec_count')
    maxMetric(bucket, row, 'network_rx_total_bytes_max')
    maxMetric(bucket, row, 'network_tx_total_bytes_max')
    maxMetric(bucket, row, 'db_file_bytes_max')
    maxMetric(bucket, row, 'stats_lag_seconds_max')
    buckets.set(key, bucket)
  }
  return [...buckets.values()]
}

function addMetric(target: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  target[key] = Number(target[key] ?? 0) + Number(source[key] ?? 0)
}

function maxMetric(target: Record<string, unknown>, source: Record<string, unknown>, key: string): void {
  const value = numberValue(source[key])
  if (value === undefined) return
  const current = numberValue(target[key])
  target[key] = current === undefined ? value : Math.max(current, value)
}

function numberValue(value: unknown): number | undefined {
  const number = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(number) ? number : undefined
}

function nullableNumber(value: unknown): number | null {
  return numberValue(value) ?? null
}

function trendBucketKey(statHour: string, bucketHours: number): string {
  if (bucketHours >= 24) {
    return statHour.slice(0, 10)
  }
  if (bucketHours <= 1) {
    return statHour
  }
  const hour = Number(statHour.slice(11, 13))
  if (!Number.isFinite(hour)) {
    return statHour
  }
  const bucketHour = Math.floor(hour / bucketHours) * bucketHours
  return `${statHour.slice(0, 11)}${String(bucketHour).padStart(2, '0')}`
}

function usageOverviewStatsScope(access?: AccessScope): { systemAccountId: string; scopeId: string } {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) {
    return { systemAccountId: scopedId, scopeId: scopedId }
  }
  if (canAccessAll(access)) {
    return { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeId: GLOBAL_STATS_SCOPE_ID }
  }
  const systemAccountId = currentSystemAccountId(access)
  return { systemAccountId, scopeId: systemAccountId }
}

function usageStatsJobState(database: DatabaseSync): { cursorCreatedAt: string; cursorId: string } {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as StatsJobStateRow | undefined
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

function usageStatsSafeCreatedBefore(): string {
  return new Date(Date.now() - USAGE_STATS_CURSOR_SAFETY_DELAY_SECONDS * 1000).toISOString()
}

function latestUsageRecordLagSeconds(database: DatabaseSync, safeCreatedBefore: string, cursorCreatedAt: string, cursorId: string): number {
  const latest = database
    .prepare(`
      SELECT created_at
      FROM usage_records
      WHERE created_at <= ?
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at DESC, id DESC
      LIMIT 1
    `)
    .get(safeCreatedBefore, cursorCreatedAt, cursorCreatedAt, cursorId) as unknown as { created_at?: string } | undefined
  return latest?.created_at ? statsLagSecondsFromCursor(latest.created_at) : 0
}

interface ConsistencyStatsRow {
  systemAccountId: string
  scopeType: string
  scopeId: string
  statDate: string
  request_count: number
  success_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  total_cost_usd: number
}

function consistencyStatsRow(row: Record<string, unknown>): ConsistencyStatsRow {
  return {
    systemAccountId: String(row.system_account_id ?? ''),
    scopeType: String(row.scope_type ?? ''),
    scopeId: String(row.scope_id ?? ''),
    statDate: String(row.stat_date ?? ''),
    request_count: Number(row.request_count ?? 0),
    success_count: Number(row.success_count ?? 0),
    error_count: Number(row.error_count ?? 0),
    input_tokens: Number(row.input_tokens ?? 0),
    output_tokens: Number(row.output_tokens ?? 0),
    cache_read_tokens: Number(row.cache_read_tokens ?? 0),
    total_cost_usd: Number(row.total_cost_usd ?? 0)
  }
}

function compareConsistencyRows(daily: ConsistencyStatsRow, hourly: ConsistencyStatsRow): UsageStatsConsistencyIssue[] {
  const metrics: UsageStatsConsistencyIssue['metric'][] = ['request_count', 'success_count', 'error_count', 'input_tokens', 'output_tokens', 'cache_read_tokens', 'total_cost_usd']
  const issues: UsageStatsConsistencyIssue[] = []
  for (const metric of metrics) {
    const dailyValue = daily[metric]
    const hourlyValue = hourly[metric]
    const tolerance = metric === 'total_cost_usd' ? 0.000001 : 0
    if (Math.abs(dailyValue - hourlyValue) <= tolerance) continue
    issues.push({
      systemAccountId: daily.systemAccountId,
      scopeType: daily.scopeType,
      scopeId: daily.scopeId,
      statDate: daily.statDate,
      metric,
      dailyValue,
      hourlyValue
    })
  }
  return issues
}

function nextDateKey(statDate: string): string {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(statDate)
  if (!match) return statDate
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
  date.setDate(date.getDate() + 1)
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function boundedConsistencySampleLimit(value: number): number {
  const number = Number(value)
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), 1), 100) : 20
}

function parseDateKeyStrict(value: string): Date | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return undefined
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
  return date.getFullYear() === Number(match[1]) && date.getMonth() === Number(match[2]) - 1 && date.getDate() === Number(match[3]) ? date : undefined
}

function localDateKey(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function addDays(date: Date, days: number): Date {
  const next = new Date(date)
  next.setDate(next.getDate() + days)
  return next
}

function upsertSystemMetricsHourly(database: DatabaseSync, statHour: string, input: SystemMetricsSampleInput, updatedAt: string): void {
  database.prepare(`
    INSERT INTO system_metrics_hourly (
      stat_hour, sample_count, cpu_percent_sum, cpu_percent_max, memory_used_percent_sum,
      memory_used_percent_max, process_rss_bytes_sum, process_rss_bytes_max, process_heap_used_bytes_sum,
      process_heap_used_bytes_max, event_loop_lag_ms_sum, event_loop_lag_ms_max,
      network_rx_bytes_per_sec_sum, network_rx_bytes_per_sec_max, network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum, network_tx_bytes_per_sec_max, network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max, network_tx_total_bytes_max,
      db_file_bytes_max, stats_lag_seconds_max, updated_at
    )
    VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(stat_hour) DO UPDATE SET
      sample_count = sample_count + 1,
      cpu_percent_sum = cpu_percent_sum + excluded.cpu_percent_sum,
      cpu_percent_max = CASE WHEN excluded.cpu_percent_max IS NULL THEN system_metrics_hourly.cpu_percent_max WHEN system_metrics_hourly.cpu_percent_max IS NULL OR excluded.cpu_percent_max > system_metrics_hourly.cpu_percent_max THEN excluded.cpu_percent_max ELSE system_metrics_hourly.cpu_percent_max END,
      memory_used_percent_sum = memory_used_percent_sum + excluded.memory_used_percent_sum,
      memory_used_percent_max = CASE WHEN excluded.memory_used_percent_max IS NULL THEN system_metrics_hourly.memory_used_percent_max WHEN system_metrics_hourly.memory_used_percent_max IS NULL OR excluded.memory_used_percent_max > system_metrics_hourly.memory_used_percent_max THEN excluded.memory_used_percent_max ELSE system_metrics_hourly.memory_used_percent_max END,
      process_rss_bytes_sum = process_rss_bytes_sum + excluded.process_rss_bytes_sum,
      process_rss_bytes_max = CASE WHEN excluded.process_rss_bytes_max IS NULL THEN system_metrics_hourly.process_rss_bytes_max WHEN system_metrics_hourly.process_rss_bytes_max IS NULL OR excluded.process_rss_bytes_max > system_metrics_hourly.process_rss_bytes_max THEN excluded.process_rss_bytes_max ELSE system_metrics_hourly.process_rss_bytes_max END,
      process_heap_used_bytes_sum = process_heap_used_bytes_sum + excluded.process_heap_used_bytes_sum,
      process_heap_used_bytes_max = CASE WHEN excluded.process_heap_used_bytes_max IS NULL THEN system_metrics_hourly.process_heap_used_bytes_max WHEN system_metrics_hourly.process_heap_used_bytes_max IS NULL OR excluded.process_heap_used_bytes_max > system_metrics_hourly.process_heap_used_bytes_max THEN excluded.process_heap_used_bytes_max ELSE system_metrics_hourly.process_heap_used_bytes_max END,
      event_loop_lag_ms_sum = event_loop_lag_ms_sum + excluded.event_loop_lag_ms_sum,
      event_loop_lag_ms_max = CASE WHEN excluded.event_loop_lag_ms_max IS NULL THEN system_metrics_hourly.event_loop_lag_ms_max WHEN system_metrics_hourly.event_loop_lag_ms_max IS NULL OR excluded.event_loop_lag_ms_max > system_metrics_hourly.event_loop_lag_ms_max THEN excluded.event_loop_lag_ms_max ELSE system_metrics_hourly.event_loop_lag_ms_max END,
      network_rx_bytes_per_sec_sum = network_rx_bytes_per_sec_sum + excluded.network_rx_bytes_per_sec_sum,
      network_rx_bytes_per_sec_max = CASE WHEN excluded.network_rx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_rx_bytes_per_sec_max WHEN system_metrics_hourly.network_rx_bytes_per_sec_max IS NULL OR excluded.network_rx_bytes_per_sec_max > system_metrics_hourly.network_rx_bytes_per_sec_max THEN excluded.network_rx_bytes_per_sec_max ELSE system_metrics_hourly.network_rx_bytes_per_sec_max END,
      network_rx_bytes_per_sec_count = network_rx_bytes_per_sec_count + excluded.network_rx_bytes_per_sec_count,
      network_tx_bytes_per_sec_sum = network_tx_bytes_per_sec_sum + excluded.network_tx_bytes_per_sec_sum,
      network_tx_bytes_per_sec_max = CASE WHEN excluded.network_tx_bytes_per_sec_max IS NULL THEN system_metrics_hourly.network_tx_bytes_per_sec_max WHEN system_metrics_hourly.network_tx_bytes_per_sec_max IS NULL OR excluded.network_tx_bytes_per_sec_max > system_metrics_hourly.network_tx_bytes_per_sec_max THEN excluded.network_tx_bytes_per_sec_max ELSE system_metrics_hourly.network_tx_bytes_per_sec_max END,
      network_tx_bytes_per_sec_count = network_tx_bytes_per_sec_count + excluded.network_tx_bytes_per_sec_count,
      network_rx_total_bytes_max = CASE WHEN excluded.network_rx_total_bytes_max IS NULL THEN system_metrics_hourly.network_rx_total_bytes_max WHEN system_metrics_hourly.network_rx_total_bytes_max IS NULL OR excluded.network_rx_total_bytes_max > system_metrics_hourly.network_rx_total_bytes_max THEN excluded.network_rx_total_bytes_max ELSE system_metrics_hourly.network_rx_total_bytes_max END,
      network_tx_total_bytes_max = CASE WHEN excluded.network_tx_total_bytes_max IS NULL THEN system_metrics_hourly.network_tx_total_bytes_max WHEN system_metrics_hourly.network_tx_total_bytes_max IS NULL OR excluded.network_tx_total_bytes_max > system_metrics_hourly.network_tx_total_bytes_max THEN excluded.network_tx_total_bytes_max ELSE system_metrics_hourly.network_tx_total_bytes_max END,
      db_file_bytes_max = CASE WHEN excluded.db_file_bytes_max IS NULL THEN system_metrics_hourly.db_file_bytes_max WHEN system_metrics_hourly.db_file_bytes_max IS NULL OR excluded.db_file_bytes_max > system_metrics_hourly.db_file_bytes_max THEN excluded.db_file_bytes_max ELSE system_metrics_hourly.db_file_bytes_max END,
      stats_lag_seconds_max = CASE WHEN excluded.stats_lag_seconds_max IS NULL THEN system_metrics_hourly.stats_lag_seconds_max WHEN system_metrics_hourly.stats_lag_seconds_max IS NULL OR excluded.stats_lag_seconds_max > system_metrics_hourly.stats_lag_seconds_max THEN excluded.stats_lag_seconds_max ELSE system_metrics_hourly.stats_lag_seconds_max END,
      updated_at = excluded.updated_at
  `).run(
    statHour,
    input.cpuPercent ?? 0,
    input.cpuPercent ?? null,
    input.memoryUsedPercent ?? 0,
    input.memoryUsedPercent ?? null,
    input.processRssBytes ?? 0,
    input.processRssBytes ?? null,
    input.processHeapUsedBytes ?? 0,
    input.processHeapUsedBytes ?? null,
    input.eventLoopLagMs ?? 0,
    input.eventLoopLagMs ?? null,
    input.networkRxBytesPerSecond ?? 0,
    input.networkRxBytesPerSecond ?? null,
    input.networkRxBytesPerSecond === undefined ? 0 : 1,
    input.networkTxBytesPerSecond ?? 0,
    input.networkTxBytesPerSecond ?? null,
    input.networkTxBytesPerSecond === undefined ? 0 : 1,
    input.networkRxTotalBytes ?? null,
    input.networkTxTotalBytes ?? null,
    input.dbFileBytes ?? null,
    input.statsLagSeconds ?? null,
    updatedAt
  )
}

function updateStatsJobState(database: DatabaseSync, input: { cursorCreatedAt?: string; cursorId?: string; lastSuccessAt?: string; lastErrorMessage?: string; lagSeconds?: number }): void {
  database.prepare(`
    INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
    VALUES ('global', '', 'usage_stats_aggregation', ?, ?, ?, ?, ?, ?)
    ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
      cursor_created_at = COALESCE(excluded.cursor_created_at, stats_job_state.cursor_created_at),
      cursor_id = COALESCE(excluded.cursor_id, stats_job_state.cursor_id),
      last_success_at = COALESCE(excluded.last_success_at, stats_job_state.last_success_at),
      last_error_message = excluded.last_error_message,
      lag_seconds = excluded.lag_seconds,
      updated_at = excluded.updated_at
  `).run(input.cursorCreatedAt ?? null, input.cursorId ?? null, input.lastSuccessAt ?? null, input.lastErrorMessage ?? null, input.lagSeconds ?? 0, nowIso())
}

function statsLagSecondsFromCursor(cursorCreatedAt: string): number {
  const cursorTime = Date.parse(cursorCreatedAt)
  return Number.isFinite(cursorTime) ? Math.max(0, Math.floor((Date.now() - cursorTime) / 1000)) : 0
}
