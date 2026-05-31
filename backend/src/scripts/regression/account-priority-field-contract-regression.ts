import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-priority-contract-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-priority-contract.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-priority-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }

try {
  const typoOnly = repositories.createAccount({
    providerCode: 'openai',
    name: '优先级拼写残留创建检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-create',
      base_url: 'http://127.0.0.1:9/v1'
    },
    prioritiy: 99
  }, access)
  assert.equal(typoOnly.priority, 0, '拼错字段 prioritiy 不应在创建账户时生效')

  const account = repositories.createAccount({
    providerCode: 'openai',
    name: '优先级拼写残留更新检查',
    type: 'api_key',
    credentials: {
      api_key: 'sk-priority-contract-update',
      base_url: 'http://127.0.0.1:9/v1'
    },
    priority: 7
  }, access)
  assert.equal(account.priority, 7, '当前 priority 字段应正常生效')

  const typoUpdated = repositories.updateAccount(account.id, { prioritiy: 66 }, access)
  assert.equal(typoUpdated?.priority, 7, '拼错字段 prioritiy 不应在更新账户时覆盖优先级')

  const updated = repositories.updateAccount(account.id, { priority: 8 }, access)
  assert.equal(updated?.priority, 8, '当前 priority 字段应仍可更新优先级')

  console.log('账户优先级字段契约回归通过：拼错字段 prioritiy 不再影响创建/更新')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
