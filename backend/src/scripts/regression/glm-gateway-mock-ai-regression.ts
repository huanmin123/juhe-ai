import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GLM_CODING_ANTHROPIC_V1_PROFILE_ID,
  GLM_CODING_OPENAI_V1_PROFILE_ID,
  GLM_GENERAL_OPENAI_V1_PROFILE_ID,
  GLM_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import {
  GLM_CODING_CONNECTION_TYPE,
  GLM_GENERAL_CONNECTION_TYPE
} from '../../domain/provider-connection-type.js'
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
      clientCompatibility: 'codex_responses',
      groupName: 'GLM Mock Coding 分组',
      localApiKeyName: 'GLM Mock Coding Key',
      providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-glm-coding-upstream'
    })
    const codingOpenAIStandard = createGlmScenario({
      accountName: 'GLM Mock Coding OpenAI 标准账户',
      baseUrl: `${upstreamOrigin}/api/coding-standard/paas/v4`,
      clientCompatibility: 'openai_standard',
      groupName: 'GLM Mock Coding OpenAI 标准分组',
      localApiKeyName: 'GLM Mock Coding OpenAI 标准 Key',
      providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
      upstreamApiKey: 'sk-glm-coding-standard-upstream'
    })
    const failover = createGlmFailoverScenario(`${upstreamOrigin}/api/paas/v4/`)

    assertGlmCredentialCapabilities(general.groupId)
    assertGlmImportAndExportRoundTrip(general.accountId, coding.accountId, general.groupId, coding.groupId)

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
    await assertGlmCodexResponsesBridge({
      baseUrl,
      localApiKey: coding.localApiKey,
      model: 'glm-5.2',
      expectedPath: '/api/coding/paas/v4/chat/completions',
      expectedAuthorization: 'Bearer sk-glm-coding-upstream',
      expectedContent: 'glm mock sse ok'
    })
    await assertGlmCodexResponsesBridgeRestoresPreviousResponseId({
      baseUrl,
      localApiKey: coding.localApiKey,
      model: 'glm-5.2'
    })
    await assertGlmCodexResponsesBridgeRejectsUnknownPreviousResponseId({
      baseUrl,
      localApiKey: coding.localApiKey,
      model: 'glm-5.2'
    })
    await assertGlmCodexResponsesBridgeGatewaySummaryCompact({
      baseUrl,
      localApiKey: coding.localApiKey,
      model: 'glm-5.2'
    })
    await assertGlmCodexResponsesBridgeFailsOnTruncatedStream({
      baseUrl,
      localApiKey: coding.localApiKey,
      model: 'glm-5.2',
      expectedPath: '/api/coding/paas/v4/chat/completions',
      expectedAuthorization: 'Bearer sk-glm-coding-upstream'
    })
    await assertGlmCodexResponsesBridgeFailsOnErrorEvent({
      baseUrl,
      localApiKey: coding.localApiKey,
      model: 'glm-5.2',
      expectedPath: '/api/coding/paas/v4/chat/completions',
      expectedAuthorization: 'Bearer sk-glm-coding-upstream'
    })
    await assertGlmCodingOpenAIStandardRejectsCodexBridge(baseUrl, codingOpenAIStandard.localApiKey)
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
  assert.equal(glmProvider.defaultProtocolProfileId, GLM_GENERAL_OPENAI_V1_PROFILE_ID, 'GLM 默认档案应是通用 API')
  assert(glmProvider.protocolProfiles.some((profile) => profile.id === GLM_GENERAL_OPENAI_V1_PROFILE_ID), 'GLM provider 应包含通用 OpenAI Chat 档案')
  assert(glmProvider.protocolProfiles.some((profile) => profile.id === GLM_CODING_OPENAI_V1_PROFILE_ID), 'GLM provider 应包含 Coding OpenAI Chat 档案')
  assert(glmProvider.protocolProfiles.some((profile) => profile.id === GLM_CODING_ANTHROPIC_V1_PROFILE_ID), 'GLM provider 应包含 Coding Anthropic Messages 档案')

  const defaultGroups = repositories.listGroups(access).filter((group) => group.providerCode === GLM_PROVIDER_CODE && group.isDefault)
  assert(defaultGroups.some((group) => group.providerProtocolProfileId === GLM_GENERAL_OPENAI_V1_PROFILE_ID), '默认分组应包含 GLM 通用分组')
  assert(defaultGroups.some((group) => group.providerProtocolProfileId === GLM_CODING_OPENAI_V1_PROFILE_ID), '默认分组应包含 GLM Coding 分组')
  assert(defaultGroups.some((group) => group.providerProtocolProfileId === GLM_CODING_ANTHROPIC_V1_PROFILE_ID), '默认分组应包含 GLM Coding Claude Code 分组')
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
  clientCompatibility?: 'openai_standard' | 'codex_responses'
  localApiKeyName: string
  providerProtocolProfileId: string
  upstreamApiKey: string
}): { accountId: string; groupId: string; localApiKey: string } {
  const group = repositories.createGroup({
    name: input.groupName,
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: input.providerProtocolProfileId,
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
    clientCompatibility: input.clientCompatibility,
    groupId: group.id,
    status: 'active',
    priority: 0,
    schedulable: true
  }, access)
  const apiKey = repositories.createApiKeyRecord({
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
    providerProtocolProfileId: GLM_GENERAL_OPENAI_V1_PROFILE_ID,
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
  const apiKey = repositories.createApiKeyRecord({
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

  assert.throws(() => repositories.createAccount({
    providerCode: GLM_PROVIDER_CODE,
    providerProtocolProfileId: GLM_CODING_OPENAI_V1_PROFILE_ID,
    name: 'GLM 错误分组绑定账户',
    type: 'api_key',
    credentials: {
      api_key: 'sk-glm-wrong-profile-group',
      base_url: 'http://127.0.0.1:1/api/coding/paas/v4'
    },
    groupId,
    status: 'disabled',
    schedulable: false
  }, access), /账户分组无效/, 'GLM Coding 账户不应加入通用 GLM 分组')
}

function assertGlmImportAndExportRoundTrip(generalAccountId: string, codingAccountId: string, generalGroupId: string, codingGroupId: string): void {
  const preview = accountImportService.previewAccountImport({
    type: accountImportService.accountImportProtocolType,
    version: accountImportService.accountImportProtocolVersion,
    accounts: [
      {
        ref: 'glm-preview-general',
        name: 'GLM 导入预览通用账号',
        providerCode: GLM_PROVIDER_CODE,
        connectionType: GLM_GENERAL_CONNECTION_TYPE,
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
        connectionType: GLM_CODING_CONNECTION_TYPE,
        clientCompatibility: 'codex_responses',
        type: 'api_key',
        status: 'disabled',
        groupId: codingGroupId,
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
  assert.equal(general?.connectionType, GLM_GENERAL_CONNECTION_TYPE, '导出通用 GLM 账号应保留 general_api_key')
  assert.equal(coding?.connectionType, GLM_CODING_CONNECTION_TYPE, '导出 GLM Coding 账号应保留 coding_api_key')
  assert.equal(general?.clientCompatibility, 'openai_standard', '导出通用 GLM 账号应保留 OpenAI 标准客户端兼容')
  assert.equal(coding?.clientCompatibility, 'codex_responses', '导出 GLM Coding Codex 账号应保留 Codex Responses 客户端兼容')
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

async function assertGlmCodexResponsesBridge(input: {
  baseUrl: string
  localApiKey: string
  model: string
  expectedPath: string
  expectedAuthorization: string
  expectedContent: string
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/responses?trace=codex-bridge`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-bridge-session',
        thread_id: 'glm-bridge-thread',
        turn_id: 'glm-bridge-turn'
      })
    },
    body: JSON.stringify({
      model: input.model,
      instructions: '只回复 OK',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'previous glm tool task' }]
        },
        {
          type: 'function_call',
          call_id: 'call_glm_history_a',
          name: 'shell_command',
          arguments: '{"command":"git log -1"}'
        },
        {
          type: 'function_call',
          call_id: 'call_glm_history_b',
          name: 'shell_command',
          arguments: '{"command":"git status --short"}'
        },
        {
          type: 'message',
          role: 'tool',
          content: [{ type: 'input_text', text: 'glm bare tool message must not reach upstream' }]
        },
        {
          type: 'function_call_output',
          call_id: 'call_glm_history_a',
          output: 'glm commit output'
        },
        {
          type: 'message',
          role: 'developer',
          content: [{ type: 'input_text', text: 'GLM approved command prefix saved' }]
        },
        {
          type: 'function_call_output',
          call_id: 'call_glm_history_b',
          output: [{ type: 'output_text', text: 'glm status output' }]
        },
        {
          type: 'function_call',
          call_id: 'call_glm_unanswered',
          name: 'shell_command',
          arguments: '{"command":"dangling"}'
        },
        {
          type: 'function_call_output',
          call_id: 'call_glm_orphan',
          output: 'glm orphan output'
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'hello glm codex bridge' }]
        }
      ],
      stream: true,
      store: false,
      tools: [
        {
          type: 'function',
          name: 'shell_command',
          description: 'Runs a PowerShell command.',
          parameters: {
            type: 'object',
            properties: {
              command: { type: 'string' }
            },
            required: ['command'],
            additionalProperties: false
          }
        },
        {
          type: 'web_search',
          external_web_access: false
        }
      ],
      tool_choice: 'auto',
      parallel_tool_calls: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GLM Coding Codex Responses 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.created/, '桥接响应应输出 Responses created 事件')
  assert.match(text, /event: response\.output_text\.delta/, '桥接响应应输出 Responses 文本 delta 事件')
  assert.match(text, /event: response\.completed/, '桥接响应应输出 Responses completed 事件')
  assert.match(text, new RegExp(input.expectedContent))
  assert.match(text, /"type":"function_call"/, '桥接响应应把 Chat tool_calls 转成 Codex 可消费的 FunctionCall output item')
  assert(!text.includes('response.function_call_arguments.delta'), 'Codex 当前不消费 function_call_arguments.delta，桥接不应依赖该事件')
  assert(!text.includes('chat.completion.chunk'), '桥接后的下游响应不应泄漏 Chat Completions SSE 原始对象')

  const hits = upstreamHits.slice(start)
  assert.equal(hits.length, 1, 'GLM Coding Codex 桥接应只命中一次上游')
  assert.equal(hits[0]?.path, input.expectedPath)
  assert.equal(hits[0]?.rawUrl, `${input.expectedPath}?trace=codex-bridge`, 'GLM Coding Codex 桥接应把 /responses 改写到 /chat/completions 并保留查询参数')
  assert.equal(hits[0]?.authorization, input.expectedAuthorization)
  const body = parseJsonObject(hits[0]?.bodyText ?? '')
  assert.equal(body.model, input.model)
  assert.equal(body.stream, true, 'GLM Coding Codex 桥接上游请求必须使用 Chat SSE')
  assert(Array.isArray(body.messages), 'GLM Coding Codex 桥接应把 Responses input 转成 Chat messages')
  assert.equal((body.messages as unknown[]).some((message) => JSON.stringify(message).includes('hello glm codex bridge')), true, 'Chat messages 应包含用户输入')
  const chatMessages = body.messages as Array<Record<string, unknown>>
  assertValidChatToolMessageSequence(chatMessages, 'GLM Coding Codex bridge')
  const historyAssistantIndex = chatMessages.findIndex((message) => Array.isArray(message.tool_calls)
    && (message.tool_calls as Array<{ id?: string }>).some((toolCall) => toolCall.id === 'call_glm_history_a'))
  assert(historyAssistantIndex >= 0, 'GLM Coding Codex 桥接应把历史 function_call 转成 assistant tool_calls')
  const historyAssistant = chatMessages[historyAssistantIndex] as { reasoning_content?: string; tool_calls?: Array<{ id?: string }> }
  assert.equal(historyAssistant.reasoning_content, undefined, 'GLM Coding Codex 桥接不应透传 DeepSeek 专用 reasoning_content 字段')
  assert.equal(historyAssistant.tool_calls?.length, 2, 'GLM Coding Codex 桥接应把连续历史 function_call 合并到一个 assistant tool_calls 消息')
  assert.equal(chatMessages[historyAssistantIndex + 1]?.role, 'tool', '历史 assistant tool_calls 后应紧跟第一个 tool 输出')
  assert.equal(chatMessages[historyAssistantIndex + 1]?.tool_call_id, 'call_glm_history_a')
  assert.equal(chatMessages[historyAssistantIndex + 1]?.content, 'glm commit output')
  assert.equal(chatMessages[historyAssistantIndex + 2]?.role, 'tool', '历史 assistant tool_calls 后应紧跟第二个 tool 输出')
  assert.equal(chatMessages[historyAssistantIndex + 2]?.tool_call_id, 'call_glm_history_b')
  assert.equal(chatMessages[historyAssistantIndex + 2]?.content, 'glm status output')
  const deferredSystemIndex = chatMessages.findIndex((message, index) => index > historyAssistantIndex
    && message.role === 'system'
    && JSON.stringify(message).includes('GLM approved command prefix saved'))
  assert(deferredSystemIndex > historyAssistantIndex + 2, '夹在 tool_call 和 tool output 中间的 developer 消息应延后到 tool 输出之后，避免破坏 Chat tool 消息不变量')
  assert(!JSON.stringify(chatMessages).includes('glm bare tool message must not reach upstream'), 'Responses message role=tool 不能生成裸 Chat tool 消息')
  assert(!JSON.stringify(chatMessages).includes('call_glm_unanswered'), '未收到输出的 dangling function_call 不应透传给 Chat 上游')
  assert(!JSON.stringify(chatMessages).includes('call_glm_orphan'), '找不到对应 function_call 的 orphan tool output 不应透传给 Chat 上游')
  assert(Array.isArray(body.tools), 'GLM Coding Codex 桥接应把 function tools 转成 Chat tools')
  assert.equal((body.tools as unknown[]).length, 1, 'GLM Coding Codex 桥接首版只透传 function tools，不透传 web_search/namespace')
  assert.match(JSON.stringify(body.tools), /shell_command/)
}

async function assertGlmCodexResponsesBridgeRestoresPreviousResponseId(input: {
  baseUrl: string
  localApiKey: string
  model: string
}): Promise<void> {
  const start = upstreamHits.length
  const firstResponse = await fetch(`${input.baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-bridge-state-session',
        thread_id: 'glm-bridge-state-thread',
        turn_id: 'glm-bridge-state-turn-1'
      })
    },
    body: JSON.stringify({
      model: input.model,
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'remember first glm codex context' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const firstText = await firstResponse.text()
  assert.equal(firstResponse.status, 200, `GLM Coding Codex bridge 首轮应成功，实际 HTTP ${firstResponse.status}: ${firstText}`)
  const firstResponseId = responsesCompletedPayload(firstText)?.id ?? ''
  assert.match(firstResponseId, /^resp_glm_bridge_/, 'GLM Coding Codex bridge 首轮应生成可续接 response id')

  const secondResponse = await fetch(`${input.baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-bridge-state-session',
        thread_id: 'glm-bridge-state-thread',
        turn_id: 'glm-bridge-state-turn-2'
      })
    },
    body: JSON.stringify({
      model: input.model,
      previous_response_id: firstResponseId,
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'continue second glm codex context' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const secondText = await secondResponse.text()
  assert.equal(secondResponse.status, 200, `GLM Coding Codex bridge previous_response_id 续链应成功，实际 HTTP ${secondResponse.status}: ${secondText}`)
  assert.match(secondText, new RegExp(`"previous_response_id":"${escapeRegExp(firstResponseId)}"`), '续链响应应回显 previous_response_id')
  const hits = upstreamHits.slice(start)
  assert.equal(hits.length, 2, 'GLM Coding Codex bridge 续链应每轮各命中一次上游')
  const secondBody = parseJsonObject(hits[1]?.bodyText ?? '{}')
  assert.equal(secondBody.previous_response_id, undefined, 'previous_response_id 只能在网关内消费，不能发给 Chat 上游')
  const messagesText = JSON.stringify(secondBody.messages)
  assert.match(messagesText, /remember first glm codex context/, '续链 Chat messages 应恢复首轮用户输入')
  assert.match(messagesText, /glm mock sse ok/, '续链 Chat messages 应恢复首轮 assistant 输出')
  assert.match(messagesText, /continue second glm codex context/, '续链 Chat messages 应追加本轮用户输入')
}

async function assertGlmCodexResponsesBridgeRejectsUnknownPreviousResponseId(input: {
  baseUrl: string
  localApiKey: string
  model: string
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-bridge-previous-session',
        thread_id: 'glm-bridge-previous-thread',
        turn_id: 'glm-bridge-previous-turn'
      })
    },
    body: JSON.stringify({
      model: input.model,
      previous_response_id: 'resp_previous_not_available_in_chat_bridge',
      input: [
        {
          type: 'function_call_output',
          call_id: 'call_previous_tool',
          output: 'tool output should not be silently dropped'
        }
      ],
      stream: true,
      store: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 404, `GLM Coding Codex bridge 未知 previous_response_id 应受控拒绝，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /codex_bridge_previous_response_not_found/, 'previous_response_id 不存在响应应带稳定错误码')
  assert.equal(upstreamHits.length, start, 'GLM Coding Codex bridge 未知 previous_response_id 受控拒绝时不应命中上游')
}

async function assertGlmCodexResponsesBridgeGatewaySummaryCompact(input: {
  baseUrl: string
  localApiKey: string
  model: string
}): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/responses/compact`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-bridge-compact-session',
        thread_id: 'glm-bridge-compact-thread',
        turn_id: 'glm-bridge-compact-turn'
      })
    },
    body: JSON.stringify({
      model: input.model,
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'compact this history' }]
        }
      ],
      store: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GLM Coding Codex bridge /responses/compact 应返回网关摘要，实际 HTTP ${response.status}: ${text}`)
  const payload = JSON.parse(text) as { output?: Array<Record<string, unknown>> }
  const compactItem = payload.output?.[0]
  assert.equal(compactItem?.type, 'compaction_summary', '/responses/compact 应返回 Codex 可消费的 compaction_summary item')
  assert.match(String(compactItem?.encrypted_content ?? ''), /^juhecmp\.v2\.cmp_[^.]+\.[a-f0-9]{64}$/i, 'compact summary 应使用网关 snapshot reference envelope')
  let hits = upstreamHits.slice(start)
  assert.equal(hits.length, 1, 'GLM Coding Codex bridge /responses/compact 应通过内部 Chat Completions 摘要请求命中一次上游')
  assert.equal(hits[0]?.path, '/api/coding/paas/v4/chat/completions', 'compact 内部请求必须走 Chat Completions 路径')
  const compactUpstreamBody = parseJsonObject(hits[0]?.bodyText ?? '{}')
  assert.equal(compactUpstreamBody.stream, false, 'compact 内部摘要请求必须是非流式 Chat 请求')
  assert.match(JSON.stringify(compactUpstreamBody.messages), /compact this history/, 'compact 内部摘要请求应包含待压缩上下文')

  const bridgeResponse = await fetch(`${input.baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-bridge-compact-followup-session',
        thread_id: 'glm-bridge-compact-followup-thread',
        turn_id: 'glm-bridge-compact-followup-turn'
      })
    },
    body: JSON.stringify({
      model: input.model,
      input: [
        compactItem,
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'continue after compact summary' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const bridgeText = await bridgeResponse.text()
  assert.equal(bridgeResponse.status, 200, `GLM Coding Codex bridge 应能消费 compaction_summary，实际 HTTP ${bridgeResponse.status}: ${bridgeText}`)
  hits = upstreamHits.slice(start)
  assert.equal(hits.length, 2, 'GLM compaction_summary 后续请求应再命中一次上游')
  assert.match(hits[1]?.bodyText ?? '', /glm mock json ok/, '后续 Chat messages 应包含 compact 摘要文本')
  assert.match(hits[1]?.bodyText ?? '', /continue after compact summary/, '后续 Chat messages 应包含 compact 后的新输入')
}

function assertValidChatToolMessageSequence(messages: Array<Record<string, unknown>>, label: string): void {
  let expectedToolCallIds: string[] = []
  for (const [index, message] of messages.entries()) {
    if (expectedToolCallIds.length > 0) {
      assert.equal(message.role, 'tool', `${label} assistant tool_calls 后第 ${index} 条消息必须是 tool 输出`)
      assert.equal(message.tool_call_id, expectedToolCallIds.shift(), `${label} tool 输出顺序必须匹配 assistant tool_calls 顺序`)
      continue
    }
    if (message.role === 'tool') {
      assert.fail(`${label} 不应生成没有前序 assistant tool_calls 的裸 tool 消息`)
    }
    if (Array.isArray(message.tool_calls)) {
      expectedToolCallIds = (message.tool_calls as Array<{ id?: unknown }>).map((toolCall) => {
        assert.equal(typeof toolCall.id, 'string', `${label} assistant tool_calls 每项都必须有字符串 id`)
        return toolCall.id as string
      })
      assert(expectedToolCallIds.length > 0, `${label} assistant tool_calls 不能为空`)
    }
  }
  assert.equal(expectedToolCallIds.length, 0, `${label} assistant tool_calls 后缺少对应 tool 输出`)
}

function responsesCompletedPayload(text: string): { id?: string; usage?: Record<string, unknown> } | undefined {
  for (const block of text.split(/\r?\n\r?\n/)) {
    if (!/^event:\s*response\.completed$/m.test(block)) continue
    const dataText = block
      .split(/\r?\n/)
      .filter((line) => line.startsWith('data:'))
      .map((line) => line.slice(5).trimStart())
      .join('\n')
    if (!dataText) continue
    const parsed = JSON.parse(dataText) as { response?: { id?: string; usage?: Record<string, unknown> } }
    return { id: parsed.response?.id, usage: parsed.response?.usage }
  }
  return undefined
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

async function assertGlmCodexResponsesBridgeFailsOnTruncatedStream(input: {
  baseUrl: string
  localApiKey: string
  model: string
  expectedPath: string
  expectedAuthorization: string
}): Promise<void> {
  const text = await requestGlmCodexBridgeFailure(input, 'hello glm codex bridge truncated')
  assert.match(text, /event: response\.failed/, '上游 Chat SSE 截断时桥接应输出 Responses failed 事件')
  assert.match(text, /upstream_stream_interrupted/, '截断错误应带稳定机器错误码')
  assert(!text.includes('event: response.completed'), '上游 Chat SSE 截断时桥接不应输出 completed 事件')
}

async function assertGlmCodexResponsesBridgeFailsOnErrorEvent(input: {
  baseUrl: string
  localApiKey: string
  model: string
  expectedPath: string
  expectedAuthorization: string
}): Promise<void> {
  const text = await requestGlmCodexBridgeFailure(input, 'hello glm codex bridge error event')
  assert.match(text, /event: response\.failed/, '上游 Chat SSE error 事件时桥接应输出 Responses failed 事件')
  assert.match(text, /upstream_retryable_error/, '上游 error 事件应归一为可重试上游错误码')
  assert.match(text, /上游流式响应在输出前失败，请重试/, '上游 error 事件应向 Codex 客户端返回统一可重试文案')
  assert(!text.includes('glm mock stream error'), 'GLM 上游 error 原文不应泄露到 Codex Responses failed payload')
  assert(!text.includes('event: response.completed'), '上游 Chat SSE error 事件时桥接不应输出 completed 事件')
}

async function requestGlmCodexBridgeFailure(input: {
  baseUrl: string
  localApiKey: string
  model: string
  expectedPath: string
  expectedAuthorization: string
}, marker: string): Promise<string> {
  const start = upstreamHits.length
  const response = await fetch(`${input.baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${input.localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'glm-bridge-failure-session',
        thread_id: 'glm-bridge-failure-thread',
        turn_id: `glm-bridge-failure-${marker.replace(/\W+/g, '-')}`
      })
    },
    body: JSON.stringify({
      model: input.model,
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: marker }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `GLM Coding Codex bridge 失败事件仍应保持 SSE 传输，实际 HTTP ${response.status}: ${text}`)
  const hits = upstreamHits.slice(start)
  assert.equal(hits.length, 1, 'GLM Coding Codex bridge 失败场景应只命中一次上游')
  assert.equal(hits[0]?.path, input.expectedPath)
  assert.equal(hits[0]?.authorization, input.expectedAuthorization)
  assert.match(hits[0]?.bodyText ?? '', new RegExp(marker))
  return text
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

async function assertGlmCodingOpenAIStandardRejectsCodexBridge(baseUrl: string, localApiKey: string): Promise<void> {
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
      input: 'codex bridge should require account clientCompatibility',
      stream: true,
      store: false
    })
  })
  const text = await response.text()
  assert.notEqual(response.status, 200, `GLM Coding OpenAI 标准账号不应承接 Codex bridge，实际返回成功：${text}`)
  assert.equal(upstreamHits.length, start, 'GLM Coding OpenAI 标准账号拒绝 Codex bridge 时不应命中上游')
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
        if (bodyText.includes('hello glm codex bridge truncated')) {
          res.end('data: {"id":"chatcmpl-glm-mock-sse","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial glm bridge"},"finish_reason":null}]}\n\n')
          return
        }
        if (bodyText.includes('hello glm codex bridge error event')) {
          res.end('event: error\ndata: {"error":{"message":"glm mock stream error","code":"glm_mock_stream_error"}}\n\n')
          return
        }
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
