import { strict as assert } from 'node:assert'
import http from 'node:http'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { publicAccountRuntimeAvailability } from '../../domain/account-runtime-availability-public.js'
import { GPT_OPENAI_V1_PROFILE_ID } from '../../domain/provider-protocol.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-account-response-redaction-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'account-response-redaction.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'account-response-redaction-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { createSystemApiApp },
  databaseModule,
  oauthRefreshService,
  repositories,
  accountResponseSanitizer
] = await Promise.all([
  import('../../modules/system-api/system-api-app.js'),
  import('../../storage/database.js'),
  import('../../modules/openai-oauth/openai-oauth-access-token-refresh.service.js'),
  import('../../storage/repositories.js'),
  import('../../modules/accounts/account-response-sanitizer.js')
])

interface ApiEnvelope<T> {
  data: T
  message?: string
}

interface AccountResponse {
  id: string
  configRevision?: number
  credentials?: Record<string, unknown>
  supportedModels?: string[]
  modelMappings?: unknown[]
  apiKeyRuntimeDetails?: unknown[]
  runtimeAvailability?: Record<string, unknown>
}

interface AccountEditBasicResponse extends AccountResponse {
  configRevision: number
  credentials: Record<string, unknown>
  supportedModels: string[]
}

interface AccountAdvancedResponse {
  id: string
  configRevision: number
  accessType: 'owner' | 'authorized'
  credentials?: Record<string, unknown>
  modelMappings: unknown[]
  temporaryUnavailableContinuousProbeEnabled: boolean
  balanceQueryEnabled: boolean
}

interface AccountCreateResponse {
  id: string
  status: string
}

interface AccountMutationResponse {
  id: string
  configRevision: number
  changedFields: string[]
  authorizationInstancesAffected?: boolean
}

interface OAuthCredentialRotationResponse {
  id: string
  configRevision: number
  updatedAt: string
}

interface AccountApiKeyRuntimeResponse {
  accountId: string
  configRevision: number
  items: Array<Record<string, unknown>>
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
    assertNoInternalRuntimeLeak(account, '账户列表响应')
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'credentials'), false, '账户列表响应不应返回 credentials 字段')
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'supportedModels'), false, '账户列表响应不应返回 supportedModels 字段')
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'modelMappings'), false, '账户列表响应不应返回 modelMappings 字段')
    assert.equal(Object.prototype.hasOwnProperty.call(account, 'apiKeyRuntimeDetails'), false, '账户列表响应不应返回 API Key 运行明细')
  }

  const legacyDetail = await fetch(`${baseUrl}/__aisys__/api/accounts/${seed.apiKeyAccountId}`, {
    headers: { cookie: seed.adminCookie }
  })
  assert.equal(legacyDetail.status, 404, '无场景的 legacy 账户详情入口应已移除')

  const unsafeRuntime = publicAccountRuntimeAvailability({
    status: 'precheck_pending',
    reason: '回归测试',
    since: '2026-07-23T00:00:00.000Z',
    failureCount: 8,
    distinctClientIpCount: 3,
    distinctApiKeyCount: 2,
    precheckAttemptCount: 4,
    localFailureCount: 3,
    probePresentation: {
      schedule: { state: 'running' },
      recoveryAt: '2026-07-23T00:02:00.000Z',
      recoveryAtKind: 'policy_ttl_expiry'
    }
  })
  assert.deepEqual(unsafeRuntime, {
    status: 'precheck_pending',
    reason: '回归测试',
    since: '2026-07-23T00:00:00.000Z',
    probePresentation: { schedule: { state: 'running' } }
  }, '公开运行态投影不得携带内部计数、租约或恢复时间')
  const sanitizedSynthetic = accountResponseSanitizer.sanitizeAccountRuntimeAvailabilityResponse({
    runtimeAvailability: {
      status: 'precheck_pending',
      failureCount: 8,
      distinctClientIpCount: 3,
      distinctApiKeyCount: 2,
      precheckAttemptCount: 4,
      localFailureCount: 3,
      probePresentation: {
        schedule: { state: 'running' },
        recoveryAt: '2026-07-23T00:02:00.000Z'
      }
    }
  })
  assertNoInternalRuntimeLeak(sanitizedSynthetic, '账户响应 sanitizer')

  const editBasicDetail = await getEnvelope<AccountEditBasicResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/edit-basic`, seed.adminCookie)
  assert(editBasicDetail.credentials, '账户编辑首屏详情应返回基础凭据字段')
  assert.equal(editBasicDetail.credentials.api_key, 'sk-redaction-existing-api-key', '账户编辑首屏详情应返回完整 API Key 供用户查看和修改')
  assert.equal(editBasicDetail.credentials.base_url, 'https://api.openai.com/v1', '账户编辑首屏详情应返回 Base URL')
  assert(Array.isArray(editBasicDetail.credentials.supported_endpoint_modes), '账户编辑首屏详情应返回接口能力')
  assert(Array.isArray(editBasicDetail.supportedModels) && editBasicDetail.supportedModels.length > 0, '账户编辑首屏详情应返回支持模型')
  assert.equal(Object.prototype.hasOwnProperty.call(editBasicDetail, 'modelMappings'), false, '账户编辑首屏详情不应返回模型映射')
  assert.equal(Object.prototype.hasOwnProperty.call(editBasicDetail, 'apiKeyRuntimeDetails'), false, '账户编辑首屏详情不应返回 API Key 运行明细')
  assert.equal(Object.hasOwn(editBasicDetail.credentials, 'error_handling_rules'), false, '账户编辑首屏不应提前返回高级错误策略')
  assert.equal(Object.hasOwn(editBasicDetail.credentials, 'response_inspection_rules'), false, '账户编辑首屏不应提前返回高级响应检查策略')
  assertNoForbiddenCredentialKeysExcept(
    editBasicDetail,
    '账户编辑首屏详情响应',
    new Set(['api_key', 'api_keys', 'api_key_strategy', 'api_key_weights'])
  )

  const detail = await getEnvelope<AccountAdvancedResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/advanced`, seed.adminCookie)
  assert.deepEqual(detail.credentials?.error_handling_rules, [{
    enabled: true,
    name: '响应脱敏账户错误处理',
    priority: 10,
    status_codes: [429],
    action: 'temp_unschedulable'
  }], '账户高级详情应按需返回错误策略')
  assert.deepEqual(detail.credentials?.response_inspection_rules, [{
    enabled: true,
    name: '响应脱敏账户响应检查',
    priority: 11,
    match: {
      outputTextIncludes: ['响应污染']
    },
    action: 'retry_next_account'
  }], '账户高级详情应按需返回响应检查策略')
  assertNoForbiddenCredentialKeysExcept(
    detail,
    '账户高级详情响应',
    new Set(['error_handling_rules', 'response_inspection_rules'])
  )
  assertNoSecretValueLeak(detail, '账户高级详情响应')

  const batchEditContext = await postEnvelope<AccountResponse[]>(baseUrl, '/__aisys__/api/accounts/batch-edit-context', seed.adminCookie, {
    accountIds: [seed.apiKeyAccountId, seed.multiApiKeyAccountId],
    fields: ['supportedModels', 'modelMappings', 'supportedEndpointModes']
  })
  assert.equal(batchEditContext.length, 2, '批量编辑上下文应一次返回全部目标账户')
  assert.deepEqual(Object.keys(batchEditContext[0] ?? {}).sort(), [
    'configRevision',
    'id',
    'modelMappings',
    'providerCode',
    'providerProtocolProfileId',
    'protocolCode',
    'protocolVersion',
    'supportedEndpointModes',
    'supportedModels',
    'type'
  ].sort(), '批量编辑上下文 HTTP 响应只能包含实际消费字段')
  for (const forbiddenField of [
    'credentials', 'tags', 'usage', 'todayUsage', 'permissions', 'runtimeAvailability',
    'currentConcurrency', 'status', 'authorizationSources', 'apiKeyRuntime'
  ]) {
    assert.equal(Object.hasOwn(batchEditContext[0] ?? {}, forbiddenField), false, `批量编辑上下文不得返回 ${forbiddenField}`)
  }
  const batchEditContextText = JSON.stringify(batchEditContext)
  for (const secret of secretValues) {
    assert.equal(batchEditContextText.includes(secret), false, `批量编辑上下文响应不应包含密钥原文 ${secret}`)
  }

  const multiKeyEditBasicDetail = await getEnvelope<AccountEditBasicResponse>(baseUrl, `/__aisys__/api/accounts/${seed.multiApiKeyAccountId}/edit-basic`, seed.adminCookie)
  assert(multiKeyEditBasicDetail.credentials, '账户编辑首屏详情应返回多 API Key 凭据')
  assert.deepEqual(multiKeyEditBasicDetail.credentials.api_keys, ['sk-redaction-multi-a', 'sk-redaction-multi-b'], '账户编辑首屏详情应返回完整多 API Key 列表供用户查看和修改')
  assert.equal(multiKeyEditBasicDetail.credentials.api_key_strategy, 'weighted_round_robin', '账户编辑首屏详情应返回多 API Key 调度策略')
  assert.deepEqual(multiKeyEditBasicDetail.credentials.api_key_weights, [2, 1], '账户编辑首屏详情应返回多 API Key 权重')
  assertNoForbiddenCredentialKeysExcept(multiKeyEditBasicDetail, '账户编辑首屏多 Key 详情响应', new Set(['api_key', 'api_keys', 'api_key_strategy', 'api_key_weights']))

  const runtimeResponse = await fetch(`${baseUrl}/__aisys__/api/accounts/${seed.multiApiKeyAccountId}/api-key-runtime`, {
    headers: { cookie: seed.adminCookie }
  })
  assert.equal(runtimeResponse.status, 200, `账户 API Key 运行明细接口应可读取，实际响应：${await runtimeResponse.clone().text()}`)
  assert.match(runtimeResponse.headers.get('cache-control') ?? '', /no-store/i, '账户 API Key 运行明细接口必须禁用客户端和中间缓存')
  const runtimeEnvelope = JSON.parse(await runtimeResponse.text()) as ApiEnvelope<AccountApiKeyRuntimeResponse>
  assert.equal(runtimeEnvelope.data.accountId, seed.multiApiKeyAccountId, '账户 API Key 运行明细必须绑定请求账户')
  assert.equal(runtimeEnvelope.data.configRevision, multiKeyEditBasicDetail.configRevision, '账户 API Key 运行明细必须携带保存配置版本')
  assert.equal(runtimeEnvelope.data.items.length, 2, '多 Key 账户运行明细应返回每个已保存 Key 的轻量状态')
  assertNoCredentialLeak(runtimeEnvelope.data, '账户 API Key 运行明细响应')
  for (const item of runtimeEnvelope.data.items) {
    assert.equal(Object.prototype.hasOwnProperty.call(item, 'credentials'), false, '账户 API Key 运行明细不得返回 credentials')
    assert.equal(Object.prototype.hasOwnProperty.call(item, 'apiKey'), false, '账户 API Key 运行明细不得返回 API Key 明文')
  }

  assert.deepEqual(Object.keys(detail).sort(), [
    'accessType',
    'balanceQueryEnabled',
    'configRevision',
    'credentials',
    'id',
    'modelMappings',
    'temporaryUnavailableContinuousProbeEnabled'
  ].sort(), '高级详情只能返回高级表单使用的窄字段')
  assert.equal(detail.accessType, 'owner')

  const multiKeyDetail = await getEnvelope<AccountAdvancedResponse>(baseUrl, `/__aisys__/api/accounts/${seed.multiApiKeyAccountId}/advanced`, seed.adminCookie)
  for (const forbiddenField of ['supportedModels', 'tags', 'runtimeAvailability', 'usage', 'todayUsage', 'permissions']) {
    assert.equal(Object.prototype.hasOwnProperty.call(multiKeyDetail, forbiddenField), false, `多 Key 高级详情不得混入 ${forbiddenField}`)
  }
  assert.deepEqual(Object.keys(multiKeyDetail.credentials ?? {}).sort(), [
    'codex_responses_safe_repair_enabled',
    'codex_responses_strict_intercept_enabled'
  ], '多 Key 高级详情只应返回高级策略开关')
  assertNoForbiddenCredentialKeysExcept(
    multiKeyDetail,
    '多 API Key 账户高级详情响应',
    new Set(['error_handling_rules', 'response_inspection_rules'])
  )
  assertNoSecretValueLeak(multiKeyDetail, '多 API Key 账户高级详情响应')

  const created = await postEnvelope<AccountCreateResponse>(baseUrl, '/__aisys__/api/accounts', seed.adminCookie, {
    providerCode: 'gpt',
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: '响应脱敏新建账号',
    type: 'api_key',
    credentials: {
      api_key: 'sk-redaction-created-api-key',
      base_url: 'https://api.openai.com/v1'
    },
    supportedModels: ['gpt-5.4-mini'],
    healthCheckModel: 'gpt-5.4-mini',
    groupId: seed.groupAId,
    status: 'disabled'
  })
  assert.deepEqual(Object.keys(created).sort(), ['id', 'status'], '账户创建响应只应返回后续定位所需的 id 与 status')
  assert.equal(created.status, 'disabled')
  assertNoCredentialLeak(created, '账户创建响应')

  const updatedApiKey = await patchEnvelope<AccountMutationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}`, seed.adminCookie, {
    expectedConfigRevision: editBasicDetail.configRevision,
    name: '响应脱敏 API Key 已更新',
    notes: '只修改名称和备注'
  })
  assert.deepEqual(Object.keys(updatedApiKey).sort(), ['authorizationInstancesAffected', 'changedFields', 'configRevision', 'id'], '账户 PATCH 响应只能返回变更定位字段和授权实例影响信号')
  assert.equal(updatedApiKey.authorizationInstancesAffected, true, '账户名称变化应通知前端定点刷新已加载的授权实例')
  assert.deepEqual(updatedApiKey.changedFields, ['name', 'notes'], '账户 PATCH 只应声明实际变更字段')
  assertNoCredentialLeak(updatedApiKey, 'API Key 编辑响应')
  assert.equal(repositories.findAccountForTest(seed.apiKeyAccountId)?.credentials.api_key, 'sk-redaction-existing-api-key', '只改名称和备注时不应覆盖 API Key')

  const replacedMultiApiKey = await patchEnvelope<AccountMutationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.multiApiKeyAccountId}`, seed.adminCookie, {
    expectedConfigRevision: multiKeyEditBasicDetail.configRevision,
    name: '响应脱敏多 Key 已改单 Key',
    credentialsPatch: {
      api_key: 'sk-redaction-single-replacement',
      api_keys: null,
      api_key_strategy: null,
      api_key_weights: null
    }
  })
  assert.deepEqual(Object.keys(replacedMultiApiKey).sort(), ['authorizationInstancesAffected', 'changedFields', 'configRevision', 'id'], '凭据 PATCH 也只能返回变更定位字段和授权实例影响信号')
  assert.equal(replacedMultiApiKey.authorizationInstancesAffected, true, '凭据变化应通知前端定点刷新已加载的授权实例')
  assert(replacedMultiApiKey.changedFields.includes('credentials.api_key'), '凭据 PATCH 应声明实际变化的 API Key 字段')
  assert(replacedMultiApiKey.changedFields.includes('credentials.api_keys'), '多 Key 改单 Key 应声明删除 api_keys')
  assertNoCredentialLeak(replacedMultiApiKey, '多 API Key 改单 API Key 编辑响应')
  const latestMultiApiKey = repositories.findAccountForTest(seed.multiApiKeyAccountId)
  assert.equal(latestMultiApiKey?.credentials.api_key, 'sk-redaction-single-replacement', '多 API Key 改单 API Key 时应保存新的单 Key')
  assert.equal(Array.isArray(latestMultiApiKey?.credentials.api_keys), false, '多 API Key 改单 API Key 时不应保留旧 api_keys 数组')

  const oauthEditBasicDetail = await getEnvelope<AccountEditBasicResponse>(baseUrl, `/__aisys__/api/accounts/${seed.oauthAccountId}/edit-basic`, seed.adminCookie)
  const updatedOAuth = await patchEnvelope<AccountMutationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.oauthAccountId}`, seed.adminCookie, {
    expectedConfigRevision: oauthEditBasicDetail.configRevision,
    name: '响应脱敏 OAuth 已更新',
    notes: 'OAuth 只改备注'
  })
  assert.deepEqual(Object.keys(updatedOAuth).sort(), ['authorizationInstancesAffected', 'changedFields', 'configRevision', 'id'], 'OAuth PATCH 响应也必须保持窄字段')
  assert.equal(updatedOAuth.authorizationInstancesAffected, true, 'OAuth 账户名称变化应通知前端定点刷新已加载的授权实例')
  assertNoCredentialLeak(updatedOAuth, 'OAuth 编辑响应')
  const latestOAuth = repositories.findAccountForTest(seed.oauthAccountId)
  assert.equal(latestOAuth?.credentials.access_token, 'oauth-redaction-access-token', 'OAuth 编辑留空时应保留原 access_token')
  assert.equal(latestOAuth?.credentials.refresh_token, 'oauth-redaction-refresh-token', 'OAuth 编辑留空时应保留原 refresh_token')
  assert.equal(latestOAuth?.credentials.id_token, 'oauth-redaction-id-token', 'OAuth 编辑留空时应保留原 id_token')

  runtimeConfig.processRole = 'db-service'
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
  const refreshedOAuth = await postEnvelope<OAuthCredentialRotationResponse>(baseUrl, `/__aisys__/api/openai-oauth/accounts/${seed.oauthAccountId}/refresh-token`, seed.adminCookie, {
    expectedConfigRevision: updatedOAuth.configRevision
  })
  assert.deepEqual(Object.keys(refreshedOAuth).sort(), ['configRevision', 'id', 'updatedAt'], 'OAuth 刷新响应只应返回版本协调字段')
  assert.equal(refreshedOAuth.id, seed.oauthAccountId)
  assert.equal(refreshedOAuth.configRevision, updatedOAuth.configRevision + 1, 'OAuth 刷新应推进配置版本')
  assertNoCredentialLeak(refreshedOAuth, 'OAuth 刷新响应')
  const refreshedOAuthStored = repositories.findAccountForTest(seed.oauthAccountId)
  assert.equal(refreshedOAuthStored?.credentials.access_token, 'oauth-redaction-refreshed-access-token', 'OAuth 刷新应保存新 access_token')
  assert.equal(refreshedOAuthStored?.credentials.expires_at, '2027-01-02T00:00:00.000Z', 'OAuth 刷新应保存新过期时间')
  assert.equal(refreshedOAuthStored?.credentials.base_url, 'https://api.openai.com/v1', 'OAuth 刷新应保留原 Base URL')
  runtimeConfig.processRole = 'worker'

  const rebound = await postEnvelope<AccountMutationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/group`, seed.adminCookie, {
    groupId: seed.groupBId,
    expectedConfigRevision: updatedApiKey.configRevision
  })
  assert.deepEqual(Object.keys(rebound).sort(), ['changedFields', 'configRevision', 'id'], '绑定分组响应只能返回变更定位字段')
  assert.deepEqual(rebound.changedFields, ['groupId'])
  assertNoCredentialLeak(rebound, '账户绑定分组响应')

  const retagged = await patchEnvelope<AccountMutationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/tags`, seed.adminCookie, {
    tags: ['响应脱敏标签'],
    expectedConfigRevision: rebound.configRevision
  })
  assert.deepEqual(Object.keys(retagged).sort(), ['changedFields', 'configRevision', 'id'], '更新标签响应只能返回变更定位字段')
  assert.deepEqual(retagged.changedFields, ['tags'])
  assertNoCredentialLeak(retagged, '账户标签响应')

  const migration = await postEnvelope<TrafficMigrationResponse>(baseUrl, `/__aisys__/api/accounts/${seed.apiKeyAccountId}/traffic-migration`, seed.adminCookie, {
    targetAccountId: seed.targetAccountId,
    sourceStatus: 'unchanged'
  })
  assertNoCredentialLeak(migration, '账户流量迁移响应')
  assert.equal(repositories.findAccountForTest(seed.apiKeyAccountId)?.status, 'active', '不影响原账户迁移不应修改源账户状态')
  assertAccountCreateResponseContracts()

  console.log('AI 账户响应凭据边界回归通过：列表和高级详情保持窄投影，基础编辑只返回表单凭据，创建与字段级 PATCH/分组/标签只返回变更定位字段')
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
    supportedModels: ['gpt-5.4-mini'],
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
    supportedModels: ['gpt-5.4-mini'],
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
    supportedModels: ['gpt-5.4-mini'],
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
    supportedModels: ['gpt-5.4-mini'],
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

function assertAccountCreateResponseContracts(): void {
  const source = readFileSync('src/modules/openai-oauth/openai-oauth.routes.ts', 'utf8')
  assert.match(source, /ok\(\{ id: account\.id, status: account\.status \}\)/, 'OpenAI OAuth 创建响应只能返回 id 与 status')
  assert.match(source, /id: updated\.id,[\s\S]*configRevision:[\s\S]*updatedAt:/, 'OpenAI OAuth 凭据更新响应只能返回版本协调字段')
  assert.doesNotMatch(source, /sanitizeAccount(?:CredentialCarrier)?Response\(updated\)/, 'OpenAI OAuth 凭据更新不得再返回完整账户响应')
  const repositorySource = readFileSync('src/storage/repositories.ts', 'utf8')
  assert.doesNotMatch(repositorySource, /loadSystemAccountNameForAccountWriteAsync/, '账户创建不得为拼装完整摘要额外读取系统账户名称')
  assert.doesNotMatch(
    repositorySource,
    /systemAccountName:\s*includeSystemAccountFields\(access\)[^\n]*loadSystemAccountNameMapByIds/,
    'SQLite 账户创建不得为拼装完整摘要额外读取系统账户名称'
  )
}

function assertNoCredentialLeak(value: unknown, label: string): void {
  assertNoForbiddenCredentialKeys(value, label)
  assertNoSecretValueLeak(value, label)
}

function assertNoSecretValueLeak(value: unknown, label: string): void {
  const text = JSON.stringify(value)
  for (const secret of secretValues) {
    assert.equal(text.includes(secret), false, `${label} 不应包含密钥原文 ${secret}`)
  }
}

function assertNoInternalRuntimeLeak(value: Pick<AccountResponse, 'runtimeAvailability'>, label: string): void {
  const runtime = value.runtimeAvailability
  if (!runtime) return
  for (const field of ['failureCount', 'distinctClientIpCount', 'distinctApiKeyCount', 'precheckAttemptCount', 'localFailureCount', 'until', 'leaseId', 'leasePurpose', 'leaseUntilMs']) {
    assert.equal(Object.prototype.hasOwnProperty.call(runtime, field), false, `${label} 不应返回运行态内部字段 ${field}`)
  }
  const probe = runtime.probePresentation
  if (probe && typeof probe === 'object') {
    assert.equal(Object.prototype.hasOwnProperty.call(probe, 'recoveryAt'), false, `${label} 不应返回运行态恢复时间`)
    assert.equal(Object.prototype.hasOwnProperty.call(probe, 'recoveryAtKind'), false, `${label} 不应返回运行态恢复时间类型`)
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
