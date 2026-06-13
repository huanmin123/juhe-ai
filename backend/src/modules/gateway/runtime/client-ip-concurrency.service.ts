import { DEFAULT_HIGH_CONCURRENCY_GROUP_SCHEDULING_POLICY, resolveGroupSchedulingPolicy } from '../../../domain/group-scheduling.js'
import type { GroupSchedulingPolicy } from '../../../domain/types.js'

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
  return [...states.values()].map((state) => ({
    key: state.key,
    current: state.current,
    queueSize: state.items.length
  }))
}

export function clearClientIpConcurrency(): void {
  for (const state of states.values()) {
    for (const item of [...state.items]) {
      completeQueueItem(item, rejectedDecision('aborted', state, 1, Date.now() - item.enqueuedAtMs))
    }
  }
  states.clear()
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

function normalizeNonNegativeInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : fallback
}

function normalizePositiveInteger(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(numeric) ? Math.max(1, Math.trunc(numeric)) : fallback
}

function noop(): void {}
