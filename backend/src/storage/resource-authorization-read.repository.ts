import type { DatabaseSync } from 'node:sqlite'

import type { AccountUsageStatsRange, AccountUsageSummary, ResourceAuthorizationListResult, ResourceAuthorizationSummary } from '../domain/types.js'
import { loadAccountAuthorizationUsageSummaries } from './account-read.repository.js'
import { canAccessAll, currentSystemAccountId, scopedSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { getDatabase, getStatsDatabase } from './database.js'
import { loadGroupAuthorizationUsageSummaries } from './group-read.repository.js'
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
import { loadAccountLookupMap, loadGroupNameMap, loadSystemAccountPrincipalMapByIds, loadSystemTeamNameMap } from './repository-lookups.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import type { ResourceAuthorizationGrantRow, ResourceAuthorizationRow } from './repository-row-types.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { emptyAccountUsageSummary, todayDateKey, usageStatsTimezone, usageSummaryFromAggregate } from './usage-stats-helpers.js'
import { optionalString, parseOptionalJsonObject } from './value-utils.js'

const RUNTIME_AUTHORIZATION_BATCH_SIZE = 200
const defaultResourceAuthorizationPageSize = 50
const maxResourceAuthorizationPageSize = 500

export interface ResourceAuthorizationListOptions {
  usageRange?: AccountUsageStatsRange
  includeUsage?: boolean
  page?: number
  pageSize?: number
  limit?: number
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

function normalizeResourceAuthorizationPageOptions(options: ResourceAuthorizationListOptions): NormalizedResourceAuthorizationPageOptions {
  const rawPage = options.page
  const rawPageSize = options.pageSize ?? options.limit
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxResourceAuthorizationPageSize, Math.max(1, rawPageSize))
    : defaultResourceAuthorizationPageSize
  return { page, pageSize }
}

function listResourceAuthorizationGrantOperationRows(filters: Record<string, unknown>, access?: AccessScope, pagination?: { limit: number; offset: number }): ResourceAuthorizationGrantRow[] {
  const clauses: string[] = []
  const params: Array<string | number | null> = []
  const grantId = optionalString(filters.id ?? filters.authorizationId ?? filters.authorization_id)
  if (grantId) { clauses.push('rag.id = ?'); params.push(grantId) }
  const resourceType = normalizeResourceType(filters.resourceType ?? filters.resource_type)
  if (resourceType) { clauses.push('rag.resource_type = ?'); params.push(resourceType) }
  const resourceId = optionalString(filters.resourceId ?? filters.resource_id)
  if (resourceId) { clauses.push('rag.resource_id = ?'); params.push(resourceId) }
  const granteeSystemAccountId = optionalString(filters.granteeSystemAccountId ?? filters.grantee_system_account_id)
  if (granteeSystemAccountId) {
    clauses.push('rag.grantee_type = ?')
    params.push('system_account')
    clauses.push('rag.grantee_system_account_id = ?')
    params.push(granteeSystemAccountId)
  }
  const status = authorizationStatusFilter(filters.status)
  if (status) { clauses.push('rag.status = ?'); params.push(status) }
  const sourceType = optionalString(filters.sourceType ?? filters.source_type)
  if (sourceType === 'manual') {
    clauses.push('rag.grantee_type = ?')
    params.push('system_account')
  } else if (sourceType === 'team') {
    clauses.push('rag.grantee_type = ?')
    params.push('team')
  }
  const teamId = optionalString(filters.teamId ?? filters.team_id)
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
  const ownerSystemAccountId = optionalString(filters.resourceOwnerSystemAccountId ?? filters.resource_owner_system_account_id)
  if (ownerSystemAccountId) {
    clauses.push('rag.resource_owner_system_account_id = ?')
    params.push(ownerSystemAccountId)
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
  return getDatabase().prepare(`SELECT ${resourceAuthorizationGrantSelectColumns('rag')} FROM resource_authorization_grants rag ${where} ORDER BY rag.created_at DESC, rag.id DESC${pageClause}`).all(...params, ...pageParams) as unknown as ResourceAuthorizationGrantRow[]
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
    'model_policy_json',
    'created_by',
    'created_at',
    'revoked_by',
    'revoked_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

export function loadRuntimeAuthorizationForUserGrant(row: ResourceAuthorizationGrantRow, database = getDatabase()): ResourceAuthorizationRow | undefined {
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
  const includeUsage = options.includeUsage ?? true
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
      modelPolicy: parseOptionalJsonObject(row.model_policy_json ?? undefined),
      effectiveSourceType: row.grantee_type === 'team' ? 'team' : 'manual',
      effectiveSourceTeamId: row.grantee_team_id ?? undefined,
      effectiveSourceTeamName: teamName,
      activatedAt: row.created_at,
      lastSourceChangedAt: row.updated_at,
      sources: [source],
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
  const database = getDatabase()
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

function loadResourceAuthorizationGrantTeamReportUsageSummaries(
  rows: ResourceAuthorizationGrantRow[],
  statDateOrRange: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>
): Map<string, AccountUsageSummary> {
  const result = new Map<string, AccountUsageSummary>()
  if (!rows.length) return result
  const database = getStatsDatabase()
  const isRange = typeof statDateOrRange !== 'string'
  const statement = isRange
    ? database.prepare(`
      SELECT request_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd AS cache_read_cost, total_cost_usd AS total_cost, last_used_at
      FROM authorization_team_usage_range_windows
      WHERE system_account_id = ?
        AND start_date = ?
        AND end_date = ?
        AND team_filter_id = ?
        AND resource_filter_type = ?
        AND resource_filter_id = ?
      LIMIT 1
    `)
    : database.prepare(`
      SELECT request_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd AS cache_read_cost, total_cost_usd AS total_cost, last_used_at
      FROM authorization_team_usage_summary_daily
      WHERE system_account_id = ?
        AND stat_date = ?
        AND team_filter_id = ?
        AND resource_filter_type = ?
        AND resource_filter_id = ?
      LIMIT 1
    `)
  for (const row of rows) {
    if (!row.grantee_team_id) continue
    const usageRow = isRange
      ? statement.get(
        row.resource_owner_system_account_id,
        statDateOrRange.startDate,
        statDateOrRange.endDate,
        row.grantee_team_id,
        row.resource_type,
        row.resource_id
      )
      : statement.get(
        row.resource_owner_system_account_id,
        statDateOrRange,
        row.grantee_team_id,
        row.resource_type,
        row.resource_id
      )
    if (usageRow) {
      result.set(row.id, usageSummaryFromAggregate(usageRow as Parameters<typeof usageSummaryFromAggregate>[0]))
    }
  }
  return result
}

function runtimeAuthorizationRowsByGrantIds(rows: ResourceAuthorizationGrantRow[]): Map<string, ResourceAuthorizationRow[]> {
  const result = new Map<string, ResourceAuthorizationRow[]>()
  const database = getDatabase()
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

function authorizationGrantUsageScopeId(row: ResourceAuthorizationGrantRow): string {
  return row.grantee_type === 'team' && row.grantee_team_id
    ? `${row.resource_id}:${row.grantee_team_id}`
    : row.id
}
