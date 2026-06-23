import { normalizeProviderToken } from '../../domain/provider-protocol.js'
import { modelPricingProviderDriverForProvider } from './provider-driver.registry.js'
import type {
  ModelPricingProviderDriverHelpers,
  ProviderModelApiProtocol,
  RawModelPricing
} from './provider-driver.types.js'

export type { ProviderModelApiProtocol } from './provider-driver.types.js'

export interface ProviderModelPricing {
  providerCode: string
  model: string
  mode?: string
  catalogOrder?: number
  releaseDate?: string
  shutdownDate?: string
  supportedApiProtocols: ProviderModelApiProtocol[]
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  catalogVisible?: boolean
  source: string
}

export interface CostInput {
  providerCode: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  cacheWrite1hTokens?: number
  thinkingTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
  inputAudioTokens?: number
  outputAudioTokens?: number
  outputImageCount?: number
}

interface CostBreakdownInput extends CostInput {
  costUsd?: number
}

export interface ProviderCostBreakdown {
  inputCostUsd?: number
  outputCostUsd?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cacheReadCostUsd?: number
  cacheReadUsdPer1M?: number
  cacheWriteCostUsd?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hCostUsd?: number
  cacheWrite1hUsdPer1M?: number
  thinkingTokens?: number
  inputImageCostUsd?: number
  outputImageCostUsd?: number
  inputImageUsdPer1M?: number
  outputImageUsdPer1M?: number
  inputAudioCostUsd?: number
  outputAudioCostUsd?: number
  inputAudioUsdPer1M?: number
  outputAudioUsdPer1M?: number
  outputImageUnitCostUsd?: number
  outputUsdPerImage?: number
  accountChargeUsd?: number
  multiplier: 1
}

const modelPricingDriverHelpers: ModelPricingProviderDriverHelpers = {
  normalizeModel,
  extractModelReleaseDate
}

export function listProviderModelPricing(providerCode: string): ProviderModelPricing[] {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return []
  const models = rawModelsForProvider(normalizedProviderCode)
  if (!models.length) return []
  const pricing = models
    .filter((item) => !hasModelShutdown(item))
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
  if (!input.model || !hasAnyCostDimension(input)) {
    return undefined
  }

  const pricing = findProviderModelPricing(input.providerCode, input.model)
  if (!pricing) return undefined

  const inputPrice = normalizePrice(pricing.input_cost_per_token)
  const outputPrice = normalizePrice(pricing.output_cost_per_token)
  const cachedInputPrice = normalizePrice(pricing.cache_read_input_token_cost) ?? inputPrice
  const cacheWritePrice = normalizePrice(pricing.cache_creation_input_token_cost)
  const cacheWrite1hPrice = normalizePrice(pricing.cache_creation_input_token_cost_above_1hr) ?? cacheWritePrice
  const inputImagePrice = normalizePrice(pricing.input_cost_per_image_token)
  const outputImagePrice = normalizePrice(pricing.output_cost_per_image_token)
  const inputAudioPrice = normalizePrice(pricing.input_cost_per_audio_token)
  const outputAudioPrice = normalizePrice(pricing.output_cost_per_audio_token)
  const outputImageUnitPrice = normalizePrice(pricing.output_cost_per_image)
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

  const cost = uncachedInputTokens * (inputPrice ?? 0)
    + cacheReadTokens * (cachedInputPrice ?? 0)
    + cacheWriteStandardTokens * (cacheWritePrice ?? 0)
    + cacheWrite1hTokens * (cacheWrite1hPrice ?? 0)
    + inputImageTokens * (inputImagePrice ?? 0)
    + outputTokens * (outputPrice ?? 0)
    + outputImageTokens * (outputImagePrice ?? 0)
    + inputAudioTokens * (inputAudioPrice ?? 0)
    + outputAudioTokens * (outputAudioPrice ?? 0)
    + outputImageCount * (outputImageUnitPrice ?? 0)

  return Number(cost.toFixed(10))
}

export function estimateProviderCacheWriteCostUsd(input: CostInput): number | undefined {
  if (!input.model || (input.cacheWriteTokens === undefined && input.cacheWrite1hTokens === undefined)) {
    return undefined
  }

  const pricing = findProviderModelPricing(input.providerCode, input.model)
  if (!pricing) return undefined

  const cacheWritePrice = normalizePrice(pricing.cache_creation_input_token_cost)
  const cacheWrite1hPrice = normalizePrice(pricing.cache_creation_input_token_cost_above_1hr) ?? cacheWritePrice
  if (cacheWritePrice === undefined && cacheWrite1hPrice === undefined) return undefined

  const cacheWriteTokens = Math.max(input.cacheWriteTokens ?? 0, 0)
  const cacheWrite1hTokens = normalizedCacheWrite1hTokens(input, cacheWriteTokens)
  const cacheWriteStandardTokens = Math.max(cacheWriteTokens - cacheWrite1hTokens, 0)
  return roundCost(
    cacheWriteStandardTokens * (cacheWritePrice ?? 0)
    + cacheWrite1hTokens * (cacheWrite1hPrice ?? 0)
  )
}

export function estimateProviderCacheReadCostUsd(input: CostInput): number | undefined {
  if (!input.model || input.cacheReadTokens === undefined) {
    return undefined
  }

  const pricing = findProviderModelPricing(input.providerCode, input.model)
  if (!pricing) return undefined

  const cachedInputPrice = normalizePrice(pricing.cache_read_input_token_cost)
    ?? normalizePrice(pricing.input_cost_per_token)
  if (cachedInputPrice === undefined) return undefined

  const cacheReadTokens = Math.max(input.cacheReadTokens ?? 0, 0)
  return roundCost(cacheReadTokens * cachedInputPrice)
}

export function buildProviderCostBreakdown(input: CostBreakdownInput): ProviderCostBreakdown | undefined {
  if (!input.model) return undefined

  const pricing = findProviderModelPricing(input.providerCode, input.model)
  if (!pricing) return undefined

  const inputPrice = normalizePrice(pricing.input_cost_per_token)
  const outputPrice = normalizePrice(pricing.output_cost_per_token)
  const cachedInputPrice = normalizePrice(pricing.cache_read_input_token_cost) ?? inputPrice
  const cacheWritePrice = normalizePrice(pricing.cache_creation_input_token_cost)
  const cacheWrite1hPrice = normalizePrice(pricing.cache_creation_input_token_cost_above_1hr) ?? cacheWritePrice
  const inputImagePrice = normalizePrice(pricing.input_cost_per_image_token)
  const outputImagePrice = normalizePrice(pricing.output_cost_per_image_token)
  const inputAudioPrice = normalizePrice(pricing.input_cost_per_audio_token)
  const outputAudioPrice = normalizePrice(pricing.output_cost_per_audio_token)
  const outputImageUnitPrice = normalizePrice(pricing.output_cost_per_image)
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
    accountChargeUsd: normalizePrice(input.costUsd) ?? sumCostParts(inputCostUsd, outputCostUsd, cacheReadCostUsd, cacheWriteCostUsd, cacheWrite1hCostUsd, inputImageCostUsd, outputImageCostUsd, inputAudioCostUsd, outputAudioCostUsd, outputImageUnitCostUsd),
    multiplier: 1
  }
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
    const matched = models.find((item) => normalizeModel(item.model) === candidate)
    if (matched && !hasModelShutdown(matched)) return matched
  }

  return undefined
}

function rawModelsForProvider(providerCode: string): readonly RawModelPricing[] {
  return modelPricingProviderDriverForProvider(providerCode)?.rawModels ?? []
}

function defaultImageOutputTokens(input: CostInput, pricing: RawModelPricing): number {
  if (input.outputImageTokens !== undefined || pricing.mode !== 'image_generation' || pricing.output_cost_per_image_token === undefined) {
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

function toProviderModelPricing(item: RawModelPricing, providerCode: string): ProviderModelPricing {
  const driver = modelPricingProviderDriverForProvider(providerCode)
  const source = driver?.pricingSource ?? 'unknown-pricing-snapshot'
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
    inputUsdPer1M: perMillion(item.input_cost_per_token),
    outputUsdPer1M: perMillion(item.output_cost_per_token),
    cachedInputUsdPer1M: perMillion(item.cache_read_input_token_cost),
    cacheWriteUsdPer1M: perMillion(item.cache_creation_input_token_cost),
    cacheWrite1hUsdPer1M: perMillion(item.cache_creation_input_token_cost_above_1hr),
    imageInputUsdPer1M: perMillion(item.input_cost_per_image_token),
    imageOutputUsdPer1M: perMillion(item.output_cost_per_image_token),
    audioInputUsdPer1M: perMillion(item.input_cost_per_audio_token),
    audioOutputUsdPer1M: perMillion(item.output_cost_per_audio_token),
    outputUsdPerImage: normalizePrice(item.output_cost_per_image),
    maxInputTokens: item.max_input_tokens,
    maxOutputTokens: item.max_output_tokens,
    maxTokens: item.max_tokens,
    supportsPromptCaching: item.supports_prompt_caching === true,
    supportsServiceTier: item.supports_service_tier === true,
    catalogVisible: item.catalog_visible !== false,
    source
  }
}

function hasModelShutdown(item: RawModelPricing): boolean {
  return typeof item.shutdown_date === 'string' && item.shutdown_date <= currentUtcDate()
}

function normalizeModel(value: string): string {
  return value.trim().toLowerCase()
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

function roundCost(value: number): number {
  return Number(value.toFixed(10))
}

function sumCostParts(...parts: Array<number | undefined>): number {
  return roundCost(parts.reduce<number>((total, value) => total + (value ?? 0), 0))
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
