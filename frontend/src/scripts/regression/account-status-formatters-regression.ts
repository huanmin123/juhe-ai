import type { AccountEffectiveAvailabilityStatus, AccountStatus, AccountSummary, ApiKeySummary } from '@/types/domain'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { accountStatusColor, accountStatusText, accountStatusTooltipLines } from '../../views/accounts/accountFormatters'
import type { AccountFilters } from '../../views/accounts/accountFormTypes'
import { filterAccounts } from '../../views/accounts/accountListFilters'
import { accountMenuItems, canToggleAccountStatus } from '../../views/accounts/accountRules'
import { apiKeyStatusTagColor, apiKeyStatusTagLabel, apiKeyStatusTooltipLines } from '../../views/api-keys/apiKeyFormatters'

const accountStatusValues: AccountStatus[] = ['active', 'pending_test', 'disabled', 'error', 'rate_limited', 'temporary_unavailable']

assertStatus('可调度账户', accountFixture(), '可调度', 'green')
assertStatus('待检查账户', accountFixture({
  status: 'pending_test',
  effectiveAvailability: {
    available: false,
    status: 'instance_pending_test',
    label: '账户待检查',
    color: 'blue',
    blockerScope: 'account',
    reason: '账户正在等待后台健康检查，检查通过前不会参与调度'
  }
}), '待检查', 'blue')
const pendingHealthCheckFailedAccount = accountFixture({
  status: 'pending_test',
  lastHealthCheckAt: '2026-07-11T01:00:00.000Z',
  nextHealthCheckAt: '2099-07-11T01:15:00.000Z',
  healthCheckFailureCount: 1,
  lastHealthCheckStatusCode: 401,
  lastHealthCheckErrorCode: 'invalid_api_key',
  lastHealthCheckErrorMessage: 'Invalid API key',
  effectiveAvailability: {
    available: false,
    status: 'instance_pending_test',
    label: '账户检查失败',
    color: 'red',
    blockerScope: 'account',
    reason: '后台健康检查未通过，系统将自动重试'
  }
})
assertStatus('待检查账户后台检查失败', pendingHealthCheckFailedAccount, '检查失败', 'red')
assertTrue(
  accountStatusTooltipLines(pendingHealthCheckFailedAccount).some((line) => line.includes('每 1 小时自动重试')),
  '待检查账户失败 tooltip 应说明固定每 1 小时自动重试'
)
assertTrue(
  accountStatusTooltipLines(pendingHealthCheckFailedAccount).some((line) => line.includes('持续 24 小时')),
  '待检查账户失败 tooltip 应说明 24 小时异常收敛窗口'
)
assertTrue(
  accountStatusTooltipLines(pendingHealthCheckFailedAccount).some((line) => line.includes('Invalid API key')),
  '待检查账户失败 tooltip 应显示后台检查原因'
)
assertTrue(
  accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'recheck-health' && item.label === '重新检查'),
  '检查失败的自有待检查账户应显示重新检查'
)
assertTrue(
  accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'restore-normal' && item.label === '恢复正常'),
  '自有待检查账户应复用统一的恢复正常操作'
)
assertTrue(
  !accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'force-activate'),
  '自有待检查账户不应额外暴露独立的人工恢复操作'
)
const accountMenuActionsSource = readFileSync(resolve('../frontend/src/views/accounts/useAccountMenuActions.ts'), 'utf8')
assertTrue(
  /if \(key === 'restore-normal'\)[\s\S]+account\.status === 'pending_test'[\s\S]+api\.(?:accounts|myAccounts)\.forceActivate/.test(accountMenuActionsSource),
  '待检查账户应在统一恢复正常处理分支内复用原子放行能力'
)
assertTrue(
  !/if \(key === 'force-activate'\)/.test(accountMenuActionsSource),
  '账户菜单处理器不应保留独立的人工恢复分支'
)
assertTrue(
  /updateLoadedAccount: \(account: AccountSummary\) => boolean/.test(accountMenuActionsSource),
  '账户菜单操作应注入当前行更新入口'
)
assertTrue(
  /const updated = options\.isManagementView\.value[\s\S]+options\.updateLoadedAccount\(updated\)/.test(accountMenuActionsSource),
  '账户状态与调度标记操作应直接回写 API 返回的当前行'
)
assertTrue(
  accountStatusTooltipLines(accountFixture({
    status: 'pending_test',
    lastHealthCheckAt: '2026-07-15T01:00:00.000Z',
    lastHealthCheckTraceId: 'trace-health-check-regression'
  })).some((line) => line.includes('健康检查 traceId：trace-health-check-regression')),
  '账户状态提示应直接展示结构化健康检查 traceId'
)
assertTrue(
  accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'toggle-status' && item.label === '停用账户'),
  '检查失败的自有待检查账户应允许停用'
)
const activeHealthTimeline = accountStatusTooltipLines(accountFixture({
  lastHealthCheckAt: '2026-07-11T01:00:00.000Z',
  lastHealthSuccessAt: '2026-07-11T01:05:00.000Z',
  nextHealthCheckAt: '2099-07-11T02:00:00.000Z'
}))
assertTrue(activeHealthTimeline.some((line) => line.includes('最近主动健康检查')), '正常账户应区分主动健康检查时间')
assertTrue(activeHealthTimeline.some((line) => line.includes('最近健康成功信号')), '正常账户应保留健康成功信号文案')
assertTrue(activeHealthTimeline.some((line) => line.includes('下次健康复核')), '正常账户应显示下次健康复核')

const coolingHealthTimeline = accountStatusTooltipLines(accountFixture({
  status: 'temporary_unavailable',
  lastHealthCheckAt: '2026-07-11T01:00:00.000Z',
  lastHealthSuccessAt: '2026-07-11T01:05:00.000Z',
  nextHealthCheckAt: '2020-07-11T02:00:00.000Z',
  cooldownUntil: '2099-07-11T03:00:00.000Z'
}))
assertTrue(coolingHealthTimeline.some((line) => line.includes('下次冷却复测')), '冷却账户应显示冷却复测时间')
assertTrue(!coolingHealthTimeline.some((line) => line.includes('下次健康复核')), '冷却账户不得混入周期健康复核计划')
assertTrue(!coolingHealthTimeline.some((line) => line.includes('等待复核')), '冷却账户的过期健康计划不得显示为等待复核')

for (const status of ['disabled', 'error'] as const) {
  const terminalTimeline = accountStatusTooltipLines(accountFixture({
    status,
    nextHealthCheckAt: '2020-07-11T02:00:00.000Z'
  }))
  assertTrue(!terminalTimeline.some((line) => line.includes('下次健康复核')), `${status} 账户不得显示下次健康复核`)
  assertTrue(!terminalTimeline.some((line) => line.includes('等待复核')), `${status} 账户不得显示过期计划等待复核`)
}
assertStatus('停用账户', accountFixture({
  status: 'disabled',
  effectiveAvailability: {
    available: false,
    status: 'instance_disabled',
    label: '账户停用',
    color: 'default',
    blockerScope: 'account',
    reason: '账户已停用，当前不可用'
  }
}), '停用', 'default')
const errorAccount = accountFixture({
  status: 'error',
  lastErrorCode: 'upstream_failure',
  lastErrorMessage: 'mock account error for formatter regression',
  effectiveAvailability: {
    available: false,
    status: 'instance_error',
    label: '账户异常',
    color: 'red',
    blockerScope: 'account',
    reason: 'mock account error for formatter regression'
  }
})
assertStatus('异常账户', errorAccount, '异常', 'red')
assertTrue(
  accountMenuItems(errorAccount).some((item) => item.key === 'restore-normal' && item.label === '异常恢复'),
  '异常账户操作名称应为异常恢复'
)
assertTrue(
  accountMenuItems(errorAccount).some((item) => item.key === 'toggle-status' && item.label === '停用账户'),
  '自有异常账户应允许停用'
)
assertTrue(canToggleAccountStatus(errorAccount), '自有异常账户应满足状态停用资格')
assertStatus('限流账户', accountFixture({
  status: 'rate_limited',
  cooldownUntil: '2099-01-01T00:00:00.000Z',
  effectiveAvailability: {
    available: false,
    status: 'instance_rate_limited',
    label: '账户限流中',
    color: 'orange',
    blockerScope: 'account',
    reason: '账户限流中，恢复前不会参与调度',
    retryAt: '2099-01-01T00:00:00.000Z'
  }
}), '限流中', 'orange')
assertStatus('冷却账户', accountFixture({
  cooldownUntil: '2099-01-01T00:00:00.000Z',
  effectiveAvailability: {
    available: false,
    status: 'instance_cooldown',
    label: '账户冷却',
    color: 'gold',
    blockerScope: 'account',
    reason: '账户正在冷却，恢复前不会参与调度',
    retryAt: '2099-01-01T00:00:00.000Z'
  }
}), '冷却中', 'gold')
assertStatus('停调账户', accountFixture({
  schedulable: false,
  effectiveAvailability: {
    available: false,
    status: 'instance_unschedulable',
    label: '账户停调',
    color: 'orange',
    blockerScope: 'account',
    reason: '账户暂时不可调用，恢复前不会参与调度'
  }
}), '停调', 'orange')
assertStatus('近窗口少量失败', accountFixture({
  qualityRecentRequestCount: 3,
  qualityRecentErrorCount: 2
}), '可调度', 'green')
assertStatus('近窗口不稳定', accountFixture({
  qualityRecentRequestCount: 4,
  qualityRecentErrorCount: 3,
  qualityRecentSuccessRate: 0.25
}), '可调度', 'green')
assertStatus('近窗口频繁失败', accountFixture({
  qualityRecentRequestCount: 6,
  qualityRecentErrorCount: 5,
  qualityRecentSuccessRate: 1 / 6,
  qualityLastErrorAt: '2026-06-13T00:00:00.000Z',
  qualityLastErrorMessage: 'mock upstream 504 failure for formatter regression',
  qualityUpdatedAt: '2026-06-13T00:00:05.000Z'
}), '可调度', 'green')
assertStatus('运行态快照未到达时保留静态可调度状态', accountFixture({
  accountRuntimeAvailabilityAvailable: false
}), '可调度', 'green')
assertTrue(
  !/运行态未知/.test(readFileSync(resolve('../frontend/src/views/accounts/accountFormatters.ts'), 'utf8')),
  '前端不应再暴露“运行态未知”这个短暂中间态'
)
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
const runtimeDegradedAccount = accountFixture({
  effectiveAvailability: {
    available: true,
    status: 'runtime_degraded',
    label: '调度降级',
    color: 'gold',
    blockerScope: 'runtime',
    reason: 'mock runtime degraded'
  },
  runtimeAvailability: {
    status: 'degraded',
    reason: 'mock runtime degraded',
    failureCount: 2
  }
})
assertStatus('运行态调度降级', runtimeDegradedAccount, '调度降级', 'gold')
assertEqual(
  filterAccounts({ accounts: [runtimeDegradedAccount], filters: accountFilters(['active']), isManagementView: false }).length,
  1,
  '运行态调度降级仍应归入正常状态筛选'
)
assertTrue(
  accountStatusTooltipLines(runtimeDegradedAccount).some((line) => line.includes('普通候选不足时才会兜底尝试')),
  '运行态调度降级 tooltip 应说明只作为兜底候选'
)
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
assertStatus('运行态半开探测', accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'runtime_half_open',
    label: '半开探测',
    color: 'blue',
    blockerScope: 'runtime',
    reason: 'mock half open'
  },
  runtimeAvailability: {
    status: 'half_open',
    reason: 'mock half open',
    until: '2099-01-01T00:00:00.000Z'
  }
}), '半开探测', 'blue')
assertStatus('运行态探针确认失败', accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'runtime_precheck_failed',
    label: '探针确认失败',
    color: 'gold',
    blockerScope: 'runtime',
    reason: 'mock precheck failed'
  },
  runtimeAvailability: {
    status: 'precheck_failed',
    reason: 'mock precheck failed',
    failureCount: 5
  }
}), '探针确认失败', 'gold')
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
const longTermUnavailableAccount = accountFixture({
  status: 'temporary_unavailable',
  lastErrorCode: 'cooldown_retest_long_term_unavailable',
  lastErrorMessage: '后台冷却复测连续失败，进入长期不可用每 1 小时复测',
  cooldownUntil: '2099-01-01T00:00:00.000Z',
  cooldownRetestFailureCount: 8,
  cooldownRetestObservationStartedAt: '2026-06-16T00:00:00.000Z',
  effectiveAvailability: {
    available: false,
    status: 'instance_temporary_unavailable',
    label: '账户临时不可调用',
    color: 'gold',
    blockerScope: 'account',
    reason: '进入长期不可用每 1 小时复测'
  }
})
assertStatus('长期不可用', longTermUnavailableAccount, '长期不可用', 'gold')
assertTrue(
  accountStatusTooltipLines(longTermUnavailableAccount).some((line) => line.includes('每 1 小时复测')),
  '长期不可用 tooltip 应说明后台每 1 小时复测'
)
assertTrue(
  accountStatusTooltipLines(longTermUnavailableAccount).some((line) => line.includes('满 7 天')),
  '长期不可用 tooltip 应说明 7 天异常收敛窗口'
)
assertEqual(
  filterAccounts({ accounts: [longTermUnavailableAccount], filters: accountFilters(['temporary_unavailable']), isManagementView: false }).length,
  1,
  '长期不可用仍应归入临时不可调用筛选'
)
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
assertStatus('授权额度耗尽', accountFixture({
  accessType: 'authorized',
  authorizationQuotaExceeded: true,
  effectiveAvailability: {
    available: false,
    status: 'authorization_quota_exceeded',
    label: '授权额度已用完',
    color: 'red',
    blockerScope: 'authorization',
    reason: '授权额度已用完，当前账户不能调用'
  }
}), '授权额度已用完', 'red')
assertStatus('授权未绑定分组', accountFixture({
  accessType: 'authorized',
  boundGroupId: undefined,
  effectiveAvailability: {
    available: false,
    status: 'binding_missing',
    label: '未绑定分组',
    color: 'red',
    blockerScope: 'binding',
    reason: '授权账户需要先绑定到你的分组'
  }
}), '未绑定分组', 'red')
assertStatus('账户 Key 池全部不可用', accountFixture({
  effectiveAvailability: {
    available: false,
    status: 'api_key_pool_unavailable',
    label: 'Key 全部不可用',
    color: 'red',
    blockerScope: 'api_key_pool',
    reason: '账户内 2 个 API Key 均不可用，后台探测恢复前不会参与调度'
  }
}), 'Key 全部不可用', 'red')
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
assertTrue(frequentFailureTooltip.some((line) => line.includes('账户状态：数据库仍为正常')), '质量反馈 tooltip 应说明不参与持久状态筛选')
assertTrue(frequentFailureTooltip.some((line) => line.includes('仅统计真实上游失败和账号依赖失败')), '质量反馈 tooltip 应说明账号质量归因范围')
assertTrue(frequentFailureTooltip.some((line) => line.includes('并发满') && line.includes('客户端断开')), '质量反馈 tooltip 应说明本地容量和客户端生命周期不计入账号质量')
assertTrue(frequentFailureTooltip.some((line) => line.includes('最后质量原因') && line.includes('mock upstream 504')), '质量反馈 tooltip 应展示最后质量失败原因')

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

const effectiveAvailabilityStatusFilterExpectations = {
  available: 'active',
  permission_denied: 'disabled',
  authorization_expired: 'disabled',
  authorization_paused: 'disabled',
  authorization_unavailable: 'disabled',
  authorization_quota_exceeded: 'rate_limited',
  source_deleted: 'disabled',
  source_expired: 'disabled',
  source_pending_test: 'pending_test',
  source_disabled: 'disabled',
  source_error: 'error',
  source_rate_limited: 'rate_limited',
  source_temporary_unavailable: 'temporary_unavailable',
  source_cooldown: 'temporary_unavailable',
  source_unschedulable: 'disabled',
  instance_expired: 'disabled',
  instance_pending_test: 'pending_test',
  instance_disabled: 'disabled',
  instance_error: 'error',
  instance_rate_limited: 'rate_limited',
  instance_temporary_unavailable: 'temporary_unavailable',
  instance_cooldown: 'temporary_unavailable',
  instance_unschedulable: 'disabled',
  binding_missing: 'disabled',
  api_key_pool_unavailable: 'temporary_unavailable',
  runtime_degraded: 'active',
  runtime_local_suppressed: 'temporary_unavailable',
  runtime_half_open: 'temporary_unavailable',
  runtime_precheck_pending: 'temporary_unavailable',
  runtime_precheck_failed: 'temporary_unavailable'
} satisfies Record<AccountEffectiveAvailabilityStatus, AccountStatus>
for (const [effectiveStatus, expectedStatus] of Object.entries(effectiveAvailabilityStatusFilterExpectations) as Array<[AccountEffectiveAvailabilityStatus, AccountStatus]>) {
  assertEffectiveAvailabilityFilter(effectiveStatus, expectedStatus)
}

const apiKeyScheduleInactive = apiKeyFixture({
  status: 'disabled'
})
assertEqual(apiKeyStatusTagLabel(apiKeyScheduleInactive), '停用', 'API Key 停用状态标签应显示停用')
assertEqual(apiKeyStatusTagColor(apiKeyScheduleInactive), 'default', 'API Key 停用状态颜色应使用停用颜色')
assertTrue(apiKeyStatusTooltipLines(apiKeyScheduleInactive).some((line) => line.includes('计划边界会自动更新当前运行状态')), 'API Key 配置时间计划时应在状态 tooltip 展示单状态提示')

console.log('账户状态 formatter 回归通过：正常、待检查、停用、异常、限流、冷却、停调、近期失败、近期不稳、频繁失败、质量归因说明、运行态调度降级、运行态短暂避让、运行态事前确认、运行态半开探测、运行态探针确认失败、授权额度、授权绑定、Key 池不可用、派生可用性筛选映射、持久临时不可调用、长期不可用、时间计划提示、无可用权限均可显示和筛选')

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
      label: '可调度',
      color: 'green'
    },
    todayUsage: emptyUsage(),
    usage: emptyUsage(),
    ...overrides
  }
}

function apiKeyFixture(overrides: Partial<ApiKeySummary> = {}): ApiKeySummary {
  return {
    id: 'api_key_status_formatter_regression',
    name: 'API Key 状态 formatter 回归',
    keyPrefix: 'sk-test',
    keySuffix: 'suffix',
    key: '',
    status: 'active',
    routeStrategyId: 'route_strategy_status_formatter_regression',
    routeStrategyName: '状态 formatter 策略路由',
    routeStrategyMode: 'normal',
    routeStrategyStatus: 'active',
    quotaLimits: {},
    availabilitySchedule: {
      enabled: true,
      timezone: 'Asia/Shanghai',
      mode: 'allow_windows',
      windows: [
        { daysOfWeek: [1, 2, 3, 4, 5, 6, 7], start: '22:00', end: '23:55' }
      ]
    },
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

function assertEffectiveAvailabilityFilter(effectiveStatus: AccountEffectiveAvailabilityStatus, expectedStatus: AccountStatus): void {
  const account = accountFixture({
    effectiveAvailability: {
      available: effectiveStatus === 'available' || effectiveStatus === 'runtime_degraded',
      status: effectiveStatus,
      label: effectiveStatus,
      color: 'default'
    }
  })
  for (const status of accountStatusValues) {
    const matched = filterAccounts({ accounts: [account], filters: accountFilters([status]), isManagementView: false }).length
    assertEqual(
      matched,
      status === expectedStatus ? 1 : 0,
      `派生状态 ${effectiveStatus} 应只命中 ${expectedStatus} 筛选，不应命中 ${status}`
    )
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
