import type { ApiKeyGroupBindingSummary, ApiKeySummary } from '../domain/types.js'
import { includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { loadApiKeyGroupBindingSummariesByApiKeyIds } from './api-key-group-bindings.repository.js'
import { decryptJson } from './crypto.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { loadApiKeyUsageSummariesForScopes } from './usage-summary-loaders.js'

export interface ApiKeyRow {
  id: string
  system_account_id: string
  name: string
  description: string | null
  key_prefix: string
  key_secret_encrypted: string
  status: 'active' | 'disabled'
  group_id: string
  group_name?: string | null
  group_owner_system_account_name?: string | null
  expires_at: string | null
  quota_limits_json: string | null
}

export function apiKeySummariesFromRows(
  rows: ApiKeyRow[],
  access?: AccessScope,
  options: { includeSecret?: boolean; bindingsByApiKeyId?: Map<string, ApiKeyGroupBindingSummary[]> } = {}
): ApiKeySummary[] {
  const includeSecret = options.includeSecret ?? true
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
  const usageScopes = rows.map((row) => ({ rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.id }))
  const usageByApiKey = loadApiKeyUsageSummariesForScopes(usageScopes)
  const bindingsByApiKeyId = options.bindingsByApiKeyId ?? loadApiKeyGroupBindingSummariesByApiKeyIds(rows.map((row) => row.id))
  return rows.map((row) => {
    const groupBindings = apiKeyGroupBindingsForRow(row, bindingsByApiKeyId.get(row.id))
    const primaryBinding = groupBindings.find((binding) => binding.status === 'active') ?? groupBindings[0]
    const groupId = primaryBinding?.groupId ?? row.group_id
    const groupName = primaryBinding?.groupName ?? row.group_name ?? undefined
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      name: row.name,
      description: row.description ?? undefined,
      keyPrefix: row.key_prefix,
      key: includeSecret ? decryptApiKeySecret(row.key_secret_encrypted) : '',
      status: row.status,
      groupId,
      groupName,
      primaryGroupId: groupId,
      primaryGroupName: groupName,
      groupBindings,
      groupOwnerSystemAccountName: row.group_owner_system_account_name ?? undefined,
      expiresAt: row.expires_at ?? undefined,
      quotaLimits: parseRequestQuotaLimitsJson(row.quota_limits_json),
      usage: usageByApiKey.get(row.id) ?? emptyAccountUsageSummary()
    }
  })
}

function decryptApiKeySecret(value: string): string {
  const decrypted = decryptJson<{ key?: unknown }>(value)
  if (typeof decrypted.key !== 'string' || decrypted.key.length === 0) {
    throw new Error('API Key 密文缺少完整密钥')
  }
  return decrypted.key
}

function apiKeyGroupBindingsForRow(row: ApiKeyRow, bindings: ApiKeyGroupBindingSummary[] | undefined): ApiKeyGroupBindingSummary[] {
  if (bindings?.length) return bindings
  return [{
    id: `legacy:${row.id}:${row.group_id}`,
    groupId: row.group_id,
    groupName: row.group_name ?? undefined,
    priority: 1,
    status: 'active',
    groupEnabled: true
  }]
}
