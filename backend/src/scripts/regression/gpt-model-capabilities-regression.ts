import assert from 'node:assert/strict'

import {
  buildCodexModelsResponseFromCatalog,
  type ProviderModelCatalogItem
} from '../../modules/model-pricing/model-catalog.service.js'
import {
  getProviderModelPricing,
  listProviderModelPricing
} from '../../modules/model-pricing/model-pricing.service.js'

const wireReasoning = ['none', 'low', 'medium', 'high', 'xhigh', 'max']
const codexReasoning = ['low', 'medium', 'high', 'xhigh', 'max']
const pricing = listProviderModelPricing('gpt')
const astra = requireModel('gpt-6-astra')
const sol = requireModel('gpt-5.6-sol')
const terra = requireModel('gpt-5.6-terra')
const luna = requireModel('gpt-5.6-luna')

const expectedApiReasoning = new Map<string, string[]>([
  ['gpt-6-astra', ['low', 'medium', 'high', 'xhigh', 'max']],
  ['gpt-5.5', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.5-2026-04-23', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.5-pro', ['medium', 'high', 'xhigh']],
  ['gpt-5.5-pro-2026-04-23', ['medium', 'high', 'xhigh']],
  ['gpt-5.4', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.4-2026-03-05', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.4-mini', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.4-mini-2026-03-17', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.4-nano', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.4-nano-2026-03-17', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.4-pro', ['medium', 'high', 'xhigh']],
  ['gpt-5.4-pro-2026-03-05', ['medium', 'high', 'xhigh']],
  ['gpt-5.2', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.2-2025-12-11', ['none', 'low', 'medium', 'high', 'xhigh']],
  ['gpt-5.2-pro', ['medium', 'high', 'xhigh']],
  ['gpt-5.2-pro-2025-12-11', ['medium', 'high', 'xhigh']],
  ['gpt-5.1', ['none', 'low', 'medium', 'high']],
  ['gpt-5.1-2025-11-13', ['none', 'low', 'medium', 'high']],
  ['gpt-5', ['minimal', 'low', 'medium', 'high']],
  ['gpt-5-2025-08-07', ['minimal', 'low', 'medium', 'high']]
])

for (const [model, efforts] of expectedApiReasoning) {
  assert.deepEqual(requireModel(model).supportedReasoningEfforts, efforts, `${model} API reasoning effort 必须与官方模型契约一致`)
}

for (const model of pricing) {
  assert.equal(model.defaultReasoningEffort, undefined, `${model.model} GPT API 默认思考级别必须留给上游决定`)
}

for (const model of [sol, terra, luna]) {
  assert.deepEqual(model.supportedServiceTiers, ['priority', 'flex'])
  assert.deepEqual(model.supportedReasoningEfforts, wireReasoning)
  assert(model.supportedTools.includes('function_calling'), `${model.model} 必须声明真实支持的函数调用能力，供站内工具路由使用`)
  assert.equal(model.supportsServiceTier, model.supportedServiceTiers.length > 0)
  assert.equal(model.supportedReasoningEfforts.some((effort) => effort === ('ultra' as string)), false)
}

assert.equal(astra.releaseDate, '2026-09-03')
assert.equal(astra.contextWindowTokens, 1_050_000)
assert.equal(astra.maxInputTokens, 922_000)
assert.equal(astra.maxOutputTokens, 128_000)
assert.deepEqual(astra.supportedServiceTiers, ['priority', 'flex'])
assert.deepEqual(astra.supportedReasoningEfforts, ['low', 'medium', 'high', 'xhigh', 'max'])
assert.equal(astra.defaultReasoningEffort, undefined)
assert.equal(astra.codexSupportedReasoningLevels.length, 0)
assert.equal(astra.codexDefaultReasoningLevel, undefined)
assert.equal(astra.codexMultiAgentVersion, undefined)
assert.deepEqual(astra.supportedApiProtocols, ['chat_completions', 'responses'])
assert.deepEqual(astra.inputModalities, ['text', 'image'])
assert.deepEqual(astra.outputModalities, ['text'])
assert.deepEqual(astra.supportedTools, ['function_calling', 'web_search', 'file_search', 'image_generation', 'code_interpreter', 'hosted_shell', 'apply_patch', 'skills', 'computer_use', 'mcp', 'tool_search'])
assert.deepEqual(
  [astra.inputUsdPer1M, astra.cachedInputUsdPer1M, astra.outputUsdPer1M, astra.cacheWriteUsdPer1M],
  [10, 1, 50, 12.5]
)
assert.deepEqual(
  [astra.serviceTierPrices?.priority?.inputUsdPer1M, astra.serviceTierPrices?.priority?.cachedInputUsdPer1M, astra.serviceTierPrices?.priority?.outputUsdPer1M, astra.serviceTierPrices?.priority?.cacheWriteUsdPer1M],
  [20, 2, 100, 25]
)
assert.deepEqual(
  [astra.serviceTierPrices?.flex?.inputUsdPer1M, astra.serviceTierPrices?.flex?.cachedInputUsdPer1M, astra.serviceTierPrices?.flex?.outputUsdPer1M, astra.serviceTierPrices?.flex?.cacheWriteUsdPer1M],
  [5, 0.5, 25, 6.25]
)
assert.equal(astra.longContextInputTokenThreshold, 272_000)
assert.equal(astra.longContextInputCostMultiplier, 2)
assert.equal(astra.longContextOutputCostMultiplier, 1.5)

assert.deepEqual(sol.codexSupportedReasoningLevels, [...codexReasoning, 'ultra'])
assert.deepEqual(terra.codexSupportedReasoningLevels, [...codexReasoning, 'ultra'])
assert.deepEqual(luna.codexSupportedReasoningLevels, codexReasoning)
assert.equal(sol.codexDefaultReasoningLevel, 'low')
assert.equal(terra.codexDefaultReasoningLevel, 'medium')
assert.equal(luna.codexDefaultReasoningLevel, 'medium')
assert.equal(sol.codexMultiAgentVersion, 'v2')
assert.equal(terra.codexMultiAgentVersion, 'v2')
assert.equal(luna.codexMultiAgentVersion, undefined)

for (const [model, standard, priority, flex] of [
  [sol, [5, 0.5, 30, 6.25], [10, 1, 60, 12.5], [2.5, 0.25, 15, 3.125]],
  [terra, [2, 0.2, 12, 2.5], [4, 0.4, 24, 5], [1, 0.1, 6, 1.25]],
  [luna, [0.2, 0.02, 1.2, 0.25], [0.4, 0.04, 2.4, 0.5], [0.1, 0.01, 0.6, 0.125]]
] as const) {
  assert(model.serviceTierPrices, `${model.model} must declare service-tier prices`)
  assert.deepEqual(
    [model.inputUsdPer1M, model.cachedInputUsdPer1M, model.outputUsdPer1M, model.cacheWriteUsdPer1M],
    standard,
    `${model.model} standard runtime price must match the catalog`
  )
  assert.deepEqual(
    [model.serviceTierPrices.priority?.inputUsdPer1M, model.serviceTierPrices.priority?.cachedInputUsdPer1M, model.serviceTierPrices.priority?.outputUsdPer1M, model.serviceTierPrices.priority?.cacheWriteUsdPer1M],
    priority,
    `${model.model} priority runtime price must match Fast-compatible 2x pricing`
  )
  assert.deepEqual(
    [model.serviceTierPrices.flex?.inputUsdPer1M, model.serviceTierPrices.flex?.cachedInputUsdPer1M, model.serviceTierPrices.flex?.outputUsdPer1M, model.serviceTierPrices.flex?.cacheWriteUsdPer1M],
    flex,
    `${model.model} flex runtime price must remain half of standard pricing`
  )
}

const alias = getProviderModelPricing('gpt', 'gpt-5.6')
assert.equal(alias?.model, 'gpt-5.6-sol')
assert.deepEqual(alias?.supportedReasoningEfforts, wireReasoning)
assert.deepEqual(alias?.codexSupportedReasoningLevels, [...codexReasoning, 'ultra'])

for (const model of pricing) {
  assert.equal(
    model.supportsServiceTier,
    model.supportedServiceTiers.length > 0,
    `${model.model} supportsServiceTier 必须从精确数组派生`
  )
}

const catalog = pricing.map((model): ProviderModelCatalogItem => ({
  ...model,
  defaultReasoningEffort: model.defaultReasoningEffort ?? null,
  scope: 'built_in',
  status: 'active'
}))
const codexModels = buildCodexModelsResponseFromCatalog(catalog).models
const codexSol = requireCodexModel('gpt-5.6-sol')
const codexTerra = requireCodexModel('gpt-5.6-terra')
const codexLuna = requireCodexModel('gpt-5.6-luna')

assert.deepEqual((codexSol.supported_reasoning_levels ?? []).map((item) => item.effort), [...codexReasoning, 'ultra'])
assert.deepEqual((codexTerra.supported_reasoning_levels ?? []).map((item) => item.effort), [...codexReasoning, 'ultra'])
assert.deepEqual((codexLuna.supported_reasoning_levels ?? []).map((item) => item.effort), codexReasoning)
assert.deepEqual(codexSol.additional_speed_tiers, ['fast'])
assert.deepEqual(codexTerra.additional_speed_tiers, ['fast'])
assert.deepEqual(codexLuna.additional_speed_tiers, ['fast'])
assert.deepEqual(codexSol.service_tiers.map((item) => item.id), ['priority', 'flex'])
assert.equal(codexSol.multi_agent_version, 'v2')
assert.equal(codexTerra.multi_agent_version, 'v2')
assert.equal(codexLuna.multi_agent_version, null)

const codexWithoutExplicitDefault = buildCodexModelsResponseFromCatalog([{
  ...catalog[0],
  model: 'custom-no-codex-default',
  codexSupportedReasoningLevels: ['low', 'high'],
  codexDefaultReasoningLevel: undefined,
  defaultReasoningEffort: 'high'
}]).models[0]
assert(codexWithoutExplicitDefault)
assert.deepEqual((codexWithoutExplicitDefault.supported_reasoning_levels ?? []).map((item) => item.effort), ['low', 'high'])
assert.equal(codexWithoutExplicitDefault.default_reasoning_level, undefined, 'Codex 默认思考级别不得从 API 默认或首个选项推断')

console.log('GPT model capabilities regression passed')

function requireModel(model: string) {
  const item = pricing.find((candidate) => candidate.model === model)
  assert(item, `缺少模型 ${model}`)
  return item
}

function requireCodexModel(model: string) {
  const item = codexModels.find((candidate) => candidate.slug === model)
  assert(item, `Codex /models 缺少模型 ${model}`)
  return item
}
