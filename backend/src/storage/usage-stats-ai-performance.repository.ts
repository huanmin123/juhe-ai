import type { DatabaseSync } from 'node:sqlite'

import type {
  AiPerformanceAccount,
  AiPerformanceAccountOption,
  AiPerformanceOverview,
  AccountUsageStatsRange
} from '../domain/types.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
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
const AI_PERFORMANCE_ACCOUNT_OPTION_DEFAULT_LIMIT = 50
const AI_PERFORMANCE_ACCOUNT_OPTION_MAX_LIMIT = 50

type AiPerformanceScopeType = 'account' | 'caller_account'

interface AiPerformanceScope {
  systemAccountId: string
  scopeType: AiPerformanceScopeType
}

export function getAiPerformanceOverview(access?: AccessScope, range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange(), accountIds: string[] = []): AiPerformanceOverview {
  const database = getStatsDatabase()
  const timezone = usageStatsTimezone()
  const scope = aiPerformanceScope(access)
  const hourBuckets = hourBucketsForRange(range)
  const windowSinceHour = hourBuckets[0] ?? `${range.startDate}T00`
  const windowEndHour = hourBuckets[hourBuckets.length - 1] ?? `${range.endDate}T23`
  const activeSinceHour = hourKey(new Date(Date.now() - 6 * DAY_MS), timezone)
  const selectedAccountIds = uniqueNonEmpty(accountIds).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)

  const defaultRows = loadDefaultAiPerformanceAccounts(database, scope)
  const selectedRows = selectedAccountIds.length
    ? loadSelectedAiPerformanceAccounts(database, scope, activeSinceHour, selectedAccountIds)
    : []
  const defaultIds = new Set(defaultRows.map((row) => row.id))
  const selectedIds = new Set(selectedRows.map((row) => row.id))
  const orderedRows = dedupeAiPerformanceAccountRows([...defaultRows, ...selectedRows])
  const accounts = orderedRows.map((row) => mapAiPerformanceAccount(row, defaultIds, selectedIds))
  const hourlyRows = accounts.length
    ? loadAiPerformanceHourlyRows(database, scope, accounts.map((account) => account.id), windowSinceHour, windowEndHour)
    : []
  const hourlyRowsByAccountHour = new Map(hourlyRows.map((row) => [`${row.scope_id}\n${row.stat_hour}`, row]))
  const summaryRow = loadAiPerformanceSummaryRow(database, scope.systemAccountId, range)

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
  const database = getStatsDatabase()
  const timezone = usageStatsTimezone()
  const scope = aiPerformanceScope(access)
  const activeSinceHour = hourKey(new Date(Date.now() - 6 * DAY_MS), timezone)
  const selectedAccountIds = uniqueNonEmpty(options.accountIds ?? []).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)
  const searchLimit = boundedAccountOptionLimit(options.limit)
  const searchRows = loadAiPerformanceAccountOptionRows(database, scope, activeSinceHour, {
    keyword: options.keyword?.trim(),
    limit: searchLimit
  })
  const selectedRows = selectedAccountIds.length
    ? loadSelectedAiPerformanceAccounts(database, scope, activeSinceHour, selectedAccountIds)
    : []
  const rows = dedupeAiPerformanceAccountRows([...searchRows, ...selectedRows])
  return rows.map((row) => ({
    id: row.id,
    name: row.name,
    status: row.status,
    providerCode: row.provider_code,
    systemAccountId: row.system_account_id,
    systemAccountName: row.system_account_name ?? undefined,
    ownerSystemAccountId: row.owner_system_account_id,
    ownerSystemAccountName: row.owner_system_account_name ?? undefined,
    accessType: row.access_type,
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

function aiPerformanceScope(access?: AccessScope): AiPerformanceScope {
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) {
    return { systemAccountId: scopedId, scopeType: 'caller_account' }
  }
  if (canAccessAll(access)) {
    return { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'account' }
  }
  return { systemAccountId: currentSystemAccountId(access), scopeType: 'caller_account' }
}

interface AiPerformanceAccountRow {
  id: string
  name: string
  status: AiPerformanceAccount['status']
  provider_code: string
  system_account_id: string
  system_account_name: string | null
  owner_system_account_id: string
  owner_system_account_name: string | null
  access_type: 'owner' | 'authorized'
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

function loadDefaultAiPerformanceAccounts(database: DatabaseSync, scope: AiPerformanceScope, limit = 10): AiPerformanceAccountRow[] {
  return loadDefaultAiPerformanceAccountsFromRankSnapshot(database, scope, limit)
}

function loadDefaultAiPerformanceAccountsFromRankSnapshot(database: DatabaseSync, scope: AiPerformanceScope, limit: number): AiPerformanceAccountRow[] {
  const rows = database.prepare(`
    SELECT scope_id, metric_value AS request_count_last_7d, snapshot_at AS last_stat_hour, rank
    FROM usage_rank_snapshots
    WHERE system_account_id = ?
      AND scope_type = ?
      AND window_key = 'last7d'
      AND metric = 'request_count'
      AND snapshot_at = (
        SELECT MAX(snapshot_at)
        FROM usage_rank_snapshots
        WHERE system_account_id = ?
          AND scope_type = ?
          AND window_key = 'last7d'
          AND metric = 'request_count'
      )
    ORDER BY rank ASC
    LIMIT ?
  `).all(scope.systemAccountId, scope.scopeType, scope.systemAccountId, scope.scopeType, limit) as unknown as Array<{ scope_id: string; request_count_last_7d: number; last_stat_hour: string | null; rank: number }>
  return mergeAiPerformanceStatsWithAccounts(rows.map((row) => ({
    id: row.scope_id,
    requestCountLast7d: Number(row.request_count_last_7d ?? 0),
    lastStatHour: row.last_stat_hour ?? null,
    rank: Number(row.rank ?? 0)
  })), scope)
}

function loadSelectedAiPerformanceAccounts(database: DatabaseSync, scope: AiPerformanceScope, activeSinceHour: string, accountIds: string[]): AiPerformanceAccountRow[] {
  void activeSinceHour
  const rows = loadUsageRankMetricsByScopeIds(database, scope.systemAccountId, scope.scopeType, 'last7d', 'request_count', accountIds)
  const merged = mergeAiPerformanceStatsWithAccounts(accountIds.map((id) => {
    const row = rows.get(id)
    return {
      id,
      requestCountLast7d: Number(row?.metricValue ?? 0),
      lastStatHour: row?.snapshotAt ?? null
    }
  }), scope)
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

function loadAiPerformanceHourlyRows(database: DatabaseSync, scope: AiPerformanceScope, accountIds: string[], sinceHour: string, endHour: string): AiPerformanceHourlyRow[] {
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
    FROM usage_stats_hourly INDEXED BY idx_usage_stats_hourly_scope_hour
    WHERE system_account_id = ?
      AND scope_type = ?
      AND scope_id IN (${placeholders})
      AND stat_hour >= ?
      AND stat_hour <= ?
  `).all(scope.systemAccountId, scope.scopeType, ...accountIds, sinceHour, endHour) as unknown as AiPerformanceHourlyRow[]
}

function loadAiPerformanceAccountOptionRows(
  database: DatabaseSync,
  scope: AiPerformanceScope,
  activeSinceHour: string,
  options: { keyword?: string; limit: number }
): AiPerformanceAccountRow[] {
  const keyword = options.keyword?.trim()
  if (!keyword) {
    return loadDefaultAiPerformanceAccounts(database, scope, options.limit)
  }

  const keywordPrefix = `${escapeLikePrefix(keyword)}%`
  const visibleFilter = aiPerformanceVisibleAccountFilter(scope)
  const accountRows = getBusinessDatabase().prepare(`
    SELECT accounts.id
    FROM accounts
    WHERE (accounts.name COLLATE NOCASE = ? OR accounts.name LIKE ? ESCAPE '\\')
      ${visibleFilter.sql}
    ORDER BY accounts.name COLLATE NOCASE ASC, accounts.id ASC
    LIMIT ?
  `).all(keyword, keywordPrefix, ...visibleFilter.params, options.limit) as unknown as Array<{ id: string }>
  const sourceInstanceParams = scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? [] : [scope.systemAccountId]
  const sourceInstanceRows = getBusinessDatabase().prepare(`
    SELECT instance_accounts.id
    FROM accounts source_accounts
    INNER JOIN accounts instance_accounts
      ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
    WHERE (source_accounts.name COLLATE NOCASE = ? OR source_accounts.name LIKE ? ESCAPE '\\')
      ${scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? '' : 'AND instance_accounts.system_account_id = ?'}
    ORDER BY source_accounts.name COLLATE NOCASE ASC, instance_accounts.id ASC
    LIMIT ?
  `).all(keyword, keywordPrefix, ...sourceInstanceParams, options.limit) as unknown as Array<{ id: string }>
  const accountIds = uniqueNonEmpty([
    ...accountRows.map((row) => row.id),
    ...sourceInstanceRows.map((row) => row.id)
  ]).slice(0, options.limit)
  return accountIds.length
    ? loadSelectedAiPerformanceAccounts(database, scope, activeSinceHour, accountIds)
    : []
}

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

function mergeAiPerformanceStatsWithAccounts(
  statsRows: Array<{ id: string; requestCountLast7d: number; lastStatHour: string | null; rank?: number }>,
  scope: AiPerformanceScope
): AiPerformanceAccountRow[] {
  const ids = [...new Set(statsRows.map((row) => row.id).filter(Boolean))]
  if (!ids.length) return []
  const placeholders = sqlPlaceholders(ids.length)
  const visibleFilter = aiPerformanceVisibleAccountFilter(scope)
  const ownerSystemAccountExpression = `CASE
      WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      THEN COALESCE(accounts.authorization_instance_owner_system_account_id, instance_authorizations.resource_owner_system_account_id, accounts.system_account_id)
      ELSE accounts.system_account_id
    END`
  const accessTypeExpression = scope.scopeType === 'caller_account' && scope.systemAccountId !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID
    ? `CASE
      WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized'
      WHEN accounts.system_account_id = ? THEN 'owner'
      ELSE 'authorized'
    END`
    : "'owner'"
  const accessTypeParams = scope.scopeType === 'caller_account' && scope.systemAccountId !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? [scope.systemAccountId] : []
  const accounts = getBusinessDatabase().prepare(`
    SELECT
      accounts.id,
      accounts.name,
      accounts.status,
      accounts.provider_code,
      accounts.system_account_id,
      system_accounts.display_name AS system_account_name,
      ${ownerSystemAccountExpression} AS owner_system_account_id,
      owner_system_accounts.display_name AS owner_system_account_name,
      ${accessTypeExpression} AS access_type
    FROM accounts
    LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id
    LEFT JOIN resource_authorizations instance_authorizations
      ON instance_authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN system_accounts owner_system_accounts
      ON owner_system_accounts.id = ${ownerSystemAccountExpression}
    WHERE accounts.id IN (${placeholders})
      ${visibleFilter.sql}
  `).all(...accessTypeParams, ...ids, ...visibleFilter.params) as unknown as Array<{
    id: string
    name: string
    status: AiPerformanceAccount['status']
    provider_code: string
    system_account_id: string
    system_account_name: string | null
    owner_system_account_id: string
    owner_system_account_name: string | null
    access_type: 'owner' | 'authorized'
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
    ownerSystemAccountId: row.owner_system_account_id,
    ownerSystemAccountName: row.owner_system_account_name ?? undefined,
    accessType: row.access_type,
    requestCountLast7d: Number(row.request_count_last_7d ?? 0),
    selected: selectedIds.has(row.id),
    defaultVisible: defaultIds.has(row.id)
  }
}

function aiPerformanceVisibleAccountFilter(scope: AiPerformanceScope): { sql: string; params: string[] } {
  if (scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID) {
    return { sql: '', params: [] }
  }
  const now = nowIso()
  return {
    sql: `AND (
      accounts.system_account_id = ?
      OR EXISTS (
        SELECT 1
        FROM group_accounts visible_group_accounts
        INNER JOIN resource_authorizations visible_group_authorization_rows
          ON visible_group_authorization_rows.resource_type = 'group'
          AND visible_group_authorization_rows.resource_id = visible_group_accounts.group_id
          AND visible_group_authorization_rows.grantee_system_account_id = ?
          AND visible_group_authorization_rows.status = 'active'
          AND (visible_group_authorization_rows.expires_at IS NULL OR visible_group_authorization_rows.expires_at > ?)
        WHERE visible_group_accounts.account_id = accounts.id
          AND visible_group_accounts.enabled = 1
      )
    )`,
    params: [scope.systemAccountId, scope.systemAccountId, now]
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
