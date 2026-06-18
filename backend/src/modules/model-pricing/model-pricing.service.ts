import { anthropicModelPricingData } from './anthropic-model-pricing.data.js'
import { openAIModelPricingData } from './openai-model-pricing.data.js'
import {
  ANTHROPIC_PROVIDER_CODE,
  isOpenAICompatibleProviderCode,
  normalizeProviderToken
} from '../../domain/provider-protocol.js'

export type ProviderModelApiProtocol = 'chat_completions' | 'responses' | 'messages' | 'message_token_counting' | 'completions' | 'images' | 'audio' | 'realtime'

export interface ProviderModelPricing {
  providerCode: string
  model: string
  mode?: string
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
  source: string
}

interface RawModelPricing {
  model: string
  mode?: string
  release_date?: string
  input_cost_per_token?: number
  output_cost_per_token?: number
  cache_creation_input_token_cost?: number
  cache_creation_input_token_cost_above_1hr?: number
  cache_read_input_token_cost?: number
  input_cost_per_image_token?: number
  output_cost_per_image?: number
  output_cost_per_image_token?: number
  input_cost_per_audio_token?: number
  output_cost_per_audio_token?: number
  max_input_tokens?: number
  max_output_tokens?: number
  max_tokens?: number
  shutdown_date?: string
  supported_api_protocols?: ProviderModelApiProtocol[]
  supports_prompt_caching?: boolean
  supports_service_tier?: boolean
}

export interface CostInput {
  providerCode: string
  model?: string
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
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

const openAIModels = openAIModelPricingData as readonly RawModelPricing[]
const anthropicModels = anthropicModelPricingData as readonly RawModelPricing[]

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
  const inputImagePrice = normalizePrice(pricing.input_cost_per_image_token)
  const outputImagePrice = normalizePrice(pricing.output_cost_per_image_token)
  const inputAudioPrice = normalizePrice(pricing.input_cost_per_audio_token)
  const outputAudioPrice = normalizePrice(pricing.output_cost_per_audio_token)
  const outputImageUnitPrice = normalizePrice(pricing.output_cost_per_image)
  if (!hasAnyPrice(inputPrice, outputPrice, cachedInputPrice, inputImagePrice, outputImagePrice, inputAudioPrice, outputAudioPrice, outputImageUnitPrice)) return undefined

  const cacheReadTokens = Math.max(input.cacheReadTokens ?? 0, 0)
  const inputImageTokens = inputImagePrice === undefined ? 0 : Math.max(input.inputImageTokens ?? 0, 0)
  const outputImageTokens = outputImagePrice === undefined ? 0 : Math.max(input.outputImageTokens ?? defaultImageOutputTokens(input, pricing), 0)
  const inputAudioTokens = inputAudioPrice === undefined ? 0 : Math.max(input.inputAudioTokens ?? defaultInputAudioTokens(input, inputPrice, cacheReadTokens, inputImageTokens), 0)
  const outputAudioTokens = outputAudioPrice === undefined ? 0 : Math.max(input.outputAudioTokens ?? defaultOutputAudioTokens(input, outputPrice, outputImageTokens), 0)
  const outputImageCount = outputImageUnitPrice === undefined ? 0 : Math.max(input.outputImageCount ?? 0, 0)
  const uncachedInputTokens = Math.max((input.inputTokens ?? 0) - cacheReadTokens - inputImageTokens - inputAudioTokens, 0)
  const outputTokens = Math.max((input.outputTokens ?? 0) - outputImageTokens - outputAudioTokens, 0)

  const cost = uncachedInputTokens * (inputPrice ?? 0)
    + cacheReadTokens * (cachedInputPrice ?? 0)
    + inputImageTokens * (inputImagePrice ?? 0)
    + outputTokens * (outputPrice ?? 0)
    + outputImageTokens * (outputImagePrice ?? 0)
    + inputAudioTokens * (inputAudioPrice ?? 0)
    + outputAudioTokens * (outputAudioPrice ?? 0)
    + outputImageCount * (outputImageUnitPrice ?? 0)

  return Number(cost.toFixed(10))
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
  const inputImagePrice = normalizePrice(pricing.input_cost_per_image_token)
  const outputImagePrice = normalizePrice(pricing.output_cost_per_image_token)
  const inputAudioPrice = normalizePrice(pricing.input_cost_per_audio_token)
  const outputAudioPrice = normalizePrice(pricing.output_cost_per_audio_token)
  const outputImageUnitPrice = normalizePrice(pricing.output_cost_per_image)
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
    inputUsdPer1M: perMillion(inputPrice),
    outputUsdPer1M: perMillion(outputPrice),
    cacheReadCostUsd,
    cacheReadUsdPer1M: perMillion(cachedInputPrice),
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
    accountChargeUsd: normalizePrice(input.costUsd) ?? sumCostParts(inputCostUsd, outputCostUsd, cacheReadCostUsd, inputImageCostUsd, outputImageCostUsd, inputAudioCostUsd, outputAudioCostUsd, outputImageUnitCostUsd),
    multiplier: 1
  }
}

function findProviderModelPricing(providerCode: string, model: string): RawModelPricing | undefined {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return undefined
  const models = rawModelsForProvider(normalizedProviderCode)
  if (!models.length) return undefined
  const normalized = normalizeModel(model)
  if (!normalized) return undefined
  if (isOpenAIProvider(normalizedProviderCode) && isUnavailableOpenAIModel(normalized)) return undefined

  const byExactName = models.find((item) => normalizeModel(item.model) === normalized)
  if (byExactName && !hasModelShutdown(byExactName)) return byExactName

  for (const candidate of buildModelCandidates(normalized, normalizedProviderCode)) {
    const matched = models.find((item) => normalizeModel(item.model) === candidate)
    if (matched && !hasModelShutdown(matched)) return matched
  }

  return undefined
}

function rawModelsForProvider(providerCode: string): readonly RawModelPricing[] {
  if (isOpenAIProvider(providerCode)) return openAIModels
  if (normalizeProviderToken(providerCode) === ANTHROPIC_PROVIDER_CODE) return anthropicModels
  return []
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
    || input.inputImageTokens !== undefined
    || input.outputImageTokens !== undefined
    || input.inputAudioTokens !== undefined
    || input.outputAudioTokens !== undefined
    || input.outputImageCount !== undefined
}

function hasAnyPrice(...prices: Array<number | undefined>): boolean {
  return prices.some((price) => price !== undefined)
}

function isUnavailableOpenAIModel(model: string): boolean {
  return unavailableOpenAIModels.has(model)
    || model.startsWith('gpt-4.5-preview')
    || model.startsWith('gpt-4-turbo-preview')
    || model.startsWith('gpt-4o-realtime-preview')
    || model.startsWith('gpt-4o-mini-realtime-preview')
    || model.startsWith('gpt-4o-audio-preview')
    || model.startsWith('gpt-4o-mini-audio-preview')
    || model.startsWith('gpt-4o-search-preview')
    || model.startsWith('gpt-4o-mini-search-preview')
    || model.startsWith('o1-preview')
}

const unavailableOpenAIModels = new Set([
  'chatgpt-4o-latest',
  'codex-mini-latest',
  'gpt-5.3-codex-spark',
  'o1-2024-12-17',
  'o1-pro-2025-03-19',
  'o1-mini',
  'o3-mini-2025-01-31',
  'o4-mini-2025-04-16',
  'gpt-4-0125-preview',
  'gpt-4-1106-vision-preview',
  'gpt-4-0314',
  'gpt-4-32k',
  'gpt-4-32k-0314',
  'gpt-4-32k-0613'
])

function buildModelCandidates(model: string, providerCode: string): string[] {
  const candidates = new Set<string>()
  const withoutDate = model.replace(/-\d{4}-\d{2}-\d{2}$/, '')
  if (withoutDate !== model) candidates.add(withoutDate)

  if (isOpenAIProvider(providerCode)) {
    if (model.startsWith('gpt-5.5-')) candidates.add('gpt-5.5')
    if (model.startsWith('gpt-5.4-mini-')) candidates.add('gpt-5.4-mini')
    if (model.startsWith('gpt-5.4-nano-')) candidates.add('gpt-5.4-nano')
    if (model.startsWith('gpt-5.4-')) candidates.add('gpt-5.4')
    if (model === 'gpt-5.3-codex') candidates.add('gpt-5.3-codex')
    if (model.startsWith('gpt-image-2-')) candidates.add('gpt-image-2')
    if (model.startsWith('gpt-realtime-mini-')) candidates.add('gpt-realtime-mini')
    if (model.startsWith('gpt-4.1-nano-')) candidates.add('gpt-4.1-nano')
    if (model.startsWith('gpt-4.1-mini-')) candidates.add('gpt-4.1-mini')
    if (model.startsWith('gpt-4.1-')) candidates.add('gpt-4.1')
    if (model.startsWith('gpt-4o-mini-transcribe-')) candidates.add('gpt-4o-mini-transcribe')
    if (model.startsWith('gpt-4o-mini-tts-')) candidates.add('gpt-4o-mini-tts')
  }

  if (normalizeProviderToken(providerCode) === ANTHROPIC_PROVIDER_CODE) {
    for (const base of anthropicModelCandidateBases) {
      if (model === base || model.startsWith(`${base}-`)) candidates.add(base)
    }
  }

  return Array.from(candidates)
}

function toProviderModelPricing(item: RawModelPricing, providerCode: string): ProviderModelPricing {
  const source = providerPricingSource(providerCode)
  return {
    providerCode,
    model: item.model,
    mode: item.mode,
    releaseDate: getProviderModelReleaseDate(item, providerCode),
    shutdownDate: item.shutdown_date,
    supportedApiProtocols: inferProviderModelApiProtocols(item, providerCode),
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
    source
  }
}

function getProviderModelReleaseDate(item: RawModelPricing, providerCode: string): string | undefined {
  if (isOpenAIProvider(providerCode)) {
    return item.release_date
      ?? extractModelReleaseDate(item.model)
      ?? inferOpenAIModelReleaseDate(item.model)
  }
  return item.release_date ?? extractModelReleaseDate(item.model)
}

const openAIModelReleaseDates = new Map<string, string>([
  ['gpt-5.5', '2026-04-23'],
  ['gpt-5.5-pro', '2026-04-23'],
  ['gpt-image-2', '2026-04-21'],
  ['gpt-5.4-mini', '2026-03-17'],
  ['gpt-5.4-nano', '2026-03-17'],
  ['gpt-5.4', '2026-03-05'],
  ['gpt-5.4-pro', '2026-03-05'],
  ['gpt-5.3-chat-latest', '2026-02-01'],
  ['gpt-5.3-codex', '2026-02-01'],
  ['gpt-audio-1.5', '2026-02-01'],
  ['gpt-image-1.5', '2026-02-01'],
  ['gpt-realtime-1.5', '2026-02-01'],
  ['gpt-5.2-codex', '2026-01-01'],
  ['gpt-5.2', '2025-12-11'],
  ['gpt-5.2-chat-latest', '2025-12-11'],
  ['gpt-5.2-pro', '2025-12-11'],
  ['gpt-5.1', '2025-11-13'],
  ['gpt-5.1-chat-latest', '2025-11-13'],
  ['gpt-5.1-codex', '2025-11-13'],
  ['gpt-5.1-codex-max', '2025-11-13'],
  ['gpt-5.1-codex-mini', '2025-11-13'],
  ['gpt-5-pro', '2025-10-06'],
  ['gpt-audio-mini', '2025-10-06'],
  ['gpt-image-1-mini', '2025-10-06'],
  ['gpt-realtime-mini', '2025-10-06'],
  ['gpt-5-codex', '2025-09-01'],
  ['gpt-audio', '2025-09-01'],
  ['gpt-realtime', '2025-09-01'],
  ['gpt-5', '2025-08-07'],
  ['gpt-5-chat-latest', '2025-08-07'],
  ['gpt-5-mini', '2025-08-07'],
  ['gpt-5-nano', '2025-08-07'],
  ['o3-pro', '2025-06-01'],
  ['gpt-image-1', '2025-04-23'],
  ['o3', '2025-04-16'],
  ['o4-mini', '2025-04-16'],
  ['gpt-4.1', '2025-04-14'],
  ['gpt-4.1-mini', '2025-04-14'],
  ['gpt-4.1-nano', '2025-04-14'],
  ['gpt-4o-mini-tts', '2025-03-20'],
  ['gpt-4o-mini-transcribe', '2025-03-20'],
  ['gpt-4o-transcribe', '2025-03-01'],
  ['gpt-4o-transcribe-diarize', '2025-03-01'],
  ['o1-pro', '2025-03-19'],
  ['o3-mini', '2025-01-31'],
  ['o1', '2024-12-17'],
  ['gpt-4o-mini', '2024-07-18'],
  ['gpt-4o', '2024-05-13'],
  ['gpt-4-turbo', '2024-04-09'],
  ['gpt-3.5-turbo', '2024-01-25'],
  ['gpt-3.5-turbo-0125', '2024-01-25'],
  ['babbage-002', '2024-01-04'],
  ['davinci-002', '2024-01-04'],
  ['gpt-4-1106-preview', '2023-11-06'],
  ['gpt-3.5-turbo-1106', '2023-11-06'],
  ['gpt-3.5-turbo-instruct', '2023-07-06'],
  ['gpt-4', '2023-06-13'],
  ['gpt-4-0613', '2023-06-13']
])

function inferOpenAIModelReleaseDate(model: string): string | undefined {
  const normalized = normalizeModel(model)
  const exactDate = openAIModelReleaseDates.get(normalized)
  if (exactDate) return exactDate

  return undefined
}

function inferProviderModelApiProtocols(item: RawModelPricing, providerCode: string): ProviderModelApiProtocol[] {
  if (item.supported_api_protocols?.length) {
    return item.supported_api_protocols
  }
  if (!isOpenAIProvider(providerCode)) return []

  const model = normalizeModel(item.model)
  const mode = (item.mode ?? '').trim()

  if (mode === 'image_generation' || model.startsWith('gpt-image') || model.startsWith('dall-e')) {
    return ['images']
  }

  if (model.includes('realtime')) {
    return ['realtime']
  }

  if (
    mode === 'audio_speech'
    || mode === 'audio_transcription'
    || model.includes('transcribe')
    || model.includes('tts')
    || model.includes('whisper')
  ) {
    return ['audio']
  }

  if (model.includes('audio')) {
    return ['chat_completions']
  }

  if (mode === 'completion') {
    return ['completions']
  }

  if (model.includes('codex') || model.includes('-pro')) {
    return ['responses']
  }

  if (mode === 'responses') {
    return ['responses']
  }

  if (mode === 'chat' || model.startsWith('gpt-') || model.startsWith('o')) {
    return ['chat_completions', 'responses']
  }

  return []
}

function hasModelShutdown(item: RawModelPricing): boolean {
  return typeof item.shutdown_date === 'string' && item.shutdown_date <= currentUtcDate()
}

function isOpenAIProvider(providerCode: string): boolean {
  return isOpenAICompatibleProviderCode(providerCode)
}

const anthropicModelCandidateBases = [
  'claude-fable-5',
  'claude-mythos-5',
  'claude-opus-4-8',
  'claude-opus-4-7',
  'claude-opus-4-6',
  'claude-opus-4-5',
  'claude-sonnet-4-6',
  'claude-sonnet-4-5',
  'claude-haiku-4-5'
]

function providerPricingSource(providerCode: string): string {
  return normalizeProviderToken(providerCode) === ANTHROPIC_PROVIDER_CODE
    ? 'anthropic-pricing-snapshot'
    : 'openai-pricing-snapshot'
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
  if (left.releaseDate && right.releaseDate && left.releaseDate !== right.releaseDate) {
    return right.releaseDate.localeCompare(left.releaseDate)
  }
  if (left.releaseDate && !right.releaseDate) return -1
  if (!left.releaseDate && right.releaseDate) return 1

  return left.model.localeCompare(right.model)
}
