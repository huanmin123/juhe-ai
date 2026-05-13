import type { DatabaseSync } from 'node:sqlite'

import type { ApiKeySummary, ProviderCode } from '../domain/types.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, currentSystemAccountId, includeSystemAccountFields, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { createApiKey, decryptJson, encryptJson, hashSecret } from './crypto.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getDatabase, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { defaultOpenAIGroupIdForSystemAccount } from './default-group.repository.js'
import { invalidateGatewayApiKeyCacheById } from './gateway-api-key.repository.js'
import { loadSystemAccountNameMap } from './repository-lookups.js'
import { emptyRequestQuotaLimits, normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { USAGE_STATS_RECORD_SELECT_COLUMNS, type UsageStatsRecordRow } from './usage-stats-types.js'
import { subtractUsageStatsRecord } from './usage-stats-writers.js'
import { loadApiKeyUsageSummariesForScopes } from './usage-summary-loaders.js'
import { optionalNullableString, optionalServerDateTimeIso, optionalString } from './value-utils.js'

interface ApiKeyRow {
  id: string
  system_account_id: string
  name: string
  description: string | null
  key_prefix: string
  key_secret_encrypted: string | null
  status: 'active' | 'disabled'
  group_id: string
  group_authorization_id: string | null
  expires_at: string | null
  quota_limits_json: string | null
}

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

type GroupOwnerRow = {
  systemAccountId: string
  providerCode: ProviderCode
}

type ResourceAuthorizationRow = {
  id: string
}

type ApiKeyDeleteRow = {
  id: string
  system_account_id: string
}

type UsageStatsAggregationCursor = {
  cursorCreatedAt: string
  cursorId: string
}

type ApiKeyFilterValue = string | number

const defaultApiKeyListPageSize = 50
const maxApiKeyListPageSize = 200

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

function apiKeySummariesFromRows(rows: ApiKeyRow[], access?: AccessScope): ApiKeySummary[] {
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields ? loadSystemAccountNameMap() : new Map<string, string>()
  const usageScopes = rows.map((row) => ({ rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.id }))
  const usageByApiKey = loadApiKeyUsageSummariesForScopes(usageScopes)
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
    systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
    name: row.name,
    description: row.description ?? undefined,
    keyPrefix: row.key_prefix,
    key: decryptApiKeySecret(row.key_secret_encrypted),
    status: row.status,
    groupId: row.group_id,
    groupAuthorizationId: row.group_authorization_id ?? undefined,
    expiresAt: row.expires_at ?? undefined,
    quotaLimits: parseRequestQuotaLimitsJson(row.quota_limits_json),
    usage: usageByApiKey.get(row.id) ?? emptyAccountUsageSummary()
  }))
}

function normalizeApiKeyListOptions(options?: ApiKeyListOptions): Required<Pick<ApiKeyListOptions, 'page' | 'pageSize'>> & Pick<ApiKeyListOptions, 'keyword' | 'status' | 'groupId'> {
  const rawPage = options?.page
  const rawPageSize = options?.pageSize ?? options?.limit
  const page = typeof rawPage === 'number' && Number.isInteger(rawPage) ? Math.max(1, rawPage) : 1
  const pageSize = typeof rawPageSize === 'number' && Number.isInteger(rawPageSize)
    ? Math.min(maxApiKeyListPageSize, Math.max(1, rawPageSize))
    : defaultApiKeyListPageSize
  return {
    page,
    pageSize,
    keyword: textFilter(options?.keyword),
    status: options?.status === 'active' || options?.status === 'disabled' ? options.status : undefined,
    groupId: textFilter(options?.groupId)
  }
}

function buildApiKeyFilters(scope: { clause: string; params: string[] }, options: ReturnType<typeof normalizeApiKeyListOptions>): { clause: string; params: ApiKeyFilterValue[] } {
  const clauses: string[] = []
  const params: ApiKeyFilterValue[] = []
  if (scope.clause) {
    clauses.push(scope.clause.replace(/^ WHERE /, ''))
    params.push(...scope.params)
  }
  if (options.keyword) {
    clauses.push('(name LIKE ? OR COALESCE(description, \'\') LIKE ? OR key_prefix LIKE ?)')
    params.push(`%${options.keyword}%`, `%${options.keyword}%`, `%${options.keyword}%`)
  }
  if (options.status) {
    clauses.push('status = ?')
    params.push(options.status)
  }
  if (options.groupId) {
    clauses.push('group_id = ?')
    params.push(options.groupId)
  }
  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function textFilter(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
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
  const group = groupOwnerAndProvider(groupId)
  if (group && !scopedOwnerId && canManageApiKeyOwner(group.systemAccountId, access)) {
    systemAccountId = group.systemAccountId
  }
  if (!group || !canUseGroup(groupId, systemAccountId)) {
    throw new Error('API Key 分组无效')
  }
  const groupAuthorization = group.systemAccountId !== systemAccountId ? activeGroupAuthorization(groupId, systemAccountId) : undefined
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
  const nextGroup = groupOwnerAndProvider(nextGroupId)
  if (!nextGroup || !canUseGroup(nextGroupId, systemAccountId)) {
    return undefined
  }
  const nextGroupAuthorization = nextGroup.systemAccountId !== systemAccountId ? activeGroupAuthorization(nextGroupId, systemAccountId) : undefined
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

export interface ApiKeyDeleteResult {
  deleted: boolean
  cleanupRelatedData: () => void
}

export function deleteApiKey(id: string, access?: AccessScope): boolean {
  return deleteApiKeyWithRelatedCleanup(id, access).deleted
}

export function deleteApiKeyWithRelatedCleanup(id: string, access?: AccessScope): ApiKeyDeleteResult {
  const scope = buildSystemAccountScopeClause(access)
  const database = getDatabase()
  const row = database.prepare(`SELECT id, system_account_id FROM api_keys WHERE id = ?${scope.clause}`).get(id, ...scope.params) as unknown as ApiKeyDeleteRow | undefined
  if (!row) {
    return { deleted: false, cleanupRelatedData: () => {} }
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
      cleanupRelatedData: result.changes > 0 ? () => deleteApiKeyRelatedData(row) : () => {}
    }
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    throw error
  }
}

function deleteApiKeyRelatedData(row: ApiKeyDeleteRow): void {
  const database = getRecordDatabase()
  const updatedAt = nowIso()
  const cursor = usageStatsAggregationCursor(database)
  let cursorCreatedAt = ''
  let cursorId = ''
  const batchLimit = 1000
  while (true) {
    const usageRows = database.prepare(`
      SELECT ${USAGE_STATS_RECORD_SELECT_COLUMNS}
      FROM usage_records
      WHERE api_key_id = ?
        AND (created_at > ? OR (created_at = ? AND id > ?))
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `).all(row.id, cursorCreatedAt, cursorCreatedAt, cursorId, batchLimit) as unknown as UsageStatsRecordRow[]
    if (usageRows.length === 0) {
      break
    }
    for (const usageRow of usageRows) {
      if (isUsageRecordAlreadyAggregated(usageRow, cursor)) {
        subtractUsageStatsRecord(database, usageRow, updatedAt)
      }
    }
    const last = usageRows[usageRows.length - 1]
    cursorCreatedAt = last.created_at
    cursorId = last.id
    if (usageRows.length < batchLimit) {
      break
    }
  }
  for (const tableName of ['usage_stats_totals', 'usage_stats_minute', 'usage_stats_hourly', 'usage_stats_daily', 'usage_stats_weekly', 'usage_stats_monthly']) {
    database.prepare(`DELETE FROM ${tableName} WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`).run(row.system_account_id, row.id)
  }
  database.prepare('DELETE FROM usage_records WHERE api_key_id = ?').run(row.id)
}

function usageStatsAggregationCursor(database: DatabaseSync): UsageStatsAggregationCursor {
  const row = database
    .prepare("SELECT cursor_created_at, cursor_id FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'")
    .get() as unknown as { cursor_created_at?: string | null; cursor_id?: string | null } | undefined
  return { cursorCreatedAt: row?.cursor_created_at ?? '', cursorId: row?.cursor_id ?? '' }
}

function isUsageRecordAlreadyAggregated(row: UsageStatsRecordRow, cursor: UsageStatsAggregationCursor): boolean {
  if (!cursor.cursorCreatedAt) return false
  return row.created_at < cursor.cursorCreatedAt || (row.created_at === cursor.cursorCreatedAt && row.id <= cursor.cursorId)
}

function decryptApiKeySecret(value: string | null | undefined): string {
  if (!value) {
    return ''
  }
  const decrypted = decryptJson<{ key?: unknown }>(value)
  return typeof decrypted.key === 'string' ? decrypted.key : ''
}

function canUseGroup(groupId: string, systemAccountId: string): boolean {
  const group = groupOwnerAndProvider(groupId)
  if (group?.systemAccountId === systemAccountId) return true
  return Boolean(activeGroupAuthorization(groupId, systemAccountId))
}

function activeGroupAuthorization(groupId: string, granteeSystemAccountId: string): ResourceAuthorizationRow | undefined {
  const now = nowIso()
  return getDatabase()
    .prepare("SELECT id FROM resource_authorizations WHERE resource_type = 'group' AND resource_id = ? AND grantee_system_account_id = ? AND status = 'active' AND (expires_at IS NULL OR expires_at > ?) LIMIT 1")
    .get(groupId, granteeSystemAccountId, now) as unknown as ResourceAuthorizationRow | undefined
}

function groupOwnerAndProvider(groupId: string): GroupOwnerRow | undefined {
  const row = getDatabase().prepare('SELECT system_account_id, provider_code FROM groups WHERE id = ?').get(groupId) as unknown as { system_account_id?: string; provider_code?: ProviderCode } | undefined
  return row?.system_account_id && row.provider_code ? { systemAccountId: row.system_account_id, providerCode: row.provider_code } : undefined
}

function apiKeySystemAccountId(apiKeyId: string): string | undefined {
  const row = getDatabase().prepare('SELECT system_account_id FROM api_keys WHERE id = ?').get(apiKeyId) as unknown as { system_account_id?: string } | undefined
  return row?.system_account_id
}

function canManageApiKeyOwner(ownerSystemAccountId: string, access?: AccessScope): boolean {
  const scopedOwnerId = manageableSystemAccountId(access)
  return !scopedOwnerId || scopedOwnerId === ownerSystemAccountId
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
