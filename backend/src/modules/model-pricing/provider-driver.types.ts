export type ProviderModelApiProtocol =
  | 'chat_completions'
  | 'responses'
  | 'messages'
  | 'message_token_counting'
  | 'completions'
  | 'images'
  | 'audio'
  | 'realtime'

export interface RawModelPricing {
  model: string
  mode?: string
  release_date?: string
  input_cost_per_token?: number
  output_cost_per_token?: number
  cache_creation_input_token_cost?: number
  cache_creation_input_token_cost_above_1hr?: number
  cache_read_input_token_cost?: number
  input_cost_per_image_token?: number
  output_cost_per_image?: number
  output_cost_per_image_token?: number
  input_cost_per_audio_token?: number
  output_cost_per_audio_token?: number
  max_input_tokens?: number
  max_output_tokens?: number
  max_tokens?: number
  shutdown_date?: string
  supported_api_protocols?: ProviderModelApiProtocol[]
  supports_prompt_caching?: boolean
  supports_service_tier?: boolean
  catalog_visible?: boolean
}

export interface ModelPricingProviderDriverHelpers {
  normalizeModel(value: string): string
  extractModelReleaseDate(model: string): string | undefined
}

export interface ModelPricingProviderDriver {
  id: string
  pricingSource: string
  rawModels: readonly RawModelPricing[]
  usesIncludedCacheReadUsage: boolean
  supportsProvider(providerCode: string): boolean
  isUnavailableModel?(normalizedModel: string): boolean
  buildModelCandidates(normalizedModel: string): string[]
  getModelReleaseDate(item: RawModelPricing, helpers: ModelPricingProviderDriverHelpers): string | undefined
  inferModelApiProtocols(item: RawModelPricing, helpers: ModelPricingProviderDriverHelpers): ProviderModelApiProtocol[]
}
