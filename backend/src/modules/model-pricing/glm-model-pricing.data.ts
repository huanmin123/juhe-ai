// Curated from official Z.ai GLM API pricing docs on 2026-06-20.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.

export const glmModelPricingData = [
  {
    model: 'glm-5.2-free',
    mode: 'chat',
    input_cost_per_token: 0,
    output_cost_per_token: 0,
    max_input_tokens: 1_000_000,
    max_output_tokens: 131_072,
    supported_api_protocols: ['chat_completions'],
    supports_prompt_caching: false,
    supports_service_tier: false
  },
  {
    model: 'glm-5.2',
    mode: 'chat',
    input_cost_per_token: 1.4 / 1_000_000,
    cache_read_input_token_cost: 0.26 / 1_000_000,
    output_cost_per_token: 4.4 / 1_000_000,
    max_input_tokens: 1_000_000,
    max_output_tokens: 131_072,
    supported_api_protocols: ['chat_completions'],
    supports_prompt_caching: true,
    supports_service_tier: false
  },
  {
    model: 'glm-5-turbo',
    mode: 'chat',
    input_cost_per_token: 1.2 / 1_000_000,
    cache_read_input_token_cost: 0.24 / 1_000_000,
    output_cost_per_token: 4.0 / 1_000_000,
    supported_api_protocols: ['chat_completions'],
    supports_prompt_caching: true,
    supports_service_tier: false
  }
] as const
