import { randomBytes } from 'node:crypto'

import { runtimeConfig } from '../../../config/runtime.js'
import { getRedisClient, invalidateRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { logger } from '../../../shared/logger.js'
import { userRequestLimitCounter, type UserRequestLimitDirtySnapshot, type UserRequestLimitSyncResult } from './user-request-limit-counter.js'

const syncIntervalMs = 1_000
const maxBatchSize = 1_024
const errorLogIntervalMs = 30_000
const capacityLogIntervalMs = 30_000
const redisCommandTimeoutMs = 3_000
const maxRetryBackoffMs = 30_000
const serverInstanceId = `${process.pid}-${randomBytes(6).toString('hex')}`
let started = false
let syncInFlight = false
let coordinatorTimer: NodeJS.Timeout | undefined
let consecutiveFailures = 0
let nextSyncAttemptAtMs = 0
let lastErrorLogAtMs = 0
let lastCapacityLogAtMs = 0
let lastLoggedCapacityEvictions = 0

export const userRequestLimitRedisSyncScript = `
local result = {}
local field = ARGV[1]
for index = 1, #KEYS do
  local count = ARGV[index * 2]
  local ttl = tonumber(ARGV[(index * 2) + 1])
  local previous = tonumber(redis.call('HGET', KEYS[index], field)) or 0
  local total = tonumber(redis.call('HGET', KEYS[index], '__total')) or 0
  local delta = tonumber(count) - previous
  if delta > 0 then
    total = redis.call('HINCRBY', KEYS[index], '__total', delta)
    redis.call('HSET', KEYS[index], field, count)
  end
  redis.call('PEXPIRE', KEYS[index], ttl)
  result[index] = total
end
return result
`

export function startUserRequestLimitCoordinator(): void {
  if (started) return
  started = true
  coordinatorTimer = setInterval(() => {
    userRequestLimitCounter.cleanupExpired()
    logCapacityPressure()
    if (runtimeConfig.runtimeStateDriver === 'redis') void synchronizeDirtyCounters()
  }, syncIntervalMs)
  coordinatorTimer.unref?.()
}

export async function stopUserRequestLimitCoordinator(timeoutMs = redisCommandTimeoutMs): Promise<boolean> {
  if (coordinatorTimer) clearInterval(coordinatorTimer)
  coordinatorTimer = undefined
  started = false
  const deadlineAtMs = Date.now() + Math.max(1, timeoutMs)
  while (syncInFlight && Date.now() < deadlineAtMs) {
    await delay(20)
  }
  if (syncInFlight || runtimeConfig.runtimeStateDriver !== 'redis') return !syncInFlight
  nextSyncAttemptAtMs = 0
  try {
    while (userRequestLimitCounter.stats().dirtyEntries > 0 && Date.now() < deadlineAtMs) {
      const dirtyBefore = userRequestLimitCounter.stats().dirtyEntries
      await withTimeout(synchronizeDirtyCounters(true), Math.max(1, deadlineAtMs - Date.now()), '用户请求限制退出同步超时')
      if (userRequestLimitCounter.stats().dirtyEntries >= dirtyBefore) return false
    }
    return userRequestLimitCounter.stats().dirtyEntries === 0
  } catch {
    return false
  }
}

async function synchronizeDirtyCounters(force = false): Promise<void> {
  if (syncInFlight) return
  if (!force && Date.now() < nextSyncAttemptAtMs) return
  const stateUrl = runtimeConfig.redis.stateUrl
  if (!stateUrl) return
  const batch = userRequestLimitCounter.dirtySnapshot(maxBatchSize)
  if (!batch.length) return

  syncInFlight = true
  let client: RedisCommandClient | undefined
  try {
    client = await withTimeout(getRedisClient(stateUrl), redisCommandTimeoutMs, '用户请求限制 Redis 连接超时')
    const raw = await withTimeout(client.eval(userRequestLimitRedisSyncScript, {
      keys: batch.map(redisKey),
      arguments: [
        serverInstanceId,
        ...batch.flatMap((entry) => [String(entry.localCount), String(entry.redisTtlMs)])
      ]
    }), redisCommandTimeoutMs, '用户请求限制 Redis 命令超时')
    const totals = Array.isArray(raw) ? raw : []
    const results: UserRequestLimitSyncResult[] = batch.map((entry, index) => ({
      entryKey: entry.entryKey,
      sentLocalCount: entry.localCount,
      remoteTotal: numericValue(totals[index])
    }))
    userRequestLimitCounter.applySyncResults(results)
    consecutiveFailures = 0
    nextSyncAttemptAtMs = 0
  } catch (error) {
    const nowMs = Date.now()
    consecutiveFailures += 1
    nextSyncAttemptAtMs = nowMs + Math.min(maxRetryBackoffMs, syncIntervalMs * (2 ** Math.min(5, consecutiveFailures - 1)))
    if (client) await invalidateRedisClient(stateUrl, client)
    if (nowMs - lastErrorLogAtMs >= errorLogIntervalMs) {
      lastErrorLogAtMs = nowMs
      logger.warn({
        event: 'gateway_user_request_limit_redis_sync_failed',
        error,
        consecutiveFailures,
        retryAfterMs: Math.max(0, nextSyncAttemptAtMs - nowMs)
      }, '用户请求限制 Redis 后台同步失败，继续使用本机内存计数')
    }
  } finally {
    syncInFlight = false
  }
}

function logCapacityPressure(): void {
  const stats = userRequestLimitCounter.stats()
  if (stats.capacityEvictions <= lastLoggedCapacityEvictions) return
  const nowMs = Date.now()
  if (nowMs - lastCapacityLogAtMs < capacityLogIntervalMs) return
  lastCapacityLogAtMs = nowMs
  lastLoggedCapacityEvictions = stats.capacityEvictions
  logger.warn({
    event: 'gateway_user_request_limit_capacity_exhausted',
    entries: stats.entries,
    dirtyEntries: stats.dirtyEntries,
    capacityEvictions: stats.capacityEvictions
  }, '用户请求限制本机计数容量已满，已淘汰最旧桶以维持固定内存上限')
}

function redisKey(entry: UserRequestLimitDirtySnapshot): string {
  return `${runtimeConfig.redis.namespace}:gateway:user-request-limit:${entry.window}:${entry.bucket}:${entry.systemAccountId}`
}

function numericValue(value: unknown): number {
  const normalized = Number(value)
  return Number.isFinite(normalized) && normalized >= 0 ? normalized : 0
}

async function withTimeout<T>(promise: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timer = setTimeout(() => reject(new Error(message)), Math.max(1, timeoutMs))
      })
    ])
  } finally {
    if (timer) clearTimeout(timer)
    promise.catch(() => undefined)
  }
}

function delay(timeoutMs: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, timeoutMs))
}
