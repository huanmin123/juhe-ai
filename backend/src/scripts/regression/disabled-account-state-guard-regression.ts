import { mkdirSync, rmSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { tmpdir } from 'node:os'
import http from 'node:http'

import cors from 'cors'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { ok } from '../../shared/http.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-disabled-account-guard-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'disabled-account-guard.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
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
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-test.service.js'),
  import('../../modules/gateway/account-error-policy.service.js'),
  import('../../modules/db-service/db-service-handlers.js')
])

const app = express()
app.use(requestContextMiddleware)
app.use(cors({ credentials: true, origin: true }))
app.use(express.json({ limit: '2mb' }))
app.use('/__aisys__/api/auth', authRouter)
app.use('/__aisys__/api/settings/public', (_req, res) => {
  res.json(ok(repositories.listPublicGlobalSettings()))
})
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface ApiEnvelope<T> {
  data: T
}

interface AccountSummary {
  id: string
  name: string
  type: string
  status: string
  schedulable: boolean
  lastErrorCode?: string
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
    repositories.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0
    })
    const account = repositories.createAccount({
      providerCode: 'openai',
      name: '停用账户状态保护回归',
      type: 'api_key',
      credentials: {
        api_key: 'sk-disabled-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true,
      superPriorityEnabled: true
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
    assertAccountDispatchFlags(account.id, true, false, '停用账户应保留用户设置的超级优先')

    const apiTestResponse = await fetch(`${baseUrl}/__aisys__/api/accounts/${account.id}/test`, {
      method: 'POST',
      headers: { cookie: adminCookie, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-4o-mini', prompt: 'hi' })
    })
    const apiTestText = await apiTestResponse.text()
    assert(apiTestResponse.ok, `停用账户测试接口应允许诊断，实际 HTTP ${apiTestResponse.status}: ${apiTestText}`)
    const apiTestResult = (JSON.parse(apiTestText) as ApiEnvelope<AccountTestResult>).data
    assert(apiTestResult.success === false, '停用账户测试接口应返回测试失败结果')
    assert(!apiTestResult.message.includes('账户已停用'), `停用账户测试不应被停用状态短路：${apiTestResult.message}`)
    assertAccountStatus(account.id, 'disabled', false, '测试接口不应改变停用状态')
    assertAccountDispatchFlags(account.id, true, false, '测试接口不应清理停用账户调度标记')

    const latestDisabled = repositories.findAccountForTest(account.id, access)
    assert(latestDisabled, '停用测试账户不存在')
    const serviceTest = await testOpenAIAccount(latestDisabled, { groupId: group.id })
    assert(serviceTest.success === false, '测试服务应允许停用账户进入真实测试并返回失败结果')
    assert(!serviceTest.message.includes('账户已停用'), `测试服务不应被停用状态短路：${serviceTest.message}`)
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
    assertAccountDispatchFlags(account.id, true, false, '清理失败态不应清理停用账户调度标记')
    const disabledFlagCleared = repositories.updateAccount(account.id, { superPriorityEnabled: false }, access)
    assert(disabledFlagCleared?.superPriorityEnabled === false, '停用账户应允许用户手动取消超级优先')

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

    const errorAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '异常账户测试成功不自动恢复',
      type: 'api_key',
      credentials: {
        api_key: 'sk-error-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true,
      fallbackEnabled: true
    }, access)
    const errorBound = repositories.setAccountGroup(errorAccount.id, group.id, access)
    assert(errorBound?.boundGroupId === group.id, '异常测试账户未绑定分组')
    const staleActiveErrorGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, errorAccount.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleActiveErrorGatewayAccount?.status === 'active', '异常前应能读取到完整网关账号对象')
    const markedError = repositories.markAccountException(errorAccount.id, 'oauth_token_refresh_failed', '模拟异常')
    assert(markedError?.status === 'error' && markedError.schedulable === false, '异常测试账户标记失败')
    assertAccountDispatchFlags(errorAccount.id, false, true, '异常账户应保留用户设置的降级备用')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '异常账户应保留初始异常类型')
    const staleErrorGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, errorAccount.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleErrorGatewayAccount?.status === 'error', '异常账户应能被测试链路按指定账号读取')
    const successOnErrorResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleErrorGatewayAccount,
      input: {
        success: true,
        bodyText: ''
      }
    })
    assert(successOnErrorResult.changed === false, '异常账户测试成功不应自动恢复，请使用恢复异常')
    assertAccountStatus(errorAccount.id, 'error', false, '成功回写不应自动恢复异常账户')
    assertAccountDispatchFlags(errorAccount.id, false, true, '成功回写不应清理异常账户调度标记')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '成功回写不应清理异常类型')

    const errorRaceAccount = repositories.createAccount({
      providerCode: 'openai',
      name: '异常竞态成功回写不自动恢复',
      type: 'api_key',
      credentials: {
        api_key: 'sk-error-race-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'active',
      schedulable: true
    }, access)
    const errorRaceBound = repositories.setAccountGroup(errorRaceAccount.id, group.id, access)
    assert(errorRaceBound?.boundGroupId === group.id, '异常竞态测试账户未绑定分组')
    repositories.markAccountCooldown(errorRaceAccount.id, new Date(Date.now() - 1000).toISOString(), '模拟过期冷却状态')
    const staleCooldownGatewayAccount = repositories.findOpenAIAccountForGroup(group.id, errorRaceAccount.id, 'sys_admin', { ignoreAvailability: true })
    assert(staleCooldownGatewayAccount?.status === 'temporary_unavailable', '异常竞态前应能读取到过期冷却网关账号对象')
    repositories.markAccountException(errorRaceAccount.id, 'oauth_token_refresh_failed', '模拟复测期间进入异常')
    const staleSuccessAfterErrorResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleCooldownGatewayAccount,
      input: {
        success: true,
        bodyText: ''
      }
    })
    assert(staleSuccessAfterErrorResult.changed === false, '过期成功回写不应把复测期间进入异常的账户恢复正常')
    assertAccountStatus(errorRaceAccount.id, 'error', false, '过期成功回写不应改变复测期间进入异常的账户状态')
    assertAccountErrorCode(errorRaceAccount.id, 'oauth_token_refresh_failed', '过期成功回写不应清理复测期间进入异常的账户异常类型')

    const staleFailureOnErrorResult = await handleDbServiceOperation({
      type: 'apply_account_error_handling',
      account: staleActiveErrorGatewayAccount,
      input: {
        success: false,
        statusCode: 503,
        bodyText: 'late upstream failure'
      }
    })
    assert(staleFailureOnErrorResult.changed === false, '过期网关失败回写不应把异常账户降级成临时不可调用')
    assertAccountStatus(errorAccount.id, 'error', false, '过期失败回写不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '过期失败回写不应覆盖异常类型')

    const errorCooldownResult = repositories.markAccountCooldown(errorAccount.id, new Date(Date.now() + 60_000).toISOString(), '异常后模拟冷却')
    assert(errorCooldownResult === undefined, '异常账户不应被标记为冷却')
    assertAccountStatus(errorAccount.id, 'error', false, '冷却写回不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '冷却写回不应覆盖异常类型')

    const errorDisabledByFailure = repositories.markAccountDisabledByFailure(errorAccount.id, '异常后模拟错误覆盖')
    assert(errorDisabledByFailure === undefined, '异常账户不应被错误策略覆盖异常类型')
    assertAccountStatus(errorAccount.id, 'error', false, '错误策略不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '错误策略不应覆盖异常类型')

    const errorStreamFailureResult = repositories.recordAccountStreamFailure({
      accountId: errorAccount.id,
      thresholdCount: 1,
      thresholdWindowMinutes: 1,
      action: 'disable',
      cooldownMinutes: 1,
      reason: '异常后模拟流式异常'
    })
    assert(errorStreamFailureResult.triggered === false, '异常账户不应触发流式熔断状态写回')
    assertAccountStatus(errorAccount.id, 'error', false, '流式熔断不应改变异常账户状态')
    assertAccountErrorCode(errorAccount.id, 'oauth_token_refresh_failed', '流式熔断不应覆盖异常类型')

    const editedError = repositories.updateAccount(errorAccount.id, { name: '异常账户编辑后仍不可调度', status: 'error', schedulable: true }, access)
    assert(editedError?.status === 'error' && editedError.schedulable === false, '编辑异常账户不应把调度标记打开')
    assert(editedError?.fallbackEnabled === true, '编辑异常账户不应清理降级备用')
    const errorFlagCleared = repositories.updateAccount(errorAccount.id, { fallbackEnabled: false }, access)
    assert(errorFlagCleared?.fallbackEnabled === false, '异常账户应允许用户手动取消降级备用')
    let statusChangeBlocked = false
    try {
      repositories.updateAccount(errorAccount.id, { status: 'temporary_unavailable' }, access)
    } catch (error) {
      statusChangeBlocked = error instanceof Error && error.message.includes('异常账户不能通过编辑切换状态')
    }
    assert(statusChangeBlocked, '编辑异常账户不应绕过恢复异常切换到其他状态')

    const createdError = repositories.createAccount({
      providerCode: 'openai',
      name: '创建时异常账户不可调度',
      type: 'api_key',
      credentials: {
        api_key: 'sk-created-error-account-guard',
        base_url: 'http://127.0.0.1:9/v1'
      },
      status: 'error',
      schedulable: true
    }, access)
    assert(createdError.status === 'error' && createdError.schedulable === false, '创建异常账户时应强制不可调度')

    console.log('停用/异常账户状态保护回归通过：测试、恢复、错误处理和熔断写回均不会改变硬状态')
  } finally {
    await closeServer(appServer)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
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

function assertAccountDispatchFlags(accountId: string, superPriorityEnabled: boolean, fallbackEnabled: boolean, message: string): void {
  const account = repositories.listAccounts().find((item: AccountSummary) => item.id === accountId)
  assert(account, `${message}：账户不存在`)
  assert(account.superPriorityEnabled === superPriorityEnabled, `${message}：实际超级优先 ${account.superPriorityEnabled}`)
  assert(account.fallbackEnabled === fallbackEnabled, `${message}：实际降级备用 ${account.fallbackEnabled}`)
}

function assertAccountErrorCode(accountId: string, code: string, message: string): void {
  const account = repositories.listAccounts().find((item: AccountSummary) => item.id === accountId)
  assert(account, `${message}：账户不存在`)
  assert(account.lastErrorCode === code, `${message}：实际异常类型 ${account.lastErrorCode}`)
}

async function login(baseUrl: string): Promise<string> {
  const captcha = await getEnvelope<{ captchaId: string; image: string }>(baseUrl, '/__aisys__/api/auth/captcha')
  const captchaCode = parseCaptchaCode(captcha.image)
  assert(captchaCode, '无法解析登录验证码')
  const response = await fetch(`${baseUrl}/__aisys__/api/auth/login`, {
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
