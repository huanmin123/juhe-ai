import type { DatabaseSync } from 'node:sqlite'

import type { AiHealthAccountRow, AiHealthHourPoint, AiHealthListResult } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { listAccountItemsPageAsync, listAccountItemsPageReadOnly } from './account-summary.repository.js'
import type { AccessScope } from './access-scope.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getStatsDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { hourBucketsUntilNow } from './usage-stats-window-helpers.js'

export interface AiHealthListOptions {
  hours?: number
  keyword?: string
  page?: number
  pageSize?: number
}

interface AccountHealthHourRow {
  account_id: string
  stat_hour: string
  status: 'success' | 'failure'
  last_observed_at: string
  status_code: number | null
  error_code: string | null
  error_message: string | null
}

export function getAiHealthList(access?: AccessScope, options: AiHealthListOptions = {}): AiHealthListResult {
  const normalized = normalizeAiHealthListOptions(options)
  const timezone = usageStatsTimezone()
  const page = listAccountItemsPageReadOnly(access, accountListOptions(normalized))
  const hourBuckets = hourBucketsUntilNow(normalized.hours, Date.now(), timezone)
  const rows = loadAccountHealthRows(getStatsDatabase(), page.items.map((item) => item.id), hourBuckets)
  return mapAiHealthList(page, rows, hourBuckets, timezone, normalized.hours)
}

export async function getAiHealthListAsync(access?: AccessScope, options: AiHealthListOptions = {}): Promise<AiHealthListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({ type: 'get_ai_health_list_read_only', access, options })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAiHealthList(access, options)
  }
  const normalized = normalizeAiHealthListOptions(options)
  const [timezone, page] = await Promise.all([
    usageStatsTimezoneAsync(),
    listAccountItemsPageAsync(access, accountListOptions(normalized))
  ])
  const hourBuckets = hourBucketsUntilNow(normalized.hours, Date.now(), timezone)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const rows = await loadAccountHealthRowsAsync(client, page.items.map((item) => item.id), hourBuckets)
  return mapAiHealthList(page, rows, hourBuckets, timezone, normalized.hours)
}

function normalizeAiHealthListOptions(options: AiHealthListOptions): Required<Pick<AiHealthListOptions, 'hours' | 'page' | 'pageSize'>> & Pick<AiHealthListOptions, 'keyword'> {
  const hours = boundedInteger(options.hours, 168, 1, 31 * 24)
  const page = boundedInteger(options.page, 1, 1, Number.MAX_SAFE_INTEGER)
  const pageSize = boundedInteger(options.pageSize, 20, 10, 50)
  const keyword = options.keyword?.trim()
  return { hours, page, pageSize, keyword: keyword || undefined }
}

function accountListOptions(options: ReturnType<typeof normalizeAiHealthListOptions>) {
  return {
    page: options.page,
    pageSize: options.pageSize,
    keyword: options.keyword,
    sorts: [{ field: 'name' as const, order: 'asc' as const }]
  }
}

function loadAccountHealthRows(database: DatabaseSync, accountIds: string[], hourBuckets: string[]): AccountHealthHourRow[] {
  if (!accountIds.length || !hourBuckets.length) return []
  const rows: AccountHealthHourRow[] = []
  const startHour = hourBuckets[0]
  const endHour = hourBuckets[hourBuckets.length - 1]
  for (const chunk of chunkValues(accountIds, 900)) {
    rows.push(...database.prepare(`
      SELECT account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message
      FROM account_health_hourly
      WHERE account_id IN (${sqlPlaceholders(chunk.length)})
        AND stat_hour >= ? AND stat_hour <= ?
      ORDER BY account_id ASC, stat_hour ASC
    `).all(...chunk, startHour, endHour) as unknown as AccountHealthHourRow[])
  }
  return rows
}

async function loadAccountHealthRowsAsync(client: DatabaseClient, accountIds: string[], hourBuckets: string[]): Promise<AccountHealthHourRow[]> {
  if (!accountIds.length || !hourBuckets.length) return []
  const rows: AccountHealthHourRow[] = []
  const startHour = hourBuckets[0]
  const endHour = hourBuckets[hourBuckets.length - 1]
  for (const chunk of chunkValues(accountIds, 900)) {
    rows.push(...await client.query<AccountHealthHourRow>(`
      SELECT account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message
      FROM ${client.dialect.qualifyTable('juhe_stats', 'account_health_hourly')}
      WHERE account_id IN (${client.dialect.bindPlaceholders(chunk.length)})
        AND stat_hour >= ? AND stat_hour <= ?
      ORDER BY account_id ASC, stat_hour ASC
    `, [...chunk, startHour, endHour]))
  }
  return rows
}

function mapAiHealthList(
  page: Awaited<ReturnType<typeof listAccountItemsPageAsync>>,
  rows: AccountHealthHourRow[],
  hourBuckets: string[],
  timezone: string,
  rangeHours: number
): AiHealthListResult {
  const rowsByAccountHour = new Map(rows.map((row) => [`${row.account_id}\u0000${row.stat_hour}`, row]))
  return {
    timezone,
    rangeHours,
    startHour: hourBuckets[0] ?? '',
    endHour: hourBuckets[hourBuckets.length - 1] ?? '',
    items: page.items.map((account) => mapAiHealthAccount(account, hourBuckets, rowsByAccountHour)),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

function mapAiHealthAccount(
  account: Awaited<ReturnType<typeof listAccountItemsPageAsync>>['items'][number],
  hourBuckets: string[],
  rowsByAccountHour: Map<string, AccountHealthHourRow>
): AiHealthAccountRow {
  let successHours = 0
  let failureHours = 0
  const hours: AiHealthHourPoint[] = hourBuckets.map((statHour) => {
    const row = rowsByAccountHour.get(`${account.id}\u0000${statHour}`)
    if (!row) return { statHour, status: 'unknown' }
    if (row.status === 'success') successHours += 1
    else failureHours += 1
    return {
      statHour,
      status: row.status,
      lastObservedAt: row.last_observed_at,
      statusCode: optionalNumber(row.status_code),
      errorCode: row.error_code ?? undefined,
      errorMessage: row.error_message ?? undefined
    }
  })
  const checkedHours = successHours + failureHours
  const latestStatus = [...hours].reverse().find((point) => point.status !== 'unknown')?.status ?? 'unknown'
  return {
    id: account.id,
    name: account.name,
    providerCode: account.providerCode,
    status: account.status,
    systemAccountId: account.systemAccountId,
    systemAccountName: account.systemAccountName,
    ownerSystemAccountId: account.ownerSystemAccountId,
    ownerSystemAccountName: account.ownerSystemAccountName,
    accessType: account.accessType,
    lastHealthCheckAt: account.lastHealthCheckAt,
    nextHealthCheckAt: account.nextHealthCheckAt,
    latestStatus,
    successHours,
    failureHours,
    unknownHours: Math.max(0, hours.length - checkedHours),
    healthRate: checkedHours > 0 ? Number(((successHours / checkedHours) * 100).toFixed(2)) : undefined,
    hours
  }
}

function boundedInteger(value: number | undefined, fallback: number, min: number, max: number): number {
  const number = Number(value ?? fallback)
  return Number.isFinite(number) ? Math.min(max, Math.max(min, Math.trunc(number))) : fallback
}

function optionalNumber(value: unknown): number | undefined {
  if (value === null || value === undefined || value === '') return undefined
  const number = Number(value)
  return Number.isFinite(number) ? number : undefined
}
