import type { ResponseInspectionDecision } from './inspection.js'
import { gatewayStreamClientRetryErrorCode } from './responses.js'

const serverRetryableSystemDefaultResponseInspectionPolicyIds = new Set([
  'default_codex_compaction_contract'
])

export interface StreamRetryResponseState {
  headersSent: boolean
  writableEnded: boolean
  destroyed: boolean
}

export function streamClientFailureCode(
  errorCode: string,
  outputReceived: boolean,
  clientRetryEnabled: boolean,
  downstreamBytesWritten: number
): string {
  return clientRetryEnabled && (!outputReceived || downstreamBytesWritten > 0)
    ? gatewayStreamClientRetryErrorCode
    : errorCode
}

export function shouldReturnResponseInspectionBeforeDownstreamWrite(
  decision: ResponseInspectionDecision | undefined,
  response: StreamRetryResponseState,
  totalResponseBytes: number
): boolean {
  const serverRetryableSystemDefault = isServerRetryableSystemDefaultResponseInspectionDecision(decision)
  return decision !== undefined
    && (decision.reason === 'configured_response_policy' || serverRetryableSystemDefault)
    && (
      decision.policySource !== 'system_default'
      || serverRetryableSystemDefault
    )
    && totalResponseBytes === 0
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

export function shouldInterruptCommittedGenericStream(clientRetryEnabled: boolean, downstreamBytesWritten: number): boolean {
  return !clientRetryEnabled && downstreamBytesWritten > 0
}
