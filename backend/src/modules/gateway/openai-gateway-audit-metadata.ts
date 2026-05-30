import type { StreamInterceptDecision } from './openai-gateway-stream-intercept.js'

export function streamInterceptAuditMetadata(decision: StreamInterceptDecision): Record<string, unknown> {
  return {
    streamPolicyMatched: true,
    streamIntercepted: decision.action !== 'dry_run',
    fallbackReason: decision.reason,
    interceptAction: decision.action,
    triggerPhase: decision.triggerPhase,
    runtimePhase: decision.runtimePhase,
    upstreamEventType: decision.upstreamEventType,
    upstreamErrorCode: decision.upstreamErrorCode,
    upstreamErrorMessage: decision.upstreamErrorMessage,
    rewriteErrorCode: decision.rewriteErrorCode,
    rewriteMessage: decision.rewriteMessage,
    downstreamWritten: decision.downstreamWritten,
    policyId: decision.policyId,
    policyName: decision.policyName,
    policySource: decision.policySource,
    executionMode: decision.executionMode,
    dataHandling: decision.dataHandling,
    retryEnabled: decision.retryEnabled,
    accountSwitch: decision.accountSwitch,
    accountState: decision.accountState,
    avoidanceTtlSeconds: decision.avoidanceTtlSeconds,
    matchedField: decision.matchedField,
    matchedValue: decision.matchedValue,
    matchedSnippet: decision.matchedSnippet
  }
}
