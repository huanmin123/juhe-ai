import type { ChatGenerationSubscriber } from './chat-generation-runner.js'

export interface ChatSseResponse {
  destroyed: boolean
  writableEnded: boolean
  write(chunk: string): boolean
  end(): void
  once?(event: 'close' | 'drain', listener: () => void): unknown
  off?(event: 'close' | 'drain', listener: () => void): unknown
}

export interface ChatSseHeartbeatScheduler {
  setInterval(callback: () => void, intervalMs: number): unknown
  clearInterval(timer: unknown): void
  setTimeout(callback: () => void, timeoutMs: number): unknown
  clearTimeout(timer: unknown): void
}

const defaultHeartbeatScheduler: ChatSseHeartbeatScheduler = {
  setInterval: (callback, intervalMs) => setInterval(callback, intervalMs),
  clearInterval: (timer) => clearInterval(timer as ReturnType<typeof setInterval>),
  setTimeout: (callback, timeoutMs) => setTimeout(callback, timeoutMs),
  clearTimeout: (timer) => clearTimeout(timer as ReturnType<typeof setTimeout>)
}

export function startChatSseHeartbeat(input: {
  response: ChatSseResponse
  intervalMs?: number
  drainTimeoutMs?: number
  onUnwritable(): void
  scheduler?: ChatSseHeartbeatScheduler
}): () => void {
  const scheduler = input.scheduler ?? defaultHeartbeatScheduler
  const intervalMs = input.intervalMs ?? 5_000
  const drainTimeoutMs = input.drainTimeoutMs ?? intervalMs * 2
  let stopped = false
  let backpressured = false
  let heartbeatTimer: unknown
  let drainTimer: unknown

  const clearHeartbeat = (): void => {
    if (heartbeatTimer === undefined) return
    scheduler.clearInterval(heartbeatTimer)
    heartbeatTimer = undefined
  }
  const clearDrainTimeout = (): void => {
    if (drainTimer === undefined) return
    scheduler.clearTimeout(drainTimer)
    drainTimer = undefined
  }

  const stop = (): void => {
    if (stopped) return
    stopped = true
    backpressured = false
    clearHeartbeat()
    clearDrainTimeout()
    try { input.response.off?.('close', onClose) } catch {}
    try { input.response.off?.('drain', onDrain) } catch {}
  }
  const markUnwritable = (): void => {
    if (stopped) return
    stop()
    try { input.onUnwritable() } catch {}
  }
  const onClose = (): void => { markUnwritable() }
  const startHeartbeat = (): void => {
    if (stopped || backpressured || heartbeatTimer !== undefined) return
    heartbeatTimer = scheduler.setInterval(writeHeartbeat, intervalMs)
    unrefTimer(heartbeatTimer)
  }
  const onDrain = (): void => {
    if (stopped || !backpressured) return
    backpressured = false
    clearDrainTimeout()
    try { input.response.off?.('drain', onDrain) } catch {}
    if (input.response.destroyed || input.response.writableEnded) {
      markUnwritable()
      return
    }
    startHeartbeat()
  }
  const waitForDrain = (): void => {
    if (stopped || backpressured) return
    backpressured = true
    clearHeartbeat()
    try {
      input.response.once?.('drain', onDrain)
    } catch {
      markUnwritable()
      return
    }
    drainTimer = scheduler.setTimeout(markUnwritable, drainTimeoutMs)
    unrefTimer(drainTimer)
  }
  const writeHeartbeat = (): void => {
    if (stopped) return
    if (input.response.destroyed || input.response.writableEnded) {
      markUnwritable()
      return
    }
    try {
      if (!input.response.write(': heartbeat\n\n')) waitForDrain()
    } catch {
      markUnwritable()
    }
  }

  startHeartbeat()
  try { input.response.once?.('close', onClose) } catch { markUnwritable() }
  return stop
}

function unrefTimer(timer: unknown): void {
  const unref = (timer as { unref?: () => void } | undefined)?.unref
  if (typeof unref === 'function') unref.call(timer)
}

export function writeChatSseEvent(response: ChatSseResponse, event: string, data: unknown): boolean {
  if (response.destroyed || response.writableEnded) return false
  try {
    return response.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
  } catch {
    return false
  }
}

export function createChatSseSubscriber(input: {
  response: ChatSseResponse
  detach(): void
}): ChatGenerationSubscriber {
  let detached = false
  const detach = (): void => {
    if (detached) return
    detached = true
    try { input.detach() } catch {}
    try { if (!input.response.writableEnded) input.response.end() } catch {}
  }
  return {
    trySend(event) {
      if (detached || input.response.destroyed || input.response.writableEnded) {
        detach()
        return false
      }
      try {
        const writable = writeChatSseEvent(input.response, event.type, { ...event.data, eventVersion: event.eventVersion })
        if (!writable) {
          detach()
          return false
        }
        if (event.type === 'message.completed' || event.type === 'message.failed' || event.type === 'message.canceled') {
          queueMicrotask(detach)
        }
        return true
      } catch {
        detach()
        return false
      }
    }
  }
}
