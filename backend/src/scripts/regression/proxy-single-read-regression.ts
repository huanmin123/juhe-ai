import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-proxy-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'proxy-single-read-secret'
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
    const proxy = repositories.createProxy({
      name: `代理单条读取回归-${String(index).padStart(3, '0')}`,
      type: 'http',
      host: '127.0.0.1',
      port: 10_000 + index,
      enabled: true
    })
    if (index === 0) {
      targetId = proxy.id
    }
  }

  databaseModule.getBusinessDatabase()
    .prepare("UPDATE proxy_profiles SET updated_at = '2000-01-01T00:00:00.000Z' WHERE id = ?")
    .run(targetId)

  const firstPageLikeList = repositories.listProxies().slice(0, 200)
  assert.equal(firstPageLikeList.some((proxy) => proxy.id === targetId), false, '最早创建的第 250 条外代理不应出现在前 200 条列表窗口里')

  const target = repositories.findProxy(targetId)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的代理')
  assert.equal(target?.name, '代理单条读取回归-000', '按 ID 单条读取应返回完整代理摘要')

  const updated = repositories.updateProxy(targetId, { description: '已通过单条读取更新' })
  assert.equal(updated?.description, '已通过单条读取更新', '更新代理应通过单条读取返回目标代理摘要')

  const tested = repositories.updateProxyTestState(targetId, {
    testStatus: 'passed',
    latencyMs: 12,
    lastTestMessage: '单条读取检测通过'
  })
  assert.equal(tested?.testStatus, 'passed', '更新代理检测状态应通过单条读取返回目标代理摘要')
  assert.equal(tested?.latencyMs, 12, '更新代理检测状态应保留延迟')

  assert.equal(repositories.deleteProxy(targetId), true, '删除代理应成功')
  assert.equal(repositories.findProxy(targetId), undefined, '删除后按 ID 单条读取应找不到代理')

  console.log('代理单条读取回归通过：更新、检测状态和删除日志 before 不再依赖全量代理列表')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
