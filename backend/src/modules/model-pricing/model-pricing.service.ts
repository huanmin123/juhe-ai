import { normalizeProviderToken } from '../../domain/provider-protocol.js'
import { modelPricingProviderDriverForProvider } from './provider-driver.registry.js'
import { buildProviderBillingCostBreakdown } from './provider-billing.service.js'
import type {
  ProviderBillingCostInput,
  ProviderBillingPriceSet,
  ProviderCostBreakdown as BillingCostBreakdown
} from './provider-billing.types.js'
import type {
  CodexReasoningLevel,
  GptServiceTier,
  GptWireReasoningEffort,
  ModelPricingProviderDriverHelpers,
  ProviderModelApiProtocol,
  ProviderModelModality,
  RawModelPricing
} from './provider-driver.types.js'

export type {
  CodexReasoningLevel,
  GptServiceTier,
  GptWireReasoningEffort,
  ProviderModelApiProtocol,
  ProviderModelModality
} from './provider-driver.types.js'

export interface ProviderModelPricing {
  providerCode: string
  model: string
  mode?: string
  catalogOrder?: number
  releaseDate?: string
  shutdownDate?: string
  supportedApiProtocols: ProviderModelApiProtocol[]
  inputModalities: ProviderModelModality[]
  outputModalities: ProviderModelModality[]
  supportedTools: string[]
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  cacheStorageUsdPer1MPerHour?: number
  serviceTierPrices?: ServiceTierPrices
  imageInputUsdPer1M?: number
  cachedImageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  longContextInputTokenThreshold?: number
  longContextInputTokenThresholdInclusive?: boolean
  longContextInputCostMultiplier?: number
  longContextOutputCostMultiplier?: number
  supportsPromptCaching: boolean
  supportedServiceTiers: string[]
  supportedReasoningEfforts: string[]
  defaultReasoningEffort?: string
  codexSupportedReasoningLevels: CodexReasoningLevel[]
  codexDefaultReasoningLevel?: CodexReasoningLevel
  codexMultiAgentVersion?: 'v2'
  supportsServiceTier: boolean
  catalogVisible?: boolean
  sourcePricingCurrency?: string
  sourceExchangeRateToUsd?: number
  sourceExchangeRateDate?: string
  sourcePricingNote?: string
  source: string
}

export interface ModelPriceSet extends ProviderBillingPriceSet {}

export type ServiceTierPrices = Record<string, ModelPriceSet>

export type CostInput = Omit<ProviderBillingCostInput, 'costUsd'>

interface CostBreakdownInput extends CostInput { costUsd?: number }

export type ProviderCostBreakdown = BillingCostBreakdown

const modelPricingDriverHelpers: ModelPricingProviderDriverHelpers = {
  normalizeModel,
  extractModelReleaseDate
}

export function listProviderModelPricing(providerCode: string): ProviderModelPricing[] {
  return listProviderModelPricingAsOf(providerCode, currentUtcDate())
}

export function listProviderModelPricingAsOf(providerCode: string, asOfDate: string): ProviderModelPricing[] {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return []
  const models = rawModelsForProvider(normalizedProviderCode)
  if (!models.length) return []
  const pricing = models
    .filter((item) => !hasModelShutdown(item, asOfDate))
    .map((item) => toProviderModelPricing(item, normalizedProviderCode))
  return pricing.sort(compareProviderModels)
}

export function getProviderModelPricing(providerCode: string, model?: string): ProviderModelPricing | undefined {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode || !model) return undefined
  const raw = findProviderModelPricing(normalizedProviderCode, model)
  return raw && normalizedProviderCode ? toProviderModelPricing(raw, normalizedProviderCode) : undefined
}

export function estimateProviderCostUsd(input: CostInput): number | undefined {
  if (!input.model || !hasAnyCostDimension(input)) return undefined
  return staticPricingBreakdown(input)?.accountChargeUsd
}

export function estimateProviderCacheWriteCostUsd(input: CostInput): number | undefined {
  if (!input.model || (input.cacheWriteTokens === undefined && input.cacheWrite1hTokens === undefined)) {
    return undefined
  }

  const breakdown = staticPricingBreakdown(input)
  if (!breakdown) return undefined
  const total = sumOptionalCosts(breakdown.cacheWriteCostUsd, breakdown.cacheWrite1hCostUsd)
  if (total !== undefined) return total
  return (input.cacheWriteTokens ?? 0) === 0 && (input.cacheWrite1hTokens ?? 0) === 0
    && (breakdown.cacheWriteUsdPer1M !== undefined || breakdown.cacheWrite1hUsdPer1M !== undefined) ? 0 : undefined
}

export function estimateProviderCacheReadCostUsd(input: CostInput): number | undefined {
  if (!input.model || input.cacheReadTokens === undefined) {
    return undefined
  }

  const breakdown = staticPricingBreakdown(input)
  if (!breakdown || breakdown.cacheReadUsdPer1M === undefined) return undefined
  return breakdown.cacheReadCostUsd ?? 0
}

export function buildProviderCostBreakdown(input: CostBreakdownInput): ProviderCostBreakdown | undefined {
  if (!input.model) return undefined

  return staticPricingBreakdown(input)
}

function staticPricingBreakdown(input: CostBreakdownInput): ProviderCostBreakdown | undefined {
  if (!input.model) return undefined
  const raw = findProviderModelPricing(input.providerCode, input.model)
  if (!raw) return undefined
  return buildProviderBillingCostBreakdown(toProviderModelPricing(raw, input.providerCode), input)
}

function findProviderModelPricing(providerCode: string, model: string): RawModelPricing | undefined {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return undefined
  const driver = modelPricingProviderDriverForProvider(normalizedProviderCode)
  if (!driver) return undefined
  const models = driver.rawModels
  if (!models.length) return undefined
  const normalized = normalizeModel(model)
  if (!normalized) return undefined
  if (driver.isUnavailableModel?.(normalized)) return undefined

  const byExactName = models.find((item) => normalizeModel(item.model) === normalized)
  if (byExactName && !hasModelShutdown(byExactName)) return byExactName

  for (const candidate of driver.buildModelCandidates(normalized)) {
    const normalizedCandidate = canonicalOpenAIModelAlias(candidate)
    const matched = models.find((item) => normalizeModel(item.model) === normalizedCandidate)
    if (matched && !hasModelShutdown(matched)) return matched
  }

  const canonicalAlias = canonicalOpenAIModelAlias(normalized)
  if (canonicalAlias !== normalized) {
    const matched = models.find((item) => normalizeModel(item.model) === canonicalAlias)
    if (matched && !hasModelShutdown(matched)) return matched
  }

  return undefined
}

function rawModelsForProvider(providerCode: string): readonly RawModelPricing[] {
  return modelPricingProviderDriverForProvider(providerCode)?.rawModels ?? []
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

function toProviderModelPricing(item: RawModelPricing, providerCode: string): ProviderModelPricing {
  const driver = modelPricingProviderDriverForProvider(providerCode)
  const source = driver?.pricingSource ?? 'unknown-pricing-snapshot'
  const supportedServiceTiers = item.supported_service_tiers ? [...item.supported_service_tiers] : []
  return {
    providerCode,
    model: item.model,
    mode: item.mode,
    catalogOrder: normalizeCatalogOrder(item.catalog_order),
    releaseDate: driver?.getModelReleaseDate(item, modelPricingDriverHelpers)
      ?? item.release_date
      ?? extractModelReleaseDate(item.model),
    shutdownDate: item.shutdown_date,
    supportedApiProtocols: driver?.inferModelApiProtocols(item, modelPricingDriverHelpers)
      ?? (item.supported_api_protocols ? [...item.supported_api_protocols] : undefined)
      ?? [],
    inputModalities: item.input_modalities ? [...item.input_modalities] : [],
    outputModalities: item.output_modalities ? [...item.output_modalities] : [],
    supportedTools: item.supported_tools ? [...item.supported_tools] : [],
    inputUsdPer1M: perMillion(item.input_cost_per_token),
    outputUsdPer1M: perMillion(item.output_cost_per_token),
    cachedInputUsdPer1M: perMillion(item.cache_read_input_token_cost),
    cacheWriteUsdPer1M: perMillion(item.cache_creation_input_token_cost),
    cacheWrite1hUsdPer1M: perMillion(item.cache_creation_input_token_cost_above_1hr),
    cacheStorageUsdPer1MPerHour: perMillion(item.cache_storage_input_token_cost_per_hour),
    serviceTierPrices: rawServiceTierPrices(item),
    imageInputUsdPer1M: perMillion(item.input_cost_per_image_token),
    cachedImageInputUsdPer1M: perMillion(item.cache_read_input_image_token_cost),
    imageOutputUsdPer1M: perMillion(item.output_cost_per_image_token),
    audioInputUsdPer1M: perMillion(item.input_cost_per_audio_token),
    audioOutputUsdPer1M: perMillion(item.output_cost_per_audio_token),
    outputUsdPerImage: normalizePrice(item.output_cost_per_image),
    contextWindowTokens: item.context_window_tokens,
    maxInputTokens: item.max_input_tokens,
    maxOutputTokens: item.max_output_tokens,
    maxTokens: item.max_tokens,
    longContextInputTokenThreshold: item.long_context_input_token_threshold,
    longContextInputTokenThresholdInclusive: item.long_context_input_token_threshold_inclusive === true,
    longContextInputCostMultiplier: item.long_context_input_cost_multiplier,
    longContextOutputCostMultiplier: item.long_context_output_cost_multiplier,
    supportsPromptCaching: item.supports_prompt_caching === true,
    supportedServiceTiers,
    supportedReasoningEfforts: item.supported_reasoning_efforts ? [...item.supported_reasoning_efforts] : [],
    defaultReasoningEffort: item.default_reasoning_effort,
    codexSupportedReasoningLevels: item.codex_supported_reasoning_levels
      ? [...item.codex_supported_reasoning_levels]
      : [],
    codexDefaultReasoningLevel: item.codex_default_reasoning_level,
    codexMultiAgentVersion: item.codex_multi_agent_version,
    supportsServiceTier: supportedServiceTiers.length > 0,
    catalogVisible: item.catalog_visible !== false,
    sourcePricingCurrency: item.source_pricing_currency,
    sourceExchangeRateToUsd: item.source_exchange_rate_to_usd,
    sourceExchangeRateDate: item.source_exchange_rate_date,
    sourcePricingNote: item.source_pricing_note,
    source
  }
}

function rawServiceTierPrices(item: RawModelPricing): ServiceTierPrices | undefined {
  const tiers: ServiceTierPrices = {}
  const add = (tier: string, prices: ModelPriceSet): void => {
    const definedPrices = Object.fromEntries(
      Object.entries(prices).filter(([, value]) => value !== undefined)
    ) as ModelPriceSet
    if (Object.keys(definedPrices).length > 0) tiers[tier] = definedPrices
  }
  add('priority', {
    inputUsdPer1M: perMillion(item.input_cost_per_token_priority),
    outputUsdPer1M: perMillion(item.output_cost_per_token_priority),
    cachedInputUsdPer1M: perMillion(item.cache_read_input_token_cost_priority),
    cacheWriteUsdPer1M: perMillion(item.cache_creation_input_token_cost_priority),
    cacheWrite1hUsdPer1M: perMillion(item.cache_creation_input_token_cost_above_1hr_priority),
    cacheStorageUsdPer1MPerHour: perMillion(item.cache_storage_input_token_cost_per_hour_priority),
    audioInputUsdPer1M: perMillion(item.input_cost_per_audio_token_priority),
    audioOutputUsdPer1M: perMillion(item.output_cost_per_audio_token)
  })
  add('flex', {
    inputUsdPer1M: perMillion(item.input_cost_per_token_flex),
    outputUsdPer1M: perMillion(item.output_cost_per_token_flex),
    cachedInputUsdPer1M: perMillion(item.cache_read_input_token_cost_flex),
    cacheWriteUsdPer1M: perMillion(item.cache_creation_input_token_cost_flex),
    cacheWrite1hUsdPer1M: perMillion(item.cache_creation_input_token_cost_above_1hr_flex),
    cacheStorageUsdPer1MPerHour: perMillion(item.cache_storage_input_token_cost_per_hour_flex),
    audioInputUsdPer1M: perMillion(item.input_cost_per_audio_token_flex),
    audioOutputUsdPer1M: perMillion(item.output_cost_per_audio_token)
  })
  add('batch', {
    inputUsdPer1M: perMillion(item.input_cost_per_token_batch),
    outputUsdPer1M: perMillion(item.output_cost_per_token_batch)
  })
  return Object.keys(tiers).length ? tiers : undefined
}

function canonicalOpenAIModelAlias(model: string): string {
  return model === 'gpt-5.6' ? 'gpt-5.6-sol' : model
}

function hasModelShutdown(item: RawModelPricing, asOfDate = currentUtcDate()): boolean {
  return typeof item.shutdown_date === 'string' && item.shutdown_date <= asOfDate
}

function normalizeModel(value: string): string {
  return value.trim()
}

function normalizePrice(value?: number): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function perMillion(value?: number): number | undefined {
  const price = normalizePrice(value)
  return price === undefined ? undefined : Number((price * 1_000_000).toFixed(8))
}

function normalizeCatalogOrder(value?: number): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function sumOptionalCosts(...parts: Array<number | undefined>): number | undefined {
  const values = parts.filter((value): value is number => value !== undefined)
  return values.length ? Number(values.reduce((total, value) => total + value, 0).toFixed(10)) : undefined
}

function extractModelReleaseDate(model: string): string | undefined {
  const match = model.match(/-(\d{4}-\d{2}-\d{2})$/)
  return match?.[1]
}

function currentUtcDate(): string {
  return new Date().toISOString().slice(0, 10)
}

function compareProviderModels(left: ProviderModelPricing, right: ProviderModelPricing): number {
  const catalogOrder = compareSharedCatalogOrder(left.catalogOrder, right.catalogOrder)
  if (catalogOrder !== 0) return catalogOrder

  if (left.releaseDate && right.releaseDate && left.releaseDate !== right.releaseDate) {
    return right.releaseDate.localeCompare(left.releaseDate)
  }
  if (left.releaseDate && !right.releaseDate) return -1
  if (!left.releaseDate && right.releaseDate) return 1

  return left.model.localeCompare(right.model)
}

function compareSharedCatalogOrder(left?: number, right?: number): number {
  if (left !== undefined && right !== undefined && left !== right) {
    return left - right
  }
  return 0
}
