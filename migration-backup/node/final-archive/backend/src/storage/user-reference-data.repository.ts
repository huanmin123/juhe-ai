import type {
  RouteStrategyMode,
  RouteStrategyStatus,
  UserDefaultRouteStrategyReference,
  UserProviderDefaultReference,
  UserReferenceData
} from '../domain/types.js'
import { GPT_VENDOR_CODE } from '../domain/provider-protocol.js'
import { runtimeConfig } from '../config/runtime.js'
import { manageableSystemAccountId, currentSystemAccountId, type AccessScope } from './access-scope.js'
import { getBusinessDatabase } from './database.js'
import {
  createPostgresDatabaseClient,
  createSqliteDatabaseClient,
  type DatabaseClient
} from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

interface UserReferenceRow {
  system_account_id: string
  provider_code?: string | null
  group_id?: string | null
  group_name?: string | null
  group_enabled?: number | boolean | null
  route_strategy_id?: string | null
  route_strategy_name?: string | null
  route_strategy_mode?: RouteStrategyMode | null
  route_strategy_status?: RouteStrategyStatus | null
  route_binding_status?: string | null
}

const businessSchemaName = 'juhe_business'

export async function findUserReferenceDataAsync(access?: AccessScope): Promise<UserReferenceData | undefined> {
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  return findUserReferenceDataForSystemAccountAsync(systemAccountId)
}

export async function findUserReferenceDataForSystemAccountAsync(
  systemAccountId: string,
  client?: DatabaseClient
): Promise<UserReferenceData | undefined> {
  const ownerSystemAccountId = systemAccountId.trim()
  if (!ownerSystemAccountId) return undefined
  const databaseClient = client ?? await getUserReferenceDatabaseClient()
  const trueLiteral = userReferenceTrueLiteral(databaseClient)
  const rows = await databaseClient.query<UserReferenceRow>(`
    SELECT
      system_accounts.id AS system_account_id,
      groups.provider_code,
      groups.id AS group_id,
      groups.name AS group_name,
      groups.enabled AS group_enabled,
      default_routes.route_strategy_id,
      default_routes.route_strategy_name,
      default_routes.route_strategy_mode,
      default_routes.route_strategy_status,
      default_routes.route_binding_status
    FROM ${userReferenceTable(databaseClient, 'system_accounts')} system_accounts
    LEFT JOIN ${userReferenceTable(databaseClient, 'groups')} groups
      ON groups.system_account_id = system_accounts.id
      AND groups.is_default = ${trueLiteral}
    LEFT JOIN (
      SELECT
        route_strategy_groups.system_account_id,
        route_strategy_groups.group_id,
        route_strategy_groups.status AS route_binding_status,
        route_strategies.id AS route_strategy_id,
        route_strategies.name AS route_strategy_name,
        route_strategies.mode AS route_strategy_mode,
        route_strategies.status AS route_strategy_status,
        route_strategies.created_at AS route_strategy_created_at
      FROM ${userReferenceTable(databaseClient, 'route_strategy_groups')} route_strategy_groups
      INNER JOIN ${userReferenceTable(databaseClient, 'route_strategies')} route_strategies
        ON route_strategies.id = route_strategy_groups.route_strategy_id
        AND route_strategies.system_account_id = route_strategy_groups.system_account_id
        AND route_strategies.is_default = ${trueLiteral}
    ) default_routes
      ON default_routes.system_account_id = groups.system_account_id
      AND default_routes.group_id = groups.id
    WHERE system_accounts.id = ?
    ORDER BY
      groups.provider_code ASC,
      CASE WHEN default_routes.route_binding_status = 'active' THEN 0 ELSE 1 END ASC,
      CASE WHEN default_routes.route_strategy_status = 'active' THEN 0 ELSE 1 END ASC,
      default_routes.route_strategy_created_at ASC,
      default_routes.route_strategy_id ASC
  `, [ownerSystemAccountId])
  if (!rows.length) return undefined
  return userReferenceDataFromRows(ownerSystemAccountId, rows)
}

export async function findPreferredDefaultRouteStrategyReferenceAsync(
  systemAccountId: string,
  client?: DatabaseClient,
  lockRows = false
): Promise<UserDefaultRouteStrategyReference | undefined> {
  const ownerSystemAccountId = systemAccountId.trim()
  if (!ownerSystemAccountId) return undefined
  const databaseClient = client ?? await getUserReferenceDatabaseClient()
  const trueLiteral = userReferenceTrueLiteral(databaseClient)
  const lockClause = lockRows && databaseClient.driver === 'postgres'
    ? ' FOR UPDATE OF route_strategies, route_strategy_groups, groups'
    : ''
  const row = await databaseClient.one<{
    id: string
    name: string
    mode: RouteStrategyMode
    status: RouteStrategyStatus
  }>(`
    SELECT
      route_strategies.id,
      route_strategies.name,
      route_strategies.mode,
      route_strategies.status
    FROM ${userReferenceTable(databaseClient, 'route_strategies')} route_strategies
    INNER JOIN ${userReferenceTable(databaseClient, 'route_strategy_groups')} route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
      AND route_strategy_groups.status = 'active'
    INNER JOIN ${userReferenceTable(databaseClient, 'groups')} groups
      ON groups.id = route_strategy_groups.group_id
      AND groups.system_account_id = route_strategy_groups.system_account_id
      AND groups.enabled = ${trueLiteral}
      AND groups.is_default = ${trueLiteral}
    WHERE route_strategies.system_account_id = ?
      AND route_strategies.status = 'active'
      AND route_strategies.is_default = ${trueLiteral}
      AND groups.provider_code = ?
    ORDER BY route_strategies.created_at ASC, route_strategies.id ASC
    LIMIT 1${lockClause}
  `, [ownerSystemAccountId, GPT_VENDOR_CODE])
  return row ? defaultRouteStrategyReference(row) : undefined
}

function userReferenceDataFromRows(systemAccountId: string, rows: UserReferenceRow[]): UserReferenceData {
  const providerDefaults: UserProviderDefaultReference[] = []
  const providerDefaultsByCode = new Map<string, UserProviderDefaultReference>()
  let preferredDefaultRouteStrategy: UserDefaultRouteStrategyReference | undefined

  for (const row of rows) {
    const providerCode = row.provider_code?.trim()
    const groupId = row.group_id?.trim()
    const groupName = row.group_name?.trim()
    if (!providerCode || !groupId || !groupName) continue

    let providerDefault = providerDefaultsByCode.get(providerCode)
    if (!providerDefault) {
      providerDefault = {
        providerCode,
        defaultGroup: { id: groupId, name: groupName }
      }
      providerDefaultsByCode.set(providerCode, providerDefault)
      providerDefaults.push(providerDefault)
    }

    const routeStrategy = routeStrategyReferenceFromRow(row)
    if (routeStrategy && !providerDefault.defaultRouteStrategy) {
      providerDefault.defaultRouteStrategy = routeStrategy
    }
    if (
      !preferredDefaultRouteStrategy
      && providerCode === GPT_VENDOR_CODE
      && Boolean(row.group_enabled)
      && row.route_binding_status === 'active'
      && routeStrategy?.status === 'active'
    ) {
      preferredDefaultRouteStrategy = routeStrategy
    }
  }

  return {
    systemAccountId,
    providerDefaults,
    ...(preferredDefaultRouteStrategy ? { preferredDefaultRouteStrategy } : {})
  }
}

function routeStrategyReferenceFromRow(row: UserReferenceRow): UserDefaultRouteStrategyReference | undefined {
  const id = row.route_strategy_id?.trim()
  const name = row.route_strategy_name?.trim()
  const mode = row.route_strategy_mode
  const status = row.route_strategy_status
  if (!id || !name || !mode || !status) return undefined
  return defaultRouteStrategyReference({ id, name, mode, status })
}

function defaultRouteStrategyReference(input: {
  id: string
  name: string
  mode: RouteStrategyMode
  status: RouteStrategyStatus
}): UserDefaultRouteStrategyReference {
  return {
    id: input.id,
    name: input.name,
    mode: input.mode,
    status: input.status
  }
}

async function getUserReferenceDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function userReferenceTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function userReferenceTrueLiteral(_client: DatabaseClient): '1' {
  return '1'
}
