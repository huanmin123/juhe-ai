import { createClient } from 'redis'

import { runtimeConfig } from '../../config/runtime.js'
import {
  clearRecordMaintenanceQueueForTest,
  enqueueRecordMaintenanceJobWithResultAsync,
  startRecordMaintenanceRedisStreamConsumer,
  stopRecordMaintenanceRedisStreamConsumer
} from '../../modules/record-maintenance/record-maintenance-queue.service.js'
import { logger } from '../../shared/logger.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { redisNamespacedGroup, redisNamespacedKey } from '../../shared/redis-namespace.js'
import { closeStorageDatabases } from '../../storage/database.js'
import { closePostgresPool } from '../../storage/postgres-client.js'

interface StreamEntry {
  id: string
  payload: Record<string, unknown>
}

interface StreamSnapshot {
  length: number
  pending: number
  lag: number
}

const streamKey = redisNamespacedKey('juhe-ai:queue:record-maintenance')
const groupName = redisNamespacedGroup('juhe-ai:record-maintenance-writers')
const smokeId = `record-maintenance-smoke-${Date.now()}`

logger.level = 'silent'

let exitCode = 0
let client: ReturnType<typeof createClient> | undefined

try {
  if (runtimeConfig.queueDriver !== 'redis_stream') {
    throw new Error('record maintenance Redis Stream smoke requires JUHE_AI_QUEUE_DRIVER=redis_stream')
  }
  if (!runtimeConfig.redis.queueUrl) {
    throw new Error('record maintenance Redis Stream smoke requires JUHE_AI_REDIS_QUEUE_URL')
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
  clearRecordMaintenanceQueueForTest()

  runtimeConfig.processRole = 'worker'
  runtimeConfig.workerRole = 'ingest-worker'
  startRecordMaintenanceRedisStreamConsumer()

  runtimeConfig.processRole = 'server'
  runtimeConfig.workerRole = 'worker'
  const result = await enqueueRecordMaintenanceJobWithResultAsync({
    type: 'usage_records_cleanup',
    id: smokeId,
    cutoffAt: new Date().toISOString(),
    batchSize: 1,
    maxBatches: 1
  })
  if (!result.queued) {
    throw new Error(`record maintenance Redis Stream enqueue failed: ${result.droppedReason ?? 'unknown'}`)
  }

  const drained = await waitForSmokeMessageDrained()
  await stopRecordMaintenanceRedisStreamConsumer()
  await cleanupSmokeMessage()

  if (!drained.found) {
    throw new Error('record maintenance stream did not expose the smoke message before draining')
  }
  if (drained.snapshot.pending !== 0 || drained.snapshot.lag !== 0) {
    throw new Error(`record maintenance stream not drained: pending=${drained.snapshot.pending} lag=${drained.snapshot.lag}`)
  }

  console.log(JSON.stringify({
    passed: true,
    before,
    after: drained.snapshot,
    cleanedSmokeMessages: drained.cleaned
  }))
} catch (error) {
  exitCode = 1
  console.error(error instanceof Error ? error.stack ?? error.message : error)
} finally {
  await stopRecordMaintenanceRedisStreamConsumer().catch(() => undefined)
  await client?.quit().catch(() => undefined)
  await closeRedisClients().catch(() => undefined)
  closeStorageDatabases()
  await closePostgresPool().catch(() => undefined)
}

process.exit(exitCode)

async function waitForSmokeMessageDrained(): Promise<{ found: boolean; snapshot: StreamSnapshot; cleaned: number }> {
  let lastSnapshot = await sampleStream()
  let found = false
  for (let attempt = 0; attempt < 50; attempt += 1) {
    const recent = await recentStreamEntries()
    found = found || recent.some((entry) => entry.payload.id === smokeId)
    lastSnapshot = await sampleStream()
    if (lastSnapshot.pending === 0 && lastSnapshot.lag === 0) {
      break
    }
    await delay(100)
  }
  const cleaned = await cleanupSmokeMessage()
  return { found, snapshot: lastSnapshot, cleaned }
}

async function cleanupSmokeMessage(): Promise<number> {
  const ids = (await recentStreamEntries())
    .filter((entry) => entry.payload.id === smokeId)
    .map((entry) => entry.id)
  if (ids.length > 0) {
    await client?.sendCommand(['XDEL', streamKey, ...ids])
  }
  return ids.length
}

async function recentStreamEntries(): Promise<StreamEntry[]> {
  const value = await client?.sendCommand(['XREVRANGE', streamKey, '+', '-', 'COUNT', '1000'])
  return parseStreamEntries(value)
}

async function sampleStream(): Promise<StreamSnapshot> {
  const length = Number(await client?.sendCommand(['XLEN', streamKey]) ?? 0)
  const pendingRaw = await sendStreamStateCommand(['XPENDING', streamKey, groupName], [0])
  const groupsRaw = await sendStreamStateCommand(['XINFO', 'GROUPS', streamKey], [])
  return {
    length,
    pending: parsePendingCount(pendingRaw),
    lag: parseGroupLag(groupsRaw)
  }
}

async function sendStreamStateCommand(command: string[], noGroupFallback: unknown): Promise<unknown> {
  try {
    return await client?.sendCommand(command)
  } catch (error) {
    if (isMissingStreamGroupError(error)) {
      return noGroupFallback
    }
    throw error
  }
}

function isMissingStreamGroupError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error ?? '')
  return /\bNOGROUP\b/i.test(message) || /no such key/i.test(message)
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
