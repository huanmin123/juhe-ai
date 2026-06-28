import { strict as assert } from 'node:assert'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-announcement-single-read-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'announcement-single-read-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const announcementsRoutesSource = readFileSync(resolve('src/modules/announcements/announcements.routes.ts'), 'utf8')
assert.match(announcementsRoutesSource, /listPublicAnnouncementsAsync/, '公告公开列表路由必须使用 async repository')
assert.match(announcementsRoutesSource, /markPublicAnnouncementsReadAsync/, '公告已读路由必须使用 async repository')
assert.match(announcementsRoutesSource, /listAnnouncementsPageAsync/, '公告管理列表路由必须使用 async repository')
assert.match(announcementsRoutesSource, /findAnnouncementAsync/, '公告详情和变更 before 读取必须使用 async repository')
assert.match(announcementsRoutesSource, /createAnnouncementAsync/, '公告创建路由必须使用 async repository')
assert.match(announcementsRoutesSource, /updateAnnouncementAsync/, '公告更新路由必须使用 async repository')
assert.match(announcementsRoutesSource, /deleteAnnouncementAsync/, '公告删除路由必须使用 async repository')
assert.match(announcementsRoutesSource, /runLoggedOperationAsync/, '公告管理操作日志必须使用 async 包裹')
assert.doesNotMatch(announcementsRoutesSource, /import \{[^}]*\bfindAnnouncement\b[^}]*\} from '..\/..\/storage\/repositories\.js'/, '公告路由不能重新导入同步 findAnnouncement')
assert.doesNotMatch(announcementsRoutesSource, /import \{[^}]*\bcreateAnnouncement\b[^}]*\} from '..\/..\/storage\/repositories\.js'/, '公告路由不能重新导入同步 createAnnouncement')

const actor = 'sys_admin'

try {
  let targetId = ''
  for (let index = 0; index < 250; index += 1) {
    const announcement = repositories.createAnnouncement({
      title: `公告单条读取回归-${String(index).padStart(3, '0')}`,
      content: `公告内容-${index}`,
      level: 'info',
      status: 'draft'
    }, actor)
    if (index === 0) {
      targetId = announcement.id
    }
  }
  const largeContent = Array.from({ length: 80 }, (_, index) => `公告长内容-${index}`).join('\n')
  const largeAnnouncement = repositories.createAnnouncement({
    title: '公告列表预览回归',
    content: largeContent,
    level: 'info',
    status: 'draft'
  }, actor)

  databaseModule.getBusinessDatabase()
    .prepare("UPDATE announcements SET created_at = '2000-01-01T00:00:00.000Z', updated_at = '2000-01-01T00:00:00.000Z' WHERE id = ?")
    .run(targetId)

  assertAnnouncementListQueryPlan()
  const firstPageLikeList = repositories.listAnnouncementsPage({ page: 1, pageSize: 200 }).items
  assert.equal(firstPageLikeList.some((announcement) => announcement.id === targetId), false, '最早创建的第 250 条外公告不应出现在前 200 条列表窗口里')
  const largeListItem = firstPageLikeList.find((announcement) => announcement.id === largeAnnouncement.id)
  assert(largeListItem, '列表应返回公告摘要')
  assert(largeListItem.content.endsWith('...') && largeListItem.content.length < largeContent.length, '公告列表应只返回内容预览')

  const target = repositories.findAnnouncement(targetId)
  assert.equal(target?.id, targetId, '按 ID 单条读取应能找到前 200 条之外的公告')
  assert.equal(target?.title, '公告单条读取回归-000', '按 ID 单条读取应返回完整公告摘要')
  assert.equal(repositories.findAnnouncement(largeAnnouncement.id)?.content, largeContent, '公告详情应返回完整内容')

  const updated = repositories.updateAnnouncement(targetId, { content: '已通过单条读取更新' }, actor)
  assert.equal(updated?.content, '已通过单条读取更新', '更新公告应通过单条读取返回目标公告摘要')

  const published = repositories.publishAnnouncement(targetId, actor)
  assert.equal(published?.status, 'published', '发布公告应返回目标公告摘要')
  assert(published?.publishedAt, '发布公告应写入发布时间')

  const unpublished = repositories.unpublishAnnouncement(targetId, actor)
  assert.equal(unpublished?.status, 'archived', '下线公告应返回目标公告摘要')

  assert.equal(repositories.deleteAnnouncement(targetId), true, '删除公告应成功')
  assert.equal(repositories.findAnnouncement(targetId), undefined, '删除后按 ID 单条读取应找不到公告')

  console.log('公告单条读取回归通过：更新、发布、下线和删除日志 before 不再依赖全量公告列表')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertAnnouncementListQueryPlan(): void {
  const details = explainBusinessQuery(`
    SELECT id
    FROM announcements
    ORDER BY updated_at DESC, created_at DESC, id DESC
    LIMIT ? OFFSET ?
  `, [51, 0])
  assert(details.includes('idx_announcements_admin_page'), `公告管理列表应使用最近更新分页索引，实际计划：${details}`)
  assert(!details.includes('USE TEMP B-TREE FOR ORDER BY'), `公告管理列表不应为默认排序创建临时 B-TREE，实际计划：${details}`)
}

function explainBusinessQuery(sql: string, params: SQLInputValue[]): string {
  return databaseModule.getBusinessDatabase()
    .prepare(`EXPLAIN QUERY PLAN ${sql}`)
    .all(...params)
    .map((row) => String((row as { detail?: unknown }).detail ?? ''))
    .join('\n')
}
