import assert from 'node:assert/strict'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-system-team-demand-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'system-team-demand-write-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, cacheInvalidation, { systemTeamsRouter }, authMiddleware, requestContext] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../shared/gateway-cache-invalidation.js'),
  import('../../modules/system-teams/system-teams.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js')
])

const database = databaseModule.getBusinessDatabase()
const invalidations: string[] = []
const unregisterInvalidator = cacheInvalidation.registerGatewayRuntimeCacheInvalidator((reason) => {
  invalidations.push(reason)
})
let server: ReturnType<ReturnType<typeof express>['listen']> | undefined

try {
  const admin = repositories.createSystemAccount({
    username: 'system_team_demand_admin',
    displayName: '系统团队按需写管理员',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const member = repositories.createSystemAccount({
    username: 'system_team_demand_member',
    displayName: '系统团队按需写成员',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const outsider = repositories.createSystemAccount({
    username: 'system_team_demand_outsider',
    displayName: '系统团队按需写无关用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const adminAccess = { systemAccountId: admin.id, role: 'admin' as const }
  const team = repositories.createSystemTeam({ name: '系统团队按需写回归', description: '原说明' }, adminAccess)
  repositories.addSystemTeamMembers(team.id, { systemAccountIds: [member.id] }, adminAccess)
  const scopedAccess = { systemAccountId: member.id, role: 'user' as const }

  const listed = repositories.listSystemTeamsPage(scopedAccess, { page: 1, pageSize: 20 })
  const listedTeam = listed.items.find((item) => item.id === team.id)
  assert(listedTeam?.updatedAt, '列表必须直接携带编辑 CAS 所需 updatedAt')
  assert.deepEqual(Object.keys(listedTeam).sort(), ['createdAt', 'description', 'id', 'memberCount', 'name', 'status', 'updatedAt'], '列表只应返回渲染字段与编辑版本')

  invalidations.length = 0
  const descriptionPatch = await captureSql(() => repositories.updateSystemTeamAsync(team.id, {
    description: '新说明',
    expectedUpdatedAt: listedTeam.updatedAt
  }, scopedAccess))
  assert.equal(descriptionPatch.result.status, 'updated')
  assert.deepEqual(descriptionPatch.result.status === 'updated' ? descriptionPatch.result.result.changedFields : [], ['description'])
  assert.deepEqual(descriptionPatch.result.status === 'updated' ? descriptionPatch.result.result.rowPatch : {}, { description: '新说明' })
  const patchRead = descriptionPatch.calls.find((call) => call.kind === 'get' && /FROM\s+system_teams/i.test(call.sql))?.sql ?? ''
  assert.match(patchRead, /SELECT\s+system_teams\.id,\s*system_teams\.name,\s*system_teams\.updated_at,\s*system_teams\.description/i, '说明 PATCH 只读取定位、审计名称、版本和说明')
  assert.doesNotMatch(patchRead, /created_at|created_by|system_teams\.status|system_team_members\.\*/i, '说明 PATCH 不得读取创建信息、状态或成员对象')
  const descriptionDml = systemTeamDml(descriptionPatch.calls)
  assert.equal(descriptionDml.length, 1, '说明 PATCH 只允许一条团队 DML')
  assert.match(descriptionDml[0]?.sql ?? '', /SET\s+description\s*=\s*\?,\s*updated_at\s*=\s*\?/i, '说明 PATCH 只能更新说明和版本')
  assert.match(descriptionDml[0]?.sql ?? '', /WHERE\s+id\s*=\s*\?[\s\S]*AND\s+updated_at\s*=\s*\?[\s\S]*EXISTS/i, '团队 ID、版本和作用域必须进入 UPDATE SQL')
  assert.deepEqual(invalidations, [], '说明 PATCH 不得清理授权运行态缓存')

  assert.equal(descriptionPatch.result.status, 'updated')
  const currentVersion = descriptionPatch.result.result.updatedAt
  invalidations.length = 0
  const noOp = await captureSql(() => repositories.updateSystemTeamAsync(team.id, {
    description: '新说明',
    expectedUpdatedAt: currentVersion
  }, scopedAccess))
  assert.equal(noOp.result.status, 'noop')
  assert.deepEqual(noOp.result.status === 'noop' ? noOp.result.result : undefined, {
    id: team.id,
    changedFields: [],
    rowPatch: {},
    updatedAt: currentVersion
  })
  assert.deepEqual(systemTeamDml(noOp.calls), [], '同值 PATCH 必须零 DML')
  assert.deepEqual(invalidations, [], '同值 PATCH 必须零网关缓存失效')

  const stale = await captureSql(() => repositories.updateSystemTeamAsync(team.id, {
    description: '过期覆盖',
    expectedUpdatedAt: listedTeam.updatedAt
  }, scopedAccess))
  assert.equal(stale.result.status, 'conflict')
  assert.deepEqual(systemTeamDml(stale.calls), [], '过期版本必须在 DML 前拒绝')

  const crossScope = await captureSql(() => repositories.updateSystemTeamAsync(team.id, {
    description: '越权覆盖',
    expectedUpdatedAt: currentVersion
  }, { systemAccountId: outsider.id, role: 'user' }))
  assert.equal(crossScope.result.status, 'not_found')
  assert.match(selectSql(crossScope.calls), /scoped_members\.system_account_id\s*=\s*\?/i, '作用域必须下推到 PATCH 定位 SQL')
  assert.deepEqual(systemTeamDml(crossScope.calls), [], '作用域外 PATCH 必须零 DML')

  const app = express()
  app.use(requestContext.requestContextMiddleware)
  app.use(express.json({ limit: '1mb' }))
  app.use('/__aisys__/api', authMiddleware.requireAuth)
  app.use('/__aisys__/api/system-teams', systemTeamsRouter)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string')
  const baseUrl = `http://127.0.0.1:${address.port}`
  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`

  const noOpHttp = await captureSql(() => fetch(`${baseUrl}/__aisys__/api/system-teams/${team.id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: '新说明', expectedUpdatedAt: currentVersion })
  }))
  assert.equal(noOpHttp.result.status, 200)
  assert.deepEqual((await noOpHttp.result.json()).data, { id: team.id, changedFields: [], rowPatch: {}, updatedAt: currentVersion }, 'HTTP no-op 必须返回最小结果')
  assert.deepEqual(systemTeamDml(noOpHttp.calls), [], 'HTTP no-op 必须零团队 DML')

  const changedHttp = await fetch(`${baseUrl}/__aisys__/api/system-teams/${team.id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ name: '系统团队按需写已更新', expectedUpdatedAt: currentVersion })
  })
  assert.equal(changedHttp.status, 200)
  const changedPayload = (await changedHttp.json()).data as Record<string, unknown>
  assert.deepEqual(Object.keys(changedPayload).sort(), ['changedFields', 'id', 'rowPatch', 'updatedAt'], 'PATCH 响应不得返回成员或完整团队摘要')
  assert.deepEqual(changedPayload.changedFields, ['name'])
  assert.deepEqual(changedPayload.rowPatch, { name: '系统团队按需写已更新' })
  const changedVersion = String(changedPayload.updatedAt)

  const staleHttp = await fetch(`${baseUrl}/__aisys__/api/system-teams/${team.id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: '过期 HTTP 覆盖', expectedUpdatedAt: currentVersion })
  })
  assert.equal(staleHttp.status, 409, 'HTTP 过期版本必须返回 409')
  assert.equal((await staleHttp.json()).message, '团队已被其他操作更新，请刷新后重试')

  const missingVersionHttp = await fetch(`${baseUrl}/__aisys__/api/system-teams/${team.id}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: '缺少版本' })
  })
  assert.equal(missingVersionHttp.status, 400, '缺少 expectedUpdatedAt 必须在路由边界拒绝')

  const routeSource = readFileSync(resolve('src/modules/system-teams/system-teams.routes.ts'), 'utf8')
  assert.match(routeSource, /log:\s*outcome\.status === 'updated'/, '只有真实更新才允许生成操作日志')
  assert.doesNotMatch(sourceBetween(routeSource, "systemTeamsRouter.patch('/:id'", "systemTeamsRouter.post('/:id/members'"), /findSystemTeamSummaryAsync|teamMemberTargets|teamMemberViewers/, '编辑路由不得读取团队摘要或成员关系')
  assert.ok(changedVersion > currentVersion, 'HTTP 真实变更必须单调推进版本')

  console.log('系统团队按需写回归通过：字段级投影、作用域 CAS、no-op 零写入、最小 HTTP 响应均已验证')
} finally {
  unregisterInvalidator()
  if (server?.listening) await closeServer(server)
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

interface SqlCall {
  kind: 'get' | 'all' | 'run'
  sql: string
  params: SQLInputValue[]
}

async function captureSql<T>(operation: () => Promise<T>): Promise<{ result: T; calls: SqlCall[] }> {
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const calls: SqlCall[] = []
  database.prepare = ((sql: string) => {
    const statement = originalPrepare(sql)
    const originalGet = statement.get.bind(statement) as typeof statement.get
    const originalAll = statement.all.bind(statement) as typeof statement.all
    const originalRun = statement.run.bind(statement) as typeof statement.run
    statement.get = ((...params: SQLInputValue[]) => {
      calls.push({ kind: 'get', sql, params })
      return originalGet(...params)
    }) as typeof statement.get
    statement.all = ((...params: SQLInputValue[]) => {
      calls.push({ kind: 'all', sql, params })
      return originalAll(...params)
    }) as typeof statement.all
    statement.run = ((...params: SQLInputValue[]) => {
      calls.push({ kind: 'run', sql, params })
      return originalRun(...params)
    }) as typeof statement.run
    return statement
  }) as typeof database.prepare
  try {
    return { result: await operation(), calls }
  } finally {
    database.prepare = originalPrepare
  }
}

function systemTeamDml(calls: SqlCall[]): SqlCall[] {
  return calls.filter((call) => call.kind === 'run' && /(?:UPDATE|INSERT|DELETE)\s+(?:FROM\s+)?system_teams/i.test(call.sql))
}

function selectSql(calls: SqlCall[]): string {
  return calls.filter((call) => call.kind !== 'run').map((call) => call.sql).join('\n')
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex)
  assert.notEqual(startIndex, -1)
  assert.notEqual(endIndex, -1)
  return source.slice(startIndex, endIndex)
}

async function onceListening(target: NonNullable<typeof server>): Promise<void> {
  if (target.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    target.once('listening', resolvePromise)
    target.once('error', rejectPromise)
  })
}

async function closeServer(target: NonNullable<typeof server>): Promise<void> {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    target.close((error) => error ? rejectPromise(error) : resolvePromise())
    target.closeIdleConnections?.()
  })
}
