import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { ref } from 'vue'

import type { AccountListResult, AccountRuntimeAvailabilityStatus, AccountStatusSnapshotResult, AccountSummary } from '../../types/domain/accounts.js'
import * as accountListMutations from '../../views/accounts/accountListMutations.js'
const { cloneAccountListCacheResult, mergeAccountListRuntimeSnapshot, mergeAccountStatusSnapshot, replaceAccountListRow } = accountListMutations
const usage = (requestCount: number) => ({
  requestCount,
  inputTokens: requestCount,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheReadCost: 0,
  cacheWriteTokens: 0,
  cacheWrite1hTokens: 0,
  cacheWriteCost: 0,
  thinkingTokens: 0,
  inputImageTokens: 0,
  outputImageTokens: 0,
  totalTokens: requestCount,
  totalCost: 0
})

const account = {
  id: 'account_a',
  name: 'A',
  status: 'active',
  schedulable: true,
  currentConcurrency: 1,
  currentConcurrencyAvailable: true,
  lastUsedAt: '2026-07-16T00:00:00.000Z',
  cooldownUntil: '2026-07-16T00:30:00.000Z',
  availabilityPresentation: {
    status: 'verification_failed',
    label: '旧状态',
    probe: {
      kind: 'health_check',
      lastObservation: { observationId: 'old', attemptedAt: '2026-07-16T00:10:00.000Z', result: 'failed', traceId: 'trace-old' },
      schedule: { state: 'scheduled', nextAttemptAt: '2026-07-16T00:20:00.000Z' }
    }
  },
  todayUsage: usage(1),
  notes: '不可被快照覆盖'
} as AccountSummary
const snapshot: AccountStatusSnapshotResult = {
  generatedAt: '2026-07-16T01:00:00.000Z',
  runtimeSnapshot: {
    accountConcurrencyAvailable: true,
    accountRuntimeAvailabilityAvailable: true
  },
  items: [{
    id: 'account_a',
    status: 'active',
    schedulable: true,
    currentConcurrency: 4,
    lastErrorTraceId: 'trace-main',
    cooldownRetestLastAt: '2026-07-16T00:50:00.000Z',
    cooldownRetestLastStatusCode: 429,
    nextHealthCheckAt: '2026-07-16T01:05:00.000Z',
    lastHealthCheckStatusCode: 200,
    lastHealthCheckTraceId: 'trace-health',
    authorizationInstanceSourceAccountLastErrorTraceId: 'trace-source',
    authorizationInstanceSourceAccountCooldownRetestLastAt: '2026-07-16T00:45:00.000Z',
    authorizationInstanceSourceAccountLastHealthCheckErrorCode: 'source_health_failed',
    lastUsedAt: '2026-07-16T00:59:00.000Z',
    todayUsage: usage(8),
    effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' },
    availabilityPresentation: { status: 'available', label: '可调度', probe: { kind: 'health_check', schedule: { state: 'none' } } }
  }]
}
const originalAccounts = [account]

assert.equal('accountListItemHasDynamicSnapshot' in accountListMutations, true, '账户列表必须能识别直接返回的完整动态快照')
const accountListItemHasDynamicSnapshot = (accountListMutations as typeof accountListMutations & {
  accountListItemHasDynamicSnapshot: (value: AccountSummary) => boolean
}).accountListItemHasDynamicSnapshot
assert.equal(accountListItemHasDynamicSnapshot({
  ...account,
  effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' }
} as AccountSummary), true)
assert.equal(accountListItemHasDynamicSnapshot({
  ...account,
  todayUsage: undefined,
  effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' }
} as unknown as AccountSummary), false)

const reactiveCachedResult = ref<AccountListResult>({
  items: [account],
  page: 1,
  pageSize: 50,
  total: 1,
  hasMore: false
}).value
assert.throws(() => structuredClone(reactiveCachedResult), /could not be cloned|DataCloneError/, 'Vue 响应式缓存值可稳定复现 structuredClone 异常')
assert.deepEqual(cloneAccountListCacheResult(reactiveCachedResult), reactiveCachedResult, '账户页缓存必须先解除 Vue Proxy 再克隆')

const merged = mergeAccountStatusSnapshot(originalAccounts, snapshot)
assert.equal(merged[0]?.currentConcurrency, 4)
assert.equal(merged[0]?.currentConcurrencyAvailable, true)
assert.equal(merged[0]?.todayUsage.requestCount, 8)
assert.equal(merged[0]?.lastUsedAt, '2026-07-16T00:59:00.000Z')
assert.equal(merged[0]?.lastErrorTraceId, 'trace-main')
assert.equal(merged[0]?.cooldownRetestLastStatusCode, 429)
assert.equal(merged[0]?.nextHealthCheckAt, '2026-07-16T01:05:00.000Z')
assert.equal(merged[0]?.lastHealthCheckTraceId, 'trace-health')
assert.equal(merged[0]?.authorizationInstanceSourceAccountLastErrorTraceId, 'trace-source')
assert.equal(merged[0]?.authorizationInstanceSourceAccountLastHealthCheckErrorCode, 'source_health_failed')
assert.equal(merged[0]?.notes, '不可被快照覆盖')
assert.equal(merged[0]?.cooldownUntil, undefined, '快照缺失 optional 状态字段时应清除旧值')
assert.equal(merged[0]?.availabilityPresentation?.status, 'available', '快照必须整体替换 presentation')
assert.equal(merged[0]?.availabilityPresentation?.probe?.lastObservation, undefined, '快照必须清除旧 observation 与 traceId')
assert.notEqual(merged, originalAccounts)

const unavailableConcurrencySnapshot = mergeAccountStatusSnapshot(originalAccounts, {
  ...snapshot,
  runtimeSnapshot: {
    ...snapshot.runtimeSnapshot,
    accountConcurrencyAvailable: false
  },
  items: snapshot.items.map((item) => ({
    ...item,
    currentConcurrency: 0,
    lastUsedAt: '2026-07-16T00:59:30.000Z',
    todayUsage: usage(9)
  }))
})
assert.equal(unavailableConcurrencySnapshot[0]?.currentConcurrency, 1, 'Redis 并发不可用时必须保留同作用域可信并发')
assert.equal(unavailableConcurrencySnapshot[0]?.currentConcurrencyAvailable, true, '保留可信并发时必须保留可用标记')
assert.equal(unavailableConcurrencySnapshot[0]?.todayUsage.requestCount, 9, 'Redis 不可用不能阻止 SQL 统计动态字段更新')
assert.equal(unavailableConcurrencySnapshot[0]?.lastUsedAt, '2026-07-16T00:59:30.000Z')

const rowUpdated = replaceAccountListRow(originalAccounts, {
  ...account,
  superPriorityEnabled: true,
  currentConcurrency: 0,
  currentConcurrencyAvailable: false,
  lastUsedAt: undefined,
  todayUsage: usage(0)
} as AccountSummary)
assert.equal(rowUpdated[0]?.superPriorityEnabled, true)
assert.equal(rowUpdated[0]?.currentConcurrency, 1, '行级操作回写不应清掉已有并发快照')
assert.equal(rowUpdated[0]?.todayUsage.requestCount, 1, '行级操作回写不应清掉已有日用量快照')
assert.equal(rowUpdated[0]?.lastUsedAt, '2026-07-16T00:00:00.000Z', '行级操作回写不应清掉最近使用时间')
assert.equal(replaceAccountListRow(originalAccounts, { ...account, id: 'missing' }), originalAccounts)

const accountListDataSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
assert.doesNotMatch(accountListDataSource, /createAccountStatusSnapshotPolling/, '账户列表不得创建状态快照自动轮询器')
assert.doesNotMatch(accountListDataSource, /accountStatusSnapshotPolling/, '账户列表不得导入状态快照轮询 helper')
assert.match(accountListDataSource, /refreshAccountStatusSnapshot/, '账户列表加载完成后必须执行一次动态快照补齐')
assert.match(accountListDataSource, /statusSnapshot\(/, '账户列表缺少动态字段时必须请求状态快照')
assert.doesNotMatch(accountListDataSource, /snapshotPolling/, '账户列表不得保留状态快照轮询生命周期连接')
assert.doesNotMatch(accountListDataSource, /setInterval|setTimeout/, '账户动态快照不得恢复定时轮询')
assert.doesNotMatch(accountListDataSource, /visibilitychange|window\.addEventListener\(['"]focus/, '账户动态快照不得因页面可见性或聚焦变化触发刷新')

const previousRuntimeAccount = {
  id: 'acc_runtime_refresh',
  runtimeAvailability: {
    status: 'local_suppressed',
    reason: '已确认运行态',
    since: '2026-07-17T00:00:00.000Z',
    until: '2026-07-17T00:01:00.000Z'
  },
  effectiveAvailability: {
    available: false,
    status: 'runtime_local_suppressed',
    label: '短暂避让',
    color: 'gold',
    blockerScope: 'runtime'
  },
  availabilityPresentation: { status: 'avoided', label: '短暂避让', reason: '旧运行态卡片' },
  accountRuntimeAvailabilityAvailable: true
} as AccountSummary
const refreshedWithoutRuntime = {
  ...previousRuntimeAccount,
  runtimeAvailability: undefined,
  accountRuntimeAvailabilityAvailable: false
} as AccountSummary
const preservedRefresh = mergeAccountListRuntimeSnapshot([previousRuntimeAccount], [refreshedWithoutRuntime], false)
assert.equal(preservedRefresh[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(preservedRefresh[0]?.accountRuntimeAvailabilityAvailable, true)

assert.equal(preservedRefresh[0]?.effectiveAvailability, previousRuntimeAccount.effectiveAvailability)
assert.equal(preservedRefresh[0]?.availabilityPresentation, previousRuntimeAccount.availabilityPresentation, '完整列表保留旧运行态时必须同步保留 presentation')

const preservedConcurrency = mergeAccountListRuntimeSnapshot(
  [{ ...account, currentConcurrency: 3, currentConcurrencyAvailable: true } as AccountSummary],
  [{ ...account, currentConcurrency: 0, currentConcurrencyAvailable: false } as AccountSummary],
  false
)
assert.equal(preservedConcurrency[0]?.currentConcurrency, 3, '静态刷新缺少并发快照时必须保留同作用域可信并发')
assert.equal(preservedConcurrency[0]?.currentConcurrencyAvailable, true, '保留可信并发时必须同步保留可用标记')

const allRuntimeStatuses = [
  'normal',
  'degraded',
  'local_suppressed',
  'half_open',
  'precheck_pending',
  'precheck_failed'
] satisfies AccountRuntimeAvailabilityStatus[]
for (const status of allRuntimeStatuses) {
  const previous = {
    ...previousRuntimeAccount,
    id: `acc_runtime_${status}`,
    runtimeAvailability: { ...previousRuntimeAccount.runtimeAvailability!, status }
  } as AccountSummary
  const refreshed = {
    ...previous,
    runtimeAvailability: undefined,
    effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' },
    accountRuntimeAvailabilityAvailable: false
  } as AccountSummary
  const preserved = mergeAccountListRuntimeSnapshot([previous], [refreshed], false)
  assert.equal(preserved[0]?.runtimeAvailability?.status, status, `列表运行态来源不可用时必须保留 ${status}`)
  assert.equal(preserved[0]?.effectiveAvailability, previous.effectiveAvailability)
}

const changedScopeRefresh = mergeAccountListRuntimeSnapshot([previousRuntimeAccount], [refreshedWithoutRuntime], false, false)
assert.equal(changedScopeRefresh[0]?.runtimeAvailability, undefined)

const changedBindingRefresh = mergeAccountListRuntimeSnapshot(
  [{ ...previousRuntimeAccount, bindingSystemAccountId: 'sys_a', boundGroupId: 'group_a', accountAuthorizationId: 'auth_a' }],
  [{ ...refreshedWithoutRuntime, bindingSystemAccountId: 'sys_b', boundGroupId: 'group_a', accountAuthorizationId: 'auth_a' }],
  false
)
assert.equal(changedBindingRefresh[0]?.runtimeAvailability, undefined)

const refreshedWithConfirmedRuntime = {
  ...previousRuntimeAccount,
  runtimeAvailability: undefined,
  accountRuntimeAvailabilityAvailable: true
} as AccountSummary
const confirmedRefresh = mergeAccountListRuntimeSnapshot([previousRuntimeAccount], [refreshedWithConfirmedRuntime], true)
assert.equal(confirmedRefresh[0]?.runtimeAvailability, undefined)
assert.equal(confirmedRefresh[0]?.accountRuntimeAvailabilityAvailable, true)

const confirmedNormalAccount = {
  ...previousRuntimeAccount,
  runtimeAvailability: undefined,
  effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' },
  accountRuntimeAvailabilityAvailable: true
} as AccountSummary
const unavailableAfterConfirmedNormal = mergeAccountListRuntimeSnapshot(
  [confirmedNormalAccount],
  [{ ...confirmedNormalAccount, accountRuntimeAvailabilityAvailable: false }],
  false
)
assert.equal(unavailableAfterConfirmedNormal[0]?.accountRuntimeAvailabilityAvailable, true, '不可用刷新必须保留已明确为正常的运行态快照证据')

const disabledEffectiveAvailability = {
  available: false,
  status: 'disabled',
  label: '已停用',
  color: 'default',
  blockerScope: 'account'
} as NonNullable<AccountSummary['effectiveAvailability']>
const disabledRefresh = {
  ...refreshedWithoutRuntime,
  status: 'disabled',
  schedulable: false,
  effectiveAvailability: disabledEffectiveAvailability,
  availabilityPresentation: { status: 'disabled', label: '已停用' }
} as AccountSummary
const preservedRuntimeWithDisabledAccount = mergeAccountListRuntimeSnapshot([previousRuntimeAccount], [disabledRefresh], false)
assert.equal(preservedRuntimeWithDisabledAccount[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(preservedRuntimeWithDisabledAccount[0]?.effectiveAvailability?.status, 'disabled', '新账户级阻断必须优先于旧运行态派生状态')
assert.equal(preservedRuntimeWithDisabledAccount[0]?.availabilityPresentation?.status, 'disabled', '完整列表新账户级阻断必须替换旧运行态 presentation')

const unavailablePollingSnapshot = mergeAccountStatusSnapshot([previousRuntimeAccount], {
  generatedAt: '2026-07-17T00:02:00.000Z',
  runtimeSnapshot: {
    accountConcurrencyAvailable: true,
    accountRuntimeAvailabilityAvailable: false
  },
  items: [{
    id: previousRuntimeAccount.id,
    status: 'active',
    schedulable: true,
    currentConcurrency: 0,
    todayUsage: usage(0),
    effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' },
    availabilityPresentation: { status: 'available', label: '可调度' }
  }]
})
assert.equal(unavailablePollingSnapshot[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(unavailablePollingSnapshot[0]?.effectiveAvailability?.status, 'runtime_local_suppressed')
assert.equal(unavailablePollingSnapshot[0]?.availabilityPresentation?.status, 'avoided', '保留旧运行态时必须同步保留对应 presentation')
assert.equal(unavailablePollingSnapshot[0]?.accountRuntimeAvailabilityAvailable, true)

const unavailablePollingWithDisabledAccount = mergeAccountStatusSnapshot([previousRuntimeAccount], {
  generatedAt: '2026-07-17T00:03:00.000Z',
  runtimeSnapshot: {
    accountConcurrencyAvailable: true,
    accountRuntimeAvailabilityAvailable: false
  },
  items: [{
    id: previousRuntimeAccount.id,
    status: 'disabled',
    schedulable: false,
    currentConcurrency: 0,
    todayUsage: usage(0),
    effectiveAvailability: disabledEffectiveAvailability,
    availabilityPresentation: { status: 'disabled', label: '已停用' }
  }]
})
assert.equal(unavailablePollingWithDisabledAccount[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(unavailablePollingWithDisabledAccount[0]?.effectiveAvailability?.status, 'disabled', '轮询更新的账户级阻断必须优先于旧运行态派生状态')
assert.equal(unavailablePollingWithDisabledAccount[0]?.availabilityPresentation?.status, 'disabled', '新账户级阻断必须替换旧运行态 presentation')

const rowRuntimePreserved = replaceAccountListRow([previousRuntimeAccount], refreshedWithoutRuntime)
assert.equal(rowRuntimePreserved[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(rowRuntimePreserved[0]?.effectiveAvailability?.status, 'runtime_local_suppressed')
assert.equal(rowRuntimePreserved[0]?.availabilityPresentation, previousRuntimeAccount.availabilityPresentation, '行级更新保留旧运行态时必须同步保留 presentation')

const rowRuntimeWithDisabledAccount = replaceAccountListRow([previousRuntimeAccount], disabledRefresh)
assert.equal(rowRuntimeWithDisabledAccount[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(rowRuntimeWithDisabledAccount[0]?.effectiveAvailability?.status, 'disabled', '行级更新的账户级阻断必须优先于旧运行态派生状态')
assert.equal(rowRuntimeWithDisabledAccount[0]?.availabilityPresentation?.status, 'disabled', '行级更新的新账户级阻断必须替换旧运行态 presentation')

const unavailableRuntimeAccount = { ...previousRuntimeAccount, accountRuntimeAvailabilityAvailable: false } as AccountSummary
const refreshedAvailable = {
  ...refreshedWithoutRuntime,
  effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' },
  availabilityPresentation: { status: 'available', label: '可调度' }
} as AccountSummary
const rowAfterUnavailableRuntime = replaceAccountListRow([unavailableRuntimeAccount], refreshedAvailable)
assert.equal(rowAfterUnavailableRuntime[0]?.runtimeAvailability, undefined, '行级更新不得保留不可用运行态的旧 runtime')
assert.equal(rowAfterUnavailableRuntime[0]?.effectiveAvailability?.status, 'available', '行级更新不得保留不可用运行态的旧 effective')
assert.equal(rowAfterUnavailableRuntime[0]?.availabilityPresentation?.status, 'available', '行级更新不得保留不可用运行态的旧 presentation')

const refreshedWithNewRuntime = {
  ...refreshedWithoutRuntime,
  runtimeAvailability: { status: 'precheck_pending', reason: '新的运行态' },
  effectiveAvailability: {
    available: false,
    status: 'runtime_precheck_pending',
    label: '等待确认',
    color: 'gold',
    blockerScope: 'runtime'
  },
  availabilityPresentation: { status: 'pending_verification', label: '等待确认', reason: '新的运行态卡片' }
} as AccountSummary
const rowWithNewRuntime = replaceAccountListRow([unavailableRuntimeAccount], refreshedWithNewRuntime)
assert.equal(rowWithNewRuntime[0]?.runtimeAvailability?.status, 'precheck_pending')
assert.equal(rowWithNewRuntime[0]?.effectiveAvailability?.status, 'runtime_precheck_pending')
assert.equal(rowWithNewRuntime[0]?.availabilityPresentation?.status, 'pending_verification', '行级更新必须采用新的运行态 presentation')

console.log('账户状态快照前端回归通过：运行态合并保持稳定，账户列表不再自动轮询状态快照')
