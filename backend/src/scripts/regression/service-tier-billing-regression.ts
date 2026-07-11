import assert from 'node:assert/strict'

import { parseGeminiUsageFromJsonBuffer } from '../../modules/gateway/protocols/gemini-v1beta/usage.js'
import { parseOpenAIUsageFromJsonBuffer } from '../../modules/gateway/protocols/openai-v1/usage.js'
import { estimateCatalogCostUsd } from '../../modules/model-pricing/model-catalog.service.js'
import { estimateProviderCostUsd, listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'

const gptPricing = listProviderModelPricing('gpt')
for (const model of [
  'gpt-5', 'gpt-5-mini', 'gpt-5-nano',
  'gpt-5.4', 'gpt-5.4-mini', 'gpt-5.4-nano', 'gpt-5.4-pro',
  'gpt-5.5', 'gpt-5.5-pro',
  'gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna',
  'o3', 'o4-mini'
]) {
  assert(gptPricing.find((item) => item.model === model)?.supportedServiceTiers.includes('flex'), `${model} 必须声明 Flex`)
}
assert.equal(gptPricing.find((item) => item.model === 'gpt-5.2')?.supportedServiceTiers.includes('flex'), false, '未声明 Flex 的旧模型不能误开放')

assert.equal(cost('gpt-5.6-sol', 'default', 100_000, 100_000), 3.5)
assert.equal(cost('gpt-5.6-sol', 'priority', 100_000, 100_000), 7)
assert.equal(cost('gpt-5.6-sol', 'flex', 100_000, 100_000), 1.75)
assert.equal(cost('gpt-5.6-terra', 'priority', 100_000, 100_000), 3.5)
assert.equal(cost('gpt-5.6-luna', 'flex', 100_000, 100_000), 0.35)
assert.equal(cost('gpt-5.6', 'priority', 100_000, 100_000), 7, '稳定别名必须按 Sol 计费')
assert.equal(cost('gpt-5.6-sol', 'default', 300_000, 100_000), 7.5, '超过 272K 后必须应用长上下文倍率')
assert.equal(estimateProviderCostUsd({
  providerCode: 'gpt',
  model: 'gpt-5.4-nano',
  serviceTier: 'priority',
  priorityPriceMultiplier: 3,
  inputTokens: 100_000,
  outputTokens: 100_000
}), 0.435, '缺少档位专用价时必须使用可配置 Priority 通用倍率')
assert.equal(estimateProviderCostUsd({
  providerCode: 'gpt',
  model: 'gpt-5.4',
  serviceTier: 'flex',
  flexPriceMultiplier: 0.4,
  inputTokens: 100_000,
  outputTokens: 100_000
}), 0.7, '缺少档位专用价时必须使用可配置 Flex 通用倍率')
assert.equal(estimateProviderCostUsd({
  providerCode: 'gpt',
  model: 'gpt-5.6-sol',
  serviceTier: 'priority',
  priorityPriceMultiplier: 3,
  inputTokens: 100_000,
  outputTokens: 100_000
}), 7, '模型精确档位价格必须优先于通用倍率')
assert.equal(estimateCatalogCostUsd({
  providerCode: 'gpt',
  model: 'gpt-5.6-sol',
  serviceTier: 'priority',
  inputTokens: 100_000,
  outputTokens: 100_000
}), 7, '网关使用的模型目录计价必须应用实际服务档位')

const openAIUsage = parseOpenAIUsageFromJsonBuffer(Buffer.from(JSON.stringify({
  service_tier: 'priority',
  usage: {
    input_tokens: 80,
    output_tokens: 100,
    output_tokens_details: { reasoning_tokens: 40 }
  }
})))
assert.equal(openAIUsage.serviceTier, 'priority')
assert.equal(openAIUsage.outputTokens, 100, 'OpenAI output_tokens 已包含 reasoning，不能重复相加')
assert.equal(openAIUsage.thinkingTokens, 40)

const geminiUsage = parseGeminiUsageFromJsonBuffer(Buffer.from(JSON.stringify({
  usageMetadata: {
    promptTokenCount: 80,
    candidatesTokenCount: 60,
    thoughtsTokenCount: 40
  }
})))
assert.equal(geminiUsage.outputTokens, 100, 'Gemini candidates 不含 thoughts，必须归一为完整可计费输出')
assert.equal(geminiUsage.thinkingTokens, 40)

console.log('服务档位计费回归通过：GPT-5.6 三档、长上下文和思考 Token 口径正确')

function cost(model: string, serviceTier: 'default' | 'priority' | 'flex', inputTokens: number, outputTokens: number): number | undefined {
  return estimateProviderCostUsd({
    providerCode: 'gpt',
    model,
    serviceTier,
    inputTokens,
    outputTokens
  })
}
