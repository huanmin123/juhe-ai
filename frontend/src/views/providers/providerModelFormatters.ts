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
  images: 'Images API',
  audio_speech: 'Audio Speech (TTS)',
  audio_transcription: 'Audio Transcription (STT)',
  video: 'Videos API'
}

export const modelStatusOptions: Array<{ label: string; value: ProviderModelStatus }> = [
  { label: '启用', value: 'active' },
  { label: '草稿', value: 'draft' },
  { label: '停用', value: 'disabled' }
]

export const modelModeOptions: Array<{ label: string; value: ProviderModelMode }> = [
  { label: '对话 / 编码', value: 'text' },
  { label: '图像', value: 'image' },
  { label: '音频', value: 'audio' },
  { label: '视频', value: 'video' }
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
  image: ['imageInputUsdPer1M', 'imageOutputUsdPer1M', 'outputUsdPerImage'],
  // 音频直接价格对齐后端写路径契约（write_routes.go customInputHasDirectPrice
  // 的 audio 分支）：TTS（语音合成）记 audioOutputUsdPer1M、STT（转写）记
  // audioInputUsdPer1M，均为 USD / 1M 口径。字符价/秒价（ttsInputUsdPer1MChars /
  // audioInputUsdPerSecond）由后端静态定价表透传、随目录 API 下发，只读展示
  // （卡片价格槽位、抽屉媒体计费分组），不走管理面直接价格编辑字段。
  audio: ['audioOutputUsdPer1M', 'audioInputUsdPer1M'],
  // 视频分类（M2）无管理面直接价格编辑通道：秒价/次价（videoOutputUsdPerSecond /
  // videoOutputUsdPerCall）由后端静态定价表透传、随目录 API 下发，只读展示；
  // 直接价格编辑字段随 M3 计价完善交付（后端 write_routes.go
  // customInputHasDirectPrice 的 video 分支同款裁决）。
  video: []
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
    // 音频分类下按任务语义展示（合成/转写），单位仍是字段本身的 USD / 1M
    // 口径；文本分类（如 Gemini 音频输入）沿用通用「音频输入/输出」标签。
    if (category === 'audio' && key === 'audioOutputUsdPer1M') {
      return { ...priceFieldDefinitions[key], label: '语音合成', description: '每 100 万语音合成（TTS）输出 Token 的美元价格。' }
    }
    if (category === 'audio' && key === 'audioInputUsdPer1M') {
      return { ...priceFieldDefinitions[key], label: '转写', description: '每 100 万转写（STT）音频输入 Token 的美元价格。' }
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
  count_tokens: 'Count Tokens',
  generate_content: 'Generate Content',
  stream_generate_content: 'Stream Generate Content',
  interactions: 'Interactions',
  embed_content: 'Embed Content',
  message_token_counting: 'Message Token Counting',
  images: 'Images',
  video: 'Video',
  realtime: 'Realtime',
  audio_speech: 'Audio Speech',
  audio_transcription: 'Audio Transcription',
  completions: 'Completions'
}

const providerCapabilityOrder = [
  'responses',
  'chat',
  'messages',
  'count_tokens',
  'generate_content',
  'stream_generate_content',
  'interactions',
  'embed_content',
  'message_token_counting',
  'images',
  'video',
  'realtime',
  'audio_speech',
  'audio_transcription',
  'completions'
] as const

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
  if (category === 'audio') return ['audio_speech']
  if (category === 'video') return ['video']
  return ['responses', 'chat_completions']
}

export function defaultProtocolsForProviderModelCategory(
  provider: ProviderDefinition | undefined,
  category: ModelCategoryKey
): ProviderModelApiProtocol[] {
  const providerCode = provider?.code.trim().toLowerCase()
  if (category === 'text' && (providerCode === 'deepseek' || providerCode === 'glm')) {
    return ['chat_completions']
  }
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

// 供应商账户类型的展示文案（与账户域 accountTypeText 同词汇），并兼容
// OpenAI-compatible 等长 code 的可读化；未知值原样透传。
const providerAccountTypeLabels: Record<string, string> = {
  api_key: 'API Key',
  oauth: 'OAuth',
  google_oauth: 'Google OAuth'
}

export function formatProviderAccountTypes(accountTypes: string[]): string {
  const labels = accountTypes.map((type) => providerAccountTypeLabels[type] ?? type)
  return labels.length ? labels.join(' / ') : ''
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
  const profiles = provider.protocolProfiles ?? []
  const defaultProfile = profiles.find((profile) => profile.id === provider.defaultProtocolProfileId)
  const remainingProfiles = profiles.filter((profile) => profile !== defaultProfile)
  return defaultProfile ? [defaultProfile, ...remainingProfiles] : remainingProfiles
}

function apiProtocolForEndpointFamily(code: string): ProviderModelApiProtocol | undefined {
  return isProviderModelApiProtocol(code) ? code : undefined
}

function apiProtocolMatchesModelCategory(protocol: ProviderModelApiProtocol, category: ModelCategoryKey): boolean {
  if (category === 'image') return protocol === 'images'
  if (category === 'audio') return protocol === 'audio_speech' || protocol === 'audio_transcription'
  if (category === 'video') return protocol === 'video'
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

/** 抽屉「计费价格」表列（模型目录卡片化与详情抽屉设计 §6.2：一张表，不是每组一张）。 */
export interface ModelPriceTableColumn {
  key: string
  label: string
}

/** 价格表行：label 为主显示列；cells 按列 key 配对各档位条目，缺配对 undefined（UI 显示 -）。 */
export interface ModelPriceTableRow {
  key: string
  label: string
  cells: Record<string, ProviderModelCatalogDisplayItem | undefined>
}

export interface ModelPriceTable {
  columns: ModelPriceTableColumn[]
  rows: ModelPriceTableRow[]
  hasOffpeak: boolean
}

/** 永不渲染的分组（§6.4）：后端已停止生成，前端按 key 过滤兜底旧后端响应。 */
const BANNED_CATALOG_SECTION_KEYS = new Set(['currency_conversion', 'source_disclosure'])

/** 价格类 section 判定（从「基础信息」排除）：token_pricing / offpeak_pricing /
 * tier_ 前缀 / image_generation / media_pricing 承载计费价；新增非价格 section
 * 自动落 false，无需改此处。 */
function isPriceSectionKey(key: string): boolean {
  return key === 'token_pricing'
    || key === 'offpeak_pricing'
    || key === 'image_generation'
    || key === 'media_pricing'
    || key.startsWith('tier_')
}

const TIER_SECTION_PREFIX = 'tier_'

/** 抽屉「计费价格」表（§6.2，对齐 juhe-ai-portal 模型广场抽屉：一张表；列语义对齐官方「正常价 / 高峰价」）：
 * - 列 = 价格档位：无 offpeak_pricing 的模型（绝大多数）单列「正常价」（token_pricing 值）；
 *   有 offpeak_pricing（DeepSeek）时两列「正常价 | 高峰价」——正常价 = offpeak_pricing 值
 *   （官方空闲/正常时段价）、高峰价 = token_pricing 值（官方高峰时段价），正常价在前；
 *   tier_* 分组每档追加一列（formatModelServiceTier 列头）。
 * - 行 = 价格项：token_pricing 条目顺序为主行；image_generation / media_pricing
 *   条目追加为额外行（key 冲突时以 section key 前缀去重）。
 * - cells 按 item.key 配对，列 key 稳定区分来源：normal→offpeak 数据源（无高峰档模型
 *   →token_pricing）、peak→token_pricing 数据源、tier 分组→对应档位列，缺配对 undefined。
 * - 不含 reasoning / capacity / long_context（见 modelInfoItems）与 banned 分组。 */
export function modelPriceTable(item: ProviderModelPricing): ModelPriceTable {
  const sections = modelCatalogDisplaySections(item)
  const tokenSection = sections.find((section) => section.key === 'token_pricing')
  const offpeakSection = sections.find((section) => section.key === 'offpeak_pricing')
  const tierSections = sections.filter((section) => section.key.startsWith(TIER_SECTION_PREFIX))

  const columns: ModelPriceTableColumn[] = [{ key: 'normal', label: '正常价' }]
  if (offpeakSection) columns.push({ key: 'peak', label: '高峰价' })
  for (const tierSection of tierSections) {
    const tier = tierSection.key.slice(TIER_SECTION_PREFIX.length)
    columns.push({ key: tier, label: formatModelServiceTier(tier) })
  }

  const offpeakByKey = new Map((offpeakSection?.items ?? []).map((entry) => [entry.key, entry]))
  const rows: ModelPriceTableRow[] = []
  const usedRowKeys = new Set<string>()
  // normal 列语义：有高峰档时取官方正常时段价（offpeak_pricing），无高峰档模型取 token_pricing 本身；
  // peak 列恒为官方高峰时段价（token_pricing）。tier 档按 item.key 从对应分组配对。
  const buildCells = (entry: ProviderModelCatalogDisplayItem): Record<string, ProviderModelCatalogDisplayItem | undefined> => {
    const cells: Record<string, ProviderModelCatalogDisplayItem | undefined> = offpeakSection
      ? { normal: offpeakByKey.get(entry.key), peak: entry }
      : { normal: entry }
    for (const tierSection of tierSections) {
      const tier = tierSection.key.slice(TIER_SECTION_PREFIX.length)
      cells[tier] = tierSection.items.find((tierEntry) => tierEntry.key === entry.key)
    }
    return cells
  }
  for (const entry of tokenSection?.items ?? []) {
    rows.push({ key: entry.key, label: entry.label, cells: buildCells(entry) })
    usedRowKeys.add(entry.key)
  }
  for (const section of sections) {
    if (section.key !== 'image_generation' && section.key !== 'media_pricing') continue
    for (const entry of section.items) {
      const rowKey = usedRowKeys.has(entry.key) ? `${section.key}_${entry.key}` : entry.key
      rows.push({ key: rowKey, label: entry.label, cells: buildCells(entry) })
      usedRowKeys.add(rowKey)
    }
  }
  return { columns, rows, hasOffpeak: Boolean(offpeakSection) }
}

/** 抽屉「基础信息」分组（§6.3，对齐 portal info-grid）：非价格分组（reasoning /
 * capacity / long_context 及未来任何未归类分组）按后端顺序返回，供 label/value
 * 两列网格渲染；banned 分组（§6.4）前端兜底过滤（旧后端响应仍可能携带）。 */
export interface ModelCatalogInfoGroup {
  key: string
  label: string
  items: ProviderModelCatalogDisplayItem[]
}

export function modelInfoItems(item: ProviderModelPricing): ModelCatalogInfoGroup[] {
  return modelCatalogDisplaySections(item)
    .filter((section) => !BANNED_CATALOG_SECTION_KEYS.has(section.key))
    .filter((section) => !isPriceSectionKey(section.key))
    .map((section) => ({ key: section.key, label: section.label, items: section.items }))
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

/** 价格表单元格（模型目录卡片化与详情抽屉设计 §6.2）：单位语义由列头与区块标题承担，值只留 $ 数字。 */
export function formatPriceTableCell(item: ProviderModelCatalogDisplayItem): string {
  return formatModelCatalogDisplayValue(item).replace(/ \/ 1M tokens$/, '')
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

const modelToolLabels: Record<string, string> = {
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

export function modelToolLabel(value: string): string {
  return modelToolLabels[value] ?? value
}

export function formatModelTools(values?: readonly string[]): string {
  return values?.length ? values.map(modelToolLabel).join(' / ') : '-'
}

export interface ModelProtocolToolsEntry {
  protocol: string
  tools: string[]
}

/**
 * 目录行「协议 × 工具」矩阵投影：每协议一条（该协议下可用工具列表）。
 * 顺序跟随该行 supportedApiProtocols；矩阵中多出的键按字典序补尾（防御性，
 * 正常数据不出现）。只返回携带工具的协议条目。
 */
export function modelToolsByProtocol(item: ProviderModelPricing): ModelProtocolToolsEntry[] {
  const matrix = item.supportedToolsByProtocol ?? {}
  const order: string[] = [...(item.supportedApiProtocols ?? [])]
  for (const key of Object.keys(matrix).sort()) {
    if (!order.includes(key)) order.push(key)
  }
  return order
    .map((protocol) => ({ protocol, tools: matrix[protocol] ?? [] }))
    .filter((entry) => entry.tools.length > 0)
}

/** 关键词命中判定：任一协议下的工具名（原始枚举或中文标签）命中即命中。 */
export function modelToolsByProtocolMatchKeyword(item: ProviderModelPricing, keyword: string): boolean {
  return modelToolsByProtocol(item).some((entry) => entry.tools.some((tool) =>
    tool.toLowerCase().includes(keyword) || modelToolLabel(tool).toLowerCase().includes(keyword)
  ))
}

// 数值格式化口径（模型目录卡片化与详情抽屉设计 §5.1）：最多 2 位小数——
// |v| ≥ 0.01 走 toFixed(2) 去尾零（0.28571429 → 0.29、1.14285714 → 1.14）；
// 0 < |v| < 0.01 保留 2 位有效数字（0.00571429 → 0.0057、0.00083333 → 0.00083），
// 避免缓存读等小单价被舍成 0.01 或 0；0 显示 '0'。卡片槽位与抽屉价格值同源。
export function trimNumber(value: number): string {
  const absolute = Math.abs(value)
  if (absolute >= 0.01) return Number(value.toFixed(2)).toString()
  if (absolute > 0) return Number(value.toPrecision(2)).toString()
  return '0'
}

/** 卡片容量速览：0 或缺失显示 -，千进制量级单位（k/m），与抽屉内精确 token 值分层。 */
export function formatSpec(value?: number | null): string {
  if (typeof value !== 'number' || value === 0) return '-'
  if (value >= 1_000_000) return `${parseFloat((value / 1_000_000).toFixed(1))}m`
  if (value >= 1_000) return `${parseFloat((value / 1_000).toFixed(1))}k`
  return String(value)
}

type ModelPriceSlotField =
  | 'inputUsdPer1M'
  | 'outputUsdPer1M'
  | 'cachedInputUsdPer1M'
  | 'outputUsdPerImage'
  | 'imageInputUsdPer1M'
  | 'imageOutputUsdPer1M'
  | 'audioInputUsdPer1M'
  | 'audioOutputUsdPer1M'
  | 'ttsInputUsdPer1MChars'
  | 'audioInputUsdPerSecond'
  | 'videoOutputUsdPerSecond'
  | 'videoOutputUsdPerCall'

export interface ModelPriceSlot {
  label: string
  value: string
  unit: string
}

// 卡片锚点价候选序列（模型目录卡片化与详情抽屉设计 5.1）：按序取真实存在
// （非 null/undefined，显式 0 保留）的字段，最多 3 个。
const modelPriceSlotCandidates: Record<ModelCategoryKey, Array<{ label: string; field: ModelPriceSlotField; unit: string }>> = {
  text: [
    { label: '输入', field: 'inputUsdPer1M', unit: '/1M tokens' },
    { label: '输出', field: 'outputUsdPer1M', unit: '/1M tokens' },
    { label: '缓存读', field: 'cachedInputUsdPer1M', unit: '/1M tokens' }
  ],
  image: [
    { label: '每张输出', field: 'outputUsdPerImage', unit: '/张' },
    { label: '图片输入', field: 'imageInputUsdPer1M', unit: '/1M tokens' },
    { label: '图片输出', field: 'imageOutputUsdPer1M', unit: '/1M tokens' }
  ],
  audio: [
    { label: '音频输入', field: 'audioInputUsdPer1M', unit: '/1M tokens' },
    { label: '音频输出', field: 'audioOutputUsdPer1M', unit: '/1M tokens' },
    { label: '语音合成', field: 'ttsInputUsdPer1MChars', unit: '/1M 字符' },
    { label: '语音识别', field: 'audioInputUsdPerSecond', unit: '/秒' },
    { label: '输入', field: 'inputUsdPer1M', unit: '/1M tokens' }
  ],
  video: [
    { label: '视频（每秒）', field: 'videoOutputUsdPerSecond', unit: '/秒' },
    { label: '视频（每次）', field: 'videoOutputUsdPerCall', unit: '/次' },
    { label: '输入', field: 'inputUsdPer1M', unit: '/1M tokens' }
  ]
}

// 分类候选全空时的回退序列（设计 5.1「分类回退」）：按 text→image→audio→video
// 展平候选表并按字段去重，label/unit 与候选表同字段保持单一来源。
const modelPriceSlotFallbackCandidates: Array<{ label: string; field: ModelPriceSlotField; unit: string }> = modelCategoryOrder
  .flatMap((category) => modelPriceSlotCandidates[category])
  .filter((candidate, index, all) => all.findIndex((entry) => entry.field === candidate.field) === index)

function collectModelPriceSlots(
  item: ProviderModelPricing,
  candidates: Array<{ label: string; field: ModelPriceSlotField; unit: string }>
): ModelPriceSlot[] {
  const slots: ModelPriceSlot[] = []
  for (const candidate of candidates) {
    if (slots.length >= 3) break
    const value = item[candidate.field]
    if (typeof value !== 'number') continue
    slots.push({ label: candidate.label, value: trimNumber(value), unit: candidate.unit })
  }
  return slots
}

export function modelPriceSlots(item: ProviderModelPricing): ModelPriceSlot[] {
  // 单 Token 价合并只跟随后端投影（token_pricing 分组恰为单「token」条目），
  // 不做字段级全局判定；其余行保持候选序列原口径。
  const tokenPricingSection = modelCatalogDisplaySections(item).find((section) => section.key === 'token_pricing')
  if (
    tokenPricingSection?.items.length === 1
    && tokenPricingSection.items[0]?.key === 'token'
    && typeof item.inputUsdPer1M === 'number'
  ) {
    return [{ label: 'Token', value: trimNumber(item.inputUsdPer1M), unit: '/1M tokens' }]
  }
  // 分类候选 0 槽时按全字段顺序回退取存在者（mode 缺失行会被误分类为 text），
  // 价格区不因分类误配隐藏真实媒体价；全部直价字段为空才返回空数组。
  const slots = collectModelPriceSlots(item, modelPriceSlotCandidates[getModelCategory(item)])
  if (slots.length) return slots
  return collectModelPriceSlots(item, modelPriceSlotFallbackCandidates)
}
