import type { AccountStatus, AccountSummary } from '@/types/domain'
import { accountStatusColor, accountStatusText, accountStatusTooltipLines } from '../../views/accounts/accountFormatters'
import type { AccountFilters } from '../../views/accounts/accountFormTypes'
import { filterAccounts } from '../../views/accounts/accountListFilters'

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
assertStatus('运行态事前确认', accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'runtime_precheck_pending',
    label: '待探针确认',
    color: 'blue',
    blockerScope: 'runtime',
    reason: 'mock precheck pending'
  },
  runtimeAvailability: {
    status: 'precheck_pending',
    reason: 'mock precheck pending',
    since: '2026-06-16T00:00:00.000Z',
    until: '2026-06-16T00:00:10.000Z',
    failureCount: 6,
    distinctClientIpCount: 2,
    distinctApiKeyCount: 3,
    precheckAttemptCount: 1
  }
}), '待探针确认', 'blue')
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
assertStatus('账户时段外', accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'instance_schedule_inactive',
    label: '账户时段外',
    color: 'gold',
    blockerScope: 'account',
    reason: '账户当前不在允许使用时段，恢复前不会参与调度'
  },
  availabilityScheduleActive: false
}), '账户时段外', 'gold')
assertStatus('来源时段外', accountFixture({
  accessType: 'authorized',
  authorizationInstanceSourceAccountId: 'source_schedule_inactive_account',
  authorizationInstanceSourceAccountStatus: 'active',
  authorizationInstanceSourceAccountScheduleActive: false,
  effectiveAvailability: {
    available: false,
    status: 'source_schedule_inactive',
    label: '来源时段外',
    color: 'gold',
    blockerScope: 'source_account',
    reason: '授权方原账户当前不在允许使用时段，当前账户不能调用'
  }
}), '来源时段外', 'gold')
const permissionDeniedAccount = accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'permission_denied',
    label: '无可用权限',
    color: 'red',
    blockerScope: 'permission',
    reason: '当前账户无可用权限'
  }
})
assertStatus('无可用权限', permissionDeniedAccount, '无可用权限', 'red')
assertEqual(
  filterAccounts({ accounts: [permissionDeniedAccount], filters: accountFilters(['disabled']), isManagementView: false }).length,
  1,
  '无可用权限账户应能被停用类状态筛选命中'
)
assertEqual(
  filterAccounts({ accounts: [permissionDeniedAccount], filters: accountFilters(['active']), isManagementView: false }).length,
  0,
  '无可用权限账户不应被正常状态筛选命中'
)

const frequentFailureTooltip = accountStatusTooltipLines(accountFixture({
  qualityRecentRequestCount: 6,
  qualityRecentErrorCount: 5,
  qualityRecentSuccessRate: 1 / 6,
  qualityLastErrorMessage: 'mock upstream 504 failure for formatter regression'
}))
assertTrue(frequentFailureTooltip.some((line) => line.includes('持久状态仍为正常')), '质量反馈 tooltip 应说明不参与持久状态筛选')
assertTrue(frequentFailureTooltip.some((line) => line.includes('mock upstream 504')), '质量反馈 tooltip 应展示最后失败原因')

const precheckTooltip = accountStatusTooltipLines(accountFixture({
  runtimeAvailability: {
    status: 'precheck_pending',
    reason: 'mock precheck pending',
    failureCount: 6,
    distinctClientIpCount: 2,
    distinctApiKeyCount: 3,
    precheckAttemptCount: 1
  }
}))
assertTrue(precheckTooltip.some((line) => line.includes('运行态状态：待探针确认')), '事前确认 tooltip 应展示运行态状态')
assertTrue(precheckTooltip.some((line) => line.includes('数据库状态仍为正常')), '事前确认 tooltip 应说明数据库状态未被写死')
assertTrue(precheckTooltip.some((line) => line.includes('短窗口失败：6 次')), '事前确认 tooltip 应展示短窗口失败次数')

const scheduleInactiveTooltip = accountStatusTooltipLines(accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'instance_schedule_inactive',
    label: '账户时段外',
    color: 'gold',
    blockerScope: 'account',
    reason: '账户当前不在允许使用时段，恢复前不会参与调度'
  },
  availabilityScheduleActive: false
}))
assertTrue(scheduleInactiveTooltip.some((line) => line.includes('实际状态：账户时段外')), '账户时段外 tooltip 应展示实际状态')
assertTrue(scheduleInactiveTooltip.some((line) => line.includes('账户当前不在允许使用时段')), '账户时段外 tooltip 应展示原因')

const sourceScheduleInactiveTooltip = accountStatusTooltipLines(accountFixture({
  accessType: 'authorized',
  authorizationInstanceSourceAccountId: 'source_schedule_inactive_account',
  authorizationInstanceSourceAccountStatus: 'active',
  authorizationInstanceSourceAccountScheduleActive: false,
  effectiveAvailability: {
    available: false,
    status: 'source_schedule_inactive',
    label: '来源时段外',
    color: 'gold',
    blockerScope: 'source_account',
    reason: '授权方原账户当前不在允许使用时段，当前账户不能调用'
  }
}))
assertTrue(sourceScheduleInactiveTooltip.some((line) => line.includes('授权方原账户当前不在允许使用时段')), '来源时段外 tooltip 应展示授权来源原因')

console.log('账户状态 formatter 回归通过：正常、近期失败、近期不稳、频繁失败、运行态短暂避让、运行态事前确认、持久临时不可调用、账户时段外、来源时段外、无可用权限均可显示和筛选')

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

function accountFilters(status: AccountStatus[]): AccountFilters {
  return {
    keyword: '',
    providerCode: 'all',
    type: 'all',
    groupId: '',
    tagIds: [],
    status,
    systemAccountId: 'all'
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
