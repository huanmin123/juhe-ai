import type { DatabaseSync } from 'node:sqlite'

import type { ApiKeyGroupBindingSummary, ApiKeySummary } from '../domain/types.js'
import {
  normalizeApiKeyGroupBindingWeight,
  normalizeApiKeyGroupRouteStrategy
} from '../domain/api-key-routing.js'
import { notifyApiKeyQuotaCacheInvalidation, notifyGatewayRuntimeCacheInvalidation } from '../shared/gateway-cache-invalidation.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { apiKeyGroupOwnerAndProvider, apiKeySystemAccountId, canBindApiKeyGroup, canManageApiKeyOwner } from './api-key-access.js'
import {
  apiKeyAvailabilityScheduleFromRequest,
  apiKeyAvailabilityScheduleJson,
  isApiKeyAvailabilityScheduleInputPresent
} from './api-key-availability-schedule.js'
import { createApiKey, encryptJson, hashSecret } from './crypto.js'
import { buildApiKeyFilters, normalizeApiKeyListOptions } from './api-key-list-query.js'
import { apiKeySummariesFromRows, type ApiKeyRow } from './api-key-mappers.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getBusinessDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { invalidateGatewayApiKeyCacheById } from './gateway-api-key.repository.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { invalidateApiKeyLookupCache, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { emptyRequestQuotaLimits, normalizeRequestQuotaLimits, requestQuotaLimitsJson } from './request-quota-limits.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { optionalNullableServerDateTimeIso, optionalNullableString, optionalServerDateTimeIso, optionalString } from './value-utils.js'

const API_KEY_GROUP_BOUNDARY_ERROR = 'API Key 只能绑定自己的分组'
const API_KEY_GROUP_PROVIDER_ERROR = 'API Key 不能绑定不同供应商的分组'
const MAX_API_KEY_GROUP_BINDINGS = 20

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
  groupEnabled: boolean
}

interface ApiKeyBindableGroupRow {
  id: string
  system_account_id: string
  provider_code: string
  name: string | null
  enabled: number
}

export function listApiKeys(access?: AccessScope, options?: ApiKeyListOptions): ApiKeySummary[] {
  return queryApiKeys(access, options).items
}

export function listApiKeysPage(access?: AccessScope, options?: ApiKeyListOptions): ApiKeyListResult {
  return queryApiKeys(access, options, true)
}

export function findApiKeySummary(id: string, access?: AccessScope): ApiKeySummary | undefined {
  const scope = buildSystemAccountScopeClause(access, 'api_keys.system_account_id')
  const row = getBusinessDatabase()
    .prepare(`SELECT ${apiKeyListColumns()} FROM api_keys LEFT JOIN system_accounts ON system_accounts.id = api_keys.system_account_id WHERE api_keys.id = ?${scope.clause}`)
    .get(id, ...scope.params) as unknown as ApiKeyRow | undefined
  return row ? apiKeySummariesFromRows([row], access, { includeSecret: false })[0] : undefined
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
  const items = apiKeySummariesFromRows(pageRows.rows, access, { includeSecret: true })
  return {
    items,
    total: paged ? pagedTotalUpperBound(normalized.page, normalized.pageSize, items.length, pageRows.hasMore) : items.length,
    hasMore: pageRows.hasMore,
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

function apiKeyListColumns(): string {
  return [
    'api_keys.id',
    'api_keys.system_account_id',
    'api_keys.name',
    'api_keys.description',
    'api_keys.key_prefix',
    'api_keys.key_secret_encrypted',
    'api_keys.status',
    'api_keys.group_route_strategy',
    'system_accounts.display_name AS group_owner_system_account_name',
    'api_keys.expires_at',
    'api_keys.quota_limits_json',
    'api_keys.availability_schedule_json'
  ].join(', ')
}

export function createApiKeyRecord(input: Record<string, unknown>, access?: AccessScope): ApiKeySummary & { key: string } {
  const now = nowIso()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const scopedOwnerId = manageableSystemAccountId(access)
  let systemAccountId = scopedOwnerId ?? currentSystemAccountId(access)
  const rawBindings = apiKeyGroupBindingInputsFromRequest(input)
  if (!rawBindings) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  const firstExplicitGroupId = rawBindings?.[0]?.groupId
  const firstGroup = firstExplicitGroupId ? apiKeyGroupOwnerAndProvider(firstExplicitGroupId) : undefined
  if (firstGroup && !scopedOwnerId && canManageApiKeyOwner(firstGroup.systemAccountId, access)) {
    systemAccountId = firstGroup.systemAccountId
  }
  const bindings = normalizeApiKeyGroupBindings(rawBindings, systemAccountId)
  const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits)
  const availabilitySchedule = apiKeyAvailabilityScheduleFromRequest(input)
  const groupRouteStrategy = normalizeApiKeyGroupRouteStrategy(input.groupRouteStrategy)
  const groupBindings = apiKeyGroupBindingSummariesForRecord(recordlessBindingPrefix(), bindings)
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMapByIds([systemAccountId]).get(systemAccountId) : undefined,
    name: normalizedApiKeyName(input.name, '未命名 API Key'),
    description: optionalString(input.description),
    keyPrefix,
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupRouteStrategy,
    groupBindings,
    expiresAt: optionalServerDateTimeIso(input.expiresAt),
    quotaLimits,
    availabilitySchedule,
    usage: emptyAccountUsageSummary(),
    key
  }
  assertApiKeyNameAvailable(systemAccountId, record.name)
  const database = getBusinessDatabase()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const insertColumns = [
      'id',
      'system_account_id',
      'name',
      'description',
      'key_hash',
      'key_prefix',
      'key_secret_encrypted',
      'status',
      'group_route_strategy',
      'expires_at',
      'quota_limits_json',
      'availability_schedule_json',
      'scopes_json',
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
      encryptJson({ key }),
      record.status,
      record.groupRouteStrategy,
      record.expiresAt ?? null,
      requestQuotaLimitsJson(record.quotaLimits),
      apiKeyAvailabilityScheduleJson(record.availabilitySchedule),
      JSON.stringify(input.scopes ?? []),
      now,
      now
    ]
    database
      .prepare(`
        INSERT INTO api_keys (${insertColumns.join(', ')})
        VALUES (${insertColumns.map(() => '?').join(', ')})
      `)
      .run(...insertValues)
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

export function updateApiKey(id: string, input: Record<string, unknown>, access?: AccessScope): ApiKeySummary | undefined {
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

  const hasBindingInput = hasApiKeyGroupBindingInput(input)
  const nextBindings = hasBindingInput
    ? normalizeApiKeyGroupBindings(apiKeyGroupBindingInputsFromRequest(input) ?? [], systemAccountId)
    : undefined
  const hasExpiresAtInput = Object.prototype.hasOwnProperty.call(input, 'expiresAt')
  const nextExpiresAt = hasExpiresAtInput
    ? optionalNullableServerDateTimeIso(input.expiresAt) ?? undefined
    : current.expiresAt
  const hasAvailabilityScheduleInput = isApiKeyAvailabilityScheduleInputPresent(input)
  const nextAvailabilitySchedule = hasAvailabilityScheduleInput
    ? apiKeyAvailabilityScheduleFromRequest(input)
    : current.availabilitySchedule
  const hasGroupRouteStrategyInput = Object.prototype.hasOwnProperty.call(input, 'groupRouteStrategy')
  const nextGroupRouteStrategy = hasGroupRouteStrategyInput
    ? normalizeApiKeyGroupRouteStrategy(input.groupRouteStrategy)
    : current.groupRouteStrategy
  const next: ApiKeySummary = {
    ...current,
    name: typeof input.name === 'string' && input.name.trim() ? input.name.trim() : current.name,
    description: input.description === undefined ? current.description : optionalNullableString(input.description) ?? undefined,
    status: input.status === 'disabled' ? 'disabled' : input.status === 'active' ? 'active' : current.status,
    groupRouteStrategy: nextGroupRouteStrategy,
    groupBindings: nextBindings ? apiKeyGroupBindingSummariesForRecord(recordlessBindingPrefix(), nextBindings) : current.groupBindings,
    expiresAt: nextExpiresAt,
    quotaLimits: normalizeRequestQuotaLimits(input.quotaLimits, current.quotaLimits ?? emptyRequestQuotaLimits()),
    availabilitySchedule: nextAvailabilitySchedule
  }
  assertApiKeyNameAvailable(systemAccountId, next.name, id)
  const database = getBusinessDatabase()
  const now = nowIso()
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const updates = [
      'name = ?',
      'description = ?',
      'status = ?',
      'group_route_strategy = ?',
      'expires_at = ?',
      'quota_limits_json = ?',
      'availability_schedule_json = ?',
      'updated_at = ?'
    ]
    const updateValues = [
      next.name,
      next.description ?? null,
      next.status,
      next.groupRouteStrategy,
      next.expiresAt ?? null,
      requestQuotaLimitsJson(next.quotaLimits),
      apiKeyAvailabilityScheduleJson(next.availabilitySchedule),
      now
    ]
    updateValues.push(id, systemAccountId)
    database
      .prepare(`UPDATE api_keys SET ${updates.join(', ')} WHERE id = ? AND system_account_id = ?`)
      .run(...updateValues)
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
  const item = value as { groupId?: unknown; priority?: unknown; weight?: unknown; status?: unknown }
  const groupId = optionalString(item.groupId)?.trim()
  if (!groupId) {
    throw new Error('API Key 分组无效')
  }
  const rawPriority = item.priority
  const priority = typeof rawPriority === 'number' && Number.isInteger(rawPriority) && rawPriority > 0
    ? rawPriority
    : index + 1
  const status = item.status === 'disabled' ? 'disabled' : 'active'
  return { groupId, priority, weight: normalizeApiKeyGroupBindingWeight(item.weight), status }
}

function normalizeApiKeyGroupBindings(inputs: ApiKeyGroupBindingInput[], systemAccountId: string): ApiKeyGroupBindingWrite[] {
  if (!inputs.length) {
    throw new Error('API Key 至少需要绑定一个分组')
  }
  if (inputs.length > MAX_API_KEY_GROUP_BINDINGS) {
    throw new Error(`API Key 最多绑定 ${MAX_API_KEY_GROUP_BINDINGS} 个分组`)
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

  const groups = loadApiKeyBindableGroups([...seenGroupIds])
  let providerCode: string | undefined
  return normalized
    .map((binding) => {
      const group = groups.get(binding.groupId)
      if (!group || !canBindApiKeyGroup(binding.groupId, systemAccountId)) {
        throw new Error(API_KEY_GROUP_BOUNDARY_ERROR)
      }
      if (providerCode && group.provider_code !== providerCode) {
        throw new Error(API_KEY_GROUP_PROVIDER_ERROR)
      }
      providerCode = providerCode ?? group.provider_code
      if (binding.status === 'active' && group.enabled === 0) {
        throw new Error(`API Key 不能启用已停用分组：${group.name ?? binding.groupId}`)
      }
      return {
        ...binding,
        weight: normalizeApiKeyGroupBindingWeight(binding.weight),
        groupName: group.name ?? undefined,
        providerCode: group.provider_code,
        groupEnabled: group.enabled !== 0
      }
    })
    .sort((left, right) => left.priority - right.priority || left.groupId.localeCompare(right.groupId))
}

function loadApiKeyBindableGroups(groupIds: string[]): Map<string, ApiKeyBindableGroupRow> {
  const ids = [...new Set(groupIds.filter(Boolean))]
  const result = new Map<string, ApiKeyBindableGroupRow>()
  if (!ids.length) return result
  const database = getBusinessDatabase()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = database
      .prepare(`SELECT id, system_account_id, provider_code, name, enabled FROM groups WHERE id IN (${sqlPlaceholders(chunk.length)})`)
      .all(...chunk) as unknown as ApiKeyBindableGroupRow[]
    for (const row of rows) {
      result.set(row.id, row)
    }
  }
  return result
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
    priority: binding.priority,
    weight: binding.weight,
    status: binding.status,
    groupEnabled: binding.groupEnabled
  }))
}

function recordlessBindingPrefix(): string {
  return 'pending:'
}

function normalizedApiKeyName(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
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

function isDuplicateApiKeyNameError(error: unknown): boolean {
  if (!(error instanceof Error)) return false
  return error.message.includes('idx_api_keys_owner_name_unique_lower')
}
