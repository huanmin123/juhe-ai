import {
  customProviderModelAccountBindingSummary,
  customProviderModelAccountBindingSummaryAsync,
  deleteCustomProviderModel,
  deleteCustomProviderModelAsync,
  findCustomProviderModelById,
  findCustomProviderModelByIdAsync,
  listCustomProviderModelsForCatalog,
  listCustomProviderModelsForCatalogAsync,
  upsertCustomProviderModel,
  upsertCustomProviderModelAsync,
  type CustomProviderModelAccountBindingSummary,
  type CustomProviderModelRecord,
  type CustomProviderModelScope,
  type CustomProviderModelStatus,
  type UpsertCustomProviderModelInput
} from '../../storage/custom-provider-models.repository.js'
import {
  listProviderModelPricing,
  type CostInput,
  type ProviderCostBreakdown,
  type ProviderModelApiProtocol,
  type ProviderModelPricing
} from './model-pricing.service.js'
import {
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  normalizeProviderToken
} from '../../domain/provider-protocol.js'
import { listOpenAIProtocolProviderCodes, listOpenAIProtocolProviderCodesAsync } from '../../storage/provider.repository.js'
import { createAppCache, createSharedJsonCache, throwIfRedisCacheIsRequired } from '../../shared/cache.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { modelPricingProviderDriverForProvider } from './provider-driver.registry.js'
import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'

export type ModelCatalogScope = 'built_in' | CustomProviderModelScope

export interface ProviderModelCatalogItem extends ProviderModelPricing {
  id?: string
  scope: ModelCatalogScope
  status: 'draft' | 'active' | 'disabled'
  systemAccountId?: string
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
  includeInactive?: boolean
  includeUnpriced?: boolean
}

export interface SaveCustomProviderModelInput extends Omit<UpsertCustomProviderModelInput, 'actorSystemAccountId'> {
  actorSystemAccountId: string
}

const modelCatalogCacheTtlMs = 60_000
const providerModelCatalogCache = createAppCache<string, ProviderModelCatalogItem[]>({
  name: 'model-pricing:provider-model-catalog',
  max: 1000,
  ttlMs: modelCatalogCacheTtlMs
})

const providerModelCatalogSharedCache = createSharedJsonCache<ProviderModelCatalogItem[]>({
  name: 'model-pricing:provider-model-catalog',
  max: 1000,
  ttlMs: modelCatalogCacheTtlMs
})

const postgresSyncOpenAIProtocolProviderCodes = [
  GPT_VENDOR_CODE,
  DEEPSEEK_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE
] as const

export function listProviderModelCatalog(options: ModelCatalogListOptions): ProviderModelCatalogItem[] {
  if (runtimeConfig.databaseDriver === 'postgres' || runtimeConfig.cacheDriver === 'redis') {
    throw new Error('高性能模式禁止同步读取模型目录，必须使用 listProviderModelCatalogAsync')
  }
  const cacheKey = modelCatalogCacheKey(options)
  const cached = providerModelCatalogCache.get(cacheKey)
  if (cached) {
    return cloneProviderModelCatalogItems(cached)
  }
  const catalog = buildProviderModelCatalog(options)
  providerModelCatalogCache.set(cacheKey, cloneProviderModelCatalogItems(catalog))
  return cloneProviderModelCatalogItems(catalog)
}

export async function listProviderModelCatalogAsync(options: ModelCatalogListOptions): Promise<ProviderModelCatalogItem[]> {
  const cacheKey = modelCatalogCacheKey(options)
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = providerModelCatalogCache.get(cacheKey)
    if (cached) {
      return cloneProviderModelCatalogItems(cached)
    }
  }
  const sharedCached = await getProviderModelCatalogSharedCacheEntry(cacheKey)
  if (sharedCached) {
    providerModelCatalogCache.set(cacheKey, cloneProviderModelCatalogItems(sharedCached))
    return cloneProviderModelCatalogItems(sharedCached)
  }
  const catalog = await buildProviderModelCatalogAsync(options)
  await setProviderModelCatalogCacheEntryAsync(cacheKey, cloneProviderModelCatalogItems(catalog))
  return cloneProviderModelCatalogItems(catalog)
}

function buildProviderModelCatalog(options: ModelCatalogListOptions): ProviderModelCatalogItem[] {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式禁止同步构建模型目录，必须使用 buildProviderModelCatalogAsync')
  }
  const sourceProviderCodes = modelCatalogSourceProviderCodes(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const builtIn = builtInSourceProviderCodes.flatMap((providerCode) => listProviderModelPricing(providerCode)
    .filter((item) => item.catalogVisible)
    .map(toBuiltInCatalogItem))
  const custom = sourceProviderCodes.flatMap((providerCode) => listCustomProviderModelsForCatalog({
      providerCode,
      systemAccountId: options.systemAccountId,
      includeInactive: options.includeInactive
    }).map(toCustomCatalogItem))
  const merged = mergeModelCatalogItems([...builtIn, ...custom])

  return merged
    .filter((item) => options.includeInactive || item.status === 'active')
    .filter((item) => options.includeUnpriced || hasResolvablePrice(item, merged))
    .sort(compareProviderModelCatalogItems)
}

async function buildProviderModelCatalogAsync(options: ModelCatalogListOptions): Promise<ProviderModelCatalogItem[]> {
  const sourceProviderCodes = await modelCatalogSourceProviderCodesAsync(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const builtIn = builtInSourceProviderCodes.flatMap((providerCode) => listProviderModelPricing(providerCode)
    .filter((item) => item.catalogVisible)
    .map(toBuiltInCatalogItem))
  const customCatalogs = await Promise.all(sourceProviderCodes.map((providerCode) => listCustomProviderModelsForCatalogAsync({
    providerCode,
    systemAccountId: options.systemAccountId,
    includeInactive: options.includeInactive
  })))
  const custom = customCatalogs.flatMap((items) => items.map(toCustomCatalogItem))
  const merged = mergeModelCatalogItems([...builtIn, ...custom])

  return merged
    .filter((item) => options.includeInactive || item.status === 'active')
    .filter((item) => options.includeUnpriced || hasResolvablePrice(item, merged))
    .sort(compareProviderModelCatalogItems)
}

export function buildOpenAIModelsResponseFromCatalog(items: ProviderModelCatalogItem[]): OpenAIModelsListResponse {
  return {
    object: 'list',
    data: items
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
      .map((item, index) => buildCodexModelInfo(item, index))
  }
}

export function saveCustomProviderModel(input: SaveCustomProviderModelInput): ProviderModelCatalogItem {
  const saved = upsertCustomProviderModel(input)
  return toCustomCatalogItem(saved)
}

export async function saveCustomProviderModelAsync(input: SaveCustomProviderModelInput): Promise<ProviderModelCatalogItem> {
  const saved = await upsertCustomProviderModelAsync(input)
  return toCustomCatalogItem(saved)
}

export function findCustomProviderModel(id: string): CustomProviderModelRecord | undefined {
  return findCustomProviderModelById(id)
}

export async function findCustomProviderModelAsync(id: string): Promise<CustomProviderModelRecord | undefined> {
  return findCustomProviderModelByIdAsync(id)
}

export function removeCustomProviderModel(id: string): boolean {
  return deleteCustomProviderModel(id)
}

export async function removeCustomProviderModelAsync(id: string): Promise<boolean> {
  return deleteCustomProviderModelAsync(id)
}

export function customProviderModelBindings(input: {
  providerCode: string
  model: string
  scope: CustomProviderModelScope
  systemAccountId?: string
}): CustomProviderModelAccountBindingSummary {
  return customProviderModelAccountBindingSummary(input)
}

export async function customProviderModelBindingsAsync(input: {
  providerCode: string
  model: string
  scope: CustomProviderModelScope
  systemAccountId?: string
}): Promise<CustomProviderModelAccountBindingSummary> {
  return customProviderModelAccountBindingSummaryAsync(input)
}

export function estimateCatalogCostUsd(input: CostInput & { systemAccountId?: string }): number | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing || !hasAnyCostDimension(input)) {
    return undefined
  }
  const breakdown = buildCatalogCostBreakdownFromPricing(pricing, { ...input, model: pricing.model })
  return breakdown?.accountChargeUsd
}

export async function estimateCatalogCostUsdAsync(input: CostInput & { systemAccountId?: string }): Promise<number | undefined> {
  const pricing = await resolveCatalogPricingAsync(input)
  if (!pricing || !hasAnyCostDimension(input)) {
    return undefined
  }
  const breakdown = buildCatalogCostBreakdownFromPricing(pricing, { ...input, model: pricing.model })
  return breakdown?.accountChargeUsd
}

export function estimateCatalogCacheReadCostUsd(input: CostInput & { systemAccountId?: string }): number | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing || input.cacheReadTokens === undefined) return undefined
  const cachedInputPrice = perToken(pricing.cachedInputUsdPer1M) ?? perToken(pricing.inputUsdPer1M)
  if (cachedInputPrice === undefined) return undefined
  return roundCost(Math.max(input.cacheReadTokens, 0) * cachedInputPrice)
}

export async function estimateCatalogCacheReadCostUsdAsync(input: CostInput & { systemAccountId?: string }): Promise<number | undefined> {
  const pricing = await resolveCatalogPricingAsync(input)
  if (!pricing || input.cacheReadTokens === undefined) return undefined
  const cachedInputPrice = perToken(pricing.cachedInputUsdPer1M) ?? perToken(pricing.inputUsdPer1M)
  if (cachedInputPrice === undefined) return undefined
  return roundCost(Math.max(input.cacheReadTokens, 0) * cachedInputPrice)
}

export function estimateCatalogCacheWriteCostUsd(input: CostInput & { systemAccountId?: string }): number | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing || (input.cacheWriteTokens === undefined && input.cacheWrite1hTokens === undefined)) return undefined
  const cacheWritePrice = perToken(pricing.cacheWriteUsdPer1M)
  const cacheWrite1hPrice = perToken(pricing.cacheWrite1hUsdPer1M) ?? cacheWritePrice
  if (cacheWritePrice === undefined && cacheWrite1hPrice === undefined) return undefined
  const cacheWriteTokens = Math.max(input.cacheWriteTokens ?? 0, 0)
  const cacheWrite1hTokens = normalizedCacheWrite1hTokens(input, cacheWriteTokens)
  const cacheWriteStandardTokens = Math.max(cacheWriteTokens - cacheWrite1hTokens, 0)
  return roundCost(
    cacheWriteStandardTokens * (cacheWritePrice ?? 0)
    + cacheWrite1hTokens * (cacheWrite1hPrice ?? 0)
  )
}

export async function estimateCatalogCacheWriteCostUsdAsync(input: CostInput & { systemAccountId?: string }): Promise<number | undefined> {
  const pricing = await resolveCatalogPricingAsync(input)
  if (!pricing || (input.cacheWriteTokens === undefined && input.cacheWrite1hTokens === undefined)) return undefined
  const cacheWritePrice = perToken(pricing.cacheWriteUsdPer1M)
  const cacheWrite1hPrice = perToken(pricing.cacheWrite1hUsdPer1M) ?? cacheWritePrice
  if (cacheWritePrice === undefined && cacheWrite1hPrice === undefined) return undefined
  const cacheWriteTokens = Math.max(input.cacheWriteTokens ?? 0, 0)
  const cacheWrite1hTokens = normalizedCacheWrite1hTokens(input, cacheWriteTokens)
  const cacheWriteStandardTokens = Math.max(cacheWriteTokens - cacheWrite1hTokens, 0)
  return roundCost(
    cacheWriteStandardTokens * (cacheWritePrice ?? 0)
    + cacheWrite1hTokens * (cacheWrite1hPrice ?? 0)
  )
}

export function resolveCatalogPricingModel(input: { providerCode: string; model?: string; systemAccountId?: string }): string | undefined {
  return resolveCatalogPricing(input)?.model
}

export async function resolveCatalogPricingModelAsync(input: { providerCode: string; model?: string; systemAccountId?: string }): Promise<string | undefined> {
  return (await resolveCatalogPricingAsync(input))?.model
}

export function buildCatalogCostBreakdown(input: CostInput & { systemAccountId?: string; costUsd?: number }): ProviderCostBreakdown | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing) return undefined
  return buildCatalogCostBreakdownFromPricing(pricing, input)
}

export async function buildCatalogCostBreakdownAsync(input: CostInput & { systemAccountId?: string; costUsd?: number }): Promise<ProviderCostBreakdown | undefined> {
  const pricing = await resolveCatalogPricingAsync(input)
  if (!pricing) return undefined
  return buildCatalogCostBreakdownFromPricing(pricing, input)
}

function buildCatalogCostBreakdownFromPricing(
  pricing: ProviderModelCatalogItem,
  input: CostInput & { systemAccountId?: string; costUsd?: number }
): ProviderCostBreakdown | undefined {
  const inputPrice = perToken(pricing.inputUsdPer1M)
  const outputPrice = perToken(pricing.outputUsdPer1M)
  const cachedInputPrice = perToken(pricing.cachedInputUsdPer1M) ?? inputPrice
  const cacheWritePrice = perToken(pricing.cacheWriteUsdPer1M)
  const cacheWrite1hPrice = perToken(pricing.cacheWrite1hUsdPer1M) ?? cacheWritePrice
  const inputImagePrice = perToken(pricing.imageInputUsdPer1M)
  const outputImagePrice = perToken(pricing.imageOutputUsdPer1M)
  const inputAudioPrice = perToken(pricing.audioInputUsdPer1M)
  const outputAudioPrice = perToken(pricing.audioOutputUsdPer1M)
  const outputImageUnitPrice = pricing.outputUsdPerImage
  if (!hasAnyPrice(inputPrice, outputPrice, cachedInputPrice, cacheWritePrice, cacheWrite1hPrice, inputImagePrice, outputImagePrice, inputAudioPrice, outputAudioPrice, outputImageUnitPrice)) return undefined

  const cacheReadTokens = Math.max(input.cacheReadTokens ?? 0, 0)
  const cacheWriteTokens = Math.max(input.cacheWriteTokens ?? 0, 0)
  const cacheWrite1hTokens = normalizedCacheWrite1hTokens(input, cacheWriteTokens)
  const cacheWriteStandardTokens = Math.max(cacheWriteTokens - cacheWrite1hTokens, 0)
  const inputImageTokens = inputImagePrice === undefined ? 0 : Math.max(input.inputImageTokens ?? 0, 0)
  const outputImageTokens = outputImagePrice === undefined ? 0 : Math.max(input.outputImageTokens ?? defaultImageOutputTokens(input, pricing), 0)
  const inputAudioTokens = inputAudioPrice === undefined ? 0 : Math.max(input.inputAudioTokens ?? defaultInputAudioTokens(input, inputPrice, cacheReadTokens, inputImageTokens), 0)
  const outputAudioTokens = outputAudioPrice === undefined ? 0 : Math.max(input.outputAudioTokens ?? defaultOutputAudioTokens(input, outputPrice, outputImageTokens), 0)
  const outputImageCount = outputImageUnitPrice === undefined ? 0 : Math.max(input.outputImageCount ?? 0, 0)
  const cacheReadTokensIncludedInInput = usesIncludedCacheReadUsage(input.providerCode) ? cacheReadTokens : 0
  const uncachedInputTokens = Math.max((input.inputTokens ?? 0) - cacheReadTokensIncludedInInput - inputImageTokens - inputAudioTokens, 0)
  const outputTokens = Math.max((input.outputTokens ?? 0) - outputImageTokens - outputAudioTokens, 0)
  const inputCostUsd = inputPrice === undefined ? undefined : roundCost(uncachedInputTokens * inputPrice)
  const outputCostUsd = outputPrice === undefined ? undefined : roundCost(outputTokens * outputPrice)
  const cacheReadCostUsd = cachedInputPrice === undefined ? undefined : roundCost(cacheReadTokens * cachedInputPrice)
  const cacheWriteCostUsd = cacheWriteStandardTokens > 0 && cacheWritePrice !== undefined ? roundCost(cacheWriteStandardTokens * cacheWritePrice) : undefined
  const cacheWrite1hCostUsd = cacheWrite1hTokens > 0 && cacheWrite1hPrice !== undefined ? roundCost(cacheWrite1hTokens * cacheWrite1hPrice) : undefined
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
    cacheWriteCostUsd,
    cacheWriteUsdPer1M: pricing.cacheWriteUsdPer1M,
    cacheWrite1hCostUsd,
    cacheWrite1hUsdPer1M: pricing.cacheWrite1hUsdPer1M ?? pricing.cacheWriteUsdPer1M,
    thinkingTokens: input.thinkingTokens,
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
    accountChargeUsd: input.costUsd ?? sumCostParts(inputCostUsd, outputCostUsd, cacheReadCostUsd, cacheWriteCostUsd, cacheWrite1hCostUsd, inputImageCostUsd, outputImageCostUsd, inputAudioCostUsd, outputAudioCostUsd, outputImageUnitCostUsd),
    multiplier: 1
  }
}

function resolveCatalogPricing(input: CostInput & { systemAccountId?: string }): ProviderModelCatalogItem | undefined {
  if (!input.model) return undefined
  const catalog = listProviderModelCatalog({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId
  })
  const item = findCatalogItem(catalog, input.model)
  if (!item) return undefined
  if (!item.pricingModel) return item
  return findCatalogItem(catalog, item.pricingModel)
}

async function resolveCatalogPricingAsync(input: CostInput & { systemAccountId?: string }): Promise<ProviderModelCatalogItem | undefined> {
  if (!input.model) return undefined
  const catalog = await listProviderModelCatalogAsync({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId
  })
  const item = findCatalogItem(catalog, input.model)
  if (!item) return undefined
  if (!item.pricingModel) return item
  return findCatalogItem(catalog, item.pricingModel)
}

function modelCatalogCacheKey(options: ModelCatalogListOptions): string {
  return [
    normalizeProviderToken(options.providerCode) ?? '',
    options.systemAccountId ?? '',
    options.includeInactive === true ? 'inactive' : 'active',
    options.includeUnpriced === true ? 'unpriced' : 'priced'
  ].join(':')
}

function cloneProviderModelCatalogItems(items: ProviderModelCatalogItem[]): ProviderModelCatalogItem[] {
  return items.map((item) => ({
    ...item,
    supportedApiProtocols: [...item.supportedApiProtocols]
  }))
}

function mergeModelCatalogItems(items: ProviderModelCatalogItem[]): ProviderModelCatalogItem[] {
  const merged = new Map<string, ProviderModelCatalogItem>()
  for (const item of items) {
    const key = item.model.trim()
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
    || item.cacheWrite1hUsdPer1M !== undefined
    || item.imageInputUsdPer1M !== undefined
    || item.imageOutputUsdPer1M !== undefined
    || item.audioInputUsdPer1M !== undefined
    || item.audioOutputUsdPer1M !== undefined
    || item.outputUsdPerImage !== undefined
}

function findCatalogItem(items: ProviderModelCatalogItem[], model: string): ProviderModelCatalogItem | undefined {
  const normalized = model.trim()
  return items.find((item) => item.model.trim() === normalized)
}

function toBuiltInCatalogItem(item: ProviderModelPricing): ProviderModelCatalogItem {
  return {
    ...item,
    scope: 'built_in',
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
    catalogVisible: true,
    source: item.scope === 'global' ? 'custom-global' : 'custom-personal',
    scope: item.scope,
    status: item.status,
    systemAccountId: item.systemAccountId,
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
    display_name: item.model,
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
  const sameProvider = normalizeProviderToken(left.providerCode) === normalizeProviderToken(right.providerCode)
  if (sameProvider) {
    const sameProviderCatalogOrder = compareSharedCatalogOrder(left.catalogOrder, right.catalogOrder)
    if (sameProviderCatalogOrder !== 0) return sameProviderCatalogOrder
  }

  const leftReleaseDate = sortableCatalogReleaseDate(left)
  const rightReleaseDate = sortableCatalogReleaseDate(right)
  if (leftReleaseDate && rightReleaseDate && leftReleaseDate !== rightReleaseDate) {
    return rightReleaseDate.localeCompare(leftReleaseDate)
  }
  if (leftReleaseDate && !rightReleaseDate) return -1
  if (!leftReleaseDate && rightReleaseDate) return 1

  if (!sameProvider) {
    const crossProviderCatalogOrder = compareSharedCatalogOrder(left.catalogOrder, right.catalogOrder)
    if (crossProviderCatalogOrder !== 0) return crossProviderCatalogOrder
  }

  const modelOrder = left.model.localeCompare(right.model, 'en')
  if (modelOrder !== 0) return modelOrder
  return (left.id ?? '').localeCompare(right.id ?? '', 'en')
}

function compareSharedCatalogOrder(left?: number, right?: number): number {
  if (left !== undefined && right !== undefined && left !== right) {
    return left - right
  }
  return 0
}

function modelCatalogSourceProviderCodes(providerCode: string): string[] {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return []
  if (normalizedProviderCode !== OPENAI_COMPATIBLE_PROVIDER_CODE) return [normalizedProviderCode]
  if (runtimeConfig.databaseDriver === 'postgres') return [...postgresSyncOpenAIProtocolProviderCodes]

  const openAIProtocolProviderCodes = listOpenAIProtocolProviderCodes()
    .map((code) => normalizeProviderToken(code))
    .filter((code): code is string => Boolean(code))
  const childCodes = openAIProtocolProviderCodes.filter((code) => code !== normalizedProviderCode)
  return [...new Set([...childCodes, normalizedProviderCode])]
}

async function modelCatalogSourceProviderCodesAsync(providerCode: string): Promise<string[]> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return []
  if (normalizedProviderCode !== OPENAI_COMPATIBLE_PROVIDER_CODE) return [normalizedProviderCode]

  const openAIProtocolProviderCodes = (await listOpenAIProtocolProviderCodesAsync())
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
    || input.cacheWriteTokens !== undefined
    || input.cacheWrite1hTokens !== undefined
    || input.inputImageTokens !== undefined
    || input.outputImageTokens !== undefined
    || input.inputAudioTokens !== undefined
    || input.outputAudioTokens !== undefined
    || input.outputImageCount !== undefined
}

function hasAnyPrice(...prices: Array<number | undefined>): boolean {
  return prices.some((price) => price !== undefined)
}

function normalizedCacheWrite1hTokens(input: CostInput, cacheWriteTokens: number): number {
  const cacheWrite1hTokens = Math.max(input.cacheWrite1hTokens ?? 0, 0)
  return cacheWriteTokens > 0 ? Math.min(cacheWrite1hTokens, cacheWriteTokens) : cacheWrite1hTokens
}

function usesIncludedCacheReadUsage(providerCode: string): boolean {
  return modelPricingProviderDriverForProvider(providerCode)?.usesIncludedCacheReadUsage ?? true
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

async function getProviderModelCatalogSharedCacheEntry(cacheKey: string): Promise<ProviderModelCatalogItem[] | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis') return undefined
  try {
    const cached = await providerModelCatalogSharedCache.get(cacheKey)
    return cached ? cloneProviderModelCatalogItems(cached) : undefined
  } catch (error) {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'model_catalog_shared_cache_read_failed'
    }), '读取模型目录 Redis 共享缓存失败')
    return undefined
  }
}

async function setProviderModelCatalogCacheEntryAsync(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  const cached = cloneProviderModelCatalogItems(value)
  await setProviderModelCatalogSharedCacheEntry(cacheKey, cached)
  providerModelCatalogCache.set(cacheKey, cloneProviderModelCatalogItems(cached))
}

async function setProviderModelCatalogSharedCacheEntry(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  try {
    await providerModelCatalogSharedCache.set(cacheKey, cloneProviderModelCatalogItems(value), { ttlMs: modelCatalogCacheTtlMs })
  } catch (error) {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'model_catalog_shared_cache_write_failed'
    }), '写入模型目录 Redis 共享缓存失败')
  }
}

function clearProviderModelCatalogSharedCache(): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  void providerModelCatalogSharedCache.clear().catch((error) => {
    throwIfRedisCacheIsRequired(error)
    logger.warn(errorLogFields(error, {
      event: 'model_catalog_shared_cache_clear_failed'
    }), '清理模型目录 Redis 共享缓存失败')
  })
}

function clearProviderModelCatalogCaches(): void {
  providerModelCatalogCache.clear()
  clearProviderModelCatalogSharedCache()
}

registerGatewayRuntimeCacheInvalidator(clearProviderModelCatalogCaches)
