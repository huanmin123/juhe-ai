import { createHash } from 'node:crypto'
import type { NextFunction, Request, Response } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from '../../shared/redis-client.js'
import { redisNamespacedKey } from '../../shared/redis-namespace.js'
import { getRequestAuthContext } from '../auth/request-context.js'
import { getSettingsAsync } from '../../storage/repositories.js'
import { getRequestContext, getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'
import { inspectClientIpPolicy } from '../gateway/runtime/client-ip-policy-cache.service.js'
import { systemApiDbAccessModeFromResponse } from './system-api-db-access.js'

type MethodClass = 'read' | 'write'
type LimiterScope = 'ip' | 'user'

interface SystemApiRateLimitSettings {
  ipReadPerMinute: number
  ipReadBurstPer10Seconds: number
  ipWritePerMinute: number
  ipWriteBurstPer10Seconds: number
  userReadPerMinute: number
  userWritePerMinute: number
}

interface RateLimitStore {
  name: string
  windowMs: number
  entries: Map<string, RateLimitEntry>
  nextCleanupAtMs: number
}

interface RateLimitEntry {
  count: number
  resetAtMs: number
}

interface RateLimitBucketInput {
  store: RateLimitStore
  key: string
  limit: number
}

interface RateLimitBucketDecision {
  allowed: boolean
  retryAfterSeconds: number
  commit: () => void
  storeName: string
  limit: number
}

interface RateLimitDecision {
  allowed: boolean
  retryAfterSeconds?: number
  bucketName?: string
  limit?: number
}

const minuteWindowMs = 60 * 1000
const burstWindowMs = 10 * 1000
const cleanupIntervalMs = 60 * 1000
const maxEntriesPerStore = 20_000

const ipMinuteStore: RateLimitStore = createStore('system_api_ip_minute', minuteWindowMs)
const ipBurstStore: RateLimitStore = createStore('system_api_ip_burst', burstWindowMs)
const userMinuteStore: RateLimitStore = createStore('system_api_user_minute', minuteWindowMs)

export async function systemApiIpRateLimit(req: Request, res: Response, next: NextFunction): Promise<void> {
  if (isSystemApiHealthPath(req)) {
    next()
    return
  }

  let settings: SystemApiRateLimitSettings
  try {
    settings = await currentSystemApiRateLimitSettings()
  } catch (error) {
    respondRateLimitFailure(req, res, error)
    return
  }

  if (await isClientIpRateLimitAllowlisted(req)) {
    next()
    return
  }

  const methodClass = methodClassFor(req, res)
  const clientIp = clientIpKey(req)
  const key = `${clientIp}:${methodClass}`
  const decision = await checkRateLimit([
    {
      store: ipMinuteStore,
      key,
      limit: methodClass === 'read' ? settings.ipReadPerMinute : settings.ipWritePerMinute
    },
    {
      store: ipBurstStore,
      key,
      limit: methodClass === 'read' ? settings.ipReadBurstPer10Seconds : settings.ipWriteBurstPer10Seconds
    }
  ], Date.now())

  if (!decision.allowed) {
    respondRateLimited(req, res, 'ip', methodClass, decision)
    return
  }

  next()
}

export async function systemApiAuthenticatedRateLimit(req: Request, res: Response, next: NextFunction): Promise<void> {
  let settings: SystemApiRateLimitSettings
  try {
    settings = await currentSystemApiRateLimitSettings()
  } catch (error) {
    next(error)
    return
  }

  if (await isClientIpRateLimitAllowlisted(req)) {
    next()
    return
  }

  const authContext = getRequestAuthContext()
  if (!authContext) {
    next()
    return
  }

  const methodClass = methodClassFor(req, res)
  const limit = methodClass === 'read' ? settings.userReadPerMinute : settings.userWritePerMinute
  const decision = await checkRateLimit([
    {
      store: userMinuteStore,
      key: `${authContext.systemAccountId}:${methodClass}`,
      limit
    }
  ], Date.now())

  if (!decision.allowed) {
    respondRateLimited(req, res, 'user', methodClass, decision)
    return
  }

  next()
}

export function clearSystemApiRateLimitStateForTest(): void {
  ipMinuteStore.entries.clear()
  ipBurstStore.entries.clear()
  userMinuteStore.entries.clear()
  ipMinuteStore.nextCleanupAtMs = 0
  ipBurstStore.nextCleanupAtMs = 0
  userMinuteStore.nextCleanupAtMs = 0
}

function createStore(name: string, windowMs: number): RateLimitStore {
  return {
    name,
    windowMs,
    entries: new Map(),
    nextCleanupAtMs: 0
  }
}

async function currentSystemApiRateLimitSettings(): Promise<SystemApiRateLimitSettings> {
  const settings = await getSettingsAsync()
  return {
    ipReadPerMinute: integerSetting(settings.systemApiRateLimitIpReadPerMinute, 'systemApiRateLimitIpReadPerMinute'),
    ipReadBurstPer10Seconds: integerSetting(settings.systemApiRateLimitIpReadBurstPer10Seconds, 'systemApiRateLimitIpReadBurstPer10Seconds'),
    ipWritePerMinute: integerSetting(settings.systemApiRateLimitIpWritePerMinute, 'systemApiRateLimitIpWritePerMinute'),
    ipWriteBurstPer10Seconds: integerSetting(settings.systemApiRateLimitIpWriteBurstPer10Seconds, 'systemApiRateLimitIpWriteBurstPer10Seconds'),
    userReadPerMinute: integerSetting(settings.systemApiRateLimitUserReadPerMinute, 'systemApiRateLimitUserReadPerMinute'),
    userWritePerMinute: integerSetting(settings.systemApiRateLimitUserWritePerMinute, 'systemApiRateLimitUserWritePerMinute')
  }
}

async function checkRateLimit(buckets: RateLimitBucketInput[], nowMs: number): Promise<RateLimitDecision> {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return checkRedisRateLimit(buckets, nowMs)
  }
  return checkMemoryRateLimit(buckets, nowMs)
}

function checkMemoryRateLimit(buckets: RateLimitBucketInput[], nowMs: number): RateLimitDecision {
  const decisions = buckets.map((bucket) => inspectBucket(bucket, nowMs))
  const blocked = decisions.find((decision) => !decision.allowed)
  if (blocked) {
    return {
      allowed: false,
      retryAfterSeconds: blocked.retryAfterSeconds,
      bucketName: blocked.storeName,
      limit: blocked.limit
    }
  }

  for (const decision of decisions) {
    decision.commit()
  }
  return { allowed: true }
}

async function checkRedisRateLimit(buckets: RateLimitBucketInput[], nowMs: number): Promise<RateLimitDecision> {
  if (buckets.length === 0) {
    return { allowed: true }
  }
  const result = await (await redisStateClient()).eval(redisFixedWindowRateLimitScript, {
    keys: buckets.map((bucket) => redisFixedWindowRateLimitKey(bucket.store.name, bucket.key)),
    arguments: [
      String(Math.trunc(nowMs)),
      String(buckets.length),
      ...buckets.flatMap((bucket) => [
        bucket.store.name,
        String(bucket.store.windowMs),
        String(bucket.limit)
      ])
    ]
  })
  const values = redisFixedWindowRateLimitResult(result)
  if (values.allowed) {
    return { allowed: true }
  }
  return {
    allowed: false,
    retryAfterSeconds: values.retryAfterSeconds,
    bucketName: values.bucketName,
    limit: values.limit
  }
}

function inspectBucket(input: RateLimitBucketInput, nowMs: number): RateLimitBucketDecision {
  const { store, key, limit } = input
  cleanupStore(store, nowMs)

  if (limit <= 0) {
    return {
      allowed: true,
      retryAfterSeconds: 0,
      commit: () => {},
      storeName: store.name,
      limit
    }
  }

  const current = store.entries.get(key)
  const isCurrentWindow = current && current.resetAtMs > nowMs
  const count = isCurrentWindow ? current.count : 0
  const resetAtMs = isCurrentWindow ? current.resetAtMs : nowMs + store.windowMs
  if (count >= limit) {
    return {
      allowed: false,
      retryAfterSeconds: Math.max(1, Math.ceil((resetAtMs - nowMs) / 1000)),
      commit: () => {},
      storeName: store.name,
      limit
    }
  }

  return {
    allowed: true,
    retryAfterSeconds: 0,
    commit: () => {
      store.entries.set(key, {
        count: count + 1,
        resetAtMs
      })
      trimStore(store, nowMs)
    },
    storeName: store.name,
    limit
  }
}

function cleanupStore(store: RateLimitStore, nowMs: number): void {
  if (store.nextCleanupAtMs > nowMs && store.entries.size <= maxEntriesPerStore) {
    return
  }

  for (const [key, entry] of store.entries) {
    if (entry.resetAtMs <= nowMs) {
      store.entries.delete(key)
    }
  }
  store.nextCleanupAtMs = nowMs + cleanupIntervalMs
  trimStore(store, nowMs)
}

function trimStore(store: RateLimitStore, nowMs: number): void {
  if (store.entries.size <= maxEntriesPerStore) {
    return
  }

  const targetSize = Math.floor(maxEntriesPerStore * 0.9)
  for (const [key, entry] of store.entries) {
    if (entry.resetAtMs <= nowMs || store.entries.size > targetSize) {
      store.entries.delete(key)
    }
    if (store.entries.size <= targetSize) {
      break
    }
  }
}

function respondRateLimited(
  req: Request,
  res: Response,
  scope: LimiterScope,
  methodClass: MethodClass,
  decision: RateLimitDecision
): void {
  const retryAfterSeconds = decision.retryAfterSeconds ?? 1
  res.setHeader('Retry-After', String(retryAfterSeconds))
  getRequestLogger().warn({
    event: 'system_api_rate_limit_blocked',
    scope,
    methodClass,
    bucketName: decision.bucketName,
    limit: decision.limit,
    retryAfterSeconds,
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl),
    clientIp: clientIpKey(req),
    systemAccountId: getRequestAuthContext()?.systemAccountId
  }, '后台系统 API 请求被全局限流拒绝')
  res.status(429).json({ message: '请求过于频繁，请稍后重试' })
}

function respondRateLimitFailure(req: Request, res: Response, error: unknown): void {
  getRequestLogger().error({
    event: 'system_api_rate_limit_failed',
    err: error instanceof Error ? error : undefined,
    errorMessage: error instanceof Error ? undefined : String(error),
    method: req.method,
    path: req.path,
    originalUrl: sanitizeUrlForLog(req.originalUrl)
  }, '后台系统 API 限流检查失败')
  res.status(500).json({ message: '服务器内部错误' })
}

function methodClassFor(req: Pick<Request, 'method'>, res: Response): MethodClass {
  const dbAccessMode = systemApiDbAccessModeFromResponse(res)
  if (dbAccessMode === 'noDb' || dbAccessMode === 'read' || dbAccessMode === 'longRead') {
    return 'read'
  }
  return req.method === 'GET' || req.method === 'HEAD' || req.method === 'OPTIONS' ? 'read' : 'write'
}

function isSystemApiHealthPath(req: Request): boolean {
  return req.path === '/health' || req.originalUrl.endsWith('/__aisys__/api/health')
}

async function isClientIpRateLimitAllowlisted(req: Request): Promise<boolean> {
  try {
    const decision = await inspectClientIpPolicy(clientIpKey(req), { ensureSnapshotLoaded: true })
    return decision.allowlisted
  } catch (error) {
    getRequestLogger().warn({
      event: 'system_api_rate_limit_allowlist_check_failed',
      err: error instanceof Error ? error : undefined,
      errorMessage: error instanceof Error ? undefined : String(error),
      method: req.method,
      path: req.path,
      originalUrl: sanitizeUrlForLog(req.originalUrl),
      clientIp: clientIpKey(req)
    }, '后台系统 API 白名单检查失败，本次请求继续执行限流')
    return false
  }
}

function clientIpKey(req: Request): string {
  return getRequestContext()?.clientIp ?? req.ip ?? req.socket.remoteAddress ?? 'unknown'
}

function integerSetting(value: unknown, key: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value)) {
    throw new Error(`${key} 必须是整数`)
  }
  if (value < 0 || value > 1_000_000) {
    throw new Error(`${key} 必须在 0 到 1000000 之间`)
  }
  return value
}

function redisStateClient(): Promise<RedisCommandClient> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return getRedisClient(redisUrl)
}

function redisFixedWindowRateLimitKey(storeName: string, key: string): string {
  return redisNamespacedKey(`juhe-ai:rate-limit:fixed:${redisKeyHash(storeName)}:${redisKeyHash(key)}`)
}

function redisKeyHash(value: string): string {
  return createHash('sha256').update(value).digest('base64url')
}

function redisFixedWindowRateLimitResult(value: unknown): {
  allowed: boolean
  retryAfterSeconds?: number
  bucketName?: string
  limit?: number
} {
  if (!Array.isArray(value)) {
    return { allowed: false, retryAfterSeconds: 1 }
  }
  const allowed = numericRedisResult(value[0]) === 1
  if (allowed) {
    return { allowed: true }
  }
  return {
    allowed: false,
    retryAfterSeconds: Math.max(1, numericRedisResult(value[1])),
    bucketName: typeof value[2] === 'string' ? value[2] : undefined,
    limit: numericRedisResult(value[3])
  }
}

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

const redisFixedWindowRateLimitScript = `
local now_ms = tonumber(ARGV[1])
local bucket_count = tonumber(ARGV[2])
local pending_counts = {}
local pending_resets = {}

for index = 1, bucket_count do
  local offset = 3 + (index - 1) * 3
  local store_name = ARGV[offset]
  local window_ms = tonumber(ARGV[offset + 1])
  local limit = tonumber(ARGV[offset + 2])
  if limit > 0 then
    local raw = redis.call('GET', KEYS[index])
    local count = 0
    local reset_at_ms = now_ms + window_ms
    if raw then
      local separator = string.find(raw, ':')
      if separator then
        count = tonumber(string.sub(raw, 1, separator - 1)) or 0
        reset_at_ms = tonumber(string.sub(raw, separator + 1)) or reset_at_ms
      end
    end
    if reset_at_ms <= now_ms then
      count = 0
      reset_at_ms = now_ms + window_ms
    end
    if count >= limit then
      return {0, math.max(1, math.ceil((reset_at_ms - now_ms) / 1000)), store_name, limit}
    end
    pending_counts[index] = count + 1
    pending_resets[index] = reset_at_ms
  end
end

for index = 1, bucket_count do
  local offset = 3 + (index - 1) * 3
  local limit = tonumber(ARGV[offset + 2])
  if limit > 0 then
    local reset_at_ms = pending_resets[index]
    local ttl_ms = math.max(1, reset_at_ms - now_ms)
    redis.call('SET', KEYS[index], tostring(pending_counts[index]) .. ':' .. tostring(reset_at_ms), 'PX', ttl_ms)
  end
end

return {1, 0, '', 0}
`
