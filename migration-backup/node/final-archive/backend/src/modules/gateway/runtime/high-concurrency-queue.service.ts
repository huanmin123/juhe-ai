import { randomBytes } from 'node:crypto'

import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, effectiveImageLaneConcurrencyLimit, resolveGroupSchedulingPolicy } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../../domain/types.js'
import { runtimeConfig } from '../../../config/runtime.js'
import { getRedisClient, type RedisCommandClient } from '../../../shared/redis-client.js'
import { redisNamespacedKey } from '../../../shared/redis-namespace.js'
import {
  getAccountCurrentConcurrency,
  loadAccountCurrentConcurrencyByIdsAsync,
  subscribeAccountConcurrencyRelease,
  type AccountConcurrencyLane
} from '../../../shared/account-concurrency.js'

interface HighConcurrencyQueueState {
  groupKey: string
  lane: AccountConcurrencyLane
  items: HighConcurrencyQueueItem[]
  perApiKeyCount: Map<string, number>
}

interface HighConcurrencyQueueItem {
  id: number
  groupKey: string
  lane: AccountConcurrencyLane
  apiKeyKey: string
  accountIds: Set<string>
  accountCapacities: Map<string, HighConcurrencyQueueAccountCapacity>
  enqueuedAtMs: number
  deadlineAtMs: number
  timer: NodeJS.Timeout
  signal?: AbortSignal
  abortListener?: () => void
  resolve: (result: HighConcurrencyQueueWaitResult) => void
}

export type HighConcurrencyQueueRejectReason =
  | 'queue_disabled'
  | 'queue_full'
  | 'api_key_queue_full'
  | 'timeout'
  | 'aborted'

export type HighConcurrencyQueueWaitResult =
  | {
    ready: true
    waitedMs: number
    queueSizeBeforeWake: number
  }
  | {
    ready: false
    reason: HighConcurrencyQueueRejectReason
    waitedMs: number
    queueSize: number
    perApiKeyQueueSize: number
  }

export interface HighConcurrencyQueueWaitInput {
  systemAccountId: string
  groupId: string
  apiKeyId?: string
  accountIds: string[]
  accountConcurrencyLimits?: Record<string, number>
  lane?: AccountConcurrencyLane
  policy?: GroupSchedulingPolicy
  maxWaitMs?: number
  signal?: AbortSignal
}

interface HighConcurrencyQueueAccountCapacity {
  hardLimit: number
  imageLaneLimit: number
}

const queues = new Map<string, HighConcurrencyQueueState>()
const queueItemsByAccountLane = new Map<string, Set<HighConcurrencyQueueItem>>()
let nextQueueItemId = 1

subscribeAccountConcurrencyRelease((event) => {
  wakeQueuesForReleasedAccount(event.accountId, event.lane)
})

export function waitForHighConcurrencyGroupCapacity(input: HighConcurrencyQueueWaitInput): Promise<HighConcurrencyQueueWaitResult> {
  const policy = resolveGroupSchedulingPolicy('high_concurrency', input.policy) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  const configuredMaxQueueWaitMs = normalizeNonNegativeInteger(policy.maxQueueWaitMs, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueWaitMs)
  const maxQueueWaitMs = input.maxWaitMs === undefined
    ? configuredMaxQueueWaitMs
    : Math.min(configuredMaxQueueWaitMs, normalizeNonNegativeInteger(input.maxWaitMs, 0))
  const maxQueueSize = normalizePositiveInteger(policy.maxQueueSize, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueSize)
  const perApiKeyQueueLimit = normalizePositiveInteger(policy.perApiKeyQueueLimit, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.perApiKeyQueueLimit)
  const lane = input.lane === 'image' ? 'image' : 'text'
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    return waitForRedisHighConcurrencyGroupCapacity(input, policy, maxQueueWaitMs, maxQueueSize, perApiKeyQueueLimit, lane)
  }
  const groupKey = highConcurrencyGroupQueueKey(input.systemAccountId, input.groupId, lane)
  const apiKeyKey = input.apiKeyId?.trim() || 'internal'
  const accountCapacities = buildAccountCapacities(input.accountIds, input.accountConcurrencyLimits, policy)
  const state = queues.get(groupKey) ?? createQueueState(groupKey, lane)
  const perApiKeyQueueSize = state.perApiKeyCount.get(apiKeyKey) ?? 0
  if (input.signal?.aborted) {
    return Promise.resolve(rejectedQueueWait('aborted', 0, state.items.length, perApiKeyQueueSize))
  }
  if (state.items.length === 0 && hasImmediateAccountCapacity(input.accountIds, accountCapacities, lane)) {
    return Promise.resolve({
      ready: true,
      waitedMs: 0,
      queueSizeBeforeWake: 0
    })
  }
  if (maxQueueWaitMs <= 0) {
    return Promise.resolve(rejectedQueueWait('queue_disabled', 0, state.items.length, perApiKeyQueueSize))
  }
  if (state.items.length >= maxQueueSize) {
    return Promise.resolve(rejectedQueueWait('queue_full', 0, state.items.length, perApiKeyQueueSize))
  }
  if (perApiKeyQueueSize >= perApiKeyQueueLimit) {
    return Promise.resolve(rejectedQueueWait('api_key_queue_full', 0, state.items.length, perApiKeyQueueSize))
  }
  queues.set(groupKey, state)
  const itemId = nextQueueItemId
  nextQueueItemId += 1
  const enqueuedAtMs = Date.now()
  return new Promise<HighConcurrencyQueueWaitResult>((resolve) => {
    const item: HighConcurrencyQueueItem = {
      id: itemId,
      groupKey,
      lane,
      apiKeyKey,
      accountIds: new Set(input.accountIds.filter(Boolean)),
      accountCapacities,
      enqueuedAtMs,
      deadlineAtMs: enqueuedAtMs + maxQueueWaitMs,
      timer: setTimeout(() => {
        completeQueueItem(item, rejectedQueueWait('timeout', Date.now() - enqueuedAtMs, state.items.length, state.perApiKeyCount.get(apiKeyKey) ?? 0))
      }, maxQueueWaitMs),
      signal: input.signal,
      resolve
    }
    if (input.signal) {
      item.abortListener = () => {
        completeQueueItem(item, rejectedQueueWait('aborted', Date.now() - enqueuedAtMs, state.items.length, state.perApiKeyCount.get(apiKeyKey) ?? 0))
      }
      input.signal.addEventListener('abort', item.abortListener, { once: true })
    }
    state.items.push(item)
    indexQueueItem(item)
    state.perApiKeyCount.set(apiKeyKey, perApiKeyQueueSize + 1)
  })
}

export function highConcurrencyGroupQueueSnapshot(): Array<{
  groupKey: string
  lane: AccountConcurrencyLane
  queueSize: number
  perApiKeyQueueSize: Record<string, number>
}> {
  if (runtimeConfig.runtimeStateDriver === 'redis') return []
  return [...queues.values()].map((state) => ({
    groupKey: state.groupKey,
    lane: state.lane,
    queueSize: state.items.length,
    perApiKeyQueueSize: Object.fromEntries(state.perApiKeyCount.entries())
  }))
}

export function clearHighConcurrencyGroupQueues(): void {
  if (runtimeConfig.runtimeStateDriver === 'redis') {
    queues.clear()
    queueItemsByAccountLane.clear()
    return
  }
  for (const state of queues.values()) {
    for (const item of [...state.items]) {
      completeQueueItem(item, rejectedQueueWait('aborted', Date.now() - item.enqueuedAtMs, state.items.length, state.perApiKeyCount.get(item.apiKeyKey) ?? 0))
    }
  }
  queues.clear()
  queueItemsByAccountLane.clear()
}

async function waitForRedisHighConcurrencyGroupCapacity(
  input: HighConcurrencyQueueWaitInput,
  policy: GroupSchedulingPolicy,
  maxQueueWaitMs: number,
  maxQueueSize: number,
  perApiKeyQueueLimit: number,
  lane: AccountConcurrencyLane
): Promise<HighConcurrencyQueueWaitResult> {
  const startedAtMs = Date.now()
  const groupKey = highConcurrencyGroupQueueKey(input.systemAccountId, input.groupId, lane)
  const apiKeyKey = input.apiKeyId?.trim() || 'internal'
  const capacities = buildAccountCapacities(input.accountIds, input.accountConcurrencyLimits, policy)
  if (input.signal?.aborted) {
    return rejectedQueueWait('aborted', 0, 0, 0)
  }
  const initialSizes = await redisHighConcurrencyQueueSizes(groupKey, apiKeyKey, Date.now())
  if (initialSizes.queueSize === 0 && await hasImmediateAccountCapacityAsync(input.accountIds, capacities, lane)) {
    return {
      ready: true,
      waitedMs: 0,
      queueSizeBeforeWake: 0
    }
  }
  if (maxQueueWaitMs <= 0) {
    return rejectedQueueWait('queue_disabled', 0, initialSizes.queueSize, initialSizes.perApiKeyQueueSize)
  }
  const deadlineAtMs = startedAtMs + maxQueueWaitMs
  const itemId = `${process.pid}:${startedAtMs}:${randomBytes(8).toString('hex')}`
  const enqueueResult = await enqueueRedisHighConcurrencyQueueItem({
    groupKey,
    apiKeyKey,
    itemId,
    deadlineAtMs,
    maxQueueSize,
    perApiKeyQueueLimit
  })
  if (enqueueResult.status === 'queue_full') {
    return rejectedQueueWait('queue_full', 0, enqueueResult.queueSize, enqueueResult.perApiKeyQueueSize)
  }
  if (enqueueResult.status === 'api_key_queue_full') {
    return rejectedQueueWait('api_key_queue_full', 0, enqueueResult.queueSize, enqueueResult.perApiKeyQueueSize)
  }
  while (Date.now() < deadlineAtMs) {
    if (input.signal?.aborted) {
      const sizes = await removeRedisHighConcurrencyQueueItem(groupKey, apiKeyKey, itemId, Date.now())
      return rejectedQueueWait('aborted', Date.now() - startedAtMs, sizes.queueSize, sizes.perApiKeyQueueSize)
    }
    const position = await redisHighConcurrencyQueuePosition(groupKey, apiKeyKey, itemId, Date.now())
    if (!position.present) {
      return rejectedQueueWait('timeout', Date.now() - startedAtMs, position.queueSize, position.perApiKeyQueueSize)
    }
    if (position.rank === 0 && await hasImmediateAccountCapacityAsync(input.accountIds, capacities, lane)) {
      const sizes = await removeRedisHighConcurrencyQueueItem(groupKey, apiKeyKey, itemId, Date.now())
      return {
        ready: true,
        waitedMs: Date.now() - startedAtMs,
        queueSizeBeforeWake: Math.max(1, sizes.queueSize + 1)
      }
    }
    await delay(Math.min(100, Math.max(1, deadlineAtMs - Date.now())))
  }
  const sizes = await removeRedisHighConcurrencyQueueItem(groupKey, apiKeyKey, itemId, Date.now())
  return rejectedQueueWait('timeout', Date.now() - startedAtMs, sizes.queueSize, sizes.perApiKeyQueueSize)
}

function createQueueState(groupKey: string, lane: AccountConcurrencyLane): HighConcurrencyQueueState {
  return {
    groupKey,
    lane,
    items: [],
    perApiKeyCount: new Map()
  }
}

function wakeQueuesForReleasedAccount(accountId: string, releasedLane: AccountConcurrencyLane): void {
  const fallbackLane: AccountConcurrencyLane = releasedLane === 'image' ? 'text' : 'image'
  const candidate = findQueueWakeCandidate(accountId, releasedLane) ?? findQueueWakeCandidate(accountId, fallbackLane)
  if (!candidate) {
    return
  }
  completeQueueItem(candidate.item, {
    ready: true,
    waitedMs: Date.now() - candidate.item.enqueuedAtMs,
    queueSizeBeforeWake: candidate.state.items.length
  })
}

function findQueueWakeCandidate(accountId: string, lane: AccountConcurrencyLane): { state: HighConcurrencyQueueState; item: HighConcurrencyQueueItem } | undefined {
  const candidates = queueItemsByAccountLane.get(accountLaneIndexKey(accountId, lane))
  if (!candidates) {
    return undefined
  }
  for (const item of candidates) {
    const state = queues.get(item.groupKey)
    if (!state) {
      unindexQueueItem(item)
      continue
    }
    if (queueItemCanAcquireAfterRelease(item, accountId)) {
      return { state, item }
    }
  }
  return undefined
}

function queueItemCanAcquireAfterRelease(item: HighConcurrencyQueueItem, accountId: string): boolean {
  const capacity = item.accountCapacities.get(accountId)
  if (!capacity) {
    return true
  }
  if (getAccountCurrentConcurrency(accountId) >= capacity.hardLimit) {
    return false
  }
  if (item.lane !== 'image') {
    return true
  }
  return getAccountCurrentConcurrency(accountId, 'image') < capacity.imageLaneLimit
}

function hasImmediateAccountCapacity(
  accountIds: string[],
  capacities: Map<string, HighConcurrencyQueueAccountCapacity>,
  lane: AccountConcurrencyLane
): boolean {
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    const capacity = capacities.get(accountId)
    if (!capacity) {
      return true
    }
    if (getAccountCurrentConcurrency(accountId) >= capacity.hardLimit) {
      continue
    }
    if (lane !== 'image' || getAccountCurrentConcurrency(accountId, 'image') < capacity.imageLaneLimit) {
      return true
    }
  }
  return false
}

async function hasImmediateAccountCapacityAsync(
  accountIds: string[],
  capacities: Map<string, HighConcurrencyQueueAccountCapacity>,
  lane: AccountConcurrencyLane
): Promise<boolean> {
  const uniqueAccountIds = [...new Set(accountIds.filter(Boolean))]
  const currentConcurrency = await loadAccountCurrentConcurrencyByIdsAsync(uniqueAccountIds)
  const imageLaneConcurrency = lane === 'image'
    ? await loadAccountCurrentConcurrencyByIdsAsync(uniqueAccountIds, 'image')
    : undefined
  for (const accountId of uniqueAccountIds) {
    const capacity = capacities.get(accountId)
    if (!capacity) {
      return true
    }
    if ((currentConcurrency.get(accountId) ?? 0) >= capacity.hardLimit) {
      continue
    }
    if (lane !== 'image' || (imageLaneConcurrency?.get(accountId) ?? 0) < capacity.imageLaneLimit) {
      return true
    }
  }
  return false
}

function completeQueueItem(item: HighConcurrencyQueueItem, result: HighConcurrencyQueueWaitResult): void {
  const state = queues.get(item.groupKey)
  unindexQueueItem(item)
  if (state) {
    const index = state.items.findIndex((candidate) => candidate.id === item.id)
    if (index >= 0) {
      state.items.splice(index, 1)
      decrementPerApiKeyCount(state, item.apiKeyKey)
    }
    if (state.items.length === 0) {
      queues.delete(item.groupKey)
    }
  }
  clearTimeout(item.timer)
  if (item.signal && item.abortListener) {
    item.signal.removeEventListener('abort', item.abortListener)
  }
  item.resolve(result)
}

function indexQueueItem(item: HighConcurrencyQueueItem): void {
  for (const accountId of item.accountIds) {
    const key = accountLaneIndexKey(accountId, item.lane)
    const items = queueItemsByAccountLane.get(key) ?? new Set<HighConcurrencyQueueItem>()
    items.add(item)
    queueItemsByAccountLane.set(key, items)
  }
}

function unindexQueueItem(item: HighConcurrencyQueueItem): void {
  for (const accountId of item.accountIds) {
    const key = accountLaneIndexKey(accountId, item.lane)
    const items = queueItemsByAccountLane.get(key)
    if (!items) {
      continue
    }
    items.delete(item)
    if (items.size === 0) {
      queueItemsByAccountLane.delete(key)
    }
  }
}

function decrementPerApiKeyCount(state: HighConcurrencyQueueState, apiKeyKey: string): void {
  const next = Math.max(0, (state.perApiKeyCount.get(apiKeyKey) ?? 0) - 1)
  if (next <= 0) {
    state.perApiKeyCount.delete(apiKeyKey)
    return
  }
  state.perApiKeyCount.set(apiKeyKey, next)
}

function rejectedQueueWait(
  reason: HighConcurrencyQueueRejectReason,
  waitedMs: number,
  queueSize: number,
  perApiKeyQueueSize: number
): HighConcurrencyQueueWaitResult {
  return {
    ready: false,
    reason,
    waitedMs: Math.max(0, Math.trunc(waitedMs)),
    queueSize,
    perApiKeyQueueSize
  }
}

function highConcurrencyGroupQueueKey(systemAccountId: string, groupId: string, lane: AccountConcurrencyLane): string {
  return `${systemAccountId}:${groupId}:${lane}`
}

interface RedisHighConcurrencyQueueSizes {
  queueSize: number
  perApiKeyQueueSize: number
}

type RedisHighConcurrencyQueueEnqueueResult =
  | ({ status: 'enqueued' } & RedisHighConcurrencyQueueSizes)
  | ({ status: 'queue_full' | 'api_key_queue_full' } & RedisHighConcurrencyQueueSizes)

async function enqueueRedisHighConcurrencyQueueItem(input: {
  groupKey: string
  apiKeyKey: string
  itemId: string
  deadlineAtMs: number
  maxQueueSize: number
  perApiKeyQueueLimit: number
}): Promise<RedisHighConcurrencyQueueEnqueueResult> {
  const nowMs = Date.now()
  const result = await (await redisStateClient()).eval(redisHighConcurrencyQueueEnqueueScript, {
    keys: [
      redisHighConcurrencyGroupQueueKey(input.groupKey),
      redisHighConcurrencyApiKeyQueueKey(input.groupKey, input.apiKeyKey)
    ],
    arguments: [
      input.itemId,
      String(Math.max(0, Math.trunc(input.deadlineAtMs))),
      String(Math.max(0, Math.trunc(nowMs))),
      String(Math.max(1, Math.trunc(input.maxQueueSize))),
      String(Math.max(1, Math.trunc(input.perApiKeyQueueLimit))),
      String(Math.max(1, Math.trunc(input.deadlineAtMs - nowMs + 60_000)))
    ]
  })
  const values = numericRedisArray(result)
  const status = values[0] === 1
    ? 'enqueued'
    : values[0] === 2
      ? 'api_key_queue_full'
      : 'queue_full'
  return {
    status,
    queueSize: values[1] ?? 0,
    perApiKeyQueueSize: values[2] ?? 0
  }
}

async function redisHighConcurrencyQueuePosition(
  groupKey: string,
  apiKeyKey: string,
  itemId: string,
  nowMs: number
): Promise<RedisHighConcurrencyQueueSizes & { present: boolean; rank: number }> {
  const result = await (await redisStateClient()).eval(redisHighConcurrencyQueuePositionScript, {
    keys: [
      redisHighConcurrencyGroupQueueKey(groupKey),
      redisHighConcurrencyApiKeyQueueKey(groupKey, apiKeyKey)
    ],
    arguments: [
      itemId,
      String(Math.max(0, Math.trunc(nowMs)))
    ]
  })
  const values = numericRedisArray(result)
  return {
    present: values[0] === 1,
    rank: values[1] ?? -1,
    queueSize: values[2] ?? 0,
    perApiKeyQueueSize: values[3] ?? 0
  }
}

async function redisHighConcurrencyQueueSizes(
  groupKey: string,
  apiKeyKey: string,
  nowMs: number
): Promise<RedisHighConcurrencyQueueSizes> {
  const result = await (await redisStateClient()).eval(redisHighConcurrencyQueueSizesScript, {
    keys: [
      redisHighConcurrencyGroupQueueKey(groupKey),
      redisHighConcurrencyApiKeyQueueKey(groupKey, apiKeyKey)
    ],
    arguments: [String(Math.max(0, Math.trunc(nowMs)))]
  })
  const values = numericRedisArray(result)
  return {
    queueSize: values[0] ?? 0,
    perApiKeyQueueSize: values[1] ?? 0
  }
}

async function removeRedisHighConcurrencyQueueItem(
  groupKey: string,
  apiKeyKey: string,
  itemId: string,
  nowMs: number
): Promise<RedisHighConcurrencyQueueSizes> {
  const result = await (await redisStateClient()).eval(redisHighConcurrencyQueueRemoveScript, {
    keys: [
      redisHighConcurrencyGroupQueueKey(groupKey),
      redisHighConcurrencyApiKeyQueueKey(groupKey, apiKeyKey)
    ],
    arguments: [
      itemId,
      String(Math.max(0, Math.trunc(nowMs)))
    ]
  })
  const values = numericRedisArray(result)
  return {
    queueSize: values[0] ?? 0,
    perApiKeyQueueSize: values[1] ?? 0
  }
}

function redisStateClient(): Promise<RedisCommandClient> {
  const redisUrl = runtimeConfig.redis.stateUrl
  if (!redisUrl) {
    throw new Error('JUHE_AI_REDIS_STATE_URL 在 Redis runtime state driver 下必须配置')
  }
  return getRedisClient(redisUrl)
}

function redisHighConcurrencyGroupQueueKey(groupKey: string): string {
  return redisNamespacedKey(`juhe-ai:state:high-concurrency-queue:${sanitizeRedisKeyPart(groupKey)}`)
}

function redisHighConcurrencyApiKeyQueueKey(groupKey: string, apiKeyKey: string): string {
  return redisNamespacedKey(`juhe-ai:state:high-concurrency-queue:${sanitizeRedisKeyPart(groupKey)}:api-key:${sanitizeRedisKeyPart(apiKeyKey)}`)
}

function sanitizeRedisKeyPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'default'
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

function accountLaneIndexKey(accountId: string, lane: AccountConcurrencyLane): string {
  return `${lane}:${accountId}`
}

function buildAccountCapacities(
  accountIds: string[],
  accountConcurrencyLimits: Record<string, number> | undefined,
  policy: GroupSchedulingPolicy
): Map<string, HighConcurrencyQueueAccountCapacity> {
  const capacities = new Map<string, HighConcurrencyQueueAccountCapacity>()
  for (const accountId of new Set(accountIds.filter(Boolean))) {
    const hardLimit = normalizePositiveInteger(accountConcurrencyLimits?.[accountId], 1)
    capacities.set(accountId, {
      hardLimit,
      imageLaneLimit: effectiveImageLaneConcurrencyLimit({
        accountConcurrencyLimit: hardLimit,
        policy
      })
    })
  }
  return capacities
}

function normalizePositiveInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(1, Math.trunc(numeric)) : fallback
}

function normalizeNonNegativeInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : fallback
}

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, Math.max(0, ms)))
}

const redisHighConcurrencyQueueEnqueueScript = `
local item_id = ARGV[1]
local deadline_at_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local max_queue_size = tonumber(ARGV[4])
local per_api_key_queue_limit = tonumber(ARGV[5])
local ttl_ms = tonumber(ARGV[6])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
local queue_size = redis.call('ZCARD', KEYS[1])
local per_api_key_queue_size = redis.call('ZCARD', KEYS[2])
if queue_size >= max_queue_size then
  return {0, queue_size, per_api_key_queue_size}
end
if per_api_key_queue_size >= per_api_key_queue_limit then
  return {2, queue_size, per_api_key_queue_size}
end
redis.call('ZADD', KEYS[1], deadline_at_ms, item_id)
redis.call('ZADD', KEYS[2], deadline_at_ms, item_id)
redis.call('PEXPIRE', KEYS[1], ttl_ms)
redis.call('PEXPIRE', KEYS[2], ttl_ms)
return {1, queue_size + 1, per_api_key_queue_size + 1}
`

const redisHighConcurrencyQueuePositionScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
local rank = redis.call('ZRANK', KEYS[1], item_id)
if rank == false then
  return {0, -1, redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
end
return {1, rank, redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
`

const redisHighConcurrencyQueueSizesScript = `
local now_ms = tonumber(ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
return {redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
`

const redisHighConcurrencyQueueRemoveScript = `
local item_id = ARGV[1]
local now_ms = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now_ms)
redis.call('ZREM', KEYS[1], item_id)
redis.call('ZREM', KEYS[2], item_id)
return {redis.call('ZCARD', KEYS[1]), redis.call('ZCARD', KEYS[2])}
`
