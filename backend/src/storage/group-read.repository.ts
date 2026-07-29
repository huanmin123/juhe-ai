import type { AccountUsageStatsRange, AccountUsageSummary, GroupType, ProviderCode } from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, userVisibleSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { runtimeConfig } from '../config/runtime.js'
import { normalizeListPage, pagedTotalUpperBound, takePageRows } from './query-utils.js'
import type { GroupListRow } from './repository-row-types.js'
import {
  loadAuthorizationUsageRangeSummariesForScopes,
  loadAuthorizationUsageRangeSummariesForScopesAsync,
  loadAuthorizationUsageSummariesForScopes,
  loadAuthorizationUsageSummariesForScopesAsync,
  type UsageSummaryScopeRequest
} from './usage-summary-loaders.js'

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

export interface RouteStrategyGroupOptionRow {
  id: string
  name: string
  provider_code: string
  enabled: number | boolean
}

export interface GroupEditRow {
  name: string
  provider_code: ProviderCode
  description?: string | null
  enabled: number | boolean | string
  group_type: GroupType | null
  scheduling_policy_json: string | null
  updated_at: string
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
const businessSchemaName = 'juhe_business'

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

export async function listGroupRowsForAccessAsync(access?: AccessScope, options?: GroupListOptions): Promise<GroupListRow[]> {
  const listOptions = normalizeGroupListOptions(options)
  const pagination = options
    ? { limit: listOptions.pageSize, offset: (listOptions.page - 1) * listOptions.pageSize }
    : undefined
  return (await queryGroupRowsForAccessAsync(access, pagination, listOptions)).rows
}

export async function listGroupOptionRowsForAccessAsync(access?: AccessScope, options?: GroupOptionListOptions): Promise<GroupListRow[]> {
  const listOptions = normalizeGroupOptionListOptions(options)
  const pagination = options
    ? { limit: listOptions.pageSize, offset: (listOptions.page - 1) * listOptions.pageSize }
    : undefined
  return (await queryGroupRowsForAccessAsync(access, pagination, listOptions)).rows
}

export async function listGroupOptionRowsForAccessInClientAsync(
  client: DatabaseClient,
  access?: AccessScope,
  options?: GroupOptionListOptions
): Promise<GroupListRow[]> {
  const listOptions = normalizeGroupOptionListOptions(options)
  const pagination = options
    ? { limit: listOptions.pageSize, offset: (listOptions.page - 1) * listOptions.pageSize }
    : undefined
  return (await queryGroupRowsForAccessInClientAsync(client, access, pagination, listOptions)).rows
}

export async function listRouteStrategyGroupOptionRowsForAccessAsync(
  access?: AccessScope,
  options?: GroupOptionListOptions
): Promise<RouteStrategyGroupOptionRow[]> {
  const listOptions = normalizeGroupOptionListOptions(options)
  const pagination = options
    ? { limit: listOptions.pageSize, offset: (listOptions.page - 1) * listOptions.pageSize }
    : undefined
  const client = await getGroupReadDatabaseClient()
  return queryRouteStrategyGroupOptionRowsForAccessInClientAsync(client, access, pagination, listOptions)
}

export async function listGroupRowsPageForAccessAsync(access: AccessScope | undefined, options?: GroupListOptions): Promise<GroupRowsPage> {
  const listOptions = normalizeGroupListOptions(options)
  const rows = (await queryGroupRowsForAccessAsync(access, {
    limit: listOptions.pageSize + 1,
    offset: (listOptions.page - 1) * listOptions.pageSize
  }, listOptions)).rows
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
        SELECT ${authorizedGroupRowSelectColumns('groups', 'authorization_settings')}, ${authorizedAuthorizationColumns()}
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        LEFT JOIN group_authorization_settings authorization_settings
          ON authorization_settings.authorization_id = ra.id
          AND authorization_settings.system_account_id = ra.grantee_system_account_id
          AND authorization_settings.group_id = ra.resource_id
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

async function queryGroupRowsForAccessAsync(access?: AccessScope, pagination?: { limit: number; offset: number }, options: Pick<NormalizedGroupListOptions, 'ids' | 'keyword' | 'providerCode' | 'manageableOnly' | 'preferDefault'> = { ids: [], manageableOnly: false, preferDefault: false }): Promise<{ rows: GroupListRow[] }> {
  const client = await getGroupReadDatabaseClient()
  return queryGroupRowsForAccessInClientAsync(client, access, pagination, options)
}

async function queryGroupRowsForAccessInClientAsync(
  client: DatabaseClient,
  access?: AccessScope,
  pagination?: { limit: number; offset: number },
  options: Pick<NormalizedGroupListOptions, 'ids' | 'keyword' | 'providerCode' | 'manageableOnly' | 'preferDefault'> = { ids: [], manageableOnly: false, preferDefault: false }
): Promise<{ rows: GroupListRow[] }> {
  const groupsTable = groupTable(client, 'groups')
  const resourceAuthorizationsTable = groupTable(client, 'resource_authorizations')
  const groupAuthorizationSettingsTable = groupTable(client, 'group_authorization_settings')
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const orderClause = groupOrderClause(options.preferDefault)
  const directFilter = buildGroupFilterForClient(client, 'groups', options)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    const rows = await client.query<GroupListRow>(`
      SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()}
      FROM ${groupsTable} groups
      ${whereClause(directFilter.clauses)}
      ${orderClause}
      ${pageClause}
    `, [...directFilter.params, ...pageParams])
    return { rows }
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }
  if (options.manageableOnly) {
    const ownerFilter = buildGroupFilterForClient(client, 'groups', options, ['groups.system_account_id = ?'], [ownerSystemAccountId ?? viewerSystemAccountId])
    const rows = await client.query<GroupListRow>(`
      SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()}
      FROM ${groupsTable} groups
      ${whereClause(ownerFilter.clauses)}
      ${orderClause}
      ${pageClause}
    `, [...ownerFilter.params, ...pageParams])
    return { rows }
  }
  const outerFilter = buildGroupFilterForClient(client, undefined, options)
  const rows = await client.query<GroupListRow>(`
    SELECT ${groupListRowOuterSelectColumns()} FROM (
      SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()}
      FROM ${groupsTable} groups
      WHERE groups.system_account_id = ?
      UNION ALL
      SELECT ${authorizedGroupRowSelectColumns('groups', 'authorization_settings')}, ${authorizedAuthorizationColumns()}
      FROM ${resourceAuthorizationsTable} ra
      INNER JOIN ${groupsTable} groups ON groups.id = ra.resource_id
      LEFT JOIN ${groupAuthorizationSettingsTable} authorization_settings
        ON authorization_settings.authorization_id = ra.id
        AND authorization_settings.system_account_id = ra.grantee_system_account_id
        AND authorization_settings.group_id = ra.resource_id
      WHERE ra.resource_type = 'group'
        AND ra.grantee_system_account_id = ?
        AND ra.status IN ('active', 'paused', 'expired')
        AND groups.system_account_id <> ?
    ) group_rows
    ${whereClause(outerFilter.clauses)}
    ${orderClause}
    ${pageClause}
  `, [ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, ownerSystemAccountId ?? viewerSystemAccountId, ...outerFilter.params, ...pageParams])
  return { rows }
}

async function queryRouteStrategyGroupOptionRowsForAccessInClientAsync(
  client: DatabaseClient,
  access?: AccessScope,
  pagination?: { limit: number; offset: number },
  options: Pick<NormalizedGroupListOptions, 'ids' | 'keyword' | 'providerCode' | 'manageableOnly' | 'preferDefault'> = { ids: [], manageableOnly: false, preferDefault: false }
): Promise<RouteStrategyGroupOptionRow[]> {
  const groupsTable = groupTable(client, 'groups')
  const resourceAuthorizationsTable = groupTable(client, 'resource_authorizations')
  const groupAuthorizationSettingsTable = groupTable(client, 'group_authorization_settings')
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  const pageClause = pagination ? ' LIMIT ? OFFSET ?' : ''
  const pageParams = pagination ? [pagination.limit, pagination.offset] : []
  const orderClause = groupOrderClause(options.preferDefault)
  const directFilter = buildGroupFilterForClient(client, 'groups', options)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return client.query<RouteStrategyGroupOptionRow>(`
      SELECT groups.id, groups.name, groups.provider_code, groups.enabled
      FROM ${groupsTable} groups
      ${whereClause(directFilter.clauses)}
      ${orderClause}
      ${pageClause}
    `, [...directFilter.params, ...pageParams])
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }
  if (options.manageableOnly) {
    const ownerFilter = buildGroupFilterForClient(client, 'groups', options, ['groups.system_account_id = ?'], [ownerSystemAccountId ?? viewerSystemAccountId])
    return client.query<RouteStrategyGroupOptionRow>(`
      SELECT groups.id, groups.name, groups.provider_code, groups.enabled
      FROM ${groupsTable} groups
      ${whereClause(ownerFilter.clauses)}
      ${orderClause}
      ${pageClause}
    `, [...ownerFilter.params, ...pageParams])
  }
  const outerFilter = buildGroupFilterForClient(client, undefined, options)
  return client.query<RouteStrategyGroupOptionRow>(`
    SELECT id, name, provider_code, enabled FROM (
      SELECT groups.id, groups.name, groups.provider_code, groups.enabled, groups.is_default, groups.updated_at
      FROM ${groupsTable} groups
      WHERE groups.system_account_id = ?
      UNION ALL
      SELECT
        groups.id,
        groups.name,
        groups.provider_code,
        CASE WHEN groups.enabled = 1 THEN COALESCE(authorization_settings.enabled, 1) ELSE 0 END AS enabled,
        groups.is_default,
        COALESCE(authorization_settings.updated_at, groups.updated_at) AS updated_at
      FROM ${resourceAuthorizationsTable} ra
      INNER JOIN ${groupsTable} groups ON groups.id = ra.resource_id
      LEFT JOIN ${groupAuthorizationSettingsTable} authorization_settings
        ON authorization_settings.authorization_id = ra.id
        AND authorization_settings.system_account_id = ra.grantee_system_account_id
        AND authorization_settings.group_id = ra.resource_id
      WHERE ra.resource_type = 'group'
        AND ra.grantee_system_account_id = ?
        AND ra.status IN ('active', 'paused', 'expired')
        AND groups.system_account_id <> ?
    ) group_rows
    ${whereClause(outerFilter.clauses)}
    ${orderClause}
    ${pageClause}
  `, [ownerSystemAccountId ?? viewerSystemAccountId, viewerSystemAccountId, ownerSystemAccountId ?? viewerSystemAccountId, ...outerFilter.params, ...pageParams])
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
        SELECT ${authorizedGroupRowSelectColumns('groups', 'authorization_settings')}, ${authorizedAuthorizationColumns()}
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        LEFT JOIN group_authorization_settings authorization_settings
          ON authorization_settings.authorization_id = ra.id
          AND authorization_settings.system_account_id = ra.grantee_system_account_id
          AND authorization_settings.group_id = ra.resource_id
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

export async function findGroupRowForAccessAsync(access: AccessScope | undefined, groupId: string): Promise<GroupListRow | undefined> {
  const client = await getGroupReadDatabaseClient()
  return findGroupRowForAccessInClientAsync(client, access, groupId)
}

export async function findGroupRowForAccessInClientAsync(client: DatabaseClient, access: AccessScope | undefined, groupId: string): Promise<GroupListRow | undefined> {
  const groupsTable = groupTable(client, 'groups')
  const resourceAuthorizationsTable = groupTable(client, 'resource_authorizations')
  const groupAuthorizationSettingsTable = groupTable(client, 'group_authorization_settings')
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return client.one<GroupListRow>(`
      SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()}
      FROM ${groupsTable} groups
      WHERE groups.id = ?
    `, [groupId])
  }
  if (!viewerSystemAccountId) {
    throw new Error('缺少系统账户上下文')
  }
  return client.one<GroupListRow>(`
    SELECT ${groupListRowOuterSelectColumns()} FROM (
      SELECT ${groupRowSelectColumns('groups')}, ${ownerAuthorizationColumns()}
      FROM ${groupsTable} groups
      WHERE groups.id = ?
        AND groups.system_account_id = ?
      UNION ALL
      SELECT ${authorizedGroupRowSelectColumns('groups', 'authorization_settings')}, ${authorizedAuthorizationColumns()}
      FROM ${resourceAuthorizationsTable} ra
      INNER JOIN ${groupsTable} groups ON groups.id = ra.resource_id
      LEFT JOIN ${groupAuthorizationSettingsTable} authorization_settings
        ON authorization_settings.authorization_id = ra.id
        AND authorization_settings.system_account_id = ra.grantee_system_account_id
        AND authorization_settings.group_id = ra.resource_id
      WHERE groups.id = ?
        AND ra.resource_type = 'group'
        AND ra.grantee_system_account_id = ?
        AND ra.status IN ('active', 'paused', 'expired')
        AND groups.system_account_id <> ?
    ) group_rows
    LIMIT 1
  `, [groupId, ownerSystemAccountId ?? viewerSystemAccountId, groupId, viewerSystemAccountId, ownerSystemAccountId ?? viewerSystemAccountId])
}

export function findGroupEditRowForAccess(access: AccessScope | undefined, groupId: string): GroupEditRow | undefined {
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return getBusinessDatabase()
      .prepare(`
        SELECT ${groupEditOwnerSelectColumns('groups')}
        FROM groups
        WHERE groups.id = ?
        LIMIT 1
      `)
      .get(groupId) as unknown as GroupEditRow | undefined
  }
  if (!viewerSystemAccountId) throw new Error('缺少系统账户上下文')
  return getBusinessDatabase()
    .prepare(`
      SELECT ${groupEditOuterSelectColumns()} FROM (
        SELECT ${groupEditOwnerSelectColumns('groups')}
        FROM groups
        WHERE groups.id = ?
          AND groups.system_account_id = ?
        UNION ALL
        SELECT ${groupEditAuthorizedSelectColumns('groups', 'authorization_settings')}
        FROM resource_authorizations ra
        INNER JOIN groups ON groups.id = ra.resource_id
        LEFT JOIN group_authorization_settings authorization_settings
          ON authorization_settings.authorization_id = ra.id
          AND authorization_settings.system_account_id = ra.grantee_system_account_id
          AND authorization_settings.group_id = ra.resource_id
        WHERE groups.id = ?
          AND ra.resource_type = 'group'
          AND ra.grantee_system_account_id = ?
          AND ra.status IN ('active', 'paused', 'expired')
          AND groups.system_account_id <> ?
      ) group_edit_rows
      LIMIT 1
    `)
    .get(groupId, ownerSystemAccountId ?? viewerSystemAccountId, groupId, viewerSystemAccountId, ownerSystemAccountId ?? viewerSystemAccountId) as unknown as GroupEditRow | undefined
}

export async function findGroupEditRowForAccessAsync(access: AccessScope | undefined, groupId: string): Promise<GroupEditRow | undefined> {
  const client = await getGroupReadDatabaseClient()
  return findGroupEditRowForAccessInClientAsync(client, access, groupId)
}

export async function findGroupEditRowForAccessInClientAsync(client: DatabaseClient, access: AccessScope | undefined, groupId: string): Promise<GroupEditRow | undefined> {
  const groupsTable = groupTable(client, 'groups')
  const resourceAuthorizationsTable = groupTable(client, 'resource_authorizations')
  const groupAuthorizationSettingsTable = groupTable(client, 'group_authorization_settings')
  const viewerSystemAccountId = userVisibleSystemAccountId(access)
  const ownerSystemAccountId = manageableSystemAccountId(access)
  if (!ownerSystemAccountId && canAccessAll(access)) {
    return client.one<GroupEditRow>(`
      SELECT ${groupEditOwnerSelectColumns('groups')}
      FROM ${groupsTable} groups
      WHERE groups.id = ?
      LIMIT 1
    `, [groupId])
  }
  if (!viewerSystemAccountId) throw new Error('缺少系统账户上下文')
  return client.one<GroupEditRow>(`
    SELECT ${groupEditOuterSelectColumns()} FROM (
      SELECT ${groupEditOwnerSelectColumns('groups')}
      FROM ${groupsTable} groups
      WHERE groups.id = ?
        AND groups.system_account_id = ?
      UNION ALL
      SELECT ${groupEditAuthorizedSelectColumns('groups', 'authorization_settings')}
      FROM ${resourceAuthorizationsTable} ra
      INNER JOIN ${groupsTable} groups ON groups.id = ra.resource_id
      LEFT JOIN ${groupAuthorizationSettingsTable} authorization_settings
        ON authorization_settings.authorization_id = ra.id
        AND authorization_settings.system_account_id = ra.grantee_system_account_id
        AND authorization_settings.group_id = ra.resource_id
      WHERE groups.id = ?
        AND ra.resource_type = 'group'
        AND ra.grantee_system_account_id = ?
        AND ra.status IN ('active', 'paused', 'expired')
        AND groups.system_account_id <> ?
    ) group_edit_rows
    LIMIT 1
  `, [groupId, ownerSystemAccountId ?? viewerSystemAccountId, groupId, viewerSystemAccountId, ownerSystemAccountId ?? viewerSystemAccountId])
}

function groupEditOwnerSelectColumns(alias: string): string {
  return [
    `${alias}.name`,
    `${alias}.provider_code`,
    `${alias}.description`,
    `${alias}.enabled`,
    `${alias}.group_type`,
    `${alias}.scheduling_policy_json`,
    `${alias}.updated_at`
  ].join(', ')
}

function groupEditAuthorizedSelectColumns(groupAlias: string, settingsAlias: string): string {
  const localGroupType = `COALESCE(${settingsAlias}.group_type, ${groupAlias}.group_type)`
  return [
    `${groupAlias}.name`,
    `${groupAlias}.provider_code`,
    `${groupAlias}.description`,
    `CASE WHEN ${groupAlias}.enabled = 1 THEN COALESCE(${settingsAlias}.enabled, 1) ELSE 0 END AS enabled`,
    `${localGroupType} AS group_type`,
    `CASE WHEN ${localGroupType} = 'high_concurrency' THEN COALESCE(${settingsAlias}.scheduling_policy_json, ${groupAlias}.scheduling_policy_json) ELSE NULL END AS scheduling_policy_json`,
    `COALESCE(${settingsAlias}.updated_at, ${groupAlias}.updated_at) AS updated_at`
  ].join(', ')
}

function groupEditOuterSelectColumns(): string {
  return [
    'name',
    'provider_code',
    'description',
    'enabled',
    'group_type',
    'scheduling_policy_json',
    'updated_at'
  ].join(', ')
}

function ownerAuthorizationColumns(): string {
  return [
    "'owner' AS access_type",
    'NULL AS authorization_id',
    'NULL AS authorization_status',
    'NULL AS authorization_expires_at',
    'NULL AS authorization_limits_json',
    'NULL AS authorization_effective_source_type',
    'NULL AS authorization_effective_source_team_id'
  ].join(', ')
}

function authorizedAuthorizationColumns(): string {
  return [
    "'authorized' AS access_type",
    'ra.id AS authorization_id',
    'ra.status AS authorization_status',
    'ra.expires_at AS authorization_expires_at',
    'ra.limits_json AS authorization_limits_json',
    'ra.effective_source_type AS authorization_effective_source_type',
    'ra.effective_source_team_id AS authorization_effective_source_team_id'
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

function authorizedGroupRowSelectColumns(groupAlias: string, settingsAlias: string): string {
  const localGroupType = `COALESCE(${settingsAlias}.group_type, ${groupAlias}.group_type)`
  return [
    `${groupAlias}.id`,
    `${groupAlias}.system_account_id`,
    `${groupAlias}.name`,
    `${groupAlias}.provider_code`,
    `${groupAlias}.description`,
    `CASE WHEN ${groupAlias}.enabled = 1 THEN COALESCE(${settingsAlias}.enabled, 1) ELSE 0 END AS enabled`,
    `${groupAlias}.is_default`,
    `${localGroupType} AS group_type`,
    `CASE WHEN ${localGroupType} = 'high_concurrency' THEN COALESCE(${settingsAlias}.scheduling_policy_json, ${groupAlias}.scheduling_policy_json) ELSE NULL END AS scheduling_policy_json`,
    `${groupAlias}.created_at`,
    `COALESCE(${settingsAlias}.updated_at, ${groupAlias}.updated_at) AS updated_at`
  ].join(', ')
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
    'authorization_limits_json',
    'authorization_effective_source_type',
    'authorization_effective_source_team_id'
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
    clauses.push(`${column('provider_code')} = ?`)
    params.push(providerCode)
  }
  const text = options.keyword?.trim()
  if (text) {
    const upperBound = textPrefixUpperBound(text)
    clauses.push(`(
      (${column('name')} >= ? AND ${column('name')} < ?)
      OR (${column('provider_code')} >= ? AND ${column('provider_code')} < ?)
    )`)
    params.push(text, upperBound, text, upperBound)
  }
  return { clauses, params }
}

function buildGroupFilterForClient(
  client: DatabaseClient,
  alias: string | undefined,
  options: Pick<NormalizedGroupListOptions, 'ids' | 'keyword' | 'providerCode'>,
  initialClauses: string[] = [],
  initialParams: string[] = []
): { clauses: string[]; params: string[] } {
  if (client.driver === 'sqlite') {
    return buildGroupFilter(alias, options, initialClauses, initialParams)
  }
  const clauses = [...initialClauses]
  const params = [...initialParams]
  const providerCode = options.providerCode?.trim()
  const column = (name: string) => alias ? `${alias}.${name}` : name
  const cRange = (name: string) => `(${column(name)} COLLATE "C" >= ? AND ${column(name)} COLLATE "C" < ?)`
  if (options.ids.length) {
    clauses.push(`${column('id')} IN (${options.ids.map(() => '?').join(', ')})`)
    params.push(...options.ids)
  }
  if (providerCode) {
    clauses.push(`${column('provider_code')} = ?`)
    params.push(providerCode)
  }
  const text = options.keyword?.trim()
  if (text) {
    const upperBound = textPrefixUpperBound(text)
    clauses.push(`(
      ${cRange('name')}
      OR ${cRange('provider_code')}
    )`)
    params.push(text, upperBound, text, upperBound)
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

function textPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index]?.codePointAt(0)
    if (codePoint !== undefined && codePoint < 0x10ffff) {
      return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
    }
  }
  return `${value}\uffff`
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

export async function loadGroupAuthorizationUsageSummariesAsync(
  scopes: UsageSummaryScopeRequest[],
  statDateOrRange?: string | Pick<AccountUsageStatsRange, 'startDate' | 'endDate'>,
  scopeType: 'group_authorization' | 'group_authorization_team' = 'group_authorization'
): Promise<Map<string, AccountUsageSummary>> {
  if (statDateOrRange && typeof statDateOrRange !== 'string') {
    return loadAuthorizationUsageRangeSummariesForScopesAsync(scopes, scopeType, statDateOrRange)
  }
  return loadAuthorizationUsageSummariesForScopesAsync(scopes, scopeType, statDateOrRange)
}

async function getGroupReadDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function groupTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
