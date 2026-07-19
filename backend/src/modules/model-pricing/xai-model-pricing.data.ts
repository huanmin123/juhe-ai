// Curated from the official xAI models and pricing pages on 2026-07-18.
// Token prices are USD per token. Models with a 200k threshold charge the
// higher rate for all request tokens once the prompt reaches the threshold.

export const xAIModelPricingData = [
  textModel('grok-4.5', 500_000, 2, 0.5, 6),
  textModel('grok-4.3', 1_000_000, 1.25, 0.2, 2.5),
  textModel('grok-4.20-0309-reasoning', 1_000_000, 1.25, 0.2, 2.5),
  textModel('grok-4.20-0309-non-reasoning', 1_000_000, 1.25, 0.2, 2.5),
  textModel('grok-build-0.1', 256_000, 1, 0.2, 2),
  textModel('grok-4.20-multi-agent-0309', 1_000_000, 1.25, 0.2, 2.5),
  {
    model: 'grok-imagine-image',
    mode: 'image',
    output_cost_per_image: 0.02,
    supported_api_protocols: ['images'] as const,
    input_modalities: ['text'] as const,
    output_modalities: ['image'] as const
  },
  {
    model: 'grok-imagine-image-quality',
    mode: 'image',
    output_cost_per_image: 0.05,
    supported_api_protocols: ['images'] as const,
    input_modalities: ['text'] as const,
    output_modalities: ['image'] as const
  }
] as const

function textModel(
  model: string,
  contextWindowTokens: number,
  inputUsdPer1M: number,
  cachedInputUsdPer1M: number,
  outputUsdPer1M: number
) {
  return {
    model,
    mode: 'chat',
    context_window_tokens: contextWindowTokens,
    input_cost_per_token: inputUsdPer1M / 1_000_000,
    cache_read_input_token_cost: cachedInputUsdPer1M / 1_000_000,
    output_cost_per_token: outputUsdPer1M / 1_000_000,
    input_cost_per_token_priority: inputUsdPer1M * 2 / 1_000_000,
    cache_read_input_token_cost_priority: cachedInputUsdPer1M * 2 / 1_000_000,
    output_cost_per_token_priority: outputUsdPer1M * 2 / 1_000_000,
    long_context_input_token_threshold: 200_000,
    long_context_input_token_threshold_inclusive: true,
    long_context_input_cost_multiplier: 2,
    long_context_output_cost_multiplier: 2,
    supports_prompt_caching: true,
    supported_service_tiers: ['priority'] as const,
    supported_api_protocols: ['chat_completions', 'responses'] as const,
    input_modalities: ['text', 'image'] as const,
    output_modalities: ['text'] as const,
    supported_tools: [] as const,
    supported_reasoning_efforts: [] as const
  }
}
