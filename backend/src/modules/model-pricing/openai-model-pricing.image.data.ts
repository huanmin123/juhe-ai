// Curated from official OpenAI model, pricing, and deprecation docs on 2026-07-24.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.

export const openAIImageModelPricingData = [
  {
    model: "gpt-image-2",
    litellm_provider: "openai",
    mode: "image_generation",
    input_modalities: ["text", "image"],
    output_modalities: ["image"],
    supported_api_protocols: ["images"],
    input_cost_per_token: 0.000005,
    cache_read_input_token_cost: 0.00000125,
    input_cost_per_image_token: 0.000008,
    cache_read_input_image_token_cost: 0.000002,
    output_cost_per_image_token: 0.00003,
    supports_prompt_caching: true
  },
  {
    model: "gpt-image-2-2026-04-21",
    litellm_provider: "openai",
    mode: "image_generation",
    input_modalities: ["text", "image"],
    output_modalities: ["image"],
    supported_api_protocols: ["images"],
    input_cost_per_token: 0.000005,
    cache_read_input_token_cost: 0.00000125,
    input_cost_per_image_token: 0.000008,
    cache_read_input_image_token_cost: 0.000002,
    output_cost_per_image_token: 0.00003,
    supports_prompt_caching: true
  },
  {
    model: "gpt-image-1.5",
    litellm_provider: "openai",
    mode: "image_generation",
    input_modalities: ["text", "image"],
    output_modalities: ["image", "text"],
    supported_api_protocols: ["images"],
    input_cost_per_token: 0.000005,
    output_cost_per_token: 0.00001,
    cache_read_input_token_cost: 0.00000125,
    input_cost_per_image_token: 0.000008,
    cache_read_input_image_token_cost: 0.000002,
    output_cost_per_image_token: 0.000032,
    supports_prompt_caching: true,
    shutdown_date: "2026-12-01"
  },
  {
    model: "gpt-image-1-mini",
    litellm_provider: "openai",
    mode: "image_generation",
    input_modalities: ["text", "image"],
    output_modalities: ["image", "text"],
    supported_api_protocols: ["images"],
    input_cost_per_token: 0.000002,
    cache_read_input_token_cost: 2e-7,
    input_cost_per_image_token: 0.0000025,
    cache_read_input_image_token_cost: 2.5e-7,
    output_cost_per_image_token: 0.000008,
    supports_prompt_caching: true,
    shutdown_date: "2026-12-01"
  },
  {
    model: "gpt-image-1",
    litellm_provider: "openai",
    mode: "image_generation",
    input_modalities: ["text", "image"],
    output_modalities: ["image"],
    supported_api_protocols: ["images", "responses"],
    input_cost_per_token: 0.000005,
    cache_read_input_token_cost: 0.00000125,
    input_cost_per_image_token: 0.00001,
    cache_read_input_image_token_cost: 0.0000025,
    output_cost_per_image_token: 0.00004,
    supports_prompt_caching: true,
    shutdown_date: "2026-10-23"
  }
] as const
