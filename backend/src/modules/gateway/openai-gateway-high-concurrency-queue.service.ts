import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, effectiveImageLaneConcurrencyLimit, resolveGroupSchedulingPolicy } from '../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../domain/types.js'
import { getAccountCurrentConcurrency, subscribeAccountConcurrencyRelease, type AccountConcurrencyLane } from '../../shared/account-concurrency.js'

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
  signal?: AbortSignal
}

interface HighConcurrencyQueueAccountCapacity {
  hardLimit: number
  imageLaneLimit: number
}

const queues = new Map<string, HighConcurrencyQueueState>()
let nextQueueItemId = 1

subscribeAccountConcurrencyRelease((event) => {
  wakeQueuesForReleasedAccount(event.accountId, event.lane)
})

export function waitForHighConcurrencyGroupCapacity(input: HighConcurrencyQueueWaitInput): Promise<HighConcurrencyQueueWaitResult> {
  const policy = resolveGroupSchedulingPolicy('high_concurrency', input.policy) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  const maxQueueWaitMs = normalizeNonNegativeInteger(policy.maxQueueWaitMs, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueWaitMs)
  const maxQueueSize = normalizePositiveInteger(policy.maxQueueSize, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueSize)
  const perApiKeyQueueLimit = normalizePositiveInteger(policy.perApiKeyQueueLimit, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.perApiKeyQueueLimit)
  const lane = input.lane === 'image' ? 'image' : 'text'
  const groupKey = highConcurrencyGroupQueueKey(input.systemAccountId, input.groupId, lane)
  const apiKeyKey = input.apiKeyId?.trim() || 'internal'
  const accountCapacities = buildAccountCapacities(input.accountIds, input.accountConcurrencyLimits, policy)
  const state = queues.get(groupKey) ?? createQueueState(groupKey, lane)
  const perApiKeyQueueSize = state.perApiKeyCount.get(apiKeyKey) ?? 0
  if (input.signal?.aborted) {
    return Promise.resolve(rejectedQueueWait('aborted', 0, state.items.length, perApiKeyQueueSize))
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
    state.perApiKeyCount.set(apiKeyKey, perApiKeyQueueSize + 1)
  })
}

export function highConcurrencyGroupQueueSnapshot(): Array<{
  groupKey: string
  lane: AccountConcurrencyLane
  queueSize: number
  perApiKeyQueueSize: Record<string, number>
}> {
  return [...queues.values()].map((state) => ({
    groupKey: state.groupKey,
    lane: state.lane,
    queueSize: state.items.length,
    perApiKeyQueueSize: Object.fromEntries(state.perApiKeyCount.entries())
  }))
}

export function clearHighConcurrencyGroupQueues(): void {
  for (const state of queues.values()) {
    for (const item of [...state.items]) {
      completeQueueItem(item, rejectedQueueWait('aborted', Date.now() - item.enqueuedAtMs, state.items.length, state.perApiKeyCount.get(item.apiKeyKey) ?? 0))
    }
  }
  queues.clear()
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
  for (const state of queues.values()) {
    if (state.lane !== lane) {
      continue
    }
    const item = state.items.find((candidate) => candidate.accountIds.has(accountId) && queueItemCanAcquireAfterRelease(candidate, accountId))
    if (item) {
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

function completeQueueItem(item: HighConcurrencyQueueItem, result: HighConcurrencyQueueWaitResult): void {
  const state = queues.get(item.groupKey)
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
