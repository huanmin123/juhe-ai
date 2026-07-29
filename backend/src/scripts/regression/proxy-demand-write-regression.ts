import assert from 'node:assert/strict'
import type { SQLInputValue } from 'node:sqlite'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-proxy-demand-write-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'proxy-demand-write-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, cacheInvalidation, { proxiesRouter }, authMiddleware, requestContext] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../shared/gateway-cache-invalidation.js'),
  import('../../modules/proxies/proxies.routes.js'),
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
  const owner = repositories.createSystemAccount({
    username: 'proxy_demand_owner',
    displayName: '代理按需写所有者',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const outsider = repositories.createSystemAccount({
    username: 'proxy_demand_outsider',
    displayName: '代理按需写其他账户',
    password: 'password',
    role: 'admin',
    status: 'active',
    mustChangePassword: false
  })
  const ordinaryUser = repositories.createSystemAccount({
    username: 'proxy_demand_user',
    displayName: '代理按需写普通用户',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: false
  })
  const ownerAccess = { systemAccountId: owner.id, role: 'admin' as const }
  const outsiderAccess = { systemAccountId: outsider.id, role: 'admin' as const }
  const target = await repositories.createProxyAsync({
    name: '代理按需写回归',
    description: '原说明',
    type: 'http',
    host: '127.0.0.1',
    port: 18_080,
    username: 'owner-user',
    password: 'secret',
    enabled: true
  }, ownerAccess)
  const other = await repositories.createProxyAsync({
    name: '其他账户代理',
    type: 'http',
    host: '127.0.0.2',
    port: 18_081,
    enabled: true
  }, outsiderAccess)

  const listed = await repositories.listProxiesPageAsync({ page: 1, pageSize: 20 })
  assert.deepEqual(new Set(listed.items.map((item) => item.id)), new Set([target.id, other.id]), '代理管理列表必须保持服务器级全局资源语义')
  assert(target.updatedAt, '创建和列表行必须直接携带编辑 CAS 版本')
  assert(!JSON.stringify(listed).includes('secret'), '列表响应不得返回代理密码或密文')

  invalidations.length = 0
  const descriptionPatch = await captureSql(() => repositories.patchProxyForManagementAsync(
    target.id,
    { description: '新说明' },
    target.updatedAt
  ))
  assert.equal(descriptionPatch.result?.mutation.changed, true)
  assert.deepEqual(descriptionPatch.result?.mutation.values, { description: '新说明' }, '响应只能返回实际变化字段')
  const patchSelect = descriptionPatch.calls.find((call) => call.kind === 'get' && /SELECT[\s\S]+FROM proxy_profiles/i.test(call.sql))?.sql ?? ''
  assert.match(patchSelect, /SELECT\s+id,\s*name,\s*updated_at,\s*description/i, '说明 PATCH 只应读取定位、审计名称、版本和说明')
  assert.doesNotMatch(patchSelect, /password_encrypted|\bhost\b|\bport\b|\busername\b|test_status|latency_ms/i, '说明 PATCH 不得读取连接密文或检测状态')
  const patchDml = proxyDml(descriptionPatch.calls)
  assert.equal(patchDml.length, 1, '说明 PATCH 只允许一条代理 DML')
  assert.match(patchDml[0]?.sql ?? '', /SET\s+description\s*=\s*\?,\s*updated_at\s*=\s*CASE/i, '说明 PATCH 只能更新说明和版本')
  assert.match(patchDml[0]?.sql ?? '', /WHERE\s+id\s*=\s*\?\s+AND\s+updated_at\s*=\s*\?/i, 'ID 和版本必须进入 UPDATE SQL CAS')
  assert.deepEqual(invalidations, [], '名称和说明不参与代理运行时解析，不得扩大缓存失效')

  const currentVersion = descriptionPatch.result?.mutation.updatedAt ?? ''
  invalidations.length = 0
  const noOp = await captureSql(() => repositories.patchProxyForManagementAsync(
    target.id,
    { description: '新说明' },
    currentVersion
  ))
  assert.deepEqual(noOp.result?.mutation, { id: target.id, updatedAt: currentVersion, changed: false, values: {} })
  assert.deepEqual(proxyDml(noOp.calls), [], '同值 PATCH 必须零 DML')
  assert.equal(database.isTransaction, false, 'SQLite no-op 返回前必须提交并关闭事务')
  assert.deepEqual(invalidations, [], '同值 PATCH 不得触发缓存失效')

  const equivalentPrecisionVersion = currentVersion.replace(/(\.\d{3})Z$/, '$1000Z')
  assert.notEqual(equivalentPrecisionVersion, currentVersion, 'SQLite CAS 精度回归需要构造等价的六位小数版本')
  const equivalentPrecisionPatch = await repositories.patchProxyForManagementAsync(
    target.id,
    { description: '等价精度版本更新' },
    equivalentPrecisionVersion
  )
  assert.equal(equivalentPrecisionPatch?.mutation.changed, true, '语义相同但小数位宽不同的 CAS 版本必须直接命中当前锁行')
  const postPrecisionVersion = equivalentPrecisionPatch?.mutation.updatedAt ?? ''

  invalidations.length = 0
  const samePasswordNoOp = await captureSql(() => repositories.patchProxyForManagementAsync(
    target.id,
    { password: 'secret' },
    postPrecisionVersion
  ))
  assert.deepEqual(samePasswordNoOp.result?.mutation, { id: target.id, updatedAt: postPrecisionVersion, changed: false, values: {} })
  assert.deepEqual(proxyDml(samePasswordNoOp.calls), [], '同值密码 PATCH 必须在重新加密前识别为 no-op，避免随机密文制造假更新')
  assert.equal(samePasswordNoOp.result?.passwordChanged, false, '同值密码不得生成敏感变更日志')
  assert.deepEqual(invalidations, [], '同值密码不得重置运行态或触发缓存失效')

  const stale = await captureOutcome(() => repositories.patchProxyForManagementAsync(
    target.id,
    { description: '陈旧覆盖' },
    target.updatedAt
  ))
  assert.equal(stale.error?.name, 'ProxyProfileUpdateConflictError')
  assert.equal(database.isTransaction, false, 'CAS 冲突必须回滚并关闭事务')

  const missing = await captureSql(() => repositories.patchProxyForManagementAsync(
    'proxy_missing',
    { description: '不存在' },
    currentVersion
  ))
  assert.equal(missing.result, undefined)
  assert.deepEqual(proxyDml(missing.calls), [], '不存在的代理 PATCH 必须零 DML')
  assert.equal(database.isTransaction, false, '未命中 ID 时也必须关闭事务')

  invalidations.length = 0
  const enabledPatch = await repositories.patchProxyForManagementAsync(target.id, { enabled: false }, postPrecisionVersion)
  assert.equal(enabledPatch?.mutation.values.enabled, false, 'SQLite 布尔更新必须保存为兼容的整数绑定值')
  assert.equal((await repositories.findProxyAsync(target.id))?.enabled, false)
  assert.deepEqual(invalidations, ['proxy_updated'], '启停变化必须失效代理运行时缓存')
  const enabledVersion = enabledPatch?.mutation.updatedAt ?? ''

  const diagnosticAt = new Date(Date.now() + 1_000).toISOString()
  const diagnostic = await repositories.updateProxyTestStateAsync(target.id, {
    testStatus: 'passed',
    latencyMs: 18,
    lastTestMessage: '停用代理历史检测结果',
    lastTestedAt: diagnosticAt,
    expectedConfigUpdatedAt: enabledVersion
  })
  assert.equal(diagnostic?.testStatus, 'passed')
  invalidations.length = 0
  const disabledNoOp = await captureSql(() => repositories.patchProxyForManagementAsync(
    target.id,
    { enabled: false },
    enabledVersion
  ))
  assert.deepEqual(disabledNoOp.result?.mutation, { id: target.id, updatedAt: enabledVersion, changed: false, values: {} })
  assert.deepEqual(proxyDml(disabledNoOp.calls), [], '已停用代理再次提交 enabled=false 必须零 DML')
  assert.equal((await repositories.findProxyAsync(target.id))?.testStatus, 'passed', '停用同值 PATCH 不得清理既有检测状态')
  assert.deepEqual(invalidations, [], '停用同值 PATCH 不得撤销运行态或触发缓存失效')

  const app = express()
  app.use(requestContext.requestContextMiddleware)
  app.use(express.json({ limit: '1mb' }))
  app.use('/__aisys__/api', authMiddleware.requireAuth)
  app.use('/__aisys__/api/proxies', proxiesRouter)
  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string')
  const baseUrl = `http://127.0.0.1:${address.port}/__aisys__/api/proxies`
  const ownerCookie = `juhe_ai_session=${repositories.createSession(owner.id, 1).token}`
  const outsiderCookie = `juhe_ai_session=${repositories.createSession(outsider.id, 1).token}`
  const ordinaryUserCookie = `juhe_ai_session=${repositories.createSession(ordinaryUser.id, 1).token}`

  const ordinaryList = await fetch(`${baseUrl}?page=1&pageSize=20`, { headers: { cookie: ordinaryUserCookie } })
  assert.equal(ordinaryList.status, 403, '普通用户不得读取代理管理列表')
  const ordinaryCreate = await fetch(baseUrl, {
    method: 'POST',
    headers: { cookie: ordinaryUserCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ name: '普通用户越权代理', type: 'http', host: '127.0.0.9', port: 18_089 })
  })
  assert.equal(ordinaryCreate.status, 403, '普通用户不得创建服务器级全局代理')
  const ordinaryPatch = await fetch(`${baseUrl}/${target.id}`, {
    method: 'PATCH',
    headers: { cookie: ordinaryUserCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: '普通用户越权更新', expectedUpdatedAt: enabledVersion })
  })
  assert.equal(ordinaryPatch.status, 403, '普通用户不得更新服务器级全局代理')
  const ordinaryDelete = await fetch(`${baseUrl}/${other.id}`, {
    method: 'DELETE',
    headers: { cookie: ordinaryUserCookie }
  })
  assert.equal(ordinaryDelete.status, 403, '普通用户不得删除服务器级全局代理')

  const listHttp = await fetch(`${baseUrl}?page=1&pageSize=20`, { headers: { cookie: ownerCookie } })
  assert.equal(listHttp.status, 200)
  const listPayload = (await listHttp.json()).data as { items: Array<{ id: string }> }
  assert.deepEqual(new Set(listPayload.items.map((item) => item.id)), new Set([target.id, other.id]), 'HTTP 管理列表必须返回全局代理')

  const changedHttp = await fetch(`${baseUrl}/${target.id}`, {
    method: 'PATCH',
    headers: { cookie: ownerCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ name: '代理按需写已更新', expectedUpdatedAt: enabledVersion })
  })
  assert.equal(changedHttp.status, 200)
  const changedPayload = (await changedHttp.json()).data as Record<string, unknown>
  assert.deepEqual(Object.keys(changedPayload).sort(), ['changed', 'id', 'updatedAt', 'values'], 'HTTP PATCH 只能返回最小 mutation')
  assert.deepEqual(changedPayload.values, { name: '代理按需写已更新' })

  const staleHttp = await fetch(`${baseUrl}/${target.id}`, {
    method: 'PATCH',
    headers: { cookie: ownerCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: 'HTTP 陈旧覆盖', expectedUpdatedAt: enabledVersion })
  })
  assert.equal(staleHttp.status, 409)

  const globalAdminPatch = await fetch(`${baseUrl}/${target.id}`, {
    method: 'PATCH',
    headers: { cookie: outsiderCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ description: '其他管理员维护全局代理', expectedUpdatedAt: changedPayload.updatedAt })
  })
  assert.equal(globalAdminPatch.status, 200, '任一管理员都应能维护服务器级全局代理')
  const globalAdminPayload = (await globalAdminPatch.json()).data as { updatedAt: string }

  const secretValue = 'proxy-demand-rotated-secret'
  const passwordPatch = await fetch(`${baseUrl}/${target.id}`, {
    method: 'PATCH',
    headers: { cookie: ownerCookie, 'content-type': 'application/json' },
    body: JSON.stringify({ password: secretValue, expectedUpdatedAt: globalAdminPayload.updatedAt })
  })
  assert.equal(passwordPatch.status, 200)
  const passwordPayload = (await passwordPatch.json()).data as Record<string, unknown>
  assert.deepEqual(Object.keys(passwordPayload).sort(), ['changed', 'id', 'updatedAt', 'values'], '密码 PATCH 仍只能返回最小 mutation')
  assert.equal(JSON.stringify(passwordPayload).includes(secretValue), false, '密码 PATCH 回执不得返回代理明文密码')
  assert.equal(JSON.stringify(passwordPayload).includes('password'), false, '密码 PATCH 回执不得暴露密码字段或设置标记')

  assert.equal((await repositories.deleteProxyForManagementAsync(other.id))?.id, other.id)
  assert.equal(database.isTransaction, false, 'SQLite 删除预检与删除完成后必须关闭事务')

  console.log('代理按需写回归通过：全局窄投影、字段级 CAS、no-op 零写入、最小 HTTP 回执均已验证')
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

async function captureOutcome(operation: () => Promise<unknown>): Promise<{ error?: Error }> {
  try {
    await operation()
    return {}
  } catch (error) {
    return { error: error instanceof Error ? error : new Error(String(error)) }
  }
}

function proxyDml(calls: SqlCall[]): SqlCall[] {
  return calls.filter((call) => /(?:UPDATE|INSERT|DELETE)\s+(?:FROM\s+)?proxy_profiles/i.test(call.sql))
}

async function onceListening(target: NonNullable<typeof server>): Promise<void> {
  if (target.listening) return
  await new Promise<void>((resolvePromise, reject) => {
    target.once('listening', resolvePromise)
    target.once('error', reject)
  })
}

async function closeServer(target: NonNullable<typeof server>): Promise<void> {
  await new Promise<void>((resolvePromise, reject) => {
    target.close((error) => error ? reject(error) : resolvePromise())
  })
}
