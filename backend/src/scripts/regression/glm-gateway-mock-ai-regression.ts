import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { listProviderModelCatalog } from '../../modules/model-pricing/model-catalog.service.js'
import { listProviderModelPricing } from '../../modules/model-pricing/model-pricing.service.js'
import { logger } from '../../shared/logger.js'

interface MockGlmHit {
  authorization: string
  bodyText: string
  method: string
  path: string
  rawUrl: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-glm-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'glm-gateway-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.codexContextStateShardCount = 4
runtimeConfig.secret = 'glm-gateway-mock-ai-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  apiKeyRotation,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  accountImportService,
  accountExportService
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/account-api-key-rotation.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/accounts/account-import.service.js'),
  import('../../modules/accounts/account-export.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: MockGlmHit[] = []

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createGlmMockUpstream()
    await listen(upstreamServer)
    const upstreamOrigin = `http://127.0.0.1:${serverAddress(upstreamServer).port}`

    assertGlmSeeds()
    assertGlmModelCatalog()
    assertGlmApiKeyPoolIsolation()

    const general = createGlmScenario({
      accountName: 'GLM Mock 通用账户',
      baseUrl: `${upstreamOrigin}/api/paas/v4/`,
      groupName: 'GLM Mock 通用分组',
      localApiKeyName: 'GLM Mock 通用 Key',
      providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-glm-general-upstream'
    })
    const coding = createGlmScenario({
      accountName: 'GLM Mock Coding 账户',
      baseUrl: `${upstreamOrigin}/api/coding/paas/v4`,
      groupName: 'GLM Mock Coding 分组',
      localApiKeyName: 'GLM Mock Coding Key',
      supportedModels: ['glm-5.2'],
      modelMappings: [{
        sourceModel: 'glm-5.2',
        sourceEndpointFamily: 'responses',
        upstreamModel: 'glm-5.2',
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }],
      providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-glm-coding-upstream'
    })
    const codingOpenAIStandard = createGlmScenario({
      accountName: 'GLM Mock Coding OpenAI 标准账户',
      baseUrl: `${upstreamOrigin}/api/coding-standard/paas/v4`,
      groupName: 'GLM Mock Coding OpenAI 标准分组',
      localApiKeyName: 'GLM Mock Coding OpenAI 标准 Key',
      providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-glm-coding-standard-upstream'
    })
    const failover = createGlmFailoverScenario(`${upstreamOrigin}/api/paas/v4/`)

    assertGlmCredentialCapabilities(general.groupId)
    assertGlmImportAndExportRoundTrip(general.accountId, coding.accountId, general.groupId)

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertGlmChatJson({
      baseUrl,
      localApiKey: general.localApiKey,
      model: 'glm-4.7-flash',
      expectedPath: '/api/paas/v4/chat/completions',
      expectedAuthorization: 'Bearer sk-glm-general-upstream',
      expectedContent: 'glm mock json ok'
    })
    await assertGlmChatSse({
      baseUrl,
      localApiKey: coding.localApiKey,
      model: 'glm-5.2',
      expectedPath: '/api/coding/paas/v4/chat/completions',
      expectedAuthorization: 'Bearer sk-glm-coding-upstream',
      expectedContent: 'glm mock sse ok'
    })
    await assertGlmCodingResponsesBridge(baseUrl, coding.localApiKey)
    await assertGlmCodingRejectsResponsesBridge(baseUrl, codingOpenAIStandard.localApiKey)
    await assertGlmJsonErrorSuppressesAndRecovers(baseUrl, failover.localApiKey, failover.rescueAccountId)
    await assertGlmRejectsResponses(baseUrl, general.localApiKey)

    console.log('glm gateway mock ai regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function assertGlmSeeds(): void {
  const glmProvider = repositories.listProviders().find((provider) => provider.code === GLM_PROVIDER_CODE)
  assert(glmProvider, '默认 provider seed 应包含 glm')
  assert.equal(glmProvider.defaultProtocolProfileId, GLM_CODING_OPENAI_V1_PROFILE_ID, 'GLM 默认档案应是 Coding Plan API')
  assert(glmProvider.protocolProfiles.some((profile) => profile.id === GLM_GENERAL_OPENAI_V1_PROFILE_ID), 'GLM provider 应包含通用 OpenAI Chat 档案')
  assert(glmProvider.protocolProfiles.some((profile) => profile.id === GLM_CODING_OPENAI_V1_PROFILE_ID), 'GLM provider 应包含 Coding OpenAI Chat 档案')
  assert(glmProvider.protocolProfiles.some((profile) => profile.id === GLM_CODING_ANTHROPIC_V1_PROFILE_ID), 'GLM provider 应包含 Coding Anthropic Messages 档案')

  const defaultGroups = repositories.listGroups(access).filter((group) => group.providerCode === GLM_PROVIDER_CODE && group.isDefault)
  assert.equal(defaultGroups.length, 1, '默认分组应只包含一个 GLM 供应商分组')
  assert.equal(defaultGroups[0]?.name, '默认 GLM 分组', '默认 GLM 分组名称应按供应商归并')
}

function assertGlmModelCatalog(): void {
  const pricing = listProviderModelPricing(GLM_PROVIDER_CODE)
  for (const id of [
    'glm-5.2',
    'glm-5.1',
    'glm-5',
    'glm-5-turbo',
    'glm-4.7',
    'glm-4.7-flashx',
    'glm-4.7-flash',
    'glm-4.6',
    'glm-4.5',
    'glm-4.5-x',
    'glm-4.5-air',
    'glm-4.5-airx',
    'glm-4.5-flash',
    'glm-4-32b-0414-128k',
    'glm-4-long',
    'glm-4-flashx-250414',
    'glm-4-flash-250414'
  ]) {
    assert(pricing.some((item) => item.model === id), `GLM 价格目录应包含官方文本模型 ${id}`)
  }
  assert(pricing.some((item) => item.model === 'glm-5.2-free'), 'GLM 价格目录应保留历史 glm-5.2-free 估算项')

  const catalog = listProviderModelCatalog({
    providerCode: GLM_PROVIDER_CODE,
    systemAccountId: access.systemAccountId,
    includeUnpriced: true
  })
  assert(catalog.some((item) => item.model === 'glm-4.7-flash'), 'GLM 模型目录应包含官方免费 glm-4.7-flash')
  assert.equal(catalog.some((item) => item.model === 'glm-5.2-free'), false, '非官方 glm-5.2-free 不应进入 GLM 可见模型目录')
  assert(catalog.every((item) => item.providerCode === GLM_PROVIDER_CODE), 'GLM 模型目录不应混入其他供应商模型')
}

function assertGlmApiKeyPoolIsolation(): void {
  assert.equal(apiKeyRotation.isAccountApiKeyPoolIsolationEnabled({
    providerCode: GLM_PROVIDER_CODE,
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-a',
      api_keys: ['sk-glm-a', 'sk-glm-b']
    }
  }), true, 'GLM API Key 账户应启用账户内 API Key 池隔离')
}

function createGlmScenario(input: {
  accountName: string
  baseUrl: string
  groupName: string
  localApiKeyName: string
  supportedModels?: string[]
  modelMappings?: Array<{
    sourceModel: string
    sourceEndpointFamily: 'responses'
    upstreamModel: string
    upstreamEndpointFamily: 'chat_completions'
    enabled: boolean
  }>
  providerProtocolProfileId: string
  upstreamApiKey: string
}): { accountId: string; groupId: string; localApiKey: string } {
  const group = repositories.createGroup({
    name: input.groupName,
    providerCode: GLM_PROVIDER_CODE,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: input.providerProtocolProfileId,
    name: input.accountName,
    type: 'api_key',
    credentials: {
      api_key: input.upstreamApiKey,
      base_url: input.baseUrl
    },
    supportedModels: input.supportedModels,
    modelMappings: input.modelMappings,
    groupId: group.id,
    status: 'active',
    priority: 0,
    schedulable: true
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: input.localApiKeyName,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')
  return { accountId: account.id, groupId: group.id, localApiKey: apiKey.key }
}

function createGlmFailoverScenario(baseUrl: string): { localApiKey: string; rescueAccountId: string } {
  const group = repositories.createGroup({
    name: 'GLM Mock 错误切号分组',
    providerCode: GLM_PROVIDER_CODE,
    enabled: true
  }, access)
  repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    name: 'GLM Mock 错误主账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-error-primary',
      base_url: baseUrl
    },
    groupId: group.id,
    status: 'active',
    priority: 0,
    schedulable: true
  }, access)
  const rescueAccount = repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    name: 'GLM Mock 错误备用账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-error-rescue',
      base_url: baseUrl
    },
    groupId: group.id,
    status: 'disabled',
    priority: 100,
    schedulable: false
  }, access)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'GLM Mock 错误切号 Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')
  return { localApiKey: apiKey.key, rescueAccountId: rescueAccount.id }
}

function assertGlmCredentialCapabilities(groupId: string): void {
  const created = repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    name: 'GLM 默认接口能力账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-default-modes',
      base_url: 'http://127.0.0.1:1/api/paas/v4/'
    },
    groupId,
    status: 'disabled',
    schedulable: false
  }, access)
  assert.deepEqual(created.credentials.supported_endpoint_modes, ['chat_json', 'chat_sse'], 'GLM 默认接口能力应只包含 Chat JSON/SSE')

  assert.throws(() => repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
    name: 'GLM 错误 Responses 能力账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-invalid-modes',
      base_url: 'http://127.0.0.1:1/api/paas/v4/',
      supported_endpoint_modes: ['responses_sse']
    },
    groupId,
    status: 'disabled',
    schedulable: false
  }, access), /Chat|Responses|接口能力/, 'GLM 账户不应允许 Responses endpoint mode')

  const codingInSameProviderGroup = repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
    name: 'GLM Coding 同供应商分组账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-same-provider-group',
      base_url: 'http://127.0.0.1:1/api/coding/paas/v4'
    },
    groupId,
    status: 'disabled',
    schedulable: false
  }, access)
  assert.equal(codingInSameProviderGroup.boundGroupId, groupId, 'GLM Coding 账户应允许加入同供应商 GLM 分组')
  assert.equal(codingInSameProviderGroup.providerProtocolProfileId, GLM_CODING_OPENAI_V1_PROFILE_ID, '账户接入类型仍应保留 GLM Coding 档案')
}

function assertGlmImportAndExportRoundTrip(generalAccountId: string, codingAccountId: string, generalGroupId: string): void {
  const preview = accountImportService.previewAccountImport({
    type: accountImportService.accountImportProtocolType,
    version: accountImportService.accountImportProtocolVersion,
    accounts: [
      {
        ref: 'glm-preview-general',
        name: 'GLM 导入预览通用账号',
        providerCode: GLM_PROVIDER_CODE,
        providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'disabled',
        groupId: generalGroupId,
        credentials: {
          api_key: 'sk-glm-preview-general',
          base_url: 'http://127.0.0.1:1/api/paas/v4/'
        }
      },
      {
        ref: 'glm-preview-coding',
        name: 'GLM 导入预览 Coding 账号',
        providerCode: GLM_PROVIDER_CODE,
        providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
        type: 'api_key',
        status: 'disabled',
        groupId: generalGroupId,
        credentials: {
          api_key: 'sk-glm-preview-coding',
          base_url: 'http://127.0.0.1:1/api/coding/paas/v4'
        }
      }
    ]
  }, { createMissingGroups: false }, access)
  assert.equal(preview.canImport, true, `GLM 导入预览应通过：${JSON.stringify(preview.accounts)}`)
  assert.equal(preview.accounts.find((item) => item.ref === 'glm-preview-general')?.providerProtocolProfileId, GLM_GENERAL_OPENAI_V1_PROFILE_ID)
  assert.equal(preview.accounts.find((item) => item.ref === 'glm-preview-coding')?.providerProtocolProfileId, GLM_CODING_OPENAI_V1_PROFILE_ID)

  const exported = accountExportService.exportAccountsAsImportDocument({
    accountIds: [generalAccountId, codingAccountId]
  }, access).document
  const general = exported.accounts.find((account) => account.ref === generalAccountId)
  const coding = exported.accounts.find((account) => account.ref === codingAccountId)
  assert.equal(general?.providerProtocolProfileId, GLM_GENERAL_OPENAI_V1_PROFILE_ID, '导出通用 GLM 账号应保留协议档案')
  assert.equal(coding?.providerProtocolProfileId, GLM_CODING_OPENAI_V1_PROFILE_ID, '导出 GLM Coding 账号应保留协议档案')
  assert.equal('clientCompatibility' in (general ?? {}), false, '导出通用 GLM 账号不应再携带账号客户端兼容')
  assert.equal('clientCompatibility' in (coding ?? {}), false, '导出 GLM Coding 账号不应再携带账号客户端兼容')
}

async function assertGlmJsonErrorSuppressesAndRecovers(baseUrl: string, localApiKey: string, rescueAccountId: string): Promise<void> {
  const start = upstreamHits.length
  const failureResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'glm-4.7-flash',
      messages: [{ role: 'user', content: 'hello glm failover' }],
      stream: false
    })
  })
  await failureResponse.text()
  assert.notEqual(failureResponse.status, 200, 'GLM 主账号失败且无备用时应返回错误')
  const failureAuthorizations = upstreamHits.slice(start).map((hit) => hit.authorization)
  assert(failureAuthorizations.length > 0, 'GLM 首次错误应至少命中一次主账号')
  assert(failureAuthorizations.every((authorization) => authorization === 'Bearer sk-glm-error-primary'), 'GLM 首次错误重试阶段只能命中主账号')

  repositories.updateAccount(rescueAccountId, {
    status: 'active',
    schedulable: true
  }, access)
  const recoveryStart = upstreamHits.length
  const recoveryResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'glm-4.7-flash',
      messages: [{ role: 'user', content: 'hello glm recovery' }],
      stream: false
    })
  })
  const recoveryText = await recoveryResponse.text()
  assert.equal(recoveryResponse.status, 200, `GLM 主账号失败后应避让并恢复到备用账号，实际 HTTP ${recoveryResponse.status}: ${recoveryText}`)
  assert.match(recoveryText, /glm mock json ok/)
  assert.deepEqual(upstreamHits.slice(recoveryStart).map((hit) => hit.authorization), [
    'Bearer sk-glm-error-rescue'
  ], 'GLM 恢复请求应避开刚失败的主账号并命中备用账号')
}

async function assertGlmChatJson(input: {
  baseUrl: string
  localApiKey: string
  model: string
  expectedPath: string
  expectedAuthorization: string
  expectedContent: string
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/chat/completions?trace=general`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: input.model,
      messages: [{ role: 'user', content: 'hello glm json' }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GLM Chat JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, new RegExp(input.expectedContent))
  const hits = upstreamHits.slice(start)
  assert.equal(hits.length, 1, 'GLM Chat JSON 应只命中一次上游')
  assert.equal(hits[0]?.path, input.expectedPath)
  assert.equal(hits[0]?.rawUrl, `${input.expectedPath}?trace=general`, 'GLM 上游 URL 应保留查询参数且不追加 /v1')
  assert.equal(hits[0]?.authorization, input.expectedAuthorization)
  assert.match(hits[0]?.bodyText ?? '', new RegExp(input.model))
}

async function assertGlmChatSse(input: {
  baseUrl: string
  localApiKey: string
  model: string
  expectedPath: string
  expectedAuthorization: string
  expectedContent: string
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: input.model,
      messages: [{ role: 'user', content: 'hello glm sse' }],
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GLM Chat SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, new RegExp(input.expectedContent))
  const hits = upstreamHits.slice(start)
  assert.equal(hits.length, 1, 'GLM Chat SSE 应只命中一次上游')
  assert.equal(hits[0]?.path, input.expectedPath)
  assert.equal(hits[0]?.rawUrl, input.expectedPath, 'GLM Coding 上游 URL 不应追加 /v1')
  assert.equal(hits[0]?.authorization, input.expectedAuthorization)
  assert.match(hits[0]?.bodyText ?? '', new RegExp(input.model))
}

async function assertGlmRejectsResponses(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'glm-4.7-flash',
      input: 'responses should not reach glm'
    })
  })
  const text = await response.text()
  assert.notEqual(response.status, 200, `GLM 不应承接 Responses，实际返回成功：${text}`)
  assert.equal(upstreamHits.length, start, 'GLM 拒绝 Responses 时不应命中上游')
}

async function assertGlmCodingResponsesBridge(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-standard-session',
        thread_id: 'glm-standard-thread',
        turn_id: 'glm-standard-turn'
      })
    },
    body: JSON.stringify({
      model: 'glm-5.2',
      input: 'responses should reach glm chat bridge',
      stream: true,
      store: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GLM Coding Key 显式 Responses -> Chat 映射应成功，实际返回失败：${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'GLM Responses -> Chat bridge 响应应是 SSE')
  assert.match(text, /response\.completed/, 'GLM Responses -> Chat bridge 应返回 Responses completed 事件')
  assert.match(text, /glm mock sse ok/, 'GLM Chat SSE 文本应转成 Responses 事件')
  assert.equal(upstreamHits.length, start + 1, 'GLM Responses -> Chat bridge 应命中一次上游')
  assert.equal(upstreamHits[start]?.path, '/api/coding/paas/v4/chat/completions')
  assert.equal(upstreamHits[start]?.authorization, 'Bearer sk-glm-coding-upstream')
  const upstreamBody = parseJsonObject(upstreamHits[start]?.bodyText ?? '{}')
  assert.equal(upstreamBody.model, 'glm-5.2')
  assert.equal(upstreamBody.stream, true, 'GLM Responses -> Chat bridge 必须使用上游 Chat SSE')
}

async function assertGlmCodingRejectsResponsesBridge(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-standard-session',
        thread_id: 'glm-standard-thread',
        turn_id: 'glm-standard-turn'
      })
    },
    body: JSON.stringify({
      model: 'glm-5.2',
      input: 'responses should require explicit model mapping',
      stream: true,
      store: false
    })
  })
  const text = await response.text()
  assert.notEqual(response.status, 200, `未配置 Responses -> Chat 映射的 GLM Coding Key 不应承接 Responses，实际返回成功：${text}`)
  assert.equal(upstreamHits.length, start, '未配置 Responses -> Chat 映射时不应命中上游')
}

function createGlmMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const rawUrl = req.url ?? ''
    const path = rawUrl.split('?', 1)[0] ?? rawUrl
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      upstreamHits.push({
        authorization: String(req.headers.authorization ?? ''),
        bodyText,
        method: req.method ?? '',
        path,
        rawUrl
      })
      if (req.method !== 'POST' || !path.endsWith('/chat/completions')) {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: `unexpected path: ${rawUrl}` } }))
        return
      }

      const requestBody = parseJsonObject(bodyText)
      if (String(req.headers.authorization ?? '') === 'Bearer sk-glm-error-primary') {
        res.writeHead(500, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          error: {
            message: 'glm mock primary failed',
            type: 'server_error',
            code: 'glm_mock_primary_failed'
          }
        }))
        return
      }
      if (requestBody.stream === true) {
        res.writeHead(200, {
          'content-type': 'text/event-stream; charset=utf-8',
          'cache-control': 'no-cache'
        })
        res.write('data: {"id":"chatcmpl-glm-mock-sse","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"glm mock reasoning"},"finish_reason":null}]}\n\n')
        res.write('data: {"id":"chatcmpl-glm-mock-sse","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"glm mock sse ok"},"finish_reason":null}]}\n\n')
        if (Array.isArray(requestBody.tools)) {
          res.write('data: {"id":"chatcmpl-glm-mock-sse","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_glm_mock_tool","type":"function","function":{"name":"shell_command","arguments":"{\\"command\\":\\"Get-Date\\"}"}}]},"finish_reason":null}]}\n\n')
        }
        res.write('data: {"id":"chatcmpl-glm-mock-sse","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}\n\n')
        res.end('data: [DONE]\n\n')
        return
      }

      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-glm-mock-json',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: { role: 'assistant', content: 'glm mock json ok' },
            finish_reason: 'stop'
          }
        ],
        usage: {
          prompt_tokens: 3,
          completion_tokens: 4,
          total_tokens: 7
        }
      }))
    })
  })
}

function parseJsonObject(text: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(text) as unknown
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed as Record<string, unknown> : {}
  } catch {
    return {}
  }
}

function listen(server: http.Server): Promise<void> {
  if (server.listening) return Promise.resolve()
  server.listen(0, '127.0.0.1')
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  assert(typeof address === 'object' && address !== null, 'server 未监听端口')
  return { port: address.port }
}

function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server || !server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.close((error) => {
      if (error) rejectPromise(error)
      else resolvePromise()
    })
  })
}
