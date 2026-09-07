import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-announcement-progressive-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'announcement-progressive-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [{ announcementsRouter }, { withRequestAuthContext }, databaseModule, repositories] = await Promise.all([
  import('../../modules/announcements/announcements.routes.js'),
  import('../../modules/auth/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const admin = repositories.createSystemAccount({
  username: 'announcement-progressive-admin',
  password: 'Test123456!',
  displayName: '公告测试管理员',
  role: 'admin',
  status: 'active'
})
const user = repositories.createSystemAccount({
  username: 'announcement-progressive-user',
  password: 'Test123456!',
  displayName: '公告测试用户',
  role: 'user',
  status: 'active'
})
const longContent = Array.from({ length: 120 }, (_, index) => `公告正文-${index}`).join('\n')
const published = repositories.createAnnouncement({
  title: '渐进加载公告',
  content: longContent,
  level: 'warning',
  status: 'published'
}, admin.id)
const draft = repositories.createAnnouncement({
  title: '未发布公告',
  content: '草稿正文',
  level: 'info',
  status: 'draft'
}, admin.id)

const app = express()
app.use(express.json())
app.use((req, _res, next) => {
  const isAdmin = req.headers['x-test-role'] === 'admin'
  const account = isAdmin ? admin : user
  withRequestAuthContext({
    systemAccountId: account.id,
    username: account.username,
    displayName: account.displayName,
    role: account.role,
    mustChangePassword: false,
    sessionId: `session-${account.id}`
  }, next)
})
app.use('/announcements', announcementsRouter)
app.use((error: unknown, _req: express.Request, res: express.Response, _next: express.NextFunction) => {
  res.status(500).json({ message: error instanceof Error ? error.message : String(error) })
})

let server: Server | undefined
try {
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverPort(server)}/announcements`

  const publicListResponse = await fetch(`${baseUrl}/public?limit=30`)
  assert.equal(publicListResponse.status, 200, '公共公告摘要列表应成功')
  const publicListPayload = await publicListResponse.json() as { data: Array<Record<string, unknown>> }
  const publicItem = publicListPayload.data.find((item) => item.id === published.id)
  assert(publicItem, '公共公告摘要列表应包含已发布公告')
  assert.deepEqual(
    Object.keys(publicItem).sort(),
    ['id', 'level', 'publishedAt', 'title'].sort(),
    '未读公共公告摘要只能返回铃铛和公告中心首屏所需字段'
  )

  const publicDetailResponse = await fetch(`${baseUrl}/public/${published.id}`)
  assert.equal(publicDetailResponse.status, 200, '普通用户应能按 ID 读取已发布公告正文')
  const publicDetailPayload = await publicDetailResponse.json() as { data: Record<string, unknown> }
  assert.equal(publicDetailPayload.data.content, longContent, '公共公告详情应返回完整正文')

  const draftDetailResponse = await fetch(`${baseUrl}/public/${draft.id}`)
  assert.equal(draftDetailResponse.status, 404, '公共详情不能泄露草稿公告')

  const adminListResponse = await fetch(`${baseUrl}?page=1&pageSize=50`, {
    headers: { 'x-test-role': 'admin' }
  })
  assert.equal(adminListResponse.status, 200, '管理公告列表应成功')
  const adminListPayload = await adminListResponse.json() as { data: { items: Array<Record<string, unknown>> } }
  const adminItem = adminListPayload.data.items.find((item) => item.id === published.id)
  assert(adminItem, '管理公告列表应包含目标公告')
  assert.equal(typeof adminItem.contentPreview, 'string', '管理列表应返回显式正文预览字段')
  assert.equal('content' in adminItem, false, '管理列表不能伪装成完整详情返回 content')
  assert.deepEqual(
    Object.keys(adminItem).sort(),
    ['id', 'title', 'contentPreview', 'contentTruncated', 'level', 'status', 'updatedByName', 'publishedAt', 'revision'].sort(),
    '管理列表只能返回当前表格和并发写入需要的字段'
  )

  const adminDetailResponse = await fetch(`${baseUrl}/${published.id}`, {
    headers: { 'x-test-role': 'admin' }
  })
  assert.equal(adminDetailResponse.status, 200, '管理公告编辑详情应成功')
  const adminDetailPayload = await adminDetailResponse.json() as { data: Record<string, unknown> }
  assert.deepEqual(
    Object.keys(adminDetailPayload.data).sort(),
    ['id', 'title', 'content', 'level', 'status', 'revision'].sort(),
    '编辑详情只能返回表单字段和 revision'
  )

  const forbiddenCreateResponse = await fetch(baseUrl, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ title: '越权公告', content: '不应创建' })
  })
  assert.equal(forbiddenCreateResponse.status, 403, '普通用户不能创建公告')

  const createResponse = await fetch(baseUrl, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ title: '轻量响应公告', content: '创建响应不应回传正文', level: 'info', status: 'draft' })
  })
  assert.equal(createResponse.status, 201, '管理员应能创建公告')
  const createPayload = await createResponse.json() as { data: Record<string, unknown> }
  assertAnnouncementMutationResult(createPayload.data, '创建')
  const createdId = String(createPayload.data.id)
  let expectedRevision = String(createPayload.data.revision)

  const missingRevisionResponse = await fetch(`${baseUrl}/${createdId}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ title: '缺少版本的公告更新' })
  })
  assert.equal(missingRevisionResponse.status, 400, '公告 PATCH 缺少 expectedRevision 必须拒绝')

  const noOpResponse = await fetch(`${baseUrl}/${createdId}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ title: '轻量响应公告', expectedRevision })
  })
  assert.equal(noOpResponse.status, 200, '同值 PATCH 应作为成功 no-op')
  const noOpPayload = await noOpResponse.json() as { data: Record<string, unknown> }
  assert.equal(noOpPayload.data.revision, expectedRevision, '同值 PATCH 不得推进 revision')

  const updateResponse = await fetch(`${baseUrl}/${createdId}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ content: '已更新但响应仍保持轻量', expectedRevision })
  })
  assert.equal(updateResponse.status, 200, '管理员应能更新公告')
  const updatePayload = await updateResponse.json() as { data: Record<string, unknown> }
  assertAnnouncementMutationResult(updatePayload.data, '更新')
  assert.equal(updatePayload.data.id, createdId, '更新响应应标识目标公告')
  expectedRevision = String(updatePayload.data.revision)

  const conflictResponse = await fetch(`${baseUrl}/${createdId}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ level: 'warning', expectedRevision: String(createPayload.data.revision) })
  })
  assert.equal(conflictResponse.status, 409, '旧 revision 的公告 PATCH 必须返回冲突')

  const publishResponse = await fetch(`${baseUrl}/${createdId}/publish`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ expectedRevision })
  })
  assert.equal(publishResponse.status, 200, '管理员应能发布公告')
  const publishPayload = await publishResponse.json() as { data: Record<string, unknown> }
  assertAnnouncementMutationResult(publishPayload.data, '发布')
  expectedRevision = String(publishPayload.data.revision)

  const unpublishResponse = await fetch(`${baseUrl}/${createdId}/unpublish`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ expectedRevision })
  })
  assert.equal(unpublishResponse.status, 200, '管理员应能下线公告')
  const unpublishPayload = await unpublishResponse.json() as { data: Record<string, unknown> }
  assertAnnouncementMutationResult(unpublishPayload.data, '下线')
  expectedRevision = String(unpublishPayload.data.revision)

  const deleteResponse = await fetch(`${baseUrl}/${createdId}`, {
    method: 'DELETE',
    headers: { 'content-type': 'application/json', 'x-test-role': 'admin' },
    body: JSON.stringify({ expectedRevision })
  })
  assert.equal(deleteResponse.status, 204, '删除公告应使用空响应')
  assert.equal(await deleteResponse.text(), '', '删除公告不能返回旧公告摘要')

  console.log('公告渐进式 HTTP 回归通过：读接口按需返回，写接口仅返回 id/revision 或 204')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function listen(server: Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverPort(server: Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('服务地址不可用')
  return address.port
}

async function closeServer(server: Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function assertAnnouncementMutationResult(value: Record<string, unknown>, action: string): void {
  assert.deepEqual(Object.keys(value).sort(), ['id', 'revision'], `${action}响应只能返回 id 和 revision`)
  assert.equal(typeof value.id, 'string', `${action}响应必须返回公告 ID`)
  assert.equal(typeof value.revision, 'string', `${action}响应必须返回可失效缓存的 revision`)
}
