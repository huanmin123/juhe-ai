import { strict as assert } from 'node:assert'
import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool, type PostgresQueryResult } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '派生窗口 dirty CAS PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `usage_dirty_cas_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const systemAccountId = `sys_${marker}`
const scopeId = `scope_${marker}`
const initialUpdatedAt = '2000-01-01T00:00:00.000Z'
const concurrentUpdatedAt = '2000-01-01T00:00:01.000Z'

try {
  await cleanupSmokeRows()
  await assertOverviewDirtyMarkerSurvivesInterleaving()
  await assertAiPerformanceDirtyMarkerSurvivesInterleaving()
  await assertQuotaDirtyMarkerSurvivesInterleaving()

  console.log(JSON.stringify({
    message: '派生窗口 dirty generation CAS PG smoke 通过',
    tables: [
      'usage_overview_dirty_scopes',
      'ai_performance_summary_dirty_system_accounts',
      'usage_quota_hourly_window_dirty_scopes'
    ]
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function assertOverviewDirtyMarkerSurvivesInterleaving(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_stats.usage_overview_dirty_scopes (
      system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at
    ) VALUES ($1, $2, '2026-07-26', 1, $3, $3)
  `, [systemAccountId, scopeId, initialUpdatedAt])

  await assertLockedClaimDoesNotLoseConcurrentMarker({
    label: 'overview',
    claimSql: `
      SELECT generation
      FROM juhe_stats.usage_overview_dirty_scopes
      WHERE system_account_id = $1
      FOR UPDATE
    `,
    claimParams: [systemAccountId],
    markSql: `
      INSERT INTO juhe_stats.usage_overview_dirty_scopes (
        system_account_id, scope_id, min_changed_date, generation, first_dirty_at, updated_at
      ) VALUES ($1, $2, '2026-07-25', 1, $3, $3)
      ON CONFLICT(system_account_id) DO UPDATE SET
        scope_id = EXCLUDED.scope_id,
        min_changed_date = LEAST(usage_overview_dirty_scopes.min_changed_date, EXCLUDED.min_changed_date),
        generation = usage_overview_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `,
    markParams: [systemAccountId, scopeId, concurrentUpdatedAt],
    deleteSql: `
      DELETE FROM juhe_stats.usage_overview_dirty_scopes
      WHERE system_account_id = $1 AND generation = $2
    `,
    deleteKeyParams: [systemAccountId]
  })

  const preserved = await pool.query(`
    SELECT min_changed_date, updated_at
    FROM juhe_stats.usage_overview_dirty_scopes
    WHERE system_account_id = $1
  `, [systemAccountId])
  const row = preserved.rows[0] as { min_changed_date?: string; updated_at?: string } | undefined
  assert.equal(row?.min_changed_date, '2026-07-25', 'overview 并发 marker 的最早变更日期必须保留')
  assert.equal(row?.updated_at, concurrentUpdatedAt, 'overview 并发 marker 必须是锁释放后的新写入')
}

async function assertAiPerformanceDirtyMarkerSurvivesInterleaving(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_stats.ai_performance_summary_dirty_system_accounts (
      system_account_id, min_stat_date, max_stat_date, generation, first_dirty_at, updated_at
    ) VALUES ($1, '2026-07-26', '2026-07-26', 1, $2, $2)
  `, [systemAccountId, initialUpdatedAt])

  await assertLockedClaimDoesNotLoseConcurrentMarker({
    label: 'AI performance',
    claimSql: `
      SELECT generation
      FROM juhe_stats.ai_performance_summary_dirty_system_accounts
      WHERE system_account_id = $1
      FOR UPDATE
    `,
    claimParams: [systemAccountId],
    markSql: `
      INSERT INTO juhe_stats.ai_performance_summary_dirty_system_accounts (
        system_account_id, min_stat_date, max_stat_date, generation, first_dirty_at, updated_at
      ) VALUES ($1, '2026-07-25', '2026-07-27', 1, $2, $2)
      ON CONFLICT(system_account_id) DO UPDATE SET
        min_stat_date = LEAST(ai_performance_summary_dirty_system_accounts.min_stat_date, EXCLUDED.min_stat_date),
        max_stat_date = GREATEST(ai_performance_summary_dirty_system_accounts.max_stat_date, EXCLUDED.max_stat_date),
        generation = ai_performance_summary_dirty_system_accounts.generation + 1,
        updated_at = EXCLUDED.updated_at
    `,
    markParams: [systemAccountId, concurrentUpdatedAt],
    deleteSql: `
      DELETE FROM juhe_stats.ai_performance_summary_dirty_system_accounts
      WHERE system_account_id = $1 AND generation = $2
    `,
    deleteKeyParams: [systemAccountId]
  })

  const preserved = await pool.query(`
    SELECT min_stat_date, max_stat_date, updated_at
    FROM juhe_stats.ai_performance_summary_dirty_system_accounts
    WHERE system_account_id = $1
  `, [systemAccountId])
  const row = preserved.rows[0] as { min_stat_date?: string; max_stat_date?: string; updated_at?: string } | undefined
  assert.equal(row?.min_stat_date, '2026-07-25', 'AI performance 并发 marker 的最早日期必须保留')
  assert.equal(row?.max_stat_date, '2026-07-27', 'AI performance 并发 marker 的最晚日期必须保留')
  assert.equal(row?.updated_at, concurrentUpdatedAt, 'AI performance 并发 marker 必须是锁释放后的新写入')
}

async function assertQuotaDirtyMarkerSurvivesInterleaving(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query(`
    INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
      system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
    ) VALUES ($1, 'api_key', $2, 1, $3, $3)
  `, [systemAccountId, scopeId, initialUpdatedAt])

  await assertLockedClaimDoesNotLoseConcurrentMarker({
    label: 'quota',
    claimSql: `
      SELECT generation
      FROM juhe_stats.usage_quota_hourly_window_dirty_scopes
      WHERE system_account_id = $1 AND scope_type = 'api_key' AND scope_id = $2
      FOR UPDATE
    `,
    claimParams: [systemAccountId, scopeId],
    markSql: `
      INSERT INTO juhe_stats.usage_quota_hourly_window_dirty_scopes (
        system_account_id, scope_type, scope_id, generation, first_dirty_at, updated_at
      ) VALUES ($1, 'api_key', $2, 1, $3, $3)
      ON CONFLICT(system_account_id, scope_type, scope_id) DO UPDATE SET
        generation = usage_quota_hourly_window_dirty_scopes.generation + 1,
        updated_at = EXCLUDED.updated_at
    `,
    markParams: [systemAccountId, scopeId, concurrentUpdatedAt],
    deleteSql: `
      DELETE FROM juhe_stats.usage_quota_hourly_window_dirty_scopes
      WHERE system_account_id = $1 AND scope_type = 'api_key' AND scope_id = $2 AND generation = $3
    `,
    deleteKeyParams: [systemAccountId, scopeId]
  })

  const preserved = await pool.query(`
    SELECT updated_at
    FROM juhe_stats.usage_quota_hourly_window_dirty_scopes
    WHERE system_account_id = $1 AND scope_type = 'api_key' AND scope_id = $2
  `, [systemAccountId, scopeId])
  const row = preserved.rows[0] as { updated_at?: string } | undefined
  assert.equal(row?.updated_at, concurrentUpdatedAt, 'quota 并发 marker 必须是锁释放后的新写入')
}

interface LockedClaimInterleavingInput {
  label: string
  claimSql: string
  claimParams: unknown[]
  markSql: string
  markParams: unknown[]
  deleteSql: string
  deleteKeyParams: unknown[]
}

async function assertLockedClaimDoesNotLoseConcurrentMarker(input: LockedClaimInterleavingInput): Promise<void> {
  const pool = await getPostgresPool()
  const consumer = await pool.connect()
  const markerWriter = await pool.connect()
  let transactionOpen = false
  let markerWrite: Promise<PostgresQueryResult> | undefined
  try {
    await consumer.query('BEGIN')
    transactionOpen = true
    const claimed = await consumer.query(input.claimSql, input.claimParams)
    assert.equal(claimed.rows.length, 1, `${input.label} consumer 必须锁定一个 dirty marker`)
    const claimedGeneration = (claimed.rows[0] as { generation: string | number }).generation

    let markerSettled = false
    const pendingMarkerWrite = markerWriter.query(input.markSql, input.markParams)
    markerWrite = pendingMarkerWrite
    void pendingMarkerWrite.then(() => {
      markerSettled = true
    }, () => {
      markerSettled = true
    })
    await delay(40)
    assert.equal(markerSettled, false, `${input.label} 新 marker 写入必须等待 consumer 行锁释放`)

    const deleted = await consumer.query(input.deleteSql, [...input.deleteKeyParams, claimedGeneration])
    assert.equal(deleted.rowCount, 1, `${input.label} consumer 应只删除已 claim 的 generation`)
    await consumer.query('COMMIT')
    transactionOpen = false
    await pendingMarkerWrite
  } finally {
    if (transactionOpen) {
      await consumer.query('ROLLBACK').catch(() => undefined)
    }
    await markerWrite?.catch(() => undefined)
    consumer.release()
    markerWriter.release()
  }
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_stats.usage_overview_dirty_scopes WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.ai_performance_summary_dirty_system_accounts WHERE system_account_id = $1', [systemAccountId])
  await pool.query('DELETE FROM juhe_stats.usage_quota_hourly_window_dirty_scopes WHERE system_account_id = $1', [systemAccountId])
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
