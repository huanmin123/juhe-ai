import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
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
  oauthRefreshService,
  repositories
] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/database.js'),
  import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js'),
  import('../../storage/repositories.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AccountResponse {
  id: string
  credentials?: Record<string, unknown>
  supportedModels?: string[]
  modelMappings?: unknown[]
  apiKeyRuntimeDetails?: unknown[]
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
  'oauth-redaction-refreshed-access-token',
  'oauth-redaction-refreshed-refresh-token',
  'oauth-redaction-refreshed-id-token',
  'sk-redaction-target-api-key',
  'sk-redaction-multi-a',
  'sk-redaction-multi-b',
  'sk-redaction-single-replacement'
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
  for (const account of accountList.items) {
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'credentials'), false, '账户列表响应不应返回 credentials 字段')
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'supportedModels'), false, '账户列表响应不应返回 supportedModels 字段')
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'modelMappings'), false, '账户列表响应不应返回 modelMappings 字段')
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'apiKeyRuntimeDetails'), false, '账户列表响应不应返回 API Key 运行明细')
  }

  const basicDetail = await getEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}`, seed.adminCookie)
  assert.equal(Object.prototype.hasOwnProperty.call(basicDetail, 'credentials'), false, '账户基础详情不应返回 credentials 字段')
  assert.equal(Object.prototype.hasOwnProperty.call(basicDetail, 'supportedModels'), false, '账户基础详情不应返回 supportedModels 字段')
  assert.equal(Object.prototype.hasOwnProperty.call(basicDetail, 'modelMappings'), false, '账户基础详情不应返回 modelMappings 字段')
  assert.equal(Object.prototype.hasOwnProperty.call(basicDetail, 'apiKeyRuntimeDetails'), false, '账户基础详情不应返回 API Key 运行明细')
  assertNoCredentialLeak(basicDetail, '账户基础详情响应')

  const editBasicDetail = await getEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/edit-basic`, seed.adminCookie)
  assert(editBasicDetail.credentials, '账户编辑首屏详情应返回基础凭据字段')
  assert.equal(editBasicDetail.credentials.api_key, 'sk-redaction-existing-api-key', '账户编辑首屏详情应返回完整 API Key 供用户查看和修改')
  assert.equal(editBasicDetail.credentials.base_url, 'https://api.openai.com/v1', '账户编辑首屏详情应返回 Base URL')
  assert(Array.isArray(editBasicDetail.credentials.supported_endpoint_modes), '账户编辑首屏详情应返回接口能力')
  assert(Array.isArray(editBasicDetail.supportedModels) && editBasicDetail.supportedModels.length > 0, '账户编辑首屏详情应返回支持模型')
  assert.equal(Object.prototype.hasOwnProperty.call(editBasicDetail, 'modelMappings'), false, '账户编辑首屏详情不应返回模型映射')
  assert.equal(Object.prototype.hasOwnProperty.call(editBasicDetail, 'apiKeyRuntimeDetails'), false, '账户编辑首屏详情不应返回 API Key 运行明细')
  assertNoForbiddenCredentialKeysExcept(editBasicDetail, '账户编辑首屏详情响应', new Set(['api_key', 'api_keys', 'api_key_strategy', 'api_key_weights']))

  const batchEditContext = await postEnvelope<AccountResponse[]>(baseUrl, '/__aisys__/api/accounts/batch-edit-context', seed.adminCookie, {
    accountIds: [seed.apiKeyAccountId, seed.multiApiKeyAccountId]
  })
  assert.equal(batchEditContext.length, 2, '批量编辑上下文应一次返回全部目标账户')
  assert.deepEqual(batchEditContext[0]?.credentials?.error_handling_rules, [{
    enabled: true,
    name: '响应脱敏账户错误处理',
    priority: 10,
    status_codes: [429],
    action: 'temp_unschedulable'
  }], '批量编辑上下文应返回允许覆盖的错误策略')
  assert.equal(batchEditContext[0]?.credentials?.api_key, undefined, '批量编辑上下文不得返回 API Key')
  assert.equal(batchEditContext[0]?.credentials?.base_url, undefined, '批量编辑上下文不得返回 Base URL')
  assertNoForbiddenCredentialKeysExcept(
    batchEditContext,
    '批量编辑上下文响应',
    new Set(['error_handling_rules', 'response_inspection_rules'])
  )
  const batchEditContextText = JSON.stringify(batchEditContext)
  for (const secret of secretValues) {
    assert.equal(batchEditContextText.includes(secret), false, `批量编辑上下文响应不应包含密钥原文 ${secret}`)
  }

  const multiKeyEditBasicDetail = await getEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.multiApiKeyAccountId}/edit-basic`, seed.adminCookie)
  assert(multiKeyEditBasicDetail.credentials, '账户编辑首屏详情应返回多 API Key 凭据')
  assert.deepEqual(multiKeyEditBasicDetail.credentials.api_keys, ['sk-redaction-multi-a', 'sk-redaction-multi-b'], '账户编辑首屏详情应返回完整多 API Key 列表供用户查看和修改')
  assert.equal(multiKeyEditBasicDetail.credentials.api_key_strategy, 'weighted_round_robin', '账户编辑首屏详情应返回多 API Key 调度策略')
  assert.deepEqual(multiKeyEditBasicDetail.credentials.api_key_weights, [2, 1], '账户编辑首屏详情应返回多 API Key 权重')
  assertNoForbiddenCredentialKeysExcept(multiKeyEditBasicDetail, '账户编辑首屏多 Key 详情响应', new Set(['api_key', 'api_keys', 'api_key_strategy', 'api_key_weights']))

  const detail = await getEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/advanced`, seed.adminCookie)
  assert(detail.credentials, '账户高级详情应返回编辑凭据')
  assert.equal(detail.credentials.base_url, 'https://api.openai.com/v1', '详情响应应保留前端编辑需要的 Base URL')
  assert.deepEqual(detail.credentials.error_handling_rules, [{
    enabled: true,
    name: '响应脱敏账户错误处理',
    priority: 10,
    status_codes: [429],
    action: 'temp_unschedulable'
  }], '详情响应应返回账户级错误处理策略供编辑弹窗维护')
  assert.deepEqual(detail.credentials.response_inspection_rules, [{
    enabled: true,
    name: '响应脱敏账户响应检查',
    priority: 11,
    match: {
      outputTextIncludes: ['响应污染']
    },
    action: 'retry_next_account'
  }], '详情响应应返回账户级响应检查策略供编辑弹窗维护')
  assert.equal(detail.credentials.api_key, 'sk-redaction-existing-api-key', '详情响应应返回完整 API Key 供编辑弹窗查看')

  const multiKeyDetail = await getEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.multiApiKeyAccountId}/advanced`, seed.adminCookie)
  assert(multiKeyDetail.credentials, '账户高级详情应返回多 API Key 凭据')
  assert.deepEqual(multiKeyDetail.credentials.api_keys, ['sk-redaction-multi-a', 'sk-redaction-multi-b'], '详情响应应返回完整多 API Key 列表供编辑弹窗查看')

  const created = await postEnvelope<AccountResponse>(baseUrl, '/__aisys__/api/accounts', seed.adminCookie, {
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '响应脱敏新建账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-created-api-key',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: seed.groupAId
  })
  assert(created.credentials, '账户创建响应应返回公开凭据字段')
  assert(created.credentials, '创建响应应返回编辑凭据')
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

  const replacedMultiApiKey = await patchEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.multiApiKeyAccountId}`, seed.adminCookie, {
    name: '响应脱敏多 Key 已改单 Key',
    credentials: {
      api_key: 'sk-redaction-single-replacement',
      base_url: 'https://api.openai.com/v1'
    },
    groupId: seed.groupAId
  })
  assertNoCredentialLeak(replacedMultiApiKey, '多 API Key 改单 API Key 编辑响应')
  const latestMultiApiKey = repositories.findAccountForTest(seed.multiApiKeyAccountId)
  assert.equal(latestMultiApiKey?.credentials.api_key, 'sk-redaction-single-replacement', '多 API Key 改单 API Key 时应保存新的单 Key')
  assert.equal(Array.isArray(latestMultiApiKey?.credentials.api_keys), false, '多 API Key 改单 API Key 时不应保留旧 api_keys 数组')

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

  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest(async ({ refreshToken, clientId }) => {
    assert.equal(refreshToken, 'oauth-redaction-refresh-token', 'OAuth 刷新路由应使用原 refresh_token 换取新令牌')
    assert.equal(clientId, 'oauth-redaction-client', 'OAuth 刷新路由应沿用账号 client_id')
    return {
      accessToken: 'oauth-redaction-refreshed-access-token',
      refreshToken: 'oauth-redaction-refreshed-refresh-token',
      idToken: 'oauth-redaction-refreshed-id-token',
      expiresIn: 3600,
      expiresAt: '2027-01-02T00:00:00.000Z',
      clientId: 'oauth-redaction-client',
      email: 'redaction-refreshed@example.com',
      accountId: 'acct-redaction-refreshed',
      chatgptUserId: 'user-redaction-refreshed',
      planType: 'pro'
    }
  })
  const refreshedOAuth = await postEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/openai-oauth/accounts/${seed.oauthAccountId}/refresh-token`, seed.adminCookie, {})
  assert(refreshedOAuth.credentials, 'OAuth 刷新响应应返回公开凭据字段')
  assert(refreshedOAuth.credentials, 'OAuth 刷新响应应返回编辑凭据')
  assert.equal(refreshedOAuth.credentials.expires_at, '2027-01-02T00:00:00.000Z', 'OAuth 刷新响应应保留前端需要展示的过期时间')
  assert.equal(refreshedOAuth.credentials.base_url, 'https://api.openai.com/v1', 'OAuth 刷新响应应保留 Base URL')
  assertNoCredentialLeak(refreshedOAuth, 'OAuth 刷新响应')

  const rebound = await postEnvelope<AccountResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/group`, seed.adminCookie, {
    groupId: seed.groupBId
  })
  assertNoCredentialLeak(rebound, '账户绑定分组响应')

  const migration = await postEnvelope<TrafficMigrationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/traffic-migration`, seed.adminCookie, {
    targetAccountId: seed.targetAccountId,
    sourceStatus: 'unchanged'
  })
  assertNoCredentialLeak(migration, '账户流量迁移响应')
  assert.equal(repositories.findAccountForTest(seed.apiKeyAccountId)?.status, 'active', '不影响原账户迁移不应修改源账户状态')
  assertOAuthRoutesUseAccountResponseSanitizer()

  console.log('AI 账户响应凭据边界回归通过：详情按权限返回明文凭据，列表、创建、编辑、绑定分组和迁移响应仍不返回明文凭据，编辑留空保留原凭据')
} finally {
  oauthRefreshService.setOpenAIOAuthTokenRefresherForTest()
  await closeServer(server)
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  await removeTempRoot()
}
process.exit(0)

function seedData(): {
  adminCookie: string
  apiKeyAccountId: string
  multiApiKeyAccountId: string
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
    providerCode: 'gpt'
  }, access)
  const groupB = repositories.createGroup({
    name: '响应脱敏分组 B',
    providerCode: 'gpt'
  }, access)
  const apiKeyAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '响应脱敏 API Key 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-existing-api-key',
      base_url: 'https://api.openai.com/v1',
      error_handling_rules: [{
        enabled: true,
        name: '响应脱敏账户错误处理',
        priority: 10,
        status_codes: [429],
        action: 'temp_unschedulable'
      }],
      response_inspection_rules: [{
        enabled: true,
        name: '响应脱敏账户响应检查',
        priority: 11,
        match: {
          outputTextIncludes: ['响应污染']
        },
        action: 'retry_next_account'
      }]
    },
    status: 'active',
    groupId: groupA.id
  }, access)
  const oauthAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
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
  const multiApiKeyAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '响应脱敏多 API Key 账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-multi-a',
      api_keys: ['sk-redaction-multi-a', 'sk-redaction-multi-b'],
      api_key_strategy: 'weighted_round_robin',
      api_key_weights: [2, 1],
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: groupA.id
  }, access)
  const targetAccount = repositories.createAccount({
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '响应脱敏迁移目标账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-target-api-key',
      base_url: 'https://api.openai.com/v1'
    },
    status: 'active',
    groupId: groupB.id
  }, access)
  for (const account of [apiKeyAccount, oauthAccount, multiApiKeyAccount, targetAccount]) {
    repositories.recordAccountHealthCheckSuccess(account.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    })
  }
  return {
    adminCookie: `juhe_ai_session=${repositories.createSession(admin.id, 1).token}`,
    apiKeyAccountId: apiKeyAccount.id,
    multiApiKeyAccountId: multiApiKeyAccount.id,
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
  assert.match(source, /ok\(sanitizeAccount(?:CredentialCarrier)?Response\(updated\)\)/, 'OpenAI OAuth 更新响应必须经过账号响应脱敏器')
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
    assert(
      !['api_key', 'access_token', 'refresh_token', 'id_token', 'error_handling_rules', 'response_inspection_rules'].includes(key),
      `${label} 不应包含凭据字段 ${path}.${key}`
    )
    assertNoForbiddenCredentialKeys(child, label, `${path}.${key}`)
  }
}

function assertNoForbiddenCredentialKeysExcept(value: unknown, label: string, allowedKeys: ReadonlySet<string>, path = '$'): void {
  if (!value || typeof value !== 'object') return
  if (Array.isArray(value)) {
    value.forEach((item, index) => assertNoForbiddenCredentialKeysExcept(item, label, allowedKeys, `${path}[${index}]`))
    return
  }
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    assert(
      allowedKeys.has(key) || !['api_key', 'access_token', 'refresh_token', 'id_token', 'error_handling_rules', 'response_inspection_rules'].includes(key),
      `${label} 不应包含凭据字段 ${path}.${key}`
    )
    assertNoForbiddenCredentialKeysExcept(child, label, allowedKeys, `${path}.${key}`)
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

async function removeTempRoot(): Promise<void> {
  for (let attempt = 0; attempt < 6; attempt += 1) {
    try {
      rmSync(tempRoot, { recursive: true, force: true })
      return
    } catch (error) {
      if (!(error instanceof Error) || !/EBUSY|EPERM/.test(error.message)) {
        throw error
      }
      if (attempt === 5) return
      await new Promise((resolve) => setTimeout(resolve, 100))
    }
  }
}
