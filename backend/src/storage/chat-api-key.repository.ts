import { runtimeConfig } from '../config/runtime.js'
import { decryptJson } from './crypto.js'
import { getBusinessDatabase } from './database.js'
import { createPostgresDatabaseClient, createSqliteDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'

const businessSchemaName = 'juhe_business'

interface ChatApiKeyRow {
  id: string
  name: string
  status: 'active' | 'disabled'
  key_secret_encrypted: string
  expires_at?: string | null
}

export interface ChatApiKeySecret {
  id: string
  name: string
  status: 'active' | 'disabled'
  key: string
  expiresAt?: string
}

export async function findChatApiKeySecretAsync(apiKeyId: string, systemAccountId: string): Promise<ChatApiKeySecret | undefined> {
  const client = await getChatApiKeyDatabaseClient()
  const now = new Date().toISOString()
  const row = await client.one<ChatApiKeyRow>(`
    SELECT id, name, status, key_secret_encrypted, expires_at
    FROM ${chatApiKeyTable(client, 'api_keys')}
    WHERE id = ?
      AND system_account_id = ?
      AND status = 'active'
      AND (expires_at IS NULL OR expires_at > ?)
    LIMIT 1
  `, [apiKeyId, systemAccountId, now])
  return row ? chatApiKeySecretFromRow(row) : undefined
}

export async function findDefaultChatApiKeySecretForProviderAsync(
  providerCode: string,
  systemAccountId: string
): Promise<ChatApiKeySecret | undefined> {
  const client = await getChatApiKeyDatabaseClient()
  const now = new Date().toISOString()
  const row = await client.one<ChatApiKeyRow>(`
    SELECT api_keys.id, api_keys.name, api_keys.status, api_keys.key_secret_encrypted, api_keys.expires_at
    FROM ${chatApiKeyTable(client, 'api_keys')} api_keys
    INNER JOIN ${chatApiKeyTable(client, 'route_strategies')} route_strategies
      ON route_strategies.id = api_keys.route_strategy_id
      AND route_strategies.system_account_id = api_keys.system_account_id
      AND route_strategies.status = 'active'
    INNER JOIN ${chatApiKeyTable(client, 'route_strategy_groups')} route_strategy_groups
      ON route_strategy_groups.route_strategy_id = route_strategies.id
      AND route_strategy_groups.system_account_id = api_keys.system_account_id
      AND route_strategy_groups.status = 'active'
    INNER JOIN ${chatApiKeyTable(client, 'groups')} groups
      ON groups.id = route_strategy_groups.group_id
      AND groups.system_account_id = api_keys.system_account_id
      AND groups.enabled = 1
      AND groups.provider_code = ?
    WHERE api_keys.system_account_id = ?
      AND api_keys.is_default = 1
      AND api_keys.status = 'active'
      AND (api_keys.expires_at IS NULL OR api_keys.expires_at > ?)
    ORDER BY api_keys.created_at ASC, api_keys.id ASC
    LIMIT 1
  `, [providerCode, systemAccountId, now])
  return row ? chatApiKeySecretFromRow(row) : undefined
}

function chatApiKeySecretFromRow(row: ChatApiKeyRow): ChatApiKeySecret {
  const secret = decryptJson<{ key?: unknown }>(row.key_secret_encrypted)
  if (typeof secret.key !== 'string' || secret.key.length === 0) {
    throw new Error(`API Key ${row.id} 的密钥数据无效`)
  }
  return {
    id: row.id,
    name: row.name,
    status: row.status,
    key: secret.key,
    expiresAt: row.expires_at ?? undefined
  }
}

async function getChatApiKeyDatabaseClient(): Promise<DatabaseClient> {
  if (runtimeConfig.databaseDriver === 'postgres') {
    return createPostgresDatabaseClient(await getPostgresPool())
  }
  return createSqliteDatabaseClient(getBusinessDatabase())
}

function chatApiKeyTable(client: DatabaseClient, tableName: string): string {
  return client.driver === 'postgres'
    ? client.dialect.qualifyTable(businessSchemaName, tableName)
    : client.dialect.quoteIdentifier(tableName)
}
