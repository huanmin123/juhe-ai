import { createHash } from 'node:crypto'

import { createClient } from 'redis'

import { runtimeConfig } from '../../config/runtime.js'
import {
  clearRuntimeLogIndexQueueForTest,
  enqueueRuntimeLogLine,
  startRuntimeLogRedisStreamConsumer,
  stopRuntimeLogRedisStreamConsumer
} from '../../modules/runtime-logs/runtime-log-index-queue.service.js'
import { logger } from '../../shared/logger.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closeStorageDatabases } from '../../storage/database.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import { listRuntimeLogsAsync } from '../../storage/runtime-logs.repository.js'

interface StreamEntry {
  id: string
  payload: Record<string, unknown>
}

interface StreamSnapshot {
  length: number
  pending: number
  lag: number
}

const streamKey = 'juhe-ai:queue:runtime-log-index'
const groupName = 'juhe-ai:runtime-log-index-writers'
const smokeId = `runtime-log-smoke-${Date.now()}`
const traceId = `runtime-log-redis-stream-smoke-${Date.now()}`
const sourceKey = `${traceId}:source`
const expectedRuntimeLogId = stableRuntimeLogId(sourceKey)

logger.level = 'silent'

let exitCode = 0
let client: ReturnType<typeof createClient> | undefined

try {
  if (runtimeConfig.queueDriver !== 'redis_stream') {
    throw new Error('runtime log Redis Stream smoke requires JUHE_AI_QUEUE_DRIVER=redis_stream')
  }
  if (runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('runtime log Redis Stream smoke requires JUHE_AI_DATABASE_DRIVER=postgres')
  }
  if (!runtimeConfig.redis.queueUrl) {
    throw new Error('runtime log Redis Stream smoke requires JUHE_AI_REDIS_QUEUE_URL')
  }

  client = createClient({
    url: runtimeConfig.redis.queueUrl,
    socket: {
      connectTimeout: 3000,
      reconnectStrategy: false
    }
  })
  client.on('error', () => {})
  await client.connect()

  const before = await sampleStream()
  clearRuntimeLogIndexQueueForTest()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  startRuntimeLogRedisStreamConsumer()

  runtimeConfig.processRole = 'server'
  runtimeConfig.workerRole = 'worker'
  enqueueRuntimeLogLine(JSON.stringify({
    time: new Date().toISOString(),
    level: 30,
    traceId,
    event: 'runtime_log_redis_stream_smoke',
    msg: `运行日志 Redis Stream 冒烟 ${smokeId}`
  }), { sourceKey })

  const drained = await waitForSmokeMessageDrained()
  await stopRuntimeLogRedisStreamConsumer()
  const cleaned = await cleanupSmokeMessage()

  if (!drained.foundPgRow) {
    throw new Error('runtime log smoke message was not written to PostgreSQL runtime_logs')
  }
  if (drained.snapshot.pending !== 0 || drained.snapshot.lag !== 0) {
    throw new Error(`runtime log stream not drained: pending=${drained.snapshot.pending} lag=${drained.snapshot.lag}`)
  }

  console.log(JSON.stringify({
    passed: true,
    before,
    after: drained.snapshot,
    runtimeLogId: expectedRuntimeLogId,
    cleanedSmokeMessages: cleaned
  }))
} catch (error) {
  exitCode = 1
  console.error(error instanceof Error ? error.stack ?? error.message : error)
} finally {
  await stopRuntimeLogRedisStreamConsumer().catch(() => undefined)
  await client?.quit().catch(() => undefined)
  await closeRedisClients().catch(() => undefined)
  closeStorageDatabases()
  await closePostgresPool().catch(() => undefined)
}

process.exit(exitCode)

async function waitForSmokeMessageDrained(): Promise<{ foundStreamEntry: boolean; foundPgRow: boolean; snapshot: StreamSnapshot }> {
  let lastSnapshot = await sampleStream()
  let foundStreamEntry = false
  let foundPgRow = false
  for (let attempt = 0; attempt < 80; attempt += 1) {
    const recent = await recentStreamEntries()
    foundStreamEntry = foundStreamEntry || recent.some((entry) => entry.payload.id === expectedRuntimeLogId)
    const logs = await listRuntimeLogsAsync({ traceId, pageSize: 5 }).catch(() => ({ items: [] }))
    foundPgRow = logs.items.some((item) => item.id === expectedRuntimeLogId)
    lastSnapshot = await sampleStream()
    if (foundPgRow && lastSnapshot.pending === 0 && lastSnapshot.lag === 0) {
      break
    }
    await delay(100)
  }
  return { foundStreamEntry, foundPgRow, snapshot: lastSnapshot }
}

async function cleanupSmokeMessage(): Promise<number> {
  const ids = (await recentStreamEntries())
    .filter((entry) => entry.payload.id === expectedRuntimeLogId)
    .map((entry) => entry.id)
  if (ids.length > 0) {
    await client?.sendCommand(['XDEL', streamKey, ...ids])
  }
  return ids.length
}

async function recentStreamEntries(): Promise<StreamEntry[]> {
  const value = await client?.sendCommand(['XREVRANGE', streamKey, '+', '-', 'COUNT', '1000']).catch(() => [])
  return parseStreamEntries(value)
}

async function sampleStream(): Promise<StreamSnapshot> {
  const length = Number(await client?.sendCommand(['XLEN', streamKey]).catch(() => 0) ?? 0)
  const pendingRaw = await client?.sendCommand(['XPENDING', streamKey, groupName]).catch(() => [0])
  const groupsRaw = await client?.sendCommand(['XINFO', 'GROUPS', streamKey]).catch(() => [])
  return {
    length,
    pending: parsePendingCount(pendingRaw),
    lag: parseGroupLag(groupsRaw)
  }
}

function parseStreamEntries(value: unknown): StreamEntry[] {
  if (!Array.isArray(value)) return []
  return value.flatMap((entry): StreamEntry[] => {
    const id = Array.isArray(entry) ? String(entry[0] ?? '') : String((entry as { id?: unknown })?.id ?? '')
    const fields = Array.isArray(entry) ? entry[1] : (entry as { message?: unknown })?.message
    const payloadText = streamPayloadText(fields)
    if (!id || !payloadText) return []
    try {
      const payload = JSON.parse(payloadText)
      return payload && typeof payload === 'object' && !Array.isArray(payload)
        ? [{ id, payload: payload as Record<string, unknown> }]
        : []
    } catch {
      return []
    }
  })
}

function streamPayloadText(fields: unknown): string | undefined {
  if (Array.isArray(fields)) {
    for (let index = 0; index < fields.length; index += 2) {
      if (String(fields[index] ?? '') === 'payload') {
        const value = fields[index + 1]
        return typeof value === 'string' ? value : value === undefined ? undefined : String(value)
      }
    }
    return undefined
  }
  if (fields && typeof fields === 'object') {
    const value = (fields as Record<string, unknown>).payload
    return typeof value === 'string' ? value : value === undefined ? undefined : String(value)
  }
  return undefined
}

function parsePendingCount(value: unknown): number {
  if (Array.isArray(value)) {
    return numberValue(value[0])
  }
  if (value && typeof value === 'object') {
    const record = value as Record<string, unknown>
    return numberValue(record.pending ?? record.pendingCount)
  }
  return 0
}

function parseGroupLag(value: unknown): number {
  if (!Array.isArray(value)) return 0
  for (const group of value) {
    const record = redisInfoGroupRecord(group)
    if (String(record.name ?? '') === groupName) {
      return numberValue(record.lag ?? record.lagCount)
    }
  }
  return 0
}

function redisInfoGroupRecord(value: unknown): Record<string, unknown> {
  if (!Array.isArray(value)) {
    return value && typeof value === 'object' ? value as Record<string, unknown> : {}
  }
  const record: Record<string, unknown> = {}
  for (let index = 0; index < value.length; index += 2) {
    const key = String(value[index] ?? '')
    if (key) {
      record[key] = value[index + 1]
    }
  }
  return record
}

function stableRuntimeLogId(value: string): string {
  const digest = createHash('sha256').update(value).digest('hex')
  return `rtlog_${digest.slice(0, 32)}`
}

function numberValue(value: unknown): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms)
    timer.unref()
  })
}
