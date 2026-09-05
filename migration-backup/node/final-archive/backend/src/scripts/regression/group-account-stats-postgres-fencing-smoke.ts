import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { refreshDirtyGroupAccountStatsCacheAsync } from '../../storage/group-account-stats-cache.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { applyPostgresSchema } from '../../storage/postgres-schema.js'
import { seedPostgresDefaults } from '../../storage/postgres-seed-defaults.js'
import {
  releaseScheduledJobLease,
  ScheduledJobLeaseLostError,
  tryAcquireScheduledJobLease
} from '../../storage/scheduled-job-lease.repository.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '分组账户统计 fencing smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `group_account_stats_fencing_${Date.now()}_${Math.random().toString(16).slice(2)}`
const leaseKey = `scheduled:group-account-stats-refresh:${marker}`
const groupId = `${marker}:group`
const systemAccountId = `${marker}:system`

try {
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)
  if (process.env.JUHE_AI_GROUP_ACCOUNT_STATS_FENCING_SMOKE_APPLY_SCHEMA === 'true') {
    await applyPostgresSchema(client)
    await seedPostgresDefaults(client)
  }
  await cleanupFixture()

  await pool.query(`
    INSERT INTO juhe_business.group_account_stats_dirty (group_id, reason, updated_at)
    VALUES ($1, 'fencing_smoke', '2026-07-26T00:00:00.000Z')
  `, [groupId])
  await pool.query(`
    INSERT INTO juhe_stats.group_account_stats (
      system_account_id, group_id, total, available, active, disabled, error,
      rate_limited, current_concurrency, concurrency_limit, updated_at
    ) VALUES ($1, $2, 73, 73, 73, 0, 0, 0, 0, 73, '2026-07-26T00:00:00.000Z')
  `, [systemAccountId, groupId])

  const firstLease = assertAcquiredLease(await tryAcquireScheduledJobLease({
    jobName: 'group-account-stats-refresh',
    leaseKey,
    ownerId: `${marker}:old-owner`,
    ttlMs: 60_000
  }, client))
  await pool.query(`
    UPDATE juhe_stats.background_job_leases
    SET lease_until = '2000-01-01T00:00:00.000Z'
    WHERE lease_key = $1
      AND owner_id = $2
      AND fencing_token = $3
  `, [leaseKey, firstLease.ownerId, firstLease.fencingToken])

  const takeoverLease = assertAcquiredLease(await tryAcquireScheduledJobLease({
    jobName: 'group-account-stats-refresh',
    leaseKey,
    ownerId: `${marker}:new-owner`,
    ttlMs: 60_000
  }, client))

  await assert.rejects(
    refreshDirtyGroupAccountStatsCacheAsync(1000, firstLease),
    (error: unknown) => error instanceof ScheduledJobLeaseLostError,
    '旧 owner 的 fencing token 必须在缓存写入前被拒绝'
  )
  assert.equal(await readCachedTotal(), 73, '旧 owner 被 fence 后不得删除或覆盖新缓存值')
  assert.equal(await dirtyRowExists(), true, '旧 owner 被 fence 后不得误删 dirty 行')

  assert.equal(await refreshDirtyGroupAccountStatsCacheAsync(1000, takeoverLease), 1, '新 owner 应能消费同一 dirty 行')
  assert.equal(await readCachedTotal(), undefined, '当前业务分组不存在时，新 owner 应按权威来源清理旧缓存')
  assert.equal(await dirtyRowExists(), false, '新 owner 提交缓存结果后应 CAS 清理 dirty 行')
  assert.equal(await releaseScheduledJobLease(takeoverLease, client), true)

  console.log(JSON.stringify({
    message: '分组账户统计 PostgreSQL fencing smoke 通过',
    oldToken: firstLease.fencingToken,
    takeoverToken: takeoverLease.fencingToken
  }))
} finally {
  await cleanupFixture().catch(() => undefined)
  await closeRedisClients()
  await closePostgresPool()
}

function assertAcquiredLease(result: Awaited<ReturnType<typeof tryAcquireScheduledJobLease>>) {
  assert(result.acquired, '预期成功取得分组账户统计租约')
  return result.lease
}

async function readCachedTotal(): Promise<number | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT total
    FROM juhe_stats.group_account_stats
    WHERE group_id = $1
    LIMIT 1
  `, [groupId])
  return result.rows[0] ? Number(result.rows[0].total) : undefined
}

async function dirtyRowExists(): Promise<boolean> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT 1
    FROM juhe_business.group_account_stats_dirty
    WHERE group_id = $1
    LIMIT 1
  `, [groupId])
  return result.rowCount === 1
}

async function cleanupFixture(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_business.group_account_stats_dirty WHERE group_id = $1', [groupId])
  await pool.query('DELETE FROM juhe_stats.group_account_stats WHERE group_id = $1', [groupId])
  await pool.query('DELETE FROM juhe_stats.background_job_leases WHERE lease_key = $1', [leaseKey])
}
