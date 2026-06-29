import type { DatabaseSync } from 'node:sqlite'

import { normalizeApiKeyGroupBindingWeight } from '../domain/api-key-routing.js'
import { normalizeHybridRoutingConfig } from '../domain/api-key-hybrid-routing.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../domain/provider-protocol.js'
import {
  normalizeRouteStrategyMode,
  parseRouteStrategyRuntimeConfigJson,
  routeStrategyConfigJson
} from '../domain/route-strategy.js'
import type {
  RouteStrategyGroupBindingSummary,
  ApiKeyHybridRoutingConfig,
  RouteStrategyListResult,
  RouteStrategyMode,
  RouteStrategyOptionSummary,
  RouteStrategyStatus,
  RouteStrategySummary
} from '../domain/types.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, buildSystemAccountScopeClause, buildSystemAccountWhereClause, type AccessScope } from './access-scope.js'
import { canManageApiKeyOwner } from './api-key-access.js'
import { maxRouteStrategyGroupBindings } from './route-strategy-group-binding-limits.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadSystemAccountNameMapByIds, loadSystemAccountNameMapByIdsAsync } from './repository-lookups.js'
import { assertKnownInputKeys, hasOwnInput, normalizeNullableTextInput, normalizeOptionalRequiredTextInput, requiredTextInput } from './repository-input-normalization.js'

const businessSchemaName = 'juhe_business'
const ROUTE_STRATEGY_GROUP_BOUNDARY_ERROR = '策略路由只能绑定自己的分组或有效授权给自己的分组'
const routeStrategyMutationInputKeys = new Set([
  'name',
  'description',
  'mode',
  'status',
  'groupBindings',
  'hybridRoutingConfig'
])

export interface RouteStrategyListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  mode?: RouteStrategyMode | 'all'
  status?: RouteStrategyStatus | 'all'
}

export interface RouteStrategyOptionListOptions {
  ids?: string[]
  keyword?: string
  limit?: number
  activeOnly?: boolean
}

type RouteStrategyGroupBindingStatus = 'active' | 'disabled'

interface RouteStrategyGroupBindingInput {
  groupId: string
  priority: number
  weight: number
  status: RouteStrategyGroupBindingStatus
}

interface RouteStrategyGroupBindingWrite extends RouteStrategyGroupBindingInput {
  groupName?: string
  providerCode: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  groupEnabled: boolean
}

interface RouteStrategyRow {
  id: string
  system_account_id: string
  system_account_name?: string | null
  name: string
  description: string | null
  mode: RouteStrategyMode | string
  status: RouteStrategyStatus | string
  is_default?: number | boolean | string | null
  config_json: string | null
  api_key_count?: number | string | null
  created_at: string
  updated_at: string
}

interface RouteStrategyGroupBindingRow {
  id: string
  route_strategy_id: string
  system_account_id: string
  group_id: string
  group_name: string | null
  provider_code: string | null
  provider_protocol_profile_id: string | null
  protocol_code: string | null
  protocol_version: string | null
  group_enabled: number | null
  priority: number
  weight?: number | null
  status: RouteStrategyGroupBindingStatus | string
}

interface RouteStrategyBindableGroupRow {
  id: string
  system_account_id: string
  provider_code: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string | null
  enabled: number
  can_bind: number
}

export function listRouteStrategiesPage(access?: AccessScope, options?: RouteStrategyListOptions): RouteStrategyListResult {
  const normalized = normalizeRouteStrategyListOptions(options)
  const scope = buildSystemAccountWhereClause(access, 'route_strategies.system_account_id')
  const filters = buildRouteStrategyFilters(scope, normalized)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${routeStrategyListColumns()}
      FROM route_strategies
      LEFT JOIN system_accounts ON system_accounts.id = route_strategies.system_account_id
      ${filters.clause}
      ORDER BY route_strategies.updated_at DESC, route_strategies.created_at DESC, route_strategies.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize) as unknown as RouteStrategyRow[]
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = routeStrategySummariesFromRows(pageRows.rows, access)
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export async function listRouteStrategiesPageAsync(access?: AccessScope, options?: RouteStrategyListOptions): Promise<RouteStrategyListResult> {
  const normalized = normalizeRouteStrategyListOptions(options)
  const client = await getRouteStrategyDatabaseClient()
  const scope = buildSystemAccountWhereClause(access, 'route_strategies.system_account_id')
  const filters = buildRouteStrategyFiltersForClient(client, scope, normalized)
  const rows = await client.query<RouteStrategyRow>(`
    SELECT ${routeStrategyListColumnsForClient(client)}
    FROM ${routeStrategyTable(client, 'route_strategies')} route_strategies
    LEFT JOIN ${routeStrategyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = route_strategies.system_account_id
    ${filters.clause}
    ORDER BY route_strategies.updated_at DESC, route_strategies.created_at DESC, route_strategies.id DESC
    LIMIT ? OFFSET ?
  `, [...filters.params, normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize])
  const pageRows = takePageRows(rows, normalized.pageSize)
  const items = await routeStrategySummariesFromRowsAsync(pageRows.rows, access, client)
  return {
    items,
    total: pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export function listRouteStrategyOptions(access?: AccessScope, options?: RouteStrategyOptionListOptions): RouteStrategyOptionSummary[] {
  const normalized = normalizeRouteStrategyOptionListOptions(options)
  const scope = buildSystemAccountWhereClause(access, 'route_strategies.system_account_id')
  const filters = buildRouteStrategyOptionFilters(scope, normalized)
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT route_strategies.id, route_strategies.system_account_id, route_strategies.name, route_strategies.mode, route_strategies.status, route_strategies.is_default
      FROM route_strategies
      ${filters.clause}
      ORDER BY route_strategies.is_default DESC, route_strategies.updated_at DESC, route_strategies.name COLLATE NOCASE ASC, route_strategies.id ASC
      LIMIT ?
    `)
    .all(...filters.params, normalized.limit) as unknown as RouteStrategyRow[]
  return routeStrategyOptionsFromRows(rows, access)
}

export async function listRouteStrategyOptionsAsync(access?: AccessScope, options?: RouteStrategyOptionListOptions): Promise<RouteStrategyOptionSummary[]> {
  const normalized = normalizeRouteStrategyOptionListOptions(options)
  const client = await getRouteStrategyDatabaseClient()
  const scope = buildSystemAccountWhereClause(access, 'route_strategies.system_account_id')
  const filters = buildRouteStrategyOptionFiltersForClient(client, scope, normalized)
  const rows = await client.query<RouteStrategyRow>(`
    SELECT route_strategies.id, route_strategies.system_account_id, route_strategies.name, route_strategies.mode, route_strategies.status, route_strategies.is_default
    FROM ${routeStrategyTable(client, 'route_strategies')} route_strategies
    ${filters.clause}
    ORDER BY route_strategies.is_default DESC, route_strategies.updated_at DESC, route_strategies.name ASC, route_strategies.id ASC
    LIMIT ?
  `, [...filters.params, normalized.limit])
  return routeStrategyOptionsFromRowsAsync(rows, access, client)
}

export function findRouteStrategySummary(id: string, access?: AccessScope): RouteStrategySummary | undefined {
  const scope = buildSystemAccountScopeClause(access, 'route_strategies.system_account_id')
  const row = getBusinessDatabase()
    .prepare(`
      SELECT ${routeStrategyListColumns()}
      FROM route_strategies
      LEFT JOIN system_accounts ON system_accounts.id = route_strategies.system_account_id
      WHERE route_strategies.id = ?${scope.clause}
    `)
    .get(id, ...scope.params) as unknown as RouteStrategyRow | undefined
  return row ? routeStrategySummariesFromRows([row], access)[0] : undefined
}

export async function findRouteStrategySummaryAsync(id: string, access?: AccessScope): Promise<RouteStrategySummary | undefined> {
  const client = await getRouteStrategyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'route_strategies.system_account_id')
  const row = await client.one<RouteStrategyRow>(`
    SELECT ${routeStrategyListColumnsForClient(client)}
    FROM ${routeStrategyTable(client, 'route_strategies')} route_strategies
    LEFT JOIN ${routeStrategyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = route_strategies.system_account_id
    WHERE route_strategies.id = ?${scope.clause}
  `, [id, ...scope.params])
  return row ? (await routeStrategySummariesFromRowsAsync([row], access, client))[0] : undefined
}

export function createRouteStrategy(input: Record<string, unknown>, access?: AccessScope): RouteStrategySummary {
  assertKnownInputKeys(input, routeStrategyMutationInputKeys, '策略路由创建参数')
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  const now = nowIso()
  const mode = normalizeRouteStrategyMode(input.mode)
  const bindingInputs = routeStrategyGroupBindingInputsFromRequest(input)
  const config = normalizeRouteStrategyConfigForWrite(input, mode)
  const record = {
    id: newId('route_strategy'),
    systemAccountId,
    name: requiredTextInput(input.name, '策略路由名称'),
    description: normalizeNullableTextInput(input.description, '策略路由说明'),
    mode,
    status: normalizeRouteStrategyStatus(input.status, 'active'),
    configJson: routeStrategyConfigJson(config),
    createdAt: now,
    updatedAt: now
  }
  assertRouteStrategyNameAvailable(systemAccountId, record.name)
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const bindings = normalizeRouteStrategyGroupBindings(bindingInputs, systemAccountId)
    database
      .prepare(`
        INSERT INTO route_strategies (id, system_account_id, name, description, mode, status, config_json, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(record.id, systemAccountId, record.name, record.description ?? null, mode, record.status, record.configJson, now, now)
    replaceRouteStrategyGroups(database, record.id, systemAccountId, mode, bindings, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    if (isDuplicateRouteStrategyNameError(error)) {
      throw new Error(`策略路由名称已存在：${record.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('route_strategy_created')
  return findRouteStrategySummary(record.id, access)!
}

export async function createRouteStrategyAsync(input: Record<string, unknown>, access?: AccessScope): Promise<RouteStrategySummary> {
  assertKnownInputKeys(input, routeStrategyMutationInputKeys, '策略路由创建参数')
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  const now = nowIso()
  const mode = normalizeRouteStrategyMode(input.mode)
  const bindingInputs = routeStrategyGroupBindingInputsFromRequest(input)
  const config = normalizeRouteStrategyConfigForWrite(input, mode)
  const record = {
    id: newId('route_strategy'),
    systemAccountId,
    name: requiredTextInput(input.name, '策略路由名称'),
    description: normalizeNullableTextInput(input.description, '策略路由说明'),
    mode,
    status: normalizeRouteStrategyStatus(input.status, 'active'),
    configJson: routeStrategyConfigJson(config),
    createdAt: now,
    updatedAt: now
  }
  const client = await getRouteStrategyDatabaseClient()
  try {
    await client.transaction(async (tx) => {
      const bindings = await normalizeRouteStrategyGroupBindingsAsync(bindingInputs, systemAccountId, tx, true)
      await assertRouteStrategyNameAvailableAsync(tx, systemAccountId, record.name)
      await tx.execute(`
        INSERT INTO ${routeStrategyTable(tx, 'route_strategies')} (id, system_account_id, name, description, mode, status, config_json, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
      `, [record.id, systemAccountId, record.name, record.description ?? null, mode, record.status, record.configJson, now, now])
      await replaceRouteStrategyGroupsAsync(tx, record.id, systemAccountId, mode, bindings, now)
    })
  } catch (error) {
    if (isDuplicateRouteStrategyNameError(error)) {
      throw new Error(`策略路由名称已存在：${record.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('route_strategy_created')
  return (await findRouteStrategySummaryAsync(record.id, access))!
}

export function updateRouteStrategy(id: string, input: Record<string, unknown>, access?: AccessScope): RouteStrategySummary | undefined {
  assertKnownInputKeys(input, routeStrategyMutationInputKeys, '策略路由更新参数')
  const systemAccountId = routeStrategySystemAccountId(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) return undefined
  const current = findRouteStrategySummary(id, { systemAccountId, role: 'super_admin', systemAccountFilterId: systemAccountId })
  if (!current) return undefined
  const mode = hasOwnInput(input, 'mode') ? normalizeRouteStrategyMode(input.mode) : current.mode
  const hasGroupBindingsInput = hasOwnInput(input, 'groupBindings')
  const bindingInputs = hasGroupBindingsInput ? routeStrategyGroupBindingInputsFromRequest(input) : undefined
  const hasHybridRoutingConfigInput = hasOwnInput(input, 'hybridRoutingConfig')
  const config = normalizeRouteStrategyConfigForWrite({
    hybridRoutingConfig: mode === 'hybrid_smart'
      ? (hasHybridRoutingConfigInput ? input.hybridRoutingConfig : current.hybridRoutingConfig)
      : (hasHybridRoutingConfigInput ? input.hybridRoutingConfig : undefined)
  }, mode)
  const next = {
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '策略路由名称'),
    description: hasOwnInput(input, 'description') ? normalizeNullableTextInput(input.description, '策略路由说明') : current.description,
    mode,
    status: hasOwnInput(input, 'status') ? normalizeRouteStrategyStatus(input.status, current.status) : current.status,
    configJson: routeStrategyConfigJson(config)
  }
  assertRouteStrategyNameAvailable(systemAccountId, next.name, id)
  const now = nowIso()
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const bindings = bindingInputs
      ? normalizeRouteStrategyGroupBindings(bindingInputs, systemAccountId)
      : routeStrategyGroupBindingWritesFromSummary(current.groupBindings)
    database
      .prepare(`
        UPDATE route_strategies
        SET name = ?, description = ?, mode = ?, status = ?, config_json = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `)
      .run(next.name, next.description ?? null, next.mode, next.status, next.configJson, now, id, systemAccountId)
    replaceRouteStrategyGroups(database, id, systemAccountId, next.mode, bindings, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    if (isDuplicateRouteStrategyNameError(error)) {
      throw new Error(`策略路由名称已存在：${next.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('route_strategy_updated')
  return findRouteStrategySummary(id, access)
}

export async function updateRouteStrategyAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<RouteStrategySummary | undefined> {
  assertKnownInputKeys(input, routeStrategyMutationInputKeys, '策略路由更新参数')
  const ownerSystemAccountId = await routeStrategySystemAccountIdAsync(id)
  if (!ownerSystemAccountId || !canManageApiKeyOwner(ownerSystemAccountId, access)) return undefined
  const current = await findRouteStrategySummaryAsync(id, { systemAccountId: ownerSystemAccountId, role: 'user' })
  if (!current) return undefined
  const mode = hasOwnInput(input, 'mode') ? normalizeRouteStrategyMode(input.mode) : current.mode
  const hasGroupBindingsInput = hasOwnInput(input, 'groupBindings')
  const bindingInputs = hasGroupBindingsInput ? routeStrategyGroupBindingInputsFromRequest(input) : undefined
  const hasHybridRoutingConfigInput = hasOwnInput(input, 'hybridRoutingConfig')
  const config = normalizeRouteStrategyConfigForWrite({
    hybridRoutingConfig: mode === 'hybrid_smart'
      ? (hasHybridRoutingConfigInput ? input.hybridRoutingConfig : current.hybridRoutingConfig)
      : (hasHybridRoutingConfigInput ? input.hybridRoutingConfig : undefined)
  }, mode)
  const next = {
    name: normalizeOptionalRequiredTextInput(input, 'name', current.name, '策略路由名称'),
    description: hasOwnInput(input, 'description') ? normalizeNullableTextInput(input.description, '策略路由说明') : current.description,
    mode,
    status: hasOwnInput(input, 'status') ? normalizeRouteStrategyStatus(input.status, current.status) : current.status,
    configJson: routeStrategyConfigJson(config)
  }
  const now = nowIso()
  const client = await getRouteStrategyDatabaseClient()
  try {
    await client.transaction(async (tx) => {
      const bindings = bindingInputs
        ? await normalizeRouteStrategyGroupBindingsAsync(bindingInputs, ownerSystemAccountId, tx, true)
        : routeStrategyGroupBindingWritesFromSummary(current.groupBindings)
      await assertRouteStrategyNameAvailableAsync(tx, ownerSystemAccountId, next.name, id)
      await tx.execute(`
        UPDATE ${routeStrategyTable(tx, 'route_strategies')}
        SET name = ?, description = ?, mode = ?, status = ?, config_json = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `, [next.name, next.description ?? null, next.mode, next.status, next.configJson, now, id, ownerSystemAccountId])
      await replaceRouteStrategyGroupsAsync(tx, id, ownerSystemAccountId, next.mode, bindings, now)
    })
  } catch (error) {
    if (isDuplicateRouteStrategyNameError(error)) {
      throw new Error(`策略路由名称已存在：${next.name}`)
    }
    throw error
  }
  notifyGatewayRuntimeCacheInvalidation('route_strategy_updated')
  return findRouteStrategySummaryAsync(id, access)
}

export function deleteRouteStrategy(id: string, access?: AccessScope): boolean {
  const systemAccountId = routeStrategySystemAccountId(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) return false
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  let deleted = false
  try {
    assertRouteStrategyNotDefault(database, id, systemAccountId)
    const count = routeStrategyApiKeyCount(id, systemAccountId)
    if (count > 0) {
      throw new Error(`策略路由已被 ${count} 个 API Key 使用，请先解绑`)
    }
    const result = database
      .prepare('DELETE FROM route_strategies WHERE id = ? AND system_account_id = ?')
      .run(id, systemAccountId)
    deleted = Number(result.changes ?? 0) > 0
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (deleted) notifyGatewayRuntimeCacheInvalidation('route_strategy_deleted')
  return deleted
}

export async function deleteRouteStrategyAsync(id: string, access?: AccessScope): Promise<boolean> {
  const systemAccountId = await routeStrategySystemAccountIdAsync(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) return false
  const client = await getRouteStrategyDatabaseClient()
  let deleted = false
  await client.transaction(async (tx) => {
    await lockRouteStrategyMutationRowAsync(tx, id, systemAccountId)
    await assertRouteStrategyNotDefaultAsync(tx, id, systemAccountId)
    const count = await routeStrategyApiKeyCountAsync(tx, id, systemAccountId)
    if (count > 0) {
      throw new Error(`策略路由已被 ${count} 个 API Key 使用，请先解绑`)
    }
    const result = await tx.execute(`
      DELETE FROM ${routeStrategyTable(tx, 'route_strategies')}
      WHERE id = ? AND system_account_id = ?
    `, [id, systemAccountId])
    deleted = Number(result.changes ?? 0) > 0
  })
  if (deleted) notifyGatewayRuntimeCacheInvalidation('route_strategy_deleted')
  return deleted
}

export function assertRouteStrategySelectableForApiKey(systemAccountId: string, routeStrategyId: unknown): string {
  const id = normalizeRouteStrategyIdInput(routeStrategyId)
  const row = getBusinessDatabase()
    .prepare(`
      SELECT id, status
      FROM route_strategies
      WHERE id = ? AND system_account_id = ?
      LIMIT 1
    `)
    .get(id, systemAccountId) as { id?: string; status?: string } | undefined
  if (!row?.id) {
    throw new Error('API Key 绑定的策略路由不存在或不属于当前用户')
  }
  if (row.status !== 'active') {
    throw new Error('API Key 只能绑定启用状态的策略路由')
  }
  return id
}

export async function assertRouteStrategySelectableForApiKeyAsync(systemAccountId: string, routeStrategyId: unknown, client?: DatabaseClient, lockRow = false): Promise<string> {
  const id = normalizeRouteStrategyIdInput(routeStrategyId)
  const db = client ?? await getRouteStrategyDatabaseClient()
  const lockClause = lockRow && db.driver === 'postgres' ? ' FOR UPDATE' : ''
  const row = await db.one<{ id?: string; status?: string }>(`
    SELECT id, status
    FROM ${routeStrategyTable(db, 'route_strategies')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1${lockClause}
  `, [id, systemAccountId])
  if (!row?.id) {
    throw new Error('API Key 绑定的策略路由不存在或不属于当前用户')
  }
  if (row.status !== 'active') {
    throw new Error('API Key 只能绑定启用状态的策略路由')
  }
  return id
}

export function loadRouteStrategyGroupBindingSummariesByRouteStrategyIds(routeStrategyIds: string[]): Map<string, RouteStrategyGroupBindingSummary[]> {
  const ids = [...new Set(routeStrategyIds.filter(Boolean))]
  const result = new Map<string, RouteStrategyGroupBindingSummary[]>()
  if (!ids.length) return result
  const database = getBusinessDatabase()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = database
      .prepare(routeStrategyGroupBindingRowsSql(`route_strategy_groups.route_strategy_id IN (${sqlPlaceholders(chunk.length)})`))
      .all(now, ...chunk) as unknown as RouteStrategyGroupBindingRow[]
    appendRouteStrategyBindingRows(result, rows)
  }
  return result
}

export async function loadRouteStrategyGroupBindingSummariesByRouteStrategyIdsAsync(routeStrategyIds: string[]): Promise<Map<string, RouteStrategyGroupBindingSummary[]>> {
  const ids = [...new Set(routeStrategyIds.filter(Boolean))]
  const result = new Map<string, RouteStrategyGroupBindingSummary[]>()
  if (!ids.length) return result
  const client = await getRouteStrategyDatabaseClient()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = await client.query<RouteStrategyGroupBindingRow>(routeStrategyGroupBindingRowsSqlForClient(client, `route_strategy_groups.route_strategy_id IN (${client.dialect.bindPlaceholders(chunk.length)})`), [now, ...chunk])
    appendRouteStrategyBindingRows(result, rows)
  }
  return result
}

function routeStrategySummariesFromRows(rows: RouteStrategyRow[], access?: AccessScope): RouteStrategySummary[] {
  const includeOwner = includeSystemAccountFields(access)
  const accountNames = includeOwner ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
  const bindingsByStrategyId = loadRouteStrategyGroupBindingSummariesByRouteStrategyIds(rows.map((row) => row.id))
  return rows.map((row) => routeStrategySummaryFromRow(row, bindingsByStrategyId.get(row.id) ?? [], includeOwner, accountNames))
}

async function routeStrategySummariesFromRowsAsync(rows: RouteStrategyRow[], access?: AccessScope, client?: DatabaseClient): Promise<RouteStrategySummary[]> {
  const includeOwner = includeSystemAccountFields(access)
  const lookupClient = includeOwner ? (client ?? await getRouteStrategyDatabaseClient()) : undefined
  const accountNames = includeOwner ? await loadSystemAccountNameMapByIdsAsync(lookupClient!, rows.map((row) => row.system_account_id)) : new Map<string, string>()
  const bindingsByStrategyId = await loadRouteStrategyGroupBindingSummariesByRouteStrategyIdsAsync(rows.map((row) => row.id))
  return rows.map((row) => routeStrategySummaryFromRow(row, bindingsByStrategyId.get(row.id) ?? [], includeOwner, accountNames))
}

function routeStrategySummaryFromRow(
  row: RouteStrategyRow,
  groupBindings: RouteStrategyGroupBindingSummary[],
  includeOwner: boolean,
  accountNames: Map<string, string>
): RouteStrategySummary {
  const mode = normalizeRouteStrategyMode(row.mode)
  const status = normalizeRouteStrategyStatus(row.status, 'active')
  const config = parseRouteStrategyRuntimeConfigJson(row.config_json)
  return {
    id: row.id,
    systemAccountId: includeOwner ? row.system_account_id : undefined,
    systemAccountName: includeOwner ? (row.system_account_name ?? accountNames.get(row.system_account_id)) : undefined,
    name: row.name,
    description: row.description ?? undefined,
    mode,
    status,
    isDefault: normalizeRouteStrategyDefaultFlag(row.is_default),
    hybridRoutingConfig: mode === 'hybrid_smart' ? config.hybridRoutingConfig : undefined,
    groupBindings,
    apiKeyCount: Number(row.api_key_count ?? 0),
    createdAt: row.created_at,
    updatedAt: row.updated_at
  }
}

function routeStrategyOptionsFromRows(rows: RouteStrategyRow[], access?: AccessScope): RouteStrategyOptionSummary[] {
  const includeOwner = includeSystemAccountFields(access)
  const accountNames = includeOwner ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: includeOwner ? row.system_account_id : undefined,
    systemAccountName: includeOwner ? accountNames.get(row.system_account_id) : undefined,
    name: row.name,
    mode: normalizeRouteStrategyMode(row.mode),
    status: normalizeRouteStrategyStatus(row.status, 'active'),
    isDefault: normalizeRouteStrategyDefaultFlag(row.is_default)
  }))
}

async function routeStrategyOptionsFromRowsAsync(rows: RouteStrategyRow[], access?: AccessScope, client?: DatabaseClient): Promise<RouteStrategyOptionSummary[]> {
  const includeOwner = includeSystemAccountFields(access)
  const lookupClient = includeOwner ? (client ?? await getRouteStrategyDatabaseClient()) : undefined
  const accountNames = includeOwner ? await loadSystemAccountNameMapByIdsAsync(lookupClient!, rows.map((row) => row.system_account_id)) : new Map<string, string>()
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: includeOwner ? row.system_account_id : undefined,
    systemAccountName: includeOwner ? accountNames.get(row.system_account_id) : undefined,
    name: row.name,
    mode: normalizeRouteStrategyMode(row.mode),
    status: normalizeRouteStrategyStatus(row.status, 'active'),
    isDefault: normalizeRouteStrategyDefaultFlag(row.is_default)
  }))
}

function normalizeRouteStrategyConfigForWrite(
  input: Partial<Record<'hybridRoutingConfig', unknown>>,
  mode: RouteStrategyMode
): { hybridRoutingConfig?: ApiKeyHybridRoutingConfig } {
  if (mode !== 'hybrid_smart') {
    if (input.hybridRoutingConfig !== undefined && input.hybridRoutingConfig !== null) {
      throw new Error('只有混合智能路由可以配置混合评分规则')
    }
    return {}
  }
  return {
    hybridRoutingConfig: normalizeHybridRoutingConfig(input.hybridRoutingConfig)
  }
}

function routeStrategyGroupBindingInputsFromRequest(input: Record<string, unknown>): RouteStrategyGroupBindingInput[] {
  if (!Array.isArray(input.groupBindings)) {
    throw new Error('策略路由至少需要绑定一个分组')
  }
  return input.groupBindings.map((item, index) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      throw new Error('策略路由分组绑定项无效')
    }
    const record = item as Record<string, unknown>
    return {
      groupId: requiredTextInput(record.groupId, '策略路由分组'),
      priority: normalizeRouteStrategyGroupBindingPriority(record.priority, index + 1),
      weight: normalizeApiKeyGroupBindingWeight(record.weight),
      status: normalizeRouteStrategyGroupBindingStatus(record.status)
    }
  })
}

function normalizeRouteStrategyGroupBindings(
  inputs: RouteStrategyGroupBindingInput[],
  systemAccountId: string
): RouteStrategyGroupBindingWrite[] {
  const normalized = normalizeRouteStrategyGroupBindingBasics(inputs)
  const groups = loadRouteStrategyBindableGroups(normalized.map((binding) => binding.groupId), systemAccountId)
  const result: RouteStrategyGroupBindingWrite[] = []
  for (const binding of normalized) {
    const group = groups.get(binding.groupId)
    const canBindNow = group ? Number(group.can_bind) === 1 : false
    if (!group || !canBindNow) {
      throw new Error(ROUTE_STRATEGY_GROUP_BOUNDARY_ERROR)
    }
    if (binding.status === 'active' && Number(group.enabled) === 0) {
      throw new Error(`策略路由不能启用已停用分组：${group.name ?? binding.groupId}`)
    }
    result.push(routeStrategyGroupBindingWriteFromGroup(binding, group))
  }
  return result.sort((left, right) => left.priority - right.priority || left.groupId.localeCompare(right.groupId))
}

async function normalizeRouteStrategyGroupBindingsAsync(
  inputs: RouteStrategyGroupBindingInput[],
  systemAccountId: string,
  client?: DatabaseClient,
  lockRows = false
): Promise<RouteStrategyGroupBindingWrite[]> {
  const normalized = normalizeRouteStrategyGroupBindingBasics(inputs)
  const groups = await loadRouteStrategyBindableGroupsAsync(normalized.map((binding) => binding.groupId), systemAccountId, client, lockRows)
  const result: RouteStrategyGroupBindingWrite[] = []
  for (const binding of normalized) {
    const group = groups.get(binding.groupId)
    const canBindNow = group ? Number(group.can_bind) === 1 : false
    if (!group || !canBindNow) {
      throw new Error(ROUTE_STRATEGY_GROUP_BOUNDARY_ERROR)
    }
    if (binding.status === 'active' && Number(group.enabled) === 0) {
      throw new Error(`策略路由不能启用已停用分组：${group.name ?? binding.groupId}`)
    }
    result.push(routeStrategyGroupBindingWriteFromGroup(binding, group))
  }
  return result.sort((left, right) => left.priority - right.priority || left.groupId.localeCompare(right.groupId))
}

function normalizeRouteStrategyGroupBindingBasics(inputs: RouteStrategyGroupBindingInput[]): RouteStrategyGroupBindingInput[] {
  if (!inputs.length) {
    throw new Error('策略路由至少需要绑定一个分组')
  }
  if (inputs.length > maxRouteStrategyGroupBindings) {
    throw new Error(`策略路由最多绑定 ${maxRouteStrategyGroupBindings} 个分组`)
  }
  const seenGroupIds = new Set<string>()
  const activePriorities = new Set<number>()
  const normalized: RouteStrategyGroupBindingInput[] = []
  for (const input of inputs) {
    const groupId = input.groupId.trim()
    if (!groupId) throw new Error('策略路由分组无效')
    if (seenGroupIds.has(groupId)) throw new Error('策略路由绑定分组不能重复')
    seenGroupIds.add(groupId)
    if (input.status === 'active') {
      if (activePriorities.has(input.priority)) throw new Error('策略路由启用分组优先级不能重复')
      activePriorities.add(input.priority)
    }
    normalized.push({ ...input, groupId })
  }
  if (!normalized.some((binding) => binding.status === 'active')) {
    throw new Error('策略路由至少需要一个启用分组')
  }
  return normalized
}

function routeStrategyGroupBindingWriteFromGroup(
  binding: RouteStrategyGroupBindingInput,
  group: RouteStrategyBindableGroupRow
): RouteStrategyGroupBindingWrite {
  return {
    ...binding,
    groupName: group.name ?? undefined,
    providerCode: group.provider_code,
    providerProtocolProfileId: group.provider_protocol_profile_id,
    protocolCode: group.protocol_code,
    protocolVersion: group.protocol_version,
    groupEnabled: Number(group.enabled) !== 0
  }
}

function validateRouteStrategyModeBindings(mode: RouteStrategyMode, bindings: RouteStrategyGroupBindingWrite[]): void {
  const activeBindings = bindings.filter((binding) => binding.status === 'active')
  if (mode === 'normal' && (bindings.length !== 1 || activeBindings.length !== 1)) {
    throw new Error('普通路由只能绑定一个启用分组')
  }
  if (mode === 'failover') {
    if (bindings.length < 2) {
      throw new Error('故障回退路由需要一个主用分组和至少一个备用分组')
    }
    if (bindings[0]?.status !== 'active') {
      throw new Error('故障回退路由的主用分组必须启用')
    }
    if (!bindings.slice(1).some((binding) => binding.status === 'active')) {
      throw new Error('故障回退路由至少需要一个启用备用分组')
    }
  }
}

function replaceRouteStrategyGroups(database: DatabaseSync, routeStrategyId: string, systemAccountId: string, mode: RouteStrategyMode, bindings: RouteStrategyGroupBindingWrite[], now: string): void {
  validateRouteStrategyModeBindings(mode, bindings)
  database.prepare('DELETE FROM route_strategy_groups WHERE route_strategy_id = ?').run(routeStrategyId)
  const statement = database.prepare(`
    INSERT INTO route_strategy_groups (id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const binding of bindings) {
    statement.run(newId('rsg'), routeStrategyId, systemAccountId, binding.groupId, binding.priority, binding.weight, binding.status, now, now)
  }
}

async function replaceRouteStrategyGroupsAsync(client: DatabaseClient, routeStrategyId: string, systemAccountId: string, mode: RouteStrategyMode, bindings: RouteStrategyGroupBindingWrite[], now: string): Promise<void> {
  validateRouteStrategyModeBindings(mode, bindings)
  await client.execute(`DELETE FROM ${routeStrategyTable(client, 'route_strategy_groups')} WHERE route_strategy_id = ?`, [routeStrategyId])
  for (const binding of bindings) {
    await client.execute(`
      INSERT INTO ${routeStrategyTable(client, 'route_strategy_groups')} (
        id, route_strategy_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [newId('rsg'), routeStrategyId, systemAccountId, binding.groupId, binding.priority, binding.weight, binding.status, now, now])
  }
}

function routeStrategyGroupBindingWritesFromSummary(bindings: RouteStrategyGroupBindingSummary[]): RouteStrategyGroupBindingWrite[] {
  return bindings.map((binding) => ({
    groupId: binding.groupId,
    groupName: binding.groupName,
    providerCode: binding.providerCode ?? '',
    providerProtocolProfileId: binding.providerProtocolProfileId ?? '',
    protocolCode: binding.protocolCode ?? '',
    protocolVersion: binding.protocolVersion ?? '',
    priority: binding.priority,
    weight: normalizeApiKeyGroupBindingWeight(binding.weight),
    status: binding.status,
    groupEnabled: binding.groupEnabled
  }))
}

function loadRouteStrategyBindableGroups(groupIds: string[], systemAccountId: string): Map<string, RouteStrategyBindableGroupRow> {
  const ids = [...new Set(groupIds.filter(Boolean))].sort((left, right) => left.localeCompare(right))
  const result = new Map<string, RouteStrategyBindableGroupRow>()
  if (!ids.length) return result
  const database = getBusinessDatabase()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = database
      .prepare(`
        SELECT
          groups.id,
          groups.system_account_id,
          groups.provider_code,
          groups.provider_protocol_profile_id,
          groups.protocol_code,
          groups.protocol_version,
          groups.name,
          CASE
            WHEN groups.system_account_id = ? THEN 1
            WHEN group_authorization.id IS NOT NULL THEN 1
            ELSE 0
          END AS can_bind,
          CASE
            WHEN groups.system_account_id = ? THEN groups.enabled
            WHEN group_authorization.id IS NOT NULL THEN CASE WHEN groups.enabled = 1 THEN COALESCE(group_authorization_settings.enabled, 1) ELSE 0 END
            ELSE 0
          END AS enabled
        FROM groups
        LEFT JOIN resource_authorizations group_authorization
          ON group_authorization.resource_type = 'group'
          AND group_authorization.resource_id = groups.id
          AND group_authorization.grantee_system_account_id = ?
          AND group_authorization.status = 'active'
          AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
        LEFT JOIN group_authorization_settings
          ON group_authorization_settings.authorization_id = group_authorization.id
          AND group_authorization_settings.system_account_id = ?
          AND group_authorization_settings.group_id = groups.id
        WHERE groups.id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(systemAccountId, systemAccountId, systemAccountId, now, systemAccountId, ...chunk) as unknown as RouteStrategyBindableGroupRow[]
    for (const row of rows) result.set(row.id, row)
  }
  return result
}

async function loadRouteStrategyBindableGroupsAsync(groupIds: string[], systemAccountId: string, client?: DatabaseClient, lockRows = false): Promise<Map<string, RouteStrategyBindableGroupRow>> {
  const ids = [...new Set(groupIds.filter(Boolean))].sort((left, right) => left.localeCompare(right))
  const result = new Map<string, RouteStrategyBindableGroupRow>()
  if (!ids.length) return result
  const db = client ?? await getRouteStrategyDatabaseClient()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 500)) {
    const lockClause = lockRows && db.driver === 'postgres' ? ' FOR UPDATE OF groups' : ''
    const rows = await db.query<RouteStrategyBindableGroupRow>(`
      SELECT
        groups.id,
        groups.system_account_id,
        groups.provider_code,
        groups.provider_protocol_profile_id,
        groups.protocol_code,
        groups.protocol_version,
        groups.name,
        CASE
          WHEN groups.system_account_id = ? THEN 1
          WHEN group_authorization.id IS NOT NULL THEN 1
          ELSE 0
        END AS can_bind,
        CASE
          WHEN groups.system_account_id = ? THEN groups.enabled
          WHEN group_authorization.id IS NOT NULL THEN CASE WHEN groups.enabled = 1 THEN COALESCE(group_authorization_settings.enabled, 1) ELSE 0 END
          ELSE 0
        END AS enabled
      FROM ${routeStrategyTable(db, 'groups')} groups
      LEFT JOIN ${routeStrategyTable(db, 'resource_authorizations')} group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = groups.id
        AND group_authorization.grantee_system_account_id = ?
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      LEFT JOIN ${routeStrategyTable(db, 'group_authorization_settings')} group_authorization_settings
        ON group_authorization_settings.authorization_id = group_authorization.id
        AND group_authorization_settings.system_account_id = ?
        AND group_authorization_settings.group_id = groups.id
      WHERE groups.id IN (${db.dialect.bindPlaceholders(chunk.length)})${lockClause}
    `, [systemAccountId, systemAccountId, systemAccountId, now, systemAccountId, ...chunk])
    for (const row of rows) result.set(row.id, row)
  }
  return result
}

function appendRouteStrategyBindingRows(result: Map<string, RouteStrategyGroupBindingSummary[]>, rows: RouteStrategyGroupBindingRow[]): void {
  for (const row of rows) {
    if (!Number.isInteger(row.priority) || row.priority <= 0) throw new Error(`策略路由分组绑定优先级无效：${row.id}`)
    if (row.status !== 'active' && row.status !== 'disabled') throw new Error(`策略路由分组绑定状态无效：${row.id}`)
    if (Number(row.group_enabled) !== 0 && Number(row.group_enabled) !== 1) throw new Error(`策略路由分组绑定关联分组状态无效：${row.id}`)
    const item: RouteStrategyGroupBindingSummary = {
      id: row.id,
      groupId: row.group_id,
      groupName: row.group_name ?? undefined,
      providerCode: row.provider_code ?? undefined,
      providerProtocolProfileId: row.provider_protocol_profile_id ?? undefined,
      protocolCode: row.protocol_code ?? undefined,
      protocolVersion: row.protocol_version ?? undefined,
      priority: row.priority,
      weight: normalizeApiKeyGroupBindingWeight(row.weight),
      status: row.status,
      groupEnabled: Number(row.group_enabled) === 1
    }
    const existing = result.get(row.route_strategy_id) ?? []
    existing.push(item)
    result.set(row.route_strategy_id, existing)
  }
}

function routeStrategyGroupBindingRowsSql(whereClause: string): string {
  return `
    SELECT
      route_strategy_groups.id,
      route_strategy_groups.route_strategy_id,
      route_strategy_groups.system_account_id,
      route_strategy_groups.group_id,
      route_strategy_groups.priority,
      route_strategy_groups.weight,
      route_strategy_groups.status,
      groups.name AS group_name,
      groups.provider_code,
      groups.provider_protocol_profile_id,
      groups.protocol_code,
      groups.protocol_version,
      CASE
        WHEN groups.id IS NULL THEN 0
        WHEN groups.system_account_id = route_strategy_groups.system_account_id THEN groups.enabled
        WHEN group_authorization.id IS NOT NULL THEN CASE WHEN groups.enabled = 1 THEN COALESCE(group_authorization_settings.enabled, 1) ELSE 0 END
        ELSE 0
      END AS group_enabled
    FROM route_strategy_groups
    LEFT JOIN groups ON groups.id = route_strategy_groups.group_id
    LEFT JOIN resource_authorizations group_authorization
      ON group_authorization.resource_type = 'group'
      AND group_authorization.resource_id = groups.id
      AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
      AND group_authorization.status = 'active'
      AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
    LEFT JOIN group_authorization_settings
      ON group_authorization_settings.authorization_id = group_authorization.id
      AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
      AND group_authorization_settings.group_id = groups.id
    WHERE ${whereClause}
    ORDER BY route_strategy_groups.route_strategy_id ASC,
      CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
      route_strategy_groups.priority ASC,
      route_strategy_groups.created_at ASC,
      route_strategy_groups.id ASC
  `
}

function routeStrategyGroupBindingRowsSqlForClient(client: DatabaseClient, whereClause: string): string {
  return `
    SELECT
      route_strategy_groups.id,
      route_strategy_groups.route_strategy_id,
      route_strategy_groups.system_account_id,
      route_strategy_groups.group_id,
      route_strategy_groups.priority,
      route_strategy_groups.weight,
      route_strategy_groups.status,
      groups.name AS group_name,
      groups.provider_code,
      groups.provider_protocol_profile_id,
      groups.protocol_code,
      groups.protocol_version,
      CASE
        WHEN groups.id IS NULL THEN 0
        WHEN groups.system_account_id = route_strategy_groups.system_account_id THEN groups.enabled
        WHEN group_authorization.id IS NOT NULL THEN CASE WHEN groups.enabled = 1 THEN COALESCE(group_authorization_settings.enabled, 1) ELSE 0 END
        ELSE 0
      END AS group_enabled
    FROM ${routeStrategyTable(client, 'route_strategy_groups')} route_strategy_groups
    LEFT JOIN ${routeStrategyTable(client, 'groups')} groups ON groups.id = route_strategy_groups.group_id
    LEFT JOIN ${routeStrategyTable(client, 'resource_authorizations')} group_authorization
      ON group_authorization.resource_type = 'group'
      AND group_authorization.resource_id = groups.id
      AND group_authorization.grantee_system_account_id = route_strategy_groups.system_account_id
      AND group_authorization.status = 'active'
      AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
    LEFT JOIN ${routeStrategyTable(client, 'group_authorization_settings')} group_authorization_settings
      ON group_authorization_settings.authorization_id = group_authorization.id
      AND group_authorization_settings.system_account_id = route_strategy_groups.system_account_id
      AND group_authorization_settings.group_id = groups.id
    WHERE ${whereClause}
    ORDER BY route_strategy_groups.route_strategy_id ASC,
      CASE WHEN route_strategy_groups.status = 'active' THEN 0 ELSE 1 END ASC,
      route_strategy_groups.priority ASC,
      route_strategy_groups.created_at ASC,
      route_strategy_groups.id ASC
  `
}

function routeStrategyListColumns(): string {
  return [
    'route_strategies.id',
    'route_strategies.system_account_id',
    'system_accounts.display_name AS system_account_name',
    'route_strategies.name',
    'route_strategies.description',
    'route_strategies.mode',
    'route_strategies.status',
    'route_strategies.is_default',
    'route_strategies.config_json',
    '(SELECT COUNT(1) FROM api_keys WHERE api_keys.route_strategy_id = route_strategies.id AND api_keys.system_account_id = route_strategies.system_account_id) AS api_key_count',
    'route_strategies.created_at',
    'route_strategies.updated_at'
  ].join(', ')
}

function routeStrategyListColumnsForClient(client: DatabaseClient): string {
  return [
    'route_strategies.id',
    'route_strategies.system_account_id',
    'system_accounts.display_name AS system_account_name',
    'route_strategies.name',
    'route_strategies.description',
    'route_strategies.mode',
    'route_strategies.status',
    'route_strategies.is_default',
    'route_strategies.config_json',
    `(SELECT COUNT(1) FROM ${routeStrategyTable(client, 'api_keys')} api_keys WHERE api_keys.route_strategy_id = route_strategies.id AND api_keys.system_account_id = route_strategies.system_account_id) AS api_key_count`,
    'route_strategies.created_at',
    'route_strategies.updated_at'
  ].join(', ')
}

function normalizeRouteStrategyListOptions(options?: RouteStrategyListOptions): Required<Pick<RouteStrategyListOptions, 'page' | 'pageSize'>> & Pick<RouteStrategyListOptions, 'keyword' | 'mode' | 'status'> {
  const pageSize = typeof options?.pageSize === 'number' && Number.isInteger(options.pageSize)
    ? Math.min(200, Math.max(1, options.pageSize))
    : 50
  const rawPage = typeof options?.page === 'number' && Number.isInteger(options.page) ? options.page : 1
  return {
    page: Math.max(1, rawPage),
    pageSize,
    keyword: textFilter(options?.keyword),
    mode: normalizeRouteStrategyListMode(options?.mode),
    status: normalizeRouteStrategyListStatus(options?.status)
  }
}

function normalizeRouteStrategyOptionListOptions(options?: RouteStrategyOptionListOptions): Required<Pick<RouteStrategyOptionListOptions, 'limit'>> & Pick<RouteStrategyOptionListOptions, 'ids' | 'keyword' | 'activeOnly'> {
  return {
    ids: [...new Set((options?.ids ?? []).map((id) => id.trim()).filter(Boolean))].slice(0, 50),
    keyword: textFilter(options?.keyword),
    activeOnly: options?.activeOnly !== false,
    limit: typeof options?.limit === 'number' && Number.isInteger(options.limit) ? Math.min(100, Math.max(1, options.limit)) : 50
  }
}

function buildRouteStrategyFilters(scope: { clause: string; params: string[] }, options: ReturnType<typeof normalizeRouteStrategyListOptions>): { clause: string; params: Array<string | number> } {
  const clauses: string[] = []
  const params: Array<string | number> = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ WHERE /, ''))
    params.push(...scope.params)
  }
  if (options.keyword) {
    const keywordPrefix = `${escapeLikePrefix(options.keyword)}%`
    clauses.push("(route_strategies.name COLLATE NOCASE = ? OR route_strategies.name LIKE ? ESCAPE '\\')")
    params.push(options.keyword, keywordPrefix)
  }
  if (options.mode) {
    clauses.push('route_strategies.mode = ?')
    params.push(options.mode)
  }
  if (options.status) {
    clauses.push('route_strategies.status = ?')
    params.push(options.status)
  }
  return { clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '', params }
}

function buildRouteStrategyFiltersForClient(client: DatabaseClient, scope: { clause: string; params: string[] }, options: ReturnType<typeof normalizeRouteStrategyListOptions>): { clause: string; params: Array<string | number> } {
  if (client.driver === 'sqlite') return buildRouteStrategyFilters(scope, options)
  const filters = buildRouteStrategyFilters(scope, options)
  return {
    clause: filters.clause.replace('COLLATE NOCASE = ?', '= ?').replace('route_strategies.name LIKE ?', 'route_strategies.name ILIKE ?'),
    params: filters.params
  }
}

function buildRouteStrategyOptionFilters(scope: { clause: string; params: string[] }, options: ReturnType<typeof normalizeRouteStrategyOptionListOptions>): { clause: string; params: Array<string | number> } {
  const clauses: string[] = []
  const params: Array<string | number> = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ WHERE /, ''))
    params.push(...scope.params)
  }
  if (options.ids?.length) {
    clauses.push(`route_strategies.id IN (${sqlPlaceholders(options.ids.length)})`)
    params.push(...options.ids)
  }
  if (options.keyword) {
    const keywordPrefix = `${escapeLikePrefix(options.keyword)}%`
    clauses.push("(route_strategies.name COLLATE NOCASE = ? OR route_strategies.name LIKE ? ESCAPE '\\')")
    params.push(options.keyword, keywordPrefix)
  }
  if (options.activeOnly) {
    clauses.push("route_strategies.status = 'active'")
  }
  return { clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '', params }
}

function buildRouteStrategyOptionFiltersForClient(client: DatabaseClient, scope: { clause: string; params: string[] }, options: ReturnType<typeof normalizeRouteStrategyOptionListOptions>): { clause: string; params: Array<string | number> } {
  if (client.driver === 'sqlite') return buildRouteStrategyOptionFilters(scope, options)
  const filters = buildRouteStrategyOptionFilters(scope, options)
  return {
    clause: filters.clause
      .replace('COLLATE NOCASE = ?', '= ?')
      .replace('route_strategies.name LIKE ?', 'route_strategies.name ILIKE ?'),
    params: filters.params
  }
}

function normalizeRouteStrategyListMode(value: unknown): RouteStrategyMode | undefined {
  if (!value || value === 'all') return undefined
  return normalizeRouteStrategyMode(value)
}

function normalizeRouteStrategyListStatus(value: unknown): RouteStrategyStatus | undefined {
  if (!value || value === 'all') return undefined
  return normalizeRouteStrategyStatus(value, 'active')
}

function normalizeRouteStrategyStatus(value: unknown, fallback: RouteStrategyStatus): RouteStrategyStatus {
  if (value === undefined || value === null || value === '') return fallback
  if (value === 'active' || value === 'disabled') return value
  throw new Error('策略路由状态无效')
}

function normalizeRouteStrategyDefaultFlag(value: unknown): boolean {
  return value === true || value === 1 || value === '1'
}

function normalizeRouteStrategyIdInput(value: unknown): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error('API Key 必须绑定策略路由')
  }
  return value.trim()
}

function normalizeRouteStrategyGroupBindingPriority(value: unknown, fallback: number): number {
  if (value === undefined || value === null || value === '') return fallback
  if (typeof value !== 'number' || !Number.isInteger(value) || value <= 0) {
    throw new Error('策略路由分组优先级必须是大于 0 的整数')
  }
  return value
}

function normalizeRouteStrategyGroupBindingStatus(value: unknown): RouteStrategyGroupBindingStatus {
  if (value === undefined || value === null || value === '') return 'active'
  if (value === 'active' || value === 'disabled') return value
  throw new Error('策略路由分组绑定状态无效')
}

function routeStrategySystemAccountId(id: string): string | undefined {
  const row = getBusinessDatabase().prepare('SELECT system_account_id FROM route_strategies WHERE id = ?').get(id) as { system_account_id?: string } | undefined
  return row?.system_account_id
}

async function routeStrategySystemAccountIdAsync(id: string): Promise<string | undefined> {
  const client = await getRouteStrategyDatabaseClient()
  const row = await client.one<{ system_account_id?: string }>(`
    SELECT system_account_id
    FROM ${routeStrategyTable(client, 'route_strategies')}
    WHERE id = ?
  `, [id])
  return row?.system_account_id
}

function routeStrategyApiKeyCount(routeStrategyId: string, systemAccountId: string): number {
  const row = getBusinessDatabase()
    .prepare('SELECT COUNT(1) AS count FROM api_keys WHERE route_strategy_id = ? AND system_account_id = ?')
    .get(routeStrategyId, systemAccountId) as { count?: number } | undefined
  return Number(row?.count ?? 0)
}

function assertRouteStrategyNotDefault(database: DatabaseSync, routeStrategyId: string, systemAccountId: string): void {
  const row = database
    .prepare('SELECT is_default FROM route_strategies WHERE id = ? AND system_account_id = ? LIMIT 1')
    .get(routeStrategyId, systemAccountId) as { is_default?: unknown } | undefined
  if (normalizeRouteStrategyDefaultFlag(row?.is_default)) {
    throw new Error('默认策略路由不允许删除')
  }
}

async function assertRouteStrategyNotDefaultAsync(client: DatabaseClient, routeStrategyId: string, systemAccountId: string): Promise<void> {
  const row = await client.one<{ is_default?: unknown }>(`
    SELECT is_default
    FROM ${routeStrategyTable(client, 'route_strategies')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1
  `, [routeStrategyId, systemAccountId])
  if (normalizeRouteStrategyDefaultFlag(row?.is_default)) {
    throw new Error('默认策略路由不允许删除')
  }
}

async function routeStrategyApiKeyCountAsync(client: DatabaseClient, routeStrategyId: string, systemAccountId: string): Promise<number> {
  const row = await client.one<{ count?: number | string }>(`
    SELECT COUNT(1) AS count
    FROM ${routeStrategyTable(client, 'api_keys')}
    WHERE route_strategy_id = ? AND system_account_id = ?
  `, [routeStrategyId, systemAccountId])
  return Number(row?.count ?? 0)
}

async function lockRouteStrategyMutationRowAsync(client: DatabaseClient, routeStrategyId: string, systemAccountId: string): Promise<void> {
  const lockClause = client.driver === 'postgres' ? ' FOR UPDATE' : ''
  await client.one<{ id?: string }>(`
    SELECT id
    FROM ${routeStrategyTable(client, 'route_strategies')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1${lockClause}
  `, [routeStrategyId, systemAccountId])
}

function assertRouteStrategyNameAvailable(systemAccountId: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) params.push(excludeId)
  const row = getBusinessDatabase()
    .prepare(`SELECT id FROM route_strategies WHERE system_account_id = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) throw new Error(`策略路由名称已存在：${name}`)
}

async function assertRouteStrategyNameAvailableAsync(client: DatabaseClient, systemAccountId: string, name: string, excludeId?: string): Promise<void> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${routeStrategyTable(client, 'route_strategies')}
    WHERE system_account_id = ? AND lower(name) = lower(?) AND id <> ?
    LIMIT 1
  `, [systemAccountId, name, excludeId ?? ''])
  if (row?.id) throw new Error(`策略路由名称已存在：${name}`)
}

async function getRouteStrategyDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function routeStrategyTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function textFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

function isDuplicateRouteStrategyNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_route_strategies_owner_name_unique_lower')
}
