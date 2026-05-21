export type RetryDelayStrategy = 'fixed' | 'exponential' | 'sequence'

export interface RetryPolicy {
  name: string
  strategy: RetryDelayStrategy
  delayMs: number
  maxDelayMs?: number
  factor?: number
  delaysMs?: number[]
  maxRetries?: number
}

export function fixedRetryPolicy(name: string, delayMs: number, maxRetries?: number): RetryPolicy {
  return {
    name,
    strategy: 'fixed',
    delayMs: normalizeDelayMs(delayMs),
    maxRetries: normalizeOptionalRetryCount(maxRetries)
  }
}

export function exponentialRetryPolicy(
  name: string,
  delayMs: number,
  maxDelayMs: number,
  factor = 2,
  maxRetries?: number
): RetryPolicy {
  return {
    name,
    strategy: 'exponential',
    delayMs: normalizeDelayMs(delayMs),
    maxDelayMs: normalizeDelayMs(maxDelayMs),
    factor: Number.isFinite(factor) && factor > 1 ? factor : 2,
    maxRetries: normalizeOptionalRetryCount(maxRetries)
  }
}

export function sequenceRetryPolicy(name: string, delaysMs: number[], maxRetries = delaysMs.length): RetryPolicy {
  const normalizedDelays = delaysMs.map(normalizeDelayMs).filter((delayMs) => delayMs >= 0)
  return {
    name,
    strategy: 'sequence',
    delayMs: normalizedDelays[0] ?? 0,
    delaysMs: normalizedDelays,
    maxRetries: normalizeRetryCount(maxRetries)
  }
}

export function retryDelayMs(policy: RetryPolicy, retryNumber = 1): number {
  const normalizedRetryNumber = Math.max(1, Math.trunc(retryNumber))
  if (policy.strategy === 'fixed') {
    return policy.delayMs
  }
  if (policy.strategy === 'sequence') {
    const delays = policy.delaysMs ?? []
    return delays[Math.min(normalizedRetryNumber - 1, Math.max(0, delays.length - 1))] ?? policy.delayMs
  }
  const maxDelayMs = policy.maxDelayMs ?? policy.delayMs
  const factor = policy.factor ?? 2
  return Math.min(policy.delayMs * factor ** (normalizedRetryNumber - 1), maxDelayMs)
}

export function retryDueAtMs(policy: RetryPolicy, retryNumber = 1, nowMs = Date.now()): number {
  return nowMs + retryDelayMs(policy, retryNumber)
}

export function retryAttemptCount(policy: RetryPolicy): number {
  return retryCount(policy) + 1
}

export function retryCount(policy: RetryPolicy): number {
  return normalizeRetryCount(policy.maxRetries)
}

export function shouldRetryPolicyAttempt(attemptIndex: number, policy: RetryPolicy): boolean {
  return shouldRetryAttempt(attemptIndex, retryCount(policy))
}

export function shouldRetryAttempt(attemptIndex: number, maxRetries: number): boolean {
  return Math.max(0, Math.trunc(attemptIndex)) < normalizeRetryCount(maxRetries)
}

export function normalizeRetryCount(value: number | undefined, fallback = 0, max?: number): number {
  const raw = typeof value === 'number' && Number.isFinite(value) ? value : fallback
  const normalized = Math.max(0, Math.trunc(raw))
  return typeof max === 'number' && Number.isFinite(max) ? Math.min(normalized, Math.max(0, Math.trunc(max))) : normalized
}

export async function waitForRetryDelay(policy: RetryPolicy, retryNumber = 1, options: RetryDelayWaitOptions = {}): Promise<void> {
  await waitForRetryDelayMs(retryDelayMs(policy, retryNumber), options)
}

export async function waitForRetryDelayMs(delayMs: number, options: RetryDelayWaitOptions = {}): Promise<void> {
  const normalizedDelayMs = normalizeDelayMs(delayMs)
  if (normalizedDelayMs <= 0 || options.signal?.aborted) {
    return
  }
  await new Promise<void>((resolve, reject) => {
    let settled = false
    let timer: NodeJS.Timeout | undefined
    let abortListener: (() => void) | undefined
    const finish = (error?: Error) => {
      if (settled) return
      settled = true
      if (timer) clearTimeout(timer)
      if (options.signal && abortListener) {
        options.signal.removeEventListener('abort', abortListener)
      }
      if (error) {
        reject(error)
        return
      }
      resolve()
    }
    timer = setTimeout(() => finish(), normalizedDelayMs)
    if (options.signal) {
      abortListener = () => finish(options.rejectOnAbort === true ? new Error('重试等待已取消') : undefined)
      options.signal.addEventListener('abort', abortListener, { once: true })
      if (options.signal.aborted) {
        abortListener()
      }
    }
  })
}

interface RetryDelayWaitOptions {
  signal?: AbortSignal
  rejectOnAbort?: boolean
}

function normalizeOptionalRetryCount(value: number | undefined): number | undefined {
  return value === undefined ? undefined : normalizeRetryCount(value)
}

function normalizeDelayMs(value: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}
