export class ChatLongSessionRunBudget {
  constructor(readonly deadline: number, readonly signal: AbortSignal) {}

  assertActive(stage: string): void {
    if (this.signal.aborted) throw abortReason(this.signal, stage)
    if (Date.now() >= this.deadline) throw new Error(`chat_long_session_total_deadline_exceeded:${stage}`)
  }

  remainingMs(stage: string): number {
    this.assertActive(stage)
    return Math.max(1, this.deadline - Date.now())
  }

  signalFor(maxDurationMs: number, stage: string): AbortSignal {
    this.assertActive(stage)
    const remaining = Math.max(1, Math.min(maxDurationMs, this.remainingMs(stage)))
    return AbortSignal.any([this.signal, AbortSignal.timeout(remaining)])
  }

  sleep(durationMs: number): Promise<void> {
    this.assertActive('sleep')
    const duration = Math.max(0, Math.min(durationMs, this.deadline - Date.now()))
    return new Promise<void>((resolveSleep, rejectSleep) => {
      const timeout = setTimeout(() => { cleanup(); resolveSleep() }, duration)
      const onAbort = (): void => { cleanup(); rejectSleep(abortReason(this.signal, 'sleep')) }
      const cleanup = (): void => { clearTimeout(timeout); this.signal.removeEventListener('abort', onAbort) }
      this.signal.addEventListener('abort', onAbort, { once: true })
      if (this.signal.aborted) onAbort()
    })
  }
}

function abortReason(signal: AbortSignal, stage: string): Error {
  return signal.reason instanceof Error ? signal.reason : new Error(`chat_long_session_interrupted:${stage}`)
}
