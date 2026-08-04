import { runtimeConfig } from '../../config/runtime.js'
import { runWithGlobalBackgroundConcurrencySlot } from '../../shared/concurrency-governor.js'
import type { AccountTestResult } from '../../domain/types.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'

export const globalSharedQueueConcurrency = runtimeConfig.concurrency.globalMax
// 60 seconds are needed by the 10s -> 20s -> 30s ladder itself. Keep a small
// scheduling margin so the outer deadline cannot cancel the third tier first.
export const accountHealthCheckProbeDeadlineMs = runtimeConfig.background.accountHealthCheckProbeDeadlineMs
export const backgroundProbeDbServiceTimeoutMs = runtimeConfig.background.accountProbeDbServiceTimeoutMs
export const cooldownAccountRetestStartupDelayMs = runtimeConfig.background.cooldownAccountRetestStartupDelayMs
export const accountApiKeyCooldownRetestStartupDelayMs = runtimeConfig.background.accountApiKeyCooldownRetestStartupDelayMs
export const normalRouteSpeedFirstProbeStartupDelayMs = runtimeConfig.background.normalRouteSpeedFirstProbeStartupDelayMs

const backgroundAccountAvailabilityProbesInFlight = new Map<string, {
  promise: Promise<BackgroundAccountAvailabilityProbeObservation>
  consumers: number
  settled: boolean
}>()

export interface BackgroundAccountAvailabilityProbeObservation {
  result: AccountTestResult
  upstreamAttempt?: UpstreamAttempt
  diagnosticCanceled?: boolean
  diagnosticTimeoutExhausted?: boolean
  diagnosticDeadlineExceeded?: boolean
}

export async function runWithBackgroundFullDiagnosticSlot<T>(task: () => Promise<T>): Promise<T> {
  return await runWithGlobalBackgroundConcurrencySlot(task)
}

export async function runWithAccountHealthCheckDiagnosticSlot<T>(task: () => Promise<T>): Promise<T> {
  return await runWithGlobalBackgroundConcurrencySlot(task)
}

export async function runWithCooldownAccountRetestDiagnosticSlot<T>(task: () => Promise<T>): Promise<T> {
  return await runWithGlobalBackgroundConcurrencySlot(task)
}

export async function runWithBackgroundAccountAvailabilityProbe<T>(
  runtimeKey: string,
  task: () => Promise<BackgroundAccountAvailabilityProbeObservation>,
  consume: (
    observation: BackgroundAccountAvailabilityProbeObservation,
    context: { joined: boolean }
  ) => Promise<T>,
  options: {
    signal?: AbortSignal
    abortedObservation?: () => BackgroundAccountAvailabilityProbeObservation
  } = {}
): Promise<T> {
  const normalizedKey = runtimeKey.trim()
  if (!normalizedKey) {
    return await consume(await task(), { joined: false })
  }
  let entry = backgroundAccountAvailabilityProbesInFlight.get(normalizedKey)
  const joined = Boolean(entry)
  if (!entry) {
    const createdEntry = {
      promise: Promise.resolve().then(task),
      consumers: 0,
      settled: false
    }
    entry = createdEntry
    backgroundAccountAvailabilityProbesInFlight.set(normalizedKey, createdEntry)
    void createdEntry.promise.then(
      () => settleBackgroundAccountAvailabilityProbe(normalizedKey, createdEntry),
      () => settleBackgroundAccountAvailabilityProbe(normalizedKey, createdEntry)
    )
  }
  entry.consumers += 1
  try {
    return await consume(await backgroundAccountAvailabilityProbeObservationWithSignal(entry.promise, options), { joined })
  } finally {
    entry.consumers -= 1
    if (entry.settled && entry.consumers === 0 && backgroundAccountAvailabilityProbesInFlight.get(normalizedKey) === entry) {
      backgroundAccountAvailabilityProbesInFlight.delete(normalizedKey)
    }
  }
}

function settleBackgroundAccountAvailabilityProbe(
  runtimeKey: string,
  entry: { consumers: number; settled: boolean }
): void {
  entry.settled = true
  if (entry.consumers === 0 && backgroundAccountAvailabilityProbesInFlight.get(runtimeKey) === entry) {
    backgroundAccountAvailabilityProbesInFlight.delete(runtimeKey)
  }
}

async function backgroundAccountAvailabilityProbeObservationWithSignal(
  promise: Promise<BackgroundAccountAvailabilityProbeObservation>,
  options: {
    signal?: AbortSignal
    abortedObservation?: () => BackgroundAccountAvailabilityProbeObservation
  }
): Promise<BackgroundAccountAvailabilityProbeObservation> {
  const signal = options.signal
  const abortedObservation = options.abortedObservation
  if (!signal || !abortedObservation) return await promise
  if (signal.aborted) return abortedObservation()
  return await new Promise<BackgroundAccountAvailabilityProbeObservation>((resolve, reject) => {
    const onAbort = () => resolve(abortedObservation())
    signal.addEventListener('abort', onAbort, { once: true })
    void promise.then(
      (result) => {
        signal.removeEventListener('abort', onAbort)
        resolve(result)
      },
      (error: unknown) => {
        signal.removeEventListener('abort', onAbort)
        reject(error)
      }
    )
  })
}
