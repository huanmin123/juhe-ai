import assert from 'node:assert/strict'

import { GPT_VENDOR_CODE } from '../../domain/provider-protocol.js'
import type { UsageRecordSummary } from '../../storage/repositories.js'
import { withCostBreakdown } from '../../modules/usage-records/usage-records.routes.js'

const successRecord = usageRecordFixture({
  success: true,
  providerCode: 'unknown-provider-for-cost-breakdown-fallback',
  model: 'unknown-model-for-cost-breakdown-fallback',
  inputTokens: 148_638,
  outputTokens: 417,
  cacheReadTokens: 28_032,
  cacheReadCostUsd: 0,
  costUsd: 0
})
const successResponse = withCostBreakdown(successRecord)
assert(successResponse.costBreakdown, '成功使用记录即使模型目录无法匹配，也应下发基础成本明细供前端悬浮展示')
assert.equal(successResponse.costBreakdown?.accountChargeUsd, 0, '基础成本明细应保留记录自身成本')
assert.equal(successResponse.costBreakdown?.cacheReadCostUsd, 0, '基础成本明细应保留缓存读取成本')

const mappedAliasResponse = withCostBreakdown(usageRecordFixture({
  id: 'usage_record_cost_breakdown_mapped_alias',
  success: true,
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.5',
  upstreamModel: 'gpt-5.5-pro20x',
  inputTokens: 1_000_000,
  outputTokens: 1_000_000,
  cacheReadTokens: 500_000,
  thinkingTokens: 435
}))
assert(mappedAliasResponse.costBreakdown?.inputCostUsd !== undefined, '上游别名未命中价格时应回落来源模型展示输入成本')
assert(mappedAliasResponse.costBreakdown?.outputCostUsd !== undefined, '上游别名未命中价格时应回落来源模型展示输出成本')
assert(mappedAliasResponse.costBreakdown?.inputUsdPer1M !== undefined, '上游别名未命中价格时应回落来源模型展示输入单价')
assert(mappedAliasResponse.costBreakdown?.outputUsdPer1M !== undefined, '上游别名未命中价格时应回落来源模型展示输出单价')
assert((mappedAliasResponse.costBreakdown?.accountChargeUsd ?? 0) > 0, '上游别名未命中价格时不应把成本明细展示为 0')
assert.equal(mappedAliasResponse.costBreakdown?.thinkingTokens, 435, '成本明细应保留思考 Tokens')

const failedRecord = usageRecordFixture({
  id: 'usage_record_cost_breakdown_failed',
  success: false,
  statusCode: 401,
  providerCode: 'openai',
  model: 'gpt-4o',
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  costUsd: 0
})
const failedResponse = withCostBreakdown(failedRecord)
assert.equal(failedResponse.costBreakdown, undefined, '失败使用记录不应下发成本悬浮明细')

console.log('使用记录成本明细响应回归通过：成功记录展示完整成本卡片，失败记录不展示成本卡片')

function usageRecordFixture(overrides: Partial<UsageRecordSummary>): UsageRecordSummary {
  return {
    id: 'usage_record_cost_breakdown_success',
    traceId: 'trace_usage_record_cost_breakdown',
    trafficSource: 'gateway',
    systemAccountId: 'system_account_usage_record_cost_breakdown',
    groupId: 'group_usage_record_cost_breakdown',
    endpoint: '/v1/chat/completions',
    stream: false,
    success: true,
    createdAt: '2026-07-01T00:00:00.000Z',
    ...overrides
  }
}
