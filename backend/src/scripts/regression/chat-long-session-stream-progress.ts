export type ChatLongSessionStreamIdleReason = 'event_idle' | 'progress_idle'

export interface ChatLongSessionStreamProgressSnapshot {
  lastEventAt: string
  lastDeltaAt: string
  eventCount: number
  partialBytes: number
  eventIdleMs: number
  progressIdleMs: number
}

export class ChatLongSessionStreamProgress {
  private lastEventAtMs: number
  private lastDeltaAtMs: number
  private partialBytes = 0

  constructor(private readonly options: { startedAt: number; eventIdleMs: number; progressIdleMs: number }) {
    this.lastEventAtMs = options.startedAt
    this.lastDeltaAtMs = options.startedAt
  }

  observe(name: string, data: Record<string, unknown>, now: number, partialBytes: number): void {
    if (isHeartbeatEvent(name)) return
    this.lastEventAtMs = now
    if (!isProgressEvent(name, data)) return
    this.lastDeltaAtMs = now
    this.partialBytes = Math.max(this.partialBytes, partialBytes)
  }

  expiredReason(now: number): ChatLongSessionStreamIdleReason | undefined {
    if (now - this.lastEventAtMs >= this.options.eventIdleMs) return 'event_idle'
    if (now - this.lastDeltaAtMs >= this.options.progressIdleMs) return 'progress_idle'
    return undefined
  }

  nextDeadlineAt(): number {
    return Math.min(
      this.lastEventAtMs + this.options.eventIdleMs,
      this.lastDeltaAtMs + this.options.progressIdleMs
    )
  }

  snapshot(now: number, eventCount: number): ChatLongSessionStreamProgressSnapshot {
    return {
      lastEventAt: new Date(this.lastEventAtMs).toISOString(),
      lastDeltaAt: new Date(this.lastDeltaAtMs).toISOString(),
      eventCount,
      partialBytes: this.partialBytes,
      eventIdleMs: Math.max(0, now - this.lastEventAtMs),
      progressIdleMs: Math.max(0, now - this.lastDeltaAtMs)
    }
  }
}

function isHeartbeatEvent(name: string): boolean {
  return /^(?:heartbeat|ping|message\.heartbeat)$/i.test(name)
}

function isProgressEvent(name: string, data: Record<string, unknown>): boolean {
  if ((name === 'message.delta' || name === 'reasoning.delta') && typeof data.delta === 'string' && data.delta.length > 0) return true
  return /^(?:tool|response\.output_item)\.(?:started|updated|completed|added|done)$/i.test(name)
}
