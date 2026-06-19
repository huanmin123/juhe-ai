import { closeStorageDatabases, getBusinessDatabase } from '../../storage/database.js'
import { rebuildAccountNameSearchTerms } from '../../storage/account-name-search.repository.js'

const startedAt = Date.now()

try {
  const result = rebuildAccountNameSearchTerms(getBusinessDatabase())
  console.log(`AI 账户名称搜索词重建完成：账户 ${result.accountCount} 个，词项 ${result.termCount} 个，耗时 ${Date.now() - startedAt}ms`)
} finally {
  closeStorageDatabases()
}
