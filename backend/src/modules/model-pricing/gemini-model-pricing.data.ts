import type { RawModelPricing } from './provider-driver.types.js'

// Curated from the official Gemini API model and pricing docs on 2026-06-25.
// Values follow LiteLLM/model-price-repo field names: token prices are USD per token.
const textGenerationProtocols = ['chat_completions', 'generate_content', 'stream_generate_content', 'count_tokens'] as const
const embeddingProtocols = ['embed_content'] as const

export const geminiModelPricingData: RawModelPricing[] = [
  textModel({
    model: 'gemini-3.5-flash',
    catalogOrder: 10,
    inputUsdPer1M: 1.5,
    outputUsdPer1M: 9,
    cachedInputUsdPer1M: 0.15
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context","computer_use"],
    supported_reasoning_efforts: ["minimal","low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-3.1-pro-preview',
    catalogOrder: 20,
    inputUsdPer1M: 2,
    outputUsdPer1M: 12,
    cachedInputUsdPer1M: 0.2,
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-3.1-pro-preview-customtools',
    catalogOrder: 30,
    inputUsdPer1M: 2,
    outputUsdPer1M: 12,
    cachedInputUsdPer1M: 0.2,
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-3-flash-preview',
    catalogOrder: 40,
    inputUsdPer1M: 0.5,
    outputUsdPer1M: 3,
    cachedInputUsdPer1M: 0.05,
    audioInputUsdPer1M: 1
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context","computer_use"],
    supported_reasoning_efforts: ["minimal","low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-3.1-flash-lite',
    catalogOrder: 50,
    inputUsdPer1M: 0.25,
    outputUsdPer1M: 1.5,
    cachedInputUsdPer1M: 0.025,
    audioInputUsdPer1M: 0.5
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["minimal","low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-2.5-pro',
    catalogOrder: 60,
    inputUsdPer1M: 1.25,
    outputUsdPer1M: 10,
    cachedInputUsdPer1M: 0.125,
    longContextInputTokenThreshold: 200_000,
    longContextInputCostMultiplier: 2,
    longContextOutputCostMultiplier: 1.5
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-2.5-flash',
    catalogOrder: 70,
    inputUsdPer1M: 0.3,
    outputUsdPer1M: 2.5,
    cachedInputUsdPer1M: 0.03,
    audioInputUsdPer1M: 1
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  textModel({
    model: 'gemini-2.5-flash-lite',
    catalogOrder: 80,
    inputUsdPer1M: 0.1,
    outputUsdPer1M: 0.4,
    cachedInputUsdPer1M: 0.01,
    audioInputUsdPer1M: 0.3
  ,
    supported_api_protocols: ["chat_completions","generate_content","stream_generate_content","count_tokens"],
    input_modalities: ["text","image","video","audio","file"],
    output_modalities: ["text"],
    supported_tools: ["code_execution","file_search","function_calling","google_maps_grounding","google_search_grounding","structured_outputs","url_context"],
    supported_reasoning_efforts: ["low","medium","high"],
    default_reasoning_effort: "medium"
  }),
  embeddingModel({
    model: 'gemini-embedding-2',
    catalogOrder: 100,
    inputUsdPer1M: 0.2
  ,
    supported_api_protocols: ["embed_content"],
    input_modalities: ["text","image","video","audio","file"]
  }),
  embeddingModel({
    model: 'gemini-embedding-001',
    catalogOrder: 110,
    inputUsdPer1M: 0.15,
    shutdownDate: '2026-07-14'
  ,
    supported_api_protocols: ["embed_content"],
    input_modalities: ["text"]
  })
]

function textModel(input: {
  model: string
  catalogOrder: number
  inputUsdPer1M: number
  outputUsdPer1M: number
  cachedInputUsdPer1M?: number
  audioInputUsdPer1M?: number
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
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
    output_cost_per_token: usdPerToken(input.outputUsdPer1M),
    cache_read_input_token_cost: input.cachedInputUsdPer1M === undefined ? undefined : usdPerToken(input.cachedInputUsdPer1M),
    input_cost_per_audio_token: input.audioInputUsdPer1M === undefined ? undefined : usdPerToken(input.audioInputUsdPer1M),
    long_context_input_token_threshold: input.longContextInputTokenThreshold,
    long_context_input_cost_multiplier: input.longContextInputCostMultiplier,
    long_context_output_cost_multiplier: input.longContextOutputCostMultiplier,
    max_input_tokens: 1_048_576,
    max_output_tokens: 65_536,
    supported_api_protocols: input.supported_api_protocols ?? textGenerationProtocols,
    input_modalities: input.input_modalities,
    output_modalities: input.output_modalities,
    supported_tools: input.supported_tools,
    supported_reasoning_efforts: input.supported_reasoning_efforts,
    default_reasoning_effort: input.default_reasoning_effort,
    supports_prompt_caching: true,
    catalog_visible: true
  }
}

function embeddingModel(input: {
  model: string
  catalogOrder: number
  inputUsdPer1M: number
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
    input_cost_per_token: usdPerToken(input.inputUsdPer1M),
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

function usdPerToken(usdPer1M: number): number {
  return usdPer1M / 1_000_000
}
