// Curated from official DeepSeek API model, pricing, and thinking docs on 2026-07-13.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.

export const deepSeekModelPricingData = [
  {
    model: 'deepseek-v4-flash',
    mode: 'chat',
    catalog_order: 10,
    release_date: '2026-06-20',
    input_cost_per_token: 0.14 / 1_000_000,
    cache_read_input_token_cost: 0.0028 / 1_000_000,
    output_cost_per_token: 0.28 / 1_000_000,
    context_window_tokens: 1_000_000,
    max_output_tokens: 384_000,
    input_modalities: ['text'],
    output_modalities: ['text'],
    supports_prompt_caching: true,
    supported_reasoning_efforts: ['high', 'max'],
    default_reasoning_effort: 'high',
    supported_api_protocols: ['chat_completions', 'messages']
  },
  {
    model: 'deepseek-v4-pro',
    mode: 'chat',
    catalog_order: 20,
    release_date: '2026-06-20',
    input_cost_per_token: 0.435 / 1_000_000,
    cache_read_input_token_cost: 0.003625 / 1_000_000,
    output_cost_per_token: 0.87 / 1_000_000,
    context_window_tokens: 1_000_000,
    max_output_tokens: 384_000,
    input_modalities: ['text'],
    output_modalities: ['text'],
    supports_prompt_caching: true,
    supported_reasoning_efforts: ['high', 'max'],
    default_reasoning_effort: 'high',
    supported_api_protocols: ['chat_completions', 'messages', 'completions']
  }
] as const
