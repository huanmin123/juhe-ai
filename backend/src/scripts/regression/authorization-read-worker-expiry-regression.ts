import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-read-worker-expiry-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-read-worker-expiry-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.workerRole = 'worker'
runtimeConfig.sqliteReadWorkerPoolSize = 1
runtimeConfig.sqliteReadWorkerQueueMaxItems = 8
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
    name: '授权read-worker到期分组',
    providerCode: 'gpt',
    enabled: true
  }, access)
  const grantee = repositories.createSystemAccount({
    username: 'authorization_read_worker_expiry_grantee',
    displayName: '授权read-worker到期接收者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const authorization = repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    expiresAt: '2099-01-01T00:00:00.000Z'
  }, access)
  databaseModule.getBusinessDatabase().prepare(`
    UPDATE resource_authorization_grants
    SET expires_at = '2000-01-01T00:00:00.000Z', status = 'active'
    WHERE id = ?
  `).run(authorization.id)

  assert.equal(readWorkerPool.sqliteReadWorkerPoolEnabled(), true, '授权到期回归必须启用真实 SQLite read worker')
  const handledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const page = await repositories.listResourceAuthorizationsPageAsync(
    { id: authorization.id, status: 'all' },
    access,
    { page: 1, pageSize: 10, includeUsage: false }
  )
  assert.equal(page.items[0]?.status, 'expired', 'read worker 授权列表投递前应完成到期扫描，不能返回伪 active')
  const persisted = databaseModule.getBusinessDatabase()
    .prepare('SELECT status FROM resource_authorization_grants WHERE id = ?')
    .get(authorization.id) as unknown as { status?: string } | undefined
  assert.equal(persisted?.status, 'expired', '授权到期扫描应由主 DB service 写回状态')
  const workerRuntime = readWorkerPool.getSqliteReadWorkerPoolRuntime()
  assert(workerRuntime.workerCount > 0, '授权列表读取应启动 query-only worker')
  assert(workerRuntime.handledJobs > handledJobsBefore, '授权列表到期处理后仍应由 read worker 执行列表查询')

  console.log('授权 SQLite read worker 到期回归通过：主 DB service 有界写回 expired，query-only worker 返回轻量列表')
} finally {
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  await removeTempRoot()
}

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (attempt === 4) throw error
      await delay(100 * (attempt + 1))
    }
  }
}
