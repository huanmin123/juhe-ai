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
  'default_gemini_cli_retryable_error'
])

export type StreamServerRetryReason =
  | 'response_inspection'
  | 'upstream_protocol_failure'
  | 'pre_commit_stream_failure'
  | 'normal_route_first_byte_timeout'
  | 'hybrid_quality'

export function shouldRetryResponseInspectionOnServer(
  streamResult: StreamPipeResult,
  response: StreamRetryResponseState
): streamResult is StreamPipeResult & { responseInspection: ResponseInspectionDecision } {
  const decision = streamResult.responseInspection
  return !streamResult.semanticCommitted
    && shouldRetryResponseInspectionDecisionOnServer(decision, {
      ...response,
      headersSent: false
    })
}

export function shouldRetryResponseInspectionDecisionOnServer(
  decision: ResponseInspectionDecision | undefined,
  response: StreamRetryResponseState
): decision is ResponseInspectionDecision {
  const serverRetryableSystemDefault = isServerRetryableSystemDefaultResponseInspectionDecision(decision)
  return decision !== undefined
    && (decision.reason === 'configured_response_policy' || serverRetryableSystemDefault)
    && (
      decision.policySource !== 'system_default'
      || serverRetryableSystemDefault
    )
    && !response.headersSent
    && !response.writableEnded
    && !response.destroyed
}

function isServerRetryableSystemDefaultResponseInspectionDecision(
  decision: ResponseInspectionDecision | undefined
): boolean {
  return decision?.policySource === 'system_default'
    && serverRetryableSystemDefaultResponseInspectionPolicyIds.has(decision.policyId ?? '')
}

export function shouldRetryPreCommitStreamFailureOnServer(
  streamResult: StreamPipeResult,
  response: StreamRetryResponseState
): boolean {
  return !streamResult.completed
    && !streamResult.semanticCommitted
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

export function shouldRememberCodexTurnStreamFailure(
  streamResult: StreamPipeResult,
  clientStrategy: OpenAIGatewayClientStrategyContext | undefined
): clientStrategy is OpenAIGatewayClientStrategyContext {
  return !streamResult.completed
    && clientStrategy?.allowCodexTurnAccountAvoidance === true
    && (
      streamResult.errorCode === gatewayStreamClientRetryErrorCode
      || streamResult.responseInspection?.rewriteErrorCode === gatewayStreamClientRetryErrorCode
    )
}
