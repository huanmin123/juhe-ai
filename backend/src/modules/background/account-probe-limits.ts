import pLimit from 'p-limit'

import type { AccountTestResult } from '../../domain/types.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'

export const backgroundFullDiagnosticConcurrency = 3
// The budget is shared by every API key in one health check, not reset per key.
export const accountHealthCheckProbeDeadlineMs = 30_000
// The queue admits lifecycle work in parallel. Routine checks receive a
// separate limiter before they acquire a shared full-diagnostic slot.
export const accountHealthCheckQueueConcurrency = backgroundFullDiagnosticConcurrency
export const routineAccountHealthCheckDiagnosticConcurrency = 1
export const backgroundProbeDbServiceTimeoutMs = 30_000
export const cooldownAccountRetestStartupDelayMs = 60_000
export const accountApiKeyCooldownRetestStartupDelayMs = 65_000
export const normalRouteSpeedFirstProbeStartupDelayMs = 75_000

const backgroundFullDiagnosticLimit = pLimit(backgroundFullDiagnosticConcurrency)
const routineAccountHealthCheckDiagnosticLimit = pLimit(routineAccountHealthCheckDiagnosticConcurrency)
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

export function backgroundFullDiagnosticQueueConcurrency(batchSize: number): number {
  const normalizedBatchSize = Number.isFinite(batchSize) ? Math.trunc(batchSize) : 1
  return Math.max(1, Math.min(normalizedBatchSize, backgroundFullDiagnosticConcurrency))
}

export async function runWithBackgroundFullDiagnosticSlot<T>(task: () => Promise<T>): Promise<T> {
  return await backgroundFullDiagnosticLimit(task)
}

export async function runWithRoutineAccountHealthCheckDiagnosticSlot<T>(task: () => Promise<T>): Promise<T> {
  return await routineAccountHealthCheckDiagnosticLimit(task)
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
