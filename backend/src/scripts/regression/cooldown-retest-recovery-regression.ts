import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-cooldown-retest-recovery-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'cooldown-retest-recovery-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const group = repositories.createGroup({
    name: '冷却复测回归分组',
    providerCode: 'openai'
  }, access)
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '冷却复测观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery'
    },
    status: 'active'
  }, access)
  assert(repositories.setAccountGroup(account.id, group.id, access), '冷却复测观察窗口账号应能绑定分组')
  const cooled = repositories.markAccountCooldown(account.id, new Date(Date.now() + 60_000).toISOString(), '模拟临时不可调用')
  assert.equal(cooled?.status, 'temporary_unavailable', '临时不可调用应进入恢复通道')
  assert.ok(cooled?.cooldownRetestObservationStartedAt, '进入临时不可调用时应记录自动恢复观察起点')
  assert.ok(Date.parse(cooled.cooldownUntil ?? '') - Date.now() <= 10_000, '临时不可调用首次暂停应走秒级快速恢复')

  const expiredObservationStartedAt = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(expiredObservationStartedAt, new Date(Date.now() - 1000).toISOString(), account.id)

  const longRecovering = repositories.recordCooldownAccountRetestFailure(account.id, {
    statusCode: 401,
    errorMessage: '仍然不可用',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(longRecovering.action, 'exception', '超过最长观察后应标异常并停止自动恢复')
  assert.equal(longRecovering.account?.status, 'error', '超过观察窗口后账号应转异常')
  assert.equal(longRecovering.account?.schedulable, false, '超过观察窗口后账号不应继续参与调度')
  assert.equal(longRecovering.account?.cooldownUntil, undefined, '超过观察窗口后应清理冷却时间，避免继续捞出复测')
  assert.equal(longRecovering.account?.lastErrorCode, 'cooldown_retest_max_recovery_exceeded', '超过观察窗口后应写入明确异常码')
  assert.match(longRecovering.errorMessage, /已停止自动复测并标记为异常/, '失败摘要应说明已停止自动复测')
  assert(!repositories.listAccountsDueForCooldownRetest(20).some((item) => item.id === account.id), '异常账号不应再进入后台复测候选')

  const freshAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '冷却复测未超观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery-fresh'
    },
    status: 'active'
  }, access)
  assert(repositories.setAccountGroup(freshAccount.id, group.id, access), '冷却复测未超观察窗口账号应能绑定分组')
  repositories.markAccountCooldown(freshAccount.id, new Date(Date.now() + 60_000).toISOString(), '模拟临时不可调用')
  databaseModule.getBusinessDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(new Date().toISOString(), new Date(Date.now() - 1000).toISOString(), freshAccount.id)

  const stillRecovering = repositories.recordCooldownAccountRetestFailure(freshAccount.id, {
    statusCode: 403,
    errorCode: 'insufficient_quota',
    errorMessage: '余额和订阅额度均不足，请充值后再使用',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(stillRecovering.recoveryStage, 'slow', '超过快速阈值后应进入慢速恢复')
  assert.notEqual(stillRecovering.action, 'exception', '未超过最长观察时不应转异常')
  const freshAfterRetest = repositories.findAccountSummary(freshAccount.id)
  assert.equal(freshAfterRetest?.status, 'temporary_unavailable', '未超过观察窗口时账号应继续恢复')
  assert.equal(freshAfterRetest?.lastErrorCode, 'insufficient_quota', '后台复测应把上游真实错误码写入账户状态')
  assert.match(freshAfterRetest?.lastErrorMessage ?? '', /HTTP 403；insufficient_quota；余额和订阅额度均不足/, '后台复测状态原因应保留真实上游错误摘要')

  const restored = repositories.clearAccountFailureState(freshAccount.id, access)
  assert.equal(restored?.cooldownRetestObservationStartedAt, undefined, '恢复正常时应清理自动恢复观察起点')

  const disabledCleanupAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '停用清理过期失败原因回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-disable-clear-expired-error'
    },
    status: 'active'
  }, access)
  repositories.markAccountCooldown(disabledCleanupAccount.id, new Date(Date.now() + 60_000).toISOString(), '过期冷却错误')
  const disabledCleanup = repositories.updateAccount(disabledCleanupAccount.id, { status: 'disabled' }, access)
  assert.equal(disabledCleanup?.status, 'disabled', '冷却账号应允许手动停用')
  assert.equal(disabledCleanup?.lastErrorCode, undefined, '手动停用应清理既有错误码')
  assert.equal(disabledCleanup?.lastErrorMessage, undefined, '手动停用应清理既有失败原因，避免停用状态展示过期冷却错误')
  assert.equal(disabledCleanup?.cooldownUntil, undefined, '手动停用应清理既有冷却结束时间')

  const rateLimitedAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '限流后台复测回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-rate-limited'
    },
    status: 'active'
  }, access)
  assert(repositories.setAccountGroup(rateLimitedAccount.id, group.id, access), '限流复测账号应能绑定分组')
  const limited = repositories.markAccountCooldown(rateLimitedAccount.id, new Date(Date.now() - 1000).toISOString(), '模拟限流', 'rate_limited')
  assert.equal(limited?.status, 'rate_limited', '限流状态应进入同一自动恢复通道')
  assert.ok(limited?.cooldownRetestObservationStartedAt, '进入限流时应记录自动恢复观察起点')
  databaseModule.getBusinessDatabase()
    .prepare('UPDATE accounts SET cooldown_until = ? WHERE id = ?')
    .run(new Date(Date.now() - 1000).toISOString(), rateLimitedAccount.id)
  const dueIds = repositories.listAccountsDueForCooldownRetest(20).map((item) => item.id)
  assert(dueIds.includes(rateLimitedAccount.id), '限流到期账号应进入后台复测候选')
  const limitedStillRecovering = repositories.recordCooldownAccountRetestFailure(rateLimitedAccount.id, {
    statusCode: 429,
    errorMessage: '仍然限流',
    maxRecoveryHours: 1,
    maxPauseMinutes: 10
  })
  assert.equal(limitedStillRecovering.action, 'retry_immediately', '限流首次复测失败应走快速恢复通道')
  assert.equal(repositories.findAccountSummary(rateLimitedAccount.id)?.status, 'rate_limited', '限流复测失败后应保持限流状态等待下次自动恢复')

  console.log('cooldown retest recovery regression passed')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}
