import type { StreamServerRetryReason } from './stream-finalization-retry-decision.js'
import type { ResponseInspectionDecision } from './inspection.js'
import type { StreamBodyOmissionSummary, StreamTransportFailure } from './stream-result.js'
import type { ParsedUsage } from '../usage/types.js'
import type { HybridQualityInspectionOutcome } from '../hybrid/quality-inspection.service.js'

export type UpstreamResponseHandlingResult =
  | { alreadyFinalized: true; transportFailure?: StreamTransportFailure }
  | {
    alreadyFinalized: false
    retryUpstream: true
    retryReason: StreamServerRetryReason
    responseInspection?: ResponseInspectionDecision
    excludeCurrentAccount: boolean
    message: string
    errorCode?: string
    statusCode?: number
    uncommittedResponseBody?: Buffer
    hybridQuality?: HybridQualityInspectionOutcome
    transportFailure?: StreamTransportFailure
  }
  | {
    alreadyFinalized: false
    retryUpstream?: false
    usage: ParsedUsage
    firstTokenMs?: number
    responseBodyText?: string
    responseResourceId?: string
    bodyOmission?: StreamBodyOmissionSummary
    errorPayload: Record<string, unknown>
    transportFailure?: StreamTransportFailure
  }
