import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-worker-local-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'worker-local.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'db-service-worker-local-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { requestDbService },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

async function main(): Promise<void> {
  try {
    const status = await requestDbService({ type: 'status' })
    assert(status.ready === true, 'worker 角色下 DB service 本地状态读取应成功')

    const settings = await requestDbService({ type: 'read_gateway_settings' })
    assert(typeof settings.streamRequestTimeoutSeconds === 'number', 'worker 角色下 DB service 本地读取网关设置应成功')
    assert.equal(repositories.listPublicGlobalSettings().appName, '聚合 AI', '断言前置数据库本身可读，失败路径不是因为库未初始化')

    const previousProcessRole = runtimeConfig.processRole
    runtimeConfig.processRole = 'server'
    try {
      await assert.rejects(
        requestDbService({ type: 'status' }),
        /DB service (暂时不可用|未就绪|请求队列已满|请求超时|已退出)/,
        'server 角色未挂载 DB service 子进程时不能本地执行 requestDbService'
      )
    } finally {
      runtimeConfig.processRole = previousProcessRole
    }

    console.log('DB service 本地执行边界回归通过：worker 可本地执行，server 无 IPC 子进程时拒绝本地执行')
  } finally {
    try {
      databaseModule.getDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

main().catch((error) => {
  console.error('\nDB service worker 本地执行回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
