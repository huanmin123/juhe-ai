process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'temporary-maintenance-worker'

const { runtimeConfig } = await import('../../config/runtime.js')

if (runtimeConfig.databaseDriver === 'sqlite') {
  const confirmed = process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE_CONFIRMED?.trim().toLowerCase()
  if (!confirmed || !['1', 'true', 'yes', 'on'].includes(confirmed)) {
    throw new Error('SQLite 操作日志搜索词重建必须先停止主服务，并设置 JUHE_AI_SQLITE_OFFLINE_MAINTENANCE_CONFIRMED=1')
  }
  process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE = '1'
}

const [{ closeStorageDatabases }, { closePostgresPool }, maintenance] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/postgres-client.js'),
  import('../../storage/operation-log-search-maintenance.js')
])
const startedAt = Date.now()

try {
  const result = runtimeConfig.databaseDriver === 'postgres'
    ? await maintenance.rebuildOperationLogSearchTermsPostgres()
    : await maintenance.rebuildOperationLogSearchTermsSqlite()
  console.log(`操作日志摘要搜索词重建完成：日志 ${result.logCount} 条，词项 ${result.termCount} 个，批次 ${result.batchCount} 个，耗时 ${Date.now() - startedAt}ms`)
} finally {
  closeStorageDatabases()
  await closePostgresPool()
}
