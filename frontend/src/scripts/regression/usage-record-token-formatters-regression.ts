import type { UsageRecordSummary } from '@/types/domain'
import {
  usageRecordCostAmountRows,
  usageRecordCostDetailTitle,
  usageRecordCostPriceRows,
  usageRecordCostTokenRows
} from '../../views/usage-records/usageRecordCostDetails'
import {
  formatRecordTokens,
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
  outputImageTokens: 2
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
  outputImageTokens: 0
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
  costBreakdown: {
    inputCostUsd: 0.003,
    outputCostUsd: 0.00375,
    cacheReadCostUsd: 0.0000375,
    cacheWriteCostUsd: 0.0001125,
    cacheWrite1hCostUsd: 0.00018,
    inputUsdPer1M: 3,
    outputUsdPer1M: 15,
    cacheReadUsdPer1M: 0.3,
    cacheWriteUsdPer1M: 3.75,
    cacheWrite1hUsdPer1M: 6,
    thinkingTokens: 40,
    accountChargeUsd: 0.00708,
    multiplier: 1
  }
})
assertEqual(usageRecordCostDetailTitle(anthropicCostRecord), '成本明细（Anthropic口径）', '成本明细标题应按供应商口径展示')
assertArrayEqual(rowTexts(usageRecordCostTokenRows(anthropicCostRecord)), [
  '5m 缓存写入 Tokens 30',
  '1h 缓存写入 Tokens 30',
  '思考 Tokens 40',
  '图片输入 Tokens 7',
  '图片输出 Tokens 2'
], 'Anthropic 成本明细应拆出 5m、1h、思考和图片 Token')
assertArrayIncludes(rowTexts(usageRecordCostAmountRows(anthropicCostRecord)), [
  '5m 缓存写入成本 $0.000112',
  '1h 缓存写入成本 $0.000180',
  '合计成本 $0.007080'
], 'Anthropic 成本明细应展示缓存写入成本和合计成本')
assertArrayIncludes(rowTexts(usageRecordCostPriceRows(anthropicCostRecord)), [
  '5m 缓存写入单价 $3.7500 / 1M Token',
  '1h 缓存写入单价 $6.0000 / 1M Token'
], 'Anthropic 成本明细应展示缓存写入单价')

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
    multiplier: 1
  }
})
assertArrayEqual(rowTexts(usageRecordCostTokenRows(openAICostRecord)), [], 'OpenAI 普通文本记录没有扩展 Token 时不应展示额外 Token 明细')
assertArrayEqual(rowTexts(usageRecordCostPriceRows(openAICostRecord)), [
  '输入单价 $1.0000 / 1M Token',
  '输出单价 $10.0000 / 1M Token',
  '缓存读单价 $0.1000 / 1M Token'
], 'OpenAI 普通文本记录只展示输入、输出和缓存读单价')

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

function rowTexts(rows: Array<{ label: string; value: string }>): string[] {
  return rows.map((row) => `${row.label} ${row.value}`)
}
