import {
  defaultGatewayFinalResponseReserveMs,
  type GatewayRequestWallBudget
} from './route-coordination.js'

export interface NormalRouteFirstByteRuntimeConfig {
  schedulingPreference: 'speed_first'
  firstByteDeadlineMs: number
}

export interface NormalRouteAttemptFirstByteDeadlineInput {
  config: NormalRouteFirstByteRuntimeConfig
  gatewayRequestWallBudget: GatewayRequestWallBudget
  attemptStartedAtMs: number
  laneFirstByteTimeoutMs: number
  uncommittedAttemptMaxLifetimeMs: number
  requestPrecommitDeadlineAtMs?: number
  finalResponseReserveMs?: number
}

export interface NormalRouteAttemptFirstByteDeadline {
  configuredDeadlineMs: number
  effectiveDeadlineMs: number
  deadlineAtMs: number
  schedulingPreference: 'speed_first'
  clipped: boolean
  limitingFactor: 'configured' | 'wall_precommit' | 'uncommitted_attempt' | 'lane_timeout'
}

/**
 * Freezes one attempt's pre-first-byte deadline at dispatch time. Readers use
 * the returned duration relative to attemptStartedAtMs, so later body reads do
 * not accidentally reset either the route deadline or the wall-clock budget.
 */
export function normalRouteAttemptFirstByteDeadline(
  input: NormalRouteAttemptFirstByteDeadlineInput
): NormalRouteAttemptFirstByteDeadline {
  const attemptStartedAtMs = normalizedTimestamp(input.attemptStartedAtMs)
  const configuredDeadlineMs = normalizedPositiveMs(input.config.firstByteDeadlineMs)
  const laneFirstByteTimeoutMs = normalizedPositiveMs(input.laneFirstByteTimeoutMs)
  const uncommittedAttemptMaxLifetimeMs = normalizedPositiveMs(input.uncommittedAttemptMaxLifetimeMs)
  const wallPrecommitRemainingMs = input.gatewayRequestWallBudget.precommitRemainingMs({
    nowMs: attemptStartedAtMs,
    requestPrecommitDeadlineAtMs: input.requestPrecommitDeadlineAtMs,
    finalResponseReserveMs: input.finalResponseReserveMs ?? defaultGatewayFinalResponseReserveMs
  })
  const candidates = [
    { factor: 'configured' as const, value: configuredDeadlineMs },
    { factor: 'wall_precommit' as const, value: wallPrecommitRemainingMs },
    { factor: 'uncommitted_attempt' as const, value: uncommittedAttemptMaxLifetimeMs },
    { factor: 'lane_timeout' as const, value: laneFirstByteTimeoutMs }
  ]
  const limitingCandidate = candidates.reduce((current, candidate) => (
    candidate.value < current.value ? candidate : current
  ))
  const effectiveDeadlineMs = input.gatewayRequestWallBudget.clipFirstByteDeadlineMs({
    nowMs: attemptStartedAtMs,
    firstByteDeadlineMs: limitingCandidate.value,
    requestPrecommitDeadlineAtMs: input.requestPrecommitDeadlineAtMs,
    finalResponseReserveMs: input.finalResponseReserveMs ?? defaultGatewayFinalResponseReserveMs,
    uncommittedAttemptDeadlineAtMs: attemptStartedAtMs + uncommittedAttemptMaxLifetimeMs
  })

  return {
    configuredDeadlineMs,
    effectiveDeadlineMs,
    deadlineAtMs: attemptStartedAtMs + effectiveDeadlineMs,
    schedulingPreference: input.config.schedulingPreference,
    clipped: effectiveDeadlineMs < configuredDeadlineMs,
    limitingFactor: limitingCandidate.factor
  }
}

function normalizedTimestamp(value: number): number {
  return Number.isFinite(value) ? Math.max(0, Math.trunc(value)) : 0
}

function normalizedPositiveMs(value: number): number {
  return Number.isFinite(value) ? Math.max(1, Math.trunc(value)) : 1
}
