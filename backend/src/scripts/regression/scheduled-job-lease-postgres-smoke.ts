import assert from 'node:assert/strict'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { applyPostgresSchema } from '../../storage/postgres-schema.js'
import { seedPostgresDefaults } from '../../storage/postgres-seed-defaults.js'
import {
  pinScheduledJobLeaseInTransaction,
  releaseScheduledJobLease,
  renewScheduledJobLease,
  ScheduledJobLeaseLostError,
  tryAcquireScheduledJobLease
} from '../../storage/scheduled-job-lease.repository.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '后台周期任务租约 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `scheduled_job_lease_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const leaseKey = `scheduled:smoke:${marker}`

try {
  const pool = await getPostgresPool()
  const client = createPostgresDatabaseClient(pool)
  if (process.env.JUHE_AI_SCHEDULED_JOB_LEASE_SMOKE_APPLY_SCHEMA === 'true') {
    await applyPostgresSchema(client)
    await seedPostgresDefaults(client)
  }
  await cleanupLease()

  const attempts = await Promise.all([
    tryAcquireScheduledJobLease({
      jobName: 'scheduled-job-lease-smoke',
      leaseKey,
      ownerId: `${marker}:owner-a`,
      ttlMs: 60_000
    }, client),
    tryAcquireScheduledJobLease({
      jobName: 'scheduled-job-lease-smoke',
      leaseKey,
      ownerId: `${marker}:owner-b`,
      ttlMs: 60_000
    }, client)
  ])
  const winners = attempts.filter((attempt) => attempt.acquired)
  assert.equal(winners.length, 1, '同一租约并发 claim 只能有一个持有者')
  const firstLease = assertAcquiredLease(winners[0])

  const renewed = await renewScheduledJobLease(firstLease, 60_000, client)
  assert.equal(renewed?.fencingToken, firstLease.fencingToken, '续租不得更换 fencing token')
  await client.transaction(async (tx) => pinScheduledJobLeaseInTransaction(tx, renewed ?? firstLease))

  await pool.query(`
    UPDATE juhe_stats.background_job_leases
    SET lease_until = '2000-01-01T00:00:00.000Z'
    WHERE lease_key = $1
      AND owner_id = $2
      AND fencing_token = $3
  `, [leaseKey, firstLease.ownerId, firstLease.fencingToken])

  const takeover = await tryAcquireScheduledJobLease({
    jobName: 'scheduled-job-lease-smoke',
    leaseKey,
    ownerId: `${marker}:takeover`,
    ttlMs: 60_000
  }, client)
  const takeoverLease = assertAcquiredLease(takeover)
  assert.equal(BigInt(takeoverLease.fencingToken), BigInt(firstLease.fencingToken) + 1n)
  assert.equal(await renewScheduledJobLease(firstLease, 60_000, client), undefined, '旧 token 不得续租新持有者的租约')
  assert.equal(await releaseScheduledJobLease(firstLease, client), false, '旧 token 不得释放新持有者的租约')
  await assert.rejects(
    client.transaction(async (tx) => pinScheduledJobLeaseInTransaction(tx, firstLease)),
    (error: unknown) => error instanceof ScheduledJobLeaseLostError
  )

  assert.equal(await releaseScheduledJobLease(takeoverLease, client), true)
  const reacquired = assertAcquiredLease(await tryAcquireScheduledJobLease({
    jobName: 'scheduled-job-lease-smoke',
    leaseKey,
    ownerId: `${marker}:after-release`,
    ttlMs: 60_000
  }, client))
  assert.equal(BigInt(reacquired.fencingToken), BigInt(takeoverLease.fencingToken) + 1n, 'release 后再次 claim 仍须递增 token')

  console.log(JSON.stringify({
    message: '后台周期任务 PG 租约 smoke 通过',
    firstToken: firstLease.fencingToken,
    takeoverToken: takeoverLease.fencingToken,
    reacquiredToken: reacquired.fencingToken
  }))
} finally {
  await cleanupLease().catch(() => undefined)
  await closeRedisClients()
  await closePostgresPool()
}

function assertAcquiredLease(
  result: Awaited<ReturnType<typeof tryAcquireScheduledJobLease>> | undefined
) {
  assert(result?.acquired, '预期成功取得租约')
  return result.lease
}

async function cleanupLease(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.background_job_leases WHERE lease_key = $1', [leaseKey])
}
