import { strict as assert } from 'node:assert'
import { spawnSync } from 'node:child_process'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const strongSecret = 'auth-captcha-production-guard-secret-32'
const productionResult = spawnSync(process.execPath, ['--import', 'tsx', '-e', "import('./src/config/runtime.ts').then(({ runtimeConfig }) => console.log(JSON.stringify(runtimeConfig.auth)))"], {
  cwd: process.cwd(),
  env: {
    ...process.env,
    NODE_ENV: 'production',
    JUHE_AI_SECRET: strongSecret,
    JUHE_AI_ALLOWED_ORIGINS: 'https://admin.example.com',
    JUHE_AI_AUTH_CAPTCHA_DISABLED: 'true'
  },
  encoding: 'utf8'
})
assert.equal(productionResult.status, 0, '生产排障时单开关应允许临时关闭验证码')
assert.match(productionResult.stdout, /"captchaDisabled":true/, '生产单开关应进入验证码关闭模式')

const tempRoot = resolve(tmpdir(), `juhe-ai-auth-captcha-disabled-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'auth-captcha-disabled.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'auth-captcha-disabled-test-secret'
runtimeConfig.auth.captchaDisabled = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [{ authRouter }, { requestContextMiddleware }, databaseModule] = await Promise.all([
  import('../../modules/auth/auth.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)

async function main(): Promise<void> {
  let server: http.Server | undefined
  try {
    server = app.listen(0, '127.0.0.1')
    await listen(server)
    const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

    const captchaResponse = await fetch(`${baseUrl}/__aisys__/api/auth/captcha`)
    assert.equal(captchaResponse.status, 200, '禁用验证码后 captcha 能力查询仍应成功')
    const captchaBody = await captchaResponse.json() as { data: { required: boolean } }
    assert.equal(captchaBody.data.required, false, '禁用验证码后前端必须能发现当前不需要验证码')

    const loginResponse = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ username: 'admin', password: 'admin' })
    })
    const loginText = await loginResponse.text()
    assert(loginResponse.ok, `禁用验证码后账号密码正确应直接登录，实际 HTTP ${loginResponse.status}: ${loginText}`)
    assert(loginResponse.headers.get('set-cookie'), '禁用验证码后成功登录仍应创建正式会话 Cookie')

    const invalidPasswordResponse = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify({ username: 'admin', password: 'wrong-password' })
    })
    assert.equal(invalidPasswordResponse.status, 401, '禁用验证码不能绕过账号密码校验')

    console.log('验证码诊断开关回归通过：各环境可显式关闭验证码，密码校验和正式会话保持不变')
  } finally {
    await closeServer(server)
    try {
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
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
  if (!address || typeof address === 'string') throw new Error('服务地址不可用')
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

main().catch((error) => {
  console.error('\n开发/测试验证码开关回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
