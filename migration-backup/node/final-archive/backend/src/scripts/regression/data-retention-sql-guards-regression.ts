import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-data-retention-sql-guards-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'data-retention-sql-guards-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, dataRetention] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/data-retention.repository.js')
])

const statsTableNames = new Set([
  'account_quality_minute_stats',
  'authorization_team_usage_range_windows',
  'authorization_user_usage_range_windows',
  'usage_stats_minute',
  'usage_overview_summary_windows',
  'usage_overview_trend_windows',
  'usage_model_rank_windows',
  'usage_error_rank_windows',
  'ai_performance_summary_windows',
  'usage_quota_hourly_windows',
  'usage_scope_range_windows',
  'client_ip_usage_range_windows',
  'account_usage_snapshots',
  'system_metrics_trend_windows',
  'process_event_loop_trend_windows'
])
try {
  const statsDatabase = databaseModule.getStatsDatabase()
  seedUsageStatsRows()

  const originalPrepare = statsDatabase.prepare.bind(statsDatabase) as typeof statsDatabase.prepare
  statsDatabase.prepare = ((sql: string) => {
    if (/^\s*DELETE\s+FROM\s+"?usage_stats_minute"?\b/i.test(sql)) {
      throw new Error('模拟 usage_stats_minute 清理失败')
    }
    return originalPrepare(sql)
  }) as typeof statsDatabase.prepare

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
    statsDatabase.prepare = originalPrepare
  }

  assert.equal(tableCount('account_quality_minute_stats'), 0, '预聚合清理按表独立执行，前序表已清理后失败表等待下一轮')
  assert.equal(tableCount('usage_stats_minute'), 1, '预聚合清理失败时，失败表数据也应保留')
  seedClientIpUsageRangeWindow()
  const retryCleanup = dataRetention.cleanupUsageStatsBucketsBefore({
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
  })
  assert.equal(retryCleanup.clientIpUsageRangeWindows, 1, 'IP 维度范围窗口应接入数据保留清理结果')
  assert.equal(tableCount('client_ip_usage_range_windows'), 0, '过期 IP 维度范围窗口清理后不应残留旧记录')

  const indexChecks: Array<{ tableName: string; columnName: string; indexName: string }> = [
    { tableName: 'authorization_team_usage_range_windows', columnName: 'end_date', indexName: 'idx_authorization_team_usage_range_end' },
    { tableName: 'authorization_user_usage_range_windows', columnName: 'end_date', indexName: 'idx_authorization_user_usage_range_end' },
    { tableName: 'usage_overview_summary_windows', columnName: 'end_date', indexName: 'idx_usage_overview_summary_windows_end' },
    { tableName: 'usage_overview_trend_windows', columnName: 'end_date', indexName: 'idx_usage_overview_trend_windows_end' },
    { tableName: 'usage_model_rank_windows', columnName: 'end_date', indexName: 'idx_usage_model_rank_windows_end' },
    { tableName: 'usage_error_rank_windows', columnName: 'end_date', indexName: 'idx_usage_error_rank_windows_end' },
    { tableName: 'ai_performance_summary_windows', columnName: 'end_date', indexName: 'idx_ai_performance_summary_windows_end' },
    { tableName: 'usage_quota_hourly_windows', columnName: 'updated_at', indexName: 'idx_usage_quota_hourly_windows_updated' },
    { tableName: 'usage_scope_range_windows', columnName: 'end_date', indexName: 'idx_usage_scope_range_windows_end' },
    { tableName: 'client_ip_usage_range_windows', columnName: 'end_date', indexName: 'idx_client_ip_range_end' },
    { tableName: 'account_usage_snapshots', columnName: 'updated_at', indexName: 'idx_account_usage_snapshots_updated' },
    { tableName: 'system_metrics_trend_windows', columnName: 'end_date', indexName: 'idx_system_metrics_trend_windows_end' },
    { tableName: 'process_event_loop_trend_windows', columnName: 'end_date', indexName: 'idx_process_event_loop_trend_windows_end' }
  ]
  for (const check of indexChecks) {
    assertQueryUsesIndex(check.tableName, check.columnName, check.indexName)
  }
  console.log('数据保留 SQL 防护回归通过：预聚合清理按表推进，失败表可重试，范围窗口清理列具备索引')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedUsageStatsRows(): void {
  const statsDatabase = databaseModule.getStatsDatabase()
  statsDatabase.prepare(`
    INSERT INTO account_quality_minute_stats (account_id, system_account_id, provider_code, stat_minute, updated_at)
    VALUES ('acct_retention_txn', 'sys_admin', 'gpt', '2000-01-01T00:00', '2000-01-01T00:00:00.000Z')
  `).run()
  statsDatabase.prepare(`
    INSERT INTO usage_stats_minute (system_account_id, scope_type, scope_id, stat_minute, updated_at)
    VALUES ('sys_admin', 'global', '', '2000-01-01T00:00', '2000-01-01T00:00:00.000Z')
  `).run()
}

function seedClientIpUsageRangeWindow(): void {
  databaseModule.getStatsDatabase()
    .prepare(`
      INSERT INTO client_ip_usage_range_windows (ip_hash, start_date, end_date, updated_at)
      VALUES ('ip_retention_old', '2000-01-01', '2000-01-01', '2000-01-01T00:00:00.000Z')
    `)
    .run()
}

function tableCount(tableName: string): number {
  const row = databaseForTable(tableName)
    .prepare(`SELECT COUNT(*) AS total FROM ${tableName}`)
    .get() as { total?: number } | undefined
  return Number(row?.total ?? 0)
}

function assertQueryUsesIndex(tableName: string, columnName: string, indexName: string): void {
  const details = databaseForTable(tableName)
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

function databaseForTable(tableName: string): ReturnType<typeof databaseModule.getDatasetDatabase> {
  return statsTableNames.has(tableName) ? databaseModule.getStatsDatabase() : databaseModule.getDatasetDatabase()
}
