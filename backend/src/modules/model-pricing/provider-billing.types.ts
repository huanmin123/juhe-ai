export interface ProviderBillingPriceSet {
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

export interface ProviderBillingPricing extends ProviderBillingPriceSet {
  providerCode: string
  model: string
  mode?: string
  serviceTierPrices?: Record<string, ProviderBillingPriceSet>
  supportedServiceTiers?: readonly string[]
  supportedReasoningEfforts?: readonly string[]
  defaultReasoningEffort?: string | null
  contextWindowTokens?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  longContextInputTokenThreshold?: number
  longContextInputTokenThresholdInclusive?: boolean
  longContextInputCostMultiplier?: number
  longContextOutputCostMultiplier?: number
  supportsPromptCaching?: boolean
  sourcePricingCurrency?: string
  sourceExchangeRateToUsd?: number
  sourceExchangeRateDate?: string
  sourcePricingNote?: string
}

export interface ProviderBillingCostInput {
  providerCode: string
  model?: string
  serviceTier?: string
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  cacheWrite1hTokens?: number
  thinkingTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
  inputAudioTokens?: number
  outputAudioTokens?: number
  outputImageCount?: number
  costUsd?: number
}

export type ProviderCostLineKind =
  | 'input'
  | 'output'
  | 'cache_read'
  | 'cache_write'
  | 'cache_write_1h'
  | 'image_input'
  | 'image_output'
  | 'audio_input'
  | 'audio_output'
  | 'image_output_unit'
  | 'other'

export interface ProviderCostLineItem {
  key: string
  kind: ProviderCostLineKind
  label: string
  quantity: number
  unit: 'token' | 'image' | 'request' | 'second' | 'minute' | 'token_hour'
  unitSize: number
  unitPriceUsd: number
  costUsd: number
}

export interface ProviderCostBreakdown {
  currency?: 'USD'
  billingPolicy?: string
  lineItems?: ProviderCostLineItem[]
  inputCostUsd?: number
  outputCostUsd?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cacheReadCostUsd?: number
  cacheReadUsdPer1M?: number
  cacheWriteCostUsd?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hCostUsd?: number
  cacheWrite1hUsdPer1M?: number
  thinkingTokens?: number
  inputImageCostUsd?: number
  outputImageCostUsd?: number
  inputImageUsdPer1M?: number
  outputImageUsdPer1M?: number
  inputAudioCostUsd?: number
  outputAudioCostUsd?: number
  inputAudioUsdPer1M?: number
  outputAudioUsdPer1M?: number
  outputImageUnitCostUsd?: number
  outputUsdPerImage?: number
  accountChargeUsd?: number
  multiplier: 1
  serviceTierPricingSource: 'default' | 'tier_specific' | 'multiplier' | 'mixed' | 'unknown'
  serviceTierMultiplier?: number
}

export type ProviderCatalogDisplayFormat =
  | 'usd_per_1m_tokens'
  | 'usd_per_image'
  | 'usd_per_1m_token_hour'
  | 'tokens'
  | 'multiplier'
  | 'text'

export interface ProviderCatalogDisplayItem {
  key: string
  label: string
  format: ProviderCatalogDisplayFormat
  value: number | string
}

export interface ProviderCatalogDisplaySection {
  key: string
  label: string
  items: ProviderCatalogDisplayItem[]
}

export interface ProviderBillingPolicy {
  id: string
  buildCostBreakdown(pricing: ProviderBillingPricing, input: ProviderBillingCostInput): ProviderCostBreakdown | undefined
  buildCatalogDisplay(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection[]
}

export interface ResolvedProviderBillingRates extends ProviderBillingPriceSet {
  serviceTierPricingSource: ProviderCostBreakdown['serviceTierPricingSource']
  serviceTierMultiplier?: number
}
