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

  console.log('公告渐进式 HTTP 回归通过：公共轮询仅返回摘要，公共详情按 ID 取正文，管理列表仅返回 contentPreview')
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
