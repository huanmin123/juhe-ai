import { accountProbeObservationId } from '../../domain/account-status-presentation.js'
import type { AccountProbeObservation } from '../../domain/types.js'
import { isCompletedRealUpstreamAttempt, isRealUpstreamAttempt, type UpstreamAttempt } from '../gateway/upstream/attempt.js'

export type AutomaticAccountProbeOutcome =
  | 'complete_success'
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

type TransportProbeUpstreamAttempt = Pick<UpstreamAttempt, 'upstreamUrl' | 'status' | 'message'>

export function transportProbeOutcomeFromAccountTestResult(
  result: TransportProbeAccountTestResult,
  evidence: {
    upstreamAttempt?: TransportProbeUpstreamAttempt
    canceled?: boolean
    timeout?: boolean
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
  const localFailureKind = transportProbeLocalFailureKind(result, realUpstreamAttempt, statusCode, evidence.timeout === true)

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
  return outcome.kind === 'framing_complete'
    && result.firstTokenMs !== undefined
    && result.firstTokenMs <= firstByteThresholdMs
}

function transportProbeLocalFailureKind(
  result: TransportProbeAccountTestResult,
  upstreamAttempt: TransportProbeUpstreamAttempt | undefined,
  statusCode: number | undefined,
  timedOut: boolean
): 'timeout' | 'connection' | 'read' | undefined {
  if (timedOut) return 'timeout'
  const errorCode = result.errorCode?.trim().toLowerCase()
  if (errorCode === 'upstream_body_interrupted' || errorCode === 'upstream_read_interrupted') {
    return 'read'
  }
  if (errorCode === 'first_byte_timeout' || errorCode === 'upstream_timeout') {
    return 'timeout'
  }
  if (statusCode !== undefined) return undefined
  const diagnostic = [result.errorCode, upstreamAttempt?.message]
    .filter((value): value is string => typeof value === 'string' && value.trim().length > 0)
    .join(' ')
    .toLowerCase()
  if (!diagnostic) return undefined
  if (/(?:timeout|timed out|deadline|etimedout|aborterror)/.test(diagnostic)) {
    return 'timeout'
  }
  if (/(?:econnrefused|econnreset|enotfound|eai_again|und_err_connect|connect error|connection refused|socket hang up|proxy connect)/.test(diagnostic)) {
    return 'connection'
  }
  return undefined
}

export function automaticAccountProbeOutcome(
  result: { success: boolean; accountFailureEligible?: boolean },
  upstreamResponseObserved: boolean
): Exclude<AutomaticAccountProbeOutcome, 'stale'> {
  if (result.success) return 'complete_success'
  return upstreamResponseObserved ? 'upstream_failure' : 'probe_task_failure'
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
