import type { ApiKeySummary } from '../domain/types.js'
import { normalizeRouteStrategyMode } from '../domain/route-strategy.js'
import { includeSystemAccountFields, type AccessScope } from './access-scope.js'
import { parseApiKeyAvailabilityScheduleJson } from './api-key-availability-schedule.js'
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
  system_account_name?: string | null
  route_strategy_id: string
  route_strategy_name?: string | null
  route_strategy_mode?: ApiKeySummary['routeStrategyMode'] | null
  route_strategy_status?: ApiKeySummary['routeStrategyStatus'] | null
  name: string
  description: string | null
  key_prefix: string
  key_suffix: string
  key_secret_encrypted?: string | null
  status: 'active' | 'disabled'
  is_default?: number | string | boolean | null
  expires_at: string | null
  quota_limits_json: string | null
  availability_schedule_json?: string | null
}

export function apiKeySummariesFromRows(
  rows: ApiKeyRow[],
  access?: AccessScope,
  options: { includeSecret?: boolean } = {}
): ApiKeySummary[] {
  const includeSecret = options.includeSecret === true
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const accountNames = shouldIncludeSystemAccountFields
    ? loadSystemAccountNameMapByIds(rows.map((row) => row.system_account_id))
    : new Map<string, string>()
  const usageScopes = rows.map((row) => ({ rowKey: row.id, systemAccountId: row.system_account_id, scopeId: row.id }))
  const usageByApiKey = loadApiKeyUsageSummariesForScopes(usageScopes)
  return rows.map((row) => apiKeySummaryFromRow(row, {
    includeSecret,
    shouldIncludeSystemAccountFields,
    accountNames,
    usage: usageByApiKey.get(row.id) ?? emptyAccountUsageSummary()
  }))
}

export async function apiKeySummariesFromRowsAsync(
  rows: ApiKeyRow[],
  access?: AccessScope,
  options: { includeSecret?: boolean } = {}
): Promise<ApiKeySummary[]> {
  const includeSecret = options.includeSecret === true
  const shouldIncludeSystemAccountFields = includeSystemAccountFields(access)
  const client = await getApiKeyMapperDatabaseClient()
  const accountNames = shouldIncludeSystemAccountFields
    ? await loadSystemAccountNameMapByIdsAsync(client, rows.map((row) => row.system_account_id))
    : new Map<string, string>()
  return rows.map((row) => apiKeySummaryFromRow(row, {
    includeSecret,
    shouldIncludeSystemAccountFields,
    accountNames,
    usage: emptyAccountUsageSummary()
  }))
}

function apiKeySummaryFromRow(
  row: ApiKeyRow,
  options: {
    includeSecret: boolean
    shouldIncludeSystemAccountFields: boolean
    accountNames: Map<string, string>
    usage: ApiKeySummary['usage']
  }
): ApiKeySummary {
  const availabilitySchedule = parseApiKeyAvailabilityScheduleJson(row.availability_schedule_json)
  const routeStrategyMode = row.route_strategy_mode ? normalizeRouteStrategyMode(row.route_strategy_mode) : undefined
  return {
    id: row.id,
    systemAccountId: options.shouldIncludeSystemAccountFields ? row.system_account_id : undefined,
    systemAccountName: options.shouldIncludeSystemAccountFields
      ? (row.system_account_name ?? options.accountNames.get(row.system_account_id))
      : undefined,
    name: row.name,
    description: row.description ?? undefined,
    keyPrefix: row.key_prefix,
    keySuffix: row.key_suffix,
    key: options.includeSecret ? decryptApiKeySecret(row.key_secret_encrypted) : '',
    status: row.status,
    isDefault: normalizeApiKeyDefaultFlag(row.is_default),
    routeStrategyId: row.route_strategy_id,
    routeStrategyName: row.route_strategy_name ?? undefined,
    routeStrategyMode,
    routeStrategyStatus: normalizeRouteStrategyStatus(row.route_strategy_status),
    expiresAt: row.expires_at ?? undefined,
    quotaLimits: parseRequestQuotaLimitsJson(row.quota_limits_json),
    availabilitySchedule,
    usage: options.usage
  }
}

function normalizeRouteStrategyStatus(value: unknown): ApiKeySummary['routeStrategyStatus'] {
  return value === 'active' || value === 'disabled' ? value : undefined
}

function normalizeApiKeyDefaultFlag(value: unknown): boolean {
  return value === true || value === 1 || value === '1'
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
