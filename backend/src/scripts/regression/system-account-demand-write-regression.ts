import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import express from 'express'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-system-account-demand-write-'))
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_PROCESS_ROLE = 'worker'
process.env.JUHE_AI_DATABASE_DRIVER = 'sqlite'
process.env.JUHE_AI_CACHE_DRIVER = 'memory'
process.env.JUHE_AI_RUNTIME_STATE_DRIVER = 'memory'
process.env.JUHE_AI_SQLITE_READ_WORKER_POOL_SIZE = '0'
process.env.JUHE_AI_DATABASE_PATH = join(tempRoot, 'business.sqlite3')
process.env.JUHE_AI_DATASET_DATABASE_PATH = join(tempRoot, 'dataset.sqlite3')
process.env.JUHE_AI_USAGE_CATALOG_DATABASE_PATH = join(tempRoot, 'usage-catalog.sqlite3')
process.env.JUHE_AI_STATS_DATABASE_PATH = join(tempRoot, 'stats.sqlite3')
process.env.JUHE_AI_USAGE_SHARD_ROOT = join(tempRoot, 'usage-shards')
process.env.JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT = join(tempRoot, 'codex-context')

const [
  { systemAccountsRouter },
  { requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  { logger },
  gatewayInvalidation
] = await Promise.all([
  import('../../modules/system-accounts/system-accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../shared/logger.js'),
  import('../../shared/gateway-cache-invalidation.js')
])

logger.level = 'silent'
const database = databaseModule.getBusinessDatabase()
const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/system-accounts', systemAccountsRouter)

let server: ReturnType<typeof app.listen> | undefined
const runtimeInvalidations: string[] = []
const validationInvalidations: string[] = []
const unregisterRuntimeInvalidation = gatewayInvalidation.registerGatewayRuntimeCacheInvalidator((reason) => {
  runtimeInvalidations.push(reason)
})
const unregisterValidationInvalidation = gatewayInvalidation.registerGatewayApiKeyValidationCacheInvalidator((_apiKeyId, metadata) => {
  if (metadata.source === 'local') validationInvalidations.push('called')
})

try {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '系统账户按需写回归需要默认超级管理员')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const target = repositories.createSystemAccount({
    username: `system_account_demand_${Date.now()}`,
    displayName: `按需写用户${Date.now()}`,
    description: '初始说明',
    password: 'password',
    role: 'user',
    status: 'active',
    mustChangePassword: true
  })
  const targetMicrosecondRevision = '2999-07-29T01:02:03.123456Z'
  database.prepare('UPDATE system_accounts SET updated_at = ? WHERE id = ?').run(targetMicrosecondRevision, target.id)
  const disabledTarget = repositories.createSystemAccount({
    username: `system_account_disabled_noop_${Date.now()}`,
    displayName: `停用同值用户${Date.now()}`,
    password: 'password',
    role: 'user',
    status: 'disabled',
    mustChangePassword: true
  })
  const disabledSession = repositories.createSession(disabledTarget.id, 1)
  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '系统账户按需写回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const created = await postEnvelope<Record<string, unknown>>(
    baseUrl,
    '/__aisys__/api/system-accounts',
    cookie,
    {
      username: `system_account_created_${Date.now()}`,
      displayName: `创建回执用户${Date.now()}`,
      description: '创建回执说明',
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: true,
      imageGenerationEnabled: false
    }
  )
  assert.equal(typeof created.editVersion, 'string', '创建回执必须直接携带列表编辑版本')
  assert.equal(Object.hasOwn(created, 'createdAt'), false, '创建回执不得返回列表不使用的 createdAt')
  assert.equal(Object.hasOwn(created, 'updatedAt'), false, '创建回执必须以 editVersion 代替详情 updatedAt')
  assert.equal(Object.hasOwn(created, 'lastLoginAt'), false, '创建回执不得返回无消费者的登录详情')

  const page = await getEnvelope<SystemAccountListResult>(
    baseUrl,
    `/__aisys__/api/system-accounts?keyword=${encodeURIComponent(target.username)}&page=1&pageSize=20`,
    cookie
  )
  const listed = page.items.find((item) => item.id === target.id)
  assert(listed, '系统账户列表应返回待编辑行')
  assert.equal(listed.editVersion, targetMicrosecondRevision, '列表必须直接携带不丢微秒的编辑 CAS 版本')
  assert.equal(Object.hasOwn(listed, 'updatedAt'), false, '列表不得暴露完整详情 updatedAt 字段')

  database.exec(`
    CREATE TRIGGER system_account_demand_unrelated_guard
    BEFORE UPDATE OF role, status, password_hash, must_change_password, image_generation_enabled, request_limits_json ON system_accounts
    BEGIN
      SELECT RAISE(ABORT, 'unrelated system account column updated');
    END
  `)

  const noOpCapture = await captureSystemAccountSql(() => patchEnvelope<SystemAccountMutationResult>(
    baseUrl,
    `/__aisys__/api/system-accounts/${target.id}`,
    cookie,
    { expectedUpdatedAt: listed.editVersion, description: '初始说明' }
  ))
  assert.deepEqual(noOpCapture.result, { id: target.id, updatedAt: listed.editVersion }, 'no-op 只应返回定位与当前版本')
  assert.equal(noOpCapture.sql.filter((sql) => /^\s*UPDATE\b/i.test(sql)).length, 0, 'no-op 不得执行 UPDATE')
  assert.deepEqual(systemAccountPatchSelectColumns(noOpCapture.sql), ['description', 'display_name', 'id', 'updated_at'], '说明 PATCH 只应读取说明、日志名称、定位与版本')

  const disabledNoOp = await patchEnvelope<SystemAccountMutationResult>(
    baseUrl,
    `/__aisys__/api/system-accounts/${disabledTarget.id}`,
    cookie,
    { expectedUpdatedAt: withTrailingRevisionZero(disabledTarget.updatedAt), status: 'disabled' }
  )
  assert.deepEqual(disabledNoOp, { id: disabledTarget.id, updatedAt: disabledTarget.updatedAt }, 'disabled 同值 PATCH 必须是 no-op')
  assert.equal(systemSessionExists(disabledSession.sessionId), true, 'disabled 同值 PATCH 不得重复撤销账户会话')

  const before = systemAccountRow(target.id)
  const updateCapture = await captureSystemAccountSql(() => patchEnvelope<SystemAccountMutationResult>(
    baseUrl,
    `/__aisys__/api/system-accounts/${target.id}`,
    cookie,
    { expectedUpdatedAt: listed.editVersion, description: '只改说明' }
  ))
  assert.deepEqual(Object.keys(updateCapture.result).sort(), ['description', 'id', 'updatedAt'], '单字段 PATCH 回执不得返回完整系统账户')
  assert.equal(updateCapture.result.description, '只改说明')
  assert.notEqual(updateCapture.result.updatedAt, listed.editVersion, '实际更新必须推进版本')
  assert(Date.parse(updateCapture.result.updatedAt) > Date.parse(listed.editVersion), '微秒版本在本机时间较旧时仍必须单调推进')
  const updateSql = updateCapture.sql.find((sql) => /^\s*UPDATE\b/i.test(sql) && /\bsystem_accounts\b/i.test(sql))
  assert(updateSql, '实际更新必须执行系统账户 UPDATE')
  assert.match(updateSql, /SET\s+description\s*=\s*\?\s*,\s*updated_at\s*=\s*\?/i, '说明 PATCH 只能 SET 说明和版本')
  assert.match(updateSql, /WHERE\s+id\s*=\s*\?\s+AND\s+updated_at\s*=\s*\?/i, '系统账户写入必须由数据库 CAS 保护')

  const after = systemAccountRow(target.id)
  assert.deepEqual(changedDatabaseColumns(before, after), ['description', 'updated_at'], '说明 PATCH 只能改变说明和版本列')
  assert.deepEqual(runtimeInvalidations, [], '说明与 no-op PATCH 不得触发 runtime cache 失效')
  assert.deepEqual(validationInvalidations, [], '说明与 no-op PATCH 不得触发 API Key validation cache 失效')

  const displayNameTarget = repositories.createSystemAccount({
    username: `system_account_display_${Date.now()}`,
    displayName: `显示名用户${Date.now()}`,
    password: 'password',
    role: 'user',
    status: 'active'
  })
  await patchEnvelope<SystemAccountMutationResult>(
    baseUrl,
    `/__aisys__/api/system-accounts/${displayNameTarget.id}`,
    cookie,
    { expectedUpdatedAt: displayNameTarget.updatedAt, displayName: `${displayNameTarget.displayName}-更新` }
  )
  assert.deepEqual(runtimeInvalidations, [], '显示名 PATCH 不得触发 runtime cache 失效')
  assert.deepEqual(validationInvalidations, [], '显示名 PATCH 不得触发 API Key validation cache 失效')

  database.exec('DROP TRIGGER system_account_demand_unrelated_guard')
  const passwordSession = repositories.createSession(target.id, 1)
  const beforePassword = systemAccountRow(target.id)
  database.exec(`
    CREATE TRIGGER system_account_session_revoke_failure
    BEFORE DELETE ON system_sessions
    BEGIN
      SELECT RAISE(ABORT, 'session revoke failed');
    END
  `)
  const failedPasswordPatch = await patchRawEnvelope(
    baseUrl,
    `/__aisys__/api/system-accounts/${target.id}`,
    cookie,
    {
      expectedUpdatedAt: updateCapture.result.updatedAt,
      password: 'rollback-password',
      mustChangePassword: true
    }
  )
  assert.equal(failedPasswordPatch.status, 409, '会话撤销失败时密码 PATCH 必须失败')
  assert.deepEqual(systemAccountRow(target.id), beforePassword, '会话撤销失败时密码哈希与版本必须一并回滚')
  assert.equal(systemSessionExists(passwordSession.sessionId), true, '会话撤销失败时原会话应保留')
  database.exec('DROP TRIGGER system_account_session_revoke_failure')

  const passwordCapture = await captureManagementSql(() => patchRawEnvelope(
    baseUrl,
    `/__aisys__/api/system-accounts/${target.id}`,
    cookie,
    {
      expectedUpdatedAt: updateCapture.result.updatedAt,
      password: 'replacement-password',
      mustChangePassword: true
    }
  ))
  const passwordPatch = passwordCapture.result
  assert.equal(passwordPatch.status, 200, `密码 PATCH 失败：${passwordPatch.text}`)
  assert.doesNotMatch(passwordPatch.text, /replacement-password|passwordHash|password_hash/i, '密码 PATCH 回执不得包含明文密码或密码哈希')
  const passwordResult = (JSON.parse(passwordPatch.text) as ApiEnvelope<SystemAccountMutationResult>).data
  assert.deepEqual(Object.keys(passwordResult).sort(), ['id', 'updatedAt'], '密码 PATCH 只应返回定位与新版本')
  assert.equal(systemSessionExists(passwordSession.sessionId), false, '实际密码变更必须撤销目标账户会话')
  assert(passwordCapture.sql.some((sql) => /^\s*DELETE\s+FROM\s+["`]?system_sessions["`]?\b/i.test(sql)), '密码变更必须在 repository 事务内撤销会话')
  const afterPassword = systemAccountRow(target.id)
  assert.deepEqual(changedDatabaseColumns(beforePassword, afterPassword), ['password_hash', 'updated_at'], '密码 PATCH 只能改变密码哈希和版本列')

  const samePasswordSession = repositories.createSession(target.id, 1)
  const samePasswordCapture = await captureManagementSql(() => patchEnvelope<SystemAccountMutationResult>(
    baseUrl,
    `/__aisys__/api/system-accounts/${target.id}`,
    cookie,
    { expectedUpdatedAt: passwordResult.updatedAt, password: 'replacement-password' }
  ))
  assert.deepEqual(samePasswordCapture.result, { id: target.id, updatedAt: passwordResult.updatedAt }, '同值密码 PATCH 只应返回定位与当前版本')
  assert.deepEqual(systemAccountPatchSelectColumns(samePasswordCapture.sql), ['display_name', 'id', 'password_hash', 'updated_at'], '密码 PATCH 只应额外读取密码哈希')
  assert.equal(samePasswordCapture.sql.filter((sql) => /^\s*UPDATE\b/i.test(sql)).length, 0, '同值密码 PATCH 不得执行 UPDATE')
  assert.equal(samePasswordCapture.sql.filter((sql) => /^\s*DELETE\s+FROM\s+["`]?system_sessions/i.test(sql)).length, 0, '同值密码 PATCH 不得撤销会话')
  assert.equal(systemSessionExists(samePasswordSession.sessionId), true, '同值密码 PATCH 必须保留现有会话')

  const statusTarget = repositories.createSystemAccount({
    username: `system_account_disable_${Date.now()}`,
    displayName: `实际停用用户${Date.now()}`,
    password: 'password',
    role: 'user',
    status: 'active'
  })
  const statusSession = repositories.createSession(statusTarget.id, 1)
  const disabledResult = await patchEnvelope<SystemAccountMutationResult & { status?: string }>(
    baseUrl,
    `/__aisys__/api/system-accounts/${statusTarget.id}`,
    cookie,
    { expectedUpdatedAt: statusTarget.updatedAt, status: 'disabled' }
  )
  assert.equal(disabledResult.status, 'disabled', '实际停用 PATCH 应返回变化后状态')
  assert.equal(systemSessionExists(statusSession.sessionId), false, '实际停用必须在同一 PATCH 事务中撤销会话')
  assert.deepEqual(runtimeInvalidations, ['system_account_status_changed'], '状态变化只能触发一次正确的 runtime cache 失效')
  assert.equal(validationInvalidations.length, 1, '状态变化只能触发一次 API Key validation cache 失效')

  const imageTarget = repositories.createSystemAccount({
    username: `system_account_image_${Date.now()}`,
    displayName: `图像权限用户${Date.now()}`,
    password: 'password',
    role: 'user',
    status: 'active'
  })
  await patchEnvelope<SystemAccountMutationResult>(
    baseUrl,
    `/__aisys__/api/system-accounts/${imageTarget.id}`,
    cookie,
    { expectedUpdatedAt: imageTarget.updatedAt, imageGenerationEnabled: true }
  )
  assert.deepEqual(runtimeInvalidations, [
    'system_account_status_changed',
    'system_account_image_generation_changed'
  ], '图像权限变化只能追加一次正确的 runtime cache 失效')
  assert.equal(validationInvalidations.length, 2, '图像权限变化只能追加一次 API Key validation cache 失效')

  const limitTarget = repositories.createSystemAccount({
    username: `system_account_limits_${Date.now()}`,
    displayName: `请求限制用户${Date.now()}`,
    password: 'password',
    role: 'user',
    status: 'active'
  })
  await patchEnvelope<SystemAccountMutationResult>(
    baseUrl,
    `/__aisys__/api/system-accounts/${limitTarget.id}`,
    cookie,
    { expectedUpdatedAt: limitTarget.updatedAt, requestLimits: { perMinute: 7 } }
  )
  assert.deepEqual(runtimeInvalidations, [
    'system_account_status_changed',
    'system_account_image_generation_changed',
    'system_account_request_limits_changed'
  ], '请求限制变化只能追加一次正确的 runtime cache 失效')
  assert.equal(validationInvalidations.length, 3, '请求限制变化只能追加一次 API Key validation cache 失效')

  const staleCapture = await captureSystemAccountSql(() => patchStatus(
    baseUrl,
    `/__aisys__/api/system-accounts/${target.id}`,
    cookie,
    { expectedUpdatedAt: listed.editVersion, description: '陈旧覆盖' }
  ))
  assert.equal(staleCapture.result, 409, '陈旧版本必须返回 409')
  assert.equal(staleCapture.sql.filter((sql) => /^\s*UPDATE\b/i.test(sql)).length, 0, '陈旧版本不得产生 DML')
  assert.equal(systemAccountRow(target.id).description, '只改说明', '陈旧 PATCH 不得覆盖已提交内容')

  assertSourceContracts()
  console.log('系统账户按需写回归通过：列表版本、窄投影、动态 SET、no-op、CAS 与最小回执均已固定')
} finally {
  unregisterRuntimeInvalidation()
  unregisterValidationInvalidation()
  await closeServer(server)
  try {
    try {
      database.close()
    } catch {
    }
    databaseModule.closeStorageDatabases()
  } catch {
  }
  try {
    rmSync(tempRoot, { recursive: true, force: true })
  } catch {
    // Windows can retain the operation-log database handle until process exit.
  }
}

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface SystemAccountListResult {
  items: Array<{ id: string; editVersion: string; updatedAt?: string }>
}

interface SystemAccountMutationResult {
  id: string
  updatedAt: string
  description?: string | null
}

interface SystemAccountDatabaseRow {
  id: string
  username: string
  display_name: string
  description: string | null
  role: string
  status: string
  password_hash: string
  must_change_password: number
  image_generation_enabled: number
  request_limits_json: string | null
  last_login_at: string | null
  created_at: string
  updated_at: string
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  assert.equal(response.status, 200, `GET ${path} 失败：${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await patchRawEnvelope(baseUrl, path, cookie, body)
  assert.equal(response.status, 200, `PATCH ${path} 失败：${response.text}`)
  return (JSON.parse(response.text) as ApiEnvelope<T>).data
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', cookie },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 201, `POST ${path} 失败：${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function patchRawEnvelope(baseUrl: string, path: string, cookie: string, body: unknown): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json', cookie },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  return { status: response.status, text }
}

async function patchStatus(baseUrl: string, path: string, cookie: string, body: unknown): Promise<number> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json', cookie },
    body: JSON.stringify(body)
  })
  await response.arrayBuffer()
  return response.status
}

async function captureSystemAccountSql<T>(operation: () => Promise<T>): Promise<{ result: T; sql: string[] }> {
  return captureSql(operation, (statementSql) => /\bsystem_accounts\b/i.test(statementSql))
}

async function captureManagementSql<T>(operation: () => Promise<T>): Promise<{ result: T; sql: string[] }> {
  return captureSql(operation, (statementSql) => /\b(?:system_accounts|system_sessions)\b/i.test(statementSql))
}

async function captureSql<T>(operation: () => Promise<T>, shouldCapture: (sql: string) => boolean): Promise<{ result: T; sql: string[] }> {
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const sql: string[] = []
  database.prepare = ((statementSql: string) => {
    if (shouldCapture(statementSql)) sql.push(statementSql)
    return originalPrepare(statementSql)
  }) as typeof database.prepare
  try {
    return { result: await operation(), sql }
  } finally {
    database.prepare = originalPrepare
  }
}

function systemAccountPatchSelectColumns(sql: string[]): string[] {
  const selectSql = sql.find((statementSql) => /\bFROM\s+["`]?system_accounts["`]?\b/i.test(statementSql) && /\bWHERE\s+id\s*=\s*\?/i.test(statementSql))
  assert(selectSql, `未捕获系统账户 PATCH SELECT：${sql.join('\n')}`)
  const projection = selectSql.match(/\bSELECT\b([\s\S]*?)\bFROM\b/i)?.[1]
  assert(projection, '无法解析系统账户 PATCH 投影')
  return projection.split(',').map((column) => column.trim().replace(/["`]/g, '').toLowerCase()).sort()
}

function systemAccountRow(id: string): SystemAccountDatabaseRow {
  const row = database.prepare('SELECT * FROM system_accounts WHERE id = ?').get(id) as unknown as SystemAccountDatabaseRow | undefined
  assert(row, '系统账户数据库行不存在')
  return row
}

function systemSessionExists(id: string): boolean {
  return Boolean(database.prepare('SELECT 1 FROM system_sessions WHERE id = ?').get(id))
}

function withTrailingRevisionZero(value: string): string {
  return value.replace(/Z$/i, '0Z')
}

function changedDatabaseColumns(before: SystemAccountDatabaseRow, after: SystemAccountDatabaseRow): string[] {
  return Object.keys(before).filter((key) => before[key as keyof SystemAccountDatabaseRow] !== after[key as keyof SystemAccountDatabaseRow]).sort()
}

function assertSourceContracts(): void {
  const repositorySource = readFileSync(fileURLToPath(new URL('../../storage/system-accounts.repository.ts', import.meta.url)), 'utf8')
  const patchSource = sourceBetween(repositorySource, 'export async function patchSystemAccountManagementAsync(', 'export function updateSystemAccount(')
  assert.match(patchSource, /systemAccountManagementPatchSelectColumns\(input\)/, 'PATCH 必须按提交字段生成最小投影')
  assert.match(patchSource, /SET \$\{assignments\.join\(', '\)\}, updated_at = \?/, 'PATCH 必须动态生成 SET')
  assert.match(patchSource, /systemAccountPatchRevisionPredicate\(\)/, 'SQLite 与 PostgreSQL 必须对 ISO 文本版本使用同一精确 CAS 谓词')
  assert.match(patchSource, /current\.updated_at\]\)/, 'CAS 必须使用锁行读取到的精确版本')
  assert.match(patchSource, /tx\.driver === 'postgres' \? ' FOR UPDATE'/, 'PostgreSQL PATCH 必须锁定当前系统账户行')
  assert.match(repositorySource, /SELECT id, username,[\s\S]{0,400}\bupdated_at\s*\n\s*FROM/, 'PostgreSQL 列表必须原样读取 ISO 文本版本')
  assert.doesNotMatch(repositorySource, /updated_at\s+AT\s+TIME\s+ZONE|updated_at\s*=\s*CAST\(\?\s+AS\s+timestamptz\)/i, 'ISO 文本版本不得按 timestamptz 投影或比较')
  assert.doesNotMatch(patchSource, /findSystemAccountById|systemAccountSummaryFromRow/, '管理 PATCH 不得物化完整系统账户摘要')
  assert.doesNotMatch(patchSource, /after:\s*input\.password/, '系统账户 PATCH 内部变更描述不得携带明文密码')
  assert.match(patchSource, /verifyPasswordAsync\(password, currentPasswordHash\)/, '密码 PATCH 必须识别同值并保持零写入')
  assert.match(patchSource, /systemAccountPatchRevokesSessions\(changes\)[\s\S]*DELETE FROM \$\{systemAccountTable\(tx, 'system_sessions'\)\}/, '实际停用或密码变更必须在 PATCH 事务内撤销会话')
  assert.match(
    patchSource,
    /const runtimeReason = systemAccountManagementRuntimeInvalidationReason\(outcome\.changes\)[\s\S]*notifyGatewayRuntimeCacheInvalidation\(runtimeReason\)[\s\S]*notifyGatewayApiKeyValidationCacheInvalidationAsync\(undefined, runtimeReason\)/,
    'runtime 与 API Key validation cache 必须复用按实际变化派生的同一失效原因'
  )

  const routeSource = readFileSync(fileURLToPath(new URL('../../modules/system-accounts/system-accounts.routes.ts', import.meta.url)), 'utf8')
  const routePatch = sourceBetween(routeSource, "systemAccountsRouter.patch('/:id'", 'function systemAccountWhitespaceError(')
  assert.doesNotMatch(routePatch, /findSystemAccountByIdAsync|updateSystemAccountWithPasswordHashAsync/, '路由不得重复宽读或回退整行更新')
  assert.doesNotMatch(routePatch, /revokeAllSessionsForAccountAsync/, '路由不得在账户 PATCH 事务之后再单独撤销会话')
  assert.match(routePatch, /outcome\.changes\.length \?/, 'no-op 不得记录操作日志')

  const asyncFindSource = sourceBetween(repositorySource, 'async function findSystemAccountByIdWithClient(', 'export function findSystemAccountByUsername(')
  assert.doesNotMatch(asyncFindSource, /password_hash/, 'PostgreSQL 系统账户详情读不得读取未使用的密码哈希')
}

function sourceBetween(source: string, start: string, end: string): string {
  const startIndex = source.indexOf(start)
  const endIndex = source.indexOf(end, startIndex + start.length)
  assert(startIndex >= 0 && endIndex > startIndex, `无法提取源码片段：${start} -> ${end}`)
  return source.slice(startIndex, endIndex)
}

async function onceListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, reject) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', reject)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer) return
  await new Promise<void>((resolvePromise) => listeningServer.close(() => resolvePromise()))
}
