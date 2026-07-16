import assert from 'node:assert/strict'

import { parseGeminiUsageFromJsonBuffer } from '../../modules/gateway/protocols/gemini-v1beta/usage.js'
import { parseAnthropicUsageFromJsonBuffer } from '../../modules/gateway/protocols/anthropic-v1/usage.js'
import { parseOpenAIUsageFromJsonBuffer } from '../../modules/gateway/protocols/openai-v1/usage.js'
import { estimateCatalogCostUsd } from '../../modules/model-pricing/model-catalog.service.js'
import { buildProviderCostBreakdown, estimateProviderCostUsd, listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'
import { resolveUsageServiceTiers } from '../../modules/gateway/usage/service-tier.js'
import { extractGatewayJsonBodyMetadata } from '../../modules/gateway/request/json-metadata-scanner.js'
import { readFileSync } from 'node:fs'
import type { Request } from 'express'
import { usageRecordSummaryFromRow } from '../../storage/usage-record-mappers.js'
import { buildCodexResponsesChatBridgeBody } from '../../modules/providers/drivers/_shared/codex-responses-chat-bridge.js'
import { saveCustomProviderModel } from '../../modules/model-pricing/model-catalog.service.js'
import { createSystemAccount } from '../../storage/repositories.js'

const catalogOwner = createSystemAccount({
  username: 'service_tier_billing_owner',
  displayName: 'ServiceTierBillingOwner',
  password: 'password',
  role: 'user',
  status: 'active',
  mustChangePassword: false
})
saveCustomProviderModel({
  providerCode: 'anthropic',
  model: 'claude-fast-billing-regression',
  scope: 'personal',
  systemAccountId: catalogOwner.id,
  supportedApiProtocols: ['messages'],
  supportedServiceTiers: ['fast'],
  inputUsdPer1M: 1,
  outputUsdPer1M: 2,
  serviceTierPrices: { fast: { inputUsdPer1M: 2, outputUsdPer1M: 4 } },
  actorSystemAccountId: catalogOwner.id
})

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
assert.equal(estimateCatalogCostUsd({
  providerCode: 'gpt', model: 'gpt-5.6-sol', serviceTier: 'standard', inputTokens: 100_000, outputTokens: 100_000
}), 3.5, 'standard 实际计费档位必须使用标准扁平价格')
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
  inputTokens: 100_000,
  outputTokens: 100_000
}), undefined, '缺少档位专用价时必须标记未定价，不能套用 Priority 通用倍率')
assert.equal(estimateProviderCostUsd({
  providerCode: 'gpt',
  model: 'gpt-5.4',
  serviceTier: 'flex',
  inputTokens: 100_000,
  outputTokens: 100_000
}), undefined, '缺少档位专用价时必须标记未定价，不能套用 Flex 通用倍率')
assert.equal(estimateProviderCostUsd({
  providerCode: 'gpt',
  model: 'gpt-5.6-sol',
  serviceTier: 'priority',
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
assert.equal(buildProviderCostBreakdown({
  providerCode: 'gpt',
  model: 'gpt-5.6-sol',
  serviceTier: 'priority',
  inputTokens: 100_000,
  outputTokens: 100_000
})?.serviceTierPricingSource, 'tier_specific', '模型档位专用价必须锁定为 tier_specific 计价来源')
const multiplierBreakdown = buildProviderCostBreakdown({
  providerCode: 'gpt',
  model: 'gpt-5.4-nano',
  serviceTier: 'priority',
  inputTokens: 100_000,
  outputTokens: 100_000
})
assert.equal(multiplierBreakdown, undefined, '缺少档位专用价时不得生成倍率计价快照')

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

assert.equal(extractGatewayJsonBodyMetadata(Buffer.from(JSON.stringify({
  model: 'gpt-5.6-sol',
  service_tier: 'flex'
}))).serviceTier, 'flex', '实际发往上游的结构化 JSON 必须识别 Flex 档位')
assert.equal(extractGatewayJsonBodyMetadata(Buffer.from(JSON.stringify({
  model: 'gpt-5.4',
  max_output_tokens: 8192
}))).maxOutputTokens, 8192, '大 JSON metadata scanner 应提取 max_output_tokens 供在途额度估算')
assert.equal(extractGatewayJsonBodyMetadata(Buffer.from(JSON.stringify({
  model: 'gpt-5.4',
  max_tokens: 4096
}))).maxOutputTokens, 4096, '大 JSON metadata scanner 应兼容提取 max_tokens')
assert.equal(extractGatewayJsonBodyMetadata(Buffer.from(JSON.stringify({
  model: 'gpt-5.6-sol',
  reasoning: { effort: 'high', summary: 'auto' }
}))).reasoningEffort, 'high', 'Responses 最终上游 body 必须提取 reasoning.effort')
assert.equal(extractGatewayJsonBodyMetadata(Buffer.from(JSON.stringify({
  model: 'gpt-5.6-sol',
  reasoning_effort: 'medium'
}))).reasoningEffort, 'medium', 'Chat Completions 最终上游 body 必须提取 reasoning_effort')
assert.equal(extractGatewayJsonBodyMetadata(Buffer.from(JSON.stringify({
  model: 'gpt-5.6-sol',
  reasoning: { effort: 'high' },
  reasoning_effort: 'low'
}))).reasoningEffort, 'high', '两种字段并存时大 JSON scanner 结果不能受字段顺序影响')

const bridgedChatBody = JSON.parse((await buildCodexResponsesChatBridgeBody({
  body: {
    model: 'gpt-5.6-sol',
    input: 'test',
    service_tier: 'flex',
    reasoning: { effort: 'high' }
  }
} as Request, { defaultModel: 'gpt-5.6-sol' })).toString('utf8')) as Record<string, unknown>
assert.equal(bridgedChatBody.service_tier, 'flex', 'Responses 到 Chat 桥接必须保留服务档位')
assert.equal(bridgedChatBody.reasoning_effort, 'high', 'Responses 到 Chat 桥接必须转换 reasoning.effort')

const geminiUsage = parseGeminiUsageFromJsonBuffer(Buffer.from(JSON.stringify({
  usageMetadata: {
    promptTokenCount: 80,
    candidatesTokenCount: 60,
    thoughtsTokenCount: 40
  }
})))
assert.equal(geminiUsage.outputTokens, 100, 'Gemini candidates 不含 thoughts，必须归一为完整可计费输出')
assert.equal(geminiUsage.thinkingTokens, 40)

const anthropicFastUsage = parseAnthropicUsageFromJsonBuffer(Buffer.from(JSON.stringify({
  usage: { input_tokens: 100_000, output_tokens: 100_000, speed: 'fast' }
})))
assert.equal(anthropicFastUsage.serviceTier, 'fast', 'Anthropic usage.speed 必须保留供应商原生档位')
const anthropicFastTiers = resolveUsageServiceTiers({
  requestedServiceTier: 'auto',
  effectiveServiceTier: 'auto',
  reportedServiceTier: anthropicFastUsage.serviceTier
})
assert.equal(anthropicFastTiers.billedServiceTier, 'fast')
assert.equal(estimateCatalogCostUsd({
  providerCode: 'anthropic',
  model: 'claude-fast-billing-regression',
  systemAccountId: catalogOwner.id,
  serviceTier: anthropicFastTiers.billedServiceTier,
  inputTokens: anthropicFastUsage.inputTokens,
  outputTokens: anthropicFastUsage.outputTokens
}), 0.6, 'Anthropic fast 必须按模型目录精确档位价格计费')

assert.deepEqual(resolveUsageServiceTiers({
  requestedServiceTier: 'flex',
  effectiveServiceTier: 'flex'
}), {
  requestedServiceTier: 'flex',
  effectiveServiceTier: 'flex',
  reportedServiceTier: undefined,
  billedServiceTier: 'flex'
}, '兼容中转不回传 service_tier 时必须按实际上游请求档位计费')
assert.equal(resolveUsageServiceTiers({
  requestedServiceTier: 'flex',
  effectiveServiceTier: 'flex',
  reportedServiceTier: 'priority'
}).billedServiceTier, 'priority', '上游明确报告实际档位时必须优先采用上游事实')

const usageSchemaSource = readFileSync(new URL('../../storage/usage-record-shards.ts', import.meta.url), 'utf8')
for (const column of ['requested_service_tier', 'effective_service_tier', 'reported_service_tier', 'billed_service_tier', 'requested_reasoning_effort', 'effective_reasoning_effort', 'cost_breakdown_snapshot_json']) {
  assert(usageSchemaSource.includes(column), `usage records schema 必须持久化 ${column}`)
}
const mappedServiceTiers = usageRecordSummaryFromRow({
  id: 'usage_service_tier_mapping',
  trace_id: 'trace_service_tier_mapping',
  traffic_source: 'gateway',
  stream: 0,
  success: 1,
  requested_service_tier: 'flex',
  effective_service_tier: 'priority',
  reported_service_tier: 'priority',
  billed_service_tier: 'priority',
  requested_reasoning_effort: 'low',
  effective_reasoning_effort: 'high',
  cost_breakdown_snapshot_json: JSON.stringify(multiplierBreakdown),
  created_at: '2026-07-11T00:00:00.000Z'
}, false, new Map())
assert.equal(mappedServiceTiers.requestedServiceTier, 'flex')
assert.equal(mappedServiceTiers.effectiveServiceTier, 'priority')
assert.equal(mappedServiceTiers.reportedServiceTier, 'priority')
assert.equal(mappedServiceTiers.billedServiceTier, 'priority')
assert.equal(mappedServiceTiers.requestedReasoningEffort, 'low')
assert.equal(mappedServiceTiers.effectiveReasoningEffort, 'high')
assert.equal(mappedServiceTiers.pricingSnapshot, undefined, '未生成档位专用价时不得持久化倍率计价快照')

const mappedProviderCapabilities = usageRecordSummaryFromRow({
  id: 'usage_provider_capability_mapping', trace_id: 'trace_provider_capability_mapping', traffic_source: 'gateway',
  stream: 0, success: 1, requested_service_tier: 'auto', effective_service_tier: 'auto',
  reported_service_tier: 'fast', billed_service_tier: 'fast', requested_reasoning_effort: 'adaptive',
  effective_reasoning_effort: 'adaptive', created_at: '2026-07-15T00:00:00.000Z'
}, false, new Map())
assert.equal(mappedProviderCapabilities.billedServiceTier, 'fast', 'usage 读回不能过滤非 GPT 档位')
assert.equal(mappedProviderCapabilities.effectiveReasoningEffort, 'adaptive', 'usage 读回不能过滤供应商原生思考值')

console.log('服务档位计费回归通过：GPT-5.6 精确三档、缺价未定价、长上下文和思考 Token 口径正确')

function cost(model: string, serviceTier: 'default' | 'priority' | 'flex', inputTokens: number, outputTokens: number): number | undefined {
  return estimateProviderCostUsd({
    providerCode: 'gpt',
    model,
    serviceTier,
    inputTokens,
    outputTokens
  })
}
