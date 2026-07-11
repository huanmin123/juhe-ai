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
const sol = requireModel('gpt-5.6-sol')
const terra = requireModel('gpt-5.6-terra')
const luna = requireModel('gpt-5.6-luna')

for (const model of [sol, terra, luna]) {
  assert.deepEqual(model.supportedServiceTiers, ['priority'])
  assert.deepEqual(model.supportedReasoningEfforts, wireReasoning)
  assert.equal(model.supportsServiceTier, model.supportedServiceTiers.length > 0)
  assert.equal(model.supportedReasoningEfforts.some((effort) => effort === ('ultra' as string)), false)
}

assert.deepEqual(sol.codexSupportedReasoningLevels, [...codexReasoning, 'ultra'])
assert.deepEqual(terra.codexSupportedReasoningLevels, [...codexReasoning, 'ultra'])
assert.deepEqual(luna.codexSupportedReasoningLevels, codexReasoning)
assert.equal(sol.codexDefaultReasoningLevel, 'low')
assert.equal(terra.codexDefaultReasoningLevel, 'medium')
assert.equal(luna.codexDefaultReasoningLevel, 'medium')
assert.equal(sol.codexMultiAgentVersion, 'v2')
assert.equal(terra.codexMultiAgentVersion, 'v2')
assert.equal(luna.codexMultiAgentVersion, undefined)

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
  scope: 'built_in',
  status: 'active'
}))
const codexModels = buildCodexModelsResponseFromCatalog(catalog).models
const codexSol = requireCodexModel('gpt-5.6-sol')
const codexTerra = requireCodexModel('gpt-5.6-terra')
const codexLuna = requireCodexModel('gpt-5.6-luna')

assert.deepEqual(codexSol.supported_reasoning_levels.map((item) => item.effort), [...codexReasoning, 'ultra'])
assert.deepEqual(codexTerra.supported_reasoning_levels.map((item) => item.effort), [...codexReasoning, 'ultra'])
assert.deepEqual(codexLuna.supported_reasoning_levels.map((item) => item.effort), codexReasoning)
assert.deepEqual(codexSol.additional_speed_tiers, ['fast'])
assert.deepEqual(codexTerra.additional_speed_tiers, ['fast'])
assert.deepEqual(codexLuna.additional_speed_tiers, ['fast'])
assert.deepEqual(codexSol.service_tiers.map((item) => item.id), ['priority'])
assert.equal(codexSol.multi_agent_version, 'v2')
assert.equal(codexTerra.multi_agent_version, 'v2')
assert.equal(codexLuna.multi_agent_version, null)

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
