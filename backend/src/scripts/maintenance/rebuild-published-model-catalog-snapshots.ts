import { closeStorageDatabases } from '../../storage/database.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import { rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync } from '../../modules/model-pricing/published-model-catalog.service.js'

const startedAt = Date.now()

async function main(): Promise<void> {
  const snapshotOwners = await rebuildPublishedModelCatalogSnapshotsAfterModelChangeAsync()
  console.log(`发布模型目录快照重建完成：目录归属 ${snapshotOwners} 个，耗时 ${Date.now() - startedAt}ms`)
}

main()
  .catch((error) => {
    console.error(error instanceof Error ? error.message : error)
    process.exitCode = 1
  })
  .finally(async () => {
    closeStorageDatabases()
    await closePostgresPool()
  })
