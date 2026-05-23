import type { StreamInterceptDecision } from './openai-gateway-stream-intercept.js'

export function streamInterceptAuditMetadata(decision: StreamInterceptDecision): Record<string, unknown> {
  return {
    streamIntercepted: true,
    fallbackReason: decision.reason,
    interceptAction: decision.action,
    triggerPhase: decision.triggerPhase,
    upstreamEventType: decision.upstreamEventType,
    upstreamErrorCode: decision.upstreamErrorCode,
    upstreamErrorMessage: decision.upstreamErrorMessage,
    rewriteErrorCode: decision.rewriteErrorCode,
    rewriteMessage: decision.rewriteMessage,
    outputSeen: decision.outputSeen
  }
}
