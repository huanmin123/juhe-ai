import type { DatabaseSync } from 'node:sqlite'

import type {
  AiPerformanceAccount,
  AiPerformanceAccountOption,
  AiPerformanceBase,
  AiPerformanceOverview,
  AiPerformanceSeries,
  AiPerformanceSummary,
  AccountUsageStatsRange
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, getStatsDatabase, nowIso } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { averageFromSum, hourKey, usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { normalizeDefaultUsageStatsRange } from './usage-stats-runtime-helpers.js'
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
  includeSystemAccountName: boolean
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
  const accounts = orderedRows.map((row) => mapAiPerformanceAccount(row, scope))
  const hourlyRows = accounts.length
    ? loadAiPerformanceHourlyRows(database, scope, accounts.map((account) => account.id), windowSinceHour, windowEndHour)
    : []
  const hourlyRowsByAccountHour = new Map(hourlyRows.map((row) => [`${row.scope_id}\n${row.stat_hour}`, row]))
  const summaryRow = loadAiPerformanceSummaryRow(database, scope.systemAccountId, range)

  const hourlySeries = accounts.map((account) => ({
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    points: hourBuckets.map((statHour) => {
      const row = hourlyRowsByAccountHour.get(`${account.id}\n${statHour}`)
      const requestCount = Number(row?.request_count ?? 0)
      const firstTokenCount = Number(row?.first_token_ms_count ?? 0)
      const durationCount = Number(row?.duration_ms_count ?? 0)
      return {
        statHour,
        requestCount,
        averageFirstTokenMs: averageFromSum(row?.first_token_ms_sum, row?.first_token_ms_count),
        maxFirstTokenMs: maxFromCountedMetric(row?.first_token_ms_max, firstTokenCount),
        averageDurationMs: averageFromSum(row?.duration_ms_sum, row?.duration_ms_count),
        maxDurationMs: maxFromCountedMetric(row?.duration_ms_max, durationCount)
      }
    })
  }))

  return {
    range,
    defaultAccounts: accounts.filter((account) => defaultIds.has(account.id)),
    selectedAccounts: accounts.filter((account) => selectedIds.has(account.id)),
    accounts,
    hourlySeries,
    summary: {
      requestCount: Number(summaryRow?.request_count ?? 0),
      averageFirstTokenMs: averageFromSum(summaryRow?.first_token_ms_sum, summaryRow?.first_token_ms_count),
      maxFirstTokenMs: maxFromCountedMetric(summaryRow?.first_token_ms_max, Number(summaryRow?.first_token_ms_count ?? 0)),
      averageDurationMs: averageFromSum(summaryRow?.duration_ms_sum, summaryRow?.duration_ms_count),
      maxDurationMs: maxFromCountedMetric(summaryRow?.duration_ms_max, Number(summaryRow?.duration_ms_count ?? 0))
    }
  }
}

export async function getAiPerformanceOverviewAsync(access?: AccessScope, range?: AccountUsageStatsRange, accountIds: string[] = []): Promise<AiPerformanceOverview> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_ai_performance_overview_read_only',
      access,
      range,
      accountIds
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAiPerformanceOverview(access, range, accountIds)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const timezone = await usageStatsTimezoneAsync()
  const normalizedRange = range ?? normalizeDefaultUsageStatsRange(timezone)
  const scope = aiPerformanceScope(access)
  const hourBuckets = hourBucketsForRange(normalizedRange)
  const windowSinceHour = hourBuckets[0] ?? `${normalizedRange.startDate}T00`
  const windowEndHour = hourBuckets[hourBuckets.length - 1] ?? `${normalizedRange.endDate}T23`
  const activeSinceHour = hourKey(new Date(Date.now() - 6 * DAY_MS), timezone)
  const selectedAccountIds = uniqueNonEmpty(accountIds).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)

  const [defaultRows, selectedRows] = await Promise.all([
    loadDefaultAiPerformanceAccountsAsync(client, scope),
    selectedAccountIds.length
      ? loadSelectedAiPerformanceAccountsAsync(client, scope, activeSinceHour, selectedAccountIds)
      : Promise.resolve([])
  ])
  const defaultIds = new Set(defaultRows.map((row) => row.id))
  const selectedIds = new Set(selectedRows.map((row) => row.id))
  const orderedRows = dedupeAiPerformanceAccountRows([...defaultRows, ...selectedRows])
  const accounts = orderedRows.map((row) => mapAiPerformanceAccount(row, scope))
  const [hourlyRows, summaryRow] = await Promise.all([
    accounts.length
      ? loadAiPerformanceHourlyRowsAsync(client, scope, accounts.map((account) => account.id), windowSinceHour, windowEndHour)
      : Promise.resolve([]),
    loadAiPerformanceSummaryRowAsync(client, scope.systemAccountId, normalizedRange)
  ])
  const hourlyRowsByAccountHour = new Map(hourlyRows.map((row) => [`${row.scope_id}\n${row.stat_hour}`, row]))
  const hourlySeries = accounts.map((account) => ({
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    points: hourBuckets.map((statHour) => {
      const row = hourlyRowsByAccountHour.get(`${account.id}\n${statHour}`)
      const requestCount = Number(row?.request_count ?? 0)
      const firstTokenCount = Number(row?.first_token_ms_count ?? 0)
      const durationCount = Number(row?.duration_ms_count ?? 0)
      return {
        statHour,
        requestCount,
        averageFirstTokenMs: averageFromSum(row?.first_token_ms_sum, row?.first_token_ms_count),
        maxFirstTokenMs: maxFromCountedMetric(row?.first_token_ms_max, firstTokenCount),
        averageDurationMs: averageFromSum(row?.duration_ms_sum, row?.duration_ms_count),
        maxDurationMs: maxFromCountedMetric(row?.duration_ms_max, durationCount)
      }
    })
  }))

  return {
    range: normalizedRange,
    defaultAccounts: accounts.filter((account) => defaultIds.has(account.id)),
    selectedAccounts: accounts.filter((account) => selectedIds.has(account.id)),
    accounts,
    hourlySeries,
    summary: {
      requestCount: Number(summaryRow?.request_count ?? 0),
      averageFirstTokenMs: averageFromSum(summaryRow?.first_token_ms_sum, summaryRow?.first_token_ms_count),
      maxFirstTokenMs: maxFromCountedMetric(summaryRow?.first_token_ms_max, Number(summaryRow?.first_token_ms_count ?? 0)),
      averageDurationMs: averageFromSum(summaryRow?.duration_ms_sum, summaryRow?.duration_ms_count),
      maxDurationMs: maxFromCountedMetric(summaryRow?.duration_ms_max, Number(summaryRow?.duration_ms_count ?? 0))
    }
  }
}

export function getAiPerformanceBase(
  access?: AccessScope,
  range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange()
): AiPerformanceBase {
  const database = getStatsDatabase()
  const scope = aiPerformanceScope(access)
  const hourBuckets = hourBucketsForRange(range)
  const defaultRows = loadDefaultAiPerformanceAccounts(database, scope, 10)
  const accounts = defaultRows.map((row) => mapAiPerformanceAccount(row, scope))
  const hourlyRows = accounts.length
    ? loadAiPerformanceHourlyRows(database, scope, accounts.map((account) => account.id), firstHour(range, hourBuckets), lastHour(range, hourBuckets))
    : []
  return {
    range,
    summary: mapAiPerformanceSummary(loadAiPerformanceSummaryRow(database, scope.systemAccountId, range)),
    accounts,
    hourlySeries: mapAiPerformanceHourlySeries(accounts, hourBuckets, hourlyRows)
  }
}

export async function getAiPerformanceBaseAsync(
  access?: AccessScope,
  range?: AccountUsageStatsRange
): Promise<AiPerformanceBase> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_ai_performance_base_read_only',
      access,
      range
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAiPerformanceBase(access, range)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const timezone = await usageStatsTimezoneAsync()
  const normalizedRange = range ?? normalizeDefaultUsageStatsRange(timezone)
  const scope = aiPerformanceScope(access)
  const hourBuckets = hourBucketsForRange(normalizedRange)
  const defaultRows = await loadDefaultAiPerformanceAccountsAsync(client, scope, 10)
  const accounts = defaultRows.map((row) => mapAiPerformanceAccount(row, scope))
  const [hourlyRows, summaryRow] = await Promise.all([
    accounts.length
      ? loadAiPerformanceHourlyRowsAsync(client, scope, accounts.map((account) => account.id), firstHour(normalizedRange, hourBuckets), lastHour(normalizedRange, hourBuckets))
      : Promise.resolve([]),
    loadAiPerformanceSummaryRowAsync(client, scope.systemAccountId, normalizedRange)
  ])
  return {
    range: normalizedRange,
    summary: mapAiPerformanceSummary(summaryRow),
    accounts,
    hourlySeries: mapAiPerformanceHourlySeries(accounts, hourBuckets, hourlyRows)
  }
}

export function getAiPerformanceSeries(
  access: AccessScope | undefined,
  range: AccountUsageStatsRange = normalizeDefaultUsageStatsRange(),
  accountIds: string[]
): AiPerformanceSeries {
  const database = getStatsDatabase()
  const scope = aiPerformanceScope(access)
  const hourBuckets = hourBucketsForRange(range)
  const selectedAccountIds = uniqueNonEmpty(accountIds).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)
  const selectedRows = loadExplicitAiPerformanceAccounts(scope, selectedAccountIds)
  const accounts = selectedRows.map((row) => mapAiPerformanceAccount(row, scope))
  const hourlyRows = accounts.length
    ? loadAiPerformanceHourlyRows(database, scope, accounts.map((account) => account.id), firstHour(range, hourBuckets), lastHour(range, hourBuckets))
    : []
  return {
    range,
    accounts,
    hourlySeries: mapAiPerformanceHourlySeries(accounts, hourBuckets, hourlyRows)
  }
}

export async function getAiPerformanceSeriesAsync(
  access: AccessScope | undefined,
  range: AccountUsageStatsRange | undefined,
  accountIds: string[]
): Promise<AiPerformanceSeries> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_ai_performance_series_read_only',
      access,
      range,
      accountIds
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAiPerformanceSeries(access, range, accountIds)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const timezone = await usageStatsTimezoneAsync()
  const normalizedRange = range ?? normalizeDefaultUsageStatsRange(timezone)
  const scope = aiPerformanceScope(access)
  const hourBuckets = hourBucketsForRange(normalizedRange)
  const selectedAccountIds = uniqueNonEmpty(accountIds).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)
  const selectedRows = await loadExplicitAiPerformanceAccountsAsync(client, scope, selectedAccountIds)
  const accounts = selectedRows.map((row) => mapAiPerformanceAccount(row, scope))
  const hourlyRows = accounts.length
    ? await loadAiPerformanceHourlyRowsAsync(client, scope, accounts.map((account) => account.id), firstHour(normalizedRange, hourBuckets), lastHour(normalizedRange, hourBuckets))
    : []
  return {
    range: normalizedRange,
    accounts,
    hourlySeries: mapAiPerformanceHourlySeries(accounts, hourBuckets, hourlyRows)
  }
}

export function listAiPerformanceAccountOptions(
  access?: AccessScope,
  options: { keyword?: string; accountIds?: string[]; limit?: number } = {}
): AiPerformanceAccountOption[] {
  const database = getStatsDatabase()
  const scope = aiPerformanceScope(access)
  const selectedAccountIds = uniqueNonEmpty(options.accountIds ?? []).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)
  const searchLimit = boundedAccountOptionLimit(options.limit)
  const searchRows = loadAiPerformanceAccountOptionRows(database, scope, {
    keyword: options.keyword?.trim(),
    limit: searchLimit
  })
  const selectedRows = selectedAccountIds.length
    ? loadExplicitAiPerformanceAccounts(scope, selectedAccountIds)
    : []
  const rows = dedupeAiPerformanceAccountRows([...searchRows, ...selectedRows])
  return rows.map((row) => mapAiPerformanceAccount(row, scope))
}

export async function listAiPerformanceAccountOptionsAsync(
  access?: AccessScope,
  options: { keyword?: string; accountIds?: string[]; limit?: number } = {}
): Promise<AiPerformanceAccountOption[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_ai_performance_account_options_read_only',
      access,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAiPerformanceAccountOptions(access, options)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const scope = aiPerformanceScope(access)
  const selectedAccountIds = uniqueNonEmpty(options.accountIds ?? []).slice(0, AI_PERFORMANCE_SELECTED_ACCOUNT_LIMIT)
  const searchLimit = boundedAccountOptionLimit(options.limit)
  const rows = await loadAiPerformanceAccountOptionRowsAsync(client, scope, {
    keyword: options.keyword?.trim(),
    limit: searchLimit,
    selectedAccountIds
  })
  return rows.map((row) => mapAiPerformanceAccount(row, scope))
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

function loadAiPerformanceSummaryRowAsync(client: DatabaseClient, systemAccountId: string, range: Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Promise<{
  request_count: number
  first_token_ms_sum: number
  first_token_ms_count: number
  first_token_ms_max: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
} | undefined> {
  return client.one(`
    SELECT request_count, first_token_ms_sum, first_token_ms_count, first_token_ms_max, duration_ms_sum, duration_ms_count, duration_ms_max
    FROM ${statsTable(client, 'ai_performance_summary_windows')}
    WHERE system_account_id = ? AND window_key = ? AND start_date = ? AND end_date = ?
  `, [systemAccountId, rangeWindowKey(range), range.startDate, range.endDate])
}

function aiPerformanceScope(access?: AccessScope): AiPerformanceScope {
  const includeSystemAccountName = canAccessAll(access)
  const scopedId = scopedSystemAccountId(access)
  if (scopedId) {
    return { systemAccountId: scopedId, scopeType: 'caller_account', includeSystemAccountName }
  }
  if (includeSystemAccountName) {
    return { systemAccountId: GLOBAL_STATS_SYSTEM_ACCOUNT_ID, scopeType: 'account', includeSystemAccountName }
  }
  return { systemAccountId: currentSystemAccountId(access), scopeType: 'caller_account', includeSystemAccountName }
}

interface AiPerformanceAccountRow {
  id: string
  name: string
  provider_code: string
  system_account_name: string | null
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

function loadDefaultAiPerformanceAccountsAsync(client: DatabaseClient, scope: AiPerformanceScope, limit = 10): Promise<AiPerformanceAccountRow[]> {
  return loadDefaultAiPerformanceAccountsFromRankSnapshotAsync(client, scope, limit)
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

async function loadDefaultAiPerformanceAccountsFromRankSnapshotAsync(client: DatabaseClient, scope: AiPerformanceScope, limit: number): Promise<AiPerformanceAccountRow[]> {
  return mergeAiPerformanceStatsWithAccountsAsync(
    client,
    await loadDefaultAiPerformanceAccountCandidatesAsync(client, scope, limit),
    scope
  )
}

async function loadDefaultAiPerformanceAccountCandidatesAsync(
  client: DatabaseClient,
  scope: AiPerformanceScope,
  limit: number
): Promise<Array<{ id: string; requestCountLast7d: number; lastStatHour: string | null; rank: number }>> {
  const rows = await client.query<{ scope_id: string; request_count_last_7d: number | string; last_stat_hour: string | null; rank: number | string }>(`
    SELECT scope_id, metric_value AS request_count_last_7d, snapshot_at AS last_stat_hour, rank
    FROM ${statsTable(client, 'usage_rank_snapshots')}
    WHERE system_account_id = ?
      AND scope_type = ?
      AND window_key = 'last7d'
      AND metric = 'request_count'
      AND snapshot_at = (
        SELECT MAX(snapshot_at)
        FROM ${statsTable(client, 'usage_rank_snapshots')}
        WHERE system_account_id = ?
          AND scope_type = ?
          AND window_key = 'last7d'
          AND metric = 'request_count'
      )
    ORDER BY rank ASC
    LIMIT ?
  `, [scope.systemAccountId, scope.scopeType, scope.systemAccountId, scope.scopeType, limit])
  return rows.map((row) => ({
    id: row.scope_id,
    requestCountLast7d: Number(row.request_count_last_7d ?? 0),
    lastStatHour: row.last_stat_hour ?? null,
    rank: Number(row.rank ?? 0)
  }))
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

function loadExplicitAiPerformanceAccounts(scope: AiPerformanceScope, accountIds: string[]): AiPerformanceAccountRow[] {
  const merged = mergeAiPerformanceStatsWithAccounts(accountIds.map((id) => ({
    id,
    requestCountLast7d: 0,
    lastStatHour: null
  })), scope)
  const order = new Map(accountIds.map((id, index) => [id, index]))
  return merged.sort((left, right) => (order.get(left.id) ?? 0) - (order.get(right.id) ?? 0))
}

async function loadSelectedAiPerformanceAccountsAsync(client: DatabaseClient, scope: AiPerformanceScope, activeSinceHour: string, accountIds: string[]): Promise<AiPerformanceAccountRow[]> {
  void activeSinceHour
  const rows = await loadUsageRankMetricsByScopeIdsAsync(client, scope.systemAccountId, scope.scopeType, 'last7d', 'request_count', accountIds)
  const merged = await mergeAiPerformanceStatsWithAccountsAsync(client, accountIds.map((id) => {
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

async function loadExplicitAiPerformanceAccountsAsync(client: DatabaseClient, scope: AiPerformanceScope, accountIds: string[]): Promise<AiPerformanceAccountRow[]> {
  const merged = await mergeAiPerformanceStatsWithAccountsAsync(client, accountIds.map((id) => ({
    id,
    requestCountLast7d: 0,
    lastStatHour: null
  })), scope)
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

async function loadUsageRankMetricsByScopeIdsAsync(
  client: DatabaseClient,
  systemAccountId: string,
  scopeType: string,
  windowKey: string,
  metric: string,
  scopeIds: string[]
): Promise<Map<string, { metricValue: number; snapshotAt: string | null }>> {
  const result = new Map<string, { metricValue: number; snapshotAt: string | null }>()
  const uniqueIds = [...new Set(scopeIds.filter(Boolean))]
  if (!uniqueIds.length) return result
  for (const idChunk of chunkValues(uniqueIds, 400)) {
    const rows = await client.query<{ scope_id: string; metric_value: number | string; snapshot_at: string | null }>(`
      SELECT scope_id, metric_value, snapshot_at
      FROM ${statsTable(client, 'usage_rank_snapshots')}
      WHERE system_account_id = ?
        AND scope_type = ?
        AND window_key = ?
        AND metric = ?
        AND scope_id IN (${client.dialect.bindPlaceholders(idChunk.length)})
        AND snapshot_at = (
          SELECT MAX(snapshot_at)
          FROM ${statsTable(client, 'usage_rank_snapshots')}
          WHERE system_account_id = ?
            AND scope_type = ?
            AND window_key = ?
            AND metric = ?
        )
    `, [systemAccountId, scopeType, windowKey, metric, ...idChunk, systemAccountId, scopeType, windowKey, metric])
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

function loadAiPerformanceHourlyRowsAsync(client: DatabaseClient, scope: AiPerformanceScope, accountIds: string[], sinceHour: string, endHour: string): Promise<AiPerformanceHourlyRow[]> {
  const placeholders = client.dialect.bindPlaceholders(accountIds.length)
  return client.query<AiPerformanceHourlyRow>(`
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
    FROM ${statsTable(client, 'usage_stats_hourly')}
    WHERE system_account_id = ?
      AND scope_type = ?
      AND scope_id IN (${placeholders})
      AND stat_hour >= ?
      AND stat_hour <= ?
  `, [scope.systemAccountId, scope.scopeType, ...accountIds, sinceHour, endHour])
}

function loadAiPerformanceAccountOptionRows(
  database: DatabaseSync,
  scope: AiPerformanceScope,
  options: { keyword?: string; limit: number }
): AiPerformanceAccountRow[] {
  const keyword = options.keyword?.trim()
  if (!keyword) {
    return loadDefaultAiPerformanceAccounts(database, scope, options.limit)
  }

  const keywordPrefix = normalizeAccountNamePrefix(keyword)
  const visibleFilter = aiPerformanceVisibleAccountFilter(scope)
  const accountRows = getBusinessDatabase().prepare(`
    SELECT accounts.id
    FROM accounts
    WHERE accounts.deleted_at IS NULL
      AND accounts.name >= ? AND accounts.name < ?
      ${visibleFilter.sql}
    ORDER BY accounts.name ASC, accounts.id ASC
    LIMIT ?
  `).all(keywordPrefix.start, keywordPrefix.end, ...visibleFilter.params, options.limit) as unknown as Array<{ id: string }>
  const sourceInstanceParams = scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? [] : [scope.systemAccountId]
  const sourceInstanceRows = getBusinessDatabase().prepare(`
    SELECT instance_accounts.id
    FROM accounts source_accounts
    INNER JOIN accounts instance_accounts
      ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
    WHERE source_accounts.deleted_at IS NULL
      AND instance_accounts.deleted_at IS NULL
      AND source_accounts.name >= ? AND source_accounts.name < ?
      ${scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? '' : 'AND instance_accounts.system_account_id = ?'}
    ORDER BY source_accounts.name ASC, instance_accounts.id ASC
    LIMIT ?
  `).all(keywordPrefix.start, keywordPrefix.end, ...sourceInstanceParams, options.limit) as unknown as Array<{ id: string }>
  const accountIds = uniqueNonEmpty([
    ...accountRows.map((row) => row.id),
    ...sourceInstanceRows.map((row) => row.id)
  ]).slice(0, options.limit)
  return accountIds.length
    ? loadExplicitAiPerformanceAccounts(scope, accountIds)
    : []
}

async function loadAiPerformanceAccountOptionRowsAsync(
  client: DatabaseClient,
  scope: AiPerformanceScope,
  options: { keyword?: string; limit: number; selectedAccountIds: string[] }
): Promise<AiPerformanceAccountRow[]> {
  const keyword = options.keyword?.trim()
  if (!keyword) {
    const defaultCandidates = await loadDefaultAiPerformanceAccountCandidatesAsync(client, scope, options.limit)
    const accountIds = uniqueNonEmpty([
      ...defaultCandidates.map((row) => row.id),
      ...options.selectedAccountIds
    ])
    return loadExplicitAiPerformanceAccountsAsync(client, scope, accountIds)
  }

  const keywordPrefix = normalizeAccountNamePrefix(keyword)
  const accountsTable = businessTable(client, 'accounts')
  const prefixParams = () => [keywordPrefix.start, keywordPrefix.end, keywordPrefix.start]
  const candidateSql: string[] = []
  const candidateParams: unknown[] = []
  if (scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID) {
    candidateSql.push(`
      SELECT accounts.id, accounts.name AS sort_name, 0 AS source_priority
      FROM ${accountsTable} accounts
      WHERE accounts.deleted_at IS NULL
        AND accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ? AND starts_with(accounts.name, ?)
    `)
    candidateParams.push(...prefixParams())
  } else {
    candidateSql.push(`
      SELECT accounts.id, accounts.name AS sort_name, 0 AS source_priority
      FROM ${accountsTable} accounts
      WHERE accounts.system_account_id = ?
        AND accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NULL
        AND accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ? AND starts_with(accounts.name, ?)
    `)
    candidateParams.push(scope.systemAccountId, ...prefixParams())
    candidateSql.push(`
      SELECT accounts.id, accounts.name AS sort_name, 1 AS source_priority
      FROM ${accountsTable} accounts
      WHERE accounts.system_account_id = ?
        AND accounts.deleted_at IS NULL
        AND accounts.authorization_instance_authorization_id IS NOT NULL
        AND accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ? AND starts_with(accounts.name, ?)
    `)
    candidateParams.push(scope.systemAccountId, ...prefixParams())
    candidateSql.push(`
      SELECT accounts.id, accounts.name AS sort_name, 2 AS source_priority
      FROM ${accountsTable} accounts
      WHERE accounts.deleted_at IS NULL
        AND accounts.name COLLATE "C" >= ? AND accounts.name COLLATE "C" < ? AND starts_with(accounts.name, ?)
        AND EXISTS (
          SELECT 1
          FROM ${businessTable(client, 'group_accounts')} visible_group_accounts
          INNER JOIN ${businessTable(client, 'resource_authorizations')} visible_group_authorization_rows
            ON visible_group_authorization_rows.resource_type = 'group'
            AND visible_group_authorization_rows.resource_id = visible_group_accounts.group_id
            AND visible_group_authorization_rows.grantee_system_account_id = ?
            AND visible_group_authorization_rows.status = 'active'
            AND (visible_group_authorization_rows.expires_at IS NULL OR visible_group_authorization_rows.expires_at > ?)
          WHERE visible_group_accounts.account_id = accounts.id
            AND visible_group_accounts.enabled = 1
        )
    `)
    candidateParams.push(...prefixParams(), scope.systemAccountId, nowIso())
  }
  candidateSql.push(`
    SELECT instance_accounts.id, source_accounts.name AS sort_name, ${scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? 1 : 3} AS source_priority
    FROM ${accountsTable} source_accounts
    INNER JOIN ${accountsTable} instance_accounts
      ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
    WHERE source_accounts.deleted_at IS NULL
      AND instance_accounts.deleted_at IS NULL
      AND source_accounts.name COLLATE "C" >= ? AND source_accounts.name COLLATE "C" < ? AND starts_with(source_accounts.name, ?)
      ${scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? '' : 'AND instance_accounts.system_account_id = ?'}
  `)
  candidateParams.push(...prefixParams(), ...(scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID ? [] : [scope.systemAccountId]))
  const candidateRows = await client.query<{ id: string }>(`
    SELECT id
    FROM (
      SELECT
        id,
        sort_name,
        source_priority,
        ROW_NUMBER() OVER (
          PARTITION BY id
          ORDER BY source_priority ASC, sort_name COLLATE "C" ASC, id ASC
        ) AS duplicate_rank
      FROM (${candidateSql.join('\nUNION ALL\n')}) candidate_rows
    ) ranked_candidates
    WHERE duplicate_rank = 1
    ORDER BY source_priority ASC, sort_name COLLATE "C" ASC, id ASC
    LIMIT ?
  `, [...candidateParams, options.limit])
  const accountIds = uniqueNonEmpty([
    ...candidateRows.map((row) => row.id),
    ...options.selectedAccountIds
  ])
  return accountIds.length
    ? loadExplicitAiPerformanceAccountsAsync(client, scope, accountIds)
    : []
}

function normalizeAccountNamePrefix(value: string): { start: string; end: string } {
  const start = value.normalize('NFKC').trim()
  return { start, end: accountNamePrefixUpperBound(start) }
}

function accountNamePrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\uffff`
}

function mergeAiPerformanceStatsWithAccounts(
  statsRows: Array<{ id: string; requestCountLast7d: number; lastStatHour: string | null; rank?: number }>,
  scope: AiPerformanceScope
): AiPerformanceAccountRow[] {
  const ids = [...new Set(statsRows.map((row) => row.id).filter(Boolean))]
  if (!ids.length) return []
  const placeholders = sqlPlaceholders(ids.length)
  const visibleFilter = aiPerformanceVisibleAccountFilter(scope)
  const includeAuthorizationLabel = scope.scopeType === 'caller_account' && scope.systemAccountId !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID
  const ownerSystemAccountExpression = `CASE
      WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      THEN COALESCE(accounts.authorization_instance_owner_system_account_id, instance_authorizations.resource_owner_system_account_id, accounts.system_account_id)
      ELSE accounts.system_account_id
    END`
  const accessTypeExpression = includeAuthorizationLabel
    ? `CASE
      WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized'
      WHEN accounts.system_account_id = ? THEN 'owner'
      ELSE 'authorized'
    END`
    : "'owner'"
  const accessTypeParams = includeAuthorizationLabel ? [scope.systemAccountId] : []
  const accounts = getBusinessDatabase().prepare(`
    SELECT
      accounts.id,
      accounts.name,
      accounts.provider_code,
      ${scope.includeSystemAccountName ? 'system_accounts.display_name' : 'NULL'} AS system_account_name,
      ${includeAuthorizationLabel ? 'owner_system_accounts.display_name' : 'NULL'} AS owner_system_account_name,
      ${accessTypeExpression} AS access_type
    FROM accounts
    ${scope.includeSystemAccountName ? 'LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id' : ''}
    ${includeAuthorizationLabel ? `LEFT JOIN resource_authorizations instance_authorizations
      ON instance_authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN system_accounts owner_system_accounts
      ON owner_system_accounts.id = ${ownerSystemAccountExpression}` : ''}
    WHERE accounts.id IN (${placeholders})
      AND accounts.deleted_at IS NULL
      ${visibleFilter.sql}
  `).all(...accessTypeParams, ...ids, ...visibleFilter.params) as unknown as Array<{
    id: string
    name: string
    provider_code: string
    system_account_name: string | null
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

async function mergeAiPerformanceStatsWithAccountsAsync(
  client: DatabaseClient,
  statsRows: Array<{ id: string; requestCountLast7d: number; lastStatHour: string | null; rank?: number }>,
  scope: AiPerformanceScope
): Promise<AiPerformanceAccountRow[]> {
  const ids = [...new Set(statsRows.map((row) => row.id).filter(Boolean))]
  if (!ids.length) return []
  const placeholders = client.dialect.bindPlaceholders(ids.length)
  const visibleFilter = aiPerformanceVisibleAccountFilterForClient(client, scope)
  const accountsTable = businessTable(client, 'accounts')
  const systemAccountsTable = businessTable(client, 'system_accounts')
  const resourceAuthorizationsTable = businessTable(client, 'resource_authorizations')
  const includeAuthorizationLabel = scope.scopeType === 'caller_account' && scope.systemAccountId !== GLOBAL_STATS_SYSTEM_ACCOUNT_ID
  const ownerSystemAccountExpression = `CASE
      WHEN accounts.authorization_instance_authorization_id IS NOT NULL
      THEN COALESCE(accounts.authorization_instance_owner_system_account_id, instance_authorizations.resource_owner_system_account_id, accounts.system_account_id)
      ELSE accounts.system_account_id
    END`
  const accessTypeExpression = includeAuthorizationLabel
    ? `CASE
      WHEN accounts.authorization_instance_authorization_id IS NOT NULL THEN 'authorized'
      WHEN accounts.system_account_id = ? THEN 'owner'
      ELSE 'authorized'
    END`
    : "'owner'"
  const accessTypeParams = includeAuthorizationLabel ? [scope.systemAccountId] : []
  const accounts = await client.query<{
    id: string
    name: string
    provider_code: string
    system_account_name: string | null
    owner_system_account_name: string | null
    access_type: 'owner' | 'authorized'
  }>(`
    SELECT
      accounts.id,
      accounts.name,
      accounts.provider_code,
      ${scope.includeSystemAccountName ? 'system_accounts.display_name' : 'NULL'} AS system_account_name,
      ${includeAuthorizationLabel ? 'owner_system_accounts.display_name' : 'NULL'} AS owner_system_account_name,
      ${accessTypeExpression} AS access_type
    FROM ${accountsTable} accounts
    ${scope.includeSystemAccountName ? `LEFT JOIN ${systemAccountsTable} system_accounts ON system_accounts.id = accounts.system_account_id` : ''}
    ${includeAuthorizationLabel ? `LEFT JOIN ${resourceAuthorizationsTable} instance_authorizations
      ON instance_authorizations.id = accounts.authorization_instance_authorization_id
    LEFT JOIN ${systemAccountsTable} owner_system_accounts
      ON owner_system_accounts.id = ${ownerSystemAccountExpression}` : ''}
    WHERE accounts.id IN (${placeholders})
      AND accounts.deleted_at IS NULL
      ${visibleFilter.sql}
  `, [...accessTypeParams, ...ids, ...visibleFilter.params])
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

function mapAiPerformanceAccount(row: AiPerformanceAccountRow, scope: AiPerformanceScope): AiPerformanceAccount {
  return {
    id: row.id,
    name: row.name,
    providerCode: row.provider_code,
    ...(scope.includeSystemAccountName && row.system_account_name
      ? { systemAccountName: row.system_account_name }
      : {}),
    ...(row.access_type === 'authorized'
      ? {
          accessType: row.access_type,
          ...(row.owner_system_account_name ? { ownerSystemAccountName: row.owner_system_account_name } : {})
        }
      : {})
  }
}

function firstHour(range: AccountUsageStatsRange, hourBuckets: string[]): string {
  return hourBuckets[0] ?? `${range.startDate}T00`
}

function lastHour(range: AccountUsageStatsRange, hourBuckets: string[]): string {
  return hourBuckets[hourBuckets.length - 1] ?? `${range.endDate}T23`
}

function mapAiPerformanceHourlySeries(
  accounts: AiPerformanceAccount[],
  hourBuckets: string[],
  hourlyRows: AiPerformanceHourlyRow[]
) {
  const hourlyRowsByAccountHour = new Map(hourlyRows.map((row) => [`${row.scope_id}\n${row.stat_hour}`, row]))
  return accounts.map((account) => ({
    accountId: account.id,
    accountName: account.name,
    providerCode: account.providerCode,
    points: hourBuckets.map((statHour) => {
      const row = hourlyRowsByAccountHour.get(`${account.id}\n${statHour}`)
      const requestCount = Number(row?.request_count ?? 0)
      const firstTokenCount = Number(row?.first_token_ms_count ?? 0)
      const durationCount = Number(row?.duration_ms_count ?? 0)
      return {
        statHour,
        requestCount,
        averageFirstTokenMs: averageFromSum(row?.first_token_ms_sum, row?.first_token_ms_count),
        maxFirstTokenMs: maxFromCountedMetric(row?.first_token_ms_max, firstTokenCount),
        averageDurationMs: averageFromSum(row?.duration_ms_sum, row?.duration_ms_count),
        maxDurationMs: maxFromCountedMetric(row?.duration_ms_max, durationCount)
      }
    })
  }))
}

function mapAiPerformanceSummary(row: {
  request_count: number
  first_token_ms_sum: number
  first_token_ms_count: number
  first_token_ms_max: number
  duration_ms_sum: number
  duration_ms_count: number
  duration_ms_max: number
} | undefined): AiPerformanceSummary {
  return {
    requestCount: Number(row?.request_count ?? 0),
    averageFirstTokenMs: averageFromSum(row?.first_token_ms_sum, row?.first_token_ms_count),
    maxFirstTokenMs: maxFromCountedMetric(row?.first_token_ms_max, Number(row?.first_token_ms_count ?? 0)),
    averageDurationMs: averageFromSum(row?.duration_ms_sum, row?.duration_ms_count),
    maxDurationMs: maxFromCountedMetric(row?.duration_ms_max, Number(row?.duration_ms_count ?? 0))
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

function aiPerformanceVisibleAccountFilterForClient(client: DatabaseClient, scope: AiPerformanceScope): { sql: string; params: string[] } {
  if (scope.systemAccountId === GLOBAL_STATS_SYSTEM_ACCOUNT_ID) {
    return { sql: '', params: [] }
  }
  const now = nowIso()
  return {
    sql: `AND (
      accounts.system_account_id = ?
      OR EXISTS (
        SELECT 1
        FROM ${businessTable(client, 'group_accounts')} visible_group_accounts
        INNER JOIN ${businessTable(client, 'resource_authorizations')} visible_group_authorization_rows
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

function statsTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_stats', tableName)
}

function businessTable(client: DatabaseClient, tableName: string): string {
  return client.dialect.qualifyTable('juhe_business', tableName)
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
