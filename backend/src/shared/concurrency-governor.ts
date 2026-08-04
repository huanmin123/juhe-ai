import { randomUUID } from 'node:crypto'

import pLimit from 'p-limit'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import { runRedisOperationWithDeadline } from './redis-client.js'
import { redisNamespacedKey } from './redis-namespace.js'

export interface GlobalConcurrencySlotOptions {
  signal?: AbortSignal
}

export interface GlobalConcurrencyLease {
  release(): void
}

const globalLimiter = pLimit(runtimeConfig.concurrency.globalMax)
const redisGlobalConcurrencyKey = redisNamespacedKey('juhe-ai:concurrency:global:v1')
const redisGlobalConcurrencyOwnerId = `${runtimeConfig.instanceId}|${randomUUID()}`
const redisOperationTimeoutMs = 3_000

// Every governed I/O operation acquires this physical-I/O-boundary slot. In
// performance mode, Redis is the fact source so every gateway and worker
// replica shares one capacity instead of each process independently using it.
export async function acquireGlobalConcurrencySlot(options: GlobalConcurrencySlotOptions = {}): Promise<GlobalConcurrencyLease> {
  options.signal?.throwIfAborted()
  if (runtimeConfig.runtimeStateDriver === 'memory') {
    return await acquireProcessLocalConcurrencySlot(options)
  }
  return await acquireRedisConcurrencySlot(options)
}

export async function runWithGlobalBackgroundConcurrencySlot<T>(
  task: () => Promise<T>,
  options: GlobalConcurrencySlotOptions = {}
): Promise<T> {
  const lease = await acquireGlobalConcurrencySlot(options)
  try {
    return await task()
  } finally {
    lease.release()
  }
}

async function acquireProcessLocalConcurrencySlot(options: GlobalConcurrencySlotOptions): Promise<GlobalConcurrencyLease> {
  let start!: () => void
  let rejectStart!: (error: unknown) => void
  let release!: () => void
  let cancelled = false
  const started = new Promise<void>((resolve, reject) => {
    start = resolve
    rejectStart = reject
  })
  const released = new Promise<void>((resolve) => { release = resolve })
  const onAbort = () => {
    cancelled = true
    rejectStart(options.signal?.reason ?? new Error('全局并发槽获取已取消'))
  }
  options.signal?.addEventListener('abort', onAbort, { once: true })
  void globalLimiter(async () => {
    try {
      if (cancelled) return
      options.signal?.throwIfAborted()
      start()
      await released
    } catch (error) {
      rejectStart(error)
    }
  })
  try {
    await started
  } finally {
    options.signal?.removeEventListener('abort', onAbort)
  }
  let done = false
  return {
    release: () => {
      if (done) return
      done = true
      release()
    }
  }
}

async function acquireRedisConcurrencySlot(options: GlobalConcurrencySlotOptions): Promise<GlobalConcurrencyLease> {
  const redisUrl = requiredRedisStateUrl()
  const token = `${redisGlobalConcurrencyOwnerId}|${randomUUID()}`
  while (true) {
    options.signal?.throwIfAborted()
    const acquired = await runRedisOperationWithDeadline(redisUrl, {
      operationName: 'Redis 全局并发槽获取',
      timeoutMs: redisOperationTimeoutMs,
      signal: options.signal
    }, async (client) => Number(await client.eval(redisAcquireGlobalConcurrencyScript, {
      keys: [redisGlobalConcurrencyKey],
      arguments: [
        String(runtimeConfig.concurrency.globalMax),
        String(runtimeConfig.concurrency.globalLeaseDurationMs),
        token
      ]
    })) === 1)
    if (acquired) return createRedisConcurrencyLease(redisUrl, token)
    await delayWithAbort(runtimeConfig.concurrency.globalAcquirePollMs, options.signal)
  }
}

function createRedisConcurrencyLease(redisUrl: string, token: string): GlobalConcurrencyLease {
  let released = false
  let refreshInFlight = false
  const refreshIntervalMs = Math.max(1_000, Math.floor(runtimeConfig.concurrency.globalLeaseDurationMs / 3))
  const refreshTimer = setInterval(() => {
    if (released || refreshInFlight) return
    refreshInFlight = true
    void refreshRedisConcurrencyLease(redisUrl, token).then((renewed) => {
      if (!renewed && !released) {
        logger.error({
          event: 'redis_global_concurrency_lease_lost',
          globalConcurrencyMax: runtimeConfig.concurrency.globalMax
        }, 'Redis 全局并发槽租约已丢失；正在执行的任务无法再保证占用共享容量')
      }
    }).catch((error) => {
      logger.error(errorLogFields(error, {
        event: 'redis_global_concurrency_lease_refresh_failed',
        globalConcurrencyMax: runtimeConfig.concurrency.globalMax
      }), 'Redis 全局并发槽续租失败；不会降级为进程内并发池')
    }).finally(() => {
      refreshInFlight = false
    })
  }, refreshIntervalMs)
  refreshTimer.unref?.()
  return {
    release: () => {
      if (released) return
      released = true
      clearInterval(refreshTimer)
      void releaseRedisConcurrencyLease(redisUrl, token).catch((error) => {
        logger.error(errorLogFields(error, {
          event: 'redis_global_concurrency_lease_release_failed',
          globalConcurrencyMax: runtimeConfig.concurrency.globalMax
        }), 'Redis 全局并发槽释放失败；将等待租约自然过期')
      })
    }
  }
}

async function refreshRedisConcurrencyLease(redisUrl: string, token: string): Promise<boolean> {
  return await runRedisOperationWithDeadline(redisUrl, {
    operationName: 'Redis 全局并发槽续租',
    timeoutMs: redisOperationTimeoutMs
  }, async (client) => Number(await client.eval(redisRefreshGlobalConcurrencyScript, {
    keys: [redisGlobalConcurrencyKey],
    arguments: [String(runtimeConfig.concurrency.globalLeaseDurationMs), token]
  })) === 1)
}

async function releaseRedisConcurrencyLease(redisUrl: string, token: string): Promise<void> {
  await runRedisOperationWithDeadline(redisUrl, {
    operationName: 'Redis 全局并发槽释放',
    timeoutMs: redisOperationTimeoutMs
  }, async (client) => {
    await client.eval(redisReleaseGlobalConcurrencyScript, {
      keys: [redisGlobalConcurrencyKey],
      arguments: [token]
    })
  })
}

function requiredRedisStateUrl(): string {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return redisUrl
}

async function delayWithAbort(delayMs: number, signal?: AbortSignal): Promise<void> {
  signal?.throwIfAborted()
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort)
      resolve()
    }, delayMs)
    timer.unref?.()
    const onAbort = () => {
      clearTimeout(timer)
      reject(signal?.reason ?? new Error('全局并发槽获取已取消'))
    }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

const redisAcquireGlobalConcurrencyScript = `
local max_slots = tonumber(ARGV[1])
local lease_duration_ms = tonumber(ARGV[2])
local token = ARGV[3]
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local current = tonumber(redis.call('ZCARD', KEYS[1]) or '0') or 0
if current >= max_slots then return 0 end
redis.call('ZADD', KEYS[1], now_ms + lease_duration_ms, token)
redis.call('PEXPIRE', KEYS[1], lease_duration_ms)
return 1
`

const redisRefreshGlobalConcurrencyScript = `
local lease_duration_ms = tonumber(ARGV[1])
local token = ARGV[2]
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local expires_at_ms = tonumber(redis.call('ZSCORE', KEYS[1], token))
if expires_at_ms == nil or expires_at_ms <= now_ms then return 0 end
redis.call('ZADD', KEYS[1], now_ms + lease_duration_ms, token)
redis.call('PEXPIRE', KEYS[1], lease_duration_ms)
return 1
`

const redisReleaseGlobalConcurrencyScript = `
redis.call('ZREM', KEYS[1], ARGV[1])
if redis.call('ZCARD', KEYS[1]) == 0 then redis.call('DEL', KEYS[1]) end
return 1
`
