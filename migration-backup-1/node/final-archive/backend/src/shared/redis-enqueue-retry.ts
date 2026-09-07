export interface RedisEnqueueRetryOptions {
  delaysMs?: readonly number[]
}

const defaultRedisEnqueueRetryDelaysMs = [25, 100] as const

export async function runRedisEnqueueWithBoundedRetry(
  operation: () => Promise<unknown>,
  options: RedisEnqueueRetryOptions = {}
): Promise<void> {
  const delaysMs = options.delaysMs ?? defaultRedisEnqueueRetryDelaysMs
  let retryIndex = 0
  while (true) {
    try {
      await operation()
      return
    } catch (error) {
      if (isQueueQuiescedError(error) || retryIndex >= delaysMs.length) {
        throw error
      }
      await delay(normalizeDelayMs(delaysMs[retryIndex]))
      retryIndex += 1
    }
  }
}

function isQueueQuiescedError(error: unknown): boolean {
  return error instanceof Error && /(?:^|\b)QUEUE_QUIESCED(?:\b|$)/.test(error.message)
}

function normalizeDelayMs(value: number | undefined): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function delay(ms: number): Promise<void> {
  if (ms <= 0) return Promise.resolve()
  return new Promise((resolve) => setTimeout(resolve, ms))
}
