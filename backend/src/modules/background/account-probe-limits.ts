import pLimit from 'p-limit'

export const backgroundFullDiagnosticConcurrency = 3
export const backgroundProbeDbServiceTimeoutMs = 30_000
export const cooldownAccountRetestStartupDelayMs = 60_000
export const accountApiKeyCooldownRetestStartupDelayMs = 65_000
export const normalRouteSpeedFirstProbeStartupDelayMs = 75_000

const backgroundFullDiagnosticLimit = pLimit(backgroundFullDiagnosticConcurrency)

export function backgroundFullDiagnosticQueueConcurrency(batchSize: number): number {
  const normalizedBatchSize = Number.isFinite(batchSize) ? Math.trunc(batchSize) : 1
  return Math.max(1, Math.min(normalizedBatchSize, backgroundFullDiagnosticConcurrency))
}

export async function runWithBackgroundFullDiagnosticSlot<T>(task: () => Promise<T>): Promise<T> {
  return await backgroundFullDiagnosticLimit(task)
}
