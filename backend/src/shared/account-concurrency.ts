import { runtimeConfig } from '../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'

const currentConcurrencyByAccountId = new Map<string, number>()
const currentConcurrencyByAccountLaneKey = new Map<string, number>()
const inFlightSlotsByAccountId = new Map<string, Map<number, AccountInFlightSlot>>()
const releaseListeners = new Set<(event: AccountConcurrencyReleaseEvent) => void>()
let nextSlotId = 1
const redisAccountConcurrencyTtlMs = 2 * 60 * 60 * 1000

export type AccountConcurrencyLane = 'text' | 'image'

export interface AccountConcurrencySlot {
  acquired: boolean
  current: number
  limit: number
  lane: AccountConcurrencyLane
  laneCurrent: number
  laneLimit: number
  release: () => void
  markFirstOutput: () => void
}

interface AccountInFlightSlot {
  slotId: number
  startedAtMs: number
  firstOutputAtMs?: number
  lane: AccountConcurrencyLane
}

export interface AccountInFlightStats {
  currentConcurrency: number
  slowInFlightCount: number
  firstOutputSlowCount: number
  oldestInFlightMs: number
}

export interface AccountConcurrencyAcquireOptions {
  lane?: AccountConcurrencyLane
  laneLimit?: number
}

export interface AccountConcurrencyReleaseEvent {
  accountId: string
  lane: AccountConcurrencyLane
}

export function tryAcquireAccountConcurrency(
  accountId: string,
  concurrencyLimit: number,
  options: AccountConcurrencyAcquireOptions = {}
): AccountConcurrencySlot {
  const limit = normalizeConcurrencyLimit(concurrencyLimit)
  const lane = normalizeConcurrencyLane(options.lane)
  const laneLimit = normalizeLaneLimit(options.laneLimit, limit, lane)
  const current = getAccountCurrentConcurrency(accountId)
  const laneCurrent = getAccountCurrentConcurrency(accountId, lane)
  if (current >= limit || laneCurrent >= laneLimit) {
    return {
      acquired: false,
      current,
      limit,
      lane,
      laneCurrent,
      laneLimit,
      release: noop,
      markFirstOutput: noop
    }
  }

  currentConcurrencyByAccountId.set(accountId, current + 1)
  const slotId = nextSlotId
  nextSlotId += 1
  const accountSlots = inFlightSlotsByAccountId.get(accountId) ?? new Map<number, AccountInFlightSlot>()
  accountSlots.set(slotId, { slotId, startedAtMs: Date.now(), lane })
  inFlightSlotsByAccountId.set(accountId, accountSlots)
  incrementAccountConcurrencyLane(accountId, lane)
  let released = false
  return {
    acquired: true,
    current: current + 1,
    limit,
    lane,
    laneCurrent: laneCurrent + 1,
    laneLimit,
    markFirstOutput: () => markAccountConcurrencyFirstOutput(accountId, slotId),
    release: () => {
      if (released) return
      released = true
      releaseAccountConcurrency(accountId, slotId)
    }
  }
}

export async function tryAcquireAccountConcurrencyAsync(
  accountId: string,
  concurrencyLimit: number,
  options: AccountConcurrencyAcquireOptions = {}
): Promise<AccountConcurrencySlot> {
  if (runtimeConfig.runtimeStateDriver === 'memory') {
    return tryAcquireAccountConcurrency(accountId, concurrencyLimit, options)
  }
  const limit = normalizeConcurrencyLimit(concurrencyLimit)
  const lane = normalizeConcurrencyLane(options.lane)
  const laneLimit = normalizeLaneLimit(options.laneLimit, limit, lane)
  const [acquired, current, laneCurrent] = await acquireRedisAccountConcurrency(accountId, limit, lane, laneLimit)
  if (!acquired) {
    return {
      acquired: false,
      current,
      limit,
      lane,
      laneCurrent,
      laneLimit,
      release: noop,
      markFirstOutput: noop
    }
  }

  let released = false
  return {
    acquired: true,
    current,
    limit,
    lane,
    laneCurrent,
    laneLimit,
    markFirstOutput: noop,
    release: () => {
      if (released) return
      released = true
      void releaseRedisAccountConcurrency(accountId, lane).catch(() => undefined).finally(() => {
        notifyAccountConcurrencyReleased({ accountId, lane })
      })
    }
  }
}

export function getAccountCurrentConcurrency(accountId: string, lane?: AccountConcurrencyLane): number {
  if (lane) {
    return Math.max(0, Math.trunc(currentConcurrencyByAccountLaneKey.get(accountLaneKey(accountId, lane)) ?? 0))
  }
  return Math.max(0, Math.trunc(currentConcurrencyByAccountId.get(accountId) ?? 0))
}

export function loadAccountCurrentConcurrencyByIds(accountIds: string[], lane?: AccountConcurrencyLane): Map<string, number> {
  const result = new Map<string, number>()
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    result.set(accountId, getAccountCurrentConcurrency(accountId, lane))
  }
  return result
}

export function loadAccountInFlightStatsByIds(accountIds: string[], input: {
  slowRequestThresholdMs: number
  firstOutputSlowThresholdMs: number
}): Map<string, AccountInFlightStats> {
  const result = new Map<string, AccountInFlightStats>()
  const now = Date.now()
  const slowRequestThresholdMs = normalizePositiveDuration(input.slowRequestThresholdMs, 30_000)
  const firstOutputSlowThresholdMs = normalizePositiveDuration(input.firstOutputSlowThresholdMs, 15_000)
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    const slots = inFlightSlotsByAccountId.get(accountId)
    let slowInFlightCount = 0
    let firstOutputSlowCount = 0
    let oldestInFlightMs = 0
    if (slots) {
      for (const slot of slots.values()) {
        const ageMs = Math.max(0, now - slot.startedAtMs)
        oldestInFlightMs = Math.max(oldestInFlightMs, ageMs)
        if (ageMs >= slowRequestThresholdMs) {
          slowInFlightCount += 1
        }
        if (slot.firstOutputAtMs === undefined && ageMs >= firstOutputSlowThresholdMs) {
          firstOutputSlowCount += 1
        }
      }
    }
    result.set(accountId, {
      currentConcurrency: getAccountCurrentConcurrency(accountId),
      slowInFlightCount,
      firstOutputSlowCount,
      oldestInFlightMs
    })
  }
  return result
}

export function subscribeAccountConcurrencyRelease(listener: (event: AccountConcurrencyReleaseEvent) => void): () => void {
  releaseListeners.add(listener)
  return () => {
    releaseListeners.delete(listener)
  }
}

export function snapshotAccountConcurrency(): Record<string, number> {
  const snapshot: Record<string, number> = {}
  for (const [accountId, current] of currentConcurrencyByAccountId.entries()) {
    const normalized = Math.max(0, Math.trunc(current))
    if (accountId && normalized > 0) {
      snapshot[accountId] = normalized
    }
  }
  return snapshot
}

export function sumAccountCurrentConcurrency(accountIds: string[], concurrencyByAccount = loadAccountCurrentConcurrencyByIds(accountIds)): number {
  let total = 0
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    total += concurrencyByAccount.get(accountId) ?? 0
  }
  return total
}

export function clearAccountConcurrency(): void {
  currentConcurrencyByAccountId.clear()
  currentConcurrencyByAccountLaneKey.clear()
  inFlightSlotsByAccountId.clear()
}

function markAccountConcurrencyFirstOutput(accountId: string, slotId: number): void {
  const slot = inFlightSlotsByAccountId.get(accountId)?.get(slotId)
  if (!slot || slot.firstOutputAtMs !== undefined) {
    return
  }
  slot.firstOutputAtMs = Date.now()
}

function releaseAccountConcurrency(accountId: string, slotId: number): void {
  const slots = inFlightSlotsByAccountId.get(accountId)
  let releasedLane: AccountConcurrencyLane = 'text'
  if (slots) {
    const releasedSlot = slots.get(slotId)
    releasedLane = releasedSlot?.lane ?? releasedLane
    slots.delete(slotId)
    if (slots.size === 0) {
      inFlightSlotsByAccountId.delete(accountId)
    }
  }
  const current = getAccountCurrentConcurrency(accountId)
  if (current <= 1) {
    currentConcurrencyByAccountId.delete(accountId)
  } else {
    currentConcurrencyByAccountId.set(accountId, current - 1)
  }
  decrementAccountConcurrencyLane(accountId, releasedLane)
  notifyAccountConcurrencyReleased({ accountId, lane: releasedLane })
}

function notifyAccountConcurrencyReleased(event: AccountConcurrencyReleaseEvent): void {
  for (const listener of releaseListeners) {
    try {
      listener(event)
    } catch {
      // Release must never fail because a scheduler observer failed.
    }
  }
}

function normalizeConcurrencyLimit(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}

function normalizeConcurrencyLane(value: unknown): AccountConcurrencyLane {
  return value === 'image' ? 'image' : 'text'
}

function normalizeLaneLimit(value: unknown, concurrencyLimit: number, lane: AccountConcurrencyLane): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (Number.isFinite(numeric) && numeric > 0) {
    return Math.min(concurrencyLimit, Math.max(1, Math.trunc(numeric)))
  }
  if (lane === 'image' && concurrencyLimit > 1) {
    return concurrencyLimit - 1
  }
  return concurrencyLimit
}

function normalizePositiveDuration(value: number, fallback: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : fallback
}

function incrementAccountConcurrencyLane(accountId: string, lane: AccountConcurrencyLane): void {
  const key = accountLaneKey(accountId, lane)
  currentConcurrencyByAccountLaneKey.set(key, (currentConcurrencyByAccountLaneKey.get(key) ?? 0) + 1)
}

function decrementAccountConcurrencyLane(accountId: string, lane: AccountConcurrencyLane): void {
  const key = accountLaneKey(accountId, lane)
  const current = Math.max(0, Math.trunc(currentConcurrencyByAccountLaneKey.get(key) ?? 0))
  if (current <= 1) {
    currentConcurrencyByAccountLaneKey.delete(key)
    return
  }
  currentConcurrencyByAccountLaneKey.set(key, current - 1)
}

function accountLaneKey(accountId: string, lane: AccountConcurrencyLane): string {
  return `${accountId}:${lane}`
}

function noop(): void {}

async function acquireRedisAccountConcurrency(
  accountId: string,
  limit: number,
  lane: AccountConcurrencyLane,
  laneLimit: number
): Promise<[boolean, number, number]> {
  const result = await (await redisStateClient()).eval(redisAcquireAccountConcurrencyScript, {
    keys: [
      redisAccountConcurrencyKey(accountId),
      redisAccountConcurrencyLaneKey(accountId, lane)
    ],
    arguments: [
      String(limit),
      String(laneLimit),
      String(redisAccountConcurrencyTtlMs)
    ]
  })
  const values = numericRedisArray(result)
  return [values[0] === 1, values[1] ?? 0, values[2] ?? 0]
}

async function releaseRedisAccountConcurrency(accountId: string, lane: AccountConcurrencyLane): Promise<void> {
  await (await redisStateClient()).eval(redisReleaseAccountConcurrencyScript, {
    keys: [
      redisAccountConcurrencyKey(accountId),
      redisAccountConcurrencyLaneKey(accountId, lane)
    ],
    arguments: []
  })
}

function redisStateClient(): Promise<RedisCommandClient> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return getRedisClient(redisUrl)
}

function redisAccountConcurrencyKey(accountId: string): string {
  return `juhe-ai:account-concurrency:${accountId}:total`
}

function redisAccountConcurrencyLaneKey(accountId: string, lane: AccountConcurrencyLane): string {
  return `juhe-ai:account-concurrency:${accountId}:${lane}`
}

const redisAcquireAccountConcurrencyScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0') or 0
local lane_current = tonumber(redis.call('GET', KEYS[2]) or '0') or 0
local total_limit = tonumber(ARGV[1])
local lane_limit = tonumber(ARGV[2])
if current >= total_limit or lane_current >= lane_limit then
  return {0, current, lane_current}
end
current = redis.call('INCR', KEYS[1])
lane_current = redis.call('INCR', KEYS[2])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('PEXPIRE', KEYS[2], ARGV[3])
return {1, current, lane_current}
`

const redisReleaseAccountConcurrencyScript = `
local current = tonumber(redis.call('GET', KEYS[1]) or '0') or 0
if current <= 1 then
  redis.call('DEL', KEYS[1])
else
  redis.call('DECR', KEYS[1])
end
local lane_current = tonumber(redis.call('GET', KEYS[2]) or '0') or 0
if lane_current <= 1 then
  redis.call('DEL', KEYS[2])
else
  redis.call('DECR', KEYS[2])
end
return 1
`

function numericRedisArray(value: unknown): number[] {
  if (!Array.isArray(value)) return []
  return value.map((item) => {
    if (typeof item === 'number' && Number.isFinite(item)) return item
    if (typeof item === 'bigint') return Number(item)
    const parsed = Number(item)
    return Number.isFinite(parsed) ? parsed : 0
  })
}
