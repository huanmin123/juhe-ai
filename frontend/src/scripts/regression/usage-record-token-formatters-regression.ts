import type { UsageRecordSummary } from '@/types/domain'
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
  '缓存读 125',
  '缓存写 60',
  '1h 30',
  '思考 40',
  '图片 9'
], '使用记录 Token 明细必须展示缓存写、1h、思考和图片 Token')
assertEqual(
  formatRecordTokens(richRecord),
  '输入 1,000 / 输出 250 / 缓存读 125 / 缓存写 60 / 1h 30 / 思考 40 / 图片 9',
  '移动端 Token 摘要必须复用完整明细'
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

console.log('使用记录 Token formatter 回归通过：完整 Token 维度和零值隐藏均符合预期')

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
