import type { RunningTurn } from './chatGenerationRuntime'

const DEFAULT_RETRY_DELAYS_MS = [5_000, 10_000, 20_000, 30_000] as const
const MAX_TRACKED_TURNS = 128

interface ReconciliationAttempt {
  signature: string
  attempts: number
  nextAttemptAt: number
  inFlight: boolean
}

export class ChatRuntimeReconciliationScheduler {
  private readonly attempts = new Map<string, ReconciliationAttempt>()
  private readonly retryDelaysMs: readonly number[]
  private readonly now: () => number

  constructor(options: { retryDelaysMs?: readonly number[]; now?: () => number } = {}) {
    this.retryDelaysMs = options.retryDelaysMs?.length ? options.retryDelaysMs : DEFAULT_RETRY_DELAYS_MS
    this.now = options.now ?? Date.now
  }

  get size(): number { return this.attempts.size }

  begin(turn: RunningTurn): boolean {
    if (!turn.reconciliationReason || isTerminal(turn.status)) {
      this.clear(turn)
      return false
    }
    const key = turnKey(turn)
    const signature = turnSignature(turn)
    let state = this.attempts.get(key)
    if (!state || state.signature !== signature) {
      state = { signature, attempts: 0, nextAttemptAt: this.now(), inFlight: false }
      this.attempts.set(key, state)
      this.trim()
    }
    if (state.inFlight || this.now() < state.nextAttemptAt) return false
    state.inFlight = true
    return true
  }

  complete(attempted: RunningTurn, latest?: RunningTurn): void {
    const key = turnKey(attempted)
    const state = this.attempts.get(key)
    if (!state) return
    if (!latest || turnKey(latest) !== key || !latest.reconciliationReason || isTerminal(latest.status) || turnSignature(latest) !== state.signature) {
      this.attempts.delete(key)
      return
    }
    state.inFlight = false
    state.attempts += 1
    state.nextAttemptAt = this.now() + this.retryDelaysMs[Math.min(state.attempts - 1, this.retryDelaysMs.length - 1)]!
  }

  clear(turn: Pick<RunningTurn, 'systemAccountId' | 'conversationId' | 'turnId' | 'clientMessageId'>): void {
    this.attempts.delete(turnKey(turn))
  }

  clearConversation(systemAccountId: string, conversationId: string): void {
    const prefix = JSON.stringify([systemAccountId, conversationId]).slice(0, -1)
    for (const key of this.attempts.keys()) if (key.startsWith(prefix)) this.attempts.delete(key)
  }

  private trim(): void {
    while (this.attempts.size > MAX_TRACKED_TURNS) this.attempts.delete(this.attempts.keys().next().value!)
  }
}

function turnKey(turn: Pick<RunningTurn, 'systemAccountId' | 'conversationId' | 'turnId' | 'clientMessageId'>): string {
  return JSON.stringify([turn.systemAccountId, turn.conversationId, turn.turnId ?? turn.clientMessageId])
}

function turnSignature(turn: Pick<RunningTurn, 'reconciliationReason' | 'eventVersion' | 'status'>): string {
  return JSON.stringify([turn.reconciliationReason, turn.eventVersion, turn.status])
}

function isTerminal(status: RunningTurn['status']): boolean {
  return status === 'completed' || status === 'failed' || status === 'canceled'
}
