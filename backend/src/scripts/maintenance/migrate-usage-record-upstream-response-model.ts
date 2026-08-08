process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_WORKER_ROLE = 'temporary-maintenance-worker'

const { runtimeConfig } = await import('../../config/runtime.js')
const offlineConfirmed = process.argv.includes('--confirm-offline')
  || process.env.JUHE_AI_CONFIRM_USAGE_RECORD_MODEL_MIGRATION === '1'
if (runtimeConfig.databaseDriver === 'sqlite' && offlineConfirmed) {
  process.env.JUHE_AI_SQLITE_OFFLINE_MAINTENANCE = '1'
}

const [databaseModule, databaseClientModule, postgresModule, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/database-client.js'),
  import('../../storage/postgres-client.js'),
  import('../../storage/usage-record-shards.js')
])
const { closeStorageDatabases } = databaseModule
const { createPostgresDatabaseClient } = databaseClientModule
const { closePostgresPool, getPostgresPool } = postgresModule

async function main(): Promise<void> {
  if (process.argv.includes('--help') || process.argv.includes('-h')) {
    printHelp()
    return
  }
  if (!offlineConfirmed) {
    throw new Error('升级使用记录上游响应模型字段必须显式确认停服/离线执行：追加 --confirm-offline，或设置 JUHE_AI_CONFIRM_USAGE_RECORD_MODEL_MIGRATION=1')
  }

  if (runtimeConfig.databaseDriver === 'postgres') {
    const client = createPostgresDatabaseClient(await getPostgresPool())
    await client.execute('ALTER TABLE juhe_usage.usage_records ADD COLUMN IF NOT EXISTS upstream_response_model text')
    console.log('PostgreSQL 使用记录已补齐 upstream_response_model 字段')
    return
  }

  const locations = usageRecordShards.listUsageRecordShardLocations()
  for (const location of locations) {
    usageRecordShards.getUsageRecordShardDatabase(location)
  }
  console.log(`SQLite 使用记录 shard 已补齐 upstream_response_model 字段：${locations.length} 个`)
}

function printHelp(): void {
  console.log(`
用法：
  pnpm --filter juhe-ai-backend maintenance:migrate-usage-response-model -- --confirm-offline

说明：
  - 必须停服或确认没有网关 / worker 写入后离线执行。
  - SQLite 会逐个补齐已注册 usage shard 的可空字段，不修改历史记录值。
  - PostgreSQL 会对 juhe_usage.usage_records 执行幂等列升级。
`)
}

void main()
  .catch((error) => {
    console.error(error instanceof Error ? error.message : error)
    process.exitCode = 1
  })
  .finally(async () => {
    closeStorageDatabases()
    await closePostgresPool()
  })
