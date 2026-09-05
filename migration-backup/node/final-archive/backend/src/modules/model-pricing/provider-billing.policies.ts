import {
  applyLongContextRates,
  buildTokenCostBreakdown,
  capacitySection,
  compactSections,
  defaultTokenBillingLabels,
  hasUnsupportedServiceTier,
  longContextSection,
  priceSection,
  reasoningSection,
  serviceTierRates,
  serviceTierSections,
  sourceConversionSection,
  standardRates
} from './provider-billing.shared.js'
import type {
  ProviderBillingPolicy,
  ProviderBillingPricing,
  ProviderCatalogDisplaySection
} from './provider-billing.types.js'

export const openAIProviderBillingPolicy: ProviderBillingPolicy = {
  id: 'openai',
  buildCostBreakdown(pricing, input) {
    const rates = applyLongContextRates(pricing, input, serviceTierRates(pricing, input))
    const billableInput = pricing.mode === 'image_generation'
      && rates.imageOutputUsdPer1M !== undefined
      && input.outputImageTokens === undefined
      ? { ...input, outputImageTokens: input.outputTokens }
      : input
    return buildTokenCostBreakdown(pricing, billableInput, rates, {
      cacheReadIncludedInInput: true,
      cacheReadFallbackToInput: true,
      labels: defaultTokenBillingLabels
    })
  },
  buildCatalogDisplay(pricing) {
    return compactSections([
      priceSection('token_pricing', 'Token 计费', [
        ...openAITokenEntries(pricing)
      ]),
      imageUnitSection(pricing),
      ...serviceTierSections(pricing),
      longContextSection(pricing),
      reasoningSection(pricing),
      capacitySection(pricing),
      sourceConversionSection(pricing)
    ])
  }
}

function openAITokenEntries(
  pricing: ProviderBillingPricing
): Array<[string, string, number | undefined, 'usd_per_1m_tokens']> {
  const hasModalityPrices = [
    pricing.imageInputUsdPer1M,
    pricing.cachedImageInputUsdPer1M,
    pricing.imageOutputUsdPer1M,
    pricing.audioInputUsdPer1M,
    pricing.audioOutputUsdPer1M
  ].some((value) => value !== undefined)
  const prefix = hasModalityPrices ? '文本' : ''
  return [
    ['input', `${prefix}输入`, pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
    ['cache_read', `${prefix}缓存读`, pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
    ['cache_write', `${prefix}缓存写入`, pricing.cacheWriteUsdPer1M, 'usd_per_1m_tokens'],
    ['output', `${prefix}输出`, pricing.outputUsdPer1M, 'usd_per_1m_tokens'],
    ['image_input', '图片输入', pricing.imageInputUsdPer1M, 'usd_per_1m_tokens'],
    ['image_cache_read', '图片缓存读', pricing.cachedImageInputUsdPer1M, 'usd_per_1m_tokens'],
    ['image_output', '图片输出', pricing.imageOutputUsdPer1M, 'usd_per_1m_tokens'],
    ['audio_input', '音频输入', pricing.audioInputUsdPer1M, 'usd_per_1m_tokens'],
    ['audio_output', '音频输出', pricing.audioOutputUsdPer1M, 'usd_per_1m_tokens']
  ]
}

export const anthropicProviderBillingPolicy: ProviderBillingPolicy = {
  id: 'anthropic',
  buildCostBreakdown(pricing, input) {
    if (hasUnsupportedServiceTier(pricing, input)) return undefined
    return buildTokenCostBreakdown(pricing, input, serviceTierRates(pricing, input), {
      cacheReadIncludedInInput: false,
      cacheReadFallbackToInput: false,
      labels: {
        ...defaultTokenBillingLabels,
        cacheRead: '缓存读 Token',
        cacheWrite: '5m 缓存写入 Token',
        cacheWrite1h: '1h 缓存写入 Token'
      }
    })
  },
  buildCatalogDisplay(pricing) {
    return compactSections([
      priceSection('token_pricing', 'Token 计费', [
        ['input', '输入', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
        ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_read', '缓存读', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_write_5m', '5m 缓存写入', pricing.cacheWriteUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_write_1h', '1h 缓存写入', pricing.cacheWrite1hUsdPer1M, 'usd_per_1m_tokens']
      ]),
      ...serviceTierSections(pricing),
      reasoningSection(pricing),
      capacitySection(pricing),
      sourceConversionSection(pricing)
    ])
  }
}

export const geminiProviderBillingPolicy: ProviderBillingPolicy = {
  id: 'gemini',
  buildCostBreakdown(pricing, input) {
    const rates = applyLongContextRates(pricing, input, serviceTierRates(pricing, input))
    return buildTokenCostBreakdown(pricing, input, rates, {
      cacheReadIncludedInInput: true,
      cacheReadFallbackToInput: false,
      labels: {
        ...defaultTokenBillingLabels,
        cacheRead: '缓存读 Token'
      }
    })
  },
  buildCatalogDisplay(pricing) {
    return compactSections([
      priceSection('token_pricing', 'Token 计费', [
        ['input', '输入', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
        ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_read', '缓存读', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_storage', '缓存存储', pricing.cacheStorageUsdPer1MPerHour, 'usd_per_1m_token_hour'],
        ...mediaTokenEntries(pricing),
        ...longContextTokenEntries(pricing, '缓存读')
      ]),
      ...serviceTierSections(pricing),
      reasoningSection(pricing),
      capacitySection(pricing),
      sourceConversionSection(pricing)
    ])
  }
}

export const xAIProviderBillingPolicy: ProviderBillingPolicy = {
  id: 'xai',
  buildCostBreakdown(pricing, input) {
    const rates = applyLongContextRates(pricing, input, serviceTierRates(pricing, input))
    return buildTokenCostBreakdown(pricing, input, rates, {
      cacheReadIncludedInInput: true,
      cacheReadFallbackToInput: false,
      labels: defaultTokenBillingLabels
    })
  },
  buildCatalogDisplay(pricing) {
    return compactSections([
      priceSection('token_pricing', pricing.mode === 'image' ? '图像输入' : 'Token 计费', [
        ['input', '输入', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_read', '缓存读', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
        ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens'],
        ...longContextTokenEntries(pricing, '缓存读')
      ]),
      imageUnitSection(pricing),
      ...serviceTierSections(pricing),
      reasoningSection(pricing),
      capacitySection(pricing),
      sourceConversionSection(pricing)
    ])
  }
}

export const deepSeekProviderBillingPolicy: ProviderBillingPolicy = {
  id: 'deepseek',
  buildCostBreakdown(pricing, input) {
    if (hasUnsupportedServiceTier(pricing, input)) return undefined
    return buildTokenCostBreakdown(pricing, input, standardRates(pricing), {
      cacheReadIncludedInInput: true,
      cacheReadFallbackToInput: false,
      labels: {
        ...defaultTokenBillingLabels,
        input: '输入 Token',
        cacheRead: '缓存读 Token'
      }
    })
  },
  buildCatalogDisplay(pricing) {
    return compactSections([
      priceSection('token_pricing', 'Token 计费', [
        ['cache_miss', '输入', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_hit', '缓存读', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
        ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens']
      ]),
      reasoningSection(pricing),
      capacitySection(pricing),
      sourceConversionSection(pricing)
    ])
  }
}

export const glmProviderBillingPolicy: ProviderBillingPolicy = {
  id: 'glm',
  buildCostBreakdown(pricing, input) {
    if (hasUnsupportedServiceTier(pricing, input)) return undefined
    return buildTokenCostBreakdown(pricing, input, standardRates(pricing), {
      cacheReadIncludedInInput: true,
      cacheReadFallbackToInput: false,
      labels: {
        ...defaultTokenBillingLabels,
        cacheRead: '缓存读 Token'
      }
    })
  },
  buildCatalogDisplay(pricing) {
    const usesSingleTokenPrice = pricing.inputUsdPer1M !== undefined
      && pricing.inputUsdPer1M === pricing.outputUsdPer1M
      && pricing.cachedInputUsdPer1M === undefined
    return compactSections([
      usesSingleTokenPrice
        ? priceSection('token_pricing', 'Token 计费', [
          ['token', 'Token', pricing.inputUsdPer1M, 'usd_per_1m_tokens']
        ])
        : priceSection('token_pricing', 'Token 计费', [
          ['input', '输入', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
          ['cache_hit', '缓存读', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
          ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens']
        ]),
      reasoningSection(pricing),
      capacitySection(pricing)
    ])
  }
}

function mediaTokenEntries(
  pricing: ProviderBillingPricing
): Array<[string, string, number | undefined, 'usd_per_1m_tokens']> {
  return [
    ['image_input', '图片输入', pricing.imageInputUsdPer1M, 'usd_per_1m_tokens'],
    ['image_output', '图片输出', pricing.imageOutputUsdPer1M, 'usd_per_1m_tokens'],
    ['audio_input', '音频输入', pricing.audioInputUsdPer1M, 'usd_per_1m_tokens'],
    ['audio_output', '音频输出', pricing.audioOutputUsdPer1M, 'usd_per_1m_tokens']
  ]
}

function imageUnitSection(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection | undefined {
  return priceSection('image_generation', '图片生成', [
    ['output_image', '每张', pricing.outputUsdPerImage, 'usd_per_image']
  ])
}

function longContextTokenEntries(
  pricing: ProviderBillingPricing,
  cacheReadLabel: string
): Array<[string, string, number | undefined, 'usd_per_1m_tokens']> {
  const threshold = pricing.longContextInputTokenThreshold
  if (threshold === undefined) return []
  const prefix = `长上下文（${pricing.longContextInputTokenThresholdInclusive ? '>=' : '>'} ${formatTokenThreshold(threshold)}）`
  return [
    ['long_context_input', `${prefix}输入`, multipliedPrice(pricing.inputUsdPer1M, pricing.longContextInputCostMultiplier), 'usd_per_1m_tokens'],
    ['long_context_cache_read', `${prefix}${cacheReadLabel}`, multipliedPrice(pricing.cachedInputUsdPer1M, pricing.longContextInputCostMultiplier), 'usd_per_1m_tokens'],
    ['long_context_output', `${prefix}输出`, multipliedPrice(pricing.outputUsdPer1M, pricing.longContextOutputCostMultiplier), 'usd_per_1m_tokens']
  ]
}

function multipliedPrice(price: number | undefined, multiplier: number | undefined): number | undefined {
  if (price === undefined) return undefined
  return price * (typeof multiplier === 'number' && Number.isFinite(multiplier) && multiplier > 0 ? multiplier : 1)
}

function formatTokenThreshold(value: number): string {
  return value >= 1_000 && value % 1_000 === 0 ? `${value / 1_000}K` : String(value)
}
