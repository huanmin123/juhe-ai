import { mkdirSync, rmSync } from 'node:fs'
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

const [
  { authRouter },
  { captchaAnswerForTest },
  { requireAuth },
  { requestContextMiddleware },
  databaseModule
] = await Promise.all([
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/captcha.service.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
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
  username: string
  mustChangePassword: boolean
}

async function main(): Promise<void> {
  let server: http.Server | undefined
  try {
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
    const cookie = await login(baseUrl)

    const currentUser = await getEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/me', cookie)
    assert(currentUser.username === 'admin', '默认管理员登录后应能读取当前用户')
    assert(currentUser.mustChangePassword === true, '默认管理员应保留初始密码修改标记')

    const blocked = await fetch(`${baseUrl}/__aisys__/api/protected`, { headers: { cookie } })
    const blockedText = await blocked.text()
    assert(blocked.status === 403, `初始密码未修改时受保护接口应返回 403，实际 HTTP ${blocked.status}: ${blockedText}`)
    const blockedBody = JSON.parse(blockedText) as ApiEnvelope<unknown>
    assert(blockedBody.code === 'must_change_password', `初始密码拦截 code 异常：${blockedBody.code}`)

    const changedUser = await postEnvelope<CurrentUser>(baseUrl, '/__aisys__/api/auth/change-password', cookie, {
      newPassword: 'changed-password'
    })
    assert(changedUser.mustChangePassword === false, '修改密码后应清除初始密码标记')

    const allowed = await getEnvelope<{ protected: boolean }>(baseUrl, '/__aisys__/api/protected', cookie)
    assert(allowed.protected === true, '修改密码后应允许访问受保护接口')

    const secondCookie = await login(baseUrl, 'changed-password')
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
    assert(finalUser.mustChangePassword === false, '普通改密后应保持非初始密码状态')
    await assertGetStatus(baseUrl, '/__aisys__/api/protected', cookie, 401, '普通改密后应撤销其他旧会话')
    const currentAllowed = await getEnvelope<{ protected: boolean }>(baseUrl, '/__aisys__/api/protected', secondCookie)
    assert(currentAllowed.protected === true, '普通改密后当前会话应继续可用')

    console.log('初始密码与普通改密边界回归通过：初始改密放行，普通改密校验旧密码并撤销其他会话')
  } finally {
    await closeServer(server)
    try {
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function login(baseUrl: string, password = 'admin'): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = captchaAnswerForTest(captcha.captchaId)
  assert(captchaCode, '测试夹具无法读取登录验证码')
  assertCaptchaImageDoesNotExposeAnswer(captcha.image, captchaCode)
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username: 'admin',
      password,
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(response.ok, `登录失败：HTTP ${response.status} ${await response.text()}`)
  assert(cookie, '登录未返回会话 Cookie')
  return cookie
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
