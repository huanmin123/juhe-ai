import type {
  AccountType,
  AccountSummary,
  ProviderProtocolProfileDefinition,
  ProviderModelApiProtocol,
  ProviderModelPricing,
  ProviderModelReasoningEffort,
  ProviderModelServiceTier
} from '@/types/domain'
import { isGptVendorCode } from '@/shared/providerProtocol'
import { asString } from './accountBasicFormatters'
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
  const allowedProtocols = providerProfileModelProtocols(profile)
  if (accountType === 'api_key' && isGptVendorCode(profile?.providerCode)) {
    allowedProtocols.add('images')
  }
  if (!allowedProtocols.size) return models
  return models.filter((item) => {
    const protocols = item.supportedApiProtocols ?? []
    return protocols.length === 0 || protocols.some((protocol) => allowedProtocols.has(protocol))
  })
}

function providerProfileModelProtocols(profile?: ProviderProtocolProfileDefinition): Set<ProviderModelApiProtocol> {
  const endpointProtocols = (profile?.endpointFamilies ?? [])
    .map((family) => typeof family === 'string' ? family : family.code)
    .filter((value): value is ProviderModelApiProtocol => Boolean(value))
  if (endpointProtocols.length) return new Set(endpointProtocols)
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
