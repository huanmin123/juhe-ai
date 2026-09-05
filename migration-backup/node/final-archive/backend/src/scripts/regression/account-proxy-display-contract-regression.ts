import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-proxy-display-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-proxy-display-regression-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { accountsRouter },
  { forceSelfAccessScope, requireAdmin, requireAuth },
  { requestContextMiddleware },
  repositoriesModule,
  databaseModule
] = await Promise.all([
  import('../../modules/accounts/accounts.routes.js'),
  import('../../modules/auth/auth.middleware.js'),
  import('../../shared/request-context.js'),
  import('../../storage/repositories.js'),
  import('../../storage/database.js')
])

const repositories = repositoriesModule
const app = express()
app.use(requestContextMiddleware)
app.use(express.json({ limit: '1mb' }))
app.use('/__aisys__/api', requireAuth)
app.use('/__aisys__/api/my-accounts', forceSelfAccessScope, accountsRouter)
app.use('/__aisys__/api/accounts', requireAdmin, accountsRouter)

interface AccountItem {
  id: string
  proxyProfileId?: string
  proxyProfileName?: string
  proxyProfileType?: string
  proxyProfileEnabled?: boolean
  proxyProfileUnavailable?: boolean
  proxyProfileErrorMessage?: string
  authorizationInstanceSourceAccountId?: string
}

interface AccountPage { items: AccountItem[] }
interface Envelope<T> { data: T }

let server: ReturnType<typeof app.listen> | undefined
try {
  const admin = repositories.listSystemAccounts().find((item) => item.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const userA = repositories.createSystemAccount({
    username: 'proxydisplayusera', displayName: '代理展示用户A', password: 'password', role: 'user', status: 'active', mustChangePassword: false
  })
  const userB = repositories.createSystemAccount({
    username: 'proxydisplayuserb', displayName: '代理展示用户B', password: 'password', role: 'user', status: 'active', mustChangePassword: false
  })
  const adminAccess = { systemAccountId: admin.id, role: 'super_admin' as const }
  const userAAccess = { systemAccountId: userA.id, role: 'user' as const }
  const userBAccess = { systemAccountId: userB.id, role: 'user' as const }
  const groupA = repositories.createGroup({ name: '代理展示用户 A 分组', providerCode: 'gpt' }, userAAccess)
  const groupB = repositories.createGroup({ name: '代理展示用户 B 分组', providerCode: 'gpt' }, userBAccess)
  const proxyEnabled = repositories.createProxy({
    name: '代理展示启用', type: 'http', host: '127.0.0.1', port: 18_081, username: 'user', password: 'secret', enabled: true
  }, userAAccess)
  const proxyDisabled = repositories.createProxy({
    name: '代理展示停用', type: 'socks5', host: '127.0.0.1', port: 18_082, enabled: true
  }, userAAccess)
  const createAccount = (name: string, proxyProfileId: string) => repositories.createAccount({
    providerCode: 'gpt', providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID, name, type: 'api_key',
    credentials: { api_key: `sk-${name}`, base_url: 'https://api.openai.com/v1' },
    supportedModels: ['gpt-5.4-mini'], healthCheckModel: 'gpt-5.4-mini',
    groupId: groupA.id, proxyProfileId, status: 'active'
  }, userAAccess)
  const enabledAccount = createAccount('代理展示启用账户', proxyEnabled.id)
  const disabledAccount = createAccount('代理展示停用账户', proxyDisabled.id)
  const missingAccount = createAccount('代理展示缺失账户', proxyEnabled.id)
  const database = databaseModule.getBusinessDatabase()
  database.prepare('UPDATE proxy_profiles SET enabled = 0 WHERE id = ?').run(proxyDisabled.id)
  database.prepare('PRAGMA foreign_keys = OFF').run()
  database.prepare('UPDATE accounts SET proxy_profile_id = ? WHERE id = ?').run('proxy-display-missing', missingAccount.id)
  database.prepare('PRAGMA foreign_keys = ON').run()
  database.prepare("UPDATE proxy_profiles SET password_encrypted = 'not-json' WHERE id = ?").run(proxyEnabled.id)

  repositories.createResourceAuthorization({
    resourceType: 'account', resourceId: enabledAccount.id, granteeType: 'system_account', granteeId: userB.id,
    targetGroupId: groupB.id, remark: '代理展示授权'
  }, adminAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account', resourceId: disabledAccount.id, granteeType: 'system_account', granteeId: userB.id,
    targetGroupId: groupB.id, remark: '代理展示停用授权'
  }, adminAccess)
  repositories.createResourceAuthorization({
    resourceType: 'account', resourceId: missingAccount.id, granteeType: 'system_account', granteeId: userB.id,
    targetGroupId: groupB.id, remark: '代理展示缺失授权'
  }, adminAccess)
  const authorizedAccount = repositories.listAccounts(userBAccess).find((item) => item.authorizationInstanceSourceAccountId === enabledAccount.id)
  const authorizedDisabledAccount = repositories.listAccounts(userBAccess).find((item) => item.authorizationInstanceSourceAccountId === disabledAccount.id)
  const authorizedMissingAccount = repositories.listAccounts(userBAccess).find((item) => item.authorizationInstanceSourceAccountId === missingAccount.id)
  assert(authorizedAccount, '授权实例未创建')
  assert(authorizedDisabledAccount, '停用代理授权实例未创建')
  assert(authorizedMissingAccount, '缺失代理授权实例未创建')

  server = app.listen(0, '127.0.0.1')
  await waitForListening(server)
  const address = server.address()
  assert(address && typeof address !== 'string', '测试服务地址不可用')
  const baseUrl = `http://127.0.0.1:${address.port}`
  const adminCookie = sessionCookie(admin.id)
  const userACookie = sessionCookie(userA.id)
  const userBCookie = sessionCookie(userB.id)

  const adminPage = await getData<AccountPage>(baseUrl, `/__aisys__/api/accounts?systemAccountId=${userA.id}&page=1&pageSize=20`, adminCookie)
  assert.equal(pick(adminPage.items, enabledAccount.id).proxyProfileName, '代理展示启用', '管理员应看到启用代理名称')
  assert.equal(pick(adminPage.items, enabledAccount.id).proxyProfileType, 'http')
  assert.equal(pick(adminPage.items, enabledAccount.id).proxyProfileEnabled, true)
  assert.equal(pick(adminPage.items, disabledAccount.id).proxyProfileName, '代理展示停用', '管理员应看到停用代理名称')
  assert.equal(pick(adminPage.items, disabledAccount.id).proxyProfileEnabled, false)

  const userAPage = await getData<AccountPage>(baseUrl, '/__aisys__/api/my-accounts?page=1&pageSize=20', userACookie)
  const userEnabled = pick(userAPage.items, enabledAccount.id)
  assert.deepEqual({ id: userEnabled.proxyProfileId, name: userEnabled.proxyProfileName, type: userEnabled.proxyProfileType, enabled: userEnabled.proxyProfileEnabled }, {
    id: proxyEnabled.id, name: '代理展示启用', type: 'http', enabled: true
  })
  const userDisabled = pick(userAPage.items, disabledAccount.id)
  assert.equal(userDisabled.proxyProfileUnavailable, true, '普通用户停用代理应标记不可用')
  assert.equal(userDisabled.proxyProfileName, '代理展示停用', '服务器级全局代理停用后仍应保留已绑定账户的可显示名称')
  assert.equal(userDisabled.proxyProfileType, 'socks5', '服务器级全局代理停用后仍应保留已绑定账户的可显示类型')
  assert.equal(userDisabled.proxyProfileEnabled, false, '服务器级全局代理停用后应明确返回不可用状态')
  const userMissing = pick(userAPage.items, missingAccount.id)
  assert.equal(userMissing.proxyProfileUnavailable, true, '普通用户缺失代理应标记不可用')
  assert.equal(Object.hasOwn(userMissing, 'proxyProfileName'), false, '普通用户不可见缺失代理名称')

  const userBPage = await getData<AccountPage>(baseUrl, '/__aisys__/api/my-accounts?page=1&pageSize=20', userBCookie)
  const userAuthorized = pick(userBPage.items, authorizedAccount.id)
  assert.equal(userAuthorized.authorizationInstanceSourceAccountId, enabledAccount.id)
  assert.equal(userAuthorized.proxyProfileName, '代理展示启用', '授权实例应使用来源账户代理展示字段')
  assert.equal(userAuthorized.proxyProfileEnabled, true)
  const userAuthorizedDisabled = pick(userBPage.items, authorizedDisabledAccount.id)
  assert.equal(userAuthorizedDisabled.proxyProfileId, proxyDisabled.id, '停用授权实例应使用来源账户代理 ID')
  assert.equal(userAuthorizedDisabled.proxyProfileUnavailable, true)
  assert.equal(userAuthorizedDisabled.proxyProfileName, '代理展示停用', '授权实例应复用服务器级全局代理的可显示名称')
  assert.equal(userAuthorizedDisabled.proxyProfileType, 'socks5', '授权实例应复用服务器级全局代理的可显示类型')
  assert.equal(userAuthorizedDisabled.proxyProfileEnabled, false, '授权实例应明确返回服务器级全局代理停用状态')
  const userAuthorizedMissing = pick(userBPage.items, authorizedMissingAccount.id)
  assert.equal(userAuthorizedMissing.proxyProfileId, 'proxy-display-missing', '缺失授权实例应使用来源账户代理 ID')
  assert.equal(userAuthorizedMissing.proxyProfileUnavailable, true)
  assert.equal(Object.hasOwn(userAuthorizedMissing, 'proxyProfileName'), false, '缺失授权实例不可泄露代理名称')

  const source = await import('node:fs/promises').then((fs) => fs.readFile(new URL('../../storage/account-summary.repository.ts', import.meta.url), 'utf8'))
  assert.match(source, /proxy_profiles[\s\S]+IN \(/, '账户代理展示必须按当前页代理 ID 批量读取')
  assert.doesNotMatch(source, /rows\.map\(async[\s\S]+proxy_profiles/, '账户代理展示禁止逐行查询')
  console.log('账户代理最小展示字段回归通过')
} finally {
  await closeServer(server)
  try { databaseModule.getBusinessDatabase().close(); databaseModule.closeStorageDatabases() } catch {}
  rmSync(tempRoot, { recursive: true, force: true })
}

function pick(items: AccountItem[], id: string): AccountItem {
  const item = items.find((candidate) => candidate.id === id)
  assert(item, `账户 ${id} 未出现在响应中`)
  return item
}

function sessionCookie(systemAccountId: string): string {
  return `juhe_ai_session=${repositories.createSession(systemAccountId, 1).token}`
}

async function getData<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { headers: { cookie } })
  const body = await response.text()
  assert.equal(response.ok, true, `${path} HTTP ${response.status}: ${body}`)
  return (JSON.parse(body) as Envelope<T>).data
}

async function waitForListening(listeningServer: ReturnType<typeof app.listen>): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: ReturnType<typeof app.listen>): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => listeningServer.close((error) => error ? rejectPromise(error) : resolvePromise()))
}
