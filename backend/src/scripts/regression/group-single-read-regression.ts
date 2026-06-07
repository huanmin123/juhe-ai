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
      providerCode: 'gpt',
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

  const groupCountBeforeInvalidCreate = repositories.listGroups(access).length
  assert.throws(() => repositories.createGroup({
    name: '分组单条读取回归-非法启用状态',
    providerCode: 'gpt',
    enabled: 'false'
  }, access), /分组启用状态必须是布尔值/, '创建分组时字符串布尔不应被兼容为启用状态')
  assert.equal(repositories.listGroups(access).length, groupCountBeforeInvalidCreate, '非法创建分组不应落库')

  const updated = repositories.updateGroup(targetId, { description: '已通过单条读取更新' }, access)
  assert.equal(updated?.description, '已通过单条读取更新', '更新分组应通过单条读取返回目标分组摘要')

  assert.throws(() => repositories.updateGroup(targetId, { name: '' }, access), /分组名称不能为空/, '更新分组时空名称不应静默沿用旧名称')
  assert.throws(() => repositories.updateGroup(targetId, { providerCode: 123 }, access), /供应商不能为空/, '更新分组时非法供应商字段不应静默沿用旧供应商')
  assert.throws(() => repositories.updateGroup(targetId, { enabled: 'true' }, access), /分组启用状态必须是布尔值/, '更新分组时字符串布尔不应静默沿用旧启用状态')
  const afterInvalidUpdate = repositories.findGroupSummary(targetId, access)
  assert.equal(afterInvalidUpdate?.name, updated?.name, '非法更新分组名称不应改变')
  assert.equal(afterInvalidUpdate?.providerCode, updated?.providerCode, '非法更新分组供应商不应改变')
  assert.equal(afterInvalidUpdate?.enabled, updated?.enabled, '非法更新分组启用状态不应改变')

  console.log('分组单条读取回归通过：写路径不再依赖全量分组列表装配')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
