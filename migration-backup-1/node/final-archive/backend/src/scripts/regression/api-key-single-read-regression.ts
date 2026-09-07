import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'api-key-single-read-secret'
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
    name: 'API Key 单条读取回归分组',
    providerCode: 'gpt'
  }, access)
  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: `单条读取回归-${String(index).padStart(3, '0')}`,
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    if (index === 0) {
      targetId = apiKey.id
    }
  }

  const firstPage = repositories.listApiKeysPage(access, { page: 1, pageSize: 200 })
  assert.equal(firstPage.items.some((apiKey) => apiKey.id === targetId), false, '最早创建的第 250 条外 API Key 不应出现在前 200 条列表里')

  const target = repositories.findApiKeySummary(targetId, access)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的 API Key')
  assert.equal(target?.name, '单条读取回归-000', '按 ID 单条读取应返回完整摘要供操作日志使用')
  assert.equal(Object.hasOwn(target ?? {}, 'key'), false, '按 ID 单条读取不应返回密钥字段')

  const updated = repositories.updateApiKey(targetId, {
    description: '单条读取回归更新'
  }, access)
  assert.equal(updated?.id, targetId, '更新 API Key 应返回目标记录')
  assert.equal(Object.hasOwn(updated ?? {}, 'key'), false, '更新 API Key 不应返回密钥字段')

  console.log('API Key 单条读取回归通过：更新/删除日志 before 不再依赖前 200 条列表')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
