import { strict as assert } from 'node:assert'
import { existsSync, mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-group-summary-stats-total-${Date.now()}-${Math.random().toString(16).slice(2)}`)

runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context')
runtimeConfig.secret = 'group-summary-stats-total-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.sqliteReadWorkerPoolSize = 2
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, readWorkerPool] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

try {
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const group = repositories.createGroup({
    name: '缺 stats 库 total 回归分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '缺 stats 库 total 回归账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-group-summary-stats-total',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: group.id,
    status: 'active'
  }, access)

  assert.equal(existsSync(runtimeConfig.statsDatabasePath), false, '回归前 stats DB 应不存在，模拟本地首次启动缺统计库')
  const handledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const page = await repositories.listGroupsPageAsync(access, { page: 1, pageSize: 20 })
  const summary = page.items.find((item) => item.id === group.id)
  assert(summary, '分组列表应返回新建分组')
  assert(summary.accountIds.includes(account.id), '分组业务绑定账号 ID 应来自业务库')
  assert.equal(summary.accountStats.total, 1, 'stats 库缺失时 accountStats.total 仍必须等于业务绑定账号数')
  assert(readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs > handledJobsBefore, '分组列表 async 读应由 SQLite read worker 执行')

  console.log('分组 summary stats total 回归通过：SQLite read worker 缺 stats 库时仍返回真实绑定账号数量')
} finally {
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
