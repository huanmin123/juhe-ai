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
  retryPolicy: sequenceRetryPolicy('balance_cleanup_test', [0, 0], 2),
  deleteSnapshot: async () => {
    cleanupAttempts += 1
    if (cleanupAttempts < 4) throw new Error(`simulated cleanup failure ${cleanupAttempts}`)
  }
})

await successfulCoordinator.cleanupAfterSave({
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
  deleteSnapshot: async () => { throw new Error('stats writer unavailable') }
})
await exhaustedCoordinator.cleanupAfterSave({
  accountId: 'account-exhausted',
  configRevision: 9,
  reason: 'balance_configuration_changed'
})
await waitFor(() => exhaustedCoordinator.snapshot().exhaustedCount === 1)
assert.equal(exhaustedCoordinator.snapshot().exhaustedAccountCount, 1)
assert.equal(exhaustedCoordinator.isSuppressed('account-exhausted'), true, '重试耗尽后进程内仍应屏蔽旧快照')

const oldSnapshotRecord: AccountBalanceSnapshotRecord = {
  snapshot: { status: 'fresh', remainingUsd: '88.00' },
  nextRefreshAfter: '2026-07-14T01:00:00.000Z'
}
assert.equal(accountBalanceSnapshotMatchesConfiguration({
  nextRefreshAt: '2026-07-14T02:00:00.000Z'
}, oldSnapshotRecord), false, '重新启用余额后，旧快照调度代次必须与持久化当前配置失配')
exhaustedCoordinator.clearForTest()
assert.equal(exhaustedCoordinator.isSuppressed('account-exhausted'), false, '模拟进程重启后内存 suppression 会清空')
assert.equal(accountBalanceSnapshotMatchesConfiguration({
  nextRefreshAt: '2026-07-14T02:00:00.000Z'
}, oldSnapshotRecord), false, '进程重启后仍必须由持久化调度代次继续屏蔽旧快照')
assert.equal(accountBalanceSnapshotMatchesConfiguration({
  nextRefreshAt: oldSnapshotRecord.nextRefreshAfter
}, oldSnapshotRecord), true, '当前刷新成功后，匹配调度代次的新快照才允许展示')

const cleanupSource = readFileSync(new URL('../../modules/accounts/account-balance-snapshot-cleanup.service.ts', import.meta.url), 'utf8')
const accountListSource = readFileSync(new URL('../../modules/accounts/account-list.routes.ts', import.meta.url), 'utf8')
const balanceRepositorySource = readFileSync(new URL('../../storage/account-balance.repository.ts', import.meta.url), 'utf8')
const accountRoutesSource = readFileSync(new URL('../../modules/accounts/accounts.routes.ts', import.meta.url), 'utf8')
assert.match(cleanupSource, /createRetryQueue<AccountBalanceSnapshotCleanupQueueItem>/, '余额快照清理必须复用轻量重试队列')
assert.match(cleanupSource, /account_balance_snapshot_cleanup_retry_exhausted/, '有限重试耗尽必须写可观测日志')
assert.match(accountRoutesSource, /cleanupAccountBalanceSnapshotAfterSave/, '单账户保存不能继续吞掉快照清理错误')
assert.match(accountListSource, /accountBalanceSnapshotMatchesConfiguration\(configuration, snapshotRecord\)/, '读取端必须校验持久化调度代次')
assert.match(balanceRepositorySource, /SELECT account_id, snapshot_json, next_refresh_after/, '快照读取必须携带持久化刷新代次')
assert.match(balanceRepositorySource, /AND updated_at <= \?/, '延迟清理只能删除保存时点前的旧快照，不能误删后续新快照')

console.log('AI 账户余额快照清理回归通过：首删失败有限重试、状态可见，重启后旧快照仍由持久化调度代次屏蔽')

async function waitFor(predicate: () => boolean, timeoutMs = 1000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error('等待余额快照清理重试状态超时')
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}
