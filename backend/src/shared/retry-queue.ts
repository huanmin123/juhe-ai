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
  run: (item: T, context: RetryQueueRunContext) => Promise<RetryQueueRunResult> | RetryQueueRunResult
  onSuccess?: (event: RetryQueueEvent<T>) => void
  onFailure?: (event: RetryQueueEvent<T> & { error?: unknown }) => void
  onRetryScheduled?: (event: RetryQueueRetryEvent<T> & { error?: unknown }) => void
  onExhausted?: (event: RetryQueueEvent<T> & { error?: unknown }) => void
}

export interface RetryQueue<T> {
  readonly name: string
  enqueue(key: string, item: T): boolean
  delete(key: string): void
  clear(): void
  snapshot(): RetryQueueSnapshot
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
}

export function createRetryQueue<T>(options: RetryQueueOptions<T>): RetryQueue<T> {
  const items = new Map<string, RetryQueueItem<T>>()
  const concurrency = Math.max(1, Math.trunc(options.concurrency ?? 1))
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
      const item = nextDueItem()
      if (!item) {
        break
      }
      item.running = true
      void runItem(item)
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

  const runningCount = (): number => [...items.values()].filter((item) => item.running).length

  const nextDueItem = (): RetryQueueItem<T> | undefined => {
    const now = Date.now()
    return [...items.values()]
      .filter((item) => !item.running && item.nextRunAtMs <= now)
      .sort((left, right) => left.nextRunAtMs - right.nextRunAtMs || left.key.localeCompare(right.key))[0]
  }

  const nextPendingRunAtMs = (): number | undefined => {
    return [...items.values()]
      .filter((item) => !item.running)
      .reduce<number | undefined>((next, item) => next === undefined ? item.nextRunAtMs : Math.min(next, item.nextRunAtMs), undefined)
  }

  return {
    name: options.name,
    enqueue: (key, item) => {
      if (items.has(key)) {
        return false
      }
      items.set(key, {
        key,
        item,
        attemptIndex: 0,
        nextRunAtMs: Date.now(),
        running: false
      })
      scheduleDrain(0)
      return true
    },
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
    snapshot: () => {
      const nextRunAtMs = nextPendingRunAtMs()
      return {
        name: options.name,
        pendingCount: [...items.values()].filter((item) => !item.running).length,
        runningCount: runningCount(),
        nextRunAt: nextRunAtMs === undefined ? undefined : new Date(nextRunAtMs).toISOString()
      }
    }
  }
}
