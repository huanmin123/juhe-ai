import { randomUUID } from 'node:crypto'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'
import { redisNamespacedKey } from './redis-namespace.js'

const currentConcurrencyByAccountId = new Map<string, number>()
const currentConcurrencyByAccountLaneKey = new Map<string, number>()
const inFlightSlotsByAccountId = new Map<string, Map<number, AccountInFlightSlot>>()
const releaseListeners = new Set<(event: AccountConcurrencyReleaseEvent) => void>()
let nextSlotId = 1
const redisAccountConcurrencySlotLeaseTtlMs = runtimeConfig.concurrency.accountSlotLeaseDurationMs
const redisAccountConcurrencySlotRefreshIntervalMs = runtimeConfig.concurrency.accountSlotRefreshIntervalMs
const redisAccountConcurrencyOwnerId = randomUUID()
let redisAccountConcurrencySlotRefreshTimer: NodeJS.Timeout | undefined

export type AccountConcurrencyLane = 'text' | 'image'

export interface AccountConcurrencyProjectionDirtyEntry {
  accountId: string
  generation: number
}

export interface AccountConcurrencyProjectionSnapshot {
  accountId: string
  currentConcurrency: number
  nextReconcileAt?: string
}

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
  redisToken?: string
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
  assertProcessLocalAccountConcurrencyAllowed('tryAcquireAccountConcurrency')
  const limit = normalizeConcurrencyLimit(concurrencyLimit)
  const lane = normalizeConcurrencyLane(options.lane)
  const laneLimit = normalizeLaneLimit(options.laneLimit, limit, lane)
  const current = getLocalAccountCurrentConcurrency(accountId)
  const laneCurrent = getLocalAccountCurrentConcurrency(accountId, lane)
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

  const slotId = acquireLocalAccountConcurrencySlot(accountId, lane)
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
  const redisToken = redisAccountConcurrencySlotToken()
  const [acquired, current, laneCurrent] = await acquireRedisAccountConcurrency(accountId, limit, lane, laneLimit, redisToken)
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
  const slotId = acquireLocalAccountConcurrencySlot(accountId, lane, redisToken)
  ensureRedisAccountConcurrencySlotRefresh()
  return {
    acquired: true,
    current,
    limit,
    lane,
    laneCurrent,
    laneLimit,
    markFirstOutput: () => markAccountConcurrencyFirstOutput(accountId, slotId),
    release: () => {
      if (released) return
      released = true
      releaseAccountConcurrency(accountId, slotId)
      void releaseRedisAccountConcurrencyWithRetry(accountId, redisToken)
    }
  }
}

export function getAccountCurrentConcurrency(accountId: string, lane?: AccountConcurrencyLane): number {
  assertProcessLocalAccountConcurrencyAllowed('getAccountCurrentConcurrency')
  if (lane) {
    return Math.max(0, Math.trunc(currentConcurrencyByAccountLaneKey.get(accountLaneKey(accountId, lane)) ?? 0))
  }
  return Math.max(0, Math.trunc(currentConcurrencyByAccountId.get(accountId) ?? 0))
}

function getLocalAccountCurrentConcurrency(accountId: string, lane?: AccountConcurrencyLane): number {
  if (lane) {
    return Math.max(0, Math.trunc(currentConcurrencyByAccountLaneKey.get(accountLaneKey(accountId, lane)) ?? 0))
  }
  return Math.max(0, Math.trunc(currentConcurrencyByAccountId.get(accountId) ?? 0))
}

export function loadAccountCurrentConcurrencyByIds(accountIds: string[], lane?: AccountConcurrencyLane): Map<string, number> {
  assertProcessLocalAccountConcurrencyAllowed('loadAccountCurrentConcurrencyByIds')
  const result = new Map<string, number>()
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    result.set(accountId, getLocalAccountCurrentConcurrency(accountId, lane))
  }
  return result
}

export async function getAccountCurrentConcurrencyAsync(accountId: string, lane?: AccountConcurrencyLane): Promise<number> {
  return (await loadAccountCurrentConcurrencyByIdsAsync([accountId], lane)).get(accountId) ?? 0
}

export async function loadAccountCurrentConcurrencyByIdsAsync(accountIds: string[], lane?: AccountConcurrencyLane): Promise<Map<string, number>> {
  const normalizedAccountIds = uniqueAccountIds(accountIds)
  if (normalizedAccountIds.length === 0) {
    return new Map<string, number>()
  }
  if (runtimeConfig.runtimeStateDriver === 'memory') {
    return loadAccountCurrentConcurrencyByIds(normalizedAccountIds, lane)
  }
  const result = new Map<string, number>()
  const client = await redisStateClient()
  for (let index = 0; index < normalizedAccountIds.length; index += 100) {
    const chunk = normalizedAccountIds.slice(index, index + 100)
    const values = numericRedisArray(await client.eval(
      runtimeConfig.performanceNodeRole === 'control-replica'
        ? redisReadOnlyAccountConcurrencyBatchScript
        : redisLoadAccountConcurrencyBatchScript,
      {
      keys: chunk.flatMap((accountId) => [
        redisAccountConcurrencyKey(accountId),
        redisAccountConcurrencyLaneKey(accountId, 'text'),
        redisAccountConcurrencyLaneKey(accountId, 'image'),
        lane ? redisAccountConcurrencyLaneKey(accountId, lane) : redisAccountConcurrencyKey(accountId),
        redisAccountConcurrencyMetadataKey(accountId)
      ]),
      arguments: []
      }
    ))
    chunk.forEach((accountId, valueIndex) => result.set(accountId, values[valueIndex] ?? 0))
  }
  return result
}

/**
 * Reads coalesced Redis changes without removing them. A PostgreSQL worker
 * acknowledges a generation only after its durable overlay write commits.
 */
export async function listAccountConcurrencyProjectionDirtyEntriesAsync(limit: number): Promise<AccountConcurrencyProjectionDirtyEntry[]> {
  const normalizedLimit = Math.min(1_000, Math.max(1, Math.trunc(limit)))
  if (runtimeConfig.runtimeStateDriver !== 'redis') return []
  const values = stringRedisArray(await (await redisStateClient()).eval(redisListAccountConcurrencyProjectionDirtyScript, {
    keys: [
      redisAccountConcurrencyProjectionDirtyKey(),
      redisAccountConcurrencyProjectionGenerationKey()
    ],
    arguments: [String(normalizedLimit)]
  }))
  const output: AccountConcurrencyProjectionDirtyEntry[] = []
  for (let index = 0; index + 1 < values.length; index += 2) {
    const accountId = values[index]?.trim()
    const generation = Number(values[index + 1])
    if (!accountId || !Number.isSafeInteger(generation) || generation < 1) continue
    output.push({ accountId, generation })
  }
  return output
}

/** Returns current counts plus the earliest slot expiry for durable rechecks. */
export async function loadAccountConcurrencyProjectionSnapshotsAsync(accountIds: string[]): Promise<AccountConcurrencyProjectionSnapshot[]> {
  const ids = uniqueAccountIds(accountIds)
  if (!ids.length) return []
  if (runtimeConfig.runtimeStateDriver !== 'redis') {
    return ids.map((accountId) => ({ accountId, currentConcurrency: getLocalAccountCurrentConcurrency(accountId) }))
  }
  const client = await redisStateClient()
  const output: AccountConcurrencyProjectionSnapshot[] = []
  for (let index = 0; index < ids.length; index += 100) {
    const chunk = ids.slice(index, index + 100)
    const values = numericRedisArray(await client.eval(redisLoadAccountConcurrencyProjectionSnapshotScript, {
      keys: chunk.flatMap((accountId) => [
        redisAccountConcurrencyKey(accountId),
        redisAccountConcurrencyLaneKey(accountId, 'text'),
        redisAccountConcurrencyLaneKey(accountId, 'image'),
        redisAccountConcurrencyKey(accountId),
        redisAccountConcurrencyMetadataKey(accountId)
      ]),
      arguments: []
    }))
    chunk.forEach((accountId, chunkIndex) => {
      const valueOffset = chunkIndex * 2
      const nextReconcileAtMs = values[valueOffset + 1] ?? 0
      output.push({
        accountId,
        currentConcurrency: Math.max(0, Math.trunc(values[valueOffset] ?? 0)),
        ...(nextReconcileAtMs > Date.now() ? { nextReconcileAt: new Date(nextReconcileAtMs).toISOString() } : {})
      })
    })
  }
  return output
}

/**
 * Keeps a future expiry check only when no newer acquire/release changed the
 * account generation while the PostgreSQL overlay was being written.
 */
export async function acknowledgeAccountConcurrencyProjectionDirtyEntriesAsync(
  entries: Array<AccountConcurrencyProjectionDirtyEntry & { nextReconcileAt?: string }>
): Promise<void> {
  if (!entries.length || runtimeConfig.runtimeStateDriver !== 'redis') return
  const args = [String(Date.now())]
  for (const entry of entries) {
    args.push(
      entry.accountId,
      String(entry.generation),
      String(entry.nextReconcileAt ? Date.parse(entry.nextReconcileAt) : 0)
    )
  }
  await (await redisStateClient()).eval(redisAcknowledgeAccountConcurrencyProjectionDirtyScript, {
    keys: [
      redisAccountConcurrencyProjectionDirtyKey(),
      redisAccountConcurrencyProjectionGenerationKey()
    ],
    arguments: args
  })
}

export function loadAccountInFlightStatsByIds(accountIds: string[], input: {
  slowRequestThresholdMs: number
  firstOutputSlowThresholdMs: number
}): Map<string, AccountInFlightStats> {
  assertProcessLocalAccountConcurrencyAllowed('loadAccountInFlightStatsByIds')
  return loadLocalAccountInFlightStatsByIds(accountIds, input)
}

export async function loadAccountInFlightStatsByIdsAsync(accountIds: string[], input: {
  slowRequestThresholdMs: number
  firstOutputSlowThresholdMs: number
}): Promise<Map<string, AccountInFlightStats>> {
  if (runtimeConfig.runtimeStateDriver === 'memory') {
    return loadLocalAccountInFlightStatsByIds(accountIds, input)
  }
  return loadRedisAccountInFlightStatsByIds(accountIds, input)
}

function loadLocalAccountInFlightStatsByIds(accountIds: string[], input: {
  slowRequestThresholdMs: number
  firstOutputSlowThresholdMs: number
}): Map<string, AccountInFlightStats> {
  const result = new Map<string, AccountInFlightStats>()
  const now = Date.now()
  const slowRequestThresholdMs = normalizePositiveDuration(input.slowRequestThresholdMs, 30_000)
  const firstOutputSlowThresholdMs = normalizePositiveDuration(input.firstOutputSlowThresholdMs, 15_000)
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    const slots = inFlightSlotsByAccountId.get(accountId)
    if (!slots) {
      continue
    }
    let slowInFlightCount = 0
    let firstOutputSlowCount = 0
    let oldestInFlightMs = 0
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
    result.set(accountId, {
      currentConcurrency: getLocalAccountCurrentConcurrency(accountId),
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
  stopRedisAccountConcurrencySlotRefresh()
}

function markAccountConcurrencyFirstOutput(accountId: string, slotId: number): void {
  const slot = inFlightSlotsByAccountId.get(accountId)?.get(slotId)
  if (!slot || slot.firstOutputAtMs !== undefined) {
    return
  }
  slot.firstOutputAtMs = Date.now()
  if (slot.redisToken) {
    void markRedisAccountConcurrencyFirstOutputWithRetry(accountId, slot.redisToken)
  }
}

function acquireLocalAccountConcurrencySlot(accountId: string, lane: AccountConcurrencyLane, redisToken?: string): number {
  const current = getLocalAccountCurrentConcurrency(accountId)
  currentConcurrencyByAccountId.set(accountId, current + 1)
  const slotId = nextSlotId
  nextSlotId += 1
  const accountSlots = inFlightSlotsByAccountId.get(accountId) ?? new Map<number, AccountInFlightSlot>()
  accountSlots.set(slotId, { slotId, startedAtMs: Date.now(), lane, redisToken })
  inFlightSlotsByAccountId.set(accountId, accountSlots)
  incrementAccountConcurrencyLane(accountId, lane)
  return slotId
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
  const current = getLocalAccountCurrentConcurrency(accountId)
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

function assertProcessLocalAccountConcurrencyAllowed(operation: string): void {
  if (runtimeConfig.runtimeStateDriver !== 'redis') return
  throw new Error(`高性能模式禁止同步读取本机账号并发状态：${operation} 必须使用 Redis async 并发入口`)
}

function uniqueAccountIds(accountIds: string[]): string[] {
  return [...new Set(accountIds.map((accountId) => accountId.trim()).filter(Boolean))]
}

async function acquireRedisAccountConcurrency(
  accountId: string,
  limit: number,
  lane: AccountConcurrencyLane,
  laneLimit: number,
  redisToken: string
): Promise<[boolean, number, number]> {
  const client = await redisStateClient()
  const result = await client.eval(redisAcquireAccountConcurrencyScript, {
    keys: [
      redisAccountConcurrencyKey(accountId),
      redisAccountConcurrencyLaneKey(accountId, 'text'),
      redisAccountConcurrencyLaneKey(accountId, 'image'),
      redisAccountConcurrencyLaneKey(accountId, lane),
      redisAccountConcurrencyMetadataKey(accountId),
      redisAccountConcurrencyProjectionDirtyKey(),
      redisAccountConcurrencyProjectionGenerationKey()
    ],
    arguments: [
      String(limit),
      String(laneLimit),
      String(redisAccountConcurrencySlotLeaseTtlMs),
      redisToken,
      accountId
    ]
  })
  const values = numericRedisArray(result)
  return [values[0] === 1, values[1] ?? 0, values[2] ?? 0]
}

async function releaseRedisAccountConcurrency(accountId: string, redisToken: string): Promise<void> {
  await (await redisStateClient()).eval(redisReleaseAccountConcurrencyScript, {
    keys: [
      redisAccountConcurrencyKey(accountId),
      redisAccountConcurrencyLaneKey(accountId, 'text'),
      redisAccountConcurrencyLaneKey(accountId, 'image'),
      redisAccountConcurrencyMetadataKey(accountId),
      redisAccountConcurrencyProjectionDirtyKey(),
      redisAccountConcurrencyProjectionGenerationKey()
    ],
    arguments: [redisToken, accountId]
  })
}

async function loadRedisAccountInFlightStatsByIds(accountIds: string[], input: {
  slowRequestThresholdMs: number
  firstOutputSlowThresholdMs: number
}): Promise<Map<string, AccountInFlightStats>> {
  const ids = uniqueAccountIds(accountIds)
  const result = new Map<string, AccountInFlightStats>()
  if (ids.length === 0) {
    return result
  }
  const client = await redisStateClient()
  const slowRequestThresholdMs = normalizePositiveDuration(input.slowRequestThresholdMs, 30_000)
  const firstOutputSlowThresholdMs = normalizePositiveDuration(input.firstOutputSlowThresholdMs, 15_000)
  for (let index = 0; index < ids.length; index += 100) {
    const chunk = ids.slice(index, index + 100)
    const values = await Promise.all(chunk.map(async (accountId) => numericRedisArray(await client.eval(redisLoadAccountInFlightStatsScript, {
      keys: [
        redisAccountConcurrencyKey(accountId),
        redisAccountConcurrencyLaneKey(accountId, 'text'),
        redisAccountConcurrencyLaneKey(accountId, 'image'),
        redisAccountConcurrencyMetadataKey(accountId)
      ],
      arguments: [
        String(slowRequestThresholdMs),
        String(firstOutputSlowThresholdMs)
      ]
    }))))
    chunk.forEach((accountId, valueIndex) => {
      const stats = values[valueIndex]
      result.set(accountId, {
        currentConcurrency: stats[0] ?? 0,
        slowInFlightCount: stats[1] ?? 0,
        firstOutputSlowCount: stats[2] ?? 0,
        oldestInFlightMs: stats[3] ?? 0
      })
    })
  }
  return result
}

function ensureRedisAccountConcurrencySlotRefresh(): void {
  if (redisAccountConcurrencySlotRefreshTimer) {
    return
  }
  redisAccountConcurrencySlotRefreshTimer = setInterval(() => {
    void refreshRedisAccountConcurrencySlots().catch(() => undefined)
  }, redisAccountConcurrencySlotRefreshIntervalMs)
  redisAccountConcurrencySlotRefreshTimer.unref?.()
}

function stopRedisAccountConcurrencySlotRefresh(): void {
  if (!redisAccountConcurrencySlotRefreshTimer) {
    return
  }
  clearInterval(redisAccountConcurrencySlotRefreshTimer)
  redisAccountConcurrencySlotRefreshTimer = undefined
}

async function refreshRedisAccountConcurrencySlots(): Promise<void> {
  const slots = localRedisAccountConcurrencySlots()
  if (slots.length === 0) {
    stopRedisAccountConcurrencySlotRefresh()
    return
  }
  const client = await redisStateClient()
  for (let index = 0; index < slots.length; index += 100) {
    const chunk = slots.slice(index, index + 100)
    const result = await client.eval(redisRefreshAccountConcurrencySlotsScript, {
      keys: chunk.flatMap((slot) => [
        redisAccountConcurrencyKey(slot.accountId),
        redisAccountConcurrencyLaneKey(slot.accountId, slot.lane),
        redisAccountConcurrencyMetadataKey(slot.accountId)
      ]).concat([
        redisAccountConcurrencyProjectionDirtyKey(),
        redisAccountConcurrencyProjectionGenerationKey()
      ]),
      arguments: [
        String(redisAccountConcurrencySlotLeaseTtlMs),
        ...chunk.map((slot) => slot.redisToken),
        ...chunk.map((slot) => slot.accountId)
      ]
    })
    const refreshed = numericRedisArray(result)
    chunk.forEach((slot, slotIndex) => {
      if (refreshed[slotIndex] === 1) return
      detachExpiredRedisAccountConcurrencySlot(slot.accountId, slot.redisToken)
    })
  }
}

function localRedisAccountConcurrencySlots(): Array<{
  accountId: string
  lane: AccountConcurrencyLane
  redisToken: string
}> {
  const result: Array<{
    accountId: string
    lane: AccountConcurrencyLane
    redisToken: string
  }> = []
  for (const [accountId, slots] of inFlightSlotsByAccountId.entries()) {
    for (const slot of slots.values()) {
      if (!slot.redisToken) continue
      result.push({ accountId, lane: slot.lane, redisToken: slot.redisToken })
    }
  }
  return result
}

function detachExpiredRedisAccountConcurrencySlot(accountId: string, redisToken: string): void {
  const slots = inFlightSlotsByAccountId.get(accountId)
  if (!slots) return
  for (const slot of slots.values()) {
    if (slot.redisToken !== redisToken) continue
    slot.redisToken = undefined
    logger.warn({
      event: 'redis_account_concurrency_slot_lease_expired',
      accountId
    }, 'Redis 账号并发槽租约已过期，停止刷新本地旧 token')
    return
  }
}

async function releaseRedisAccountConcurrencyWithRetry(accountId: string, redisToken: string): Promise<void> {
  const delays = [0, 250, 1000, 5000]
  let lastError: unknown
  for (const delayMs of delays) {
    if (delayMs > 0) {
      await delay(delayMs)
    }
    try {
      await releaseRedisAccountConcurrency(accountId, redisToken)
      return
    } catch (error) {
      lastError = error
    }
  }
  logger.error(errorLogFields(lastError, {
    event: 'redis_account_concurrency_release_failed',
    accountId
  }), 'Redis 账号并发槽释放失败')
}

async function markRedisAccountConcurrencyFirstOutputWithRetry(
  accountId: string,
  redisToken: string
): Promise<void> {
  const delays = [0, 250, 1000]
  let lastError: unknown
  for (const delayMs of delays) {
    if (delayMs > 0) {
      await delay(delayMs)
    }
    try {
      await (await redisStateClient()).eval(redisMarkAccountConcurrencyFirstOutputScript, {
        keys: [
          redisAccountConcurrencyKey(accountId),
          redisAccountConcurrencyMetadataKey(accountId)
        ],
        arguments: [redisToken]
      })
      return
    } catch (error) {
      lastError = error
    }
  }
  logger.warn(errorLogFields(lastError, {
    event: 'redis_account_concurrency_first_output_mark_failed',
    accountId
  }), 'Redis 账号并发槽首包时间标记失败')
}

function redisStateClient(): Promise<RedisCommandClient> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return getRedisClient(redisUrl)
}

function redisAccountConcurrencyKey(accountId: string): string {
  return redisNamespacedKey(`juhe-ai:account-concurrency-v2:${accountId}:total`)
}

function redisAccountConcurrencyLaneKey(accountId: string, lane: AccountConcurrencyLane): string {
  return redisNamespacedKey(`juhe-ai:account-concurrency-v2:${accountId}:${lane}`)
}

function redisAccountConcurrencyMetadataKey(accountId: string): string {
  return redisNamespacedKey(`juhe-ai:account-concurrency-v2:${accountId}:metadata`)
}

function redisAccountConcurrencyProjectionDirtyKey(): string {
  return redisNamespacedKey('juhe-ai:account-concurrency-projection-dirty')
}

function redisAccountConcurrencyProjectionGenerationKey(): string {
  return redisNamespacedKey('juhe-ai:account-concurrency-projection-generation')
}

function redisAccountConcurrencySlotToken(): string {
  return `${redisAccountConcurrencyOwnerId}|${randomUUID()}`
}

const redisAcquireAccountConcurrencyScript = `
local total_limit = tonumber(ARGV[1])
local lane_limit = tonumber(ARGV[2])
local slot_ttl_ms = tonumber(ARGV[3])
local slot_token = ARGV[4]
local account_id = ARGV[5]
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local started_at_ms = now_ms
local expires_at_ms = now_ms + slot_ttl_ms

local function hdel_expired(metadata_key, expired)
  local index = 1
  while index <= #expired do
    local last = math.min(index + 199, #expired)
    redis.call('HDEL', metadata_key, unpack(expired, index, last))
    index = last + 1
  end
end

local function cleanup_expired()
  local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms)
  if #expired > 0 then
    hdel_expired(KEYS[5], expired)
    redis.call('HINCRBY', KEYS[7], account_id, 1)
    redis.call('ZADD', KEYS[6], now_ms, account_id)
  end
  redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now_ms)
end

local function latest_expiry_ms(key)
  local latest = redis.call('ZREVRANGE', key, 0, 0, 'WITHSCORES')
  if #latest < 2 then
    return now_ms
  end
  return tonumber(latest[2]) or now_ms
end

local function expire_at_latest_slot(key, expiry_ms)
  if redis.call('EXISTS', key) ~= 0 then
    redis.call('PEXPIRE', key, math.max(1, expiry_ms - now_ms))
  end
end

cleanup_expired()

local current = tonumber(redis.call('ZCARD', KEYS[1]) or '0') or 0
local lane_current = tonumber(redis.call('ZCARD', KEYS[4]) or '0') or 0
if current >= total_limit or lane_current >= lane_limit then
  return {0, current, lane_current}
end
redis.call('ZADD', KEYS[1], expires_at_ms, slot_token)
redis.call('ZADD', KEYS[4], expires_at_ms, slot_token)
redis.call('HSET', KEYS[5], slot_token, cjson.encode({startedAtMs = started_at_ms}))
redis.call('HINCRBY', KEYS[7], account_id, 1)
redis.call('ZADD', KEYS[6], now_ms, account_id)
local total_latest_expiry_ms = latest_expiry_ms(KEYS[1])
local lane_latest_expiry_ms = latest_expiry_ms(KEYS[4])
expire_at_latest_slot(KEYS[1], total_latest_expiry_ms)
expire_at_latest_slot(KEYS[4], lane_latest_expiry_ms)
expire_at_latest_slot(KEYS[5], total_latest_expiry_ms)
return {1, current + 1, lane_current + 1}
`

const redisReleaseAccountConcurrencyScript = `
local slot_token = ARGV[1]
local account_id = ARGV[2]
redis.call('ZREM', KEYS[1], slot_token)
redis.call('ZREM', KEYS[2], slot_token)
redis.call('ZREM', KEYS[3], slot_token)
redis.call('HDEL', KEYS[4], slot_token)
if redis.call('ZCARD', KEYS[1]) == 0 then
  redis.call('DEL', KEYS[1])
end
if redis.call('ZCARD', KEYS[2]) == 0 then
  redis.call('DEL', KEYS[2])
end
if redis.call('ZCARD', KEYS[3]) == 0 then
  redis.call('DEL', KEYS[3])
end
if redis.call('HLEN', KEYS[4]) == 0 then
  redis.call('DEL', KEYS[4])
end
redis.call('HINCRBY', KEYS[6], account_id, 1)
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
redis.call('ZADD', KEYS[5], now_ms, account_id)
return 1
`

const redisLoadAccountConcurrencyBatchScript = `
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local results = {}

local function hdel_expired(metadata_key, expired)
  local index = 1
  while index <= #expired do
    local last = math.min(index + 199, #expired)
    redis.call('HDEL', metadata_key, unpack(expired, index, last))
    index = last + 1
  end
end

for key_index = 1, #KEYS, 5 do
  local expired = redis.call('ZRANGEBYSCORE', KEYS[key_index], '-inf', now_ms)
  if #expired > 0 then
    hdel_expired(KEYS[key_index + 4], expired)
  end
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index + 1], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index + 2], '-inf', now_ms)
  if redis.call('ZCARD', KEYS[key_index]) == 0 then redis.call('DEL', KEYS[key_index]) end
  if redis.call('ZCARD', KEYS[key_index + 1]) == 0 then redis.call('DEL', KEYS[key_index + 1]) end
  if redis.call('ZCARD', KEYS[key_index + 2]) == 0 then redis.call('DEL', KEYS[key_index + 2]) end
  if redis.call('HLEN', KEYS[key_index + 4]) == 0 then redis.call('DEL', KEYS[key_index + 4]) end
  table.insert(results, redis.call('ZCARD', KEYS[key_index + 3]))
end
return results
`

const redisReadOnlyAccountConcurrencyBatchScript = `
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local results = {}
for key_index = 1, #KEYS, 5 do
  table.insert(results, redis.call('ZCOUNT', KEYS[key_index + 3], '(' .. tostring(now_ms), '+inf'))
end
return results
`

const redisLoadAccountConcurrencyProjectionSnapshotScript = `
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local results = {}

local function hdel_expired(metadata_key, expired)
  local index = 1
  while index <= #expired do
    local last = math.min(index + 199, #expired)
    redis.call('HDEL', metadata_key, unpack(expired, index, last))
    index = last + 1
  end
end

for key_index = 1, #KEYS, 5 do
  local expired = redis.call('ZRANGEBYSCORE', KEYS[key_index], '-inf', now_ms)
  if #expired > 0 then
    hdel_expired(KEYS[key_index + 4], expired)
  end
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index + 1], '-inf', now_ms)
  redis.call('ZREMRANGEBYSCORE', KEYS[key_index + 2], '-inf', now_ms)
  if redis.call('ZCARD', KEYS[key_index]) == 0 then redis.call('DEL', KEYS[key_index]) end
  if redis.call('ZCARD', KEYS[key_index + 1]) == 0 then redis.call('DEL', KEYS[key_index + 1]) end
  if redis.call('ZCARD', KEYS[key_index + 2]) == 0 then redis.call('DEL', KEYS[key_index + 2]) end
  if redis.call('HLEN', KEYS[key_index + 4]) == 0 then redis.call('DEL', KEYS[key_index + 4]) end
  table.insert(results, redis.call('ZCARD', KEYS[key_index + 3]))
  local next_expiry = redis.call('ZRANGE', KEYS[key_index], 0, 0, 'WITHSCORES')
  table.insert(results, tonumber(next_expiry[2]) or 0)
end
return results
`

const redisListAccountConcurrencyProjectionDirtyScript = `
local limit = tonumber(ARGV[1])
local entries = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', '+inf', 'LIMIT', 0, limit)
local results = {}
for _, account_id in ipairs(entries) do
  local generation = redis.call('HGET', KEYS[2], account_id)
  if generation ~= false then
    table.insert(results, account_id)
    table.insert(results, generation)
  else
    redis.call('ZREM', KEYS[1], account_id)
  end
end
return results
`

const redisAcknowledgeAccountConcurrencyProjectionDirtyScript = `
local now_ms = tonumber(ARGV[1])
for arg_index = 2, #ARGV, 3 do
  local account_id = ARGV[arg_index]
  local expected_generation = ARGV[arg_index + 1]
  local next_reconcile_at_ms = tonumber(ARGV[arg_index + 2]) or 0
  local current_generation = redis.call('HGET', KEYS[2], account_id)
  if current_generation ~= false and current_generation == expected_generation then
    if next_reconcile_at_ms > now_ms then
      redis.call('ZADD', KEYS[1], next_reconcile_at_ms, account_id)
    else
      redis.call('ZREM', KEYS[1], account_id)
      redis.call('HDEL', KEYS[2], account_id)
    end
  end
end
return 1
`

const redisRefreshAccountConcurrencySlotsScript = `
local slot_ttl_ms = tonumber(ARGV[1])
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local requested_expires_at_ms = now_ms + slot_ttl_ms
local results = {}

local function latest_expiry_ms(key)
  local latest = redis.call('ZREVRANGE', key, 0, 0, 'WITHSCORES')
  if #latest < 2 then
    return now_ms
  end
  return tonumber(latest[2]) or now_ms
end

local function expire_at_latest_slot(key, expiry_ms)
  if redis.call('EXISTS', key) ~= 0 then
    redis.call('PEXPIRE', key, math.max(1, expiry_ms - now_ms))
  end
end

for arg_index = 2, #ARGV do
  if arg_index > 1 + ((#KEYS - 2) / 3) then break end
  local key_index = (arg_index - 2) * 3 + 1
  local slot_token = ARGV[arg_index]
  local account_id = ARGV[arg_index + ((#KEYS - 2) / 3)]
  local total_expiry_raw = redis.call('ZSCORE', KEYS[key_index], slot_token)
  local lane_expiry_raw = redis.call('ZSCORE', KEYS[key_index + 1], slot_token)
  local total_expiry_ms = tonumber(total_expiry_raw)
  local lane_expiry_ms = tonumber(lane_expiry_raw)
  if total_expiry_ms ~= nil and lane_expiry_ms ~= nil and total_expiry_ms > now_ms and lane_expiry_ms > now_ms then
    local expires_at_ms = math.max(requested_expires_at_ms, total_expiry_ms, lane_expiry_ms)
    redis.call('ZADD', KEYS[key_index], expires_at_ms, slot_token)
    redis.call('ZADD', KEYS[key_index + 1], expires_at_ms, slot_token)
    local total_latest_expiry_ms = latest_expiry_ms(KEYS[key_index])
    local lane_latest_expiry_ms = latest_expiry_ms(KEYS[key_index + 1])
    expire_at_latest_slot(KEYS[key_index], total_latest_expiry_ms)
    expire_at_latest_slot(KEYS[key_index + 1], lane_latest_expiry_ms)
    expire_at_latest_slot(KEYS[key_index + 2], total_latest_expiry_ms)
    table.insert(results, 1)
  else
    redis.call('ZREM', KEYS[key_index], slot_token)
    redis.call('ZREM', KEYS[key_index + 1], slot_token)
    redis.call('HDEL', KEYS[key_index + 2], slot_token)
    redis.call('HINCRBY', KEYS[#KEYS], account_id, 1)
    redis.call('ZADD', KEYS[#KEYS - 1], now_ms, account_id)
    table.insert(results, 0)
  end
end
return results
`

const redisLoadAccountInFlightStatsScript = `
local redis_time = redis.call('TIME')
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
local slow_threshold_ms = tonumber(ARGV[1])
local first_output_slow_threshold_ms = tonumber(ARGV[2])

local function hdel_expired(metadata_key, expired)
  local index = 1
  while index <= #expired do
    local last = math.min(index + 199, #expired)
    redis.call('HDEL', metadata_key, unpack(expired, index, last))
    index = last + 1
  end
end

local expired = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now_ms)
if #expired > 0 then
  hdel_expired(KEYS[4], expired)
end
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', now_ms)

local tokens = redis.call('ZRANGE', KEYS[1], 0, -1)
local current = #tokens
local slow_count = 0
local first_output_slow_count = 0
local oldest_in_flight_ms = 0

for _, token in ipairs(tokens) do
  local encoded = redis.call('HGET', KEYS[4], token)
  if encoded ~= false then
    local ok, metadata = pcall(cjson.decode, encoded)
    if ok and metadata ~= nil and metadata['startedAtMs'] ~= nil then
      local started_at_ms = tonumber(metadata['startedAtMs'])
      if started_at_ms ~= nil then
        local age_ms = math.max(0, now_ms - started_at_ms)
        oldest_in_flight_ms = math.max(oldest_in_flight_ms, age_ms)
        if age_ms >= slow_threshold_ms then
          slow_count = slow_count + 1
        end
        if metadata['firstOutputAtMs'] == nil and age_ms >= first_output_slow_threshold_ms then
          first_output_slow_count = first_output_slow_count + 1
        end
      end
    end
  end
end

if current == 0 then
  redis.call('DEL', KEYS[1])
  redis.call('DEL', KEYS[2])
  redis.call('DEL', KEYS[3])
  redis.call('DEL', KEYS[4])
elseif redis.call('HLEN', KEYS[4]) == 0 then
  redis.call('DEL', KEYS[4])
else
  redis.call('PEXPIRE', KEYS[4], ${redisAccountConcurrencySlotLeaseTtlMs})
end

return {current, slow_count, first_output_slow_count, oldest_in_flight_ms}
`

const redisMarkAccountConcurrencyFirstOutputScript = `
local slot_token = ARGV[1]
local redis_time = redis.call('TIME')
local first_output_at_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)
if redis.call('ZSCORE', KEYS[1], slot_token) == false then
  return 0
end
local encoded = redis.call('HGET', KEYS[2], slot_token)
if encoded == false then
  return 0
end
local ok, metadata = pcall(cjson.decode, encoded)
if not ok or metadata == nil then
  return 0
end
if metadata['firstOutputAtMs'] == nil then
  metadata['firstOutputAtMs'] = first_output_at_ms
  redis.call('HSET', KEYS[2], slot_token, cjson.encode(metadata))
end
return 1
`

function numericRedisResult(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return value
  if (typeof value === 'bigint') return Number(value)
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
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

function stringRedisArray(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.map((item) => String(item ?? ''))
}

function delay(ms: number): Promise<void> {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, Math.max(0, ms)))
}
