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
  standardRates,
  textSection
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
        ['input', '输入', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
        ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_read', '缓存命中', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens']
      ]),
      mediaSection(pricing),
      imageUnitSection(pricing),
      ...serviceTierSections(pricing),
      longContextSection(pricing),
      reasoningSection(pricing),
      capacitySection(pricing),
      sourceConversionSection(pricing)
    ])
  }
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
        cacheRead: '缓存读取 Token',
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
        ['cache_read', '缓存读取', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
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
        cacheRead: 'Cached content Token'
      }
    })
  },
  buildCatalogDisplay(pricing) {
    return compactSections([
      priceSection('token_pricing', 'Token 计费', [
        ['input', '输入', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
        ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_read', '缓存命中', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_storage', '缓存存储', pricing.cacheStorageUsdPer1MPerHour, 'usd_per_1m_token_hour']
      ]),
      mediaSection(pricing),
      ...serviceTierSections(pricing),
      longContextSection(pricing),
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
        ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_read', '缓存命中', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens']
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

export const deepSeekProviderBillingPolicy: ProviderBillingPolicy = {
  id: 'deepseek',
  buildCostBreakdown(pricing, input) {
    if (hasUnsupportedServiceTier(pricing, input)) return undefined
    return buildTokenCostBreakdown(pricing, input, standardRates(pricing), {
      cacheReadIncludedInInput: true,
      cacheReadFallbackToInput: false,
      labels: {
        ...defaultTokenBillingLabels,
        input: 'Cache miss Token',
        cacheRead: 'Cache hit Token'
      }
    })
  },
  buildCatalogDisplay(pricing) {
    return compactSections([
      priceSection('token_pricing', 'Token 计费', [
        ['cache_miss', 'Cache miss', pricing.inputUsdPer1M, 'usd_per_1m_tokens'],
        ['cache_hit', 'Cache hit', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
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
        cacheRead: '自动缓存命中 Token'
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
          ['cache_hit', '缓存命中', pricing.cachedInputUsdPer1M, 'usd_per_1m_tokens'],
          ['output', '输出', pricing.outputUsdPer1M, 'usd_per_1m_tokens']
        ]),
      reasoningSection(pricing),
      capacitySection(pricing)
    ])
  }
}

function mediaSection(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection | undefined {
  return priceSection('multimodal_pricing', '多模态计费', [
    ['image_input', '图片输入', pricing.imageInputUsdPer1M, 'usd_per_1m_tokens'],
    ['image_output', '图片输出', pricing.imageOutputUsdPer1M, 'usd_per_1m_tokens'],
    ['audio_input', '音频输入', pricing.audioInputUsdPer1M, 'usd_per_1m_tokens'],
    ['audio_output', '音频输出', pricing.audioOutputUsdPer1M, 'usd_per_1m_tokens']
  ])
}

function imageUnitSection(pricing: ProviderBillingPricing): ProviderCatalogDisplaySection | undefined {
  return priceSection('image_generation', '图片生成', [
    ['output_image', '每张', pricing.outputUsdPerImage, 'usd_per_image']
  ])
}
