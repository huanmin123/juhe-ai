import { accountProbeObservationId } from '../../domain/account-status-presentation.js'
import type { AccountProbeObservation } from '../../domain/types.js'
import { isCompletedRealUpstreamAttempt, isRealUpstreamAttempt, type UpstreamAttempt } from '../gateway/upstream/attempt.js'

export type AutomaticAccountProbeOutcome =
  | 'complete_success'
  | 'framing_complete_neutral'
  | 'upstream_failure'
  | 'probe_task_failure'
  | 'stale'

export type TransportProbeFailureKind =
  | 'timeout'
  | 'connection'
  | 'read'
  | 'canceled'
  | 'task_failure'

export type TransportProbeOutcome =
  | {
      kind: 'framing_complete'
      statusCode: number
    }
  | {
      kind: 'transport_incomplete'
      failureKind: 'timeout' | 'connection' | 'read'
      statusCode?: number
    }
  | {
      kind: 'unknown'
      failureKind: 'canceled' | 'task_failure'
    }

interface TransportProbeAccountTestResult {
  success: boolean
  statusCode?: number
  errorCode?: string
  message?: string
}

type TransportProbeUpstreamAttempt = Pick<UpstreamAttempt, 'upstreamUrl' | 'status' | 'message' | 'transportFailureKind'>

export function transportProbeOutcomeFromAccountTestResult(
  result: TransportProbeAccountTestResult,
  evidence: {
    upstreamAttempt?: TransportProbeUpstreamAttempt
    canceled?: boolean
    timeout?: boolean
    diagnosticTimeoutExhausted?: boolean
  } = {}
): TransportProbeOutcome {
  if (evidence.canceled) {
    return { kind: 'unknown', failureKind: 'canceled' }
  }

  const upstreamAttempt = evidence.upstreamAttempt
  const realUpstreamAttempt = upstreamAttempt && isRealUpstreamAttempt(upstreamAttempt)
    ? upstreamAttempt
    : undefined
  const statusCode = realUpstreamAttempt && isCompletedRealUpstreamAttempt(realUpstreamAttempt)
    ? realUpstreamAttempt.status
    : undefined
  const localFailureKind = transportProbeLocalFailureKind(
    realUpstreamAttempt,
    statusCode,
    evidence.timeout === true,
    evidence.diagnosticTimeoutExhausted === true
  )

  if (localFailureKind) {
    return {
      kind: 'transport_incomplete',
      failureKind: localFailureKind,
      ...(statusCode === undefined ? {} : { statusCode })
    }
  }
  if (statusCode !== undefined) {
    return { kind: 'framing_complete', statusCode }
  }
  if (evidence.timeout === true && evidence.diagnosticTimeoutExhausted !== true && !localFailureKind) {
    return { kind: 'unknown', failureKind: 'task_failure' }
  }
  if (realUpstreamAttempt) {
    return { kind: 'transport_incomplete', failureKind: 'connection' }
  }
  return { kind: 'unknown', failureKind: 'task_failure' }
}

export function transportProbeMeetsFirstByteTarget(
  result: { success?: boolean; firstTokenMs?: number },
  outcome: TransportProbeOutcome,
  firstByteThresholdMs: number
): boolean {
  return result.success === true
    && outcome.kind === 'framing_complete'
    && result.firstTokenMs !== undefined
    && result.firstTokenMs <= firstByteThresholdMs
}

function transportProbeLocalFailureKind(
  upstreamAttempt: TransportProbeUpstreamAttempt | undefined,
  statusCode: number | undefined,
  timedOut: boolean,
  diagnosticTimeoutExhausted: boolean
): 'timeout' | 'connection' | 'read' | undefined {
  // A diagnostic deadline becomes upstream timeout evidence only after every
  // tier in the current probe phase made a real HTTP(S) attempt and timed out.
  if (upstreamAttempt?.transportFailureKind === 'timeout') return 'timeout'
  if (upstreamAttempt?.transportFailureKind === 'read_incomplete') return 'read'
  if (upstreamAttempt?.transportFailureKind === 'connection') return 'connection'
  if (statusCode !== undefined) return undefined
  if (timedOut) return diagnosticTimeoutExhausted ? 'timeout' : undefined
  return upstreamAttempt ? 'connection' : undefined
}

export function automaticAccountProbeOutcome(
  result: TransportProbeAccountTestResult & { accountFailureEligible?: boolean },
  evidence: {
    upstreamAttempt?: TransportProbeUpstreamAttempt
    canceled?: boolean
    timeout?: boolean
    diagnosticTimeoutExhausted?: boolean
  } = {}
): Exclude<AutomaticAccountProbeOutcome, 'stale'> {
  const transportOutcome = transportProbeOutcomeFromAccountTestResult(result, evidence)
  if (transportOutcome.kind === 'transport_incomplete') return 'upstream_failure'
  if (transportOutcome.kind === 'unknown') return 'probe_task_failure'
  return result.success ? 'complete_success' : 'framing_complete_neutral'
}

export function automaticAccountAvailabilityProbeFailed(
  outcome: Exclude<AutomaticAccountProbeOutcome, 'stale'>
): boolean {
  return outcome === 'upstream_failure' || outcome === 'framing_complete_neutral'
}

export function automaticAccountProbeObservation(input: {
  runtimeKey: string
  generation: number
  attemptCount: number
  attemptedAt: string
  probeOutcome: AutomaticAccountProbeOutcome
  success: boolean
  statusCode?: number
  errorCode?: string
  reason?: string
  traceId?: string
}): AccountProbeObservation | undefined {
  if (input.probeOutcome === 'probe_task_failure' || input.probeOutcome === 'stale') return undefined
  const observationId = accountProbeObservationId({
    kind: 'runtime_probe',
    identity: `${input.runtimeKey}:${input.generation}:${input.attemptCount}`,
    attemptedAt: input.attemptedAt,
    traceId: input.traceId,
    errorCode: input.errorCode,
    reason: input.reason
  })
  return {
    observationId,
    attemptedAt: input.attemptedAt,
    result: input.success ? 'success' : 'failed',
    httpStatus: input.statusCode,
    errorCode: input.errorCode,
    reason: input.success ? undefined : input.reason,
    traceId: input.traceId
  }
}
