import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-account-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-account-single-read-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

try {
  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const account = repositories.createSystemAccount({
      username: `system_account_single_read_${String(index).padStart(3, '0')}`,
      displayName: `系统账号单条读取回归-${String(index).padStart(3, '0')}`,
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    })
    if (index === 249) {
      targetId = account.id
    }
  }

  const firstPageLikeList = repositories.listSystemAccounts().slice(0, 200)
  assert.equal(firstPageLikeList.some((account) => account.id === targetId), false, '第 250 个创建的系统账号不应出现在前 200 条列表窗口里')

  const target = repositories.findSystemAccountById(targetId)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的系统账号')
  assert.equal(target?.displayName, '系统账号单条读取回归-249', '按 ID 单条读取应返回完整系统账号摘要')

  const updated = repositories.updateSystemAccount(targetId, { description: '已通过单条读取更新' })
  assert.equal(updated?.description, '已通过单条读取更新', '更新系统账号应通过单条读取返回目标账号摘要')

  console.log('系统账号单条读取回归通过：更新日志 before 不再依赖全量系统账号列表')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
