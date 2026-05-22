import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-data-retention-sql-guards-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
runtimeConfig.secret = 'data-retention-sql-guards-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, dataRetention, runtimeLogsRepository] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/data-retention.repository.js'),
  import('../../storage/runtime-logs.repository.js')
])

try {
  const recordDatabase = databaseModule.getRecordDatabase()
  seedUsageStatsRows()

  const originalPrepare = recordDatabase.prepare.bind(recordDatabase) as typeof recordDatabase.prepare
  recordDatabase.prepare = ((sql: string) => {
    if (/^\s*DELETE\s+FROM\s+usage_stats_minute\b/i.test(sql)) {
      throw new Error('模拟 usage_stats_minute 清理失败')
    }
    return originalPrepare(sql)
  }) as typeof recordDatabase.prepare

  try {
    assert.throws(() => dataRetention.cleanupUsageStatsBucketsBefore({
      accountQualityMinuteCutoffMinute: '2001-01-01T00:00',
      minuteCutoffMinute: '2001-01-01T00:00',
      hourlyCutoffHour: '2001-01-01T00',
      dailyCutoffDate: '2001-01-01',
      weeklyCutoffWeek: '2001-W01',
      monthlyCutoffMonth: '2001-01',
      rankSnapshotCutoffIso: '2001-01-01T00:00:00.000Z',
      windowCutoffDate: '2001-01-01',
      windowCutoffIso: '2001-01-01T00:00:00.000Z',
      limit: 100
    }), /模拟 usage_stats_minute 清理失败/, '预聚合清理中途失败应向上抛出错误')
  } finally {
    recordDatabase.prepare = originalPrepare
  }

  assert.equal(tableCount('account_quality_minute_stats'), 1, '预聚合清理失败时，已执行的前序表删除应随事务回滚')
  assert.equal(tableCount('usage_stats_minute'), 1, '预聚合清理失败时，失败表数据也应保留')

  seedModelCheckHistory()
  const modelCheckCleanup = dataRetention.cleanupModelCheckRunsBefore('2001-01-01T00:00:00.000Z', 100)
  assert.equal(modelCheckCleanup.modelCheckRuns, 1, '模型检测历史应接入统一保留清理')
  assert.equal(modelCheckCleanup.modelCheckItems, 1, '模型检测项应随过期检测运行一起清理')
  assert.equal(tableCount('model_check_runs'), 0, '模型检测运行清理后不应残留旧记录')
  assert.equal(tableCount('model_check_items'), 0, '模型检测项清理后不应残留旧记录')

  seedRuntimeLogFileCursor()
  assert.equal(
    runtimeLogsRepository.cleanupRuntimeLogFileCursorsBefore('2001-01-01T00:00:00.000Z', 100),
    1,
    '运行日志文件游标应清理长期未更新的旧文件状态'
  )
  assert.equal(tableCount('runtime_log_file_cursors'), 0, '过期运行日志文件游标清理后不应残留旧记录')

  const indexChecks: Array<{ tableName: string; columnName: string; indexName: string }> = [
    { tableName: 'audit_error_groups', columnName: 'updated_at', indexName: 'idx_audit_error_groups_updated' },
    { tableName: 'authorization_team_usage_range_windows', columnName: 'end_date', indexName: 'idx_authorization_team_usage_range_end' },
    { tableName: 'authorization_user_usage_range_windows', columnName: 'end_date', indexName: 'idx_authorization_user_usage_range_end' },
    { tableName: 'usage_overview_summary_windows', columnName: 'end_date', indexName: 'idx_usage_overview_summary_windows_end' },
    { tableName: 'usage_overview_trend_windows', columnName: 'end_date', indexName: 'idx_usage_overview_trend_windows_end' },
    { tableName: 'usage_model_rank_windows', columnName: 'end_date', indexName: 'idx_usage_model_rank_windows_end' },
    { tableName: 'usage_error_rank_windows', columnName: 'end_date', indexName: 'idx_usage_error_rank_windows_end' },
    { tableName: 'ai_performance_summary_windows', columnName: 'end_date', indexName: 'idx_ai_performance_summary_windows_end' },
    { tableName: 'usage_quota_hourly_windows', columnName: 'updated_at', indexName: 'idx_usage_quota_hourly_windows_updated' },
    { tableName: 'usage_scope_range_windows', columnName: 'end_date', indexName: 'idx_usage_scope_range_windows_end' },
    { tableName: 'account_usage_snapshots', columnName: 'updated_at', indexName: 'idx_account_usage_snapshots_updated' },
    { tableName: 'system_metrics_trend_windows', columnName: 'end_date', indexName: 'idx_system_metrics_trend_windows_end' },
    { tableName: 'process_event_loop_trend_windows', columnName: 'end_date', indexName: 'idx_process_event_loop_trend_windows_end' },
    { tableName: 'runtime_log_file_cursors', columnName: 'updated_at', indexName: 'idx_runtime_log_file_cursors_updated' }
  ]
  for (const check of indexChecks) {
    assertQueryUsesIndex(check.tableName, check.columnName, check.indexName)
  }
  assertModelCheckRunCleanupUsesIndex()

  console.log('数据保留 SQL 防护回归通过：预聚合清理失败可回滚，模型检测与运行日志游标可清理，清理列具备索引')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageStatsRows(): void {
  const recordDatabase = databaseModule.getRecordDatabase()
  recordDatabase.prepare(`
    INSERT INTO account_quality_minute_stats (account_id, system_account_id, provider_code, stat_minute, updated_at)
    VALUES ('acct_retention_txn', 'sys_admin', 'openai', '2000-01-01T00:00', '2000-01-01T00:00:00.000Z')
  `).run()
  recordDatabase.prepare(`
    INSERT INTO usage_stats_minute (system_account_id, scope_type, scope_id, stat_minute, updated_at)
    VALUES ('sys_admin', 'global', '', '2000-01-01T00:00', '2000-01-01T00:00:00.000Z')
  `).run()
}

function seedModelCheckHistory(): void {
  const recordDatabase = databaseModule.getRecordDatabase()
  recordDatabase.prepare(`
    INSERT INTO model_check_runs (id, target_type, target_id, model, started_at, created_at, updated_at)
    VALUES ('mcr_retention_old', 'account', 'acct_retention_old', 'gpt-5.5', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')
  `).run()
  recordDatabase.prepare(`
    INSERT INTO model_check_items (id, run_id, item_key, item_type, status, created_at, updated_at)
    VALUES ('mci_retention_old', 'mcr_retention_old', 'json_schema', 'capability', 'passed', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')
  `).run()
}

function seedRuntimeLogFileCursor(): void {
  databaseModule.getRecordDatabase()
    .prepare(`
      INSERT INTO runtime_log_file_cursors (log_file, created_at, updated_at)
      VALUES ('logs/old-runtime.log', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')
    `)
    .run()
}

function tableCount(tableName: string): number {
  const row = databaseModule.getRecordDatabase()
    .prepare(`SELECT COUNT(*) AS total FROM ${tableName}`)
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function assertQueryUsesIndex(tableName: string, columnName: string, indexName: string): void {
  const details = databaseModule.getRecordDatabase()
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT rowid
      FROM ${tableName}
      WHERE ${columnName} < ?
      ORDER BY ${columnName} ASC, rowid ASC
      LIMIT ?
    `)
    .all('2099-01-01', 1)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes(indexName), `${tableName}.${columnName} 清理查询应使用 ${indexName}，实际计划：${details}`)
}

function assertModelCheckRunCleanupUsesIndex(): void {
  const details = databaseModule.getRecordDatabase()
    .prepare(`
      EXPLAIN QUERY PLAN
      SELECT id
      FROM model_check_runs
      WHERE created_at < ?
      ORDER BY created_at ASC, id ASC
      LIMIT ?
    `)
    .all('2099-01-01', 1)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
  assert(details.includes('idx_model_check_runs_created'), `model_check_runs.created_at 清理查询应使用 idx_model_check_runs_created，实际计划：${details}`)
  assert(!/USE TEMP B-TREE/i.test(details), `model_check_runs.created_at 清理查询不应使用临时排序，实际计划：${details}`)
}
