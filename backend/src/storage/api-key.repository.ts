import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyQuotaLimits,
  ApiKeySummary,
  RouteStrategyMode,
  RouteStrategyStatus
} from '../domain/types.js'
import { GPT_VENDOR_CODE, HYBRID_PROVIDER_CODE } from '../domain/provider-protocol.js'
import { normalizeRouteStrategyMode } from '../domain/route-strategy.js'
import { runtimeConfig } from '../config/runtime.js'
import { rfc3339InstantMilliseconds } from '../shared/rfc3339.js'
import {
  notifyGatewayApiKeyValidationCacheInvalidationAsync,
  notifyApiKeyQuotaCacheInvalidation,
  notifyGatewayRuntimeCacheInvalidation
} from '../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../shared/logger.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { apiKeySystemAccountId, canManageApiKeyOwner } from './api-key-access.js'
import {
  apiKeyAvailabilityScheduleFromRequest,
  apiKeyAvailabilityScheduleJson,
  apiKeyAvailabilityScheduleStatus,
  isApiKeyAvailabilityScheduleInputPresent,
  nextApiKeyAvailabilityScheduleCheckAt,
  parseApiKeyAvailabilityScheduleJson
} from './api-key-availability-schedule.js'
import { buildApiKeyFilters, normalizeApiKeyListOptions } from './api-key-list-query.js'
import {
  apiKeyListItemsFromRows,
  apiKeyListItemsFromRowsAsync,
  type ApiKeyListItem,
  type ApiKeyListRow
} from './api-key-list-mappers.js'
import { apiKeySummariesFromRows, apiKeySummariesFromRowsAsync, type ApiKeyRow } from './api-key-mappers.js'
import { registerDeletedApiKeyRecordCleanupTargetInClientAsync } from './api-key-record-cleanup.js'
import { createApiKey, decryptJson, encryptJson, hashSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { invalidateGatewayApiKeyCacheById } from './gateway-api-key.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { pagedTotalUpperBound, takePageRows } from './query-utils.js'
import { invalidateApiKeyLookupCache, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import {
  syncApiKeyRequestQuotaHourlyWindowScopeBinding,
  syncApiKeyRequestQuotaHourlyWindowScopeBindingAsync
} from './request-quota-hourly-windows.repository.js'
import { emptyRequestQuotaLimits, normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import {
  assertRouteStrategySelectableForApiKey,
  assertRouteStrategySelectableForApiKeyAsync,
  ensureDefaultRouteStrategiesForSystemAccount,
  ensureDefaultRouteStrategiesForSystemAccountAsync
} from './route-strategy.repository.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from './sqlite-read-worker-pool.js'
import { findPreferredDefaultRouteStrategyReferenceAsync } from './user-reference-data.repository.js'
import { optionalServerDateTimeIso } from './value-utils.js'

export type { ApiKeyListItem } from './api-key-list-mappers.js'

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
  items: ApiKeyListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

type ApiKeyDeleteRow = {
  id: string
  system_account_id: string
  name: string
  key_hash: string
  is_default?: number | string | boolean | null
  purpose?: string | null
}

interface ApiKeySecretRow {
  id: string
  system_account_id: string
  name: string
  key_prefix: string
  key_suffix: string
  key_secret_encrypted: string | null
}

interface ApiKeyMutationRow {
  id: string
  system_account_id: string
  name: string
  key_hash?: string
  description?: string | null
  route_strategy_id?: string
  status?: 'active' | 'disabled'
  is_default?: number | string | boolean | null
  purpose?: string | null
  expires_at?: string | null
  quota_limits_json?: string | null
  availability_schedule_json?: string | null
  updated_at: string
}

interface ApiKeyRefreshRow {
  id: string
  system_account_id: string
  name: string
  key_hash: string
  key_prefix: string
  key_suffix: string
  updated_at: string
}

interface ApiKeyUpdateViewRow {
  id: string
  name: string
  key_prefix: string
  status: 'active' | 'disabled'
  route_strategy_id: string
  route_strategy_name: string | null
  route_strategy_mode: unknown
  route_strategy_status: unknown
  expires_at: string | null
  availability_schedule_json: string | null
  updated_at: string
}

export interface ApiKeySecretRecord {
  id: string
  systemAccountId: string
  name: string
  keyPrefix: string
  keySuffix: string
  key: string
}

export interface ApiKeyCreateResult {
  id: string
  key: string
  keyPrefix: string
  keySuffix: string
  revision: string
}

export interface ApiKeyRefreshOutcome {
  result: ApiKeyCreateResult
  ownerSystemAccountId: string
  resourceName: string
  previousKeyPrefix: string
  previousKeySuffix: string
  validationCacheError?: ApiKeyValidationCacheInvalidationError
}

export type ApiKeyCreatedRecord = ApiKeySummary & { key: string; revision: string }

export type ApiKeyUpdateView = Pick<
  ApiKeySummary,
  | 'id'
  | 'name'
  | 'keyPrefix'
  | 'status'
  | 'routeStrategyId'
  | 'routeStrategyName'
  | 'routeStrategyMode'
  | 'routeStrategyStatus'
  | 'expiresAt'
  | 'availabilitySchedule'
>

interface ApiKeyUpdateSnapshot extends ApiKeyUpdateView {
  revision: string
}

export type ApiKeyMutableField =
  | 'name'
  | 'description'
  | 'routeStrategyId'
  | 'status'
  | 'expiresAt'
  | 'quotaLimits'
  | 'availabilitySchedule'

export interface ApiKeyMutationRowPatch {
  revision: string
  name?: string
  description?: string | null
  routeStrategyId?: string
  routeStrategyName?: string
  routeStrategyMode?: RouteStrategyMode
  routeStrategyStatus?: RouteStrategyStatus
  status?: 'active' | 'disabled'
  expiresAt?: string | null
  quotaLimits?: ApiKeyQuotaLimits
  availabilitySchedule?: ApiKeyAvailabilitySchedule | null
}

export interface ApiKeyPatchResult {
  id: string
  revision: string
  changedFields: ApiKeyMutableField[]
  rowPatch: ApiKeyMutationRowPatch
}

export interface ApiKeyPatchOutcome {
  result: ApiKeyPatchResult
  ownerSystemAccountId: string
  resourceName: string
  before: Partial<Record<ApiKeyMutableField, unknown>>
  after: Partial<Record<ApiKeyMutableField, unknown>>
  validationCacheError?: ApiKeyValidationCacheInvalidationError
}

export class ApiKeyRevisionConflictError extends Error {
  constructor(readonly currentRevision: string) {
    super('API Key 已被其他操作修改，请刷新后重试')
    this.name = 'ApiKeyRevisionConflictError'
  }
}

export class ApiKeyValidationCacheInvalidationError extends Error {
  constructor(readonly apiKeyId: string, readonly invalidationCause: unknown) {
    super('API Key validation cache 失效失败')
    this.name = 'ApiKeyValidationCacheInvalidationError'
  }
}

export function listApiKeys(access?: AccessScope, options?: ApiKeyListOptions): ApiKeyListItem[] {
  return queryApiKeys(access, options).items
}

export async function listApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions): Promise<ApiKeyListItem[]> {
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

export function listApiKeysReadOnly(access?: AccessScope, options?: ApiKeyListOptions): ApiKeyListItem[] {
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

export function findApiKeySecret(id: string, access?: AccessScope): ApiKeySecretRecord | undefined {
  return findApiKeySecretReadOnly(id, access)
}

export function findApiKeySecretReadOnly(id: string, access?: AccessScope): ApiKeySecretRecord | undefined {
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = getBusinessDatabase()
    .prepare(`SELECT ${apiKeySecretColumns()} FROM api_keys WHERE api_keys.id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeySecretRow | undefined
  return row ? apiKeySecretRecordFromRow(row) : undefined
}

export async function findApiKeySecretAsync(id: string, access?: AccessScope): Promise<ApiKeySecretRecord | undefined> {
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
  const row = await client.one<ApiKeySecretRow>(`
    SELECT ${apiKeySecretColumns()}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    WHERE api_keys.id = ?${scope.clause}
  `, [id, ...scope.params])
  return row ? apiKeySecretRecordFromRow(row) : undefined
}

function queryApiKeys(access: AccessScope | undefined, options: ApiKeyListOptions | undefined, paged: true): ApiKeyListResult
function queryApiKeys(access?: AccessScope, options?: ApiKeyListOptions, paged?: false): Omit<ApiKeyListResult, 'items'> & { items: ApiKeyListItem[] }
function queryApiKeys(access?: AccessScope, options?: ApiKeyListOptions, paged = false): ApiKeyListResult | (Omit<ApiKeyListResult, 'items'> & { items: ApiKeyListItem[] }) {
  const normalized = normalizeApiKeyListOptions(options)
  const scope = buildSystemAccountWhereClause(access, 'api_keys.system_account_id')
  const filters = buildApiKeyFilters(scope, normalized)
  const limitClause = paged ? 'LIMIT ? OFFSET ?' : ''
  const limitParams = paged ? [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize] : []
  const rows = getBusinessDatabase()
    .prepare(`
      SELECT ${apiKeyPageColumns(access)}
      FROM api_keys
      ${apiKeyPageJoins(access)}
      ${filters.clause}
      ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
      ${limitClause}
    `)
    .all(...filters.params, ...limitParams) as unknown as ApiKeyListRow[]
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = apiKeyListItemsFromRows(pageRows.rows, access)
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

function queryApiKeysAsync(access: AccessScope | undefined, options: ApiKeyListOptions | undefined, paged: true): Promise<ApiKeyListResult>
function queryApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions, paged?: false): Promise<Omit<ApiKeyListResult, 'items'> & { items: ApiKeyListItem[] }>
async function queryApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions, paged = false): Promise<ApiKeyListResult | (Omit<ApiKeyListResult, 'items'> & { items: ApiKeyListItem[] })> {
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
  const rows = await client.query<ApiKeyListRow>(`
    WITH ${cteParts.join(', ')}
    SELECT ${apiKeyPageColumns(access, client)}
    FROM page_api_key_ids
    INNER JOIN ${apiKeyTable(client, 'api_keys')} api_keys
      ON api_keys.id = page_api_key_ids.id
    ${apiKeyPageJoinsForClient(client, access)}
    ORDER BY api_keys.is_default DESC, api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
  `, [...(keywordCte?.params ?? []), ...filters.params, ...limitParams])
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = await apiKeyListItemsFromRowsAsync(pageRows.rows, access)
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

function apiKeyPageColumns(access?: AccessScope, client?: DatabaseClient): string {
  const columns = [
    'api_keys.id',
    'api_keys.system_account_id',
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
    'api_keys.purpose',
    'api_keys.expires_at',
    'api_keys.quota_limits_json',
    'api_keys.availability_schedule_json',
    apiKeyRevisionSelectExpression(client)
  ]
  if (includeSystemAccountFields(access)) {
    columns.splice(2, 0, 'system_accounts.display_name AS system_account_name')
  }
  return columns.join(', ')
}

function apiKeyRevisionSelectExpression(_client?: DatabaseClient, tableAlias = 'api_keys'): string {
  return `${tableAlias}.updated_at`
}

function apiKeyPageJoins(access?: AccessScope): string {
  return `
    ${includeSystemAccountFields(access) ? 'LEFT JOIN system_accounts ON system_accounts.id = api_keys.system_account_id' : ''}
    INNER JOIN route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
  `
}

function apiKeyPageJoinsForClient(client: DatabaseClient, access?: AccessScope): string {
  return `
    ${includeSystemAccountFields(access)
      ? `LEFT JOIN ${apiKeyTable(client, 'system_accounts')} system_accounts ON system_accounts.id = api_keys.system_account_id`
      : ''}
    INNER JOIN ${apiKeyTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
  `
}

function apiKeySecretColumns(): string {
  return [
    'api_keys.id',
    'api_keys.system_account_id',
    'api_keys.name',
    'api_keys.key_prefix',
    'api_keys.key_suffix',
    'api_keys.key_secret_encrypted'
  ].join(', ')
}

function apiKeySecretRecordFromRow(row: ApiKeySecretRow): ApiKeySecretRecord {
  if (!row.key_secret_encrypted) {
    throw new Error('API Key 密文缺少完整密钥')
  }
  const decrypted = decryptJson<{ key?: unknown }>(row.key_secret_encrypted)
  if (typeof decrypted.key !== 'string' || !decrypted.key) {
    throw new Error('API Key 密文缺少完整密钥')
  }
  return {
    id: row.id,
    systemAccountId: row.system_account_id,
    name: row.name,
    keyPrefix: row.key_prefix,
    keySuffix: row.key_suffix,
    key: decrypted.key
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
    'api_keys.purpose',
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

export function createApiKeyRecord(input: Record<string, unknown>, access?: AccessScope): ApiKeyCreatedRecord {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 创建参数')
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const revision = apiKeyRevisionFromTimestamp(nowDate.getTime())
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  const name = normalizedApiKeyName(input.name)
  let routeStrategyId = typeof input.routeStrategyId === 'string' ? input.routeStrategyId.trim() : ''
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
    purpose: 'general',
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
    const preferredRouteStrategy = input.routeStrategyId === undefined
      ? defaultGptRouteStrategyForSystemAccount(database, systemAccountId)
      : undefined
    if (input.routeStrategyId === undefined && !preferredRouteStrategy) {
      throw new Error('当前用户缺少可用的默认策略路由')
    }
    routeStrategyId = assertRouteStrategySelectableForApiKey(
      systemAccountId,
      preferredRouteStrategy?.id ?? input.routeStrategyId
    )
    record.routeStrategyId = routeStrategyId
    const routeStrategy = apiKeyRouteStrategyReference(database, systemAccountId, routeStrategyId)
    record.routeStrategyName = routeStrategy?.name
    record.routeStrategyMode = routeStrategy?.mode
    record.routeStrategyStatus = routeStrategy?.status
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
        revision
      )
    syncApiKeyRequestQuotaHourlyWindowScopeBinding({
      apiKeyId: record.id,
      systemAccountId,
      limitsJson: quotaLimitsJson,
      active: record.status === 'active'
    }, database, revision)
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
  return {
    ...record,
    revision
  }
}

export async function createApiKeyRecordAsync(input: Record<string, unknown>, access?: AccessScope): Promise<ApiKeyCreatedRecord> {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 创建参数')
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const revision = apiKeyRevisionFromTimestamp(nowDate.getTime())
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const systemAccountId = manageableSystemAccountId(access) ?? currentSystemAccountId(access)
  const name = normalizedApiKeyName(input.name)
  const client = await getApiKeyDatabaseClient()
  let routeStrategyId = typeof input.routeStrategyId === 'string' ? input.routeStrategyId.trim() : ''
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
    purpose: 'general',
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
      const preferredRouteStrategy = input.routeStrategyId === undefined
        ? await findPreferredDefaultRouteStrategyReferenceAsync(systemAccountId, tx, true)
        : undefined
      if (input.routeStrategyId === undefined && !preferredRouteStrategy) {
        throw new Error('当前用户缺少可用的默认策略路由')
      }
      routeStrategyId = await assertRouteStrategySelectableForApiKeyAsync(
        systemAccountId,
        preferredRouteStrategy?.id ?? input.routeStrategyId,
        tx,
        true
      )
      record.routeStrategyId = routeStrategyId
      const routeStrategy = preferredRouteStrategy ?? await apiKeyRouteStrategyReferenceAsync(tx, systemAccountId, routeStrategyId)
      record.routeStrategyName = routeStrategy?.name
      record.routeStrategyMode = routeStrategy?.mode
      record.routeStrategyStatus = routeStrategy?.status
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
        revision
      ])
      await syncApiKeyRequestQuotaHourlyWindowScopeBindingForClientAsync(tx, {
        apiKeyId: record.id,
        systemAccountId,
        limitsJson: quotaLimitsJson,
        active: record.status === 'active'
      }, revision)
    })
  } catch (error) {
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${record.name}`)
    }
    throw error
  }
  return {
    ...record,
    revision
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

  const nextName = Object.prototype.hasOwnProperty.call(input, 'name') ? normalizedApiKeyName(input.name) : current.name
  assertApiKeyNameChangeAllowed(current, nextName)
  const hasRouteStrategyInput = Object.prototype.hasOwnProperty.call(input, 'routeStrategyId')
  let nextRouteStrategyId = hasRouteStrategyInput
    ? assertRouteStrategySelectableForApiKey(systemAccountId, input.routeStrategyId)
    : current.routeStrategyId
  if (current.isDefault && current.purpose !== 'chat' && nextRouteStrategyId !== current.routeStrategyId) {
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
    name: nextName,
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
    syncApiKeyRequestQuotaHourlyWindowScopeBinding({
      apiKeyId: id,
      systemAccountId,
      limitsJson: quotaLimitsJson,
      active: next.status === 'active'
    }, database, now)
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

export async function updateApiKeyAsync(id: string, input: Record<string, unknown>, access?: AccessScope): Promise<ApiKeyUpdateView | undefined> {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 更新参数')
  const client = await getApiKeyDatabaseClient()
  let snapshot = await findApiKeyUpdateSnapshotAsync(client, id, access)
  if (!snapshot) return undefined

  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      const outcome = await patchApiKeyAsync(id, input, snapshot.revision, access)
      if (!outcome) return undefined
      if (outcome.validationCacheError) throw outcome.validationCacheError
      return applyApiKeyMutationRowPatch(snapshot, outcome.result.rowPatch)
    } catch (error) {
      if (!(error instanceof ApiKeyRevisionConflictError) || attempt === 2) throw error
      snapshot = await findApiKeyUpdateSnapshotAsync(client, id, access)
      if (!snapshot) return undefined
    }
  }
  return undefined
}

async function findApiKeyUpdateSnapshotAsync(
  client: DatabaseClient,
  id: string,
  access?: AccessScope
): Promise<ApiKeyUpdateSnapshot | undefined> {
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = await client.one<ApiKeyUpdateViewRow>(`
    SELECT
      api_keys.id,
      api_keys.name,
      api_keys.key_prefix,
      api_keys.status,
      api_keys.route_strategy_id,
      route_strategies.name AS route_strategy_name,
      route_strategies.mode AS route_strategy_mode,
      route_strategies.status AS route_strategy_status,
      api_keys.expires_at,
      api_keys.availability_schedule_json,
      ${apiKeyRevisionSelectExpression(client)}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    INNER JOIN ${apiKeyTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
    WHERE api_keys.id = ?${scope.clause}
    LIMIT 1
  `, [id, ...scope.params])
  if (!row) return undefined
  return {
    id: row.id,
    name: row.name,
    keyPrefix: row.key_prefix,
    status: row.status,
    routeStrategyId: row.route_strategy_id,
    routeStrategyName: row.route_strategy_name ?? undefined,
    routeStrategyMode: normalizeRouteStrategyMode(row.route_strategy_mode),
    routeStrategyStatus: row.route_strategy_status === 'active' || row.route_strategy_status === 'disabled'
      ? row.route_strategy_status
      : undefined,
    expiresAt: row.expires_at ?? undefined,
    availabilitySchedule: parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json),
    revision: row.updated_at
  }
}

function applyApiKeyMutationRowPatch(
  snapshot: ApiKeyUpdateSnapshot,
  patch: ApiKeyMutationRowPatch
): ApiKeyUpdateView {
  const { revision: _revision, ...current } = snapshot
  return {
    ...current,
    name: patch.name ?? current.name,
    status: patch.status ?? current.status,
    routeStrategyId: patch.routeStrategyId ?? current.routeStrategyId,
    routeStrategyName: patch.routeStrategyId ? patch.routeStrategyName : current.routeStrategyName,
    routeStrategyMode: patch.routeStrategyId ? patch.routeStrategyMode : current.routeStrategyMode,
    routeStrategyStatus: patch.routeStrategyId ? patch.routeStrategyStatus : current.routeStrategyStatus,
    expiresAt: Object.hasOwn(patch, 'expiresAt') ? patch.expiresAt ?? undefined : current.expiresAt,
    availabilitySchedule: Object.hasOwn(patch, 'availabilitySchedule')
      ? patch.availabilitySchedule ?? undefined
      : current.availabilitySchedule
  }
}

function apiKeyPatchSelectColumns(client: DatabaseClient, input: Record<string, unknown>): string {
  const columns = new Set<string>([
    'api_keys.id',
    'api_keys.system_account_id',
    'api_keys.name',
    apiKeyRevisionSelectExpression(client)
  ])
  const hasInput = (field: string): boolean => Object.hasOwn(input, field)
  const hasNameInput = hasInput('name')
  const hasRouteStrategyInput = hasInput('routeStrategyId')
  const hasStatusInput = hasInput('status')
  const hasExpiresAtInput = hasInput('expiresAt')
  const hasQuotaLimitsInput = hasInput('quotaLimits')
  const hasScheduleInput = isApiKeyAvailabilityScheduleInputPresent(input)

  if (hasNameInput || hasRouteStrategyInput) {
    columns.add('api_keys.is_default')
    columns.add('api_keys.purpose')
  }
  if (hasInput('description')) columns.add('api_keys.description')
  if (hasRouteStrategyInput) columns.add('api_keys.route_strategy_id')
  if (hasStatusInput || hasQuotaLimitsInput || hasScheduleInput) {
    columns.add('api_keys.status')
    columns.add('api_keys.quota_limits_json')
  }
  if (hasExpiresAtInput) columns.add('api_keys.expires_at')
  if (hasScheduleInput) columns.add('api_keys.availability_schedule_json')
  if (hasRouteStrategyInput || hasStatusInput || hasExpiresAtInput || hasQuotaLimitsInput || hasScheduleInput) {
    columns.add('api_keys.key_hash')
  }
  return [...columns].join(',\n          ')
}

function requiredApiKeyMutationColumn<K extends keyof ApiKeyMutationRow>(
  row: ApiKeyMutationRow,
  column: K
): Exclude<ApiKeyMutationRow[K], undefined> {
  if (!Object.hasOwn(row, column)) {
    throw new Error(`API Key PATCH 内部投影缺少字段：${String(column)}`)
  }
  return row[column] as Exclude<ApiKeyMutationRow[K], undefined>
}

export async function patchApiKeyAsync(
  id: string,
  input: Record<string, unknown>,
  expectedRevision: string,
  access?: AccessScope
): Promise<ApiKeyPatchOutcome | undefined> {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 更新参数')
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  let outcome: ApiKeyPatchOutcome | undefined
  let committedKeyHash: string | undefined

  try {
    outcome = await client.transaction(async (tx) => {
      const lockClause = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
      const current = await tx.one<ApiKeyMutationRow>(`
        SELECT
          ${apiKeyPatchSelectColumns(tx, input)}
        FROM ${apiKeyTable(tx, 'api_keys')} api_keys
        WHERE api_keys.id = ?${scope.clause}
        LIMIT 1${lockClause}
      `, [id, ...scope.params])
      if (!current) return undefined
      if (current.updated_at !== expectedRevision) {
        throw new ApiKeyRevisionConflictError(current.updated_at)
      }

      const changedFields: ApiKeyMutableField[] = []
      const rowPatch: ApiKeyMutationRowPatch = { revision: current.updated_at }
      const before: Partial<Record<ApiKeyMutableField, unknown>> = {}
      const after: Partial<Record<ApiKeyMutableField, unknown>> = {}
      const setClauses: string[] = []
      const setParams: unknown[] = []
      let nextName = current.name
      const hasStatusInput = Object.hasOwn(input, 'status')
      const hasQuotaLimitsInput = Object.hasOwn(input, 'quotaLimits')
      const hasScheduleInput = isApiKeyAvailabilityScheduleInputPresent(input)
      let nextStatus = hasStatusInput || hasQuotaLimitsInput || hasScheduleInput
        ? requiredApiKeyMutationColumn(current, 'status')
        : undefined
      let nextQuotaLimitsJson = hasStatusInput || hasQuotaLimitsInput || hasScheduleInput
        ? requiredApiKeyMutationColumn(current, 'quota_limits_json')
        : undefined
      let nextAvailabilitySchedule = hasScheduleInput
        ? parseApiKeyAvailabilityScheduleJson(requiredApiKeyMutationColumn(current, 'availability_schedule_json'))
        : undefined
      const mutationNow = new Date()

      const addChange = (field: ApiKeyMutableField, previous: unknown, next: unknown): void => {
        changedFields.push(field)
        before[field] = previous
        after[field] = next
      }

      if (Object.hasOwn(input, 'name')) {
        const value = normalizedApiKeyName(input.name)
        assertApiKeyNameChangeAllowed({
          name: current.name,
          isDefault: normalizeApiKeyDefaultFlag(requiredApiKeyMutationColumn(current, 'is_default')),
          purpose: requiredApiKeyMutationColumn(current, 'purpose') === 'chat' ? 'chat' : 'general'
        }, value)
        if (value !== current.name) {
          nextName = value
          addChange('name', current.name, value)
          setClauses.push('name = ?')
          setParams.push(value)
          rowPatch.name = value
        }
      }

      if (Object.hasOwn(input, 'description')) {
        const value = normalizeOptionalApiKeyDescription(input.description)
        const previous = requiredApiKeyMutationColumn(current, 'description') ?? undefined
        if (value !== previous) {
          addChange('description', previous, value)
          setClauses.push('description = ?')
          setParams.push(value ?? null)
          rowPatch.description = value ?? null
        }
      }

      if (Object.hasOwn(input, 'routeStrategyId')) {
        const candidate = typeof input.routeStrategyId === 'string' ? input.routeStrategyId.trim() : input.routeStrategyId
        const currentRouteStrategyId = requiredApiKeyMutationColumn(current, 'route_strategy_id')
        if (candidate !== currentRouteStrategyId) {
          if (
            normalizeApiKeyDefaultFlag(requiredApiKeyMutationColumn(current, 'is_default'))
            && requiredApiKeyMutationColumn(current, 'purpose') !== 'chat'
          ) {
            throw new Error('默认 API Key 不允许更换策略路由')
          }
          const routeStrategyId = await assertRouteStrategySelectableForApiKeyAsync(
            current.system_account_id,
            candidate,
            tx,
            true
          )
          const routeStrategy = await apiKeyRouteStrategyReferenceAsync(tx, current.system_account_id, routeStrategyId)
          if (!routeStrategy) throw new Error('API Key 绑定的策略路由不存在或不属于当前用户')
          addChange('routeStrategyId', currentRouteStrategyId, routeStrategyId)
          setClauses.push('route_strategy_id = ?')
          setParams.push(routeStrategyId)
          Object.assign(rowPatch, {
            routeStrategyId,
            routeStrategyName: routeStrategy.name,
            routeStrategyMode: routeStrategy.mode,
            routeStrategyStatus: routeStrategy.status
          })
        }
      }

      if (Object.hasOwn(input, 'expiresAt')) {
        const value = normalizeOptionalApiKeyExpiresAt(input.expiresAt)
        const previous = requiredApiKeyMutationColumn(current, 'expires_at') ?? undefined
        if (value !== previous) {
          addChange('expiresAt', previous, value)
          setClauses.push('expires_at = ?')
          setParams.push(value ?? null)
          rowPatch.expiresAt = value ?? null
        }
      }

      if (hasQuotaLimitsInput) {
        const currentQuotaLimitsJson = requiredApiKeyMutationColumn(current, 'quota_limits_json')
        const currentQuotaLimits = parseRequestQuotaLimitsJson(currentQuotaLimitsJson)
        const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits, currentQuotaLimits)
        const quotaLimitsJson = requestQuotaLimitsJson(quotaLimits)
        if (quotaLimitsJson !== currentQuotaLimitsJson) {
          nextQuotaLimitsJson = quotaLimitsJson
          addChange('quotaLimits', currentQuotaLimits, quotaLimits)
          setClauses.push('quota_limits_json = ?')
          setParams.push(quotaLimitsJson)
          rowPatch.quotaLimits = quotaLimits
        }
      }

      if (hasScheduleInput) {
        const schedule = apiKeyAvailabilityScheduleFromRequest(input)
        if (apiKeyAvailabilityScheduleJson(schedule) !== apiKeyAvailabilityScheduleJson(nextAvailabilitySchedule)) {
          addChange('availabilitySchedule', nextAvailabilitySchedule, schedule)
          nextAvailabilitySchedule = schedule
          setClauses.push('availability_schedule_json = ?', 'availability_schedule_next_check_at = ?')
          setParams.push(
            apiKeyAvailabilityScheduleJson(schedule),
            nextApiKeyAvailabilityScheduleCheckAt(schedule, mutationNow)
          )
          rowPatch.availabilitySchedule = schedule ?? null
        }
      }

      if (hasStatusInput || hasScheduleInput) {
        const currentStatus = requiredApiKeyMutationColumn(current, 'status')
        const requestedStatus = hasStatusInput
          ? normalizeApiKeyStatus(input.status, currentStatus)
          : currentStatus
        nextStatus = hasScheduleInput
          ? apiKeyStatusForScheduleMutation({ requestedStatus, schedule: nextAvailabilitySchedule, now: mutationNow })
          : requestedStatus
        if (nextStatus !== currentStatus) {
          addChange('status', currentStatus, nextStatus)
          setClauses.push('status = ?')
          setParams.push(nextStatus)
          rowPatch.status = nextStatus
        }
      }

      if (!changedFields.length) {
        return {
          result: { id: current.id, revision: current.updated_at, changedFields, rowPatch },
          ownerSystemAccountId: current.system_account_id,
          resourceName: current.name,
          before,
          after
        }
      }

      const revision = nextApiKeyRevision(current.updated_at)
      setClauses.push('updated_at = ?')
      setParams.push(revision)
      const updateResult = await tx.execute(`
        UPDATE ${apiKeyTable(tx, 'api_keys')}
        SET ${setClauses.join(', ')}
        WHERE id = ? AND system_account_id = ? AND updated_at = ?
      `, [...setParams, current.id, current.system_account_id, current.updated_at])
      if (updateResult.changes !== 1) {
        throw new ApiKeyRevisionConflictError(current.updated_at)
      }

      const quotaChanged = changedFields.includes('quotaLimits')
      const statusChanged = changedFields.includes('status')
      if (quotaChanged || statusChanged) {
        await syncApiKeyRequestQuotaHourlyWindowScopeBindingForClientAsync(tx, {
          apiKeyId: current.id,
          systemAccountId: current.system_account_id,
          limitsJson: nextQuotaLimitsJson,
          active: nextStatus === 'active'
        }, revision)
      }

      rowPatch.revision = revision
      if (changedFields.some((field) => ['routeStrategyId', 'status', 'expiresAt', 'quotaLimits'].includes(field))) {
        committedKeyHash = requiredApiKeyMutationColumn(current, 'key_hash')
      }
      return {
        result: { id: current.id, revision, changedFields, rowPatch },
        ownerSystemAccountId: current.system_account_id,
        resourceName: nextName,
        before,
        after
      }
    })
  } catch (error) {
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${normalizedApiKeyName(input.name)}`)
    }
    throw error
  }

  if (!outcome || !outcome.result.changedFields.length) return outcome
  const changed = new Set(outcome.result.changedFields)
  const validationChanged = ['routeStrategyId', 'status', 'expiresAt', 'quotaLimits']
    .some((field) => changed.has(field as ApiKeyMutableField))
  if (validationChanged) {
    outcome.validationCacheError = await invalidateRequiredApiKeyValidationCacheAsync(
      id,
      'api_key_updated',
      committedKeyHash ? [committedKeyHash] : []
    )
  }
  await invalidateCommittedApiKeyCachesBestEffortAsync({
    apiKeyId: id,
    reason: 'api_key_updated',
    lookup: changed.has('name'),
    quota: changed.has('quotaLimits'),
    quotaReason: 'api_key_quota_updated'
  })
  return outcome
}

function apiKeyStatusForScheduleMutation(input: {
  requestedStatus: 'active' | 'disabled'
  schedule: ApiKeySummary['availabilitySchedule']
  now: Date
}): 'active' | 'disabled' {
  return apiKeyAvailabilityScheduleStatus(input.schedule, input.now) ?? input.requestedStatus
}

async function apiKeyRouteStrategyReferenceAsync(
  client: DatabaseClient,
  systemAccountId: string,
  routeStrategyId: string
): Promise<{
    id: string
    name: string
    mode: RouteStrategyMode
    status: RouteStrategyStatus
  } | undefined> {
  return client.one<{
    id: string
    name: string
    mode: RouteStrategyMode
    status: RouteStrategyStatus
  }>(`
    SELECT id, name, mode, status
    FROM ${apiKeyTable(client, 'route_strategies')}
    WHERE id = ? AND system_account_id = ?
    LIMIT 1
  `, [routeStrategyId, systemAccountId])
}

function apiKeyRouteStrategyReference(
  database: ReturnType<typeof getBusinessDatabase>,
  systemAccountId: string,
  routeStrategyId: string
): {
    id: string
    name: string
    mode: RouteStrategyMode
    status: RouteStrategyStatus
  } | undefined {
  return database.prepare(`
    SELECT id, name, mode, status
    FROM route_strategies
    WHERE id = ? AND system_account_id = ?
    LIMIT 1
  `).get(routeStrategyId, systemAccountId) as {
    id: string
    name: string
    mode: RouteStrategyMode
    status: RouteStrategyStatus
  } | undefined
}

async function syncApiKeyRequestQuotaHourlyWindowScopeBindingForClientAsync(
  client: DatabaseClient,
  input: {
    apiKeyId: string
    systemAccountId: string
    limitsJson: string | null | undefined
    active: boolean
  },
  timestamp: string
): Promise<void> {
  if (client.driver === 'postgres') {
    await syncApiKeyRequestQuotaHourlyWindowScopeBindingAsync(client, input, timestamp)
    return
  }
  syncApiKeyRequestQuotaHourlyWindowScopeBinding(input, getBusinessDatabase(), timestamp)
}

function nextApiKeyRevision(currentRevision: string): string {
  const currentTimestamp = rfc3339InstantMilliseconds(currentRevision)
  if (currentTimestamp === undefined) throw new Error(`API Key revision 必须是带 Z 或数值 offset 的 RFC3339 时间：${currentRevision}`)
  const nextTimestamp = Math.max(
    Date.now(),
    currentTimestamp + 1
  )
  return apiKeyRevisionFromTimestamp(nextTimestamp)
}

function apiKeyRevisionFromTimestamp(timestamp: number): string {
  return new Date(timestamp).toISOString().replace(
    /(\.\d{3})Z$/,
    (_match, milliseconds: string) => `${milliseconds}000Z`
  )
}

async function invalidateCommittedApiKeyCachesBestEffortAsync(input: {
  apiKeyId: string
  reason: string
  lookup?: boolean
  quota?: boolean
  quotaReason?: string
}): Promise<void> {
  const effects: Array<{ name: string; run: () => void | Promise<void> }> = []
  if (input.lookup) {
    effects.push({ name: 'api_key_lookup', run: () => invalidateApiKeyLookupCache(input.apiKeyId) })
  }
  if (input.quota) {
    effects.push({
      name: 'api_key_quota',
      run: () => notifyApiKeyQuotaCacheInvalidation(input.apiKeyId, input.quotaReason ?? input.reason)
    })
  }
  for (const effect of effects) {
    try {
      await effect.run()
    } catch (error) {
      logger.warn(errorLogFields(error, {
        event: 'api_key_cache_sync_failed_after_commit',
        apiKeyId: input.apiKeyId,
        reason: input.reason,
        cache: effect.name
      }), 'API Key 已提交，但缓存同步失败')
    }
  }
}

async function invalidateRequiredApiKeyValidationCacheAsync(
  apiKeyId: string,
  reason: string,
  keyHashes: readonly string[]
): Promise<ApiKeyValidationCacheInvalidationError | undefined> {
  try {
    await notifyGatewayApiKeyValidationCacheInvalidationAsync(apiKeyId, reason, keyHashes)
    return undefined
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'api_key_validation_cache_sync_failed_after_commit',
      apiKeyId,
      reason,
      cache: 'gateway_api_key'
    }), 'API Key 已提交，但 validation cache 必需失效重试耗尽')
    return new ApiKeyValidationCacheInvalidationError(apiKeyId, error)
  }
}

export function refreshApiKeySecret(id: string, access?: AccessScope): ApiKeyCreateResult | undefined {
  const database = getBusinessDatabase()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const transactionStarted = beginDatabaseTransaction(database)
  let row: ApiKeyRefreshRow | undefined
  let revision = ''
  try {
    row = database.prepare(`
      SELECT id, system_account_id, name, key_hash, key_prefix, key_suffix, updated_at
      FROM api_keys
      WHERE id = ?${scope.clause}
      LIMIT 1
    `).get(id, ...scope.params) as unknown as ApiKeyRefreshRow | undefined
    if (row) {
      revision = nextApiKeyRevision(row.updated_at)
      const result = database.prepare(`
        UPDATE api_keys
        SET key_hash = ?, key_prefix = ?, key_suffix = ?, key_secret_encrypted = ?, updated_at = ?
        WHERE id = ? AND system_account_id = ? AND updated_at = ?
      `).run(
        hashSecret(key),
        keyPrefix,
        keySuffix,
        encryptJson({ key }),
        revision,
        row.id,
        row.system_account_id,
        row.updated_at
      )
      if (result.changes !== 1) throw new ApiKeyRevisionConflictError(row.updated_at)
    }
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
  if (!row) return undefined
  invalidateGatewayApiKeyCacheById(id)
  return { id: row.id, key, keyPrefix, keySuffix, revision }
}

export async function refreshApiKeySecretAsync(id: string, access?: AccessScope): Promise<ApiKeyCreateResult | undefined> {
  const outcome = await refreshApiKeySecretForManagementAsync(id, access)
  if (outcome?.validationCacheError) throw outcome.validationCacheError
  return outcome?.result
}

export async function refreshApiKeySecretForManagementAsync(
  id: string,
  access?: AccessScope
): Promise<ApiKeyRefreshOutcome | undefined> {
  const key = createApiKey()
  const keyHash = hashSecret(key)
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  let previousKeyHash: string | undefined
  const outcome: ApiKeyRefreshOutcome | undefined = await client.transaction(async (tx) => {
    const lockClause = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const row = await tx.one<ApiKeyRefreshRow>(`
      SELECT id, system_account_id, name, key_hash, key_prefix, key_suffix, ${apiKeyRevisionSelectExpression(tx)}
      FROM ${apiKeyTable(tx, 'api_keys')} api_keys
      WHERE id = ?${scope.clause}
      LIMIT 1${lockClause}
    `, [id, ...scope.params])
    if (!row) return undefined
    previousKeyHash = row.key_hash
    const revision = nextApiKeyRevision(row.updated_at)
    const result = await tx.execute(`
      UPDATE ${apiKeyTable(tx, 'api_keys')}
      SET key_hash = ?, key_prefix = ?, key_suffix = ?, key_secret_encrypted = ?, updated_at = ?
      WHERE id = ? AND system_account_id = ? AND updated_at = ?
    `, [
      keyHash,
      keyPrefix,
      keySuffix,
      encryptJson({ key }),
      revision,
      row.id,
      row.system_account_id,
      row.updated_at
    ])
    if (result.changes !== 1) throw new ApiKeyRevisionConflictError(row.updated_at)
    return {
      result: { id: row.id, key, keyPrefix, keySuffix, revision },
      ownerSystemAccountId: row.system_account_id,
      resourceName: row.name,
      previousKeyPrefix: row.key_prefix,
      previousKeySuffix: row.key_suffix
    }
  })
  if (!outcome) return undefined
  outcome.validationCacheError = await invalidateRequiredApiKeyValidationCacheAsync(
    id,
    'api_key_secret_refreshed',
    previousKeyHash ? [previousKeyHash, keyHash] : [keyHash]
  )
  return outcome
}

export interface ApiKeyDeleteCleanupTarget {
  apiKeyId: string
  systemAccountId: string
}

export type ApiKeyDeleteResult = {
  deleted: false
  cleanupTarget?: undefined
  ownerSystemAccountId?: undefined
  resourceName?: undefined
} | {
  deleted: true
  cleanupTarget: ApiKeyDeleteCleanupTarget
  ownerSystemAccountId: string
  resourceName: string
  validationCacheError?: ApiKeyValidationCacheInvalidationError
}

export function deleteApiKey(id: string, access?: AccessScope): boolean {
  return deleteApiKeyWithRelatedCleanup(id, access).deleted
}

export async function deleteApiKeyAsync(id: string, access?: AccessScope): Promise<boolean> {
  const outcome = await deleteApiKeyWithRelatedCleanupAsync(id, access)
  if (outcome.deleted && outcome.validationCacheError) throw outcome.validationCacheError
  return outcome.deleted
}

export function deleteApiKeyWithRelatedCleanup(id: string, access?: AccessScope): ApiKeyDeleteResult {
  const scope = buildSystemAccountScopeClause(access)
  const database = getBusinessDatabase()
  const row = database
    .prepare(`SELECT id, system_account_id, name, key_hash, is_default, purpose FROM api_keys WHERE id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeyDeleteRow | undefined
  if (!row) return { deleted: false }
  assertApiKeyNotDefault(row)

  const transactionStarted = beginDatabaseTransaction(database)
  let deleted = false
  try {
    syncApiKeyRequestQuotaHourlyWindowScopeBinding({
      apiKeyId: row.id,
      systemAccountId: row.system_account_id,
      limitsJson: null,
      active: false
    }, database)
    const result = database
      .prepare('DELETE FROM api_keys WHERE id = ? AND system_account_id = ?')
      .run(row.id, row.system_account_id)
    deleted = result.changes > 0
    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    rollbackDatabaseTransaction(database, transactionStarted)
    throw error
  }
  if (deleted) {
    invalidateGatewayApiKeyCacheById(row.id)
    invalidateApiKeyLookupCache(row.id)
    notifyApiKeyQuotaCacheInvalidation(row.id, 'api_key_deleted')
  }
  if (!deleted) return { deleted: false }
  return {
    deleted: true,
    cleanupTarget: { apiKeyId: row.id, systemAccountId: row.system_account_id },
    ownerSystemAccountId: row.system_account_id,
    resourceName: row.name
  }
}

export async function deleteApiKeyWithRelatedCleanupAsync(id: string, access?: AccessScope): Promise<ApiKeyDeleteResult> {
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const outcome = await client.transaction(async (tx) => {
    const lockClause = tx.driver === 'postgres' ? ' FOR UPDATE' : ''
    const row = await tx.one<ApiKeyDeleteRow>(`
      SELECT api_keys.id, api_keys.system_account_id, api_keys.name, api_keys.key_hash, api_keys.is_default, api_keys.purpose
      FROM ${apiKeyTable(tx, 'api_keys')} api_keys
      WHERE api_keys.id = ?${scope.clause}
      LIMIT 1${lockClause}
    `, [id, ...scope.params])
    if (!row) return undefined
    assertApiKeyNotDefault(row)
    const cleanupTarget = { apiKeyId: row.id, systemAccountId: row.system_account_id }
    const result = await tx.execute(`
      DELETE FROM ${apiKeyTable(tx, 'api_keys')}
      WHERE id = ? AND system_account_id = ?
    `, [row.id, row.system_account_id])
    if (result.changes !== 1) return undefined
    await syncApiKeyRequestQuotaHourlyWindowScopeBindingForClientAsync(tx, {
      apiKeyId: row.id,
      systemAccountId: row.system_account_id,
      limitsJson: null,
      active: false
    }, nowIso())
    if (tx.driver === 'postgres') {
      await registerDeletedApiKeyRecordCleanupTargetInClientAsync(tx, cleanupTarget)
    }
    return { row, cleanupTarget }
  })
  if (!outcome) return { deleted: false }

  const validationCacheError = await invalidateRequiredApiKeyValidationCacheAsync(
    outcome.row.id,
    'api_key_deleted',
    [outcome.row.key_hash]
  )
  await invalidateCommittedApiKeyCachesBestEffortAsync({
    apiKeyId: outcome.row.id,
    reason: 'api_key_deleted',
    lookup: true,
    quota: true
  })
  return {
    deleted: true,
    cleanupTarget: outcome.cleanupTarget,
    ownerSystemAccountId: outcome.row.system_account_id,
    resourceName: outcome.row.name,
    validationCacheError
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

export function ensureChatApiKeyForSystemAccount(systemAccountId: string, timestamp = nowIso()): string {
  ensureDefaultRouteStrategiesForSystemAccount(systemAccountId, timestamp)
  const database = getBusinessDatabase()
  const existing = chatApiKeyIdForSystemAccount(database, systemAccountId)
  if (existing) return existing
  const routeStrategy = defaultGptRouteStrategyForSystemAccount(database, systemAccountId)
  if (!routeStrategy) throw new Error('创建 AI 对话 API Key 前必须先创建 GPT 默认策略路由')
  const apiKeyId = newId('key')
  const key = createApiKey()
  const name = nextDefaultApiKeyName(database, systemAccountId, 'AI 对话 API Key')
  try {
    database.prepare(`
      INSERT INTO api_keys (
        id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
        key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json,
        availability_schedule_next_check_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0, 'chat', NULL, NULL, NULL, NULL, ?, ?)
    `).run(
      apiKeyId,
      systemAccountId,
      routeStrategy.id,
      name,
      `AI 对话专用 API Key，默认绑定${routeStrategy.name}，可在 API Key 页面修改策略路由。`,
      hashSecret(key),
      key.slice(0, 8),
      key.slice(-8),
      encryptJson({ key }),
      timestamp,
      timestamp
    )
    return apiKeyId
  } catch (error) {
    const raced = chatApiKeyIdForSystemAccount(database, systemAccountId)
    if (raced && (isDuplicateApiKeyNameError(error) || isDuplicateChatApiKeyError(error))) return raced
    throw error
  }
}

export async function ensureChatApiKeyForSystemAccountAsync(systemAccountId: string, timestamp = nowIso(), client?: DatabaseClient): Promise<string> {
  const databaseClient = client ?? await getApiKeyDatabaseClient()
  await ensureDefaultRouteStrategiesForSystemAccountAsync(databaseClient, systemAccountId, timestamp)
  const existing = await chatApiKeyIdForSystemAccountAsync(databaseClient, systemAccountId)
  if (existing) return existing
  const routeStrategy = await defaultGptRouteStrategyForSystemAccountAsync(databaseClient, systemAccountId)
  if (!routeStrategy) throw new Error('创建 AI 对话 API Key 前必须先创建 GPT 默认策略路由')
  const apiKeyId = newId('key')
  const key = createApiKey()
  const name = await nextDefaultApiKeyNameAsync(databaseClient, systemAccountId, 'AI 对话 API Key')
  try {
    await databaseClient.execute(`
      INSERT INTO ${apiKeyTable(databaseClient, 'api_keys')} (
        id, system_account_id, route_strategy_id, name, description, key_hash, key_prefix, key_suffix,
        key_secret_encrypted, status, is_default, purpose, expires_at, quota_limits_json, availability_schedule_json,
        availability_schedule_next_check_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', 0, 'chat', NULL, NULL, NULL, NULL, ?, ?)
    `, [
      apiKeyId,
      systemAccountId,
      routeStrategy.id,
      name,
      `AI 对话专用 API Key，默认绑定${routeStrategy.name}，可在 API Key 页面修改策略路由。`,
      hashSecret(key),
      key.slice(0, 8),
      key.slice(-8),
      encryptJson({ key }),
      timestamp,
      timestamp
    ])
    return apiKeyId
  } catch (error) {
    const raced = await chatApiKeyIdForSystemAccountAsync(databaseClient, systemAccountId)
    if (raced && (isDuplicateApiKeyNameError(error) || isDuplicateChatApiKeyError(error))) return raced
    throw error
  }
}

function chatApiKeyIdForSystemAccount(database: ReturnType<typeof getBusinessDatabase>, systemAccountId: string): string | undefined {
  const row = database.prepare(`
    SELECT id FROM api_keys WHERE system_account_id = ? AND purpose = 'chat' LIMIT 1
  `).get(systemAccountId) as { id?: string } | undefined
  return row?.id
}

async function chatApiKeyIdForSystemAccountAsync(client: DatabaseClient, systemAccountId: string): Promise<string | undefined> {
  const row = await client.one<{ id?: string }>(`
    SELECT id FROM ${apiKeyTable(client, 'api_keys')} WHERE system_account_id = ? AND purpose = 'chat' LIMIT 1
  `, [systemAccountId])
  return row?.id
}

function defaultGptRouteStrategyForSystemAccount(
  database: ReturnType<typeof getBusinessDatabase>,
  systemAccountId: string
): { id: string; name: string } | undefined {
  return database.prepare(`
    SELECT route_strategies.id, route_strategies.name
    FROM route_strategies
    INNER JOIN route_strategy_groups ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
      AND route_strategy_groups.status = 'active'
    INNER JOIN groups ON groups.id = route_strategy_groups.group_id
      AND groups.system_account_id = route_strategy_groups.system_account_id
      AND groups.enabled = 1
      AND groups.is_default = 1
    WHERE route_strategies.system_account_id = ?
      AND route_strategies.status = 'active'
      AND route_strategies.is_default = 1
      AND groups.provider_code = ?
    ORDER BY route_strategies.created_at ASC, route_strategies.id ASC
    LIMIT 1
  `).get(systemAccountId, GPT_VENDOR_CODE) as { id: string; name: string } | undefined
}

async function defaultGptRouteStrategyForSystemAccountAsync(
  client: DatabaseClient,
  systemAccountId: string
): Promise<{ id: string; name: string } | undefined> {
  return client.one<{ id: string; name: string }>(`
    SELECT route_strategies.id, route_strategies.name
    FROM ${apiKeyTable(client, 'route_strategies')} route_strategies
    INNER JOIN ${apiKeyTable(client, 'route_strategy_groups')} route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = route_strategies.system_account_id
      AND route_strategy_groups.status = 'active'
    INNER JOIN ${apiKeyTable(client, 'groups')} groups
      ON groups.id = route_strategy_groups.group_id
      AND groups.system_account_id = route_strategy_groups.system_account_id
      AND groups.enabled = 1
      AND groups.is_default = 1
    WHERE route_strategies.system_account_id = ?
      AND route_strategies.status = 'active'
      AND route_strategies.is_default = 1
      AND groups.provider_code = ?
    ORDER BY route_strategies.created_at ASC, route_strategies.id ASC
    LIMIT 1
  `, [systemAccountId, GPT_VENDOR_CODE])
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

function assertApiKeyNotDefault(row: Pick<ApiKeyDeleteRow, 'is_default' | 'purpose'>): void {
  if (row.purpose === 'chat') {
    throw new Error('AI 对话 API Key 不允许删除')
  }
  if (normalizeApiKeyDefaultFlag(row.is_default)) {
    throw new Error('默认 API Key 不允许删除')
  }
}

function assertApiKeyNameChangeAllowed(current: Pick<ApiKeySummary, 'name' | 'isDefault' | 'purpose'>, nextName: string): void {
  if (nextName === current.name) return
  if (current.purpose === 'chat') {
    throw new Error('AI 对话 API Key 不允许修改名称')
  }
  if (current.isDefault) {
    throw new Error('默认 API Key 不允许修改名称')
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

function isDuplicateChatApiKeyError(error: unknown): boolean {
  return error instanceof Error && (
    error.message.includes('idx_api_keys_chat_purpose_unique')
    || error.message.includes('UNIQUE constraint failed: api_keys.system_account_id')
  )
}
