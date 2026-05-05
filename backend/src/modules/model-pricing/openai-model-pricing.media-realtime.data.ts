// Generated from https://raw.githubusercontent.com/Wei-Shaw/model-price-repo/main/model_prices_and_context_window.json at 2026-05-01T17:12:56Z.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.
// OpenAI audio, image, and realtime model prices.

export const openAIMediaRealtimeModelPricingData = [
  {
    model: "gpt-audio",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 0.0000025,
    output_cost_per_token: 0.00001,
    supports_prompt_caching: false
  },
  {
    model: "gpt-audio-1.5",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 0.0000025,
    output_cost_per_token: 0.00001,
    supports_prompt_caching: false
  },
  {
    model: "gpt-audio-2025-08-28",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 0.0000025,
    output_cost_per_token: 0.00001,
    supports_prompt_caching: false
  },
  {
    model: "gpt-audio-mini",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 6e-7,
    output_cost_per_token: 0.0000024,
    supports_prompt_caching: false
  },
  {
    model: "gpt-audio-mini-2025-10-06",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 6e-7,
    output_cost_per_token: 0.0000024,
    supports_prompt_caching: false
  },
  {
    model: "gpt-audio-mini-2025-12-15",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 6e-7,
    output_cost_per_token: 0.0000024,
    supports_prompt_caching: false
  },
  {
    model: "gpt-image-1",
    litellm_provider: "openai",
    mode: "image_generation",
    input_cost_per_token: 0.000005,
    cache_read_input_token_cost: 0.00000125,
    output_cost_per_image_token: 0.00004
  },
  {
    model: "gpt-image-1-mini",
    litellm_provider: "openai",
    mode: "image_generation",
    input_cost_per_token: 0.000002,
    cache_read_input_token_cost: 2e-7,
    output_cost_per_image_token: 0.000008
  },
  {
    model: "gpt-image-1.5",
    litellm_provider: "openai",
    mode: "image_generation",
    input_cost_per_token: 0.000005,
    output_cost_per_token: 0.00001,
    cache_read_input_token_cost: 0.00000125,
    output_cost_per_image_token: 0.000032
  },
  {
    model: "gpt-image-1.5-2025-12-16",
    litellm_provider: "openai",
    mode: "image_generation",
    input_cost_per_token: 0.000005,
    output_cost_per_token: 0.00001,
    cache_read_input_token_cost: 0.00000125,
    output_cost_per_image_token: 0.000032
  },
  {
    model: "gpt-image-2",
    litellm_provider: "openai",
    mode: "image_generation",
    input_cost_per_token: 0.000005,
    output_cost_per_token: 0.00001,
    cache_read_input_token_cost: 0.00000125,
    output_cost_per_image_token: 0.00003
  },
  {
    model: "gpt-image-2-2026-04-21",
    litellm_provider: "openai",
    mode: "image_generation",
    input_cost_per_token: 0.000005,
    output_cost_per_token: 0.00001,
    cache_read_input_token_cost: 0.00000125,
    output_cost_per_image_token: 0.00003
  },
  {
    model: "gpt-realtime",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 32000,
    max_output_tokens: 4096,
    input_cost_per_token: 0.000004,
    output_cost_per_token: 0.000016,
    cache_read_input_token_cost: 4e-7
  },
  {
    model: "gpt-realtime-1.5",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 32000,
    max_output_tokens: 4096,
    input_cost_per_token: 0.000004,
    output_cost_per_token: 0.000016,
    cache_read_input_token_cost: 4e-7
  },
  {
    model: "gpt-realtime-2025-08-28",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 32000,
    max_output_tokens: 4096,
    input_cost_per_token: 0.000004,
    output_cost_per_token: 0.000016,
    cache_read_input_token_cost: 4e-7
  },
  {
    model: "gpt-realtime-mini",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    input_cost_per_token: 6e-7,
    output_cost_per_token: 0.0000024
  },
  {
    model: "gpt-realtime-mini-2025-10-06",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    input_cost_per_token: 6e-7,
    output_cost_per_token: 0.0000024,
    cache_read_input_token_cost: 6e-8
  },
  {
    model: "gpt-realtime-mini-2025-12-15",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    input_cost_per_token: 6e-7,
    output_cost_per_token: 0.0000024,
    cache_read_input_token_cost: 6e-8
  }
] as const
