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
  getProviderModelPricing,
  type CostInput,
  type ModelPriceSet,
  type ProviderCostBreakdown,
  type ProviderModelApiProtocol,
  type ProviderModelPricing
} from './model-pricing.service.js'
import {
  listBuiltInProviderModels,
  listBuiltInProviderModelsAsync,
  type BuiltInProviderModelRecord
} from '../../storage/provider-model-catalog.repository.js'
import {
  DEEPSEEK_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GPT_VENDOR_CODE,
  HYBRID_PROVIDER_CODE,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  XAI_PROVIDER_CODE,
  normalizeProviderToken
} from '../../domain/provider-protocol.js'
import { usesOpenAICodexResponsesLite } from '../gateway/adapters/gpt-codex/client-headers.js'
import {
  listAnthropicProtocolProviderCodes,
  listAnthropicProtocolProviderCodesAsync,
  listGeminiProtocolProviderCodes,
  listGeminiProtocolProviderCodesAsync,
  listOpenAIProtocolProviderCodes,
  listOpenAIProtocolProviderCodesAsync
} from '../../storage/provider.repository.js'
import { createAppCache, createSharedJsonCache } from '../../shared/cache.js'
import { registerGatewayRuntimeCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { modelPricingProviderDriverForProvider } from './provider-driver.registry.js'
import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { requestSqliteReadWorker, sqliteReadWorkerPoolEnabled } from '../../storage/sqlite-read-worker-pool.js'

export type ModelCatalogScope = 'built_in' | CustomProviderModelScope

export interface ProviderModelCatalogItem extends Omit<ProviderModelPricing, 'defaultReasoningEffort'> {
  id?: string
  scope: ModelCatalogScope
  status: 'draft' | 'active' | 'disabled'
  defaultReasoningEffort: string | null
  systemAccountId?: string
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
  default_reasoning_level?: string | null
  supported_reasoning_levels?: CodexReasoningEffortPreset[]
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
  multi_agent_version: 'v2' | null
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
const pendingProviderModelCatalogLoads = new Map<string, Promise<ProviderModelCatalogItem[]>>()
let providerModelCatalogCacheGeneration = 0
let providerModelCatalogInvalidationInFlight: Promise<void> = Promise.resolve()
let providerModelCatalogSharedCacheUsable = true

const postgresSyncOpenAIProtocolProviderCodes = [
  GPT_VENDOR_CODE,
  DEEPSEEK_PROVIDER_CODE,
  GLM_PROVIDER_CODE,
  GEMINI_PROVIDER_CODE,
  XAI_PROVIDER_CODE,
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

export function listProviderModelCatalogReadOnly(options: ModelCatalogListOptions): ProviderModelCatalogItem[] {
  return cloneProviderModelCatalogItems(buildProviderModelCatalog(options))
}

export async function listProviderModelCatalogAsync(options: ModelCatalogListOptions): Promise<ProviderModelCatalogItem[]> {
  await providerModelCatalogInvalidationInFlight
  const cacheKey = modelCatalogCacheKey(options)
  if (runtimeConfig.cacheDriver !== 'redis') {
    const cached = providerModelCatalogCache.get(cacheKey)
    if (cached) {
      return cloneProviderModelCatalogItems(cached)
    }
  }
  const pending = pendingProviderModelCatalogLoads.get(cacheKey)
  if (pending) return cloneProviderModelCatalogItems(await pending)

  const generation = providerModelCatalogCacheGeneration
  const load = (async () => {
    const sharedCached = await getProviderModelCatalogSharedCacheEntry(cacheKey)
    if (sharedCached) {
      if (generation === providerModelCatalogCacheGeneration) {
        providerModelCatalogCache.set(cacheKey, cloneProviderModelCatalogItems(sharedCached))
      }
      return cloneProviderModelCatalogItems(sharedCached)
    }
    const catalog = sqliteReadWorkerPoolEnabled()
      ? await requestSqliteReadWorker({
          type: 'list_provider_model_catalog_read_only',
          options
        })
      : await buildProviderModelCatalogAsync(options)
    if (generation === providerModelCatalogCacheGeneration) {
      await setProviderModelCatalogCacheEntryAsync(cacheKey, cloneProviderModelCatalogItems(catalog))
    }
    return cloneProviderModelCatalogItems(catalog)
  })()
  pendingProviderModelCatalogLoads.set(cacheKey, load)
  try {
    return cloneProviderModelCatalogItems(await load)
  } finally {
    if (pendingProviderModelCatalogLoads.get(cacheKey) === load) {
      pendingProviderModelCatalogLoads.delete(cacheKey)
    }
  }
}

function buildProviderModelCatalog(options: ModelCatalogListOptions): ProviderModelCatalogItem[] {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式禁止同步构建模型目录，必须使用 buildProviderModelCatalogAsync')
  }
  const sourceProviderCodes = modelCatalogSourceProviderCodes(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const builtIn = listBuiltInProviderModels(builtInSourceProviderCodes).map(toBuiltInCatalogItem)
  const custom = sourceProviderCodes.flatMap((providerCode) => listCustomProviderModelsForCatalog({
      providerCode,
      systemAccountId: options.systemAccountId,
      includeInactive: options.includeInactive
    }).map(toCustomCatalogItem))
  const merged = mergeModelCatalogItems([...builtIn, ...custom], normalizeProviderToken(options.providerCode) === HYBRID_PROVIDER_CODE)

  return merged
    .filter((item) => options.includeInactive || item.status === 'active')
    .filter((item) => options.includeUnpriced || hasResolvablePrice(item, merged))
    .sort(compareProviderModelCatalogItems)
}

async function buildProviderModelCatalogAsync(options: ModelCatalogListOptions): Promise<ProviderModelCatalogItem[]> {
  const sourceProviderCodes = await modelCatalogSourceProviderCodesAsync(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const builtIn = (await listBuiltInProviderModelsAsync(builtInSourceProviderCodes)).map(toBuiltInCatalogItem)
  const customCatalogs = await Promise.all(sourceProviderCodes.map((providerCode) => listCustomProviderModelsForCatalogAsync({
    providerCode,
    systemAccountId: options.systemAccountId,
    includeInactive: options.includeInactive
  })))
  const custom = customCatalogs.flatMap((items) => items.map(toCustomCatalogItem))
  const merged = mergeModelCatalogItems([...builtIn, ...custom], normalizeProviderToken(options.providerCode) === HYBRID_PROVIDER_CODE)

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
  const prices = effectiveCatalogTokenPrices(pricing, input)
  const cachedInputPrice = prices.cachedInputPrice ?? prices.inputPrice
  if (cachedInputPrice === undefined) return undefined
  return roundCost(Math.max(input.cacheReadTokens, 0) * cachedInputPrice)
}

export async function estimateCatalogCacheReadCostUsdAsync(input: CostInput & { systemAccountId?: string }): Promise<number | undefined> {
  const pricing = await resolveCatalogPricingAsync(input)
  if (!pricing || input.cacheReadTokens === undefined) return undefined
  const prices = effectiveCatalogTokenPrices(pricing, input)
  const cachedInputPrice = prices.cachedInputPrice ?? prices.inputPrice
  if (cachedInputPrice === undefined) return undefined
  return roundCost(Math.max(input.cacheReadTokens, 0) * cachedInputPrice)
}

export function estimateCatalogCacheWriteCostUsd(input: CostInput & { systemAccountId?: string }): number | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing || (input.cacheWriteTokens === undefined && input.cacheWrite1hTokens === undefined)) return undefined
  const prices = effectiveCatalogTokenPrices(pricing, input)
  const cacheWritePrice = prices.cacheWritePrice
  const cacheWrite1hPrice = prices.cacheWrite1hPrice ?? cacheWritePrice
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
  const prices = effectiveCatalogTokenPrices(pricing, input)
  const cacheWritePrice = prices.cacheWritePrice
  const cacheWrite1hPrice = prices.cacheWrite1hPrice ?? cacheWritePrice
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

export function buildCatalogCostBreakdownFromPricing(
  pricing: ProviderModelCatalogItem,
  input: CostInput & { systemAccountId?: string; costUsd?: number }
): ProviderCostBreakdown | undefined {
  const tokenPrices = effectiveCatalogTokenPrices(pricing, input)
  const inputPrice = tokenPrices.inputPrice
  const outputPrice = tokenPrices.outputPrice
  const cachedInputPrice = tokenPrices.cachedInputPrice ?? inputPrice
  const cacheWritePrice = tokenPrices.cacheWritePrice
  const cacheWrite1hPrice = tokenPrices.cacheWrite1hPrice ?? cacheWritePrice
  const inputImagePrice = perToken(pricing.imageInputUsdPer1M)
  const outputImagePrice = tokenPrices.outputImagePrice
  const inputAudioPrice = tokenPrices.inputAudioPrice
  const outputAudioPrice = tokenPrices.outputAudioPrice
  const outputImageUnitPrice = tokenPrices.outputImageUnitPrice
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
  if (hasUnpricedTokenUsage({
    uncachedInputTokens,
    inputPrice,
    outputTokens,
    outputPrice,
    cacheReadTokens,
    cachedInputPrice,
    cacheWriteStandardTokens,
    cacheWritePrice,
    cacheWrite1hTokens,
    cacheWrite1hPrice
  })) return undefined
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
    inputUsdPer1M: perMillion(inputPrice),
    outputUsdPer1M: perMillion(outputPrice),
    cacheReadCostUsd,
    cacheReadUsdPer1M: perMillion(cachedInputPrice),
    cacheWriteCostUsd,
    cacheWriteUsdPer1M: perMillion(cacheWritePrice),
    cacheWrite1hCostUsd,
    cacheWrite1hUsdPer1M: perMillion(cacheWrite1hPrice),
    thinkingTokens: input.thinkingTokens,
    inputImageCostUsd,
    outputImageCostUsd,
    inputImageUsdPer1M: perMillion(inputImagePrice),
    outputImageUsdPer1M: perMillion(outputImagePrice),
    inputAudioCostUsd,
    outputAudioCostUsd,
    inputAudioUsdPer1M: perMillion(inputAudioPrice),
    outputAudioUsdPer1M: perMillion(outputAudioPrice),
    outputImageUnitCostUsd,
    outputUsdPerImage: outputImageUnitPrice,
    accountChargeUsd: input.costUsd ?? sumCostParts(inputCostUsd, outputCostUsd, cacheReadCostUsd, cacheWriteCostUsd, cacheWrite1hCostUsd, inputImageCostUsd, outputImageCostUsd, inputAudioCostUsd, outputAudioCostUsd, outputImageUnitCostUsd),
    multiplier: 1,
    serviceTierPricingSource: tokenPrices.serviceTierPricingSource,
    serviceTierMultiplier: tokenPrices.serviceTierMultiplier
  }
}

function resolveCatalogPricing(input: CostInput & { systemAccountId?: string }): ProviderModelCatalogItem | undefined {
  if (!input.model) return undefined
  const catalog = listProviderModelCatalog({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId
  })
  return findCatalogItem(catalog, input.model)
}

async function resolveCatalogPricingAsync(input: CostInput & { systemAccountId?: string }): Promise<ProviderModelCatalogItem | undefined> {
  if (!input.model) return undefined
  const catalog = await listProviderModelCatalogAsync({
    providerCode: input.providerCode,
    systemAccountId: input.systemAccountId
  })
  return findCatalogItem(catalog, input.model)
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
    defaultReasoningEffort: item.defaultReasoningEffort ?? null,
    supportedApiProtocols: [...item.supportedApiProtocols],
    inputModalities: [...(item.inputModalities ?? [])],
    outputModalities: [...(item.outputModalities ?? [])],
    supportedTools: [...(item.supportedTools ?? [])],
    serviceTierPrices: cloneServiceTierPrices(item.serviceTierPrices),
    supportedServiceTiers: [...item.supportedServiceTiers],
    supportedReasoningEfforts: [...item.supportedReasoningEfforts],
    codexSupportedReasoningLevels: [...item.codexSupportedReasoningLevels]
  }))
}

function mergeModelCatalogItems(items: ProviderModelCatalogItem[], preserveProviderIdentity = false): ProviderModelCatalogItem[] {
  const merged = new Map<string, ProviderModelCatalogItem>()
  for (const item of items) {
    const model = item.model.trim()
    if (!model) continue
    const key = preserveProviderIdentity
      ? `${normalizeProviderToken(item.providerCode) ?? ''}\n${model}`
      : model
    const previous = merged.get(key)
    if (!previous || catalogPriority(item) >= catalogPriority(previous)) {
      merged.set(key, item)
    }
  }
  return [...merged.values()]
}

function hasResolvablePrice(item: ProviderModelCatalogItem, _allItems: ProviderModelCatalogItem[]): boolean {
  return hasDirectPrice(item)
}

function hasDirectPrice(item: Omit<ProviderModelPricing, 'defaultReasoningEffort'>): boolean {
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
    || Object.keys(item.serviceTierPrices ?? {}).length > 0
}

export function findCatalogItem(items: ProviderModelCatalogItem[], model: string): ProviderModelCatalogItem | undefined {
  const normalized = model.trim()
  return items.find((item) => item.model.trim() === normalized)
}

function toBuiltInCatalogItem(item: BuiltInProviderModelRecord): ProviderModelCatalogItem {
  const staticCapabilities = getProviderModelPricing(item.providerCode, item.model)
  return {
    ...item,
    defaultReasoningEffort: item.defaultReasoningEffort ?? null,
    scope: 'built_in',
    status: item.status,
    inputModalities: [...(item.inputModalities?.length ? item.inputModalities : staticCapabilities?.inputModalities ?? [])],
    outputModalities: [...(item.outputModalities?.length ? item.outputModalities : staticCapabilities?.outputModalities ?? [])],
    supportedTools: [...(item.supportedTools?.length ? item.supportedTools : staticCapabilities?.supportedTools ?? [])],
    supportedServiceTiers: [...(item.supportedServiceTiers ?? [])],
    supportedReasoningEfforts: [...(item.supportedReasoningEfforts ?? [])],
    codexSupportedReasoningLevels: [...(item.codexSupportedReasoningLevels ?? [])],
    supportsServiceTier: (item.supportedServiceTiers?.length ?? 0) > 0
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
    inputModalities: [],
    outputModalities: [],
    supportedTools: [],
    inputUsdPer1M: item.inputUsdPer1M,
    outputUsdPer1M: item.outputUsdPer1M,
    cachedInputUsdPer1M: item.cachedInputUsdPer1M,
    cacheWriteUsdPer1M: item.cacheWriteUsdPer1M,
    cacheWrite1hUsdPer1M: item.cacheWrite1hUsdPer1M,
    serviceTierPrices: cloneServiceTierPrices(item.serviceTierPrices),
    imageInputUsdPer1M: item.imageInputUsdPer1M,
    imageOutputUsdPer1M: item.imageOutputUsdPer1M,
    audioInputUsdPer1M: item.audioInputUsdPer1M,
    audioOutputUsdPer1M: item.audioOutputUsdPer1M,
    outputUsdPerImage: item.outputUsdPerImage,
    maxInputTokens: item.maxInputTokens,
    maxOutputTokens: item.maxOutputTokens,
    supportsPromptCaching: item.cachedInputUsdPer1M !== undefined,
    supportedServiceTiers: [...item.supportedServiceTiers],
    supportedReasoningEfforts: [...item.supportedReasoningEfforts],
    defaultReasoningEffort: item.defaultReasoningEffort ?? null,
    codexSupportedReasoningLevels: [],
    supportsServiceTier: item.supportedServiceTiers.length > 0,
    catalogVisible: true,
    source: item.scope === 'global' ? 'custom-global' : 'custom-personal',
    scope: item.scope,
    status: item.status,
    systemAccountId: item.systemAccountId,
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
  const supportedReasoningLevels = item.codexSupportedReasoningLevels.map((effort) => ({
    effort,
    description: codexReasoningLevelDescription(effort)
  }))
  const serviceTiers = item.supportedServiceTiers.map((tier) => ({
    id: tier,
    name: tier === 'priority' ? 'Fast' : 'Flex',
    description: tier === 'priority' ? 'Priority processing' : 'Flex processing'
  }))
  const codexDefaultReasoningLevel = item.codexDefaultReasoningLevel
  const reasoningMetadata: Pick<CodexModelListItem, 'default_reasoning_level' | 'supported_reasoning_levels'> = supportedReasoningLevels.length
    ? {
        ...(codexDefaultReasoningLevel && supportedReasoningLevels.some((level) => level.effort === codexDefaultReasoningLevel)
          ? { default_reasoning_level: codexDefaultReasoningLevel }
          : {}),
        supported_reasoning_levels: supportedReasoningLevels
      }
    : {}
  return {
    slug: item.model,
    display_name: item.model,
    description: item.capabilityNotes || item.pricingNotes || item.notes || null,
    ...reasoningMetadata,
    shell_type: 'shell_command',
    visibility: 'list',
    supported_in_api: true,
    priority: index,
    additional_speed_tiers: item.supportedServiceTiers.includes('priority') ? ['fast'] : [],
    service_tiers: serviceTiers,
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
    use_responses_lite: usesOpenAICodexResponsesLite(item.model),
    auto_review_model_override: null,
    tool_mode: null,
    multi_agent_version: item.codexMultiAgentVersion ?? null
  }
}

function codexReasoningLevelDescription(level: string): string {
  switch (level) {
    case 'none':
      return 'None'
    case 'minimal':
      return 'Minimal'
    case 'low':
      return 'Low'
    case 'medium':
      return 'Medium'
    case 'high':
      return 'High'
    case 'xhigh':
      return 'Extra High'
    case 'max':
      return 'Max'
    case 'ultra':
      return 'Ultra'
    default:
      return level
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
  if (normalizedProviderCode === OPENAI_COMPATIBLE_PROVIDER_CODE && runtimeConfig.databaseDriver === 'postgres') {
    return [...postgresSyncOpenAIProtocolProviderCodes]
  }
  if (normalizedProviderCode === HYBRID_PROVIDER_CODE) {
    return [...new Set([
      ...listOpenAIProtocolProviderCodes(),
      ...listAnthropicProtocolProviderCodes(),
      ...listGeminiProtocolProviderCodes()
    ].map(normalizeProviderToken).filter((code): code is string => Boolean(code) && code !== HYBRID_PROVIDER_CODE))]
  }
  if (normalizedProviderCode !== OPENAI_COMPATIBLE_PROVIDER_CODE) return [normalizedProviderCode]
  const openAIProtocolProviderCodes = listOpenAIProtocolProviderCodes()
    .map((code) => normalizeProviderToken(code))
    .filter((code): code is string => Boolean(code))
  const childCodes = openAIProtocolProviderCodes.filter((code) => code !== normalizedProviderCode)
  return [...new Set([...childCodes, normalizedProviderCode])]
}

async function modelCatalogSourceProviderCodesAsync(providerCode: string): Promise<string[]> {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return []
  if (normalizedProviderCode === HYBRID_PROVIDER_CODE) {
    const providerCodes = await Promise.all([
      listOpenAIProtocolProviderCodesAsync(),
      listAnthropicProtocolProviderCodesAsync(),
      listGeminiProtocolProviderCodesAsync()
    ])
    return [...new Set(providerCodes.flat().map(normalizeProviderToken).filter((code): code is string => Boolean(code) && code !== HYBRID_PROVIDER_CODE))]
  }
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

function defaultImageOutputTokens(
  input: CostInput,
  pricing: Pick<ProviderModelPricing, 'mode' | 'imageOutputUsdPer1M'>
): number {
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

function hasUnpricedTokenUsage(input: {
  uncachedInputTokens: number
  inputPrice?: number
  outputTokens: number
  outputPrice?: number
  cacheReadTokens: number
  cachedInputPrice?: number
  cacheWriteStandardTokens: number
  cacheWritePrice?: number
  cacheWrite1hTokens: number
  cacheWrite1hPrice?: number
}): boolean {
  return (input.uncachedInputTokens > 0 && input.inputPrice === undefined)
    || (input.outputTokens > 0 && input.outputPrice === undefined)
    || (input.cacheReadTokens > 0 && input.cachedInputPrice === undefined)
    || (input.cacheWriteStandardTokens > 0 && input.cacheWritePrice === undefined)
    || (input.cacheWrite1hTokens > 0 && input.cacheWrite1hPrice === undefined)
}

function normalizedCacheWrite1hTokens(input: CostInput, cacheWriteTokens: number): number {
  const cacheWrite1hTokens = Math.max(input.cacheWrite1hTokens ?? 0, 0)
  return cacheWriteTokens > 0 ? Math.min(cacheWrite1hTokens, cacheWriteTokens) : cacheWrite1hTokens
}

function effectiveCatalogTokenPrices(pricing: ProviderModelCatalogItem, input: CostInput): {
  inputPrice?: number
  outputPrice?: number
  cachedInputPrice?: number
  cacheWritePrice?: number
  cacheWrite1hPrice?: number
  inputImagePrice?: number
  outputImagePrice?: number
  inputAudioPrice?: number
  outputAudioPrice?: number
  outputImageUnitPrice?: number
  serviceTierPricingSource: ProviderCostBreakdown['serviceTierPricingSource']
  serviceTierMultiplier?: number
} {
  const tier = input.serviceTier
  const tierKey = typeof tier === 'string' && tier !== 'default' && tier !== 'standard' ? tier : undefined
  const tierSupported = tierKey !== undefined && pricing.supportedServiceTiers.some((supported) => supported === tierKey)
  const tierPrices = tierSupported ? pricing.serviceTierPrices?.[tierKey] : undefined
  const selectPrice = (standard: number | undefined, specific: number | undefined): number | undefined =>
    tierKey ? specific : standard
  const inputTokens = Math.max(input.inputTokens ?? 0, 0)
  const longContext = pricing.longContextInputTokenThreshold !== undefined
    && (pricing.longContextInputTokenThresholdInclusive
      ? inputTokens >= pricing.longContextInputTokenThreshold
      : inputTokens > pricing.longContextInputTokenThreshold)
  const inputMultiplier = longContext ? normalizeCatalogMultiplier(pricing.longContextInputCostMultiplier) : 1
  const outputMultiplier = longContext ? normalizeCatalogMultiplier(pricing.longContextOutputCostMultiplier) : 1
  const serviceTierPricing = catalogServiceTierPricingMetadata(pricing, input, tierSupported)
  return {
    inputPrice: perToken(multiplyCatalogPrice(selectPrice(pricing.inputUsdPer1M, tierPrices?.inputUsdPer1M), inputMultiplier)),
    outputPrice: perToken(multiplyCatalogPrice(selectPrice(pricing.outputUsdPer1M, tierPrices?.outputUsdPer1M), outputMultiplier)),
    cachedInputPrice: perToken(multiplyCatalogPrice(selectPrice(pricing.cachedInputUsdPer1M, tierPrices?.cachedInputUsdPer1M), inputMultiplier)),
    cacheWritePrice: perToken(multiplyCatalogPrice(selectPrice(pricing.cacheWriteUsdPer1M, tierPrices?.cacheWriteUsdPer1M), inputMultiplier)),
    cacheWrite1hPrice: perToken(multiplyCatalogPrice(selectPrice(pricing.cacheWrite1hUsdPer1M, tierPrices?.cacheWrite1hUsdPer1M), inputMultiplier)),
    inputImagePrice: perToken(pricing.imageInputUsdPer1M),
    outputImagePrice: perToken(pricing.imageOutputUsdPer1M),
    inputAudioPrice: perToken(selectPrice(pricing.audioInputUsdPer1M, tierPrices?.audioInputUsdPer1M)),
    outputAudioPrice: perToken(selectPrice(pricing.audioOutputUsdPer1M, tierPrices?.audioOutputUsdPer1M)),
    outputImageUnitPrice: pricing.outputUsdPerImage,
    ...serviceTierPricing
  }
}

function catalogServiceTierPricingMetadata(
  pricing: ProviderModelCatalogItem,
  input: CostInput,
  tierSupported: boolean
): Pick<ProviderCostBreakdown, 'serviceTierPricingSource' | 'serviceTierMultiplier'> {
  const tier = input.serviceTier
  if (tier === undefined || tier === 'default' || tier === 'standard') {
    return { serviceTierPricingSource: 'default' }
  }
  if (!tierSupported) return { serviceTierPricingSource: 'unknown' }
  const tierPrices = pricing.serviceTierPrices?.[tier]
  const pairs = [
    [pricing.inputUsdPer1M, tierPrices?.inputUsdPer1M],
    [pricing.outputUsdPer1M, tierPrices?.outputUsdPer1M],
    [pricing.cachedInputUsdPer1M, tierPrices?.cachedInputUsdPer1M],
    [pricing.cacheWriteUsdPer1M, tierPrices?.cacheWriteUsdPer1M],
    [pricing.cacheWrite1hUsdPer1M, tierPrices?.cacheWrite1hUsdPer1M],
    [pricing.audioInputUsdPer1M, tierPrices?.audioInputUsdPer1M],
    [pricing.audioOutputUsdPer1M, tierPrices?.audioOutputUsdPer1M]
  ] as const
  let specificCount = 0
  let missingSpecificCount = 0
  for (const [standard, specific] of pairs) {
    if (specific !== undefined) {
      specificCount += 1
    } else if (standard !== undefined) {
      missingSpecificCount += 1
    }
  }
  if (specificCount > 0 && missingSpecificCount === 0) {
    return { serviceTierPricingSource: 'tier_specific' }
  }
  return { serviceTierPricingSource: 'unknown' }
}

function cloneServiceTierPrices(value?: Record<string, ModelPriceSet>): Record<string, ModelPriceSet> | undefined {
  if (!value) return undefined
  return Object.fromEntries(Object.entries(value).map(([tier, prices]) => [tier, { ...prices }]))
}

function multiplyCatalogPrice(value: number | undefined, multiplier: number): number | undefined {
  return value === undefined ? undefined : value * multiplier
}

function normalizeCatalogMultiplier(value: number | undefined, fallback = 1): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : fallback
}

function usesIncludedCacheReadUsage(providerCode: string): boolean {
  return modelPricingProviderDriverForProvider(providerCode)?.usesIncludedCacheReadUsage ?? true
}

function perToken(value: number | undefined): number | undefined {
  return value === undefined ? undefined : value / 1_000_000
}

function perMillion(value: number | undefined): number | undefined {
  return value === undefined ? undefined : Number((value * 1_000_000).toFixed(8))
}

function roundCost(value: number): number {
  return Number(value.toFixed(10))
}

function sumCostParts(...parts: Array<number | undefined>): number | undefined {
  const values = parts.filter((part): part is number => part !== undefined)
  return values.length ? roundCost(values.reduce((sum, part) => sum + part, 0)) : undefined
}

async function getProviderModelCatalogSharedCacheEntry(cacheKey: string): Promise<ProviderModelCatalogItem[] | undefined> {
  if (runtimeConfig.cacheDriver !== 'redis' || !providerModelCatalogSharedCacheUsable) return undefined
  const cached = await providerModelCatalogSharedCache.get(cacheKey)
  return cached ? cloneProviderModelCatalogItems(cached) : undefined
}

async function setProviderModelCatalogCacheEntryAsync(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  const cached = cloneProviderModelCatalogItems(value)
  await setProviderModelCatalogSharedCacheEntry(cacheKey, cached)
  providerModelCatalogCache.set(cacheKey, cloneProviderModelCatalogItems(cached))
}

async function setProviderModelCatalogSharedCacheEntry(cacheKey: string, value: ProviderModelCatalogItem[]): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await providerModelCatalogSharedCache.set(cacheKey, cloneProviderModelCatalogItems(value), { ttlMs: modelCatalogCacheTtlMs })
  providerModelCatalogSharedCacheUsable = true
}

async function clearProviderModelCatalogSharedCacheAsync(): Promise<void> {
  if (runtimeConfig.cacheDriver !== 'redis') return
  await providerModelCatalogSharedCache.clear()
}

async function clearProviderModelCatalogCaches(): Promise<void> {
  providerModelCatalogCacheGeneration += 1
  providerModelCatalogCache.clear()
  const pendingLoads = [...pendingProviderModelCatalogLoads.values()]
  const previous = providerModelCatalogInvalidationInFlight
  const operation = (async () => {
    await previous
    await Promise.allSettled(pendingLoads)
    providerModelCatalogCache.clear()
    try {
      await clearProviderModelCatalogSharedCacheAsync()
      providerModelCatalogSharedCacheUsable = true
    } catch (error) {
      providerModelCatalogSharedCacheUsable = false
      logger.warn(errorLogFields(error, {
        event: 'provider_model_catalog_shared_cache_clear_failed',
        cacheName: providerModelCatalogSharedCache.name
      }), '供应商模型目录 Redis shared cache 清理失败')
      throw error
    }
  })()
  providerModelCatalogInvalidationInFlight = operation.catch(() => undefined)
  return await operation
}

registerGatewayRuntimeCacheInvalidator(clearProviderModelCatalogCaches)
