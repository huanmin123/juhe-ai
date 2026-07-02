import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { cleanupDeletedAccountRelatedRecordDataAsync } from '../../storage/account-record-cleanup.js'
import { cleanupDeletedApiKeyRelatedRecordDataAsync } from '../../storage/api-key-record-cleanup.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'PG 记录清理 smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `record_cleanup_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = 'sys_admin'
const apiKeyId = `api_key_${marker}`
const accountId = `account_${marker}`
const accountApiKeyId = `api_key_for_${accountId}`
const relatedAccountId = `account_related_${marker}`
const authorizationId = `auth_${marker}`
const teamScopeId = `${accountId}:team_${marker}`
const now = new Date().toISOString()

const pool = await getPostgresPool()
const client = createPostgresDatabaseClient(pool)

try {
  await seedApiKeyRows()
  const apiKeyCleanup = await cleanupDeletedApiKeyRelatedRecordDataAsync({ apiKeyId, systemAccountId })
  assert.equal(apiKeyCleanup.hasMore, false, 'API Key PG 清理不应遗留后续批次')
  assert.ok(apiKeyCleanup.deletedRows >= 2, 'API Key PG 清理应删除 usage/audit 记录')
  assert.equal(await countRows('juhe_usage.usage_records', 'api_key_id = ?', [apiKeyId]), 0, 'API Key usage PG 记录应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_shard_entries', 'usage_id = ?', [`usage_${apiKeyId}`]), 0, 'API Key usage catalog entry 应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_api_key_shards', 'api_key_id = ? AND system_account_id = ?', [apiKeyId, systemAccountId]), 0, 'API Key usage scope catalog 应被清理')
  assert.equal(await countRows('juhe_dataset.audit_logs', 'api_key_id = ?', [apiKeyId]), 0, 'API Key audit PG 记录应被清理')
  assert.equal(await countRows('juhe_stats.usage_stats_totals', "scope_type = 'api_key' AND scope_id = ?", [apiKeyId]), 0, 'API Key stats PG 记录应被清理')
  assert.equal(await countRows('juhe_dataset.api_key_record_cleanup_targets', 'api_key_id = ?', [apiKeyId]), 0, 'API Key PG 清理目标应完成后删除')

  await seedAccountRows()
  const accountCleanup = await cleanupDeletedAccountRelatedRecordDataAsync({
    accountId,
    systemAccountId,
    relatedAccountIds: [relatedAccountId],
    authorizationIds: [authorizationId],
    teamScopeIds: [teamScopeId]
  })
  assert.equal(accountCleanup.hasMore, false, 'AI 账户 PG 清理不应遗留后续批次')
  assert.ok(accountCleanup.deletedRows >= 3, 'AI 账户 PG 清理应删除 usage/audit/model check 记录')
  assert.equal(await countRows('juhe_usage.usage_records', 'account_id = ? OR account_authorization_id = ?', [accountId, authorizationId]), 0, 'AI 账户 usage PG 记录应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_shard_entries', 'usage_id = ?', [`usage_${accountId}`]), 0, 'AI 账户 usage catalog entry 应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_account_shards', 'account_id = ?', [accountId]), 0, 'AI 账户 usage scope catalog 应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_api_key_shards', 'api_key_id = ? AND system_account_id = ?', [accountApiKeyId, systemAccountId]), 0, 'AI 账户关联 API Key usage scope catalog 应被清理')
  assert.equal(await countRows('juhe_dataset.audit_logs', 'account_id = ?', [accountId]), 0, 'AI 账户 audit PG 记录应被清理')
  assert.equal(await countRows('juhe_dataset.model_check_runs', 'target_id = ?', [accountId]), 0, 'AI 账户 model check PG 记录应被清理')
  assert.equal(await countRows('juhe_stats.usage_stats_totals', "scope_type IN ('account', 'caller_account', 'account_authorization') AND scope_id = ANY(?::text[])", [[accountId, authorizationId]]), 0, 'AI 账户 stats PG 记录应被清理')
  assert.equal(await countRows('juhe_dataset.account_record_cleanup_targets', 'account_id = ?', [accountId]), 0, 'AI 账户 PG 清理目标应完成后删除')

  console.log(JSON.stringify({
    message: 'PG 记录清理 smoke 通过',
    apiKeyDeletedRows: apiKeyCleanup.deletedRows,
    accountDeletedRows: accountCleanup.deletedRows
  }))
} finally {
  await cleanupSmokeRows().catch(() => undefined)
  await closePostgresPool()
}

async function seedApiKeyRows(): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_usage.usage_records (
      id, system_account_id, trace_id, traffic_source, api_key_id, account_id, provider_code, model,
      success, input_tokens, output_tokens, cost_usd, account_owner_system_account_id, account_access_type, created_at
    ) VALUES (?, ?, ?, 'gateway', ?, NULL, 'gpt', 'gpt-5-mini', 1, 1, 1, 0.001, ?, 'owner', ?)
  `, [`usage_${apiKeyId}`, systemAccountId, `trace_${apiKeyId}`, apiKeyId, systemAccountId, now])
  await seedUsageCatalogRows({
    usageId: `usage_${apiKeyId}`,
    shardKey: `shard_${apiKeyId}`,
    traceId: `trace_${apiKeyId}`,
    apiKeyId
  })
  await client.execute(`
    INSERT INTO juhe_dataset.audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, method, path,
      audit_outcome, success, sample_bucket, sample_reason, started_at, ended_at, created_at
    ) VALUES (?, ?, 'gateway', ?, ?, 'POST', '/v1/chat/completions', 'success', 1, 0, 'smoke', ?, ?, ?)
  `, [`audit_${apiKeyId}`, `trace_audit_${apiKeyId}`, systemAccountId, apiKeyId, now, now, now])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, request_count, updated_at)
    VALUES (?, 'api_key', ?, 1, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET request_count = EXCLUDED.request_count, updated_at = EXCLUDED.updated_at
  `, [systemAccountId, apiKeyId, now])
}

async function seedAccountRows(): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_usage.usage_records (
      id, system_account_id, trace_id, traffic_source, api_key_id, account_id, provider_code, model,
      success, input_tokens, output_tokens, cost_usd, account_owner_system_account_id, account_access_type,
      account_authorization_id, account_authorization_source_team_id, created_at
    ) VALUES (?, ?, ?, 'gateway', ?, ?, 'gpt', 'gpt-5-mini', 1, 1, 1, 0.001, ?, 'owner', ?, ?, ?)
  `, [`usage_${accountId}`, systemAccountId, `trace_${accountId}`, accountApiKeyId, accountId, systemAccountId, authorizationId, `team_${marker}`, now])
  await seedUsageCatalogRows({
    usageId: `usage_${accountId}`,
    shardKey: `shard_${accountId}`,
    traceId: `trace_${accountId}`,
    apiKeyId: accountApiKeyId,
    accountId
  })
  await client.execute(`
    INSERT INTO juhe_dataset.audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, account_id, method, path,
      audit_outcome, success, sample_bucket, sample_reason, started_at, ended_at, created_at
    ) VALUES (?, ?, 'gateway', ?, ?, ?, 'POST', '/v1/chat/completions', 'success', 1, 0, 'smoke', ?, ?, ?)
  `, [`audit_${accountId}`, `trace_audit_${accountId}`, systemAccountId, apiKeyId, accountId, now, now, now])
  await client.execute(`
    INSERT INTO juhe_dataset.model_check_runs (
      id, system_account_id, actor_system_account_id, provider_code, target_type, target_id, account_id,
      model, profile, status, started_at, created_at, updated_at
    ) VALUES (?, ?, ?, 'gpt', 'account', ?, ?, 'gpt-5-mini', 'full', 'completed', ?, ?, ?)
  `, [`model_check_${accountId}`, systemAccountId, systemAccountId, accountId, accountId, now, now, now])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_totals (system_account_id, scope_type, scope_id, request_count, updated_at)
    VALUES
      (?, 'account', ?, 1, ?),
      (?, 'caller_account', ?, 1, ?),
      (?, 'account_authorization', ?, 1, ?),
      (?, 'account_authorization_team', ?, 1, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET request_count = EXCLUDED.request_count, updated_at = EXCLUDED.updated_at
  `, [
    systemAccountId, accountId, now,
    systemAccountId, accountId, now,
    systemAccountId, authorizationId, now,
    systemAccountId, teamScopeId, now
  ])
}

async function seedUsageCatalogRows(input: {
  usageId: string
  shardKey: string
  traceId: string
  apiKeyId?: string
  accountId?: string
}): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_usage.usage_record_shards (
      shard_key, bucket_date, shard_id, file_path, schema_version, status,
      first_seen_at, last_write_at, created_at, updated_at
    ) VALUES (?, ?, 1, ?, 1, 'active', ?, ?, ?, ?)
    ON CONFLICT(shard_key) DO UPDATE SET
      last_write_at = EXCLUDED.last_write_at,
      updated_at = EXCLUDED.updated_at
  `, [input.shardKey, now.slice(0, 10), `/tmp/${input.shardKey}.sqlite`, now, now, now, now])
  await client.execute(`
    INSERT INTO juhe_usage.usage_record_shard_entries (
      usage_id, shard_key, system_account_id, trace_id, api_key_id, account_id, group_id, model,
      traffic_source, success, status_code, client_ip, first_token_ms, duration_ms, cost_usd, created_at, indexed_at
    ) VALUES (?, ?, ?, ?, ?, ?, NULL, 'gpt-5-mini', 'gateway', 1, 200, '127.0.0.1', 10, 20, 0.001, ?, ?)
    ON CONFLICT(usage_id) DO UPDATE SET
      shard_key = EXCLUDED.shard_key,
      system_account_id = EXCLUDED.system_account_id,
      trace_id = EXCLUDED.trace_id,
      api_key_id = EXCLUDED.api_key_id,
      account_id = EXCLUDED.account_id,
      indexed_at = EXCLUDED.indexed_at
  `, [input.usageId, input.shardKey, systemAccountId, input.traceId, input.apiKeyId ?? null, input.accountId ?? null, now, now])
  if (input.accountId) {
    await client.execute(`
      INSERT INTO juhe_usage.usage_record_account_shards (account_id, shard_key, first_created_at, last_seen_at)
      VALUES (?, ?, ?, ?)
      ON CONFLICT(account_id, shard_key) DO UPDATE SET
        first_created_at = LEAST(usage_record_account_shards.first_created_at, EXCLUDED.first_created_at),
        last_seen_at = GREATEST(usage_record_account_shards.last_seen_at, EXCLUDED.last_seen_at)
    `, [input.accountId, input.shardKey, now, now])
  }
  if (input.apiKeyId) {
    await client.execute(`
      INSERT INTO juhe_usage.usage_record_api_key_shards (api_key_id, system_account_id, shard_key, first_created_at, last_seen_at)
      VALUES (?, ?, ?, ?, ?)
      ON CONFLICT(api_key_id, system_account_id, shard_key) DO UPDATE SET
        first_created_at = LEAST(usage_record_api_key_shards.first_created_at, EXCLUDED.first_created_at),
        last_seen_at = GREATEST(usage_record_api_key_shards.last_seen_at, EXCLUDED.last_seen_at)
    `, [input.apiKeyId, systemAccountId, input.shardKey, now, now])
  }
}

async function countRows(tableName: string, whereClause: string, params: unknown[]): Promise<number> {
  const row = await client.one<{ count?: string | number }>(`SELECT COUNT(*) AS count FROM ${tableName} WHERE ${whereClause}`, params)
  return Number(row?.count ?? 0)
}

async function cleanupSmokeRows(): Promise<void> {
  await client.execute('DELETE FROM juhe_dataset.account_record_cleanup_targets WHERE account_id = ?', [accountId])
  await client.execute('DELETE FROM juhe_dataset.api_key_record_cleanup_targets WHERE api_key_id = ?', [apiKeyId])
  await client.execute('DELETE FROM juhe_dataset.model_check_items WHERE run_id = ?', [`model_check_${accountId}`])
  await client.execute('DELETE FROM juhe_dataset.model_check_runs WHERE id = ?', [`model_check_${accountId}`])
  await client.execute('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY(?::text[])', [[`audit_${apiKeyId}`, `audit_${accountId}`]])
  await client.execute('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = ANY(?::text[])', [[`audit_${apiKeyId}`, `audit_${accountId}`]])
  await client.execute('DELETE FROM juhe_dataset.audit_logs WHERE id = ANY(?::text[])', [[`audit_${apiKeyId}`, `audit_${accountId}`]])
  await client.execute('DELETE FROM juhe_usage.usage_record_account_shards WHERE account_id = ANY(?::text[])', [[accountId]])
  await client.execute('DELETE FROM juhe_usage.usage_record_api_key_shards WHERE api_key_id = ANY(?::text[])', [[apiKeyId, accountApiKeyId]])
  await client.execute('DELETE FROM juhe_usage.usage_record_shard_entries WHERE usage_id = ANY(?::text[])', [[`usage_${apiKeyId}`, `usage_${accountId}`]])
  await client.execute('DELETE FROM juhe_usage.usage_records WHERE id = ANY(?::text[])', [[`usage_${apiKeyId}`, `usage_${accountId}`]])
  await client.execute('DELETE FROM juhe_usage.usage_record_shards WHERE shard_key = ANY(?::text[])', [[`shard_${apiKeyId}`, `shard_${accountId}`]])
  await client.execute('DELETE FROM juhe_stats.usage_stats_totals WHERE scope_id = ANY(?::text[])', [[apiKeyId, accountId, authorizationId, teamScopeId]])
  await client.execute('DELETE FROM juhe_stats.usage_record_cleanup_deductions WHERE usage_id = ANY(?::text[])', [[`usage_${apiKeyId}`, `usage_${accountId}`]])
}
