export type UpstreamErrorFeatureRuleProvider = 'openai' | 'all'
export type UpstreamErrorFeatureRuleEndpoint = string | 'all'
export type UpstreamErrorFeatureRuleAction = 'passthrough_request_error'
export type UpstreamErrorFeatureRuleAccountPolicy = 'none'
export type UpstreamErrorFeatureRuleSource = 'audit_log' | 'source_code' | 'manual_verification'

export interface UpstreamErrorFeatureRuleMatch {
  statusCodes?: readonly number[]
  errorTypes?: readonly string[]
  errorCodes?: readonly string[]
  messageKeywords?: readonly string[]
  bodyKeywords?: readonly string[]
}

export interface UpstreamErrorFeatureRule {
  id: string
  enabled: boolean
  name: string
  description?: string
  source?: UpstreamErrorFeatureRuleSource
  rationale?: string
  provider: UpstreamErrorFeatureRuleProvider
  endpoint: UpstreamErrorFeatureRuleEndpoint
  streamOnly: boolean
  match: UpstreamErrorFeatureRuleMatch
  action: UpstreamErrorFeatureRuleAction
  accountPolicy: UpstreamErrorFeatureRuleAccountPolicy
}

export interface UpstreamErrorFeatureContext {
  provider: 'openai'
  endpoint: string
  stream: boolean
  statusCode: number
  bodyText: string
  parsedError: Record<string, unknown>
}

export interface UpstreamErrorFeatureDecision {
  ruleId: string
  ruleName: string
  action: UpstreamErrorFeatureRuleAction
  statusCode: number
  upstreamErrorType?: string
  upstreamErrorCode?: string
  upstreamErrorMessage?: string
  accountPolicy: UpstreamErrorFeatureRuleAccountPolicy
}

export const openAIUpstreamErrorFeatureRules = [
  {
    id: 'openai_tool_output_missing_request_passthrough',
    enabled: true,
    name: 'OpenAI 工具输出缺失按请求级错误返回',
    description: '上游 HTTP 400 返回 No tool output found for function call 时，判定为客户端请求上下文缺少工具结果，原样返回客户端。',
    source: 'audit_log',
    rationale: '该错误由请求消息链中 assistant tool_call 缺少对应 tool 输出导致，和具体上游账号无关；继续扫账号只会误冷却可用账号。',
    provider: 'openai',
    endpoint: 'all',
    streamOnly: false,
    match: {
      statusCodes: [400],
      errorTypes: ['invalid_request_error'],
      messageKeywords: ['No tool output found for function call']
    },
    action: 'passthrough_request_error',
    accountPolicy: 'none'
  },
  {
    id: 'openai_instructions_required_request_passthrough',
    enabled: true,
    name: 'OpenAI instructions 缺失按请求级错误返回',
    description: '上游 HTTP 400 返回 Instructions are required 时，判定为请求或上游协议形态错误，原样返回客户端。',
    source: 'audit_log',
    rationale: '生产审计显示该错误会把可用账号误判为临时不可调用；特征命中只说明本次请求被上游明确拒绝，不代表账号健康问题。',
    provider: 'openai',
    endpoint: '/v1/chat/completions',
    streamOnly: false,
    match: {
      statusCodes: [400],
      errorTypes: ['invalid_request_error'],
      messageKeywords: ['Instructions are required']
    },
    action: 'passthrough_request_error',
    accountPolicy: 'none'
  }
] satisfies readonly UpstreamErrorFeatureRule[]

export function matchUpstreamErrorFeatureRule(
  rules: readonly UpstreamErrorFeatureRule[],
  context: UpstreamErrorFeatureContext
): UpstreamErrorFeatureDecision | undefined {
  const rule = rules.find((item) => matchesUpstreamErrorFeatureRule(item, context))
  if (!rule) {
    return undefined
  }
  return {
    ruleId: rule.id,
    ruleName: rule.name,
    action: rule.action,
    statusCode: context.statusCode,
    upstreamErrorType: stringValue(context.parsedError.type),
    upstreamErrorCode: stringValue(context.parsedError.code),
    upstreamErrorMessage: stringValue(context.parsedError.message),
    accountPolicy: rule.accountPolicy
  }
}

function matchesUpstreamErrorFeatureRule(
  rule: UpstreamErrorFeatureRule,
  context: UpstreamErrorFeatureContext
): boolean {
  if (!rule.enabled) return false
  if (rule.streamOnly && !context.stream) return false
  if (rule.provider !== 'all' && rule.provider !== context.provider) return false
  if (rule.endpoint !== 'all' && !normalizeEndpoint(context.endpoint).endsWith(rule.endpoint)) return false
  if (rule.match.statusCodes && rule.match.statusCodes.length > 0 && !rule.match.statusCodes.includes(context.statusCode)) return false
  if (rule.match.errorTypes && rule.match.errorTypes.length > 0 && !rule.match.errorTypes.includes(stringValue(context.parsedError.type) ?? '')) return false
  if (rule.match.errorCodes && rule.match.errorCodes.length > 0 && !rule.match.errorCodes.includes(stringValue(context.parsedError.code) ?? '')) return false
  if (rule.match.messageKeywords && rule.match.messageKeywords.length > 0) {
    const message = (stringValue(context.parsedError.message) ?? context.bodyText).toLowerCase()
    if (!rule.match.messageKeywords.every((keyword) => message.includes(keyword.toLowerCase()))) return false
  }
  if (rule.match.bodyKeywords && rule.match.bodyKeywords.length > 0) {
    const bodyText = context.bodyText.toLowerCase()
    if (!rule.match.bodyKeywords.every((keyword) => bodyText.includes(keyword.toLowerCase()))) return false
  }
  return true
}

function normalizeEndpoint(endpoint: string): string {
  const [path] = endpoint.split('?')
  const trimmed = path.trim()
  const methodPath = trimmed.match(/^[A-Z]+\s+(.+)$/)
  return methodPath?.[1] ?? trimmed
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.length > 0 ? value : undefined
}
