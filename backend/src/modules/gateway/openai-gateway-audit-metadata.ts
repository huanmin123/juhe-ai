import type { StreamInterceptDecision } from './openai-gateway-stream-intercept.js'
import type { UpstreamErrorFeatureDecision } from './openai-gateway-upstream-error-rules.js'

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

export function upstreamErrorFeatureAuditMetadata(decision: UpstreamErrorFeatureDecision): Record<string, unknown> {
  return {
    upstreamErrorFeatureMatched: true,
    featureRuleId: decision.ruleId,
    featureRuleName: decision.ruleName,
    action: decision.action,
    statusCode: decision.statusCode,
    upstreamErrorType: decision.upstreamErrorType,
    upstreamErrorCode: decision.upstreamErrorCode,
    upstreamErrorMessage: decision.upstreamErrorMessage,
    accountPolicy: decision.accountPolicy
  }
}

export function upstreamErrorFeatureActionLogMessage(_decision: UpstreamErrorFeatureDecision): string {
  return '命中上游错误响应特征规则，按请求级失败原样返回客户端'
}
