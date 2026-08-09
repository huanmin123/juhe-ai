// Curated from official DeepSeek API pricing docs and change log on 2026-07-23.
// DeepSeek currently documents Responses for deepseek-v4-flash only. V4 Pro is
// deliberately listed for product-level pre-compatibility until upstream status changes.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.

export const deepSeekModelPricingData = [
  {
    model: 'deepseek-v4-flash',
    mode: 'chat',
    catalog_order: 10,
    release_date: '2026-04-24',
    input_cost_per_token: 0.14 / 1_000_000,
    cache_read_input_token_cost: 0.0028 / 1_000_000,
    output_cost_per_token: 0.28 / 1_000_000,
    context_window_tokens: 1_000_000,
    max_output_tokens: 384_000,
    supports_prompt_caching: true,
    supported_api_protocols: ['chat_completions', 'responses', 'messages'],
    input_modalities: ['text'],
    output_modalities: ['text'],
    supported_tools: ['function_calling'],
    supported_reasoning_efforts: ['high', 'max'],
    default_reasoning_effort: 'high'
  },
  {
    model: 'deepseek-v4-pro',
    mode: 'chat',
    catalog_order: 20,
    release_date: '2026-04-24',
    input_cost_per_token: 0.435 / 1_000_000,
    cache_read_input_token_cost: 0.003625 / 1_000_000,
    output_cost_per_token: 0.87 / 1_000_000,
    context_window_tokens: 1_000_000,
    max_output_tokens: 384_000,
    supports_prompt_caching: true,
    supported_api_protocols: ['chat_completions', 'responses', 'messages', 'completions'],
    input_modalities: ['text'],
    output_modalities: ['text'],
    supported_tools: ['function_calling'],
    supported_reasoning_efforts: ['high', 'max'],
    default_reasoning_effort: 'high'
  },
  {
    model: 'deepseek-chat',
    mode: 'chat',
    catalog_order: 50,
    release_date: '2026-04-24',
    input_cost_per_token: 0.14 / 1_000_000,
    cache_read_input_token_cost: 0.0028 / 1_000_000,
    output_cost_per_token: 0.28 / 1_000_000,
    context_window_tokens: 1_000_000,
    max_output_tokens: 384_000,
    shutdown_date: '2026-07-24',
    supports_prompt_caching: true,
    supported_api_protocols: ['chat_completions', 'messages'],
    input_modalities: ['text'],
    output_modalities: ['text'],
    supported_tools: ['function_calling']
  },
  {
    model: 'deepseek-reasoner',
    mode: 'chat',
    catalog_order: 60,
    release_date: '2025-01-20',
    input_cost_per_token: 0.14 / 1_000_000,
    cache_read_input_token_cost: 0.0028 / 1_000_000,
    output_cost_per_token: 0.28 / 1_000_000,
    context_window_tokens: 1_000_000,
    max_output_tokens: 384_000,
    shutdown_date: '2026-07-24',
    supports_prompt_caching: true,
    supported_api_protocols: ['chat_completions', 'messages'],
    input_modalities: ['text'],
    output_modalities: ['text'],
    supported_tools: ['function_calling'],
    supported_reasoning_efforts: ['high', 'max'],
    default_reasoning_effort: 'high'
  }
] as const
