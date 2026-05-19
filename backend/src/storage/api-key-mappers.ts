import type { ApiKeySummary } from '../domain/types.js'
import { includeSystemAccountFields, type AccessScope } from './access-scope.js'
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
  key_secret_encrypted: string | null
  status: 'active' | 'disabled'
  group_id: string
  group_authorization_id: string | null
  expires_at: string | null
  quota_limits_json: string | null
}

export function apiKeySummariesFromRows(rows: ApiKeyRow[], access?: AccessScope, options: { includeSecret?: boolean } = {}): ApiKeySummary[] {
  const includeSecret = options.includeSecret ?? true
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
  const usageScopes = rows.map((row) => ({ rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.id }))
  const usageByApiKey = loadApiKeyUsageSummariesForScopes(usageScopes)
  return rows.map((row) => ({
    id: row.id,
    systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
    systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
    name: row.name,
    description: row.description ?? undefined,
    keyPrefix: row.key_prefix,
    key: includeSecret ? decryptApiKeySecret(row.key_secret_encrypted) : '',
    status: row.status,
    groupId: row.group_id,
    groupAuthorizationId: row.group_authorization_id ?? undefined,
    expiresAt: row.expires_at ?? undefined,
    quotaLimits: parseRequestQuotaLimitsJson(row.quota_limits_json),
    usage: usageByApiKey.get(row.id) ?? emptyAccountUsageSummary()
  }))
}

function decryptApiKeySecret(value: string | null | undefined): string {
  if (!value) {
    return ''
  }
  const decrypted = decryptJson<{ key?: unknown }>(value)
  return typeof decrypted.key === 'string' ? decrypted.key : ''
}
