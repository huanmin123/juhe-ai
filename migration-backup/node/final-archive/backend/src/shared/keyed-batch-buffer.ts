export interface KeyedBatchBufferRuntime {
  keyCount: number
  itemCount: number
  flushedBatches: number
  flushedItems: number
  failedBatches: number
}

export interface KeyedBatchBufferOptions<T> {
  name: string
  maxItems: number
  delayMs: number
  flush: (key: string, items: T[]) => Promise<void> | void
}

interface KeyedBatchItem<T> {
  item: T
  resolve: () => void
  reject: (error: Error) => void
}

interface KeyedBatchBucket<T> {
  items: Array<KeyedBatchItem<T>>
  timer?: NodeJS.Timeout
  flushing: boolean
}

export class KeyedBatchBuffer<T> {
  private readonly buckets = new Map<string, KeyedBatchBucket<T>>()
  private flushedBatches = 0
  private flushedItems = 0
  private failedBatches = 0

  constructor(private readonly options: KeyedBatchBufferOptions<T>) {}

  enqueue(key: string, item: T): Promise<void> {
    const normalizedKey = key.trim()
    if (!normalizedKey) {
      return Promise.reject(new Error(`${this.options.name} batch key 不能为空`))
    }
    const bucket = this.bucketForKey(normalizedKey)
    const promise = new Promise<void>((resolve, reject) => {
      bucket.items.push({ item, resolve, reject })
    })
    if (bucket.items.length >= Math.max(1, Math.trunc(this.options.maxItems))) {
      this.scheduleFlush(normalizedKey, bucket, 0)
    } else {
      this.scheduleFlush(normalizedKey, bucket, Math.max(0, Math.trunc(this.options.delayMs)))
    }
    return promise
  }

  async flushAll(): Promise<void> {
    const entries = [...this.buckets.entries()]
    await Promise.all(entries.map(async ([key, bucket]) => {
      await this.flushBucket(key, bucket)
    }))
  }

  resetMetrics(): void {
    this.flushedBatches = 0
    this.flushedItems = 0
    this.failedBatches = 0
  }

  runtime(): KeyedBatchBufferRuntime {
    let itemCount = 0
    for (const bucket of this.buckets.values()) {
      itemCount += bucket.items.length
    }
    return {
      keyCount: this.buckets.size,
      itemCount,
      flushedBatches: this.flushedBatches,
      flushedItems: this.flushedItems,
      failedBatches: this.failedBatches
    }
  }

  private bucketForKey(key: string): KeyedBatchBucket<T> {
    const existing = this.buckets.get(key)
    if (existing) {
      return existing
    }
    const bucket: KeyedBatchBucket<T> = {
      items: [],
      flushing: false
    }
    this.buckets.set(key, bucket)
    return bucket
  }

  private scheduleFlush(key: string, bucket: KeyedBatchBucket<T>, delayMs: number): void {
    if (bucket.flushing) {
      return
    }
    if (bucket.timer) {
      if (delayMs > 0) return
      clearTimeout(bucket.timer)
      bucket.timer = undefined
    }
    bucket.timer = setTimeout(() => {
      bucket.timer = undefined
      void this.flushBucket(key, bucket)
    }, delayMs)
  }

  private async flushBucket(key: string, bucket: KeyedBatchBucket<T>): Promise<void> {
    if (bucket.flushing || bucket.items.length === 0) {
      return
    }
    if (bucket.timer) {
      clearTimeout(bucket.timer)
      bucket.timer = undefined
    }
    bucket.flushing = true
    const batch = bucket.items.splice(0, Math.max(1, Math.trunc(this.options.maxItems)))
    try {
      await this.options.flush(key, batch.map((entry) => entry.item))
      this.flushedBatches += 1
      this.flushedItems += batch.length
      for (const entry of batch) {
        entry.resolve()
      }
    } catch (error) {
      this.failedBatches += 1
      const normalizedError = error instanceof Error ? error : new Error(String(error))
      for (const entry of batch) {
        entry.reject(normalizedError)
      }
    } finally {
      bucket.flushing = false
      if (bucket.items.length === 0) {
        this.buckets.delete(key)
      } else {
        this.scheduleFlush(key, bucket, 0)
      }
    }
  }
}
