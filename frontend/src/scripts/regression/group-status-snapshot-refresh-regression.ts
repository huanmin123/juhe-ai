import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import type { GroupStatusSnapshotResult, GroupSummary } from '../../types/domain/accounts.js'

const helperUrl = new URL('../../views/groups/groupListMutations.ts', import.meta.url)
assert.equal(existsSync(fileURLToPath(helperUrl)), true, '分组列表必须提供动态快照合并 helper')
const {
  groupListItemHasDynamicSnapshot,
  mergeGroupListDynamicSnapshot,
  mergeGroupStatusSnapshot
} = await import(helperUrl.href)

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

const trustedGroup = {
  id: 'group_a',
  name: 'A',
  accountStats: {
    total: 2,
    available: 2,
    active: 2,
    disabled: 0,
    error: 0,
    rateLimited: 0,
    currentConcurrency: 4,
    currentConcurrencyAvailable: true,
    concurrencyLimit: 20,
    todayUsage: usage(1),
    usage: usage(3)
  }
} as GroupSummary

assert.equal(groupListItemHasDynamicSnapshot(trustedGroup, true), true)
assert.equal(groupListItemHasDynamicSnapshot({
  ...trustedGroup,
  accountStats: { ...trustedGroup.accountStats, currentConcurrencyAvailable: false }
}, false), false)

const preservedList = mergeGroupListDynamicSnapshot([trustedGroup], [{
  ...trustedGroup,
  accountStats: {
    ...trustedGroup.accountStats,
    currentConcurrency: 0,
    currentConcurrencyAvailable: false,
    todayUsage: usage(2)
  }
}], true)
assert.equal(preservedList[0]?.accountStats.currentConcurrency, 4)
assert.equal(preservedList[0]?.accountStats.currentConcurrencyAvailable, true)
assert.equal(preservedList[0]?.accountStats.todayUsage.requestCount, 2)

const unavailableSnapshot = mergeGroupStatusSnapshot([trustedGroup], {
  generatedAt: '2026-07-22T00:00:00.000Z',
  runtimeSnapshot: { accountConcurrencyAvailable: false },
  items: [{ id: trustedGroup.id, currentConcurrency: 0, todayUsage: usage(9) }]
} as GroupStatusSnapshotResult)
assert.equal(unavailableSnapshot[0]?.accountStats.currentConcurrency, 4, 'Redis 不可用时必须保留可信分组并发')
assert.equal(unavailableSnapshot[0]?.accountStats.currentConcurrencyAvailable, true)
assert.equal(unavailableSnapshot[0]?.accountStats.todayUsage.requestCount, 9, 'Redis 不可用时仍应更新分组当日用量')

const changedScope = mergeGroupListDynamicSnapshot([trustedGroup], [{
  ...trustedGroup,
  accountStats: { ...trustedGroup.accountStats, currentConcurrency: 0, currentConcurrencyAvailable: false }
}], false)
assert.equal(changedScope[0]?.accountStats.currentConcurrencyAvailable, false, '切换作用域后不得复用旧分组并发')

const groupsViewSource = readFileSync(fileURLToPath(new URL('../../views/groups/GroupsView.vue', import.meta.url)), 'utf8')
assert.match(groupsViewSource, /loadDynamicSnapshotsInBatches/, '分组状态快照必须使用有界分批')
assert.match(groupsViewSource, /refreshGroupStatusSnapshot/, '分组列表加载后必须执行一次动态快照补齐')
assert.doesNotMatch(groupsViewSource, /currentConcurrencyAvailable:\s*true/, '分组快照不得无条件标记实时并发可用')
assert.doesNotMatch(groupsViewSource, /setInterval|setTimeout|visibilitychange|window\.addEventListener\(['"]focus/, '分组动态值不得恢复定时或聚焦轮询')

console.log('分组状态快照刷新回归通过：直接值、同作用域兜底、Redis 不可用和有界刷新均符合契约')
