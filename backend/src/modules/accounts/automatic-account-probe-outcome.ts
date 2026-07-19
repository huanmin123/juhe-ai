import { accountProbeObservationId } from '../../domain/account-status-presentation.js'
import type { AccountProbeObservation } from '../../domain/types.js'

export type AutomaticAccountProbeOutcome =
  | 'complete_success'
  | 'upstream_failure'
  | 'probe_task_failure'
  | 'stale'

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
