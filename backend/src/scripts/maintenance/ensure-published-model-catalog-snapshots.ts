import { closeStorageDatabases } from '../../storage/database.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { ensurePublishedModelCatalogSnapshotsInitializedAsync } from '../../modules/model-pricing/published-model-catalog.service.js'

const startedAt = Date.now()

async function main(): Promise<void> {
  const result = await ensurePublishedModelCatalogSnapshotsInitializedAsync()
  const message = result.action === 'unchanged'
    ? '已存在持久化发布模型目录，未执行重建'
    : `发布模型目录首次初始化完成：目录归属 ${result.snapshotOwners} 个`
  console.log(`${message}，模型 ${result.modelCount} 个，耗时 ${Date.now() - startedAt}ms`)
}

main()
  .catch((error) => {
    console.error(error instanceof Error ? error.message : '发布模型目录首次初始化失败')
    process.exitCode = 1
  })
  .finally(async () => {
    await closeRedisClients()
    closeStorageDatabases()
    await closePostgresPool()
  })
