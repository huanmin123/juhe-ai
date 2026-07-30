import { createHmac, randomUUID, timingSafeEqual } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { logger } from '../../../shared/logger.js'
import { getRedisClient, isRecoverableRedisClientError, type RedisCommandClient } from '../../../shared/redis-client.js'
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

let heartbeatTimer: NodeJS.Timeout | undefined
let bootId: string | undefined
let publishInFlight = false

export function startInternalGatewayRegistry(): void {
  if (!internalGatewayRegistryPublisherEnabled() || heartbeatTimer) return
  bootId = randomUUID()
  void publishCurrentGateway().finally(scheduleHeartbeat)
}

export function stopInternalGatewayRegistry(): void {
  if (heartbeatTimer) {
    clearTimeout(heartbeatTimer)
    heartbeatTimer = undefined
  }
  const currentBootId = bootId
  bootId = undefined
  if (!currentBootId || !internalGatewayRegistryPublisherEnabled()) return
  void unregisterCurrentGateway(currentBootId).catch(() => undefined)
}

export async function listInternalGatewayEndpoints(): Promise<InternalGatewayEndpoint[]> {
  if (!internalGatewayRegistryReaderEnabled()) return []
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) return []
  const client = await withTimeout(getRedisClient(redisUrl), commandTimeoutMs, '内部 Gateway 注册表连接超时')
  const keys = await withTimeout(client.eval(readScript, {
    keys: [indexKey],
    arguments: [String(entryTtlSeconds), String(entryLimit)]
  }), commandTimeoutMs, '内部 Gateway 注册表读取超时')
  const entryKeys = Array.isArray(keys)
    ? [...new Set(keys.filter((key): key is string => typeof key === 'string' && key.startsWith(entryKeyPrefix)))].slice(0, entryLimit)
    : []
  if (!entryKeys.length) return []
  const values = await withTimeout(client.sendCommand(['MGET', ...entryKeys]), commandTimeoutMs, '内部 Gateway 注册表条目读取超时')
  if (!Array.isArray(values)) return []
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

function scheduleHeartbeat(): void {
  if (!bootId || heartbeatTimer) return
  heartbeatTimer = setTimeout(() => {
    heartbeatTimer = undefined
    void publishCurrentGateway().finally(scheduleHeartbeat)
  }, heartbeatIntervalMs)
  heartbeatTimer.unref()
}

async function publishCurrentGateway(): Promise<void> {
  const currentBootId = bootId
  if (!currentBootId || publishInFlight || !internalGatewayRegistryPublisherEnabled()) return
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) return
  publishInFlight = true
  try {
    const entry = signedRegistryEntry({
      version: entryVersion,
      instanceId: runtimeConfig.instanceId,
      origin: `http://127.0.0.1:${runtimeConfig.port}`,
      bootId: currentBootId
    })
    const client = await withTimeout(getRedisClient(redisUrl), commandTimeoutMs, '内部 Gateway 注册表连接超时')
    await withTimeout(client.eval(publishScript, {
      keys: [internalGatewayRegistryEntryKey(entry.instanceId), indexKey],
      arguments: [JSON.stringify(entry), String(entryTtlSeconds), String(entryLimit), String(entryTtlSeconds * 3)]
    }), commandTimeoutMs, '内部 Gateway 注册表写入超时')
  } catch (error) {
    if (!isRecoverableRedisClientError(error)) {
      logger.warn({ event: 'internal_gateway_registry_publish_failed', err: error }, '内部 Gateway 注册失败')
    }
  } finally {
    publishInFlight = false
  }
}

async function unregisterCurrentGateway(currentBootId: string): Promise<void> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) return
  const client = await withTimeout(getRedisClient(redisUrl), commandTimeoutMs, '内部 Gateway 注册表连接超时')
  const key = internalGatewayRegistryEntryKey(runtimeConfig.instanceId)
  const raw = await withTimeout(client.get(key), commandTimeoutMs, '内部 Gateway 注册表读取超时')
  const entry = parseRegistryEntry(raw)
  if (entry?.bootId !== currentBootId) return
  await withTimeout(client.sendCommand(['DEL', key]), commandTimeoutMs, '内部 Gateway 注册表注销超时')
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
    && runtimeConfig.performanceNodeRole === 'control'
    && runtimeConfig.processRole === 'db-service'
    && runtimeConfig.runtimeStateDriver === 'redis'
    && Boolean(runtimeConfig.redis.stateUrl)
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs)
        timer.unref()
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
    promise.catch(() => undefined)
  }
}
