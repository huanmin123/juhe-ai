import { strict as assert } from 'node:assert'

import type { OpenAIAccountSecret } from '../../storage/openai-account-selector.types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { createAccountAsync, createGroupAsync } from '../../storage/repositories.js'
import {
  listAccountApiKeyRuntimeStatesDueForProbeAsync,
  recordAccountApiKeyRuntimeFailureAsync,
  recordAccountApiKeyRuntimeSuccessAsync
} from '../../storage/account-api-key-runtime-state.repository.js'
import { accountApiKeyEntries } from '../../storage/account-api-key-rotation.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { closeRedisClients } from '../../shared/redis-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账户内 API Key runtime state PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `api_key_runtime_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  const group = await createGroupAsync({
    name: `API Key runtime PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)

  const apiKeys = [
    `sk-api-key-runtime-pg-${marker}-a`,
    `sk-api-key-runtime-pg-${marker}-b`
  ]
  const credentials = {
    api_keys: apiKeys,
    api_key: apiKeys[0],
    base_url: 'https://example.invalid/v1'
  }
  const account = await createAccountAsync({
    name: `API Key runtime PG smoke 账号 ${marker}`,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'active',
    groupId: group.id,
    credentials,
    concurrencyLimit: 20,
    supportedModels: ['gpt-5-mini']
  }, access)
  createdAccountIds.push(account.id)

  const entries = accountApiKeyEntries(credentials)
  assert.equal(entries.length, 2, '测试账号必须启用多 API Key runtime isolation')
  const selected = entries[0]
  const dispatchAccount: OpenAIAccountSecret = {
    id: account.id,
    providerCode: account.providerCode,
    providerProtocolProfileId: account.providerProtocolProfileId ?? 'gpt-openai-v1',
    protocolCode: account.protocolCode ?? 'openai',
    protocolVersion: account.protocolVersion ?? 'v1',
    systemAccountId: access.systemAccountId,
    accountOwnerSystemAccountId: access.systemAccountId,
    groupOwnerSystemAccountId: access.systemAccountId,
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    boundGroupId: group.id,
    name: account.name,
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 20,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: account.clientCompatibility,
    supportedModels: ['gpt-5-mini'],
    healthCheckEndpointMode: 'responses_sse',
    baseUrl: 'https://example.invalid/v1',
    apiKey: selected.key,
    apiKeys,
    selectedApiKeyFingerprint: selected.fingerprint,
    selectedApiKeyIndex: selected.index,
    streamFailureCount: 0,
    credentials
  }

  const dueAt = new Date(Date.now() - 1000).toISOString()
  const failure = await recordAccountApiKeyRuntimeFailureAsync({
    account: dispatchAccount,
    status: 'rate_limited',
    statusCode: 429,
    errorCode: 'rate_limit_smoke',
    errorMessage: 'PG runtime state smoke',
    cooldownUntil: dueAt
  })
  assert.equal(failure.changed, true, 'PG failure 写回应创建 runtime state')

  const candidates = await listAccountApiKeyRuntimeStatesDueForProbeAsync(20)
  const candidate = candidates.find((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint)
  assert.ok(candidate, 'PG due-for-probe 读取应返回刚写入的 key')
  assert.equal(candidate.apiKey, selected.key, 'PG due-for-probe 应能从加密凭据恢复目标 API Key')

  const dirty = await readDirtyReason(group.id)
  assert.equal(dirty, 'account_api_key_runtime', 'PG runtime state 写回应标记分组账号统计 dirty')

  const success = await recordAccountApiKeyRuntimeSuccessAsync(dispatchAccount)
  assert.equal(success.changed, true, 'PG success 写回应恢复 key 到 active')
  const afterSuccess = await listAccountApiKeyRuntimeStatesDueForProbeAsync(20)
  assert.equal(afterSuccess.some((item) => item.accountId === account.id && item.keyFingerprint === selected.fingerprint), false, 'PG success 后 key 不应继续进入 probe 候选')

  await assertProbeExplainUsesIndex(dueAt)

  console.log(JSON.stringify({
    message: '账户内 API Key runtime state PG smoke 通过',
    candidatesBeforeSuccess: candidates.length,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function readDirtyReason(groupId: string): Promise<string | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query(
    'SELECT reason FROM juhe_business.group_account_stats_dirty WHERE group_id = $1 LIMIT 1',
    [groupId]
  )
  const row = result.rows[0] as { reason?: unknown } | undefined
  return typeof row?.reason === 'string' ? row.reason : undefined
}

async function assertProbeExplainUsesIndex(dueAt: string): Promise<void> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL enable_seqscan = off')
    const result = await client.query(
      `EXPLAIN (COSTS OFF)
       SELECT states.account_id, states.key_fingerprint
       FROM juhe_business.account_api_key_runtime_states states
       JOIN juhe_business.accounts accounts ON accounts.id = states.account_id
       WHERE states.status IN ('temporary_unavailable', 'rate_limited', 'error')
         AND states.next_probe_at IS NOT NULL
         AND states.next_probe_at <= $1
         AND accounts.deleted_at IS NULL
         AND accounts.status = 'active'
         AND accounts.schedulable = 1
         AND (accounts.account_expires_at IS NULL OR accounts.account_expires_at > $2)
       ORDER BY states.next_probe_at ASC, states.updated_at ASC, states.account_id ASC, states.key_index ASC
       LIMIT 20`,
      [dueAt, dueAt]
    )
    const plan = result.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(plan, /idx_account_api_key_runtime_probe/, 'PG probe 查询应使用 next_probe_at/status 索引')
    assert.doesNotMatch(plan, /\bSeq Scan\b/, 'PG probe 查询不应出现 Seq Scan')
  } finally {
    await client.query('ROLLBACK').catch(() => undefined)
    client.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (createdAccountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[])', [createdAccountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [createdAccountIds])
  }
  if (createdGroupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [createdGroupIds])
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [createdGroupIds])
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
  }
}
