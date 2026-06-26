import type { ApiKeyGroupBindingSummary, ApiKeySummary } from '../domain/types.js'
import { normalizeApiKeyGroupRouteStrategy } from '../domain/api-key-routing.js'
import {
  normalizeApiKeyRouteMode,
  parseHybridRoutingConfigJson
} from '../domain/api-key-hybrid-routing.js'
import {
  normalizeApiKeyClientProfile,
  parseExplicitHybridRouteRulesJson
} from '../domain/api-key-explicit-hybrid-routing.js'
import { includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { parseApiKeyAvailabilityScheduleJson } from './api-key-availability-schedule.js'
import { loadApiKeyGroupBindingSummariesByApiKeyIds, loadApiKeyGroupBindingSummariesByApiKeyIdsAsync } from './api-key-group-bindings.repository.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { parseRequestQuotaLimitsJson } from './request-quota-limits.js'
import { runtimeConfig } from '../config/runtime.js'
import { emptyAccountUsageSummary } from './usage-stats-helpers.js'
import { loadApiKeyUsageSummariesForScopes } from './usage-summary-loaders.js'
import { chunkValues } from './query-utils.js'

export interface ApiKeyRow {
  id: string
  system_account_id: string
  name: string
  description: string | null
  key_prefix: string
  key_suffix: string
  key_secret_encrypted?: string | null
  status: 'active' | 'disabled'
  client_profile?: ApiKeySummary['clientProfile'] | null
  route_mode?: ApiKeySummary['routeMode'] | null
  group_route_strategy?: ApiKeySummary['groupRouteStrategy'] | null
  hybrid_routing_config_json?: string | null
  explicit_hybrid_route_rules_json?: string | null
  group_owner_system_account_name?: string | null
  expires_at: string | null
  quota_limits_json: string | null
  availability_schedule_json?: string | null
  availability_schedule_active?: number | null
}

export function apiKeySummariesFromRows(
  rows: ApiKeyRow[],
  access?: AccessScope,
  options: { includeSecret?: boolean; bindingsByApiKeyId?: Map<string, ApiKeyGroupBindingSummary[]> } = {}
): ApiKeySummary[] {
  const includeSecret = options.includeSecret === true
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id)) : new Map<string, string>()
  const usageScopes = rows.map((row) => ({ rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.id }))
  const usageByApiKey = loadApiKeyUsageSummariesForScopes(usageScopes)
  const bindingsByApiKeyId = options.bindingsByApiKeyId ?? loadApiKeyGroupBindingSummariesByApiKeyIds(rows.map((row) => row.id))
  return rows.map((row) => {
    const groupBindings = bindingsByApiKeyId.get(row.id) ?? []
    const availabilitySchedule = parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json)
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      name: row.name,
      description: row.description ?? undefined,
      keyPrefix: row.key_prefix,
      keySuffix: row.key_suffix,
      key: includeSecret ? decryptApiKeySecret(row.key_secret_encrypted) : '',
      status: row.status,
      clientProfile: normalizeApiKeyClientProfile(row.client_profile),
      routeMode: normalizeApiKeyRouteMode(row.route_mode),
      groupRouteStrategy: normalizeApiKeyGroupRouteStrategy(row.group_route_strategy),
      hybridRoutingConfig: row.route_mode === 'hybrid'
        ? parseHybridRoutingConfigJson(row.hybrid_routing_config_json)
        : undefined,
      explicitHybridRouteRules: parseExplicitHybridRouteRulesJson(row.explicit_hybrid_route_rules_json),
      groupBindings,
      groupOwnerSystemAccountName: row.group_owner_system_account_name ?? undefined,
      expiresAt: row.expires_at ?? undefined,
      quotaLimits: parseRequestQuotaLimitsJson(row.quota_limits_json),
      availabilitySchedule,
      availabilityScheduleActive: availabilitySchedule?.enabled
        ? row.availability_schedule_active !== 0
        : undefined,
      usage: usageByApiKey.get(row.id) ?? emptyAccountUsageSummary()
    }
  })
}

export async function apiKeySummariesFromRowsAsync(
  rows: ApiKeyRow[],
  access?: AccessScope,
  options: { includeSecret?: boolean; bindingsByApiKeyId?: Map<string, ApiKeyGroupBindingSummary[]> } = {}
): Promise<ApiKeySummary[]> {
  const includeSecret = options.includeSecret === true
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const client = await getApiKeyMapperDatabaseClient()
  const accountNames = shouldIncludeSystemAccountFields
    ? await loadSystemAccountNameMapByIdsAsync(client, rows.map((row) => row.system_account_id))
    : new Map<string, string>()
  const bindingsByApiKeyId = options.bindingsByApiKeyId ?? await loadApiKeyGroupBindingSummariesByApiKeyIdsAsync(rows.map((row) => row.id))
  return rows.map((row) => {
    const groupBindings = bindingsByApiKeyId.get(row.id) ?? []
    const availabilitySchedule = parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json)
    return {
      id: row.id,
      systemAccountId: shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
      systemAccountName: shouldIncludeSystemAccountFields ? accountNames.get(row.system_account_id) : undefined,
      name: row.name,
      description: row.description ?? undefined,
      keyPrefix: row.key_prefix,
      keySuffix: row.key_suffix,
      key: includeSecret ? decryptApiKeySecret(row.key_secret_encrypted) : '',
      status: row.status,
      clientProfile: normalizeApiKeyClientProfile(row.client_profile),
      routeMode: normalizeApiKeyRouteMode(row.route_mode),
      groupRouteStrategy: normalizeApiKeyGroupRouteStrategy(row.group_route_strategy),
      hybridRoutingConfig: row.route_mode === 'hybrid'
        ? parseHybridRoutingConfigJson(row.hybrid_routing_config_json)
        : undefined,
      explicitHybridRouteRules: parseExplicitHybridRouteRulesJson(row.explicit_hybrid_route_rules_json),
      groupBindings,
      groupOwnerSystemAccountName: row.group_owner_system_account_name ?? undefined,
      expiresAt: row.expires_at ?? undefined,
      quotaLimits: parseRequestQuotaLimitsJson(row.quota_limits_json),
      availabilitySchedule,
      availabilityScheduleActive: availabilitySchedule?.enabled
        ? Number(row.availability_schedule_active ?? 1) !== 0
        : undefined,
      usage: emptyAccountUsageSummary()
    }
  })
}

function decryptApiKeySecret(value: string | null | undefined): string {
  if (!value) {
    throw new Error('API Key 密文缺少完整密钥')
  }
  const decrypted = decryptJson<{ key?: unknown }>(value)
  if (typeof decrypted.key !== 'string' || decrypted.key.length === 0) {
    throw new Error('API Key 密文缺少完整密钥')
  }
  return decrypted.key
}

async function getApiKeyMapperDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

async function loadSystemAccountNameMapByIdsAsync(client: DatabaseClient, systemAccountIds: Array<string | undefined>): Promise<Map<string, string>> {
  const ids = [...new Set(systemAccountIds.map((value) => value?.trim()).filter((value): value is string => Boolean(value)))]
  if (!ids.length) return new Map()
  const rows: Array<{ id: string; display_name: string }> = []
  const table = apiKeyMapperTable(client, 'system_accounts')
  for (const chunk of chunkValues(ids, 500)) {
    rows.push(...await client.query<{ id: string; display_name: string }>(`
      SELECT id, display_name
      FROM ${table}
      WHERE id IN (${client.dialect.bindPlaceholders(chunk.length)})
    `, chunk))
  }
  return new Map(rows.map((row) => [row.id, row.display_name]))
}

function apiKeyMapperTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
