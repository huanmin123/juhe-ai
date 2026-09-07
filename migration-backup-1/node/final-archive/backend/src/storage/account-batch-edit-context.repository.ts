import { runtimeConfig } from '../config/runtime.js'
import type {
  AccountBatchEditContextField,
  AccountBatchEditContextItem,
  AccountModelMapping,
  AccountSupportedEndpointMode,
  AccountType,
  ProviderCode
} from '../domain/types.js'
import { canAccessAll, manageableSystemAccountId, type AccessScope } from './access-scope.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

const businessSchemaName = 'juhe_business'
const contextFieldNames = new Set<AccountBatchEditContextField>([
  'supportedModels',
  'modelMappings',
  'supportedEndpointModes'
])

export interface AccountBatchEditContextRecord extends AccountBatchEditContextItem {
  ownerSystemAccountId: string
}

export async function loadAccountBatchEditContextRecordsAsync(
  accountIds: string[],
  requestedFields: readonly AccountBatchEditContextField[],
  access?: AccessScope
): Promise<AccountBatchEditContextRecord[]> {
  const client = runtimeConfig.databaseDriver === 'postgres'
    ? createPostgresDatabaseClient(await getPostgresPool())
    : createSqliteDatabaseClient(getBusinessDatabase())
  return loadAccountBatchEditContextRecordsWithClientAsync(client, accountIds, requestedFields, access)
}

export async function loadAccountBatchEditContextRecordsWithClientAsync(
  client: DatabaseClient,
  accountIds: string[],
  requestedFields: readonly AccountBatchEditContextField[],
  access?: AccessScope
): Promise<AccountBatchEditContextRecord[]> {
  const ids = normalizedAccountIds(accountIds)
  const fields = normalizedContextFields(requestedFields)
  const scopedOwnerId = manageableSystemAccountId(access)
  if (!scopedOwnerId && !canAccessAll(access)) return []

  const columns = [
    'id',
    'config_revision',
    'system_account_id',
    'provider_code',
    'provider_protocol_profile_id',
    'protocol_code',
    'protocol_version',
    'type'
  ]
  if (fields.has('supportedEndpointModes')) columns.push('credentials_encrypted')
  const placeholders = ids.map(() => '?').join(', ')
  const ownerScopeClause = scopedOwnerId
    ? ` AND ${client.dialect.quoteIdentifier('system_account_id')} = ?`
    : ''
  const rows = await client.query<AccountBatchEditContextRow>(`
    SELECT ${columns.map((column) => client.dialect.quoteIdentifier(column)).join(', ')}
    FROM ${contextTable(client, 'accounts')}
    WHERE id IN (${placeholders})
      AND deleted_at IS NULL
      AND authorization_instance_authorization_id IS NULL
      AND authorization_instance_source_account_id IS NULL${ownerScopeClause}
    ORDER BY id ASC
  `, scopedOwnerId ? [...ids, scopedOwnerId] : ids)

  const rowIds = rows.map((row) => row.id)
  const [supportedModels, modelMappings] = await Promise.all([
    fields.has('supportedModels')
      ? loadSupportedModelsAsync(client, rowIds)
      : Promise.resolve(new Map<string, string[]>()),
    fields.has('modelMappings')
      ? loadModelMappingsAsync(client, rowIds)
      : Promise.resolve(new Map<string, AccountModelMapping[]>())
  ])
  const rowsById = new Map(rows.map((row) => [row.id, row]))
  return ids.flatMap((accountId) => {
    const row = rowsById.get(accountId)
    if (!row) return []
    const item: AccountBatchEditContextRecord = {
      id: row.id,
      configRevision: Number(row.config_revision),
      ownerSystemAccountId: row.system_account_id,
      providerCode: row.provider_code,
      providerProtocolProfileId: row.provider_protocol_profile_id,
      protocolCode: row.protocol_code,
      protocolVersion: row.protocol_version,
      type: row.type
    }
    if (fields.has('supportedModels')) item.supportedModels = supportedModels.get(row.id) ?? []
    if (fields.has('modelMappings')) item.modelMappings = modelMappings.get(row.id) ?? []
    if (fields.has('supportedEndpointModes')) {
      const credentials = row.credentials_encrypted
        ? decryptJson<Record<string, unknown>>(row.credentials_encrypted)
        : {}
      item.supportedEndpointModes = storedEndpointModes(credentials.supported_endpoint_modes)
    }
    return [item]
  })
}

async function loadSupportedModelsAsync(
  client: DatabaseClient,
  accountIds: string[]
): Promise<Map<string, string[]>> {
  const output = new Map<string, string[]>()
  if (!accountIds.length) return output
  const rows = await client.query<{ account_id: string; model: string }>(`
    SELECT account_id, model
    FROM ${contextTable(client, 'account_supported_models')}
    WHERE account_id IN (${accountIds.map(() => '?').join(', ')})
    ORDER BY account_id ASC, model ASC
  `, accountIds)
  for (const row of rows) appendMapValue(output, row.account_id, row.model)
  return output
}

async function loadModelMappingsAsync(
  client: DatabaseClient,
  accountIds: string[]
): Promise<Map<string, AccountModelMapping[]>> {
  const output = new Map<string, AccountModelMapping[]>()
  if (!accountIds.length) return output
  const rows = await client.query<AccountBatchEditContextModelMappingRow>(`
    SELECT account_id, source_model, source_endpoint_family, upstream_model, upstream_endpoint_family, enabled
    FROM ${contextTable(client, 'account_model_mappings')}
    WHERE account_id IN (${accountIds.map(() => '?').join(', ')})
    ORDER BY account_id ASC, source_model ASC, source_endpoint_family ASC
  `, accountIds)
  for (const row of rows) {
    appendMapValue(output, row.account_id, {
      sourceModel: row.source_model,
      sourceEndpointFamily: row.source_endpoint_family,
      upstreamModel: row.upstream_model,
      upstreamEndpointFamily: row.upstream_endpoint_family,
      enabled: databaseBoolean(row.enabled)
    })
  }
  return output
}

function normalizedAccountIds(accountIds: string[]): string[] {
  const ids = accountIds.map((accountId) => accountId.trim())
  if (ids.length < 2 || ids.length > 100 || ids.some((accountId) => !accountId) || new Set(ids).size !== ids.length) {
    throw new Error('批量编辑账户数量必须在 2-100 个之间且不能重复')
  }
  return ids
}

function normalizedContextFields(
  requestedFields: readonly AccountBatchEditContextField[]
): Set<AccountBatchEditContextField> {
  const fields = new Set<AccountBatchEditContextField>()
  for (const field of requestedFields) {
    if (!contextFieldNames.has(field)) throw new Error(`不支持的批量编辑上下文字段：${field}`)
    fields.add(field)
  }
  return fields
}

function storedEndpointModes(value: unknown): AccountSupportedEndpointMode[] {
  if (!Array.isArray(value)) return []
  return [...new Set(value.filter((item): item is AccountSupportedEndpointMode => typeof item === 'string'))]
}

function appendMapValue<T>(target: Map<string, T[]>, key: string, value: T): void {
  const values = target.get(key)
  if (values) values.push(value)
  else target.set(key, [value])
}

function contextTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}

function databaseBoolean(value: unknown): boolean {
  return value === true || value === 1 || value === '1'
}

interface AccountBatchEditContextRow {
  id: string
  config_revision: number
  system_account_id: string
  provider_code: ProviderCode
  provider_protocol_profile_id: string
  protocol_code: string
  protocol_version: string
  type: AccountType
  credentials_encrypted?: string
}

interface AccountBatchEditContextModelMappingRow {
  account_id: string
  source_model: string
  source_endpoint_family: AccountModelMapping['sourceEndpointFamily']
  upstream_model: string
  upstream_endpoint_family: AccountModelMapping['upstreamEndpointFamily']
  enabled: number | boolean | string
}
