import type { AccountSummary, AccountUsageSummary } from '@/types/domain'
import { formatRequestCountTag } from '@/shared/formatters'
import {
  formatAccountUsageSummary,
  formatCost,
  formatRelativeReset,
  formatUsageAmount,
  oauthUsageBars
} from '../../views/accounts/accountUsageFormatters'

const usage: AccountUsageSummary = {
  requestCount: 1200,
  inputTokens: 3200,
  outputTokens: 1300,
  cacheReadTokens: 500,
  cacheReadCost: 0.11,
  cacheWriteTokens: 0,
  cacheWrite1hTokens: 0,
  cacheWriteCost: 0,
  thinkingTokens: 0,
  inputImageTokens: 0,
  outputImageTokens: 0,
  totalTokens: 4500,
  totalCost: 1.234
}
assertEqual(formatRequestCountTag(usage.requestCount), '1200req', '请求数标签不应使用千分位分隔')
assertEqual(formatAccountUsageSummary(usage), '1200req / 4.5K / $1.23', '账户用量摘要里的请求数不应使用千分位分隔')
assertEqual(formatUsageAmount(1_500_000), '1.5M', 'Token 数应按紧凑格式展示')
assertEqual(formatCost(0.126), '$0.13', '成本应保留两位小数')

const originalNow = Date.now
Date.now = () => Date.parse('2026-06-16T00:00:00.000Z')
try {
  assertEqual(formatRelativeReset('2026-06-16T01:30:00.000Z'), '1h 30m', '重置时间应展示小时分钟')
  assertEqual(formatRelativeReset('2026-06-18T01:00:00.000Z'), '2d 1h', '重置时间应展示天和小时')
  assertEqual(formatRelativeReset('2026-06-15T23:59:00.000Z'), '现在', '已到期重置时间应展示现在')
  assertEqual(formatRelativeReset('bad-date'), '时间格式异常', '非法服务端时间应展示格式异常')

  const bars = oauthUsageBars(accountFixture({
    type: 'oauth',
    oauthUsage: {
      kind: 'openai_codex',
      fiveHour: {
        utilization: 82.4,
        resetsAt: '2026-06-16T01:30:00.000Z',
        remainingSeconds: 5400
      },
      sevenDay: {
        utilization: 1005,
        resetsAt: '2026-06-18T01:00:00.000Z',
        remainingSeconds: 176400
      }
    }
  }))
  assertEqual(bars.length, 2, '支持 OAuth 管理的 OpenAI v1 账户应展示 5h/7d 两条用量条')
  assertEqual(bars[0]?.key, '5h', '第一条应为 5h 窗口')
  assertEqual(bars[0]?.percent, 82, '5h 百分比应四舍五入')
  assertEqual(bars[0]?.displayPercent, '82%', '5h 百分比文案应保持原格式')
  assertEqual(bars[0]?.tone, 'warning', '5h 超过 80% 应显示警告')
  assertEqual(bars[0]?.resetText, '1h 30m', '5h 重置文案应展示相对时间')
  assertEqual(bars[1]?.percent, 100, '7d 进度条应封顶到 100')
  assertEqual(bars[1]?.displayPercent, '>999%', '7d 超高占用应展示 >999%')
  assertEqual(bars[1]?.tone, 'danger', '7d 超过 100% 应显示危险状态')
  assertEqual(bars[1]?.resetText, '2d 1h', '7d 重置文案应展示天数')

  assertEqual(oauthUsageBars(accountFixture({ providerCode: 'openai', type: 'oauth' })).length, 0, '没有 OAuth 用量快照时不应展示 OAuth 用量条')
  assertEqual(oauthUsageBars(accountFixture({ type: 'api_key' })).length, 0, 'API Key 账户不应展示 OAuth 用量条')
  assertEqual(oauthUsageBars(accountFixture({ type: 'oauth', protocolVersion: 'v2' })).length, 0, '非 OpenAI v1 协议不应展示 OAuth 用量条')
} finally {
  Date.now = originalNow
}

console.log('账户用量 formatter 回归通过：摘要格式、OAuth 用量条、百分比封顶和重置时间均符合预期')

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'account_usage_formatter_regression',
    providerCode: 'gpt',
    protocolCode: 'openai',
    protocolVersion: 'v1',
    name: '用量 formatter 回归账户',
    type: 'oauth',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'codex_responses',
    schedulable: true,
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function emptyUsage(): AccountUsageSummary {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    cacheWriteTokens: 0,
    cacheWrite1hTokens: 0,
    cacheWriteCost: 0,
    thinkingTokens: 0,
    inputImageTokens: 0,
    outputImageTokens: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，实际 ${String(actual)}`)
  }
}
