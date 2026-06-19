export interface ProviderModelPricingLike {
  providerCode: string
  model: string
  inputPriceUsdPerMillion?: number
  outputPriceUsdPerMillion?: number
  cacheReadPriceUsdPerMillion?: number
  cacheWritePriceUsdPerMillion?: number
}

export interface ProviderCostInput {
  providerCode: string
  pricingModel: string
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
  thinkingTokens?: number
  imageTokens?: number
  audioTokens?: number
}

export interface ProviderCostBreakdown {
  inputCostUsd: number
  outputCostUsd: number
  cacheReadCostUsd: number
  cacheWriteCostUsd: number
  totalCostUsd: number
}

export interface CostCalculator {
  providerCode: string
  calculate(input: ProviderCostInput, pricing: ProviderModelPricingLike): ProviderCostBreakdown | undefined
}

export interface ModelCatalogProvider {
  providerCode: string
  normalizeModelCandidates(model: string): string[]
  resolvePricingModel(model: string): string | undefined
}
