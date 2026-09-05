import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-usage-stats-lag-unavailable-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'usage-stats-lag-unavailable-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, usageStatsHelpers] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/usage-stats-runtime-helpers.js')
])

try {
  const statsDatabase = databaseModule.getStatsDatabase()
  assert.equal(usageStatsHelpers.latestUsageStatsLagSeconds(), undefined, '缺少 job state 时应返回未知，而不是 0')

  const now = new Date().toISOString()
  statsDatabase.prepare(`
    INSERT INTO stats_job_state (
      scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
  `).run(
    'global',
    '',
    'usage_stats_aggregation',
    null,
    null,
    null,
    null,
    null,
    now
  )
  assert.equal(usageStatsHelpers.latestUsageStatsLagSeconds(), undefined, 'lag 为空时应继续保持未知')

  statsDatabase.prepare(`
    UPDATE stats_job_state
    SET lag_seconds = ?, updated_at = ?
    WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'
  `).run(37, now)
  assert.equal(usageStatsHelpers.latestUsageStatsLagSeconds(), 37, 'lag 有值时应原样返回')

  console.log('用量统计 lag 回归通过：缺少状态时返回 undefined，不再伪装成 0')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
