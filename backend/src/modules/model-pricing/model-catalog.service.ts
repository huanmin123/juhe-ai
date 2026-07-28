import {
  customProviderModelAccountBindingSummary,
  customProviderModelAccountBindingSummaryAsync,
  deleteCustomProviderModel,
  deleteCustomProviderModelAsync,
  findCustomProviderModelById,
  findCustomProviderModelByIdAsync,
  findCustomProviderModelPatchStateAsync,
  findCustomProviderModelTestCatalogAsync,
  listCustomProviderModelsForCatalog,
  listCustomProviderModelsForCatalogAsync,
  listCustomProviderModelTestCatalogAsync,
  patchCustomProviderModelAsync,
  upsertCustomProviderModel,
  upsertCustomProviderModelAsync,
  type CustomProviderModelAccountBindingSummary,
  type CustomProviderModelRecord,
  type CustomProviderModelPatchField,
  type CustomProviderModelPatchState,
  type CustomProviderModelPatchOutcome,
  type CustomProviderModelTestCatalogRecord,
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
  findBuiltInProviderModelTestCatalogAsync,
  listBuiltInProviderModels,
  listBuiltInProviderModelsAsync,
  listBuiltInProviderModelTestCatalogAsync,
  type BuiltInProviderModelRecord,
  type ProviderModelTestCatalogRecord
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
import { shouldInvalidateProviderModelCatalog } from '../gateway/response/model-catalog-cache-policy.js'
import { buildProviderBillingCostBreakdown, buildProviderCatalogDisplay } from './provider-billing.service.js'
import type { ProviderCatalogDisplaySection } from './provider-billing.types.js'
import { runtimeConfig } from '../../config/runtime.js'
import { generationParameterCapabilitiesForModel, limitGenerationParameterMaxOutputTokens } from '../chat/chat-generation-parameters.js'
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
  catalogDisplay?: ProviderCatalogDisplaySection[]
}

export interface ProviderModelTestCatalogItem {
  id: string
  providerCode: string
  model: string
  scope: ModelCatalogScope
  mode?: string
  catalogOrder?: number
  releaseDate?: string
  supportedApiProtocols: ProviderModelApiProtocol[]
  supportedServiceTiers: string[]
  supportedReasoningEfforts: string[]
  defaultReasoningEffort?: string
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

const modelCatalogCacheTtlMs = 24 * 60 * 60_000
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

export async function listProviderModelTestCatalogAsync(options: {
  providerCode: string
  systemAccountId?: string
}): Promise<ProviderModelTestCatalogItem[]> {
  const sourceProviderCodes = await modelCatalogSourceProviderCodesAsync(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const [builtIn, custom] = await Promise.all([
    listBuiltInProviderModelTestCatalogAsync(builtInSourceProviderCodes),
    listCustomProviderModelTestCatalogAsync({
      providerCodes: sourceProviderCodes,
      systemAccountId: options.systemAccountId
    })
  ])
  return mergeProviderModelTestCatalogItems([
    ...builtIn.map(providerModelTestCatalogItemFromBuiltIn),
    ...custom.map(providerModelTestCatalogItemFromCustom)
  ], normalizeProviderToken(options.providerCode) === HYBRID_PROVIDER_CODE)
    .sort(compareProviderModelTestCatalogItems)
}

export async function findProviderModelTestCatalogItemAsync(options: {
  providerCode: string
  systemAccountId?: string
  model: string
  protocolsOnly?: boolean
}): Promise<ProviderModelTestCatalogItem | undefined> {
  const model = options.model.trim()
  if (!model) return undefined
  const sourceProviderCodes = await modelCatalogSourceProviderCodesAsync(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const [builtIn, custom] = await Promise.all([
    findBuiltInProviderModelTestCatalogAsync(builtInSourceProviderCodes, model, options.protocolsOnly ? 'protocols' : 'test'),
    findCustomProviderModelTestCatalogAsync({
      providerCodes: sourceProviderCodes,
      systemAccountId: options.systemAccountId,
      model,
      ...(options.protocolsOnly ? { projection: 'protocols' as const } : {})
    })
  ])
  return mergeProviderModelTestCatalogItems([
    ...builtIn.map(providerModelTestCatalogItemFromBuiltIn),
    ...custom.map(providerModelTestCatalogItemFromCustom)
  ], normalizeProviderToken(options.providerCode) === HYBRID_PROVIDER_CODE)
    .sort(compareProviderModelTestCatalogItems)[0]
}

export async function findProviderModelCapabilitiesAsync(options: {
  providerCode: string
  systemAccountId?: string
  model: string
}): Promise<{
  id: string
  name: string
  supportedApiProtocols: ProviderModelApiProtocol[]
  supportedServiceTiers: string[]
  supportedReasoningEfforts: string[]
  defaultReasoningEffort?: string
} | undefined> {
  const item = await findProviderModelTestCatalogItemAsync(options)
  if (!item) return undefined
  return {
    id: item.model,
    name: item.model,
    supportedApiProtocols: [...item.supportedApiProtocols],
    supportedServiceTiers: [...item.supportedServiceTiers],
    supportedReasoningEfforts: [...item.supportedReasoningEfforts],
    ...(item.defaultReasoningEffort ? { defaultReasoningEffort: item.defaultReasoningEffort } : {})
  }
}

function buildProviderModelCatalog(options: ModelCatalogListOptions): ProviderModelCatalogItem[] {
  if (runtimeConfig.databaseDriver === 'postgres') {
    throw new Error('PostgreSQL 模式禁止同步构建模型目录，必须使用 buildProviderModelCatalogAsync')
  }
  const sourceProviderCodes = modelCatalogSourceProviderCodes(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const builtIn = listBuiltInProviderModels(builtInSourceProviderCodes, {
    includeInactive: options.includeInactive
  }).map(toBuiltInCatalogItem)
  const custom = sourceProviderCodes.flatMap((providerCode) => listCustomProviderModelsForCatalog({
      providerCode,
      systemAccountId: options.systemAccountId,
      includeInactive: options.includeInactive
    }).map(toCustomCatalogItem))
  const merged = mergeModelCatalogItems([...builtIn, ...custom], normalizeProviderToken(options.providerCode) === HYBRID_PROVIDER_CODE)

  return merged
    .filter(isSupportedCatalogModel)
    .filter((item) => options.includeInactive || item.status === 'active')
    .filter((item) => options.includeUnpriced || hasResolvablePrice(item, merged))
    .sort(compareProviderModelCatalogItems)
    .map(withCatalogDisplay)
}

async function buildProviderModelCatalogAsync(options: ModelCatalogListOptions): Promise<ProviderModelCatalogItem[]> {
  const sourceProviderCodes = await modelCatalogSourceProviderCodesAsync(options.providerCode)
  const builtInSourceProviderCodes = modelCatalogBuiltInSourceProviderCodes(options.providerCode, sourceProviderCodes)
  const builtIn = (await listBuiltInProviderModelsAsync(builtInSourceProviderCodes, {
    includeInactive: options.includeInactive
  })).map(toBuiltInCatalogItem)
  const customCatalogs = await Promise.all(sourceProviderCodes.map((providerCode) => listCustomProviderModelsForCatalogAsync({
    providerCode,
    systemAccountId: options.systemAccountId,
    includeInactive: options.includeInactive
  })))
  const custom = customCatalogs.flatMap((items) => items.map(toCustomCatalogItem))
  const merged = mergeModelCatalogItems([...builtIn, ...custom], normalizeProviderToken(options.providerCode) === HYBRID_PROVIDER_CODE)

  return merged
    .filter(isSupportedCatalogModel)
    .filter((item) => options.includeInactive || item.status === 'active')
    .filter((item) => options.includeUnpriced || hasResolvablePrice(item, merged))
    .sort(compareProviderModelCatalogItems)
    .map(withCatalogDisplay)
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

export async function findCustomProviderModelPatchState(
  id: string,
  submitted: Record<string, unknown>
): Promise<CustomProviderModelPatchState | undefined> {
  return findCustomProviderModelPatchStateAsync(id, submitted)
}

export type ProviderModelPatchField = CustomProviderModelPatchField

export async function patchCustomProviderModelConfigurationAsync(input: {
  current: CustomProviderModelPatchState
  next: UpsertCustomProviderModelInput
  fields: ProviderModelPatchField[]
  expectedUpdatedAt: string
}): Promise<CustomProviderModelPatchOutcome> {
  return patchCustomProviderModelAsync(input)
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
  const breakdown = buildCatalogCostBreakdownFromPricing(pricing, input)
  if (!breakdown || breakdown.cacheReadUsdPer1M === undefined) return undefined
  return breakdown.cacheReadCostUsd ?? 0
}

export async function estimateCatalogCacheReadCostUsdAsync(input: CostInput & { systemAccountId?: string }): Promise<number | undefined> {
  const pricing = await resolveCatalogPricingAsync(input)
  if (!pricing || input.cacheReadTokens === undefined) return undefined
  const breakdown = buildCatalogCostBreakdownFromPricing(pricing, input)
  if (!breakdown || breakdown.cacheReadUsdPer1M === undefined) return undefined
  return breakdown.cacheReadCostUsd ?? 0
}

export function estimateCatalogCacheWriteCostUsd(input: CostInput & { systemAccountId?: string }): number | undefined {
  const pricing = resolveCatalogPricing(input)
  if (!pricing || (input.cacheWriteTokens === undefined && input.cacheWrite1hTokens === undefined)) return undefined
  return cacheWriteCostFromBreakdown(buildCatalogCostBreakdownFromPricing(pricing, input), input)
}

export async function estimateCatalogCacheWriteCostUsdAsync(input: CostInput & { systemAccountId?: string }): Promise<number | undefined> {
  const pricing = await resolveCatalogPricingAsync(input)
  if (!pricing || (input.cacheWriteTokens === undefined && input.cacheWrite1hTokens === undefined)) return undefined
  return cacheWriteCostFromBreakdown(buildCatalogCostBreakdownFromPricing(pricing, input), input)
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
  return buildProviderBillingCostBreakdown(pricing, input)
}

function cacheWriteCostFromBreakdown(
  breakdown: ProviderCostBreakdown | undefined,
  input: Pick<CostInput, 'cacheWriteTokens' | 'cacheWrite1hTokens'>
): number | undefined {
  if (!breakdown) return undefined
  const values = [breakdown.cacheWriteCostUsd, breakdown.cacheWrite1hCostUsd]
    .filter((value): value is number => value !== undefined)
  if (values.length) return Number(values.reduce((total, value) => total + value, 0).toFixed(10))
  return (input.cacheWriteTokens ?? 0) === 0 && (input.cacheWrite1hTokens ?? 0) === 0
    && (breakdown.cacheWriteUsdPer1M !== undefined || breakdown.cacheWrite1hUsdPer1M !== undefined) ? 0 : undefined
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
    generationParameterCapabilities: Object.fromEntries(Object.entries(item.generationParameterCapabilities ?? {}).map(([protocol, capabilities]) => [
      protocol,
      capabilities.map((capability) => ({ ...capability }))
    ])),
    serviceTierPrices: cloneServiceTierPrices(item.serviceTierPrices),
    supportedServiceTiers: [...item.supportedServiceTiers],
    supportedReasoningEfforts: [...item.supportedReasoningEfforts],
    codexSupportedReasoningLevels: [...item.codexSupportedReasoningLevels],
    catalogDisplay: item.catalogDisplay?.map((section) => ({
      ...section,
      items: section.items.map((entry) => ({ ...entry }))
    }))
  }))
}

function withCatalogDisplay(item: ProviderModelCatalogItem): ProviderModelCatalogItem {
  return { ...item, catalogDisplay: buildProviderCatalogDisplay(item) }
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

function mergeProviderModelTestCatalogItems(
  items: ProviderModelTestCatalogItem[],
  preserveProviderIdentity: boolean
): ProviderModelTestCatalogItem[] {
  const merged = new Map<string, ProviderModelTestCatalogItem>()
  for (const item of items) {
    const model = item.model.trim()
    if (!model) continue
    const key = preserveProviderIdentity
      ? `${normalizeProviderToken(item.providerCode) ?? ''}\n${model}`
      : model
    const previous = merged.get(key)
    if (!previous || modelTestCatalogPriority(item) >= modelTestCatalogPriority(previous)) {
      merged.set(key, item)
    }
  }
  return [...merged.values()]
}

function providerModelTestCatalogItemFromBuiltIn(item: ProviderModelTestCatalogRecord): ProviderModelTestCatalogItem {
  return { ...item, scope: 'built_in', supportedApiProtocols: [...item.supportedApiProtocols] }
}

function providerModelTestCatalogItemFromCustom(item: CustomProviderModelTestCatalogRecord): ProviderModelTestCatalogItem {
  return { ...item, supportedApiProtocols: [...item.supportedApiProtocols] }
}

function modelTestCatalogPriority(item: ProviderModelTestCatalogItem): number {
  if (item.scope === 'personal') return 3
  if (item.scope === 'global') return 2
  return 1
}

function compareProviderModelTestCatalogItems(
  left: ProviderModelTestCatalogItem,
  right: ProviderModelTestCatalogItem
): number {
  const sameProvider = normalizeProviderToken(left.providerCode) === normalizeProviderToken(right.providerCode)
  if (left.releaseDate && right.releaseDate && left.releaseDate !== right.releaseDate) {
    return right.releaseDate.localeCompare(left.releaseDate)
  }
  if (left.releaseDate && !right.releaseDate) return -1
  if (!left.releaseDate && right.releaseDate) return 1
  if (sameProvider && left.catalogOrder !== undefined && right.catalogOrder !== undefined && left.catalogOrder !== right.catalogOrder) {
    return left.catalogOrder - right.catalogOrder
  }
  const modelOrder = left.model.localeCompare(right.model, 'en')
  if (modelOrder !== 0) return modelOrder
  return left.id.localeCompare(right.id, 'en')
}

function hasResolvablePrice(item: ProviderModelCatalogItem, _allItems: ProviderModelCatalogItem[]): boolean {
  return hasDirectPrice(item)
}

function isSupportedCatalogModel(item: ProviderModelCatalogItem): boolean {
  const mode = item.mode?.trim().toLowerCase()
  if (mode === 'audio' || mode === 'audio_speech' || mode === 'audio_transcription') return false
  const protocols = item.supportedApiProtocols ?? []
  if (protocols.includes('realtime')) return false
  if (protocols.length === 1 && protocols[0] === 'audio') return false
  const model = item.model.trim().toLowerCase()
  return !/(?:^|[-_.])(audio|realtime|transcribe|tts|whisper)(?:$|[-_.])/.test(model)
}

function hasDirectPrice(item: Omit<ProviderModelPricing, 'defaultReasoningEffort'>): boolean {
  return item.inputUsdPer1M !== undefined
    || item.outputUsdPer1M !== undefined
    || item.cachedInputUsdPer1M !== undefined
    || item.cacheWriteUsdPer1M !== undefined
    || item.cacheWrite1hUsdPer1M !== undefined
    || item.cacheStorageUsdPer1MPerHour !== undefined
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
  const keepStaticPricingSource = item.source !== 'manual-override'
  return {
    ...item,
    defaultReasoningEffort: item.defaultReasoningEffort ?? null,
    scope: 'built_in',
    status: item.status,
    inputModalities: [...(item.inputModalities?.length ? item.inputModalities : staticCapabilities?.inputModalities ?? [])],
    outputModalities: [...(item.outputModalities?.length ? item.outputModalities : staticCapabilities?.outputModalities ?? [])],
    supportedTools: [...(item.supportedTools?.length ? item.supportedTools : staticCapabilities?.supportedTools ?? [])],
    generationParameterCapabilities: limitGenerationParameterMaxOutputTokens(
      staticCapabilities?.generationParameterCapabilities
        ?? generationParameterCapabilitiesForModel({
        providerCode: item.providerCode,
        model: item.model,
        maxOutputTokens: item.maxOutputTokens
        }),
      item.maxOutputTokens
    ),
    supportedServiceTiers: [...(item.supportedServiceTiers ?? [])],
    supportedReasoningEfforts: [...(item.supportedReasoningEfforts ?? [])],
    codexSupportedReasoningLevels: [...(item.codexSupportedReasoningLevels ?? [])],
    supportsServiceTier: (item.supportedServiceTiers?.length ?? 0) > 0,
    cachedImageInputUsdPer1M: keepStaticPricingSource ? staticCapabilities?.cachedImageInputUsdPer1M : undefined,
    sourcePricingCurrency: keepStaticPricingSource ? staticCapabilities?.sourcePricingCurrency : undefined,
    sourceExchangeRateToUsd: keepStaticPricingSource ? staticCapabilities?.sourceExchangeRateToUsd : undefined,
    sourceExchangeRateDate: keepStaticPricingSource ? staticCapabilities?.sourceExchangeRateDate : undefined,
    sourcePricingNote: keepStaticPricingSource ? staticCapabilities?.sourcePricingNote : undefined
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
    generationParameterCapabilities: generationParameterCapabilitiesForModel({
      providerCode: item.providerCode,
      model: item.model,
      maxOutputTokens: item.maxOutputTokens
    }),
    inputUsdPer1M: item.inputUsdPer1M,
    outputUsdPer1M: item.outputUsdPer1M,
    cachedInputUsdPer1M: item.cachedInputUsdPer1M,
    cacheWriteUsdPer1M: item.cacheWriteUsdPer1M,
    cacheWrite1hUsdPer1M: item.cacheWrite1hUsdPer1M,
    cacheStorageUsdPer1MPerHour: item.cacheStorageUsdPer1MPerHour,
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
    catalogVisible: item.catalogVisible,
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
  const leftReleaseDate = sortableCatalogReleaseDate(left)
  const rightReleaseDate = sortableCatalogReleaseDate(right)
  if (leftReleaseDate && rightReleaseDate && leftReleaseDate !== rightReleaseDate) {
    return rightReleaseDate.localeCompare(leftReleaseDate)
  }
  if (leftReleaseDate && !rightReleaseDate) return -1
  if (!leftReleaseDate && rightReleaseDate) return 1

  const catalogOrder = compareSharedCatalogOrder(left.catalogOrder, right.catalogOrder)
  if (catalogOrder !== 0) return catalogOrder

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

export async function modelCatalogSourceProviderCodesAsync(providerCode: string): Promise<string[]> {
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

export function modelCatalogBuiltInSourceProviderCodes(providerCode: string, sourceProviderCodes: string[]): string[] {
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

function cloneServiceTierPrices(value?: Record<string, ModelPriceSet>): Record<string, ModelPriceSet> | undefined {
  if (!value) return undefined
  return Object.fromEntries(Object.entries(value).map(([tier, prices]) => [tier, { ...prices }]))
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

registerGatewayRuntimeCacheInvalidator((reason) => shouldInvalidateProviderModelCatalog(reason)
  ? clearProviderModelCatalogCaches()
  : undefined)
