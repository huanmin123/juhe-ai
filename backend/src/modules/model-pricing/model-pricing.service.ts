import { openAIModelPricingData } from './openai-model-pricing.data.js'

export interface ProviderModelPricing {
  providerCode: string
  model: string
  mode?: string
  releaseDate?: string
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  imageOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  source: 'openai-pricing-snapshot'
}

interface RawModelPricing {
  model: string
  mode?: string
  input_cost_per_token?: number
  output_cost_per_token?: number
  cache_creation_input_token_cost?: number
  cache_creation_input_token_cost_above_1hr?: number
  cache_read_input_token_cost?: number
  output_cost_per_image?: number
  output_cost_per_image_token?: number
  max_input_tokens?: number
  max_output_tokens?: number
  max_tokens?: number
  supports_prompt_caching?: boolean
  supports_service_tier?: boolean
}

interface CostInput {
  providerCode: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
}

const openAIModels = openAIModelPricingData as readonly RawModelPricing[]

export function listProviderModelPricing(providerCode: string): ProviderModelPricing[] {
  if (!isOpenAIProvider(providerCode)) return []
  const pricing = openAIModels.map((item) => toProviderModelPricing(item))
  const familyReleaseDates = buildFamilyReleaseDateMap(pricing)
  return pricing.sort((left, right) => compareProviderModels(left, right, familyReleaseDates))
}

export function getProviderModelPricing(providerCode: string, model?: string): ProviderModelPricing | undefined {
  if (!isOpenAIProvider(providerCode) || !model) return undefined
  const raw = findOpenAIModelPricing(model)
  return raw ? toProviderModelPricing(raw) : undefined
}

export function estimateProviderCostUsd(input: CostInput): number | undefined {
  if (!input.model || (input.inputTokens === undefined && input.outputTokens === undefined && input.cacheReadTokens === undefined)) {
    return undefined
  }

  const pricing = findOpenAIModelPricing(input.model)
  if (!pricing) return undefined

  const inputPrice = normalizePrice(pricing.input_cost_per_token)
  const outputPrice = normalizePrice(pricing.output_cost_per_token)
  const cachedInputPrice = normalizePrice(pricing.cache_read_input_token_cost) ?? inputPrice
  if (inputPrice === undefined && outputPrice === undefined && cachedInputPrice === undefined) return undefined

  const cacheReadTokens = Math.max(input.cacheReadTokens ?? 0, 0)
  const uncachedInputTokens = Math.max((input.inputTokens ?? 0) - cacheReadTokens, 0)
  const outputTokens = Math.max(input.outputTokens ?? 0, 0)

  const cost = uncachedInputTokens * (inputPrice ?? 0)
    + cacheReadTokens * (cachedInputPrice ?? 0)
    + outputTokens * (outputPrice ?? 0)

  return Number(cost.toFixed(10))
}

function findOpenAIModelPricing(model: string): RawModelPricing | undefined {
  const normalized = normalizeModel(model)
  if (!normalized) return undefined

  const byExactName = openAIModels.find((item) => normalizeModel(item.model) === normalized)
  if (byExactName) return byExactName

  for (const candidate of buildModelCandidates(normalized)) {
    const matched = openAIModels.find((item) => normalizeModel(item.model) === candidate)
    if (matched) return matched
  }

  return undefined
}

function buildModelCandidates(model: string): string[] {
  const candidates = new Set<string>()
  const withoutDate = model.replace(/-\d{4}-\d{2}-\d{2}$/, '')
  if (withoutDate !== model) candidates.add(withoutDate)

  if (model.startsWith('gpt-5.5-')) candidates.add('gpt-5.5')
  if (model.startsWith('gpt-5.4-mini-')) candidates.add('gpt-5.4-mini')
  if (model.startsWith('gpt-5.4-nano-')) candidates.add('gpt-5.4-nano')
  if (model.startsWith('gpt-5.4-')) candidates.add('gpt-5.4')
  if (model.startsWith('gpt-5.3-codex-spark')) candidates.add('gpt-5.3-codex-spark')
  if (model.startsWith('gpt-5.3-codex')) candidates.add('gpt-5.3-codex')
  if (model.startsWith('gpt-5.3-')) candidates.add('gpt-5.3-chat-latest')
  if (model.startsWith('gpt-5.2-codex')) candidates.add('gpt-5.2-codex')
  if (model.startsWith('gpt-5.2-')) candidates.add('gpt-5.2')
  if (model.startsWith('gpt-5.1-codex-mini')) candidates.add('gpt-5.1-codex-mini')
  if (model.startsWith('gpt-5.1-codex-max')) candidates.add('gpt-5.1-codex-max')
  if (model.startsWith('gpt-5.1-codex')) candidates.add('gpt-5.1-codex')
  if (model.startsWith('gpt-5.1-')) candidates.add('gpt-5.1')
  if (model.startsWith('gpt-5-mini-')) candidates.add('gpt-5-mini')
  if (model.startsWith('gpt-5-nano-')) candidates.add('gpt-5-nano')
  if (model.startsWith('gpt-5-')) candidates.add('gpt-5')
  if (model.startsWith('gpt-4.1-mini-')) candidates.add('gpt-4.1-mini')
  if (model.startsWith('gpt-4.1-nano-')) candidates.add('gpt-4.1-nano')
  if (model.startsWith('gpt-4.1-')) candidates.add('gpt-4.1')
  if (model.startsWith('gpt-4o-mini-')) candidates.add('gpt-4o-mini')
  if (model.startsWith('gpt-4o-')) candidates.add('gpt-4o')

  return Array.from(candidates)
}

function toProviderModelPricing(item: RawModelPricing): ProviderModelPricing {
  return {
    providerCode: 'openai',
    model: item.model,
    mode: item.mode,
    releaseDate: extractModelReleaseDate(item.model),
    inputUsdPer1M: perMillion(item.input_cost_per_token),
    outputUsdPer1M: perMillion(item.output_cost_per_token),
    cachedInputUsdPer1M: perMillion(item.cache_read_input_token_cost),
    cacheWriteUsdPer1M: perMillion(item.cache_creation_input_token_cost),
    cacheWrite1hUsdPer1M: perMillion(item.cache_creation_input_token_cost_above_1hr),
    imageOutputUsdPer1M: perMillion(item.output_cost_per_image_token),
    outputUsdPerImage: normalizePrice(item.output_cost_per_image),
    maxInputTokens: item.max_input_tokens,
    maxOutputTokens: item.max_output_tokens,
    maxTokens: item.max_tokens,
    supportsPromptCaching: item.supports_prompt_caching === true,
    supportsServiceTier: item.supports_service_tier === true,
    source: 'openai-pricing-snapshot'
  }
}

function isOpenAIProvider(providerCode: string): boolean {
  return normalizeModel(providerCode) === 'openai'
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

function extractModelReleaseDate(model: string): string | undefined {
  const match = model.match(/-(\d{4}-\d{2}-\d{2})$/)
  return match?.[1]
}

function extractModelFamily(model: string): string {
  return model.replace(/-\d{4}-\d{2}-\d{2}$/, '')
}

function buildFamilyReleaseDateMap(models: readonly ProviderModelPricing[]): Map<string, string> {
  const familyReleaseDates = new Map<string, string>()
  for (const item of models) {
    if (!item.releaseDate) continue
    const family = extractModelFamily(item.model)
    const current = familyReleaseDates.get(family)
    if (!current || item.releaseDate > current) {
      familyReleaseDates.set(family, item.releaseDate)
    }
  }
  return familyReleaseDates
}

function compareProviderModels(
  left: ProviderModelPricing,
  right: ProviderModelPricing,
  familyReleaseDates: Map<string, string>
): number {
  const leftFamilyDate = familyReleaseDates.get(extractModelFamily(left.model))
  const rightFamilyDate = familyReleaseDates.get(extractModelFamily(right.model))

  if (leftFamilyDate && rightFamilyDate && leftFamilyDate !== rightFamilyDate) {
    return rightFamilyDate.localeCompare(leftFamilyDate)
  }
  if (leftFamilyDate && !rightFamilyDate) return -1
  if (!leftFamilyDate && rightFamilyDate) return 1

  if (left.releaseDate && right.releaseDate && left.releaseDate !== right.releaseDate) {
    return right.releaseDate.localeCompare(left.releaseDate)
  }
  if (left.releaseDate && !right.releaseDate) return -1
  if (!left.releaseDate && right.releaseDate) return 1

  return left.model.localeCompare(right.model)
}
