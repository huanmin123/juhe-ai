import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

process.env.JUHE_AI_SQLITE_WRITER_BOUNDARY_STRICT = '0'

const tempRoot = resolve(tmpdir(), `juhe-ai-announcement-sqlite-worker-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'announcement-sqlite-worker-secret'
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
  const announcement = repositories.createAnnouncement({
    title: 'SQLite DB service 公告',
    content: 'SQLite DB service 公告完整正文',
    level: 'warning',
    status: 'published'
  }, 'sys_admin')

  assert.equal(readWorkerPool.sqliteReadWorkerPoolEnabled(), true, 'DB service + SQLite 必须启用公告 read worker')
  const handledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs

  const publicList = await repositories.listPublicAnnouncementsAsync('sys_admin', 30)
  const listItem = publicList.find((item) => item.id === announcement.id)
  assert(listItem, '公告轻量摘要应由 SQLite read worker 返回')
  assert.equal('content' in listItem, false, 'SQLite read worker 公告摘要不能返回正文')

  const detail = await repositories.findPublicAnnouncementAsync(announcement.id)
  assert.equal(detail?.content, 'SQLite DB service 公告完整正文', '公告正文详情应由 SQLite read worker 按 ID 返回')
  const editDetail = await repositories.findAnnouncementEditDetailAsync(announcement.id)
  assert.deepEqual(
    Object.keys(editDetail ?? {}).sort(),
    ['id', 'title', 'content', 'level', 'status', 'revision'].sort(),
    '公告管理编辑详情应由 read worker 返回专用窄投影'
  )
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs >= handledJobsBefore + 3,
    '公告摘要、公共详情和管理编辑详情必须分别进入 SQLite read worker'
  )

  repositories.unpublishAnnouncement(announcement.id, 'sys_admin')
  assert.equal(await repositories.findPublicAnnouncementAsync(announcement.id), undefined, '下线公告通过 SQLite read worker 公共详情应返回空')

  console.log('公告 SQLite DB service read-worker 回归通过：摘要与正文详情均在读取进程执行')
} finally {
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}
