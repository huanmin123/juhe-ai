import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import {
  createAccountBalanceSnapshotCleanupCoordinator
} from '../../modules/accounts/account-balance-snapshot-cleanup.service.js'
import {
  accountBalanceSnapshotMatchesConfiguration,
  type AccountBalanceSnapshotRecord
} from '../../storage/account-balance.repository.js'
import { sequenceRetryPolicy } from '../../shared/retry-policy.js'

let cleanupAttempts = 0
const successfulCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  retryPolicy: sequenceRetryPolicy('balance_cleanup_test', [0, 0, 0], 3),
  deleteSnapshot: async () => {
    cleanupAttempts += 1
    if (cleanupAttempts < 4) throw new Error(`simulated cleanup failure ${cleanupAttempts}`)
  }
})

successfulCoordinator.cleanupAfterSave({
  accountId: 'account-retry',
  configRevision: 7,
  reason: 'multiple_api_keys'
})
assert.equal(successfulCoordinator.isSuppressed('account-retry'), true, '首删失败后必须立即屏蔽旧余额快照')
await waitFor(() => successfulCoordinator.snapshot().completedCount === 1)
assert.equal(cleanupAttempts, 4, '首删失败后只允许既定次数的有限幂等重试')
assert.equal(successfulCoordinator.snapshot().failedAttemptCount, 3)
assert.equal(successfulCoordinator.isSuppressed('account-retry'), false, '重试成功后应解除读取屏蔽')

const exhaustedCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  retryPolicy: sequenceRetryPolicy('balance_cleanup_exhausted_test', [0], 1),
  deleteSnapshot: async () => { throw new Error('stats writer unavailable') },
  now: () => '2026-07-14T02:00:00.000Z'
})
exhaustedCoordinator.cleanupAfterSave({
  accountId: 'account-exhausted',
  configRevision: 9,
  reason: 'balance_configuration_changed'
})
await waitFor(() => exhaustedCoordinator.snapshot().exhaustedCount === 1)
assert.equal(exhaustedCoordinator.snapshot().exhaustedAccountCount, 1)
assert.equal(exhaustedCoordinator.isSuppressed('account-exhausted'), true, '重试耗尽后进程内仍应屏蔽旧快照')

const oldSnapshotRecord: AccountBalanceSnapshotRecord = {
  snapshot: { status: 'fresh', remainingUsd: '88.00' },
  nextRefreshAfter: '2026-07-14T01:00:00.000Z',
  updatedAt: '2026-07-14T01:30:00.000Z'
}
assert.equal(accountBalanceSnapshotMatchesConfiguration({
  nextRefreshAt: '2026-07-14T02:00:00.000Z'
}, oldSnapshotRecord), false, '重新启用余额后，旧快照调度代次必须与持久化当前配置失配')

const currentGeneration = { nextRefreshAt: '2026-07-14T03:00:00.000Z' }
const staleCurrentGenerationSnapshot: AccountBalanceSnapshotRecord = {
  snapshot: { status: 'fresh', remainingUsd: '66.00' },
  nextRefreshAfter: currentGeneration.nextRefreshAt,
  updatedAt: '2026-07-14T01:59:59.999Z'
}
assert.equal(exhaustedCoordinator.isSuppressed('account-exhausted', {
  configuration: currentGeneration,
  snapshotRecord: staleCurrentGenerationSnapshot
}), true, '清理截止点之前的同代次旧快照仍必须被屏蔽')
const cutoffCurrentGenerationSnapshot: AccountBalanceSnapshotRecord = {
  snapshot: { status: 'fresh', remainingUsd: '66.50' },
  nextRefreshAfter: currentGeneration.nextRefreshAt,
  updatedAt: '2026-07-14T02:00:00.000Z'
}
assert.equal(exhaustedCoordinator.isSuppressed('account-exhausted', {
  configuration: currentGeneration,
  snapshotRecord: cutoffCurrentGenerationSnapshot
}), true, '与清理截止点同一毫秒的快照仍在删除谓词内，不能提前解除屏蔽')
const freshCurrentGenerationSnapshot: AccountBalanceSnapshotRecord = {
  snapshot: { status: 'fresh', remainingUsd: '77.00' },
  nextRefreshAfter: currentGeneration.nextRefreshAt,
  updatedAt: '2026-07-14T02:00:00.001Z'
}
assert.equal(exhaustedCoordinator.isSuppressed('account-exhausted', {
  configuration: currentGeneration,
  snapshotRecord: freshCurrentGenerationSnapshot
}), false, '清理截止点之后写入且匹配当前刷新代次的新快照必须解除屏蔽')
assert.equal(exhaustedCoordinator.snapshot().suppressedAccountCount, 0)
assert.equal(exhaustedCoordinator.snapshot().exhaustedAccountCount, 0)
exhaustedCoordinator.clearForTest()
assert.equal(exhaustedCoordinator.isSuppressed('account-exhausted'), false, '模拟进程重启后内存 suppression 会清空')
assert.equal(accountBalanceSnapshotMatchesConfiguration({
  nextRefreshAt: '2026-07-14T02:00:00.000Z'
}, oldSnapshotRecord), false, '进程重启后仍必须由持久化调度代次继续屏蔽旧快照')
assert.equal(accountBalanceSnapshotMatchesConfiguration({
  nextRefreshAt: oldSnapshotRecord.nextRefreshAfter
}, oldSnapshotRecord), true, '当前刷新成功后，匹配调度代次的新快照才允许展示')

let slowDeleteStarted = false
let releaseSlowDelete: (() => void) | undefined
const slowDelete = new Promise<void>((resolve) => {
  releaseSlowDelete = resolve
})
const nonBlockingCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  deleteSnapshot: async () => {
    slowDeleteStarted = true
    await slowDelete
  }
})
const registrationResult = nonBlockingCoordinator.cleanupAfterSave({
  accountId: 'account-slow-delete',
  configRevision: 10,
  reason: 'balance_configuration_changed'
})
assert.equal(registrationResult, undefined, '保存后的清理 API 只能同步登记，不能返回等待外部 stats-writer 的 Promise')
assert.equal(slowDeleteStarted, false, '登记调用返回前不能直接执行外部删除')
await waitFor(() => nonBlockingCoordinator.snapshot().runningCount === 1)
assert.equal(nonBlockingCoordinator.snapshot().completedCount, 0)
releaseSlowDelete?.()
await waitFor(() => nonBlockingCoordinator.snapshot().completedCount === 1)

let cutoffSnapshot: AccountBalanceSnapshotRecord | undefined = {
  snapshot: { status: 'fresh', remainingUsd: '55.00' },
  nextRefreshAfter: '2026-07-14T04:00:00.000Z',
  updatedAt: '2026-07-14T02:00:00.000Z'
}
let releaseCutoffDelete: (() => void) | undefined
const cutoffDeleteGate = new Promise<void>((resolve) => {
  releaseCutoffDelete = resolve
})
const cutoffCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  now: () => '2026-07-14T02:00:00.000Z',
  deleteSnapshot: async (item) => {
    await cutoffDeleteGate
    if (cutoffSnapshot && Date.parse(cutoffSnapshot.updatedAt) <= Date.parse(item.updatedBefore)) {
      cutoffSnapshot = undefined
    }
  }
})
cutoffCoordinator.cleanupAfterSave({
  accountId: 'account-cutoff-delete',
  configRevision: 12,
  reason: 'balance_configuration_changed'
})
await waitFor(() => cutoffCoordinator.snapshot().runningCount === 1)
assert.equal(cutoffCoordinator.isSuppressed('account-cutoff-delete', {
  configuration: { nextRefreshAt: '2026-07-14T04:00:00.000Z' },
  snapshotRecord: cutoffSnapshot
}), true, '等于 cutoff 的快照不能在运行中删除完成前解除屏蔽')
releaseCutoffDelete?.()
await waitFor(() => cutoffCoordinator.snapshot().completedCount === 1)
assert.equal(cutoffSnapshot, undefined, '等于 cutoff 的快照必须仍由 updated_at <= cutoff 删除')
assert.equal(cutoffCoordinator.isSuppressed('account-cutoff-delete'), false)

let postCutoffSnapshot: AccountBalanceSnapshotRecord | undefined = {
  snapshot: { status: 'fresh', remainingUsd: '99.00' },
  nextRefreshAfter: '2026-07-14T05:00:00.000Z',
  updatedAt: '2026-07-14T02:00:00.001Z'
}
let postCutoffDeleteFinished = false
let releasePostCutoffDelete: (() => void) | undefined
const postCutoffDeleteGate = new Promise<void>((resolve) => {
  releasePostCutoffDelete = resolve
})
const postCutoffCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  now: () => '2026-07-14T02:00:00.000Z',
  deleteSnapshot: async (item) => {
    await postCutoffDeleteGate
    if (postCutoffSnapshot && Date.parse(postCutoffSnapshot.updatedAt) <= Date.parse(item.updatedBefore)) {
      postCutoffSnapshot = undefined
    }
    postCutoffDeleteFinished = true
  }
})
postCutoffCoordinator.cleanupAfterSave({
  accountId: 'account-post-cutoff-delete',
  configRevision: 13,
  reason: 'balance_configuration_changed'
})
await waitFor(() => postCutoffCoordinator.snapshot().runningCount === 1)
assert.equal(postCutoffCoordinator.isSuppressed('account-post-cutoff-delete', {
  configuration: { nextRefreshAt: '2026-07-14T05:00:00.000Z' },
  snapshotRecord: postCutoffSnapshot
}), false, 'cutoff 后写入且匹配当前代次的新快照必须在读侧解除屏蔽')
assert.equal(postCutoffCoordinator.snapshot().suppressedAccountCount, 0)
releasePostCutoffDelete?.()
await waitFor(() => postCutoffDeleteFinished)
assert(postCutoffSnapshot, '运行中的旧删除只能删除 cutoff 及以前的快照，不能误删 cutoff 后的新快照')

let releaseFirstReplacementDelete: (() => void) | undefined
const firstReplacementDeleteGate = new Promise<void>((resolve) => {
  releaseFirstReplacementDelete = resolve
})
const replacementRevisions: number[] = []
const replacementCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  deleteSnapshot: async (item) => {
    replacementRevisions.push(item.configRevision)
    if (item.configRevision === 20) await firstReplacementDeleteGate
  }
})
replacementCoordinator.cleanupAfterSave({
  accountId: 'account-replacement',
  configRevision: 20,
  reason: 'balance_configuration_changed'
})
await waitFor(() => replacementCoordinator.snapshot().runningCount === 1)
replacementCoordinator.cleanupAfterSave({
  accountId: 'account-replacement',
  configRevision: 21,
  reason: 'balance_configuration_changed'
})
replacementCoordinator.cleanupAfterSave({
  accountId: 'account-replacement',
  configRevision: 22,
  reason: 'balance_configuration_changed'
})
releaseFirstReplacementDelete?.()
await waitFor(() => replacementCoordinator.snapshot().completedCount === 1)
assert.deepEqual(replacementRevisions, [20, 22], '同账户连续保存必须保留运行中首项并只执行最新替换项')
assert.equal(replacementCoordinator.snapshot().suppressedAccountCount, 0)

let activeDeletes = 0
let maxActiveDeletes = 0
const boundedCoordinator = createAccountBalanceSnapshotCleanupCoordinator({
  deleteSnapshot: async () => {
    activeDeletes += 1
    maxActiveDeletes = Math.max(maxActiveDeletes, activeDeletes)
    await new Promise((resolve) => setTimeout(resolve, 5))
    activeDeletes -= 1
  }
})
const registrations = Array.from({ length: 100 }, (_, index) => boundedCoordinator.cleanupAfterSave({
  accountId: `account-batch-${index}`,
  configRevision: 11,
  reason: 'batch_multiple_api_keys',
  batchId: 'batch-bounded-cleanup'
}))
assert(registrations.every((result) => result === undefined), '批量保存只能同步登记清理项')
assert.equal(boundedCoordinator.snapshot().pendingCount, 100, '100 个批量清理项应先进入有界队列')
await waitFor(() => boundedCoordinator.snapshot().completedCount === 100, 5_000)
assert.equal(maxActiveDeletes, 2, '首次删除和后续重试必须共同遵守并发上限 2')

const cleanupSource = readFileSync(new URL('../../modules/accounts/account-balance-snapshot-cleanup.service.ts', import.meta.url), 'utf8')
const accountListSource = readFileSync(new URL('../../modules/accounts/account-list.routes.ts', import.meta.url), 'utf8')
const balanceRepositorySource = readFileSync(new URL('../../storage/account-balance.repository.ts', import.meta.url), 'utf8')
const accountRoutesSource = readFileSync(new URL('../../modules/accounts/accounts.routes.ts', import.meta.url), 'utf8')
const accountBatchEditSource = readFileSync(new URL('../../modules/accounts/account-batch-edit.service.ts', import.meta.url), 'utf8')
assert.match(cleanupSource, /createRetryQueue<AccountBalanceSnapshotCleanupQueueItem>/, '余额快照清理必须复用轻量重试队列')
assert.match(cleanupSource, /account_balance_snapshot_cleanup_retry_exhausted/, '有限重试耗尽必须写可观测日志')
assert.doesNotMatch(cleanupSource, /cleanupAfterSave:\s*async/, '首次删除不能绕过重试队列阻塞保存响应')
assert.match(accountRoutesSource, /cleanupAccountBalanceSnapshotAfterSave/, '单账户保存不能继续吞掉快照清理错误')
assert.doesNotMatch(accountRoutesSource, /await cleanupAccountBalanceSnapshotAfterSave/, '单账户保存不能等待派生快照清理')
assert.match(
  accountBatchEditSource,
  /function cleanupChangedBalanceSnapshots\([\s\S]{0,500}for \(const accountId of accountIds\)/,
  '批量首次删除必须逐项登记到 coordinator 的有界队列'
)
assert.doesNotMatch(
  accountBatchEditSource,
  /function cleanupChangedBalanceSnapshots\([\s\S]{0,500}Promise\.all/,
  '批量首次删除不能无界并发调用 stats-writer'
)
assert.match(accountListSource, /isAccountBalanceSnapshotSuppressed\(account\.id, \{ configuration, snapshotRecord \}\)/, '读取端必须用当前快照事实解析抑制状态')
assert.match(accountListSource, /accountBalanceSnapshotMatchesConfiguration\(configuration, snapshotRecord\)/, '读取端必须校验持久化调度代次')
assert.match(balanceRepositorySource, /SELECT account_id, snapshot_json, next_refresh_after, updated_at/, '快照读取必须携带持久化刷新代次和更新时间')
assert.match(balanceRepositorySource, /AND updated_at <= \?/, '延迟清理只能删除保存时点前的旧快照，不能误删后续新快照')

console.log('AI 账户余额快照清理回归通过：首次删除有界且不阻塞保存，cutoff 边界不重叠，同账户替换与持久化代次稳定')

async function waitFor(predicate: () => boolean, timeoutMs = 2_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待余额快照清理重试状态超时')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
