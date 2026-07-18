import assert from 'node:assert/strict'
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
    lastUsedAt: '2026-07-16T00:59:00.000Z',
    todayUsage: usage(8),
    effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' }
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
assert.equal(merged[0]?.notes, '不可被快照覆盖')
assert.equal(merged[0]?.cooldownUntil, undefined, '快照缺失 optional 状态字段时应清除旧值')
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
  effectiveAvailability: disabledEffectiveAvailability
} as AccountSummary
const preservedRuntimeWithDisabledAccount = mergeAccountListRuntimeSnapshot([previousRuntimeAccount], [disabledRefresh], false)
assert.equal(preservedRuntimeWithDisabledAccount[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(preservedRuntimeWithDisabledAccount[0]?.effectiveAvailability?.status, 'disabled', '新账户级阻断必须优先于旧运行态派生状态')

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
    effectiveAvailability: { available: true, status: 'available', label: '可调度', color: 'green' }
  }]
})
assert.equal(unavailablePollingSnapshot[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(unavailablePollingSnapshot[0]?.effectiveAvailability?.status, 'runtime_local_suppressed')
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
    effectiveAvailability: disabledEffectiveAvailability
  }]
})
assert.equal(unavailablePollingWithDisabledAccount[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(unavailablePollingWithDisabledAccount[0]?.effectiveAvailability?.status, 'disabled', '轮询更新的账户级阻断必须优先于旧运行态派生状态')

const rowRuntimePreserved = replaceAccountListRow([previousRuntimeAccount], refreshedWithoutRuntime)
assert.equal(rowRuntimePreserved[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(rowRuntimePreserved[0]?.effectiveAvailability?.status, 'runtime_local_suppressed')

const rowRuntimeWithDisabledAccount = replaceAccountListRow([previousRuntimeAccount], disabledRefresh)
assert.equal(rowRuntimeWithDisabledAccount[0]?.runtimeAvailability?.status, 'local_suppressed')
assert.equal(rowRuntimeWithDisabledAccount[0]?.effectiveAvailability?.status, 'disabled', '行级更新的账户级阻断必须优先于旧运行态派生状态')

console.log('账户状态快照前端回归通过：全部运行态稳定合并、100 ID 分块全覆盖、递归周期、hidden 与非重叠约束生效')
