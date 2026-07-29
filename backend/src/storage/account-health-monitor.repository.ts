import type { DatabaseSync, SQLInputValue } from 'node:sqlite'

import type { AiHealthAccountRow, AiHealthHourDetail, AiHealthHourPoint, AiHealthListResult } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { includeSystemAccountFields, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getBusinessDatabase, getStatsDatabase } from './database.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { usageStatsTimezone, usageStatsTimezoneAsync } from './usage-stats-helpers.js'
import { hourBucketsUntilNow } from './usage-stats-window-helpers.js'

export interface AiHealthListOptions {
  hours?: number
  keyword?: string
  page?: number
  pageSize?: number
}

interface AccountHealthSlotRow {
  account_id: string
  stat_hour: string
  status: 'success' | 'failure'
  source_order?: number
}

interface AccountHealthHourRow extends AccountHealthSlotRow {
  last_observed_at: string
  status_code: number | null
  error_code: string | null
  error_message: string | null
}

interface AiHealthAccountProjection {
  id: string
  system_account_name: string | null
  provider_code: AiHealthAccountRow['providerCode']
  name: string
  status: AiHealthAccountRow['status']
  last_health_check_at: string | null
  last_health_success_at: string | null
  next_health_check_at: string | null
}

interface AiHealthAccountPage {
  items: AiHealthAccountProjection[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export function getAiHealthList(access?: AccessScope, options: AiHealthListOptions = {}): AiHealthListResult {
  const normalized = normalizeAiHealthListOptions(options)
  const timezone = usageStatsTimezone()
  const page = loadAiHealthAccountPageReadOnly(access, normalized)
  const hourBuckets = hourBucketsUntilNow(normalized.hours, Date.now(), timezone)
  const rows = loadAccountHealthRows(getStatsDatabase(), page.items.map((item) => item.id), hourBuckets)
  return mapAiHealthList(page, rows, hourBuckets)
}

export async function getAiHealthListAsync(access?: AccessScope, options: AiHealthListOptions = {}): Promise<AiHealthListResult> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({ type: 'get_ai_health_list_read_only', access, options })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAiHealthList(access, options)
  }
  const normalized = normalizeAiHealthListOptions(options)
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const [timezone, page] = await Promise.all([
    usageStatsTimezoneAsync(),
    loadAiHealthAccountPage(client, access, normalized)
  ])
  const hourBuckets = hourBucketsUntilNow(normalized.hours, Date.now(), timezone)
  const rows = await loadAccountHealthRowsAsync(client, page.items.map((item) => item.id), hourBuckets)
  return mapAiHealthList(page, rows, hourBuckets)
}

export function getAiHealthHourDetail(
  access: AccessScope | undefined,
  accountId: string,
  statHour: string
): AiHealthHourDetail | undefined {
  const normalized = normalizeAiHealthHourDetailInput(accountId, statHour)
  const visible = aiHealthAccountVisibleReadOnly(access, normalized.accountId)
  if (!visible) return undefined
  return mapAiHealthHourDetail(
    normalized.statHour,
    loadAccountHealthHourDetail(getStatsDatabase(), normalized.accountId, normalized.statHour)
  )
}

export async function getAiHealthHourDetailAsync(
  access: AccessScope | undefined,
  accountId: string,
  statHour: string
): Promise<AiHealthHourDetail | undefined> {
  const normalized = normalizeAiHealthHourDetailInput(accountId, statHour)
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'get_ai_health_hour_detail_read_only',
      access,
      accountId: normalized.accountId,
      statHour: normalized.statHour
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return getAiHealthHourDetail(access, normalized.accountId, normalized.statHour)
  }
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const visible = (await aiHealthAccountVisible(client, access, normalized.accountId))
  if (!visible) return undefined
  return mapAiHealthHourDetail(
    normalized.statHour,
    await loadAccountHealthHourDetailAsync(client, normalized.accountId, normalized.statHour)
  )
}

function normalizeAiHealthListOptions(options: AiHealthListOptions): Required<Pick<AiHealthListOptions, 'hours' | 'page' | 'pageSize'>> & Pick<AiHealthListOptions, 'keyword'> {
  const hours = boundedInteger(options.hours, 168, 1, 31 * 24)
  const page = boundedInteger(options.page, 1, 1, Number.MAX_SAFE_INTEGER)
  const pageSize = boundedInteger(options.pageSize, 20, 10, 50)
  const keyword = options.keyword?.trim()
  return { hours, page, pageSize, keyword: keyword || undefined }
}


async function loadAiHealthAccountPage(
  client: DatabaseClient,
  access: AccessScope | undefined,
  options: ReturnType<typeof normalizeAiHealthListOptions>
): Promise<AiHealthAccountPage> {
  const scopeId = scopedSystemAccountId(access)
  const includeSystemAccount = includeSystemAccountFields(access)
  const clauses = ['accounts.deleted_at IS NULL']
  const params: SQLInputValue[] = []
  if (scopeId) {
    clauses.push('accounts.system_account_id = ?')
    params.push(scopeId)
  }
  if (options.keyword) {
    clauses.push(client.driver === 'postgres'
      ? 'position(lower(?) in lower(accounts.name)) > 0'
      : 'instr(lower(accounts.name), lower(?)) > 0')
    params.push(options.keyword)
  }
  const currentIso = client.driver === 'postgres'
    ? `to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`
    : "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"
  const rows = await client.query<AiHealthAccountProjection>(`
    SELECT
      accounts.id,
      ${includeSystemAccount
        ? 'COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id)'
        : 'NULL'} AS system_account_name,
      COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
      accounts.name,
      CASE
        WHEN accounts.authorization_instance_authorization_id IS NOT NULL
          AND (
            authorizations.status <> 'active'
            OR (authorizations.expires_at IS NOT NULL AND authorizations.expires_at <= ${currentIso})
          )
        THEN 'disabled'
        WHEN source_accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated')
        THEN source_accounts.status
        ELSE accounts.status
      END AS status,
      accounts.last_health_check_at,
      accounts.last_health_success_at,
      accounts.next_health_check_at
    FROM ${client.dialect.qualifyTable('juhe_business', 'accounts')} accounts
    LEFT JOIN ${client.dialect.qualifyTable('juhe_business', 'accounts')} source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN ${client.dialect.qualifyTable('juhe_business', 'resource_authorizations')} authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    ${includeSystemAccount
      ? `LEFT JOIN ${client.dialect.qualifyTable('juhe_business', 'system_accounts')} system_accounts
      ON system_accounts.id = accounts.system_account_id`
      : ''}
    WHERE ${clauses.join(' AND ')}
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
    ORDER BY
      CASE WHEN accounts.last_used_at IS NULL THEN 1 ELSE 0 END ASC,
      accounts.last_used_at DESC,
      accounts.name ASC,
      accounts.id ASC
    LIMIT ? OFFSET ?
  `, [...params, options.pageSize + 1, (options.page - 1) * options.pageSize])
  const pageRows = takePageRows(rows, options.pageSize)
  return {
    items: pageRows.rows,
    total: pagedTotalUpperBound(options.page, options.pageSize, pageRows.rows.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: options.page,
    pageSize: options.pageSize
  }
}

function loadAiHealthAccountPageReadOnly(
  access: AccessScope | undefined,
  options: ReturnType<typeof normalizeAiHealthListOptions>
): AiHealthAccountPage {
  const scopeId = scopedSystemAccountId(access)
  const includeSystemAccount = includeSystemAccountFields(access)
  const clauses = ['accounts.deleted_at IS NULL']
  const params: SQLInputValue[] = []
  if (scopeId) {
    clauses.push('accounts.system_account_id = ?')
    params.push(scopeId)
  }
  if (options.keyword) {
    clauses.push('instr(lower(accounts.name), lower(?)) > 0')
    params.push(options.keyword)
  }
  const rows = getBusinessDatabase().prepare(`
    SELECT
      accounts.id,
      ${includeSystemAccount
        ? 'COALESCE(system_accounts.display_name, system_accounts.username, accounts.system_account_id)'
        : 'NULL'} AS system_account_name,
      COALESCE(source_accounts.provider_code, accounts.provider_code) AS provider_code,
      accounts.name,
      CASE
        WHEN accounts.authorization_instance_authorization_id IS NOT NULL
          AND (
            authorizations.status <> 'active'
            OR (authorizations.expires_at IS NOT NULL AND authorizations.expires_at <= strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
          )
        THEN 'disabled'
        WHEN source_accounts.status IN ('pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable', 'quality_isolated')
        THEN source_accounts.status
        ELSE accounts.status
      END AS status,
      accounts.last_health_check_at,
      accounts.last_health_success_at,
      accounts.next_health_check_at
    FROM accounts
    LEFT JOIN accounts source_accounts
      ON source_accounts.id = accounts.authorization_instance_source_account_id
      AND source_accounts.deleted_at IS NULL
    LEFT JOIN resource_authorizations authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    ${includeSystemAccount ? 'LEFT JOIN system_accounts ON system_accounts.id = accounts.system_account_id' : ''}
    WHERE ${clauses.join(' AND ')}
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
    ORDER BY
      CASE WHEN accounts.last_used_at IS NULL THEN 1 ELSE 0 END ASC,
      accounts.last_used_at DESC,
      accounts.name ASC,
      accounts.id ASC
    LIMIT ? OFFSET ?
  `).all(...params, options.pageSize + 1, (options.page - 1) * options.pageSize) as unknown as AiHealthAccountProjection[]
  const pageRows = takePageRows(rows, options.pageSize)
  return {
    items: pageRows.rows,
    total: pagedTotalUpperBound(options.page, options.pageSize, pageRows.rows.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: options.page,
    pageSize: options.pageSize
  }
}

async function aiHealthAccountVisible(
  client: DatabaseClient,
  access: AccessScope | undefined,
  accountId: string
): Promise<boolean> {
  const scopeId = scopedSystemAccountId(access)
  const rows = await client.query<{ id: string }>(`
    SELECT accounts.id
    FROM ${client.dialect.qualifyTable('juhe_business', 'accounts')} accounts
    LEFT JOIN ${client.dialect.qualifyTable('juhe_business', 'resource_authorizations')} authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      ${scopeId ? 'AND accounts.system_account_id = ?' : ''}
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
    LIMIT 1
  `, scopeId ? [accountId, scopeId] : [accountId])
  return rows.length > 0
}

function aiHealthAccountVisibleReadOnly(access: AccessScope | undefined, accountId: string): boolean {
  const scopeId = scopedSystemAccountId(access)
  const row = getBusinessDatabase().prepare(`
    SELECT accounts.id
    FROM accounts
    LEFT JOIN resource_authorizations authorizations
      ON authorizations.id = accounts.authorization_instance_authorization_id
    WHERE accounts.id = ?
      AND accounts.deleted_at IS NULL
      ${scopeId ? 'AND accounts.system_account_id = ?' : ''}
      AND (
        accounts.authorization_instance_authorization_id IS NULL
        OR authorizations.status IN ('active', 'paused', 'expired')
      )
    LIMIT 1
  `).get(...(scopeId ? [accountId, scopeId] : [accountId])) as { id: string } | undefined
  return Boolean(row)
}

function loadAccountHealthRows(database: DatabaseSync, accountIds: string[], hourBuckets: string[]): AccountHealthSlotRow[] {
  if (!accountIds.length || !hourBuckets.length) return []
  const rows: AccountHealthSlotRow[] = []
  const startHour = hourBuckets[0]
  const endHour = hourBuckets[hourBuckets.length - 1]
  for (const chunk of chunkValues(accountIds, 900)) {
    rows.push(...database.prepare(`
      SELECT account_id, stat_hour, status, source_order
      FROM (
        SELECT account_id, stat_hour, status, 0 AS source_order
        FROM account_health_hourly
        WHERE account_id IN (${sqlPlaceholders(chunk.length)}) AND stat_hour >= ? AND stat_hour <= ?
        UNION ALL
        SELECT account_id, stat_hour, 'failure' AS status, 1 AS source_order
        FROM account_quality_health_hourly
        WHERE account_id IN (${sqlPlaceholders(chunk.length)}) AND stat_hour >= ? AND stat_hour <= ?
      ) merged_health
      ORDER BY account_id ASC, stat_hour ASC, source_order ASC
    `).all(...chunk, startHour, endHour, ...chunk, startHour, endHour) as unknown as AccountHealthSlotRow[])
  }
  return rows
}

async function loadAccountHealthRowsAsync(client: DatabaseClient, accountIds: string[], hourBuckets: string[]): Promise<AccountHealthSlotRow[]> {
  if (!accountIds.length || !hourBuckets.length) return []
  const rows: AccountHealthSlotRow[] = []
  const startHour = hourBuckets[0]
  const endHour = hourBuckets[hourBuckets.length - 1]
  for (const chunk of chunkValues(accountIds, 900)) {
    rows.push(...await client.query<AccountHealthSlotRow>(`
      SELECT account_id, stat_hour, status, source_order
      FROM (
        SELECT account_id, stat_hour, status, 0 AS source_order
        FROM ${client.dialect.qualifyTable('juhe_stats', 'account_health_hourly')}
        WHERE account_id IN (${client.dialect.bindPlaceholders(chunk.length)}) AND stat_hour >= ? AND stat_hour <= ?
        UNION ALL
        SELECT account_id, stat_hour, 'failure' AS status, 1 AS source_order
        FROM ${client.dialect.qualifyTable('juhe_stats', 'account_quality_health_hourly')}
        WHERE account_id IN (${client.dialect.bindPlaceholders(chunk.length)}) AND stat_hour >= ? AND stat_hour <= ?
      ) merged_health
      ORDER BY account_id ASC, stat_hour ASC, source_order ASC
    `, [...chunk, startHour, endHour, ...chunk, startHour, endHour]))
  }
  return rows
}

function loadAccountHealthHourDetail(
  database: DatabaseSync,
  accountId: string,
  statHour: string
): AccountHealthHourRow | undefined {
  return database.prepare(`
    SELECT account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message, source_order
    FROM (
      SELECT account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message, 0 AS source_order
      FROM account_health_hourly
      WHERE account_id = ? AND stat_hour = ?
      UNION ALL
      SELECT account_id, stat_hour, 'failure' AS status, observed_at AS last_observed_at,
             NULL AS status_code, 'model_quality_failed' AS error_code,
             '模型质量检查不达标：' || score || ' 分，阈值 ' || threshold || ' 分' AS error_message,
             1 AS source_order
      FROM account_quality_health_hourly
      WHERE account_id = ? AND stat_hour = ?
    ) merged_health
    ORDER BY source_order DESC
    LIMIT 1
  `).get(accountId, statHour, accountId, statHour) as unknown as AccountHealthHourRow | undefined
}

async function loadAccountHealthHourDetailAsync(
  client: DatabaseClient,
  accountId: string,
  statHour: string
): Promise<AccountHealthHourRow | undefined> {
  const rows = await client.query<AccountHealthHourRow>(`
    SELECT account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message, source_order
    FROM (
      SELECT account_id, stat_hour, status, last_observed_at, status_code, error_code, error_message, 0 AS source_order
      FROM ${client.dialect.qualifyTable('juhe_stats', 'account_health_hourly')}
      WHERE account_id = ? AND stat_hour = ?
      UNION ALL
      SELECT account_id, stat_hour, 'failure' AS status, observed_at AS last_observed_at,
             NULL AS status_code, 'model_quality_failed' AS error_code,
             '模型质量检查不达标：' || score || ' 分，阈值 ' || threshold || ' 分' AS error_message,
             1 AS source_order
      FROM ${client.dialect.qualifyTable('juhe_stats', 'account_quality_health_hourly')}
      WHERE account_id = ? AND stat_hour = ?
    ) merged_health
    ORDER BY source_order DESC
    LIMIT 1
  `, [accountId, statHour, accountId, statHour])
  return rows[0]
}

function mapAiHealthList(
  page: AiHealthAccountPage,
  rows: AccountHealthSlotRow[],
  hourBuckets: string[]
): AiHealthListResult {
  const rowsByAccountHour = new Map(rows.map((row) => [`${row.account_id}\u0000${row.stat_hour}`, row]))
  return {
    items: page.items.map((account) => mapAiHealthAccount(account, hourBuckets, rowsByAccountHour)),
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

function mapAiHealthAccount(
  account: AiHealthAccountProjection,
  hourBuckets: string[],
  rowsByAccountHour: Map<string, AccountHealthSlotRow>
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
      status: row.status
    }
  })
  const checkedHours = successHours + failureHours
  const latestStatus = [...hours].reverse().find((point) => point.status !== 'unknown')?.status ?? 'unknown'
  return {
    id: account.id,
    name: account.name,
    providerCode: account.provider_code,
    status: account.status,
    ...(account.system_account_name ? { systemAccountName: account.system_account_name } : {}),
    ...(account.last_health_check_at ? { lastHealthCheckAt: account.last_health_check_at } : {}),
    ...(account.last_health_success_at ? { lastHealthSuccessAt: account.last_health_success_at } : {}),
    ...(account.next_health_check_at ? { nextHealthCheckAt: account.next_health_check_at } : {}),
    latestStatus,
    successHours,
    failureHours,
    unknownHours: Math.max(0, hours.length - checkedHours),
    ...(checkedHours > 0 ? { healthRate: Number(((successHours / checkedHours) * 100).toFixed(2)) } : {}),
    hours
  }
}

function normalizeAiHealthHourDetailInput(accountId: string, statHour: string): { accountId: string; statHour: string } {
  const normalizedAccountId = accountId.trim()
  const normalizedStatHour = statHour.trim()
  if (!normalizedAccountId) throw new Error('账户 ID 不能为空')
  if (!/^\d{4}-\d{2}-\d{2}T(?:[01]\d|2[0-3])$/.test(normalizedStatHour)) {
    throw new Error('统计小时格式不合法')
  }
  return { accountId: normalizedAccountId, statHour: normalizedStatHour }
}

function mapAiHealthHourDetail(statHour: string, row?: AccountHealthHourRow): AiHealthHourDetail {
  if (!row) return { statHour, status: 'unknown' }
  const statusCode = optionalNumber(row.status_code)
  return {
    statHour,
    status: row.status,
    lastObservedAt: row.last_observed_at,
    ...(statusCode === undefined ? {} : { statusCode }),
    ...(row.error_code === null ? {} : { errorCode: row.error_code }),
    ...(row.error_message === null ? {} : { errorMessage: row.error_message })
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
