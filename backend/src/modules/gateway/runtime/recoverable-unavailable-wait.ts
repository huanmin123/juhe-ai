import { getRequestLogger } from '../../../shared/request-context.js'
import type { AuditCaptureContext } from '../audit/capture.service.js'

export const recoverableUnavailableMaxWaitMs = 30_000
export const recoverableUnavailableCheckIntervalMs = 5_000
const recoverableUnavailableDueRetryDelayMs = 250

type RecoverableUnavailableWaitSkippedReason =
  | 'no_retry_time'
  | 'retry_after_exceeds_window'
  | 'aborted'

export interface RecoverableUnavailableWaitResult<T> {
  state: T
  waitedMs: number
  checkCount: number
  ready: boolean
  timedOut: boolean
  skippedReason?: RecoverableUnavailableWaitSkippedReason
}

interface RecoverableUnavailableWaitInput<T> {
  scopeKey: string
  reason: string
  initialState: T
  refresh: () => T | Promise<T>
  isReady: (state: T) => boolean
  nextRetryAfterMs: (state: T) => number | undefined
  auditCapture: AuditCaptureContext
  signal?: AbortSignal
  waitWithoutRetryAfter?: boolean
  maxWaitMs?: number
  checkIntervalMs?: number
}

const sharedWaits = new Map<string, { untilMs: number; promise: Promise<void> }>()

export async function waitForRecoverableUnavailableState<T>(
  input: RecoverableUnavailableWaitInput<T>
): Promise<RecoverableUnavailableWaitResult<T>> {
  const maxWaitMs = normalizePositiveMs(input.maxWaitMs, recoverableUnavailableMaxWaitMs)
  const checkIntervalMs = normalizePositiveMs(input.checkIntervalMs, recoverableUnavailableCheckIntervalMs)
  const startedAtMs = Date.now()
  let state = input.initialState
  let checkCount = 0

  if (input.isReady(state)) {
    return {
      state,
      waitedMs: 0,
      checkCount,
      ready: true,
      timedOut: false
    }
  }

  input.auditCapture.addGatewayMetadata({
    label: 'recoverable_unavailable_wait',
    metadata: {
      reason: input.reason,
      scopeKey: input.scopeKey,
      maxWaitMs,
      checkIntervalMs,
      nextRetryAfterMs: input.nextRetryAfterMs(state)
    }
  })

  while (!input.signal?.aborted) {
    const elapsedMs = Date.now() - startedAtMs
    const remainingMs = maxWaitMs - elapsedMs
    if (remainingMs <= 0) {
      return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, false, true)
    }

    const delayMs = nextRecoverableWaitDelayMs({
      nextRetryAfterMs: input.nextRetryAfterMs(state),
      remainingMs,
      checkIntervalMs,
      waitWithoutRetryAfter: input.waitWithoutRetryAfter === true
    })
    if (!delayMs.wait) {
      const timedOut = delayMs.skippedReason === 'retry_after_exceeds_window' && checkCount > 0
      return finalizeRecoverableUnavailableWait(
        input,
        state,
        startedAtMs,
        checkCount,
        false,
        timedOut,
        delayMs.skippedReason
      )
    }

    getRequestLogger().info({
      event: 'gateway_recoverable_unavailable_wait_scheduled',
      reason: input.reason,
      scopeKey: input.scopeKey,
      delayMs: delayMs.delayMs,
      remainingMs
    }, '本地可恢复阻塞短等后重新检查调度候选')

    await waitForSharedRecoverableTick(input.scopeKey, input.reason, delayMs.delayMs, input.signal)
    if (input.signal?.aborted) {
      return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, false, false, 'aborted')
    }
    checkCount += 1
    state = await input.refresh()
    if (input.isReady(state)) {
      return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, true, false)
    }
  }

  return finalizeRecoverableUnavailableWait(input, state, startedAtMs, checkCount, false, false, 'aborted')
}

function finalizeRecoverableUnavailableWait<T>(
  input: RecoverableUnavailableWaitInput<T>,
  state: T,
  startedAtMs: number,
  checkCount: number,
  ready: boolean,
  timedOut: boolean,
  skippedReason?: RecoverableUnavailableWaitSkippedReason
): RecoverableUnavailableWaitResult<T> {
  const waitedMs = Date.now() - startedAtMs
  input.auditCapture.addGatewayMetadata({
    label: 'recoverable_unavailable_wait_result',
    metadata: {
      reason: input.reason,
      scopeKey: input.scopeKey,
      waitedMs,
      checkCount,
      ready,
      timedOut,
      skippedReason,
      nextRetryAfterMs: input.nextRetryAfterMs(state)
    }
  })
  return {
    state,
    waitedMs,
    checkCount,
    ready,
    timedOut,
    skippedReason
  }
}

function nextRecoverableWaitDelayMs(input: {
  nextRetryAfterMs: number | undefined
  remainingMs: number
  checkIntervalMs: number
  waitWithoutRetryAfter: boolean
}): { wait: true; delayMs: number } | { wait: false; skippedReason: RecoverableUnavailableWaitSkippedReason } {
  const remainingMs = Math.max(0, Math.trunc(input.remainingMs))
  if (remainingMs <= 0) {
    return { wait: false, skippedReason: 'retry_after_exceeds_window' }
  }
  const nextRetryAfterMs = normalizeOptionalMs(input.nextRetryAfterMs)
  if (nextRetryAfterMs === undefined) {
    return input.waitWithoutRetryAfter
      ? { wait: true, delayMs: Math.min(input.checkIntervalMs, remainingMs) }
      : { wait: false, skippedReason: 'no_retry_time' }
  }
  if (!input.waitWithoutRetryAfter && nextRetryAfterMs > remainingMs) {
    return { wait: false, skippedReason: 'retry_after_exceeds_window' }
  }
  const targetDelayMs = nextRetryAfterMs <= 0 ? recoverableUnavailableDueRetryDelayMs : nextRetryAfterMs
  return {
    wait: true,
    delayMs: Math.min(Math.max(50, targetDelayMs), input.checkIntervalMs, remainingMs)
  }
}

async function waitForSharedRecoverableTick(
  scopeKey: string,
  reason: string,
  delayMs: number,
  signal?: AbortSignal
): Promise<void> {
  if (delayMs <= 0 || signal?.aborted) {
    return
  }
  const key = `${reason}:${scopeKey}`
  const now = Date.now()
  const existing = sharedWaits.get(key)
  const promise = existing && existing.untilMs > now
    ? existing.promise
    : createSharedWait(key, delayMs, now)
  await waitForPromiseOrAbort(promise, signal)
}

function createSharedWait(key: string, delayMs: number, now: number): Promise<void> {
  let timer: NodeJS.Timeout | undefined
  const promise = new Promise<void>((resolve) => {
    timer = setTimeout(resolve, delayMs)
    timer.unref()
  }).finally(() => {
    if (timer) {
      clearTimeout(timer)
    }
    if (sharedWaits.get(key)?.promise === promise) {
      sharedWaits.delete(key)
    }
  })
  sharedWaits.set(key, {
    untilMs: now + delayMs,
    promise
  })
  return promise
}

async function waitForPromiseOrAbort(promise: Promise<void>, signal?: AbortSignal): Promise<void> {
  if (!signal) {
    await promise
    return
  }
  if (signal.aborted) {
    return
  }
  await Promise.race([
    promise,
    new Promise<void>((resolve) => {
      const listener = () => resolve()
      signal.addEventListener('abort', listener, { once: true })
      promise.finally(() => signal.removeEventListener('abort', listener))
    })
  ])
}

function normalizeOptionalMs(value: number | undefined): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : undefined
}

function normalizePositiveMs(value: number | undefined, fallback: number): number {
  const raw = typeof value === 'number' && Number.isFinite(value) ? value : fallback
  return Math.max(1, Math.trunc(raw))
}
