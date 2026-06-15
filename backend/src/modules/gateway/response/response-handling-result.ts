import type { StreamServerRetryReason } from './stream-finalization-retry-decision.js'
import type { ResponseInspectionDecision } from './inspection.js'
import type { StreamBodyOmissionSummary } from './stream-result.js'
import type { ParsedUsage } from '../usage/types.js'

export type UpstreamResponseHandlingResult =
  | { alreadyFinalized: true }
  | {
    alreadyFinalized: false
    retryUpstream: true
    retryReason: StreamServerRetryReason
    responseInspection?: ResponseInspectionDecision
    excludeCurrentAccount: boolean
    message: string
    errorCode?: string
    uncommittedResponseBody?: Buffer
  }
  | {
    alreadyFinalized: false
    retryUpstream?: false
    usage: ParsedUsage
    firstTokenMs?: number
    responseBodyText?: string
    bodyOmission?: StreamBodyOmissionSummary
    errorPayload: Record<string, unknown>
  }
