// Curated from official OpenAI model, pricing, and deprecation docs on 2026-05-08.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.
// Current GPT-4.1 and GPT-4o family model prices.

export const openAIGPT4ModelPricingData = [
  {
    model: "gpt-4.1",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 32768,
    max_input_tokens: 1047576,
    max_output_tokens: 32768,
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
    model: "gpt-4.1-mini",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 32768,
    max_input_tokens: 1047576,
    max_output_tokens: 32768,
    input_cost_per_token: 4e-7,
    input_cost_per_token_priority: 7e-7,
    output_cost_per_token: 0.0000016,
    output_cost_per_token_priority: 0.0000028,
    cache_read_input_token_cost: 1e-7,
    cache_read_input_token_cost_priority: 1.75e-7,
    supports_prompt_caching: true,
    supports_service_tier: true
  },
  {
    model: "gpt-4.1-nano",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 32768,
    max_input_tokens: 1047576,
    max_output_tokens: 32768,
    input_cost_per_token: 1e-7,
    input_cost_per_token_priority: 2e-7,
    output_cost_per_token: 4e-7,
    output_cost_per_token_priority: 8e-7,
    cache_read_input_token_cost: 2.5e-8,
    cache_read_input_token_cost_priority: 5e-8,
    shutdown_date: "2026-10-23",
    supports_prompt_caching: true,
    supports_service_tier: true
  },
  {
    model: "gpt-4o",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 0.0000025,
    input_cost_per_token_priority: 0.00000425,
    output_cost_per_token: 0.00001,
    output_cost_per_token_priority: 0.000017,
    cache_read_input_token_cost: 0.00000125,
    cache_read_input_token_cost_priority: 0.000002125,
    supports_prompt_caching: true,
    supports_service_tier: true
  },
  {
    model: "gpt-4o-mini",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 16384,
    max_input_tokens: 128000,
    max_output_tokens: 16384,
    input_cost_per_token: 1.5e-7,
    input_cost_per_token_priority: 2.5e-7,
    output_cost_per_token: 6e-7,
    output_cost_per_token_priority: 0.000001,
    cache_read_input_token_cost: 7.5e-8,
    cache_read_input_token_cost_priority: 1.25e-7,
    supports_prompt_caching: true,
    supports_service_tier: true
  },
  {
    model: "gpt-4o-2024-05-13",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    input_cost_per_token: 0.000005,
    output_cost_per_token: 0.000015,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-4-turbo",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    input_cost_per_token: 0.00001,
    output_cost_per_token: 0.00003,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-4-turbo-2024-04-09",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    input_cost_per_token: 0.00001,
    output_cost_per_token: 0.00003,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-4-1106-preview",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 128000,
    max_output_tokens: 4096,
    input_cost_per_token: 0.00001,
    output_cost_per_token: 0.00003,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-4",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 8192,
    max_input_tokens: 8192,
    max_output_tokens: 4096,
    input_cost_per_token: 0.00003,
    output_cost_per_token: 0.00006,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-4-0613",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 8192,
    max_input_tokens: 8192,
    max_output_tokens: 4096,
    input_cost_per_token: 0.00003,
    output_cost_per_token: 0.00006,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-3.5-turbo",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 16385,
    max_output_tokens: 4096,
    input_cost_per_token: 5e-7,
    output_cost_per_token: 0.0000015,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-3.5-turbo-0125",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 16385,
    max_output_tokens: 4096,
    input_cost_per_token: 5e-7,
    output_cost_per_token: 0.0000015,
    shutdown_date: "2026-10-23"
  },
  {
    model: "gpt-3.5-turbo-1106",
    litellm_provider: "openai",
    mode: "chat",
    max_tokens: 4096,
    max_input_tokens: 16385,
    max_output_tokens: 4096,
    input_cost_per_token: 0.000001,
    output_cost_per_token: 0.000002,
    shutdown_date: "2026-09-28"
  },
  {
    model: "gpt-3.5-turbo-instruct",
    litellm_provider: "openai",
    mode: "completion",
    max_tokens: 4096,
    max_input_tokens: 4096,
    max_output_tokens: 4096,
    input_cost_per_token: 0.0000015,
    output_cost_per_token: 0.000002,
    shutdown_date: "2026-09-28"
  },
  {
    model: "babbage-002",
    litellm_provider: "openai",
    mode: "completion",
    max_tokens: 16384,
    max_input_tokens: 16384,
    max_output_tokens: 4096,
    input_cost_per_token: 4e-7,
    output_cost_per_token: 4e-7,
    shutdown_date: "2026-09-28"
  },
  {
    model: "davinci-002",
    litellm_provider: "openai",
    mode: "completion",
    max_tokens: 16384,
    max_input_tokens: 16384,
    max_output_tokens: 4096,
    input_cost_per_token: 0.000002,
    output_cost_per_token: 0.000002,
    shutdown_date: "2026-09-28"
  },
  {
    model: "gpt-4o-transcribe",
    litellm_provider: "openai",
    mode: "audio_transcription",
    max_input_tokens: 16000,
    max_output_tokens: 2000,
    input_cost_per_token: 0.0000025,
    output_cost_per_token: 0.00001
  },
  {
    model: "gpt-4o-transcribe-diarize",
    litellm_provider: "openai",
    mode: "audio_transcription",
    max_input_tokens: 16000,
    max_output_tokens: 2000,
    input_cost_per_token: 0.0000025,
    output_cost_per_token: 0.00001
  },
  {
    model: "gpt-4o-mini-transcribe",
    litellm_provider: "openai",
    mode: "audio_transcription",
    max_input_tokens: 16000,
    max_output_tokens: 2000,
    input_cost_per_token: 0.00000125,
    output_cost_per_token: 0.000005
  },
  {
    model: "gpt-4o-mini-transcribe-2025-12-15",
    litellm_provider: "openai",
    mode: "audio_transcription",
    max_input_tokens: 16000,
    max_output_tokens: 2000,
    input_cost_per_token: 0.00000125,
    output_cost_per_token: 0.000005
  },
  {
    model: "gpt-4o-mini-tts",
    litellm_provider: "openai",
    mode: "audio_speech",
    input_cost_per_token: 0.0000025,
    output_cost_per_token: 0.00001
  },
  {
    model: "gpt-4o-mini-tts-2025-12-15",
    litellm_provider: "openai",
    mode: "audio_speech",
    input_cost_per_token: 0.0000025,
    output_cost_per_token: 0.00001
  }
] as const
