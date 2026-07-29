import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import express from 'express'

const tempRoot = mkdtempSync(join(tmpdir(), 'juhe-system-account-demand-write-'))
process.env.JUHE_AI_RUNTIME_MODE = 'standalone'
process.env.JUHE_AI_PROCESS_ROLE = 'db-service'
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
  { logger }
] = await Promise.all([
  import('../../modules/system-accounts/system-accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../shared/logger.js')
])

logger.level = 'silent'
const database = databaseModule.getBusinessDatabase()
const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/system-accounts', systemAccountsRouter)

let server: ReturnType<typeof app.listen> | undefined

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
  const cookie = `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`

  server = app.listen(0, '127.0.0.1')
  await onceListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '系统账户按需写回归服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`

  const page = await getEnvelope<SystemAccountListResult>(
    baseUrl,
    `/__aisys__/api/system-accounts?keyword=${encodeURIComponent(target.username)}&page=1&pageSize=20`,
    cookie
  )
  const listed = page.items.find((item) => item.id === target.id)
  assert(listed, '系统账户列表应返回待编辑行')
  assert.equal(listed.editVersion, target.updatedAt, '列表必须直接携带编辑 CAS 版本')
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
  const updateSql = updateCapture.sql.find((sql) => /^\s*UPDATE\b/i.test(sql) && /\bsystem_accounts\b/i.test(sql))
  assert(updateSql, '实际更新必须执行系统账户 UPDATE')
  assert.match(updateSql, /SET\s+description\s*=\s*\?\s*,\s*updated_at\s*=\s*\?/i, '说明 PATCH 只能 SET 说明和版本')
  assert.match(updateSql, /WHERE\s+id\s*=\s*\?\s+AND\s+updated_at\s*=\s*\?/i, '系统账户写入必须由数据库 CAS 保护')

  const after = systemAccountRow(target.id)
  assert.deepEqual(changedDatabaseColumns(before, after), ['description', 'updated_at'], '说明 PATCH 只能改变说明和版本列')

  const staleCapture = await captureSystemAccountSql(() => patchStatus(
    baseUrl,
    `/__aisys__/api/system-accounts/${target.id}`,
    cookie,
    { expectedUpdatedAt: listed.editVersion, description: '陈旧覆盖' }
  ))
  assert.equal(staleCapture.result, 409, '陈旧版本必须返回 409')
  assert.equal(staleCapture.sql.filter((sql) => /^\s*UPDATE\b/i.test(sql)).length, 0, '陈旧版本不得产生 DML')
  assert.equal(systemAccountRow(target.id).description, '只改说明', '陈旧 PATCH 不得覆盖已提交内容')

  database.exec('DROP TRIGGER system_account_demand_unrelated_guard')
  assertSourceContracts()
  console.log('系统账户按需写回归通过：列表版本、窄投影、动态 SET、no-op、CAS 与最小回执均已固定')
} finally {
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
  const payload = await response.json() as ApiEnvelope<T>
  assert.equal(response.status, 200, payload.message ?? `GET ${path} 失败`)
  return payload.data
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json', cookie },
    body: JSON.stringify(body)
  })
  const payload = await response.json() as ApiEnvelope<T>
  assert.equal(response.status, 200, payload.message ?? `PATCH ${path} 失败`)
  return payload.data
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
  const originalPrepare = database.prepare.bind(database) as typeof database.prepare
  const sql: string[] = []
  database.prepare = ((statementSql: string) => {
    if (/\bsystem_accounts\b/i.test(statementSql)) sql.push(statementSql)
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

function changedDatabaseColumns(before: SystemAccountDatabaseRow, after: SystemAccountDatabaseRow): string[] {
  return Object.keys(before).filter((key) => before[key as keyof SystemAccountDatabaseRow] !== after[key as keyof SystemAccountDatabaseRow]).sort()
}

function assertSourceContracts(): void {
  const repositorySource = readFileSync(fileURLToPath(new URL('../../storage/system-accounts.repository.ts', import.meta.url)), 'utf8')
  const patchSource = sourceBetween(repositorySource, 'export async function patchSystemAccountManagementAsync(', 'export function updateSystemAccount(')
  assert.match(patchSource, /systemAccountManagementPatchSelectColumns\(input\)/, 'PATCH 必须按提交字段生成投影')
  assert.match(patchSource, /SET \$\{assignments\.join\(', '\)\}, updated_at = \?/, 'PATCH 必须动态生成 SET')
  assert.match(patchSource, /WHERE id = \? AND updated_at = \?/, 'SQLite 与 PostgreSQL 必须共享 CAS 条件')
  assert.match(patchSource, /tx\.driver === 'postgres' \? ' FOR UPDATE'/, 'PostgreSQL PATCH 必须锁定当前系统账户行')
  assert.doesNotMatch(patchSource, /findSystemAccountById|systemAccountSummaryFromRow/, '管理 PATCH 不得物化完整系统账户摘要')

  const routeSource = readFileSync(fileURLToPath(new URL('../../modules/system-accounts/system-accounts.routes.ts', import.meta.url)), 'utf8')
  const routePatch = sourceBetween(routeSource, "systemAccountsRouter.patch('/:id'", 'function systemAccountWhitespaceError(')
  assert.doesNotMatch(routePatch, /findSystemAccountByIdAsync|updateSystemAccountWithPasswordHashAsync/, '路由不得重复宽读或回退整行更新')
  assert.match(routePatch, /outcome\.changes\.length \?/, 'no-op 不得记录操作日志')
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
