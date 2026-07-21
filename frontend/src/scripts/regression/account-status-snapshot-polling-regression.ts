import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { ref } from 'vue'

import type { AccountListResult, AccountRuntimeAvailabilityStatus, AccountStatusSnapshotResult, AccountSummary } from '../../types/domain/accounts.js'
import { cloneAccountListCacheResult, mergeAccountListRuntimeSnapshot, mergeAccountStatusSnapshot, replaceAccountListRow } from '../../views/accounts/accountListMutations.js'
import { accountStatusSnapshotPollingDelayMs, createAccountStatusSnapshotPolling, isAccountStatusSnapshotCurrent } from '../../views/accounts/accountStatusSnapshotPolling.js'

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

const acceptedIdentity = { sequence: 3, revision: 7, idSignature: 'a\u0000b', scopeSignature: 'self:' }
assert.equal(isAccountStatusSnapshotCurrent(acceptedIdentity, acceptedIdentity), true)
for (const changed of [
  { ...acceptedIdentity, sequence: 4 },
  { ...acceptedIdentity, revision: 8 },
  { ...acceptedIdentity, idSignature: 'b\u0000a' },
  { ...acceptedIdentity, scopeSignature: 'management:sys_a' }
]) {
  assert.equal(isAccountStatusSnapshotCurrent(acceptedIdentity, changed), false, '任一列表身份变化都必须拒绝迟到快照')
}
assert.equal(accountStatusSnapshotPollingDelayMs(() => 0), 29_000)
assert.equal(accountStatusSnapshotPollingDelayMs(() => 1), 31_000)

let visible = true
let pending: (() => void) | undefined
let requestSignal: AbortSignal | undefined
let requestCount = 0
let maxConcurrent = 0
let concurrent = 0
const requestedBatchSizes: number[] = []
const delays: number[] = []
const polling = createAccountStatusSnapshotPolling({
  accountIds: () => Array.from({ length: 105 }, (_, index) => `account_${index}`),
  isBlocked: () => false,
  isVisible: () => visible,
  random: () => 0.5,
  request: async (ids, signal) => {
    requestedBatchSizes.push(ids.length)
    requestSignal = signal
    requestCount += 1
    concurrent += 1
    maxConcurrent = Math.max(maxConcurrent, concurrent)
    await new Promise<void>((resolve) => { pending = resolve })
    concurrent -= 1
  },
  setTimer: (callback, delay) => {
    delays.push(delay)
    return callback
  },
  clearTimer: () => undefined
})

polling.start()
await Promise.resolve()
polling.refreshNow()
assert.equal(requestCount, 1, '请求未完成时不得重叠')
visible = false
polling.refreshNow()
assert.equal(requestSignal?.aborted, true, '页面转为 hidden 时必须中止当前快照请求')
pending?.()
await Promise.resolve()
await Promise.resolve()
assert.equal(requestCount, 1, '页面隐藏时不得继续下一分块或刷新')
visible = true
polling.refreshNow()
await Promise.resolve()
assert.equal(requestCount, 2, '页面重新可见时应立即恢复首个分块')
pending?.()
await Promise.resolve()
await Promise.resolve()
assert.equal(requestCount, 3, '超过 100 个账户时必须继续请求剩余分块')
assert.deepEqual(requestedBatchSizes, [100, 100, 5], '每个运行态快照请求最多 100 个账户且必须覆盖全部已加载账户')
pending?.()
await Promise.resolve()
await Promise.resolve()
assert.equal(maxConcurrent, 1)
assert.equal(delays[0], 30_000)
polling.stop()

const accountListDataSource = readFileSync(fileURLToPath(new URL('../../views/accounts/useAccountListData.ts', import.meta.url)), 'utf8')
assert.match(accountListDataSource, /onActivated[\s\S]*snapshotPollingLifecycle\.activate\(\)/, 'KeepAlive 账户页恢复时必须激活轮询生命周期')
assert.match(accountListDataSource, /onDeactivated[\s\S]*snapshotPollingLifecycle\.deactivate\(\)/, 'KeepAlive 账户页隐藏时必须停用状态快照轮询')
assert.match(accountListDataSource, /onBeforeUnmount[\s\S]*snapshotPollingLifecycle\.dispose\(\)/, '账户页卸载时必须销毁状态快照轮询')
assert.match(accountListDataSource, /onActivate:\s*\(\)\s*=>\s*snapshotPolling\.refreshNow\(\)/, '账户页每次恢复必须立即请求状态快照')

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

console.log('账户状态快照前端回归通过：全部运行态稳定合并、100 ID 分块全覆盖、递归周期、hidden 与非重叠约束生效')
