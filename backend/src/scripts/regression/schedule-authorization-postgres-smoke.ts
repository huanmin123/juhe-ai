import { strict as assert } from 'node:assert'

import { DEFAULT_OPENAI_SUPPORTED_MODELS } from '../../storage/schema-defaults.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { runtimeConfig } from '../../config/runtime.js'
import { createApiKeyRecordAsync } from '../../storage/api-key.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAccountAsync,
  createGroupAsync,
  createResourceAuthorizationAsync,
  createRouteStrategyAsync,
  expireDueResourceAuthorizationsAsync,
  syncAccountAvailabilityScheduleStatusesAsync,
  syncApiKeyAvailabilityScheduleStatusesAsync
} from '../../storage/repositories.js'
import type { AccessScope } from '../../storage/access-scope.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '时间计划 / 授权过期 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `schedule_auth_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const access: AccessScope = { systemAccountId: 'sys_admin', role: 'super_admin' }
const boundaryAt = '2026-06-01T22:00:00.000Z'
const expireAt = '2026-01-01T00:00:00.000Z'
const schedule = {
  enabled: true,
  timezone: 'UTC',
  mode: 'allow_windows',
  windows: [
    { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
  ]
}

const createdApiKeyIds: string[] = []
const createdRouteStrategyIds: string[] = []
const createdAccountIds: string[] = []
const createdGroupIds: string[] = []
const createdSystemAccountIds: string[] = []

try {
  const group = await createGroupAsync({
    name: `时间计划授权 PG smoke 分组 ${marker}`,
    providerCode: 'gpt',
    enabled: true
  }, access)
  createdGroupIds.push(group.id)

  const routeStrategy = await createRouteStrategyAsync({
    name: `时间计划授权 PG smoke 路由 ${marker}`,
    mode: 'normal',
    status: 'active',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }]
  }, access)
  createdRouteStrategyIds.push(routeStrategy.id)

  const apiKey = await createApiKeyRecordAsync({
    name: `时间计划授权 PG smoke Key ${marker}`,
    routeStrategyId: routeStrategy.id,
    status: 'active',
    availabilitySchedule: schedule
  }, access)
  createdApiKeyIds.push(apiKey.id)
  await forceApiKeyScheduleBoundary(apiKey.id, false, boundaryAt)
  const apiKeyFirst = await syncApiKeyAvailabilityScheduleStatusesAsync(new Date(boundaryAt))
  assert.equal(apiKeyFirst.activated, 1, 'PG API Key 时间计划开始边界应启用 status')
  assert(apiKeyFirst.changedIds.includes(apiKey.id), 'PG API Key 时间计划结果应返回 changedIds')
  assert.equal(await readApiKeyStatus(apiKey.id), 'active', 'PG API Key 时间计划应写回 status=active')
  await forceApiKeyScheduleBoundary(apiKey.id, true, boundaryAt)
  const apiKeySecond = await syncApiKeyAvailabilityScheduleStatusesAsync(new Date(boundaryAt))
  assert.equal(apiKeySecond.skipped, 1, 'PG API Key 时间计划重复事件应被事件表幂等跳过')

  const account = await createAccountAsync({
    name: `时间计划授权 PG smoke 账号 ${marker}`,
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    type: 'api_key',
    status: 'active',
    groupId: group.id,
    credentials: {
      api_key: `sk-schedule-auth-pg-${marker}`,
      base_url: 'https://example.invalid/v1'
	    },
	    supportedModels: [DEFAULT_OPENAI_SUPPORTED_MODELS[0]],
	    availabilitySchedule: schedule
	  }, access)
	  createdAccountIds.push(account.id)
	  await forceAccountScheduleBoundary(account.id, false, boundaryAt)
	  const accountFirst = await syncAccountAvailabilityScheduleStatusesAsync(new Date(boundaryAt))
	  assert.equal(accountFirst.activated, 1, 'PG 账户时间计划开始边界应启用 status')
	  assert(accountFirst.changedIds.includes(account.id), 'PG 账户时间计划结果应返回 changedIds')
	  assert.equal(await readAccountStatus(account.id), 'active', 'PG 账户时间计划应写回 status=active')
  await forceAccountScheduleBoundary(account.id, true, boundaryAt)
  const accountSecond = await syncAccountAvailabilityScheduleStatusesAsync(new Date(boundaryAt))
  assert.equal(accountSecond.skipped, 1, 'PG 账户时间计划重复事件应被事件表幂等跳过')

  const granteeId = `sysacc_${marker}`
  await insertSmokeSystemAccount(granteeId)
  createdSystemAccountIds.push(granteeId)
  const authorization = await createResourceAuthorizationAsync({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'system_account',
    granteeId,
    expiresAt: '2999-01-01T00:00:00.000Z'
  }, access)
  await forceAuthorizationGrantExpired(group.id, granteeId)
  const expired = await expireDueResourceAuthorizationsAsync(5)
  assert.equal(expired, 1, 'PG 授权过期扫描应处理到期 grant')
  assert.equal(await readExpiredGrantCount(group.id, granteeId), 1, 'PG 授权过期扫描应写回 grant expired')
  assert.equal(await readRuntimeAuthorizationStatus(group.id, granteeId), 'expired', 'PG 授权过期扫描应同步 runtime authorization 状态')
  await assertAuthorizationExpiryExplainUsesIndex()

  console.log(JSON.stringify({
    message: '时间计划 / 授权过期 PG smoke 通过',
    apiKeyId: apiKey.id,
    accountId: account.id,
    authorizationId: authorization.id,
    expired,
    explainIndexed: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function forceApiKeyScheduleBoundary(id: string, active: boolean, nextCheckAt: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(
    `UPDATE juhe_business.api_keys
     SET status = $1,
         availability_schedule_next_check_at = $2
     WHERE id = $3`,
    [active ? 'active' : 'disabled', nextCheckAt, id]
  )
}

async function readApiKeyStatus(id: string): Promise<string | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT status FROM juhe_business.api_keys WHERE id = $1', [id])
  const row = result.rows[0] as { status?: string } | undefined
  return row?.status
}

async function forceAccountScheduleBoundary(id: string, active: boolean, nextCheckAt: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(
    `UPDATE juhe_business.accounts
     SET status = $1,
         availability_schedule_next_check_at = $2
     WHERE id = $3`,
    [active ? 'active' : 'disabled', nextCheckAt, id]
  )
}

async function readAccountStatus(id: string): Promise<string | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT status FROM juhe_business.accounts WHERE id = $1', [id])
  const row = result.rows[0] as { status?: string } | undefined
  return row?.status
}

async function insertSmokeSystemAccount(id: string): Promise<void> {
  const pool = await getPostgresPool()
  const now = new Date().toISOString()
  await pool.query(
    `INSERT INTO juhe_business.system_accounts (
       id, username, display_name, role, status, password_hash, must_change_password,
       image_generation_enabled, created_at, updated_at
     ) VALUES ($1, $2, $3, 'user', 'active', $4, 0, 0, $5, $5)`,
    [id, `smoke_${marker}`, `时间计划授权 PG smoke 用户 ${marker}`, 'pg-smoke-password-hash', now]
  )
}

async function forceAuthorizationGrantExpired(groupId: string, granteeId: string): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(
    `UPDATE juhe_business.resource_authorization_grants
     SET expires_at = $1
     WHERE resource_type = 'group'
       AND resource_id = $2
       AND grantee_system_account_id = $3
       AND status = 'active'`,
    [expireAt, groupId, granteeId]
  )
}

async function readExpiredGrantCount(groupId: string, granteeId: string): Promise<number> {
  const pool = await getPostgresPool()
  const result = await pool.query(
    `SELECT COUNT(*)::int AS count
     FROM juhe_business.resource_authorization_grants
     WHERE resource_type = 'group'
       AND resource_id = $1
       AND grantee_system_account_id = $2
       AND status = 'expired'`,
    [groupId, granteeId]
  )
  return Number((result.rows[0] as { count?: unknown } | undefined)?.count ?? 0)
}

async function readRuntimeAuthorizationStatus(groupId: string, granteeId: string): Promise<string | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query(
    `SELECT status
     FROM juhe_business.resource_authorizations
     WHERE resource_type = 'group'
       AND resource_id = $1
       AND grantee_system_account_id = $2
     ORDER BY updated_at DESC
     LIMIT 1`,
    [groupId, granteeId]
  )
  const row = result.rows[0] as { status?: unknown } | undefined
  return typeof row?.status === 'string' ? row.status : undefined
}

async function assertAuthorizationExpiryExplainUsesIndex(): Promise<void> {
  const pool = await getPostgresPool()
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('SET LOCAL enable_seqscan = off')
    const result = await client.query(
      `EXPLAIN (COSTS OFF)
       SELECT *
       FROM juhe_business.resource_authorization_grants
       WHERE status IN ('active', 'paused')
         AND expires_at IS NOT NULL
         AND expires_at <= $1
       ORDER BY expires_at ASC, updated_at ASC, id ASC
       LIMIT 5
       FOR UPDATE SKIP LOCKED`,
      [expireAt]
    )
    const plan = result.rows.map((row) => String(row['QUERY PLAN'] ?? '')).join('\n')
    assert.match(plan, /idx_resource_authorization_grants_expiry_sweep/, 'PG 授权过期扫描应命中过期扫描索引')
    assert.doesNotMatch(plan, /\bSeq Scan\b/, 'PG 授权过期扫描不应出现 Seq Scan')
  } finally {
    await client.query('ROLLBACK').catch(() => undefined)
    client.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (createdGroupIds.length > 0 || createdSystemAccountIds.length > 0) {
    await pool.query(
      `DELETE FROM juhe_business.resource_authorization_sources
       WHERE authorization_id IN (
         SELECT id
         FROM juhe_business.resource_authorizations
         WHERE resource_id = ANY($1::text[])
            OR grantee_system_account_id = ANY($2::text[])
       )`,
      [createdGroupIds, createdSystemAccountIds]
    )
    await pool.query(
      `DELETE FROM juhe_business.resource_authorizations
       WHERE resource_id = ANY($1::text[])
          OR grantee_system_account_id = ANY($2::text[])`,
      [createdGroupIds, createdSystemAccountIds]
    )
    await pool.query(
      `DELETE FROM juhe_business.resource_authorization_grants
       WHERE resource_id = ANY($1::text[])
          OR grantee_system_account_id = ANY($2::text[])`,
      [createdGroupIds, createdSystemAccountIds]
    )
  }
  if (createdApiKeyIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.api_key_schedule_status_events WHERE api_key_id = ANY($1::text[])', [createdApiKeyIds])
    await pool.query('DELETE FROM juhe_business.api_keys WHERE id = ANY($1::text[])', [createdApiKeyIds])
  }
  if (createdRouteStrategyIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE route_strategy_id = ANY($1::text[])', [createdRouteStrategyIds])
    await pool.query('DELETE FROM juhe_business.route_strategies WHERE id = ANY($1::text[])', [createdRouteStrategyIds])
  }
  if (createdAccountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.account_schedule_status_events WHERE account_id = ANY($1::text[])', [createdAccountIds])
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
    await pool.query('DELETE FROM juhe_business.groups WHERE id = ANY($1::text[])', [createdGroupIds])
  }
  if (createdSystemAccountIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])', [createdSystemAccountIds])
  }
}
