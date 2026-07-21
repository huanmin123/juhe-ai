import { setImmediate } from 'node:timers'

export type LogPriority = 'normal' | 'failure'

export interface AsyncLogDestination {
  write(chunk: Buffer, callback: (error?: Error | null) => void): void
  writeBatch?(chunks: Buffer[], callback: (error?: Error | null) => void): void
}

export interface AsyncLogPublisherOptions {
  maxNormalEvents: number
  maxFailureEvents: number
  maxBytes: number
  maxFailureBytes?: number
  destinations: AsyncLogDestination[]
}

interface QueueItem {
  chunk: Buffer
  priority: LogPriority
}

export interface AsyncLogPublisherStats {
  pendingEvents: number
  pendingBytes: number
  normalDropped: number
  failureDropped: number
  destinationErrors: number
}

export class AsyncLogPublisher {
  private readonly normalQueue: QueueItem[] = []
  private readonly failureQueue: QueueItem[] = []
  private pendingBytes = 0
  private pendingFailureBytes = 0
  private pendingNormalEvents = 0
  private pendingFailureEvents = 0
  private flushing = false
  private closed = false
  private scheduled = false
  private normalDropped = 0
  private failureDropped = 0
  private destinationErrors = 0
  private readonly flushWaiters: Array<() => void> = []

  constructor(private readonly options: AsyncLogPublisherOptions) {}

  enqueue(value: Buffer | string, priority: LogPriority = 'normal'): boolean {
    if (this.closed) return false
    const chunk = Buffer.isBuffer(value) ? value : Buffer.from(value)
    const queue = priority === 'failure' ? this.failureQueue : this.normalQueue
    const limit = priority === 'failure' ? this.options.maxFailureEvents : this.options.maxNormalEvents
    const pendingLaneEvents = priority === 'failure' ? this.pendingFailureEvents : this.pendingNormalEvents
    const pendingLaneBytes = priority === 'failure' ? this.pendingFailureBytes : this.pendingBytes - this.pendingFailureBytes
    const laneByteLimit = priority === 'failure' ? (this.options.maxFailureBytes ?? this.options.maxBytes) : this.options.maxBytes
    if (pendingLaneEvents >= limit || pendingLaneBytes + chunk.byteLength > laneByteLimit) {
      if (priority === 'failure') this.failureDropped += 1
      else this.normalDropped += 1
      return false
    }
    queue.push({ chunk, priority })
    this.pendingBytes += chunk.byteLength
    if (priority === 'failure') {
      this.pendingFailureBytes += chunk.byteLength
      this.pendingFailureEvents += 1
    } else {
      this.pendingNormalEvents += 1
    }
    this.scheduleFlush()
    return true
  }

  stats(): AsyncLogPublisherStats {
    return {
      pendingEvents: this.pendingNormalEvents + this.pendingFailureEvents,
      pendingBytes: this.pendingBytes,
      normalDropped: this.normalDropped,
      failureDropped: this.failureDropped,
      destinationErrors: this.destinationErrors
    }
  }

  async flush(): Promise<void> {
    if (!this.flushing && this.normalQueue.length === 0 && this.failureQueue.length === 0) return
    const completed = new Promise<void>((resolve) => this.flushWaiters.push(resolve))
    if (!this.flushing) void this.drain()
    await completed
  }

  async close(): Promise<void> {
    await this.closeWithin(3_000)
  }

  async closeWithin(timeoutMs: number): Promise<boolean> {
    if (this.closed) return true
    this.closed = true
    if (this.normalQueue.length === 0 && this.failureQueue.length === 0 && !this.flushing) return true
    const flushPromise = this.flush().then(() => true)
    return await new Promise<boolean>((resolve) => {
      let settled = false
      const settle = (value: boolean) => {
        if (settled) return
        settled = true
        clearTimeout(timeout)
        resolve(value)
      }
      const timeout = setTimeout(() => {
        this.discardQueuedItems()
        settle(false)
      }, Math.max(0, timeoutMs))
      void flushPromise.then(settle)
    })
  }

  private scheduleFlush(): void {
    if (this.scheduled || this.flushing) return
    this.scheduled = true
    setImmediate(() => {
      this.scheduled = false
      void this.drain()
    })
  }

  private async drain(): Promise<void> {
    if (this.flushing) return
    this.flushing = true
    try {
      while (this.failureQueue.length > 0 || this.normalQueue.length > 0) {
        const items = this.takeBatch(256)
        try {
          await this.writeBatchToDestinations(items.map((item) => item.chunk))
        } finally {
          this.releaseBatch(items)
        }
      }
    } finally {
      this.flushing = false
      if (!this.closed && (this.failureQueue.length > 0 || this.normalQueue.length > 0)) this.scheduleFlush()
      if (this.normalQueue.length === 0 && this.failureQueue.length === 0) {
        for (const resolve of this.flushWaiters.splice(0)) resolve()
      }
    }
  }

  private takeBatch(maxItems: number): QueueItem[] {
    const items: QueueItem[] = []
    while (items.length < maxItems && (this.failureQueue.length > 0 || this.normalQueue.length > 0)) {
      const item = this.failureQueue.shift() ?? this.normalQueue.shift()!
      items.push(item)
    }
    return items
  }

  private releaseBatch(items: QueueItem[]): void {
    for (const item of items) {
      this.pendingBytes = Math.max(0, this.pendingBytes - item.chunk.byteLength)
      if (item.priority === 'failure') {
        this.pendingFailureBytes = Math.max(0, this.pendingFailureBytes - item.chunk.byteLength)
        this.pendingFailureEvents = Math.max(0, this.pendingFailureEvents - 1)
      } else {
        this.pendingNormalEvents = Math.max(0, this.pendingNormalEvents - 1)
      }
    }
  }

  private discardQueuedItems(): void {
    const items = [...this.failureQueue.splice(0), ...this.normalQueue.splice(0)]
    this.releaseBatch(items)
  }

  private async writeBatchToDestinations(chunks: Buffer[]): Promise<void> {
    for (const destination of this.options.destinations) {
      if (destination.writeBatch) {
        await new Promise<void>((resolve) => {
          try {
            destination.writeBatch!(chunks, (error) => {
              if (error) this.destinationErrors += 1
              resolve()
            })
          } catch {
            this.destinationErrors += 1
            resolve()
          }
        })
        continue
      }
      for (const chunk of chunks) await this.writeToDestination(destination, chunk)
    }
  }

  private async writeToDestination(destination: AsyncLogDestination, chunk: Buffer): Promise<void> {
    await new Promise<void>((resolve) => {
      try {
        destination.write(chunk, (error) => {
          if (error) this.destinationErrors += 1
          resolve()
        })
      } catch {
        this.destinationErrors += 1
        resolve()
      }
    })
  }
}
