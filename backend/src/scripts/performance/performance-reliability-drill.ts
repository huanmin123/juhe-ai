import { randomUUID } from 'node:crypto'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'
import { spawn } from 'node:child_process'

import type { Client as PgClient } from 'pg'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import { createDedicatedRedisClient, type RedisCommandClient } from '../../shared/redis-client.js'
import { RedisStreamQueue } from '../../shared/redis-stream-queue.js'

type DrillStatus = 'passed' | 'failed' | 'skipped'

interface DrillCheck {
  name: string
  status: DrillStatus
  latencyMs: number
  category?: string
  message?: string
  details?: Record<string, unknown>
}

interface ReliabilityDrillReport {
  mode: {
    runtimeMode: string
    databaseDriver: string
    cacheDriver: string
    runtimeStateDriver: string
    queueDriver: string
  }
  runId: string
  startedAt: string
  finishedAt: string
  durationMs: number
  reportPath: string
  checks: DrillCheck[]
  pass: boolean
}

interface PendingSummary {
  pending: number
  minId?: string
  maxId?: string
  consumers: Array<{ name: string; pending: number }>
}

interface DrillPayload {
  runId: string
  index: number
}

class DrillSkippedError extends Error {
  readonly category: string
  readonly details?: Record<string, unknown>

  constructor(category: string, message: string, details?: Record<string, unknown>) {
    super(message)
    this.name = 'DrillSkippedError'
    this.category = category
    this.details = details
  }
}

const postgresSchemas = [
  'juhe_business',
  'juhe_dataset',
  'juhe_usage',
  'juhe_stats',
  'juhe_codex_context'
]

const reportPath = process.env.JUHE_PERFORMANCE_RELIABILITY_DRILL_REPORT
  || resolve(backendRoot, '..', 'reports', `performance-reliability-drill-${timestampForFile(new Date())}.json`)
const backupDirectory = process.env.JUHE_PERFORMANCE_RELIABILITY_BACKUP_DIR
  || resolve(backendRoot, '..', 'reports', 'backups')
const runId = `reliability_${Date.now()}_${randomUUID().slice(0, 8)}`

let exitCode = 0

try {
  assertPerformanceRuntime()
  const report = await runReliabilityDrill()
  writeReport(report)
  printReport(report)
  if (!report.pass) {
    exitCode = 1
  }
} catch (error) {
  exitCode = 1
  console.error(error instanceof Error ? error.stack ?? error.message : error)
} finally {
  await closePostgresPool().catch(() => undefined)
}

process.exit(exitCode)

async function runReliabilityDrill(): Promise<ReliabilityDrillReport> {
  const startedAt = new Date()
  const startedAtMs = performance.now()
  const checks: DrillCheck[] = []

  checks.push(await runCheck('postgres_health', postgresHealthCheck))
  checks.push(await runCheck('postgres_blocking_snapshot', postgresBlockingSnapshotCheck))
  checks.push(await runCheck('redis_queue_health', redisQueueHealthCheck))
  checks.push(await runCheck('redis_usage_stream_snapshot', redisUsageStreamSnapshotCheck))
  checks.push(await runCheck('redis_stream_pending_reclaim', redisStreamPendingReclaimCheck))
  checks.push(await runCheck('postgres_backup_restore', postgresBackupRestoreCheck))
  checks.push(await optionalRedisRestartRecoveryCheck())
  checks.push(await optionalCommandRecoveryCheck(
    'postgres_restart_recovery',
    'JUHE_AI_RELIABILITY_POSTGRES_RESTART_COMMAND',
    waitForPostgresHealth
  ))

  const finishedAt = new Date()
  return {
    mode: {
      runtimeMode: runtimeConfig.runtimeMode,
      databaseDriver: runtimeConfig.databaseDriver,
      cacheDriver: runtimeConfig.cacheDriver,
      runtimeStateDriver: runtimeConfig.runtimeStateDriver,
      queueDriver: runtimeConfig.queueDriver
    },
    runId,
    startedAt: startedAt.toISOString(),
    finishedAt: finishedAt.toISOString(),
    durationMs: performance.now() - startedAtMs,
    reportPath,
    checks,
    pass: checks.every((check) => check.status !== 'failed')
  }
}

async function runCheck(name: string, operation: () => Promise<Record<string, unknown> | void>): Promise<DrillCheck> {
  const startedAt = performance.now()
  try {
    const details = await operation()
    return {
      name,
      status: 'passed',
      latencyMs: performance.now() - startedAt,
      details: details ?? undefined
    }
  } catch (error) {
    if (error instanceof DrillSkippedError) {
      return {
        name,
        status: 'skipped',
        latencyMs: performance.now() - startedAt,
        category: error.category,
        message: error.message,
        details: error.details
      }
    }
    return {
      name,
      status: 'failed',
      latencyMs: performance.now() - startedAt,
      category: classifyReliabilityError(error),
      message: error instanceof Error ? error.message : String(error)
    }
  }
}

async function postgresHealthCheck(): Promise<Record<string, unknown>> {
  const client = await createPgClient(requiredPostgresUrl(), 'juhe-ai-reliability-health')
  try {
    const result = await client.query('SELECT version() AS version, current_database() AS database_name')
    return result.rows[0] ?? {}
  } finally {
    await client.end()
  }
}

async function postgresBlockingSnapshotCheck(): Promise<Record<string, unknown>> {
  const client = await createPgClient(requiredPostgresUrl(), 'juhe-ai-reliability-blocking')
  try {
    const result = await client.query(`
      SELECT
        (SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()) AS deadlocks,
        (SELECT COUNT(*) FROM pg_locks WHERE NOT granted) AS lock_waiters,
        (
          SELECT COALESCE(MAX(EXTRACT(EPOCH FROM now() - xact_start)), 0)
          FROM pg_stat_activity
          WHERE datname = current_database()
            AND xact_start IS NOT NULL
        ) AS max_xact_seconds,
        (
          SELECT COALESCE(MAX(EXTRACT(EPOCH FROM now() - query_start)), 0)
          FROM pg_stat_activity
          WHERE datname = current_database()
            AND state = 'active'
            AND query_start IS NOT NULL
            AND pid <> pg_backend_pid()
        ) AS max_active_query_seconds,
        (
          SELECT COUNT(*)
          FROM pg_stat_activity
          WHERE datname = current_database()
            AND state = 'idle in transaction'
        ) AS idle_in_transaction
    `)
    return normalizePgRowNumbers(result.rows[0] ?? {})
  } finally {
    await client.end()
  }
}

async function redisQueueHealthCheck(): Promise<Record<string, unknown>> {
  const client = await createDedicatedRedisClient(requiredRedisQueueUrl())
  try {
    const ping = await client.sendCommand(['PING'])
    return {
      ping: String(ping ?? '')
    }
  } finally {
    await closeRedisClient(client)
  }
}

async function redisUsageStreamSnapshotCheck(): Promise<Record<string, unknown>> {
  const client = await createDedicatedRedisClient(requiredRedisQueueUrl())
  try {
    const streamKey = 'juhe-ai:queue:usage-records'
    const groupName = 'juhe-ai:usage-record-writers'
    const length = await client.sendCommand(['XLEN', streamKey]).catch((error) => {
      return { error: error instanceof Error ? error.message : String(error) }
    })
    const pending = await client.sendCommand(['XPENDING', streamKey, groupName]).catch((error) => {
      return { error: error instanceof Error ? error.message : String(error) }
    })
    return {
      streamKey,
      groupName,
      length: normalizeRedisIntegerOrError(length),
      pending: Array.isArray(pending) || isPlainObject(pending) ? parsePendingSummary(pending) : normalizeRedisIntegerOrError(pending)
    }
  } finally {
    await closeRedisClient(client)
  }
}

async function redisStreamPendingReclaimCheck(): Promise<Record<string, unknown>> {
  const streamKey = `juhe-ai:drill:stream:${runId}`
  const groupName = `juhe-ai:drill:group:${runId}`
  const deadConsumerName = `dead:${runId}`
  const liveConsumerName = `live:${runId}`
  const redisUrl = requiredRedisQueueUrl()
  const deadQueue = new RedisStreamQueue<DrillPayload>({
    streamKey,
    groupName,
    consumerName: deadConsumerName,
    redisUrl,
    claimIdleMs: 1,
    readCount: 10,
    blockMs: 20
  })
  const liveQueue = new RedisStreamQueue<DrillPayload>({
    streamKey,
    groupName,
    consumerName: liveConsumerName,
    redisUrl,
    claimIdleMs: 1,
    readCount: 10,
    blockMs: 20
  })
  const client = await createDedicatedRedisClient(redisUrl)
  try {
    await deadQueue.claimPending()
    const enqueuedIds: string[] = []
    for (let index = 0; index < 3; index += 1) {
      enqueuedIds.push(await deadQueue.enqueue({ runId, index }))
    }

    const readByDeadConsumer = await deadQueue.readNew()
    if (readByDeadConsumer.length !== enqueuedIds.length) {
      throw new Error(`dead consumer 读取数量异常：expected=${enqueuedIds.length}, actual=${readByDeadConsumer.length}`)
    }
    const pendingBeforeClaim = parsePendingSummary(await client.sendCommand(['XPENDING', streamKey, groupName]))
    await delay(10)
    const claimedByLiveConsumer = await liveQueue.claimPending()
    if (claimedByLiveConsumer.length !== enqueuedIds.length) {
      throw new Error(`live consumer 接管数量异常：expected=${enqueuedIds.length}, actual=${claimedByLiveConsumer.length}`)
    }
    const pendingAfterClaim = parsePendingSummary(await client.sendCommand(['XPENDING', streamKey, groupName]))
    const acked = await liveQueue.ack(claimedByLiveConsumer.map((message) => message.id))
    if (acked !== enqueuedIds.length) {
      throw new Error(`ACK 数量异常：expected=${enqueuedIds.length}, actual=${acked}`)
    }
    const pendingAfterAck = parsePendingSummary(await client.sendCommand(['XPENDING', streamKey, groupName]))
    if (pendingAfterAck.pending !== 0) {
      throw new Error(`ACK 后 pending 未清空：${pendingAfterAck.pending}`)
    }
    return {
      streamKey,
      groupName,
      enqueued: enqueuedIds.length,
      pendingBeforeClaim,
      pendingAfterClaim,
      pendingAfterAck
    }
  } finally {
    await Promise.all([
      deadQueue.closeConsumer().catch(() => undefined),
      liveQueue.closeConsumer().catch(() => undefined)
    ])
    await client.sendCommand(['DEL', streamKey]).catch(() => undefined)
    await closeRedisClient(client)
  }
}

async function postgresBackupRestoreCheck(): Promise<Record<string, unknown>> {
  if (process.env.JUHE_AI_RELIABILITY_SKIP_PG_BACKUP_RESTORE === 'true') {
    throw new DrillSkippedError('postgres_backup_restore_skipped_by_env', 'JUHE_AI_RELIABILITY_SKIP_PG_BACKUP_RESTORE=true，跳过备份恢复演练', {
      skippedByEnv: true
    })
  }
  if (process.env.JUHE_AI_RELIABILITY_PG_BACKUP_RESTORE_MODE === 'remote_docker') {
    return postgresRemoteDockerBackupRestoreCheck()
  }

  mkdirSync(backupDirectory, { recursive: true })
  const backupPath = resolve(backupDirectory, `juhe-ai-${runId}.dump`)
  const sourceUrl = requiredPostgresUrl()
  const adminUrl = process.env.JUHE_AI_RELIABILITY_PG_ADMIN_URL || adminDatabaseUrl(sourceUrl)
  const restoreDatabaseName = safeDatabaseName(process.env.JUHE_AI_RELIABILITY_PG_RESTORE_DATABASE || `juhe_ai_restore_${runId}`)
  const restoreUrl = databaseUrlWithName(sourceUrl, restoreDatabaseName)
  const sourceCounts = await readPostgresSchemaTableCounts(sourceUrl)

  await runExecutable('pg_dump', [
    '--format=custom',
    '--no-owner',
    '--no-privileges',
    '--file',
    backupPath,
    sourceUrl
  ], 120_000)

  const adminClient = await createPgClient(adminUrl, 'juhe-ai-reliability-restore-admin')
  try {
    await dropDatabaseIfExists(adminClient, restoreDatabaseName)
    await adminClient.query(`CREATE DATABASE ${quoteIdentifier(restoreDatabaseName)} WITH TEMPLATE template0 ENCODING 'UTF8'`)
  } finally {
    await adminClient.end()
  }

  try {
    await runExecutable('pg_restore', [
      '--no-owner',
      '--no-privileges',
      '--dbname',
      restoreUrl,
      backupPath
    ], 120_000)
    const restoreCounts = await readPostgresSchemaTableCounts(restoreUrl)
    assertRestoredSchemaCounts(sourceCounts, restoreCounts)
    return {
      backupPath,
      restoreDatabaseName,
      sourceCounts,
      restoreCounts,
      backupSizeBytes: existsSync(backupPath) ? (await import('node:fs')).statSync(backupPath).size : 0
    }
  } finally {
    const cleanupClient = await createPgClient(adminUrl, 'juhe-ai-reliability-restore-cleanup')
    try {
      await dropDatabaseIfExists(cleanupClient, restoreDatabaseName)
    } finally {
      await cleanupClient.end()
    }
  }
}

async function postgresRemoteDockerBackupRestoreCheck(): Promise<Record<string, unknown>> {
  const sshTarget = requiredEnv('JUHE_AI_RELIABILITY_REMOTE_SSH_TARGET')
  const remoteDirectory = process.env.JUHE_AI_RELIABILITY_REMOTE_DOCKER_DIR || '/home/huanmin/juhe-ai-performance'
  const container = process.env.JUHE_AI_RELIABILITY_POSTGRES_CONTAINER || 'juhe-ai-postgres'
  const database = process.env.JUHE_AI_RELIABILITY_POSTGRES_DB || 'juhe_ai'
  const user = process.env.JUHE_AI_RELIABILITY_POSTGRES_USER || 'juhe_ai'
  const restoreDatabaseName = safeDatabaseName(process.env.JUHE_AI_RELIABILITY_PG_RESTORE_DATABASE || `juhe_ai_restore_${runId}`)
  const remoteBackupPath = `/tmp/juhe-ai-${runId}.dump`
  const schemaSqlList = postgresSchemas.map((schema) => `'${schema}'`).join(',')
  const remoteScript = `
set -euo pipefail
cd ${shellSingleQuote(remoteDirectory)}
container=${shellSingleQuote(container)}
database=${shellSingleQuote(database)}
user=${shellSingleQuote(user)}
restore_database=${shellSingleQuote(restoreDatabaseName)}
backup_path=${shellSingleQuote(remoteBackupPath)}
cleanup() {
  docker exec "$container" psql -v ON_ERROR_STOP=1 -U "$user" -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$restore_database' AND pid <> pg_backend_pid();" >/dev/null 2>&1 || true
  docker exec "$container" psql -v ON_ERROR_STOP=1 -U "$user" -d postgres -c "DROP DATABASE IF EXISTS \\"$restore_database\\";" >/dev/null 2>&1 || true
  rm -f "$backup_path"
}
trap cleanup EXIT
docker exec "$container" pg_dump -U "$user" -d "$database" --format=custom --no-owner --no-privileges > "$backup_path"
backup_size=$(stat -c%s "$backup_path")
docker exec "$container" psql -v ON_ERROR_STOP=1 -U "$user" -d postgres -c "DROP DATABASE IF EXISTS \\"$restore_database\\";"
docker exec "$container" psql -v ON_ERROR_STOP=1 -U "$user" -d postgres -c "CREATE DATABASE \\"$restore_database\\" WITH TEMPLATE template0 ENCODING 'UTF8';"
cat "$backup_path" | docker exec -i "$container" pg_restore -U "$user" -d "$restore_database" --no-owner --no-privileges
echo "__JUHE_BACKUP_PATH__=$backup_path"
echo "__JUHE_RESTORE_DATABASE__=$restore_database"
echo "__JUHE_BACKUP_SIZE__=$backup_size"
echo "__JUHE_SOURCE_COUNTS__"
docker exec "$container" psql -At -U "$user" -d "$database" -c "SELECT table_schema || '=' || COUNT(*)::int FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND table_schema IN (${schemaSqlList}) GROUP BY table_schema ORDER BY table_schema;"
echo "__JUHE_RESTORE_COUNTS__"
docker exec "$container" psql -At -U "$user" -d "$restore_database" -c "SELECT table_schema || '=' || COUNT(*)::int FROM information_schema.tables WHERE table_type = 'BASE TABLE' AND table_schema IN (${schemaSqlList}) GROUP BY table_schema ORDER BY table_schema;"
`
  const output = await runExecutableCapture('ssh', [
    sshTarget,
    `bash -lc ${shellSingleQuote(remoteScript)}`
  ], 180_000)
  const sourceCounts = parseMarkedCounts(output, '__JUHE_SOURCE_COUNTS__')
  const restoreCounts = parseMarkedCounts(output, '__JUHE_RESTORE_COUNTS__')
  assertRestoredSchemaCounts(sourceCounts, restoreCounts)
  return {
    mode: 'remote_docker',
    sshTarget,
    remoteDirectory,
    container,
    database,
    user,
    remoteBackupPath: parseMarkedValue(output, '__JUHE_BACKUP_PATH__') || remoteBackupPath,
    restoreDatabaseName: parseMarkedValue(output, '__JUHE_RESTORE_DATABASE__') || restoreDatabaseName,
    backupSizeBytes: Number(parseMarkedValue(output, '__JUHE_BACKUP_SIZE__') || 0),
    sourceCounts,
    restoreCounts
  }
}

async function optionalCommandRecoveryCheck(
  name: string,
  envName: string,
  healthProbe: () => Promise<Record<string, unknown>>
): Promise<DrillCheck> {
  const command = process.env[envName]?.trim()
  if (!command) {
    return {
      name,
      status: 'skipped',
      latencyMs: 0,
      category: 'external_command_not_configured',
      message: `${envName} 未配置；跳过破坏性重启演练`
    }
  }
  const startedAt = performance.now()
  try {
    await healthProbe()
    await runShellCommand(command, 120_000)
    const details = await waitForHealth(healthProbe, 60_000)
    return {
      name,
      status: 'passed',
      latencyMs: performance.now() - startedAt,
      details
    }
  } catch (error) {
    return {
      name,
      status: 'failed',
      latencyMs: performance.now() - startedAt,
      category: classifyReliabilityError(error),
      message: error instanceof Error ? error.message : String(error)
    }
  }
}

async function optionalRedisRestartRecoveryCheck(): Promise<DrillCheck> {
  const command = process.env.JUHE_AI_RELIABILITY_REDIS_RESTART_COMMAND?.trim()
  if (!command) {
    return {
      name: 'redis_restart_recovery',
      status: 'skipped',
      latencyMs: 0,
      category: 'external_command_not_configured',
      message: 'JUHE_AI_RELIABILITY_REDIS_RESTART_COMMAND 未配置；跳过 Redis 重启恢复演练'
    }
  }
  const startedAt = performance.now()
  try {
    const details = await redisRestartPendingRecoveryCheck(command)
    return {
      name: 'redis_restart_recovery',
      status: 'passed',
      latencyMs: performance.now() - startedAt,
      details
    }
  } catch (error) {
    return {
      name: 'redis_restart_recovery',
      status: 'failed',
      latencyMs: performance.now() - startedAt,
      category: classifyReliabilityError(error),
      message: error instanceof Error ? error.message : String(error)
    }
  }
}

async function redisRestartPendingRecoveryCheck(command: string): Promise<Record<string, unknown>> {
  const streamKey = `juhe-ai:drill:restart:${runId}`
  const groupName = `juhe-ai:drill:restart-group:${runId}`
  const deadConsumerName = `dead-restart:${runId}`
  const liveConsumerName = `live-restart:${runId}`
  const redisUrl = requiredRedisQueueUrl()
  const deadQueue = new RedisStreamQueue<DrillPayload>({
    streamKey,
    groupName,
    consumerName: deadConsumerName,
    redisUrl,
    claimIdleMs: 1,
    readCount: 10,
    blockMs: 20
  })
  let client: RedisCommandClient | undefined = await createDedicatedRedisClient(redisUrl)
  try {
    await deadQueue.claimPending()
    const enqueuedIds: string[] = []
    for (let index = 0; index < 3; index += 1) {
      enqueuedIds.push(await deadQueue.enqueue({ runId, index }))
    }
    const readByDeadConsumer = await deadQueue.readNew()
    if (readByDeadConsumer.length !== enqueuedIds.length) {
      throw new Error(`Redis 重启前 pending 构造失败：expected=${enqueuedIds.length}, actual=${readByDeadConsumer.length}`)
    }
    const pendingBeforeRestart = parsePendingSummary(await client.sendCommand(['XPENDING', streamKey, groupName]))
    await delay(1500)
    await deadQueue.closeConsumer().catch(() => undefined)
    await closeRedisClient(client)
    client = undefined

    await runShellCommand(command, 120_000)
    const recoveredHealth = await waitForRedisQueueHealth()
    const liveQueue = new RedisStreamQueue<DrillPayload>({
      streamKey,
      groupName,
      consumerName: liveConsumerName,
      redisUrl,
      claimIdleMs: 1,
      readCount: 10,
      blockMs: 20
    })
    try {
      const claimedAfterRestart = await waitForRedisClaim(liveQueue, enqueuedIds.length, 30_000)
      const acked = await liveQueue.ack(claimedAfterRestart.map((message) => message.id))
      if (acked !== enqueuedIds.length) {
        throw new Error(`Redis 重启恢复 ACK 数量异常：expected=${enqueuedIds.length}, actual=${acked}`)
      }
      const cleanupClient = await createDedicatedRedisClient(redisUrl)
      try {
        const pendingAfterAck = parsePendingSummary(await cleanupClient.sendCommand(['XPENDING', streamKey, groupName]))
        return {
          streamKey,
          groupName,
          enqueued: enqueuedIds.length,
          pendingBeforeRestart,
          recoveredHealth,
          claimedAfterRestart: claimedAfterRestart.length,
          pendingAfterAck
        }
      } finally {
        await cleanupClient.sendCommand(['DEL', streamKey]).catch(() => undefined)
        await closeRedisClient(cleanupClient)
      }
    } finally {
      await liveQueue.closeConsumer().catch(() => undefined)
    }
  } finally {
    await deadQueue.closeConsumer().catch(() => undefined)
    if (client) {
      await client.sendCommand(['DEL', streamKey]).catch(() => undefined)
      await closeRedisClient(client)
    }
  }
}

async function waitForRedisClaim(
  queue: RedisStreamQueue<DrillPayload>,
  expectedCount: number,
  timeoutMs: number
): Promise<Array<{ id: string; payload: DrillPayload }>> {
  const startedAt = Date.now()
  let lastClaimed: Array<{ id: string; payload: DrillPayload }> = []
  while (Date.now() - startedAt < timeoutMs) {
    lastClaimed = await queue.claimPending()
    if (lastClaimed.length >= expectedCount) {
      return lastClaimed
    }
    await delay(500)
  }
  throw new Error(`Redis 重启后 pending 消息在 ${timeoutMs}ms 内未恢复：expected=${expectedCount}, actual=${lastClaimed.length}`)
}

async function waitForPostgresHealth(): Promise<Record<string, unknown>> {
  return waitForHealth(postgresHealthCheck, 60_000)
}

async function waitForRedisQueueHealth(): Promise<Record<string, unknown>> {
  return waitForHealth(redisQueueHealthCheck, 60_000)
}

async function waitForHealth(
  healthProbe: () => Promise<Record<string, unknown>>,
  timeoutMs: number
): Promise<Record<string, unknown>> {
  const startedAt = Date.now()
  let lastError: unknown
  while (Date.now() - startedAt < timeoutMs) {
    try {
      const details = await healthProbe()
      return {
        ...details,
        recoveredAfterMs: Date.now() - startedAt
      }
    } catch (error) {
      lastError = error
      await delay(1000)
    }
  }
  throw new Error(`健康检查在 ${timeoutMs}ms 内未恢复：${lastError instanceof Error ? lastError.message : String(lastError)}`)
}

async function createPgClient(connectionString: string, applicationName: string): Promise<PgClient> {
  const { Client } = await import('pg')
  const client = new Client({
    connectionString,
    application_name: applicationName
  })
  await client.connect()
  return client
}

async function readPostgresSchemaTableCounts(connectionString: string): Promise<Record<string, number>> {
  const client = await createPgClient(connectionString, 'juhe-ai-reliability-counts')
  try {
    const result = await client.query(`
      SELECT table_schema, COUNT(*)::int AS table_count
      FROM information_schema.tables
      WHERE table_type = 'BASE TABLE'
        AND table_schema = ANY($1::text[])
      GROUP BY table_schema
      ORDER BY table_schema
    `, [postgresSchemas])
    const counts: Record<string, number> = {}
    for (const schema of postgresSchemas) {
      counts[schema] = 0
    }
    for (const row of result.rows) {
      counts[String(row.table_schema)] = Number(row.table_count ?? 0)
    }
    return counts
  } finally {
    await client.end()
  }
}

function assertRestoredSchemaCounts(sourceCounts: Record<string, number>, restoreCounts: Record<string, number>): void {
  const sourceTotal = Object.values(sourceCounts).reduce((sum, count) => sum + count, 0)
  const restoreTotal = Object.values(restoreCounts).reduce((sum, count) => sum + count, 0)
  if (sourceTotal === 0) {
    throw new Error('源库 schema 表数量为 0，拒绝把空库视为备份恢复成功')
  }
  for (const schema of postgresSchemas) {
    if ((restoreCounts[schema] ?? 0) !== (sourceCounts[schema] ?? 0)) {
      throw new Error(`恢复库 schema 表数量不一致：${schema} source=${sourceCounts[schema] ?? 0}, restore=${restoreCounts[schema] ?? 0}`)
    }
  }
  if (restoreTotal !== sourceTotal) {
    throw new Error(`恢复库表总数不一致：source=${sourceTotal}, restore=${restoreTotal}`)
  }
}

async function dropDatabaseIfExists(client: PgClient, databaseName: string): Promise<void> {
  await client.query('SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()', [databaseName])
  await client.query(`DROP DATABASE IF EXISTS ${quoteIdentifier(databaseName)}`)
}

function adminDatabaseUrl(sourceUrl: string): string {
  const parsed = new URL(sourceUrl)
  parsed.pathname = '/postgres'
  parsed.search = ''
  return parsed.toString()
}

function databaseUrlWithName(sourceUrl: string, databaseName: string): string {
  const parsed = new URL(sourceUrl)
  parsed.pathname = `/${databaseName}`
  return parsed.toString()
}

function safeDatabaseName(value: string): string {
  const normalized = value.trim().replace(/[^a-zA-Z0-9_]+/g, '_').slice(0, 58)
  if (!/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(normalized)) {
    throw new Error(`恢复演练数据库名非法：${value}`)
  }
  return normalized
}

function quoteIdentifier(value: string): string {
  return `"${value.replace(/"/g, '""')}"`
}

function requiredEnv(name: string): string {
  const value = process.env[name]?.trim()
  if (!value) {
    throw new Error(`${name} 未配置`)
  }
  return value
}

function shellSingleQuote(value: string): string {
  return `'${value.replace(/'/g, "'\\''")}'`
}

function parseMarkedValue(output: string, marker: string): string | undefined {
  const prefix = `${marker}=`
  for (const line of output.split(/\r?\n/)) {
    if (line.startsWith(prefix)) {
      return line.slice(prefix.length).trim()
    }
  }
  return undefined
}

function parseMarkedCounts(output: string, marker: string): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const schema of postgresSchemas) {
    counts[schema] = 0
  }
  let collecting = false
  for (const rawLine of output.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line) continue
    if (line === marker) {
      collecting = true
      continue
    }
    if (collecting && line.startsWith('__JUHE_')) {
      break
    }
    if (!collecting) continue
    const [schema, rawCount] = line.split('=', 2)
    if (!schema || rawCount === undefined || !postgresSchemas.includes(schema)) {
      continue
    }
    counts[schema] = Number(rawCount)
  }
  return counts
}

async function runExecutable(command: string, args: string[], timeoutMs: number): Promise<void> {
  await runExecutableCapture(command, args, timeoutMs)
}

async function runExecutableCapture(command: string, args: string[], timeoutMs: number): Promise<string> {
  return await new Promise<string>((resolvePromise, rejectPromise) => {
    const child = spawn(command, args, {
      stdio: ['ignore', 'pipe', 'pipe']
    })
    const stdoutChunks: Buffer[] = []
    const stderrChunks: Buffer[] = []
    const timer = setTimeout(() => {
      child.kill('SIGTERM')
      rejectPromise(new Error(`${command} 超时 ${timeoutMs}ms`))
    }, timeoutMs)
    child.stdout.on('data', (chunk: Buffer) => stdoutChunks.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderrChunks.push(chunk))
    child.on('error', (error) => {
      clearTimeout(timer)
      rejectPromise(error)
    })
    child.on('exit', (code) => {
      clearTimeout(timer)
      const stdout = Buffer.concat(stdoutChunks).toString('utf8')
      if (code === 0) {
        resolvePromise(stdout)
        return
      }
      const output = Buffer.concat([...stdoutChunks, ...stderrChunks]).toString('utf8').slice(-4000)
      rejectPromise(new Error(`${command} 退出码 ${code}：${output}`))
    })
  })
}

async function runShellCommand(command: string, timeoutMs: number): Promise<void> {
  const shell = process.platform === 'win32' ? 'pwsh' : '/bin/sh'
  const args = process.platform === 'win32'
    ? ['-NoProfile', '-Command', command]
    : ['-c', command]
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const child = spawn(shell, args, {
      stdio: ['ignore', 'pipe', 'pipe']
    })
    const stdoutChunks: Buffer[] = []
    const stderrChunks: Buffer[] = []
    const timer = setTimeout(() => {
      child.kill('SIGTERM')
      rejectPromise(new Error(`外部命令超时 ${timeoutMs}ms`))
    }, timeoutMs)
    child.stdout.on('data', (chunk: Buffer) => stdoutChunks.push(chunk))
    child.stderr.on('data', (chunk: Buffer) => stderrChunks.push(chunk))
    child.on('error', (error) => {
      clearTimeout(timer)
      rejectPromise(error)
    })
    child.on('exit', (code) => {
      clearTimeout(timer)
      if (code === 0) {
        resolvePromise()
        return
      }
      const output = Buffer.concat([...stdoutChunks, ...stderrChunks]).toString('utf8').slice(-4000)
      rejectPromise(new Error(`外部命令退出码 ${code}：${output}`))
    })
  })
}

function parsePendingSummary(result: unknown): PendingSummary {
  if (Array.isArray(result)) {
    return {
      pending: Number(result[0] ?? 0),
      minId: typeof result[1] === 'string' ? result[1] : undefined,
      maxId: typeof result[2] === 'string' ? result[2] : undefined,
      consumers: parsePendingConsumers(result[3])
    }
  }
  if (isPlainObject(result)) {
    const record = result as Record<string, unknown>
    return {
      pending: Number(record.pending ?? record.count ?? 0),
      minId: typeof record.firstId === 'string' ? record.firstId : typeof record.minId === 'string' ? record.minId : undefined,
      maxId: typeof record.lastId === 'string' ? record.lastId : typeof record.maxId === 'string' ? record.maxId : undefined,
      consumers: parsePendingConsumers(record.consumers)
    }
  }
  return {
    pending: 0,
    consumers: []
  }
}

function parsePendingConsumers(value: unknown): Array<{ name: string; pending: number }> {
  if (!Array.isArray(value)) return []
  return value.map((entry) => {
    if (Array.isArray(entry)) {
      return {
        name: String(entry[0] ?? ''),
        pending: Number(entry[1] ?? 0)
      }
    }
    if (isPlainObject(entry)) {
      const record = entry as Record<string, unknown>
      return {
        name: String(record.name ?? record.consumer ?? ''),
        pending: Number(record.pending ?? record.count ?? 0)
      }
    }
    return {
      name: String(entry ?? ''),
      pending: 0
    }
  }).filter((entry) => entry.name.length > 0)
}

function normalizeRedisIntegerOrError(value: unknown): unknown {
  if (isPlainObject(value) && typeof (value as Record<string, unknown>).error === 'string') {
    return value
  }
  const numberValue = Number(value)
  return Number.isFinite(numberValue) ? numberValue : value
}

function normalizePgRowNumbers(row: Record<string, unknown>): Record<string, unknown> {
  const output: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(row)) {
    const numberValue = typeof value === 'string' ? Number(value) : value
    output[key] = typeof numberValue === 'number' && Number.isFinite(numberValue) ? numberValue : value
  }
  return output
}

function requiredRedisQueueUrl(): string {
  const url = runtimeConfig.redis.queueUrl
  if (!url) {
    throw new Error('JUHE_AI_REDIS_QUEUE_URL 未配置')
  }
  return url
}

function requiredPostgresUrl(): string {
  const url = runtimeConfig.postgres.url
  if (!url) {
    throw new Error('JUHE_AI_POSTGRES_URL 未配置')
  }
  return url
}

async function closeRedisClient(client: RedisCommandClient): Promise<void> {
  if (typeof client.destroy === 'function') {
    client.destroy()
    return
  }
  if (typeof client.quit === 'function') {
    await client.quit()
  }
}

function assertPerformanceRuntime(): void {
  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('可靠性演练必须使用 JUHE_AI_DATABASE_DRIVER=postgres 或 JUHE_AI_RUNTIME_MODE=performance')
  }
  if (runtimeConfig.queueDriver !== 'redis_stream') {
    throw new Error('可靠性演练必须使用 JUHE_AI_QUEUE_DRIVER=redis_stream')
  }
  if (!runtimeConfig.postgres.url) {
    throw new Error('可靠性演练必须配置 JUHE_AI_POSTGRES_URL')
  }
  if (!runtimeConfig.redis.queueUrl) {
    throw new Error('可靠性演练必须配置 JUHE_AI_REDIS_QUEUE_URL')
  }
}

function classifyReliabilityError(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error)
  if (message.includes('ENOENT') && (message.includes('pg_dump') || message.includes('pg_restore'))) {
    return 'postgres_client_tool_missing'
  }
  if (message.includes('password authentication failed') || message.includes('WRONGPASS') || message.includes('NOAUTH')) {
    return 'authentication_failed'
  }
  if (message.includes('timeout') || message.includes('超时') || message.includes('ECONNREFUSED') || message.includes('ETIMEDOUT')) {
    return 'connectivity_or_timeout'
  }
  if (message.includes('permission denied') || message.includes('must be owner') || message.includes('permission')) {
    return 'permission_failed'
  }
  return 'unexpected_error'
}

function writeReport(report: ReliabilityDrillReport): void {
  mkdirSync(dirname(reportPath), { recursive: true })
  writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`, 'utf8')
}

function printReport(report: ReliabilityDrillReport): void {
  console.log(`高性能模式可靠性演练完成：pass=${report.pass}`)
  console.log(`报告：${report.reportPath}`)
  for (const check of report.checks) {
    const suffix = check.status === 'failed'
      ? ` ${check.category ?? 'error'} ${check.message ?? ''}`.trimEnd()
      : check.status === 'skipped'
        ? ` ${check.category ?? 'skipped'}`
        : ''
    console.log(`- ${check.name}: ${check.status} ${check.latencyMs.toFixed(2)}ms${suffix ? ` (${suffix})` : ''}`)
  }
}

function timestampForFile(date: Date): string {
  const pad = (value: number) => String(value).padStart(2, '0')
  return [
    date.getFullYear(),
    pad(date.getMonth() + 1),
    pad(date.getDate()),
    '-',
    pad(date.getHours()),
    pad(date.getMinutes()),
    pad(date.getSeconds())
  ].join('')
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => {
    const timer = setTimeout(resolvePromise, ms)
    timer.unref()
  })
}
