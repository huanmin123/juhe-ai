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
const pendingAccount = accountFixture({
  status: 'pending_test',
  effectiveAvailability: {
    available: false,
    status: 'instance_pending_test',
    label: '账户待检查',
    color: 'blue',
    blockerScope: 'account',
    reason: '账户正在等待后台健康检查，检查通过前不会参与调度'
  }
})
assertStatus('待检查账户', pendingAccount, '待检查', 'blue')
assertTrue(
  !accountMenuItems(pendingAccount).some((item) => item.key === 'restore-normal' || item.key === 'recheck-health'),
  '未失败的待检查账户不应显示恢复或重新检查操作'
)
assertTrue(
  accountMenuItems(pendingAccount).some((item) => item.key === 'super-priority-on')
    && accountMenuItems(pendingAccount).some((item) => item.key === 'fallback-on'),
  '待检查账户应允许直接设置超级优先或降级备用'
)
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
  },
  availabilityPresentation: {
    status: 'check_failed',
    label: '检查失败',
    reason: 'Invalid API key',
    probe: {
      kind: 'health_check',
      lastObservation: {
        observationId: 'health-check-regression',
        attemptedAt: '2026-07-11T01:00:00.000Z',
        result: 'failed',
        httpStatus: 401,
        errorCode: 'invalid_api_key',
        reason: 'Invalid API key',
        traceId: 'trace-health-check-failed'
      },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-07-11T01:15:00.000Z' }
    }
  }
})
assertStatus('待检查账户后台检查失败', pendingHealthCheckFailedAccount, '检查失败', 'red')
assertTrue(
  accountStatusTooltipLines(pendingHealthCheckFailedAccount).some((line) => line.includes('最近检查：')),
  '待检查账户失败 tooltip 应显示最近检查时间'
)
assertTrue(
  accountStatusTooltipLines(pendingHealthCheckFailedAccount).some((line) => line.includes('下次检查：')),
  '待检查账户失败 tooltip 应显示下次检查时间'
)
assertTrue(
  accountStatusTooltipLines(pendingHealthCheckFailedAccount).some((line) => line.includes('原因：Invalid API key')),
  '待检查账户失败 tooltip 应显示后台检查原因'
)
assertTrue(
  accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'recheck-health' && item.label === '重新检查'),
  '检查失败的自有待检查账户应显示重新检查'
)
assertTrue(
  !accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'restore-normal'),
  '检查失败的待检查账户不应显示跳过检查的恢复正常操作'
)
assertTrue(
  !accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'force-activate'),
  '自有待检查账户不应额外暴露独立的人工恢复操作'
)
const accountMenuActionsSource = readFileSync(resolve('../frontend/src/views/accounts/useAccountMenuActions.ts'), 'utf8')
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
    availabilityPresentation: {
      status: 'check_failed',
      label: '检查失败',
      probe: {
        kind: 'health_check',
        lastObservation: { observationId: 'trace-test', attemptedAt: '2026-07-15T01:00:00.000Z', result: 'failed', traceId: 'trace-health-check-regression' },
        schedule: { state: 'none' }
      }
    }
  })).some((line) => line.includes('traceId：trace-health-check-regression')),
  '账户状态提示应直接展示结构化健康检查 traceId'
)
assertTrue(
  accountMenuItems(pendingHealthCheckFailedAccount).some((item) => item.key === 'toggle-status' && item.label === '停用账户'),
  '检查失败的自有待检查账户应允许停用'
)
const activeHealthTimeline = accountStatusTooltipLines(accountFixture({
  availabilityPresentation: {
    status: 'available',
    label: '可调度',
    probe: {
      kind: 'health_check',
      lastObservation: { observationId: 'active-health', attemptedAt: '2026-07-11T01:05:00.000Z', result: 'success' },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-07-11T02:00:00.000Z' }
    }
  }
}))
assertTrue(activeHealthTimeline.some((line) => line.includes('最近检查：')), '正常账户应显示最近检查时间')
assertTrue(activeHealthTimeline.some((line) => line.includes('下次检查：')), '正常账户应显示下次检查时间')

const coolingHealthTimeline = accountStatusTooltipLines(accountFixture({
  status: 'temporary_unavailable',
  availabilityPresentation: {
    status: 'temporarily_unavailable',
    label: '临时不可调用',
    reason: '上游暂时不可用',
    probe: {
      kind: 'cooldown_retest',
      lastObservation: { observationId: 'cooldown', attemptedAt: '2026-07-11T01:00:00.000Z', result: 'failed', reason: '上游暂时不可用' },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-07-11T03:00:00.000Z' }
    }
  }
}))
assertTrue(coolingHealthTimeline.some((line) => line.includes('下次检查：')), '冷却账户应显示下一次实际检查时间')
assertTrue(!coolingHealthTimeline.some((line) => line.includes('健康')), '冷却账户不得混入健康检查内部文案')

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
    reason: 'mock runtime degraded'
  }
})
assertStatus('运行态调度降级', runtimeDegradedAccount, '调度降级', 'gold')
assertEqual(
  filterAccounts({ accounts: [runtimeDegradedAccount], filters: accountFilters(['active']), isManagementView: false }).length,
  1,
  '运行态调度降级仍应归入正常状态筛选'
)
assertTrue(
  accountStatusTooltipLines(runtimeDegradedAccount).some((line) => line.includes('原因：mock runtime degraded')),
  '运行态调度降级 tooltip 应只显示用户关心的原因'
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
    probePresentation: {
      lastObservation: {
        observationId: 'precheck-pending',
        attemptedAt: '2026-06-16T00:00:05.000Z',
        result: 'failed',
        reason: 'mock precheck pending',
        traceId: 'trace-precheck-pending'
      },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-06-16T00:01:00.000Z' }
    }
  }
}), '待探针确认', 'blue')
const precheckPresentationLines = accountStatusTooltipLines(accountFixture({
  effectiveAvailability: { available: false, status: 'runtime_precheck_pending', label: '待探针确认', color: 'blue', blockerScope: 'runtime', reason: 'mock precheck pending' },
  runtimeAvailability: {
    status: 'precheck_pending',
    reason: 'mock precheck pending',
    probePresentation: {
      lastObservation: { observationId: 'precheck-pending', attemptedAt: '2026-06-16T00:00:05.000Z', result: 'failed', reason: 'mock precheck pending', traceId: 'trace-precheck-pending' },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-06-16T00:01:00.000Z' }
    }
  },
  availabilityPresentation: {
    status: 'verifying',
    label: '待探针确认',
    reason: 'mock precheck pending',
    probe: {
      kind: 'runtime_probe',
      lastObservation: { observationId: 'precheck-pending', attemptedAt: '2026-06-16T00:00:05.000Z', result: 'failed', reason: 'mock precheck pending', traceId: 'trace-precheck-pending' },
      schedule: { state: 'scheduled', nextAttemptAt: '2099-06-16T00:01:00.000Z' }
    }
  }
}))
assertTrue(precheckPresentationLines.some((line) => line.includes('状态：待探针确认')), '事前确认 tooltip 应展示用户状态')
assertTrue(precheckPresentationLines.some((line) => line.includes('最近检查：')), '事前确认 tooltip 应展示最近检查')
assertTrue(precheckPresentationLines.some((line) => line.includes('下次检查：')), '事前确认 tooltip 应展示下次检查')
assertTrue(precheckPresentationLines.some((line) => line.includes('traceId：trace-precheck-pending')), '事前确认 tooltip 应展示 traceId')
assertTrue(!precheckPresentationLines.some((line) => line.includes('短窗口失败') || line.includes('来源 IP') || line.includes('API Key') || line.includes('数据库状态')), '事前确认 tooltip 不应展示内部统计字段')
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
    reason: 'mock half open'
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
    reason: 'mock precheck failed'
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
    reason: '账户进入长期不可用，等待后续检查'
  }
})
assertStatus('长期不可用', longTermUnavailableAccount, '长期不可用', 'gold')
assertTrue(
  accountStatusTooltipLines(longTermUnavailableAccount).some((line) => line.includes('原因：账户进入长期不可用')),
  '长期不可用 tooltip 应显示用户可理解的原因'
)
assertTrue(
  !accountStatusTooltipLines(longTermUnavailableAccount).some((line) => line.includes('满 7 天') || line.includes('每 1 小时')),
  '长期不可用 tooltip 不应展示后台复测机制细节'
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
assertTrue(!frequentFailureTooltip.some((line) => line.includes('账户状态：数据库仍为正常') || line.includes('仅统计真实上游失败') || line.includes('并发满') || line.includes('最后质量原因')), '质量内部统计不应出现在状态 tooltip')
assertTrue(
  !accountStatusTooltipLines(accountFixture({
    availabilityPresentation: {
      status: 'temporarily_unavailable',
      label: '临时不可调用',
      statusBoundary: { at: '2099-01-01T00:00:00.000Z', kind: 'cooldown_expiry' },
      probe: { kind: 'cooldown_retest', schedule: { state: 'none' } }
    }
  })).some((line) => line.includes('预计恢复') || line.includes('预计释放')),
  '业务边界时间不应伪装成探针计划或预计释放'
)

const precheckTooltip = accountStatusTooltipLines(accountFixture({
  runtimeAvailability: {
    status: 'precheck_pending',
    reason: 'mock precheck pending'
  }
}))
assertTrue(!precheckTooltip.some((line) => line.includes('运行态状态') || line.includes('数据库状态') || line.includes('短窗口失败')), '事前确认 tooltip 不应展示内部机制')

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
