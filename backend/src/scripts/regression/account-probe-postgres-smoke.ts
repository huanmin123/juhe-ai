import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { handleDbServiceOperation } from '../../modules/db-service/db-service-handlers.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { createAccountAsync, createGroupAsync } from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '账号探测 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `account_probe_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []

try {
  const group = await createGroupAsync({
    name: `账号探测PG烟测分组${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)

  const account = await createAccountAsync({
    name: `账号探测PG烟测账号${marker}`,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'active',
    groupId: group.id,
    credentials: {
      api_key: `sk-${marker}`,
      base_url: 'https://example.invalid/v1'
    },
    supportedModels: ['gpt-5-mini']
  }, access)
  createdAccountIds.push(account.id)

  const dueAt = new Date(Date.now() - 60_000).toISOString()
  await setHealthCheckDue(account.id, dueAt)

  const healthCandidates = await handleDbServiceOperation({
    type: 'list_accounts_due_for_health_check',
    input: {
      limit: 20,
      intervalHours: 1,
      jitterMinutes: 0,
      failureThreshold: 2
    }
  })
  const healthCandidate = healthCandidates.find((item) => item.id === account.id)
  assert.ok(healthCandidate, 'PG health check 候选应返回到期账号')
  assert.equal(healthCandidate.boundGroupId, group.id, 'PG health check 候选应带分组绑定')
  assert.deepEqual(healthCandidate.supportedModels, ['gpt-5-mini'], 'PG health check 候选应带支持模型')

  const healthAccount = await handleDbServiceOperation({
    type: 'find_account_for_health_check',
    accountId: account.id
  })
  assert.equal(healthAccount?.id, account.id, 'PG health check 单账号读取应返回目标账号')

  const checkedAt = new Date().toISOString()
  const healthSuccess = await handleDbServiceOperation({
    type: 'record_account_health_check_success',
    accountId: account.id,
    input: {
      intervalHours: 1,
      jitterMinutes: 0,
      failureThreshold: 2,
      checkedAt,
      statusCode: 200
    }
  })
  assert.equal(healthSuccess.changed, true, 'PG health check success 应写回成功状态')
  const afterSuccess = await readAccountRuntimeFields(account.id)
  assert.equal(afterSuccess.health_check_failure_count, 0, 'PG health success 应清零失败次数')
  assert.equal(afterSuccess.last_health_check_status_code, 200, 'PG health success 应写入状态码')
  assert(afterSuccess.next_health_check_at && afterSuccess.next_health_check_at > checkedAt, 'PG health success 应顺延下次检测')

  await setHealthCheckDue(account.id, dueAt)
  const healthFailure = await handleDbServiceOperation({
    type: 'record_account_health_check_failure',
    accountId: account.id,
    input: {
      intervalHours: 1,
      jitterMinutes: 0,
      failureThreshold: 2,
      statusCode: 503,
      errorCode: 'health_probe_smoke',
      errorMessage: 'PG health smoke'
    }
  })
  assert.equal(healthFailure.changed, true, 'PG health check failure 应写回失败状态')
  assert.equal(healthFailure.failureCount, 1, 'PG health failure 应累加失败次数')
  assert.equal(healthFailure.reachedThreshold, false, '首次失败不应达到阈值')
  const afterHealthFailure = await readAccountRuntimeFields(account.id)
  assert.equal(afterHealthFailure.health_check_failure_count, 1, 'PG health failure 应落库失败次数')
  assert.equal(afterHealthFailure.last_health_check_error_code, 'health_probe_smoke', 'PG health failure 应落库错误码')

  await setCooldownDue(account.id, dueAt)
  const cooldownCandidates = await handleDbServiceOperation({
    type: 'list_accounts_due_for_cooldown_retest',
    limit: 20
  })
  const cooldownCandidate = cooldownCandidates.find((item) => item.id === account.id)
  assert.ok(cooldownCandidate, 'PG cooldown retest 候选应返回到期冷却账号')
  assert.equal(cooldownCandidate.status, 'temporary_unavailable', 'PG cooldown retest 候选应保留冷却状态')
  assert.equal(cooldownCandidate.boundGroupId, group.id, 'PG cooldown retest 候选应带分组绑定')

  const cooldownAccount = await handleDbServiceOperation({
    type: 'find_account_for_cooldown_retest',
    accountId: account.id
  })
  assert.equal(cooldownAccount?.id, account.id, 'PG cooldown retest 单账号读取应返回目标账号')

  const cooldownFailure = await handleDbServiceOperation({
    type: 'record_cooldown_account_retest_failure',
    accountId: account.id,
    input: {
      statusCode: 503,
      errorCode: 'cooldown_probe_smoke',
      errorMessage: 'PG cooldown smoke',
      initialBackoffSeconds: 1,
      fastThresholdSeconds: 60,
      maxPauseMinutes: 1,
      maxRecoveryHours: 12,
      longTermIntervalHours: 24
    }
  })
  assert.equal(cooldownFailure.changed, true, 'PG cooldown failure 应写回退避状态')
  assert.equal(cooldownFailure.failureCount, 1, 'PG cooldown failure 应累加失败次数')
  assert.equal(cooldownFailure.action, 'retry_immediately', '短退避应保持快速恢复通道')
  assert.equal(cooldownFailure.errorCode, 'cooldown_probe_smoke', 'PG cooldown failure 应返回持久化错误码')
  const afterCooldownFailure = await readAccountRuntimeFields(account.id)
  assert.equal(afterCooldownFailure.cooldown_retest_failure_count, 1, 'PG cooldown failure 应落库失败次数')
  assert.equal(afterCooldownFailure.last_error_code, 'cooldown_probe_smoke', 'PG cooldown failure 应落库错误码')
  assert(afterCooldownFailure.cooldown_until && afterCooldownFailure.cooldown_until > dueAt, 'PG cooldown failure 应写入下一次冷却时间')

  await assertProbeExplainUsesIndexes(dueAt)

  console.log(JSON.stringify({
    message: '账号健康检测 / 冷却复测 DB service PG smoke 通过',
    accountId: account.id,
    healthCandidates: healthCandidates.length,
    cooldownCandidates: cooldownCandidates.length,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function setHealthCheckDue(accountId: string, dueAt: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'active',
        schedulable = 1,
        health_check_enabled = 1,
        next_health_check_at = $1,
        last_health_success_at = NULL,
        cooldown_until = NULL,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        account_expires_at = NULL,
        updated_at = $1
    WHERE id = $2
  `, [dueAt, accountId])
}

async function setCooldownDue(accountId: string, dueAt: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable',
        schedulable = 1,
        cooldown_until = $1,
        cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = NULL,
        cooldown_retest_last_at = NULL,
        cooldown_retest_last_status_code = NULL,
        account_expires_at = NULL,
        updated_at = $1
    WHERE id = $2
  `, [dueAt, accountId])
}

async function readAccountRuntimeFields(accountId: string): Promise<Record<string, string | number | null>> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT status, cooldown_until, last_error_code, last_error_message,
      cooldown_retest_failure_count, last_health_check_status_code,
      next_health_check_at, health_check_failure_count, last_health_check_error_code
    FROM juhe_business.accounts
    WHERE id = $1
    LIMIT 1
  `, [accountId])
  const row = result.rows[0] as Record<string, string | number | null> | undefined
  assert(row, 'PG smoke 应能读回账号运行态字段')
  return row
}

async function assertProbeExplainUsesIndexes(dueAt: string): Promise<void> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL enable_seqscan = off')
    const healthPlanRows = await client.query(`
      EXPLAIN (COSTS OFF)
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      WHERE accounts.provider_protocol_profile_id IN ('gpt-openai-v1')
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.deleted_at IS NULL
        AND accounts.status = 'active'
        AND accounts.schedulable = 1
        AND accounts.health_check_enabled = 1
        AND (accounts.next_health_check_at IS NULL OR accounts.next_health_check_at <= $1)
      ORDER BY accounts.next_health_check_at IS NOT NULL ASC,
        accounts.next_health_check_at ASC,
        accounts.last_health_check_at ASC,
        accounts.created_at ASC,
        accounts.id ASC
      LIMIT 20
    `, [dueAt])
    const healthPlan = healthPlanRows.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(healthPlan, /idx_accounts_health_check_pg_due/, 'PG health check due 查询应命中 profile 前导 health due 索引')
    assert.doesNotMatch(healthPlan, /\bSeq Scan\b/, 'PG health check due 查询不应出现 Seq Scan')

    const cooldownPlanRows = await client.query(`
      EXPLAIN (COSTS OFF)
      SELECT accounts.id
      FROM juhe_business.accounts accounts
      WHERE accounts.provider_protocol_profile_id IN ('gpt-openai-v1')
        AND accounts.type IN ('api_key', 'oauth')
        AND accounts.deleted_at IS NULL
        AND accounts.status IN ('temporary_unavailable', 'rate_limited')
        AND accounts.schedulable = 1
        AND accounts.cooldown_until IS NOT NULL
        AND accounts.cooldown_until <= $1
      ORDER BY accounts.cooldown_until ASC, accounts.priority ASC, accounts.created_at ASC, accounts.id ASC
      LIMIT 20
    `, [dueAt])
    const cooldownPlan = cooldownPlanRows.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(cooldownPlan, /idx_accounts_cooldown_retest_pg_due/, 'PG cooldown due 查询应命中 profile 前导 cooldown 索引')
    assert.doesNotMatch(cooldownPlan, /\bSeq Scan\b/, 'PG cooldown due 查询不应出现 Seq Scan')
  } finally {
    await client.query('ROLLBACK').catch(() => undefined)
    client.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const accountIds = [...new Set(createdAccountIds)]
  if (accountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.group_accounts WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_supported_models WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_model_mappings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_tag_bindings WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_terms WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_name_search_documents WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.account_api_key_runtime_states WHERE account_id = ANY($1::text[])', [accountIds])
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = ANY($1::text[])', [accountIds])
  }
  const groupIds = [...new Set(createdGroupIds)]
  if (groupIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE group_id = ANY($1::text[])', [groupIds])
    await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = ANY($1::text[])', [groupIds]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [groupIds])
  }
  await pool.query('DELETE FROM juhe_business.groups WHERE name = $1', [`账号探测PG烟测分组${marker}`])
}
