import type { UsageRecordSummary } from '@/types/domain'
import {
  usageRecordCostAmountRows,
  usageRecordCostDetailTitle,
  usageRecordHasCostDetails,
  usageRecordCostMetadataRows,
  usageRecordCostPriceRows,
  usageRecordCostTokenRows
} from '../../views/usage-records/usageRecordCostDetails'
import {
  formatRecordTokens,
  usageRecordLatencyParts,
  usageRecordReasoningEffortText,
  usageRecordServiceTierText,
  usageRecordTokenParts
} from '../../views/usage-records/usageRecordFormatters'

const richRecord = usageRecordFixture({
  inputTokens: 1000,
  outputTokens: 250,
  cacheReadTokens: 125,
  cacheWriteTokens: 60,
  cacheWrite1hTokens: 30,
  thinkingTokens: 40,
  inputImageTokens: 7,
  outputImageTokens: 2,
  inputAudioTokens: 11,
  outputAudioTokens: 13,
  outputImageCount: 3
})

assertArrayEqual(usageRecordTokenParts(richRecord), [
  '输入 1,000',
  '输出 250',
  '缓存读 125'
], '使用记录 Token 用量列只展示输入、输出和缓存读')
assertEqual(
  formatRecordTokens(richRecord),
  '输入 1,000 / 输出 250 / 缓存读 125',
  '移动端 Token 摘要必须复用三项列表明细'
)

assertArrayEqual(usageRecordTokenParts(usageRecordFixture({
  inputTokens: 1,
  outputTokens: 2,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
  cacheWrite1hTokens: 0,
  thinkingTokens: 0,
  inputImageTokens: 0,
  outputImageTokens: 0,
  inputAudioTokens: 0,
  outputAudioTokens: 0,
  outputImageCount: 0
})), [
  '输入 1',
  '输出 2',
  '缓存读 0'
], '为 0 的扩展 Token 维度不应占用使用记录列表空间')

const anthropicCostRecord = usageRecordFixture({
  providerCode: 'anthropic',
  usageSemantic: 'anthropic',
  inputTokens: 1000,
  outputTokens: 250,
  cacheReadTokens: 125,
  cacheWriteTokens: 60,
  cacheWrite1hTokens: 30,
  thinkingTokens: 40,
  inputImageTokens: 7,
  outputImageTokens: 2,
  inputAudioTokens: 11,
  outputAudioTokens: 13,
  outputImageCount: 3,
  costBreakdown: {
    inputCostUsd: 0.003,
    outputCostUsd: 0.00375,
    cacheReadCostUsd: 0.0000375,
    cacheWriteCostUsd: 0.0001125,
    cacheWrite1hCostUsd: 0.00018,
    inputAudioCostUsd: 0.000044,
    outputAudioCostUsd: 0.000156,
    outputImageUnitCostUsd: 0.12,
    inputUsdPer1M: 3,
    outputUsdPer1M: 15,
    cacheReadUsdPer1M: 0.3,
    cacheWriteUsdPer1M: 3.75,
    cacheWrite1hUsdPer1M: 6,
    inputAudioUsdPer1M: 4,
    outputAudioUsdPer1M: 12,
    outputUsdPerImage: 0.04,
    thinkingTokens: 40,
    accountChargeUsd: 0.00708,
    multiplier: 1,
    serviceTierPricingSource: 'default'
  }
})
assertEqual(usageRecordCostDetailTitle(anthropicCostRecord), '成本明细', '成本明细标题不再展示供应商或模型口径')
assertArrayEqual(rowTexts(usageRecordCostTokenRows(anthropicCostRecord)), [
  '5m 缓存写入 Tokens 30',
  '1h 缓存写入 Tokens 30',
  '思考 Tokens 40',
  '图片输入 Tokens 7',
  '图片输出 Tokens 2',
  '音频输入 Tokens 11',
  '音频输出 Tokens 13',
  '输出图片张数 3 张'
], 'Anthropic 成本明细应拆出 5m、1h、思考、图片和音频用量')
assertArrayIncludes(rowTexts(usageRecordCostAmountRows(anthropicCostRecord)), [
  '5m 缓存写入成本 $0.000112',
  '1h 缓存写入成本 $0.000180',
  '音频输入成本 $0.000044',
  '音频输出成本 $0.000156',
  '图片张数成本 $0.120000',
  '合计成本 $0.007080'
], 'Anthropic 成本明细应展示缓存写入、音频、按张图片和合计成本')
assertArrayIncludes(rowTexts(usageRecordCostPriceRows(anthropicCostRecord)), [
  '5m 缓存写入单价 $3.7500 / 1M Token',
  '1h 缓存写入单价 $6.0000 / 1M Token',
  '音频输入单价 $4.0000 / 1M Token',
  '音频输出单价 $12.0000 / 1M Token',
  '每张图片单价 $0.040000'
], 'Anthropic 成本明细应展示缓存写入、音频和按张图片单价')

const openAICostRecord = usageRecordFixture({
  providerCode: 'openai',
  usageSemantic: 'openai',
  inputTokens: 1000,
  outputTokens: 250,
  cacheReadTokens: 125,
  cacheWriteTokens: 0,
  cacheWrite1hTokens: 0,
  costBreakdown: {
    inputCostUsd: 0.001,
    outputCostUsd: 0.0025,
    cacheReadCostUsd: 0.000125,
    inputUsdPer1M: 1,
    outputUsdPer1M: 10,
    cacheReadUsdPer1M: 0.1,
    accountChargeUsd: 0.003625,
    multiplier: 1,
    serviceTierPricingSource: 'default'
  }
})
assertArrayEqual(rowTexts(usageRecordCostTokenRows(openAICostRecord)), [], 'OpenAI 普通文本记录没有扩展 Token 时不应展示额外 Token 明细')
assertArrayEqual(rowTexts(usageRecordCostPriceRows(openAICostRecord)), [
  '输入单价 $1.0000 / 1M Token',
  '输出单价 $10.0000 / 1M Token',
  '缓存读单价 $0.1000 / 1M Token'
], 'OpenAI 普通文本记录只展示输入、输出和缓存读单价')

const openAICacheWriteCostRecord = usageRecordFixture({
  providerCode: 'openai',
  usageSemantic: 'openai',
  inputTokens: 1000,
  outputTokens: 250,
  cacheReadTokens: 125,
  cacheWriteTokens: 80,
  cacheWrite1hTokens: 0,
  costBreakdown: {
    inputCostUsd: 0.001,
    outputCostUsd: 0.0025,
    cacheReadCostUsd: 0.000125,
    cacheWriteCostUsd: 0.0001,
    inputUsdPer1M: 1,
    outputUsdPer1M: 10,
    cacheReadUsdPer1M: 0.1,
    cacheWriteUsdPer1M: 1.25,
    accountChargeUsd: 0.003725,
    multiplier: 1,
    serviceTierPricingSource: 'default'
  }
})
assertEqual(usageRecordCostDetailTitle(openAICacheWriteCostRecord), '成本明细', 'OpenAI 兼容成本明细标题不再显示模型口径')
assertEqual(
  usageRecordCostDetailTitle(usageRecordFixture({ providerCode: 'openai-compatible' })),
  '成本明细',
  'OpenAI-compatible 驱动别名也不应展示模型口径'
)
assertArrayEqual(rowTexts(usageRecordCostTokenRows(openAICacheWriteCostRecord)), [
  '缓存写入 Tokens 80'
], 'OpenAI 兼容记录有缓存写入时应在成本明细展示缓存写入 Tokens')
assertArrayIncludes(rowTexts(usageRecordCostAmountRows(openAICacheWriteCostRecord)), [
  '缓存写入成本 $0.000100',
  '合计成本 $0.003725'
], 'OpenAI 兼容记录有缓存写入成本时应展示缓存写入成本')
assertArrayIncludes(rowTexts(usageRecordCostPriceRows(openAICacheWriteCostRecord)), [
  '缓存写入单价 $1.2500 / 1M Token'
], 'OpenAI 兼容记录有缓存写入单价时应展示缓存写入单价')

const openAICacheWriteTokenOnlyRecord = usageRecordFixture({
  providerCode: 'openai',
  usageSemantic: 'openai',
  cacheWriteTokens: 42,
  cacheWrite1hTokens: 0
})
assertArrayEqual(rowTexts(usageRecordCostTokenRows(openAICacheWriteTokenOnlyRecord)), [
  '缓存写入 Tokens 42'
], '没有成本拆解但有缓存写入 Token 时仍应生成成本明细 Token 行')
assertTrue(!usageRecordHasCostDetails(openAICacheWriteTokenOnlyRecord), '只有 Token、没有计价事实时不应展示成本明细浮层')

const mappedCostRecord = usageRecordFixture({
  model: 'gpt-5.5',
  upstreamModel: 'gpt-5.6-terra',
  pricingModel: 'gpt-5.6-terra',
  modelMappingApplied: true,
  modelMappingSource: 'account',
  sourceEndpointFamily: 'responses',
  upstreamEndpointFamily: 'responses',
  requestedServiceTier: 'flex',
  effectiveServiceTier: 'priority',
  reportedServiceTier: 'priority',
  billedServiceTier: 'priority',
  costBreakdown: {
    accountChargeUsd: 0.25,
    multiplier: 1,
    serviceTierPricingSource: 'multiplier',
    serviceTierMultiplier: 2
  }
})
assertArrayEqual(rowTexts(usageRecordCostMetadataRows(mappedCostRecord)), [
  '计价模型 gpt-5.6-terra',
  '实际服务档位 Priority',
  '计价来源 基础价 2x'
], '成本明细只展示计价模型、实际档位和计价来源')
assertTrue(usageRecordHasCostDetails(mappedCostRecord), '存在锁定计价事实时应允许查看成本明细')
assertArrayEqual(rowTexts(usageRecordCostMetadataRows(usageRecordFixture({
  pricingModel: 'gpt-5.6-sol',
  billedServiceTier: 'default'
}))), ['计价模型 gpt-5.6-sol'], 'Default 档位不应占用成本明细空间')

const displayFactsRecord = usageRecordFixture({
  billedServiceTier: 'flex',
  effectiveReasoningEffort: 'xhigh',
  firstTokenMs: 320,
  durationMs: 1250
})
assertEqual(usageRecordServiceTierText(displayFactsRecord) ?? '', 'Flex', '模型列应展示实际计费档位')
assertEqual(usageRecordReasoningEffortText(displayFactsRecord) ?? '', '超高', '模型列应展示有效思考级别')
assertArrayEqual(usageRecordLatencyParts(displayFactsRecord), ['首 token 0.32s', '总耗时 1.3s'], '延迟列必须合并首 token 与总耗时')

console.log('使用记录 Token 与成本明细 formatter 回归通过：列表三项展示和供应商成本明细均符合预期')

function usageRecordFixture(overrides: Partial<UsageRecordSummary>): UsageRecordSummary {
  return {
    id: 'usage_record_token_formatter_regression',
    traceId: 'trace_usage_record_token_formatter_regression',
    trafficSource: 'gateway',
    systemAccountId: 'system_account_usage_record_token_formatter',
    groupId: 'group_usage_record_token_formatter',
    endpoint: '/v1/chat/completions',
    stream: false,
    success: true,
    createdAt: '2026-07-01T00:00:00.000Z',
    ...overrides
  }
}

function assertEqual(actual: string, expected: string, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}：实际 ${actual}，期望 ${expected}`)
  }
}

function assertArrayEqual(actual: string[], expected: string[], message: string): void {
  if (actual.length !== expected.length || actual.some((value, index) => value !== expected[index])) {
    throw new Error(`${message}：实际 ${JSON.stringify(actual)}，期望 ${JSON.stringify(expected)}`)
  }
}

function assertArrayIncludes(actual: string[], expectedItems: string[], message: string): void {
  const missing = expectedItems.filter((item) => !actual.includes(item))
  if (missing.length) {
    throw new Error(`${message}：缺少 ${JSON.stringify(missing)}，实际 ${JSON.stringify(actual)}`)
  }
}

function assertTrue(actual: boolean, message: string): void {
  if (!actual) {
    throw new Error(`${message}：实际 false，期望 true`)
  }
}

function rowTexts(rows: Array<{ label: string; value: string }>): string[] {
  return rows.map((row) => `${row.label} ${row.value}`)
}
