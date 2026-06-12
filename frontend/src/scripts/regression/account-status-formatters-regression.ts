import type { AccountSummary } from '@/types/domain'
import { accountStatusColor, accountStatusText, accountStatusTooltipLines } from '../../views/accounts/accountFormatters'

assertStatus('正常账户', accountFixture(), '正常', 'green')
assertStatus('近窗口少量失败', accountFixture({
  qualityRecentRequestCount: 3,
  qualityRecentErrorCount: 2
}), '近期失败', 'gold')
assertStatus('近窗口不稳定', accountFixture({
  qualityRecentRequestCount: 4,
  qualityRecentErrorCount: 3,
  qualityRecentSuccessRate: 0.25
}), '近期不稳', 'orange')
assertStatus('近窗口频繁失败', accountFixture({
  qualityRecentRequestCount: 6,
  qualityRecentErrorCount: 5,
  qualityRecentSuccessRate: 1 / 6,
  qualityLastErrorAt: '2026-06-13T00:00:00.000Z',
  qualityLastErrorMessage: 'mock upstream 504 failure for formatter regression',
  qualityUpdatedAt: '2026-06-13T00:00:05.000Z'
}), '频繁失败', 'red')
assertStatus('运行态短暂避让', accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'runtime_local_suppressed',
    label: '短暂避让',
    color: 'gold',
    blockerScope: 'runtime',
    reason: 'mock runtime suppression'
  },
  runtimeAvailability: {
    status: 'local_suppressed',
    reason: 'mock runtime suppression'
  }
}), '短暂避让', 'gold')
assertStatus('持久临时不可调用', accountFixture({
  status: 'temporary_unavailable',
  effectiveAvailability: {
    available: false,
    status: 'instance_temporary_unavailable',
    label: '账户临时不可调用',
    color: 'gold',
    blockerScope: 'account',
    reason: '慢速通道确认失败'
  }
}), '临时不可调用', 'gold')

const frequentFailureTooltip = accountStatusTooltipLines(accountFixture({
  qualityRecentRequestCount: 6,
  qualityRecentErrorCount: 5,
  qualityRecentSuccessRate: 1 / 6,
  qualityLastErrorMessage: 'mock upstream 504 failure for formatter regression'
}))
assertTrue(frequentFailureTooltip.some((line) => line.includes('持久状态仍为正常')), '质量反馈 tooltip 应说明不参与持久状态筛选')
assertTrue(frequentFailureTooltip.some((line) => line.includes('mock upstream 504')), '质量反馈 tooltip 应展示最后失败原因')

console.log('账户状态 formatter 回归通过：正常、近期失败、近期不稳、频繁失败、运行态短暂避让、持久临时不可调用均可显示')

function assertStatus(name: string, account: AccountSummary, text: string, color: string): void {
  assertEqual(accountStatusText(account), text, `${name} 文案应为 ${text}`)
  assertEqual(accountStatusColor(account), color, `${name} 颜色应为 ${color}`)
}

function accountFixture(overrides: Partial<AccountSummary> = {}): AccountSummary {
  return {
    id: 'account_status_formatter_regression',
    providerCode: 'gpt',
    name: '状态 formatter 回归账户',
    type: 'api_key',
    credentials: {},
    status: 'active',
    concurrencyLimit: 1,
    currentConcurrency: 0,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    clientCompatibility: 'openai_standard',
    schedulable: true,
    effectiveAvailability: {
      available: true,
      status: 'available',
      label: '正常',
      color: 'green'
    },
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function emptyUsage() {
  return {
    requestCount: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheReadCost: 0,
    totalTokens: 0,
    totalCost: 0
  }
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (actual !== expected) {
    throw new Error(`${message}，实际 ${String(actual)}`)
  }
}

function assertTrue(value: boolean, message: string): void {
  if (!value) {
    throw new Error(message)
  }
}
