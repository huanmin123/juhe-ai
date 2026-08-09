import pLimit from 'p-limit'

import {
  retryDelayMs,
  shouldRetryPolicyAttempt,
  type RetryPolicy
} from './retry-policy.js'

export interface RetryQueueRunContext {
  attemptIndex: number
  retryNumber: number
}

export type RetryQueueRunResult = boolean | {
  success: boolean
  retry?: boolean
}

export interface RetryQueueEvent<T> {
  key: string
  item: T
  attemptIndex: number
  retryNumber: number
}

export interface RetryQueueRetryEvent<T> extends RetryQueueEvent<T> {
  delayMs: number
  nextAttemptAtMs: number
}

export interface RetryQueueOptions<T> {
  name: string
  policy: RetryPolicy
  concurrency?: number
  reservedPriorityConcurrency?: {
    priorityAtMost: number
    slots: number
  }
  run: (item: T, context: RetryQueueRunContext) => Promise<RetryQueueRunResult> | RetryQueueRunResult
  onSuccess?: (event: RetryQueueEvent<T>) => void
  onFailure?: (event: RetryQueueEvent<T> & { error?: unknown }) => void
  onRetryScheduled?: (event: RetryQueueRetryEvent<T> & { error?: unknown }) => void
  onExhausted?: (event: RetryQueueEvent<T> & { error?: unknown }) => void
}

export interface RetryQueue<T> {
  readonly name: string
  enqueue(key: string, item: T, options?: RetryQueueEnqueueOptions): boolean
  hasFollowUp(key: string): boolean
  delete(key: string): void
  clear(): void
  setConcurrency(concurrency: number): void
  snapshot(): RetryQueueSnapshot
}

export interface RetryQueueEnqueueOptions {
  priority?: number
  delayMs?: number
  replaceExisting?: boolean
  replaceExistingOnlyIfHigherPriority?: boolean
  /**
   * Adds at most one latest follow-up only while the keyed item is running.
   * Pending items continue to use the normal replacement policy.
   */
  followUpWhenRunning?: boolean
}

export interface RetryQueueSnapshot {
  name: string
  pendingCount: number
  runningCount: number
  nextRunAt?: string
}

interface RetryQueueItem<T> {
  key: string
  item: T
  attemptIndex: number
  nextRunAtMs: number
  running: boolean
  priority: number
  followUp?: {
    item: T
    priority: number
    nextRunAtMs: number
  }
}

export function createRetryQueue<T>(options: RetryQueueOptions<T>): RetryQueue<T> {
  const items = new Map<string, RetryQueueItem<T>>()
  let concurrency = normalizedConcurrency(options.concurrency)
  const limit = pLimit(concurrency)
  let timer: NodeJS.Timeout | undefined
  let timerDueAtMs: number | undefined

  const scheduleDrain = (delayMs: number): void => {
    const dueAtMs = Date.now() + Math.max(0, Math.trunc(delayMs))
    if (timer) {
      if (timerDueAtMs !== undefined && timerDueAtMs <= dueAtMs) {
        return
      }
      clearTimeout(timer)
      timer = undefined
      timerDueAtMs = undefined
    }
    timerDueAtMs = dueAtMs
    timer = setTimeout(() => {
      timer = undefined
      timerDueAtMs = undefined
      void drain()
    }, Math.max(0, dueAtMs - Date.now()))
    timer.unref()
  }

  const scheduleNext = (): void => {
    if (timer) {
      return
    }
    if (runningCount() >= concurrency) {
      return
    }
    const nextRunAtMs = nextPendingRunAtMs()
    if (nextRunAtMs === undefined) {
      return
    }
    scheduleDrain(Math.max(0, nextRunAtMs - Date.now()))
  }

  const drain = async (): Promise<void> => {
    while (runningCount() < concurrency) {
      const item = nextDueItem(concurrency, options.reservedPriorityConcurrency)
      if (!item) {
        break
      }
      item.running = true
      void limit(runItem, item)
    }
    scheduleNext()
  }

  const runItem = async (queueItem: RetryQueueItem<T>): Promise<void> => {
    let error: unknown
    let success = false
    let retry = true
    const retryNumber = queueItem.attemptIndex + 1
    try {
      const result = await options.run(queueItem.item, {
        attemptIndex: queueItem.attemptIndex,
        retryNumber
      })
      if (typeof result === 'boolean') {
        success = result
      } else {
        success = result.success
        retry = result.retry !== false
      }
    } catch (runError) {
      error = runError
      success = false
    }

    queueItem.running = false
    if (items.get(queueItem.key) !== queueItem) {
      scheduleNext()
      return
    }

    const event = {
      key: queueItem.key,
      item: queueItem.item,
      attemptIndex: queueItem.attemptIndex,
      retryNumber
    }

    const followUp = queueItem.followUp
    if (followUp) {
      if (success) {
        options.onSuccess?.(event)
      } else {
        options.onFailure?.({ ...event, error })
      }
      queueItem.item = followUp.item
      queueItem.priority = followUp.priority
      queueItem.attemptIndex = 0
      queueItem.nextRunAtMs = followUp.nextRunAtMs
      queueItem.followUp = undefined
      scheduleNext()
      return
    }

    if (success) {
      items.delete(queueItem.key)
      options.onSuccess?.(event)
      scheduleNext()
      return
    }

    options.onFailure?.({ ...event, error })
    if (retry && shouldRetryPolicyAttempt(queueItem.attemptIndex, options.policy)) {
      const delayMs = retryDelayMs(options.policy, retryNumber)
      queueItem.attemptIndex += 1
      queueItem.nextRunAtMs = Date.now() + delayMs
      options.onRetryScheduled?.({
        ...event,
        delayMs,
        nextAttemptAtMs: queueItem.nextRunAtMs,
        error
      })
      scheduleNext()
      return
    }

    items.delete(queueItem.key)
    options.onExhausted?.({ ...event, error })
    scheduleNext()
  }

  const runningCount = (): number => {
    let count = 0
    for (const item of items.values()) {
      if (item.running) count += 1
    }
    return count
  }

  const nextDueItem = (
    currentConcurrency: number,
    reservation: RetryQueueOptions<T>['reservedPriorityConcurrency']
  ): RetryQueueItem<T> | undefined => {
    const now = Date.now()
    let next: RetryQueueItem<T> | undefined
    const normalRunningLimit = reservedNormalRunningLimit(currentConcurrency, reservation)
    const normalRunningCount = reservation ? runningCountAbovePriority(reservation.priorityAtMost) : 0
    for (const item of items.values()) {
      if (item.running || item.nextRunAtMs > now) continue
      if (reservation && item.priority > reservation.priorityAtMost && normalRunningCount >= normalRunningLimit) continue
      if (!next || compareRetryQueueItems(item, next) < 0) {
        next = item
      }
    }
    return next
  }

  const nextPendingRunAtMs = (): number | undefined => {
    let nextRunAtMs: number | undefined
    const reservation = options.reservedPriorityConcurrency
    const normalRunningLimit = reservedNormalRunningLimit(concurrency, reservation)
    const normalRunningCount = reservation ? runningCountAbovePriority(reservation.priorityAtMost) : 0
    for (const item of items.values()) {
      if (item.running) continue
      if (reservation && item.priority > reservation.priorityAtMost && normalRunningCount >= normalRunningLimit) continue
      nextRunAtMs = nextRunAtMs === undefined ? item.nextRunAtMs : Math.min(nextRunAtMs, item.nextRunAtMs)
    }
    return nextRunAtMs
  }

  const pendingCount = (): number => {
    let count = 0
    for (const item of items.values()) {
      if (!item.running) count += 1
    }
    return count
  }

  const runningCountAbovePriority = (priority: number): number => {
    let count = 0
    for (const item of items.values()) {
      if (item.running && item.priority > priority) count += 1
    }
    return count
  }

  return {
    name: options.name,
    enqueue: (key, item, enqueueOptions = {}) => {
      const priority = normalizedPriority(enqueueOptions.priority)
      const nextRunAtMs = Date.now() + Math.max(0, Math.trunc(enqueueOptions.delayMs ?? 0))
      const existing = items.get(key)
      if (existing) {
        if (existing.running && enqueueOptions.followUpWhenRunning) {
          const existingFollowUpPriority = existing.followUp?.priority
          if (
            existingFollowUpPriority === undefined
            && enqueueOptions.replaceExistingOnlyIfHigherPriority
            && priority > existing.priority
          ) return false
          if (existingFollowUpPriority !== undefined && existingFollowUpPriority < priority) return false
          existing.followUp = {
            item,
            priority: existingFollowUpPriority === undefined ? priority : Math.min(existingFollowUpPriority, priority),
            nextRunAtMs
          }
          scheduleDrain(0)
          return true
        }
        if (!enqueueOptions.replaceExisting) return false
        const existingPriority = existing.followUp?.priority ?? existing.priority
        if (enqueueOptions.replaceExistingOnlyIfHigherPriority && priority >= existingPriority) return false
        const replacementPriority = Math.min(existing.followUp?.priority ?? existing.priority, priority)
        if (existing.running) {
          existing.followUp = { item, priority: replacementPriority, nextRunAtMs }
        } else {
          existing.item = item
          existing.priority = replacementPriority
          existing.attemptIndex = 0
          existing.nextRunAtMs = nextRunAtMs
        }
        scheduleDrain(0)
        return true
      }
      items.set(key, {
        key,
        item,
        attemptIndex: 0,
        nextRunAtMs,
        running: false,
        priority
      })
      scheduleDrain(0)
      return true
    },
    hasFollowUp: (key) => items.get(key)?.followUp !== undefined,
    delete: (key) => {
      items.delete(key)
      scheduleNext()
    },
    clear: () => {
      items.clear()
      if (timer) {
        clearTimeout(timer)
        timer = undefined
        timerDueAtMs = undefined
      }
    },
    setConcurrency: (nextConcurrency) => {
      concurrency = normalizedConcurrency(nextConcurrency)
      limit.concurrency = concurrency
      scheduleNext()
    },
    snapshot: () => {
      const nextRunAtMs = nextPendingRunAtMs()
      return {
        name: options.name,
        pendingCount: pendingCount(),
        runningCount: runningCount(),
        nextRunAt: nextRunAtMs === undefined ? undefined : new Date(nextRunAtMs).toISOString()
      }
    }
  }
}

function normalizedConcurrency(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) {
    return 1
  }
  return Math.max(1, Math.trunc(value))
}

function compareRetryQueueItems<T>(left: RetryQueueItem<T>, right: RetryQueueItem<T>): number {
  return left.priority - right.priority || left.nextRunAtMs - right.nextRunAtMs || left.key.localeCompare(right.key)
}

function normalizedPriority(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : 0
}

function reservedNormalRunningLimit(
  concurrency: number,
  reservation: RetryQueueOptions<unknown>['reservedPriorityConcurrency']
): number {
  if (!reservation) return concurrency
  const slots = Math.max(0, Math.min(Math.trunc(reservation.slots), Math.max(0, concurrency - 1)))
  return concurrency - slots
}
