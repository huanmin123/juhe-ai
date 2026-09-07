import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-captcha-rate-limit-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'captcha-rate-limit.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'captcha-rate-limit-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { authRouter },
  { captchaAnswerForTest },
  { requestContextMiddleware },
  databaseModule
] = await Promise.all([
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/captcha.service.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js')
])

const app = express()
app.set('trust proxy', true)
app.use(requestContextMiddleware)
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface CaptchaPayload {
  captchaId: string
  image: string
}

async function main(): Promise<void> {
  let server: http.Server | undefined
  try {
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`
    const floodIp = '198.51.100.77'

    const firstCaptcha = await getCaptcha(baseUrl, floodIp)
    const firstAnswer = captchaAnswerForTest(firstCaptcha.captchaId)
    assert(firstAnswer, '测试夹具无法读取首个验证码')

    const blocked = await requestCaptchaUntilBlocked(baseUrl, floodIp)
    assert(blocked.status === 429, `验证码高频请求应返回 429，实际 HTTP ${blocked.status}`)
    assert(blocked.headers.get('retry-after'), '验证码高频请求应返回 Retry-After')
    assert.equal(
      captchaAnswerForTest(firstCaptcha.captchaId),
      firstAnswer,
      '同一 IP 高频刷新验证码被限流后，不应继续生成新 challenge 并淘汰已发验证码'
    )

    const loginResponse = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        'x-forwarded-for': floodIp
      },
      body: JSON.stringify({
        username: 'admin',
        password: 'admin',
        captchaId: firstCaptcha.captchaId,
        captchaCode: firstAnswer
      })
    })
    assert(loginResponse.ok, `验证码限流后使用已发验证码登录应仍可通过，实际 HTTP ${loginResponse.status}: ${await loginResponse.text()}`)
    assert(loginResponse.headers.get('set-cookie'), '验证码限流后成功登录仍应返回会话 Cookie')

    const otherIpCaptcha = await getCaptcha(baseUrl, '198.51.100.88')
    assert(otherIpCaptcha.captchaId, '其他 IP 不应受当前 IP 验证码限流影响')

    console.log('验证码限流回归通过：公开验证码接口按 IP 限制生成频率，超限前已发验证码仍可提交')
  } finally {
    await closeServer(server)
    try {
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function requestCaptchaUntilBlocked(baseUrl: string, clientIp: string): Promise<Response> {
  for (let index = 0; index < 80; index += 1) {
    const response = await fetch(`${baseUrl}/__aisys__/api/auth/captcha`, {
      headers: { 'x-forwarded-for': clientIp }
    })
    if (response.status === 429) {
      return response
    }
    const text = await response.text()
    assert(response.ok, `验证码请求在触发限流前失败：HTTP ${response.status}: ${text}`)
  }
  throw new Error('验证码高频请求未触发限流')
}

async function getCaptcha(baseUrl: string, clientIp: string): Promise<CaptchaPayload> {
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/captcha`, {
    headers: { 'x-forwarded-for': clientIp }
  })
  const text = await response.text()
  assert(response.ok, `获取验证码失败：HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as ApiEnvelope<CaptchaPayload>
  assert(body.data.image.startsWith('data:image/png;base64,'), '验证码应返回 PNG data URL')
  return body.data
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

main().catch((error) => {
  console.error('\n验证码限流回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
