import type { OpenAIGatewayClientStrategyContext } from '../client-profiles/strategy.js'
import {
  isCodexRetryableAfterOutputResponseFailureCode,
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
  | 'codex_pre_commit_stream_failure'
  | 'speed_first_first_byte_timeout'
  | 'hybrid_quality'

export function shouldRetryResponseInspectionOnServer(
  streamResult: StreamPipeResult,
  response: StreamRetryResponseState
): streamResult is StreamPipeResult & { responseInspection: ResponseInspectionDecision } {
  const decision = streamResult.responseInspection
  return shouldRetryResponseInspectionDecisionOnServer(decision, response)
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

export function shouldRetryCodexPreCommitStreamFailureOnServer(
  streamResult: StreamPipeResult,
  clientStrategy: OpenAIGatewayClientStrategyContext | undefined,
  response: StreamRetryResponseState
): boolean {
  const retryableAfterOutput = isCodexRetryableAfterOutputResponseFailureCode(streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode)
  return !streamResult.completed
    && streamResult.downstreamBytesWritten === 0
    && (!streamResult.outputReceived || retryableAfterOutput)
    && clientStrategy?.allowCodexTurnAccountAvoidance === true
    && (
      streamResult.errorCode === gatewayStreamClientRetryErrorCode
      || streamResult.responseInspection?.rewriteErrorCode === gatewayStreamClientRetryErrorCode
    )
    && !response.headersSent
    && !response.writableEnded
    && !response.destroyed
}

export function shouldRetryPreCommitStreamFailureOnServer(
  streamResult: StreamPipeResult,
  response: StreamRetryResponseState
): boolean {
  return !streamResult.completed
    && streamResult.downstreamBytesWritten === 0
    && !streamResult.outputReceived
    && streamResult.errorCode !== undefined
    && !response.headersSent
    && !response.writableEnded
    && !response.destroyed
}

export function preCommitStreamServerRetryErrorCode(
  streamResult: StreamPipeResult,
  clientStrategy: OpenAIGatewayClientStrategyContext | undefined
): string | undefined {
  return clientStrategy?.allowCodexStreamClientRetry === true
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
  const retryableAfterOutput = isCodexRetryableAfterOutputResponseFailureCode(streamResult.responseInspection?.upstreamErrorCode ?? streamResult.errorCode)
  return !streamResult.completed
    && (!streamResult.outputReceived || retryableAfterOutput)
    && clientStrategy?.allowCodexTurnAccountAvoidance === true
    && (
      streamResult.errorCode === gatewayStreamClientRetryErrorCode
      || streamResult.responseInspection?.rewriteErrorCode === gatewayStreamClientRetryErrorCode
    )
}
