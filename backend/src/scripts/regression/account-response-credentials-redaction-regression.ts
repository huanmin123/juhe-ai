import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-response-redaction-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-response-redaction.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-response-redaction-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { createSystemApiApp },
  databaseModule,
  repositories
] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AccountResponse {
  id: string
  credentials: Record<string, unknown>
}

interface AccountListResponse {
  items: AccountResponse[]
}

interface TrafficMigrationResponse {
  sourceAccount: AccountResponse
  targetAccount: AccountResponse
}

interface JsonRequestInit {
  method: string
  headers?: Record<string, string>
  body?: string
}

const secretValues = [
  'sk-redaction-existing-api-key',
  'sk-redaction-created-api-key',
  'oauth-redaction-access-token',
  'oauth-redaction-refresh-token',
  'oauth-redaction-id-token',
  'sk-redaction-target-api-key'
]

let server: http.Server | undefined

try {
  const seed = seedData()
  const app = createSystemApiApp({ systemApiPrefix: '/__aisys__/api' })
  server = app.listen(0, '127.0.0.1')
  await listen(server)
  const baseUrl = `http://127.0.0.1:${serverAddress(server).port}`

  const accountList = await getEnvelope<AccountListResponse>(baseUrl, '/__aisys__/api/accounts?page=1&pageSize=20', seed.adminCookie)
  assertNoCredentialLeak(accountList, '账户列表响应')

  const detail = await getEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}`, seed.adminCookie)
  assert.equal(detail.credentials.base_url, 'https://api.openai.com/v1', '详情响应应保留前端编辑需要的 Base URL')
  assert.deepEqual(detail.credentials.error_handling_rules, seed.errorHandlingRules, '详情响应应保留非敏感错误策略配置')
  assertNoCredentialLeak(detail, '账户详情响应')

  const created = await postEnvelope<AccountResponse>(baseUrl, '/__aisys__/api/accounts', seed.adminCookie, {
    providerCode: 'openai',
    name: '响应脱敏新建账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-created-api-key',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: seed.groupAId
  })
  assert.equal(created.credentials.base_url, 'https://api.openai.com/v1', '创建响应应保留 Base URL')
  assertNoCredentialLeak(created, '账户创建响应')

  const updatedApiKey = await patchEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}`, seed.adminCookie, {
    name: '响应脱敏 API Key 已更新',
    credentials: {
      base_url: 'https://api.openai.com/v1'
    },
    groupId: seed.groupAId
  })
  assertNoCredentialLeak(updatedApiKey, 'API Key 编辑响应')
  assert.equal(repositories.findAccountForTest(seed.apiKeyAccountId)?.credentials.api_key, 'sk-redaction-existing-api-key', 'API Key 编辑留空时应保留原密钥')

  const updatedOAuth = await patchEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.oauthAccountId}`, seed.adminCookie, {
    name: '响应脱敏 OAuth 已更新',
    credentials: {
      base_url: 'https://api.openai.com/v1'
    },
    groupId: seed.groupAId
  })
  assertNoCredentialLeak(updatedOAuth, 'OAuth 编辑响应')
  const latestOAuth = repositories.findAccountForTest(seed.oauthAccountId)
  assert.equal(latestOAuth?.credentials.access_token, 'oauth-redaction-access-token', 'OAuth 编辑留空时应保留原 access_token')
  assert.equal(latestOAuth?.credentials.refresh_token, 'oauth-redaction-refresh-token', 'OAuth 编辑留空时应保留原 refresh_token')
  assert.equal(latestOAuth?.credentials.id_token, 'oauth-redaction-id-token', 'OAuth 编辑留空时应保留原 id_token')

  const rebound = await postEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/group`, seed.adminCookie, {
    groupId: seed.groupBId
  })
  assertNoCredentialLeak(rebound, '账户绑定分组响应')

  const migration = await postEnvelope<TrafficMigrationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/traffic-migration`, seed.adminCookie, {
    targetAccountId: seed.targetAccountId,
    sourceStatus: 'disabled'
  })
  assertNoCredentialLeak(migration, '账户流量迁移响应')
  assertOAuthRoutesUseAccountResponseSanitizer()

  console.log('AI 账户响应凭据脱敏回归通过：详情、创建、编辑、绑定分组和迁移响应均不返回明文凭据，编辑留空保留原凭据')
} finally {
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function seedData(): {
  adminCookie: string
  apiKeyAccountId: string
  errorHandlingRules: unknown[]
  groupAId: string
  groupBId: string
  oauthAccountId: string
  targetAccountId: string
} {
  const admin = repositories.listSystemAccounts().find((account) => account.username === 'admin')
  assert(admin, '默认管理员不存在')
  repositories.updateSystemAccount(admin.id, { mustChangePassword: false })
  const access = { systemAccountId: admin.id, role: admin.role }
  const groupA = repositories.createGroup({
    name: '响应脱敏分组 A',
    providerCode: 'openai'
  }, access)
  const groupB = repositories.createGroup({
    name: '响应脱敏分组 B',
    providerCode: 'openai'
  }, access)
  const errorHandlingRules = [{
    enabled: true,
    name: '响应脱敏限流规则',
    priority: 10,
    status_codes: [429],
    action: 'rate_limited',
    reset_strategy: 'duration',
    duration_hours: 1
  }]
  const apiKeyAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '响应脱敏 API Key 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-existing-api-key',
      base_url: 'https://api.openai.com/v1',
      error_handling_rules: errorHandlingRules
    },
    status: 'active',
    groupId: groupA.id
  }, access)
  const oauthAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '响应脱敏 OAuth 账号',
    type: 'oauth',
    credentials: {
      access_token: 'oauth-redaction-access-token',
      refresh_token: 'oauth-redaction-refresh-token',
      id_token: 'oauth-redaction-id-token',
      expires_at: '2027-01-01T00:00:00.000Z',
      client_id: 'oauth-redaction-client',
      email: 'redaction@example.com',
      account_id: 'acct-redaction',
      chatgpt_user_id: 'user-redaction',
      plan_type: 'plus',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: groupA.id
  }, access)
  const targetAccount = repositories.createAccount({
    providerCode: 'openai',
    name: '响应脱敏迁移目标账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-target-api-key',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: groupB.id
  }, access)
  return {
    adminCookie: `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`,
    apiKeyAccountId: apiKeyAccount.id,
    errorHandlingRules,
    groupAId: groupA.id,
    groupBId: groupB.id,
    oauthAccountId: oauthAccount.id,
    targetAccountId: targetAccount.id
  }
}

async function getEnvelope<T>(baseUrl: string, path: string, cookie: string): Promise<T> {
  return requestEnvelope<T>(baseUrl, path, cookie, { method: 'GET' })
}

async function postEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  return requestEnvelope<T>(baseUrl, path, cookie, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
}

async function patchEnvelope<T>(baseUrl: string, path: string, cookie: string, body: unknown): Promise<T> {
  return requestEnvelope<T>(baseUrl, path, cookie, {
    method: 'PATCH',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body)
  })
}

async function requestEnvelope<T>(baseUrl: string, path: string, cookie: string, init: JsonRequestInit): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, {
    ...init,
    headers: {
      ...(init.headers ?? {}),
      cookie
    }
  })
  const text = await response.text()
  if (!response.ok) {
    throw new Error(`${path} HTTP ${response.status}: ${text}`)
  }
  return (JSON.parse(text) as ApiEnvelope<T>).data
}

function assertOAuthRoutesUseAccountResponseSanitizer(): void {
  const source = readFileSync('src/modules/openai-oauth/openai-oauth.routes.ts', 'utf8')
  assert.match(source, /ok\(sanitizeAccountResponse\(account\)\)/, 'OpenAI OAuth 创建响应必须经过账号响应脱敏器')
  assert.match(source, /ok\(sanitizeAccountResponse\(updated\)\)/, 'OpenAI OAuth 更新响应必须经过账号响应脱敏器')
}

function assertNoCredentialLeak(value: unknown, label: string): void {
  assertNoForbiddenCredentialKeys(value, label)
  const text = JSON.stringify(value)
  for (const secret of secretValues) {
    assert.equal(text.includes(secret), false, `${label} 不应包含密钥原文 ${secret}`)
  }
}

function assertNoForbiddenCredentialKeys(value: unknown, label: string, path = '$'): void {
  if (!value || typeof value !== 'object') return
  if (Array.isArray(value)) {
    value.forEach((item, index) => assertNoForbiddenCredentialKeys(item, label, `${path}[${index}]`))
    return
  }
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    assert(!['api_key', 'access_token', 'refresh_token', 'id_token'].includes(key), `${label} 不应包含凭据字段 ${path}.${key}`)
    assertNoForbiddenCredentialKeys(child, label, `${path}.${key}`)
  }
}

async function listen(listeningServer: http.Server): Promise<void> {
  if (listeningServer.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.once('listening', resolvePromise)
    listeningServer.once('error', rejectPromise)
  })
}

async function closeServer(listeningServer?: http.Server): Promise<void> {
  if (!listeningServer?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    listeningServer.close((error) => {
      if (error) {
        rejectPromise(error)
      } else {
        resolvePromise()
      }
    })
  })
}

function serverAddress(listeningServer: http.Server): { port: number } {
  const address = listeningServer.address()
  assert(address && typeof address !== 'string', '测试服务器应监听 TCP 地址')
  return { port: address.port }
}
