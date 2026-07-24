import assert from 'node:assert/strict'

import {
  getProviderModelPricing,
  type ProviderCostBreakdown,
  type ProviderModelPricing
} from '../../modules/model-pricing/model-pricing.service.js'
import {
  buildProviderBillingCostBreakdown,
  buildProviderCatalogDisplay
} from '../../modules/model-pricing/provider-billing.service.js'
import type {
  ProviderBillingCostInput
} from '../../modules/model-pricing/provider-billing.types.js'

interface RepresentativeCase {
  providerCode: string
  model: string
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  expectedCostUsd: number
  expectedLabels: string[]
}

const representativeCases: RepresentativeCase[] = [
  {
    providerCode: 'gpt',
    model: 'gpt-5.6-sol',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.755,
    expectedLabels: ['输入 Token', '输出 Token', '缓存命中 Token']
  },
  {
    providerCode: 'anthropic',
    model: 'claude-sonnet-4-6',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.453,
    expectedLabels: ['输入 Token', '输出 Token', '缓存读取 Token']
  },
  {
    providerCode: 'deepseek',
    model: 'deepseek-v4-flash',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.015428,
    expectedLabels: ['Cache miss Token', '输出 Token', 'Cache hit Token']
  },
  {
    providerCode: 'glm',
    model: 'glm-5.2',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.1726,
    expectedLabels: ['输入 Token', '输出 Token', '自动缓存命中 Token']
  },
  {
    providerCode: 'gemini',
    model: 'gemini-2.5-pro',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.21375,
    expectedLabels: ['输入 Token', '输出 Token', 'Cached content Token']
  },
  {
    providerCode: 'xai',
    model: 'grok-4.5',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.243,
    expectedLabels: ['输入 Token', '输出 Token', '缓存命中 Token']
  }
]

const representativeBreakdowns = representativeCases.map((testCase) => {
  const pricing = requiredPricing(testCase.providerCode, testCase.model)
  const breakdown = requiredBreakdown(pricing, {
    inputTokens: testCase.inputTokens,
    outputTokens: testCase.outputTokens,
    cacheReadTokens: testCase.cacheReadTokens
  })
  assert.equal(
    breakdown.accountChargeUsd,
    testCase.expectedCostUsd,
    `${testCase.providerCode}/${testCase.model} 应按供应商独立策略计费`
  )
  assert.deepEqual(
    breakdown.lineItems?.map((line) => line.label),
    testCase.expectedLabels,
    `${testCase.providerCode}/${testCase.model} 应保留供应商计费项标签`
  )
  return breakdown
})

for (const [providerCode, model] of [
  ['gpt', 'gpt-5.6-sol'],
  ['anthropic', 'claude-sonnet-4-6'],
  ['gemini', 'gemini-2.5-pro'],
  ['xai', 'grok-4.5'],
  ['glm', 'glm-5.2']
] as const) {
  const section = buildProviderCatalogDisplay(requiredPricing(providerCode, model))
    .find((item) => item.key === 'reasoning')
  assert(section, `${providerCode}/${model} 应显示思考能力`)
  assert.equal(section.label, '思考能力', `${providerCode}/${model} 的同类能力标题必须统一`)
  assert.match(String(section.items[0]?.value), /low|medium|high|max/, '思考级别必须保留上游原始值')
}
const xAIReasoningSection = buildProviderCatalogDisplay(requiredPricing('xai', 'grok-4.5'))
  .find((item) => item.key === 'reasoning')
assert.equal(xAIReasoningSection?.items[0]?.value, 'low / medium / high / xhigh', '目录思考能力只显示支持级别，不展示默认标记')

const anthropicPricing = requiredPricing('anthropic', 'claude-sonnet-4-6')
const anthropicCacheBreakdown = requiredBreakdown(anthropicPricing, {
  inputTokens: 1_000_000,
  cacheReadTokens: 1_000_000
})
assert.equal(anthropicCacheBreakdown.inputCostUsd, 3, 'Anthropic input_tokens 不包含 cached tokens，不能扣减缓存读取量')
assert.equal(anthropicCacheBreakdown.cacheReadCostUsd, 0.3, 'Anthropic 缓存读取必须按独立费率计费')
assert.equal(anthropicCacheBreakdown.accountChargeUsd, 3.3, 'Anthropic 输入和缓存读取必须分别累计且不重复归一')

const openAIPricingWithoutCache = {
  ...requiredPricing('gpt', 'gpt-5.6-sol'),
  cachedInputUsdPer1M: undefined
}
const openAICacheFallback = requiredBreakdown(openAIPricingWithoutCache, {
  inputTokens: 1_000_000,
  cacheReadTokens: 1_000_000
})
assert.equal(openAICacheFallback.cacheReadUsdPer1M, 10, 'OpenAI 长上下文缓存缺价时应显式回退到已生效的输入价')
assert.equal(openAICacheFallback.accountChargeUsd, 10, 'OpenAI 缓存回退不得重复计算普通输入')

for (const providerCode of ['anthropic', 'deepseek', 'glm', 'gemini', 'xai']) {
  const original = representativePricing(providerCode)
  const pricingWithoutCache = { ...original, cachedInputUsdPer1M: undefined }
  const breakdown = buildProviderBillingCostBreakdown(pricingWithoutCache, {
    providerCode,
    model: pricingWithoutCache.model,
    inputTokens: 1_000_000,
    cacheReadTokens: 1_000_000
  })
  assert.equal(breakdown, undefined, `${providerCode} 缓存费率缺失时必须保持 unpriced，不能回退到输入价`)
}

assertLongContextBoundary({
  pricing: requiredPricing('xai', 'grok-4.5'),
  threshold: 200_000,
  expected: [
    { inputRate: 2, outputRate: 6 },
    { inputRate: 4, outputRate: 12 },
    { inputRate: 4, outputRate: 12 }
  ]
})

assertLongContextBoundary({
  pricing: requiredPricing('gemini', 'gemini-2.5-pro'),
  threshold: 200_000,
  expected: [
    { inputRate: 1.25, outputRate: 10 },
    { inputRate: 1.25, outputRate: 10 },
    { inputRate: 2.5, outputRate: 15 }
  ]
})

const unknownTierBreakdown = buildProviderBillingCostBreakdown(requiredPricing('gpt', 'gpt-5.6-sol'), {
  providerCode: 'gpt',
  model: 'gpt-5.6-sol',
  serviceTier: 'unregistered-tier',
  inputTokens: 1_000
})
assert.equal(unknownTierBreakdown, undefined, '未知 service tier 必须 unpriced，不能回退标准价')

const tieredMediaPricing: ProviderModelPricing = {
  ...requiredPricing('gpt', 'gpt-5.6-sol'),
  imageInputUsdPer1M: 2,
  audioInputUsdPer1M: 3,
  outputUsdPerImage: 0.04,
  supportedServiceTiers: ['priority'],
  serviceTierPrices: {
    priority: {
      inputUsdPer1M: 20,
      outputUsdPer1M: 30,
      cachedInputUsdPer1M: 4
    }
  }
}
const tieredMediaBreakdown = requiredBreakdown(tieredMediaPricing, {
  serviceTier: 'priority',
  inputTokens: 2_000_000,
  inputImageTokens: 1_000_000,
  inputAudioTokens: 1_000_000,
  outputImageCount: 2
})
assert.equal(tieredMediaBreakdown.accountChargeUsd, 5.08, '档位文本价格不得覆盖供应商独立图片、音频和按张价格')
assert.deepEqual(
  tieredMediaBreakdown.lineItems?.map((line) => [line.kind, line.unitPriceUsd]),
  [['image_input', 2], ['audio_input', 3], ['image_output_unit', 0.04]],
  '档位未单列媒体价格时应继承供应商标准媒体维度'
)
assert.equal(buildProviderBillingCostBreakdown({ ...tieredMediaPricing, outputUsdPerImage: undefined }, {
  providerCode: 'gpt',
  model: tieredMediaPricing.model,
  serviceTier: 'priority',
  outputImageCount: 1
}), undefined, '存在按张图片用量但目录缺价时必须保持 unpriced')

const glmCatalogDisplay = buildProviderCatalogDisplay(requiredPricing('glm', 'glm-5.2'))
assert.equal(glmCatalogDisplay.some((section) => section.key === 'batch'), false, 'GLM 目录不应生成批量处理列')
assert.equal(glmCatalogDisplay.some((section) => section.key === 'currency_conversion'), false, 'GLM 目录不应生成美元换算列')

const serializedHistory = JSON.stringify({
  version: 1,
  breakdowns: representativeBreakdowns
})
const restoredHistory = JSON.parse(serializedHistory) as {
  version: number
  breakdowns: ProviderCostBreakdown[]
}
assert.equal(restoredHistory.version, 1)
assert.equal(restoredHistory.breakdowns.length, representativeCases.length)
assert.deepEqual(
  restoredHistory.breakdowns.map((breakdown) => breakdown.accountChargeUsd),
  representativeCases.map((testCase) => testCase.expectedCostUsd),
  '历史快照序列化后必须保留六家供应商的美元总成本'
)
assert.deepEqual(
  restoredHistory.breakdowns.flatMap((breakdown) => breakdown.lineItems ?? []).map((line) => line.label),
  representativeBreakdowns.flatMap((breakdown) => breakdown.lineItems ?? []).map((line) => line.label),
  '历史快照序列化后必须保留供应商 lineItems 标签'
)

console.log('provider billing policy regression passed')

function requiredPricing(providerCode: string, model: string): ProviderModelPricing {
  const pricing = getProviderModelPricing(providerCode, model)
  assert(pricing, `缺少代表模型价格：${providerCode}/${model}`)
  return pricing
}

function representativePricing(providerCode: string): ProviderModelPricing {
  const testCase = representativeCases.find((item) => item.providerCode === providerCode)
  assert(testCase, `缺少供应商代表用例：${providerCode}`)
  return requiredPricing(testCase.providerCode, testCase.model)
}

function requiredBreakdown(
  pricing: ProviderModelPricing,
  input: Omit<ProviderBillingCostInput, 'providerCode' | 'model'>
): ProviderCostBreakdown {
  const breakdown = buildProviderBillingCostBreakdown(pricing, {
    providerCode: pricing.providerCode,
    model: pricing.model,
    ...input
  })
  assert(breakdown, `计费结果不应为空：${pricing.providerCode}/${pricing.model}`)
  return breakdown
}

function assertLongContextBoundary(input: {
  pricing: ProviderModelPricing
  threshold: number
  expected: Array<{ inputRate: number; outputRate: number }>
}): void {
  const tokenCounts = [input.threshold - 1, input.threshold, input.threshold + 1]
  tokenCounts.forEach((inputTokens, index) => {
    const breakdown = requiredBreakdown(input.pricing, { inputTokens, outputTokens: 1 })
    assert.equal(
      breakdown.inputUsdPer1M,
      input.expected[index]?.inputRate,
      `${input.pricing.providerCode}/${input.pricing.model} 输入长上下文边界 ${inputTokens}`
    )
    assert.equal(
      breakdown.outputUsdPer1M,
      input.expected[index]?.outputRate,
      `${input.pricing.providerCode}/${input.pricing.model} 输出长上下文边界 ${inputTokens}`
    )
  })
}
