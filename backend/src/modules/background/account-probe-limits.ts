import pLimit from 'p-limit'

import type { AccountTestResult } from '../../domain/types.js'
import type { UpstreamAttempt } from '../gateway/upstream/attempt.js'

export const backgroundFullDiagnosticConcurrency = 3
export const backgroundProbeDbServiceTimeoutMs = 30_000
export const cooldownAccountRetestStartupDelayMs = 60_000
export const accountApiKeyCooldownRetestStartupDelayMs = 65_000
export const normalRouteSpeedFirstProbeStartupDelayMs = 75_000

const backgroundFullDiagnosticLimit = pLimit(backgroundFullDiagnosticConcurrency)
const backgroundAccountAvailabilityProbesInFlight = new Map<string, {
  promise: Promise<BackgroundAccountAvailabilityProbeObservation>
  consumers: number
}>()

export interface BackgroundAccountAvailabilityProbeObservation {
  result: AccountTestResult
  upstreamAttempt?: UpstreamAttempt
}

export function backgroundFullDiagnosticQueueConcurrency(batchSize: number): number {
  const normalizedBatchSize = Number.isFinite(batchSize) ? Math.trunc(batchSize) : 1
  return Math.max(1, Math.min(normalizedBatchSize, backgroundFullDiagnosticConcurrency))
}

export async function runWithBackgroundFullDiagnosticSlot<T>(task: () => Promise<T>): Promise<T> {
  return await backgroundFullDiagnosticLimit(task)
}

export async function runWithBackgroundAccountAvailabilityProbe<T>(
  runtimeKey: string,
  task: () => Promise<BackgroundAccountAvailabilityProbeObservation>,
  consume: (
    observation: BackgroundAccountAvailabilityProbeObservation,
    context: { joined: boolean }
  ) => Promise<T>
): Promise<T> {
  const normalizedKey = runtimeKey.trim()
  if (!normalizedKey) {
    return await consume(await task(), { joined: false })
  }
  let entry = backgroundAccountAvailabilityProbesInFlight.get(normalizedKey)
  const joined = Boolean(entry)
  if (!entry) {
    entry = {
      promise: Promise.resolve().then(task),
      consumers: 0
    }
    backgroundAccountAvailabilityProbesInFlight.set(normalizedKey, entry)
  }
  entry.consumers += 1
  try {
    return await consume(await entry.promise, { joined })
  } finally {
    entry.consumers -= 1
    if (entry.consumers === 0 && backgroundAccountAvailabilityProbesInFlight.get(normalizedKey) === entry) {
      backgroundAccountAvailabilityProbesInFlight.delete(normalizedKey)
    }
  }
}
