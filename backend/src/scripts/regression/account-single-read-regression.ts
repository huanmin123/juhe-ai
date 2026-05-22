import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-single-read-secret'
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
  const group = repositories.createGroup({
    name: '账户单条读取回归分组',
    providerCode: 'openai',
    enabled: true
  }, access)

  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: `账户单条读取回归-${String(index).padStart(3, '0')}`,
      type: 'api_key',
      credentials: {
        api_key: `sk-account-single-read-${index}`,
        base_url: 'https://api.openai.com/v1'
      },
      groupId: group.id,
      status: 'disabled'
    }, access)
    if (index === 249) {
      targetId = account.id
    }
  }

  const firstPage = repositories.listAccountsPage(access, { limit: 200 })
  assert.equal(firstPage.items.some((account) => account.id === targetId), false, '第 250 个创建的停用账户不应出现在默认前 200 条列表里')

  const target = repositories.findAccountSummary(targetId, access)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的停用账户')
  assert.equal(target?.name, '账户单条读取回归-249', '按 ID 单条读取应返回完整账户摘要')
  assert.equal(target?.status, 'disabled', '按 ID 单条读取应可用于删除日志 before 的停用账户快照')

  assert.equal(repositories.deleteAccount(targetId, access), true, '删除第 200 条之外的停用账户应成功')
  assert.equal(repositories.findAccountSummary(targetId, access), undefined, '删除后按 ID 单条读取应找不到账户')

  console.log('账户单条读取回归通过：删除日志 before 不再依赖前 200 条列表或测试用读取')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
