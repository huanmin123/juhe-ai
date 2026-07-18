export type GatewayAccountAvailability = 'dispatchable_now' | 'recoverable_later' | 'hard_exhausted'

export function shouldHandoffClient(input: {
  availability: GatewayAccountAvailability
  noAvailableElapsedMs: number
  waitBudgetMs: number
}): boolean {
  if (input.availability === 'dispatchable_now') return false
  if (input.availability === 'hard_exhausted') return true
  return normalizedMs(input.noAvailableElapsedMs) >= Math.max(1, normalizedMs(input.waitBudgetMs))
}

export class ServerRetryBudget {
  private accumulatedWaitMs = 0
  private waitingSinceMs: number | undefined

  constructor(readonly waitBudgetMs: number) {
    this.waitBudgetMs = Math.max(1, normalizedMs(waitBudgetMs))
  }

  beginNoAvailableWait(nowMs = Date.now()): void {
    if (this.waitingSinceMs !== undefined) return
    this.waitingSinceMs = normalizedTimestamp(nowMs)
  }

  pauseNoAvailableWait(nowMs = Date.now()): void {
    if (this.waitingSinceMs === undefined) return
    const now = normalizedTimestamp(nowMs)
    this.accumulatedWaitMs = Math.min(
      this.waitBudgetMs,
      this.accumulatedWaitMs + Math.max(0, now - this.waitingSinceMs)
    )
    this.waitingSinceMs = undefined
  }

  elapsedMs(nowMs = Date.now()): number {
    const currentWaitMs = this.waitingSinceMs === undefined
      ? 0
      : Math.max(0, normalizedTimestamp(nowMs) - this.waitingSinceMs)
    return Math.min(this.waitBudgetMs, this.accumulatedWaitMs + currentWaitMs)
  }

  remainingMs(nowMs = Date.now()): number {
    return Math.max(0, this.waitBudgetMs - this.elapsedMs(nowMs))
  }

  deadlineAtMs(nowMs = Date.now()): number {
    const now = normalizedTimestamp(nowMs)
    this.beginNoAvailableWait(now)
    return now + this.remainingMs(now)
  }

  handoffRequired(availability: GatewayAccountAvailability, nowMs = Date.now()): boolean {
    return shouldHandoffClient({
      availability,
      noAvailableElapsedMs: this.elapsedMs(nowMs),
      waitBudgetMs: this.waitBudgetMs
    })
  }
}

function normalizedMs(value: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function normalizedTimestamp(value: number): number {
  return Number.isFinite(value) ? Math.trunc(value) : Date.now()
}
