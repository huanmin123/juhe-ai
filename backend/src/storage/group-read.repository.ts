import type { AccountUsageStatsRange, AccountUsageSummary } from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, nowIso } from './database.js'
import { compatiblePagedTotal, takePageRows } from './query-utils.js'
import type { GroupListRow } from './repository-row-types.js'
import { loadAuthorizationUsageRangeSummariesForScopes, loadAuthorizationUsageSummariesForScopes, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'

export interface GroupListOptions {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
  providerCode?: string
  manageableOnly?: boolean
  preferDefault?: boolean
}

export interface GroupRowsPage {
  rows: GroupListRow[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface NormalizedGroupListOptions {
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

export function listGroupRowsPageForAccess(access: AccessScope | undefined, options?: GroupListOptions): GroupRowsPage {
  const listOptions = normalizeGroupListOptions(options)
  const rows = queryGroupRowsForAccess(access, {
    limit: listOptions.pageSize + 1,
    offset: (listOptions.page - 1) * listOptions.pageSize
  }, listOptions).rows
  const pageRows = takePageRows(rows, listOptions.pageSize)
  return {
    rows: pageRows.rows,
    total: compatiblePagedTotal(listOptions.page, listOptions.pageSize, pageRows.rows.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: listOptions.page,
    pageSize: listOptions.pageSize
  }
}

function normalizeGroupListOptions(options?: GroupListOptions): NormalizedGroupListOptions {
  const rawPage = options?.page
  const rawPageSize = options?.pageSize ?? options?.limit
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxGroupListPageSize, Math.max(1, rawPageSize))
    : defaultGroupListPageSize
  return {
    keyword: normalizeTextFilter(options?.keyword),
    providerCode: normalizeTextFilter(options?.providerCode),
    manageableOnly: options?.manageableOnly === true,
    preferDefault: options?.preferDefault === true,
    page,
    pageSize
  }
}

function queryGroupRowsForAccess(access?: AccessScope, pagination?: { limit: number; offset: number }, options: Pick<NormalizedGroupListOptions, 'keyword' | 'providerCode' | 'manageableOnly' | 'preferDefault'> = { manageableOnly: false, preferDefault: false }): { rows: GroupListRow[] } {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const orderClause = groupOrderClause(options.preferDefault)
  const directFilter = buildGroupFilter('groups', options)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    const rows = getDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups${whereClause(directFilter.clauses)}${orderClause}${pageClause}`)
      .all(...directFilter.params, ...pageParams) as unknown as GroupListRow[]
    return { rows }
  }
  if (!viewerSystemAccountId) {
    const rows = getDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups${whereClause(directFilter.clauses)}${orderClause}${pageClause}`)
      .all(...directFilter.params, ...pageParams) as unknown as GroupListRow[]
    return { rows }
  }
  if (options.manageableOnly) {
    const ownerFilter = buildGroupFilter('groups', options, ['groups.system_account_id = ?'], [ownerSystemAccountId ?? viewerSystemAccountId])
    const rows = getDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups${whereClause(ownerFilter.clauses)}${orderClause}${pageClause}`)
      .all(...ownerFilter.params, ...pageParams) as unknown as GroupListRow[]
    return { rows }
  }
  const outerFilter = buildGroupFilter(undefined, options)
  const rows = getDatabase()
    .prepare(`
      SELECT ${groupListRowOuterSelectColumns()} FROM (
        SELECT ${groupRowSelectColumns('groups')}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
        FROM groups
        WHERE groups.system_account_id = ?
        UNION ALL
        SELECT ${groupRowSelectColumns('groups')}, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        WHERE ra.resource_type = 'group'
          AND ra.grantee_system_account_id = ?
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND groups.system_account_id <> ?
      )
      ${whereClause(outerFilter.clauses)}
      ${orderClause}
      ${pageClause}
    `)
    .all(ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId, ...outerFilter.params, ...pageParams) as unknown as GroupListRow[]
  return { rows }
}

export function findGroupRowForAccess(access: AccessScope | undefined, groupId: string): GroupListRow | undefined {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return getDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups WHERE groups.id = ?`)
      .get(groupId) as unknown as GroupListRow | undefined
  }
  if (!viewerSystemAccountId) {
    return getDatabase()
      .prepare(`SELECT ${groupRowSelectColumns('groups')}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status FROM groups WHERE groups.id = ?`)
      .get(groupId) as unknown as GroupListRow | undefined
  }
  return getDatabase()
    .prepare(`
      SELECT ${groupListRowOuterSelectColumns()} FROM (
        SELECT ${groupRowSelectColumns('groups')}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
        FROM groups
        WHERE groups.id = ?
          AND groups.system_account_id = ?
        UNION ALL
        SELECT ${groupRowSelectColumns('groups')}, 'authorized' AS access_type, ra.id AS authorization_id, ra.status AS authorization_status
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        WHERE groups.id = ?
          AND ra.resource_type = 'group'
          AND ra.grantee_system_account_id = ?
          AND ra.status = 'active'
          AND (ra.expires_at IS NULL OR ra.expires_at > ?)
          AND groups.system_account_id <> ?
      )
      LIMIT 1
    `)
    .get(groupId, ownerSystemAccountId ?? viewerSystemAccountId, groupId, viewerSystemAccountId, nowIso(), ownerSystemAccountId ?? viewerSystemAccountId) as unknown as GroupListRow | undefined
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
    'created_at',
    'updated_at',
    'access_type',
    'authorization_id',
    'authorization_status'
  ].join(', ')
}

function groupOrderClause(preferDefault: boolean): string {
  return preferDefault ? ' ORDER BY is_default DESC, updated_at DESC, id DESC' : ' ORDER BY updated_at DESC, id DESC'
}

function buildGroupFilter(
  alias: string | undefined,
  options: Pick<NormalizedGroupListOptions, 'keyword' | 'providerCode'>,
  initialClauses: string[] = [],
  initialParams: string[] = []
): { clauses: string[]; params: string[] } {
  const clauses = [...initialClauses]
  const params = [...initialParams]
  const providerCode = options.providerCode?.trim()
  const column = (name: string) => alias ? `${alias}.${name}` : name
  if (providerCode) {
    clauses.push(`${column('provider_code')} COLLATE NOCASE = ?`)
    params.push(providerCode)
  }
  const text = options.keyword?.trim()
  if (text) {
    const prefix = `${escapeLikePrefix(text)}%`
    clauses.push(`(
      ${column('id')} = ?
      OR ${column('id')} LIKE ? ESCAPE '\\'
      OR ${column('name')} COLLATE NOCASE = ?
      OR ${column('name')} LIKE ? ESCAPE '\\'
      OR ${column('provider_code')} COLLATE NOCASE = ?
      OR ${column('provider_code')} LIKE ? ESCAPE '\\'
    )`)
    params.push(text, prefix, text, prefix, text, prefix)
  }
  return { clauses, params }
}

function whereClause(clauses: string[]): string {
  return clauses.length ? ` WHERE ${clauses.join(' AND ')}` : ''
}

function normalizeTextFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
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
