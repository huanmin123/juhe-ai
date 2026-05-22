import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, resolveGroupSchedulingPolicy } from '../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../domain/types.js'
import { subscribeAccountConcurrencyRelease } from '../../shared/account-concurrency.js'

interface HighConcurrencyQueueState {
  groupKey: string
  items: HighConcurrencyQueueItem[]
  perApiKeyCount: Map<string, number>
}

interface HighConcurrencyQueueItem {
  id: number
  groupKey: string
  apiKeyKey: string
  accountIds: Set<string>
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
  policy?: GroupSchedulingPolicy
  signal?: AbortSignal
}

const queues = new Map<string, HighConcurrencyQueueState>()
let nextQueueItemId = 1

subscribeAccountConcurrencyRelease((accountId) => {
  wakeQueuesForReleasedAccount(accountId)
})

export function waitForHighConcurrencyGroupCapacity(input: HighConcurrencyQueueWaitInput): Promise<HighConcurrencyQueueWaitResult> {
  const policy = resolveGroupSchedulingPolicy('high_concurrency', input.policy) ?? DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY
  const maxQueueWaitMs = normalizeNonNegativeInteger(policy.maxQueueWaitMs, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueWaitMs)
  const maxQueueSize = normalizePositiveInteger(policy.maxQueueSize, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.maxQueueSize)
  const perApiKeyQueueLimit = normalizePositiveInteger(policy.perApiKeyQueueLimit, DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY.perApiKeyQueueLimit)
  const groupKey = highConcurrencyGroupQueueKey(input.systemAccountId, input.groupId)
  const apiKeyKey = input.apiKeyId?.trim() || 'internal'
  const state = queues.get(groupKey) ?? createQueueState(groupKey)
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
      apiKeyKey,
      accountIds: new Set(input.accountIds.filter(Boolean)),
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
  queueSize: number
  perApiKeyQueueSize: Record<string, number>
}> {
  return [...queues.values()].map((state) => ({
    groupKey: state.groupKey,
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

function createQueueState(groupKey: string): HighConcurrencyQueueState {
  return {
    groupKey,
    items: [],
    perApiKeyCount: new Map()
  }
}

function wakeQueuesForReleasedAccount(accountId: string): void {
  for (const state of queues.values()) {
    const item = state.items.find((candidate) => candidate.accountIds.has(accountId))
    if (!item) {
      continue
    }
    completeQueueItem(item, {
      ready: true,
      waitedMs: Date.now() - item.enqueuedAtMs,
      queueSizeBeforeWake: state.items.length
    })
  }
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

function highConcurrencyGroupQueueKey(systemAccountId: string, groupId: string): string {
  return `${systemAccountId}:${groupId}`
}

function normalizePositiveInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(1, Math.trunc(numeric)) : fallback
}

function normalizeNonNegativeInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : fallback
}
