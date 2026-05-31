import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AccountQualityRealtimeRefreshResult } from '../../storage/account-quality.repository.js'
import { minuteKey, usageStatsTimezone } from '../../storage/usage-stats-helpers.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-quality-refresh-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-quality-refresh.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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
  const statsDatabase = databaseModule.getStatsDatabase()
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
  const staleAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '质量刷新无新样本账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-account-quality-stale-refresh',
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active'
  }, access)
  const batchAccounts = Array.from({ length: 5 }, (_, index) => repositories.createAccount({
    providerCode: 'openai',
    name: `质量刷新批量账户 ${index}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-account-quality-batch-${index}`,
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active'
  }, access))
  const nowDate = new Date()
  const now = nowDate.toISOString()
  const statMinute = minuteKey(nowDate, usageStatsTimezone())
  const inactiveAccountId = 'acct_quality_inactive_batch_cleanup'
  statsDatabase
    .prepare(`
      INSERT INTO account_quality_minute_stats (
        account_id, system_account_id, provider_code, stat_minute,
        request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
        last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
      ) VALUES (?, ?, ?, ?, 1, 0, 1, 0, 0, ?, NULL, ?, ?, ?)
    `)
    .run(account.id, 'sys_admin', 'openai', statMinute, now, now, '质量刷新模拟错误', now)
  for (const [index, batchAccount] of batchAccounts.entries()) {
    statsDatabase
      .prepare(`
        INSERT INTO account_quality_minute_stats (
          account_id, system_account_id, provider_code, stat_minute,
          request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
          last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
        ) VALUES (?, ?, ?, ?, 1, 1, 0, ?, 1, ?, ?, NULL, NULL, ?)
      `)
      .run(batchAccount.id, 'sys_admin', 'openai', statMinute, 800 + index, now, now, now)
  }
  for (let index = 0; index < 1205; index += 1) {
    const inactiveMinute = minuteKey(new Date(nowDate.getTime() - (60 + index) * 60 * 1000), usageStatsTimezone())
    statsDatabase
      .prepare(`
        INSERT INTO account_quality_minute_stats (
          account_id, system_account_id, provider_code, stat_minute,
          request_count, success_count, error_count, first_token_ms_sum, first_token_ms_count,
          last_sample_at, last_success_at, last_error_at, last_error_message, updated_at
        ) VALUES (?, 'sys_admin', 'openai', ?, 1, 1, 0, 800, 1, ?, ?, NULL, NULL, ?)
      `)
      .run(inactiveAccountId, inactiveMinute, now, now, now)
  }
  statsDatabase
    .prepare(`
      INSERT INTO account_quality_scores (
        account_id, system_account_id, provider_code, quality_score, quality_state,
        recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
        recent_avg_first_token_ms, ewma_first_token_ms, success_rate,
        window_started_at, window_ended_at, last_sample_at, updated_at
      ) VALUES (?, 'sys_admin', 'openai', 1000, 'fresh', 1, 1, 0, 1, 1000, 1000, 1, ?, ?, ?, ?)
    `)
    .run(staleAccount.id, now, now, now, now)

  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  let qualityScoreUpsertPrepares = 0
  statsDatabase.prepare = ((sql: string) => {
    if (/^\s*INSERT\s+INTO\s+account_quality_scores\b/i.test(sql)) {
      qualityScoreUpsertPrepares += 1
    }
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare

  let result: AccountQualityRealtimeRefreshResult
  try {
    result = accountQualityRepository.refreshAccountQualityFromUsage(10)
  } finally {
    statsDatabase.prepare = originalPrepare
  }
  assert.equal(result.refreshed, 1 + batchAccounts.length, '账号质量刷新应处理分钟桶样本')
  assert.equal(qualityScoreUpsertPrepares, 1, '账号质量刷新应复用 account_quality_scores upsert statement')
  assert.equal(inactiveQualityMinuteCount(inactiveAccountId), 205, '账号质量刷新应小批清理已失效账户分钟桶，剩余等待后续轮次')
  const row = statsDatabase
    .prepare('SELECT quality_score, quality_state, recent_error_count, last_error_message FROM account_quality_scores WHERE account_id = ?')
    .get(account.id) as { quality_score?: number; quality_state?: string; recent_error_count?: number; last_error_message?: string } | undefined
  assert.equal(row?.quality_state, 'unknown', '只有失败样本时不能把账号质量标记为失败，避免请求形态错误污染调度')
  assert.equal(row?.recent_error_count, 1)
  assert.equal(row?.last_error_message, '质量刷新模拟错误')
  assert(row?.quality_score && row.quality_score >= 1_000_000, '没有成功首段样本时质量分应保持未知保守值')
  const staleRow = statsDatabase
    .prepare('SELECT quality_state, recent_request_count FROM account_quality_scores WHERE account_id = ?')
    .get(staleAccount.id) as { quality_state?: string; recent_request_count?: number } | undefined
  assert.equal(staleRow?.quality_state, 'stale', '活跃账户没有新质量样本时应标记为 stale')
  assert.equal(staleRow?.recent_request_count, 0)
  for (const batchAccount of batchAccounts) {
    const batchRow = statsDatabase
      .prepare('SELECT quality_state, recent_success_count FROM account_quality_scores WHERE account_id = ?')
      .get(batchAccount.id) as { quality_state?: string; recent_success_count?: number } | undefined
    assert.equal(batchRow?.quality_state, 'fresh', '批量质量样本账号应标记为 fresh')
    assert.equal(batchRow?.recent_success_count, 1)
  }

  console.log('账号质量刷新回归通过')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function inactiveQualityMinuteCount(accountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_quality_minute_stats WHERE account_id = ?')
    .get(accountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}
