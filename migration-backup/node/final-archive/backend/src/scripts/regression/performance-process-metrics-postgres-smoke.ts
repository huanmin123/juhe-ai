import assert from 'node:assert/strict'
import { performance } from 'node:perf_hooks'

import { runtimeConfig } from '../../config/runtime.js'
import type { ProcessEventLoopRole } from '../../shared/process-event-loop-monitor.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { insertSystemMetricsSampleBatchAsync } from '../../storage/system-metrics.repository.js'
import { hourKey, usageStatsTimezoneAsync } from '../../storage/usage-stats-helpers.js'

assert.equal(process.env.JUHE_AI_ALLOW_PERFORMANCE_PROCESS_METRICS_POSTGRES_SMOKE, '1')
assert.equal(runtimeConfig.databaseDriver, 'postgres')
assert.ok(runtimeConfig.postgres.url)
assert.equal(new URL(runtimeConfig.postgres.url).hostname, '192.168.1.203', '只允许在授权测试 PostgreSQL 执行')

const roles = performanceRoles(32, 32, 32)
assert.equal(roles.length, 132)
const pool = await getPostgresPool()
let sampledAt = ''
let statHour = ''

try {
  const timezone = await usageStatsTimezoneAsync()
  const candidate = await findUnusedFutureHour(timezone)
  sampledAt = candidate.sampledAt
  statHour = candidate.statHour
  const startedAtMs = performance.now()
  await insertSystemMetricsSampleBatchAsync({
    sampledAt,
    cpuPercent: 1,
    memoryUsedPercent: 1
  }, roles.map((processRole, index) => ({
    sampledAt,
    processRole,
    processPid: 120_000 + index,
    eventLoopLagMs: index,
    processRssBytes: 100_000_000 + index
  })))
  const elapsedMs = performance.now() - startedAtMs

  const detailResult = await pool.query(`
    SELECT process_role
    FROM juhe_stats.process_event_loop_samples
    WHERE sampled_at = $1
    ORDER BY process_role ASC
  `, [sampledAt])
  const hourlyResult = await pool.query(`
    SELECT process_role
    FROM juhe_stats.process_event_loop_hourly
    WHERE stat_hour = $1
    ORDER BY process_role ASC
  `, [statHour])
  assert.deepEqual(new Set(detailResult.rows.map((row) => String(row.process_role))), new Set(roles))
  assert.deepEqual(new Set(hourlyResult.rows.map((row) => String(row.process_role))), new Set(roles))
  assert.ok(elapsedMs < 20_000, `132 角色单事务写入 ${elapsedMs.toFixed(1)}ms，超过 20 秒采样任务边界`)

  console.log(JSON.stringify({
    event: 'performance_process_metrics_postgres_smoke_passed',
    processCount: roles.length,
    statementCount: 266,
    elapsedMs: Number(elapsedMs.toFixed(1)),
    detailsPersisted: detailResult.rowCount,
    hourlyRolesPersisted: hourlyResult.rowCount
  }))
} finally {
  if (sampledAt) {
    await pool.query('DELETE FROM juhe_stats.system_metrics_samples WHERE sampled_at = $1', [sampledAt]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_stats.process_event_loop_samples WHERE sampled_at = $1', [sampledAt]).catch(() => undefined)
  }
  if (statHour) {
    await pool.query('DELETE FROM juhe_stats.system_metrics_hourly WHERE stat_hour = $1', [statHour]).catch(() => undefined)
    await pool.query('DELETE FROM juhe_stats.process_event_loop_hourly WHERE stat_hour = $1', [statHour]).catch(() => undefined)
  }
  await closePostgresPool()
}

async function findUnusedFutureHour(timezone: string): Promise<{ sampledAt: string; statHour: string }> {
  for (let offset = 0; offset < 240; offset += 1) {
    const candidateDate = new Date(Date.UTC(2080, 0, 1 + offset, offset % 24, 0, 0, offset))
    const candidateSampledAt = candidateDate.toISOString()
    const candidateStatHour = hourKey(candidateDate, timezone)
    const existing = await pool.query(`
      SELECT 1 FROM juhe_stats.system_metrics_hourly WHERE stat_hour = $1
      UNION ALL
      SELECT 1 FROM juhe_stats.process_event_loop_hourly WHERE stat_hour = $1
      LIMIT 1
    `, [candidateStatHour])
    if (existing.rowCount === 0) return { sampledAt: candidateSampledAt, statHour: candidateStatHour }
  }
  throw new Error('没有找到隔离的未来系统指标小时桶')
}

function performanceRoles(gatewayReplicas: number, usageReplicas: number, logReplicas: number): ProcessEventLoopRole[] {
  const roles: ProcessEventLoopRole[] = ['control:control-1', 'db-service:control-1']
  for (let replica = 1; replica <= gatewayReplicas; replica += 1) {
    roles.push(`gateway:gateway-${replica}`, `db-service:gateway-${replica}`)
  }
  for (let replica = 1; replica <= usageReplicas; replica += 1) roles.push(`usage-worker:${replica}`)
  for (let replica = 1; replica <= logReplicas; replica += 1) roles.push(`log-worker:${replica}`)
  roles.push('stats-worker:1', 'ops-worker:1')
  return roles
}
