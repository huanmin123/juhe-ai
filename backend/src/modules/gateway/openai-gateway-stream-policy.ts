import type {
  StreamInterceptPolicyAccountState,
  StreamInterceptPolicyAccountSwitch,
  StreamInterceptPolicyAction,
  StreamInterceptPolicyDataHandling,
  StreamInterceptPolicyExecutionMode,
  StreamInterceptPolicyMatch,
  StreamInterceptPolicySummary
} from '../../storage/stream-intercept-policy.repository.js'
import {
  actionUsesTtl,
  streamInterceptPolicyActionRuntime
} from '../../storage/stream-intercept-policy.repository.js'
import type { UpstreamAccount } from './openai-gateway-route-helpers.js'
import type { ParsedOpenAIStreamEvent } from './openai-gateway-stream-events.js'

export type StreamInterceptPolicySource = 'system_default' | 'management' | 'account'
export type StreamInterceptRuntimePhase = 'before_downstream_write' | 'after_downstream_write'

export interface RuntimeStreamInterceptPolicy {
  id: string
  source: StreamInterceptPolicySource
  name: string
  enabled: boolean
  action: StreamInterceptPolicyAction
  executionMode: StreamInterceptPolicyExecutionMode
  priority: number
  match: StreamInterceptPolicyMatch
  dataHandling: StreamInterceptPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: StreamInterceptPolicyAccountSwitch
  accountState: StreamInterceptPolicyAccountState
  avoidanceTtlSeconds?: number
}

export interface StreamInterceptPolicyMatchResult {
  policy: RuntimeStreamInterceptPolicy
  matchedField: string
  matchedValue?: string
  phase: StreamInterceptRuntimePhase
  snippet?: string
}

interface ResolveStreamInterceptPoliciesInput {
  account: UpstreamAccount
  managementPolicies?: StreamInterceptPolicySummary[]
}

const textScanMaxEventChars = 64 * 1024

export function resolveRuntimeStreamInterceptPolicies(input: ResolveStreamInterceptPoliciesInput): RuntimeStreamInterceptPolicy[] {
  const management = (input.managementPolicies ?? [])
    .filter((policy) => policy.enabled && policyMatchesProvider(policy, input))
    .map(runtimePolicyFromSummary)
  const accountRules = accountStreamInterceptRules(input.account.credentials)
  return [...management, ...accountRules].sort((left, right) => sourceOrder(left.source) - sourceOrder(right.source) || left.priority - right.priority || left.id.localeCompare(right.id))
}

export function matchRuntimeStreamInterceptPolicy(
  event: ParsedOpenAIStreamEvent,
  policies: RuntimeStreamInterceptPolicy[],
  phase: StreamInterceptRuntimePhase
): StreamInterceptPolicyMatchResult | undefined {
  for (const policy of policies) {
    if (!policy.enabled) continue
    const match = policy.match
    const positive = firstPositiveMatch(event, match)
    if (!positive) continue
    if (textExcluded(event, match)) continue
    return {
      policy,
      phase,
      ...positive
    }
  }
  return undefined
}

function policyMatchesProvider(policy: StreamInterceptPolicySummary, input: ResolveStreamInterceptPoliciesInput): boolean {
  return normalizeComparable(policy.providerCode) === normalizeComparable(input.account.providerCode ?? 'openai')
}

function runtimePolicyFromSummary(policy: StreamInterceptPolicySummary): RuntimeStreamInterceptPolicy {
  const runtime = streamInterceptPolicyActionRuntime(policy.action)
  return {
    id: policy.id,
    source: policy.defaultRule ? 'system_default' : 'management',
    name: policy.name,
    enabled: policy.enabled,
    action: policy.action,
    priority: policy.priority,
    match: policy.match,
    ...runtime,
    avoidanceTtlSeconds: actionUsesTtl(policy.action) ? policy.avoidanceTtlSeconds : undefined
  }
}

function firstPositiveMatch(event: ParsedOpenAIStreamEvent, match: StreamInterceptPolicyMatch): Pick<StreamInterceptPolicyMatchResult, 'matchedField' | 'matchedValue' | 'snippet'> | undefined {
  const matched: Array<Pick<StreamInterceptPolicyMatchResult, 'matchedField' | 'matchedValue' | 'snippet'>> = []
  const eventTypeValues = [event.eventType, event.eventName].filter(Boolean)
  const eventType = findListMatch(match.eventTypes, eventTypeValues)
  if (match.eventTypes?.length && !eventType) return undefined
  if (eventType) matched.push({ matchedField: 'eventType', matchedValue: eventType })
  const dataType = findListMatch(match.dataTypes, [event.eventType])
  if (match.dataTypes?.length && !dataType) return undefined
  if (dataType) matched.push({ matchedField: 'data.type', matchedValue: dataType })
  const errorCode = findListMatch(match.errorCodes, [event.errorCode])
  if (match.errorCodes?.length && !errorCode) return undefined
  if (errorCode) matched.push({ matchedField: 'error.code', matchedValue: errorCode })
  const errorType = findListMatch(match.errorTypes, [stringPath(event.data, ['error', 'type']), stringPath(event.data, ['response', 'error', 'type'])])
  if (match.errorTypes?.length && !errorType) return undefined
  if (errorType) matched.push({ matchedField: 'error.type', matchedValue: errorType })
  const jsonPath = (match.jsonPathsExists ?? []).find((path) => jsonPathExists(event.data, path))
  if (match.jsonPathsExists?.length && !jsonPath) return undefined
  if (jsonPath) matched.push({ matchedField: 'jsonPath', matchedValue: jsonPath })
  const textMatch = findTextIncludesMatch(event, match.textIncludes)
  if (match.textIncludes?.length && !textMatch) return undefined
  if (textMatch) matched.push({ matchedField: 'textIncludes', matchedValue: textMatch.keyword, snippet: textMatch.snippet })
  return matched.find((item) => item.snippet) ?? matched[0]
}

function textExcluded(event: ParsedOpenAIStreamEvent, match: StreamInterceptPolicyMatch): boolean {
  return Boolean(findTextIncludesMatch(event, match.textExcludes))
}

function findListMatch(expected: string[] | undefined, actualValues: Array<string | undefined>): string | undefined {
  if (!expected?.length) return undefined
  const actualSet = new Set(actualValues.map(normalizeComparable).filter((value): value is string => Boolean(value)))
  return expected.find((value) => actualSet.has(normalizeComparable(value) ?? ''))
}

function findTextIncludesMatch(event: ParsedOpenAIStreamEvent, keywords: string[] | undefined): { keyword: string; snippet?: string } | undefined {
  if (!keywords?.length || !event.dataText || event.dataText.length > textScanMaxEventChars || isImageLikeEvent(event)) {
    return undefined
  }
  const haystack = event.dataText
  for (const keyword of keywords) {
    if (!keyword || !haystack.includes(keyword)) continue
    return {
      keyword,
      snippet: snippetAround(haystack, keyword)
    }
  }
  return undefined
}

function snippetAround(text: string, keyword: string): string {
  const index = text.indexOf(keyword)
  if (index < 0) return ''
  const start = Math.max(0, index - 40)
  const end = Math.min(text.length, index + keyword.length + 40)
  return text.slice(start, end)
}

function isImageLikeEvent(event: ParsedOpenAIStreamEvent): boolean {
  const type = event.eventType || event.eventName
  return type.startsWith('response.image_generation_call.')
    || type === 'image_generation.partial_image'
    || type === 'image_generation.completed'
    || type === 'image_generation.failed'
    || event.dataText.includes('partial_image_b64')
    || event.dataText.includes('b64_json')
    || event.dataText.includes('data:image/')
}

function jsonPathExists(value: unknown, path: string): boolean {
  const parts = path.split('.').map((part) => part.trim()).filter(Boolean)
  if (!parts.length) return false
  let current: unknown = value
  for (const part of parts) {
    if (typeof current !== 'object' || current === null || Array.isArray(current)) return false
    if (!Object.prototype.hasOwnProperty.call(current, part)) return false
    current = (current as Record<string, unknown>)[part]
  }
  return hasJsonPathMeaningfulValue(current)
}

function hasJsonPathMeaningfulValue(value: unknown): boolean {
  if (value === null || value === undefined || value === false) return false
  if (typeof value === 'string') return value.trim().length > 0
  if (Array.isArray(value)) return value.length > 0
  if (typeof value === 'object') return hasOwnEnumerableKey(value as Record<string, unknown>)
  return true
}

function hasOwnEnumerableKey(value: Record<string, unknown>): boolean {
  for (const key in value) {
    if (Object.prototype.hasOwnProperty.call(value, key)) return true
  }
  return false
}

function stringPath(value: unknown, path: string[]): string | undefined {
  let current: unknown = value
  for (const part of path) {
    if (typeof current !== 'object' || current === null || Array.isArray(current)) return undefined
    current = (current as Record<string, unknown>)[part]
  }
  return typeof current === 'string' ? current : undefined
}

function accountStreamInterceptRules(credentials: Record<string, unknown>): RuntimeStreamInterceptPolicy[] {
  if (credentials.stream_intercept_rules === undefined) return []
  if (!Array.isArray(credentials.stream_intercept_rules)) {
    throw new Error('账户流式拦截规则格式无效')
  }
  return credentials.stream_intercept_rules.map((item, index) => accountStreamInterceptRule(item, index))
}

function accountStreamInterceptRule(value: unknown, index: number): RuntimeStreamInterceptPolicy {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error(`第 ${index + 1} 条账户流式拦截规则格式无效`)
  }
  const record = value as Record<string, unknown>
  const action = normalizeAction(record.action)
  if (!action) throw new Error(`第 ${index + 1} 条账户流式拦截规则动作无效`)
  const match = normalizeMatch(record.match)
  const runtime = streamInterceptPolicyActionRuntime(action)
  return {
    id: stringValue(record.id) || `account_rule_${index + 1}`,
    source: 'account',
    name: requiredString(record.name, `第 ${index + 1} 条账户流式拦截规则名称`),
    enabled: requiredBoolean(record.enabled, `第 ${index + 1} 条账户流式拦截规则启用状态`),
    action,
    priority: requiredPositiveInt(record.priority, `第 ${index + 1} 条账户流式拦截规则优先级`),
    match,
    ...runtime,
    avoidanceTtlSeconds: actionUsesTtl(action) ? requiredPositiveInt(record.avoidanceTtlSeconds, `第 ${index + 1} 条账户流式拦截规则避让秒数`) : undefined
  }
}

function normalizeMatch(value: unknown): StreamInterceptPolicyMatch {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('账户流式拦截规则匹配条件无效')
  }
  const record = value as Record<string, unknown>
  assertOnlyKeys(record, ['eventTypes', 'dataTypes', 'errorCodes', 'errorTypes', 'textIncludes', 'textExcludes', 'jsonPathsExists'], '账户流式拦截规则匹配条件')
  return {
    eventTypes: textList(record.eventTypes),
    dataTypes: textList(record.dataTypes),
    errorCodes: textList(record.errorCodes),
    errorTypes: textList(record.errorTypes),
    textIncludes: textList(record.textIncludes),
    textExcludes: textList(record.textExcludes),
    jsonPathsExists: textList(record.jsonPathsExists)
  }
}

function normalizeAction(value: unknown): StreamInterceptPolicyAction | undefined {
  if (
    value === 'observe'
    || value === 'drop_event'
    || value === 'fail_stream'
    || value === 'retry_no_avoidance'
    || value === 'retry_next_account'
    || value === 'avoid_account_ttl'
    || value === 'avoid_upstream_bucket_ttl'
  ) {
    return value
  }
  return undefined
}

function textList(value: unknown): string[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) {
    throw new Error('账户流式拦截规则匹配条件必须是字符串数组')
  }
  if (value.length > 50) {
    throw new Error('账户流式拦截规则匹配条件不能超过 50 项')
  }
  const output = value.map((item) => {
    const text = requiredString(item, '账户流式拦截规则匹配条件')
    if (text.length > 200) {
      throw new Error('账户流式拦截规则匹配条件不能超过 200 个字符')
    }
    return text
  })
  return output.length ? output : undefined
}

function sourceOrder(source: StreamInterceptPolicySource): number {
  if (source === 'account') return 0
  if (source === 'management') return 1
  return 2
}


function normalizeComparable(value: string | undefined): string | undefined {
  const text = value?.trim()
  return text ? text : undefined
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function requiredBoolean(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') throw new Error(`${label}必须是布尔值`)
  return value
}

function requiredPositiveInt(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value <= 0) {
    throw new Error(`${label}必须是大于 0 的整数`)
  }
  return value
}

function requiredString(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) {
    throw new Error(`${label}不能为空`)
  }
  return value.trim()
}

function assertOnlyKeys(value: Record<string, unknown>, allowedKeys: readonly string[], label: string): void {
  const allowed = new Set(allowedKeys)
  const unexpected = Object.keys(value).find((key) => !allowed.has(key))
  if (unexpected) {
    throw new Error(`${label}包含不支持字段：${unexpected}`)
  }
}
