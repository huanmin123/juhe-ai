import type { DatabaseSync } from 'node:sqlite'

import type { AccountUsageStatsRange, AccountUsageSummary, ResourceAuthorizationListResult, ResourceAuthorizationSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, getStatsDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { loadGroupAuthorizationUsageSummaries } from './group-read.repository.js'
import { getPostgresPool } from './postgres-client.js'
import {
  authorizationDirectionFilter,
  authorizationStatusFilter,
  compareResourceAuthorizationOperations,
  normalizeResourceType,
  resourceAuthorizationGrantSourceSummary,
  sanitizeResourceAuthorizationSummaryForAccess,
  withResourceAuthorizationPermissions
} from './resource-authorization-list-helpers.js'
import { resourceAuthorizationSelectColumns, usageScope } from './resource-authorization-helpers.js'
import {
  loadAccountLookupMap,
  loadAccountLookupMapAsync,
  loadGroupNameMap,
  loadGroupNameMapAsync,
  loadSystemAccountPrincipalMapByIds,
  loadSystemAccountPrincipalMapByIdsAsync,
  loadSystemTeamNameMap,
  loadSystemTeamNameMapAsync
} from './repository-lookups.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import type { ResourceAuthorizationGrantRow, ResourceAuthorizationRow } from './repository-row-types.js'
import { chunkValues, normalizeListPage, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone, usageStatsTimezoneAsync, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { loadAuthorizationUsageRangeSummariesForScopesAsync, loadAuthorizationUsageSummariesForScopesAsync, type UsageSummaryScopeRequest } from './usage-summary-loaders.js'
import { optionalString } from './value-utils.js'

const RUNTIME_AUTHORIZATION_BATCH_SIZE = 200
const TEAM_REPORT_USAGE_BATCH_SIZE = 100
const defaultResourceAuthorizationPageSize = 50
const maxResourceAuthorizationPageSize = 500
const businessSchemaName = 'juhe_business'
const statsSchemaName = 'juhe_stats'

export interface ResourceAuthorizationListOptions {
  usageRange?: AccountUsageStatsRange
  includeUsage?: boolean
  page?: number
  pageSize?: number
}

export async function listAccountAuthorizationGranteeIdsAsync(accountId: string): Promise<string[]> {
  const normalizedAccountId = accountId.trim()
  if (!normalizedAccountId) return []
  const client = await getResourceAuthorizationReadDatabaseClient()
  const rows = await client.query<{ grantee_system_account_id: string }>(`
    SELECT DISTINCT grantee_system_account_id
    FROM ${resourceAuthorizationReadTable(client, 'resource_authorizations')}
    WHERE resource_type = 'account'
      AND resource_id = ?
    ORDER BY grantee_system_account_id ASC
  `, [normalizedAccountId])
  return rows.map((row) => row.grantee_system_account_id).filter(Boolean)
}

interface NormalizedResourceAuthorizationPageOptions {
  page: number
  pageSize: number
}

interface ResourceAuthorizationGrantRowsPage {
  rows: ResourceAuthorizationGrantRow[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

interface ResourceAuthorizationReadTables {
  resourceAuthorizationGrants: string
  resourceAuthorizations: string
  resourceAuthorizationSources: string
  systemTeamMembers: string
  systemAccounts: string
  systemTeams: string
  accounts: string
  groups: string
}

export function listResourceAuthorizationSummaries(filters: Record<string, unknown>, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): ResourceAuthorizationSummary[] {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const grantRows = listResourceAuthorizationGrantOperationRows(filters, access)
  const summaries = resourceAuthorizationGrantSummaries(grantRows, options)
    .sort(compareResourceAuthorizationOperations)
  return summaries.map((summary) => withResourceAuthorizationPermissions(
    sanitizeResourceAuthorizationSummaryForAccess(summary, access),
    viewerSystemAccountId,
    access
  ))
}

export function listResourceAuthorizationSummariesPage(filters: Record<string, unknown>, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): ResourceAuthorizationListResult {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const page = listResourceAuthorizationGrantOperationRowsPage(filters, access, options)
  const items = resourceAuthorizationGrantSummaries(page.rows, options)
    .sort(compareResourceAuthorizationOperations)
    .map((summary) => withResourceAuthorizationPermissions(
      sanitizeResourceAuthorizationSummaryForAccess(summary, access),
      viewerSystemAccountId,
      access
    ))
  return {
    items,
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export async function listResourceAuthorizationSummariesPageAsync(filters: Record<string, unknown>, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): Promise<ResourceAuthorizationListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listResourceAuthorizationSummariesPage(filters, access, options)
  }
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const page = await listResourceAuthorizationGrantOperationRowsPageAsync(filters, access, options)
  const items = (await resourceAuthorizationGrantSummariesAsync(page.rows, options))
    .sort(compareResourceAuthorizationOperations)
    .map((summary) => withResourceAuthorizationPermissions(
      sanitizeResourceAuthorizationSummaryForAccess(summary, access),
      viewerSystemAccountId,
      access
    ))
  return {
    items,
    total: page.total,
    hasMore: page.hasMore,
    page: page.page,
    pageSize: page.pageSize
  }
}

export function findResourceAuthorizationSummary(id: string, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): ResourceAuthorizationSummary | undefined {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const grantRows = listResourceAuthorizationGrantOperationRows({ id, status: 'all' }, access)
  const summary = resourceAuthorizationGrantSummaries(grantRows.slice(0, 1), options)[0]
  return summary
    ? withResourceAuthorizationPermissions(
      sanitizeResourceAuthorizationSummaryForAccess(summary, access),
      viewerSystemAccountId,
      access
    )
    : undefined
}

export async function findResourceAuthorizationSummaryAsync(id: string, access?: AccessScope, options: ResourceAuthorizationListOptions = {}): Promise<ResourceAuthorizationSummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return findResourceAuthorizationSummary(id, access, options)
  }
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const grantRows = await listResourceAuthorizationGrantOperationRowsAsync({ id, status: 'all' }, access, { limit: 1, offset: 0 })
  const summary = (await resourceAuthorizationGrantSummariesAsync(grantRows.slice(0, 1), options))[0]
  return summary
    ? withResourceAuthorizationPermissions(
      sanitizeResourceAuthorizationSummaryForAccess(summary, access),
      viewerSystemAccountId,
      access
    )
    : undefined
}

function listResourceAuthorizationGrantOperationRowsPage(filters: Record<string, unknown>, access: AccessScope | undefined, options: ResourceAuthorizationListOptions): ResourceAuthorizationGrantRowsPage {
  const pageOptions = normalizeResourceAuthorizationPageOptions(options)
  const rows = listResourceAuthorizationGrantOperationRows(filters, access, {
    limit: pageOptions.pageSize + 1,
    offset: (pageOptions.page - 1) * pageOptions.pageSize
  })
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  return {
    rows: pageRows.rows,
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, pageRows.rows.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: pageOptions.page,
    pageSize: pageOptions.pageSize
  }
}

async function listResourceAuthorizationGrantOperationRowsPageAsync(filters: Record<string, unknown>, access: AccessScope | undefined, options: ResourceAuthorizationListOptions): Promise<ResourceAuthorizationGrantRowsPage> {
  const pageOptions = normalizeResourceAuthorizationPageOptions(options)
  const rows = await listResourceAuthorizationGrantOperationRowsAsync(filters, access, {
    limit: pageOptions.pageSize + 1,
    offset: (pageOptions.page - 1) * pageOptions.pageSize
  })
  const pageRows = takePageRows(rows, pageOptions.pageSize)
  return {
    rows: pageRows.rows,
    total: pagedTotalUpperBound(pageOptions.page, pageOptions.pageSize, pageRows.rows.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: pageOptions.page,
    pageSize: pageOptions.pageSize
  }
}

function normalizeResourceAuthorizationPageOptions(options: ResourceAuthorizationListOptions): NormalizedResourceAuthorizationPageOptions {
  const rawPage = options.page
  const rawPageSize = options.pageSize
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxResourceAuthorizationPageSize, Math.max(1, rawPageSize))
    : defaultResourceAuthorizationPageSize
  const page = normalizeListPage(rawPage, pageSize)
  return { page, pageSize }
}

function listResourceAuthorizationGrantOperationRows(filters: Record<string, unknown>, access?: AccessScope, pagination?: { limit: number; offset: number }): ResourceAuthorizationGrantRow[] {
  const clauses: string[] = []
  const params: Array<string | number | null> = []
  const grantId = optionalString(filters.id)
  if (grantId) { clauses.push('rag.id = ?'); params.push(grantId) }
  const resourceType = normalizeResourceType(filters.resourceType)
  if (resourceType) { clauses.push('rag.resource_type = ?'); params.push(resourceType) }
  const resourceId = optionalString(filters.resourceId)
  if (resourceId) { clauses.push('rag.resource_id = ?'); params.push(resourceId) }
  const granteeSystemAccountId = optionalString(filters.granteeSystemAccountId)
  if (granteeSystemAccountId) {
    clauses.push('rag.grantee_type = ?')
    params.push('system_account')
    clauses.push('rag.grantee_system_account_id = ?')
    params.push(granteeSystemAccountId)
  }
  const status = authorizationStatusFilter(filters.status)
  if (status) { clauses.push('rag.status = ?'); params.push(status) }
  const sourceType = optionalString(filters.sourceType)
  if (sourceType === 'manual') {
    clauses.push('rag.grantee_type = ?')
    params.push('system_account')
  } else if (sourceType === 'team') {
    clauses.push('rag.grantee_type = ?')
    params.push('team')
  }
  const teamId = optionalString(filters.teamId)
  if (teamId) {
    if (!canAccessAll(access)) {
      clauses.push('EXISTS (SELECT 1 FROM system_team_members stm_scope WHERE stm_scope.team_id = ? AND stm_scope.system_account_id = ? AND stm_scope.status = \'active\')')
      params.push(teamId, currentSystemAccountId(access))
    }
    clauses.push('rag.grantee_type = ?')
    params.push('team')
    clauses.push('rag.grantee_team_id = ?')
    params.push(teamId)
  }
  const ownerSystemAccountId = optionalString(filters.resourceOwnerSystemAccountId)
  if (ownerSystemAccountId) {
    clauses.push('rag.resource_owner_system_account_id = ?')
    params.push(ownerSystemAccountId)
  }
  const keywordFilter = resourceAuthorizationKeywordFilter(filters.keyword)
  if (keywordFilter.clause) {
    clauses.push(keywordFilter.clause)
    params.push(...keywordFilter.params)
  }
  const scopeSystemAccountId = scopedSystemAccountId(access)
  if (scopeSystemAccountId) {
    clauses.push(`(rag.resource_owner_system_account_id = ? OR rag.grantee_system_account_id = ? OR EXISTS (
      SELECT 1
      FROM system_team_members stm_scope
      WHERE stm_scope.team_id = rag.grantee_team_id
        AND stm_scope.system_account_id = ?
        AND stm_scope.status = 'active'
    ))`)
    params.push(scopeSystemAccountId, scopeSystemAccountId, scopeSystemAccountId)
  } else if (!canAccessAll(access)) {
    clauses.push(`(rag.resource_owner_system_account_id = ? OR rag.grantee_system_account_id = ? OR EXISTS (
      SELECT 1
      FROM system_team_members stm_scope
      WHERE stm_scope.team_id = rag.grantee_team_id
        AND stm_scope.system_account_id = ?
        AND stm_scope.status = 'active'
    ))`)
    params.push(currentSystemAccountId(access), currentSystemAccountId(access), currentSystemAccountId(access))
  }
  const direction = authorizationDirectionFilter(filters.direction)
  const directionSystemAccountId = scopeSystemAccountId ?? (!canAccessAll(access) ? currentSystemAccountId(access) : undefined)
  if (direction && directionSystemAccountId) {
    if (direction === 'outbound') {
      clauses.push('rag.resource_owner_system_account_id = ?')
      params.push(directionSystemAccountId)
    } else {
      clauses.push(`(rag.grantee_system_account_id = ? OR EXISTS (
        SELECT 1
        FROM system_team_members stm_direction
        WHERE stm_direction.team_id = rag.grantee_team_id
          AND stm_direction.system_account_id = ?
          AND stm_direction.status = 'active'
      ))`)
      params.push(directionSystemAccountId, directionSystemAccountId)
    }
  }
  const where = clauses.length ? `WHERE ${clauses.join(' AND ')}` : ''
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  return getBusinessDatabase().prepare(`SELECT ${resourceAuthorizationGrantSelectColumns('rag')} FROM resource_authorization_grants rag ${where} ORDER BY rag.created_at DESC, rag.id DESC${pageClause}`).all(...params, ...pageParams) as unknown as ResourceAuthorizationGrantRow[]
}

async function listResourceAuthorizationGrantOperationRowsAsync(filters: Record<string, unknown>, access?: AccessScope, pagination?: { limit: number; offset: number }): Promise<ResourceAuthorizationGrantRow[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listResourceAuthorizationGrantOperationRows(filters, access, pagination)
  }
  const client = await getResourceAuthorizationReadDatabaseClient()
  const tables = resourceAuthorizationReadTables(client)
  const filter = resourceAuthorizationGrantOperationFilterForClient(filters, access, tables)
  const where = filter.clauses.length ? `WHERE ${filter.clauses.join(' AND ')}` : ''
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  return client.query<ResourceAuthorizationGrantRow>(`
    SELECT ${resourceAuthorizationGrantSelectColumns('rag')}
    FROM ${tables.resourceAuthorizationGrants} rag
    ${where}
    ORDER BY rag.created_at DESC, rag.id DESC${pageClause}
  `, [...filter.params, ...pageParams])
}

function resourceAuthorizationGrantOperationFilterForClient(
  filters: Record<string, unknown>,
  access: AccessScope | undefined,
  tables: ResourceAuthorizationReadTables
): { clauses: string[]; params: Array<string | number | null> } {
  const clauses: string[] = []
  const params: Array<string | number | null> = []
  const grantId = optionalString(filters.id)
  if (grantId) { clauses.push('rag.id = ?'); params.push(grantId) }
  const resourceType = normalizeResourceType(filters.resourceType)
  if (resourceType) { clauses.push('rag.resource_type = ?'); params.push(resourceType) }
  const resourceId = optionalString(filters.resourceId)
  if (resourceId) { clauses.push('rag.resource_id = ?'); params.push(resourceId) }
  const granteeSystemAccountId = optionalString(filters.granteeSystemAccountId)
  if (granteeSystemAccountId) {
    clauses.push('rag.grantee_type = ?')
    params.push('system_account')
    clauses.push('rag.grantee_system_account_id = ?')
    params.push(granteeSystemAccountId)
  }
  const status = authorizationStatusFilter(filters.status)
  if (status) { clauses.push('rag.status = ?'); params.push(status) }
  const sourceType = optionalString(filters.sourceType)
  if (sourceType === 'manual') {
    clauses.push('rag.grantee_type = ?')
    params.push('system_account')
  } else if (sourceType === 'team') {
    clauses.push('rag.grantee_type = ?')
    params.push('team')
  }
  const teamId = optionalString(filters.teamId)
  if (teamId) {
    if (!canAccessAll(access)) {
      clauses.push(`EXISTS (SELECT 1 FROM ${tables.systemTeamMembers} stm_scope WHERE stm_scope.team_id = ? AND stm_scope.system_account_id = ? AND stm_scope.status = 'active')`)
      params.push(teamId, currentSystemAccountId(access))
    }
    clauses.push('rag.grantee_type = ?')
    params.push('team')
    clauses.push('rag.grantee_team_id = ?')
    params.push(teamId)
  }
  const ownerSystemAccountId = optionalString(filters.resourceOwnerSystemAccountId)
  if (ownerSystemAccountId) {
    clauses.push('rag.resource_owner_system_account_id = ?')
    params.push(ownerSystemAccountId)
  }
  const keywordFilter = resourceAuthorizationKeywordFilterForClient(filters.keyword, tables)
  if (keywordFilter.clause) {
    clauses.push(keywordFilter.clause)
    params.push(...keywordFilter.params)
  }
  const scopeSystemAccountId = scopedSystemAccountId(access)
  if (scopeSystemAccountId) {
    clauses.push(`(rag.resource_owner_system_account_id = ? OR rag.grantee_system_account_id = ? OR EXISTS (
      SELECT 1
      FROM ${tables.systemTeamMembers} stm_scope
      WHERE stm_scope.team_id = rag.grantee_team_id
        AND stm_scope.system_account_id = ?
        AND stm_scope.status = 'active'
    ))`)
    params.push(scopeSystemAccountId, scopeSystemAccountId, scopeSystemAccountId)
  } else if (!canAccessAll(access)) {
    clauses.push(`(rag.resource_owner_system_account_id = ? OR rag.grantee_system_account_id = ? OR EXISTS (
      SELECT 1
      FROM ${tables.systemTeamMembers} stm_scope
      WHERE stm_scope.team_id = rag.grantee_team_id
        AND stm_scope.system_account_id = ?
        AND stm_scope.status = 'active'
    ))`)
    params.push(currentSystemAccountId(access), currentSystemAccountId(access), currentSystemAccountId(access))
  }
  const direction = authorizationDirectionFilter(filters.direction)
  const directionSystemAccountId = scopeSystemAccountId ?? (!canAccessAll(access) ? currentSystemAccountId(access) : undefined)
  if (direction && directionSystemAccountId) {
    if (direction === 'outbound') {
      clauses.push('rag.resource_owner_system_account_id = ?')
      params.push(directionSystemAccountId)
    } else {
      clauses.push(`(rag.grantee_system_account_id = ? OR EXISTS (
        SELECT 1
        FROM ${tables.systemTeamMembers} stm_direction
        WHERE stm_direction.team_id = rag.grantee_team_id
          AND stm_direction.system_account_id = ?
          AND stm_direction.status = 'active'
      ))`)
      params.push(directionSystemAccountId, directionSystemAccountId)
    }
  }
  return { clauses, params }
}

function resourceAuthorizationKeywordFilter(value: unknown): { clause: string; params: string[] } {
  const keyword = optionalString(value)?.trim()
  if (!keyword) return { clause: '', params: [] }
  const upperBound = textPrefixUpperBound(keyword)
  let matchCount = 0
  const matchText = (expression: string) => {
    matchCount += 1
    return `(${expression} >= ? AND ${expression} < ?)`
  }
  const clause = `(
    ${matchText('rag.id')}
    OR ${matchText('rag.resource_id')}
    OR ${matchText('rag.remark')}
    OR EXISTS (
      SELECT 1
      FROM system_accounts owner_accounts
      WHERE owner_accounts.id = rag.resource_owner_system_account_id
        AND (
          ${matchText('owner_accounts.username')}
          OR ${matchText('owner_accounts.display_name')}
        )
    )
    OR (
      rag.grantee_type = 'system_account'
      AND EXISTS (
        SELECT 1
        FROM system_accounts grantee_accounts
        WHERE grantee_accounts.id = rag.grantee_system_account_id
          AND (
            ${matchText('grantee_accounts.username')}
            OR ${matchText('grantee_accounts.display_name')}
          )
      )
    )
    OR (
      rag.grantee_type = 'team'
      AND EXISTS (
        SELECT 1
        FROM system_teams grantee_teams
        WHERE grantee_teams.id = rag.grantee_team_id
          AND ${matchText('grantee_teams.name')}
      )
    )
    OR (
      rag.resource_type = 'account'
      AND EXISTS (
        SELECT 1
        FROM accounts resource_accounts
        WHERE resource_accounts.id = rag.resource_id
          AND ${matchText('resource_accounts.name')}
      )
    )
    OR (
      rag.resource_type = 'account'
      AND EXISTS (
        SELECT 1
        FROM resource_authorizations resource_runtime
        INNER JOIN accounts authorization_instances
          ON authorization_instances.authorization_instance_authorization_id = resource_runtime.id
        WHERE resource_runtime.resource_type = 'account'
          AND resource_runtime.resource_id = rag.resource_id
          AND ${matchText('authorization_instances.name')}
      )
    )
    OR (
      rag.resource_type = 'group'
      AND EXISTS (
        SELECT 1
        FROM groups resource_groups
        WHERE resource_groups.id = rag.resource_id
          AND ${matchText('resource_groups.name')}
      )
    )
  )`
  return {
    clause,
    params: Array.from({ length: matchCount }, () => [keyword, upperBound]).flat()
  }
}

function resourceAuthorizationKeywordFilterForClient(value: unknown, tables: ResourceAuthorizationReadTables): { clause: string; params: string[] } {
  const keyword = optionalString(value)?.trim()
  if (!keyword) return { clause: '', params: [] }
  const upperBound = textPrefixUpperBound(keyword)
  let matchCount = 0
  const matchText = (expression: string) => {
    matchCount += 1
    return `(${expression} COLLATE "C" >= ? AND ${expression} COLLATE "C" < ? AND starts_with(${expression}, ?))`
  }
  const clause = `(
    ${matchText('rag.id')}
    OR ${matchText('rag.resource_id')}
    OR ${matchText('rag.remark')}
    OR EXISTS (
      SELECT 1
      FROM ${tables.systemAccounts} owner_accounts
      WHERE owner_accounts.id = rag.resource_owner_system_account_id
        AND (
          ${matchText('owner_accounts.username')}
          OR ${matchText('owner_accounts.display_name')}
        )
    )
    OR (
      rag.grantee_type = 'system_account'
      AND EXISTS (
        SELECT 1
        FROM ${tables.systemAccounts} grantee_accounts
        WHERE grantee_accounts.id = rag.grantee_system_account_id
          AND (
            ${matchText('grantee_accounts.username')}
            OR ${matchText('grantee_accounts.display_name')}
          )
      )
    )
    OR (
      rag.grantee_type = 'team'
      AND EXISTS (
        SELECT 1
        FROM ${tables.systemTeams} grantee_teams
        WHERE grantee_teams.id = rag.grantee_team_id
          AND ${matchText('grantee_teams.name')}
      )
    )
    OR (
      rag.resource_type = 'account'
      AND EXISTS (
        SELECT 1
        FROM ${tables.accounts} resource_accounts
        WHERE resource_accounts.id = rag.resource_id
          AND ${matchText('resource_accounts.name')}
      )
    )
    OR (
      rag.resource_type = 'account'
      AND EXISTS (
        SELECT 1
        FROM ${tables.resourceAuthorizations} resource_runtime
        INNER JOIN ${tables.accounts} authorization_instances
          ON authorization_instances.authorization_instance_authorization_id = resource_runtime.id
        WHERE resource_runtime.resource_type = 'account'
          AND resource_runtime.resource_id = rag.resource_id
          AND ${matchText('authorization_instances.name')}
      )
    )
    OR (
      rag.resource_type = 'group'
      AND EXISTS (
        SELECT 1
        FROM ${tables.groups} resource_groups
        WHERE resource_groups.id = rag.resource_id
          AND ${matchText('resource_groups.name')}
      )
    )
  )`
  return {
    clause,
    params: Array.from({ length: matchCount }, () => [keyword, upperBound, keyword]).flat()
  }
}

function textPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\u{10ffff}`
}

function resourceAuthorizationGrantSelectColumns(alias: string): string {
  return [
    'id',
    'resource_type',
    'resource_id',
    'resource_owner_system_account_id',
    'grantee_type',
    'grantee_system_account_id',
    'grantee_team_id',
    'scope',
    'status',
    'remark',
    'expires_at',
    'limits_json',
    'created_by',
    'created_at',
    'revoked_by',
    'revoked_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

export function loadRuntimeAuthorizationForUserGrant(row: ResourceAuthorizationGrantRow, database = getBusinessDatabase()): ResourceAuthorizationRow | undefined {
  if (!row.grantee_system_account_id) return undefined
  return database.prepare(`
    SELECT ${resourceAuthorizationSelectColumns()}
    FROM resource_authorizations
    WHERE resource_type = ?
      AND resource_id = ?
      AND grantee_system_account_id = ?
    LIMIT 1
  `).get(row.resource_type, row.resource_id, row.grantee_system_account_id) as unknown as ResourceAuthorizationRow | undefined
}

function resourceAuthorizationGrantSummaries(rows: ResourceAuthorizationGrantRow[], options: ResourceAuthorizationListOptions = {}): ResourceAuthorizationSummary[] {
  const accounts = loadAccountLookupMap(rows.filter((row) => row.resource_type === 'account').map((row) => row.resource_id))
  const authorizationInstanceAccountNames = loadAuthorizationInstanceAccountNameMap(rows.filter((row) => row.resource_type === 'account').map((row) => row.resource_id))
  const groupNames = loadGroupNameMap(rows.filter((row) => row.resource_type === 'group').map((row) => row.resource_id))
  const systemAccounts = loadSystemAccountPrincipalMapByIds(rows.flatMap((row) => [row.resource_owner_system_account_id, row.grantee_system_account_id ?? '']))
  const teamNames = loadSystemTeamNameMap(rows.map((row) => row.grantee_team_id ?? ''))
  const includeUsage = options.includeUsage ?? Boolean(options.usageRange)
  const usage = includeUsage
    ? loadResourceAuthorizationGrantUsageSummaries(rows, options.usageRange ?? todayDateKey(usageStatsTimezone()))
    : new Map<string, AccountUsageSummary>()
  const totalUsage = includeUsage
    ? loadResourceAuthorizationGrantUsageSummaries(rows)
    : new Map<string, AccountUsageSummary>()
  return rows.map((row) => {
    const owner = systemAccounts.get(row.resource_owner_system_account_id)
    const grantee = row.grantee_system_account_id ? systemAccounts.get(row.grantee_system_account_id) : undefined
    const teamName = row.grantee_team_id ? teamNames.get(row.grantee_team_id) : undefined
    const source = resourceAuthorizationGrantSourceSummary(row, teamName)
    const account = row.resource_type === 'account' ? accounts.get(row.resource_id) : undefined
    return {
      id: row.id,
      resourceType: row.resource_type,
      resourceId: row.resource_id,
      resourceName: row.resource_type === 'account' ? account?.name ?? authorizationInstanceAccountNames.get(row.resource_id) : groupNames.get(row.resource_id),
      resourceOwnerSystemAccountId: row.resource_owner_system_account_id,
      resourceOwnerSystemAccountName: owner?.displayName,
      granteeType: row.grantee_type,
      granteeSystemAccountId: row.grantee_system_account_id ?? undefined,
      granteeSystemAccountName: grantee?.displayName,
      granteeUsername: grantee?.username,
      granteeTeamId: row.grantee_team_id ?? undefined,
      granteeTeamName: teamName,
      scope: 'use',
      status: row.status,
      remark: row.remark ?? undefined,
      expiresAt: row.expires_at ?? undefined,
      limits: parseRequestQuotaLimitsJson(row.limits_json),
      resourceAccountExpiresAt: account?.accountExpiresAt,
      effectiveSourceType: row.grantee_type === 'team' ? 'team' : 'manual',
      effectiveSourceTeamId: row.grantee_team_id ?? undefined,
      effectiveSourceTeamName: teamName,
      activatedAt: row.created_at,
      lastSourceChangedAt: row.updated_at,
      authorizationSources: [source],
      usage: usage.get(row.id) ?? emptyAccountUsageSummary(),
      lastUsedAt: totalUsage.get(row.id)?.lastUsedAt,
      createdBy: row.created_by,
      createdAt: row.created_at,
      revokedBy: row.revoked_by ?? undefined,
      revokedAt: row.revoked_at ?? undefined,
      revokedReason: row.status === 'expired'
        ? 'authorization_expired'
        : row.status === 'returned'
          ? 'grantee_returned'
          : row.status === 'revoked'
            ? 'authorization_revoked'
            : undefined,
      updatedAt: row.updated_at
    }
  })
}

async function resourceAuthorizationGrantSummariesAsync(rows: ResourceAuthorizationGrantRow[], options: ResourceAuthorizationListOptions = {}): Promise<ResourceAuthorizationSummary[]> {
  const client = await getResourceAuthorizationReadDatabaseClient()
  const includeUsage = options.includeUsage ?? Boolean(options.usageRange)
  const usageRange = includeUsage
    ? options.usageRange ?? todayDateKey(await usageStatsTimezoneAsync())
    : undefined
  const [
    accounts,
    authorizationInstanceAccountNames,
    groupNames,
    systemAccounts,
    teamNames,
    usage,
    totalUsage
  ] = await Promise.all([
    loadAccountLookupMapAsync(client, rows.filter((row) => row.resource_type === 'account').map((row) => row.resource_id)),
    loadAuthorizationInstanceAccountNameMapAsync(client, rows.filter((row) => row.resource_type === 'account').map((row) => row.resource_id)),
    loadGroupNameMapAsync(client, rows.filter((row) => row.resource_type === 'group').map((row) => row.resource_id)),
    loadSystemAccountPrincipalMapByIdsAsync(client, rows.flatMap((row) => [row.resource_owner_system_account_id, row.grantee_system_account_id ?? ''])),
    loadSystemTeamNameMapAsync(client, rows.map((row) => row.grantee_team_id ?? '')),
    includeUsage
      ? loadResourceAuthorizationGrantUsageSummariesAsync(rows, usageRange)
      : Promise.resolve(new Map<string, AccountUsageSummary>()),
    includeUsage
      ? loadResourceAuthorizationGrantUsageSummariesAsync(rows)
      : Promise.resolve(new Map<string, AccountUsageSummary>())
  ])
  return rows.map((row) => {
    const owner = systemAccounts.get(row.resource_owner_system_account_id)
    const grantee = row.grantee_system_account_id ? systemAccounts.get(row.grantee_system_account_id) : undefined
    const teamName = row.grantee_team_id ? teamNames.get(row.grantee_team_id) : undefined
    const source = resourceAuthorizationGrantSourceSummary(row, teamName)
    const account = row.resource_type === 'account' ? accounts.get(row.resource_id) : undefined
    return {
      id: row.id,
      resourceType: row.resource_type,
      resourceId: row.resource_id,
      resourceName: row.resource_type === 'account' ? account?.name ?? authorizationInstanceAccountNames.get(row.resource_id) : groupNames.get(row.resource_id),
      resourceOwnerSystemAccountId: row.resource_owner_system_account_id,
      resourceOwnerSystemAccountName: owner?.displayName,
      granteeType: row.grantee_type,
      granteeSystemAccountId: row.grantee_system_account_id ?? undefined,
      granteeSystemAccountName: grantee?.displayName,
      granteeUsername: grantee?.username,
      granteeTeamId: row.grantee_team_id ?? undefined,
      granteeTeamName: teamName,
      scope: 'use',
      status: row.status,
      remark: row.remark ?? undefined,
      expiresAt: row.expires_at ?? undefined,
      limits: parseRequestQuotaLimitsJson(row.limits_json),
      resourceAccountExpiresAt: account?.accountExpiresAt,
      effectiveSourceType: row.grantee_type === 'team' ? 'team' : 'manual',
      effectiveSourceTeamId: row.grantee_team_id ?? undefined,
      effectiveSourceTeamName: teamName,
      activatedAt: row.created_at,
      lastSourceChangedAt: row.updated_at,
      authorizationSources: [source],
      usage: usage.get(row.id) ?? emptyAccountUsageSummary(),
      lastUsedAt: totalUsage.get(row.id)?.lastUsedAt,
      createdBy: row.created_by,
      createdAt: row.created_at,
      revokedBy: row.revoked_by ?? undefined,
      revokedAt: row.revoked_at ?? undefined,
      revokedReason: row.status === 'expired'
        ? 'authorization_expired'
        : row.status === 'returned'
          ? 'grantee_returned'
          : row.status === 'revoked'
            ? 'authorization_revoked'
            : undefined,
      updatedAt: row.updated_at
    }
  })
}

function loadAuthorizationInstanceAccountNameMap(resourceIds: string[]): Map<string, string> {
  const ids = [...new Set(resourceIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map()
  const output = new Map<string, string>()
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 900)) {
    const rows = database.prepare(`
      SELECT ra.resource_id, accounts.name
      FROM resource_authorizations ra
      INNER JOIN accounts ON accounts.authorization_instance_authorization_id = ra.id
      WHERE ra.resource_type = 'account'
        AND ra.resource_id IN (${sqlPlaceholders(chunk.length)})
      ORDER BY ra.created_at ASC, ra.id ASC
    `).all(...chunk) as unknown as Array<{ resource_id?: string | null; name?: string | null }>
    for (const row of rows) {
      const resourceId = String(row.resource_id ?? '')
      const name = String(row.name ?? '')
      if (resourceId && name && !output.has(resourceId)) {
        output.set(resourceId, name)
      }
    }
  }
  return output
}

async function loadAuthorizationInstanceAccountNameMapAsync(client: DatabaseClient, resourceIds: string[]): Promise<Map<string, string>> {
  const ids = [...new Set(resourceIds.map((id) => id.trim()).filter(Boolean))]
  if (!ids.length) return new Map()
  const output = new Map<string, string>()
  const tables = resourceAuthorizationReadTables(client)
  for (const chunk of chunkValues(ids, 900)) {
    const rows = await client.query<{ resource_id?: string | null; name?: string | null }>(`
      SELECT ra.resource_id, accounts.name
      FROM ${tables.resourceAuthorizations} ra
      INNER JOIN ${tables.accounts} accounts ON accounts.authorization_instance_authorization_id = ra.id
      WHERE ra.resource_type = 'account'
        AND ra.resource_id IN (${sqlPlaceholders(chunk.length)})
      ORDER BY ra.created_at ASC, ra.id ASC
    `, chunk)
    for (const row of rows) {
      const resourceId = String(row.resource_id ?? '')
      const name = String(row.name ?? '')
      if (resourceId && name && !output.has(resourceId)) {
        output.set(resourceId, name)
      }
    }
  }
  return output
}

function loadResourceAuthorizationUsageSummaries(rows: ResourceAuthorizationRow[], statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Map<string, AccountUsageSummary> {
  const accountScopes = rows
    .filter((row) => row.resource_type === 'account')
    .map((row) => usageScope(row.id, authorizationUsageSystemAccountId(row), row.id))
  const groupScopes = rows
    .filter((row) => row.resource_type === 'group')
    .map((row) => usageScope(row.id, authorizationUsageSystemAccountId(row), row.id))
  return new Map([
    ...loadAccountAuthorizationUsageSummaries(accountScopes, statDateOrRange),
    ...loadGroupAuthorizationUsageSummaries(groupScopes, statDateOrRange)
  ])
}

function authorizationUsageSystemAccountId(row: ResourceAuthorizationRow): string {
  return row.resource_type === 'account'
    ? row.grantee_system_account_id ?? row.resource_owner_system_account_id
    : row.resource_owner_system_account_id
}

function loadResourceAuthorizationGrantUsageSummaries(rows: ResourceAuthorizationGrantRow[], statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Map<string, AccountUsageSummary> {
  const result = new Map<string, AccountUsageSummary>()
  const userGrantRows = rows.filter((row) => row.grantee_type === 'system_account')
  const runtimeRowsByGrant = runtimeAuthorizationRowsByGrantIds(userGrantRows)
  const runtimeRows = [...runtimeRowsByGrant.values()].flat()
  const runtimeUsage = loadResourceAuthorizationUsageSummaries(runtimeRows, statDateOrRange)
  for (const row of userGrantRows) {
    const runtime = runtimeRowsByGrant.get(row.id)?.[0]
    result.set(row.id, runtime ? runtimeUsage.get(runtime.id) ?? emptyAccountUsageSummary() : emptyAccountUsageSummary())
  }

  const teamGrantRows = rows.filter((row) => row.grantee_type === 'team' && row.grantee_team_id)
  const accountTeamScopes = teamGrantRows
    .filter((row) => row.resource_type === 'account')
    .map((row) => usageScope(row.id, row.resource_owner_system_account_id, authorizationGrantUsageScopeId(row)))
  const groupTeamScopes = teamGrantRows
    .filter((row) => row.resource_type === 'group')
    .map((row) => usageScope(row.id, row.resource_owner_system_account_id, authorizationGrantUsageScopeId(row)))
  const teamUsage = new Map([
    ...loadAccountAuthorizationUsageSummaries(accountTeamScopes, statDateOrRange, 'account_authorization_team'),
    ...loadGroupAuthorizationUsageSummaries(groupTeamScopes, statDateOrRange, 'group_authorization_team')
  ])
  const reportTeamUsage = statDateOrRange
    ? loadResourceAuthorizationGrantTeamReportUsageSummaries(teamGrantRows, statDateOrRange)
    : new Map<string, AccountUsageSummary>()
  for (const row of teamGrantRows) {
    result.set(row.id, reportTeamUsage.get(row.id) ?? teamUsage.get(row.id) ?? emptyAccountUsageSummary())
  }

  return result
}

async function loadResourceAuthorizationGrantUsageSummariesAsync(rows: ResourceAuthorizationGrantRow[], statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>): Promise<Map<string, AccountUsageSummary>> {
  const result = new Map<string, AccountUsageSummary>()
  const userGrantRows = rows.filter((row) => row.grantee_type === 'system_account')
  const runtimeRowsByGrant = await runtimeAuthorizationRowsByGrantIdsAsync(userGrantRows)
  const runtimeRows = [...runtimeRowsByGrant.values()].flat()
  const runtimeUsage = await loadResourceAuthorizationUsageSummariesAsync(runtimeRows, statDateOrRange)
  for (const row of userGrantRows) {
    const runtime = runtimeRowsByGrant.get(row.id)?.[0]
    result.set(row.id, runtime ? runtimeUsage.get(runtime.id) ?? emptyAccountUsageSummary() : emptyAccountUsageSummary())
  }

  const teamGrantRows = rows.filter((row) => row.grantee_type === 'team' && row.grantee_team_id)
  const accountTeamScopes = teamGrantRows
    .filter((row) => row.resource_type === 'account')
    .map((row) => usageScope(row.id, row.resource_owner_system_account_id, authorizationGrantUsageScopeId(row)))
  const groupTeamScopes = teamGrantRows
    .filter((row) => row.resource_type === 'group')
    .map((row) => usageScope(row.id, row.resource_owner_system_account_id, authorizationGrantUsageScopeId(row)))
  const [accountTeamUsage, groupTeamUsage] = await Promise.all([
    loadAccountAuthorizationUsageSummariesAsync(accountTeamScopes, statDateOrRange, 'account_authorization_team'),
    loadGroupAuthorizationUsageSummariesAsync(groupTeamScopes, statDateOrRange, 'group_authorization_team')
  ])
  const teamUsage = new Map([
    ...accountTeamUsage,
    ...groupTeamUsage
  ])
  const reportTeamUsage = statDateOrRange
    ? await loadResourceAuthorizationGrantTeamReportUsageSummariesAsync(teamGrantRows, statDateOrRange)
    : new Map<string, AccountUsageSummary>()
  for (const row of teamGrantRows) {
    result.set(row.id, reportTeamUsage.get(row.id) ?? teamUsage.get(row.id) ?? emptyAccountUsageSummary())
  }

  return result
}

function loadResourceAuthorizationGrantTeamReportUsageSummaries(
  rows: ResourceAuthorizationGrantRow[],
  statDateOrRange: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): Map<string, AccountUsageSummary> {
  const result = new Map<string, AccountUsageSummary>()
  const scopedRows = rows.filter((row) => row.grantee_team_id)
  if (!scopedRows.length) return result
  const database = getStatsDatabase()
  const isRange = typeof statDateOrRange !== 'string'
  for (const chunk of chunkValues(scopedRows, TEAM_REPORT_USAGE_BATCH_SIZE)) {
    const values = chunk.map(() => isRange ? '(?, ?, ?, ?, ?, ?, ?)' : '(?, ?, ?, ?, ?, ?)').join(', ')
    const params = chunk.flatMap((row) => isRange
      ? [
        row.id,
        row.resource_owner_system_account_id,
        statDateOrRange.startDate,
        statDateOrRange.endDate,
        row.grantee_team_id,
        row.resource_type,
        row.resource_id
      ]
      : [
        row.id,
        row.resource_owner_system_account_id,
        statDateOrRange,
        row.grantee_team_id,
        row.resource_type,
        row.resource_id
      ])
    const usageRows = isRange
      ? database.prepare(`
        WITH requested(grant_id, system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id) AS (
          VALUES ${values}
        )
        SELECT requested.grant_id, report.request_count, report.input_tokens, report.output_tokens, report.cache_read_tokens,
          report.cache_read_cost_usd, report.total_cost_usd AS total_cost, report.last_used_at
        FROM requested
        INNER JOIN authorization_team_usage_range_windows report
          ON report.system_account_id = requested.system_account_id
          AND report.start_date = requested.start_date
          AND report.end_date = requested.end_date
          AND report.team_filter_id = requested.team_filter_id
          AND report.resource_filter_type = requested.resource_filter_type
          AND report.resource_filter_id = requested.resource_filter_id
      `).all(...params)
      : database.prepare(`
        WITH requested(grant_id, system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id) AS (
          VALUES ${values}
        )
        SELECT requested.grant_id, report.request_count, report.input_tokens, report.output_tokens, report.cache_read_tokens,
          report.cache_read_cost_usd, report.total_cost_usd AS total_cost, report.last_used_at
        FROM requested
        INNER JOIN authorization_team_usage_summary_daily report
          ON report.system_account_id = requested.system_account_id
          AND report.stat_date = requested.stat_date
          AND report.team_filter_id = requested.team_filter_id
          AND report.resource_filter_type = requested.resource_filter_type
          AND report.resource_filter_id = requested.resource_filter_id
      `).all(...params)
    for (const usageRow of usageRows as Array<{ grant_id?: string | null } & Parameters<typeof usageSummaryFromAggregate>[0]>) {
      if (usageRow.grant_id) {
        result.set(usageRow.grant_id, usageSummaryFromAggregate(usageRow))
      }
    }
  }
  return result
}

async function loadResourceAuthorizationGrantTeamReportUsageSummariesAsync(
  rows: ResourceAuthorizationGrantRow[],
  statDateOrRange: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): Promise<Map<string, AccountUsageSummary>> {
  const result = new Map<string, AccountUsageSummary>()
  const scopedRows = rows.filter((row) => row.grantee_team_id)
  if (!scopedRows.length) return result
  const client = await getResourceAuthorizationStatsDatabaseClient()
  const isRange = typeof statDateOrRange !== 'string'
  for (const chunk of chunkValues(scopedRows, TEAM_REPORT_USAGE_BATCH_SIZE)) {
    const values = chunk.map(() => isRange ? '(?, ?, ?, ?, ?, ?, ?)' : '(?, ?, ?, ?, ?, ?)').join(', ')
    const params = chunk.flatMap((row) => isRange
      ? [
        row.id,
        row.resource_owner_system_account_id,
        statDateOrRange.startDate,
        statDateOrRange.endDate,
        row.grantee_team_id,
        row.resource_type,
        row.resource_id
      ]
      : [
        row.id,
        row.resource_owner_system_account_id,
        statDateOrRange,
        row.grantee_team_id,
        row.resource_type,
        row.resource_id
      ])
    const usageRows = isRange
      ? await client.query<Array<{ grant_id?: string | null } & Parameters<typeof usageSummaryFromAggregate>[0]>[number]>(`
        WITH requested(grant_id, system_account_id, start_date, end_date, team_filter_id, resource_filter_type, resource_filter_id) AS (
          VALUES ${values}
        )
        SELECT requested.grant_id, report.request_count, report.input_tokens, report.output_tokens, report.cache_read_tokens,
          CAST(report.cache_read_cost_usd AS double precision) AS cache_read_cost_usd, CAST(report.total_cost_usd AS double precision) AS total_cost, report.last_used_at
        FROM requested
        INNER JOIN ${resourceAuthorizationStatsTable(client, 'authorization_team_usage_range_windows')} report
          ON report.system_account_id = requested.system_account_id
          AND report.start_date = requested.start_date
          AND report.end_date = requested.end_date
          AND report.team_filter_id = requested.team_filter_id
          AND report.resource_filter_type = requested.resource_filter_type
          AND report.resource_filter_id = requested.resource_filter_id
      `, params)
      : await client.query<Array<{ grant_id?: string | null } & Parameters<typeof usageSummaryFromAggregate>[0]>[number]>(`
        WITH requested(grant_id, system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id) AS (
          VALUES ${values}
        )
        SELECT requested.grant_id, report.request_count, report.input_tokens, report.output_tokens, report.cache_read_tokens,
          CAST(report.cache_read_cost_usd AS double precision) AS cache_read_cost_usd, CAST(report.total_cost_usd AS double precision) AS total_cost, report.last_used_at
        FROM requested
        INNER JOIN ${resourceAuthorizationStatsTable(client, 'authorization_team_usage_summary_daily')} report
          ON report.system_account_id = requested.system_account_id
          AND report.stat_date = requested.stat_date
          AND report.team_filter_id = requested.team_filter_id
          AND report.resource_filter_type = requested.resource_filter_type
          AND report.resource_filter_id = requested.resource_filter_id
      `, params)
    for (const usageRow of usageRows) {
      if (usageRow.grant_id) {
        result.set(usageRow.grant_id, usageSummaryFromAggregate(usageRow))
      }
    }
  }
  return result
}

function runtimeAuthorizationRowsByGrantIds(rows: ResourceAuthorizationGrantRow[]): Map<string, ResourceAuthorizationRow[]> {
  const result = new Map<string, ResourceAuthorizationRow[]>()
  const database = getBusinessDatabase()
  for (const row of rows) {
    result.set(row.id, [])
  }

  const userRows = rows.filter((row) => row.grantee_type === 'system_account' && row.grantee_system_account_id)
  for (let index = 0; index < userRows.length; index += RUNTIME_AUTHORIZATION_BATCH_SIZE) {
    const chunk = userRows.slice(index, index + RUNTIME_AUTHORIZATION_BATCH_SIZE)
    const values = chunk.map(() => '(?, ?, ?, ?)').join(', ')
    const params = chunk.flatMap((row) => [row.id, row.resource_type, row.resource_id, row.grantee_system_account_id ?? ''])
    const runtimeRows = database.prepare(`
      WITH requested(grant_id, resource_type, resource_id, grantee_system_account_id) AS (
        VALUES ${values}
      )
      SELECT requested.grant_id, ra.*
      FROM requested
      INNER JOIN resource_authorizations ra
        ON ra.resource_type = requested.resource_type
        AND ra.resource_id = requested.resource_id
        AND ra.grantee_system_account_id = requested.grantee_system_account_id
      ORDER BY requested.grant_id ASC, ra.created_at ASC, ra.id ASC
    `).all(...params) as unknown as Array<ResourceAuthorizationRow & { grant_id: string }>
    for (const runtime of runtimeRows) {
      result.set(runtime.grant_id, [...(result.get(runtime.grant_id) ?? []), runtime])
    }
  }

  const teamRows = rows.filter((row) => row.grantee_type === 'team' && row.grantee_team_id)
  for (let index = 0; index < teamRows.length; index += RUNTIME_AUTHORIZATION_BATCH_SIZE) {
    const chunk = teamRows.slice(index, index + RUNTIME_AUTHORIZATION_BATCH_SIZE)
    const values = chunk.map(() => '(?, ?, ?, ?, ?)').join(', ')
    const params = chunk.flatMap((row) => [row.id, row.resource_type, row.resource_id, row.resource_owner_system_account_id, row.grantee_team_id ?? ''])
    const runtimeRows = database.prepare(`
      WITH requested(grant_id, resource_type, resource_id, resource_owner_system_account_id, grantee_team_id) AS (
        VALUES ${values}
      )
      SELECT DISTINCT requested.grant_id, ra.*
      FROM requested
      INNER JOIN resource_authorization_sources ras
        ON ras.source_type = 'team'
        AND ras.source_team_id = requested.grantee_team_id
      INNER JOIN resource_authorizations ra
        ON ra.id = ras.authorization_id
        AND ra.resource_type = requested.resource_type
        AND ra.resource_id = requested.resource_id
        AND ra.resource_owner_system_account_id = requested.resource_owner_system_account_id
      ORDER BY requested.grant_id ASC, ra.created_at ASC, ra.id ASC
    `).all(...params) as unknown as Array<ResourceAuthorizationRow & { grant_id: string }>
    for (const runtime of runtimeRows) {
      result.set(runtime.grant_id, [...(result.get(runtime.grant_id) ?? []), runtime])
    }
  }
  return result
}

async function runtimeAuthorizationRowsByGrantIdsAsync(rows: ResourceAuthorizationGrantRow[]): Promise<Map<string, ResourceAuthorizationRow[]>> {
  const result = new Map<string, ResourceAuthorizationRow[]>()
  const client = await getResourceAuthorizationReadDatabaseClient()
  const tables = resourceAuthorizationReadTables(client)
  for (const row of rows) {
    result.set(row.id, [])
  }

  const userRows = rows.filter((row) => row.grantee_type === 'system_account' && row.grantee_system_account_id)
  for (let index = 0; index < userRows.length; index += RUNTIME_AUTHORIZATION_BATCH_SIZE) {
    const chunk = userRows.slice(index, index + RUNTIME_AUTHORIZATION_BATCH_SIZE)
    const values = chunk.map(() => '(?, ?, ?, ?)').join(', ')
    const params = chunk.flatMap((row) => [row.id, row.resource_type, row.resource_id, row.grantee_system_account_id ?? ''])
    const runtimeRows = await client.query<ResourceAuthorizationRow & { grant_id: string }>(`
      WITH requested(grant_id, resource_type, resource_id, grantee_system_account_id) AS (
        VALUES ${values}
      )
      SELECT requested.grant_id, ra.*
      FROM requested
      INNER JOIN ${tables.resourceAuthorizations} ra
        ON ra.resource_type = requested.resource_type
        AND ra.resource_id = requested.resource_id
        AND ra.grantee_system_account_id = requested.grantee_system_account_id
      ORDER BY requested.grant_id ASC, ra.created_at ASC, ra.id ASC
    `, params)
    for (const runtime of runtimeRows) {
      result.set(runtime.grant_id, [...(result.get(runtime.grant_id) ?? []), runtime])
    }
  }

  const teamRows = rows.filter((row) => row.grantee_type === 'team' && row.grantee_team_id)
  for (let index = 0; index < teamRows.length; index += RUNTIME_AUTHORIZATION_BATCH_SIZE) {
    const chunk = teamRows.slice(index, index + RUNTIME_AUTHORIZATION_BATCH_SIZE)
    const values = chunk.map(() => '(?, ?, ?, ?, ?)').join(', ')
    const params = chunk.flatMap((row) => [row.id, row.resource_type, row.resource_id, row.resource_owner_system_account_id, row.grantee_team_id ?? ''])
    const runtimeRows = await client.query<ResourceAuthorizationRow & { grant_id: string }>(`
      WITH requested(grant_id, resource_type, resource_id, resource_owner_system_account_id, grantee_team_id) AS (
        VALUES ${values}
      )
      SELECT DISTINCT requested.grant_id, ra.*
      FROM requested
      INNER JOIN ${tables.resourceAuthorizationSources} ras
        ON ras.source_type = 'team'
        AND ras.source_team_id = requested.grantee_team_id
      INNER JOIN ${tables.resourceAuthorizations} ra
        ON ra.id = ras.authorization_id
        AND ra.resource_type = requested.resource_type
        AND ra.resource_id = requested.resource_id
        AND ra.resource_owner_system_account_id = requested.resource_owner_system_account_id
      ORDER BY requested.grant_id ASC, ra.created_at ASC, ra.id ASC
    `, params)
    for (const runtime of runtimeRows) {
      result.set(runtime.grant_id, [...(result.get(runtime.grant_id) ?? []), runtime])
    }
  }
  return result
}

function authorizationGrantUsageScopeId(row: ResourceAuthorizationGrantRow): string {
  return row.grantee_type === 'team' && row.grantee_team_id
    ? `${row.resource_id}:${row.grantee_team_id}`
    : row.id
}

async function loadResourceAuthorizationUsageSummariesAsync(
  rows: ResourceAuthorizationRow[],
  statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): Promise<Map<string, AccountUsageSummary>> {
  const accountScopes = rows
    .filter((row) => row.resource_type === 'account')
    .map((row) => usageScope(row.id, authorizationUsageSystemAccountId(row), row.id))
  const groupScopes = rows
    .filter((row) => row.resource_type === 'group')
    .map((row) => usageScope(row.id, authorizationUsageSystemAccountId(row), row.id))
  const [accountUsage, groupUsage] = await Promise.all([
    loadAccountAuthorizationUsageSummariesAsync(accountScopes, statDateOrRange),
    loadGroupAuthorizationUsageSummariesAsync(groupScopes, statDateOrRange)
  ])
  return new Map([
    ...accountUsage,
    ...groupUsage
  ])
}

async function loadAccountAuthorizationUsageSummariesAsync(
  scopes: UsageSummaryScopeRequest[],
  statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>,
  scopeType: 'account_authorization' | 'account_authorization_team' = 'account_authorization'
): Promise<Map<string, AccountUsageSummary>> {
  if (statDateOrRange && typeof statDateOrRange !== 'string') {
    return loadAuthorizationUsageRangeSummariesForScopesAsync(scopes, scopeType, statDateOrRange)
  }
  return loadAuthorizationUsageSummariesForScopesAsync(scopes, scopeType, statDateOrRange)
}

async function loadGroupAuthorizationUsageSummariesAsync(
  scopes: UsageSummaryScopeRequest[],
  statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>,
  scopeType: 'group_authorization' | 'group_authorization_team' = 'group_authorization'
): Promise<Map<string, AccountUsageSummary>> {
  if (statDateOrRange && typeof statDateOrRange !== 'string') {
    return loadAuthorizationUsageRangeSummariesForScopesAsync(scopes, scopeType, statDateOrRange)
  }
  return loadAuthorizationUsageSummariesForScopesAsync(scopes, scopeType, statDateOrRange)
}

async function getResourceAuthorizationReadDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

async function getResourceAuthorizationStatsDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getStatsDatabase())
}

function resourceAuthorizationReadTables(client: DatabaseClient): ResourceAuthorizationReadTables {
  return {
    resourceAuthorizationGrants: resourceAuthorizationReadTable(client, 'resource_authorization_grants'),
    resourceAuthorizations: resourceAuthorizationReadTable(client, 'resource_authorizations'),
    resourceAuthorizationSources: resourceAuthorizationReadTable(client, 'resource_authorization_sources'),
    systemTeamMembers: resourceAuthorizationReadTable(client, 'system_team_members'),
    systemAccounts: resourceAuthorizationReadTable(client, 'system_accounts'),
    systemTeams: resourceAuthorizationReadTable(client, 'system_teams'),
    accounts: resourceAuthorizationReadTable(client, 'accounts'),
    groups: resourceAuthorizationReadTable(client, 'groups')
  }
}

function resourceAuthorizationReadTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function resourceAuthorizationStatsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(statsSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
