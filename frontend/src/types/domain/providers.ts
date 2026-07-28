import type { AccountType, ProviderCode } from './base'

export type ProviderModelScope = 'built_in' | 'global' | 'personal'
export type CustomProviderModelScope = Exclude<ProviderModelScope, 'built_in'>
export type ProviderModelStatus = 'draft' | 'active' | 'disabled'
export type ProviderModelMode = 'text' | 'image'
export type ProviderModelServiceTier = string
export type ProviderModelReasoningEffort = string
export type ProviderModelCodexReasoningLevel = ProviderModelReasoningEffort | 'ultra'
export interface ProviderModelPriceSet {
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  cacheStorageUsdPer1MPerHour?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
}

export type ProviderModelCatalogDisplayFormat =
  | 'usd_per_1m_tokens'
  | 'usd_per_image'
  | 'usd_per_1m_token_hour'
  | 'tokens'
  | 'multiplier'
  | 'text'

export interface ProviderModelCatalogDisplayItem {
  key: string
  label: string
  format: ProviderModelCatalogDisplayFormat
  value: number | string
}

export interface ProviderModelCatalogDisplaySection {
  key: string
  label: string
  items: ProviderModelCatalogDisplayItem[]
}

export type ProviderModelApiProtocol =
  | 'chat_completions'
  | 'responses'
  | 'messages'
  | 'message_token_counting'
  | 'generate_content'
  | 'stream_generate_content'
  | 'count_tokens'
  | 'embed_content'
  | 'interactions'
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
  defaultHealthCheckModel: string
  systemDefaultHealthCheckModel?: string
  defaultSupportedModels: string[]
  accountTypes: AccountType[]
  capabilities: string[]
  protocolProfiles: ProviderProtocolProfileDefinition[]
}

export interface ProviderListItem {
  id: string
  code: ProviderCode
  name: string
  parentCode?: ProviderCode
  description?: string
  enabled: boolean
  protocolCode: string
  baseUrl: string
  defaultHealthCheckModel: string
  defaultSupportedModels: string[]
  accountTypes: AccountType[]
  capabilities: string[]
}

export interface ProviderOption {
  id: string
  code: ProviderCode
  name: string
  enabled: boolean
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
  defaultHealthCheckModel: string
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
  catalogVisible?: boolean
  systemAccountId?: string
  mode?: string
  catalogOrder?: number
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  supportedApiProtocols?: ProviderModelApiProtocol[]
  inputModalities?: Array<'text' | 'image' | 'audio' | 'video' | 'file'>
  outputModalities?: Array<'text' | 'image' | 'audio' | 'video' | 'file'>
  supportedTools?: string[]
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  cacheStorageUsdPer1MPerHour?: number
  serviceTierPrices?: Record<string, ProviderModelPriceSet>
  imageInputUsdPer1M?: number
  cachedImageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  longContextInputTokenThreshold?: number
  longContextInputTokenThresholdInclusive?: boolean
  longContextInputCostMultiplier?: number
  longContextOutputCostMultiplier?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  supportedServiceTiers?: ProviderModelServiceTier[]
  supportedReasoningEfforts?: ProviderModelReasoningEffort[]
  defaultReasoningEffort: ProviderModelReasoningEffort | null
  codexSupportedReasoningLevels?: ProviderModelCodexReasoningLevel[]
  codexDefaultReasoningLevel?: ProviderModelCodexReasoningLevel
  codexMultiAgentVersion?: 'v2'
  pricingNotes?: string
  capabilityNotes?: string
  notes?: string
  catalogDisplay?: ProviderModelCatalogDisplaySection[]
  createdAt?: string
  updatedAt?: string
  source: string
}

export interface ProviderModelOption {
  id: string
  name: string
  providerCode?: ProviderCode
  supportedApiProtocols: ProviderModelApiProtocol[]
  supportedServiceTiers: ProviderModelServiceTier[]
  supportedReasoningEfforts: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort
}

export interface ProviderModelCapabilities {
  id: string
  name: string
  supportedApiProtocols: ProviderModelApiProtocol[]
  supportedServiceTiers: ProviderModelServiceTier[]
  supportedReasoningEfforts: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort
}

export interface ProviderDefaultHealthCheckModelResult {
  providerCode: ProviderCode
  defaultHealthCheckModel: string
}

export interface ProviderModelsParams {
  systemAccountId?: string
  viewScope?: 'admin' | 'self'
  includeInactive?: boolean
  includeUnpriced?: boolean
}

export interface ProviderModelUpsertPayload {
  configurationTemplateId?: string
  scope?: CustomProviderModelScope
  model: string
  status?: ProviderModelStatus
  catalogVisible?: boolean
  mode?: ProviderModelMode | null
  supportedApiProtocols?: ProviderModelApiProtocol[]
  supportedServiceTiers?: ProviderModelServiceTier[]
  supportedReasoningEfforts?: ProviderModelReasoningEffort[]
  defaultReasoningEffort?: ProviderModelReasoningEffort | null
  releaseDate?: string | null
  shutdownDate?: string | null
  contextWindowTokens?: number | null
  maxInputTokens?: number | null
  maxOutputTokens?: number | null
  inputUsdPer1M?: number | null
  outputUsdPer1M?: number | null
  cachedInputUsdPer1M?: number | null
  cacheWriteUsdPer1M?: number | null
  cacheWrite1hUsdPer1M?: number | null
  cacheStorageUsdPer1MPerHour?: number | null
  serviceTierPrices?: Record<string, ProviderModelPriceSet> | null
  imageInputUsdPer1M?: number | null
  imageOutputUsdPer1M?: number | null
  audioInputUsdPer1M?: number | null
  audioOutputUsdPer1M?: number | null
  outputUsdPerImage?: number | null
}

export type ProviderModelPatchPayload = Partial<Omit<
  ProviderModelUpsertPayload,
  'configurationTemplateId' | 'scope' | 'model'
>> & {
  expectedUpdatedAt: string
}

export interface ProviderModelMutationResult {
  id: string
  providerCode: ProviderCode
  model: string
  status: ProviderModelStatus
  updatedAt: string
  defaultHealthCheckModelCleared?: boolean
}
