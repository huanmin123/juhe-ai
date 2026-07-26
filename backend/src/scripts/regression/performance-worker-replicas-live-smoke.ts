import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import {
  enqueueUsageRecord,
  startUsageRecordRedisSpoolReplay,
  stopUsageRecordRedisSpoolReplay
} from '../../modules/gateway/usage/record-queue.service.js'
import { getUsageRecordSpoolRuntime } from '../../modules/gateway/usage/usage-record-spool.js'
import { closeRedisClients, createDedicatedRedisClient, type RedisCommandClient } from '../../shared/redis-client.js'
import {
  readPerformanceProcessEventLoopSamples,
  stopPerformanceProcessMetricsPublisher
} from '../../shared/performance-process-metrics-registry.js'
import { acquireRedisQueueFence, releaseRedisQueueFence } from '../../shared/redis-queue-fence.js'
import { redisNamespacedGroup, redisNamespacedKey } from '../../shared/redis-namespace.js'
import { RedisStreamQueue } from '../../shared/redis-stream-queue.js'
import { redisStreamQueueContracts } from '../../shared/redis-stream-drain.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import type { UsageRecordInput } from '../../storage/repositories.js'
import type { PublicApiLogInput } from '../../storage/public-api-logs.repository.js'

assert.equal(process.env.JUHE_AI_ALLOW_PERFORMANCE_WORKER_REPLICAS_LIVE_SMOKE, '1')
assert.equal(runtimeConfig.runtimeMode, 'performance')
assert.equal(runtimeConfig.databaseDriver, 'postgres')
assert.equal(runtimeConfig.queueDriver, 'redis_stream')
assert.ok(runtimeConfig.postgres.url)
assert.ok(runtimeConfig.redis.queueUrl)
assert.ok(runtimeConfig.redis.cacheUrl)
const expectedInfrastructureHost = process.env.JUHE_AI_PERFORMANCE_WORKER_REPLICAS_LIVE_SMOKE_HOST?.trim()
assert.ok(expectedInfrastructureHost, 'live smoke 必须显式配置 JUHE_AI_PERFORMANCE_WORKER_REPLICAS_LIVE_SMOKE_HOST')
assert.ok(
  runtimeConfig.queue.redisStreamClaimIdleMs <= 5_000,
  'live smoke 必须显式将 JUHE_AI_REDIS_STREAM_CLAIM_IDLE_MS 设置为不超过 5000，避免测试等待默认 60 秒接管窗口'
)
assert.equal(new URL(runtimeConfig.postgres.url).hostname, expectedInfrastructureHost)
assert.equal(new URL(runtimeConfig.redis.queueUrl).hostname, expectedInfrastructureHost)
assert.match(runtimeConfig.redis.namespace, /^codex-worker-replicas-/)

const scriptDirectory = dirname(fileURLToPath(import.meta.url))
const backendRoot = resolve(scriptDirectory, '../../..')
const workerEntry = resolve(backendRoot, 'src/worker.ts')
const spoolRoot = mkdtempSync(join(tmpdir(), 'juhe-ai-worker-replicas-spool-'))
runtimeConfig.usageSpool.directory = spoolRoot
runtimeConfig.usageSpool.replayIntervalMs = 50
runtimeConfig.usageSpool.replayBatchSize = 50

const marker = `worker_replicas_${Date.now()}_${Math.random().toString(16).slice(2)}`
const usageIds = Array.from({ length: 4 }, (_value, index) => `usage_${marker}_${index}`)
const publicLogIds = Array.from({ length: 4 }, (_value, index) => `publog_${marker}_${index}`)
const usageStreamKey = redisNamespacedKey(redisStreamQueueContracts.usageRecords.streamKey)
const usageGroupName = redisNamespacedGroup(redisStreamQueueContracts.usageRecords.groupName)
const publicStreamKey = redisNamespacedKey(redisStreamQueueContracts.publicApiLogs.streamKey)
const publicGroupName = redisNamespacedGroup(redisStreamQueueContracts.publicApiLogs.groupName)
const allQueueKeys = Object.values(redisStreamQueueContracts).map((contract) => redisNamespacedKey(contract.streamKey))
const queueFenceToken = `worker-replicas-fence-${marker}`
const processMetricsKeyPattern = `${redisNamespacedKey('juhe-ai:runtime:process-event-loop:')}*`
const workers: ChildProcess[] = []
const workerLabels = new Map<ChildProcess, string>()
const workerOutput = new Map<ChildProcess, string>()
const pool = await getPostgresPool()
const redis = await createDedicatedRedisClient(runtimeConfig.redis.queueUrl, {
  disableOfflineQueue: true,
  connectTimeoutMs: 3_000
})
const cacheRedis = await createDedicatedRedisClient(runtimeConfig.redis.cacheUrl, {
  disableOfflineQueue: true,
  connectTimeoutMs: 3_000
})

try {
  const usageWorkers = await Promise.all([
    startWorker('usage-worker', 0),
    startWorker('usage-worker', 1)
  ])
  const logWorkers = await Promise.all([
    startWorker('log-worker', 0),
    startWorker('log-worker', 1)
  ])

  await waitUntil(async () => {
    const keys = await scanKeys(cacheRedis, processMetricsKeyPattern)
    return (['usage-worker:1', 'usage-worker:2', 'log-worker:1', 'log-worker:2'] as const)
      .every((role) => keys.some((key) => key.endsWith(`:${role}`)))
  }, '等待四个 worker 注册独立进程指标')

  await waitUntil(async () => await consumerCount(usageStreamKey, usageGroupName) >= 2, '等待双 Usage consumer 注册')
  await waitUntil(async () => await consumerCount(publicStreamKey, publicGroupName) >= 2, '等待双 Log consumer 注册')

  const firstUsage = usageRecord(usageIds[0]!, 0)
  assert.equal(await acquireRedisQueueFence(runtimeConfig.redis.queueUrl, queueFenceToken), true)
  await enqueueUsageRecord(firstUsage)
  assert.equal(getUsageRecordSpoolRuntime().persistedCount, 1, 'queue fence 时 usage 必须进入 durable spool')
  assert.equal(await releaseRedisQueueFence(runtimeConfig.redis.queueUrl, queueFenceToken), true)
  startUsageRecordRedisSpoolReplay()

  const usageQueue = new RedisStreamQueue<UsageRecordInput>({
    streamKey: redisStreamQueueContracts.usageRecords.streamKey,
    groupName: redisStreamQueueContracts.usageRecords.groupName,
    redisUrl: runtimeConfig.redis.queueUrl
  })
  const publicQueue = new RedisStreamQueue<PublicApiLogInput>({
    streamKey: redisStreamQueueContracts.publicApiLogs.streamKey,
    groupName: redisStreamQueueContracts.publicApiLogs.groupName,
    redisUrl: runtimeConfig.redis.queueUrl
  })
  await usageQueue.enqueue(usageRecord(usageIds[1]!, 1))
  await publicQueue.enqueue(publicApiLog(publicLogIds[0]!, 0))
  await publicQueue.enqueue(publicApiLog(publicLogIds[1]!, 1))
  await waitForRows(usageIds.slice(0, 2), publicLogIds.slice(0, 2))

  await stopWorker(usageWorkers[0]!)
  await stopWorker(logWorkers[0]!)
  await usageQueue.enqueue(usageRecord(usageIds[2]!, 2))
  await usageQueue.enqueue(usageRecord(usageIds[3]!, 3))
  await publicQueue.enqueue(publicApiLog(publicLogIds[2]!, 2))
  await publicQueue.enqueue(publicApiLog(publicLogIds[3]!, 3))
  await waitForRows(usageIds, publicLogIds)

  await waitUntil(async () => await queueDrained(usageStreamKey, usageGroupName), '等待 usage Stream 排空')
  await waitUntil(async () => await queueDrained(publicStreamKey, publicGroupName), '等待 public log Stream 排空')
  assert.equal(getUsageRecordSpoolRuntime().replayedCount, 1, 'Redis 恢复后必须重放 durable spool')
  assert.equal(await rowCount('juhe_usage.usage_records', usageIds), usageIds.length)
  assert.equal(await rowCount('juhe_dataset.public_api_logs', publicLogIds), publicLogIds.length)

  await usageQueue.closeConsumer()
  await publicQueue.closeConsumer()
  console.log(JSON.stringify({
    event: 'performance_worker_replicas_live_smoke_passed',
    usageWorkers: 2,
    logWorkers: 2,
    usageRows: usageIds.length,
    publicLogRows: publicLogIds.length,
    processMetricsRegistered: 4,
    failoverWorkersStopped: 2,
    spoolReplayed: getUsageRecordSpoolRuntime().replayedCount
  }))
} catch (error) {
  console.error(JSON.stringify(await failureDiagnostics(), undefined, 2))
  throw error
} finally {
  await releaseRedisQueueFence(runtimeConfig.redis.queueUrl, queueFenceToken).catch(() => false)
  await stopUsageRecordRedisSpoolReplay().catch(() => undefined)
  for (const worker of [...workers]) await stopWorker(worker)
  await pool.query('DELETE FROM juhe_usage.usage_records WHERE id = ANY($1::text[])', [usageIds]).catch(() => undefined)
  await pool.query('DELETE FROM juhe_dataset.public_api_logs WHERE id = ANY($1::text[])', [publicLogIds]).catch(() => undefined)
  if (allQueueKeys.length > 0) await redis.sendCommand(['DEL', ...allQueueKeys]).catch(() => 0)
  const processMetricKeys = await scanKeys(cacheRedis, processMetricsKeyPattern).catch(() => [])
  if (processMetricKeys.length > 0) await cacheRedis.sendCommand(['DEL', ...processMetricKeys]).catch(() => 0)
  stopPerformanceProcessMetricsPublisher()
  await redis.quit?.().catch(() => undefined)
  await cacheRedis.quit?.().catch(() => undefined)
  await closeRedisClients().catch(() => undefined)
  await closePostgresPool().catch(() => undefined)
  rmSync(spoolRoot, { recursive: true, force: true })
}

function usageRecord(id: string, offset: number): UsageRecordInput {
  const createdAt = new Date(Date.now() + offset).toISOString()
  return {
    id,
    traceId: `trace_${id}`,
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    model: 'worker-replicas-smoke',
    statusCode: 200,
    success: true,
    durationMs: 10 + offset,
    inputTokens: 1,
    outputTokens: 1,
    createdAt
  }
}

function publicApiLog(id: string, offset: number): PublicApiLogInput {
  const createdAt = new Date(Date.now() + offset).toISOString()
  return {
    id,
    traceId: `trace_${id}`,
    method: 'GET',
    path: '/__aipublic__/worker-replicas-smoke',
    statusCode: 200,
    success: true,
    durationMs: 2,
    requestData: { marker },
    responseData: { marker },
    startedAt: createdAt,
    endedAt: createdAt,
    createdAt
  }
}

async function startWorker(role: 'usage-worker' | 'log-worker', replicaIndex: number): Promise<ChildProcess> {
  const child = spawn(process.execPath, ['--import', 'tsx', workerEntry], {
    cwd: backendRoot,
    env: {
      ...process.env,
      JUHE_AI_PROCESS_ROLE: 'worker',
      JUHE_AI_WORKER_ROLE: role,
      JUHE_AI_WORKER_REPLICA_INDEX: String(replicaIndex),
      JUHE_AI_INSTANCE_ID: `live-${role}-${replicaIndex}`,
      JUHE_AI_LOG_LEVEL: 'error',
      JUHE_AI_LOG_FILE_ENABLED: 'false',
      JUHE_AI_LOG_CONSOLE_ENABLED: 'true',
      JUHE_AI_RUNTIME_LOG_INDEX_ENABLED: 'false'
    },
    serialization: 'advanced',
    stdio: ['ignore', 'pipe', 'pipe', 'ipc']
  })
  workers.push(child)
  workerLabels.set(child, `${role}#${replicaIndex}`)
  workerOutput.set(child, '')
  let stderr = ''
  child.stderr?.on('data', (chunk: Buffer) => {
    stderr = `${stderr}${chunk.toString('utf8')}`.slice(-8_000)
    appendWorkerOutput(child, chunk)
  })
  child.stdout?.on('data', (chunk: Buffer) => appendWorkerOutput(child, chunk))
  await new Promise<void>((resolvePromise, rejectPromise) => {
    const timeout = setTimeout(() => rejectPromise(new Error(`${role}#${replicaIndex} ready 超时：${stderr}`)), 15_000)
    child.once('error', (error) => {
      clearTimeout(timeout)
      rejectPromise(error)
    })
    child.once('exit', (code, signal) => {
      clearTimeout(timeout)
      rejectPromise(new Error(`${role}#${replicaIndex} ready 前退出：code=${code} signal=${signal} ${stderr}`))
    })
    child.on('message', (message: unknown) => {
      if (typeof message !== 'object' || message === null || Array.isArray(message)) return
      if ((message as { type?: unknown }).type !== 'background_worker_ready') return
      clearTimeout(timeout)
      resolvePromise()
    })
  })
  return child
}

function appendWorkerOutput(child: ChildProcess, chunk: Buffer): void {
  workerOutput.set(child, `${workerOutput.get(child) ?? ''}${chunk.toString('utf8')}`.slice(-8_000))
}

async function failureDiagnostics(): Promise<Record<string, unknown>> {
  return {
    event: 'performance_worker_replicas_live_smoke_failed',
    workers: workers.map((worker) => ({
      label: workerLabels.get(worker),
      pid: worker.pid,
      exitCode: worker.exitCode,
      signalCode: worker.signalCode,
      output: sanitizeDiagnosticOutput(workerOutput.get(worker) ?? '')
    })),
    usageQueue: await queueDiagnostics(usageStreamKey, usageGroupName),
    publicLogQueue: await queueDiagnostics(publicStreamKey, publicGroupName),
    processMetrics: {
      keys: await scanKeys(cacheRedis, processMetricsKeyPattern).catch(() => []),
      samples: await readPerformanceProcessEventLoopSamples().catch(() => [])
    },
    usageRows: await rowCount('juhe_usage.usage_records', usageIds).catch(() => -1),
    publicLogRows: await rowCount('juhe_dataset.public_api_logs', publicLogIds).catch(() => -1)
  }
}

async function queueDiagnostics(streamKey: string, groupName: string): Promise<Record<string, unknown>> {
  try {
    const length = Number(await redis.sendCommand(['XLEN', streamKey]))
    const pending = await redis.sendCommand(['XPENDING', streamKey, groupName])
    return { length, pending: pendingCount(pending), consumers: await consumerCount(streamKey, groupName) }
  } catch (error) {
    return { error: sanitizeDiagnosticOutput(error instanceof Error ? error.message : String(error)) }
  }
}

function sanitizeDiagnosticOutput(value: string): string {
  return value
    .replace(/\b(redis|postgres(?:ql)?):\/\/[^\s@]*@/gi, '$1://***@')
    .replace(/\b[a-f0-9]{40,}\b/gi, '***')
    .trim()
}

async function stopWorker(child: ChildProcess): Promise<void> {
  const index = workers.indexOf(child)
  if (index >= 0) workers.splice(index, 1)
  if (child.exitCode !== null || child.signalCode !== null) return
  child.kill('SIGTERM')
  await new Promise<void>((resolvePromise) => {
    const timeout = setTimeout(() => {
      child.kill('SIGKILL')
      resolvePromise()
    }, 5_000)
    child.once('exit', () => {
      clearTimeout(timeout)
      resolvePromise()
    })
  })
}

async function waitForRows(expectedUsageIds: string[], expectedPublicLogIds: string[]): Promise<void> {
  await waitUntil(async () =>
    await rowCount('juhe_usage.usage_records', expectedUsageIds) === expectedUsageIds.length
      && await rowCount('juhe_dataset.public_api_logs', expectedPublicLogIds) === expectedPublicLogIds.length,
  `等待 PostgreSQL 落库 usage=${expectedUsageIds.length} public=${expectedPublicLogIds.length}`)
}

async function rowCount(table: string, ids: string[]): Promise<number> {
  if (ids.length === 0) return 0
  const result = await pool.query(`SELECT COUNT(*)::int AS count FROM ${table} WHERE id = ANY($1::text[])`, [ids])
  return Number(result.rows[0]?.count ?? 0)
}

async function consumerCount(streamKey: string, groupName: string): Promise<number> {
  try {
    const raw = await redis.sendCommand(['XINFO', 'CONSUMERS', streamKey, groupName])
    return Array.isArray(raw) ? raw.length : 0
  } catch (error) {
    if (/no such key|NOGROUP/i.test(error instanceof Error ? error.message : String(error))) return 0
    throw error
  }
}

async function queueDrained(streamKey: string, groupName: string): Promise<boolean> {
  const length = Number(await redis.sendCommand(['XLEN', streamKey]))
  if (length !== 0) return false
  try {
    const pending = await redis.sendCommand(['XPENDING', streamKey, groupName])
    return pendingCount(pending) === 0
  } catch (error) {
    if (/no such key|NOGROUP/i.test(error instanceof Error ? error.message : String(error))) return false
    throw error
  }
}

function pendingCount(value: unknown): number {
  if (Array.isArray(value)) return Number(value[0] ?? 0)
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    return Number(record.pending ?? record.pendingCount ?? 0)
  }
  return 0
}

async function scanKeys(client: RedisCommandClient, pattern: string): Promise<string[]> {
  const keys: string[] = []
  let cursor = '0'
  do {
    const result = await client.sendCommand(['SCAN', cursor, 'MATCH', pattern, 'COUNT', '64'])
    if (!Array.isArray(result) || !Array.isArray(result[1])) break
    cursor = String(result[0] ?? '0')
    keys.push(...result[1].filter((key): key is string => typeof key === 'string'))
  } while (cursor !== '0' && keys.length < 128)
  return keys.slice(0, 128)
}

async function waitUntil(predicate: () => Promise<boolean>, label: string): Promise<void> {
  const deadline = Date.now() + 30_000
  let lastError: unknown
  while (Date.now() < deadline) {
    try {
      if (await predicate()) return
    } catch (error) {
      lastError = error
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 100))
  }
  throw new Error(`${label}超时${lastError ? `：${lastError instanceof Error ? lastError.message : String(lastError)}` : ''}`)
}
