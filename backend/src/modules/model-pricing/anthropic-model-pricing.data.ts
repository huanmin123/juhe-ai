import type { RawModelPricing } from './provider-driver.types.js'

// Curated from the official Anthropic model, pricing, and thinking docs on 2026-07-13.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.
const claudeProtocols = ['messages', 'message_token_counting'] as const
const claudeInputModalities = ['text', 'image'] as const
const claudeOutputModalities = ['text'] as const
const claudeTools = ['function_calling', 'code_execution'] as const
const adaptiveThinkingLevels = ['low', 'medium', 'high', 'max'] as const
const adaptiveThinkingLevelsWithXhigh = ['low', 'medium', 'high', 'xhigh', 'max'] as const

export const anthropicModelPricingData: RawModelPricing[] = [
  claudeModel({
    model: 'claude-fable-5',
    catalogOrder: 10,
    inputUsdPer1M: 10,
    outputUsdPer1M: 50,
    contextWindowTokens: 1_000_000,
    maxOutputTokens: 128_000,
    reasoningEfforts: adaptiveThinkingLevelsWithXhigh
  }),
  claudeModel({
    model: 'claude-sonnet-5',
    catalogOrder: 20,
    inputUsdPer1M: 2,
    outputUsdPer1M: 10,
    contextWindowTokens: 1_000_000,
    maxOutputTokens: 128_000,
    reasoningEfforts: adaptiveThinkingLevelsWithXhigh
  }),
  claudeModel({
    model: 'claude-opus-4-8',
    catalogOrder: 30,
    inputUsdPer1M: 5,
    outputUsdPer1M: 25,
    contextWindowTokens: 1_000_000,
    maxOutputTokens: 128_000,
    reasoningEfforts: adaptiveThinkingLevelsWithXhigh
  }),
  claudeModel({
    model: 'claude-opus-4-7',
    catalogOrder: 40,
    inputUsdPer1M: 5,
    outputUsdPer1M: 25,
    contextWindowTokens: 1_000_000,
    maxOutputTokens: 128_000,
    reasoningEfforts: adaptiveThinkingLevelsWithXhigh
  }),
  claudeModel({
    model: 'claude-opus-4-6',
    catalogOrder: 50,
    inputUsdPer1M: 5,
    outputUsdPer1M: 25,
    contextWindowTokens: 1_000_000,
    maxOutputTokens: 128_000,
    reasoningEfforts: adaptiveThinkingLevels
  }),
  claudeModel({
    model: 'claude-sonnet-4-6',
    catalogOrder: 60,
    inputUsdPer1M: 3,
    outputUsdPer1M: 15,
    contextWindowTokens: 1_000_000,
    maxOutputTokens: 128_000,
    reasoningEfforts: adaptiveThinkingLevels
  }),
  claudeModel({
    model: 'claude-opus-4-5',
    catalogOrder: 70,
    inputUsdPer1M: 5,
    outputUsdPer1M: 25,
    contextWindowTokens: 200_000,
    maxOutputTokens: 64_000
  }),
  claudeModel({
    model: 'claude-opus-4-5-20251101',
    catalogOrder: 80,
    releaseDate: '2025-11-01',
    inputUsdPer1M: 5,
    outputUsdPer1M: 25,
    contextWindowTokens: 200_000,
    maxOutputTokens: 64_000
  }),
  claudeModel({
    model: 'claude-sonnet-4-5',
    catalogOrder: 90,
    inputUsdPer1M: 3,
    outputUsdPer1M: 15,
    contextWindowTokens: 200_000,
    maxOutputTokens: 64_000
  }),
  claudeModel({
    model: 'claude-sonnet-4-5-20250929',
    catalogOrder: 100,
    releaseDate: '2025-09-29',
    inputUsdPer1M: 3,
    outputUsdPer1M: 15,
    contextWindowTokens: 200_000,
    maxOutputTokens: 64_000
  }),
  claudeModel({
    model: 'claude-haiku-4-5',
    catalogOrder: 110,
    inputUsdPer1M: 1,
    outputUsdPer1M: 5,
    contextWindowTokens: 200_000,
    maxOutputTokens: 64_000
  }),
  claudeModel({
    model: 'claude-haiku-4-5-20251001',
    catalogOrder: 120,
    releaseDate: '2025-10-01',
    inputUsdPer1M: 1,
    outputUsdPer1M: 5,
    contextWindowTokens: 200_000,
    maxOutputTokens: 64_000
  }),
  claudeModel({
    model: 'claude-opus-4-1',
    catalogOrder: 130,
    releaseDate: '2025-08-05',
    shutdownDate: '2026-08-05',
    inputUsdPer1M: 15,
    outputUsdPer1M: 75,
    contextWindowTokens: 200_000,
    maxOutputTokens: 32_000
  }),
  claudeModel({
    model: 'claude-opus-4-1-20250805',
    catalogOrder: 140,
    releaseDate: '2025-08-05',
    shutdownDate: '2026-08-05',
    inputUsdPer1M: 15,
    outputUsdPer1M: 75,
    contextWindowTokens: 200_000,
    maxOutputTokens: 32_000
  }),
  claudeModel({
    model: 'claude-mythos-5',
    catalogOrder: 900,
    catalogVisible: false,
    inputUsdPer1M: 10,
    outputUsdPer1M: 50,
    contextWindowTokens: 1_000_000,
    maxOutputTokens: 128_000,
    reasoningEfforts: adaptiveThinkingLevelsWithXhigh
  }),
  ...compatibilityAliases()
]

function compatibilityAliases(): RawModelPricing[] {
  const aliases: Array<{
    models: string[]
    inputUsdPer1M: number
    outputUsdPer1M: number
    contextWindowTokens?: number
    maxOutputTokens?: number
    reasoningEfforts?: NonNullable<RawModelPricing['supported_reasoning_efforts']>
  }> = [
    {
      models: ['best', 'fable'],
      inputUsdPer1M: 10,
      outputUsdPer1M: 50,
      contextWindowTokens: 1_000_000,
      maxOutputTokens: 128_000,
      reasoningEfforts: adaptiveThinkingLevelsWithXhigh
    },
    {
      models: ['opus', 'opus[1m]', 'opusplan'],
      inputUsdPer1M: 5,
      outputUsdPer1M: 25,
      contextWindowTokens: 1_000_000,
      maxOutputTokens: 128_000,
      reasoningEfforts: adaptiveThinkingLevelsWithXhigh
    },
    {
      models: ['sonnet', 'sonnet[1m]'],
      inputUsdPer1M: 3,
      outputUsdPer1M: 15,
      contextWindowTokens: 1_000_000,
      maxOutputTokens: 128_000,
      reasoningEfforts: adaptiveThinkingLevels
    },
    {
      models: ['haiku'],
      inputUsdPer1M: 1,
      outputUsdPer1M: 5,
      contextWindowTokens: 200_000,
      maxOutputTokens: 64_000
    },
    {
      models: [
        'claude-opus-4-6-thinking',
        'antigravity-claude-opus-4-6-thinking',
        'antigravity/claude-opus-4-6-thinking',
        'google/antigravity-claude-opus-4-6-thinking',
        'google-antigravity/claude-opus-4-6-thinking',
        'google-antigravity:claude-opus-4-6-thinking',
        'claude-opus-4-6-antigravity'
      ],
      inputUsdPer1M: 5,
      outputUsdPer1M: 25,
      contextWindowTokens: 1_000_000,
      maxOutputTokens: 128_000,
      reasoningEfforts: adaptiveThinkingLevels
    },
    {
      models: [
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
        'google-antigravity:claude-sonnet-4-6-thinking'
      ],
      inputUsdPer1M: 3,
      outputUsdPer1M: 15,
      contextWindowTokens: 1_000_000,
      maxOutputTokens: 128_000,
      reasoningEfforts: adaptiveThinkingLevels
    },
    {
      models: ['claude-fake-5'],
      inputUsdPer1M: 1,
      outputUsdPer1M: 5
    }
  ]

  let catalogOrder = 1_000
  return aliases.flatMap((group) => group.models.map((model) => compatibilityAlias({
    model,
    catalogOrder: catalogOrder++,
    inputUsdPer1M: group.inputUsdPer1M,
    outputUsdPer1M: group.outputUsdPer1M,
    contextWindowTokens: group.contextWindowTokens,
    maxOutputTokens: group.maxOutputTokens,
    reasoningEfforts: group.reasoningEfforts
  })))
}

function compatibilityAlias(input: {
  model: string
  catalogOrder: number
  inputUsdPer1M: number
  outputUsdPer1M: number
  contextWindowTokens?: number
  maxOutputTokens?: number
  reasoningEfforts?: NonNullable<RawModelPricing['supported_reasoning_efforts']>
}): RawModelPricing {
  return {
    model: input.model,
    mode: 'chat',
    catalog_order: input.catalogOrder,
    catalog_visible: false,
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
    output_cost_per_token: usdPerToken(input.outputUsdPer1M),
    cache_creation_input_token_cost: usdPerToken(input.inputUsdPer1M * 1.25),
    cache_creation_input_token_cost_above_1hr: usdPerToken(input.inputUsdPer1M * 2),
    cache_read_input_token_cost: usdPerToken(input.inputUsdPer1M * 0.1),
    context_window_tokens: input.contextWindowTokens,
    max_output_tokens: input.maxOutputTokens,
    input_modalities: claudeInputModalities,
    output_modalities: claudeOutputModalities,
    supported_tools: claudeTools,
    supported_api_protocols: claudeProtocols,
    supports_prompt_caching: true,
    supported_reasoning_efforts: input.reasoningEfforts,
    default_reasoning_effort: input.reasoningEfforts ? 'medium' : undefined
  }
}

function claudeModel(input: {
  model: string
  catalogOrder: number
  releaseDate?: string
  shutdownDate?: string
  catalogVisible?: boolean
  inputUsdPer1M: number
  outputUsdPer1M: number
  contextWindowTokens: number
  maxOutputTokens: number
  reasoningEfforts?: NonNullable<RawModelPricing['supported_reasoning_efforts']>
}): RawModelPricing {
  return {
    model: input.model,
    mode: 'chat',
    catalog_order: input.catalogOrder,
    release_date: input.releaseDate,
    shutdown_date: input.shutdownDate,
    catalog_visible: input.catalogVisible ?? true,
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
    output_cost_per_token: usdPerToken(input.outputUsdPer1M),
    cache_creation_input_token_cost: usdPerToken(input.inputUsdPer1M * 1.25),
    cache_creation_input_token_cost_above_1hr: usdPerToken(input.inputUsdPer1M * 2),
    cache_read_input_token_cost: usdPerToken(input.inputUsdPer1M * 0.1),
    context_window_tokens: input.contextWindowTokens,
    max_output_tokens: input.maxOutputTokens,
    input_modalities: claudeInputModalities,
    output_modalities: claudeOutputModalities,
    supported_tools: claudeTools,
    supported_api_protocols: claudeProtocols,
    supports_prompt_caching: true,
    supported_reasoning_efforts: input.reasoningEfforts,
    default_reasoning_effort: input.reasoningEfforts ? 'medium' : undefined
  }
}

function usdPerToken(usdPer1M: number): number {
  return usdPer1M / 1_000_000
}
