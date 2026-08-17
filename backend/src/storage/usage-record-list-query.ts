import { requiredRfc3339Instant } from '../shared/rfc3339.js'
import { buildSystemAccountScopeClause, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import type { UsageRecordListOptions, UsageRecordSortField } from './usage-records.repository.js'

type UsageRecordFilterValue = string | number

export type NormalizedUsageRecordListOptions = Required<Pick<UsageRecordListOptions, 'page' | 'pageSize' | 'sortBy' | 'sortOrder'>>

export interface UsageRecordFilterResult {
  clause: string
  params: UsageRecordFilterValue[]
  tracePrefixLookup?: boolean
}

export interface UsageRecordFilterSettings {
  textPrefixCollation?: string
  textPrefixUpperBoundMode?: 'append_high' | 'binary'
}

interface UsageRecordQueryColumns {
  id: string
  systemAccountId: string
  traceId: string
  accountId: string
  success: string
  statusCode: string
  clientIp: string
  groupId: string
  createdAt: string
  model: string
  trafficSource: string
  firstTokenMs: string
  durationMs: string
  costUsd: string
}

const usageRecordShardColumns: UsageRecordQueryColumns = {
  id: 'ur.id',
  systemAccountId: 'ur.system_account_id',
  traceId: 'ur.trace_id',
  accountId: 'ur.account_id',
  success: 'ur.success',
  statusCode: 'ur.status_code',
  clientIp: 'ur.client_ip',
  groupId: 'ur.group_id',
  createdAt: 'ur.created_at',
  model: 'ur.model',
  trafficSource: 'ur.traffic_source',
  firstTokenMs: 'ur.first_token_ms',
  durationMs: 'ur.duration_ms',
  costUsd: 'ur.cost_usd'
}

const usageRecordEntryColumns: UsageRecordQueryColumns = {
  ...usageRecordShardColumns,
  id: 'ue.usage_id',
  systemAccountId: 'ue.system_account_id',
  traceId: 'ue.trace_id',
  accountId: 'ue.account_id',
  success: 'ue.success',
  statusCode: 'ue.status_code',
  clientIp: 'ue.client_ip',
  groupId: 'ue.group_id',
  createdAt: 'ue.created_at',
  model: 'ue.model',
  trafficSource: 'ue.traffic_source',
  firstTokenMs: 'ue.first_token_ms',
  durationMs: 'ue.duration_ms',
  costUsd: 'ue.cost_usd'
}

const usageRecordDefaultPageSize = 50
const usageRecordMaxPageSize = 200
const usageRecordMaxListWindowRows = 1001

export function normalizeUsageRecordListOptions(options?: UsageRecordListOptions): NormalizedUsageRecordListOptions {
  const acceptsRequestedSort = options?.sortBy === 'createdAt'
  const sortBy = 'createdAt'
  const sortOrder = acceptsRequestedSort && options?.sortOrder === 'asc' ? 'asc' : 'desc'
  const rawPage = options?.page
  const rawPageSize = options?.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(usageRecordMaxPageSize, Math.max(1, rawPageSize))
    : usageRecordDefaultPageSize
  const maxPage = Math.max(1, Math.floor((usageRecordMaxListWindowRows - 1) / pageSize))
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.min(maxPage, Math.max(1, rawPage)) : 1
  return { page, pageSize, sortBy, sortOrder }
}

export function buildUsageRecordOrderClause(options: NormalizedUsageRecordListOptions): string {
  return buildUsageRecordOrderClauseForColumns(options, usageRecordShardColumns)
}

export function buildUsageRecordEntryOrderClause(options: NormalizedUsageRecordListOptions): string {
  return buildUsageRecordOrderClauseForColumns(options, usageRecordEntryColumns)
}

function buildUsageRecordOrderClauseForColumns(options: NormalizedUsageRecordListOptions, columns: UsageRecordQueryColumns): string {
  const direction = options.sortOrder === 'asc' ? 'ASC' : 'DESC'
  if (options.sortBy === 'createdAt') {
    return `ORDER BY ${columns.createdAt} ${direction}, ${columns.id} ${direction}`
  }
  return `ORDER BY ${usageRecordSortColumns(columns)[options.sortBy]} ${direction}, ${columns.createdAt} ${direction}, ${columns.id} ${direction}`
}

export function buildUsageRecordFilters(access?: AccessScope, options?: UsageRecordListOptions, settings?: UsageRecordFilterSettings): UsageRecordFilterResult {
  return buildUsageRecordFiltersForColumns(access, options, usageRecordShardColumns, settings)
}

export function buildUsageRecordEntryFilters(access?: AccessScope, options?: UsageRecordListOptions, settings?: UsageRecordFilterSettings): UsageRecordFilterResult {
  return buildUsageRecordFiltersForColumns(access, options, usageRecordEntryColumns, settings)
}

function buildUsageRecordFiltersForColumns(
  access: AccessScope | undefined,
  options: UsageRecordListOptions | undefined,
  columns: UsageRecordQueryColumns,
  settings?: UsageRecordFilterSettings
): UsageRecordFilterResult {
  const clauses: string[] = []
  const params: UsageRecordFilterValue[] = []
  const scope = buildSystemAccountScopeClause(access, columns.systemAccountId)
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ AND /, ''))
    params.push(...scope.params)
  }
  const tracePrefixText = options?.traceId?.trim()
  pushPrefixFilter(clauses, params, columns.traceId, tracePrefixText, settings)
  const accountKeyword = options?.accountKeyword?.trim()
  if (accountKeyword) {
    const matchedAccountIds = accountIdsForKeyword(accountKeyword, access)
    if (matchedAccountIds.length > 0) {
      clauses.push(`${columns.accountId} IN (${matchedAccountIds.map(() => '?').join(', ')})`)
      params.push(...matchedAccountIds)
    } else {
      clauses.push('1 = 0')
    }
  }
  if (options?.result === 'success') {
    clauses.push(`${columns.success} = 1`)
  } else if (options?.result === 'failed') {
    clauses.push(`${columns.success} = 0`)
  }
  if (isHttpStatusCode(options?.statusCode)) {
    clauses.push(`${columns.statusCode} = ?`)
    params.push(options.statusCode)
  }
  pushPrefixFilter(clauses, params, columns.clientIp, options?.clientIp, settings)
  const groupId = options?.groupId?.trim()
  if (groupId) {
    clauses.push(`${columns.groupId} = ?`)
    params.push(groupId)
  }
  const startAt = normalizeUsageRecordBoundary(options?.startAt, '使用记录开始时间')
  if (startAt) {
    clauses.push(`${columns.createdAt} >= ?`)
    params.push(startAt)
  }
  const endAt = normalizeUsageRecordBoundary(options?.endAt, '使用记录结束时间')
  if (endAt) {
    clauses.push(`${columns.createdAt} < ?`)
    params.push(endAt)
  }
  const model = options?.model?.trim()
  if (model) {
    clauses.push(`${columns.model} = ?`)
    params.push(model)
  }
  const trafficSource = options?.trafficSource
  if (trafficSource) {
    clauses.push(`${columns.trafficSource} = ?`)
    params.push(trafficSource)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params,
    tracePrefixLookup: Boolean(tracePrefixText && settings?.textPrefixCollation)
  }
}

function normalizeUsageRecordBoundary(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  return requiredRfc3339Instant(value, label)
}

function usageRecordSortColumns(columns: UsageRecordQueryColumns): Record<UsageRecordSortField, string> {
  return {
    createdAt: columns.createdAt,
    firstTokenMs: columns.firstTokenMs,
    durationMs: columns.durationMs,
    costUsd: columns.costUsd
  }
}

function accountIdsForKeyword(keyword: string, access?: AccessScope): string[] {
  const database = getBusinessDatabase()
  const normalizedKeyword = normalizeAccountKeyword(keyword)
  const upperBound = accountKeywordUpperBound(normalizedKeyword)
  const ownerSystemAccountId = scopedSystemAccountId(access)
  const ids: string[] = []
  appendAccountIds(ids, database
    .prepare(`
      SELECT accounts.id
      FROM accounts
      WHERE accounts.deleted_at IS NULL
        AND accounts.name >= ? AND accounts.name < ?${accountOwnerFilterClause(ownerSystemAccountId)}
      ORDER BY accounts.name ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(normalizedKeyword, upperBound, ...accountOwnerFilterParams(ownerSystemAccountId), accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
  appendAccountIds(ids, database
    .prepare(`
      SELECT instance_accounts.id
      FROM accounts source_accounts
      CROSS JOIN accounts instance_accounts
      WHERE source_accounts.deleted_at IS NULL
        AND instance_accounts.authorization_instance_source_account_id = source_accounts.id
        AND instance_accounts.deleted_at IS NULL
        AND source_accounts.name >= ? AND source_accounts.name < ?${accountOwnerFilterClause(ownerSystemAccountId, 'instance_accounts')}
      ORDER BY source_accounts.name ASC, instance_accounts.id ASC
      LIMIT ?
    `)
    .all(normalizedKeyword, upperBound, ...accountOwnerFilterParams(ownerSystemAccountId), accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
  if (ownerSystemAccountId) {
    appendAccountIds(ids, database
      .prepare(`
        SELECT accounts.id
        FROM accounts
        INNER JOIN resource_authorizations ra
          ON ra.resource_type = 'account'
          AND ra.resource_id = accounts.id
          AND ra.grantee_system_account_id = ?
        WHERE accounts.deleted_at IS NULL
          AND accounts.name >= ? AND accounts.name < ?
        ORDER BY accounts.name ASC, accounts.id ASC
        LIMIT ?
      `)
      .all(ownerSystemAccountId, normalizedKeyword, upperBound, accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
    appendAccountIds(ids, database
      .prepare(`
        SELECT accounts.id
        FROM accounts
        INNER JOIN group_accounts ga
          ON ga.account_id = accounts.id
          AND ga.enabled = 1
        INNER JOIN resource_authorizations ra
          ON ra.resource_type = 'group'
          AND ra.resource_id = ga.group_id
          AND ra.grantee_system_account_id = ?
        WHERE accounts.deleted_at IS NULL
          AND accounts.name >= ? AND accounts.name < ?
        ORDER BY accounts.name ASC, accounts.id ASC
        LIMIT ?
      `)
      .all(ownerSystemAccountId, normalizedKeyword, upperBound, accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
  }
  return ids.slice(0, accountKeywordMatchLimit)
}

function appendAccountIds(target: string[], rows: Array<{ id?: string }>): void {
  const seen = new Set(target)
  for (const row of rows) {
    if (!row.id || seen.has(row.id) || target.length >= accountKeywordMatchLimit) continue
    target.push(row.id)
    seen.add(row.id)
  }
}

function accountOwnerFilterClause(systemAccountId?: string, tableName = 'accounts'): string {
  return systemAccountId ? ` AND ${tableName}.system_account_id = ?` : ''
}

function accountOwnerFilterParams(systemAccountId?: string): string[] {
  return systemAccountId ? [systemAccountId] : []
}

function normalizeAccountKeyword(value: string): string {
  return value.normalize('NFKC').trim()
}

function accountKeywordUpperBound(value: string): string {
  return `${value}\uffff`
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}

function pushPrefixFilter(
  clauses: string[],
  params: UsageRecordFilterValue[],
  column: string,
  value: string | undefined,
  settings?: UsageRecordFilterSettings
): void {
  const text = value?.trim()
  if (!text) return
  const columnExpression = settings?.textPrefixCollation ? `${column} COLLATE ${settings.textPrefixCollation}` : column
  clauses.push(`${columnExpression} >= ? AND ${columnExpression} < ?`)
  params.push(text, usageRecordTextPrefixUpperBound(text, settings))
}

const accountKeywordMatchLimit = 200

function usageRecordTextPrefixUpperBound(value: string, settings?: UsageRecordFilterSettings): string {
  if (settings?.textPrefixUpperBoundMode === 'binary') {
    return usageRecordBinaryPrefixUpperBound(value)
  }
  return `${value}\uffff`
}

function usageRecordBinaryPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\u{10ffff}`
}
