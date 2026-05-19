import type { DatabaseSync } from 'node:sqlite'

import type {
  AiPerformanceAccount,
  AiPerformanceAccountOption,
  AiPerformanceOverview,
  AccountUsageStatsRange
} from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, getRecordDatabase } from './database.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { averageFromSum, hourKey, usageStatsTimezone } from './usage-stats-helpers.js'
import { latestUsageStatsLagSeconds, normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'
import {
  GLOBAL_STATS_SYSTEM_ACCOUNT_ID
} from './usage-stats-types.js'
import {
  DAY_MS,
  hourBucketsForRange,
  rangeWindowKey
} from './usage-stats-window-helpers.js'

const AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT = 20
const AI_PERFORMANCE_ACCOUNT_OPTION_DEFAULT_LIMIT = 30
const AI_PERFORMANCE_ACCOUNT_OPTION_MAX_LIMIT = 50

export function getAiPerformanceOverview(access?: AccessScope, range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange(), accountIds: string[] = []): AiPerformanceOverview {
  const database = getRecordDatabase()
  const timezone = usageStatsTimezone()
  const systemAccountId = aiPerformanceSystemAccountId(access)
  const hourBuckets = hourBucketsForRange(range)
  const windowSinceHour = hourBuckets[0] ?? `${range.startDate}T00`
  const windowEndHour = hourBuckets[hourBuckets.length - 1] ?? `${range.endDate}T23`
  const activeSinceHour = hourKey(new Date(Date.now() - 6 * DAY_MS), timezone)
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
    ? loadAiPerformanceHourlyRows(database, systemAccountId, accounts.map((account) => account.id), windowSinceHour, windowEndHour)
    : []
  const hourlyRowsByAccountHour = new Map(hourlyRows.map((row) => [`${row.scope_id}\n${row.stat_hour}`, row]))
  const summaryRow = loadAiPerformanceSummaryRow(database, systemAccountId, range)

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
    range,
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

export function listAiPerformanceAccountOptions(
  access?: AccessScope,
  options: { keyword?: string; accountIds?: string[]; limit?: number } = {}
): AiPerformanceAccountOption[] {
  const database = getRecordDatabase()
  const timezone = usageStatsTimezone()
  const systemAccountId = aiPerformanceSystemAccountId(access)
  const activeSinceHour = hourKey(new Date(Date.now() - 6 * DAY_MS), timezone)
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
    systemAccountName: row.system_account_name ?? undefined,
    requestCountLast7d: Number(row.request_count_last_7d ?? 0)
  }))
}

function loadAiPerformanceSummaryRow(database: DatabaseSync, systemAccountId: string, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): {
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
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `).get(systemAccountId, rangeWindowKey(range), range.startDate, range.endDate) as unknown as {
    request_count: number
    first_token_ms_sum: number
    first_token_ms_count: number
    first_token_ms_max: number
    duration_ms_sum: number
    duration_ms_count: number
    duration_ms_max: number
  } | undefined
}

function aiPerformanceSystemAccountId(access?: AccessScope): string {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) return scopedId
  if (canAccessAll(access)) return GLOBAL_STATS_SYSTEM_ACCOUNT_ID
  return currentSystemAccountId(access)
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

function loadAiPerformanceHourlyRows(database: DatabaseSync, systemAccountId: string, accountIds: string[], sinceHour: string, endHour: string): AiPerformanceHourlyRow[] {
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
      AND stat_hour <= ?
    ORDER BY stat_hour ASC
  `).all(systemAccountId, ...accountIds, sinceHour, endHour) as unknown as AiPerformanceHourlyRow[]
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

  const keywordPrefix = `${keyword}%`
  const ownerIds = loadAiPerformanceAccountOptionOwnerIds(systemAccountId, keyword, options.limit)
  const keywordClauses = [
    'accounts.id = ?',
    'accounts.id LIKE ?',
    'accounts.name LIKE ?',
    'accounts.provider_code = ?',
    'accounts.provider_code LIKE ?'
  ]
  const keywordParams: string[] = [keyword, keywordPrefix, keywordPrefix, keyword, keywordPrefix]
  if (ownerIds.length) {
    keywordClauses.push(`accounts.system_account_id IN (${sqlPlaceholders(ownerIds.length)})`)
    keywordParams.push(...ownerIds)
  }
  const systemAccountWhere = systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? '' : 'AND accounts.system_account_id = ?'
  const systemAccountParams = systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? [] : [systemAccountId]
  const accountRows = getDatabase().prepare(`
    SELECT accounts.id
    FROM accounts
    WHERE (${keywordClauses.join(' OR ')})
      ${systemAccountWhere}
    ORDER BY lower(accounts.name) ASC, accounts.id ASC
    LIMIT ?
  `).all(...keywordParams, ...systemAccountParams, options.limit) as unknown as Array<{ id: string }>
  const accountIds = accountRows.map((row) => row.id)
  return accountIds.length
    ? loadSelectedAiPerformanceAccounts(database, systemAccountId, activeSinceHour, accountIds)
    : []
}

function loadAiPerformanceAccountOptionOwnerIds(systemAccountId: string, keyword: string, limit: number): string[] {
  const keywordPrefix = `${keyword}%`
  const database = getDatabase()
  if (systemAccountId !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID) {
    const row = database.prepare(`
      SELECT id
      FROM system_accounts
      WHERE id = ?
        AND (username = ? OR username LIKE ? OR display_name LIKE ?)
      LIMIT 1
    `).get(systemAccountId, keyword, keywordPrefix, keywordPrefix) as unknown as { id?: string } | undefined
    return row?.id ? [row.id] : []
  }
  const rows = database.prepare(`
    SELECT id
    FROM system_accounts
    WHERE username = ? OR username LIKE ? OR display_name LIKE ?
    ORDER BY lower(display_name) ASC, id ASC
    LIMIT ?
  `).all(keyword, keywordPrefix, keywordPrefix, limit) as unknown as Array<{ id?: string }>
  return rows.map((row) => row.id).filter((id): id is string => Boolean(id))
}

function mergeAiPerformanceStatsWithAccounts(
  statsRows: Array<{ id: string; requestCountLast7d: number; lastStatHour: string | null; rank?: number }>,
  systemAccountId: string
): AiPerformanceAccountRow[] {
  const ids = [...new Set(statsRows.map((row) => row.id).filter(Boolean))]
  if (!ids.length) return []
  const placeholders = sqlPlaceholders(ids.length)
  const systemAccountWhere = systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? '' : 'AND accounts.system_account_id = ?'
  const systemAccountParams = systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? [] : [systemAccountId]
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
    WHERE accounts.id IN (${placeholders})
      ${systemAccountWhere}
  `).all(...ids, ...systemAccountParams) as unknown as Array<{
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

function maxFromCountedMetric(value: unknown, count: number): number | undefined {
  const number = Number(value ?? 0)
  return count > 0 && Number.isFinite(number) ? Math.max(0, Math.round(number)) : undefined
}
