import type { StreamServerRetryReason } from './stream-finalization-retry-decision.js'
import type { ResponseInspectionDecision } from './inspection.js'
import type { StreamBodyOmissionSummary, StreamTransportFailure } from './stream-result.js'
import type { ParsedUsage } from '../usage/types.js'
import type { HybridQualityInspectionOutcome } from '../hybrid/quality-inspection.service.js'
import type { CodexResponsesGuardUsageSummary } from '../codex-responses/response-guard.js'

export type UpstreamResponseHandlingResult =
  | { alreadyFinalized: true; errorCode?: string; transportFailure?: StreamTransportFailure; gatewayLocalFailure?: boolean }
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
    gatewayLocalFailure?: boolean
  }
  | {
    alreadyFinalized: false
    retryUpstream?: false
    usage: ParsedUsage
    firstTokenMs?: number
    responseBodyText?: string
    responseResourceId?: string
    bodyOmission?: StreamBodyOmissionSummary
    codexResponsesGuard?: CodexResponsesGuardUsageSummary
    protocolValidatedSuccess?: boolean
    errorPayload: Record<string, unknown>
    transportFailure?: StreamTransportFailure
    gatewayLocalFailure?: boolean
  }
