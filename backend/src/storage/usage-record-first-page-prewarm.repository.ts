import { runtimeConfig } from '../config/runtime.js'
import { getStatsDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { listSystemAccountOptionsAsync } from './system-accounts.repository.js'

const defaultCandidateLimit = 128
const maximumCandidateLimit = 128
const maximumCandidateScanLimit = 512
const maximumCandidateWindowDays = 7
const postgresCandidateStatementTimeoutMs = 1_500
const systemAccountLookupChunkSize = 50

interface UsageRecordFirstPagePrewarmCandidateRow {
  system_account_id: string
  request_count: number | string
  last_used_at: string | null
}

export interface UsageRecordFirstPagePrewarmCandidate {
  systemAccountId: string
  requestCount: number
  lastUsedAt?: string
}

export interface UsageRecordFirstPagePrewarmCandidateListOptions {
  startDate: string
  endDate: string
  limit?: number
}

export async function listUsageRecordFirstPagePrewarmCandidatesAsync(
  options: UsageRecordFirstPagePrewarmCandidateListOptions
): Promise<UsageRecordFirstPagePrewarmCandidate[]> {
  const dates = candidateDateKeys(options.startDate, options.endDate)
  const limit = boundedCandidateLimit(options.limit)
  const scanLimit = Math.min(maximumCandidateScanLimit, limit * 4)
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getStatsDatabase())
  const rows = runtimeConfig.databaseDriver === 'postgres'
    ? await client.transaction(async (tx) => {
        await tx.execute(`SET LOCAL statement_timeout = ${postgresCandidateStatementTimeoutMs}`)
        return await listCandidateRowsWithFallback(tx, dates, scanLimit)
      })
    : await listCandidateRowsWithFallback(client, dates, scanLimit)
  if (rows.length === 0) return []

  const activeIds = await activeSystemAccountIdSet(rows.map((row) => row.system_account_id))
  return rows
    .filter((row) => activeIds.has(row.system_account_id))
    .slice(0, limit)
    .map((row) => ({
      systemAccountId: row.system_account_id,
      requestCount: finiteNonNegativeNumber(row.request_count),
      ...(row.last_used_at ? { lastUsedAt: row.last_used_at } : {})
    }))
}

async function listCandidateRowsWithFallback(
  client: DatabaseClient,
  dates: string[],
  scanLimit: number
): Promise<UsageRecordFirstPagePrewarmCandidateRow[]> {
  const summaryRows = await listSummaryWindowCandidateRows(client, dates[0], dates.at(-1)!, scanLimit)
  if (summaryRows.length > 0) return summaryRows
  return await listBoundedCandidateRowsByDate(client, dates, maximumCandidateScanLimit, scanLimit)
}

async function listSummaryWindowCandidateRows(
  client: DatabaseClient,
  startDate: string,
  endDate: string,
  limit: number
): Promise<UsageRecordFirstPagePrewarmCandidateRow[]> {
  const sqliteIndexHint = client.driver === 'sqlite'
    ? 'INDEXED BY idx_usage_overview_summary_windows_prewarm_order'
    : ''
  return await client.query<UsageRecordFirstPagePrewarmCandidateRow>(`
    SELECT system_account_id, request_count, last_used_at
    FROM ${client.dialect.qualifyTable('juhe_stats', 'usage_overview_summary_windows')} ${sqliteIndexHint}
    WHERE window_key = ?
      AND system_account_id <> 'global'
      AND request_count > 0
    ORDER BY request_count DESC, last_used_at DESC, system_account_id ASC
    LIMIT ?
  `, [`${startDate}:${endDate}`, limit])
}

async function listBoundedCandidateRowsByDate(
  client: DatabaseClient,
  dates: string[],
  perDateLimit: number,
  aggregateLimit: number
): Promise<UsageRecordFirstPagePrewarmCandidateRow[]> {
  const accumulated = new Map<string, { requestCount: number; lastUsedAt: string | null }>()
  for (const date of dates) {
    const rows = await listCandidateRowsForDate(client, date, perDateLimit)
    for (const row of rows) {
      const current = accumulated.get(row.system_account_id)
      const requestCount = finiteNonNegativeNumber(row.request_count)
      const lastUsedAt = latestIso(current?.lastUsedAt, row.last_used_at)
      accumulated.set(row.system_account_id, {
        requestCount: (current?.requestCount ?? 0) + requestCount,
        lastUsedAt
      })
    }
  }
  return [...accumulated.entries()]
    .map(([systemAccountId, value]) => ({
      system_account_id: systemAccountId,
      request_count: value.requestCount,
      last_used_at: value.lastUsedAt
    }))
    .sort(compareCandidateRows)
    .slice(0, aggregateLimit)
}

async function listCandidateRowsForDate(
  client: DatabaseClient,
  date: string,
  limit: number
): Promise<UsageRecordFirstPagePrewarmCandidateRow[]> {
  const sqliteIndexHint = client.driver === 'sqlite'
    ? 'INDEXED BY idx_usage_stats_daily_system_account_top_activity'
    : ''
  return await client.query<UsageRecordFirstPagePrewarmCandidateRow>(`
    SELECT system_account_id, request_count, last_used_at
    FROM ${client.dialect.qualifyTable('juhe_stats', 'usage_stats_daily')} ${sqliteIndexHint}
    WHERE stat_date = ?
      AND scope_type = 'system_account'
      AND scope_id = system_account_id
      AND system_account_id <> 'global'
      AND request_count > 0
    ORDER BY request_count DESC, last_used_at DESC, system_account_id ASC
    LIMIT ?
  `, [date, limit])
}

async function activeSystemAccountIdSet(systemAccountIds: string[]): Promise<Set<string>> {
  const activeIds = new Set<string>()
  for (let offset = 0; offset < systemAccountIds.length; offset += systemAccountLookupChunkSize) {
    const ids = systemAccountIds.slice(offset, offset + systemAccountLookupChunkSize)
    const options = await listSystemAccountOptionsAsync({ ids, limit: systemAccountLookupChunkSize })
    for (const option of options) {
      if (!option.disabledReason) activeIds.add(option.id)
    }
  }
  return activeIds
}

function candidateDateKeys(startDateInput: string, endDateInput: string): string[] {
  const startDate = parseDateKey(startDateInput)
  const endDate = parseDateKey(endDateInput)
  if (startDate.getTime() > endDate.getTime()) {
    throw new Error('使用记录首屏预热候选日期范围无效')
  }
  const dates: string[] = []
  for (let cursor = startDate.getTime(); cursor <= endDate.getTime(); cursor += 86_400_000) {
    dates.push(new Date(cursor).toISOString().slice(0, 10))
    if (dates.length > maximumCandidateWindowDays) {
      throw new Error(`使用记录首屏预热候选日期范围不得超过 ${maximumCandidateWindowDays} 天`)
    }
  }
  return dates
}

function parseDateKey(value: string): Date {
  const normalized = value.trim()
  if (!/^\d{4}-\d{2}-\d{2}$/.test(normalized)) {
    throw new Error('使用记录首屏预热候选日期无效')
  }
  const date = new Date(`${normalized}T00:00:00.000Z`)
  if (!Number.isFinite(date.getTime()) || date.toISOString().slice(0, 10) !== normalized) {
    throw new Error('使用记录首屏预热候选日期无效')
  }
  return date
}

function compareCandidateRows(
  left: UsageRecordFirstPagePrewarmCandidateRow,
  right: UsageRecordFirstPagePrewarmCandidateRow
): number {
  const requestCountDifference = finiteNonNegativeNumber(right.request_count) - finiteNonNegativeNumber(left.request_count)
  if (requestCountDifference !== 0) return requestCountDifference
  const lastUsedDifference = String(right.last_used_at ?? '').localeCompare(String(left.last_used_at ?? ''))
  if (lastUsedDifference !== 0) return lastUsedDifference
  return left.system_account_id.localeCompare(right.system_account_id)
}

function latestIso(left: string | null | undefined, right: string | null | undefined): string | null {
  if (!left) return right ?? null
  if (!right) return left
  return left >= right ? left : right
}

function boundedCandidateLimit(value: number | undefined): number {
  const number = Number(value ?? defaultCandidateLimit)
  return Number.isFinite(number)
    ? Math.min(maximumCandidateLimit, Math.max(1, Math.trunc(number)))
    : defaultCandidateLimit
}

function finiteNonNegativeNumber(value: number | string): number {
  const number = Number(value)
  return Number.isFinite(number) ? Math.max(0, number) : 0
}
