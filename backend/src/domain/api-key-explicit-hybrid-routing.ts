import type {
  AccountModelMappingSourceEndpointFamily,
  AccountModelMappingUpstreamEndpointFamily,
  ApiKeyClientProfile,
  ApiKeyExplicitHybridRouteAdapterMode,
  ApiKeyExplicitHybridRouteRule
} from './types.js'
import {
  ANTHROPIC_MESSAGES_FAMILY,
  GEMINI_GENERATE_CONTENT_FAMILY,
  GEMINI_STREAM_GENERATE_CONTENT_FAMILY,
  OPENAI_CHAT_COMPLETIONS_FAMILY,
  OPENAI_RESPONSES_FAMILY
} from './provider-protocol.js'

export const DEFAULT_API_KEY_CLIENT_PROFILE: ApiKeyClientProfile = 'auto'
const EXPLICIT_HYBRID_ROUTE_RULE_MAX_COUNT = 100

export function normalizeApiKeyClientProfile(value: unknown, fallback: ApiKeyClientProfile = DEFAULT_API_KEY_CLIENT_PROFILE): ApiKeyClientProfile {
  if (value === undefined || value === null || value === '') return fallback
  if (
    value === 'auto'
    || value === 'generic_openai'
    || value === 'codex'
    || value === 'generic_anthropic'
    || value === 'claude_code'
    || value === 'generic_gemini'
    || value === 'gemini_cli'
  ) {
    return value
  }
  throw new Error('API Key 默认客户端画像无效')
}

export function parseExplicitHybridRouteRulesJson(value: string | null | undefined): ApiKeyExplicitHybridRouteRule[] | undefined {
  if (!value) return undefined
  try {
    return normalizeExplicitHybridRouteRules(JSON.parse(value))
  } catch (error) {
    if (error instanceof Error) throw error
    throw new Error('显式混合路由规则无效')
  }
}

export function explicitHybridRouteRulesJson(value: ApiKeyExplicitHybridRouteRule[] | undefined): string | null {
  return value?.length ? JSON.stringify(normalizeExplicitHybridRouteRules(value)) : null
}

export function normalizeExplicitHybridRouteRules(value: unknown): ApiKeyExplicitHybridRouteRule[] | undefined {
  if (value === undefined || value === null || value === '') return undefined
  if (!Array.isArray(value)) {
    throw new Error('显式混合路由规则必须是数组')
  }
  if (value.length > EXPLICIT_HYBRID_ROUTE_RULE_MAX_COUNT) {
    throw new Error(`显式混合路由规则最多 ${EXPLICIT_HYBRID_ROUTE_RULE_MAX_COUNT} 条`)
  }

  const output = value
    .map((item, index) => normalizeExplicitHybridRouteRule(item, index))
    .sort((left, right) => left.priority - right.priority || left.id.localeCompare(right.id))
  const seenIds = new Set<string>()
  for (const rule of output) {
    if (seenIds.has(rule.id)) {
      throw new Error(`显式混合路由规则 ID 不能重复：${rule.id}`)
    }
    seenIds.add(rule.id)
  }
  return output.length ? output : undefined
}

export function explicitHybridRouteEndpointFamilyConversionAllowed(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): boolean {
  if (sourceEndpointFamily === upstreamEndpointFamily) return true
  if (sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY && upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY) return true
  if (sourceEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY) {
    return upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
      || upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
  }
  if (sourceEndpointFamily === OPENAI_RESPONSES_FAMILY) {
    return upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
      || upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
      || upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
  }
  if (sourceEndpointFamily === ANTHROPIC_MESSAGES_FAMILY) {
    return upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
      || upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY
  }
  if (sourceEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY || sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) {
    return upstreamEndpointFamily === OPENAI_CHAT_COMPLETIONS_FAMILY
      || upstreamEndpointFamily === ANTHROPIC_MESSAGES_FAMILY
  }
  return false
}

export function assertExplicitHybridRouteEndpointFamilyConversionAllowed(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): void {
  if (explicitHybridRouteEndpointFamilyConversionAllowed(sourceEndpointFamily, upstreamEndpointFamily)) {
    return
  }
  throw new Error(`显式混合路由暂不支持 ${endpointFamilyLabel(sourceEndpointFamily)} 到 ${endpointFamilyLabel(upstreamEndpointFamily)} 的协议转换`)
}

function normalizeExplicitHybridRouteRule(value: unknown, index: number): ApiKeyExplicitHybridRouteRule {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('显式混合路由规则项必须是对象')
  }
  const record = value as Record<string, unknown>
  const enabled = record.enabled === undefined ? true : booleanValue(record.enabled, '显式混合路由规则启用状态必须是布尔值')
  const sourceEndpointFamily = sourceEndpointFamilyValue(record.sourceEndpointFamily)
  const upstreamEndpointFamily = upstreamEndpointFamilyValue(record.upstreamEndpointFamily)
  assertExplicitHybridRouteEndpointFamilyConversionAllowed(sourceEndpointFamily, upstreamEndpointFamily)
  const adapterMode = adapterModeValue(record.adapterMode)
  if (adapterMode === 'direct' && !directEndpointFamilyConversionAllowed(sourceEndpointFamily, upstreamEndpointFamily)) {
    throw new Error('显式混合路由直连模式只能用于同协议模型别名')
  }
  return {
    id: optionalString(record.id) ?? `rule_${index + 1}`,
    enabled,
    priority: integerValue(record.priority, index + 1, 1, 10000, '显式混合路由规则优先级必须是 1-10000 的整数'),
    sourceClientProfile: normalizeApiKeyClientProfile(record.sourceClientProfile),
    sourceEndpointFamily,
    ...(optionalString(record.sourceModel) ? { sourceModel: optionalString(record.sourceModel) } : {}),
    targetGroupId: requiredString(record.targetGroupId, '显式混合路由目标分组不能为空'),
    ...(optionalString(record.targetAccountId) ? { targetAccountId: optionalString(record.targetAccountId) } : {}),
    ...(optionalString(record.targetProviderProtocolProfileId) ? { targetProviderProtocolProfileId: optionalString(record.targetProviderProtocolProfileId) } : {}),
    upstreamEndpointFamily,
    upstreamModel: requiredString(record.upstreamModel, '显式混合路由上游模型不能为空'),
    adapterMode
  }
}

function directEndpointFamilyConversionAllowed(
  sourceEndpointFamily: AccountModelMappingSourceEndpointFamily,
  upstreamEndpointFamily: AccountModelMappingUpstreamEndpointFamily
): boolean {
  return sourceEndpointFamily === upstreamEndpointFamily
    || (sourceEndpointFamily === GEMINI_STREAM_GENERATE_CONTENT_FAMILY && upstreamEndpointFamily === GEMINI_GENERATE_CONTENT_FAMILY)
}

function sourceEndpointFamilyValue(value: unknown): AccountModelMappingSourceEndpointFamily {
  if (
    value === OPENAI_CHAT_COMPLETIONS_FAMILY
    || value === OPENAI_RESPONSES_FAMILY
    || value === ANTHROPIC_MESSAGES_FAMILY
    || value === GEMINI_GENERATE_CONTENT_FAMILY
    || value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY
  ) {
    return value
  }
  throw new Error('显式混合路由下游协议无效')
}

function upstreamEndpointFamilyValue(value: unknown): AccountModelMappingUpstreamEndpointFamily {
  if (
    value === OPENAI_CHAT_COMPLETIONS_FAMILY
    || value === OPENAI_RESPONSES_FAMILY
    || value === ANTHROPIC_MESSAGES_FAMILY
    || value === GEMINI_GENERATE_CONTENT_FAMILY
  ) {
    return value
  }
  throw new Error('显式混合路由上游协议无效')
}

function adapterModeValue(value: unknown): ApiKeyExplicitHybridRouteAdapterMode {
  if (value === undefined || value === null || value === '') return 'bridge'
  if (value === 'direct' || value === 'bridge') return value
  throw new Error('显式混合路由适配模式无效')
}

function requiredString(value: unknown, message: string): string {
  const text = optionalString(value)
  if (!text) throw new Error(message)
  return text
}

function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function booleanValue(value: unknown, message: string): boolean {
  if (typeof value !== 'boolean') throw new Error(message)
  return value
}

function integerValue(value: unknown, fallback: number, min: number, max: number, message: string): number {
  if (value === undefined || value === null || value === '') return fallback
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isInteger(numeric) || numeric < min || numeric > max) {
    throw new Error(message)
  }
  return numeric
}

function endpointFamilyLabel(value: AccountModelMappingSourceEndpointFamily | AccountModelMappingUpstreamEndpointFamily): string {
  if (value === OPENAI_RESPONSES_FAMILY) return 'Responses'
  if (value === ANTHROPIC_MESSAGES_FAMILY) return 'Messages'
  if (value === GEMINI_GENERATE_CONTENT_FAMILY) return 'Gemini GenerateContent'
  if (value === GEMINI_STREAM_GENERATE_CONTENT_FAMILY) return 'Gemini StreamGenerateContent'
  return 'Chat Completions'
}
