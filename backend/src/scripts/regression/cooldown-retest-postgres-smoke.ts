import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { encryptJson } from '../../storage/crypto.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  recordCooldownAccountRetestFailureAsync,
  recordCooldownAccountRetestSuccessAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '冷却复测 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `cooldown_retest_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const accountId = `acc_${marker}`

try {
  const pool = await getPostgresPool()
  const profileResult = await pool.query(`
    SELECT id, protocol_code, protocol_version
    FROM juhe_business.provider_protocol_profiles
    WHERE provider_code = 'gpt'
    ORDER BY id ASC
    LIMIT 1
  `)
  const profile = profileResult.rows[0] as {
    id: string
    protocol_code: string
    protocol_version: string
  } | undefined
  assert(profile, '冷却复测 PG smoke 需要已初始化的 GPT 协议档案')

  const now = new Date().toISOString()
  const observationStartedAt = new Date(Date.now() - 60_000).toISOString()
  await pool.query(`
    INSERT INTO juhe_business.accounts (
      id, system_account_id, provider_code, provider_protocol_profile_id,
      protocol_code, protocol_version, name, type, status, schedulable,
      credentials_encrypted, health_check_model, health_check_endpoint_mode,
      cooldown_until, cooldown_retest_observation_started_at, created_at, updated_at
    ) VALUES ($1, 'sys_admin', 'gpt', $2, $3, $4, $5, 'api_key',
      'temporary_unavailable', 1, $6, 'gpt-5-mini', 'responses_json', $7, $8, $9, $9)
  `, [
    accountId,
    profile.id,
    profile.protocol_code,
    profile.protocol_version,
    `冷却复测PG写回烟测${marker}`,
    encryptJson({ api_key: `sk-${marker}`, base_url: 'https://example.invalid/v1' }),
    new Date(Date.now() - 1_000).toISOString(),
    observationStartedAt,
    now
  ])

  const currentFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorCode: 'insufficient_user_quota',
    errorMessage: 'PG cooldown writeback smoke',
    expectedConfigRevision: 1,
    expectedObservationStartedAt: observationStartedAt,
    initialBackoffSeconds: 1,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(currentFailure.changed, true, 'PG 当前冷却失败应写回')
  assert.equal(currentFailure.failureCount, 1, 'PG 当前冷却失败应累加计数')

  const beforeStaleFailure = await readRuntimeState(accountId)
  const staleConfigFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorMessage: 'stale config failure',
    expectedConfigRevision: 2,
    expectedObservationStartedAt: observationStartedAt,
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(staleConfigFailure.changed, false, 'PG 陈旧配置版本的失败不得写回')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧配置版本不得改变运行态')

  const staleObservationFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 403,
    errorMessage: 'stale observation failure',
    expectedConfigRevision: 1,
    expectedObservationStartedAt: new Date(Date.now() - 120_000).toISOString(),
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(staleObservationFailure.changed, false, 'PG 陈旧观察窗口的失败不得写回')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleFailure, 'PG 陈旧观察窗口不得改变运行态')

  const successObservationStartedAt = new Date(Date.now() - 30_000).toISOString()
  await resetCoolingState(accountId, successObservationStartedAt)
  const beforeStaleSuccess = await readRuntimeState(accountId)
  const staleConfigSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    expectedConfigRevision: 2,
    expectedObservationStartedAt: successObservationStartedAt
  })
  assert.equal(staleConfigSuccess.changed, false, 'PG 陈旧配置版本的成功不得恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleSuccess, 'PG 陈旧配置版本的成功不得改变运行态')

  const staleObservationSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    expectedConfigRevision: 1,
    expectedObservationStartedAt: new Date(Date.now() - 120_000).toISOString()
  })
  assert.equal(staleObservationSuccess.changed, false, 'PG 陈旧观察窗口的成功不得恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), beforeStaleSuccess, 'PG 陈旧观察窗口的成功不得改变运行态')

  const currentSuccess = await recordCooldownAccountRetestSuccessAsync(accountId, {
    expectedConfigRevision: 1,
    expectedObservationStartedAt: successObservationStartedAt
  })
  assert.equal(currentSuccess.changed, true, 'PG 当前冷却成功应恢复账户')
  assert.deepEqual(await readRuntimeState(accountId), {
    status: 'active',
    schedulable: 1,
    cooldown_until: null,
    cooldown_retest_failure_count: 0,
    cooldown_retest_observation_started_at: null,
    last_error_code: null
  }, 'PG 当前冷却成功应清理冷却运行态')

  await resetCoolingState(accountId, null)
  const unguardedFailure = await recordCooldownAccountRetestFailureAsync(accountId, {
    statusCode: 503,
    errorMessage: 'unguarded failure',
    maxPauseMinutes: 1,
    maxRecoveryHours: 12
  })
  assert.equal(unguardedFailure.changed, true, 'PG 未提供期望值时应保持既有可选保护语义')

  console.log(JSON.stringify({
    message: '冷却复测 PostgreSQL 写回 smoke 通过',
    currentFailure: true,
    currentSuccess: true,
    staleConfigGuard: true,
    staleObservationGuard: true,
    optionalGuard: true
  }))
} finally {
  const pool = await getPostgresPool().catch(() => undefined)
  if (pool) {
    await pool.query('DELETE FROM juhe_business.accounts WHERE id = $1', [accountId]).catch(() => undefined)
  }
  await closeRedisClients()
  await closePostgresPool()
}

async function resetCoolingState(id: string, observationStartedAt: string | null): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    UPDATE juhe_business.accounts
    SET status = 'temporary_unavailable', schedulable = 1,
        cooldown_until = $1, cooldown_retest_failure_count = 0,
        cooldown_retest_observation_started_at = $2,
        cooldown_retest_last_at = NULL, cooldown_retest_last_status_code = NULL,
        updated_at = $1
    WHERE id = $3
  `, [new Date(Date.now() - 1_000).toISOString(), observationStartedAt, id])
}

async function readRuntimeState(id: string): Promise<Record<string, string | number | null>> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT status, schedulable, cooldown_until, cooldown_retest_failure_count,
      cooldown_retest_observation_started_at, last_error_code
    FROM juhe_business.accounts
    WHERE id = $1
  `, [id])
  const row = result.rows[0] as Record<string, string | number | null> | undefined
  assert(row, '冷却复测 PG smoke 应读回测试账户')
  return row
}
