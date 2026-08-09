// Curated from Anthropic's official model API, model announcements, pricing,
// and deprecation pages. Prices are USD per token.
// https://docs.anthropic.com/en/docs/about-claude/models
// https://docs.anthropic.com/en/docs/about-claude/pricing
// https://www.anthropic.com/news/claude-opus-5
// https://docs.anthropic.com/en/docs/about-claude/models/introducing-claude-fable-5-and-claude-mythos-5

type AnthropicModelConfig = {
  model: string
  catalog_order: number
  release_date: string
  input_usd_per_million: number
  output_usd_per_million: number
  context_window_tokens: number
  max_input_tokens: number
  max_output_tokens: number
  supported_reasoning_efforts: readonly string[]
  default_reasoning_effort?: string
  shutdown_date?: string
}

const model = (config: AnthropicModelConfig) => ({
  model: config.model,
  catalog_order: config.catalog_order,
  release_date: config.release_date,
  shutdown_date: config.shutdown_date,
  input_cost_per_token: config.input_usd_per_million / 1_000_000,
  output_cost_per_token: config.output_usd_per_million / 1_000_000,
  cache_creation_input_token_cost: config.input_usd_per_million * 1.25 / 1_000_000,
  cache_creation_input_token_cost_above_1hr: config.input_usd_per_million * 2 / 1_000_000,
  cache_read_input_token_cost: config.input_usd_per_million * 0.1 / 1_000_000,
  context_window_tokens: config.context_window_tokens,
  max_input_tokens: config.max_input_tokens,
  max_output_tokens: config.max_output_tokens,
  supported_api_protocols: ['messages', 'message_token_counting'],
  supports_prompt_caching: true,
  supported_service_tiers: [],
  input_modalities: ['text', 'image'],
  output_modalities: ['text'],
  supported_tools: ['function_calling', 'code_execution'],
  supported_reasoning_efforts: config.supported_reasoning_efforts,
  default_reasoning_effort: config.default_reasoning_effort
})

export const anthropicModelPricingData = [
  model({
    model: 'claude-opus-5', catalog_order: 5, release_date: '2026-07-24',
    input_usd_per_million: 5, output_usd_per_million: 25,
    context_window_tokens: 1_000_000, max_input_tokens: 1_000_000, max_output_tokens: 128_000,
    supported_reasoning_efforts: ['low', 'medium', 'high', 'xhigh', 'max'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-fable-5', catalog_order: 10, release_date: '2026-06-09',
    input_usd_per_million: 10, output_usd_per_million: 50,
    context_window_tokens: 1_000_000, max_input_tokens: 1_000_000, max_output_tokens: 128_000,
    supported_reasoning_efforts: ['low', 'medium', 'high', 'xhigh', 'max'], default_reasoning_effort: 'high'
  }),
  // Claude Sonnet 5 remains at its $2/$10 introductory price through 2026-08-31;
  // update the snapshot after that date instead of adding a runtime date branch.
  model({
    model: 'claude-sonnet-5', catalog_order: 25, release_date: '2026-06-30',
    input_usd_per_million: 2, output_usd_per_million: 10,
    context_window_tokens: 1_000_000, max_input_tokens: 1_000_000, max_output_tokens: 128_000,
    supported_reasoning_efforts: ['low', 'medium', 'high', 'xhigh', 'max'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-opus-4-8', catalog_order: 40, release_date: '2026-05-28',
    input_usd_per_million: 5, output_usd_per_million: 25,
    context_window_tokens: 1_000_000, max_input_tokens: 1_000_000, max_output_tokens: 128_000,
    supported_reasoning_efforts: ['low', 'medium', 'high', 'xhigh', 'max'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-opus-4-7', catalog_order: 50, release_date: '2026-04-16',
    input_usd_per_million: 5, output_usd_per_million: 25,
    context_window_tokens: 1_000_000, max_input_tokens: 1_000_000, max_output_tokens: 128_000,
    supported_reasoning_efforts: ['low', 'medium', 'high', 'xhigh', 'max'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-opus-4-6', catalog_order: 60, release_date: '2026-02-05',
    input_usd_per_million: 5, output_usd_per_million: 25,
    context_window_tokens: 1_000_000, max_input_tokens: 1_000_000, max_output_tokens: 128_000,
    supported_reasoning_efforts: ['low', 'medium', 'high', 'max'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-opus-4-5', catalog_order: 80, release_date: '2025-11-24',
    input_usd_per_million: 5, output_usd_per_million: 25,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 64_000,
    supported_reasoning_efforts: ['low', 'medium', 'high'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-opus-4-5-20251101', catalog_order: 90, release_date: '2025-11-01',
    input_usd_per_million: 5, output_usd_per_million: 25,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 64_000,
    supported_reasoning_efforts: ['low', 'medium', 'high'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-opus-4-1', catalog_order: 100, release_date: '2025-08-05', shutdown_date: '2026-08-05',
    input_usd_per_million: 15, output_usd_per_million: 75,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 32_000,
    supported_reasoning_efforts: []
  }),
  model({
    model: 'claude-opus-4-1-20250805', catalog_order: 110, release_date: '2025-08-05', shutdown_date: '2026-08-05',
    input_usd_per_million: 15, output_usd_per_million: 75,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 32_000,
    supported_reasoning_efforts: []
  }),
  model({
    model: 'claude-sonnet-4-6', catalog_order: 120, release_date: '2026-02-17',
    input_usd_per_million: 3, output_usd_per_million: 15,
    context_window_tokens: 1_000_000, max_input_tokens: 1_000_000, max_output_tokens: 64_000,
    supported_reasoning_efforts: ['low', 'medium', 'high', 'max'], default_reasoning_effort: 'high'
  }),
  model({
    model: 'claude-sonnet-4-5', catalog_order: 140, release_date: '2025-09-29',
    input_usd_per_million: 3, output_usd_per_million: 15,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 64_000,
    supported_reasoning_efforts: []
  }),
  model({
    model: 'claude-sonnet-4-5-20250929', catalog_order: 150, release_date: '2025-09-29',
    input_usd_per_million: 3, output_usd_per_million: 15,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 64_000,
    supported_reasoning_efforts: []
  }),
  model({
    model: 'claude-haiku-4-5', catalog_order: 160, release_date: '2025-10-15',
    input_usd_per_million: 1, output_usd_per_million: 5,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 64_000,
    supported_reasoning_efforts: []
  }),
  model({
    model: 'claude-haiku-4-5-20251001', catalog_order: 170, release_date: '2025-10-01',
    input_usd_per_million: 1, output_usd_per_million: 5,
    context_window_tokens: 200_000, max_input_tokens: 200_000, max_output_tokens: 64_000,
    supported_reasoning_efforts: []
  })
]
