import type {
  ProviderDefinition,
  ProviderModelApiProtocol,
  ProviderModelCatalogDisplayItem,
  ProviderModelCatalogDisplaySection,
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
  | 'cacheStorageUsdPer1MPerHour'
  | 'imageInputUsdPer1M'
  | 'imageOutputUsdPer1M'
  | 'audioInputUsdPer1M'
  | 'audioOutputUsdPer1M'
  | 'outputUsdPerImage'

export interface ModelPriceFieldDefinition {
  key: DirectPriceFieldKey
  label: string
  description: string
}

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
  generate_content: 'Generate Content',
  stream_generate_content: 'Stream Generate Content',
  count_tokens: 'Count Tokens',
  embed_content: 'Embed Content',
  interactions: 'Interactions',
  completions: 'Completions',
  images: 'Images API'
}

export const modelStatusOptions: Array<{ label: string; value: ProviderModelStatus }> = [
  { label: '启用', value: 'active' },
  { label: '草稿', value: 'draft' },
  { label: '停用', value: 'disabled' }
]

export const modelModeOptions: Array<{ label: string; value: ProviderModelMode }> = [
  { label: '对话 / 编码', value: 'text' },
  { label: '图像', value: 'image' }
]

export const apiProtocolOptions: Array<{ label: string; value: ProviderModelApiProtocol }> = Object.entries(apiProtocolLabels)
  .map(([value, label]) => ({ value: value as ProviderModelApiProtocol, label }))

export const directPriceFieldKeys: DirectPriceFieldKey[] = [
  'inputUsdPer1M',
  'outputUsdPer1M',
  'cachedInputUsdPer1M',
  'cacheWriteUsdPer1M',
  'cacheWrite1hUsdPer1M',
  'cacheStorageUsdPer1MPerHour',
  'imageInputUsdPer1M',
  'imageOutputUsdPer1M',
  'audioInputUsdPer1M',
  'audioOutputUsdPer1M',
  'outputUsdPerImage'
]

export const directPriceFieldsByCategory: Record<ModelCategoryKey, DirectPriceFieldKey[]> = {
  text: ['inputUsdPer1M', 'outputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M', 'cacheWrite1hUsdPer1M', 'cacheStorageUsdPer1MPerHour', 'audioInputUsdPer1M'],
  image: ['imageInputUsdPer1M', 'imageOutputUsdPer1M', 'outputUsdPerImage']
}

const priceFieldDefinitions: Record<DirectPriceFieldKey, ModelPriceFieldDefinition> = {
  inputUsdPer1M: { key: 'inputUsdPer1M', label: '输入', description: '每 100 万输入 Token 的美元价格。' },
  outputUsdPer1M: { key: 'outputUsdPer1M', label: '输出', description: '每 100 万输出 Token 的美元价格。' },
  cachedInputUsdPer1M: { key: 'cachedInputUsdPer1M', label: '缓存读', description: '每 100 万缓存读取 Token 的美元价格。' },
  cacheWriteUsdPer1M: { key: 'cacheWriteUsdPer1M', label: '缓存写入', description: '每 100 万缓存写入 Token 的美元价格；供应商未单独收费时留空。' },
  cacheWrite1hUsdPer1M: { key: 'cacheWrite1hUsdPer1M', label: '1h 缓存写入', description: '每 100 万写入并保留 1 小时的缓存 Token 美元价格。' },
  cacheStorageUsdPer1MPerHour: { key: 'cacheStorageUsdPer1MPerHour', label: '缓存存储', description: '每 100 万缓存 Token、每小时的美元存储价格。' },
  imageInputUsdPer1M: { key: 'imageInputUsdPer1M', label: '图片输入', description: '每 100 万图片输入 Token 的美元价格。' },
  imageOutputUsdPer1M: { key: 'imageOutputUsdPer1M', label: '图片输出', description: '每 100 万图片输出 Token 的美元价格。' },
  audioInputUsdPer1M: { key: 'audioInputUsdPer1M', label: '音频输入', description: '每 100 万音频输入 Token 的美元价格。' },
  audioOutputUsdPer1M: { key: 'audioOutputUsdPer1M', label: '音频输出', description: '每 100 万音频输出 Token 的美元价格。' },
  outputUsdPerImage: { key: 'outputUsdPerImage', label: '每张图片', description: '每生成一张图片的美元价格。' }
}

const textPriceFieldsByProvider: Record<string, DirectPriceFieldKey[]> = {
  anthropic: ['inputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M', 'cacheWrite1hUsdPer1M', 'outputUsdPer1M'],
  deepseek: ['inputUsdPer1M', 'cachedInputUsdPer1M', 'outputUsdPer1M'],
  gemini: ['inputUsdPer1M', 'cachedInputUsdPer1M', 'cacheStorageUsdPer1MPerHour', 'audioInputUsdPer1M', 'outputUsdPer1M'],
  glm: ['inputUsdPer1M', 'cachedInputUsdPer1M', 'outputUsdPer1M'],
  gpt: ['inputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M', 'outputUsdPer1M'],
  openai: ['inputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M', 'outputUsdPer1M'],
  xai: ['inputUsdPer1M', 'cachedInputUsdPer1M', 'outputUsdPer1M']
}

export function customModelPriceFields(providerCode: string, category: ModelCategoryKey): ModelPriceFieldDefinition[] {
  const code = providerCode.trim().toLowerCase()
  const keys = category === 'text'
    ? textPriceFieldsByProvider[code] ?? directPriceFieldsByCategory.text
    : directPriceFieldsByCategory[category]
  return keys.map((key) => {
    if (code === 'anthropic' && key === 'cacheWriteUsdPer1M') {
      return { ...priceFieldDefinitions[key], label: '5m 缓存写入', description: '每 100 万写入并保留 5 分钟的缓存 Token 美元价格。' }
    }
    return priceFieldDefinitions[key]
  })
}

export function customModelTierPriceFields(providerCode: string): ModelPriceFieldDefinition[] {
  return customModelPriceFields(providerCode, 'text')
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
    item.cacheStorageUsdPer1MPerHour,
    item.imageInputUsdPer1M,
    item.imageOutputUsdPer1M,
    item.audioInputUsdPer1M,
    item.audioOutputUsdPer1M,
    item.outputUsdPerImage
  )
}

export function defaultProtocolsForModelCategory(category: ModelCategoryKey): ProviderModelApiProtocol[] {
  if (category === 'image') return ['images']
  return ['responses', 'chat_completions']
}

export function defaultProtocolsForProviderModelCategory(
  provider: ProviderDefinition | undefined,
  category: ModelCategoryKey
): ProviderModelApiProtocol[] {
  const protocols = providerProfileApiProtocolsForModelCategory(provider, category)
  if (protocols.length) return protocols
  const protocolCode = provider?.protocolCode?.trim().toLowerCase()
  if (category === 'text' && protocolCode === 'anthropic') return ['messages', 'message_token_counting']
  if (category === 'text' && protocolCode === 'gemini') {
    return ['generate_content', 'stream_generate_content', 'count_tokens', 'embed_content', 'interactions']
  }
  return defaultProtocolsForModelCategory(category)
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
  if (value === 'priority') return 'Priority'
  if (value === 'flex') return 'Flex'
  return value
}

export function formatModelReasoningEffort(value: ProviderModelReasoningEffort | 'ultra'): string {
  if (value === 'none') return '关闭'
  if (value === 'minimal') return 'Minimal'
  if (value === 'low') return 'Low'
  if (value === 'medium') return 'Medium'
  if (value === 'high') return 'High'
  if (value === 'xhigh') return 'XHigh'
  if (value === 'max') return 'Max'
  if (value === 'ultra') return 'Ultra'
  return value
}

export function formatModelReasoningCapabilities(item: ProviderModelPricing): string {
  if (!item.supportedReasoningEfforts?.length) return '不支持'
  return item.supportedReasoningEfforts.map(formatModelReasoningEffort).join(' / ')
}

export function formatModelServiceTierCapabilities(item: ProviderModelPricing): string {
  return item.supportedServiceTiers?.length
    ? item.supportedServiceTiers.map(formatModelServiceTier).join(' / ')
    : '仅标准'
}

export function formatModelRequestCapabilities(item: ProviderModelPricing): string {
  const parts: string[] = []
  if (item.supportedServiceTiers?.length) {
    parts.push(`服务等级 ${item.supportedServiceTiers.map(formatModelServiceTier).join(' / ')}`)
  }
  const reasoningCapabilities = formatModelReasoningCapabilities(item)
  if (reasoningCapabilities !== '不支持') parts.push(reasoningCapabilities)
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
    case 'generate_content':
    case 'stream_generate_content':
      return 'geekblue'
    case 'interactions':
      return 'magenta'
    case 'count_tokens':
    case 'embed_content':
      return 'gold'
    case 'message_token_counting':
      return 'lime'
    case 'images':
      return 'cyan'
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
  return protocol === 'responses'
    || protocol === 'chat_completions'
    || protocol === 'messages'
    || protocol === 'message_token_counting'
    || protocol === 'generate_content'
    || protocol === 'stream_generate_content'
    || protocol === 'count_tokens'
    || protocol === 'embed_content'
    || protocol === 'interactions'
    || protocol === 'completions'
}

function isProviderModelApiProtocol(value: string): value is ProviderModelApiProtocol {
  return Object.prototype.hasOwnProperty.call(apiProtocolLabels, value)
}

export function modelCatalogDisplaySections(item: ProviderModelPricing): ProviderModelCatalogDisplaySection[] {
  return (item.catalogDisplay ?? []).filter((section) => section.items.length > 0)
}

export function modelCatalogDisplaySection(
  item: ProviderModelPricing,
  sectionKey: string
): ProviderModelCatalogDisplaySection | undefined {
  return modelCatalogDisplaySections(item).find((section) => section.key === sectionKey)
}

export function formatModelCatalogDisplayValue(item: ProviderModelCatalogDisplayItem): string {
  if (item.format === 'text') return String(item.value)

  const numericValue = typeof item.value === 'number' ? item.value : Number(item.value)
  if (!Number.isFinite(numericValue)) return String(item.value)

  if (item.format === 'tokens') return formatTokens(numericValue)
  if (item.format === 'multiplier') return `${trimNumber(numericValue)}x`
  if (item.format === 'usd_per_image') return `$${trimNumber(numericValue)} / 张`
  if (item.format === 'usd_per_1m_token_hour') return `$${trimNumber(numericValue)} / 1M tokens·小时`
  return `$${trimNumber(numericValue)} / 1M tokens`
}

export function formatTokens(value?: number): string {
  if (typeof value !== 'number') return '-'
  if (value >= 1_048_576 && value % 1_048_576 === 0) {
    return `${trimNumber(value / 1_048_576)}M`
  }
  if (value >= 1_000_000) return `${trimNumber(value / 1_000_000)}M`
  if ((value < 100_000 && value % 1_024 === 0) || value % 16_384 === 0) {
    return `${trimNumber(value / 1_024)}K`
  }
  if (value >= 1_000) return `${trimNumber(value / 1_000)}K`
  return String(value)
}

export function formatModelModalities(values?: readonly string[]): string {
  const labels: Record<string, string> = { text: '文本', image: '图片', audio: '音频', video: '视频', file: '文件' }
  return values?.length ? values.map((value) => labels[value] ?? value).join(' / ') : '-'
}

export function formatModelTools(values?: readonly string[]): string {
  const labels: Record<string, string> = {
    function_calling: '函数调用',
    web_search: '联网搜索',
    google_search_grounding: 'Google 搜索',
    google_maps_grounding: 'Google 地图检索',
    file_search: '文件搜索',
    image_generation: '图像生成',
    code_interpreter: '代码解释器',
    code_execution: '代码执行',
    hosted_shell: '托管终端',
    apply_patch: '文件补丁',
    skills: '技能',
    computer_use: '计算机操作',
    mcp: 'MCP',
    tool_search: '工具搜索',
    structured_outputs: '结构化输出',
    url_context: 'URL 上下文'
  }
  return values?.length ? values.map((value) => labels[value] ?? value).join(' / ') : '-'
}

export function trimNumber(value: number): string {
  return Number(value.toFixed(8)).toString()
}
