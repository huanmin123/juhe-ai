import type { ApiKeyGroupBindingSummary } from '../domain/types.js'
import { normalizeApiKeyGroupBindingWeight } from '../domain/api-key-routing.js'
import { runtimeConfig } from '../config/runtime.js'
import { getBusinessDatabase, nowIso } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'

export interface ApiKeyGroupBindingRow {
  id: string
  api_key_id: string
  system_account_id: string
  group_id: string
  group_name: string | null
  provider_code: string | null
  provider_protocol_profile_id: string | null
  protocol_code: string | null
  protocol_version: string | null
  group_enabled: number | null
  priority: number
  weight?: number | null
  status: 'active' | 'disabled' | string
}

export function loadApiKeyGroupBindingSummariesByApiKeyIds(apiKeyIds: string[]): Map<string, ApiKeyGroupBindingSummary[]> {
  const ids = [...new Set(apiKeyIds.filter(Boolean))]
  const result = new Map<string, ApiKeyGroupBindingSummary[]>()
  if (!ids.length) return result

  const database = getBusinessDatabase()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = database
      .prepare(`
        SELECT
          api_key_group_bindings.id,
          api_key_group_bindings.api_key_id,
          api_key_group_bindings.system_account_id,
          api_key_group_bindings.group_id,
          api_key_group_bindings.priority,
          api_key_group_bindings.weight,
          api_key_group_bindings.status,
          groups.name AS group_name,
          groups.provider_code,
          groups.provider_protocol_profile_id,
          groups.protocol_code,
          groups.protocol_version,
        CASE
          WHEN groups.id IS NULL THEN 0
          WHEN groups.system_account_id = api_key_group_bindings.system_account_id THEN groups.enabled
          WHEN group_authorization.id IS NOT NULL THEN CASE WHEN groups.enabled = 1 THEN COALESCE(group_authorization_settings.enabled, 1) ELSE 0 END
          ELSE 0
        END AS group_enabled
      FROM api_key_group_bindings
      LEFT JOIN groups ON groups.id = api_key_group_bindings.group_id
        LEFT JOIN resource_authorizations group_authorization
          ON group_authorization.resource_type = 'group'
          AND group_authorization.resource_id = groups.id
          AND group_authorization.grantee_system_account_id = api_key_group_bindings.system_account_id
          AND group_authorization.status = 'active'
          AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
        LEFT JOIN group_authorization_settings
          ON group_authorization_settings.authorization_id = group_authorization.id
          AND group_authorization_settings.system_account_id = api_key_group_bindings.system_account_id
          AND group_authorization_settings.group_id = groups.id
        WHERE api_key_group_bindings.api_key_id IN (${sqlPlaceholders(chunk.length)})
        ORDER BY api_key_group_bindings.api_key_id ASC,
          CASE WHEN api_key_group_bindings.status = 'active' THEN 0 ELSE 1 END ASC,
          api_key_group_bindings.priority ASC,
          api_key_group_bindings.created_at ASC,
          api_key_group_bindings.id ASC
      `)
      .all(now, ...chunk) as unknown as ApiKeyGroupBindingRow[]
    for (const row of rows) {
      if (!Number.isInteger(row.priority) || row.priority <= 0) {
        throw new Error(`API Key 分组绑定优先级无效：${row.id}`)
      }
      if (row.status !== 'active' && row.status !== 'disabled') {
        throw new Error(`API Key 分组绑定状态无效：${row.id}`)
      }
      if (row.group_enabled !== 0 && row.group_enabled !== 1) {
        throw new Error(`API Key 分组绑定关联分组状态无效：${row.id}`)
      }
      const item: ApiKeyGroupBindingSummary = {
        id: row.id,
        groupId: row.group_id,
        groupName: row.group_name ?? undefined,
        providerCode: row.provider_code ?? undefined,
        providerProtocolProfileId: row.provider_protocol_profile_id ?? undefined,
        protocolCode: row.protocol_code ?? undefined,
        protocolVersion: row.protocol_version ?? undefined,
        priority: row.priority,
        weight: normalizeApiKeyGroupBindingWeight(row.weight),
        status: row.status,
        groupEnabled: row.group_enabled === 1
      }
      const existing = result.get(row.api_key_id) ?? []
      existing.push(item)
      result.set(row.api_key_id, existing)
    }
  }
  return result
}

export async function loadApiKeyGroupBindingSummariesByApiKeyIdsAsync(apiKeyIds: string[]): Promise<Map<string, ApiKeyGroupBindingSummary[]>> {
  const ids = [...new Set(apiKeyIds.filter(Boolean))]
  const result = new Map<string, ApiKeyGroupBindingSummary[]>()
  if (!ids.length) return result

  const client = await getApiKeyGroupBindingDatabaseClient()
  const now = nowIso()
  for (const chunk of chunkValues(ids, 500)) {
    const rows = await client.query<ApiKeyGroupBindingRow>(`
      SELECT
        api_key_group_bindings.id,
        api_key_group_bindings.api_key_id,
        api_key_group_bindings.system_account_id,
        api_key_group_bindings.group_id,
        api_key_group_bindings.priority,
        api_key_group_bindings.weight,
        api_key_group_bindings.status,
        groups.name AS group_name,
        groups.provider_code,
        groups.provider_protocol_profile_id,
        groups.protocol_code,
        groups.protocol_version,
        CASE
          WHEN groups.id IS NULL THEN 0
          WHEN groups.system_account_id = api_key_group_bindings.system_account_id THEN groups.enabled
          WHEN group_authorization.id IS NOT NULL THEN CASE WHEN groups.enabled = 1 THEN COALESCE(group_authorization_settings.enabled, 1) ELSE 0 END
          ELSE 0
        END AS group_enabled
      FROM ${apiKeyGroupBindingTable(client, 'api_key_group_bindings')} api_key_group_bindings
      LEFT JOIN ${apiKeyGroupBindingTable(client, 'groups')} groups ON groups.id = api_key_group_bindings.group_id
      LEFT JOIN ${apiKeyGroupBindingTable(client, 'resource_authorizations')} group_authorization
        ON group_authorization.resource_type = 'group'
        AND group_authorization.resource_id = groups.id
        AND group_authorization.grantee_system_account_id = api_key_group_bindings.system_account_id
        AND group_authorization.status = 'active'
        AND (group_authorization.expires_at IS NULL OR group_authorization.expires_at > ?)
      LEFT JOIN ${apiKeyGroupBindingTable(client, 'group_authorization_settings')} group_authorization_settings
        ON group_authorization_settings.authorization_id = group_authorization.id
        AND group_authorization_settings.system_account_id = api_key_group_bindings.system_account_id
        AND group_authorization_settings.group_id = groups.id
      WHERE api_key_group_bindings.api_key_id IN (${client.dialect.bindPlaceholders(chunk.length)})
      ORDER BY api_key_group_bindings.api_key_id ASC,
        CASE WHEN api_key_group_bindings.status = 'active' THEN 0 ELSE 1 END ASC,
        api_key_group_bindings.priority ASC,
        api_key_group_bindings.created_at ASC,
        api_key_group_bindings.id ASC
    `, [now, ...chunk])
    appendBindingRows(result, rows)
  }
  return result
}

function appendBindingRows(result: Map<string, ApiKeyGroupBindingSummary[]>, rows: ApiKeyGroupBindingRow[]): void {
  for (const row of rows) {
    if (!Number.isInteger(row.priority) || row.priority <= 0) {
      throw new Error(`API Key 分组绑定优先级无效：${row.id}`)
    }
    if (row.status !== 'active' && row.status !== 'disabled') {
      throw new Error(`API Key 分组绑定状态无效：${row.id}`)
    }
    if (Number(row.group_enabled) !== 0 && Number(row.group_enabled) !== 1) {
      throw new Error(`API Key 分组绑定关联分组状态无效：${row.id}`)
    }
    const item: ApiKeyGroupBindingSummary = {
      id: row.id,
      groupId: row.group_id,
      groupName: row.group_name ?? undefined,
      providerCode: row.provider_code ?? undefined,
      providerProtocolProfileId: row.provider_protocol_profile_id ?? undefined,
      protocolCode: row.protocol_code ?? undefined,
      protocolVersion: row.protocol_version ?? undefined,
      priority: row.priority,
      weight: normalizeApiKeyGroupBindingWeight(row.weight),
      status: row.status,
      groupEnabled: Number(row.group_enabled) === 1
    }
    const existing = result.get(row.api_key_id) ?? []
    existing.push(item)
    result.set(row.api_key_id, existing)
  }
}

async function getApiKeyGroupBindingDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function apiKeyGroupBindingTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable('juhe_business', tableName)
    : client.dialect.quoteIdentifier(tableName)
}
