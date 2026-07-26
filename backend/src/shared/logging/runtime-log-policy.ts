export const DB_SERVICE_SLOW_REQUEST_THRESHOLD_MS = 50
export const GATEWAY_SLOW_STAGE_THRESHOLD_MS = 1_000

export type RuntimeStageOutcome = 'success' | 'skipped' | 'expected_failure' | 'unexpected_failure' | 'aborted'
export type RuntimeStageLogLevel = 'debug' | 'info' | 'warn' | 'error'

export function dbServiceSuccessLogLevel(durationMs: number): 'debug' | 'info' {
  return normalizedDurationMs(durationMs) >= DB_SERVICE_SLOW_REQUEST_THRESHOLD_MS ? 'info' : 'debug'
}

export function gatewayRequestStageLogLevel(
  outcome: RuntimeStageOutcome,
  durationMs: number
): RuntimeStageLogLevel {
  if (outcome === 'unexpected_failure') return 'error'
  if (outcome === 'expected_failure' || outcome === 'aborted') return 'warn'
  return normalizedDurationMs(durationMs) >= GATEWAY_SLOW_STAGE_THRESHOLD_MS ? 'info' : 'debug'
}

function normalizedDurationMs(value: number): number {
  return Number.isFinite(value) ? Math.max(0, value) : 0
}
