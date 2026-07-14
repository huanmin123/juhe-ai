export type ProviderModelApiProtocol =
  | 'chat_completions'
  | 'responses'
  | 'messages'
  | 'message_token_counting'
  | 'generate_content'
  | 'stream_generate_content'
  | 'count_tokens'
  | 'embed_content'
  | 'completions'
  | 'images'
  | 'audio'
  | 'realtime'

export type GptServiceTier = 'priority' | 'flex'

export type GptWireReasoningEffort =
  | 'none'
  | 'minimal'
  | 'low'
  | 'medium'
  | 'high'
  | 'xhigh'
  | 'max'

export type CodexReasoningLevel = GptWireReasoningEffort | 'ultra'

export interface RawModelPricing {
  model: string
  mode?: string
  catalog_order?: number
  release_date?: string
  input_cost_per_token?: number
  input_cost_per_token_priority?: number
  input_cost_per_token_flex?: number
  output_cost_per_token?: number
  output_cost_per_token_priority?: number
  output_cost_per_token_flex?: number
  cache_creation_input_token_cost?: number
  cache_creation_input_token_cost_priority?: number
  cache_creation_input_token_cost_flex?: number
  cache_creation_input_token_cost_above_1hr?: number
  cache_creation_input_token_cost_above_1hr_priority?: number
  cache_creation_input_token_cost_above_1hr_flex?: number
  cache_read_input_token_cost?: number
  cache_read_input_token_cost_priority?: number
  cache_read_input_token_cost_flex?: number
  input_cost_per_image_token?: number
  output_cost_per_image?: number
  output_cost_per_image_token?: number
  input_cost_per_audio_token?: number
  output_cost_per_audio_token?: number
  context_window_tokens?: number
  max_input_tokens?: number
  max_output_tokens?: number
  max_tokens?: number
  long_context_input_token_threshold?: number
  long_context_input_cost_multiplier?: number
  long_context_output_cost_multiplier?: number
  shutdown_date?: string
  supported_api_protocols?: readonly ProviderModelApiProtocol[]
  supports_prompt_caching?: boolean
  supported_service_tiers?: readonly GptServiceTier[]
  supported_reasoning_efforts?: readonly GptWireReasoningEffort[]
  default_reasoning_effort?: GptWireReasoningEffort
  codex_supported_reasoning_levels?: readonly CodexReasoningLevel[]
  codex_default_reasoning_level?: CodexReasoningLevel
  codex_multi_agent_version?: 'v2'
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
