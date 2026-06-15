import {
  deleteCustomProviderModel,
  findCustomProviderModelById,
  listCustomProviderModelsForCatalog,
  upsertCustomProviderModel,
  type CustomProviderModelRecord,
  type CustomProviderModelScope,
  type CustomProviderModelStatus,
  type CustomProviderModelVisibility,
  type UpsertCustomProviderModelInput
} from '../../storage/custom-provider-models.repository.js'
import {
  listProviderModelPricing,
  type CostInput,
  type ProviderCostBreakdown,
  type ProviderModelApiProtocol,
  type ProviderModelPricing
} from './model-pricing.service.js'
import { OPENAI_COMPATIBLE_PROVIDER_CODE, normalizeProviderToken } from '../../domain/provider-protocol.js'
import { listOpenAIProtocolProviderCodes } from '../../storage/provider.repository.js'

export type ModelCatalogScope = 'built_in' | CustomProviderModelScope
export type ModelCatalogVisibility = 'public' | 'mapping_target_only'

export interface ProviderModelCatalogItem extends ProviderModelPricing {
  id?: string
  scope: ModelCatalogScope
  visibility: ModelCatalogVisibility
  status: 'draft' | 'active' | 'disabled'
  systemAccountId?: string
  displayName?: string
  pricingModel?: string
  contextWindowTokens?: number
  pricingNotes?: string
  capabilityNotes?: string
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface OpenAIModelListItem {
  id: string
  object: 'model'
  created: number
  owned_by: string
}

export interface OpenAIModelsListResponse {
  object: 'list'
  data: OpenAIModelListItem[]
}

export interface CodexReasoningEffortPreset {
  effort: string
  description: string
}

export interface CodexModelListItem {
  slug: string
  display_name: string
  description: string | null
  default_reasoning_level: string
  supported_reasoning_levels: CodexReasoningEffortPreset[]
  shell_type: 'shell_command'
  visibility: 'list'
  supported_in_api: boolean
  priority: number
  additional_speed_tiers: string[]
  service_tiers: Array<{ id: string; name: string; description: string }>
  default_service_tier: string | null
  availability_nux: null
  upgrade: null
  base_instructions: string
  model_messages: null
  supports_reasoning_summaries: boolean
  default_reasoning_summary: 'auto'
  support_verbosity: boolean
  default_verbosity: null
  apply_patch_tool_type: null
  web_search_tool_type: 'text'
  truncation_policy: { mode: 'bytes'; limit: number }
  supports_parallel_tool_calls: boolean
  supports_image_detail_original: boolean
  context_window: number
  max_context_window: number
  auto_compact_token_limit: null
  effective_context_window_percent: number
  experimental_supported_tools: string[]
  input_modalities: Array<'text' | 'image'>
  supports_search_tool: boolean
  use_responses_lite: boolean
  auto_review_model_override: null
  tool_mode: null
  multi_agent_version: null
}

export interface CodexModelsListResponse {
  models: CodexModelListItem[]
}

export interface ModelCatalogListOptions {
  providerCode: string
  systemAccountId?: string
  includeMappingTargets?: boolean
  includeInactive?: boolean
  includeUnpriced?: boolean
}

export interface SaveCustomProviderModelInput extends Omit<UpsertCustomProviderModelInput, 'actorSystemAccountId'> {
  actorSystemAccountId: string
}

export function listProviderModelCatalog(options: ModelCatalogListOptions): ProviderModelCatalogItem[] {
  const sourceProviderCodes = modelCatalogSourceProviderCodes(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const builtIn = builtInSourceProviderCodes.flatMap((providerCode) => listProviderModelPricing(providerCode).map(toBuiltInCatalogItem))
  const custom = sourceProviderCodes.flatMap((providerCode) => listCustomProviderModelsForCatalog({
    providerCode,
    systemAccountId: options.systemAccountId,
    includeMappingTargets: true,
    includeInactive: options.includeInactive
  }).map(toCustomCatalogItem))
  const merged = mergeModelCatalogItems([...builtIn, ...custom])

  return merged
    .filter((item) => options.includeMappingTargets || item.visibility === 'public')
    .filter((item) => options.includeInactive || item.status === 'active')
    .filter((item) => options.includeUnpriced || hasResolvablePrice(item, merged))
    .sort(compareProviderModelCatalogItems)
}

export function buildOpenAIModelsResponseFromCatalog(items: ProviderModelCatalogItem[]): OpenAIModelsListResponse {
  return {
    object: 'list',
    data: items
      .filter((item) => item.visibility === 'public')
      .map((item) => ({
        id: item.model,
        object: 'model',
        created: modelCreatedUnixSeconds(item),
        owned_by: item.scope === 'built_in' ? 'openai' : 'juhe-ai'
      }))
  }
}

export function buildCodexModelsResponseFromCatalog(items: ProviderModelCatalogItem[]): CodexModelsListResponse {
  return {
    models: items
      .filter((item) => item.visibility === 'public')
      .map((item, index) => buildCodexModelInfo(item, index))
  }
}

export function saveCustomProviderModel(input: SaveCustomProviderModelInput): ProviderModelCatalogItem {
  const saved = upsertCustomProviderModel(input)
  return toCustomCatalogItem(saved)
}

export function findCustomProviderModel(id: string): CustomProviderModelRecord | undefined {
  return findCustomProviderModelById(id)
}

export function removeCustomProviderModel(id: string): boolean {
  return deleteCustomProviderModel(id)
}

export function estimateCatalogCostUsd(input: CostInput & { systemAccountId?: string }): number | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing || !hasAnyCostDimension(input)) {
    return undefined
  }
  const breakdown = buildCatalogCostBreakdown({ ...input, model: pricing.model })
  return breakdown?.accountChargeUsd
}

export function estimateCatalogCacheReadCostUsd(input: CostInput & { systemAccountId?: string }): number | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing || input.cacheReadTokens === undefined) return undefined
  const cachedInputPrice = perToken(pricing.cachedInputUsdPer1M) ?? perToken(pricing.inputUsdPer1M)
  if (cachedInputPrice === undefined) return undefined
  return roundCost(Math.max(input.cacheReadTokens, 0) * cachedInputPrice)
}

export function resolveCatalogPricingModel(input: { providerCode: string; model?: string; systemAccountId?: string }): string | undefined {
  return resolveCatalogPricing(input)?.model
}

export function buildCatalogCostBreakdown(input: CostInput & { systemAccountId?: string; costUsd?: number }): ProviderCostBreakdown | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing) return undefined

  const inputPrice = perToken(pricing.inputUsdPer1M)
  const outputPrice = perToken(pricing.outputUsdPer1M)
  const cachedInputPrice = perToken(pricing.cachedInputUsdPer1M) ?? inputPrice
  const inputImagePrice = perToken(pricing.imageInputUsdPer1M)
  const outputImagePrice = perToken(pricing.imageOutputUsdPer1M)
  const inputAudioPrice = perToken(pricing.audioInputUsdPer1M)
  const outputAudioPrice = perToken(pricing.audioOutputUsdPer1M)
  const outputImageUnitPrice = pricing.outputUsdPerImage
  if (!hasAnyPrice(inputPrice, outputPrice, cachedInputPrice, inputImagePrice, outputImagePrice, inputAudioPrice, outputAudioPrice, outputImageUnitPrice)) return undefined

  const cacheReadTokens = Math.max(input.cacheReadTokens ?? 0, 0)
  const inputImageTokens = inputImagePrice === undefined ? 0 : Math.max(input.inputImageTokens ?? 0, 0)
  const outputImageTokens = outputImagePrice === undefined ? 0 : Math.max(input.outputImageTokens ?? defaultImageOutputTokens(input, pricing), 0)
  const inputAudioTokens = inputAudioPrice === undefined ? 0 : Math.max(input.inputAudioTokens ?? defaultInputAudioTokens(input, inputPrice, cacheReadTokens, inputImageTokens), 0)
  const outputAudioTokens = outputAudioPrice === undefined ? 0 : Math.max(input.outputAudioTokens ?? defaultOutputAudioTokens(input, outputPrice, outputImageTokens), 0)
  const outputImageCount = outputImageUnitPrice === undefined ? 0 : Math.max(input.outputImageCount ?? 0, 0)
  const uncachedInputTokens = Math.max((input.inputTokens ?? 0) - cacheReadTokens - inputImageTokens - inputAudioTokens, 0)
  const outputTokens = Math.max((input.outputTokens ?? 0) - outputImageTokens - outputAudioTokens, 0)
  const inputCostUsd = inputPrice === undefined ? undefined : roundCost(uncachedInputTokens * inputPrice)
  const outputCostUsd = outputPrice === undefined ? undefined : roundCost(outputTokens * outputPrice)
  const cacheReadCostUsd = cachedInputPrice === undefined ? undefined : roundCost(cacheReadTokens * cachedInputPrice)
  const inputImageCostUsd = inputImageTokens > 0 && inputImagePrice !== undefined ? roundCost(inputImageTokens * inputImagePrice) : undefined
  const outputImageCostUsd = outputImageTokens > 0 && outputImagePrice !== undefined ? roundCost(outputImageTokens * outputImagePrice) : undefined
  const inputAudioCostUsd = inputAudioTokens > 0 && inputAudioPrice !== undefined ? roundCost(inputAudioTokens * inputAudioPrice) : undefined
  const outputAudioCostUsd = outputAudioTokens > 0 && outputAudioPrice !== undefined ? roundCost(outputAudioTokens * outputAudioPrice) : undefined
  const outputImageUnitCostUsd = outputImageCount > 0 && outputImageUnitPrice !== undefined ? roundCost(outputImageCount * outputImageUnitPrice) : undefined

  return {
    inputCostUsd,
    outputCostUsd,
    inputUsdPer1M: pricing.inputUsdPer1M,
    outputUsdPer1M: pricing.outputUsdPer1M,
    cacheReadCostUsd,
    cacheReadUsdPer1M: pricing.cachedInputUsdPer1M ?? pricing.inputUsdPer1M,
    inputImageCostUsd,
    outputImageCostUsd,
    inputImageUsdPer1M: pricing.imageInputUsdPer1M,
    outputImageUsdPer1M: pricing.imageOutputUsdPer1M,
    inputAudioCostUsd,
    outputAudioCostUsd,
    inputAudioUsdPer1M: pricing.audioInputUsdPer1M,
    outputAudioUsdPer1M: pricing.audioOutputUsdPer1M,
    outputImageUnitCostUsd,
    outputUsdPerImage: pricing.outputUsdPerImage,
    accountChargeUsd: input.costUsd ?? sumCostParts(inputCostUsd, outputCostUsd, cacheReadCostUsd, inputImageCostUsd, outputImageCostUsd, inputAudioCostUsd, outputAudioCostUsd, outputImageUnitCostUsd),
    multiplier: 1
  }
}

function resolveCatalogPricing(input: CostInput & { systemAccountId?: string }): ProviderModelCatalogItem | undefined {
  if (!input.model) return undefined
  const catalog = listProviderModelCatalog({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId,
    includeMappingTargets: true
  })
  const item = findCatalogItem(catalog, input.model)
  if (!item) return undefined
  if (!item.pricingModel) return item
  return findCatalogItem(catalog, item.pricingModel)
}

function mergeModelCatalogItems(items: ProviderModelCatalogItem[]): ProviderModelCatalogItem[] {
  const merged = new Map<string, ProviderModelCatalogItem>()
  for (const item of items) {
    const key = item.model.trim().toLowerCase()
    if (!key) continue
    const previous = merged.get(key)
    if (!previous || catalogPriority(item) >= catalogPriority(previous)) {
      merged.set(key, item)
    }
  }
  return [...merged.values()]
}

function hasResolvablePrice(item: ProviderModelCatalogItem, allItems: ProviderModelCatalogItem[]): boolean {
  if (hasDirectPrice(item)) return true
  if (!item.pricingModel) return false
  const target = findCatalogItem(allItems, item.pricingModel)
  return Boolean(target && hasDirectPrice(target) && !target.pricingModel)
}

function hasDirectPrice(item: ProviderModelPricing): boolean {
  return item.inputUsdPer1M !== undefined
    || item.outputUsdPer1M !== undefined
    || item.cachedInputUsdPer1M !== undefined
    || item.cacheWriteUsdPer1M !== undefined
    || item.imageInputUsdPer1M !== undefined
    || item.imageOutputUsdPer1M !== undefined
    || item.audioInputUsdPer1M !== undefined
    || item.audioOutputUsdPer1M !== undefined
    || item.outputUsdPerImage !== undefined
}

function findCatalogItem(items: ProviderModelCatalogItem[], model: string): ProviderModelCatalogItem | undefined {
  const normalized = model.trim().toLowerCase()
  return items.find((item) => item.model.trim().toLowerCase() === normalized)
}

function toBuiltInCatalogItem(item: ProviderModelPricing): ProviderModelCatalogItem {
  return {
    ...item,
    scope: 'built_in',
    visibility: 'public',
    status: 'active'
  }
}

function toCustomCatalogItem(item: CustomProviderModelRecord): ProviderModelCatalogItem {
  return {
    id: item.id,
    providerCode: item.providerCode,
    model: item.model,
    mode: item.mode,
    releaseDate: item.releaseDate,
    shutdownDate: item.shutdownDate,
    supportedApiProtocols: item.supportedApiProtocols as ProviderModelApiProtocol[],
    inputUsdPer1M: item.inputUsdPer1M,
    outputUsdPer1M: item.outputUsdPer1M,
    cachedInputUsdPer1M: item.cachedInputUsdPer1M,
    cacheWriteUsdPer1M: item.cacheWriteUsdPer1M,
    imageInputUsdPer1M: item.imageInputUsdPer1M,
    imageOutputUsdPer1M: item.imageOutputUsdPer1M,
    audioInputUsdPer1M: item.audioInputUsdPer1M,
    audioOutputUsdPer1M: item.audioOutputUsdPer1M,
    outputUsdPerImage: item.outputUsdPerImage,
    maxOutputTokens: item.maxOutputTokens,
    supportsPromptCaching: item.cachedInputUsdPer1M !== undefined,
    supportsServiceTier: false,
    source: item.scope === 'global' ? 'custom-global' : 'custom-personal',
    scope: item.scope,
    visibility: item.visibility,
    status: item.status,
    systemAccountId: item.systemAccountId,
    displayName: item.displayName,
    pricingModel: item.pricingModel,
    contextWindowTokens: item.contextWindowTokens,
    pricingNotes: item.pricingNotes,
    capabilityNotes: item.capabilityNotes,
    notes: item.notes,
    createdAt: item.createdAt,
    updatedAt: item.updatedAt
  }
}

function buildCodexModelInfo(item: ProviderModelCatalogItem, index: number): CodexModelListItem {
  const contextWindow = codexContextWindow(item)
  return {
    slug: item.model,
    display_name: item.displayName || item.model,
    description: item.capabilityNotes || item.pricingNotes || item.notes || null,
    default_reasoning_level: 'medium',
    supported_reasoning_levels: [
      { effort: 'minimal', description: 'Minimal' },
      { effort: 'low', description: 'Low' },
      { effort: 'medium', description: 'Medium' },
      { effort: 'high', description: 'High' }
    ],
    shell_type: 'shell_command',
    visibility: 'list',
    supported_in_api: true,
    priority: index,
    additional_speed_tiers: [],
    service_tiers: [],
    default_service_tier: null,
    availability_nux: null,
    upgrade: null,
    base_instructions: 'You are Codex, a coding agent.',
    model_messages: null,
    supports_reasoning_summaries: false,
    default_reasoning_summary: 'auto',
    support_verbosity: false,
    default_verbosity: null,
    apply_patch_tool_type: null,
    web_search_tool_type: 'text',
    truncation_policy: { mode: 'bytes', limit: 10_000 },
    supports_parallel_tool_calls: false,
    supports_image_detail_original: false,
    context_window: contextWindow,
    max_context_window: contextWindow,
    auto_compact_token_limit: null,
    effective_context_window_percent: 95,
    experimental_supported_tools: [],
    input_modalities: ['text', 'image'],
    supports_search_tool: false,
    use_responses_lite: false,
    auto_review_model_override: null,
    tool_mode: null,
    multi_agent_version: null
  }
}

function codexContextWindow(item: ProviderModelCatalogItem): number {
  const configured = item.contextWindowTokens
  if (Number.isFinite(configured) && configured && configured > 0) {
    return Math.trunc(configured)
  }
  return 272_000
}

function catalogPriority(item: ProviderModelCatalogItem): number {
  if (item.scope === 'personal') return 3
  if (item.scope === 'global') return 2
  return 1
}

export function compareProviderModelCatalogItems(left: ProviderModelCatalogItem, right: ProviderModelCatalogItem): number {
  const leftReleaseDate = sortableCatalogReleaseDate(left)
  const rightReleaseDate = sortableCatalogReleaseDate(right)
  if (leftReleaseDate && rightReleaseDate && leftReleaseDate !== rightReleaseDate) {
    return rightReleaseDate.localeCompare(leftReleaseDate)
  }
  if (leftReleaseDate && !rightReleaseDate) return -1
  if (!leftReleaseDate && rightReleaseDate) return 1

  const modelOrder = left.model.localeCompare(right.model, 'en')
  if (modelOrder !== 0) return modelOrder
  return (left.id ?? '').localeCompare(right.id ?? '', 'en')
}

function modelCatalogSourceProviderCodes(providerCode: string): string[] {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return []
  if (normalizedProviderCode !== OPENAI_COMPATIBLE_PROVIDER_CODE) return [normalizedProviderCode]

  const openAIProtocolProviderCodes = listOpenAIProtocolProviderCodes()
    .map((code) => normalizeProviderToken(code))
    .filter((code): code is string => Boolean(code))
  const childCodes = openAIProtocolProviderCodes.filter((code) => code !== normalizedProviderCode)
  return [...new Set([...childCodes, normalizedProviderCode])]
}

function modelCatalogBuiltInSourceProviderCodes(providerCode: string, sourceProviderCodes: string[]): string[] {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (normalizedProviderCode !== OPENAI_COMPATIBLE_PROVIDER_CODE) return sourceProviderCodes
  return sourceProviderCodes.filter((code) => code !== normalizedProviderCode)
}

function sortableCatalogReleaseDate(item: ProviderModelCatalogItem): string | undefined {
  return typeof item.releaseDate === 'string' && item.releaseDate.trim()
    ? item.releaseDate.trim()
    : undefined
}

function modelCreatedUnixSeconds(item: ProviderModelCatalogItem): number {
  const source = item.releaseDate
    ? `${item.releaseDate}T00:00:00.000Z`
    : item.createdAt
  if (!source) return 0
  const time = Date.parse(source)
  return Number.isFinite(time) ? Math.trunc(time / 1000) : 0
}

function defaultImageOutputTokens(input: CostInput, pricing: ProviderModelPricing): number {
  if (input.outputImageTokens !== undefined || pricing.mode !== 'image_generation' || pricing.imageOutputUsdPer1M === undefined) {
    return 0
  }
  return Math.max(input.outputTokens ?? 0, 0)
}

function defaultInputAudioTokens(input: CostInput, inputPrice: number | undefined, cacheReadTokens: number, inputImageTokens: number): number {
  if (input.inputAudioTokens !== undefined || inputPrice !== undefined) return 0
  return Math.max((input.inputTokens ?? 0) - cacheReadTokens - inputImageTokens, 0)
}

function defaultOutputAudioTokens(input: CostInput, outputPrice: number | undefined, outputImageTokens: number): number {
  if (input.outputAudioTokens !== undefined || outputPrice !== undefined) return 0
  return Math.max((input.outputTokens ?? 0) - outputImageTokens, 0)
}

function hasAnyCostDimension(input: CostInput): boolean {
  return input.inputTokens !== undefined
    || input.outputTokens !== undefined
    || input.cacheReadTokens !== undefined
    || input.inputImageTokens !== undefined
    || input.outputImageTokens !== undefined
    || input.inputAudioTokens !== undefined
    || input.outputAudioTokens !== undefined
    || input.outputImageCount !== undefined
}

function hasAnyPrice(...prices: Array<number | undefined>): boolean {
  return prices.some((price) => price !== undefined)
}

function perToken(value: number | undefined): number | undefined {
  return value === undefined ? undefined : value / 1_000_000
}

function roundCost(value: number): number {
  return Number(value.toFixed(10))
}

function sumCostParts(...parts: Array<number | undefined>): number | undefined {
  const values = parts.filter((part): part is number => part !== undefined)
  return values.length ? roundCost(values.reduce((sum, part) => sum + part, 0)) : undefined
}
