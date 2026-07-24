import type { RawModelPricing } from './provider-driver.types.js'

// Curated from the official Gemini API model, pricing, release, and deprecation docs on 2026-07-23.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.
const textGenerationProtocols = ['chat_completions', 'generate_content', 'stream_generate_content', 'count_tokens'] as const
const embeddingProtocols = ['embed_content'] as const

interface GeminiTierPrices {
  inputUsdPer1M: number
  outputUsdPer1M: number
  cachedInputUsdPer1M?: number
  cacheStorageUsdPer1MPerHour?: number
  audioInputUsdPer1M?: number
}

export const geminiModelPricingData: RawModelPricing[] = [
  textModel({
    model: 'gemini-3.6-flash',
    catalogOrder: 1,
    releaseDate: '2026-07-21',
    inputUsdPer1M: 1.5,
    outputUsdPer1M: 7.5,
    cachedInputUsdPer1M: 0.15,
    cacheStorageUsdPer1MPerHour: 1,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.75, outputUsdPer1M: 3.75, cachedInputUsdPer1M: 0.075, cacheStorageUsdPer1MPerHour: 1 },
      priority: { inputUsdPer1M: 2.7, outputUsdPer1M: 13.5, cachedInputUsdPer1M: 0.27, cacheStorageUsdPer1MPerHour: 1.8 }
    },
    supported_api_protocols: ['chat_completions', 'generate_content', 'stream_generate_content', 'count_tokens', 'interactions'],
    input_modalities: ['text', 'image', 'video', 'audio', 'file'],
    output_modalities: ['text'],
    supported_tools: ['code_execution', 'file_search', 'function_calling', 'google_maps_grounding', 'google_search_grounding', 'structured_outputs', 'url_context', 'computer_use'],
    supported_reasoning_efforts: ['minimal', 'low', 'medium', 'high'],
    default_reasoning_effort: 'medium'
  }),
  textModel({
    model: 'gemini-3.5-flash-lite',
    catalogOrder: 5,
    releaseDate: '2026-07-21',
    inputUsdPer1M: 0.3,
    outputUsdPer1M: 2.5,
    cachedInputUsdPer1M: 0.03,
    cacheStorageUsdPer1MPerHour: 1,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.15, outputUsdPer1M: 1.25, cachedInputUsdPer1M: 0.02, cacheStorageUsdPer1MPerHour: 1 },
      priority: { inputUsdPer1M: 0.54, outputUsdPer1M: 4.5, cachedInputUsdPer1M: 0.05, cacheStorageUsdPer1MPerHour: 1.8 }
    },
    supported_api_protocols: ['chat_completions', 'generate_content', 'stream_generate_content', 'count_tokens', 'interactions'],
    input_modalities: ['text', 'image', 'video', 'audio', 'file'],
    output_modalities: ['text'],
    supported_tools: ['code_execution', 'file_search', 'function_calling', 'google_maps_grounding', 'google_search_grounding', 'structured_outputs', 'url_context'],
    supported_reasoning_efforts: ['minimal', 'low', 'medium', 'high'],
    default_reasoning_effort: 'minimal'
  }),
  textModel({
    model: 'gemini-3.5-flash',
    catalogOrder: 10,
    releaseDate: '2026-05-19',
    inputUsdPer1M: 1.5,
    outputUsdPer1M: 9,
    cachedInputUsdPer1M: 0.15,
    cacheStorageUsdPer1MPerHour: 1,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.75, outputUsdPer1M: 4.5, cachedInputUsdPer1M: 0.08, cacheStorageUsdPer1MPerHour: 1 },
      priority: { inputUsdPer1M: 2.7, outputUsdPer1M: 16.2, cachedInputUsdPer1M: 0.27, cacheStorageUsdPer1MPerHour: 1.8 }
    }
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens","interactions"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context","computer_use"],
    supported_reasoning_efforts: ["minimal","low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-3.1-pro-preview',
    catalogOrder: 20,
    releaseDate: '2026-02-19',
    inputUsdPer1M: 2,
    outputUsdPer1M: 12,
    cachedInputUsdPer1M: 0.2,
    cacheStorageUsdPer1MPerHour: 4.5,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 1, outputUsdPer1M: 6, cachedInputUsdPer1M: 0.2, cacheStorageUsdPer1MPerHour: 4.5 },
      priority: { inputUsdPer1M: 3.6, outputUsdPer1M: 21.6, cachedInputUsdPer1M: 0.36, cacheStorageUsdPer1MPerHour: 8.1 }
    },
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens","interactions"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
    default_reasoning_effort: "high"
  }),
  textModel({
    model: 'gemini-3.1-pro-preview-customtools',
    catalogOrder: 30,
    releaseDate: '2026-02-19',
    inputUsdPer1M: 2,
    outputUsdPer1M: 12,
    cachedInputUsdPer1M: 0.2,
    cacheStorageUsdPer1MPerHour: 4.5,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 1, outputUsdPer1M: 6, cachedInputUsdPer1M: 0.2, cacheStorageUsdPer1MPerHour: 4.5 },
      priority: { inputUsdPer1M: 3.6, outputUsdPer1M: 21.6, cachedInputUsdPer1M: 0.36, cacheStorageUsdPer1MPerHour: 8.1 }
    },
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
    default_reasoning_effort: "high"
  }),
  textModel({
    model: 'gemini-3-flash-preview',
    catalogOrder: 40,
    releaseDate: '2025-12-17',
    inputUsdPer1M: 0.5,
    outputUsdPer1M: 3,
    cachedInputUsdPer1M: 0.05,
    cacheStorageUsdPer1MPerHour: 1,
    audioInputUsdPer1M: 1,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.25, outputUsdPer1M: 1.5, cachedInputUsdPer1M: 0.05, audioInputUsdPer1M: 0.5, cacheStorageUsdPer1MPerHour: 1 },
      priority: { inputUsdPer1M: 0.9, outputUsdPer1M: 5.4, cachedInputUsdPer1M: 0.09, audioInputUsdPer1M: 1.8, cacheStorageUsdPer1MPerHour: 1.8 }
    }
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens","interactions"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context","computer_use"],
    supported_reasoning_efforts: ["minimal","low","medium","high"],
    default_reasoning_effort: "high"
  }),
  textModel({
    model: 'gemini-3.1-flash-lite',
    catalogOrder: 50,
    releaseDate: '2026-05-07',
    shutdownDate: '2027-05-07',
    inputUsdPer1M: 0.25,
    outputUsdPer1M: 1.5,
    cachedInputUsdPer1M: 0.025,
    cacheStorageUsdPer1MPerHour: 1,
    audioInputUsdPer1M: 0.5,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.125, outputUsdPer1M: 0.75, cachedInputUsdPer1M: 0.0125, audioInputUsdPer1M: 0.25, cacheStorageUsdPer1MPerHour: 0.5 },
      priority: { inputUsdPer1M: 0.45, outputUsdPer1M: 2.7, cachedInputUsdPer1M: 0.045, audioInputUsdPer1M: 0.9, cacheStorageUsdPer1MPerHour: 1.8 }
    }
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens","interactions"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["minimal","low","medium","high"],
  }),
  textModel({
    model: 'gemini-2.5-pro',
    catalogOrder: 60,
    releaseDate: '2025-06-17',
    shutdownDate: '2026-10-16',
    inputUsdPer1M: 1.25,
    outputUsdPer1M: 10,
    cachedInputUsdPer1M: 0.125,
    cacheStorageUsdPer1MPerHour: 4.5,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.625, outputUsdPer1M: 5, cachedInputUsdPer1M: 0.125, cacheStorageUsdPer1MPerHour: 4.5 },
      priority: { inputUsdPer1M: 2.25, outputUsdPer1M: 18, cachedInputUsdPer1M: 0.225, cacheStorageUsdPer1MPerHour: 8.1 }
    },
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens","interactions"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
  }),
  textModel({
    model: 'gemini-2.5-flash',
    catalogOrder: 70,
    releaseDate: '2025-06-17',
    shutdownDate: '2026-10-16',
    inputUsdPer1M: 0.3,
    outputUsdPer1M: 2.5,
    cachedInputUsdPer1M: 0.03,
    cacheStorageUsdPer1MPerHour: 1,
    audioInputUsdPer1M: 1,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.15, outputUsdPer1M: 1.25, cachedInputUsdPer1M: 0.03, audioInputUsdPer1M: 0.5, cacheStorageUsdPer1MPerHour: 1 },
      priority: { inputUsdPer1M: 0.54, outputUsdPer1M: 4.5, cachedInputUsdPer1M: 0.054, audioInputUsdPer1M: 1.8, cacheStorageUsdPer1MPerHour: 1.8 }
    }
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens","interactions"],
    input_modalities: ["text","image","video","audio"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
  }),
  textModel({
    model: 'gemini-2.5-flash-lite',
    catalogOrder: 80,
    releaseDate: '2025-07-22',
    shutdownDate: '2026-10-16',
    inputUsdPer1M: 0.1,
    outputUsdPer1M: 0.4,
    cachedInputUsdPer1M: 0.01,
    cacheStorageUsdPer1MPerHour: 1,
    audioInputUsdPer1M: 0.3,
    serviceTierPrices: {
      flex: { inputUsdPer1M: 0.05, outputUsdPer1M: 0.2, cachedInputUsdPer1M: 0.01, audioInputUsdPer1M: 0.15, cacheStorageUsdPer1MPerHour: 1 },
      priority: { inputUsdPer1M: 0.18, outputUsdPer1M: 0.72, cachedInputUsdPer1M: 0.018, audioInputUsdPer1M: 0.54, cacheStorageUsdPer1MPerHour: 1.8 }
    }
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens","interactions"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
  }),
  embeddingModel({
    model: 'gemini-embedding-2',
    catalogOrder: 100,
    releaseDate: '2026-04-22',
    inputUsdPer1M: 0.2,
    imageInputUsdPer1M: 0.45,
    audioInputUsdPer1M: 6.5,
    maxInputTokens: 8_192,
    supported_api_protocols: ['embed_content'],
    input_modalities: ['text', 'image', 'video', 'audio', 'file'],
    output_modalities: ['text']
  }),
  embeddingModel({
    model: 'gemini-embedding-001',
    catalogOrder: 110,
    releaseDate: '2025-07-14',
    inputUsdPer1M: 0.15,
    shutdownDate: '2026-07-14',
    maxInputTokens: 2_048,
    supported_api_protocols: ['embed_content'],
    input_modalities: ['text'],
    output_modalities: ['text']
  })
]

function textModel(input: {
  model: string
  catalogOrder: number
  releaseDate?: string
  shutdownDate?: string
  inputUsdPer1M: number
  outputUsdPer1M: number
  cachedInputUsdPer1M?: number
  cacheStorageUsdPer1MPerHour: number
  audioInputUsdPer1M?: number
  serviceTierPrices?: { flex: GeminiTierPrices; priority: GeminiTierPrices }
  longContextInputTokenThreshold?: number
  longContextInputCostMultiplier?: number
  longContextOutputCostMultiplier?: number
  supported_api_protocols?: RawModelPricing['supported_api_protocols']
  input_modalities?: RawModelPricing['input_modalities']
  output_modalities?: RawModelPricing['output_modalities']
  supported_tools?: RawModelPricing['supported_tools']
  supported_reasoning_efforts?: RawModelPricing['supported_reasoning_efforts']
  default_reasoning_effort?: RawModelPricing['default_reasoning_effort']
}): RawModelPricing {
  return {
    model: input.model,
    mode: 'chat',
    catalog_order: input.catalogOrder,
    release_date: input.releaseDate,
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
    output_cost_per_token: usdPerToken(input.outputUsdPer1M),
    cache_read_input_token_cost: input.cachedInputUsdPer1M === undefined ? undefined : usdPerToken(input.cachedInputUsdPer1M),
    cache_storage_input_token_cost_per_hour: usdPerToken(input.cacheStorageUsdPer1MPerHour),
    input_cost_per_audio_token: input.audioInputUsdPer1M === undefined ? undefined : usdPerToken(input.audioInputUsdPer1M),
    input_cost_per_token_flex: usdPerToken(input.serviceTierPrices?.flex.inputUsdPer1M),
    output_cost_per_token_flex: usdPerToken(input.serviceTierPrices?.flex.outputUsdPer1M),
    cache_read_input_token_cost_flex: usdPerToken(input.serviceTierPrices?.flex.cachedInputUsdPer1M),
    cache_storage_input_token_cost_per_hour_flex: usdPerToken(input.serviceTierPrices?.flex.cacheStorageUsdPer1MPerHour),
    input_cost_per_audio_token_flex: usdPerToken(input.serviceTierPrices?.flex.audioInputUsdPer1M),
    input_cost_per_token_priority: usdPerToken(input.serviceTierPrices?.priority.inputUsdPer1M),
    output_cost_per_token_priority: usdPerToken(input.serviceTierPrices?.priority.outputUsdPer1M),
    cache_read_input_token_cost_priority: usdPerToken(input.serviceTierPrices?.priority.cachedInputUsdPer1M),
    cache_storage_input_token_cost_per_hour_priority: usdPerToken(input.serviceTierPrices?.priority.cacheStorageUsdPer1MPerHour),
    input_cost_per_audio_token_priority: usdPerToken(input.serviceTierPrices?.priority.audioInputUsdPer1M),
    long_context_input_token_threshold: input.longContextInputTokenThreshold,
    long_context_input_cost_multiplier: input.longContextInputCostMultiplier,
    long_context_output_cost_multiplier: input.longContextOutputCostMultiplier,
    max_input_tokens: 1_048_576,
    max_output_tokens: 65_536,
    shutdown_date: input.shutdownDate,
    supported_api_protocols: input.supported_api_protocols ?? textGenerationProtocols,
    input_modalities: input.input_modalities,
    output_modalities: input.output_modalities,
    supported_tools: input.supported_tools,
    supported_service_tiers: input.serviceTierPrices ? ['priority', 'flex'] : undefined,
    supported_reasoning_efforts: input.supported_reasoning_efforts,
    default_reasoning_effort: input.default_reasoning_effort,
    supports_prompt_caching: true,
    catalog_visible: true
  }
}

function embeddingModel(input: {
  model: string
  catalogOrder: number
  releaseDate?: string
  inputUsdPer1M: number
  imageInputUsdPer1M?: number
  audioInputUsdPer1M?: number
  maxInputTokens: number
  shutdownDate?: string
  supported_api_protocols?: RawModelPricing['supported_api_protocols']
  input_modalities?: RawModelPricing['input_modalities']
  output_modalities?: RawModelPricing['output_modalities']
  supported_tools?: RawModelPricing['supported_tools']
  supported_reasoning_efforts?: RawModelPricing['supported_reasoning_efforts']
  default_reasoning_effort?: RawModelPricing['default_reasoning_effort']
}): RawModelPricing {
  return {
    model: input.model,
    mode: 'embedding',
    catalog_order: input.catalogOrder,
    release_date: input.releaseDate,
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
    input_cost_per_image_token: usdPerToken(input.imageInputUsdPer1M),
    input_cost_per_audio_token: usdPerToken(input.audioInputUsdPer1M),
    max_input_tokens: input.maxInputTokens,
    shutdown_date: input.shutdownDate,
    supported_api_protocols: input.supported_api_protocols ?? embeddingProtocols,
    input_modalities: input.input_modalities,
    output_modalities: input.output_modalities,
    supported_tools: input.supported_tools,
    supported_reasoning_efforts: input.supported_reasoning_efforts,
    default_reasoning_effort: input.default_reasoning_effort,
    supports_prompt_caching: false,
    catalog_visible: true
  }
}

function usdPerToken(usdPer1M?: number): number | undefined {
  return usdPer1M === undefined ? undefined : usdPer1M / 1_000_000
}
