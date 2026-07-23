import type { ApiKeySummary, RequestQuotaLimits } from '../domain/types.js'
import { HYBRID_PROVIDER_CODE } from '../domain/provider-protocol.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyApiKeyQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { apiKeySystemAccountId, canManageApiKeyOwner } from './api-key-access.js'
import {
  apiKeyAvailabilityScheduleFromRequest,
  apiKeyAvailabilityScheduleJson,
  apiKeyAvailabilityScheduleStatus,
  isApiKeyAvailabilityScheduleInputPresent,
  nextApiKeyAvailabilityScheduleCheckAt
} from './api-key-availability-schedule.js'
import { buildApiKeyFilters, normalizeApiKeyListOptions } from './api-key-list-query.js'
import { apiKeyListItemsFromRows, apiKeyListItemsFromRowsAsync, apiKeySummariesFromRows, apiKeySummariesFromRowsAsync, type ApiKeyRow } from './api-key-mappers.js'
import { registerDeletedApiKeyRecordCleanupTargetInClientAsync } from './api-key-record-cleanup.js'
import { createApiKey, encryptJson, hashSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { invalidateGatewayApiKeyCacheById, invalidateGatewayApiKeyCacheByIdAsync } from './gateway-api-key.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound, takePageRows } from './query-utils.js'
import { invalidateApiKeyLookupCache, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { rememberRequestQuotaHourlyWindowsFromJson } from './request-quota-hourly-windows.repository.js'
import { emptyRequestQuotaLimits, normalizeRequestQuotaLimits, requestQuotaLimitsJson } from './request-quota-limits.js'
import {
  assertRouteStrategySelectableForApiKey,
  assertRouteStrategySelectableForApiKeyAsync,
  ensureDefaultRouteStrategiesForSystemAccount,
  ensureDefaultRouteStrategiesForSystemAccountAsync
} from './route-strategy.repository.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { optionalServerDateTimeIso } from './value-utils.js'
import { loadApiKeyUsageSummariesForScopesAsync } from './usage-summary-loaders.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'

const businessSchemaName = 'juhe_business'
const apiKeyMutationInputKeys = new Set([
  'name',
  'description',
  'routeStrategyId',
  'status',
  'expiresAt',
  'quotaLimits',
  'availabilitySchedule'
])

export interface ApiKeyListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  routeStrategyId?: string
}

export interface ApiKeyListResult {
  items: Array<Omit<ApiKeySummary, 'usage'>>
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface ApiKeyUsageResult {
  items: Array<{ id: string; usage: ApiKeySummary['usage'] }>
}

type ApiKeyDeleteRow = {
  id: string
  system_account_id: string
  is_default?: number | string | boolean | null
}

export function listApiKeys(access?: AccessScope, options?: ApiKeyListOptions): ApiKeySummary[] {
  return queryApiKeys(access, options).items
}

export async function listApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions): Promise<ApiKeySummary[]> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_api_keys_read_only',
        access,
        options
      })
    }
    return listApiKeysReadOnly(access, options)
  }
  return (await queryApiKeysAsync(access, options)).items
}

export function listApiKeysReadOnly(access?: AccessScope, options?: ApiKeyListOptions): ApiKeySummary[] {
  return queryApiKeys(access, options).items
}

export function listApiKeysPage(access?: AccessScope, options?: ApiKeyListOptions): ApiKeyListResult {
  return queryApiKeys(access, options, true)
}

export async function listApiKeysPageAsync(access?: AccessScope, options?: ApiKeyListOptions): Promise<ApiKeyListResult> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'list_api_keys_page_read_only',
        access,
        options
      })
    }
    return listApiKeysPageReadOnly(access, options)
  }
  return queryApiKeysAsync(access, options, true)
}

export function listApiKeysPageReadOnly(access?: AccessScope, options?: ApiKeyListOptions): ApiKeyListResult {
  return queryApiKeys(access, options, true)
}

export async function getApiKeyUsageByIdsAsync(ids: string[], access?: AccessScope): Promise<ApiKeyUsageResult> {
  const normalizedIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (normalizedIds.length < 1 || normalizedIds.length > 100) throw new Error('API Key 用量 ids 数量必须为 1 到 100')
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const client = await getApiKeyDatabaseClient()
  const rows = await client.query<{ id: string; system_account_id: string }>(`
    SELECT id, system_account_id
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    WHERE api_keys.id IN (${client.dialect.bindPlaceholders(normalizedIds.length)})${scope.clause}
  `, [...normalizedIds, ...scope.params])
  const usage = await loadApiKeyUsageSummariesForScopesAsync(rows.map((row) => ({
    rowKey: row.id,
    systemAccountId: row.system_account_id,
    scopeId: row.id
  })))
  return {
    items: rows.map((row) => ({ id: row.id, usage: usage.get(row.id) ?? emptyAccountUsageSummary() }))
  }
}

export function findApiKeySummary(id: string, access?: AccessScope): ApiKeySummary | undefined {
  return findApiKeySummaryReadOnly(id, access)
}

export function findApiKeySummaryReadOnly(id: string, access?: AccessScope): ApiKeySummary | undefined {
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns()} FROM api_keys ${apiKeyListJoins()} WHERE api_keys.id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeyRow | undefined
  return row ? apiKeySummariesFromRows([row], access, { includeSecret: false })[0] : undefined
}

export async function findApiKeySummaryAsync(id: string, access?: AccessScope): Promise<ApiKeySummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'find_api_key_summary_read_only',
        id,
        access
      })
    }
    return findApiKeySummaryReadOnly(id, access)
  }
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = await client.one<ApiKeyRow>(`
    SELECT ${apiKeyListColumns()}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    ${apiKeyListJoinsForClient(client)}
    WHERE api_keys.id = ?${scope.clause}
  `, [id, ...scope.params])
  return row ? (await apiKeySummariesFromRowsAsync([row], access, { includeSecret: false }))[0] : undefined
}

export function findApiKeySecret(id: string, access?: AccessScope): ApiKeySummary | undefined {
  return findApiKeySecretReadOnly(id, access)
}

export function findApiKeySecretReadOnly(id: string, access?: AccessScope): ApiKeySummary | undefined {
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns({ includeSecret: true })} FROM api_keys ${apiKeyListJoins()} WHERE api_keys.id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeyRow | undefined
  return row ? apiKeySummariesFromRows([row], access, { includeSecret: true })[0] : undefined
}

export async function findApiKeySecretAsync(id: string, access?: AccessScope): Promise<ApiKeySummary | undefined> {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    if (sqliteReadWorkerPoolEnabled()) {
      return requestSqliteReadWorker({
        type: 'find_api_key_secret_read_only',
        id,
        access
      })
    }
    return findApiKeySecretReadOnly(id, access)
  }
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = await client.one<ApiKeyRow>(`
    SELECT ${apiKeyListColumns({ includeSecret: true })}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    ${apiKeyListJoinsForClient(client)}
    WHERE api_keys.id = ?${scope.clause}
  `, [id, ...scope.params])
  return row ? (await apiKeySummariesFromRowsAsync([row], access, { includeSecret: true }))[0] : undefined
}

function queryApiKeys(access: AccessScope | undefined, options: ApiKeyListOptions | undefined, paged: true): ApiKeyListResult
function queryApiKeys(access?: AccessScope, options?: ApiKeyListOptions, paged?: false): Omit<ApiKeyListResult, 'items'> & { items: ApiKeySummary[] }
function queryApiKeys(access?: AccessScope, options?: ApiKeyListOptions, paged = false): ApiKeyListResult | (Omit<ApiKeyListResult, 'items'> & { items: ApiKeySummary[] }) {
  const normalized = normalizeApiKeyListOptions(options)
  const scope = buildSystemAccountWhereClause(access, 'api_keys.system_account_id')
  const filters = buildApiKeyFilters(scope, normalized)
  const limitClause = paged ? 'LIMIT ? OFFSET ?' : ''
  const limitParams = paged ? [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize] : []
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${apiKeyListColumns()}
      FROM api_keys
      ${apiKeyListJoins()}
      ${filters.clause}
      ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
      ${limitClause}
    `)
    .all(...filters.params, ...limitParams) as unknown as ApiKeyRow[]
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = paged
    ? apiKeyListItemsFromRows(pageRows.rows, access)
    : apiKeySummariesFromRows(pageRows.rows, access, { includeSecret: false })
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

function queryApiKeysAsync(access: AccessScope | undefined, options: ApiKeyListOptions | undefined, paged: true): Promise<ApiKeyListResult>
function queryApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions, paged?: false): Promise<Omit<ApiKeyListResult, 'items'> & { items: ApiKeySummary[] }>
async function queryApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions, paged = false): Promise<ApiKeyListResult | (Omit<ApiKeyListResult, 'items'> & { items: ApiKeySummary[] })> {
  const normalized = normalizeApiKeyListOptions(options)
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountWhereClause(access, 'api_keys.system_account_id')
  const keywordCte = client.driver === 'postgres' && normalized.keyword
    ? buildPostgresApiKeyKeywordCte(client, access, normalized.keyword)
    : undefined
  const filterOptions = keywordCte ? { ...normalized, keyword: undefined } : normalized
  const filters = buildApiKeyFiltersForClient(client, scope, filterOptions)
  const filterClauses: string[] = []
  if (filters.clause) {
    filterClauses.push(filters.clause.replace(/^\s*WHERE\s+/i, ''))
  }
  if (keywordCte) {
    filterClauses.push('api_keys.id IN (SELECT id FROM matched_api_key_ids)')
  }
  const whereClause = filterClauses.length ? `WHERE ${filterClauses.join(' AND ')}` : ''
  const limitClause = paged ? 'LIMIT ? OFFSET ?' : ''
  const limitParams = paged ? [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize] : []
  const cteParts = [
    keywordCte?.sql.replace(/^\s*WITH\s+/i, ''),
    `page_api_key_ids AS MATERIALIZED (
      SELECT api_keys.id
      FROM ${apiKeyTable(client, 'api_keys')} api_keys
      ${whereClause}
      ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
      ${limitClause}
    )`
  ].filter((part): part is string => Boolean(part))
  const rows = await client.query<ApiKeyRow>(`
    WITH ${cteParts.join(', ')}
    SELECT ${apiKeyListColumns()}
    FROM page_api_key_ids
    INNER JOIN ${apiKeyTable(client, 'api_keys')} api_keys
      ON api_keys.id = page_api_key_ids.id
    ${apiKeyListJoinsForClient(client)}
    ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
  `, [...(keywordCte?.params ?? []), ...filters.params, ...limitParams])
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = paged
    ? await apiKeyListItemsFromRowsAsync(pageRows.rows, access)
    : await apiKeySummariesFromRowsAsync(pageRows.rows, access, { includeSecret: false })
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

function buildPostgresApiKeyKeywordCte(
  client: DatabaseClient,
  access: AccessScope | undefined,
  keyword: string
): { sql: string; params: string[] } {
  const scope = buildSystemAccountWhereClause(access, 'keyword_api_keys.system_account_id')
  const clauses: string[] = []
  const params: string[] = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^\s*WHERE\s+/i, ''))
    params.push(...scope.params)
  }
  clauses.push(`keyword_api_keys.name COLLATE "C" >= ?
    AND keyword_api_keys.name COLLATE "C" < ?
    AND starts_with(keyword_api_keys.name, ?)`)
  params.push(keyword, apiKeyTextPrefixUpperBound(keyword), keyword)
  return {
    sql: `WITH matched_api_key_ids AS MATERIALIZED (
      SELECT keyword_api_keys.id
      FROM ${apiKeyTable(client, 'api_keys')} keyword_api_keys
      WHERE ${clauses.join(' AND ')}
    )`,
    params
  }
}

function apiKeyListColumns(options: { includeSecret?: boolean } = {}): string {
  const columns = [
    'api_keys.id',
    'api_keys.system_account_id',
    'system_accounts.display_name AS system_account_name',
    'api_keys.route_strategy_id',
    'route_strategies.name AS route_strategy_name',
    'route_strategies.mode AS route_strategy_mode',
    'route_strategies.status AS route_strategy_status',
    'api_keys.name',
    'api_keys.description',
    'api_keys.key_prefix',
    'api_keys.key_suffix',
    'api_keys.status',
    'api_keys.is_default',
    'api_keys.expires_at',
    'api_keys.quota_limits_json',
    'api_keys.availability_schedule_json'
  ]
  if (options.includeSecret) {
    columns.splice(10, 0, 'api_keys.key_secret_encrypted')
  }
  return columns.join(', ')
}

function apiKeyListJoins(): string {
  return `
    LEFT JOIN system_accounts ON system_accounts.id = api_keys.system_account_id
    INNER JOIN route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
  `
}

function apiKeyListJoinsForClient(client: DatabaseClient): string {
  return `
    LEFT JOIN ${apiKeyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = api_keys.system_account_id
    INNER JOIN ${apiKeyTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
  `
}

export function createApiKeyRecord(input: Record<string, unknown>, access?: AccessScope): ApiKeySummary & { key: string } {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 创建参数')
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  const name = normalizedApiKeyName(input.name)
  let routeStrategyId = assertRouteStrategySelectableForApiKey(systemAccountId, input.routeStrategyId)
  const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits)
  const availabilitySchedule = apiKeyAvailabilityScheduleFromRequest(input)
  const status = apiKeyStatusForScheduleMutation({
    requestedStatus: normalizeApiKeyStatus(input.status, 'active'),
    schedule: availabilitySchedule,
    now: nowDate
  })
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name,
    description: normalizeOptionalApiKeyDescription(input.description),
    keyPrefix,
    keySuffix,
    status,
    routeStrategyId,
    expiresAt: normalizeOptionalApiKeyExpiresAt(input.expiresAt),
    quotaLimits,
    availabilitySchedule,
    isDefault: false,
    usage: {
      requestCount: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheReadCost: 0,
      cacheWriteTokens: 0,
      cacheWrite1hTokens: 0,
      cacheWriteCost: 0,
      thinkingTokens: 0,
      inputImageTokens: 0,
      outputImageTokens: 0,
      totalTokens: 0,
      totalCost: 0
    },
    key
  }
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    routeStrategyId = assertRouteStrategySelectableForApiKey(systemAccountId, routeStrategyId)
    const quotaLimitsJson = requestQuotaLimitsJson(record.quotaLimits)
    const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(record.availabilitySchedule, nowDate)
    database
      .prepare(`
        INSERT INTO api_keys (
          id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
          key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
          availability_schedule_next_check_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)
      `)
      .run(
        record.id,
        systemAccountId,
        routeStrategyId,
        record.name,
        record.description ?? null,
        hashSecret(key),
        record.keyPrefix,
        record.keySuffix,
        encryptJson({ key }),
        record.status,
        record.expiresAt ?? null,
        quotaLimitsJson,
        apiKeyAvailabilityScheduleJson(record.availabilitySchedule),
        availabilityScheduleNextCheckAt,
        now,
        now
      )
    rememberRequestQuotaHourlyWindowsFromJson(quotaLimitsJson, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${record.name}`)
    }
    throw error
  }
  invalidateApiKeyLookupCache(record.id)
  notifyGatewayRuntimeCacheInvalidation('api_key_created')
  notifyApiKeyQuotaCacheInvalidation(record.id, 'api_key_created')
  return {
    ...(findApiKeySummary(record.id, access) ?? record),
    key
  }
}

export async function createApiKeyRecordAsync(input: Record<string, unknown>, access?: AccessScope): Promise<ApiKeySummary & { key: string }> {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 创建参数')
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  const name = normalizedApiKeyName(input.name)
  const client = await getApiKeyDatabaseClient()
  let routeStrategyId = await assertRouteStrategySelectableForApiKeyAsync(systemAccountId, input.routeStrategyId)
  const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits)
  const availabilitySchedule = apiKeyAvailabilityScheduleFromRequest(input)
  const status = apiKeyStatusForScheduleMutation({
    requestedStatus: normalizeApiKeyStatus(input.status, 'active'),
    schedule: availabilitySchedule,
    now: nowDate
  })
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: undefined,
    name,
    description: normalizeOptionalApiKeyDescription(input.description),
    keyPrefix,
    keySuffix,
    status,
    routeStrategyId,
    expiresAt: normalizeOptionalApiKeyExpiresAt(input.expiresAt),
    quotaLimits,
    availabilitySchedule,
    isDefault: false,
    usage: {
      requestCount: 0,
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      cacheReadCost: 0,
      cacheWriteTokens: 0,
      cacheWrite1hTokens: 0,
      cacheWriteCost: 0,
      thinkingTokens: 0,
      inputImageTokens: 0,
      outputImageTokens: 0,
      totalTokens: 0,
      totalCost: 0
    },
    key
  }
  try {
    await client.transaction(async (tx) => {
      routeStrategyId = await assertRouteStrategySelectableForApiKeyAsync(systemAccountId, routeStrategyId, tx, true)
      const quotaLimitsJson = requestQuotaLimitsJson(record.quotaLimits)
      const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(record.availabilitySchedule, nowDate)
      await tx.execute(`
        INSERT INTO ${apiKeyTable(tx, 'api_keys')} (
          id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
          key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
          availability_schedule_next_check_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)
      `, [
        record.id,
        systemAccountId,
        routeStrategyId,
        record.name,
        record.description ?? null,
        hashSecret(key),
        record.keyPrefix,
        record.keySuffix,
        encryptJson({ key }),
        record.status,
        record.expiresAt ?? null,
        quotaLimitsJson,
        apiKeyAvailabilityScheduleJson(record.availabilitySchedule),
        availabilityScheduleNextCheckAt,
        now,
        now
      ])
      await rememberRequestQuotaHourlyWindowsFromLimitsAsync(tx, record.quotaLimits, now)
    })
  } catch (error) {
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${record.name}`)
    }
    throw error
  }
  invalidateApiKeyLookupCache(record.id)
  notifyGatewayRuntimeCacheInvalidation('api_key_created')
  notifyApiKeyQuotaCacheInvalidation(record.id, 'api_key_created')
  return {
    ...(await findApiKeySummaryAsync(record.id, access) ?? record),
    key
  }
}

export function updateApiKey(id: string, input: Record<string, unknown>, access?: AccessScope): ApiKeySummary | undefined {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 更新参数')
  const systemAccountId = apiKeySystemAccountId(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) return undefined
  const currentRow = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns()} FROM api_keys ${apiKeyListJoins()} WHERE api_keys.id = ? AND api_keys.system_account_id = ?`)
    .get(id, systemAccountId) as unknown as ApiKeyRow | undefined
  const current = currentRow ? apiKeySummariesFromRows([currentRow], { systemAccountId, role: 'user' }, { includeSecret: false })[0] : undefined
  if (!current || !currentRow) return undefined

  const hasRouteStrategyInput = Object.prototype.hasOwnProperty.call(input, 'routeStrategyId')
  let nextRouteStrategyId = hasRouteStrategyInput
    ? assertRouteStrategySelectableForApiKey(systemAccountId, input.routeStrategyId)
    : current.routeStrategyId
  if (current.isDefault && nextRouteStrategyId !== current.routeStrategyId) {
    throw new Error('默认 API Key 不允许更换策略路由')
  }
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const hasAvailabilityScheduleInput = isApiKeyAvailabilityScheduleInputPresent(input)
  const mutationNow = new Date()
  const requestedStatus = hasStatusInput ? normalizeApiKeyStatus(input.status, current.status) : current.status
  const nextAvailabilitySchedule = hasAvailabilityScheduleInput ? apiKeyAvailabilityScheduleFromRequest(input) : current.availabilitySchedule
  const nextStatus = hasAvailabilityScheduleInput
    ? apiKeyStatusForScheduleMutation({ requestedStatus, schedule: nextAvailabilitySchedule, now: mutationNow })
    : requestedStatus
  const next: ApiKeySummary = {
    ...current,
    name: Object.prototype.hasOwnProperty.call(input, 'name') ? normalizedApiKeyName(input.name) : current.name,
    description: Object.prototype.hasOwnProperty.call(input, 'description') ? normalizeOptionalApiKeyDescription(input.description) : current.description,
    status: nextStatus,
    routeStrategyId: nextRouteStrategyId,
    expiresAt: hasExpiresAtInput ? normalizeOptionalApiKeyExpiresAt(input.expiresAt) : current.expiresAt,
    quotaLimits: normalizeRequestQuotaLimits(input.quotaLimits, current.quotaLimits ?? emptyRequestQuotaLimits()),
    availabilitySchedule: nextAvailabilitySchedule
  }
  const database = getBusinessDatabase()
  const nowDate = mutationNow
  const now = nowDate.toISOString()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    nextRouteStrategyId = assertRouteStrategySelectableForApiKey(systemAccountId, nextRouteStrategyId)
    const quotaLimitsJson = requestQuotaLimitsJson(next.quotaLimits)
    const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(next.availabilitySchedule, nowDate)
    database
      .prepare(`
        UPDATE api_keys
        SET name = ?, description = ?, route_strategy_id = ?, status = ?, expires_at = ?,
            quota_limits_json = ?, availability_schedule_json = ?, availability_schedule_next_check_at = ?,
            updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `)
      .run(
        next.name,
        next.description ?? null,
        nextRouteStrategyId,
        next.status,
        next.expiresAt ?? null,
        quotaLimitsJson,
        apiKeyAvailabilityScheduleJson(next.availabilitySchedule),
        availabilityScheduleNextCheckAt,
        now,
        id,
        systemAccountId
      )
    rememberRequestQuotaHourlyWindowsFromJson(quotaLimitsJson, database, now)
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${next.name}`)
    }
    throw error
  }
  invalidateGatewayApiKeyCacheById(id)
  invalidateApiKeyLookupCache(id)
  notifyGatewayRuntimeCacheInvalidation('api_key_updated')
  notifyApiKeyQuotaCacheInvalidation(id, 'api_key_updated')
  return findApiKeySummary(id, access) ?? next
}

export async function updateApiKeyAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<ApiKeySummary | undefined> {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 更新参数')
  const systemAccountId = await apiKeySystemAccountIdAsync(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) return undefined
  const client = await getApiKeyDatabaseClient()
  const currentRow = await client.one<ApiKeyRow>(`
    SELECT ${apiKeyListColumns()}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    ${apiKeyListJoinsForClient(client)}
    WHERE api_keys.id = ? AND api_keys.system_account_id = ?
  `, [id, systemAccountId])
  const current = currentRow ? (await apiKeySummariesFromRowsAsync([currentRow], { systemAccountId, role: 'user' }, { includeSecret: false }))[0] : undefined
  if (!current || !currentRow) return undefined

  const hasRouteStrategyInput = Object.prototype.hasOwnProperty.call(input, 'routeStrategyId')
  let nextRouteStrategyId = hasRouteStrategyInput
    ? await assertRouteStrategySelectableForApiKeyAsync(systemAccountId, input.routeStrategyId)
    : current.routeStrategyId
  if (current.isDefault && nextRouteStrategyId !== current.routeStrategyId) {
    throw new Error('默认 API Key 不允许更换策略路由')
  }
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const hasAvailabilityScheduleInput = isApiKeyAvailabilityScheduleInputPresent(input)
  const mutationNow = new Date()
  const requestedStatus = hasStatusInput ? normalizeApiKeyStatus(input.status, current.status) : current.status
  const nextAvailabilitySchedule = hasAvailabilityScheduleInput ? apiKeyAvailabilityScheduleFromRequest(input) : current.availabilitySchedule
  const nextStatus = hasAvailabilityScheduleInput
    ? apiKeyStatusForScheduleMutation({ requestedStatus, schedule: nextAvailabilitySchedule, now: mutationNow })
    : requestedStatus
  const next: ApiKeySummary = {
    ...current,
    name: Object.prototype.hasOwnProperty.call(input, 'name') ? normalizedApiKeyName(input.name) : current.name,
    description: Object.prototype.hasOwnProperty.call(input, 'description') ? normalizeOptionalApiKeyDescription(input.description) : current.description,
    status: nextStatus,
    routeStrategyId: nextRouteStrategyId,
    expiresAt: hasExpiresAtInput ? normalizeOptionalApiKeyExpiresAt(input.expiresAt) : current.expiresAt,
    quotaLimits: normalizeRequestQuotaLimits(input.quotaLimits, current.quotaLimits ?? emptyRequestQuotaLimits()),
    availabilitySchedule: nextAvailabilitySchedule
  }
  try {
    await client.transaction(async (tx) => {
      nextRouteStrategyId = await assertRouteStrategySelectableForApiKeyAsync(systemAccountId, nextRouteStrategyId, tx, true)
      const nowDate = mutationNow
      const now = nowDate.toISOString()
      const quotaLimitsJson = requestQuotaLimitsJson(next.quotaLimits)
      const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(next.availabilitySchedule, nowDate)
      await tx.execute(`
        UPDATE ${apiKeyTable(tx, 'api_keys')}
        SET name = ?, description = ?, route_strategy_id = ?, status = ?, expires_at = ?,
            quota_limits_json = ?, availability_schedule_json = ?, availability_schedule_next_check_at = ?,
            updated_at = ?
        WHERE id = ? AND system_account_id = ?
      `, [
        next.name,
        next.description ?? null,
        nextRouteStrategyId,
        next.status,
        next.expiresAt ?? null,
        quotaLimitsJson,
        apiKeyAvailabilityScheduleJson(next.availabilitySchedule),
        availabilityScheduleNextCheckAt,
        now,
        id,
        systemAccountId
      ])
      await rememberRequestQuotaHourlyWindowsFromLimitsAsync(tx, next.quotaLimits, now)
    })
  } catch (error) {
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${next.name}`)
    }
    throw error
  }
  await invalidateGatewayApiKeyCacheByIdAsync(id)
  invalidateApiKeyLookupCache(id)
  notifyGatewayRuntimeCacheInvalidation('api_key_updated')
  notifyApiKeyQuotaCacheInvalidation(id, 'api_key_updated')
  return await findApiKeySummaryAsync(id, access) ?? next
}

function apiKeyStatusForScheduleMutation(input: {
  requestedStatus: 'active' | 'disabled'
  schedule: ApiKeySummary['availabilitySchedule']
  now: Date
}): 'active' | 'disabled' {
  return apiKeyAvailabilityScheduleStatus(input.schedule, input.now) ?? input.requestedStatus
}

export function refreshApiKeySecret(id: string, access?: AccessScope): (ApiKeySummary & { key: string }) | undefined {
  const systemAccountId = apiKeySystemAccountId(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) return undefined
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const now = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE api_keys
      SET key_hash = ?, key_prefix = ?, key_suffix = ?, key_secret_encrypted = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ?
    `)
    .run(hashSecret(key), keyPrefix, keySuffix, encryptJson({ key }), now, id, systemAccountId)
  if (result.changes <= 0) return undefined
  invalidateGatewayApiKeyCacheById(id)
  invalidateApiKeyLookupCache(id)
  notifyGatewayRuntimeCacheInvalidation('api_key_secret_refreshed')
  notifyApiKeyQuotaCacheInvalidation(id, 'api_key_secret_refreshed')
  const summary = findApiKeySummary(id, access)
  return summary ? { ...summary, key } : undefined
}

export async function refreshApiKeySecretAsync(id: string, access?: AccessScope): Promise<(ApiKeySummary & { key: string }) | undefined> {
  const systemAccountId = await apiKeySystemAccountIdAsync(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) return undefined
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const now = nowIso()
  const client = await getApiKeyDatabaseClient()
  const result = await client.execute(`
    UPDATE ${apiKeyTable(client, 'api_keys')}
    SET key_hash = ?, key_prefix = ?, key_suffix = ?, key_secret_encrypted = ?, updated_at = ?
    WHERE id = ? AND system_account_id = ?
  `, [hashSecret(key), keyPrefix, keySuffix, encryptJson({ key }), now, id, systemAccountId])
  if (result.changes <= 0) return undefined
  await invalidateGatewayApiKeyCacheByIdAsync(id)
  invalidateApiKeyLookupCache(id)
  notifyGatewayRuntimeCacheInvalidation('api_key_secret_refreshed')
  notifyApiKeyQuotaCacheInvalidation(id, 'api_key_secret_refreshed')
  const summary = await findApiKeySummaryAsync(id, access)
  return summary ? { ...summary, key } : undefined
}

export interface ApiKeyDeleteCleanupTarget {
  apiKeyId: string
  systemAccountId: string
}

export interface ApiKeyDeleteResult {
  deleted: boolean
  cleanupTarget?: ApiKeyDeleteCleanupTarget
}

export function deleteApiKey(id: string, access?: AccessScope): boolean {
  return deleteApiKeyWithRelatedCleanup(id, access).deleted
}

export async function deleteApiKeyAsync(id: string, access?: AccessScope): Promise<boolean> {
  return (await deleteApiKeyWithRelatedCleanupAsync(id, access)).deleted
}

export function deleteApiKeyWithRelatedCleanup(id: string, access?: AccessScope): ApiKeyDeleteResult {
  const scope = buildSystemAccountScopeClause(access)
  const database = getBusinessDatabase()
  const row = database
    .prepare(`SELECT id, system_account_id, is_default FROM api_keys WHERE id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeyDeleteRow | undefined
  if (!row) return { deleted: false }
  assertApiKeyNotDefault(row)

  const result = database
    .prepare('DELETE FROM api_keys WHERE id = ? AND system_account_id = ?')
    .run(row.id, row.system_account_id)
  if (result.changes > 0) {
    invalidateGatewayApiKeyCacheById(row.id)
    invalidateApiKeyLookupCache(row.id)
    notifyGatewayRuntimeCacheInvalidation('api_key_deleted')
    notifyApiKeyQuotaCacheInvalidation(row.id, 'api_key_deleted')
  }
  return {
    deleted: result.changes > 0,
    cleanupTarget: result.changes > 0 ? { apiKeyId: row.id, systemAccountId: row.system_account_id } : undefined
  }
}

export async function deleteApiKeyWithRelatedCleanupAsync(id: string, access?: AccessScope): Promise<ApiKeyDeleteResult> {
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access)
  const row = await client.one<ApiKeyDeleteRow>(`
    SELECT id, system_account_id, is_default
    FROM ${apiKeyTable(client, 'api_keys')}
    WHERE id = ?${scope.clause}
  `, [id, ...scope.params])
  if (!row) return { deleted: false }
  assertApiKeyNotDefault(row)

  const cleanupTarget = { apiKeyId: row.id, systemAccountId: row.system_account_id }
  let deleted = false
  if (runtimeConfig.databaseDriver === 'postgres') {
    deleted = await client.transaction(async (tx) => {
      const result = await tx.execute(`
        DELETE FROM ${apiKeyTable(tx, 'api_keys')}
        WHERE id = ? AND system_account_id = ?
      `, [row.id, row.system_account_id])
      const rowDeleted = result.changes > 0
      if (rowDeleted) {
        await registerDeletedApiKeyRecordCleanupTargetInClientAsync(tx, cleanupTarget)
      }
      return rowDeleted
    })
  } else {
    const result = await client.execute(`
      DELETE FROM ${apiKeyTable(client, 'api_keys')}
      WHERE id = ? AND system_account_id = ?
    `, [row.id, row.system_account_id])
    deleted = result.changes > 0
  }
  if (deleted) {
    await invalidateGatewayApiKeyCacheByIdAsync(row.id)
    invalidateApiKeyLookupCache(row.id)
    notifyGatewayRuntimeCacheInvalidation('api_key_deleted')
    notifyApiKeyQuotaCacheInvalidation(row.id, 'api_key_deleted')
  }
  return {
    deleted,
    cleanupTarget: deleted ? cleanupTarget : undefined
  }
}

export function ensureDefaultApiKeysForSystemAccount(systemAccountId: string, timestamp = nowIso()): string[] {
  ensureDefaultRouteStrategiesForSystemAccount(systemAccountId, timestamp)
  const database = getBusinessDatabase()
  const routeStrategies = defaultRouteStrategiesForSystemAccount(database, systemAccountId)
  const apiKeyIds: string[] = []
  for (const routeStrategy of routeStrategies) {
    const existing = defaultApiKeyIdForRouteStrategy(database, routeStrategy.id)
    if (existing) {
      apiKeyIds.push(existing)
      continue
    }
    const apiKeyId = newId('key')
    const key = createApiKey()
    const name = nextDefaultApiKeyName(database, systemAccountId, defaultApiKeyNameForRouteStrategy(routeStrategy.name))
    try {
      database.prepare(`
        INSERT INTO api_keys (
          id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
          key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
          availability_schedule_next_check_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 1, NULL, NULL, NULL, NULL, ?, ?)
      `).run(
        apiKeyId,
        systemAccountId,
        routeStrategy.id,
        name,
        `系统默认 API Key，绑定${routeStrategy.name}。`,
        hashSecret(key),
        key.slice(0, 8),
        key.slice(-8),
        encryptJson({ key }),
        timestamp,
        timestamp
      )
      apiKeyIds.push(apiKeyId)
    } catch (error) {
      const raced = defaultApiKeyIdForRouteStrategy(database, routeStrategy.id)
      if (raced && (isDuplicateApiKeyNameError(error) || isDuplicateDefaultApiKeyError(error))) {
        apiKeyIds.push(raced)
        continue
      }
      throw error
    }
  }
  return apiKeyIds
}

export async function ensureDefaultApiKeysForSystemAccountAsync(client: DatabaseClient, systemAccountId: string, timestamp = nowIso()): Promise<string[]> {
  await ensureDefaultRouteStrategiesForSystemAccountAsync(client, systemAccountId, timestamp)
  const routeStrategies = await defaultRouteStrategiesForSystemAccountAsync(client, systemAccountId)
  const apiKeyIds: string[] = []
  for (const routeStrategy of routeStrategies) {
    const existing = await defaultApiKeyIdForRouteStrategyAsync(client, routeStrategy.id)
    if (existing) {
      apiKeyIds.push(existing)
      continue
    }
    const apiKeyId = newId('key')
    const key = createApiKey()
    const name = await nextDefaultApiKeyNameAsync(client, systemAccountId, defaultApiKeyNameForRouteStrategy(routeStrategy.name))
    try {
      await client.execute(`
        INSERT INTO ${apiKeyTable(client, 'api_keys')} (
          id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
          key_secret_encrypted, status, is_default, expires_at, quota_limits_json, availability_schedule_json,
          availability_schedule_next_check_at, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 1, NULL, NULL, NULL, NULL, ?, ?)
      `, [
        apiKeyId,
        systemAccountId,
        routeStrategy.id,
        name,
        `系统默认 API Key，绑定${routeStrategy.name}。`,
        hashSecret(key),
        key.slice(0, 8),
        key.slice(-8),
        encryptJson({ key }),
        timestamp,
        timestamp
      ])
      apiKeyIds.push(apiKeyId)
    } catch (error) {
      const raced = await defaultApiKeyIdForRouteStrategyAsync(client, routeStrategy.id)
      if (raced && (isDuplicateApiKeyNameError(error) || isDuplicateDefaultApiKeyError(error))) {
        apiKeyIds.push(raced)
        continue
      }
      throw error
    }
  }
  return apiKeyIds
}

function defaultRouteStrategiesForSystemAccount(database: ReturnType<typeof getBusinessDatabase>, systemAccountId: string): Array<{ id: string; name: string }> {
  return database.prepare(`
    SELECT route_strategies.id, route_strategies.name
    FROM route_strategies
    INNER JOIN route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
    INNER JOIN groups
      ON groups.id = route_strategy_groups.group_id
      AND groups.system_account_id = route_strategy_groups.system_account_id
    WHERE route_strategies.system_account_id = ?
      AND route_strategies.is_default = 1
      AND groups.provider_code <> ?
    ORDER BY route_strategies.created_at ASC, route_strategies.id ASC
  `).all(systemAccountId, HYBRID_PROVIDER_CODE) as Array<{ id: string; name: string }>
}

async function defaultRouteStrategiesForSystemAccountAsync(client: DatabaseClient, systemAccountId: string): Promise<Array<{ id: string; name: string }>> {
  return client.query<{ id: string; name: string }>(`
    SELECT route_strategies.id, route_strategies.name
    FROM ${apiKeyTable(client, 'route_strategies')} route_strategies
    INNER JOIN ${apiKeyTable(client, 'route_strategy_groups')} route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
    INNER JOIN ${apiKeyTable(client, 'groups')} groups
      ON groups.id = route_strategy_groups.group_id
      AND groups.system_account_id = route_strategy_groups.system_account_id
    WHERE route_strategies.system_account_id = ?
      AND route_strategies.is_default = 1
      AND groups.provider_code <> ?
    ORDER BY route_strategies.created_at ASC, route_strategies.id ASC
  `, [systemAccountId, HYBRID_PROVIDER_CODE])
}

function defaultApiKeyIdForRouteStrategy(database: ReturnType<typeof getBusinessDatabase>, routeStrategyId: string): string | undefined {
  const row = database
    .prepare('SELECT id FROM api_keys WHERE route_strategy_id = ? AND is_default = 1 ORDER BY created_at ASC, id ASC LIMIT 1')
    .get(routeStrategyId) as { id?: string } | undefined
  return row?.id
}

async function defaultApiKeyIdForRouteStrategyAsync(client: DatabaseClient, routeStrategyId: string): Promise<string | undefined> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${apiKeyTable(client, 'api_keys')}
    WHERE route_strategy_id = ? AND is_default = 1
    ORDER BY created_at ASC, id ASC
    LIMIT 1
  `, [routeStrategyId])
  return row?.id
}

function nextDefaultApiKeyName(database: ReturnType<typeof getBusinessDatabase>, systemAccountId: string, baseName: string): string {
  const rows = database
    .prepare(`
      SELECT name
      FROM api_keys
      WHERE system_account_id = ? AND (name = ? OR name LIKE ? ESCAPE '\\')
    `)
    .all(systemAccountId, baseName, `${baseName} %`) as Array<{ name?: string | null }>
  return nextDefaultApiKeyNameFromExisting(rows.map((row) => row.name), baseName)
}

async function nextDefaultApiKeyNameAsync(client: DatabaseClient, systemAccountId: string, baseName: string): Promise<string> {
  const rows = await client.query<{ name?: string | null }>(`
    SELECT name
    FROM ${apiKeyTable(client, 'api_keys')}
    WHERE system_account_id = ? AND (name = ? OR name LIKE ? ESCAPE '\\')
  `, [systemAccountId, baseName, `${baseName} %`])
  return nextDefaultApiKeyNameFromExisting(rows.map((row) => row.name), baseName)
}

function nextDefaultApiKeyNameFromExisting(names: Array<string | null | undefined>, baseName: string): string {
  const existing = new Set(names.map((name) => String(name ?? '').trim()).filter(Boolean))
  if (!existing.has(baseName)) return baseName
  for (let index = 2; index <= 1000; index += 1) {
    const candidate = `${baseName} ${index}`
    if (!existing.has(candidate)) return candidate
  }
  return `${baseName} ${Date.now()}`
}

function defaultApiKeyNameForRouteStrategy(routeStrategyName: string): string {
  return routeStrategyName.replace(/路由$/, 'API Key')
}

function assertApiKeyNotDefault(row: Pick<ApiKeyDeleteRow, 'is_default'>): void {
  if (normalizeApiKeyDefaultFlag(row.is_default)) {
    throw new Error('默认 API Key 不允许删除')
  }
}

function normalizeApiKeyDefaultFlag(value: unknown): boolean {
  return value === true || value === 1 || value === '1'
}

function normalizedApiKeyName(value: unknown): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error('API Key 名称不能为空')
  }
  return value.trim()
}

function normalizeOptionalApiKeyDescription(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value !== 'string') {
    throw new Error('API Key 说明必须是字符串')
  }
  const description = value.trim()
  if (!description) return undefined
  if (description.length > 200) {
    throw new Error('API Key 说明不能超过 200 个字符')
  }
  return description
}

function normalizeOptionalApiKeyExpiresAt(value: unknown): string | undefined {
  if (value === undefined || value === null || value === '') return undefined
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error('API Key 过期时间必须是有效时间字符串')
  }
  const normalized = optionalServerDateTimeIso(value)
  if (!normalized) {
    throw new Error('API Key 过期时间必须是有效时间字符串')
  }
  return normalized
}

function normalizeApiKeyStatus(value: unknown, fallback: 'active' | 'disabled'): 'active' | 'disabled' {
  if (value === undefined) return fallback
  if (value === 'active' || value === 'disabled') return value
  throw new Error('API Key 状态无效')
}

function assertKnownInputKeys(input: Record<string, unknown>, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function buildApiKeyFiltersForClient(
  client: DatabaseClient,
  scope: { clause: string; params: string[] },
  options: ReturnType<typeof normalizeApiKeyListOptions>
): { clause: string; params: Array<string | number> } {
  if (client.driver === 'sqlite') return buildApiKeyFilters(scope, options)
  const clauses: string[] = []
  const params: Array<string | number> = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^\s*WHERE\s+/i, ''))
    params.push(...scope.params)
  }
  if (options.keyword) {
    clauses.push(`(
      api_keys.name COLLATE "C" >= ?
      AND api_keys.name COLLATE "C" < ?
      AND starts_with(api_keys.name, ?)
    )`)
    params.push(options.keyword, apiKeyTextPrefixUpperBound(options.keyword), options.keyword)
  }
  if (options.status) {
    clauses.push('api_keys.status = ?')
    params.push(options.status)
  }
  if (options.routeStrategyId) {
    clauses.push('api_keys.route_strategy_id = ?')
    params.push(options.routeStrategyId)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function apiKeyTextPrefixUpperBound(value: string): string {
  const chars = [...value]
  for (let index = chars.length - 1; index >= 0; index -= 1) {
    const codePoint = chars[index].codePointAt(0)
    if (codePoint === undefined || codePoint >= 0x10ffff) continue
    return `${chars.slice(0, index).join('')}${String.fromCodePoint(codePoint + 1)}`
  }
  return `${value}\uffff`
}

async function apiKeySystemAccountIdAsync(apiKeyId: string): Promise<string | undefined> {
  const client = await getApiKeyDatabaseClient()
  const row = await client.one<{ system_account_id?: string }>(`
    SELECT system_account_id
    FROM ${apiKeyTable(client, 'api_keys')}
    WHERE id = ?
  `, [apiKeyId])
  return row?.system_account_id
}

async function rememberRequestQuotaHourlyWindowsFromLimitsAsync(client: DatabaseClient, limits: RequestQuotaLimits, timestamp: string): Promise<void> {
  const hours = limits.hourly?.enabled ? limits.hourly.hours : undefined
  if (!Number.isInteger(hours) || typeof hours !== 'number') return
  await client.execute(`
    INSERT INTO ${apiKeyTable(client, 'request_quota_hourly_window_configs')} (window_hours, created_at, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(window_hours) DO UPDATE SET updated_at = excluded.updated_at
  `, [hours, timestamp, timestamp])
}

async function getApiKeyDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function apiKeyTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function isDuplicateApiKeyNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_api_keys_owner_name_unique')
    || error.message.includes('idx_api_keys_owner_name_unique_lower')
    || error.message.includes('UNIQUE constraint failed: api_keys.system_account_id, api_keys.name')
}

function isDuplicateDefaultApiKeyError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_api_keys_route_default_unique')
}
