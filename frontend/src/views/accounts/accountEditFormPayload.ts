import type {
  AccountSummary,
  AccountType,
  ProviderProtocolProfileDefinition,
  ProviderModelApiProtocol,
  ProviderModelPricing,
  ProviderModelReasoningEffort,
  ProviderModelServiceTier
} from '@/types/domain'
import { asString } from './accountBasicFormatters'
import { accountProviderProtocolKind, endpointModesForProfile } from './accountProviderCapabilities'
import type { AccountFormModel } from './accountFormTypes'

export interface AccountModelSelectOption {
  label: string
  value: string
  supportedApiProtocols?: ProviderModelApiProtocol[]
  supportedServiceTiers?: ProviderModelServiceTier[]
  supportedReasoningEfforts?: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort | null
}

export function providerModelsToOptions(models: ProviderModelPricing[]): AccountModelSelectOption[] {
  return models.map((item) => ({
    label: item.model,
    value: item.model,
    supportedApiProtocols: item.supportedApiProtocols,
    supportedServiceTiers: item.supportedServiceTiers,
    supportedReasoningEfforts: item.supportedReasoningEfforts,
    defaultReasoningEffort: item.defaultReasoningEffort
  }))
}

export function providerModelsForProtocolProfile(
  models: AccountModelSelectOption[],
  profile?: ProviderProtocolProfileDefinition,
  accountType?: AccountType
): AccountModelSelectOption[] {
  // v8-C：仅保留协议层过滤（域 mode 过滤之后的协议正确性）。gpt api_key 追加
  // 'images' 的分支已随对话域纯粹化（v8-A）删除——对话账户不再经此放行
  // image-only 模型，图像域账户由域 mode 过滤 + 媒体域 token 过滤承担。
  const allowedProtocols = providerProfileModelProtocols(profile, accountType)
  if (!allowedProtocols.size) return models
  return models.filter((item) => {
    const protocols = item.supportedApiProtocols ?? []
    return protocols.length === 0 || protocols.some((protocol) => allowedProtocols.has(protocol))
  })
}

function providerProfileModelProtocols(
  profile?: ProviderProtocolProfileDefinition,
  accountType?: AccountType
): Set<ProviderModelApiProtocol> {
  const endpointProtocols = (profile?.endpointFamilies ?? [])
    .map((family) => typeof family === 'string' ? family : family.code)
    .filter((value): value is ProviderModelApiProtocol => Boolean(value))
  if (endpointProtocols.length) {
    // OpenAI OAuth 的写侧词表只有 Responses 对（后端
    // openAIFamilyVocabulary("oauth")）：共用 profile 的 endpointFamilies 仍含
    // chat，但 OAuth 账户不能保存 chat mode，模型下拉也必须收敛到 responses，
    // 避免把 chat-only 模型展示成可调度支持模型。
    if (accountType === 'oauth' && accountProviderProtocolKind(profile) === 'openai_v1') {
      return new Set(['responses'])
    }
    const protocols = new Set(endpointProtocols)
    // 2026-11-20 向量/重排协议补齐批：endpointFamilies 只表达 chat/responses/
    // 媒体族，向量的 OpenAI 兼容面与 qwen 重排面不在族表内。按档案可声明的
    // endpoint mode 反推协议，而不是只对 qwen 硬编码——gpt/deepseek/xai 的自定义
    // embedding 模型也必须在账户模型下拉中可见；glm、volcengine 与 gemini
    // OpenAI Chat 窄 preset 的词表不含 embeddings_json，自然不会放行
    // embeddings 协议。
    const profileModes = new Set(endpointModesForProfile(profile, accountType))
    if (profileModes.has('embeddings_json')) protocols.add('embeddings')
    if (profileModes.has('rerank_json')) protocols.add('rerank')
    return protocols
  }
  if (profile?.protocolCode === 'openai') return new Set(['chat_completions', 'responses'])
  if (profile?.protocolCode === 'anthropic') return new Set(['messages', 'message_token_counting'])
  if (profile?.protocolCode === 'gemini') {
    return new Set(['generate_content', 'stream_generate_content', 'count_tokens', 'embed_content', 'interactions'])
  }
  return new Set()
}

export function cloneAccountName(name: string): string {
  const trimmed = name.trim()
  return trimmed ? `${trimmed} - 克隆` : ''
}

export function cloneAccountModelMappings(value: AccountSummary['modelMappings']): AccountFormModel['modelMappings'] {
  return (value ?? []).map((item) => ({ ...item }))
}

export function accountTagNames(value: AccountSummary['tags']): string[] {
  return (value ?? []).map((tag) => tag.name).filter(Boolean)
}

export function sameTagNames(left: string[], right: AccountSummary['tags']): boolean {
  return stableTagKey(normalizeFormTagNames(left)) === stableTagKey(accountTagNames(right))
}

export function normalizeFormTagNames(value: string[]): string[] {
  const output: string[] = []
  const seen = new Set<string>()
  for (const item of value ?? []) {
    const name = item.replace(/\s+/g, ' ').trim()
    if (!name) continue
    const key = name
    if (seen.has(key)) continue
    seen.add(key)
    output.push(name)
  }
  return output
}

function stableTagKey(value: string[]): string {
  return normalizeFormTagNames(value).sort().join('\n')
}

export function accountApiKeysForForm(credentials: Record<string, unknown>): string[] {
  const values = Array.isArray(credentials.api_keys)
    ? credentials.api_keys
    : [credentials.api_key]
  const keys = values.map((value) => asString(value) ?? '').filter(Boolean)
  return keys.length ? keys : ['']
}

export function accountApiKeyWeightsForForm(credentials: Record<string, unknown>): number[] {
  const keys = accountApiKeysForForm(credentials)
  const rawWeights = Array.isArray(credentials.api_key_weights) ? credentials.api_key_weights : []
  return keys.map((_, index) => {
    const value = Number(rawWeights[index] ?? 1)
    return Number.isInteger(value) ? Math.min(100, Math.max(1, value)) : 1
  })
}
