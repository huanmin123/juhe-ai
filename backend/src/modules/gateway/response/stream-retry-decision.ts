import type { ResponseInspectionDecision } from './inspection.js'
import { gatewayStreamClientRetryErrorCode } from './responses.js'

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
  return decision?.reason === 'configured_response_policy'
    && decision.retryEnabled === true
    && decision.policySource !== 'system_default'
    && totalResponseBytes === 0
    && !response.headersSent
    && !response.writableEnded
    && !response.destroyed
}

export function shouldInterruptCommittedGenericStream(clientRetryEnabled: boolean, downstreamBytesWritten: number): boolean {
  return !clientRetryEnabled && downstreamBytesWritten > 0
}
