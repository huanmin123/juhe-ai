import { runtimeConfig } from '../../config/runtime.js'
import { closePostgresPool } from '../../storage/postgres-client.js'
import { closeStorageDatabases, getBusinessDatabase } from '../../storage/database.js'
import { rebuildAccountNameSearchTerms, rebuildAccountNameSearchTermsAsync } from '../../storage/account-name-search.repository.js'

const startedAt = Date.now()

async function main(): Promise<void> {
  const result = runtimeConfig.databaseDriver === 'postgres'
    ? await rebuildAccountNameSearchTermsAsync()
    : rebuildAccountNameSearchTerms(getBusinessDatabase())
  console.log(`AI 账户名称搜索词重建完成：账户 ${result.accountCount} 个，词项 ${result.termCount} 个，耗时 ${Date.now() - startedAt}ms`)
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
