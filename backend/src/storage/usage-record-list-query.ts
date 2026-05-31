import { buildSystemAccountScopeClause, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import type { UsageRecordListOptions, UsageRecordSortField } from './usage-records.repository.js'

type UsageRecordFilterValue = string | number

export type NormalizedUsageRecordListOptions = Required<Pick<UsageRecordListOptions, 'page' | 'pageSize' | 'sortBy' | 'sortOrder'>>

export interface UsageRecordFilterResult {
  clause: string
  params: UsageRecordFilterValue[]
}

interface UsageRecordQueryColumns {
  id: string
  systemAccountId: string
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
  const sortBy = options?.sortBy && Object.prototype.hasOwnProperty.call(usageRecordShardSortColumns, options.sortBy)
    ? options.sortBy
    : 'createdAt'
  const sortOrder = options?.sortOrder === 'asc' ? 'asc' : 'desc'
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

export function buildUsageRecordFilters(access?: AccessScope, options?: UsageRecordListOptions): UsageRecordFilterResult {
  return buildUsageRecordFiltersForColumns(access, options, usageRecordShardColumns)
}

export function buildUsageRecordEntryFilters(access?: AccessScope, options?: UsageRecordListOptions): UsageRecordFilterResult {
  return buildUsageRecordFiltersForColumns(access, options, usageRecordEntryColumns)
}

function buildUsageRecordFiltersForColumns(access: AccessScope | undefined, options: UsageRecordListOptions | undefined, columns: UsageRecordQueryColumns): UsageRecordFilterResult {
  const clauses: string[] = []
  const params: UsageRecordFilterValue[] = []
  const scope = buildSystemAccountScopeClause(access, columns.systemAccountId)
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ AND /, ''))
    params.push(...scope.params)
  }
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
  pushPrefixFilter(clauses, params, columns.clientIp, options?.clientIp)
  const groupId = options?.groupId?.trim()
  if (groupId) {
    clauses.push(`${columns.groupId} = ?`)
    params.push(groupId)
  }
  const startAt = options?.startAt?.trim()
  if (startAt) {
    clauses.push(`${columns.createdAt} >= ?`)
    params.push(startAt)
  }
  const endAt = options?.endAt?.trim()
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
    clauses.push(`COALESCE(${columns.trafficSource}, 'gateway') = ?`)
    params.push(trafficSource)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

const usageRecordShardSortColumns = usageRecordSortColumns(usageRecordShardColumns)

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
  const pattern = `${escapeLikePrefix(keyword)}%`
  const ownerSystemAccountId = scopedSystemAccountId(access)
  const ids: string[] = []
  appendAccountIds(ids, database
    .prepare(`
      SELECT accounts.id
      FROM accounts
      WHERE (accounts.name COLLATE NOCASE = ? OR accounts.name LIKE ? ESCAPE '\\')${accountOwnerFilterClause(ownerSystemAccountId)}
      ORDER BY accounts.name COLLATE NOCASE ASC, accounts.id ASC
      LIMIT ?
    `)
    .all(keyword, pattern, ...accountOwnerFilterParams(ownerSystemAccountId), accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
  appendAccountIds(ids, database
    .prepare(`
      SELECT instance_accounts.id
      FROM accounts source_accounts
      INNER JOIN accounts instance_accounts
        ON instance_accounts.authorization_instance_source_account_id = source_accounts.id
      WHERE (source_accounts.name COLLATE NOCASE = ? OR source_accounts.name LIKE ? ESCAPE '\\')${accountOwnerFilterClause(ownerSystemAccountId, 'instance_accounts')}
      ORDER BY source_accounts.name COLLATE NOCASE ASC, instance_accounts.id ASC
      LIMIT ?
    `)
    .all(keyword, pattern, ...accountOwnerFilterParams(ownerSystemAccountId), accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
  if (ownerSystemAccountId) {
    appendAccountIds(ids, database
      .prepare(`
        SELECT accounts.id
        FROM accounts
        INNER JOIN resource_authorizations ra
          ON ra.resource_type = 'account'
          AND ra.resource_id = accounts.id
          AND ra.grantee_system_account_id = ?
        WHERE accounts.name COLLATE NOCASE = ? OR accounts.name LIKE ? ESCAPE '\\'
        ORDER BY accounts.name COLLATE NOCASE ASC, accounts.id ASC
        LIMIT ?
      `)
      .all(ownerSystemAccountId, keyword, pattern, accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
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
        WHERE accounts.name COLLATE NOCASE = ? OR accounts.name LIKE ? ESCAPE '\\'
        ORDER BY accounts.name COLLATE NOCASE ASC, accounts.id ASC
        LIMIT ?
      `)
      .all(ownerSystemAccountId, keyword, pattern, accountKeywordMatchLimit) as unknown as Array<{ id?: string }>)
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

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}

function pushPrefixFilter(clauses: string[], params: UsageRecordFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} >= ? AND ${column} < ?`)
  params.push(text, `${text}\uffff`)
}

const accountKeywordMatchLimit = 200
