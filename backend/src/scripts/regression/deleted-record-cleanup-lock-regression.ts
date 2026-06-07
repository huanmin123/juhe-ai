import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { sqliteBusyTimeoutMs } from '../../storage/sqlite-config.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-deleted-record-cleanup-lock-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'deleted-record-cleanup-lock-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, accountCleanup, apiKeyCleanup] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/account-record-cleanup.js'),
  import('../../storage/api-key-record-cleanup.js')
])

const now = new Date(Date.now() - 60_000).toISOString()
const systemAccountId = 'sys_deleted_cleanup_lock'
const accountId = 'acct_deleted_cleanup_stats_locked'
const apiKeyId = 'key_deleted_cleanup_stats_locked'

try {
  seedAccountStats(accountId, systemAccountId)
  seedApiKeyStats(apiKeyId, systemAccountId)

  withStatsWriteLock(() => {
    const accountResult = accountCleanup.cleanupDeletedAccountRelatedRecordData({
      accountId,
      systemAccountId
    })
    assert.equal(accountResult.hasMore, true, '统计库写锁占用时 AI 账户删除清理应转为后台重试')
    assert.match(accountResult.blockedReason ?? '', /SQLite/, 'AI 账户删除清理应记录明确 locked blockedReason')
    assert.equal(accountCleanupTargetExists(accountId), true, '统计库写锁占用时应保留 AI 账户清理目标')
    assert.equal(accountQualityScoreCount(accountId), 1, '统计库写锁占用时不应强行删除账户质量统计')

    const apiKeyResult = apiKeyCleanup.cleanupDeletedApiKeyRelatedRecordData({
      apiKeyId,
      systemAccountId
    })
    assert.equal(apiKeyResult.hasMore, true, '统计库写锁占用时 API Key 删除清理应转为后台重试')
    assert.match(apiKeyResult.blockedReason ?? '', /SQLite/, 'API Key 删除清理应记录明确 locked blockedReason')
    assert.equal(apiKeyCleanupTargetExists(apiKeyId), true, '统计库写锁占用时应保留 API Key 清理目标')
    assert.equal(apiKeyStatsTotal(apiKeyId), 1, '统计库写锁占用时不应强行删除 API Key 统计')
  })

  const accountRetry = accountCleanup.cleanupDeletedAccountRelatedRecordData({
    accountId,
    systemAccountId
  })
  assert.equal(accountRetry.hasMore, false, '统计库锁释放后 AI 账户删除清理应完成')
  assert.equal(accountCleanupTargetExists(accountId), false, '统计库锁释放后应移除 AI 账户清理目标')
  assert.equal(accountQualityScoreCount(accountId), 0, '统计库锁释放后应清理账户质量统计')
  assert.equal(accountUsageSnapshotCount(accountId), 0, '统计库锁释放后应清理账户外部用量快照')

  const apiKeyRetry = apiKeyCleanup.cleanupDeletedApiKeyRelatedRecordData({
    apiKeyId,
    systemAccountId
  })
  assert.equal(apiKeyRetry.hasMore, false, '统计库锁释放后 API Key 删除清理应完成')
  assert.equal(apiKeyCleanupTargetExists(apiKeyId), false, '统计库锁释放后应移除 API Key 清理目标')
  assert.equal(apiKeyStatsTotal(apiKeyId), 0, '统计库锁释放后应清理 API Key 聚合统计')

  console.log('已删除记录清理锁回归通过：统计库 busy 时删除清理转为 blocked 重试，锁释放后继续完成')
} finally {
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedAccountStats(targetAccountId: string, ownerSystemAccountId: string): void {
  const statsDatabase = databaseModule.getStatsDatabase()
  statsDatabase.prepare(`
    INSERT INTO account_quality_scores (
      account_id, system_account_id, provider_code, quality_score, quality_state,
      recent_request_count, recent_success_count, recent_error_count, recent_first_token_sample_count,
      window_started_at, window_ended_at, updated_at
    ) VALUES (?, ?, 'gpt', 100, 'healthy', 1, 1, 0, 0, ?, ?, ?)
  `).run(targetAccountId, ownerSystemAccountId, now, now, now)
  statsDatabase.prepare(`
    INSERT INTO account_usage_snapshots (
      system_account_id, account_id, kind, source, snapshot_json, updated_at, created_at
    ) VALUES (?, ?, 'openai_codex', 'regression', '{}', ?, ?)
  `).run(ownerSystemAccountId, targetAccountId, now, now)
}

function seedApiKeyStats(targetApiKeyId: string, ownerSystemAccountId: string): void {
  databaseModule.getStatsDatabase().prepare(`
    INSERT INTO usage_stats_totals (
      system_account_id, scope_type, scope_id, request_count, success_count, updated_at
    ) VALUES (?, 'api_key', ?, 1, 1, ?)
  `).run(ownerSystemAccountId, targetApiKeyId, now)
}

function withStatsWriteLock(action: () => void): void {
  const statsDatabase = databaseModule.getStatsDatabase()
  statsDatabase.exec('PRAGMA busy_timeout = 1')
  const statsLock = new DatabaseSync(runtimeConfig.statsDatabasePath)
  statsLock.exec('PRAGMA busy_timeout = 1; BEGIN IMMEDIATE')
  try {
    action()
  } finally {
    statsLock.exec('ROLLBACK')
    statsLock.close()
    statsDatabase.exec(`PRAGMA busy_timeout = ${sqliteBusyTimeoutMs}`)
  }
}

function accountCleanupTargetExists(targetAccountId: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT account_id FROM account_record_cleanup_targets WHERE account_id = ?')
    .get(targetAccountId) as { account_id?: string } | undefined
  return Boolean(row?.account_id)
}

function apiKeyCleanupTargetExists(targetApiKeyId: string): boolean {
  const row = databaseModule.getDatasetDatabase()
    .prepare('SELECT api_key_id FROM api_key_record_cleanup_targets WHERE api_key_id = ?')
    .get(targetApiKeyId) as { api_key_id?: string } | undefined
  return Boolean(row?.api_key_id)
}

function accountQualityScoreCount(targetAccountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_quality_scores WHERE account_id = ?')
    .get(targetAccountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function accountUsageSnapshotCount(targetAccountId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare('SELECT COUNT(*) AS total FROM account_usage_snapshots WHERE account_id = ?')
    .get(targetAccountId) as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function apiKeyStatsTotal(targetApiKeyId: string): number {
  const row = databaseModule.getStatsDatabase()
    .prepare("SELECT request_count FROM usage_stats_totals WHERE system_account_id = ? AND scope_type = 'api_key' AND scope_id = ?")
    .get(systemAccountId, targetApiKeyId) as { request_count?: number } | undefined
  return Number(row?.request_count ?? 0)
}
