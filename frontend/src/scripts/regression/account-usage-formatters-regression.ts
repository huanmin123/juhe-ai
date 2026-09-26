import type { AccountSummary, AccountUsageSummary } from '@/types/domain'
import { formatRequestCountTag } from '@/shared/formatters'
import {
  formatAccountUsageSummary,
  formatCost,
  formatGrokPeriodReset,
  formatRelativeReset,
  formatUsageAmount,
  grokOAuthUsageBar,
  grokPeriodLabel,
  grokProductUsageSummary,
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

  const grokBar = grokOAuthUsageBar(accountFixture({
    providerCode: 'xai',
    type: 'oauth',
    oauthUsage: {
      kind: 'xai_grok',
      usedPercent: 14,
      periodType: 'USAGE_PERIOD_TYPE_WEEKLY',
      periodStart: '2026-06-11T01:00:00.000Z',
      periodEnd: '2026-06-18T01:00:00.000Z',
      subscriptionTier: 'SuperGrok Heavy',
      productUsage: JSON.stringify([
        { product: 'GrokBuild', usagePercent: 13 },
        { product: 'GrokChat', usagePercent: 1 },
        { product: 'GrokImagine' }
      ])
    }
  }))
  assertTrue(Boolean(grokBar), 'Grok OAuth 快照应产生用量条')
  assertEqual(grokBar?.key, 'grok', 'Grok 用量条 key 应为 grok')
  assertEqual(grokBar?.label, '周', 'WEEKLY 周期徽章应为周')
  assertEqual(grokBar?.percent, 14, 'Grok 百分比应四舍五入')
  assertEqual(grokBar?.displayPercent, '14%', 'Grok 百分比文案应保持原格式')
  assertEqual(grokBar?.tone, 'normal', 'Grok 未超阈值应为 normal')
  assertTrue(Boolean(grokBar?.resetText && grokBar.resetText !== '—'), 'Grok 应展示相对重置时间')
  assertTrue(Boolean(grokBar?.tooltip?.startsWith('SuperGrok Heavy · 本周已用 14% · ')), 'Grok tooltip 应以套餐与规范化的本周用量开头')
  assertTrue(Boolean(grokBar?.tooltip?.includes(' 重置')), 'Grok tooltip 应含本地化重置时间')
  assertEqual(grokBar?.tooltip?.endsWith('GrokBuild 13% / GrokChat 1% / GrokImagine'), true, '分产品明细应展示产品名与百分比')

  const grokMonthlyBar = grokOAuthUsageBar(accountFixture({
    type: 'oauth',
    oauthUsage: {
      kind: 'xai_grok',
      usedPercent: 7.4,
      periodType: 'USAGE_PERIOD_TYPE_MONTHLY',
      periodEnd: '2026-07-01T00:00:00.000Z'
    }
  }))
  assertTrue(Boolean(grokMonthlyBar), '无套餐名时 Grok 条仍应渲染')
  assertEqual(grokMonthlyBar?.label, '月', 'Grok 周期类型 MONTHLY 徽章应为月')
  assertEqual(grokMonthlyBar?.displayPercent, '7%', 'Grok 百分比应四舍五入')
  assertTrue(Boolean(grokMonthlyBar?.tooltip?.includes('本月已用 7%')), 'MONTHLY tooltip 应规范化为本月')
  assertEqual(grokMonthlyBar?.tooltip?.includes('GrokBuild'), false, '没有分产品明细时 tooltip 不应含产品段')

  assertEqual(
    grokOAuthUsageBar(accountFixture({
      type: 'oauth',
      oauthUsage: { kind: 'xai_grok', usedPercent: 3, periodType: 'USAGE_PERIOD_TYPE_DAILY' }
    }))?.label,
    '期',
    '未知周期类型徽章应回退期'
  )
  assertTrue(
    Boolean(grokOAuthUsageBar(accountFixture({ type: 'oauth', oauthUsage: { kind: 'xai_grok', usedPercent: 3 } }))?.tooltip?.includes('本周期已用 3%')),
    '缺失周期类型应回退本周期文案'
  )
  assertEqual(grokOAuthUsageBar(accountFixture({ type: 'oauth', oauthUsage: { kind: 'xai_grok' } })), undefined, '快照没有已用百分比时不应渲染 Grok 条')
  assertEqual(grokOAuthUsageBar(accountFixture({ type: 'api_key', oauthUsage: { kind: 'xai_grok', usedPercent: 3 } })), undefined, 'API Key 账户不应渲染 Grok 条')
  assertEqual(grokOAuthUsageBar(accountFixture({ providerCode: 'xai', type: 'oauth', oauthUsage: { kind: 'openai_codex' } })), undefined, 'openai_codex 快照不应渲染 Grok 条')

  const grokBarsViaAggregation = oauthUsageBars(accountFixture({
    providerCode: 'xai',
    type: 'oauth',
    oauthUsage: { kind: 'xai_grok', usedPercent: 14, periodType: 'USAGE_PERIOD_TYPE_WEEKLY' }
  }))
  assertEqual(grokBarsViaAggregation.length, 1, 'xai oauth 账户应经聚合入口产出 Grok 条')
  assertEqual(grokBarsViaAggregation[0]?.key, 'grok', '聚合入口应返回 Grok 条')

  assertEqual(grokPeriodLabel('USAGE_PERIOD_TYPE_WEEKLY'), '本周', '周期类型 WEEKLY 应规范化为本周')
  assertEqual(grokPeriodLabel('USAGE_PERIOD_TYPE_MONTHLY'), '本月', '周期类型 MONTHLY 应规范化为本月')
  assertEqual(grokPeriodLabel('USAGE_PERIOD_TYPE_DAILY'), 'USAGE_PERIOD_TYPE_DAILY', '其他周期类型应显示原文')
  assertEqual(grokPeriodLabel(undefined), '本周期', '缺失周期类型应回退本周期')
  assertEqual(grokProductUsageSummary('not-json'), undefined, '非法分产品 JSON 不应产生明细')
  assertEqual(grokProductUsageSummary('{"product":"GrokBuild"}'), undefined, '非数组分产品 JSON 不应产生明细')
  assertEqual(grokProductUsageSummary(undefined), undefined, '缺失分产品 JSON 不应产生明细')
} finally {
  Date.now = originalNow
}

assertEqual(formatGrokPeriodReset('bad-date'), '时间格式异常', '非法重置时间应展示格式异常')

console.log('账户用量 formatter 回归通过：摘要格式、OAuth 用量条、Grok 用量行、百分比封顶和重置时间均符合预期')

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

function assertTrue(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message)
  }
}
