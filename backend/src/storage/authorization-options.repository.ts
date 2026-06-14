import type { GroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '../domain/types.js'
import type { AccessScope } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import { buildGroupOptionSummaries } from './group-summary.repository.js'
import { escapeLikePrefix, sqlPlaceholders } from './query-utils.js'
import type { GroupListRow, SystemTeamRow } from './repository-row-types.js'
import { authorizedPermissions } from './resource-permissions.js'
import { systemAccountPrincipalSummaryFromRow, type SystemAccountRow } from './system-account-mappers.js'
import { findSystemAccountById } from './system-accounts.repository.js'
import { optionalString } from './value-utils.js'

interface AuthorizationPrincipalOptionListOptions {
  ids?: string[]
  keyword?: string
  limit?: number
}

interface AuthorizationGranteeGroupOptionListOptions extends AuthorizationPrincipalOptionListOptions {
  granteeSystemAccountId?: string
  providerCode?: string
  preferDefault?: boolean
}

export function listAuthorizationGranteeAccounts(access?: AccessScope, options: AuthorizationPrincipalOptionListOptions = {}): SystemAccountPrincipalSummary[] {
  void access
  const database = getBusinessDatabase()
  const principalFilter = buildSystemAccountPrincipalFilter(options)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = database.prepare(`
    SELECT id, username, display_name, status
    FROM system_accounts
    ${principalFilter.clause}
    ORDER BY status ASC, display_name ASC, username ASC, id ASC
    ${limitClause.clause}
  `).all(...principalFilter.params, ...limitClause.params) as unknown as Array<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>
  return rows.map(systemAccountPrincipalSummaryFromRow)
}

export function listAuthorizationGranteeTeams(access?: AccessScope, options: AuthorizationPrincipalOptionListOptions = {}): SystemTeamPrincipalSummary[] {
  void access
  const database = getBusinessDatabase()
  const principalFilter = buildSystemTeamPrincipalFilter(options)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = database.prepare(`
    SELECT id, name, status
    FROM system_teams
    ${principalFilter.clause}
    ORDER BY status ASC, name ASC, id ASC
    ${limitClause.clause}
  `).all(...principalFilter.params, ...limitClause.params) as unknown as SystemTeamRow[]
  return rows.map(systemTeamPrincipalSummaryFromRow)
}

export function listAuthorizationGranteeGroups(access?: AccessScope, options: AuthorizationGranteeGroupOptionListOptions = {}): GroupOptionSummary[] {
  void access
  const granteeSystemAccountId = optionalString(options.granteeSystemAccountId)
  if (!granteeSystemAccountId) return []
  const grantee = findSystemAccountById(granteeSystemAccountId)
  if (!grantee || grantee.status !== 'active') return []
  const filter = buildAuthorizationGranteeGroupFilter(options, granteeSystemAccountId)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${authorizationGranteeGroupSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
      FROM groups
      ${filter.clause}
      ${options.preferDefault === false ? 'ORDER BY groups.updated_at DESC, groups.id DESC' : 'ORDER BY groups.is_default DESC, groups.updated_at DESC, groups.id DESC'}
      ${limitClause.clause}
    `)
    .all(...filter.params, ...limitClause.params) as unknown as GroupListRow[]
  return buildGroupOptionSummaries(rows, access).map((group) => ({
    ...group,
    permissions: authorizedPermissions()
  }))
}

function authorizationGranteeGroupSelectColumns(): string {
  return [
    'id',
    'system_account_id',
    'name',
    'provider_code',
    'provider_protocol_profile_id',
    'protocol_code',
    'protocol_version',
    'description',
    'enabled',
    'is_default',
    'group_type',
    'scheduling_policy_json',
    'created_at',
    'updated_at'
  ].map((column) => `groups.${column}`).join(', ')
}

function buildSystemAccountPrincipalFilter(options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, buildSystemAccountPrincipalKeywordFilter)
}

function buildSystemTeamPrincipalFilter(options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, buildSystemTeamPrincipalKeywordFilter)
}

function buildAuthorizationGranteeGroupFilter(options: AuthorizationGranteeGroupOptionListOptions, granteeSystemAccountId: string): { clause: string; params: string[] } {
  const clauses = ['groups.system_account_id = ?', 'groups.enabled = 1']
  const params = [granteeSystemAccountId]
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`groups.id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const providerCode = optionalString(options.providerCode)
  if (providerCode) {
    clauses.push('groups.provider_code COLLATE NOCASE = ?')
    params.push(providerCode)
  }
  const keyword = optionalString(options.keyword)
  if (keyword) {
    const prefix = `${escapeLikePrefix(keyword)}%`
    clauses.push(`(
      groups.name COLLATE NOCASE = ?
      OR groups.name LIKE ? ESCAPE '\\'
    )`)
    params.push(keyword, prefix)
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

function buildPrincipalFilter(
  options: AuthorizationPrincipalOptionListOptions,
  keywordFilterBuilder: (keyword?: string) => { clause: string; params: string[] }
): { clause: string; params: string[] } {
  const clauses: string[] = []
  const params: string[] = []
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const keywordFilter = keywordFilterBuilder(options.keyword)
  if (keywordFilter.clause) {
    clauses.push(keywordFilter.clause.replace(/^WHERE\s+/i, ''))
    params.push(...keywordFilter.params)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function normalizeTextList(values?: string[]): string[] {
  if (!values?.length) return []
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
    .sort()
    .slice(0, 50)
}

function buildSystemAccountPrincipalKeywordFilter(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      username COLLATE NOCASE = ?
      OR username LIKE ? ESCAPE '\\'
      OR display_name COLLATE NOCASE = ?
      OR display_name LIKE ? ESCAPE '\\'
    )`,
    params: [text, prefix, text, prefix]
  }
}

function buildSystemTeamPrincipalKeywordFilter(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      name COLLATE NOCASE = ?
      OR name LIKE ? ESCAPE '\\'
    )`,
    params: [text, prefix]
  }
}

function authorizationPrincipalOptionLimitClause(limit?: number): { clause: string; params: number[] } {
  const safeLimit = typeof limit === 'number' && Number.isInteger(limit)
    ? Math.min(50, Math.max(1, limit))
    : 50
  return { clause: 'LIMIT ?', params: [safeLimit] }
}

function systemTeamPrincipalSummaryFromRow(row: SystemTeamRow): SystemTeamPrincipalSummary {
  return {
    id: row.id,
    name: row.name,
    status: row.status
  }
}
