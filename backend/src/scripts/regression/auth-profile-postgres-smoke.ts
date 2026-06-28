import { strict as assert } from 'node:assert'
import http from 'node:http'

import { runtimeConfig } from '../../config/runtime.js'
import { captchaAnswerForTest } from '../../modules/auth/captcha.service.js'
import { createSystemApiApp } from '../../modules/system-api/system-api-app.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '认证资料更新 / 改密 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

interface ApiEnvelope<T> {
  data: T
  message?: string
  code?: string
}

interface CurrentUser {
  id: string
  username: string
  displayName: string
  role: string
  mustChangePassword: boolean
}

interface SystemAccountResponse extends CurrentUser {
  status: string
}

const marker = `auth_profile_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const username = `authpg_${Date.now()}_${Math.random().toString(16).slice(2, 8)}`
const initialPassword = `Pwd_${marker}`
const changedPassword = `Changed_${marker}`
const finalPassword = `Final_${marker}`
const createdSystemAccountIds: string[] = []
let server: http.Server | undefined

try {
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api', trustProxy: true })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
  const adminCookie = await login(baseUrl, 'admin', 'admin')

  const createdUser = await postEnvelope<SystemAccountResponse>(baseUrl, '/__aisys__/api/system-accounts', {
    username,
    displayName: `认证PG烟测用户${marker}`,
    password: initialPassword,
    role: 'user',
    status: 'active',
    mustChangePassword: true
  }, adminCookie, 201)
  createdSystemAccountIds.push(createdUser.id)
  assert.equal(createdUser.mustChangePassword, true, '临时用户应保留初始改密标记')

  const firstCookie = await login(baseUrl, username, initialPassword)
  const mustChangeUser = await getEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', firstCookie)
  assert.equal(mustChangeUser.id, createdUser.id, '临时用户登录后应读取当前用户')
  assert.equal(mustChangeUser.mustChangePassword, true, '临时用户首次登录应要求改密')
  await assertJsonStatus(baseUrl, '/__aisys__/api/auth/me', firstCookie, 'PATCH', {
    displayName: `认证PG未改密${marker}`
  }, 403, '初始密码未修改前不能修改显示名称')

  const changedUser = await postEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/change-password', {
    newPassword: changedPassword
  }, firstCookie)
  assert.equal(changedUser.mustChangePassword, false, '强制改密后应清除 mustChangePassword')

  const renamedUser = await patchEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', {
    displayName: `认证PG已改名${marker}`
  }, firstCookie)
  assert.equal(renamedUser.displayName, `认证PG已改名${marker}`, '改密后应允许修改显示名称')

  const secondCookie = await login(baseUrl, username, changedPassword)
  await assertJsonStatus(baseUrl, '/__aisys__/api/auth/change-password', secondCookie, 'POST', {
    newPassword: finalPassword
  }, 400, '普通改密必须填写当前密码')
  await assertJsonStatus(baseUrl, '/__aisys__/api/auth/change-password', secondCookie, 'POST', {
    oldPassword: 'wrong-password',
    newPassword: finalPassword
  }, 400, '普通改密必须校验当前密码')

  const finalUser = await postEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/change-password', {
    oldPassword: changedPassword,
    newPassword: finalPassword
  }, secondCookie)
  assert.equal(finalUser.mustChangePassword, false, '普通改密后应保持非初始密码状态')
  await assertGetStatus(baseUrl, '/__aisys__/api/auth/me', firstCookie, 401, '普通改密后应撤销其他旧会话')
  const currentUser = await getEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', secondCookie)
  assert.equal(currentUser.displayName, `认证PG已改名${marker}`, '当前会话应保留修改后的显示名称')

  const stored = await readStoredUser(createdUser.id)
  assert.equal(stored?.display_name, `认证PG已改名${marker}`, 'PG 应保存修改后的显示名称')
  assert.equal(Number(stored?.must_change_password), 0, 'PG 应保存 must_change_password=false')
  assert(stored?.password_hash && String(stored.password_hash) !== initialPassword, 'PG 不应保存明文密码')

  console.log(JSON.stringify({
    message: '认证资料更新与改密 HTTP PG smoke 通过',
    systemAccountId: createdUser.id,
    username,
    displayName: stored?.display_name
  }))
} finally {
  if (server) {
    await closeServer(server).catch(() => undefined)
  }
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function login(baseUrl: string, username: string, password: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert(captchaCode, '测试夹具应能读取验证码答案')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    signal: AbortSignal.timeout(10_000),
    body: JSON.stringify({
      username,
      password,
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `登录应成功，实际 HTTP ${response.status}: ${text}`)
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(cookie, '登录应返回 session cookie')
  return cookie
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: cookie ? { cookie } : undefined,
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GET ${path} 应返回 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function postEnvelope<T>(
  baseUrl: string,
  path: string,
  body: unknown,
  cookie: string,
  expectedStatus = 200
): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      cookie
    },
    signal: AbortSignal.timeout(15_000),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `POST ${path} 应返回 ${expectedStatus}，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function patchEnvelope<T>(baseUrl: string, path: string, body: unknown, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: {
      'content-type': 'application/json',
      cookie
    },
    signal: AbortSignal.timeout(15_000),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, 200, `PATCH ${path} 应返回 200，实际 HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

async function assertJsonStatus(
  baseUrl: string,
  path: string,
  cookie: string,
  method: 'PATCH' | 'POST',
  body: unknown,
  expectedStatus: number,
  message: string
): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: {
      'content-type': 'application/json',
      cookie
    },
    signal: AbortSignal.timeout(15_000),
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `${message}，实际 HTTP ${response.status}: ${text}`)
}

async function assertGetStatus(baseUrl: string, path: string, cookie: string, expectedStatus: number, message: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    headers: { cookie },
    signal: AbortSignal.timeout(10_000)
  })
  const text = await response.text()
  assert.equal(response.status, expectedStatus, `${message}，实际 HTTP ${response.status}: ${text}`)
}

async function readStoredUser(systemAccountId: string): Promise<Record<string, unknown> | undefined> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT id, username, display_name, must_change_password, password_hash
    FROM juhe_business.system_accounts
    WHERE id = $1
    LIMIT 1
  `, [systemAccountId])
  return result.rows[0] as Record<string, unknown> | undefined
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  const lookup = await pool.query(`
    SELECT id
    FROM juhe_business.system_accounts
    WHERE id = ANY($1::text[])
       OR username = $2
  `, [[...new Set(createdSystemAccountIds)], username])
  const systemAccountIds = lookup.rows
    .map((row) => row.id)
    .filter((id): id is string => typeof id === 'string')
  if (systemAccountIds.length === 0) {
    return
  }
  await cleanupOperationLogs(systemAccountIds)
  await pool.query('DELETE FROM juhe_business.api_keys WHERE system_account_id = ANY($1::text[]) OR route_strategy_id IN (SELECT id FROM juhe_business.route_strategies WHERE system_account_id = ANY($1::text[]))', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.route_strategy_groups WHERE system_account_id = ANY($1::text[]) OR route_strategy_id IN (SELECT id FROM juhe_business.route_strategies WHERE system_account_id = ANY($1::text[]))', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.route_strategies WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.group_accounts WHERE group_id IN (SELECT id FROM juhe_business.groups WHERE system_account_id = ANY($1::text[]))', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.resource_authorization_sources WHERE authorization_id IN (SELECT id FROM juhe_business.resource_authorizations WHERE grantee_system_account_id = ANY($1::text[]) OR resource_owner_system_account_id = ANY($1::text[]))', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.resource_authorization_grants WHERE grantee_system_account_id = ANY($1::text[]) OR resource_owner_system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.resource_authorizations WHERE grantee_system_account_id = ANY($1::text[]) OR resource_owner_system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.system_team_members WHERE system_account_id = ANY($1::text[])', [systemAccountIds]).catch(() => undefined)
  await pool.query('DELETE FROM juhe_business.groups WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.system_sessions WHERE system_account_id = ANY($1::text[])', [systemAccountIds])
  await pool.query('DELETE FROM juhe_business.system_accounts WHERE id = ANY($1::text[])', [systemAccountIds])
}

async function cleanupOperationLogs(systemAccountIds: string[]): Promise<void> {
  const pool = await getPostgresPool()
  const operationLogIds = await pool.query(`
    SELECT id
    FROM juhe_dataset.operation_logs
    WHERE actor_system_account_id = ANY($1::text[])
       OR operation_scope_system_account_id = ANY($1::text[])
       OR resource_id = ANY($1::text[])
  `, [systemAccountIds]).catch(() => ({ rows: [] }))
  const ids = operationLogIds.rows
    .map((row: Record<string, unknown>) => row.id)
    .filter((id: unknown): id is string => typeof id === 'string')
  if (ids.length === 0) {
    return
  }
  await pool.query('DELETE FROM juhe_dataset.operation_log_targets WHERE operation_log_id = ANY($1::text[])', [ids]).catch(() => undefined)
  await pool.query('DELETE FROM juhe_dataset.operation_log_viewers WHERE operation_log_id = ANY($1::text[])', [ids]).catch(() => undefined)
  await pool.query('DELETE FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id = ANY($1::text[])', [ids]).catch(() => undefined)
  await pool.query('DELETE FROM juhe_dataset.operation_logs WHERE id = ANY($1::text[])', [ids]).catch(() => undefined)
}

function listen(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.once('error', reject)
    server.once('listening', () => resolve())
  })
}

function closeServer(server: http.Server): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) reject(error)
      else resolve()
    })
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(address && typeof address === 'object', '测试 HTTP 服务应监听到本地端口')
  return address
}
