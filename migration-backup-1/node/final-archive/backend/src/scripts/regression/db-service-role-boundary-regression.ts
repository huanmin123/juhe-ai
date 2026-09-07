import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

const tempRoot = resolve(tmpdir(), `juhe-ai-db-service-role-boundary-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const databasePath = join(tempRoot, 'role-boundary.sqlite3')
const chatDatabasePath = join(tempRoot, 'chat.sqlite3')
const datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
const usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
const statsDatabasePath = join(tempRoot, 'stats.sqlite3')
const usageShardRoot = join(tempRoot, 'usage-shards')
const codexContextRoot = join(tempRoot, 'codex-context')
const codexContextStateShardRoot = join(tempRoot, 'codex-context-state-shards')
mkdirSync(tempRoot, { recursive: true })

Object.assign(process.env, {
  JUHE_AI_PROCESS_ROLE: 'worker',
  JUHE_AI_DATABASE_DRIVER: 'sqlite',
  JUHE_AI_DATABASE_PATH: databasePath,
  JUHE_AI_CHAT_DATABASE_PATH: chatDatabasePath,
  JUHE_AI_DATASET_DATABASE_PATH: datasetDatabasePath,
  JUHE_AI_USAGE_CATALOG_DATABASE_PATH: usageCatalogDatabasePath,
  JUHE_AI_STATS_DATABASE_PATH: statsDatabasePath,
  JUHE_AI_USAGE_SHARD_ROOT: usageShardRoot,
  JUHE_AI_USAGE_SHARD_COUNT: '2',
  JUHE_AI_USAGE_SPOOL_DIR: join(tempRoot, 'usage-spool'),
  JUHE_AI_CODEX_CONTEXT_ROOT: codexContextRoot,
  JUHE_AI_CHAT_ASSETS_ROOT: join(tempRoot, 'chat-assets'),
  JUHE_AI_OPENAI_COMPATIBLE_FILES_ROOT: join(tempRoot, 'openai-compatible-files'),
  JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: codexContextStateShardRoot,
  JUHE_AI_CODEX_CONTEXT_STATE_SHARD_COUNT: '2',
  JUHE_AI_CODE_INTERPRETER_TEMP_ROOT: join(tempRoot, 'code-interpreter'),
  JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE: '2',
  JUHE_AI_SECRET: 'db-service-role-boundary-secret',
  JUHE_AI_LOG_DIR: join(tempRoot, 'logs'),
  JUHE_AI_LOG_CONSOLE_ENABLED: 'false',
  JUHE_AI_LOG_FILE_ENABLED: 'false'
})

const [
  { runtimeConfig },
  { logger }
] = await Promise.all([
  import('../../config/runtime.js'),
  import('../../shared/logger.js')
])

runtimeConfig.processRole = 'worker'
logger.level = 'silent'

const [
  { requestDbService },
  databaseModule,
  repositories,
  { closeSqliteReadWorkerPool }
] = await Promise.all([
  import('../../modules/db-service/db-service-ipc.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

async function main(): Promise<void> {
  try {
    await assert.rejects(
      requestDbService({ type: 'status' }),
      /worker 角色不能本地执行 DB service 操作/,
      'worker 角色不能绕过 DB service IPC 本地执行 requestDbService'
    )

    runtimeConfig.processRole = 'db-service'
    assert.equal(
      repositories.listPublicGlobalSettings().appName,
      '聚合 AI',
      '断言前置数据库本身可读，并在只读 worker 启动前完成当前 schema 初始化'
    )
    const status = await requestDbService({ type: 'status' })
    assert(status.ready === true, 'db-service 角色下 DB service 本地状态读取应成功')

    const settings = await requestDbService({ type: 'read_gateway_settings' })
    assert(typeof settings.textFirstResponseTimeoutSeconds === 'number', 'db-service 角色下 DB service 本地读取网关设置应成功')

    const previousProcessRole = runtimeConfig.processRole
    runtimeConfig.processRole = 'server'
    try {
      await assert.rejects(
        requestDbService({ type: 'status' }),
        /(?:DB service|本地数据库服务)(暂时不可用|未就绪|请求超时|已退出)/,
        'server 角色未挂载 DB service 子进程时不能本地执行 requestDbService'
      )
    } finally {
      runtimeConfig.processRole = previousProcessRole
    }

    console.log('DB service 角色边界回归通过：只有 db-service 可本地执行，worker 和无 IPC 子进程的 server 均拒绝本地执行')
  } finally {
    await closeSqliteReadWorkerPool().catch(() => undefined)
    try {
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

main().catch((error) => {
  console.error('\nDB service 角色边界回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
