// Curated from official OpenAI model, pricing, and deprecation docs on 2026-05-08.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.
// Current o-series reasoning model prices.

export const openAIReasoningModelPricingData = [
  {
    model: "o3",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 100000,
    max_input_tokens: 200000,
    max_output_tokens: 100000,
    input_cost_per_token: 0.000002,
    input_cost_per_token_priority: 0.0000035,
    output_cost_per_token: 0.000008,
    output_cost_per_token_priority: 0.000014,
    cache_read_input_token_cost: 5e-7,
    cache_read_input_token_cost_priority: 8.75e-7,
    supports_prompt_caching: true,
    supports_service_tier: true
  },
  {
    model: "o3-pro",
    litellm_provider: "openai",
    mode: "responses",
    max_tokens: 100000,
    max_input_tokens: 200000,
    max_output_tokens: 100000,
    input_cost_per_token: 0.00002,
    output_cost_per_token: 0.00008,
    supports_prompt_caching: true
  },
  {
    model: "o4-mini",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 100000,
    max_input_tokens: 200000,
    max_output_tokens: 100000,
    input_cost_per_token: 0.0000011,
    input_cost_per_token_priority: 0.000002,
    output_cost_per_token: 0.0000044,
    output_cost_per_token_priority: 0.000008,
    cache_read_input_token_cost: 2.75e-7,
    cache_read_input_token_cost_priority: 5e-7,
    supports_prompt_caching: true,
    supports_service_tier: true
  }
] as const
