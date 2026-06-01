import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { normalizeListPage, pagedTotalUpperBound, takePageRows } from './query-utils.js'
import type { GroupListRow } from './repository-row-types.js'
import { loadAuthorizationUsageRangeSummariesForScopes, loadAuthorizationUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export interface GroupListOptions {
  page?: number
  pageSize?: number
  ids?: string[]
  keyword?: string
  providerCode?: string
  manageableOnly?: boolean
  preferDefault?: boolean
}

export interface GroupOptionListOptions extends Omit<GroupListOptions, 'pageSize'> {
  limit?: number
}

export interface GroupRowsPage {
  rows: GroupListRow[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface NormalizedGroupListOptions {
  ids: string[]
  keyword?: string
  providerCode?: string
  manageableOnly: boolean
  preferDefault: boolean
  page: number
  pageSize: number
}

const defaultGroupListPageSize = 50
const maxGroupListPageSize = 500

export function listGroupRowsForAccess(access?: AccessScope, options?: GroupListOptions): GroupListRow[] {
  const listOptions = normalizeGroupListOptions(options)
  const pagination = options
    ? { limit: listOptions.pageSize, offset: (listOptions.page - 1) * listOptions.pageSize }
    : undefined
  return queryGroupRowsForAccess(access, pagination, listOptions).rows
}

export function listGroupOptionRowsForAccess(access?: AccessScope, options?: GroupOptionListOptions): GroupListRow[] {
  const listOptions = normalizeGroupOptionListOptions(options)
  const pagination = options
    ? { limit: listOptions.pageSize, offset: (listOptions.page - 1) * listOptions.pageSize }
    : undefined
  return queryGroupRowsForAccess(access, pagination, listOptions).rows
}

export function listGroupRowsPageForAccess(access: AccessScope | undefined, options?: GroupListOptions): GroupRowsPage {
  const listOptions = normalizeGroupListOptions(options)
  const rows = queryGroupRowsForAccess(access, {
    limit: listOptions.pageSize + 1,
    offset: (listOptions.page - 1) * listOptions.pageSize
  }, listOptions).rows
  const pageRows = takePageRows(rows, listOptions.pageSize)
  return {
    rows: pageRows.rows,
    total: pagedTotalUpperBound(listOptions.page, listOptions.pageSize, pageRows.rows.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

function normalizeGroupListOptions(options?: GroupListOptions): NormalizedGroupListOptions {
  const rawPage = options?.page
  const rawPageSize = options?.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxGroupListPageSize, Math.max(1, rawPageSize))
    : defaultGroupListPageSize
  const page = normalizeListPage(rawPage, pageSize)
  return {
    ids: normalizeTextList(options?.ids),
    keyword: normalizeTextFilter(options?.keyword),
    providerCode: normalizeTextFilter(options?.providerCode),
    manageableOnly: options?.manageableOnly === true,
    preferDefault: options?.preferDefault === true,
    page,
    pageSize
  }
}

function normalizeGroupOptionListOptions(options?: GroupOptionListOptions): NormalizedGroupListOptions {
  return normalizeGroupListOptions({ ...options, pageSize: options?.limit })
}

function queryGroupRowsForAccess(access?: AccessScope, pagination?: { limit: number; offset: number }, options: Pick<NormalizedGroupListOptions, 'ids' | 'keyword' | 'providerCode' | 'manageableOnly' | 'preferDefault'> = { ids: [], manageableOnly: false, preferDefault: false }): { rows: GroupListRow[] } {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const orderClause = groupOrderClause(options.preferDefault)
  const directFilter = buildGroupFilter('groups', options)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    const rows = getBusinessDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()} FROM groups${whereClause(directFilter.clauses)}${orderClause}${pageClause}`)
      .all(...directFilter.params, ...pageParams) as unknown as GroupListRow[]
    return { rows }
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }
  if (options.manageableOnly) {
    const ownerFilter = buildGroupFilter('groups', options, ['groups.system_account_id = ?'], [ownerSystemAccountId ?? viewerSystemAccountId])
    const rows = getBusinessDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()} FROM groups${whereClause(ownerFilter.clauses)}${orderClause}${pageClause}`)
      .all(...ownerFilter.params, ...pageParams) as unknown as GroupListRow[]
    return { rows }
  }
  const outerFilter = buildGroupFilter(undefined, options)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${groupListRowOuterSelectColumns()} FROM (
        SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()}
        FROM groups
        WHERE groups.system_account_id = ?
        UNION ALL
        SELECT ${groupRowSelectColumns('groups')}, ${authorizedAuthorizationColumns()}
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        WHERE ra.resource_type = 'group'
          AND ra.grantee_system_account_id = ?
          AND ra.status IN ('active', 'paused', 'expired')
          AND groups.system_account_id <> ?
      )
      ${whereClause(outerFilter.clauses)}
      ${orderClause}
      ${pageClause}
    `)
    .all(ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, ownerSystemAccountId ?? viewerSystemAccountId, ...outerFilter.params, ...pageParams) as unknown as GroupListRow[]
  return { rows }
}

export function findGroupRowForAccess(access: AccessScope | undefined, groupId: string): GroupListRow | undefined {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return getBusinessDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()} FROM groups WHERE groups.id = ?`)
      .get(groupId) as unknown as GroupListRow | undefined
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }
  return getBusinessDatabase()
    .prepare(`
      SELECT ${groupListRowOuterSelectColumns()} FROM (
        SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()}
        FROM groups
        WHERE groups.id = ?
          AND groups.system_account_id = ?
        UNION ALL
        SELECT ${groupRowSelectColumns('groups')}, ${authorizedAuthorizationColumns()}
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        WHERE groups.id = ?
          AND ra.resource_type = 'group'
          AND ra.grantee_system_account_id = ?
          AND ra.status IN ('active', 'paused', 'expired')
          AND groups.system_account_id <> ?
      )
      LIMIT 1
    `)
    .get(groupId, ownerSystemAccountId ?? viewerSystemAccountId, groupId, viewerSystemAccountId, ownerSystemAccountId ?? viewerSystemAccountId) as unknown as GroupListRow | undefined
}

function ownerAuthorizationColumns(): string {
  return [
    "'owner' AS access_type",
    'NULL AS authorization_id',
    'NULL AS authorization_status',
    'NULL AS authorization_expires_at',
    'NULL AS authorization_limits_json'
  ].join(', ')
}

function authorizedAuthorizationColumns(): string {
  return [
    "'authorized' AS access_type",
    'ra.id AS authorization_id',
    'ra.status AS authorization_status',
    'ra.expires_at AS authorization_expires_at',
    'ra.limits_json AS authorization_limits_json'
  ].join(', ')
}

function groupRowSelectColumns(alias: string): string {
  return [
    'id',
    'system_account_id',
    'name',
    'provider_code',
    'description',
    'enabled',
    'is_default',
    'group_type',
    'scheduling_policy_json',
    'created_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

function groupListRowOuterSelectColumns(): string {
  return [
    'id',
    'system_account_id',
    'name',
    'provider_code',
    'description',
    'enabled',
    'is_default',
    'group_type',
    'scheduling_policy_json',
    'created_at',
    'updated_at',
    'access_type',
    'authorization_id',
    'authorization_status',
    'authorization_expires_at',
    'authorization_limits_json'
  ].join(', ')
}

function groupOrderClause(preferDefault: boolean): string {
  return preferDefault ? ' ORDER BY is_default DESC, updated_at DESC, id DESC' : ' ORDER BY updated_at DESC, id DESC'
}

function buildGroupFilter(
  alias: string | undefined,
  options: Pick<NormalizedGroupListOptions, 'ids' | 'keyword' | 'providerCode'>,
  initialClauses: string[] = [],
  initialParams: string[] = []
): { clauses: string[]; params: string[] } {
  const clauses = [...initialClauses]
  const params = [...initialParams]
  const providerCode = options.providerCode?.trim()
  const column = (name: string) => alias ? `${alias}.${name}` : name
  if (options.ids.length) {
    clauses.push(`${column('id')} IN (${options.ids.map(() => '?').join(', ')})`)
    params.push(...options.ids)
  }
  if (providerCode) {
    clauses.push(`${column('provider_code')} COLLATE NOCASE = ?`)
    params.push(providerCode)
  }
  const text = options.keyword?.trim()
  if (text) {
    const prefix = `${escapeLikePrefix(text)}%`
    clauses.push(`(
      ${column('name')} COLLATE NOCASE = ?
      OR ${column('name')} LIKE ? ESCAPE '\\'
      OR ${column('provider_code')} COLLATE NOCASE = ?
      OR ${column('provider_code')} LIKE ? ESCAPE '\\'
    )`)
    params.push(text, prefix, text, prefix)
  }
  return { clauses, params }
}

function whereClause(clauses: string[]): string {
  return clauses.length ? ` WHERE ${clauses.join(' AND ')}` : ''
}

function normalizeTextFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function normalizeTextList(values?: string[]): string[] {
  if (!values?.length) return []
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
    .sort()
    .slice(0, 500)
}

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

export function loadGroupAuthorizationUsageSummaries(
  scopes: UsageSummaryScopeRequest[],
  statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>,
  scopeType: 'group_authorization' | 'group_authorization_team' = 'group_authorization'
): Map<string, AccountUsageSummary> {
  if (statDateOrRange && typeof statDateOrRange !== 'string') {
    return loadAuthorizationUsageRangeSummariesForScopes(scopes, scopeType, statDateOrRange)
  }
  return loadAuthorizationUsageSummariesForScopes(scopes, scopeType, statDateOrRange)
}
