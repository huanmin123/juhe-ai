import type {
  ProviderDefinition,
  ProviderModelApiProtocol,
  ProviderModelMode,
  ProviderModelPricing,
  ProviderModelReasoningEffort,
  ProviderModelServiceTier,
  ProviderModelStatus
} from '@/types/domain'
import {
  categoryFromModeOrModel,
  getModelCategoryFromPricing,
  modelCategoryLabels,
  modelCategoryOrder,
  type ModelCategoryKey
} from './providerModelCategoryRules'

export type DirectPriceFieldKey =
  | 'inputUsdPer1M'
  | 'outputUsdPer1M'
  | 'cachedInputUsdPer1M'
  | 'cacheWriteUsdPer1M'
  | 'cacheWrite1hUsdPer1M'
  | 'imageInputUsdPer1M'
  | 'imageOutputUsdPer1M'
  | 'audioInputUsdPer1M'
  | 'audioOutputUsdPer1M'
  | 'outputUsdPerImage'

export {
  categoryFromModeOrModel,
  isModelCategoryKey,
  modelCategoryLabels,
  modelCategoryOrder,
  type ModelCategoryKey
} from './providerModelCategoryRules'

export const apiProtocolLabels: Record<string, string> = {
  chat_completions: 'Chat Completions',
  responses: 'Responses',
  messages: 'Messages',
  message_token_counting: 'Message Token Counting',
  completions: 'Completions',
  images: 'Images API',
  audio: 'Audio API',
  realtime: 'Realtime API'
}

export const modelStatusOptions: Array<{ label: string; value: ProviderModelStatus }> = [
  { label: '启用', value: 'active' },
  { label: '草稿', value: 'draft' },
  { label: '停用', value: 'disabled' }
]

export const modelModeOptions: Array<{ label: string; value: ProviderModelMode }> = [
  { label: '对话 / 编码', value: 'text' },
  { label: '图像', value: 'image' },
  { label: '音频', value: 'audio' }
]

export const apiProtocolOptions: Array<{ label: string; value: ProviderModelApiProtocol }> = Object.entries(apiProtocolLabels)
  .map(([value, label]) => ({ value: value as ProviderModelApiProtocol, label }))

export const directPriceFieldKeys: DirectPriceFieldKey[] = [
  'inputUsdPer1M',
  'outputUsdPer1M',
  'cachedInputUsdPer1M',
  'cacheWriteUsdPer1M',
  'cacheWrite1hUsdPer1M',
  'imageInputUsdPer1M',
  'imageOutputUsdPer1M',
  'audioInputUsdPer1M',
  'audioOutputUsdPer1M',
  'outputUsdPerImage'
]

export const directPriceFieldsByCategory: Record<ModelCategoryKey, DirectPriceFieldKey[]> = {
  text: ['inputUsdPer1M', 'outputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M', 'cacheWrite1hUsdPer1M'],
  image: ['imageInputUsdPer1M', 'imageOutputUsdPer1M', 'outputUsdPerImage'],
  audio: ['audioInputUsdPer1M', 'audioOutputUsdPer1M']
}

const hiddenProviderCapabilities = new Set(['models', 'passthrough', 'stream'])

const providerCapabilityLabels: Record<string, string> = {
  responses: 'Responses',
  chat: 'Chat',
  chat_completions: 'Chat',
  messages: 'Messages',
  count_tokens: 'Count Tokens'
}

const providerCapabilityOrder = ['responses', 'chat', 'messages', 'count_tokens'] as const

export function hasAnyNumber(...values: Array<number | undefined>): boolean {
  return values.some((value) => typeof value === 'number')
}

export function hasDirectModelPrice(item: ProviderModelPricing): boolean {
  return hasAnyNumber(
    item.inputUsdPer1M,
    item.outputUsdPer1M,
    item.cachedInputUsdPer1M,
    item.cacheWriteUsdPer1M,
    item.cacheWrite1hUsdPer1M,
    item.imageInputUsdPer1M,
    item.imageOutputUsdPer1M,
    item.audioInputUsdPer1M,
    item.audioOutputUsdPer1M,
    item.outputUsdPerImage
  )
}

export function defaultProtocolsForModelCategory(category: ModelCategoryKey): ProviderModelApiProtocol[] {
  if (category === 'image') return ['images']
  if (category === 'audio') return ['audio']
  return ['responses', 'chat_completions']
}

export function defaultProtocolsForProviderModelCategory(
  provider: ProviderDefinition | undefined,
  category: ModelCategoryKey
): ProviderModelApiProtocol[] {
  const protocols = providerProfileApiProtocolsForModelCategory(provider, category)
  return protocols.length ? protocols : defaultProtocolsForModelCategory(category)
}

export function findFirstModelCategory(models: ProviderModelPricing[]): ModelCategoryKey {
  for (const key of modelCategoryOrder) {
    if (models.some((item) => getModelCategory(item) === key)) {
      return key
    }
  }
  return 'text'
}

export function getModelCategory(item: ProviderModelPricing): ModelCategoryKey {
  return getModelCategoryFromPricing(item)
}

export function formatModelCategory(item: ProviderModelPricing): string {
  return modelCategoryLabels[getModelCategory(item)]
}

export function formatModelScope(scope?: string): string {
  if (scope === 'built_in') return '内置'
  if (scope === 'global') return '全局'
  if (scope === 'personal') return '个人'
  return '-'
}

export function modelScopeColor(scope?: string): string {
  if (scope === 'built_in') return 'blue'
  if (scope === 'global') return 'purple'
  if (scope === 'personal') return 'green'
  return 'default'
}

export function formatModelStatus(status?: string): string {
  if (status === 'active') return '启用'
  if (status === 'draft') return '草稿'
  if (status === 'disabled') return '停用'
  return '-'
}

export function modelStatusColor(status?: string): string {
  if (status === 'active') return 'green'
  if (status === 'draft') return 'gold'
  if (status === 'disabled') return 'default'
  return 'default'
}

export function formatApiProtocol(protocol?: string): string {
  return apiProtocolLabels[protocol ?? ''] ?? protocol ?? '-'
}

export function formatModelServiceTier(value: ProviderModelServiceTier): string {
  return value === 'priority' ? 'Priority' : 'Flex'
}

export function formatModelReasoningEffort(value: ProviderModelReasoningEffort | 'ultra'): string {
  if (value === 'none') return '关闭'
  if (value === 'minimal') return 'Minimal'
  if (value === 'low') return 'Low'
  if (value === 'medium') return 'Medium'
  if (value === 'high') return 'High'
  if (value === 'xhigh') return 'XHigh'
  if (value === 'max') return 'Max'
  return 'Ultra'
}

export function formatModelReasoningCapabilities(item: ProviderModelPricing): string {
  return item.supportedReasoningEfforts?.length
    ? item.supportedReasoningEfforts.map(formatModelReasoningEffort).join(' / ')
    : '-'
}

export function formatModelRequestCapabilities(item: ProviderModelPricing): string {
  const parts: string[] = []
  if (item.supportedServiceTiers?.length) {
    parts.push(`服务等级 ${item.supportedServiceTiers.map(formatModelServiceTier).join(' / ')}`)
  }
  const reasoningCapabilities = formatModelReasoningCapabilities(item)
  if (reasoningCapabilities !== '-') parts.push(reasoningCapabilities)
  return parts.join('；') || '-'
}

export function getApiProtocolTagColor(protocol?: string): string {
  switch (protocol) {
    case 'chat_completions':
      return 'blue'
    case 'responses':
      return 'purple'
    case 'messages':
      return 'green'
    case 'message_token_counting':
      return 'lime'
    case 'images':
      return 'cyan'
    case 'audio':
      return 'green'
    case 'realtime':
      return 'orange'
    default:
      return 'default'
  }
}

export function visibleProviderCapabilities(capabilities: string[]): string[] {
  const normalized = new Set<string>()
  for (const capability of capabilities) {
    if (capability === 'chat_completions' || capability === 'passthrough') {
      normalized.add('chat')
      continue
    }
    if (!hiddenProviderCapabilities.has(capability)) {
      normalized.add(capability)
    }
  }
  return [...normalized].sort((left, right) => {
    const leftIndex = providerCapabilityOrder.indexOf(left as typeof providerCapabilityOrder[number])
    const rightIndex = providerCapabilityOrder.indexOf(right as typeof providerCapabilityOrder[number])
    if (leftIndex !== -1 || rightIndex !== -1) {
      return (leftIndex === -1 ? providerCapabilityOrder.length : leftIndex) - (rightIndex === -1 ? providerCapabilityOrder.length : rightIndex)
    }
    return left.localeCompare(right)
  })
}

export function formatProviderCapability(capability: string): string {
  return providerCapabilityLabels[capability] ?? capability
}

export function formatCapabilitiesSummary(capabilities: string[]): string {
  const visibleCapabilities = visibleProviderCapabilities(capabilities)
  return visibleCapabilities.length ? visibleCapabilities.map(formatProviderCapability).join(' / ') : '-'
}

function providerProfileApiProtocolsForModelCategory(
  provider: ProviderDefinition | undefined,
  category: ModelCategoryKey
): ProviderModelApiProtocol[] {
  if (!provider) return []
  const protocols = new Set<ProviderModelApiProtocol>()
  for (const profile of preferredProviderProfiles(provider)) {
    for (const family of profile.endpointFamilies ?? []) {
      const protocol = apiProtocolForEndpointFamily(family.code)
      if (protocol && apiProtocolMatchesModelCategory(protocol, category)) {
        protocols.add(protocol)
      }
    }
  }
  return [...protocols]
}

function preferredProviderProfiles(provider: ProviderDefinition): ProviderDefinition['protocolProfiles'] {
  const defaultProfile = provider.protocolProfiles.find((profile) => profile.id === provider.defaultProtocolProfileId)
  const remainingProfiles = provider.protocolProfiles.filter((profile) => profile !== defaultProfile)
  return defaultProfile ? [defaultProfile, ...remainingProfiles] : remainingProfiles
}

function apiProtocolForEndpointFamily(code: string): ProviderModelApiProtocol | undefined {
  return isProviderModelApiProtocol(code) ? code : undefined
}

function apiProtocolMatchesModelCategory(protocol: ProviderModelApiProtocol, category: ModelCategoryKey): boolean {
  if (category === 'image') return protocol === 'images'
  if (category === 'audio') return protocol === 'audio'
  return protocol === 'responses'
    || protocol === 'chat_completions'
    || protocol === 'messages'
    || protocol === 'message_token_counting'
    || protocol === 'completions'
}

function isProviderModelApiProtocol(value: string): value is ProviderModelApiProtocol {
  return Object.prototype.hasOwnProperty.call(apiProtocolLabels, value)
}

export function formatPrice(value?: number): string {
  return typeof value === 'number' ? `$${trimNumber(value)}` : '-'
}

export function formatUnitPrice(value?: number): string {
  return typeof value === 'number' ? `$${trimNumber(value)}` : '-'
}

export function formatModelPriceSummary(item: ProviderModelPricing): string {
  const category = getModelCategory(item)
  if (category === 'image') {
    return [
      `图片输入 ${formatPrice(item.imageInputUsdPer1M)}`,
      `图片输出 ${formatPrice(item.imageOutputUsdPer1M)}`,
      `每张 ${formatUnitPrice(item.outputUsdPerImage)}`
    ].join(' / ')
  }
  if (category === 'audio') {
    return [
      `音频输入 ${formatPrice(item.audioInputUsdPer1M)}`,
      `音频输出 ${formatPrice(item.audioOutputUsdPer1M)}`
    ].join(' / ')
  }
  return [
    `输入 ${formatPrice(item.inputUsdPer1M)}`,
    `输出 ${formatPrice(item.outputUsdPer1M)}`,
    `缓存读 ${formatPrice(item.cachedInputUsdPer1M)}`
  ].join(' / ')
}

export function formatTokens(value?: number): string {
  if (typeof value !== 'number') return '-'
  if (value >= 1_000_000) return `${trimNumber(value / 1_000_000)}M`
  if (value >= 1_000) return `${trimNumber(value / 1_000)}K`
  return String(value)
}

export function formatModelInputTokens(item: ProviderModelPricing): string {
  return formatTokens(item.maxInputTokens ?? item.contextWindowTokens)
}

export function trimNumber(value: number): string {
  return Number(value.toFixed(8)).toString()
}
