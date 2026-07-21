import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-authorization-lightweight-http-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'authorization-lightweight-http-contract-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { authorizationsRouter },
  { authorizationOptionsRouter },
  { forceSelfAccessScope, requireAuth },
  { requestContextMiddleware },
  { closeSqliteReadWorkerPool },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/authorizations/authorizations.routes.js'),
  import('../../modules/authorization-options/authorization-options.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/sqlite-read-worker-pool.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-authorizations', forceSelfAccessScope, authorizationsRouter)
app.use('/__aisys__/api/my-authorization-options', forceSelfAccessScope, authorizationOptionsRouter)

interface ApiEnvelope<T> {
  data?: T
  message?: string
}

let server: ReturnType<typeof app.listen> | undefined

try {
  const owner = repositories.createSystemAccount({
    username: 'authorization_lightweight_owner',
    displayName: '轻量授权所有者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const grantee = repositories.createSystemAccount({
    username: 'authorization_lightweight_grantee',
    displayName: '轻量授权接收者',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
  const group = repositories.createGroup({ name: '轻量授权资源分组', providerCode: 'gpt' }, ownerAccess)
  repositories.createResourceAuthorization({
    resourceType: 'group',
    resourceId: group.id,
    granteeType: 'system_account',
    granteeId: grantee.id,
    remark: '轻量列表契约',
    expiresAt: '2099-01-01T00:00:00.000Z'
  }, ownerAccess)

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('授权轻量 HTTP 回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`
  const ownerCookie = sessionCookie(owner.id)

  const page = await requestEnvelope<{ items: Array<Record<string, unknown>> }>(
    baseUrl,
    '/__aisys__/api/my-authorizations?status=all&page=1&pageSize=20',
    ownerCookie
  )
  const item = page.items.find((candidate) => candidate.resourceId === group.id)
  assert(item, '授权列表应返回新建授权')
  assert.deepEqual(Object.keys(item).sort(), [
    'createdAt',
    'effectiveSourceType',
    'expiresAt',
    'granteeSystemAccountId',
    'granteeSystemAccountName',
    'granteeType',
    'granteeUsername',
    'id',
    'permissions',
    'remark',
    'resourceId',
    'resourceName',
    'resourceOwnerSystemAccountId',
    'resourceOwnerSystemAccountName',
    'resourceType',
    'sourceSummary',
    'status'
  ].sort(), '授权列表项只能返回页面展示、方向判断和操作权限所需字段')
  assert.deepEqual(Object.keys(item.permissions as Record<string, unknown>).sort(), ['canAuthorize', 'canEdit'])
  assert.deepEqual(Object.keys(item.sourceSummary as Record<string, unknown>).sort(), ['activeSourceCount', 'hasManual', 'hasTeam', 'teamSources'])

  const groups = await requestEnvelope<Array<Record<string, unknown>>>(
    baseUrl,
    `/__aisys__/api/my-authorization-options/grantee-groups?granteeSystemAccountId=${encodeURIComponent(grantee.id)}&providerCode=gpt&limit=20`,
    ownerCookie
  )
  assert(groups.length > 0, '被授权用户应返回目标分组选项')
  for (const option of groups) {
    assert.deepEqual(Object.keys(option).sort(), ['id', 'name'], '授权目标分组选项只能返回 id/name')
  }
  const defaultGroup = databaseModule.getBusinessDatabase()
    .prepare("SELECT id FROM groups WHERE system_account_id = ? AND provider_code = 'gpt' AND is_default = 1 LIMIT 1")
    .get(grantee.id) as unknown as { id?: string } | undefined
  assert.equal(groups[0]?.id, defaultGroup?.id, '目标分组仅返回 id/name 后仍应通过排序把默认分组放在首项')

  console.log('授权轻量 HTTP 契约回归通过：授权列表与目标分组选项仅返回页面实际需要字段')
} finally {
  await closeServer(server)
  await closeSqliteReadWorkerPool()
  try {
    databaseModule.getBusinessDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function requestEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  assert.equal(response.status, 200, `${path} HTTP ${response.status}: ${text}`)
  const payload = JSON.parse(text) as ApiEnvelope<T>
  return payload.data as T
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
