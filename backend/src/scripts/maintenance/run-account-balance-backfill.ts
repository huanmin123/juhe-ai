process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'temporary-maintenance-worker'

const { runtimeConfig } = await import('../../config/runtime.js')

if (runtimeConfig.databaseDriver === 'sqlite') {
  const confirmed = process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE_CONFIRMED?.trim().toLowerCase()
  if (!confirmed || !['1', 'true', 'yes', 'on'].includes(confirmed)) {
    throw new Error('SQLite 全量余额扫描必须先停止主服务，并设置 JUHE_AI_SQLITE_OFFLINE_MAINTENANCE_CONFIRMED=1')
  }
  process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE = '1'
}

await import('./backfill-account-balance-auto-detect.js')
