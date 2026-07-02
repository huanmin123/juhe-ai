import { randomBytes } from 'node:crypto'

import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, resolveGroupSchedulingPolicy } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../../domain/types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { errorLogFields, logger } from '../../../shared/logger.js'
import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'

export type ClientIpConcurrencyRejectReason =
  | 'limit_reached'
  | 'queue_disabled'
  | 'queue_full'
  | 'timeout'
  | 'aborted'

export type ClientIpConcurrencyDecision =
  | {
    enabled: false
    acquired: true
    release: () => void
  }
  | {
    enabled: true
    acquired: true
    current: number
    limit: number
    waitedMs: number
    queued: boolean
    queueSizeBeforeAcquire: number
    release: () => void
  }
  | {
    enabled: true
    acquired: false
    reason: ClientIpConcurrencyRejectReason
    current: number
    limit: number
    waitedMs: number
    queueSize: number
  }

interface ClientIpConcurrencyState {
  key: string
  limit: number
  current: number
  items: ClientIpConcurrencyQueueItem[]
}

interface ClientIpConcurrencyQueueItem {
  id: number
  key: string
  enqueuedAtMs: number
  timer: NodeJS.Timeout
  signal?: AbortSignal
  abortListener?: () => void
  resolve: (decision: ClientIpConcurrencyDecision) => void
}

export interface ClientIpConcurrencyAcquireInput {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
  clientIp?: string
  policy?: GroupSchedulingPolicy
  signal?: AbortSignal
}

const states = new Map<string, ClientIpConcurrencyState>()
let nextQueueItemId = 1
const redisClientIpConcurrencyTtlMs = 2 * 60 * 60 * 1000
const redisClientIpConcurrencyRenewIntervalMs = Math.max(60_000, Math.floor(redisClientIpConcurrencyTtlMs / 4))

export function acquireHighConcurrencyClientIpSlot(input: ClientIpConcurrencyAcquireInput): Promise<ClientIpConcurrencyDecision> {
  const policy = resolveGroupSchedulingPolicy('high_concurrency', input.policy) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  const limit = normalizeNonNegativeInteger(policy.clientIpConcurrencyLimit, 0)
  const clientIp = input.clientIp?.trim()
  if (!clientIp || limit <= 0) {
    return Promise.resolve({
      enabled: false,
      acquired: true,
      release: noop
    })
  }
  if (input.signal?.aborted) {
    return Promise.resolve({
      enabled: true,
      acquired: false,
      reason: 'aborted',
      current: 0,
      limit,
      waitedMs: 0,
      queueSize: 0
    })
  }

  const key = clientIpConcurrencyKey(input.systemAccountId, input.groupId, input.apiKeyId, clientIp)
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return acquireRedisClientIpSlot(input, policy, key, limit)
  }
  const state = states.get(key) ?? createState(key)
  state.limit = limit
  states.set(key, state)
  if (state.current < limit) {
    return Promise.resolve(acquiredDecision(state, limit, 0, false, state.items.length))
  }
  if (policy.clientIpConcurrencyOverflowMode !== 'queue') {
    return Promise.resolve(rejectedDecision('limit_reached', state, limit, 0))
  }

  const maxQueueWaitMs = normalizeNonNegativeInteger(policy.maxQueueWaitMs, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueWaitMs)
  if (maxQueueWaitMs <= 0) {
    return Promise.resolve(rejectedDecision('queue_disabled', state, limit, 0))
  }
  const queueLimit = normalizePositiveInteger(policy.perApiKeyQueueLimit, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.perApiKeyQueueLimit)
  if (state.items.length >= queueLimit) {
    return Promise.resolve(rejectedDecision('queue_full', state, limit, 0))
  }

  return new Promise<ClientIpConcurrencyDecision>((resolve) => {
    const enqueuedAtMs = Date.now()
    const item: ClientIpConcurrencyQueueItem = {
      id: nextQueueItemId,
      key,
      enqueuedAtMs,
      timer: setTimeout(() => {
        completeQueueItem(item, rejectedDecision('timeout', state, limit, Date.now() - enqueuedAtMs))
      }, maxQueueWaitMs),
      signal: input.signal,
      resolve
    }
    nextQueueItemId += 1
    if (input.signal) {
      item.abortListener = () => {
        completeQueueItem(item, rejectedDecision('aborted', state, limit, Date.now() - enqueuedAtMs))
      }
      input.signal.addEventListener('abort', item.abortListener, { once: true })
    }
    state.items.push(item)
  })
}

export function clientIpConcurrencySnapshot(): Array<{ key: string; current: number; queueSize: number }> {
  if (runtimeConfig.runtimeStateDriver === 'redis') return []
  return [...states.values()].map((state) => ({
    key: state.key,
    current: state.current,
    queueSize: state.items.length
  }))
}

export function clearClientIpConcurrency(): void {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    states.clear()
    return
  }
  for (const state of states.values()) {
    for (const item of [...state.items]) {
      completeQueueItem(item, rejectedDecision('aborted', state, 1, Date.now() - item.enqueuedAtMs))
    }
  }
  states.clear()
}

async function acquireRedisClientIpSlot(
  input: ClientIpConcurrencyAcquireInput,
  policy: GroupSchedulingPolicy,
  key: string,
  limit: number
): Promise<ClientIpConcurrencyDecision> {
  const startedAtMs = Date.now()
  const firstSlotToken = redisClientIpConcurrencySlotToken()
  const firstAttempt = await tryAcquireRedisClientIpSlot(key, limit, true, firstSlotToken)
  if (firstAttempt.acquired) {
    return redisAcquiredDecision(key, firstSlotToken, firstAttempt.current, limit, 0, false)
  }
  if (policy.clientIpConcurrencyOverflowMode !== 'queue') {
    return redisRejectedDecision('limit_reached', firstAttempt.current, limit, 0)
  }
  const maxQueueWaitMs = normalizeNonNegativeInteger(policy.maxQueueWaitMs, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueWaitMs)
  if (maxQueueWaitMs <= 0) {
    return redisRejectedDecision('queue_disabled', firstAttempt.current, limit, 0)
  }
  const queueLimit = normalizePositiveInteger(policy.perApiKeyQueueLimit, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.perApiKeyQueueLimit)
  const deadlineAtMs = startedAtMs + maxQueueWaitMs
  const itemId = `${process.pid}:${startedAtMs}:${randomBytes(8).toString('hex')}`
  const queuedSlotToken = redisClientIpConcurrencySlotToken()
  const enqueueResult = await enqueueRedisClientIpQueueItem(key, itemId, deadlineAtMs, queueLimit)
  if (enqueueResult.status === 'queue_full') {
    return redisRejectedDecision('queue_full', firstAttempt.current, limit, 0, enqueueResult.queueSize)
  }
  let current = firstAttempt.current
  while (Date.now() < deadlineAtMs) {
    if (input.signal?.aborted) {
      const queueSize = await removeRedisClientIpQueueItem(key, itemId, Date.now())
      return redisRejectedDecision('aborted', current, limit, Date.now() - startedAtMs, queueSize)
    }
    const position = await redisClientIpQueuePosition(key, itemId, Date.now())
    if (!position.present) {
      return redisRejectedDecision('timeout', current, limit, Date.now() - startedAtMs, position.queueSize)
    }
    if (position.rank === 0) {
      const attempt = await tryAcquireRedisClientIpSlot(key, limit, false, queuedSlotToken)
      current = attempt.current
      if (attempt.acquired) {
        let queueSize: number
        try {
          queueSize = await removeRedisClientIpQueueItem(key, itemId, Date.now())
        } catch (error) {
          await releaseRedisClientIpSlotWithRetry(key, queuedSlotToken)
          throw error
        }
        return redisAcquiredDecision(key, queuedSlotToken, attempt.current, limit, Date.now() - startedAtMs, true, queueSize + 1)
      }
    }
    await delay(Math.min(100, Math.max(1, deadlineAtMs - Date.now())))
  }
  const queueSize = await removeRedisClientIpQueueItem(key, itemId, Date.now())
  return redisRejectedDecision('timeout', current, limit, Date.now() - startedAtMs, queueSize)
}

async function tryAcquireRedisClientIpSlot(
  key: string,
  limit: number,
  requireEmptyQueue: boolean,
  slotToken: string
): Promise<{ acquired: boolean; current: number }> {
  const result = await (await redisStateClient()).eval(redisAcquireClientIpConcurrencyScript, {
    keys: [
      redisClientIpConcurrencyKey(key),
      redisClientIpQueueKey(key)
    ],
    arguments: [
      String(limit),
      String(redisClientIpConcurrencyTtlMs),
      requireEmptyQueue ? '1' : '0',
      String(Date.now()),
      slotToken
    ]
  })
  const values = numericRedisArray(result)
  return {
    acquired: values[0] === 1,
    current: values[1] ?? 0
  }
}

function redisAcquiredDecision(
  key: string,
  slotToken: string,
  current: number,
  limit: number,
  waitedMs: number,
  queued: boolean,
  queueSizeBeforeAcquire = 0
): ClientIpConcurrencyDecision {
  let released = false
  const stopRenewal = startRedisClientIpSlotRenewal(key, slotToken)
  return {
    enabled: true,
    acquired: true,
    current,
    limit,
    waitedMs: Math.max(0, Math.trunc(waitedMs)),
    queued,
    queueSizeBeforeAcquire,
    release: () => {
      if (released) return
      released = true
      stopRenewal()
      void releaseRedisClientIpSlotWithRetry(key, slotToken)
    }
  }
}

function redisRejectedDecision(
  reason: ClientIpConcurrencyRejectReason,
  current: number,
  limit: number,
  waitedMs: number,
  queueSize = 0
): ClientIpConcurrencyDecision {
  return {
    enabled: true,
    acquired: false,
    reason,
    current,
    limit,
    waitedMs: Math.max(0, Math.trunc(waitedMs)),
    queueSize
  }
}

async function releaseRedisClientIpSlot(key: string, slotToken: string): Promise<void> {
  await (await redisStateClient()).eval(redisReleaseClientIpConcurrencyScript, {
    keys: [redisClientIpConcurrencyKey(key)],
    arguments: [slotToken]
  })
}

async function renewRedisClientIpSlot(key: string, slotToken: string): Promise<boolean> {
  const result = await (await redisStateClient()).eval(redisRenewClientIpConcurrencyScript, {
    keys: [redisClientIpConcurrencyKey(key)],
    arguments: [
      String(Date.now()),
      String(redisClientIpConcurrencyTtlMs),
      slotToken
    ]
  })
  return numericRedisArray(result)[0] === 1
}

function startRedisClientIpSlotRenewal(key: string, slotToken: string): () => void {
  let stopped = false
  let timer: NodeJS.Timeout | undefined
  const stop = (): void => {
    if (stopped) return
    stopped = true
    if (timer) {
      clearInterval(timer)
    }
  }
  timer = setInterval(() => {
    if (stopped) return
    void renewRedisClientIpSlot(key, slotToken)
      .then((renewed) => {
        if (!renewed && !stopped) {
          stop()
        }
      })
      .catch((error) => {
        if (stopped) return
        logger.warn(errorLogFields(error, {
          event: 'redis_client_ip_concurrency_renew_failed',
          key
        }), 'Redis Client-IP 并发槽续租失败')
      })
  }, redisClientIpConcurrencyRenewIntervalMs)
  timer.unref?.()
  return stop
}

async function releaseRedisClientIpSlotWithRetry(key: string, slotToken: string): Promise<void> {
  const delays = [0, 250, 1000, 5000]
  let lastError: unknown
  for (const delayMs of delays) {
    if (delayMs > 0) {
      await delay(delayMs)
    }
    try {
      await releaseRedisClientIpSlot(key, slotToken)
      return
    } catch (error) {
      lastError = error
    }
  }
  logger.error(errorLogFields(lastError, {
    event: 'redis_client_ip_concurrency_release_failed',
    key
  }), 'Redis Client-IP 并发槽释放失败')
}

function createState(key: string): ClientIpConcurrencyState {
  return {
    key,
    limit: 1,
    current: 0,
    items: []
  }
}

function acquiredDecision(
  state: ClientIpConcurrencyState,
  limit: number,
  waitedMs: number,
  queued: boolean,
  queueSizeBeforeAcquire: number
): ClientIpConcurrencyDecision {
  state.current += 1
  let released = false
  return {
    enabled: true,
    acquired: true,
    current: state.current,
    limit,
    waitedMs: Math.max(0, Math.trunc(waitedMs)),
    queued,
    queueSizeBeforeAcquire,
    release: () => {
      if (released) return
      released = true
      releaseClientIpSlot(state.key)
    }
  }
}

function rejectedDecision(
  reason: ClientIpConcurrencyRejectReason,
  state: ClientIpConcurrencyState,
  limit: number,
  waitedMs: number
): ClientIpConcurrencyDecision {
  return {
    enabled: true,
    acquired: false,
    reason,
    current: state.current,
    limit,
    waitedMs: Math.max(0, Math.trunc(waitedMs)),
    queueSize: state.items.length
  }
}

function releaseClientIpSlot(key: string): void {
  const state = states.get(key)
  if (!state) {
    return
  }
  state.current = Math.max(0, state.current - 1)
  wakeQueuedClientIpRequests(state)
  cleanupStateIfIdle(state)
}

function wakeQueuedClientIpRequests(state: ClientIpConcurrencyState): void {
  while (state.current < state.limit && state.items.length > 0) {
    const item = state.items[0]
    if (!item) {
      return
    }
    if (item.signal?.aborted) {
      completeQueueItem(item, rejectedDecision('aborted', state, state.limit, Date.now() - item.enqueuedAtMs))
      continue
    }
    completeQueueItem(item, acquiredDecision(state, state.limit, Date.now() - item.enqueuedAtMs, true, state.items.length))
    return
  }
}

function completeQueueItem(item: ClientIpConcurrencyQueueItem, decision: ClientIpConcurrencyDecision): void {
  const state = states.get(item.key)
  if (state) {
    const index = state.items.findIndex((candidate) => candidate.id === item.id)
    if (index >= 0) {
      state.items.splice(index, 1)
    }
  }
  clearTimeout(item.timer)
  if (item.signal && item.abortListener) {
    item.signal.removeEventListener('abort', item.abortListener)
  }
  item.resolve(decision)
  if (state) {
    cleanupStateIfIdle(state)
  }
}

function cleanupStateIfIdle(state: ClientIpConcurrencyState): void {
  if (state.current <= 0 && state.items.length === 0) {
    states.delete(state.key)
  }
}

function clientIpConcurrencyKey(systemAccountId: string, groupId: string, apiKeyId: string | undefined, clientIp: string): string {
  return `${systemAccountId}:${groupId}:${apiKeyId?.trim() || 'internal'}:${clientIp}`
}

function redisClientIpConcurrencyKey(key: string): string {
  return `juhe-ai:client-ip-concurrency:${Buffer.from(key).toString('base64url')}`
}

function redisClientIpQueueKey(key: string): string {
  return `juhe-ai:client-ip-concurrency-queue:${Buffer.from(key).toString('base64url')}`
}

function redisClientIpConcurrencySlotToken(): string {
  return `${process.pid}:${Date.now()}:${randomBytes(8).toString('hex')}`
}

async function enqueueRedisClientIpQueueItem(
  key: string,
  itemId: string,
  deadlineAtMs: number,
  queueLimit: number
): Promise<{ status: 'enqueued' | 'queue_full'; queueSize: number }> {
  const nowMs = Date.now()
  const result = await (await redisStateClient()).eval(redisClientIpQueueEnqueueScript, {
    keys: [redisClientIpQueueKey(key)],
    arguments: [
      itemId,
      String(Math.max(0, Math.trunc(deadlineAtMs))),
      String(Math.max(0, Math.trunc(nowMs))),
      String(Math.max(1, Math.trunc(queueLimit))),
      String(Math.max(1, Math.trunc(deadlineAtMs - nowMs + 60_000)))
    ]
  })
  const values = numericRedisArray(result)
  return {
    status: values[0] === 1 ? 'enqueued' : 'queue_full',
    queueSize: values[1] ?? 0
  }
}

async function redisClientIpQueuePosition(
  key: string,
  itemId: string,
  nowMs: number
): Promise<{ present: boolean; rank: number; queueSize: number }> {
  const result = await (await redisStateClient()).eval(redisClientIpQueuePositionScript, {
    keys: [redisClientIpQueueKey(key)],
    arguments: [
      itemId,
      String(Math.max(0, Math.trunc(nowMs)))
    ]
  })
  const values = numericRedisArray(result)
  return {
    present: values[0] === 1,
    rank: values[1] ?? -1,
    queueSize: values[2] ?? 0
  }
}

async function removeRedisClientIpQueueItem(key: string, itemId: string, nowMs: number): Promise<number> {
  const result = await (await redisStateClient()).eval(redisClientIpQueueRemoveScript, {
    keys: [redisClientIpQueueKey(key)],
    arguments: [
      itemId,
      String(Math.max(0, Math.trunc(nowMs)))
    ]
  })
  const values = numericRedisArray(result)
  return values[0] ?? 0
}

function redisStateClient(): Promise<RedisCommandClient> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return getRedisClient(redisUrl)
}

function normalizeNonNegativeInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : fallback
}

function normalizePositiveInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(1, Math.trunc(numeric)) : fallback
}

function noop(): void {}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, Math.max(0, ms)))
}

function numericRedisArray(value: unknown): number[] {
  if (!Array.isArray(value)) return []
  return value.map((item) => {
    if (typeof item === 'number' && Number.isFinite(item)) return item
    if (typeof item === 'bigint') return Number(item)
    const parsed = Number(item)
    return Number.isFinite(parsed) ? parsed : 0
  })
}

const redisAcquireClientIpConcurrencyScript = `
local limit = tonumber(ARGV[1])
local slot_ttl_ms = tonumber(ARGV[2])
local require_empty_queue = ARGV[3] == '1'
local now_ms = tonumber(ARGV[4])
local slot_token = ARGV[5]
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local current = tonumber(redis.call('ZCARD', KEYS[1]) or '0') or 0
if require_empty_queue then
  redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
  if redis.call('ZCARD', KEYS[2]) > 0 then
    return {0, current}
  end
end
if current >= limit then
  return {0, current}
end
redis.call('ZADD', KEYS[1], now_ms + slot_ttl_ms, slot_token)
redis.call('PEXPIRE', KEYS[1], slot_ttl_ms)
return {1, current + 1}
`

const redisReleaseClientIpConcurrencyScript = `
local slot_token = ARGV[1]
redis.call('ZREM', KEYS[1], slot_token)
if redis.call('ZCARD', KEYS[1]) == 0 then
  redis.call('DEL', KEYS[1])
end
return 1
`

const redisRenewClientIpConcurrencyScript = `
local now_ms = tonumber(ARGV[1])
local slot_ttl_ms = tonumber(ARGV[2])
local slot_token = ARGV[3]
local current_score = tonumber(redis.call('ZSCORE', KEYS[1], slot_token))
if not current_score then
  return {0}
end
if current_score <= now_ms then
  redis.call('ZREM', KEYS[1], slot_token)
  return {0}
end
redis.call('ZADD', KEYS[1], now_ms + slot_ttl_ms, slot_token)
redis.call('PEXPIRE', KEYS[1], slot_ttl_ms)
return {1}
`

const redisClientIpQueueEnqueueScript = `
local item_id = ARGV[1]
local deadline_at_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local queue_limit = tonumber(ARGV[4])
local ttl_ms = tonumber(ARGV[5])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local queue_size = redis.call('ZCARD', KEYS[1])
if queue_size >= queue_limit then
  return {0, queue_size}
end
redis.call('ZADD', KEYS[1], deadline_at_ms, item_id)
redis.call('PEXPIRE', KEYS[1], ttl_ms)
return {1, queue_size + 1}
`

const redisClientIpQueuePositionScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
local rank = redis.call('ZRANK', KEYS[1], item_id)
if rank == false then
  return {0, -1, redis.call('ZCARD', KEYS[1])}
end
return {1, rank, redis.call('ZCARD', KEYS[1])}
`

const redisClientIpQueueRemoveScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREM', KEYS[1], item_id)
return {redis.call('ZCARD', KEYS[1])}
`
