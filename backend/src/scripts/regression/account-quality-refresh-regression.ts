import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { minuteKey, usageStatsTimezone } from '../../storage/usage-stats-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-quality-refresh-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-quality-refresh.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'account-quality-refresh-records.sqlite3')
runtimeConfig.secret = 'account-quality-refresh-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, accountQualityRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-quality.repository.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const recordDatabase = databaseModule.getRecordDatabase()
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '质量刷新回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-quality-refresh',
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active'
  }, access)
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const statMinute = minuteKey(nowDate, usageStatsTimezone())
  recordDatabase
    .prepare(`
      INSERT INTO account_quality_minute_stats (
        account_id, system_account_id, provider_code, stat_minute,
        request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
        last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
      ) VALUES (?, ?, ?, ?, 1, 0, 1, 0, 0, ?, NULL, ?, ?, ?)
    `)
    .run(account.id, 'sys_admin', 'openai', statMinute, now, now, '质量刷新模拟错误', now)

  const result = accountQualityRepository.refreshAccountQualityFromUsage(10)
  assert.equal(result.refreshed, 1, '账号质量刷新应处理分钟桶样本')
  const row = recordDatabase
    .prepare('SELECT quality_state, recent_error_count, last_error_message FROM account_quality_scores WHERE account_id = ?')
    .get(account.id) as { quality_state?: string; recent_error_count?: number; last_error_message?: string } | undefined
  assert.equal(row?.quality_state, 'failed')
  assert.equal(row?.recent_error_count, 1)
  assert.equal(row?.last_error_message, '质量刷新模拟错误')

  console.log('账号质量刷新回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
