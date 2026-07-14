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

const missingProviderResponse = withCostBreakdown(usageRecordFixture({
  id: 'usage_record_cost_breakdown_missing_provider',
  success: true,
  model: 'legacy-model-without-provider-code',
  inputTokens: 1,
  outputTokens: 2,
  costUsd: 0.123
}))
assert.equal(missingProviderResponse.costBreakdown?.accountChargeUsd, 0.123, '缺少供应商编码的历史记录应回退基础成本明细，不能让列表 / 详情 500')

const legacyRecordResponse = withCostBreakdown(usageRecordFixture({
  id: 'usage_record_cost_breakdown_legacy_without_snapshot',
  success: true,
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.5',
  upstreamModel: 'gpt-5.5-pro20x',
  billedServiceTier: 'priority',
  inputTokens: 1_000_000,
  outputTokens: 1_000_000,
  cacheReadTokens: 500_000,
  thinkingTokens: 435,
  cacheReadCostUsd: 0.25,
  costUsd: 3.5
}))
assert.equal(legacyRecordResponse.costBreakdown?.accountChargeUsd, 3.5, '旧空快照只能展示当时已落库的总成本')
assert.equal(legacyRecordResponse.costBreakdown?.cacheReadCostUsd, 0.25, '旧空快照应保留当时已落库的缓存成本')
assert.equal(legacyRecordResponse.costBreakdown?.thinkingTokens, 435, '旧空快照应保留当时已落库的思考 Tokens')
assert.equal(legacyRecordResponse.costBreakdown?.serviceTierPricingSource, 'unknown', '旧空快照必须明确标记计价来源未知')
assert.equal(legacyRecordResponse.costBreakdown?.inputUsdPer1M, undefined, '旧空快照禁止使用当前目录伪造历史单价')

const lockedSnapshot = {
  inputCostUsd: 1.25,
  inputUsdPer1M: 2.5,
  accountChargeUsd: 1.25,
  multiplier: 1 as const,
  serviceTierPricingSource: 'multiplier' as const,
  serviceTierMultiplier: 2.25
}
const lockedSnapshotResponse = withCostBreakdown(usageRecordFixture({
  id: 'usage_record_cost_breakdown_locked_snapshot',
  providerCode: GPT_VENDOR_CODE,
  model: 'gpt-5.6-sol',
  pricingModel: 'gpt-5.6-sol',
  billedServiceTier: 'priority',
  pricingSnapshot: lockedSnapshot,
  inputTokens: 500_000,
  costUsd: 1.25
}))
assert.deepEqual(lockedSnapshotResponse.costBreakdown, lockedSnapshot, '使用记录响应必须优先返回请求时锁定的计价快照')
assert.equal('pricingSnapshot' in lockedSnapshotResponse, false, '内部计价快照字段不能重复暴露给前端')

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
