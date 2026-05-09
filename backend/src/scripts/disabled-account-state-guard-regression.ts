import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import http from 'node:http'

import cors from 'cors'
import express from 'express'

import { runtimeConfig } from '../config/runtime.js'
import { ok } from '../shared/http.js'
import { logger } from '../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-disabled-account-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'disabled-account-guard.sqlite3')
runtimeConfig.secret = 'disabled-account-guard-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { authRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  { testOpenAIAccount },
  { applyAccountErrorHandling },
  { handleDbServiceOperation }
] = await Promise.all([
  import('../modules/accounts/accounts.routes.js'),
  import('../modules/auth/auth.routes.js'),
  import('../modules/auth/auth.middleware.js'),
  import('../shared/request-context.js'),
  import('../storage/database.js'),
  import('../storage/repositories.js'),
  import('../modules/accounts/account-test.service.js'),
  import('../modules/gateway/account-error-policy.service.js'),
  import('../modules/db-service/db-service-handlers.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/api/auth', authRouter)
app.use('/api/settings/public', (_req, res) => {
  res.json(ok(repositories.listPublicGlobalSettings()))
})
app.use('/api', requireAuth)
app.use('/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/api/accounts', requireAdmin, accountsRouter)

interface ApiEnvelope<T> {
  data: T
}

interface AccountSummary {
  id: string
  name: string
  type: string
  status: string
  schedulable: boolean
}

interface AccountTestResult {
  success: boolean
  message: string
}

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  try {
    appServer = app.listen(0, '127.0.0.1')
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`
    const adminCookie = await login(baseUrl)

    const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: '停用账户状态保护回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-disabled-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true
    }, access)
    const group = repositories.createGroup({
      name: '停用账户状态保护回归分组',
      providerCode: 'openai'
    }, access)
    const bound = repositories.setAccountGroup(account.id, group.id, access)
    assert(bound?.boundGroupId === group.id, '测试账户未绑定分组')

    const staleGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, account.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleGatewayAccount?.status === 'active', '停用前应能读取到完整网关账号对象')
    const disabled = repositories.updateAccount(account.id, { status: 'disabled' }, access)
    assert(disabled?.status === 'disabled' && disabled.schedulable === false, '测试账户停用失败')

    const apiTestResponse = await fetch(`${baseUrl}/api/accounts/${account.id}/test`, {
      method: 'POST',
      headers: { cookie: adminCookie, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-4o-mini', prompt: 'hi' })
    })
    const apiTestText = await apiTestResponse.text()
    assert(apiTestResponse.status === 400, `停用账户测试接口应拒绝，实际 HTTP ${apiTestResponse.status}: ${apiTestText}`)
    assert(apiTestText.includes('账户已停用'), `停用账户测试接口错误信息异常：${apiTestText}`)
    assertAccountStatus(account.id, 'disabled', false, '测试接口不应改变停用状态')

    const latestDisabled = repositories.findAccountForTest(account.id, access)
    assert(latestDisabled, '停用测试账户不存在')
    const serviceTest = await testOpenAIAccount(latestDisabled, { groupId: group.id })
    assert(serviceTest.success === false && serviceTest.message.includes('账户已停用'), `测试服务应拒绝停用账户：${serviceTest.message}`)
    assertAccountStatus(account.id, 'disabled', false, '测试服务不应改变停用状态')

    const cooldownResult = repositories.markAccountCooldown(account.id, new Date(Date.now() + 60_000).toISOString(), '模拟冷却')
    assert(cooldownResult === undefined, '停用账户不应被标记为冷却')
    assertAccountStatus(account.id, 'disabled', false, '冷却写回不应改变停用状态')

    const disabledByFailure = repositories.markAccountDisabledByFailure(account.id, '模拟错误停用')
    assert(disabledByFailure === undefined, '停用账户不应被错误策略改为 error')
    assertAccountStatus(account.id, 'disabled', false, '错误停用写回不应改变停用状态')

    const clearResult = repositories.clearAccountFailureState(account.id, access)
    assert(clearResult?.status === 'disabled', '清理失败态不应恢复停用账户')
    assertAccountStatus(account.id, 'disabled', false, '清理失败态不应改变停用状态')

    const errorHandlingResult = applyAccountErrorHandling({
      id: account.id,
      status: 'disabled',
      credentials: {}
    }, {
      success: false,
      statusCode: 503,
      bodyText: 'upstream failed'
    })
    assert(errorHandlingResult.changed === false && errorHandlingResult.accountStatus === 'disabled', '错误处理不应改变停用账户')
    assertAccountStatus(account.id, 'disabled', false, '错误处理写回不应改变停用状态')

    const streamFailureResult = repositories.recordAccountStreamFailure({
      accountId: account.id,
      thresholdCount: 1,
      thresholdWindowMinutes: 1,
      action: 'cooldown',
      cooldownMinutes: 1,
      reason: '模拟流式异常'
    })
    assert(streamFailureResult.triggered === false, '停用账户不应触发流式熔断状态写回')
    assertAccountStatus(account.id, 'disabled', false, '流式熔断不应改变停用状态')

    const dbServiceResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleGatewayAccount,
      input: {
        success: true,
        bodyText: ''
      }
    })
    assert(dbServiceResult.changed === false, 'DB service 成功回写不应恢复停用账户')
    assertAccountStatus(account.id, 'disabled', false, 'DB service 成功回写不应改变停用状态')

    const staleFailureResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleGatewayAccount,
      input: {
        success: false,
        statusCode: 503,
        bodyText: 'late upstream failure'
      }
    })
    assert(staleFailureResult.changed === false, 'DB service 失败回写不应把已停用账户改成临时不可调用')
    assertAccountStatus(account.id, 'disabled', false, 'DB service 失败回写不应改变停用状态')

    console.log('停用账户状态保护回归通过：测试、恢复、错误处理和熔断写回均不会改变停用状态')
  } finally {
    await closeServer(appServer)
    try {
      databaseModule.getDatabase().close()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function assertAccountStatus(accountId: string, status: string, schedulable: boolean, message: string): void {
  const account = repositories.listAccounts().find((item: AccountSummary) => item.id === accountId)
  assert(account, `${message}：账户不存在`)
  assert(account.status === status, `${message}：实际状态 ${account.status}`)
  assert(account.schedulable === schedulable, `${message}：实际调度标记 ${account.schedulable}`)
}

async function login(baseUrl: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>(baseUrl, '/api/auth/captcha')
  const captchaCode = parseCaptchaCode(captcha.image)
  assert(captchaCode, '无法解析登录验证码')
  const response = await fetch(`${baseUrl}/api/auth/login`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      username: 'admin',
      password: 'admin',
      captchaId: captcha.captchaId,
      captchaCode
    })
  })
  const cookie = response.headers.get('set-cookie')?.split(';')[0]
  assert(response.ok, `登录失败：HTTP ${response.status} ${await response.text()}`)
  assert(cookie, '登录未返回会话 Cookie')
  return cookie
}

async function getEnvelope<T>(baseUrl: string, path: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`)
  const text = await response.text()
  assert(response.ok, `${path} HTTP ${response.status}: ${text}`)
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function parseCaptchaCode(image: string): string {
  const base64 = image.replace(/^data:image\/svg\+xml;base64,/, '')
  const svg = Buffer.from(base64, 'base64').toString('utf8')
  return [...svg.matchAll(/<text[^>]*>([^<]+)<\/text>/g)].map((match) => match[1]).join('')
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
  console.error('\n停用账户状态保护回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
