import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { refreshUsageQuotaHourlyWindowsCacheAsync } from '../../storage/usage-stats.repository.js'
import { hourKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', 'quota 增量 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `quota_window_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const scopeId = `key_${marker}`
const updatedAt = new Date().toISOString()

try {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const statHour = hourKey(new Date(), await usageStatsTimezoneAsync())
  await cleanupSmokeRows()
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_totals (
      system_account_id, scope_type, scope_id, total_cost_usd, updated_at
    ) VALUES (?, 'api_key', ?, 1.25, ?)
  `, [systemAccountId, scopeId, updatedAt])
  await client.execute(`
    INSERT INTO juhe_stats.usage_stats_hourly (
      system_account_id, scope_type, scope_id, stat_hour, total_cost_usd, updated_at
    ) VALUES (?, 'api_key', ?, ?, 1.25, ?)
  `, [systemAccountId, scopeId, statHour, updatedAt])
  await markDirty(client)

  const firstRefresh = await refreshUsageQuotaHourlyWindowsCacheAsync()
  assert.equal(firstRefresh.changed, true, 'dirty quota scope 应触发局部刷新')
  await assertWindowCost(client, 1, 1.25)

  await client.execute(`
    INSERT INTO juhe_business.request_quota_hourly_window_configs (window_hours, created_at, updated_at)
    VALUES (37, ?, ?)
    ON CONFLICT(window_hours) DO UPDATE SET updated_at = EXCLUDED.updated_at
  `, [updatedAt, updatedAt])
  const configRefresh = await refreshUsageQuotaHourlyWindowsCacheAsync()
  assert.equal(configRefresh.changed, true, '新增 quota 配置应通过 keyset seed 标脏已有 scope')
  await assertWindowCost(client, 37, 1.25)

  await client.execute(`
    UPDATE juhe_stats.usage_stats_hourly
    SET total_cost_usd = 0, updated_at = ?
    WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ? AND stat_hour = ?
  `, [new Date().toISOString(), systemAccountId, scopeId, statHour])
  await markDirty(client)
  const zeroRefresh = await refreshUsageQuotaHourlyWindowsCacheAsync()
  assert.equal(zeroRefresh.changed, true, '减账归零应刷新 quota scope')
  const remaining = await client.one<{ count: number | string }>(`
    SELECT COUNT(*) AS count
    FROM juhe_stats.usage_quota_hourly_windows
    WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?
  `, [systemAccountId, scopeId])
  assert.equal(Number(remaining?.count), 0, 'quota 归零后应删除局部快照，不得保留陈旧正值')

  console.log(JSON.stringify({
    message: 'quota 小时窗口 PG 增量 smoke 通过',
    configSeeded: true,
    zeroValueDeleted: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function markDirty(client: ReturnType<typeof createPostgresDatabaseClient>): Promise<void> {
  const timestamp = new Date().toISOString()
  await client.execute(`
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    ) VALUES (?, 'api_key', ?, 1, ?, ?)
    ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
      generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
      updated_at = EXCLUDED.updated_at
  `, [systemAccountId, scopeId, timestamp, timestamp])
}

async function assertWindowCost(
  client: ReturnType<typeof createPostgresDatabaseClient>,
  windowHours: number,
  expectedCost: number
): Promise<void> {
  const row = await client.one<{ total_cost_usd: number | string }>(`
    SELECT total_cost_usd
    FROM juhe_stats.usage_quota_hourly_windows
    WHERE system_account_id = ?
      AND scope_type = 'api_key'
      AND scope_id = ?
      AND window_hours = ?
  `, [systemAccountId, scopeId, windowHours])
  assert.equal(Number(row?.total_cost_usd), expectedCost, `${windowHours} 小时 quota 成本不正确`)
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_quota_hourly_windows WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_quota_hourly_window_dirty_scopes WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_hourly WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_stats_totals WHERE system_account_id = $1', [systemAccountId])
}
