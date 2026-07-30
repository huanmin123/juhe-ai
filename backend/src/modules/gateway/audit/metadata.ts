import type { ResponseInspectionDecision } from '../response/inspection.js'

export function responseInspectionAuditMetadata(decision: ResponseInspectionDecision): Record<string, unknown> {
  return {
    responsePolicyMatched: true,
    responseInspectionIntercepted: decision.action !== 'dry_run',
    fallbackReason: decision.reason,
    inspectionAction: decision.action,
    transport: decision.transport,
    endpointFamily: decision.endpointFamily,
    frameType: decision.frameType,
    triggerPhase: decision.triggerPhase,
    upstreamEventType: decision.upstreamEventType,
    upstreamErrorCode: decision.upstreamErrorCode,
    upstreamErrorType: decision.upstreamErrorType,
    upstreamErrorMessage: decision.upstreamErrorMessage,
    finishReason: decision.finishReason,
    clientProfile: decision.clientProfile,
    codexCompactionExpected: decision.codexCompactionExpected,
    rewriteErrorCode: decision.rewriteErrorCode,
    rewriteMessage: decision.rewriteMessage,
    downstreamWritten: decision.downstreamWritten,
    policyId: decision.policyId,
    policyName: decision.policyName,
    policySource: decision.policySource,
    policyScopeType: decision.policyScopeType,
    policyProtocolCode: decision.policyProtocolCode,
    policyProviderCode: decision.policyProviderCode,
    executionMode: decision.executionMode,
    dataHandling: decision.dataHandling,
    retryEnabled: decision.retryEnabled,
    accountSwitch: decision.accountSwitch,
    accountState: decision.accountState,
    matchedField: decision.matchedField,
    matchedValue: decision.matchedValue,
    matchedSnippet: decision.matchedSnippet
  }
}
