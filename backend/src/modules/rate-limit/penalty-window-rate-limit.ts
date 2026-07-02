import { createHash } from 'node:crypto'

import { runtimeConfig } from '../../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from '../../shared/redis-client.js'

export interface PenaltyWindowRateLimitRule {
  windowSeconds: number
  maxRequests: number
}

export interface PenaltyWindowRateLimitStore {
  name: string
  entries: Map<string, PenaltyWindowRateLimitEntry>
  maxEntries: number
  cleanupIntervalMs: number
  maxIdleMs: number
  maxPenaltyMs: number
  nextCleanupAtMs: number
}

export interface PenaltyWindowRateLimitDecision {
  allowed: boolean
  retryAfterSeconds?: number
  rule?: PenaltyWindowRateLimitRule
  storeName?: string
  limit?: number
}

interface PenaltyWindowRateLimitEntry {
  windowStartedAt: number
  count: number
  penaltyMs: number
  blockedUntilMs?: number
  lastSeenAtMs: number
}

interface InspectPenaltyWindowRateLimitBucket {
  allowed: boolean
  retryAfterSeconds: number
  commit: () => void
  rule: PenaltyWindowRateLimitRule
}

const defaultCleanupIntervalMs = 60_000
const defaultMaxEntries = 20_000
const defaultMaxIdleMs = 86_400_000
const defaultMaxPenaltyMs = 15 * 60_000

export function createPenaltyWindowRateLimitStore(input: {
  name: string
  maxEntries?: number
  cleanupIntervalMs?: number
  maxIdleMs?: number
  maxPenaltyMs?: number
}): PenaltyWindowRateLimitStore {
  return {
    name: input.name,
    entries: new Map(),
    maxEntries: positiveInteger(input.maxEntries, defaultMaxEntries),
    cleanupIntervalMs: positiveInteger(input.cleanupIntervalMs, defaultCleanupIntervalMs),
    maxIdleMs: positiveInteger(input.maxIdleMs, defaultMaxIdleMs),
    maxPenaltyMs: positiveInteger(input.maxPenaltyMs, defaultMaxPenaltyMs),
    nextCleanupAtMs: 0
  }
}

export function consumePenaltyWindowRateLimit(input: {
  store: PenaltyWindowRateLimitStore
  scopeKey: string
  rules: readonly PenaltyWindowRateLimitRule[]
  nowMs?: number
}): PenaltyWindowRateLimitDecision {
  assertPenaltyWindowMemoryStoreAllowed('consumePenaltyWindowRateLimit')
  const nowMs = input.nowMs ?? Date.now()
  cleanupPenaltyWindowRateLimitStore(input.store, nowMs)
  const buckets = input.rules
    .filter((rule) => rule.maxRequests > 0 && rule.windowSeconds > 0)
    .map((rule) => inspectPenaltyWindowRateLimitBucket(input.store, input.scopeKey, rule, nowMs))
  const blocked = buckets.find((bucket) => !bucket.allowed)
  if (blocked) {
    return {
      allowed: false,
      retryAfterSeconds: blocked.retryAfterSeconds,
      rule: blocked.rule,
      storeName: input.store.name,
      limit: blocked.rule.maxRequests
    }
  }

  for (const bucket of buckets) {
    bucket.commit()
  }
  return { allowed: true }
}

export async function consumePenaltyWindowRateLimitAsync(input: {
  store: PenaltyWindowRateLimitStore
  scopeKey: string
  rules: readonly PenaltyWindowRateLimitRule[]
  nowMs?: number
}): Promise<PenaltyWindowRateLimitDecision> {
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return consumePenaltyWindowRateLimit(input)
  }
  const nowMs = input.nowMs ?? Date.now()
  const activeRules = input.rules.filter((rule) => rule.maxRequests > 0 && rule.windowSeconds > 0)
  return consumeRedisPenaltyWindowRateLimit(input.store, input.scopeKey, activeRules, nowMs)
}

export function clearPenaltyWindowRateLimitStore(store: PenaltyWindowRateLimitStore): void {
  store.entries.clear()
  store.nextCleanupAtMs = 0
}

function inspectPenaltyWindowRateLimitBucket(
  store: PenaltyWindowRateLimitStore,
  scopeKey: string,
  rule: PenaltyWindowRateLimitRule,
  nowMs: number
): InspectPenaltyWindowRateLimitBucket {
  const windowMs = rule.windowSeconds * 1000
  const windowStartedAt = Math.floor(nowMs / windowMs) * windowMs
  const key = `${scopeKey}:${rule.windowSeconds}:${rule.maxRequests}`
  const current = store.entries.get(key)
  const entry = current && current.windowStartedAt === windowStartedAt
    ? current
    : {
        windowStartedAt,
        count: 0,
        penaltyMs: current?.penaltyMs ?? 0,
        blockedUntilMs: current?.blockedUntilMs,
        lastSeenAtMs: nowMs
      }
  entry.lastSeenAtMs = nowMs

  if (entry.blockedUntilMs && entry.blockedUntilMs > nowMs) {
    openPenaltyBlock(store, entry, windowMs, nowMs)
    store.entries.set(key, entry)
    return blockedBucket(rule, entry.blockedUntilMs - nowMs)
  }

  entry.blockedUntilMs = undefined
  if (entry.count >= rule.maxRequests) {
    openPenaltyBlock(store, entry, windowMs, nowMs)
    store.entries.set(key, entry)
    return blockedBucket(rule, (entry.blockedUntilMs ?? nowMs) - nowMs)
  }

  return {
    allowed: true,
    retryAfterSeconds: 0,
    rule,
    commit: () => {
      entry.count += 1
      entry.lastSeenAtMs = nowMs
      store.entries.set(key, entry)
      trimPenaltyWindowRateLimitStore(store, nowMs)
    }
  }
}

async function consumeRedisPenaltyWindowRateLimit(
  store: PenaltyWindowRateLimitStore,
  scopeKey: string,
  rules: readonly PenaltyWindowRateLimitRule[],
  nowMs: number
): Promise<PenaltyWindowRateLimitDecision> {
  if (!rules.length) {
    return { allowed: true }
  }
  const result = await (await redisStateClient()).eval(redisPenaltyWindowRateLimitScript, {
    keys: rules.map((rule) => redisPenaltyWindowRateLimitKey(store.name, scopeKey, rule)),
    arguments: [
      String(Math.trunc(nowMs)),
      String(rules.length),
      ...rules.flatMap((rule) => {
        const windowMs = rule.windowSeconds * 1000
        const maxPenaltyMs = Math.max(windowMs, store.maxPenaltyMs)
        return [
          String(windowMs),
          String(Math.floor(nowMs / windowMs) * windowMs),
          String(rule.maxRequests),
          String(maxPenaltyMs),
          String(Math.max(store.maxIdleMs, maxPenaltyMs, windowMs))
        ]
      })
    ]
  })
  const values = numericRedisArray(result)
  const allowed = values[0] === 1
  if (allowed) {
    return { allowed: true }
  }
  const ruleIndex = Math.max(1, Math.trunc(values[2] ?? 1))
  const rule = rules[ruleIndex - 1] ?? rules[0]!
  return {
    allowed: false,
    retryAfterSeconds: Math.max(1, Math.ceil((values[1] ?? rule.windowSeconds * 1000) / 1000)),
    rule,
    storeName: store.name,
    limit: rule.maxRequests
  }
}

function openPenaltyBlock(
  store: PenaltyWindowRateLimitStore,
  entry: PenaltyWindowRateLimitEntry,
  windowMs: number,
  nowMs: number
): void {
  const maxPenaltyMs = Math.max(windowMs, store.maxPenaltyMs)
  const basePenaltyMs = entry.penaltyMs > 0 ? entry.penaltyMs * 2 : windowMs
  entry.penaltyMs = Math.min(maxPenaltyMs, basePenaltyMs)
  entry.blockedUntilMs = nowMs + entry.penaltyMs
}

function blockedBucket(rule: PenaltyWindowRateLimitRule, retryAfterMs: number): InspectPenaltyWindowRateLimitBucket {
  return {
    allowed: false,
    retryAfterSeconds: Math.max(1, Math.ceil(retryAfterMs / 1000)),
    commit: () => {},
    rule
  }
}

function cleanupPenaltyWindowRateLimitStore(store: PenaltyWindowRateLimitStore, nowMs: number): void {
  if (store.nextCleanupAtMs > nowMs && store.entries.size <= store.maxEntries) {
    return
  }
  for (const [key, entry] of store.entries) {
    if (entry.blockedUntilMs && entry.blockedUntilMs > nowMs) {
      continue
    }
    if (nowMs - entry.lastSeenAtMs > store.maxIdleMs) {
      store.entries.delete(key)
    }
  }
  store.nextCleanupAtMs = nowMs + store.cleanupIntervalMs
  trimPenaltyWindowRateLimitStore(store, nowMs)
}

function trimPenaltyWindowRateLimitStore(store: PenaltyWindowRateLimitStore, nowMs: number): void {
  if (store.entries.size <= store.maxEntries) {
    return
  }
  const targetSize = Math.floor(store.maxEntries * 0.9)
  for (const [key, entry] of store.entries) {
    if ((!entry.blockedUntilMs || entry.blockedUntilMs <= nowMs) || store.entries.size > targetSize) {
      store.entries.delete(key)
    }
    if (store.entries.size <= targetSize) {
      break
    }
  }
}

function positiveInteger(value: number | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
    ? Math.trunc(value)
    : fallback
}

function assertPenaltyWindowMemoryStoreAllowed(operation: string): void {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return
  throw new Error(`高性能模式禁止使用本机 penalty window 限流状态：${operation} 必须使用 Redis async 限流入口`)
}

function redisStateClient(): Promise<RedisCommandClient> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return getRedisClient(redisUrl)
}

function redisPenaltyWindowRateLimitKey(
  storeName: string,
  scopeKey: string,
  rule: PenaltyWindowRateLimitRule
): string {
  return [
    'juhe-ai:rate-limit:penalty',
    redisKeyHash(storeName),
    redisKeyHash(scopeKey),
    rule.windowSeconds,
    rule.maxRequests
  ].join(':')
}

function redisKeyHash(value: string): string {
  return createHash('sha256').update(value).digest('base64url')
}

function numericRedisArray(value: unknown): number[] {
  if (Array.isArray(value)) {
    return value.map(numericRedisResult)
  }
  return []
}

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

const redisPenaltyWindowRateLimitScript = `
local now_ms = tonumber(ARGV[1])
local rule_count = tonumber(ARGV[2])
local counts = {}
local penalty_values = {}
local window_started_values = {}
local ttl_values = {}
local blocked_index = 0
local blocked_retry_ms = 0

for index = 1, rule_count do
  local offset = 3 + (index - 1) * 5
  local window_ms = tonumber(ARGV[offset])
  local window_started_at = tonumber(ARGV[offset + 1])
  local max_requests = tonumber(ARGV[offset + 2])
  local max_penalty_ms = tonumber(ARGV[offset + 3])
  local ttl_ms = tonumber(ARGV[offset + 4])
  local values = redis.call('HMGET', KEYS[index], 'windowStartedAt', 'count', 'penaltyMs', 'blockedUntilMs')
  local stored_window_started_at = tonumber(values[1])
  local count = 0
  if stored_window_started_at == window_started_at then
    count = tonumber(values[2]) or 0
  end
  local penalty_ms = tonumber(values[3]) or 0
  local blocked_until_ms = tonumber(values[4]) or 0
  counts[index] = count
  penalty_values[index] = penalty_ms
  window_started_values[index] = window_started_at
  ttl_values[index] = ttl_ms

  if blocked_until_ms > now_ms or count >= max_requests then
    local next_penalty_ms = penalty_ms > 0 and penalty_ms * 2 or window_ms
    if next_penalty_ms > max_penalty_ms then
      next_penalty_ms = max_penalty_ms
    end
    blocked_until_ms = now_ms + next_penalty_ms
    redis.call(
      'HSET',
      KEYS[index],
      'windowStartedAt', tostring(window_started_at),
      'count', tostring(count),
      'penaltyMs', tostring(next_penalty_ms),
      'blockedUntilMs', tostring(blocked_until_ms)
    )
    redis.call('PEXPIRE', KEYS[index], ttl_ms)
    if blocked_index == 0 then
      blocked_index = index
      blocked_retry_ms = blocked_until_ms - now_ms
    end
  end
end

if blocked_index > 0 then
  return {0, blocked_retry_ms, blocked_index}
end

for index = 1, rule_count do
  redis.call(
    'HSET',
    KEYS[index],
    'windowStartedAt', tostring(window_started_values[index]),
    'count', tostring(counts[index] + 1),
    'penaltyMs', tostring(penalty_values[index]),
    'blockedUntilMs', '0'
  )
  redis.call('PEXPIRE', KEYS[index], ttl_values[index])
end
return {1, 0, 0}
`
