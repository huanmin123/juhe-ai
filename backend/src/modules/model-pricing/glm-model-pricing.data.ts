// Curated from official Z.ai / BigModel GLM text model and pricing docs on 2026-06-22.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.

export const glmModelPricingData = [
  {
    model: 'glm-5.2',
    mode: 'chat',
    catalog_order: 10,
    release_date: '2026-06-16',
    input_cost_per_token: 1.4 / 1_000_000,
    cache_read_input_token_cost: 0.26 / 1_000_000,
    output_cost_per_token: 4.4 / 1_000_000,
    context_window_tokens: 1_000_000,
    max_output_tokens: 128_000,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"],
    supported_reasoning_efforts: ["high","max"],
    default_reasoning_effort: "max"
  },
  {
    model: 'glm-5.1',
    mode: 'chat',
    catalog_order: 20,
    release_date: '2026-04-07',
    input_cost_per_token: 1.4 / 1_000_000,
    cache_read_input_token_cost: 0.26 / 1_000_000,
    output_cost_per_token: 4.4 / 1_000_000,
    context_window_tokens: 200_000,
    max_output_tokens: 128_000,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-5',
    mode: 'chat',
    catalog_order: 30,
    release_date: '2026-02-12',
    input_cost_per_token: 1.0 / 1_000_000,
    cache_read_input_token_cost: 0.2 / 1_000_000,
    output_cost_per_token: 3.2 / 1_000_000,
    context_window_tokens: 200_000,
    max_output_tokens: 128_000,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-5-turbo',
    mode: 'chat',
    catalog_order: 40,
    release_date: '2026-03-16',
    input_cost_per_token: 1.2 / 1_000_000,
    cache_read_input_token_cost: 0.24 / 1_000_000,
    output_cost_per_token: 4.0 / 1_000_000,
    context_window_tokens: 200_000,
    max_output_tokens: 128_000,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.7',
    mode: 'chat',
    catalog_order: 50,
    release_date: '2025-12-22',
    input_cost_per_token: 0.6 / 1_000_000,
    cache_read_input_token_cost: 0.11 / 1_000_000,
    output_cost_per_token: 2.2 / 1_000_000,
    context_window_tokens: 200_000,
    max_output_tokens: 131_072,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.7-flashx',
    mode: 'chat',
    catalog_order: 60,
    release_date: '2025-12-22',
    input_cost_per_token: 0.07 / 1_000_000,
    cache_read_input_token_cost: 0.01 / 1_000_000,
    output_cost_per_token: 0.4 / 1_000_000,
    context_window_tokens: 200_000,
    max_output_tokens: 131_072,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.7-flash',
    mode: 'chat',
    catalog_order: 70,
    release_date: '2025-12-22',
    input_cost_per_token: 0,
    cache_read_input_token_cost: 0,
    output_cost_per_token: 0,
    context_window_tokens: 200_000,
    max_output_tokens: 131_072,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.6',
    mode: 'chat',
    catalog_order: 80,
    release_date: '2025-09-30',
    input_cost_per_token: 0.6 / 1_000_000,
    cache_read_input_token_cost: 0.11 / 1_000_000,
    output_cost_per_token: 2.2 / 1_000_000,
    context_window_tokens: 200_000,
    max_output_tokens: 131_072,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.5',
    mode: 'chat',
    catalog_order: 90,
    release_date: '2025-07-28',
    input_cost_per_token: 0.6 / 1_000_000,
    cache_read_input_token_cost: 0.11 / 1_000_000,
    output_cost_per_token: 2.2 / 1_000_000,
    context_window_tokens: 128_000,
    max_output_tokens: 98_304,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.5-x',
    mode: 'chat',
    catalog_order: 100,
    release_date: '2025-07-28',
    input_cost_per_token: 2.2 / 1_000_000,
    cache_read_input_token_cost: 0.45 / 1_000_000,
    output_cost_per_token: 8.9 / 1_000_000,
    context_window_tokens: 128_000,
    max_output_tokens: 98_304,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.5-air',
    mode: 'chat',
    catalog_order: 110,
    release_date: '2025-07-28',
    input_cost_per_token: 0.2 / 1_000_000,
    cache_read_input_token_cost: 0.03 / 1_000_000,
    output_cost_per_token: 1.1 / 1_000_000,
    context_window_tokens: 128_000,
    max_output_tokens: 98_304,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.5-airx',
    mode: 'chat',
    catalog_order: 120,
    release_date: '2025-07-28',
    input_cost_per_token: 1.1 / 1_000_000,
    cache_read_input_token_cost: 0.22 / 1_000_000,
    output_cost_per_token: 4.5 / 1_000_000,
    context_window_tokens: 128_000,
    max_output_tokens: 98_304,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  },
  {
    model: 'glm-4.5-flash',
    mode: 'chat',
    catalog_order: 130,
    release_date: '2025-07-28',
    input_cost_per_token: 0,
    cache_read_input_token_cost: 0,
    output_cost_per_token: 0,
    context_window_tokens: 128_000,
    max_output_tokens: 98_304,
    supported_api_protocols: ["chat_completions"],
    supports_prompt_caching: true,
    supports_service_tier: false
  ,
    input_modalities: ["text"],
    output_modalities: ["text"]
  }
] as const
