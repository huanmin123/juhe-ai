export type StreamInterceptRuleProvider = 'openai' | 'all'
export type StreamInterceptRuleEndpoint = string | 'all'
export type StreamInterceptRuleAction = 'client_retry' | 'server_replay' | 'custom_rewrite'
export type StreamInterceptRuleTriggerPhase = 'before_output' | 'after_output' | 'all'
export type StreamInterceptRuleAccountPolicy = 'temporary_unavailable' | 'none'
export type StreamInterceptRuleSource = 'audit_log' | 'source_code' | 'manual_verification'

export interface StreamInterceptRuleMatch {
  eventTypes: readonly string[] | 'all'
  errorCodes?: readonly string[]
  messageKeywords?: readonly string[]
  dataKeywords?: readonly string[]
}

export interface StreamInterceptClientRetryAction {
  rewriteErrorCode: string
  rewriteMessage: string
}

export interface StreamInterceptServerReplayAction {
  maxAttempts?: number
}

export interface StreamInterceptCustomRewriteAction {
  eventName: string
  data: Record<string, unknown> | string
  closeStream: boolean
}

export interface StreamInterceptRule {
  id: string
  enabled: boolean
  name: string
  description?: string
  source?: StreamInterceptRuleSource
  rationale?: string
  provider: StreamInterceptRuleProvider
  endpoint: StreamInterceptRuleEndpoint
  streamOnly: boolean
  match: StreamInterceptRuleMatch
  triggerPhase: StreamInterceptRuleTriggerPhase
  action: StreamInterceptRuleAction
  clientRetry?: StreamInterceptClientRetryAction
  serverReplay?: StreamInterceptServerReplayAction
  customRewrite?: StreamInterceptCustomRewriteAction
  accountPolicy: StreamInterceptRuleAccountPolicy
  cooldownMinutes?: number
}

export type StreamClientRetryInterceptRule = StreamInterceptRule & {
  triggerPhase: 'before_output'
  action: 'client_retry'
  clientRetry: StreamInterceptClientRetryAction
}

export const openAIStreamInterceptRules = [
  {
    id: 'codex_server_overloaded_client_retry',
    enabled: true,
    name: 'Codex 容量错误转客户端重试',
    description: '上游 SSE 返回 server_is_overloaded 且未产生输出时，改写为 Codex 可重试错误。',
    source: 'audit_log',
    rationale: 'Codex 会把 server_is_overloaded 映射为不可重试的 ServerOverloaded；未输出前改写为普通流错误可触发客户端自动重试。',
    provider: 'openai',
    endpoint: '/v1/responses',
    streamOnly: true,
    match: {
      eventTypes: 'all',
      errorCodes: ['server_is_overloaded']
    },
    triggerPhase: 'before_output',
    action: 'client_retry',
    clientRetry: {
      rewriteErrorCode: 'upstream_retryable_error',
      rewriteMessage: 'Upstream returned a retryable stream failure before output. Please retry.'
    },
    accountPolicy: 'none'
  },
  {
    id: 'codex_slow_down_client_retry',
    enabled: true,
    name: 'Codex slow_down 转客户端重试',
    description: '上游 SSE 返回 slow_down 且未产生输出时，改写为 Codex 可重试错误。',
    source: 'source_code',
    rationale: 'Codex 会把 slow_down 映射为不可重试的 ServerOverloaded；未输出前改写为普通流错误可触发客户端自动重试。',
    provider: 'openai',
    endpoint: '/v1/responses',
    streamOnly: true,
    match: {
      eventTypes: 'all',
      errorCodes: ['slow_down']
    },
    triggerPhase: 'before_output',
    action: 'client_retry',
    clientRetry: {
      rewriteErrorCode: 'upstream_retryable_error',
      rewriteMessage: 'Upstream returned a retryable stream failure before output. Please retry.'
    },
    accountPolicy: 'none'
  }
] satisfies readonly StreamInterceptRule[]

export function isExecutableClientRetryStreamRule(rule: StreamInterceptRule): rule is StreamClientRetryInterceptRule {
  return rule.enabled
    && rule.action === 'client_retry'
    && rule.triggerPhase === 'before_output'
    && Boolean(rule.clientRetry?.rewriteErrorCode)
    && Boolean(rule.clientRetry?.rewriteMessage)
}
