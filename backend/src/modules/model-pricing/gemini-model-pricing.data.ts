import type { RawModelPricing } from './provider-driver.types.js'

// Curated from the official Gemini API model and pricing docs on 2026-06-25.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.
const textGenerationProtocols = ['chat_completions', 'generate_content', 'stream_generate_content', 'count_tokens'] as const
const embeddingProtocols = ['embed_content'] as const
const standardThinkingLevels = ['low', 'medium', 'high'] as const
const gemini3ThinkingLevels = ['minimal', 'low', 'medium', 'high'] as const
const standardGeminiTools = [
  'code_execution',
  'file_search',
  'function_calling',
  'google_maps_grounding',
  'google_search_grounding',
  'structured_outputs',
  'url_context'
] as const

export const geminiModelPricingData: RawModelPricing[] = [
  textModel({
    model: 'gemini-3.5-flash',
    catalogOrder: 10,
    inputUsdPer1M: 1.5,
    outputUsdPer1M: 9,
    cachedInputUsdPer1M: 0.15,
    reasoningEfforts: gemini3ThinkingLevels,
    tools: [...standardGeminiTools, 'computer_use']
  }),
  textModel({
    model: 'gemini-3.1-pro-preview',
    catalogOrder: 20,
    inputUsdPer1M: 2,
    outputUsdPer1M: 12,
    cachedInputUsdPer1M: 0.2,
    reasoningEfforts: standardThinkingLevels,
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  }),
  textModel({
    model: 'gemini-3.1-pro-preview-customtools',
    catalogOrder: 30,
    inputUsdPer1M: 2,
    outputUsdPer1M: 12,
    cachedInputUsdPer1M: 0.2,
    reasoningEfforts: standardThinkingLevels,
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  }),
  textModel({
    model: 'gemini-3-flash-preview',
    catalogOrder: 40,
    inputUsdPer1M: 0.5,
    outputUsdPer1M: 3,
    cachedInputUsdPer1M: 0.05,
    audioInputUsdPer1M: 1,
    reasoningEfforts: gemini3ThinkingLevels,
    tools: [...standardGeminiTools, 'computer_use']
  }),
  textModel({
    model: 'gemini-3.1-flash-lite',
    catalogOrder: 50,
    inputUsdPer1M: 0.25,
    outputUsdPer1M: 1.5,
    cachedInputUsdPer1M: 0.025,
    audioInputUsdPer1M: 0.5,
    reasoningEfforts: gemini3ThinkingLevels
  }),
  textModel({
    model: 'gemini-2.5-pro',
    catalogOrder: 60,
    inputUsdPer1M: 1.25,
    outputUsdPer1M: 10,
    cachedInputUsdPer1M: 0.125,
    reasoningEfforts: standardThinkingLevels,
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  }),
  textModel({
    model: 'gemini-2.5-flash',
    catalogOrder: 70,
    inputUsdPer1M: 0.3,
    outputUsdPer1M: 2.5,
    cachedInputUsdPer1M: 0.03,
    audioInputUsdPer1M: 1,
    reasoningEfforts: standardThinkingLevels,
    inputModalities: ['text', 'image', 'video', 'audio']
  }),
  textModel({
    model: 'gemini-2.5-flash-lite',
    catalogOrder: 80,
    inputUsdPer1M: 0.1,
    outputUsdPer1M: 0.4,
    cachedInputUsdPer1M: 0.01,
    audioInputUsdPer1M: 0.3,
    reasoningEfforts: standardThinkingLevels
  }),
  embeddingModel({
    model: 'gemini-embedding-2',
    catalogOrder: 100,
    inputUsdPer1M: 0.2,
    maxInputTokens: 8_192,
    inputModalities: ['text', 'image', 'video', 'audio', 'file']
  }),
  embeddingModel({
    model: 'gemini-embedding-001',
    catalogOrder: 110,
    inputUsdPer1M: 0.15,
    maxInputTokens: 2_048,
    inputModalities: ['text']
  })
]

function textModel(input: {
  model: string
  catalogOrder: number
  inputUsdPer1M: number
  outputUsdPer1M: number
  cachedInputUsdPer1M?: number
  audioInputUsdPer1M?: number
  reasoningEfforts: NonNullable<RawModelPricing['supported_reasoning_efforts']>
  inputModalities?: RawModelPricing['input_modalities']
  tools?: RawModelPricing['supported_tools']
  longContextInputTokenThreshold?: number
  longContextInputCostMultiplier?: number
  longContextOutputCostMultiplier?: number
}): RawModelPricing {
  return {
    model: input.model,
    mode: 'chat',
    catalog_order: input.catalogOrder,
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
    output_cost_per_token: usdPerToken(input.outputUsdPer1M),
    cache_read_input_token_cost: input.cachedInputUsdPer1M === undefined ? undefined : usdPerToken(input.cachedInputUsdPer1M),
    input_cost_per_audio_token: input.audioInputUsdPer1M === undefined ? undefined : usdPerToken(input.audioInputUsdPer1M),
    max_input_tokens: 1_048_576,
    max_output_tokens: 65_536,
    context_window_tokens: 1_048_576,
    input_modalities: input.inputModalities ?? ['text', 'image', 'video', 'audio', 'file'],
    output_modalities: ['text'],
    supported_api_protocols: textGenerationProtocols,
    supports_prompt_caching: true,
    supported_service_tiers: ['priority', 'flex'],
    supported_reasoning_efforts: input.reasoningEfforts,
    default_reasoning_effort: 'medium',
    supported_tools: input.tools ?? standardGeminiTools,
    long_context_input_token_threshold: input.longContextInputTokenThreshold,
    long_context_input_cost_multiplier: input.longContextInputCostMultiplier,
    long_context_output_cost_multiplier: input.longContextOutputCostMultiplier,
    catalog_visible: true
  }
}

function embeddingModel(input: {
  model: string
  catalogOrder: number
  inputUsdPer1M: number
  maxInputTokens: number
  inputModalities: RawModelPricing['input_modalities']
}): RawModelPricing {
  return {
    model: input.model,
    mode: 'embedding',
    catalog_order: input.catalogOrder,
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
    max_input_tokens: input.maxInputTokens,
    context_window_tokens: input.maxInputTokens,
    input_modalities: input.inputModalities,
    supported_api_protocols: embeddingProtocols,
    supports_prompt_caching: false,
    catalog_visible: true
  }
}

function usdPerToken(usdPer1M: number): number {
  return usdPer1M / 1_000_000
}
