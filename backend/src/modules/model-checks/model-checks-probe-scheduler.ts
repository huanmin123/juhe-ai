import { runtimeConfig } from '../../config/runtime.js'

type ReleaseProbeSlot = () => void

type ProbeWaiter = {
  resolve: (release: ReleaseProbeSlot) => void
  reject: (error: Error) => void
  signal?: AbortSignal
  abortListener?: () => void
}

const probeWaiters: ProbeWaiter[] = []
let activeProbeCount = 0
let lastProbeStartAt = 0
let drainTimer: ReturnType<typeof setTimeout> | undefined

export async function acquireModelCheckProbeSlot(signal?: AbortSignal): Promise<ReleaseProbeSlot> {
  if (signal?.aborted) {
    throw abortSignalError(signal)
  }
  return await new Promise<ReleaseProbeSlot>((resolve, reject) => {
    const waiter: ProbeWaiter = { resolve, reject, signal }
    if (signal) {
      waiter.abortListener = () => {
        removeWaiter(waiter)
        reject(abortSignalError(signal))
      }
      signal.addEventListener('abort', waiter.abortListener, { once: true })
    }
    probeWaiters.push(waiter)
    drainProbeWaiters()
  })
}

export function modelCheckProbeSchedulerRuntime(): { active: number; pending: number; maxInFlight: number; minStartIntervalMs: number } {
  return {
    active: activeProbeCount,
    pending: probeWaiters.length,
    maxInFlight: normalizedMaxInFlight(),
    minStartIntervalMs: normalizedMinStartIntervalMs()
  }
}

function drainProbeWaiters(): void {
  if (drainTimer) {
    clearTimeout(drainTimer)
    drainTimer = undefined
  }
  while (probeWaiters.length > 0 && activeProbeCount < normalizedMaxInFlight()) {
    const waitMs = nextStartWaitMs()
    if (waitMs > 0) {
      scheduleDrain(waitMs)
      return
    }
    const waiter = probeWaiters.shift()
    if (!waiter) return
    cleanupWaiter(waiter)
    if (waiter.signal?.aborted) {
      waiter.reject(abortSignalError(waiter.signal))
      continue
    }
    activeProbeCount += 1
    lastProbeStartAt = Date.now()
    let released = false
    waiter.resolve(() => {
      if (released) return
      released = true
      activeProbeCount = Math.max(0, activeProbeCount - 1)
      drainProbeWaiters()
    })
    if (normalizedMinStartIntervalMs() > 0) {
      if (probeWaiters.length > 0) {
        scheduleDrain(normalizedMinStartIntervalMs())
      }
      return
    }
  }
}

function scheduleDrain(delayMs: number): void {
  if (drainTimer) return
  drainTimer = setTimeout(() => {
    drainTimer = undefined
    drainProbeWaiters()
  }, Math.max(1, Math.trunc(delayMs)))
  drainTimer.unref()
}

function nextStartWaitMs(): number {
  const intervalMs = normalizedMinStartIntervalMs()
  if (intervalMs <= 0 || lastProbeStartAt <= 0) return 0
  return Math.max(0, lastProbeStartAt + intervalMs - Date.now())
}

function normalizedMaxInFlight(): number {
  return Math.max(1, Math.trunc(runtimeConfig.modelCheck.probeMaxInFlight))
}

function normalizedMinStartIntervalMs(): number {
  return Math.max(0, Math.trunc(runtimeConfig.modelCheck.probeMinStartIntervalMs))
}

function removeWaiter(waiter: ProbeWaiter): void {
  const index = probeWaiters.indexOf(waiter)
  if (index >= 0) {
    probeWaiters.splice(index, 1)
  }
  cleanupWaiter(waiter)
}

function cleanupWaiter(waiter: ProbeWaiter): void {
  if (waiter.signal && waiter.abortListener) {
    waiter.signal.removeEventListener('abort', waiter.abortListener)
    waiter.abortListener = undefined
  }
}

function abortSignalError(signal: AbortSignal): Error {
  return signal.reason instanceof Error ? signal.reason : new Error('模型检测已取消')
}
