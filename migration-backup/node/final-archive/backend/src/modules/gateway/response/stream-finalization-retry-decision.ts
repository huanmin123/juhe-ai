import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import {
  type ResponseInspectionDecision
} from './inspection.js'
import {
  gatewayStreamClientRetryErrorCode,
  gatewayStreamFailureCode
} from './responses.js'
import type { StreamRetryResponseState } from './stream-retry-decision.js'
import type { StreamPipeResult } from './stream.js'

const serverRetryableSystemDefaultResponseInspectionPolicyIds = new Set([
  'default_codex_compaction_contract',
  'default_openai_context_window_error',
  'default_gemini_cli_retryable_error'
])

const transientPrecommitUpstreamPolicyIds = new Set([
  'default_openai_transient_precommit_error',
  'default_anthropic_transient_precommit_error',
  'default_gemini_transient_precommit_error'
])

export type StreamServerRetryReason =
  | 'response_inspection'
  | 'upstream_protocol_failure'
  | 'pre_commit_stream_failure'
  | 'codex_encrypted_content_recovery'
  | 'normal_route_first_byte_timeout'
  | 'hybrid_quality'

export function shouldRetryResponseInspectionOnServer(
  streamResult: StreamPipeResult,
  response: StreamRetryResponseState
): streamResult is StreamPipeResult & { responseInspection: ResponseInspectionDecision } {
  // Response-inspection is diagnostic/validation logic, not an account error
  // rule.  A failure it observes must be reported by the current attempt.
  return false
}

export function shouldRetryResponseInspectionDecisionOnServer(
  decision: ResponseInspectionDecision | undefined,
  response: StreamRetryResponseState
): decision is ResponseInspectionDecision {
  const serverRetryableSystemDefault = isServerRetryableSystemDefaultResponseInspectionDecision(decision)
  const transientPrecommitUpstreamFailure = isTransientPrecommitUpstreamFailureDecision(decision)
  return decision !== undefined
    && (
      transientPrecommitUpstreamFailure
      || decision.replayAuthority === 'explicit_user_policy'
      || decision.replayAuthority === 'system_default_retry_next_account'
    )
    && (
      transientPrecommitUpstreamFailure
      || decision.accountSwitch === 'request_next_account'
      || decision.accountSwitch === 'avoid_account_ttl'
      || decision.accountSwitch === 'avoid_upstream_bucket_ttl'
    )
    && (decision.reason === 'configured_response_policy' || serverRetryableSystemDefault || transientPrecommitUpstreamFailure)
    && (
      transientPrecommitUpstreamFailure
      || decision.policySource !== 'system_default'
      || serverRetryableSystemDefault
    )
    && !response.writableEnded
    && !response.destroyed
}

function isServerRetryableSystemDefaultResponseInspectionDecision(
  decision: ResponseInspectionDecision | undefined
): boolean {
  return decision?.policySource === 'system_default'
    && serverRetryableSystemDefaultResponseInspectionPolicyIds.has(decision.policyId ?? '')
}

export function isTransientPrecommitUpstreamFailureDecision(
  decision: ResponseInspectionDecision | undefined
): boolean {
  return decision?.policySource === 'system_default'
    && decision.reason === 'before_downstream_write_response_failure'
    && decision.triggerPhase === 'before_downstream_write'
    && decision.downstreamWritten !== true
    && transientPrecommitUpstreamPolicyIds.has(decision.policyId ?? '')
}

export function shouldRetryPreCommitStreamFailureOnServer(
  streamResult: StreamPipeResult,
  response: StreamRetryResponseState
): boolean {
  // A stream with no semantic event is replayable whether it has written a
  // transport-only heartbeat or has not committed any downstream bytes yet.
  // The downstream byte/state pair is only evidence that a transport heartbeat
  // was actually written; HTTP headers alone never enter this decision.
  return !streamResult.completed
    && streamResult.semanticCommitted !== true
    && streamResult.gatewayLocalFailure !== true
    && streamResult.errorCode !== undefined
    && !response.writableEnded
    && !response.destroyed
}

export function preCommitStreamServerRetryErrorCode(
  streamResult: StreamPipeResult,
  clientStrategy: OpenAIGatewayClientStrategyContext | undefined
): string | undefined {
  return clientStrategy?.retryCoordination.preCommitFailureSignal === 'protocol_error_event'
    ? gatewayStreamClientRetryErrorCode
    : gatewayStreamFailureCode(streamResult.message)
}

export function shouldExcludeCurrentAccountForStreamServerRetry(decision: ResponseInspectionDecision): boolean {
  return decision.accountSwitch === 'request_next_account'
    || decision.accountSwitch === 'avoid_account_ttl'
    || decision.accountSwitch === 'avoid_upstream_bucket_ttl'
    || decision.accountState === 'runtime_avoidance'
}

export function shouldRememberGatewayClientSourceFailure(
  streamResult: StreamPipeResult,
  clientStrategy: OpenAIGatewayClientStrategyContext | undefined
): clientStrategy is OpenAIGatewayClientStrategyContext {
  return !streamResult.completed
    && streamResult.gatewayLocalFailure !== true
    && clientStrategy?.allowClientSourceAccountAvoidance === true
    && (
      streamResult.errorCode === gatewayStreamClientRetryErrorCode
      || streamResult.responseInspection?.rewriteErrorCode === gatewayStreamClientRetryErrorCode
    )
}
