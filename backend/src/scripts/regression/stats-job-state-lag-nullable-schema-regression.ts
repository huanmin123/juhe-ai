import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { DatabaseSync } from 'node:sqlite'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-stats-job-state-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const recordDatabasePath = join(tempRoot, 'records.sqlite3')

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = recordDatabasePath
runtimeConfig.secret = 'stats-job-state-lag-nullable-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const legacyDatabase = new DatabaseSync(recordDatabasePath)
legacyDatabase.exec(`
  CREATE TABLE stats_job_state (
    scope_type TEXT NOT NULL,
    scope_id TEXT NOT NULL DEFAULT '',
    job_name TEXT NOT NULL,
    cursor_created_at TEXT,
    cursor_id TEXT,
    last_success_at TEXT,
    last_error_message TEXT,
    lag_seconds INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (scope_type, scope_id, job_name)
  );

  INSERT INTO stats_job_state (
    scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at
  ) VALUES (
    'global', '', 'usage_stats_aggregation', '2026-01-01T00:00:00.000Z', 'usage_1', '2026-01-01T00:00:00.000Z', NULL, 12, '2026-01-01T00:00:00.000Z'
  );
`)
legacyDatabase.close()

const databaseModule = await import('../../storage/database.js')

try {
  const database = databaseModule.getRecordDatabase()
  const lagColumn = database.prepare('PRAGMA table_info(stats_job_state)').all()
    .find((column) => (column as { name?: string }).name === 'lag_seconds') as { notnull?: number } | undefined
  assert.equal(lagColumn?.notnull, 0, 'stats_job_state.lag_seconds 应允许 NULL，表达任务滞后未知')

  database.prepare(`
    INSERT INTO stats_job_state (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run('table_monitor', 'business', 'table_storage_snapshots', '2026-01-01T00:00:00.000Z', 'accounts', '2026-01-01T00:00:00.000Z', null, null, '2026-01-01T00:00:00.000Z')

  const preservedRow = database.prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'global' AND job_name = 'usage_stats_aggregation'")
    .get() as { lag_seconds?: number | null } | undefined
  assert.equal(preservedRow?.lag_seconds, 12, '重建 stats_job_state 时应保留已有 lag_seconds')

  const nullableRow = database.prepare("SELECT lag_seconds FROM stats_job_state WHERE scope_type = 'table_monitor' AND job_name = 'table_storage_snapshots'")
    .get() as { lag_seconds?: number | null } | undefined
  assert.equal(nullableRow?.lag_seconds, null, '表监控游标应能写入 NULL lag_seconds')

  console.log('stats_job_state lag_seconds nullable schema 回归通过')
} finally {
  try {
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
