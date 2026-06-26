import type { DatabaseSync } from 'node:sqlite'

import type { ApiKeyGroupBindingSummary, ApiKeySummary, RequestQuotaLimits } from '../domain/types.js'
import {
  normalizeApiKeyGroupBindingWeight,
  normalizeApiKeyGroupRouteStrategy
} from '../domain/api-key-routing.js'
import {
  hybridRoutingConfigJson,
  normalizeApiKeyRouteMode,
  normalizeHybridRoutingConfig
} from '../domain/api-key-hybrid-routing.js'
import {
  explicitHybridRouteRulesJson,
  normalizeApiKeyClientProfile,
  normalizeExplicitHybridRouteRules
} from '../domain/api-key-explicit-hybrid-routing.js'
import { runtimeConfig } from '../config/runtime.js'
import { notifyApiKeyQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { apiKeyGroupOwnerAndProvider, apiKeySystemAccountId, canBindApiKeyGroup, canManageApiKeyOwner } from './api-key-access.js'
import {
  apiKeyAvailabilityScheduleFromRequest,
  apiKeyAvailabilityScheduleJson,
  evaluateApiKeyAvailabilitySchedule,
  isApiKeyAvailabilityScheduleInputPresent,
  nextApiKeyAvailabilityScheduleCheckAt
} from './api-key-availability-schedule.js'
import { maxApiKeyGroupBindings } from './api-key-group-binding-limits.js'
import { createApiKey, encryptJson, hashSecret } from './crypto.js'
import { buildApiKeyFilters, normalizeApiKeyListOptions } from './api-key-list-query.js'
import { apiKeySummariesFromRows, apiKeySummariesFromRowsAsync, type ApiKeyRow } from './api-key-mappers.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { invalidateGatewayApiKeyCacheById } from './gateway-api-key.repository.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { invalidateApiKeyLookupCache, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { rememberRequestQuotaHourlyWindowsFromJson } from './request-quota-hourly-windows.repository.js'
import { emptyRequestQuotaLimits, normalizeRequestQuotaLimits, requestQuotaLimitsJson } from './request-quota-limits.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { optionalServerDateTimeIso, optionalString } from './value-utils.js'

const API_KEY_GROUP_BOUNDARY_ERROR = 'API Key 只能绑定自己的分组或有效授权给自己的分组'
const businessSchemaName = 'juhe_business'
const apiKeyMutationInputKeys = new Set([
  'name',
  'description',
  'groupBindings',
  'routeMode',
  'groupRouteStrategy',
  'hybridRoutingConfig',
  'clientProfile',
  'explicitHybridRouteRules',
  'status',
  'expiresAt',
  'quotaLimits',
  'availabilitySchedule',
  'availabilityScheduleActive'
])
const apiKeyGroupBindingInputKeys = new Set([
  'groupId',
  'priority',
  'weight',
  'status'
])

export interface ApiKeyListOptions {
  page?: number
  pageSize?: number
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  groupId?: string
}

export interface ApiKeyListResult {
  items: ApiKeySummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

type ApiKeyDeleteRow = {
  id: string
  system_account_id: string
}

type ApiKeyGroupBindingStatus = 'active' | 'disabled'

interface ApiKeyGroupBindingInput {
  groupId: string
  priority: number
  weight: number
  status: ApiKeyGroupBindingStatus
}

interface ApiKeyGroupBindingWrite extends ApiKeyGroupBindingInput {
  groupName?: string
  providerCode: string
  providerProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  groupEnabled: boolean
}

interface ApiKeyBindableGroupRow {
  id: string
  system_account_id: string
  provider_code: string
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  name: string | null
  enabled: number
}

export function listApiKeys(access?: AccessScope, options?: ApiKeyListOptions): ApiKeySummary[] {
  return queryApiKeys(access, options).items
}

export async function listApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions): Promise<ApiKeySummary[]> {
  return (await queryApiKeysAsync(access, options)).items
}

export function listApiKeysPage(access?: AccessScope, options?: ApiKeyListOptions): ApiKeyListResult {
  return queryApiKeys(access, options, true)
}

export async function listApiKeysPageAsync(access?: AccessScope, options?: ApiKeyListOptions): Promise<ApiKeyListResult> {
  return queryApiKeysAsync(access, options, true)
}

export function findApiKeySummary(id: string, access?: AccessScope): ApiKeySummary | undefined {
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns()} FROM api_keys LEFT JOIN system_accounts ON system_accounts.id = api_keys.system_account_id WHERE api_keys.id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeyRow | undefined
  return row ? apiKeySummariesFromRows([row], access, { includeSecret: false })[0] : undefined
}

export async function findApiKeySummaryAsync(id: string, access?: AccessScope): Promise<ApiKeySummary | undefined> {
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = await client.one<ApiKeyRow>(`
    SELECT ${apiKeyListColumns()}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    LEFT JOIN ${apiKeyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = api_keys.system_account_id
    WHERE api_keys.id = ?${scope.clause}
  `, [id, ...scope.params])
  return row ? (await apiKeySummariesFromRowsAsync([row], access, { includeSecret: false }))[0] : undefined
}

export function findApiKeySecret(id: string, access?: AccessScope): ApiKeySummary | undefined {
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns({ includeSecret: true })} FROM api_keys LEFT JOIN system_accounts ON system_accounts.id = api_keys.system_account_id WHERE api_keys.id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeyRow | undefined
  return row ? apiKeySummariesFromRows([row], access, { includeSecret: true })[0] : undefined
}

export async function findApiKeySecretAsync(id: string, access?: AccessScope): Promise<ApiKeySummary | undefined> {
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = await client.one<ApiKeyRow>(`
    SELECT ${apiKeyListColumns({ includeSecret: true })}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    LEFT JOIN ${apiKeyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = api_keys.system_account_id
    WHERE api_keys.id = ?${scope.clause}
  `, [id, ...scope.params])
  return row ? (await apiKeySummariesFromRowsAsync([row], access, { includeSecret: true }))[0] : undefined
}

function queryApiKeys(access?: AccessScope, options?: ApiKeyListOptions, paged = false): ApiKeyListResult {
  const normalized = normalizeApiKeyListOptions(options)
  const scope = buildSystemAccountWhereClause(access, 'api_keys.system_account_id')
  const filters = buildApiKeyFilters(scope, normalized)
  const limitClause = paged ? 'LIMIT ? OFFSET ?' : ''
  const limitParams = paged ? [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize] : []
  const rows = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns()} FROM api_keys LEFT JOIN system_accounts ON system_accounts.id = api_keys.system_account_id ${filters.clause} ORDER BY api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC ${limitClause}`)
    .all(...filters.params, ...limitParams) as unknown as ApiKeyRow[]
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = apiKeySummariesFromRows(pageRows.rows, access, { includeSecret: false })
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

async function queryApiKeysAsync(access?: AccessScope, options?: ApiKeyListOptions, paged = false): Promise<ApiKeyListResult> {
  const normalized = normalizeApiKeyListOptions(options)
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountWhereClause(access, 'api_keys.system_account_id')
  const filters = buildApiKeyFiltersForClient(client, scope, normalized)
  const limitClause = paged ? 'LIMIT ? OFFSET ?' : ''
  const limitParams = paged ? [normalized.pageSize + 1, (normalized.page - 1) * normalized.pageSize] : []
  const rows = await client.query<ApiKeyRow>(`
    SELECT ${apiKeyListColumns()}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    LEFT JOIN ${apiKeyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = api_keys.system_account_id
    ${filters.clause}
    ORDER BY api_keys.updated_at DESC, api_keys.created_at DESC, api_keys.id DESC
    ${limitClause}
  `, [...filters.params, ...limitParams])
  const pageRows = paged ? takePageRows(rows, normalized.pageSize) : { rows, hasMore: false }
  const items = await apiKeySummariesFromRowsAsync(pageRows.rows, access, { includeSecret: false })
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

function apiKeyListColumns(options: { includeSecret?: boolean } = {}): string {
  const columns = [
    'api_keys.id',
    'api_keys.system_account_id',
    'api_keys.name',
    'api_keys.description',
    'api_keys.key_prefix',
    'api_keys.key_suffix',
    'api_keys.status',
    'api_keys.client_profile',
    'api_keys.route_mode',
    'api_keys.group_route_strategy',
    'api_keys.hybrid_routing_config_json',
    'api_keys.explicit_hybrid_route_rules_json',
    'system_accounts.display_name AS group_owner_system_account_name',
    'api_keys.expires_at',
    'api_keys.quota_limits_json',
    'api_keys.availability_schedule_json',
    'api_keys.availability_schedule_active'
  ]
  if (options.includeSecret) {
    columns.splice(5, 0, 'api_keys.key_secret_encrypted')
  }
  return columns.join(', ')
}

export function createApiKeyRecord(input: Record<string, unknown>, access?: AccessScope): ApiKeySummary & { key: string } {
  assertKnownInputKeys(input, apiKeyMutationInputKeys, 'API Key 创建参数')
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const scopedOwnerId = manageableSystemAccountId(access)
  let systemAccountId = scopedOwnerId ?? currentSystemAccountId(access)
  const rawBindings = apiKeyGroupBindingInputsFromRequest(input)
  if (!rawBindings) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  const routeMode = normalizeApiKeyRouteMode(input.routeMode)
  const clientProfile = normalizeApiKeyClientProfile(input.clientProfile)
  const firstExplicitGroupId = rawBindings?.[0]?.groupId
  const firstGroup = firstExplicitGroupId ? apiKeyGroupOwnerAndProvider(firstExplicitGroupId) : undefined
  if (firstGroup && !scopedOwnerId && canManageApiKeyOwner(firstGroup.systemAccountId, access)) {
    systemAccountId = firstGroup.systemAccountId
  }
  const bindings = normalizeApiKeyGroupBindings(rawBindings, systemAccountId)
  const hybridRoutingConfig = normalizeApiKeyHybridRoutingConfigForWrite(input.hybridRoutingConfig, routeMode, bindings)
  const explicitHybridRouteRules = normalizeApiKeyExplicitHybridRouteRulesForWrite(input.explicitHybridRouteRules, bindings)
  const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits)
  const availabilitySchedule = apiKeyAvailabilityScheduleFromRequest(input)
  const hasAvailabilityScheduleActiveInput = Object.prototype.hasOwnProperty.call(input, 'availabilityScheduleActive')
  const availabilityScheduleActive = hasAvailabilityScheduleActiveInput
    ? normalizeApiKeyAvailabilityScheduleActiveOverride(input.availabilityScheduleActive, availabilitySchedule)
    : apiKeyAvailabilityScheduleActiveValue(availabilitySchedule, nowDate)
  const groupRouteStrategy = normalizeApiKeyGroupRouteStrategy(input.groupRouteStrategy)
  const groupBindings = apiKeyGroupBindingSummariesForRecord(recordlessBindingPrefix(), bindings)
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name: normalizedApiKeyName(input.name),
    description: normalizeOptionalApiKeyDescription(input.description),
    keyPrefix,
    keySuffix,
    status: normalizeApiKeyStatus(input.status, 'active'),
    clientProfile,
    routeMode,
    groupRouteStrategy,
    hybridRoutingConfig,
    explicitHybridRouteRules,
    groupBindings,
    expiresAt: normalizeOptionalApiKeyExpiresAt(input.expiresAt),
    quotaLimits,
    availabilitySchedule,
    availabilityScheduleActive: availabilitySchedule?.enabled ? availabilityScheduleActive !== 0 : undefined,
    usage: emptyAccountUsageSummary(),
    key
  }
  assertApiKeyNameAvailable(systemAccountId, record.name)
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const quotaLimitsJson = requestQuotaLimitsJson(record.quotaLimits)
    const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(record.availabilitySchedule, nowDate)
    const insertColumns = [
      'id',
      'system_account_id',
      'name',
      'description',
      'key_hash',
      'key_prefix',
      'key_suffix',
      'key_secret_encrypted',
      'status',
      'client_profile',
      'route_mode',
      'group_route_strategy',
      'hybrid_routing_config_json',
      'explicit_hybrid_route_rules_json',
      'expires_at',
      'quota_limits_json',
      'availability_schedule_json',
      'availability_schedule_active',
      'availability_schedule_next_check_at',
      'created_at',
      'updated_at'
    ]
    const insertValues = [
      record.id,
      systemAccountId,
      record.name,
      record.description ?? null,
      hashSecret(key),
      record.keyPrefix,
      record.keySuffix,
      encryptJson({ key }),
      record.status,
      record.clientProfile,
      record.routeMode,
      record.groupRouteStrategy,
      hybridRoutingConfigJson(record.hybridRoutingConfig),
      explicitHybridRouteRulesJson(record.explicitHybridRouteRules),
      record.expiresAt ?? null,
      quotaLimitsJson,
      apiKeyAvailabilityScheduleJson(record.availabilitySchedule),
      availabilityScheduleActive,
      availabilityScheduleNextCheckAt,
      now,
      now
    ]
    database
      .prepare(`
        INSERT INTO api_keys (${insertColumns.join(', ')})
        VALUES (${insertColumns.map(() => '?').join(', ')})
      `)
      .run(...insertValues)
    rememberRequestQuotaHourlyWindowsFromJson(quotaLimitsJson, database, now)
    replaceApiKeyGroupBindings(database, record.id, systemAccountId, bindings, now)
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
  const scopedOwnerId = manageableSystemAccountId(access)
  let systemAccountId = scopedOwnerId ?? currentSystemAccountId(access)
  const rawBindings = apiKeyGroupBindingInputsFromRequest(input)
  if (!rawBindings) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  const routeMode = normalizeApiKeyRouteMode(input.routeMode)
  const clientProfile = normalizeApiKeyClientProfile(input.clientProfile)
  const firstExplicitGroupId = rawBindings?.[0]?.groupId
  const firstGroup = firstExplicitGroupId ? await apiKeyGroupOwnerAndProviderAsync(firstExplicitGroupId) : undefined
  if (firstGroup && !scopedOwnerId && canManageApiKeyOwner(firstGroup.systemAccountId, access)) {
    systemAccountId = firstGroup.systemAccountId
  }
  const bindings = await normalizeApiKeyGroupBindingsAsync(rawBindings, systemAccountId)
  const hybridRoutingConfig = normalizeApiKeyHybridRoutingConfigForWrite(input.hybridRoutingConfig, routeMode, bindings)
  const explicitHybridRouteRules = normalizeApiKeyExplicitHybridRouteRulesForWrite(input.explicitHybridRouteRules, bindings)
  const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits)
  const availabilitySchedule = apiKeyAvailabilityScheduleFromRequest(input)
  const hasAvailabilityScheduleActiveInput = Object.prototype.hasOwnProperty.call(input, 'availabilityScheduleActive')
  const availabilityScheduleActive = hasAvailabilityScheduleActiveInput
    ? normalizeApiKeyAvailabilityScheduleActiveOverride(input.availabilityScheduleActive, availabilitySchedule)
    : apiKeyAvailabilityScheduleActiveValue(availabilitySchedule, nowDate)
  const groupRouteStrategy = normalizeApiKeyGroupRouteStrategy(input.groupRouteStrategy)
  const groupBindings = apiKeyGroupBindingSummariesForRecord(recordlessBindingPrefix(), bindings)
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: undefined,
    name: normalizedApiKeyName(input.name),
    description: normalizeOptionalApiKeyDescription(input.description),
    keyPrefix,
    keySuffix,
    status: normalizeApiKeyStatus(input.status, 'active'),
    clientProfile,
    routeMode,
    groupRouteStrategy,
    hybridRoutingConfig,
    explicitHybridRouteRules,
    groupBindings,
    expiresAt: normalizeOptionalApiKeyExpiresAt(input.expiresAt),
    quotaLimits,
    availabilitySchedule,
    availabilityScheduleActive: availabilitySchedule?.enabled ? availabilityScheduleActive !== 0 : undefined,
    usage: emptyAccountUsageSummary(),
    key
  }
  const client = await getApiKeyDatabaseClient()
  try {
    await client.transaction(async (tx) => {
      await assertApiKeyNameAvailableAsync(tx, systemAccountId, record.name)
      const quotaLimitsJson = requestQuotaLimitsJson(record.quotaLimits)
      const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(record.availabilitySchedule, nowDate)
      const insertColumns = [
        'id',
        'system_account_id',
        'name',
        'description',
        'key_hash',
        'key_prefix',
        'key_suffix',
        'key_secret_encrypted',
        'status',
        'client_profile',
        'route_mode',
        'group_route_strategy',
        'hybrid_routing_config_json',
        'explicit_hybrid_route_rules_json',
        'expires_at',
        'quota_limits_json',
        'availability_schedule_json',
        'availability_schedule_active',
        'availability_schedule_next_check_at',
        'created_at',
        'updated_at'
      ]
      const insertValues = [
        record.id,
        systemAccountId,
        record.name,
        record.description ?? null,
        hashSecret(key),
        record.keyPrefix,
        record.keySuffix,
        encryptJson({ key }),
        record.status,
        record.clientProfile,
        record.routeMode,
        record.groupRouteStrategy,
        hybridRoutingConfigJson(record.hybridRoutingConfig),
        explicitHybridRouteRulesJson(record.explicitHybridRouteRules),
        record.expiresAt ?? null,
        quotaLimitsJson,
        apiKeyAvailabilityScheduleJson(record.availabilitySchedule),
        availabilityScheduleActive,
        availabilityScheduleNextCheckAt,
        now,
        now
      ]
      await tx.execute(`
        INSERT INTO ${apiKeyTable(tx, 'api_keys')} (${insertColumns.join(', ')})
        VALUES (${insertColumns.map(() => '?').join(', ')})
      `, insertValues)
      await rememberRequestQuotaHourlyWindowsFromLimitsAsync(tx, record.quotaLimits, now)
      await replaceApiKeyGroupBindingsAsync(tx, record.id, systemAccountId, bindings, now)
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
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) {
    return undefined
  }
  const currentRow = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns()} FROM api_keys LEFT JOIN system_accounts ON system_accounts.id = api_keys.system_account_id WHERE api_keys.id = ? AND api_keys.system_account_id = ?`)
    .get(id, systemAccountId) as unknown as ApiKeyRow | undefined
  const current = currentRow ? apiKeySummariesFromRows([currentRow], { systemAccountId, role: 'user' }, { includeSecret: false })[0] : undefined
  if (!current) {
    return undefined
  }

  const hasRouteModeInput = Object.prototype.hasOwnProperty.call(input, 'routeMode')
  const nextRouteMode = hasRouteModeInput
    ? normalizeApiKeyRouteMode(input.routeMode)
    : current.routeMode
  const hasBindingInput = hasApiKeyGroupBindingInput(input)
  const nextBindings = hasBindingInput
    ? normalizeApiKeyGroupBindings(apiKeyGroupBindingInputsFromRequest(input) ?? [], systemAccountId, {
      retainableGroupIds: current.groupBindings.map((binding) => binding.groupId)
    })
    : undefined
  const effectiveBindings = nextBindings ?? current.groupBindings.map((binding) => ({
    groupId: binding.groupId,
    status: binding.status,
    groupEnabled: binding.groupEnabled
  }))
  const hasHybridRoutingConfigInput = Object.prototype.hasOwnProperty.call(input, 'hybridRoutingConfig')
  const nextHybridRoutingConfig = (hasHybridRoutingConfigInput || hasRouteModeInput || hasBindingInput)
    ? normalizeApiKeyHybridRoutingConfigForWrite(
      hasHybridRoutingConfigInput ? input.hybridRoutingConfig : current.hybridRoutingConfig,
      nextRouteMode,
      effectiveBindings
    )
    : current.hybridRoutingConfig
  const hasExplicitHybridRouteRulesInput = Object.prototype.hasOwnProperty.call(input, 'explicitHybridRouteRules')
  const nextExplicitHybridRouteRules = (hasExplicitHybridRouteRulesInput || hasBindingInput)
    ? normalizeApiKeyExplicitHybridRouteRulesForWrite(
      hasExplicitHybridRouteRulesInput ? input.explicitHybridRouteRules : current.explicitHybridRouteRules,
      effectiveBindings
    )
    : current.explicitHybridRouteRules
  const hasClientProfileInput = Object.prototype.hasOwnProperty.call(input, 'clientProfile')
  const nextClientProfile = hasClientProfileInput
    ? normalizeApiKeyClientProfile(input.clientProfile, current.clientProfile)
    : current.clientProfile
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
  const nextExpiresAt = hasExpiresAtInput
    ? normalizeOptionalApiKeyExpiresAt(input.expiresAt)
    : current.expiresAt
  const hasAvailabilityScheduleInput = isApiKeyAvailabilityScheduleInputPresent(input)
  const hasAvailabilityScheduleActiveInput = Object.prototype.hasOwnProperty.call(input, 'availabilityScheduleActive')
  const nextAvailabilitySchedule = hasAvailabilityScheduleInput
    ? apiKeyAvailabilityScheduleFromRequest(input)
    : current.availabilitySchedule
  const hasGroupRouteStrategyInput = Object.prototype.hasOwnProperty.call(input, 'groupRouteStrategy')
  const nextGroupRouteStrategy = hasGroupRouteStrategyInput
    ? normalizeApiKeyGroupRouteStrategy(input.groupRouteStrategy)
    : current.groupRouteStrategy
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const nextManualStatus = hasStatusInput
    ? normalizeApiKeyStatus(input.status, current.status)
    : current.status
  const nextAvailabilityScheduleActive = nextApiKeyAvailabilityScheduleActiveValue({
    currentActive: currentRow?.availability_schedule_active,
    hasScheduleInput: hasAvailabilityScheduleInput,
    hasScheduleActiveInput: hasAvailabilityScheduleActiveInput,
    hasStatusInput,
    nextManualStatus,
    nextSchedule: nextAvailabilitySchedule,
    scheduleActiveInput: input.availabilityScheduleActive
  })
  const next: ApiKeySummary = {
    ...current,
    name: Object.prototype.hasOwnProperty.call(input, 'name') ? normalizedApiKeyName(input.name) : current.name,
    description: Object.prototype.hasOwnProperty.call(input, 'description') ? normalizeOptionalApiKeyDescription(input.description) : current.description,
    status: nextManualStatus,
    clientProfile: nextClientProfile,
    routeMode: nextRouteMode,
    groupRouteStrategy: nextGroupRouteStrategy,
    hybridRoutingConfig: nextHybridRoutingConfig,
    explicitHybridRouteRules: nextExplicitHybridRouteRules,
    groupBindings: nextBindings ? apiKeyGroupBindingSummariesForRecord(recordlessBindingPrefix(), nextBindings) : current.groupBindings,
    expiresAt: nextExpiresAt,
    quotaLimits: normalizeRequestQuotaLimits(input.quotaLimits, current.quotaLimits ?? emptyRequestQuotaLimits()),
    availabilitySchedule: nextAvailabilitySchedule
  }
  assertApiKeyNameAvailable(systemAccountId, next.name, id)
  const database = getBusinessDatabase()
  const now = nowIso()
  const nowDate = new Date(now)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const quotaLimitsJson = requestQuotaLimitsJson(next.quotaLimits)
    const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(next.availabilitySchedule, nowDate)
    const updates = [
      'name = ?',
      'description = ?',
      'status = ?',
      'client_profile = ?',
      'route_mode = ?',
      'group_route_strategy = ?',
      'hybrid_routing_config_json = ?',
      'explicit_hybrid_route_rules_json = ?',
      'expires_at = ?',
      'quota_limits_json = ?',
      'availability_schedule_json = ?',
      'availability_schedule_active = ?',
      'availability_schedule_next_check_at = ?',
      'updated_at = ?'
    ]
    const updateValues = [
      next.name,
      next.description ?? null,
      next.status,
      next.clientProfile,
      next.routeMode,
      next.groupRouteStrategy,
      hybridRoutingConfigJson(next.hybridRoutingConfig),
      explicitHybridRouteRulesJson(next.explicitHybridRouteRules),
      next.expiresAt ?? null,
      quotaLimitsJson,
      apiKeyAvailabilityScheduleJson(next.availabilitySchedule),
      nextAvailabilityScheduleActive,
      availabilityScheduleNextCheckAt,
      now
    ]
    updateValues.push(id, systemAccountId)
    database
      .prepare(`UPDATE api_keys SET ${updates.join(', ')} WHERE id = ? AND system_account_id = ?`)
      .run(...updateValues)
    rememberRequestQuotaHourlyWindowsFromJson(quotaLimitsJson, database, now)
    if (nextBindings) {
      replaceApiKeyGroupBindings(database, id, systemAccountId, nextBindings, now)
    }
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
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) {
    return undefined
  }
  const client = await getApiKeyDatabaseClient()
  const currentRow = await client.one<ApiKeyRow>(`
    SELECT ${apiKeyListColumns()}
    FROM ${apiKeyTable(client, 'api_keys')} api_keys
    LEFT JOIN ${apiKeyTable(client, 'system_accounts')} system_accounts
      ON system_accounts.id = api_keys.system_account_id
    WHERE api_keys.id = ? AND api_keys.system_account_id = ?
  `, [id, systemAccountId])
  const current = currentRow ? (await apiKeySummariesFromRowsAsync([currentRow], { systemAccountId, role: 'user' }, { includeSecret: false }))[0] : undefined
  if (!current) {
    return undefined
  }

  const hasRouteModeInput = Object.prototype.hasOwnProperty.call(input, 'routeMode')
  const nextRouteMode = hasRouteModeInput
    ? normalizeApiKeyRouteMode(input.routeMode)
    : current.routeMode
  const hasBindingInput = hasApiKeyGroupBindingInput(input)
  const nextBindings = hasBindingInput
    ? await normalizeApiKeyGroupBindingsAsync(apiKeyGroupBindingInputsFromRequest(input) ?? [], systemAccountId, {
      retainableGroupIds: current.groupBindings.map((binding) => binding.groupId)
    })
    : undefined
  const effectiveBindings = nextBindings ?? current.groupBindings.map((binding) => ({
    groupId: binding.groupId,
    status: binding.status,
    groupEnabled: binding.groupEnabled
  }))
  const hasHybridRoutingConfigInput = Object.prototype.hasOwnProperty.call(input, 'hybridRoutingConfig')
  const nextHybridRoutingConfig = (hasHybridRoutingConfigInput || hasRouteModeInput || hasBindingInput)
    ? normalizeApiKeyHybridRoutingConfigForWrite(
      hasHybridRoutingConfigInput ? input.hybridRoutingConfig : current.hybridRoutingConfig,
      nextRouteMode,
      effectiveBindings
    )
    : current.hybridRoutingConfig
  const hasExplicitHybridRouteRulesInput = Object.prototype.hasOwnProperty.call(input, 'explicitHybridRouteRules')
  const nextExplicitHybridRouteRules = (hasExplicitHybridRouteRulesInput || hasBindingInput)
    ? normalizeApiKeyExplicitHybridRouteRulesForWrite(
      hasExplicitHybridRouteRulesInput ? input.explicitHybridRouteRules : current.explicitHybridRouteRules,
      effectiveBindings
    )
    : current.explicitHybridRouteRules
  const hasClientProfileInput = Object.prototype.hasOwnProperty.call(input, 'clientProfile')
  const nextClientProfile = hasClientProfileInput
    ? normalizeApiKeyClientProfile(input.clientProfile, current.clientProfile)
    : current.clientProfile
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
  const nextExpiresAt = hasExpiresAtInput
    ? normalizeOptionalApiKeyExpiresAt(input.expiresAt)
    : current.expiresAt
  const hasAvailabilityScheduleInput = isApiKeyAvailabilityScheduleInputPresent(input)
  const hasAvailabilityScheduleActiveInput = Object.prototype.hasOwnProperty.call(input, 'availabilityScheduleActive')
  const nextAvailabilitySchedule = hasAvailabilityScheduleInput
    ? apiKeyAvailabilityScheduleFromRequest(input)
    : current.availabilitySchedule
  const hasGroupRouteStrategyInput = Object.prototype.hasOwnProperty.call(input, 'groupRouteStrategy')
  const nextGroupRouteStrategy = hasGroupRouteStrategyInput
    ? normalizeApiKeyGroupRouteStrategy(input.groupRouteStrategy)
    : current.groupRouteStrategy
  const hasStatusInput = Object.prototype.hasOwnProperty.call(input, 'status')
  const nextManualStatus = hasStatusInput
    ? normalizeApiKeyStatus(input.status, current.status)
    : current.status
  const nextAvailabilityScheduleActive = nextApiKeyAvailabilityScheduleActiveValue({
    currentActive: currentRow?.availability_schedule_active,
    hasScheduleInput: hasAvailabilityScheduleInput,
    hasScheduleActiveInput: hasAvailabilityScheduleActiveInput,
    hasStatusInput,
    nextManualStatus,
    nextSchedule: nextAvailabilitySchedule,
    scheduleActiveInput: input.availabilityScheduleActive
  })
  const next: ApiKeySummary = {
    ...current,
    name: Object.prototype.hasOwnProperty.call(input, 'name') ? normalizedApiKeyName(input.name) : current.name,
    description: Object.prototype.hasOwnProperty.call(input, 'description') ? normalizeOptionalApiKeyDescription(input.description) : current.description,
    status: nextManualStatus,
    clientProfile: nextClientProfile,
    routeMode: nextRouteMode,
    groupRouteStrategy: nextGroupRouteStrategy,
    hybridRoutingConfig: nextHybridRoutingConfig,
    explicitHybridRouteRules: nextExplicitHybridRouteRules,
    groupBindings: nextBindings ? apiKeyGroupBindingSummariesForRecord(recordlessBindingPrefix(), nextBindings) : current.groupBindings,
    expiresAt: nextExpiresAt,
    quotaLimits: normalizeRequestQuotaLimits(input.quotaLimits, current.quotaLimits ?? emptyRequestQuotaLimits()),
    availabilitySchedule: nextAvailabilitySchedule
  }
  try {
    await client.transaction(async (tx) => {
      await assertApiKeyNameAvailableAsync(tx, systemAccountId, next.name, id)
      const now = nowIso()
      const nowDate = new Date(now)
      const quotaLimitsJson = requestQuotaLimitsJson(next.quotaLimits)
      const availabilityScheduleNextCheckAt = nextApiKeyAvailabilityScheduleCheckAt(next.availabilitySchedule, nowDate)
      const updates = [
        'name = ?',
        'description = ?',
        'status = ?',
        'client_profile = ?',
        'route_mode = ?',
        'group_route_strategy = ?',
        'hybrid_routing_config_json = ?',
        'explicit_hybrid_route_rules_json = ?',
        'expires_at = ?',
        'quota_limits_json = ?',
        'availability_schedule_json = ?',
        'availability_schedule_active = ?',
        'availability_schedule_next_check_at = ?',
        'updated_at = ?'
      ]
      const updateValues = [
        next.name,
        next.description ?? null,
        next.status,
        next.clientProfile,
        next.routeMode,
        next.groupRouteStrategy,
        hybridRoutingConfigJson(next.hybridRoutingConfig),
        explicitHybridRouteRulesJson(next.explicitHybridRouteRules),
        next.expiresAt ?? null,
        quotaLimitsJson,
        apiKeyAvailabilityScheduleJson(next.availabilitySchedule),
        nextAvailabilityScheduleActive,
        availabilityScheduleNextCheckAt,
        now,
        id,
        systemAccountId
      ]
      await tx.execute(`
        UPDATE ${apiKeyTable(tx, 'api_keys')}
        SET ${updates.join(', ')}
        WHERE id = ? AND system_account_id = ?
      `, updateValues)
      await rememberRequestQuotaHourlyWindowsFromLimitsAsync(tx, next.quotaLimits, now)
      if (nextBindings) {
        await replaceApiKeyGroupBindingsAsync(tx, id, systemAccountId, nextBindings, now)
      }
    })
  } catch (error) {
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${next.name}`)
    }
    throw error
  }
  invalidateGatewayApiKeyCacheById(id)
  invalidateApiKeyLookupCache(id)
  notifyGatewayRuntimeCacheInvalidation('api_key_updated')
  notifyApiKeyQuotaCacheInvalidation(id, 'api_key_updated')
  return await findApiKeySummaryAsync(id, access) ?? next
}

function nextApiKeyAvailabilityScheduleActiveValue(input: {
  currentActive: number | null | undefined
  hasScheduleInput: boolean
  hasScheduleActiveInput: boolean
  hasStatusInput: boolean
  nextManualStatus: 'active' | 'disabled'
  nextSchedule: ApiKeySummary['availabilitySchedule']
  scheduleActiveInput: unknown
}): number {
  if (input.hasScheduleActiveInput) {
    return normalizeApiKeyAvailabilityScheduleActiveOverride(input.scheduleActiveInput, input.nextSchedule)
  }
  if (input.hasScheduleInput) {
    return apiKeyAvailabilityScheduleActiveValue(input.nextSchedule)
  }
  if (input.hasStatusInput && input.nextManualStatus === 'active') {
    return 1
  }
  return input.currentActive === 0 ? 0 : 1
}

function normalizeApiKeyAvailabilityScheduleActiveOverride(
  value: unknown,
  schedule: ApiKeySummary['availabilitySchedule']
): number {
  if (!schedule?.enabled) {
    throw new Error('只有启用时间计划的 API Key 才能调整时间计划派生状态')
  }
  if (typeof value !== 'boolean') {
    throw new Error('API Key 时间计划派生状态必须是布尔值')
  }
  return value ? 1 : 0
}

function apiKeyAvailabilityScheduleActiveValue(
  schedule: ApiKeySummary['availabilitySchedule'],
  now = new Date()
): number {
  return evaluateApiKeyAvailabilitySchedule(schedule, now).allowed ? 1 : 0
}

export function refreshApiKeySecret(id: string, access?: AccessScope): (ApiKeySummary & { key: string }) | undefined {
  const systemAccountId = apiKeySystemAccountId(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) {
    return undefined
  }
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const now = nowIso()
  const result = getBusinessDatabase()
    .prepare(`
      UPDATE api_keys
      SET key_hash = ?,
          key_prefix = ?,
          key_suffix = ?,
          key_secret_encrypted = ?,
          updated_at = ?
      WHERE id = ? AND system_account_id = ?
    `)
    .run(hashSecret(key), keyPrefix, keySuffix, encryptJson({ key }), now, id, systemAccountId)
  if (result.changes <= 0) {
    return undefined
  }
  invalidateGatewayApiKeyCacheById(id)
  invalidateApiKeyLookupCache(id)
  notifyGatewayRuntimeCacheInvalidation('api_key_secret_refreshed')
  notifyApiKeyQuotaCacheInvalidation(id, 'api_key_secret_refreshed')
  const summary = findApiKeySummary(id, access)
  return summary ? { ...summary, key } : undefined
}

export async function refreshApiKeySecretAsync(id: string, access?: AccessScope): Promise<(ApiKeySummary & { key: string }) | undefined> {
  const systemAccountId = await apiKeySystemAccountIdAsync(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) {
    return undefined
  }
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const keySuffix = key.slice(-8)
  const now = nowIso()
  const client = await getApiKeyDatabaseClient()
  const result = await client.execute(`
    UPDATE ${apiKeyTable(client, 'api_keys')}
    SET key_hash = ?,
        key_prefix = ?,
        key_suffix = ?,
        key_secret_encrypted = ?,
        updated_at = ?
    WHERE id = ? AND system_account_id = ?
  `, [hashSecret(key), keyPrefix, keySuffix, encryptJson({ key }), now, id, systemAccountId])
  if (result.changes <= 0) {
    return undefined
  }
  invalidateGatewayApiKeyCacheById(id)
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
  const row = database.prepare(`SELECT id, system_account_id FROM api_keys WHERE id = ?${scope.clause}`).get(id, ...scope.params) as unknown as ApiKeyDeleteRow | undefined
  if (!row) {
    return { deleted: false }
  }

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    database.prepare('DELETE FROM api_key_group_bindings WHERE api_key_id = ?').run(row.id)
    const result = database.prepare('DELETE FROM api_keys WHERE id = ? AND system_account_id = ?').run(row.id, row.system_account_id)
    commitDatabaseTransaction(database, transactionStarted)
    if (result.changes > 0) {
      invalidateGatewayApiKeyCacheById(row.id)
      invalidateApiKeyLookupCache(row.id)
      notifyGatewayRuntimeCacheInvalidation('api_key_deleted')
      notifyApiKeyQuotaCacheInvalidation(row.id, 'api_key_deleted')
    }
    return {
      deleted: result.changes > 0,
      cleanupTarget: result.changes > 0
        ? {
          apiKeyId: row.id,
          systemAccountId: row.system_account_id
        }
        : undefined
    }
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

export async function deleteApiKeyWithRelatedCleanupAsync(id: string, access?: AccessScope): Promise<ApiKeyDeleteResult> {
  const client = await getApiKeyDatabaseClient()
  const scope = buildSystemAccountScopeClause(access)
  const row = await client.one<ApiKeyDeleteRow>(`
    SELECT id, system_account_id
    FROM ${apiKeyTable(client, 'api_keys')}
    WHERE id = ?${scope.clause}
  `, [id, ...scope.params])
  if (!row) {
    return { deleted: false }
  }

  let deleted = false
  await client.transaction(async (tx) => {
    await tx.execute(`
      DELETE FROM ${apiKeyTable(tx, 'api_key_group_bindings')}
      WHERE api_key_id = ?
    `, [row.id])
    const result = await tx.execute(`
      DELETE FROM ${apiKeyTable(tx, 'api_keys')}
      WHERE id = ? AND system_account_id = ?
    `, [row.id, row.system_account_id])
    deleted = result.changes > 0
  })
  if (deleted) {
    invalidateGatewayApiKeyCacheById(row.id)
    invalidateApiKeyLookupCache(row.id)
    notifyGatewayRuntimeCacheInvalidation('api_key_deleted')
    notifyApiKeyQuotaCacheInvalidation(row.id, 'api_key_deleted')
  }
  return {
    deleted,
    cleanupTarget: deleted
      ? {
        apiKeyId: row.id,
        systemAccountId: row.system_account_id
      }
      : undefined
  }
}

function hasApiKeyGroupBindingInput(input: Record<string, unknown>): boolean {
  return Object.prototype.hasOwnProperty.call(input, 'groupBindings')
}

function apiKeyGroupBindingInputsFromRequest(input: Record<string, unknown>): ApiKeyGroupBindingInput[] | undefined {
  const rawBindings = Object.prototype.hasOwnProperty.call(input, 'groupBindings') ? input.groupBindings : undefined
  if (rawBindings !== undefined) {
    if (!Array.isArray(rawBindings) || rawBindings.length === 0) {
      throw new Error('API Key 至少需要绑定一个分组')
    }
    return rawBindings.map(apiKeyGroupBindingInputFromUnknown)
  }
  return undefined
}

function apiKeyGroupBindingInputFromUnknown(value: unknown, index: number): ApiKeyGroupBindingInput {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('API Key 分组绑定参数无效')
  }
  const item = value as Record<string, unknown> & { groupId?: unknown; priority?: unknown; weight?: unknown; status?: unknown }
  assertKnownInputKeys(item, apiKeyGroupBindingInputKeys, 'API Key 分组绑定参数')
  const groupId = optionalString(item.groupId)?.trim()
  if (!groupId) {
    throw new Error('API Key 分组无效')
  }
  const rawPriority = item.priority
  const priority = rawPriority === undefined
    ? index + 1
    : normalizeApiKeyGroupBindingPriority(rawPriority)
  const status = normalizeApiKeyGroupBindingStatus(item.status)
  return { groupId, priority, weight: normalizeApiKeyGroupBindingWeight(item.weight), status }
}

function normalizeApiKeyGroupBindings(
  inputs: ApiKeyGroupBindingInput[],
  systemAccountId: string,
  options: { retainableGroupIds?: string[] } = {}
): ApiKeyGroupBindingWrite[] {
  if (!inputs.length) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  if (inputs.length > maxApiKeyGroupBindings) {
    throw new Error(`API Key 最多绑定 ${maxApiKeyGroupBindings} 个分组`)
  }
  const seenGroupIds = new Set<string>()
  const activePriorities = new Set<number>()
  const normalized = inputs.map((input) => {
    const groupId = input.groupId.trim()
    if (!groupId) {
      throw new Error('API Key 分组无效')
    }
    if (seenGroupIds.has(groupId)) {
      throw new Error('API Key 绑定分组不能重复')
    }
    seenGroupIds.add(groupId)
    if (input.status === 'active') {
      if (activePriorities.has(input.priority)) {
        throw new Error('API Key 启用分组优先级不能重复')
      }
      activePriorities.add(input.priority)
    }
    return {
      groupId,
      priority: input.priority,
      weight: input.weight,
      status: input.status
    }
  })
  if (!normalized.some((binding) => binding.status === 'active')) {
    throw new Error('API Key 至少需要一个启用分组')
  }

  const groups = loadApiKeyBindableGroups([...seenGroupIds], systemAccountId)
  const retainableGroupIds = new Set((options.retainableGroupIds ?? []).filter(Boolean))
  return normalized
    .map((binding) => {
      const group = groups.get(binding.groupId)
      const canBindNow = group ? canBindApiKeyGroup(binding.groupId, systemAccountId) : false
      if (!group || (!canBindNow && !retainableGroupIds.has(binding.groupId))) {
        throw new Error(API_KEY_GROUP_BOUNDARY_ERROR)
      }
      if (binding.status === 'active' && group.enabled === 0) {
        throw new Error(`API Key 不能启用已停用分组：${group.name ?? binding.groupId}`)
      }
      return {
        ...binding,
        weight: normalizeApiKeyGroupBindingWeight(binding.weight),
        groupName: group.name ?? undefined,
        providerCode: group.provider_code,
        providerProtocolProfileId: group.provider_protocol_profile_id,
        protocolCode: group.protocol_code,
        protocolVersion: group.protocol_version,
        groupEnabled: group.enabled !== 0
      }
    })
    .sort((left, right) => left.priority - right.priority || left.groupId.localeCompare(right.groupId))
}

function loadApiKeyBindableGroups(groupIds: string[], systemAccountId: string): Map<string, ApiKeyBindableGroupRow> {
  const ids = [...new Set(groupIds.filter(Boolean))]
  const result = new Map<string, ApiKeyBindableGroupRow>()
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
      .all(systemAccountId, systemAccountId, now, systemAccountId, ...chunk) as unknown as ApiKeyBindableGroupRow[]
    for (const row of rows) {
      result.set(row.id, row)
    }
  }
  return result
}

function normalizeApiKeyHybridRoutingConfigForWrite(
  value: unknown,
  routeMode: ApiKeySummary['routeMode'],
  _bindings: Array<Pick<ApiKeyGroupBindingWrite, 'groupId' | 'status' | 'groupEnabled'>>
): ApiKeySummary['hybridRoutingConfig'] {
  if (routeMode !== 'hybrid') {
    return undefined
  }
  return normalizeHybridRoutingConfig(value)
}

function normalizeApiKeyExplicitHybridRouteRulesForWrite(
  value: unknown,
  bindings: Array<Pick<ApiKeyGroupBindingWrite, 'groupId' | 'status' | 'groupEnabled'>>
): ApiKeySummary['explicitHybridRouteRules'] {
  const rules = normalizeExplicitHybridRouteRules(value)
  if (!rules?.length) return undefined
  const activeBindingGroupIds = new Set(
    bindings
      .filter((binding) => binding.status === 'active' && binding.groupEnabled)
      .map((binding) => binding.groupId)
  )
  const invalidTargetGroupIds = rules
    .map((rule) => rule.targetGroupId)
    .filter((groupId) => !activeBindingGroupIds.has(groupId))
  if (invalidTargetGroupIds.length) {
    throw new Error(`显式混合路由目标分组必须是当前 API Key 已绑定且启用的分组：${[...new Set(invalidTargetGroupIds)].slice(0, 5).join('、')}`)
  }
  return rules
}

function replaceApiKeyGroupBindings(
  database: DatabaseSync,
  apiKeyId: string,
  systemAccountId: string,
  bindings: ApiKeyGroupBindingWrite[],
  now: string
): void {
  database.prepare('DELETE FROM api_key_group_bindings WHERE api_key_id = ?').run(apiKeyId)
  const statement = database.prepare(`
    INSERT INTO api_key_group_bindings (id, api_key_id, system_account_id, group_id, priority, weight, status, created_at, updated_at)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `)
  for (const binding of bindings) {
    statement.run(newId('akgb'), apiKeyId, systemAccountId, binding.groupId, binding.priority, binding.weight, binding.status, now, now)
  }
}

function apiKeyGroupBindingSummariesForRecord(idPrefix: string, bindings: ApiKeyGroupBindingWrite[]): ApiKeyGroupBindingSummary[] {
  return bindings.map((binding, index) => ({
    id: `${idPrefix}${index + 1}`,
    groupId: binding.groupId,
    groupName: binding.groupName,
    providerCode: binding.providerCode,
    providerProtocolProfileId: binding.providerProtocolProfileId,
    protocolCode: binding.protocolCode,
    protocolVersion: binding.protocolVersion,
    priority: binding.priority,
    weight: binding.weight,
    status: binding.status,
    groupEnabled: binding.groupEnabled
  }))
}

function recordlessBindingPrefix(): string {
  return 'pending:'
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
  if (value === undefined || value === null) return undefined
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

function normalizeApiKeyGroupBindingPriority(value: unknown): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value <= 0) {
    throw new Error('API Key 分组优先级必须是大于 0 的整数')
  }
  return value
}

function normalizeApiKeyGroupBindingStatus(value: unknown): 'active' | 'disabled' {
  if (value === undefined) return 'active'
  if (value === 'active' || value === 'disabled') return value
  throw new Error('API Key 分组绑定状态无效')
}

function assertKnownInputKeys(input: Record<string, unknown>, allowedKeys: ReadonlySet<string>, label: string): void {
  const unknownKeys = Object.keys(input).filter((key) => !allowedKeys.has(key))
  if (unknownKeys.length) {
    throw new Error(`${label}包含未知字段：${unknownKeys.join('、')}`)
  }
}

function assertApiKeyNameAvailable(systemAccountId: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getBusinessDatabase()
    .prepare(`SELECT id FROM api_keys WHERE system_account_id = ? AND lower(name) = lower(?)${excludeClause} LIMIT 1`)
    .get(...params) as { id?: string } | undefined
  if (row?.id) {
    throw new Error(`API Key 名称已存在：${name}`)
  }
}

function buildApiKeyFiltersForClient(
  client: DatabaseClient,
  scope: { clause: string; params: string[] },
  options: ReturnType<typeof normalizeApiKeyListOptions>
): { clause: string; params: Array<string | number> } {
  if (client.driver === 'sqlite') {
    return buildApiKeyFilters(scope, options)
  }
  const clauses: string[] = []
  const params: Array<string | number> = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ WHERE /, ''))
    params.push(...scope.params)
  }
  if (options.keyword) {
    const keywordPrefix = `${escapeLikePrefix(options.keyword)}%`
    clauses.push("(lower(api_keys.name) = lower(?) OR lower(api_keys.name) LIKE lower(?) ESCAPE '\\')")
    params.push(options.keyword, keywordPrefix)
  }
  if (options.status) {
    if (options.status === 'active') {
      clauses.push("api_keys.status = 'active' AND api_keys.availability_schedule_active = 1")
    } else {
      clauses.push("(api_keys.status = 'disabled' OR api_keys.availability_schedule_active <> 1)")
    }
  }
  if (options.groupId) {
    clauses.push(`EXISTS (
      SELECT 1
      FROM ${apiKeyTable(client, 'api_key_group_bindings')} api_key_group_bindings
      WHERE api_key_group_bindings.api_key_id = api_keys.id
        AND api_key_group_bindings.group_id = ?
    )`)
    params.push(options.groupId)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

async function normalizeApiKeyGroupBindingsAsync(
  inputs: ApiKeyGroupBindingInput[],
  systemAccountId: string,
  options: { retainableGroupIds?: string[] } = {}
): Promise<ApiKeyGroupBindingWrite[]> {
  if (!inputs.length) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  if (inputs.length > maxApiKeyGroupBindings) {
    throw new Error(`API Key 最多绑定 ${maxApiKeyGroupBindings} 个分组`)
  }
  const seenGroupIds = new Set<string>()
  const activePriorities = new Set<number>()
  const normalized = inputs.map((input) => {
    const groupId = input.groupId.trim()
    if (!groupId) {
      throw new Error('API Key 分组无效')
    }
    if (seenGroupIds.has(groupId)) {
      throw new Error('API Key 绑定分组不能重复')
    }
    seenGroupIds.add(groupId)
    if (input.status === 'active') {
      if (activePriorities.has(input.priority)) {
        throw new Error('API Key 启用分组优先级不能重复')
      }
      activePriorities.add(input.priority)
    }
    return {
      groupId,
      priority: input.priority,
      weight: input.weight,
      status: input.status
    }
  })
  if (!normalized.some((binding) => binding.status === 'active')) {
    throw new Error('API Key 至少需要一个启用分组')
  }

  const groups = await loadApiKeyBindableGroupsAsync([...seenGroupIds], systemAccountId)
  const retainableGroupIds = new Set((options.retainableGroupIds ?? []).filter(Boolean))
  const result: ApiKeyGroupBindingWrite[] = []
  for (const binding of normalized) {
    const group = groups.get(binding.groupId)
    const canBindNow = group ? await canBindApiKeyGroupAsync(binding.groupId, systemAccountId) : false
    if (!group || (!canBindNow && !retainableGroupIds.has(binding.groupId))) {
      throw new Error(API_KEY_GROUP_BOUNDARY_ERROR)
    }
    if (binding.status === 'active' && Number(group.enabled) === 0) {
      throw new Error(`API Key 不能启用已停用分组：${group.name ?? binding.groupId}`)
    }
    result.push({
      ...binding,
      weight: normalizeApiKeyGroupBindingWeight(binding.weight),
      groupName: group.name ?? undefined,
      providerCode: group.provider_code,
      providerProtocolProfileId: group.provider_protocol_profile_id,
      protocolCode: group.protocol_code,
      protocolVersion: group.protocol_version,
      groupEnabled: Number(group.enabled) !== 0
    })
  }
  return result.sort((left, right) => left.priority - right.priority || left.groupId.localeCompare(right.groupId))
}

async function loadApiKeyBindableGroupsAsync(groupIds: string[], systemAccountId: string): Promise<Map<string, ApiKeyBindableGroupRow>> {
  const ids = [...new Set(groupIds.filter(Boolean))]
  const result = new Map<string, ApiKeyBindableGroupRow>()
  if (!ids.length) return result
  const client = await getApiKeyDatabaseClient()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = await client.query<ApiKeyBindableGroupRow>(`
      SELECT
        groups.id,
        groups.system_account_id,
        groups.provider_code,
        groups.provider_protocol_profile_id,
        groups.protocol_code,
        groups.protocol_version,
        groups.name,
        CASE
          WHEN groups.system_account_id = ? THEN groups.enabled
          WHEN group_authorization.id IS NOT NULL THEN CASE WHEN groups.enabled = 1 THEN COALESCE(group_authorization_settings.enabled, 1) ELSE 0 END
          ELSE 0
        END AS enabled
      FROM ${apiKeyTable(client, 'groups')} groups
      LEFT JOIN ${apiKeyTable(client, 'resource_authorizations')} group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = groups.id
        AND group_authorization.grantee_system_account_id = ?
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      LEFT JOIN ${apiKeyTable(client, 'group_authorization_settings')} group_authorization_settings
        ON group_authorization_settings.authorization_id = group_authorization.id
        AND group_authorization_settings.system_account_id = ?
        AND group_authorization_settings.group_id = groups.id
      WHERE groups.id IN (${client.dialect.bindPlaceholders(chunk.length)})
    `, [systemAccountId, systemAccountId, now, systemAccountId, ...chunk])
    for (const row of rows) {
      result.set(row.id, row)
    }
  }
  return result
}

async function canBindApiKeyGroupAsync(groupId: string, systemAccountId: string): Promise<boolean> {
  const group = await apiKeyGroupOwnerAndProviderAsync(groupId)
  if (!group) return false
  if (group.systemAccountId === systemAccountId) return true
  return activeGroupAuthorizationExistsAsync(groupId, systemAccountId)
}

async function apiKeyGroupOwnerAndProviderAsync(groupId: string): Promise<ReturnType<typeof apiKeyGroupOwnerAndProvider>> {
  const client = await getApiKeyDatabaseClient()
  const row = await client.one<{ system_account_id?: string; provider_code?: string; provider_protocol_profile_id?: string; protocol_code?: string; protocol_version?: string; name?: string }>(`
    SELECT system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version, name
    FROM ${apiKeyTable(client, 'groups')}
    WHERE id = ?
  `, [groupId])
  return row?.system_account_id && row.provider_code && row.provider_protocol_profile_id && row.protocol_code && row.protocol_version
    ? {
        systemAccountId: row.system_account_id,
        providerCode: row.provider_code as ApiKeyBindableGroupRow['provider_code'],
        providerProtocolProfileId: row.provider_protocol_profile_id,
        protocolCode: row.protocol_code,
        protocolVersion: row.protocol_version,
        name: row.name
      }
    : undefined
}

async function activeGroupAuthorizationExistsAsync(groupId: string, granteeSystemAccountId: string): Promise<boolean> {
  const client = await getApiKeyDatabaseClient()
  const now = nowIso()
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${apiKeyTable(client, 'resource_authorizations')}
    WHERE resource_type = 'group'
      AND resource_id = ?
      AND grantee_system_account_id = ?
      AND status = 'active'
      AND (expires_at IS NULL OR expires_at > ?)
    LIMIT 1
  `, [groupId, granteeSystemAccountId, now])
  return Boolean(row?.id)
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

async function replaceApiKeyGroupBindingsAsync(
  client: DatabaseClient,
  apiKeyId: string,
  systemAccountId: string,
  bindings: ApiKeyGroupBindingWrite[],
  now: string
): Promise<void> {
  await client.execute(`
    DELETE FROM ${apiKeyTable(client, 'api_key_group_bindings')}
    WHERE api_key_id = ?
  `, [apiKeyId])
  for (const binding of bindings) {
    await client.execute(`
      INSERT INTO ${apiKeyTable(client, 'api_key_group_bindings')} (
        id, api_key_id, system_account_id, group_id, priority, weight, status, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, [newId('akgb'), apiKeyId, systemAccountId, binding.groupId, binding.priority, binding.weight, binding.status, now, now])
  }
}

async function rememberRequestQuotaHourlyWindowsFromLimitsAsync(client: DatabaseClient, limits: RequestQuotaLimits, timestamp: string): Promise<void> {
  const hours = limits.hourly?.enabled ? limits.hourly.hours : undefined
  if (!Number.isInteger(hours) || typeof hours !== 'number') {
    return
  }
  await client.execute(`
    INSERT INTO ${apiKeyTable(client, 'request_quota_hourly_window_configs')} (window_hours, created_at, updated_at)
    VALUES (?, ?, ?)
    ON CONFLICT(window_hours) DO UPDATE SET updated_at = excluded.updated_at
  `, [hours, timestamp, timestamp])
}

async function assertApiKeyNameAvailableAsync(client: DatabaseClient, systemAccountId: string, name: string, excludeId?: string): Promise<void> {
  const row = await client.one<{ id?: string }>(`
    SELECT id
    FROM ${apiKeyTable(client, 'api_keys')}
    WHERE system_account_id = ? AND lower(name) = lower(?) AND id <> ?
    LIMIT 1
  `, [systemAccountId, name, excludeId ?? ''])
  if (row?.id) {
    throw new Error(`API Key 名称已存在：${name}`)
  }
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

function escapeLikePrefix(value: string): string {
  return value.replace(/[\\%_]/g, (char) => `\\${char}`)
}

function isDuplicateApiKeyNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_api_keys_owner_name_unique_lower')
}
