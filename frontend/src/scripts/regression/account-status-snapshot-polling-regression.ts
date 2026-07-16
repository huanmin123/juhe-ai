import assert from 'node:assert/strict'

import type { AccountStatusSnapshotResult, AccountSummary } from '../../types/domain/accounts.js'
import { mergeAccountStatusSnapshot, replaceAccountListRow } from '../../views/accounts/accountListMutations.js'
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
const delays: number[] = []
const polling = createAccountStatusSnapshotPolling({
  accountIds: () => Array.from({ length: 105 }, (_, index) => `account_${index}`),
  isBlocked: () => false,
  isVisible: () => visible,
  random: () => 0.5,
  request: async (ids, signal) => {
    assert.equal(ids.length, 100)
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
assert.equal(requestCount, 1, '页面隐藏时不得继续刷新')
visible = true
polling.refreshNow()
await Promise.resolve()
assert.equal(requestCount, 2, '页面重新可见时应立即恢复刷新')
pending?.()
await Promise.resolve()
await Promise.resolve()
assert.equal(maxConcurrent, 1)
assert.equal(delays[0], 30_000)
polling.stop()

console.log('账户状态快照前端回归通过：局部合并、100 ID、递归周期、hidden 与非重叠约束生效')
