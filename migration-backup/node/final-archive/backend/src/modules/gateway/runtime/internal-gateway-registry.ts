import { createHmac, randomUUID, timingSafeEqual } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { logger } from '../../../shared/logger.js'
import {
  isRecoverableRedisClientError,
  runRedisOperationWithDeadline,
  type RedisCommandClient
} from '../../../shared/redis-client.js'
import { redisNamespacedKey, sanitizeRedisNamespacePart } from '../../../shared/redis-namespace.js'

const entryVersion = 1
const entryTtlSeconds = 20
const heartbeatIntervalMs = 5_000
const commandTimeoutMs = 800
const entryLimit = 64
const entryKeyPrefix = redisNamespacedKey('juhe-ai:runtime:internal-gateway:v1:')
const indexKey = redisNamespacedKey('juhe-ai:runtime:internal-gateway-index:v1')
const publishScript = `
local redis_time = redis.call('TIME')
local now_ms = redis_time[1] * 1000 + math.floor(redis_time[2] / 1000)
local minimum_score = now_ms - tonumber(ARGV[2]) * 1000
redis.call('SET', KEYS[1], ARGV[1], 'EX', ARGV[2])
redis.call('ZADD', KEYS[2], now_ms, KEYS[1])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', '(' .. minimum_score)
local cardinality = redis.call('ZCARD', KEYS[2])
if cardinality > tonumber(ARGV[3]) then
  redis.call('ZREMRANGEBYRANK', KEYS[2], 0, cardinality - tonumber(ARGV[3]) - 1)
end
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[4]))
return now_ms
`
const unregisterScript = `
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local ok, entry = pcall(cjson.decode, raw)
if not ok or type(entry) ~= 'table' or entry.bootId ~= ARGV[1] then return 0 end
redis.call('DEL', KEYS[1])
redis.call('ZREM', KEYS[2], KEYS[1])
return 1
`
const readScript = `
local redis_time = redis.call('TIME')
local now_ms = redis_time[1] * 1000 + math.floor(redis_time[2] / 1000)
local minimum_score = now_ms - tonumber(ARGV[1]) * 1000
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', '(' .. minimum_score)
return redis.call('ZRANGEBYSCORE', KEYS[1], minimum_score, now_ms, 'LIMIT', '0', ARGV[2])
`

export interface InternalGatewayEndpoint {
  instanceId: string
  origin: string
}

interface RegistryEntry extends InternalGatewayEndpoint {
  version: number
  bootId: string
  signature: string
}

interface RegistrySession {
  bootId: string
  stopping: boolean
  heartbeatTimer?: NodeJS.Timeout
  publishPromise?: Promise<void>
}

type RegistryRedisOperationRunner = typeof runRedisOperationWithDeadline

let registrySession: RegistrySession | undefined
let registryStopPromise: Promise<void> | undefined
let publishRequested = false
let registryRedisOperationRunner: RegistryRedisOperationRunner = runRedisOperationWithDeadline

export function startInternalGatewayRegistry(): void {
  publishRequested = true
  ensureInternalGatewayRegistryStarted()
}

export async function stopInternalGatewayRegistry(): Promise<void> {
  publishRequested = false
  const existingStop = registryStopPromise
  if (existingStop) {
    await existingStop
    return
  }

  const session = registrySession
  if (!session) return
  registrySession = undefined
  session.stopping = true
  if (session.heartbeatTimer) {
    clearTimeout(session.heartbeatTimer)
    session.heartbeatTimer = undefined
  }

  const stopPromise = (async () => {
    try {
      await session.publishPromise
      await unregisterCurrentGateway(session.bootId)
    } finally {
      registryStopPromise = undefined
      ensureInternalGatewayRegistryStarted()
    }
  })()
  registryStopPromise = stopPromise
  await stopPromise
}

export async function listInternalGatewayEndpoints(): Promise<InternalGatewayEndpoint[]> {
  if (!internalGatewayRegistryReaderEnabled()) return []
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) return []
  const values = await registryRedisOperationRunner<unknown[]>(redisUrl, {
    operationName: '内部 Gateway 注册表读取',
    timeoutMs: commandTimeoutMs
  }, async (client) => {
    const keys = await client.eval(readScript, {
      keys: [indexKey],
      arguments: [String(entryTtlSeconds), String(entryLimit)]
    })
    const entryKeys = Array.isArray(keys)
      ? [...new Set(keys.filter((key): key is string => typeof key === 'string' && key.startsWith(entryKeyPrefix)))].slice(0, entryLimit)
      : []
    if (!entryKeys.length) return []
    const raw = await client.sendCommand(['MGET', ...entryKeys])
    return Array.isArray(raw) ? raw : []
  })
  const endpoints = new Map<string, InternalGatewayEndpoint>()
  for (const value of values) {
    const entry = parseRegistryEntry(value)
    if (entry) endpoints.set(entry.instanceId, { instanceId: entry.instanceId, origin: entry.origin })
  }
  return [...endpoints.values()].sort((left, right) => left.instanceId.localeCompare(right.instanceId))
}

export function internalGatewayRegistryEntryKey(instanceId: string): string {
  return `${entryKeyPrefix}${sanitizeRedisNamespacePart(instanceId)}`
}

export function setInternalGatewayRegistryOperationRunnerForTest(runner?: RegistryRedisOperationRunner): void {
  registryRedisOperationRunner = runner ?? runRedisOperationWithDeadline
}

function ensureInternalGatewayRegistryStarted(): void {
  if (!publishRequested || registrySession || registryStopPromise || !internalGatewayRegistryPublisherEnabled()) return
  const session: RegistrySession = {
    bootId: randomUUID(),
    stopping: false
  }
  registrySession = session
  publishAndScheduleHeartbeat(session)
}

function publishAndScheduleHeartbeat(session: RegistrySession): void {
  if (!isCurrentPublishingSession(session) || session.publishPromise) return
  const publishPromise = publishCurrentGateway(session)
  session.publishPromise = publishPromise
  void publishPromise.then(
    () => finishPublishAndScheduleHeartbeat(session, publishPromise),
    () => finishPublishAndScheduleHeartbeat(session, publishPromise)
  )
}

function finishPublishAndScheduleHeartbeat(session: RegistrySession, publishPromise: Promise<void>): void {
  if (session.publishPromise === publishPromise) session.publishPromise = undefined
  if (!isCurrentPublishingSession(session) || session.heartbeatTimer) return
  session.heartbeatTimer = setTimeout(() => {
    session.heartbeatTimer = undefined
    publishAndScheduleHeartbeat(session)
  }, heartbeatIntervalMs)
  session.heartbeatTimer.unref()
}

async function publishCurrentGateway(session: RegistrySession): Promise<void> {
  if (!isCurrentPublishingSession(session)) return
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) return
  const entry = signedRegistryEntry({
    version: entryVersion,
    instanceId: runtimeConfig.instanceId,
    origin: `http://127.0.0.1:${runtimeConfig.port}`,
    bootId: session.bootId
  })
  try {
    await registryRedisOperationRunner(redisUrl, {
      operationName: '内部 Gateway 注册表写入',
      timeoutMs: commandTimeoutMs
    }, async (client) => {
      if (!isCurrentPublishingSession(session)) return undefined
      return await client.eval(publishScript, {
        keys: [internalGatewayRegistryEntryKey(entry.instanceId), indexKey],
        arguments: [JSON.stringify(entry), String(entryTtlSeconds), String(entryLimit), String(entryTtlSeconds * 3)]
      })
    })
  } catch (error) {
    if (!isRecoverableRedisClientError(error)) {
      logger.warn({ event: 'internal_gateway_registry_publish_failed', err: error }, '内部 Gateway 注册失败')
    }
  }
}

async function unregisterCurrentGateway(currentBootId: string): Promise<void> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) return
  try {
    await registryRedisOperationRunner(redisUrl, {
      operationName: '内部 Gateway 注册表注销',
      timeoutMs: commandTimeoutMs
    }, async (client) => await client.eval(unregisterScript, {
      keys: [internalGatewayRegistryEntryKey(runtimeConfig.instanceId), indexKey],
      arguments: [currentBootId]
    }))
  } catch (error) {
    if (!isRecoverableRedisClientError(error)) {
      logger.warn({ event: 'internal_gateway_registry_unregister_failed', err: error }, '内部 Gateway 注册注销失败')
    }
  }
}

function isCurrentPublishingSession(session: RegistrySession): boolean {
  return publishRequested
    && registrySession === session
    && !session.stopping
    && internalGatewayRegistryPublisherEnabled()
}

function parseRegistryEntry(value: unknown): RegistryEntry | undefined {
  if (typeof value !== 'string') return undefined
  try {
    const parsed = JSON.parse(value) as Record<string, unknown>
    const instanceId = typeof parsed.instanceId === 'string' ? parsed.instanceId.trim() : ''
    const origin = typeof parsed.origin === 'string' ? parsed.origin.trim() : ''
    const bootId = typeof parsed.bootId === 'string' ? parsed.bootId.trim() : ''
    const signature = typeof parsed.signature === 'string' ? parsed.signature : ''
    if (parsed.version !== entryVersion || !instanceId || !bootId || !isLoopbackHttpOrigin(origin)) return undefined
    const expected = registrySignature(entryVersion, instanceId, origin, bootId)
    const actual = Buffer.from(signature, 'hex')
    const expectedBuffer = Buffer.from(expected, 'hex')
    if (actual.length !== expectedBuffer.length || !timingSafeEqual(actual, expectedBuffer)) return undefined
    return { version: entryVersion, instanceId, origin, bootId, signature }
  } catch {
    return undefined
  }
}

function signedRegistryEntry(entry: Omit<RegistryEntry, 'signature'>): RegistryEntry {
  return { ...entry, signature: registrySignature(entry.version, entry.instanceId, entry.origin, entry.bootId) }
}

function registrySignature(version: number, instanceId: string, origin: string, currentBootId: string): string {
  return createHmac('sha256', runtimeConfig.secret).update(`${version}|${instanceId}|${origin}|${currentBootId}`).digest('hex')
}

function isLoopbackHttpOrigin(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'http:' && url.hostname === '127.0.0.1' && Boolean(url.port)
      && !url.username && !url.password && (url.pathname === '' || url.pathname === '/') && !url.search && !url.hash
  } catch {
    return false
  }
}

function internalGatewayRegistryPublisherEnabled(): boolean {
  return runtimeConfig.runtimeMode === 'performance'
    && runtimeConfig.performanceNodeRole === 'gateway'
    && runtimeConfig.processRole === 'server'
    && runtimeConfig.runtimeStateDriver === 'redis'
    && Boolean(runtimeConfig.redis.stateUrl)
}

function internalGatewayRegistryReaderEnabled(): boolean {
  return runtimeConfig.runtimeMode === 'performance'
    && (runtimeConfig.performanceNodeRole === 'control' || runtimeConfig.performanceNodeRole === 'control-replica')
    && runtimeConfig.processRole === 'db-service'
    && runtimeConfig.runtimeStateDriver === 'redis'
    && Boolean(runtimeConfig.redis.stateUrl)
}
