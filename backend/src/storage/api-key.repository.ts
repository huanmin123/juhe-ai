import type { ApiKeySummary } from '../domain/types.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { apiKeyGroupAuthorization, apiKeyGroupOwnerAndProvider, apiKeySystemAccountId, canManageApiKeyOwner, canUseApiKeyGroup } from './api-key-access.js'
import { createApiKey, encryptJson, hashSecret } from './crypto.js'
import { buildApiKeyFilters, normalizeApiKeyListOptions } from './api-key-list-query.js'
import { apiKeySummariesFromRows, type ApiKeyRow } from './api-key-mappers.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { defaultOpenAIGroupIdForSystemAccount } from './default-group.repository.js'
import { invalidateGatewayApiKeyCacheById } from './gateway-api-key.repository.js'
import { loadSystemAccountNameMap } from './repository-lookups.js'
import { emptyRequestQuotaLimits, normalizeRequestQuotaLimits, requestQuotaLimitsJson } from './request-quota-limits.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { optionalNullableString, optionalServerDateTimeIso, optionalString } from './value-utils.js'

export interface ApiKeyListOptions {
  page?: number
  pageSize?: number
  limit?: number
  keyword?: string
  status?: 'active' | 'disabled' | 'all'
  groupId?: string
}

export interface ApiKeyListResult {
  items: ApiKeySummary[]
  total: number
  page: number
  pageSize: number
}

type ApiKeyDeleteRow = {
  id: string
  system_account_id: string
}

export function listApiKeys(access?: AccessScope, options?: ApiKeyListOptions): ApiKeySummary[] {
  return queryApiKeys(access, options).items
}

export function listApiKeysPage(access?: AccessScope, options?: ApiKeyListOptions): ApiKeyListResult {
  return queryApiKeys(access, options, true)
}

function queryApiKeys(access?: AccessScope, options?: ApiKeyListOptions, paged = false): ApiKeyListResult {
  const normalized = normalizeApiKeyListOptions(options)
  const scope = buildSystemAccountWhereClause(access)
  const filters = buildApiKeyFilters(scope, normalized)
  const limitClause = paged ? 'LIMIT ? OFFSET ?' : ''
  const limitParams = paged ? [normalized.pageSize, (normalized.page - 1) * normalized.pageSize] : []
  const totalRow = getDatabase()
    .prepare(`SELECT COUNT(*) AS total FROM api_keys ${filters.clause}`)
    .get(...filters.params) as { total?: number } | undefined
  const rows = getDatabase()
    .prepare(`SELECT * FROM api_keys ${filters.clause} ORDER BY updated_at DESC, created_at DESC, id DESC ${limitClause}`)
    .all(...filters.params, ...limitParams) as unknown as ApiKeyRow[]
  const items = apiKeySummariesFromRows(rows, access)
  return {
    items,
    total: Number(totalRow?.total ?? 0),
    page: normalized.page,
    pageSize: normalized.pageSize
  }
}

export function createApiKeyRecord(input: Record<string, unknown>, access?: AccessScope): ApiKeySummary & { key: string } {
  const now = nowIso()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const scopedOwnerId = manageableSystemAccountId(access)
  let systemAccountId = scopedOwnerId ?? currentSystemAccountId(access)
  const explicitGroupId = typeof input.groupId === 'string' && input.groupId ? input.groupId : typeof input.group_id === 'string' && input.group_id ? input.group_id : undefined
  const groupId = explicitGroupId ?? defaultOpenAIGroupIdForSystemAccount(systemAccountId)
  if (!groupId) {
    throw new Error('API Key 分组无效')
  }
  const group = apiKeyGroupOwnerAndProvider(groupId)
  if (group && !scopedOwnerId && canManageApiKeyOwner(group.systemAccountId, access)) {
    systemAccountId = group.systemAccountId
  }
  if (!group || !canUseApiKeyGroup(groupId, systemAccountId)) {
    throw new Error('API Key 分组无效')
  }
  const groupAuthorization = apiKeyGroupAuthorization(group.systemAccountId, groupId, systemAccountId)
  if (group.systemAccountId !== systemAccountId && !groupAuthorization) {
    throw new Error('API Key 分组无效')
  }
  const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits)
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields(access) ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields(access) ? loadSystemAccountNameMap().get(systemAccountId) : undefined,
    name: normalizedApiKeyName(input.name, '未命名 API Key'),
    description: optionalString(input.description),
    keyPrefix,
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupId,
    expiresAt: optionalServerDateTimeIso(input.expiresAt ?? input.expires_at),
    quotaLimits,
    usage: emptyAccountUsageSummary(),
    key
  }
  assertApiKeyNameAvailable(systemAccountId, record.name)
  try {
    getDatabase()
      .prepare(`
        INSERT INTO api_keys (id, system_account_id, name, description, key_hash, key_prefix, key_secret_encrypted, status, group_id, group_authorization_id, expires_at, quota_limits_json, scopes_json, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      `)
      .run(record.id, systemAccountId, record.name, record.description ?? null, hashSecret(key), record.keyPrefix, encryptJson({ key }), record.status, record.groupId, groupAuthorization?.id ?? null, record.expiresAt ?? null, requestQuotaLimitsJson(record.quotaLimits), JSON.stringify(input.scopes ?? []), now, now)
  } catch (error) {
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${record.name}`)
    }
    throw error
  }
  return record
}

export function updateApiKey(id: string, input: Record<string, unknown>, access?: AccessScope): ApiKeySummary | undefined {
  const systemAccountId = apiKeySystemAccountId(id)
  if (!systemAccountId || !canManageApiKeyOwner(systemAccountId, access)) {
    return undefined
  }
  const current = listApiKeys({ systemAccountId, role: 'user' }).find((apiKey) => apiKey.id === id)
  if (!current) {
    return undefined
  }
  const nextGroupId = typeof input.groupId === 'string' ? input.groupId : typeof input.group_id === 'string' ? input.group_id : current.groupId
  const nextGroup = apiKeyGroupOwnerAndProvider(nextGroupId)
  if (!nextGroup || !canUseApiKeyGroup(nextGroupId, systemAccountId)) {
    return undefined
  }
  const nextGroupAuthorization = apiKeyGroupAuthorization(nextGroup.systemAccountId, nextGroupId, systemAccountId)
  if (nextGroup.systemAccountId !== systemAccountId && !nextGroupAuthorization) {
    return undefined
  }
  const next: ApiKeySummary = {
    ...current,
    name: typeof input.name === 'string' && input.name.trim() ? input.name.trim() : current.name,
    description: input.description === undefined ? current.description : optionalNullableString(input.description) ?? undefined,
    status: input.status === 'disabled' ? 'disabled' : input.status === 'active' ? 'active' : current.status,
    groupId: nextGroupId,
    expiresAt: optionalServerDateTimeIso(input.expiresAt ?? input.expires_at) ?? current.expiresAt,
    quotaLimits: normalizeRequestQuotaLimits(input.quotaLimits, current.quotaLimits ?? emptyRequestQuotaLimits())
  }
  assertApiKeyNameAvailable(systemAccountId, next.name, id)
  try {
    getDatabase()
      .prepare('UPDATE api_keys SET name = ?, description = ?, status = ?, group_id = ?, group_authorization_id = ?, expires_at = ?, quota_limits_json = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
      .run(next.name, next.description ?? null, next.status, next.groupId, nextGroupAuthorization?.id ?? null, next.expiresAt ?? null, requestQuotaLimitsJson(next.quotaLimits), nowIso(), id, systemAccountId)
  } catch (error) {
    if (isDuplicateApiKeyNameError(error)) {
      throw new Error(`API Key 名称已存在：${next.name}`)
    }
    throw error
  }
  invalidateGatewayApiKeyCacheById(id)
  return next
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
  const database = getDatabase()
  const row = database.prepare(`SELECT id, system_account_id FROM api_keys WHERE id = ?${scope.clause}`).get(id, ...scope.params) as unknown as ApiKeyDeleteRow | undefined
  if (!row) {
    return { deleted: false }
  }

  const transactionStarted = beginDatabaseTransaction(database)
  try {
    const result = database.prepare('DELETE FROM api_keys WHERE id = ? AND system_account_id = ?').run(row.id, row.system_account_id)
    commitDatabaseTransaction(database, transactionStarted)
    if (result.changes > 0) {
      invalidateGatewayApiKeyCacheById(row.id)
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

function normalizedApiKeyName(value: unknown, fallback: string): string {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback
}

function assertApiKeyNameAvailable(systemAccountId: string, name: string, excludeId?: string): void {
  const params: string[] = [systemAccountId, name]
  const excludeClause = excludeId ? ' AND id <> ?' : ''
  if (excludeId) {
    params.push(excludeId)
  }
  const row = getDatabase()
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
