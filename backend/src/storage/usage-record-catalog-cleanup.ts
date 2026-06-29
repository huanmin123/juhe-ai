import type { DatabaseClient } from './database-client.js'

interface UsageRecordShardEntryScopeRow {
  usage_id?: string | null
  shard_key?: string | null
  system_account_id?: string | null
  api_key_id?: string | null
  account_id?: string | null
}

interface UsageRecordShardEntryScope {
  usageId: string
  shardKey: string
  systemAccountId: string
  apiKeyId?: string
  accountId?: string
}

export async function deletePostgresUsageRecordCatalogRowsByUsageIds(client: DatabaseClient, ids: string[]): Promise<void> {
  const usageIds = uniqueNonEmpty(ids)
  if (!usageIds.length) return
  const scopes = await listPostgresUsageRecordShardEntryScopes(client, usageIds)
  await client.execute('DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY(?)', [usageIds])
  await cleanupPostgresUsageRecordScopeShardCatalog(client, scopes)
}

function uniqueNonEmpty(values: string[]): string[] {
  return [...new Set(values.map((value) => value.trim()).filter(Boolean))]
}

async function listPostgresUsageRecordShardEntryScopes(client: DatabaseClient, ids: string[]): Promise<UsageRecordShardEntryScope[]> {
  const rows = await client.query<UsageRecordShardEntryScopeRow>(`
    SELECT usage_id, shard_key, system_account_id, api_key_id, account_id
    FROM juhe_usage.usage_record_shard_entries
    WHERE usage_id = ANY(?)
  `, [ids])
  return rows
    .map((row) => ({
      usageId: String(row.usage_id ?? ''),
      shardKey: String(row.shard_key ?? ''),
      systemAccountId: String(row.system_account_id ?? ''),
      apiKeyId: typeof row.api_key_id === 'string' ? row.api_key_id : undefined,
      accountId: typeof row.account_id === 'string' ? row.account_id : undefined
    }))
    .filter((row) => row.usageId && row.shardKey && row.systemAccountId)
}

async function cleanupPostgresUsageRecordScopeShardCatalog(client: DatabaseClient, scopes: UsageRecordShardEntryScope[]): Promise<void> {
  const accountScopes = new Set<string>()
  const apiKeyScopes = new Set<string>()
  for (const scope of scopes) {
    const accountId = scope.accountId?.trim()
    if (accountId) {
      accountScopes.add(`${accountId}\u0000${scope.shardKey}`)
    }
    const apiKeyId = scope.apiKeyId?.trim()
    if (apiKeyId) {
      apiKeyScopes.add(`${apiKeyId}\u0000${scope.systemAccountId}\u0000${scope.shardKey}`)
    }
  }

  for (const key of accountScopes) {
    const [accountId, shardKey] = key.split('\u0000')
    if (!accountId || !shardKey) continue
    await client.execute(`
      DELETE FROM juhe_usage.usage_record_account_shards scope
      WHERE scope.account_id = ?
        AND scope.shard_key = ?
        AND NOT EXISTS (
          SELECT 1
          FROM juhe_usage.usage_record_shard_entries entry
          WHERE entry.account_id = scope.account_id
            AND entry.shard_key = scope.shard_key
          LIMIT 1
        )
    `, [accountId, shardKey])
  }

  for (const key of apiKeyScopes) {
    const [apiKeyId, systemAccountId, shardKey] = key.split('\u0000')
    if (!apiKeyId || !systemAccountId || !shardKey) continue
    await client.execute(`
      DELETE FROM juhe_usage.usage_record_api_key_shards scope
      WHERE scope.api_key_id = ?
        AND scope.system_account_id = ?
        AND scope.shard_key = ?
        AND NOT EXISTS (
          SELECT 1
          FROM juhe_usage.usage_record_shard_entries entry
          WHERE entry.api_key_id = scope.api_key_id
            AND entry.system_account_id = scope.system_account_id
            AND entry.shard_key = scope.shard_key
          LIMIT 1
        )
    `, [apiKeyId, systemAccountId, shardKey])
  }
}
