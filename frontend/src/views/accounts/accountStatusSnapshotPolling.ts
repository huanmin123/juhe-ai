export interface AccountStatusSnapshotPolling {
  start(): void
  stop(): void
  refreshNow(): void
}

export interface AccountStatusSnapshotIdentity {
  sequence: number
  revision: number
  idSignature: string
  scopeSignature: string
}

export function isAccountStatusSnapshotCurrent(
  expected: AccountStatusSnapshotIdentity,
  current: AccountStatusSnapshotIdentity
): boolean {
  return expected.sequence === current.sequence
    && expected.revision === current.revision
    && expected.idSignature === current.idSignature
    && expected.scopeSignature === current.scopeSignature
}

export function accountStatusSnapshotPollingDelayMs(random: () => number = Math.random): number {
  return 30_000 + Math.round((random() * 2 - 1) * 1_000)
}

interface AccountStatusSnapshotPollingOptions {
  accountIds: () => string[]
  isBlocked: () => boolean
  isVisible: () => boolean
  request: (accountIds: string[], signal: AbortSignal) => Promise<void>
  random?: () => number
  setTimer?: (callback: () => void, delay: number) => unknown
  clearTimer?: (timer: unknown) => void
}

export function createAccountStatusSnapshotPolling(options: AccountStatusSnapshotPollingOptions): AccountStatusSnapshotPolling {
  const random = options.random ?? Math.random
  const setTimer = options.setTimer ?? ((callback, delay) => globalThis.setTimeout(callback, delay))
  const clearTimer = options.clearTimer ?? ((timer) => globalThis.clearTimeout(timer as ReturnType<typeof setTimeout>))
  let started = false
  let inFlight = false
  let timer: unknown
  let abortController: AbortController | undefined

  const schedule = (): void => {
    if (!started || options.isBlocked() || !options.isVisible()) return
    if (timer !== undefined) clearTimer(timer)
    const delay = accountStatusSnapshotPollingDelayMs(random)
    timer = setTimer(() => {
      timer = undefined
      void refresh()
    }, delay)
  }

  const refresh = async (): Promise<void> => {
    if (!started || inFlight || options.isBlocked() || !options.isVisible()) {
      return
    }
    const ids = [...new Set(options.accountIds().filter(Boolean))]
    if (ids.length === 0) {
      schedule()
      return
    }
    inFlight = true
    abortController = new AbortController()
    try {
      for (let offset = 0; offset < ids.length; offset += 100) {
        if (abortController.signal.aborted) break
        await options.request(ids.slice(offset, offset + 100), abortController.signal)
      }
    } catch (error) {
      if (!(error instanceof DOMException && error.name === 'AbortError')) {
        // Periodic refresh failures retain the last accepted snapshot.
      }
    } finally {
      inFlight = false
      abortController = undefined
      schedule()
    }
  }

  return {
    start() {
      if (started) return
      started = true
      void refresh()
    },
    stop() {
      started = false
      if (timer !== undefined) clearTimer(timer)
      timer = undefined
      abortController?.abort()
      abortController = undefined
    },
    refreshNow() {
      if (!started) return
      if (timer !== undefined) clearTimer(timer)
      timer = undefined
      if (options.isBlocked() || !options.isVisible()) {
        abortController?.abort()
        return
      }
      void refresh()
    }
  }
}
