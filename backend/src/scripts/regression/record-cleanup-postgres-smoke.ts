import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { cleanupDeletedAccountRelatedRecordDataAsync } from '../../storage/account-record-cleanup.js'
import { cleanupDeletedApiKeyRelatedRecordDataAsync } from '../../storage/api-key-record-cleanup.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { ensurePostgresUsageRecordPartitions } from '../../storage/postgres-usage-record-partitions.js'
import { GLOBAL_STATS_SCOPE_ID, GLOBAL_STATS_SYSTEM_ACCOUNT_ID } from '../../storage/usage-stats-types.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'PG 记录清理 smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `record_cleanup_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const providerCode = `provider_${marker}`
const modelName = `model_${marker}`
const apiKeyId = `api_key_${marker}`
const accountId = `account_${marker}`
const accountApiKeyId = `api_key_for_${accountId}`
const relatedAccountId = `account_related_${marker}`
const authorizationId = `auth_${marker}`
const teamScopeId = `${accountId}:team_${marker}`
const cleanupCursorJobNames = ['usage_stats_aggregation', 'client_ip_stats_aggregation'] as const
let now = new Date().toISOString()
let originalCleanupCursorRows: StatsJobStateRow[] | undefined
const modifiedCleanupCursorJobNames = new Set<string>()
const pendingGlobalStatsSmokeUsageIds = new Set<string>()

const pool = await getPostgresPool()
const client = createPostgresDatabaseClient(pool)

try {
  now = await cleanupEligibleUsageCreatedAt()
  await ensurePostgresUsageRecordPartitions(client, [now])
  await seedApiKeyRows()
  const apiKeyCleanup = await cleanupDeletedApiKeyRelatedRecordDataAsync({ apiKeyId, systemAccountId })
  pendingGlobalStatsSmokeUsageIds.delete(`usage_${apiKeyId}`)
  assert.equal(apiKeyCleanup.hasMore, false, 'API Key PG 清理不应遗留后续批次')
  assert.ok(apiKeyCleanup.deletedRows >= 2, 'API Key PG 清理应删除 usage/audit 记录')
  assert.equal(await countRows('juhe_usage.usage_records', 'api_key_id = ?', [apiKeyId]), 0, 'API Key usage PG 记录应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_shard_entries', 'usage_id = ?', [`usage_${apiKeyId}`]), 0, 'API Key usage catalog entry 应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_api_key_shards', 'api_key_id = ? AND system_account_id = ?', [apiKeyId, systemAccountId]), 0, 'API Key usage scope catalog 应被清理')
  assert.equal(await countRows('juhe_dataset.audit_logs', 'api_key_id = ?', [apiKeyId]), 0, 'API Key audit PG 记录应被清理')
  assert.equal(await countRows('juhe_stats.usage_stats_totals', "scope_type = 'api_key' AND scope_id = ?", [apiKeyId]), 0, 'API Key stats PG 记录应被清理')
  assert.equal(await countRows('juhe_stats.usage_stats_totals', "scope_id = ANY(?::text[])", [[systemAccountId, providerCode, modelName]]), 0, 'API Key cleanup 应扣减并清理 marker 统计 scope')
  assert.equal(await countRows('juhe_dataset.api_key_record_cleanup_targets', 'api_key_id = ?', [apiKeyId]), 0, 'API Key PG 清理目标应完成后删除')

  await seedAccountRows()
  const accountCleanup = await cleanupDeletedAccountRelatedRecordDataAsync({
    accountId,
    systemAccountId,
    relatedAccountIds: [relatedAccountId],
    authorizationIds: [authorizationId],
    teamScopeIds: [teamScopeId]
  })
  pendingGlobalStatsSmokeUsageIds.delete(`usage_${accountId}`)
  assert.equal(accountCleanup.hasMore, false, 'AI 账户 PG 清理不应遗留后续批次')
  assert.ok(accountCleanup.deletedRows >= 3, 'AI 账户 PG 清理应删除 usage/audit/model check 记录')
  assert.equal(await countRows('juhe_usage.usage_records', 'account_id = ? OR account_authorization_id = ?', [accountId, authorizationId]), 0, 'AI 账户 usage PG 记录应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_shard_entries', 'usage_id = ?', [`usage_${accountId}`]), 0, 'AI 账户 usage catalog entry 应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_account_shards', 'account_id = ?', [accountId]), 0, 'AI 账户 usage scope catalog 应被清理')
  assert.equal(await countRows('juhe_usage.usage_record_api_key_shards', 'api_key_id = ? AND system_account_id = ?', [accountApiKeyId, systemAccountId]), 0, 'AI 账户关联 API Key usage scope catalog 应被清理')
  assert.equal(await countRows('juhe_dataset.audit_logs', 'account_id = ?', [accountId]), 0, 'AI 账户 audit PG 记录应被清理')
  assert.equal(await countRows('juhe_dataset.model_check_runs', 'target_id = ?', [accountId]), 0, 'AI 账户 model check PG 记录应被清理')
  assert.equal(await countRows('juhe_stats.usage_stats_totals', "scope_type IN ('account', 'caller_account', 'account_authorization') AND scope_id = ANY(?::text[])", [[accountId, authorizationId]]), 0, 'AI 账户 stats PG 记录应被清理')
  assert.equal(await countRows('juhe_stats.usage_stats_totals', "scope_id = ANY(?::text[])", [[systemAccountId, providerCode, modelName]]), 0, 'AI 账户 cleanup 应扣减并清理 marker 统计 scope')
  assert.equal(await countRows('juhe_dataset.account_record_cleanup_targets', 'account_id = ?', [accountId]), 0, 'AI 账户 PG 清理目标应完成后删除')

  console.log(JSON.stringify({
    message: 'PG 记录清理 smoke 通过',
    usageCreatedAt: now,
    apiKeyDeletedRows: apiKeyCleanup.deletedRows,
    accountDeletedRows: accountCleanup.deletedRows
  }))
} finally {
  await revertPendingGlobalStatsSmokeCompensations().catch(() => undefined)
  await cleanupSmokeRows().catch(() => undefined)
  await restoreCleanupCursorRows().catch(() => undefined)
  await closePostgresPool()
}

interface StatsJobStateRow {
  scope_type: string
  scope_id: string
  job_name: string
  cursor_created_at?: string | null
  cursor_id?: string | null
  last_success_at?: string | null
  last_error_message?: string | null
  lag_seconds?: string | number | null
  updated_at: string
}

async function seedApiKeyRows(): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_usage.usage_records (
      id, system_account_id, trace_id, traffic_source, api_key_id, account_id, provider_code, model,
      success, input_tokens, output_tokens, cost_usd, account_owner_system_account_id, account_access_type, created_at
    ) VALUES (?, ?, ?, 'gateway', ?, NULL, ?, ?, 1, 1, 1, 0.001, ?, 'owner', ?)
  `, [`usage_${apiKeyId}`, systemAccountId, `trace_${apiKeyId}`, apiKeyId, providerCode, modelName, systemAccountId, now])
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
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count,
      input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at
    )
    VALUES
      (?, 'api_key', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'system_account', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'provider', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'model', ?, 1, 1, 1, 1, 0.001, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = EXCLUDED.request_count,
      success_count = EXCLUDED.success_count,
      input_tokens = EXCLUDED.input_tokens,
      output_tokens = EXCLUDED.output_tokens,
      total_cost_usd = EXCLUDED.total_cost_usd,
      last_used_at = EXCLUDED.last_used_at,
      updated_at = EXCLUDED.updated_at
  `, [
    systemAccountId, apiKeyId, now, now,
    systemAccountId, systemAccountId, now, now,
    systemAccountId, providerCode, now, now,
    systemAccountId, modelName, now, now
  ])
  await addGlobalStatsSmokeCompensation(`usage_${apiKeyId}`)
}

async function seedAccountRows(): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_usage.usage_records (
      id, system_account_id, trace_id, traffic_source, api_key_id, account_id, provider_code, model,
      success, input_tokens, output_tokens, cost_usd, account_owner_system_account_id, account_access_type,
      account_authorization_id, account_authorization_source_team_id, created_at
    ) VALUES (?, ?, ?, 'gateway', ?, ?, ?, ?, 1, 1, 1, 0.001, ?, 'owner', ?, ?, ?)
  `, [`usage_${accountId}`, systemAccountId, `trace_${accountId}`, accountApiKeyId, accountId, providerCode, modelName, systemAccountId, authorizationId, `team_${marker}`, now])
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
    ) VALUES (?, ?, ?, ?, 'account', ?, ?, ?, 'full', 'completed', ?, ?, ?)
  `, [`model_check_${accountId}`, systemAccountId, systemAccountId, providerCode, accountId, accountId, modelName, now, now, now])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count,
      input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at
    )
    VALUES
      (?, 'account', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'caller_account', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'account_authorization', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'account_authorization_team', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'system_account', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'provider', ?, 1, 1, 1, 1, 0.001, ?, ?),
      (?, 'model', ?, 1, 1, 1, 1, 0.001, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = EXCLUDED.request_count,
      success_count = EXCLUDED.success_count,
      input_tokens = EXCLUDED.input_tokens,
      output_tokens = EXCLUDED.output_tokens,
      total_cost_usd = EXCLUDED.total_cost_usd,
      last_used_at = EXCLUDED.last_used_at,
      updated_at = EXCLUDED.updated_at
  `, [
    systemAccountId, accountId, now, now,
    systemAccountId, accountId, now, now,
    systemAccountId, authorizationId, now, now,
    systemAccountId, teamScopeId, now, now,
    systemAccountId, systemAccountId, now, now,
    systemAccountId, providerCode, now, now,
    systemAccountId, modelName, now, now
  ])
  await addGlobalStatsSmokeCompensation(`usage_${accountId}`)
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
  `, [input.shardKey, now.slice(0, 10), `postgres:${input.shardKey}`, now, now, now, now])
  await client.execute(`
    INSERT INTO juhe_usage.usage_record_shard_entries (
      usage_id, shard_key, system_account_id, trace_id, api_key_id, account_id, group_id, model,
      traffic_source, success, status_code, client_ip, first_token_ms, duration_ms, cost_usd, created_at, indexed_at
    ) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, 'gateway', 1, 200, '127.0.0.1', 10, 20, 0.001, ?, ?)
    ON CONFLICT(usage_id) DO UPDATE SET
      shard_key = EXCLUDED.shard_key,
      system_account_id = EXCLUDED.system_account_id,
      trace_id = EXCLUDED.trace_id,
      api_key_id = EXCLUDED.api_key_id,
      account_id = EXCLUDED.account_id,
      indexed_at = EXCLUDED.indexed_at
  `, [input.usageId, input.shardKey, systemAccountId, input.traceId, input.apiKeyId ?? null, input.accountId ?? null, modelName, now, now])
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

async function addGlobalStatsSmokeCompensation(usageId: string): Promise<void> {
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count,
      input_tokens, output_tokens, total_cost_usd, last_used_at, updated_at
    ) VALUES (?, 'system_account', ?, 1, 1, 1, 1, 0.001, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      request_count = usage_stats_totals.request_count + EXCLUDED.request_count,
      success_count = usage_stats_totals.success_count + EXCLUDED.success_count,
      input_tokens = usage_stats_totals.input_tokens + EXCLUDED.input_tokens,
      output_tokens = usage_stats_totals.output_tokens + EXCLUDED.output_tokens,
      total_cost_usd = usage_stats_totals.total_cost_usd + EXCLUDED.total_cost_usd,
      last_used_at = CASE
        WHEN usage_stats_totals.last_used_at IS NULL OR EXCLUDED.last_used_at > usage_stats_totals.last_used_at THEN EXCLUDED.last_used_at
        ELSE usage_stats_totals.last_used_at
      END,
      updated_at = EXCLUDED.updated_at
  `, [GLOBAL_STATS_SYSTEM_ACCOUNT_ID, GLOBAL_STATS_SCOPE_ID, now, now])
  pendingGlobalStatsSmokeUsageIds.add(usageId)
}

async function revertPendingGlobalStatsSmokeCompensations(): Promise<void> {
  const usageIds = [...pendingGlobalStatsSmokeUsageIds]
  if (usageIds.length === 0) {
    return
  }

  const subtractedRows = await client.query<{ usage_id?: string | null }>(`
    SELECT usage_id
    FROM juhe_stats.usage_record_cleanup_deductions
    WHERE usage_id = ANY(?::text[])
      AND stats_subtracted_at IS NOT NULL
  `, [usageIds])
  for (const row of subtractedRows) {
    if (row.usage_id) {
      pendingGlobalStatsSmokeUsageIds.delete(row.usage_id)
    }
  }

  const pendingCount = pendingGlobalStatsSmokeUsageIds.size
  if (pendingCount === 0) {
    return
  }
  await client.execute(`
    UPDATE juhe_stats.usage_stats_totals
    SET request_count = GREATEST(0, request_count - ?),
        success_count = GREATEST(0, success_count - ?),
        input_tokens = GREATEST(0, input_tokens - ?),
        output_tokens = GREATEST(0, output_tokens - ?),
        total_cost_usd = GREATEST(0, total_cost_usd - ?),
        last_used_at = CASE WHEN request_count <= ? THEN NULL ELSE last_used_at END,
        updated_at = ?
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
  `, [
    pendingCount,
    pendingCount,
    pendingCount,
    pendingCount,
    pendingCount * 0.001,
    pendingCount,
    now,
    GLOBAL_STATS_SYSTEM_ACCOUNT_ID,
    GLOBAL_STATS_SCOPE_ID
  ])
  await client.execute(`
    DELETE FROM juhe_stats.usage_stats_totals
    WHERE system_account_id = ?
      AND scope_type = 'system_account'
      AND scope_id = ?
      AND request_count = 0
      AND success_count = 0
      AND error_count = 0
      AND input_tokens = 0
      AND output_tokens = 0
      AND cache_read_tokens = 0
      AND cache_read_cost_usd = 0
      AND cache_write_tokens = 0
      AND cache_write_1h_tokens = 0
      AND cache_write_cost_usd = 0
      AND thinking_tokens = 0
      AND input_image_tokens = 0
      AND output_image_tokens = 0
      AND total_cost_usd = 0
  `, [GLOBAL_STATS_SYSTEM_ACCOUNT_ID, GLOBAL_STATS_SCOPE_ID])
  pendingGlobalStatsSmokeUsageIds.clear()
}

async function cleanupEligibleUsageCreatedAt(): Promise<string> {
  if (!originalCleanupCursorRows) {
    originalCleanupCursorRows = await client.query<StatsJobStateRow>(`
      SELECT scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at,
        last_error_message, lag_seconds, updated_at
      FROM juhe_stats.stats_job_state
      WHERE scope_type = 'global'
        AND scope_id = ''
        AND job_name = ANY(?::text[])
    `, [[...cleanupCursorJobNames]])
  }

  const existingUsableJobs = new Set(originalCleanupCursorRows
    .filter((row) => Number.isFinite(Date.parse(String(row.cursor_created_at ?? ''))) && String(row.cursor_id ?? '').trim())
    .map((row) => row.job_name))
  const cursorNow = new Date().toISOString()
  for (const jobName of cleanupCursorJobNames) {
    if (existingUsableJobs.has(jobName)) {
      continue
    }
    await client.execute(`
      INSERT INTO juhe_stats.stats_job_state (
        scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at,
        last_error_message, lag_seconds, updated_at
      ) VALUES ('global', '', ?, ?, ?, ?, NULL, 0, ?)
      ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
        cursor_created_at = EXCLUDED.cursor_created_at,
        cursor_id = EXCLUDED.cursor_id,
        last_success_at = EXCLUDED.last_success_at,
        last_error_message = NULL,
        lag_seconds = 0,
        updated_at = EXCLUDED.updated_at
    `, [jobName, cursorNow, `${marker}_${jobName}_cursor`, cursorNow, cursorNow])
    modifiedCleanupCursorJobNames.add(jobName)
  }

  const rows = await client.query<{ job_name?: string | null; cursor_created_at?: string | null }>(`
    SELECT job_name, cursor_created_at
    FROM juhe_stats.stats_job_state
    WHERE scope_type = 'global'
      AND scope_id = ''
      AND job_name = ANY(?::text[])
      AND cursor_created_at IS NOT NULL
      AND cursor_id IS NOT NULL
    ORDER BY cursor_created_at ASC
  `, [[...cleanupCursorJobNames]])
  const jobNames = new Set(rows.map((row) => String(row.job_name ?? '').trim()).filter(Boolean))
  if (!jobNames.has('usage_stats_aggregation') || !jobNames.has('client_ip_stats_aggregation')) {
    throw new Error('PG 记录清理 smoke 需要 usage_stats_aggregation 和 client_ip_stats_aggregation 全局游标')
  }
  const cursorCreatedAt = rows[0]?.cursor_created_at?.trim()
  const cursorTime = Date.parse(cursorCreatedAt ?? '')
  if (!Number.isFinite(cursorTime)) {
    throw new Error('PG 记录清理 smoke 未读到有效 usage 清理游标时间')
  }
  const preferredSmokeCreatedAt = Date.parse('2000-01-01T00:00:00.000Z')
  return new Date(Math.max(0, Math.min(cursorTime - 1000, preferredSmokeCreatedAt))).toISOString()
}

async function restoreCleanupCursorRows(): Promise<void> {
  if (!originalCleanupCursorRows || modifiedCleanupCursorJobNames.size === 0) {
    return
  }
  for (const jobName of modifiedCleanupCursorJobNames) {
    const row = originalCleanupCursorRows.find((candidate) => candidate.job_name === jobName)
    if (!row) {
      await client.execute(`
        DELETE FROM juhe_stats.stats_job_state
        WHERE scope_type = 'global'
          AND scope_id = ''
          AND job_name = ?
      `, [jobName])
      continue
    }
    await client.execute(`
      INSERT INTO juhe_stats.stats_job_state (
        scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at,
        last_error_message, lag_seconds, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(scope_type, scope_id, job_name) DO UPDATE SET
        cursor_created_at = EXCLUDED.cursor_created_at,
        cursor_id = EXCLUDED.cursor_id,
        last_success_at = EXCLUDED.last_success_at,
        last_error_message = EXCLUDED.last_error_message,
        lag_seconds = EXCLUDED.lag_seconds,
        updated_at = EXCLUDED.updated_at
    `, [
      row.scope_type,
      row.scope_id,
      row.job_name,
      row.cursor_created_at ?? null,
      row.cursor_id ?? null,
      row.last_success_at ?? null,
      row.last_error_message ?? null,
      row.lag_seconds ?? null,
      row.updated_at
    ])
  }
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
  await cleanupSmokeStatsRows()
  await client.execute('DELETE FROM juhe_stats.usage_record_cleanup_deductions WHERE usage_id = ANY(?::text[])', [[`usage_${apiKeyId}`, `usage_${accountId}`]])
}

async function cleanupSmokeStatsRows(): Promise<void> {
  const scopeIds = [apiKeyId, accountId, relatedAccountId, authorizationId, teamScopeId, systemAccountId, providerCode, modelName]
  for (const tableName of ['usage_stats_totals', 'usage_stats_minute', 'usage_stats_hourly', 'usage_stats_daily', 'usage_stats_weekly', 'usage_stats_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE system_account_id = ? OR scope_id = ANY(?::text[])`, [systemAccountId, scopeIds])
  }
  for (const tableName of ['usage_latency_minute', 'usage_latency_hourly', 'usage_latency_daily', 'usage_latency_weekly', 'usage_latency_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE system_account_id = ? OR scope_id = ANY(?::text[])`, [systemAccountId, scopeIds])
  }
  for (const tableName of ['usage_model_minute', 'usage_model_hourly', 'usage_model_daily', 'usage_model_weekly', 'usage_model_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE system_account_id = ? OR provider_code = ? OR model = ?`, [systemAccountId, providerCode, modelName])
  }
  for (const tableName of ['usage_error_minute', 'usage_error_hourly', 'usage_error_daily', 'usage_error_weekly', 'usage_error_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE system_account_id = ? OR error_group = ? OR provider_code = ? OR error_code = ?`, [systemAccountId, providerCode, providerCode, providerCode])
  }
  await client.execute('DELETE FROM juhe_stats.stats_job_state WHERE scope_id = ANY(?::text[])', [scopeIds])
  await client.execute('DELETE FROM juhe_stats.account_quality_scores WHERE account_id = ANY(?::text[])', [[accountId, relatedAccountId]])
  await client.execute('DELETE FROM juhe_stats.account_quality_dirty_accounts WHERE account_id = ANY(?::text[])', [[accountId, relatedAccountId]])
  await client.execute('DELETE FROM juhe_stats.account_quality_minute_stats WHERE account_id = ANY(?::text[])', [[accountId, relatedAccountId]])
  await client.execute('DELETE FROM juhe_stats.account_usage_snapshots WHERE account_id = ANY(?::text[])', [[accountId, relatedAccountId]])
  await client.execute(`
    DELETE FROM juhe_stats.authorization_team_usage_summary_daily
    WHERE system_account_id = ?
      OR team_filter_id = ANY(?::text[])
      OR resource_filter_id = ANY(?::text[])
  `, [systemAccountId, [`team_${marker}`, teamScopeId], scopeIds])
  await client.execute(`
    DELETE FROM juhe_stats.authorization_user_usage_summary_daily
    WHERE system_account_id = ?
      OR team_filter_id = ANY(?::text[])
      OR grantee_filter_system_account_id = ?
      OR resource_filter_id = ANY(?::text[])
  `, [systemAccountId, [`team_${marker}`, teamScopeId], systemAccountId, scopeIds])
}
