import type { AccountType, ProviderCode } from './base'

export type ProviderModelScope = 'built_in' | 'global' | 'personal'
export type CustomProviderModelScope = Exclude<ProviderModelScope, 'built_in'>
export type ProviderModelStatus = 'draft' | 'active' | 'disabled'
export type ProviderModelMode = 'text' | 'image' | 'audio'
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

export interface ProviderDefinition {
  id: string
  code: ProviderCode
  name: string
  parentCode?: ProviderCode
  description?: string
  enabled: boolean
  defaultProtocolProfileId: string
  protocolCode: string
  protocolVersion: string
  baseUrl: string
  defaultTestModel: string
  systemDefaultTestModel?: string
  defaultSupportedModels: string[]
  accountTypes: AccountType[]
  capabilities: string[]
  protocolProfiles: ProviderProtocolProfileDefinition[]
}

export interface ProtocolEndpointFamilyDefinition {
  code: string
  name: string
  description?: string
}

export interface ProviderProtocolProfileDefinition {
  id: string
  providerCode: ProviderCode
  name: string
  description?: string
  enabled: boolean
  protocolCode: string
  protocolVersion: string
  baseUrl: string
  defaultTestModel: string
  accountTypes: AccountType[]
  capabilities: string[]
  endpointFamilies: ProtocolEndpointFamilyDefinition[]
}

export interface ProviderModelPricing {
  providerCode: ProviderCode
  model: string
  id?: string
  scope?: ProviderModelScope
  status?: ProviderModelStatus
  systemAccountId?: string
  pricingModel?: string
  mode?: string
  catalogOrder?: number
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  supportedApiProtocols?: ProviderModelApiProtocol[]
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  pricingNotes?: string
  capabilityNotes?: string
  notes?: string
  createdAt?: string
  updatedAt?: string
  source: string
}

export interface ProviderModelOption {
  providerCode: ProviderCode
  model: string
  supportedApiProtocols?: ProviderModelApiProtocol[]
}

export interface ProviderDefaultTestModelResult {
  providerCode: ProviderCode
  defaultTestModel: string
}

export interface ProviderModelsParams {
  systemAccountId?: string
  includeInactive?: boolean
  includeUnpriced?: boolean
}

export interface ProviderModelUpsertPayload {
  scope?: CustomProviderModelScope
  model: string
  status?: ProviderModelStatus
  mode?: ProviderModelMode | null
  supportedApiProtocols?: ProviderModelApiProtocol[]
  pricingModel?: null
  releaseDate?: string | null
  shutdownDate?: string | null
  contextWindowTokens?: number | null
  maxOutputTokens?: number | null
  inputUsdPer1M?: number | null
  outputUsdPer1M?: number | null
  cachedInputUsdPer1M?: number | null
  cacheWriteUsdPer1M?: number | null
  imageInputUsdPer1M?: number | null
  imageOutputUsdPer1M?: number | null
  audioInputUsdPer1M?: number | null
  audioOutputUsdPer1M?: number | null
  outputUsdPerImage?: number | null
}
