import { aggregateUsageStatsBatch } from '../storage/usage-stats.repository.js'
import { getDatabase, nowIso } from '../storage/database.js'
import { runtimeConfig } from '../config/runtime.js'

const batchSize = normalizeBatchSize(process.argv[2])

function main(): void {
  const database = getDatabase()
  const startedAt = Date.now()
  resetUsageStatsCache(database)

  let totalProcessed = 0
  while (true) {
    const processed = aggregateUsageStatsBatch(batchSize)
    totalProcessed += processed
    if (processed <= 0) {
      break
    }
  }

  const durationMs = Date.now() - startedAt
  console.log(`Usage stats rebuilt: ${totalProcessed} records scanned in ${durationMs}ms`)
  console.log(`Database: ${runtimeConfig.databasePath}`)
}

function resetUsageStatsCache(database: ReturnType<typeof getDatabase>): void {
  const updatedAt = nowIso()
  database.exec('BEGIN')
  try {
    database.prepare('DELETE FROM usage_stats_totals').run()
    database.prepare('DELETE FROM usage_stats_daily').run()
    database.prepare('DELETE FROM usage_stats_hourly').run()
    database.prepare('DELETE FROM usage_model_daily').run()
    database.prepare('DELETE FROM usage_model_hourly').run()
    database.prepare('DELETE FROM usage_error_daily').run()
    database.prepare('DELETE FROM usage_error_hourly').run()
    database.prepare("DELETE FROM stats_job_state WHERE scope_type = 'global' AND scope_id = '' AND job_name = 'usage_stats_aggregation'").run()
    database.prepare(`
      INSERT INTO stats_job_state (scope_type, scope_id, job_name, cursor_created_at, cursor_id, last_success_at, last_error_message, lag_seconds, updated_at)
      VALUES ('global', '', 'usage_stats_aggregation', '', '', NULL, NULL, 0, ?)
    `).run(updatedAt)
    database.exec('COMMIT')
  } catch (error) {
    database.exec('ROLLBACK')
    throw error
  }
}

function normalizeBatchSize(value?: string): number {
  const number = Number(value ?? 2000)
  return Number.isFinite(number) ? Math.min(Math.max(Math.trunc(number), 1), 50000) : 2000
}

main()
