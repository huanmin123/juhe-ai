import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID, GPT_VENDOR_CODE, OPENAI_PROTOCOL_CODE, OPENAI_PROTOCOL_VERSION } from '../../domain/provider-protocol.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { getAuthorizationTeamUsageOverviewAsync, getAuthorizationUserUsageOverviewAsync } from '../../storage/authorization-usage.repository.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { refreshUsageRankSnapshotsInStages } from '../../storage/usage-stats.repository.js'
import { dateKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '授权范围窗口 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `authorization_range_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const ownerSystemAccountId = `owner_${marker}`
const granteeSystemAccountId = `grantee_${marker}`
const teamId = `team_${marker}`
const memberId = `team_member_${marker}`
const accountId = `acct_${marker}`
const jobName = `authorization-usage-range-windows-refresh:${marker}`
const updatedAt = new Date().toISOString()

const ownerAccess: AccessScope = {
  systemAccountId: `admin_${marker}`,
  role: 'admin',
  systemAccountFilterId: ownerSystemAccountId
}

try {
  const timezone = await usageStatsTimezoneAsync()
  const today = dateKey(new Date(), timezone)
  const range = {
    startDate: today,
    endDate: today,
    days: 1,
    maxDays: 31
  }
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)

  await cleanupSmokeRows()
  await client.transaction(async (tx) => {
    await tx.execute(`
      INSERT INTO juhe_business.system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
      VALUES (?, ?, '授权范围窗口 owner', 'admin', 'active', 'smoke-password-hash', ?, ?)
    `, [ownerSystemAccountId, `${marker}_owner`, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_business.system_accounts (id, username, display_name, role, status, password_hash, created_at, updated_at)
      VALUES (?, ?, '授权范围窗口 grantee', 'user', 'active', 'smoke-password-hash', ?, ?)
    `, [granteeSystemAccountId, `${marker}_grantee`, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_business.system_teams (id, name, description, status, created_by, created_at, updated_at)
      VALUES (?, '授权范围窗口团队', 'authorization range smoke team', 'active', ?, ?, ?)
    `, [teamId, ownerSystemAccountId, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_business.system_team_members (
        id, team_id, system_account_id, member_role, status, joined_at, created_by, created_at, updated_at
      ) VALUES (?, ?, ?, 'member', 'active', ?, ?, ?, ?)
    `, [memberId, teamId, granteeSystemAccountId, updatedAt, ownerSystemAccountId, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_business.accounts (
        id, system_account_id, provider_code, provider_protocol_profile_id, protocol_code, protocol_version,
        name, type, status, credentials_encrypted, credential_mask, concurrency_limit, schedulable,
        health_check_model, health_check_endpoint_mode,
        created_at, updated_at
      )
      VALUES (?, ?, ?, ?, ?, ?, '授权范围窗口账号', 'api_key', 'active', '{}', 'sk-***auth-range-smoke', 20, 1,
      'gpt-5.4-mini', 'responses_sse', ?, ?)
    `, [
      accountId,
      ownerSystemAccountId,
      GPT_VENDOR_CODE,
      GPT_OPENAI_V1_PROFILE_ID,
      OPENAI_PROTOCOL_CODE,
      OPENAI_PROTOCOL_VERSION,
      updatedAt,
      updatedAt
    ])
    await tx.execute(`
      INSERT INTO juhe_stats.authorization_team_usage_summary_daily (
        system_account_id, stat_date, team_filter_id, resource_filter_type, resource_filter_id,
        request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      ) VALUES (?, ?, ?, 'account', ?, 13, 12, 1, 130, 39, 7, 0.003, 0.456, ?, ?)
    `, [ownerSystemAccountId, today, teamId, accountId, updatedAt, updatedAt])
    await tx.execute(`
      INSERT INTO juhe_stats.authorization_user_usage_summary_daily (
        system_account_id, stat_date, team_filter_id, grantee_filter_system_account_id, resource_filter_type, resource_filter_id,
        request_count, success_count, error_count, input_tokens, output_tokens, cache_read_tokens,
        cache_read_cost_usd, total_cost_usd, last_used_at, updated_at
      ) VALUES (?, ?, ?, ?, 'account', ?, 17, 16, 1, 170, 51, 9, 0.004, 0.789, ?, ?)
    `, [ownerSystemAccountId, today, teamId, granteeSystemAccountId, accountId, updatedAt, updatedAt])
  })

  const refreshed = await refreshUsageRankSnapshotsInStages({
    stageNames: ['authorization_usage_range_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(refreshed.skipped, false, '首次 PG authorization range refresh 不应跳过')
  assert.equal(refreshed.stages.length, 1, 'PG authorization range refresh 应只执行一个阶段')

  const teamOverview = await getAuthorizationTeamUsageOverviewAsync({
    teamId,
    resourceType: 'account',
    resourceId: accountId
  }, ownerAccess, range, { page: 1, pageSize: 10 })
  assert.equal(teamOverview.summary.requestCount, 13, '团队授权范围 summary 应从 PG 窗口表读取 requestCount')
  assert.equal(teamOverview.rows.length, 1, '团队授权范围明细应返回一行')
  assert.equal(teamOverview.rows[0]?.teamId, teamId, '团队授权范围明细应保留团队 ID')
  assert.equal(teamOverview.rows[0]?.resourceName, '授权范围窗口账号', '团队授权范围明细应通过 async lookup 装配资源名')
  assert.equal(teamOverview.rows[0]?.usage.totalCost, 0.456, '团队授权范围明细应读取 totalCost')

  const userOverview = await getAuthorizationUserUsageOverviewAsync({
    teamId,
    granteeSystemAccountId,
    resourceType: 'account',
    resourceId: accountId
  }, ownerAccess, range, { page: 1, pageSize: 10 })
  assert.equal(userOverview.summary.requestCount, 17, '用户授权范围 summary 应从 PG 窗口表读取 requestCount')
  assert.equal(userOverview.rows.length, 1, '用户授权范围明细应返回一行')
  assert.equal(userOverview.rows[0]?.systemAccountId, granteeSystemAccountId, '用户授权范围明细应保留用户 ID')
  assert.equal(userOverview.rows[0]?.userName, '授权范围窗口 grantee', '用户授权范围明细应通过 async lookup 装配用户名')
  assert.equal(userOverview.rows[0]?.resourceName, '授权范围窗口账号', '用户授权范围明细应通过 async lookup 装配资源名')
  assert.equal(userOverview.rows[0]?.usage.totalCost, 0.789, '用户授权范围明细应读取 totalCost')

  const skipped = await refreshUsageRankSnapshotsInStages({
    stageNames: ['authorization_usage_range_windows'],
    skipIfUnchanged: true,
    jobName,
    yieldToEventLoop: async () => {}
  })
  assert.equal(skipped.skipped, true, 'PG authorization range refresh 源水位不变时应跳过')

  console.log(JSON.stringify({
    message: '授权范围窗口 PG smoke 通过',
    teamRequestCount: teamOverview.summary.requestCount,
    userRequestCount: userOverview.summary.requestCount,
    teamRows: teamOverview.rows.length,
    userRows: userOverview.rows.length,
    skipped: skipped.skipped === true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.authorization_team_usage_range_windows WHERE system_account_id = $1', [ownerSystemAccountId])
  await pool.query('DELETE FROM juhe_stats.authorization_user_usage_range_windows WHERE system_account_id = $1', [ownerSystemAccountId])
  await pool.query('DELETE FROM juhe_stats.authorization_team_usage_summary_daily WHERE system_account_id = $1', [ownerSystemAccountId])
  await pool.query('DELETE FROM juhe_stats.authorization_user_usage_summary_daily WHERE system_account_id = $1', [ownerSystemAccountId])
  await pool.query('DELETE FROM juhe_stats.stats_job_state WHERE job_name = $1', [jobName])
  await pool.query('DELETE FROM juhe_business.accounts WHERE id = $1', [accountId])
  await pool.query('DELETE FROM juhe_business.system_team_members WHERE id = $1', [memberId])
  await pool.query('DELETE FROM juhe_business.system_teams WHERE id = $1', [teamId])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])', [[ownerSystemAccountId, granteeSystemAccountId]])
}
