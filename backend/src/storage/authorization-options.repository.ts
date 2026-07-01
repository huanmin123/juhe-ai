import type { GroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import type { AccessScope } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { buildGroupOptionSummaries, buildGroupOptionSummariesAsync } from './group-summary.repository.js'
import { getPostgresPool } from './postgres-client.js'
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

const businessSchemaName = 'juhe_business'

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

export async function listAuthorizationGranteeAccountsAsync(access?: AccessScope, options: AuthorizationPrincipalOptionListOptions = {}): Promise<SystemAccountPrincipalSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAuthorizationGranteeAccounts(access, options)
  }
  void access
  const client = await getAuthorizationOptionsDatabaseClient()
  const principalFilter = buildSystemAccountPrincipalFilterForClient(client, options)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = await client.query<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>(`
    SELECT id, username, display_name, status
    FROM ${authorizationOptionsTable(client, 'system_accounts')}
    ${principalFilter.clause}
    ORDER BY status ASC, display_name ASC, username ASC, id ASC
    ${limitClause.clause}
  `, [...principalFilter.params, ...limitClause.params])
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

export async function listAuthorizationGranteeTeamsAsync(access?: AccessScope, options: AuthorizationPrincipalOptionListOptions = {}): Promise<SystemTeamPrincipalSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAuthorizationGranteeTeams(access, options)
  }
  void access
  const client = await getAuthorizationOptionsDatabaseClient()
  const principalFilter = buildSystemTeamPrincipalFilterForClient(client, options)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = await client.query<SystemTeamRow>(`
    SELECT id, name, status
    FROM ${authorizationOptionsTable(client, 'system_teams')}
    ${principalFilter.clause}
    ORDER BY status ASC, name ASC, id ASC
    ${limitClause.clause}
  `, [...principalFilter.params, ...limitClause.params])
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

export async function listAuthorizationGranteeGroupsAsync(access?: AccessScope, options: AuthorizationGranteeGroupOptionListOptions = {}): Promise<GroupOptionSummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAuthorizationGranteeGroups(access, options)
  }
  void access
  const granteeSystemAccountId = optionalString(options.granteeSystemAccountId)
  if (!granteeSystemAccountId) return []
  const client = await getAuthorizationOptionsDatabaseClient()
  const grantee = await client.one<Pick<SystemAccountRow, 'id' | 'username' | 'display_name' | 'status'>>(`
    SELECT id, username, display_name, status
    FROM ${authorizationOptionsTable(client, 'system_accounts')}
    WHERE id = ?
    LIMIT 1
  `, [granteeSystemAccountId])
  if (!grantee || grantee.status !== 'active') return []
  const filter = buildAuthorizationGranteeGroupFilterForClient(client, options, granteeSystemAccountId)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = await client.query<GroupListRow>(`
    SELECT ${authorizationGranteeGroupSelectColumns()}, 'owner' AS access_type, NULL AS authorization_id, NULL AS authorization_status
    FROM ${authorizationOptionsTable(client, 'groups')} groups
    ${filter.clause}
    ${options.preferDefault === false ? 'ORDER BY groups.updated_at DESC, groups.id DESC' : 'ORDER BY groups.is_default DESC, groups.updated_at DESC, groups.id DESC'}
    ${limitClause.clause}
  `, [...filter.params, ...limitClause.params])
  return (await buildGroupOptionSummariesAsync(rows, access)).map((group) => ({
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

function buildSystemAccountPrincipalFilterForClient(client: DatabaseClient, options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, (keyword) => buildSystemAccountPrincipalKeywordFilterForClient(client, keyword))
}

function buildSystemTeamPrincipalFilterForClient(client: DatabaseClient, options: AuthorizationPrincipalOptionListOptions): { clause: string; params: string[] } {
  return buildPrincipalFilter(options, (keyword) => buildSystemTeamPrincipalKeywordFilterForClient(client, keyword))
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

function buildAuthorizationGranteeGroupFilterForClient(client: DatabaseClient, options: AuthorizationGranteeGroupOptionListOptions, granteeSystemAccountId: string): { clause: string; params: string[] } {
  const clauses = ['groups.system_account_id = ?', 'groups.enabled = 1']
  const params = [granteeSystemAccountId]
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`groups.id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const providerCode = optionalString(options.providerCode)
  if (providerCode) {
    clauses.push(client.driver === 'postgres' ? 'lower(groups.provider_code) = lower(?)' : 'groups.provider_code COLLATE NOCASE = ?')
    params.push(providerCode)
  }
  const keyword = optionalString(options.keyword)
  if (keyword) {
    const prefix = `${escapeLikePrefix(keyword)}%`
    clauses.push(client.driver === 'postgres'
      ? `(
        lower(groups.name) = lower(?)
        OR groups.name ILIKE ? ESCAPE '\\'
      )`
      : `(
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

function buildSystemAccountPrincipalKeywordFilterForClient(client: DatabaseClient, keyword?: string): { clause: string; params: string[] } {
  if (client.driver !== 'postgres') return buildSystemAccountPrincipalKeywordFilter(keyword)
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      lower(username) = lower(?)
      OR username ILIKE ? ESCAPE '\\'
      OR lower(display_name) = lower(?)
      OR display_name ILIKE ? ESCAPE '\\'
    )`,
    params: [text, prefix, text, prefix]
  }
}

function buildSystemTeamPrincipalKeywordFilterForClient(client: DatabaseClient, keyword?: string): { clause: string; params: string[] } {
  if (client.driver !== 'postgres') return buildSystemTeamPrincipalKeywordFilter(keyword)
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  const prefix = `${escapeLikePrefix(text)}%`
  return {
    clause: `WHERE (
      lower(name) = lower(?)
      OR name ILIKE ? ESCAPE '\\'
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

async function getAuthorizationOptionsDatabaseClient(): Promise<DatabaseClient> {
  return createPostgresDatabaseClient(await getPostgresPool())
}

function authorizationOptionsTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
