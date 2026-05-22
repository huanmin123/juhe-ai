import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'group-single-read-secret'
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
  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const group = repositories.createGroup({
      name: `分组单条读取回归-${String(index).padStart(3, '0')}`,
      providerCode: 'openai',
      enabled: true
    }, access)
    if (index === 0) {
      targetId = group.id
    }
  }

  const firstPageLikeList = repositories.listGroups(access).slice(0, 200)
  assert.equal(firstPageLikeList.some((group) => group.id === targetId), false, '最早创建的第 250 条外分组不应出现在前 200 条列表窗口里')

  const target = repositories.findGroupSummary(targetId, access)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的分组')
  assert.equal(target?.name, '分组单条读取回归-000', '按 ID 单条读取应返回完整分组摘要')

  const updated = repositories.updateGroup(targetId, { description: '已通过单条读取更新' }, access)
  assert.equal(updated?.description, '已通过单条读取更新', '更新分组应通过单条读取返回目标分组摘要')

  console.log('分组单条读取回归通过：写路径不再依赖全量分组列表装配')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
