import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-api-key-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'records.sqlite3')
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
  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const apiKey = repositories.createApiKeyRecord({
      name: `单条读取回归-${String(index).padStart(3, '0')}`,
      status: 'active'
    }, access)
    if (index === 0) {
      targetId = apiKey.id
    }
  }

  const firstPage = repositories.listApiKeysPage(access, { limit: 200 })
  assert.equal(firstPage.items.some((apiKey) => apiKey.id === targetId), false, '最早创建的第 250 条外 API Key 不应出现在前 200 条列表里')

  const target = repositories.findApiKeySummary(targetId, access)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的 API Key')
  assert.equal(target?.name, '单条读取回归-000', '按 ID 单条读取应返回完整摘要供操作日志使用')

  console.log('API Key 单条读取回归通过：更新/删除日志 before 不再依赖前 200 条列表')
} finally {
  try {
    databaseModule.getDatabase().close()
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
