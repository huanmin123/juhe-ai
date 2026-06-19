import {
  ANTHROPIC_PROVIDER_CODE,
  isOpenAICompatibleProviderCode,
  normalizeProviderToken
} from '../../domain/provider-protocol.js'
import { anthropicModelPricingData } from './anthropic-model-pricing.data.js'
import { openAIModelPricingData } from './openai-model-pricing.data.js'
import type {
  ModelPricingProviderDriver,
  ModelPricingProviderDriverHelpers,
  ProviderModelApiProtocol,
  RawModelPricing
} from './provider-driver.types.js'

const openAIModels = openAIModelPricingData as readonly RawModelPricing[]
const anthropicModels = anthropicModelPricingData as readonly RawModelPricing[]

const openAIModelPricingDriver: ModelPricingProviderDriver = {
  id: 'openai-compatible',
  pricingSource: 'openai-pricing-snapshot',
  rawModels: openAIModels,
  usesIncludedCacheReadUsage: true,
  supportsProvider(providerCode) {
    return isOpenAICompatibleProviderCode(providerCode)
  },
  isUnavailableModel: isUnavailableOpenAIModel,
  buildModelCandidates: buildOpenAIModelCandidates,
  getModelReleaseDate(item, helpers) {
    return item.release_date
      ?? helpers.extractModelReleaseDate(item.model)
      ?? inferOpenAIModelReleaseDate(item.model, helpers)
  },
  inferModelApiProtocols: inferOpenAIModelApiProtocols
}

const anthropicModelPricingDriver: ModelPricingProviderDriver = {
  id: 'anthropic',
  pricingSource: 'anthropic-pricing-snapshot',
  rawModels: anthropicModels,
  usesIncludedCacheReadUsage: false,
  supportsProvider(providerCode) {
    return normalizeProviderToken(providerCode) === ANTHROPIC_PROVIDER_CODE
  },
  buildModelCandidates: buildAnthropicModelCandidates,
  getModelReleaseDate(item, helpers) {
    return item.release_date ?? helpers.extractModelReleaseDate(item.model)
  },
  inferModelApiProtocols(item) {
    return item.supported_api_protocols?.length ? item.supported_api_protocols : []
  }
}

const modelPricingProviderDrivers: readonly ModelPricingProviderDriver[] = [
  openAIModelPricingDriver,
  anthropicModelPricingDriver
]

export function listModelPricingProviderDrivers(): readonly ModelPricingProviderDriver[] {
  return modelPricingProviderDrivers
}

export function modelPricingProviderDriverForProvider(providerCode: string | undefined): ModelPricingProviderDriver | undefined {
  const normalizedProviderCode = normalizeProviderToken(providerCode)
  if (!normalizedProviderCode) return undefined
  return modelPricingProviderDrivers.find((driver) => driver.supportsProvider(normalizedProviderCode))
}

function buildOpenAIModelCandidates(model: string): string[] {
  const candidates = new Set<string>()
  const withoutDate = model.replace(/-\d{4}-\d{2}-\d{2}$/, '')
  if (withoutDate !== model) candidates.add(withoutDate)

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

  return Array.from(candidates)
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

function inferOpenAIModelApiProtocols(
  item: RawModelPricing,
  helpers: ModelPricingProviderDriverHelpers
): ProviderModelApiProtocol[] {
  if (item.supported_api_protocols?.length) {
    return item.supported_api_protocols
  }

  const model = helpers.normalizeModel(item.model)
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

function inferOpenAIModelReleaseDate(model: string, helpers: ModelPricingProviderDriverHelpers): string | undefined {
  const normalized = helpers.normalizeModel(model)
  const exactDate = openAIModelReleaseDates.get(normalized)
  if (exactDate) return exactDate

  return undefined
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

function buildAnthropicModelCandidates(model: string): string[] {
  const candidates = new Set<string>()
  const withoutDate = model.replace(/-\d{4}-\d{2}-\d{2}$/, '')
  if (withoutDate !== model) candidates.add(withoutDate)

  for (const base of anthropicModelCandidateBasesBySpecificity) {
    if (model === base || model.startsWith(`${base}-`)) candidates.add(base)
  }

  return Array.from(candidates)
}

const anthropicModelCandidateBases = [
  'best',
  'fable',
  'opus',
  'opus[1m]',
  'opusplan',
  'sonnet',
  'sonnet[1m]',
  'haiku',
  'claude-fable-5',
  'claude-mythos-5',
  'claude-mythos-preview',
  'claude-fake-5',
  'claude-opus-4-8',
  'claude-opus-4-7',
  'claude-opus-4-6',
  'claude-opus-4-6-thinking',
  'antigravity-claude-opus-4-6-thinking',
  'antigravity/claude-opus-4-6-thinking',
  'google/antigravity-claude-opus-4-6-thinking',
  'google-antigravity/claude-opus-4-6-thinking',
  'google-antigravity:claude-opus-4-6-thinking',
  'claude-opus-4-6-antigravity',
  'claude-opus-4-5',
  'claude-opus-4-1',
  'claude-sonnet-4-6',
  'claude-sonnet-4-6-antigravity',
  'antigravity-claude-sonnet-4-6',
  'antigravity/claude-sonnet-4-6',
  'google/antigravity-claude-sonnet-4-6',
  'google-antigravity/claude-sonnet-4-6',
  'google-antigravity:claude-sonnet-4-6',
  'claude-sonnet-4-6-thinking',
  'antigravity-claude-sonnet-4-6-thinking',
  'antigravity/claude-sonnet-4-6-thinking',
  'google/antigravity-claude-sonnet-4-6-thinking',
  'google-antigravity/claude-sonnet-4-6-thinking',
  'google-antigravity:claude-sonnet-4-6-thinking',
  'claude-sonnet-4-5',
  'claude-haiku-4-5'
]
const anthropicModelCandidateBasesBySpecificity = [...anthropicModelCandidateBases]
  .sort((left, right) => right.length - left.length)
