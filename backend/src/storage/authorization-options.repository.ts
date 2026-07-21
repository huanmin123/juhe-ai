import type { AuthorizationGranteeGroupOptionSummary, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import type { AccessScope } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { sqlPlaceholders } from './query-utils.js'
import type { SystemTeamRow } from './repository-row-types.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { systemAccountPrincipalSummaryFromRow, type SystemAccountRow } from './system-account-mappers.js'
import { optionalString } from './value-utils.js'

export interface AuthorizationPrincipalOptionListOptions {
  ids?: string[]
  keyword?: string
  limit?: number
}

export interface AuthorizationGranteeGroupOptionListOptions extends AuthorizationPrincipalOptionListOptions {
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
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_authorization_grantee_accounts_read_only',
      access,
      options
    })
  }
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
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_authorization_grantee_teams_read_only',
      access,
      options
    })
  }
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

export function listAuthorizationGranteeGroups(access?: AccessScope, options: AuthorizationGranteeGroupOptionListOptions = {}): AuthorizationGranteeGroupOptionSummary[] {
  void access
  const granteeSystemAccountId = optionalString(options.granteeSystemAccountId)
  if (!granteeSystemAccountId) return []
  const filter = buildAuthorizationGranteeGroupFilter(options, granteeSystemAccountId)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT groups.id, groups.name
      FROM groups
      ${filter.clause}
      ${options.preferDefault === false ? 'ORDER BY groups.updated_at DESC, groups.id DESC' : 'ORDER BY groups.is_default DESC, groups.updated_at DESC, groups.id DESC'}
      ${limitClause.clause}
    `)
    .all(...filter.params, ...limitClause.params) as unknown as AuthorizationGranteeGroupOptionSummary[]
  return rows
}

export async function listAuthorizationGranteeGroupsAsync(access?: AccessScope, options: AuthorizationGranteeGroupOptionListOptions = {}): Promise<AuthorizationGranteeGroupOptionSummary[]> {
  if (sqliteReadWorkerPoolEnabled()) {
    return requestSqliteReadWorker({
      type: 'list_authorization_grantee_groups_read_only',
      access,
      options
    })
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    return listAuthorizationGranteeGroups(access, options)
  }
  void access
  const granteeSystemAccountId = optionalString(options.granteeSystemAccountId)
  if (!granteeSystemAccountId) return []
  const client = await getAuthorizationOptionsDatabaseClient()
  const filter = buildAuthorizationGranteeGroupFilterForClient(client, options, granteeSystemAccountId)
  const limitClause = authorizationPrincipalOptionLimitClause(options.limit)
  return client.query<AuthorizationGranteeGroupOptionSummary>(`
    SELECT groups.id, groups.name
    FROM ${authorizationOptionsTable(client, 'groups')} groups
    ${filter.clause}
    ${options.preferDefault === false ? 'ORDER BY groups.updated_at DESC, groups.id DESC' : 'ORDER BY groups.is_default DESC, groups.updated_at DESC, groups.id DESC'}
    ${limitClause.clause}
  `, [...filter.params, ...limitClause.params])
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
  const clauses = [
    'groups.system_account_id = ?',
    'groups.enabled = 1',
    "EXISTS (SELECT 1 FROM system_accounts grantee WHERE grantee.id = ? AND grantee.status = 'active')"
  ]
  const params = [granteeSystemAccountId, granteeSystemAccountId]
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`groups.id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const providerCode = optionalString(options.providerCode)
  if (providerCode) {
    clauses.push('groups.provider_code = ?')
    params.push(providerCode)
  }
  const keyword = optionalString(options.keyword)
  if (keyword) {
    clauses.push('(groups.name >= ? AND groups.name < ?)')
    params.push(keyword, textPrefixUpperBound(keyword))
  }
  return {
    clause: `WHERE ${clauses.join(' AND ')}`,
    params
  }
}

function buildAuthorizationGranteeGroupFilterForClient(client: DatabaseClient, options: AuthorizationGranteeGroupOptionListOptions, granteeSystemAccountId: string): { clause: string; params: string[] } {
  const clauses = [
    'groups.system_account_id = ?',
    'groups.enabled = 1',
    `EXISTS (SELECT 1 FROM ${authorizationOptionsTable(client, 'system_accounts')} grantee WHERE grantee.id = ? AND grantee.status = 'active')`
  ]
  const params = [granteeSystemAccountId, granteeSystemAccountId]
  const ids = normalizeTextList(options.ids)
  if (ids.length) {
    clauses.push(`groups.id IN (${sqlPlaceholders(ids.length)})`)
    params.push(...ids)
  }
  const providerCode = optionalString(options.providerCode)
  if (providerCode) {
    clauses.push('groups.provider_code = ?')
    params.push(providerCode)
  }
  const keyword = optionalString(options.keyword)
  if (keyword) {
    clauses.push(client.driver === 'postgres'
      ? '(groups.name COLLATE "C" >= ? AND groups.name COLLATE "C" < ? AND starts_with(groups.name, ?))'
      : '(groups.name >= ? AND groups.name < ?)')
    params.push(...(client.driver === 'postgres'
      ? [keyword, textPrefixUpperBound(keyword), keyword]
      : [keyword, textPrefixUpperBound(keyword)]))
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
  return {
    clause: `WHERE (
      (username >= ? AND username < ?)
      OR (display_name >= ? AND display_name < ?)
    )`,
    params: [text, textPrefixUpperBound(text), text, textPrefixUpperBound(text)]
  }
}

function buildSystemTeamPrincipalKeywordFilter(keyword?: string): { clause: string; params: string[] } {
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  return {
    clause: 'WHERE (name >= ? AND name < ?)',
    params: [text, textPrefixUpperBound(text)]
  }
}

function buildSystemAccountPrincipalKeywordFilterForClient(client: DatabaseClient, keyword?: string): { clause: string; params: string[] } {
  if (client.driver !== 'postgres') return buildSystemAccountPrincipalKeywordFilter(keyword)
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  return {
    clause: `WHERE (
      (username COLLATE "C" >= ? AND username COLLATE "C" < ? AND starts_with(username, ?))
      OR (display_name COLLATE "C" >= ? AND display_name COLLATE "C" < ? AND starts_with(display_name, ?))
    )`,
    params: [text, textPrefixUpperBound(text), text, text, textPrefixUpperBound(text), text]
  }
}

function buildSystemTeamPrincipalKeywordFilterForClient(client: DatabaseClient, keyword?: string): { clause: string; params: string[] } {
  if (client.driver !== 'postgres') return buildSystemTeamPrincipalKeywordFilter(keyword)
  const text = optionalString(keyword)
  if (!text) return { clause: '', params: [] }
  return {
    clause: 'WHERE (name COLLATE "C" >= ? AND name COLLATE "C" < ? AND starts_with(name, ?))',
    params: [text, textPrefixUpperBound(text), text]
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
