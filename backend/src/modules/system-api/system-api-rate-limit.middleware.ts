import type { NextFunction, Request, Response } from 'express'

import { getRequestAuthContext } from '../auth/request-context.js'
import { getSettingsAsync } from '../../storage/repositories.js'
import { getRequestContext, getRequestLogger, sanitizeUrlForLog } from '../../shared/request-context.js'

type MethodClass = 'read' | 'write'
type LimiterScope = 'ip' | 'user'

interface SystemApiRateLimitSettings {
  enabled: boolean
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

  if (!settings.enabled) {
    next()
    return
  }

  const methodClass = methodClassFor(req.method)
  const clientIp = clientIpKey(req)
  const key = `${clientIp}:${methodClass}`
  const decision = checkRateLimit([
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

  if (!settings.enabled) {
    next()
    return
  }

  const authContext = getRequestAuthContext()
  if (!authContext) {
    next()
    return
  }

  const methodClass = methodClassFor(req.method)
  const limit = methodClass === 'read' ? settings.userReadPerMinute : settings.userWritePerMinute
  const decision = checkRateLimit([
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
    enabled: booleanSetting(settings.systemApiRateLimitEnabled, 'systemApiRateLimitEnabled'),
    ipReadPerMinute: integerSetting(settings.systemApiRateLimitIpReadPerMinute, 'systemApiRateLimitIpReadPerMinute'),
    ipReadBurstPer10Seconds: integerSetting(settings.systemApiRateLimitIpReadBurstPer10Seconds, 'systemApiRateLimitIpReadBurstPer10Seconds'),
    ipWritePerMinute: integerSetting(settings.systemApiRateLimitIpWritePerMinute, 'systemApiRateLimitIpWritePerMinute'),
    ipWriteBurstPer10Seconds: integerSetting(settings.systemApiRateLimitIpWriteBurstPer10Seconds, 'systemApiRateLimitIpWriteBurstPer10Seconds'),
    userReadPerMinute: integerSetting(settings.systemApiRateLimitUserReadPerMinute, 'systemApiRateLimitUserReadPerMinute'),
    userWritePerMinute: integerSetting(settings.systemApiRateLimitUserWritePerMinute, 'systemApiRateLimitUserWritePerMinute')
  }
}

function checkRateLimit(buckets: RateLimitBucketInput[], nowMs: number): RateLimitDecision {
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

function methodClassFor(method: string): MethodClass {
  return method === 'GET' || method === 'HEAD' || method === 'OPTIONS' ? 'read' : 'write'
}

function isSystemApiHealthPath(req: Request): boolean {
  return req.path === '/health' || req.originalUrl.endsWith('/__aisys__/api/health')
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

function booleanSetting(value: unknown, key: string): boolean {
  if (typeof value !== 'boolean') {
    throw new Error(`${key} 必须是布尔值`)
  }
  return value
}
