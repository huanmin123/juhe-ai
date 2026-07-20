export type RuntimeLogRedisProducerDropReason = 'saturated' | 'disconnected' | 'timeout' | 'command_failed'

export interface RuntimeLogRedisProducerDropEvent<T> {
  payload: T
  bytes: number
  reason: RuntimeLogRedisProducerDropReason
  error?: unknown
}

export interface RuntimeLogRedisProducerSnapshot {
  inFlightCount: number
  inFlightBytes: number
  maxInFlightCount: number
  maxInFlightBytes: number
  acceptedCount: number
  successCount: number
  droppedCount: number
  saturatedDropCount: number
  disconnectedDropCount: number
  timeoutDropCount: number
  commandFailureDropCount: number
}

interface BoundedRuntimeLogRedisProducerOptions<T> {
  maxInFlightCount: number
  maxInFlightBytes: number
  readinessTimeoutMs?: number
  commandTimeoutMs: number
  isReady: () => boolean | Promise<boolean>
  send: (payload: T) => Promise<unknown>
  classifyError?: (error: unknown) => RuntimeLogRedisProducerDropReason
  onDrop?: (event: RuntimeLogRedisProducerDropEvent<T>) => void
  onSuccess?: (payload: T) => void
  onTimeout?: () => void
}

class RuntimeLogRedisCommandTimeoutError extends Error {
  constructor(timeoutMs: number) {
    super(`runtime log Redis XADD timed out after ${timeoutMs}ms`)
    this.name = 'RuntimeLogRedisCommandTimeoutError'
  }
}

class RuntimeLogRedisReadinessTimeoutError extends Error {
  constructor(timeoutMs: number) {
    super(`runtime log Redis readiness timed out after ${timeoutMs}ms`)
    this.name = 'RuntimeLogRedisReadinessTimeoutError'
  }
}

export class BoundedRuntimeLogRedisProducer<T> {
  private readonly maxInFlightCount: number
  private readonly maxInFlightBytes: number
  private readonly readinessTimeoutMs: number
  private readonly commandTimeoutMs: number
  private readonly options: BoundedRuntimeLogRedisProducerOptions<T>
  private generation = 0
  private inFlightCount = 0
  private inFlightBytes = 0
  private acceptedCount = 0
  private successCount = 0
  private droppedCount = 0
  private saturatedDropCount = 0
  private disconnectedDropCount = 0
  private timeoutDropCount = 0
  private commandFailureDropCount = 0

  constructor(options: BoundedRuntimeLogRedisProducerOptions<T>) {
    this.options = options
    this.maxInFlightCount = positiveInteger(options.maxInFlightCount)
    this.maxInFlightBytes = positiveInteger(options.maxInFlightBytes)
    this.readinessTimeoutMs = positiveInteger(options.readinessTimeoutMs ?? options.commandTimeoutMs)
    this.commandTimeoutMs = positiveInteger(options.commandTimeoutMs)
  }

  enqueue(payload: T, estimatedBytes: number): boolean {
    const bytes = positiveInteger(estimatedBytes)
    if (
      bytes > this.maxInFlightBytes
      || this.inFlightCount >= this.maxInFlightCount
      || this.inFlightBytes + bytes > this.maxInFlightBytes
    ) {
      this.recordDrop({ payload, bytes, reason: 'saturated' })
      return false
    }

    const generation = this.generation
    this.inFlightCount += 1
    this.inFlightBytes += bytes
    this.acceptedCount += 1
    void this.run(payload, bytes, generation)
    return true
  }

  snapshot(): RuntimeLogRedisProducerSnapshot {
    return {
      inFlightCount: this.inFlightCount,
      inFlightBytes: this.inFlightBytes,
      maxInFlightCount: this.maxInFlightCount,
      maxInFlightBytes: this.maxInFlightBytes,
      acceptedCount: this.acceptedCount,
      successCount: this.successCount,
      droppedCount: this.droppedCount,
      saturatedDropCount: this.saturatedDropCount,
      disconnectedDropCount: this.disconnectedDropCount,
      timeoutDropCount: this.timeoutDropCount,
      commandFailureDropCount: this.commandFailureDropCount
    }
  }

  clearForTest(): void {
    this.generation += 1
    this.inFlightCount = 0
    this.inFlightBytes = 0
    this.acceptedCount = 0
    this.successCount = 0
    this.droppedCount = 0
    this.saturatedDropCount = 0
    this.disconnectedDropCount = 0
    this.timeoutDropCount = 0
    this.commandFailureDropCount = 0
  }

  private async run(payload: T, bytes: number, generation: number): Promise<void> {
    try {
      const ready = await withDeadline(
        Promise.resolve(this.options.isReady()),
        this.readinessTimeoutMs,
        () => new RuntimeLogRedisReadinessTimeoutError(this.readinessTimeoutMs)
      )
      if (!ready) {
        if (generation === this.generation) {
          this.recordDrop({ payload, bytes, reason: 'disconnected' })
        }
        return
      }
      await withDeadline(
        this.options.send(payload),
        this.commandTimeoutMs,
        () => new RuntimeLogRedisCommandTimeoutError(this.commandTimeoutMs)
      )
      if (generation === this.generation) {
        this.successCount += 1
        this.options.onSuccess?.(payload)
      }
    } catch (error) {
      if (generation !== this.generation) return
      const reason = error instanceof RuntimeLogRedisReadinessTimeoutError
        ? 'disconnected'
        : error instanceof RuntimeLogRedisCommandTimeoutError
          ? 'timeout'
          : this.options.classifyError?.(error) ?? 'command_failed'
      if (error instanceof RuntimeLogRedisCommandTimeoutError) {
        this.options.onTimeout?.()
      }
      this.recordDrop({ payload, bytes, reason, error })
    } finally {
      if (generation === this.generation) {
        this.inFlightCount = Math.max(0, this.inFlightCount - 1)
        this.inFlightBytes = Math.max(0, this.inFlightBytes - bytes)
      }
    }
  }

  private recordDrop(event: RuntimeLogRedisProducerDropEvent<T>): void {
    this.droppedCount += 1
    if (event.reason === 'saturated') this.saturatedDropCount += 1
    if (event.reason === 'disconnected') this.disconnectedDropCount += 1
    if (event.reason === 'timeout') this.timeoutDropCount += 1
    if (event.reason === 'command_failed') this.commandFailureDropCount += 1
    this.options.onDrop?.(event)
  }
}

async function withDeadline<T>(promise: Promise<T>, timeoutMs: number, createTimeoutError = () => new RuntimeLogRedisCommandTimeoutError(timeoutMs)): Promise<T> {
  let timeout: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      promise,
      new Promise<T>((_resolve, reject) => {
        timeout = setTimeout(() => reject(createTimeoutError()), timeoutMs)
      })
    ])
  } finally {
    if (timeout) clearTimeout(timeout)
    promise.catch(() => undefined)
  }
}

function positiveInteger(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}
