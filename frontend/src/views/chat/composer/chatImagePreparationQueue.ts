export interface ChatImagePreparationQueueSnapshot {
  activeCount: number
  queuedCount: number
  pendingCount: number
}

export class ChatImagePreparationQueue<T> {
  private readonly queued: T[] = []
  private readonly active = new Set<T>()
  private readonly canceled = new Set<T>()
  private readonly drainWaiters = new Set<() => void>()
  private readonly maxConcurrency: number

  constructor(private readonly options: {
    run: (item: T) => Promise<void>
    maxConcurrency?: number
    onChange?: (snapshot: ChatImagePreparationQueueSnapshot) => void
  }) {
    this.maxConcurrency = Math.max(1, Math.min(4, Math.floor(options.maxConcurrency ?? 2)))
  }

  enqueue(item: T): void {
    if (this.active.has(item) || this.queued.includes(item)) return
    this.canceled.delete(item)
    this.queued.push(item)
    this.changed()
    this.pump()
  }

  cancel(item: T): void {
    const index = this.queued.indexOf(item)
    if (index >= 0) {
      this.queued.splice(index, 1)
      this.canceled.delete(item)
    } else if (this.active.has(item)) {
      this.canceled.add(item)
    }
    this.changed()
  }

  clear(): void {
    for (const item of this.active) this.canceled.add(item)
    this.queued.length = 0
    this.changed()
  }

  snapshot(): ChatImagePreparationQueueSnapshot {
    const activeCount = [...this.active].reduce((count, item) => count + Number(!this.canceled.has(item)), 0)
    return {
      activeCount,
      queuedCount: this.queued.length,
      pendingCount: activeCount + this.queued.length
    }
  }

  drain(): Promise<void> {
    if (!this.snapshot().pendingCount) return Promise.resolve()
    return new Promise((resolve) => { this.drainWaiters.add(resolve) })
  }

  private pump(): void {
    while (this.active.size < this.maxConcurrency && this.queued.length) {
      const item = this.queued.shift()!
      if (this.canceled.has(item)) continue
      this.active.add(item)
      this.changed()
      void Promise.resolve()
        .then(() => this.options.run(item))
        .catch(() => undefined)
        .finally(() => {
          this.active.delete(item)
          this.canceled.delete(item)
          this.changed()
          this.pump()
        })
    }
    this.changed()
  }

  private changed(): void {
    const snapshot = this.snapshot()
    this.options.onChange?.(snapshot)
    if (snapshot.pendingCount) return
    for (const resolve of this.drainWaiters) resolve()
    this.drainWaiters.clear()
  }
}
