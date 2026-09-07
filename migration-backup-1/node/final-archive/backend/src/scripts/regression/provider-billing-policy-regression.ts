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
    expectedLabels: ['输入 Token', '输出 Token', '缓存读 Token']
  },
  {
    providerCode: 'anthropic',
    model: 'claude-sonnet-4-6',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.453,
    expectedLabels: ['输入 Token', '输出 Token', '缓存读 Token']
  },
  {
    providerCode: 'deepseek',
    model: 'deepseek-v4-flash',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.015428,
    expectedLabels: ['输入 Token', '输出 Token', '缓存读 Token']
  },
  {
    providerCode: 'glm',
    model: 'glm-5.2',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.1726,
    expectedLabels: ['输入 Token', '输出 Token', '缓存读 Token']
  },
  {
    providerCode: 'gemini',
    model: 'gemini-2.5-pro',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.21375,
    expectedLabels: ['输入 Token', '输出 Token', '缓存读 Token']
  },
  {
    providerCode: 'xai',
    model: 'grok-4.5',
    inputTokens: 100_000,
    outputTokens: 10_000,
    cacheReadTokens: 10_000,
    expectedCostUsd: 0.243,
    expectedLabels: ['输入 Token', '输出 Token', '缓存读 Token']
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

for (const testCase of representativeCases) {
  const labels = buildProviderCatalogDisplay(requiredPricing(testCase.providerCode, testCase.model))
    .flatMap((section) => section.items.map((item) => item.label))
  assert.doesNotMatch(
    labels.join(' / '),
    /Cache|缓存命中|缓存读取/,
    `${testCase.providerCode}/${testCase.model} 的模型目录缓存读标签必须统一为中文“缓存读”`
  )
}

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
  assert.doesNotMatch(String(section.items[0]?.value), /\bnone\b/, '关闭推理的 none 不是思考级别，不应显示在思考能力中')
}
const xAIReasoningSection = buildProviderCatalogDisplay(requiredPricing('xai', 'grok-4.5'))
  .find((item) => item.key === 'reasoning')
assert.equal(xAIReasoningSection?.items[0]?.value, 'low / medium / high / xhigh', '目录思考能力只显示支持级别，不展示默认标记')

const xAITokenSection = buildProviderCatalogDisplay(requiredPricing('xai', 'grok-4.5'))
  .find((item) => item.key === 'token_pricing')
assert(xAITokenSection, 'xAI/Grok 应显示 Token 计费栏目')
assert.equal(xAITokenSection.label, 'Token 计费', 'xAI/Grok 不应使用标准计费等同义标题')
assert.deepEqual(
  xAITokenSection.items.map((item) => [item.key, item.label, item.value]),
  [
    ['input', '输入', 2],
    ['cache_read', '缓存读', 0.3],
    ['output', '输出', 6],
    ['long_context_input', '长上下文（>= 200K）输入', 4],
    ['long_context_cache_read', '长上下文（>= 200K）缓存读', 0.6],
    ['long_context_output', '长上下文（>= 200K）输出', 12]
  ],
  'xAI/Grok 的标准、缓存读取与长上下文实际 Token 单价必须合并到同一栏目'
)
assert.equal(
  buildProviderCatalogDisplay(requiredPricing('xai', 'grok-4.5')).some((item) => item.key === 'long_context'),
  false,
  'xAI/Grok 不应生成独立长上下文计费栏目'
)

const openAI56Display = buildProviderCatalogDisplay(requiredPricing('gpt', 'gpt-5.6-sol'))
for (const [sectionKey, expectedItems] of [
  ['token_pricing', [
    ['input', '输入', 5],
    ['cache_read', '缓存读', 0.5],
    ['cache_write', '缓存写入', 6.25],
    ['output', '输出', 30]
  ]],
  ['tier_priority', [
    ['input', '输入', 10],
    ['cache_read', '缓存读', 1],
    ['cache_write', '缓存写入', 12.5],
    ['output', '输出', 60]
  ]],
  ['tier_flex', [
    ['input', '输入', 2.5],
    ['cache_read', '缓存读', 0.25],
    ['cache_write', '缓存写入', 3.125],
    ['output', '输出', 15]
  ]]
] as const) {
  const section = openAI56Display.find((item) => item.key === sectionKey)
  assert(section, `GPT-5.6 Sol 应显示 ${sectionKey} 计费栏目`)
  assert.deepEqual(
    section.items.map((item) => [item.key, item.label, item.value]),
    expectedItems,
    `GPT-5.6 Sol 的 ${sectionKey} 应按官方价格完整显示输入、缓存读、缓存写入和输出`
  )
}

const openAIImageDisplay = buildProviderCatalogDisplay(requiredPricing('gpt', 'gpt-image-2'))
const openAIImageTokenSection = openAIImageDisplay.find((item) => item.key === 'token_pricing')
assert.deepEqual(
  openAIImageTokenSection?.items.map((item) => [item.key, item.label, item.value]),
  [
    ['input', '文本输入', 5],
    ['cache_read', '文本缓存读', 1.25],
    ['image_input', '图片输入', 8],
    ['image_cache_read', '图片缓存读', 2],
    ['image_output', '图片输出', 30]
  ],
  'GPT 图片模型的文本、缓存和图片 Token 单价必须合并到同一个 Token 计费栏目'
)
assert.equal(
  openAIImageDisplay.some((item) => item.key === 'multimodal_pricing'),
  false,
  'GPT 不应生成独立多模态计费栏目'
)

const openAITierFamilies = [
  {
    models: ['gpt-5.5', 'gpt-5.5-2026-04-23'],
    tiers: {
      tier_priority: [['input', '输入', 12.5], ['cache_read', '缓存读', 1.25], ['output', '输出', 75]],
      tier_flex: [['input', '输入', 2.5], ['cache_read', '缓存读', 0.25], ['output', '输出', 15]]
    }
  },
  {
    models: ['gpt-5.5-pro', 'gpt-5.5-pro-2026-04-23'],
    tiers: { tier_flex: [['input', '输入', 15], ['output', '输出', 90]] }
  },
  {
    models: ['gpt-5.4', 'gpt-5.4-2026-03-05'],
    tiers: {
      tier_priority: [['input', '输入', 5], ['cache_read', '缓存读', 0.5], ['output', '输出', 30]],
      tier_flex: [['input', '输入', 1.25], ['cache_read', '缓存读', 0.13], ['output', '输出', 7.5]]
    }
  },
  {
    models: ['gpt-5.4-mini', 'gpt-5.4-mini-2026-03-17'],
    tiers: {
      tier_priority: [['input', '输入', 1.5], ['cache_read', '缓存读', 0.15], ['output', '输出', 9]],
      tier_flex: [['input', '输入', 0.375], ['cache_read', '缓存读', 0.0375], ['output', '输出', 2.25]]
    }
  },
  {
    models: ['gpt-5.4-nano', 'gpt-5.4-nano-2026-03-17'],
    tiers: { tier_flex: [['input', '输入', 0.1], ['cache_read', '缓存读', 0.01], ['output', '输出', 0.625]] }
  },
  {
    models: ['gpt-5.4-pro', 'gpt-5.4-pro-2026-03-05'],
    tiers: { tier_flex: [['input', '输入', 15], ['output', '输出', 90]] }
  }
] as const

for (const testCase of openAITierFamilies) {
  for (const model of testCase.models) {
    const display = buildProviderCatalogDisplay(requiredPricing('gpt', model))
    const actualTierKeys = display.filter((section) => section.key.startsWith('tier_')).map((section) => section.key)
    assert.deepEqual(actualTierKeys, Object.keys(testCase.tiers), `${model} 只能显示官方已公布价格的服务档位`)
    for (const [sectionKey, expectedItems] of Object.entries(testCase.tiers)) {
      const section = display.find((item) => item.key === sectionKey)
      assert(section, `${model} 应显示 ${sectionKey} 计费栏目`)
      assert.deepEqual(
        section.items.map((item) => [item.key, item.label, item.value]),
        expectedItems,
        `${model} 的 ${sectionKey} 条目数量与价格必须和官方表一致`
      )
    }
  }
}

const geminiTokenSection = buildProviderCatalogDisplay(requiredPricing('gemini', 'gemini-2.5-pro'))
  .find((item) => item.key === 'token_pricing')
assert(geminiTokenSection, 'Gemini 应显示 Token 计费栏目')
assert.deepEqual(
  geminiTokenSection.items.map((item) => [item.key, item.label, item.value]),
  [
    ['input', '输入', 1.25],
    ['output', '输出', 10],
    ['cache_read', '缓存读', 0.125],
    ['cache_storage', '缓存存储', 4.5],
    ['long_context_input', '长上下文（> 200K）输入', 2.5],
    ['long_context_cache_read', '长上下文（> 200K）缓存读', 0.25],
    ['long_context_output', '长上下文（> 200K）输出', 15]
  ],
  'Gemini 的缓存与长上下文实际 Token 单价必须合并到同一栏目'
)
const geminiFlashTokenSection = buildProviderCatalogDisplay(requiredPricing('gemini', 'gemini-2.5-flash'))
  .find((item) => item.key === 'token_pricing')
assert.equal(
  geminiFlashTokenSection?.items.some((item) => item.key === 'audio_input' && item.label === '音频输入' && item.value === 1),
  true,
  'Gemini 的多模态 Token 价格必须合并到 Token 计费栏目'
)
for (const model of ['gemini-2.5-pro', 'gemini-2.5-flash']) {
  const display = buildProviderCatalogDisplay(requiredPricing('gemini', model))
  assert.equal(display.some((item) => item.key === 'multimodal_pricing'), false, 'Gemini 不应生成独立多模态计费栏目')
  assert.equal(display.some((item) => item.key === 'long_context'), false, 'Gemini 不应生成独立长上下文计费栏目')
}

for (const testCase of [
  { providerCode: 'gpt', model: 'gpt-5.6-sol', cacheItemKeys: ['cache_read', 'cache_write'] },
  { providerCode: 'anthropic', model: 'claude-sonnet-4-6', cacheItemKeys: ['cache_read', 'cache_write_5m', 'cache_write_1h'] },
  { providerCode: 'deepseek', model: 'deepseek-v4-flash', cacheItemKeys: ['cache_hit'] },
  { providerCode: 'gemini', model: 'gemini-2.5-pro', cacheItemKeys: ['cache_read', 'cache_storage'] },
  { providerCode: 'xai', model: 'grok-4.5', cacheItemKeys: ['cache_read'] },
  { providerCode: 'glm', model: 'glm-5.2', cacheItemKeys: ['cache_hit'] }
] as const) {
  const display = buildProviderCatalogDisplay(requiredPricing(testCase.providerCode, testCase.model))
  const tokenSections = display.filter((section) => section.key === 'token_pricing')
  assert.equal(tokenSections.length, 1, `${testCase.providerCode}/${testCase.model} 只应有一个 Token 计费栏目`)
  const tokenSection = tokenSections[0]
  assert(tokenSection, `${testCase.providerCode}/${testCase.model} 应显示 Token 计费栏目`)
  for (const cacheItemKey of testCase.cacheItemKeys) {
    assert.equal(
      tokenSection.items.some((item) => item.key === cacheItemKey),
      true,
      `${testCase.providerCode}/${testCase.model} 的缓存价格必须属于 Token 计费栏目`
    )
  }
  assert.equal(
    display.some((section) => ['cache_read', 'prompt_caching', 'context_caching', 'automatic_caching'].includes(section.key)),
    false,
    `${testCase.providerCode}/${testCase.model} 不应生成独立缓存计费栏目`
  )
}

const deepSeekTokenSection = buildProviderCatalogDisplay(requiredPricing('deepseek', 'deepseek-v4-flash'))
  .find((item) => item.key === 'token_pricing')
assert.deepEqual(
  deepSeekTokenSection?.items.map((item) => [item.key, item.label]),
  [['cache_miss', '输入'], ['cache_hit', '缓存读'], ['output', '输出']],
  'DeepSeek 的 cache miss 必须归一为输入，cache hit 必须归一为缓存读'
)

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
