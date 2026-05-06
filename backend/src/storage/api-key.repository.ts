import type { ApiKeySummary, ProviderCode } from '../domain/types.js'
import { buildSystemAccountScopeClause, buildSystemAccountWhereClause, currentSystemAccountId, includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { createApiKey, decryptJson, encryptJson, hashSecret } from './crypto.js'
import { getDatabase, newId, nowIso } from './database.js'
import { defaultOpenAIGroupIdForSystemAccount } from './default-group.repository.js'
import { invalidateGatewayApiKeyCacheById } from './gateway-api-key.repository.js'
import { loadSystemAccountNameMap } from './repository-lookups.js'
import { emptyRequestQuotaLimits, normalizeRequestQuotaLimits, parseRequestQuotaLimitsJson, requestQuotaLimitsJson } from './request-quota-limits.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import type { UsageStatsRecordRow } from './usage-stats-types.js'
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

export function listApiKeys(access?: AccessScope): ApiKeySummary[] {
  const scope = buildSystemAccountWhereClause(access)
  const rows = getDatabase().prepare(`SELECT * FROM api_keys${scope.clause} ORDER BY updated_at DESC`).all(...scope.params) as unknown as ApiKeyRow[]
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

export function createApiKeyRecord(input: Record<string, unknown>): ApiKeySummary & { key: string } {
  const now = nowIso()
  const key = createApiKey()
  const keyPrefix = key.slice(0, 8)
  const systemAccountId = currentSystemAccountId()
  const explicitGroupId = typeof input.groupId === 'string' && input.groupId ? input.groupId : typeof input.group_id === 'string' && input.group_id ? input.group_id : undefined
  const groupId = explicitGroupId ?? defaultOpenAIGroupIdForSystemAccount(systemAccountId)
  if (!groupId) {
    throw new Error('Invalid API key group')
  }
  const group = groupOwnerAndProvider(groupId)
  if (!group || !canUseGroup(groupId, systemAccountId)) {
    throw new Error('Invalid API key group')
  }
  const groupAuthorization = group.systemAccountId !== systemAccountId ? activeGroupAuthorization(groupId, systemAccountId) : undefined
  if (group.systemAccountId !== systemAccountId && !groupAuthorization) {
    throw new Error('Invalid API key group')
  }
  const quotaLimits = normalizeRequestQuotaLimits(input.quotaLimits)
  const record: ApiKeySummary & { key: string } = {
    id: newId('key'),
    systemAccountId: includeSystemAccountFields() ? systemAccountId : undefined,
    systemAccountName: includeSystemAccountFields() ? loadSystemAccountNameMap().get(systemAccountId) : undefined,
    name: String(input.name ?? '未命名 API Key'),
    description: optionalString(input.description),
    keyPrefix,
    status: input.status === 'disabled' ? 'disabled' : 'active',
    groupId,
    expiresAt: optionalServerDateTimeIso(input.expiresAt ?? input.expires_at),
    quotaLimits,
    usage: emptyAccountUsageSummary(),
    key
  }
  getDatabase()
    .prepare(`
      INSERT INTO api_keys (id, system_account_id, name, description, key_hash, key_prefix, key_secret_encrypted, status, group_id, group_authorization_id, expires_at, quota_limits_json, scopes_json, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `)
    .run(record.id, systemAccountId, record.name, record.description ?? null, hashSecret(key), record.keyPrefix, encryptJson({ key }), record.status, record.groupId, groupAuthorization?.id ?? null, record.expiresAt ?? null, requestQuotaLimitsJson(record.quotaLimits), JSON.stringify(input.scopes ?? []), now, now)
  return record
}

export function updateApiKey(id: string, input: Record<string, unknown>): ApiKeySummary | undefined {
  const current = listApiKeys().find((apiKey) => apiKey.id === id)
  if (!current) {
    return undefined
  }
  const systemAccountId = apiKeySystemAccountId(id)
  if (!systemAccountId) {
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
    name: typeof input.name === 'string' ? input.name : current.name,
    description: input.description === undefined ? current.description : optionalNullableString(input.description) ?? undefined,
    status: input.status === 'disabled' ? 'disabled' : input.status === 'active' ? 'active' : current.status,
    groupId: nextGroupId,
    expiresAt: optionalServerDateTimeIso(input.expiresAt ?? input.expires_at) ?? current.expiresAt,
    quotaLimits: normalizeRequestQuotaLimits(input.quotaLimits, current.quotaLimits ?? emptyRequestQuotaLimits())
  }
  getDatabase()
    .prepare('UPDATE api_keys SET name = ?, description = ?, status = ?, group_id = ?, group_authorization_id = ?, expires_at = ?, quota_limits_json = ?, updated_at = ? WHERE id = ? AND system_account_id = ?')
    .run(next.name, next.description ?? null, next.status, next.groupId, nextGroupAuthorization?.id ?? null, next.expiresAt ?? null, requestQuotaLimitsJson(next.quotaLimits), nowIso(), id, systemAccountId)
  invalidateGatewayApiKeyCacheById(id)
  return next
}

export function deleteApiKey(id: string): boolean {
  const scope = buildSystemAccountScopeClause()
  const database = getDatabase()
  const row = database.prepare(`SELECT id, system_account_id FROM api_keys WHERE id = ?${scope.clause}`).get(id, ...scope.params) as unknown as ApiKeyDeleteRow | undefined
  if (!row) {
    return false
  }

  database.exec('BEGIN')
  try {
    deleteApiKeyRelatedData(database, row)
    const result = database.prepare('DELETE FROM api_keys WHERE id = ? AND system_account_id = ?').run(row.id, row.system_account_id)
    database.exec('COMMIT')
    if (result.changes > 0) {
      invalidateGatewayApiKeyCacheById(row.id)
    }
    return result.changes > 0
  } catch (error) {
    try {
      database.exec('ROLLBACK')
    } catch {
    }
    throw error
  }
}

function deleteApiKeyRelatedData(database: ReturnType<typeof getDatabase>, row: ApiKeyDeleteRow): void {
  const updatedAt = nowIso()
  const cursor = usageStatsAggregationCursor(database)
  const usageRows = database.prepare('SELECT * FROM usage_records WHERE api_key_id = ? ORDER BY created_at ASC, id ASC').all(row.id) as unknown as UsageStatsRecordRow[]
  for (const usageRow of usageRows) {
    if (!isUsageRecordAlreadyAggregated(usageRow, cursor)) {
      continue
    }
    subtractUsageStatsRecord(database, usageRow, updatedAt)
  }
  for (const tableName of ['usage_stats_totals', 'usage_stats_daily', 'usage_stats_hourly']) {
    database.prepare(`DELETE FROM ${tableName} WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?`).run(row.system_account_id, row.id)
  }
  database.prepare('DELETE FROM audit_logs WHERE api_key_id = ?').run(row.id)
  database.prepare('DELETE FROM usage_records WHERE api_key_id = ?').run(row.id)
}

function usageStatsAggregationCursor(database: ReturnType<typeof getDatabase>): UsageStatsAggregationCursor {
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
