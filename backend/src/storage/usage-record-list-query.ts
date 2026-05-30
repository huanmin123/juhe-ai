import { buildSystemAccountScopeClause, scopedSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase } from './database.js'
import type { UsageRecordListOptions, UsageRecordSortField } from './usage-records.repository.js'

type UsageRecordFilterValue = string | number

export type NormalizedUsageRecordListOptions = Required<Pick<UsageRecordListOptions, 'page' | 'pageSize' | 'sortBy' | 'sortOrder'>>

export interface UsageRecordFilterResult {
  clause: string
  params: UsageRecordFilterValue[]
}

const usageRecordSortColumns: Record<UsageRecordSortField, string> = {
  createdAt: 'ur.created_at',
  firstTokenMs: 'ur.first_token_ms',
  durationMs: 'ur.duration_ms',
  costUsd: 'ur.cost_usd'
}

const usageRecordDefaultPageSize = 50
const usageRecordMaxPageSize = 200

export function normalizeUsageRecordListOptions(options?: UsageRecordListOptions): NormalizedUsageRecordListOptions {
  const sortBy = options?.sortBy && Object.prototype.hasOwnProperty.call(usageRecordSortColumns, options.sortBy)
    ? options.sortBy
    : 'createdAt'
  const sortOrder = options?.sortOrder === 'asc' ? 'asc' : 'desc'
  const rawPage = options?.page
  const rawPageSize = options?.pageSize ?? options?.limit
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(usageRecordMaxPageSize, Math.max(1, rawPageSize))
    : usageRecordDefaultPageSize
  return { page, pageSize, sortBy, sortOrder }
}

export function buildUsageRecordOrderClause(options: NormalizedUsageRecordListOptions): string {
  const direction = options.sortOrder === 'asc' ? 'ASC' : 'DESC'
  if (options.sortBy === 'createdAt') {
    return `ORDER BY ur.created_at ${direction}, ur.id ${direction}`
  }
  return `ORDER BY ${usageRecordSortColumns[options.sortBy]} ${direction}, ur.created_at ${direction}, ur.id ${direction}`
}

export function buildUsageRecordFilters(access?: AccessScope, options?: UsageRecordListOptions): UsageRecordFilterResult {
  const clauses: string[] = []
  const params: UsageRecordFilterValue[] = []
  const scope = buildSystemAccountScopeClause(access, 'ur.system_account_id')
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ AND /, ''))
    params.push(...scope.params)
  }
  const accountKeyword = options?.accountKeyword?.trim()
  if (accountKeyword) {
    const matchedAccountIds = accountIdsForKeyword(accountKeyword, access)
    if (matchedAccountIds.length > 0) {
      clauses.push(`ur.account_id IN (${matchedAccountIds.map(() => '?').join(', ')})`)
      params.push(...matchedAccountIds)
    } else {
      clauses.push('1 = 0')
    }
  }
  if (options?.result === 'success') {
    clauses.push('ur.success = 1')
  } else if (options?.result === 'failed') {
    clauses.push('ur.success = 0')
  }
  if (isHttpStatusCode(options?.statusCode)) {
    clauses.push('ur.status_code = ?')
    params.push(options.statusCode)
  }
  pushPrefixFilter(clauses, params, 'ur.client_ip', options?.clientIp)
  const groupId = options?.groupId?.trim()
  if (groupId) {
    clauses.push('ur.group_id = ?')
    params.push(groupId)
  }
  const startAt = options?.startAt?.trim()
  if (startAt) {
    clauses.push('ur.created_at >= ?')
    params.push(startAt)
  }
  const endAt = options?.endAt?.trim()
  if (endAt) {
    clauses.push('ur.created_at < ?')
    params.push(endAt)
  }
  const model = options?.model?.trim()
  if (model) {
    clauses.push('ur.model = ?')
    params.push(model)
  }
  const trafficSource = options?.trafficSource
  if (trafficSource) {
    clauses.push("COALESCE(ur.traffic_source, 'gateway') = ?")
    params.push(trafficSource)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function accountIdsForKeyword(keyword: string, access?: AccessScope): string[] {
  const database = getDatabase()
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
