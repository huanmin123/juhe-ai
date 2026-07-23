import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import cors from 'cors'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ok } from '../../shared/http.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-auth-must-change-password-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'auth-must-change-password.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'auth-must-change-password-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const authRoutesSource = readFileSync(resolve('src/modules/auth/auth.routes.ts'), 'utf8')
assert(authRoutesSource.includes('findSystemAccountByIdAsync'), '认证 /me 更新必须使用 async 系统账户读取')
assert(authRoutesSource.includes('updateSystemAccountAsync'), '认证 /me 更新和改密必须使用 async 系统账户写入')
assert(authRoutesSource.includes('revokeOtherSessionsForAccountAsync'), '改密后撤销其他会话必须使用 async 会话写入')
assert(authRoutesSource.includes('recordOperationLogAsync'), '认证 /me 更新操作日志必须使用 async 写入')
assert(!/import \{[^}]*\bfindSystemAccountById\b[^}]*\} from '..\/..\/storage\/repositories\.js'/.test(authRoutesSource), '认证路由不能重新导入同步 findSystemAccountById')
assert(!/import \{[^}]*\brevokeOtherSessionsForAccount\b[^}]*\} from '..\/..\/storage\/repositories\.js'/.test(authRoutesSource), '认证路由不能重新导入同步 revokeOtherSessionsForAccount')

const [
  { authRouter },
  { captchaAnswerForTest },
  { requireAuth },
  { requestContextMiddleware },
  repositories,
  databaseModule
] = await Promise.all([
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/captcha.service.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/database.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)
app.use('/__aisys__/api', requireAuth)
app.get('/__aisys__/api/protected', (_req, res) => {
  res.json(ok({ protected: true }))
})

interface ApiEnvelope<T> {
  data: T
  message?: string
  code?: string
}

interface CurrentUser {
  id: string
  username: string
  displayName: string
  role: 'admin' | 'user'
  mustChangePassword: boolean
}

async function main(): Promise<void> {
  let server: http.Server | undefined
  try {
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
    databaseModule.getBusinessDatabase()
      .prepare("UPDATE system_accounts SET must_change_password = 1 WHERE id = 'sys_admin'")
      .run()

    const adminCookie = await login(baseUrl)
    const currentAdmin = await getEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', adminCookie)
    assertCurrentUserProjection(currentAdmin, 'GET /auth/me')
    assert(currentAdmin.username === 'admin', '默认管理员登录后应能读取当前用户')
    assert(currentAdmin.mustChangePassword === false, '管理员账户不应触发初始密码强制修改')

    const adminAllowed = await getEnvelope<{ protected: boolean }>(baseUrl, '/__aisys__/api/protected', adminCookie)
    assert(adminAllowed.protected === true, '管理员账户即使存在旧改密标记也应允许访问受保护接口')
    await assertJsonStatus(baseUrl, '/__aisys__/api/auth/me', adminCookie, 'PATCH', {
      displayName: ''
    }, 400, '管理员修改显示名称仍应校验显示名称不能为空')
    const renamedAdmin = await patchEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', adminCookie, {
      displayName: '控制台管理员'
    })
    assertCurrentUserProjection(renamedAdmin, 'PATCH /auth/me')
    assert(renamedAdmin.displayName === '控制台管理员', `管理员修改显示名称后响应应返回新名称，实际：${renamedAdmin.displayName}`)

    const createdUser = repositories.createSystemAccount({
      username: 'locked_user',
      displayName: '待改密用户',
      password: 'user-password',
      role: 'user',
      mustChangePassword: true
    })
    assert(createdUser.mustChangePassword === true, '普通用户仍应保留初始密码修改标记')
    const cookie = await login(baseUrl, 'locked_user', 'user-password')
    const currentUser = await getEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', cookie)
    assertCurrentUserProjection(currentUser, '普通用户 GET /auth/me')
    assert(currentUser.username === 'locked_user', '普通用户登录后应能读取当前用户')
    assert(currentUser.mustChangePassword === true, '普通用户应保留初始密码修改标记')

    const blocked = await fetch(`${baseUrl}/__aisys__/api/protected`, { headers: { cookie } })
    const blockedText = await blocked.text()
    assert(blocked.status === 403, `普通用户初始密码未修改时受保护接口应返回 403，实际 HTTP ${blocked.status}: ${blockedText}`)
    const blockedBody = JSON.parse(blockedText) as ApiEnvelope<unknown>
    assert(blockedBody.code === 'must_change_password', `初始密码拦截 code 异常：${blockedBody.code}`)
    await assertJsonStatus(baseUrl, '/__aisys__/api/auth/me', cookie, 'PATCH', {
      displayName: '未改密用户'
    }, 403, '普通用户初始密码未修改时不能修改显示名称')

    const changedUser = await postEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/change-password', cookie, {
      newPassword: 'changed-password'
    })
    assertCurrentUserProjection(changedUser, '初始密码 POST /auth/change-password')
    assert(changedUser.mustChangePassword === false, '修改密码后应清除初始密码标记')

    const allowed = await getEnvelope<{ protected: boolean }>(baseUrl, '/__aisys__/api/protected', cookie)
    assert(allowed.protected === true, '修改密码后应允许访问受保护接口')
    await assertJsonStatus(baseUrl, '/__aisys__/api/auth/me', cookie, 'PATCH', {
      displayName: ''
    }, 400, '显示名称不能为空')
    const renamedUser = await patchEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', cookie, {
      displayName: '普通控制台用户'
    })
    assertCurrentUserProjection(renamedUser, '普通用户 PATCH /auth/me')
    assert(renamedUser.displayName === '普通控制台用户', `修改显示名称后响应应返回新名称，实际：${renamedUser.displayName}`)
    const renamedCurrentUser = await getEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', cookie)
    assertCurrentUserProjection(renamedCurrentUser, '改名后 GET /auth/me')
    assert(renamedCurrentUser.displayName === '普通控制台用户', '修改显示名称后当前会话应读取到新名称')

    const secondCookie = await login(baseUrl, 'locked_user', 'changed-password')
    await assertPostStatus(baseUrl, '/__aisys__/api/auth/change-password', secondCookie, {
      newPassword: 'missing-old-password'
    }, 400, '普通改密必须填写当前密码')
    await assertPostStatus(baseUrl, '/__aisys__/api/auth/change-password', secondCookie, {
      oldPassword: 'wrong-password',
      newPassword: 'wrong-old-password'
    }, 400, '普通改密必须校验当前密码')

    const finalUser = await postEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/change-password', secondCookie, {
      oldPassword: 'changed-password',
      newPassword: 'changed-password-again'
    })
    assertCurrentUserProjection(finalUser, '普通改密 POST /auth/change-password')
    assert(finalUser.mustChangePassword === false, '普通改密后应保持非初始密码状态')
    await assertGetStatus(baseUrl, '/__aisys__/api/protected', cookie, 401, '普通改密后应撤销其他旧会话')
    const currentAllowed = await getEnvelope<{ protected: boolean }>(baseUrl, '/__aisys__/api/protected', secondCookie)
    assert(currentAllowed.protected === true, '普通改密后当前会话应继续可用')

    console.log('初始密码、管理员免强制改密、普通改密与显示名称修改边界回归通过：管理员直接进入控制台，普通用户初始改密放行，普通改密校验旧密码')
  } finally {
    await closeServer(server)
    try {
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function login(baseUrl: string, username = 'admin', password = 'admin'): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert(captchaCode, '测试夹具无法读取登录验证码')
  assertCaptchaImageDoesNotExposeAnswer(captcha.image, captchaCode)
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username,
      password,
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const responseText = await response.text()
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(response.ok, `登录失败：HTTP ${response.status} ${responseText}`)
  assert(cookie, '登录未返回会话 Cookie')
  const loginData = (JSON.parse(responseText) as { data?: CurrentUser }).data
  assert(loginData, '登录响应缺少 CurrentUserSummary')
  assertCurrentUserProjection(loginData, 'POST /auth/login')
  return cookie
}

function assertCurrentUserProjection(value: CurrentUser, label: string): void {
  const actualKeys = JSON.stringify(Object.keys(value).sort())
  const expectedKeys = JSON.stringify(['displayName', 'id', 'mustChangePassword', 'role', 'username'].sort())
  assert(actualKeys === expectedKeys, `${label} 响应必须使用 CurrentUserSummary 窄投影，实际字段：${actualKeys}`)
}

function assertCaptchaImageDoesNotExposeAnswer(image: string, answer: string): void {
  assert(image.startsWith('data:image/png;base64,'), `验证码应返回 PNG 图片，实际：${image.slice(0, 32)}`)
  const bytes = Buffer.from(image.replace(/^data:image\/png;base64,/, ''), 'base64')
  assert(bytes.subarray(0, 8).equals(Buffer.from([137, 80, 78, 71, 13, 10, 26, 10])), '验证码 PNG 文件头异常')
  const decodedText = bytes.toString('utf8')
  assert(!decodedText.includes('<text') && !decodedText.includes('</svg'), '验证码图片不应包含可解析 SVG 文本')
  assert(!decodedText.includes(answer), '验证码图片不应明文包含答案')
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie?: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, cookie ? { headers: { cookie } } : undefined)
  return unwrapEnvelope<T>(response, path)
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return unwrapEnvelope<T>(response, path)
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'PATCH',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  return unwrapEnvelope<T>(response, path)
}

async function assertJsonStatus(baseUrl: string, path: string, cookie: string, method: 'PATCH' | 'POST', body: unknown, expectedStatus: number, message: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method,
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert(response.status === expectedStatus, `${message}，实际 HTTP ${response.status}: ${text}`)
}

async function assertPostStatus(baseUrl: string, path: string, cookie: string, body: unknown, expectedStatus: number, message: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
  const text = await response.text()
  assert(response.status === expectedStatus, `${message}，实际 HTTP ${response.status}: ${text}`)
}

async function assertGetStatus(baseUrl: string, path: string, cookie: string, expectedStatus: number, message: string): Promise<void> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const text = await response.text()
  assert(response.status === expectedStatus, `${message}，实际 HTTP ${response.status}: ${text}`)
}

async function unwrapEnvelope<T>(response: Response, path: string): Promise<T> {
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) {
    throw new Error(message)
  }
}

main().catch((error) => {
  console.error('\n初始密码强制修改边界回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
