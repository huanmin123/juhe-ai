import type { AccountType, ProviderCode } from './base'

export interface ProviderDefinition {
  id: string
  code: ProviderCode
  name: string
  description?: string
  enabled: boolean
  baseUrl: string
  defaultTestModel: string
  accountTypes: AccountType[]
  capabilities: string[]
}

export interface ProviderModelPricing {
  providerCode: ProviderCode
  model: string
  mode?: string
  releaseDate?: string
  shutdownDate?: string
  supportedApiProtocols?: Array<'chat_completions' | 'responses' | 'completions' | 'images' | 'audio' | 'realtime'>
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  source: string
}

export interface ProviderModelOption {
  providerCode: ProviderCode
  model: string
}
