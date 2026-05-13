import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-list-stable-sort-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-list-stable-sort.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'account-list-stable-sort-records.sqlite3')
runtimeConfig.secret = 'account-list-stable-sort-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../storage/database.js'),
  import('../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const accounts = [
    createStableAccount('列表稳定排序-A', 'sk-list-stable-a', '2026-01-01T00:00:00.000Z'),
    createStableAccount('列表稳定排序-B', 'sk-list-stable-b', '2026-01-01T00:00:01.000Z'),
    createStableAccount('列表稳定排序-C', 'sk-list-stable-c', '2026-01-01T00:00:02.000Z')
  ]
  const expectedIds = accounts.map((account) => account.id)

  assert.deepEqual(listStableAccountIds(expectedIds), expectedIds, '同优先级账户初始列表顺序应按创建顺序稳定')

  repositories.updateAccount(accounts[1].id, { superPriorityEnabled: true }, access)
  assert.deepEqual(listStableAccountIds(expectedIds), expectedIds, '切换超级优先不应改变默认列表相对顺序')

  repositories.updateAccount(accounts[2].id, { fallbackEnabled: true }, access)
  assert.deepEqual(listStableAccountIds(expectedIds), expectedIds, '切换降级备用不应改变默认列表相对顺序')

  console.log('AI 账户列表稳定排序回归通过')
} finally {
  try {
    databaseModule.getDatabase().close()
      databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function createStableAccount(name: string, apiKey: string, createdAt: string): { id: string } {
  const account = repositories.createAccount({
    providerCode: 'openai',
    name,
    type: 'api_key',
    credentials: {
      api_key: apiKey,
      base_url: 'http://127.0.0.1:9/v1'
    },
    status: 'active',
    priority: 10,
    schedulable: true
  }, access)
  databaseModule.getDatabase()
    .prepare('UPDATE accounts SET created_at = ?, updated_at = ? WHERE id = ?')
    .run(createdAt, createdAt, account.id)
  return { id: account.id }
}

function listStableAccountIds(expectedIds: string[]): string[] {
  const expected = new Set(expectedIds)
  return repositories.listAccounts(access)
    .filter((account) => expected.has(account.id))
    .map((account) => account.id)
}
