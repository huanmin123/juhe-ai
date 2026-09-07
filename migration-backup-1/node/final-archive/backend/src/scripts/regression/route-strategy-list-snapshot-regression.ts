import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import type { Server } from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-route-strategy-list-snapshot-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'route-strategy-list-snapshot-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { routeStrategiesRouter },
  { forceSelfAccessScope, requireAdmin },
  { withRequestAuthContext },
  databaseModule,
  repositories,
  sqliteReadWorkerPoolModule
] = await Promise.all([
  import('../../modules/route-strategies/route-strategies.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../modules/auth/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const owner = repositories.createSystemAccount({
  username: 'route_strategy_snapshot_owner',
  displayName: '策略路由快照用户',
  password: 'Test123456!',
  role: 'user',
  status: 'active',
  mustChangePassword: false
})
const ownerAccess = { systemAccountId: owner.id, role: 'user' as const }
const adminAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
const ownerGroups = Array.from({ length: 4 }, (_, index) => repositories.createGroup({
  name: `策略路由快照分组-${index + 1}`,
  providerCode: 'gpt',
  enabled: true
}, ownerAccess))
const previewStrategy = repositories.createRouteStrategy({
  name: '策略路由快照排序目标',
  mode: 'round_robin',
  status: 'active',
  groupBindings: ownerGroups.map((group, index) => ({
    groupId: group.id,
    priority: index + 1,
    weight: 1,
    status: index === 3 ? 'disabled' : 'active'
  }))
}, ownerAccess)
const zeroStrategy = repositories.createRouteStrategy({
  name: '策略路由快照零值目标',
  mode: 'normal',
  status: 'active',
  groupBindings: [{ groupId: ownerGroups[0].id, priority: 1, weight: 1, status: 'active' }]
}, ownerAccess)
const foreignGroup = repositories.createGroup({
  name: '策略路由快照管理员分组',
  providerCode: 'gpt',
  enabled: true
}, adminAccess)
const foreignStrategy = repositories.createRouteStrategy({
  name: '策略路由快照不可见目标',
  mode: 'normal',
  status: 'active',
  groupBindings: [{ groupId: foreignGroup.id, priority: 1, weight: 1, status: 'active' }]
}, adminAccess)
const authorizationOwner = repositories.createSystemAccount({
  username: 'route_strategy_snapshot_authorization_owner',
  displayName: '策略路由快照授权方',
  password: 'Test123456!',
  role: 'user',
  status: 'active',
  mustChangePassword: false
})
const authorizationOwnerAccess = { systemAccountId: authorizationOwner.id, role: 'user' as const }
const authorizedSourceGroup = repositories.createGroup({
  name: '策略路由快照授权来源分组',
  providerCode: 'gpt',
  enabled: true
}, authorizationOwnerAccess)
repositories.createResourceAuthorization({
  resourceType: 'group',
  resourceId: authorizedSourceGroup.id,
  granteeType: 'system_account',
  granteeId: owner.id,
  remark: '策略路由 list snapshot 授权设置回归'
}, authorizationOwnerAccess)
const authorizedStrategy = repositories.createRouteStrategy({
  name: '策略路由快照授权分组目标',
  mode: 'round_robin',
  status: 'active',
  groupBindings: [
    { groupId: authorizedSourceGroup.id, priority: 1, weight: 1, status: 'active' },
    { groupId: ownerGroups[0].id, priority: 2, weight: 1, status: 'active' }
  ]
}, ownerAccess)

const database = databaseModule.getBusinessDatabase()
database.prepare('UPDATE groups SET enabled = 0 WHERE id = ?').run(ownerGroups[2].id)
database.prepare('DELETE FROM route_strategy_groups WHERE route_strategy_id = ?').run(zeroStrategy.id)
const previewBindingRows = database.prepare(`
  SELECT id, group_id
  FROM route_strategy_groups
  WHERE route_strategy_id = ?
`).all(previewStrategy.id) as unknown as Array<{ id: string; group_id: string }>
const previewOrder = new Map([
  [ownerGroups[0].id, { priority: 3, status: 'active', createdAt: '2026-07-23T00:03:00.000Z' }],
  [ownerGroups[1].id, { priority: 1, status: 'active', createdAt: '2026-07-23T00:02:00.000Z' }],
  [ownerGroups[2].id, { priority: 1, status: 'active', createdAt: '2026-07-23T00:01:00.000Z' }],
  [ownerGroups[3].id, { priority: 0, status: 'disabled', createdAt: '2026-07-23T00:00:00.000Z' }]
])
for (const row of previewBindingRows) {
  const order = previewOrder.get(row.group_id)
  assert(order)
  database.prepare(`
    UPDATE route_strategy_groups
    SET priority = ?, status = ?, created_at = ?, updated_at = ?
    WHERE id = ?
  `).run(order.priority, order.status, order.createdAt, order.createdAt, row.id)
}

const activeKey = repositories.createApiKeyRecord({
  name: '策略路由快照启用 Key',
  routeStrategyId: previewStrategy.id,
  status: 'active'
}, ownerAccess)
const disabledKey = repositories.createApiKeyRecord({
  name: '策略路由快照停用 Key',
  routeStrategyId: previewStrategy.id,
  status: 'disabled'
}, ownerAccess)
assert(activeKey.id && disabledKey.id)

let server: Server | undefined
try {
  assertBaseListProjection()
  assertSnapshotRepositoryContract()
  assertAuthorizedPreviewSettings()
  assertFixedBatchQueryCount()
  assertWorkerOperationRegistered()
  await assertReadWorkerContract()

  server = await startHttpServer()
  await assertHttpContract(server)

  console.log('策略路由 list snapshot 回归通过：基础投影、参数权限、零值排序、授权设置和固定批量查询均正确')
} finally {
  await closeServer(server)
  await sqliteReadWorkerPoolModule.closeSqliteReadWorkerPool()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertBaseListProjection(): void {
  const capturedSql: string[] = []
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  database.prepare = ((sql: string) => {
    capturedSql.push(sql)
    return originalPrepare(sql)
  }) as typeof database.prepare
  let item: Record<string, unknown> | undefined
  try {
    item = repositories.listRouteStrategyListItemsPage(ownerAccess, {
      keyword: previewStrategy.name,
      page: 1,
      pageSize: 20
    }).items[0] as unknown as Record<string, unknown> | undefined
  } finally {
    database.prepare = originalPrepare
  }
  assert(item, '基础列表应返回目标策略路由')
  assert.equal('bindingCount' in item, false, '基础 DTO 不得返回 bindingCount')
  assert.equal('apiKeyCount' in item, false, '基础 DTO 不得返回 apiKeyCount')
  assert.equal('groupBindingPreview' in item, false, '基础 DTO 不得返回 groupBindingPreview')
  const sql = capturedSql.join('\n')
  assert.doesNotMatch(sql, /\bCOUNT\s*\(/i, '基础列表执行 SQL 不得包含 COUNT')
  assert.doesNotMatch(sql, /\bFROM\s+(?:"[^"]+"\.)?route_strategy_groups\b/i, '基础列表不得加载分组预览')
  assert.doesNotMatch(sql, /\bFROM\s+(?:"[^"]+"\.)?api_keys\b/i, '基础列表不得读取 API Key 计数')
}

function assertSnapshotRepositoryContract(): void {
  const snapshot = repositories.listRouteStrategyListSnapshot(ownerAccess, [
    foreignStrategy.id,
    zeroStrategy.id,
    previewStrategy.id,
    'route_strategy_missing',
    previewStrategy.id
  ])
  assert.deepEqual(
    snapshot.items.map((item) => item.id),
    [zeroStrategy.id, previewStrategy.id],
    '用户 snapshot 必须静默省略不可见/不存在 ID，并按去重后的请求顺序返回'
  )
  assert.deepEqual(snapshot.items[0], {
    id: zeroStrategy.id,
    bindingCount: 0,
    apiKeyCount: 0,
    groupBindingPreview: []
  }, '真实零值必须返回显式 snapshot 项')
  const preview = snapshot.items[1]
  assert.equal(preview?.bindingCount, 4, 'bindingCount 必须统计全部绑定')
  assert.equal(preview?.apiKeyCount, 2, 'apiKeyCount 必须统计启用和停用 Key')
  assert.deepEqual(
    preview?.groupBindingPreview.map((item) => item.groupId),
    [ownerGroups[2].id, ownerGroups[1].id, ownerGroups[0].id],
    '预览必须按 active、priority、createdAt、id 排序并截取前三项'
  )
  assert.equal(preview?.groupBindingPreview[0]?.groupEnabled, false, '预览必须保留当前分组启用事实')
  assert.equal(preview?.groupBindingPreview.some((item) => item.groupId === ownerGroups[3].id), false, '停用绑定排序在前三项之后时不得进入预览')

  const ownerFiltered = repositories.listRouteStrategyListSnapshot({
    ...adminAccess,
    systemAccountFilterId: owner.id
  }, [foreignStrategy.id, previewStrategy.id])
  assert.deepEqual(ownerFiltered.items.map((item) => item.id), [previewStrategy.id], '管理员指定 owner 时必须在 SQL 权限范围内裁剪')
  const globalAdmin = repositories.listRouteStrategyListSnapshot(adminAccess, [foreignStrategy.id, previewStrategy.id])
  assert.deepEqual(globalAdmin.items.map((item) => item.id), [foreignStrategy.id, previewStrategy.id], '管理员全局作用域应读取所有请求中的可见策略')
}

function assertAuthorizedPreviewSettings(): void {
  const before = repositories.listRouteStrategyListSnapshot(ownerAccess, [authorizedStrategy.id]).items[0]
  const beforeAuthorized = before?.groupBindingPreview.find((item) => item.groupId === authorizedSourceGroup.id)
  assert.equal(beforeAuthorized?.groupEnabled, true, '有效授权分组的 preview 初始应可用')

  const updated = repositories.updateGroup(authorizedSourceGroup.id, { enabled: false }, ownerAccess)
  assert.equal(updated?.enabled, false, '被授权方停用授权分组时应写入自己的 authorization settings')
  const settingsRow = database.prepare(`
    SELECT enabled
    FROM group_authorization_settings
    WHERE system_account_id = ? AND group_id = ?
  `).get(owner.id, authorizedSourceGroup.id) as unknown as { enabled?: number } | undefined
  assert.equal(settingsRow?.enabled, 0, '授权分组停用必须落到 group_authorization_settings，而不是修改来源分组')
  assert.equal(
    repositories.findGroupSummary(authorizedSourceGroup.id, authorizationOwnerAccess)?.enabled,
    true,
    '被授权方设置不得改变授权方来源分组状态'
  )

  const after = repositories.listRouteStrategyListSnapshot(ownerAccess, [authorizedStrategy.id]).items[0]
  const afterAuthorized = after?.groupBindingPreview.find((item) => item.groupId === authorizedSourceGroup.id)
  assert.equal(afterAuthorized?.status, 'active', '授权设置停用不应改写策略绑定自身状态')
  assert.equal(afterAuthorized?.groupEnabled, false, 'snapshot preview 必须应用 group_authorization_settings.enabled=false')
}

function assertFixedBatchQueryCount(): void {
  const oneIdQueries = captureSnapshotSelects([previewStrategy.id])
  const manyIdQueries = captureSnapshotSelects([previewStrategy.id, zeroStrategy.id, foreignStrategy.id])
  assert.equal(oneIdQueries.length, 3, `单 ID snapshot 应固定执行可见性、计数和预览三条批量查询，实际 ${oneIdQueries.length}`)
  assert.equal(manyIdQueries.length, oneIdQueries.length, 'snapshot SQL 数量不得随 ID 数量增加')
  assert(manyIdQueries.every((sql) => /\bIN\s*\(/i.test(sql)), 'snapshot 三个阶段都必须使用批量 ID 窗口')
}

function captureSnapshotSelects(ids: string[]): string[] {
  const capturedSql: string[] = []
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  database.prepare = ((sql: string) => {
    if (/^\s*SELECT\b/i.test(sql)) capturedSql.push(sql)
    const statement = originalPrepare(sql)
    return statement
  }) as typeof database.prepare
  try {
    repositories.listRouteStrategyListSnapshot(adminAccess, ids)
  } finally {
    database.prepare = originalPrepare
  }
  return capturedSql
}

function assertWorkerOperationRegistered(): void {
  const repositorySource = readFileSync(resolve('src/storage/route-strategy.repository.ts'), 'utf8')
  const workerSource = readFileSync(resolve('src/storage/sqlite-read-worker.ts'), 'utf8')
  assert.match(repositorySource, /type:\s*'list_route_strategy_list_snapshot_read_only'/, 'SQLite async snapshot 必须投递单个 read worker operation')
  assert.match(workerSource, /case\s+'list_route_strategy_list_snapshot_read_only'/, 'SQLite read worker 必须实现 snapshot operation')
}

async function assertReadWorkerContract(): Promise<void> {
  const expected = repositories.listRouteStrategyListSnapshot(ownerAccess, [zeroStrategy.id, previewStrategy.id])
  const previousProcessRole = runtimeConfig.processRole
  const previousPoolSize = runtimeConfig.sqliteReadWorkerPoolSize
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.sqliteReadWorkerPoolSize = 1
  try {
    const actual = await repositories.listRouteStrategyListSnapshotAsync(ownerAccess, [zeroStrategy.id, previewStrategy.id])
    assert.deepEqual(actual.items, expected.items, 'SQLite read worker snapshot 必须与主线程逐字段一致')
  } finally {
    runtimeConfig.processRole = previousProcessRole
    runtimeConfig.sqliteReadWorkerPoolSize = previousPoolSize
    await sqliteReadWorkerPoolModule.closeSqliteReadWorkerPool()
  }
}

async function startHttpServer(): Promise<Server> {
  const app = express()
  app.use((req, _res, next) => {
    const account = req.headers['x-test-role'] === 'admin'
      ? { id: 'sys_admin', username: 'admin', displayName: '管理员', role: 'admin' as const }
      : owner
    withRequestAuthContext({
      systemAccountId: account.id,
      username: account.username,
      displayName: account.displayName,
      role: account.role,
      mustChangePassword: false,
      sessionId: `session-${account.id}`
    }, next)
  })
  app.use('/my-route-strategies', forceSelfAccessScope, routeStrategiesRouter)
  app.use('/route-strategies', requireAdmin, routeStrategiesRouter)
  app.use((error: unknown, _req: express.Request, res: express.Response, _next: express.NextFunction) => {
    res.status(500).json({ message: error instanceof Error ? error.message : String(error) })
  })
  const httpServer = app.listen(0, '127.0.0.1')
  await listen(httpServer)
  return httpServer
}

async function assertHttpContract(httpServer: Server): Promise<void> {
  const baseUrl = `http://127.0.0.1:${serverPort(httpServer)}`
  const forbiddenManagement = await fetch(`${baseUrl}/route-strategies?page=1&pageSize=20`)
  assert.equal(forbiddenManagement.status, 403, '普通用户请求管理端策略路由列表必须被生产 requireAdmin 边界拒绝')

  const listResponse = await fetch(`${baseUrl}/route-strategies?page=1&pageSize=50&systemAccountId=${encodeURIComponent(owner.id)}`, {
    headers: { 'x-test-role': 'admin' }
  })
  assert.equal(listResponse.status, 200, '管理端策略路由列表应返回完整当前页')
  const listPayload = await listResponse.json() as { data: { generatedAt: string; items: Array<{ id: string; bindingCount: number; apiKeyCount: number; groupBindingPreview: unknown[] }> } }
  const previewItem = listPayload.data.items.find((item) => item.id === previewStrategy.id)
  assert(previewItem, '完整列表应返回目标策略路由')
  assert.equal(typeof previewItem.bindingCount, 'number', '完整列表应内联绑定分组数')
  assert.equal(typeof previewItem.apiKeyCount, 'number', '完整列表应内联 API Key 数')
  assert.ok(Array.isArray(previewItem.groupBindingPreview), '完整列表应内联绑定分组预览')
  assert.ok(listPayload.data.generatedAt, '完整列表应返回生成时间')

  const forgedSelf = await fetch(`${baseUrl}/my-route-strategies?page=1&pageSize=50&systemAccountId=sys_admin`)
  assert.equal(forgedSelf.status, 200, '个人列表应忽略伪造 owner query')
  const forgedPayload = await forgedSelf.json() as { data: { items: Array<{ id: string }> } }
  assert.equal(forgedPayload.data.items.some((item) => item.id === foreignStrategy.id), false, '个人列表必须强制当前用户作用域')
}

function listen(httpServer: Server): Promise<void> {
  if (httpServer.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    httpServer.once('listening', resolvePromise)
    httpServer.once('error', rejectPromise)
  })
}

function serverPort(httpServer: Server): number {
  const address = httpServer.address()
  if (!address || typeof address === 'string') throw new Error('服务地址不可用')
  return address.port
}

async function closeServer(httpServer: Server | undefined): Promise<void> {
  if (!httpServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    httpServer.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}
