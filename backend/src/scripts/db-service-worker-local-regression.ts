import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-worker-local-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'worker-local.sqlite3')
runtimeConfig.secret = 'db-service-worker-local-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { requestDbService },
  databaseModule
] = await Promise.all([
  import('../modules/db-service/db-service-ipc.js'),
  import('../storage/database.js')
])

async function main(): Promise<void> {
  try {
    const status = await requestDbService({ type: 'status' }, { fallbackToLocal: false })
    assert(status.ready === true, 'worker 角色下 DB service 本地状态读取应成功')

    const settings = await requestDbService({ type: 'read_gateway_settings' }, { fallbackToLocal: false })
    assert(typeof settings.streamRequestTimeoutSeconds === 'number', 'worker 角色下 DB service 本地读取网关设置应成功')

    console.log('DB service worker 本地执行回归通过：worker 角色无需 IPC 子进程也能执行 requestDbService')
  } finally {
    try {
      databaseModule.getDatabase().close()
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
