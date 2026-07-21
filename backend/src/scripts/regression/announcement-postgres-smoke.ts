import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import {
  createAnnouncementAsync,
  deleteAnnouncementAsync,
  findAnnouncementAsync,
  findPublicAnnouncementAsync,
  listAnnouncementsPageAsync,
  listPublicAnnouncementsAsync,
  markPublicAnnouncementsReadAsync,
  publishAnnouncementAsync,
  unpublishAnnouncementAsync,
  updateAnnouncementAsync
} from '../../storage/repositories.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '公告 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `ann_pg_${Date.now().toString(36)}_${Math.random().toString(16).slice(2, 8)}`
const actor = 'sys_admin'
const createdAnnouncementIds: string[] = []

try {
  const created = await createAnnouncementAsync({
    title: `公告 PG smoke ${marker}`,
    content: `公告 PG smoke 内容 ${marker}`,
    level: 'info',
    status: 'draft'
  }, actor)
  createdAnnouncementIds.push(created.id)
  assert.equal(created.status, 'draft', 'PG 创建公告默认应保留草稿状态')

  const listed = await listAnnouncementsPageAsync({ page: 1, pageSize: 20 })
  assert.ok(listed.items.some((announcement) => announcement.id === created.id), 'PG 公告管理列表应返回刚创建的公告')

  const found = await findAnnouncementAsync(created.id)
  assert.equal(found?.title, created.title, 'PG 公告详情应按 ID 读取完整公告')

  const updated = await updateAnnouncementAsync(created.id, {
    content: `公告 PG smoke 已更新 ${marker}`,
    level: 'warning'
  }, actor)
  assert.equal(updated?.level, 'warning', 'PG 更新公告应返回更新后的级别')
  assert.equal(updated?.content, `公告 PG smoke 已更新 ${marker}`, 'PG 更新公告应返回完整内容')

  const published = await publishAnnouncementAsync(created.id, actor)
  assert.equal(published?.status, 'published', 'PG 发布公告应变为 published')
  assert.ok(published?.publishedAt, 'PG 发布公告应写入发布时间')

  const publicList = await listPublicAnnouncementsAsync(actor, 30)
  assert.ok(publicList.some((announcement) => announcement.id === created.id), 'PG 公开公告列表应返回已发布公告')
  const publicDetail = await findPublicAnnouncementAsync(created.id)
  assert.equal(publicDetail?.content, `公告 PG smoke 已更新 ${marker}`, 'PG 公共公告详情应按 ID 返回完整正文')

  const readResult = await markPublicAnnouncementsReadAsync(actor, [created.id, created.id])
  assert.equal(readResult.count, 1, 'PG 公告已读写入应按 ID 去重')
  const readPublicList = await listPublicAnnouncementsAsync(actor, 30)
  assert.ok(readPublicList.find((announcement) => announcement.id === created.id)?.readAt, 'PG 公开公告列表应返回已读时间')

  const republished = await updateAnnouncementAsync(created.id, { status: 'published' }, actor)
  assert.equal(republished?.status, 'published', 'PG 已发布公告重复发布状态应保持 published')
  const stillRead = await listPublicAnnouncementsAsync(actor, 30)
  assert.ok(stillRead.find((announcement) => announcement.id === created.id)?.readAt, 'PG 已发布公告重复更新为 published 不应清理已读状态')

  await updateAnnouncementAsync(created.id, { status: 'archived' }, actor)
  const republishedFromArchive = await publishAnnouncementAsync(created.id, actor)
  assert.equal(republishedFromArchive?.status, 'published', 'PG 归档公告重新发布应恢复 published')
  const afterRepublish = await listPublicAnnouncementsAsync(actor, 30)
  assert.equal(afterRepublish.find((announcement) => announcement.id === created.id)?.readAt, undefined, 'PG 从非发布状态重新发布应清理已读状态')

  const unpublished = await unpublishAnnouncementAsync(created.id, actor)
  assert.equal(unpublished?.status, 'archived', 'PG 下线公告应归档')
  assert.equal((await listPublicAnnouncementsAsync(actor, 30)).some((announcement) => announcement.id === created.id), false, 'PG 下线公告不应出现在公开列表')
  assert.equal(await findPublicAnnouncementAsync(created.id), undefined, 'PG 下线公告不能通过公共详情读取')

  assert.equal(await deleteAnnouncementAsync(created.id), true, 'PG 删除公告应成功')
  assert.equal(await findAnnouncementAsync(created.id), undefined, 'PG 删除后公告详情应不存在')

  console.log(JSON.stringify({
    message: '公告 PG smoke 通过',
    announcementId: created.id,
    publicReadChecked: true
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  if (createdAnnouncementIds.length > 0) {
    await pool.query('DELETE FROM juhe_business.announcements WHERE id = ANY($1::text[])', [createdAnnouncementIds])
  }
  await pool.query('DELETE FROM juhe_business.announcements WHERE title LIKE $1', [`公告 PG smoke ${marker}%`])
}
