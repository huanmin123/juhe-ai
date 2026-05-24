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
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '冷却复测观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery'
    },
    status: 'active'
  }, access)
  const cooled = repositories.markAccountCooldown(account.id, new Date(Date.now() + 60_000).toISOString(), '模拟临时不可调用')
  assert.equal(cooled?.status, 'temporary_unavailable', '临时不可调用应进入恢复通道')
  assert.ok(cooled?.cooldownRetestObservationStartedAt, '进入临时不可调用时应记录自动恢复观察起点')
  assert.ok(Date.parse(cooled.cooldownUntil ?? '') - Date.now() <= 10_000, '临时不可调用首次暂停应走秒级快速恢复')

  const oldObservationStartedAt = new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString()
  databaseModule.getDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(oldObservationStartedAt, new Date(Date.now() - 1000).toISOString(), account.id)

  const exhausted = repositories.recordCooldownAccountRetestFailure(account.id, {
    statusCode: 401,
    errorMessage: '仍然不可用',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(exhausted.action, 'error', '超过最长自动恢复观察后应直接转异常')
  assert.equal(exhausted.account?.status, 'error', '超过观察窗口后账号状态应为异常')
  assert.match(exhausted.errorMessage, /已观察/, '异常摘要应包含真实观察时长')

  const freshAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '冷却复测未超观察窗口回归',
    type: 'api_key',
    credentials: {
      api_key: 'sk-cooldown-retest-recovery-fresh'
    },
    status: 'active'
  }, access)
  repositories.markAccountCooldown(freshAccount.id, new Date(Date.now() + 60_000).toISOString(), '模拟临时不可调用')
  databaseModule.getDatabase()
    .prepare(`
      UPDATE accounts
      SET cooldown_retest_failure_count = 4,
          cooldown_retest_observation_started_at = ?,
          cooldown_until = ?
      WHERE id = ?
    `)
    .run(new Date().toISOString(), new Date(Date.now() - 1000).toISOString(), freshAccount.id)

  const stillRecovering = repositories.recordCooldownAccountRetestFailure(freshAccount.id, {
    statusCode: 503,
    errorMessage: '短期失败',
    maxRecoveryHours: 1,
    maxPauseMinutes: 1440
  })
  assert.equal(stillRecovering.recoveryStage, 'slow', '超过快速阈值后应进入慢速恢复')
  assert.notEqual(stillRecovering.action, 'error', '未超过最长观察时不应转异常')
  assert.equal(repositories.findAccountSummary(freshAccount.id)?.status, 'temporary_unavailable', '未超过观察窗口时账号应继续恢复')

  const restored = repositories.clearAccountFailureState(freshAccount.id, access)
  assert.equal(restored?.cooldownRetestObservationStartedAt, undefined, '恢复正常时应清理自动恢复观察起点')

  console.log('cooldown retest recovery regression passed')
} finally {
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}
