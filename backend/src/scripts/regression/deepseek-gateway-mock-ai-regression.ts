import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  DEEPSEEK_ANTHROPIC_V1_PROFILE_ID,
  DEEPSEEK_OPENAI_V1_PROFILE_ID,
  DEEPSEEK_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { parseOpenAISseEventText } from '../../modules/gateway/protocols/openai-v1/stream-events.js'
import {
  extractOpenAIJsonSemanticFrames,
  extractOpenAISseSemanticFrames
} from '../../modules/gateway/protocols/openai-v1/response-semantics.js'
import { parseOpenAIUsageFromJsonBuffer } from '../../modules/gateway/protocols/openai-v1/usage.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import {
  accountSupportsGatewayRequest,
  buildGatewayUpstreamUrlsForAccount,
  providerDriverForAccount
} from '../../modules/providers/drivers/registry.js'
import { logger } from '../../shared/logger.js'

interface DeepSeekUpstreamHit {
  path: string
  rawUrl: string
  method: string
  authorization: string
  contentLength: string
  transferEncoding: string
  bodyText: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-deepseek-gateway-mock-ai-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'deepseek-gateway-mock-ai.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.codexContextStateShardCount = 4
runtimeConfig.secret = 'deepseek-gateway-mock-ai-secret'
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
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: DeepSeekUpstreamHit[] = []

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
  auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createDeepSeekMockUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}`

    assertDeepSeekSeeds()

    const group = repositories.createGroup({
      name: 'DeepSeek Mock AI 回归分组',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    const account = repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Mock AI 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-deepseek-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      supportedModels: ['deepseek-ai-v4-flash', 'deepseek-ai-v4-pro'],
      modelMappings: [
        {
          sourceModel: 'deepseek-ai-v4-flash',
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: 'deepseek-ai-v4-pro',
          upstreamEndpointFamily: 'chat_completions',
          enabled: true
        }
      ]
    }, access)
    assert.equal(account.providerCode, DEEPSEEK_PROVIDER_CODE)
    assert.equal(account.providerProtocolProfileId, DEEPSEEK_OPENAI_V1_PROFILE_ID)
    assert.deepEqual(account.credentials.supported_endpoint_modes, ['chat_json', 'chat_sse'])
    assertDeepSeekDispatchCapability(group.id, account.id)

    const bodyInterruptedGroup = repositories.createGroup({
      name: 'DeepSeek Mock AI JSON 正文中断重试分组',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Mock AI JSON 正文中断主账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-deepseek-interrupt-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: bodyInterruptedGroup.id,
      status: 'active',
      schedulable: true,
      supportedModels: ['deepseek-ai-v4-flash']
    }, access)
    repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Mock AI JSON 正文中断备用账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-deepseek-interrupt-rescue-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: bodyInterruptedGroup.id,
      status: 'active',
      schedulable: true,
      priority: 10,
      supportedModels: ['deepseek-ai-v4-flash']
    }, access)

    const retryGroup = repositories.createGroup({
      name: 'DeepSeek Mock AI 协议失败重试分组',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Mock AI 协议失败主账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-deepseek-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: retryGroup.id,
      status: 'active',
      schedulable: true,
      supportedModels: ['deepseek-ai-v4-flash', 'deepseek-ai-v4-pro'],
      modelMappings: [
        {
          sourceModel: 'deepseek-ai-v4-flash',
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: 'deepseek-ai-v4-pro',
          upstreamEndpointFamily: 'chat_completions',
          enabled: true
        }
      ]
    }, access)
    repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Mock AI 协议失败备用账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-deepseek-rescue-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: retryGroup.id,
      status: 'active',
      schedulable: true,
      priority: 10,
      supportedModels: ['deepseek-ai-v4-flash', 'deepseek-ai-v4-pro'],
      modelMappings: [
        {
          sourceModel: 'deepseek-ai-v4-flash',
          sourceEndpointFamily: 'chat_completions',
          upstreamModel: 'deepseek-ai-v4-pro',
          upstreamEndpointFamily: 'chat_completions',
          enabled: true
        }
      ]
    }, access)

    const allBadGroup = repositories.createGroup({
      name: 'DeepSeek Mock AI 协议失败耗尽分组',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    for (const item of [
      { name: 'DeepSeek Mock AI 协议失败耗尽账户 A', apiKey: 'sk-deepseek-allbad-a', priority: 0 },
      { name: 'DeepSeek Mock AI 协议失败耗尽账户 B', apiKey: 'sk-deepseek-allbad-b', priority: 10 }
    ]) {
      repositories.createAccount({
        providerCode: DEEPSEEK_PROVIDER_CODE,
        providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
        name: item.name,
        type: 'api_key',
        credentials: {
          api_key: item.apiKey,
          base_url: upstreamBaseUrl
        },
        groupId: allBadGroup.id,
        status: 'active',
        schedulable: true,
        priority: item.priority,
        supportedModels: ['deepseek-ai-v4-flash']
      }, access)
    }

    const codexBridgeGroup = repositories.createGroup({
      name: 'DeepSeek Codex bridge Mock AI 回归分组',
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      enabled: true
    }, access)
    const codexBridgeAccount = repositories.createAccount({
      providerCode: DEEPSEEK_PROVIDER_CODE,
      providerProtocolProfileId: DEEPSEEK_OPENAI_V1_PROFILE_ID,
      name: 'DeepSeek Codex bridge Mock AI 回归账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-deepseek-codex-upstream',
        base_url: upstreamBaseUrl
      },
      groupId: codexBridgeGroup.id,
      status: 'active',
      schedulable: true,
      supportedModels: ['deepseek-ai-v4-flash'],
      modelMappings: [{
        sourceModel: 'deepseek-v4-flash',
        sourceEndpointFamily: 'responses',
        upstreamModel: 'deepseek-ai-v4-flash',
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }, {
        sourceModel: 'deepseek-v4-flash',
        sourceEndpointFamily: 'chat_completions',
        upstreamModel: 'deepseek-ai-v4-flash',
        upstreamEndpointFamily: 'chat_completions',
        enabled: true
      }]
    }, access)
    assert.deepEqual(codexBridgeAccount.credentials.supported_endpoint_modes, ['chat_json', 'chat_sse'])
    assertDeepSeekCodexDispatchCapability(codexBridgeGroup.id, codexBridgeAccount.id)

    const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'DeepSeek Mock AI 回归 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(apiKey.key, '回归 API Key 未返回明文密钥')
    const bodyInterruptedApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'DeepSeek Mock AI JSON 正文中断重试 Key',
      groupBindings: [{ groupId: bodyInterruptedGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(bodyInterruptedApiKey.key, 'JSON 正文中断重试回归 API Key 未返回明文密钥')
    const retryApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'DeepSeek Mock AI 协议失败重试 Key',
      groupBindings: [{ groupId: retryGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(retryApiKey.key, '协议失败重试回归 API Key 未返回明文密钥')
    const allBadApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'DeepSeek Mock AI 协议失败耗尽 Key',
      groupBindings: [{ groupId: allBadGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(allBadApiKey.key, '协议失败耗尽回归 API Key 未返回明文密钥')
    const codexBridgeApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'DeepSeek Codex bridge Mock AI 回归 Key',
      groupBindings: [{ groupId: codexBridgeGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(codexBridgeApiKey.key, 'Codex bridge 回归 API Key 未返回明文密钥')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    await assertDeepSeekModels(baseUrl, apiKey.key)
    await assertDeepSeekChatJson(baseUrl, apiKey.key)
    await assertDeepSeekChatJsonBufferedBodyInterruptionRetriesNextAccount(baseUrl, bodyInterruptedApiKey.key)
    await assertDeepSeekInvalidChatJsonChoicesRetriesNextAccount(baseUrl, retryApiKey.key)
    await assertDeepSeekInvalidChatJsonChoicesBecomesGatewayError(baseUrl, allBadApiKey.key)
    await assertDeepSeekChatSse(baseUrl, apiKey.key)
    await assertDeepSeekChatSsePreCommitFailureUsesHttpError(baseUrl, apiKey.key)
    await assertDeepSeekRejectsResponses(baseUrl, apiKey.key)
    await assertDeepSeekCodexResponsesBridge(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeRejectsUnsupportedHostedTool(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeRestoresPreviousResponseId(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeRejectsUnknownPreviousResponseId(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeGatewaySummaryCompact(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeStringUsage(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeFallbackUsage(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeFailsOnTruncatedStream(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeFailsOnErrorEvent(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekCodexResponsesBridgeFailsOnInsufficientResourceFinishReason(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekExplicitResponsesBridgeAllowsStandardClient(baseUrl, codexBridgeApiKey.key)
    await assertDeepSeekRejectsNonChatRoutes(baseUrl, apiKey.key)
    assertDeepSeekSemanticParsing()

    console.log('deepseek gateway mock ai regression passed')
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

function assertDeepSeekSeeds(): void {
  const deepSeekProvider = repositories.listProviders().find((provider) => provider.code === DEEPSEEK_PROVIDER_CODE)
  assert(deepSeekProvider, '默认 provider seed 应包含 deepseek')
  assert.equal(deepSeekProvider.defaultProtocolProfileId, DEEPSEEK_OPENAI_V1_PROFILE_ID, 'DeepSeek 默认档案应保持 OpenAI-compatible Chat')
  assert(deepSeekProvider.protocolProfiles.some((profile) => profile.id === DEEPSEEK_OPENAI_V1_PROFILE_ID), 'DeepSeek provider 应包含 OpenAI Chat 档案')
  assert(deepSeekProvider.protocolProfiles.some((profile) => profile.id === DEEPSEEK_ANTHROPIC_V1_PROFILE_ID), 'DeepSeek provider 应包含 Anthropic Messages 档案')

  const defaultGroups = repositories.listGroups(access).filter((group) => group.providerCode === DEEPSEEK_PROVIDER_CODE && group.isDefault)
  assert.equal(defaultGroups.length, 1, 'DeepSeek 默认分组应只按供应商创建一个')
  assert.equal(defaultGroups[0]?.name, '默认 DeepSeek 分组', 'DeepSeek 默认分组应使用供应商级名称')
  assert.equal(defaultGroups[0]?.providerProtocolProfileId, DEEPSEEK_OPENAI_V1_PROFILE_ID, 'DeepSeek 默认分组只保留默认协议档案作为元数据')
}

async function assertDeepSeekModels(baseUrl: string, localApiKey: string): Promise<void> {
  const response = await fetch(`${baseUrl}/v1/models`, {
    headers: {
      authorization: `Bearer ${localApiKey}`
    }
  })
  const text = await response.text()
  assert.equal(response.status, 200, `DeepSeek 本地模型目录应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { data?: Array<{ id?: string }> }
  const models = new Set((body.data ?? []).map((item) => item.id))
  assert(models.has('deepseek-ai-v4-flash'), 'DeepSeek 本地模型目录应包含 deepseek-ai-v4-flash')
  assert(models.has('deepseek-ai-v4-pro'), 'DeepSeek 本地模型目录应包含 deepseek-ai-v4-pro')
}

async function assertDeepSeekChatJson(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-flash',
      messages: [{ role: 'user', content: 'hello deepseek json' }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `DeepSeek Chat JSON 应成功，实际 HTTP ${response.status}: ${text}`)
  const body = JSON.parse(text) as { choices?: Array<{ message?: { content?: string; reasoning_content?: string } }> }
  assert.equal(body.choices?.[0]?.message?.content, 'deepseek json ok')
  assert.equal(body.choices?.[0]?.message?.reasoning_content, 'deepseek json reasoning')
  assert.equal(upstreamHits.length, 1, 'DeepSeek Chat JSON 应命中一次 mock 上游')
  const hit = upstreamHits[0]
  assert.equal(hit.path, '/v1/chat/completions')
  assert.equal(hit.method, 'POST')
  assert.equal(hit.authorization, 'Bearer sk-deepseek-upstream')
  assert(Number(hit.contentLength) > 0, 'DeepSeek 上游请求应携带 Content-Length')
  assert.equal(hit.transferEncoding, '', 'DeepSeek 上游请求不应使用 chunked transfer-encoding')
  const upstreamBody = JSON.parse(hit.bodyText) as { model?: string }
  assert.equal(upstreamBody.model, 'deepseek-ai-v4-pro', 'DeepSeek 账户模型映射应改写上游模型')

  const usage = parseOpenAIUsageFromJsonBuffer(Buffer.from(text, 'utf8'))
  assert.equal(usage.inputTokens, 1000)
  assert.equal(usage.outputTokens, 80)
  assert.equal(usage.cacheReadTokens, 640)
}

async function assertDeepSeekChatJsonBufferedBodyInterruptionRetriesNextAccount(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-flash',
      messages: [{ role: 'user', content: 'hello deepseek json body interrupted' }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `DeepSeek Chat JSON 检查窗口内上游正文中断应先服务端换号重试，实际 HTTP ${response.status}: ${text}`)
  assert.equal(JSON.parse(text).choices?.[0]?.message?.content, 'deepseek json ok')
  assert.equal(upstreamHits.length, 2, `DeepSeek JSON 正文中断场景应先命中断连账号，再命中备用账号，实际命中：${JSON.stringify(upstreamHits.map((hit) => ({
    authorization: hit.authorization,
    body: hit.bodyText.slice(0, 160)
  })))}`
  )
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-deepseek-interrupt-upstream')
  assert.equal(upstreamHits[1]?.authorization, 'Bearer sk-deepseek-interrupt-rescue-upstream')
}

async function assertDeepSeekInvalidChatJsonChoicesRetriesNextAccount(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-flash',
      messages: [{ role: 'user', content: 'hello deepseek invalid choices' }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `DeepSeek Chat JSON 上游 choices:null 应先服务端换号重试，实际 HTTP ${response.status}: ${text}`)
  assert.equal(JSON.parse(text).choices?.[0]?.message?.content, 'deepseek json ok')
  assert.equal(upstreamHits.length, 2, 'DeepSeek invalid choices 场景应先命中坏账号，再命中备用账号')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-deepseek-upstream')
  assert.equal(upstreamHits[1]?.authorization, 'Bearer sk-deepseek-rescue-upstream')

  upstreamHits.length = 0
  const secondResponse = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-flash',
      messages: [{ role: 'user', content: 'hello deepseek invalid choices' }],
      stream: false
    })
  })
  const secondText = await secondResponse.text()
  assert.equal(secondResponse.status, 200, `DeepSeek Chat JSON 协议失败账号进入短避让后应直接命中备用账号，实际 HTTP ${secondResponse.status}: ${secondText}`)
  assert.equal(JSON.parse(secondText).choices?.[0]?.message?.content, 'deepseek json ok')
  assert.equal(upstreamHits.length, 1, 'DeepSeek invalid choices 二次请求应避开已短暂屏蔽的坏账号')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-deepseek-rescue-upstream')
}

async function assertDeepSeekInvalidChatJsonChoicesBecomesGatewayError(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-flash',
      messages: [{ role: 'user', content: 'hello deepseek invalid choices all bad' }],
      stream: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 502, `DeepSeek Chat JSON 协议失败账号耗尽后应转成网关错误，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /upstream_protocol_error/, '网关错误应带通用协议结构错误码')
  assert.doesNotMatch(text, /"choices"\s*:\s*null/, '网关不应把上游 choices:null 原样暴露给下游客户端')
  assert.equal(upstreamHits.length, 2, 'DeepSeek invalid choices 耗尽场景应尝试同组全部候选账号')
}

async function assertDeepSeekChatSse(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-pro',
      messages: [{ role: 'user', content: 'hello deepseek sse' }],
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `DeepSeek Chat SSE 应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /deepseek sse reasoning/)
  assert.match(text, /deepseek sse ok/)
  assert.match(text, /data: \[DONE\]/)
  assert.equal(upstreamHits.length, 1, 'DeepSeek Chat SSE 应命中一次 mock 上游')
  assert.equal(upstreamHits[0]?.path, '/v1/chat/completions')
}

async function assertDeepSeekChatSsePreCommitFailureUsesHttpError(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/chat/completions`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-pro',
      messages: [{ role: 'user', content: 'hello deepseek precommit stream failure' }],
      stream: true
    })
  })
  const text = await response.text()
  assert.equal(response.status, 503, `DeepSeek Chat SSE 建流前失败应收口为 HTTP 503，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /service_unavailable|stream_server_retry_exhausted/, 'DeepSeek Chat SSE 建流前失败应返回统一网关错误 payload')
  assert(!text.includes('response.failed'), '普通 Chat SSE 客户端不应收到 Responses SSE failed 事件')
  assert.equal(upstreamHits.length, 1, 'DeepSeek Chat SSE 建流前失败应只命中一次上游')
  assert.equal(upstreamHits[0]?.path, '/v1/chat/completions')
}

async function assertDeepSeekRejectsResponses(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json'
    },
    body: JSON.stringify({
      model: 'deepseek-ai-v4-flash',
      input: 'must not reach upstream',
      stream: false
    })
  })
  const text = await response.text()
  assert.notEqual(response.status, 200, `DeepSeek 不应承接 Responses，实际响应不应成功：${text}`)
  assert.equal(upstreamHits.length, 0, 'DeepSeek Responses 请求不应命中上游')
}

async function assertDeepSeekCodexResponsesBridge(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses?trace=deepseek-codex-bridge`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-session',
        thread_id: 'deepseek-bridge-thread',
        turn_id: 'deepseek-bridge-turn'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
      instructions: '只回复 OK',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'previous deepseek tool task' }]
        },
        {
          type: 'reasoning',
          summary: [{ type: 'summary_text', text: 'inspect repo before tools' }]
        },
        {
          type: 'function_call',
          call_id: 'call_deepseek_history_a',
          name: 'shell_command',
          arguments: '{"command":"git log -1"}'
        },
        {
          type: 'function_call',
          call_id: 'call_deepseek_history_b',
          name: 'shell_command',
          arguments: '{"command":"git status --short"}'
        },
        {
          type: 'message',
          role: 'tool',
          content: [{ type: 'input_text', text: 'bare tool message must not reach upstream' }]
        },
        {
          type: 'function_call_output',
          call_id: 'call_deepseek_history_a',
          output: 'commit output'
        },
        {
          type: 'message',
          role: 'developer',
          content: [{ type: 'input_text', text: 'Approved command prefix saved' }]
        },
        {
          type: 'function_call_output',
          call_id: 'call_deepseek_history_b',
          output: [{ type: 'output_text', text: 'status output' }]
        },
        {
          type: 'function_call',
          call_id: 'call_deepseek_unanswered',
          name: 'shell_command',
          arguments: '{"command":"dangling"}'
        },
        {
          type: 'function_call_output',
          call_id: 'call_deepseek_orphan',
          output: 'orphan output'
        },
        {
          type: 'message',
          role: 'user',
          content: [
            { type: 'input_text', text: 'hello deepseek codex bridge' },
            { type: 'input_image', image_url: 'data:image/png;base64,REVFUFNFRUs=', detail: 'low' }
          ]
        }
      ],
      stream: true,
      store: false,
      max_output_tokens: 128,
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
        }
      ],
      tool_choice: 'auto',
      parallel_tool_calls: false,
      temperature: 0,
      top_p: 1
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `DeepSeek Codex Responses 桥接应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'DeepSeek Codex bridge 响应应是 SSE')
  assert.match(text, /event: response\.created/, '桥接响应应输出 Responses created 事件')
  assert.match(text, /event: response\.reasoning_summary_text\.delta/, '桥接响应应保留 DeepSeek reasoning_content')
  assert.match(text, /event: response\.output_text\.delta/, '桥接响应应输出 Responses 文本 delta 事件')
  assert.match(text, /event: response\.completed/, '桥接响应应输出 Responses completed 事件')
  assert.match(text, /deepseek sse reasoning/, 'DeepSeek reasoning 内容应进入 Responses 事件')
  assert.match(text, /deepseek sse ok/, 'DeepSeek 文本内容应进入 Responses 事件')
  assert.match(text, /"type":"reasoning"/, '桥接响应应把 reasoning_content 聚合成 Codex 可消费的 reasoning output item')
  assert.match(text, /"type":"function_call"/, '桥接响应应把 Chat tool_calls 转成 Codex 可消费的 function_call output item')
  assert(!text.includes('chat.completion.chunk'), '桥接后的下游响应不应泄漏 Chat Completions SSE 原始对象')
  assert(!text.includes('glm_bridge'), 'DeepSeek bridge 响应 ID 不应暴露 GLM bridge 前缀')
  assert.match(text, /deepseek_bridge/, 'DeepSeek bridge 响应 ID 应使用 DeepSeek bridge 前缀，便于排障')

  assert.equal(upstreamHits.length, 1, 'DeepSeek Codex bridge 应只命中一次上游')
  const hit = upstreamHits[0]
  assert.equal(hit.path, '/v1/chat/completions')
  assert.equal(hit.rawUrl, '/v1/chat/completions?trace=deepseek-codex-bridge', 'DeepSeek Codex bridge 应保留查询参数')
  assert.equal(hit.authorization, 'Bearer sk-deepseek-codex-upstream')
  const body = JSON.parse(hit.bodyText) as {
    model?: string
    stream?: boolean
    stream_options?: { include_usage?: boolean }
    messages?: unknown[]
    tools?: unknown[]
    max_tokens?: number
    temperature?: number
    top_p?: number
  }
  assert.equal(body.model, 'deepseek-ai-v4-flash', 'DeepSeek Codex bridge 应应用账号模型映射')
  assert.equal(body.stream, true, 'DeepSeek Codex bridge 上游请求必须使用 Chat SSE')
  assert.equal(body.stream_options?.include_usage, true, 'DeepSeek Codex bridge 应请求流式 usage')
  assert.equal(body.max_tokens, 128, 'DeepSeek Codex bridge 应把 max_output_tokens 转成 max_tokens')
  assert.equal(body.temperature, 0)
  assert.equal(body.top_p, 1)
  assert(Array.isArray(body.messages), 'DeepSeek Codex bridge 应把 Responses input 转成 Chat messages')
  assert.match(JSON.stringify(body.messages), /hello deepseek codex bridge/, 'Chat messages 应包含用户文本输入')
  assert.match(JSON.stringify(body.messages), /data:image\/png;base64,REVFUFNFRUs=/, 'Chat messages 应保留 Codex input_image data URL')
  const chatMessages = body.messages as Array<Record<string, unknown>>
  assertValidChatToolMessageSequence(chatMessages, 'DeepSeek Codex bridge')
  const historyAssistantIndex = chatMessages.findIndex((message) => Array.isArray(message.tool_calls)
    && (message.tool_calls as Array<{ id?: string }>).some((toolCall) => toolCall.id === 'call_deepseek_history_a'))
  assert(historyAssistantIndex >= 0, 'DeepSeek Codex bridge 应把历史 function_call 转成 assistant tool_calls')
  const historyAssistant = chatMessages[historyAssistantIndex] as { reasoning_content?: string; tool_calls?: Array<{ id?: string }> }
  assert.equal(historyAssistant.reasoning_content, 'inspect repo before tools', 'DeepSeek Codex bridge 应把 Codex reasoning 历史转成 DeepSeek reasoning_content')
  assert.equal(historyAssistant.tool_calls?.length, 2, 'DeepSeek Codex bridge 应把连续历史 function_call 合并到一个 assistant tool_calls 消息')
  assert.equal(chatMessages[historyAssistantIndex + 1]?.role, 'tool', '历史 assistant tool_calls 后应紧跟第一个 tool 输出')
  assert.equal(chatMessages[historyAssistantIndex + 1]?.tool_call_id, 'call_deepseek_history_a')
  assert.equal(chatMessages[historyAssistantIndex + 1]?.content, 'commit output')
  assert.equal(chatMessages[historyAssistantIndex + 2]?.role, 'tool', '历史 assistant tool_calls 后应紧跟第二个 tool 输出')
  assert.equal(chatMessages[historyAssistantIndex + 2]?.tool_call_id, 'call_deepseek_history_b')
  assert.equal(chatMessages[historyAssistantIndex + 2]?.content, 'status output')
  const deferredSystemIndex = chatMessages.findIndex((message, index) => index > historyAssistantIndex
    && message.role === 'system'
    && JSON.stringify(message).includes('Approved command prefix saved'))
  assert(deferredSystemIndex > historyAssistantIndex + 2, '夹在 tool_call 和 tool output 中间的 developer 消息应延后到 tool 输出之后，避免破坏 Chat tool 消息不变量')
  assert(!JSON.stringify(chatMessages).includes('bare tool message must not reach upstream'), 'Responses message role=tool 不能生成裸 Chat tool 消息')
  assert(!JSON.stringify(chatMessages).includes('call_deepseek_unanswered'), '未收到输出的 dangling function_call 不应透传给 Chat 上游')
  assert(!JSON.stringify(chatMessages).includes('call_deepseek_orphan'), '找不到对应 function_call 的 orphan tool output 不应透传给 Chat 上游')
  assert(Array.isArray(body.tools), 'DeepSeek Codex bridge 应把 function tools 转成 Chat tools')
  assert.equal(body.tools.length, 1, 'DeepSeek Codex bridge 应透传 function tools')
  assert.match(JSON.stringify(body.tools), /shell_command/)
  assert.doesNotMatch(JSON.stringify(body.messages), /不能代执行以下 Responses 原生托管工具/, 'DeepSeek Codex bridge 不应注入 hosted tool 降级 system prompt')
}

async function assertDeepSeekCodexResponsesBridgeRejectsUnsupportedHostedTool(baseUrl: string, localApiKey: string): Promise<void> {
  const start = upstreamHits.length
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-native-tool-auto-reject-session',
        thread_id: 'deepseek-native-tool-auto-reject-thread',
        turn_id: 'deepseek-native-tool-auto-reject-turn'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
      input: 'hello deepseek auto web search should reject as unsupported hosted tool',
      stream: true,
      store: false,
      tools: [
        {
          type: 'web_search',
          external_web_access: false
        }
      ],
      tool_choice: 'auto'
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `auto web_search 应返回正常 guidance，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.completed/, 'auto 原生托管工具 guidance 应正常完成 Responses SSE')
  assert.match(text, /能力未执行：web_search/, 'auto guidance 应说明未执行 web_search')
  assert.match(text, /建议下一步/, 'auto guidance 应给出下一步建议')
  assert.doesNotMatch(text, /response\.failed|unsupported_codex_native_tool/, 'auto guidance 不应返回失败事件或旧错误码')
  assert.equal(upstreamHits.length, start, 'auto web_search guidance 不应命中 DeepSeek Chat 上游')

  const mixedResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-native-tool-mixed-auto-session',
        thread_id: 'deepseek-native-tool-mixed-auto-thread',
        turn_id: 'deepseek-native-tool-mixed-auto-turn'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
      input: 'hello deepseek mixed auto should guide as unsupported hosted tool',
      stream: true,
      store: false,
      tools: [
        { type: 'function', name: 'shell_command', parameters: { type: 'object', properties: {} } },
        { type: 'custom', name: 'apply_patch', description: 'Apply a patch in the workspace.' },
        { type: 'tool_search' },
        { type: 'web_search', external_web_access: false }
      ],
      tool_choice: 'auto'
    })
  })
  const mixedText = await mixedResponse.text()
  assert.equal(mixedResponse.status, 200, `混合 auto hosted tools 应返回正常 guidance，实际 HTTP ${mixedResponse.status}: ${mixedText}`)
  assert.match(mixedText, /event: response\.completed/, '混合 auto guidance 应正常完成 Responses SSE')
  assert.match(mixedText, /tool_search/, '混合 auto guidance 应包含 tool_search')
  assert.match(mixedText, /web_search/, '混合 auto guidance 应包含 web_search')
  assert.doesNotMatch(mixedText, /response\.failed|unsupported_codex_native_tool/, '混合 auto guidance 不应返回失败事件或旧错误码')
  assert.equal(upstreamHits.length, start, '混合 auto guidance 不应命中 DeepSeek Chat 上游')
}

async function assertDeepSeekCodexResponsesBridgeRestoresPreviousResponseId(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const firstResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-state-session',
        thread_id: 'deepseek-bridge-state-thread',
        turn_id: 'deepseek-bridge-state-turn-1'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'remember first deepseek codex context' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const firstText = await firstResponse.text()
  assert.equal(firstResponse.status, 200, `DeepSeek Codex bridge 首轮应成功，实际 HTTP ${firstResponse.status}: ${firstText}`)
  const firstResponseId = responsesCompletedPayload(firstText)?.id ?? ''
  assert.match(firstResponseId, /^resp_deepseek_bridge_/, 'DeepSeek Codex bridge 首轮应生成可续接 response id')

  const secondResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-state-session',
        thread_id: 'deepseek-bridge-state-thread',
        turn_id: 'deepseek-bridge-state-turn-2'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
      previous_response_id: firstResponseId,
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'continue second deepseek codex context' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const secondText = await secondResponse.text()
  assert.equal(secondResponse.status, 200, `DeepSeek Codex bridge previous_response_id 续链应成功，实际 HTTP ${secondResponse.status}: ${secondText}`)
  assert.match(secondText, new RegExp(`"previous_response_id":"${escapeRegExp(firstResponseId)}"`), '续链响应应回显 previous_response_id')
  assert.equal(upstreamHits.length, 2, 'DeepSeek Codex bridge 续链应每轮各命中一次上游')
  const secondBody = JSON.parse(upstreamHits[1]?.bodyText ?? '{}') as { messages?: unknown[]; previous_response_id?: unknown }
  assert.equal(secondBody.previous_response_id, undefined, 'previous_response_id 只能在网关内消费，不能发给 Chat 上游')
  const messagesText = JSON.stringify(secondBody.messages)
  assert.match(messagesText, /remember first deepseek codex context/, '续链 Chat messages 应恢复首轮用户输入')
  assert.match(messagesText, /deepseek sse ok/, '续链 Chat messages 应恢复首轮 assistant 输出')
  assert.match(messagesText, /continue second deepseek codex context/, '续链 Chat messages 应追加本轮用户输入')
}

async function assertDeepSeekCodexResponsesBridgeRejectsUnknownPreviousResponseId(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-previous-session',
        thread_id: 'deepseek-bridge-previous-thread',
        turn_id: 'deepseek-bridge-previous-turn'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
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
  assert.equal(response.status, 404, `DeepSeek Codex bridge 未知 previous_response_id 应受控拒绝，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /codex_bridge_previous_response_not_found/, 'previous_response_id 不存在响应应带稳定错误码')
  assert.equal(upstreamHits.length, 0, 'DeepSeek Codex bridge 未知 previous_response_id 受控拒绝时不应命中上游')
}

async function assertDeepSeekCodexResponsesBridgeGatewaySummaryCompact(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses/compact`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'application/json',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-compact-session',
        thread_id: 'deepseek-bridge-compact-thread',
        turn_id: 'deepseek-bridge-compact-turn'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
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
  assert.equal(response.status, 200, `DeepSeek Codex bridge /responses/compact 应返回网关摘要，实际 HTTP ${response.status}: ${text}`)
  const payload = JSON.parse(text) as { id?: unknown; object?: unknown; created_at?: unknown; output?: Array<Record<string, unknown>> }
  assert.equal(payload.object, 'response.compaction', '/responses/compact 应返回 OpenAI CompactResource 兼容 object')
  assert.equal(typeof payload.id, 'string', '/responses/compact 应返回顶层 response id')
  assert.equal(typeof payload.created_at, 'number', '/responses/compact 应返回 created_at')
  const compactItem = payload.output?.[0]
  assert.equal(compactItem?.type, 'compaction', '/responses/compact 应返回 OpenAI CompactResource compaction item')
  assert.match(String(compactItem?.encrypted_content ?? ''), /^juhecmp\.v2\.cmp_[^.]+\.[a-f0-9]{64}$/i, 'compact summary 应使用网关 snapshot reference envelope')
  assert.equal(upstreamHits.length, 1, 'DeepSeek Codex bridge /responses/compact 应通过内部 Chat Completions 摘要请求命中一次上游')
  assert.equal(upstreamHits[0]?.path, '/v1/chat/completions', 'compact 内部请求必须走 Chat Completions 路径')
  const compactUpstreamBody = JSON.parse(upstreamHits[0]?.bodyText ?? '{}') as { stream?: boolean; messages?: unknown[] }
  assert.equal(compactUpstreamBody.stream, false, 'compact 内部摘要请求必须是非流式 Chat 请求')
  assert.match(JSON.stringify(compactUpstreamBody.messages), /compact this history/, 'compact 内部摘要请求应包含待压缩上下文')

  const bridgeResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-compact-followup-session',
        thread_id: 'deepseek-bridge-compact-followup-thread',
        turn_id: 'deepseek-bridge-compact-followup-turn'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
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
  assert.equal(bridgeResponse.status, 200, `DeepSeek Codex bridge 应能消费 compaction item，实际 HTTP ${bridgeResponse.status}: ${bridgeText}`)
  assert.equal(upstreamHits.length, 2, 'DeepSeek compaction 后续请求应再命中一次上游')
  assert.match(upstreamHits[1]?.bodyText ?? '', /deepseek json ok/, '后续 Chat messages 应包含 compact 摘要文本')
  assert.match(upstreamHits[1]?.bodyText ?? '', /continue after compact summary/, '后续 Chat messages 应包含 compact 后的新输入')

  const hitsBeforeTamperedCompact = upstreamHits.length
  const tamperedCompactResponse = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-compact-tampered-session',
        thread_id: 'deepseek-bridge-compact-tampered-thread',
        turn_id: 'deepseek-bridge-compact-tampered-turn'
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
      input: [
        {
          ...compactItem,
          encrypted_content: tamperCompactDigest(String(compactItem?.encrypted_content ?? ''))
        },
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: 'this must not reach upstream with tampered compact' }]
        }
      ],
      stream: true,
      store: false
    })
  })
  const tamperedCompactText = await tamperedCompactResponse.text()
  assert.equal(tamperedCompactResponse.status, 404, `DeepSeek tampered compact snapshot 应受控失败，实际 HTTP ${tamperedCompactResponse.status}: ${tamperedCompactText}`)
  assert.match(tamperedCompactText, /codex_bridge_compact_snapshot_not_found/, '篡改 compact digest 应返回稳定错误码')
  assert.equal(upstreamHits.length, hitsBeforeTamperedCompact, '篡改 compact snapshot 受控失败时不应命中上游')
}

function tamperCompactDigest(encryptedContent: string): string {
  const parts = encryptedContent.split('.')
  assert.equal(parts.length, 4, 'compact envelope 应包含 prefix、version、compact id 和 digest')
  const digest = parts[3] ?? ''
  assert.match(digest, /^[a-f0-9]{64}$/i, 'compact digest 应为 64 位 hex')
  parts[3] = `${digest.slice(0, -1)}${digest.endsWith('0') ? '1' : '0'}`
  return parts.join('.')
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

async function assertDeepSeekCodexResponsesBridgeStringUsage(baseUrl: string, localApiKey: string): Promise<void> {
  const text = await requestDeepSeekCodexBridgeUsage(baseUrl, localApiKey, 'hello deepseek codex bridge string usage')
  const completed = responsesCompletedPayload(text)
  assert.equal(completed?.usage?.input_tokens, 21, 'DeepSeek bridge 应接受上游 usage.prompt_tokens 数字字符串')
  assert.equal(completed?.usage?.output_tokens, 5, 'DeepSeek bridge 应接受上游 usage.completion_tokens 数字字符串')
  assert.equal(completed?.usage?.total_tokens, 26, 'DeepSeek bridge 应接受上游 usage.total_tokens 数字字符串')
}

async function assertDeepSeekCodexResponsesBridgeFallbackUsage(baseUrl: string, localApiKey: string): Promise<void> {
  const text = await requestDeepSeekCodexBridgeUsage(baseUrl, localApiKey, 'hello deepseek codex bridge fallback usage')
  const completed = responsesCompletedPayload(text)
  assert(completed?.usage && typeof completed.usage === 'object', 'DeepSeek bridge 上游无 usage 时 completed 事件应输出估算 usage 对象')
  assert(Number(completed.usage.input_tokens) > 0, 'DeepSeek bridge fallback usage 应包含正数 input_tokens')
  assert(Number(completed.usage.output_tokens) > 0, 'DeepSeek bridge fallback usage 应包含正数 output_tokens')
  assert.equal(Number(completed.usage.total_tokens), Number(completed.usage.input_tokens) + Number(completed.usage.output_tokens), 'DeepSeek bridge fallback usage total_tokens 应等于输入输出之和')
}

async function requestDeepSeekCodexBridgeUsage(baseUrl: string, localApiKey: string, marker: string): Promise<string> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-usage-session',
        thread_id: 'deepseek-bridge-usage-thread',
        turn_id: `deepseek-bridge-usage-${marker.replace(/\W+/g, '-')}`
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
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
  assert.equal(response.status, 200, `DeepSeek Codex bridge usage 场景应成功，实际 HTTP ${response.status}: ${text}`)
  assert.match(text, /event: response\.completed/, 'DeepSeek Codex bridge usage 场景应输出 completed 事件')
  assert.equal(upstreamHits.length, 1, 'DeepSeek Codex bridge usage 场景应只命中一次上游')
  assert.match(upstreamHits[0]?.bodyText ?? '', new RegExp(marker))
  return text
}

async function assertDeepSeekCodexResponsesBridgeFailsOnTruncatedStream(baseUrl: string, localApiKey: string): Promise<void> {
  const text = await requestDeepSeekCodexBridgeFailure(baseUrl, localApiKey, 'hello deepseek codex bridge truncated')
  assert.match(text, /event: response\.failed/, '上游 Chat SSE 截断时 DeepSeek bridge 应输出 Responses failed 事件')
  assert.match(text, /upstream_stream_interrupted/, '截断错误应带稳定机器错误码')
  assert(!text.includes('event: response.completed'), '上游 Chat SSE 截断时 DeepSeek bridge 不应输出 completed 事件')
}

async function assertDeepSeekCodexResponsesBridgeFailsOnErrorEvent(baseUrl: string, localApiKey: string): Promise<void> {
  const text = await requestDeepSeekCodexBridgeFailure(baseUrl, localApiKey, 'hello deepseek codex bridge error event')
  assert.match(text, /event: response\.failed/, '上游 Chat SSE error 事件时 DeepSeek bridge 应输出 Responses failed 事件')
  assert.match(text, /upstream_retryable_error/, '上游 error 事件应归一为可重试上游错误码')
  assert.match(text, /上游流式响应在输出前失败，请重试/, '上游 error 事件应向 Codex 客户端返回统一可重试文案')
  assert(!text.includes('deepseek mock stream error'), 'DeepSeek 上游 error 原文不应泄露到 Codex Responses failed payload')
  assert(!text.includes('event: response.completed'), '上游 Chat SSE error 事件时 DeepSeek bridge 不应输出 completed 事件')
}

async function assertDeepSeekCodexResponsesBridgeFailsOnInsufficientResourceFinishReason(baseUrl: string, localApiKey: string): Promise<void> {
  const text = await requestDeepSeekCodexBridgeFailure(baseUrl, localApiKey, 'hello deepseek codex bridge insufficient resource')
  assert.match(text, /event: response\.failed/, 'DeepSeek insufficient_system_resource finish_reason 应转换为 Responses failed 事件')
  assert.match(text, /upstream_retryable_error/, 'DeepSeek insufficient_system_resource finish_reason 应标记为可重试上游错误')
  assert.match(text, /上游流式响应在输出前失败，请重试/, 'DeepSeek insufficient_system_resource finish_reason 应返回 Codex 可重试文案')
  assert(!text.includes('event: response.completed'), 'DeepSeek insufficient_system_resource finish_reason 不应输出 completed 事件')
}

async function requestDeepSeekCodexBridgeFailure(baseUrl: string, localApiKey: string, marker: string): Promise<string> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        session_id: 'deepseek-bridge-failure-session',
        thread_id: 'deepseek-bridge-failure-thread',
        turn_id: `deepseek-bridge-failure-${marker.replace(/\W+/g, '-')}`
      })
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
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
  assert.equal(response.status, 200, `DeepSeek Codex bridge 失败事件仍应保持 SSE 传输，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 1, 'DeepSeek Codex bridge 失败场景应只命中一次上游')
  assert.equal(upstreamHits[0]?.path, '/v1/chat/completions')
  assert.equal(upstreamHits[0]?.authorization, 'Bearer sk-deepseek-codex-upstream')
  assert.match(upstreamHits[0]?.bodyText ?? '', new RegExp(marker))
  return text
}

async function assertDeepSeekExplicitResponsesBridgeAllowsStandardClient(baseUrl: string, localApiKey: string): Promise<void> {
  upstreamHits.length = 0
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${localApiKey}`,
      'content-type': 'application/json',
      accept: 'text/event-stream'
    },
    body: JSON.stringify({
      model: 'deepseek-v4-flash',
      input: 'standard responses explicit mapping bridge',
      stream: true,
      store: false
    })
  })
  const text = await response.text()
  assert.equal(response.status, 200, `DeepSeek 显式协议映射应允许标准 OpenAI Responses 流式桥接，实际 HTTP ${response.status}: ${text}`)
  assert.equal(upstreamHits.length, 1, 'DeepSeek 显式协议映射应命中一次 Chat Completions 上游')
  assert.equal(upstreamHits[0]?.path, '/v1/chat/completions')
  assert.match(upstreamHits[0]?.bodyText ?? '', /standard responses explicit mapping bridge/)
}

async function assertDeepSeekRejectsNonChatRoutes(baseUrl: string, localApiKey: string): Promise<void> {
  const cases = [
    {
      name: 'GET chat completions',
      path: '/v1/chat/completions',
      init: {
        method: 'GET',
        headers: {
          authorization: `Bearer ${localApiKey}`
        }
      }
    },
    {
      name: 'nested chat completions path',
      path: '/v1/foo/chat/completions/bar',
      init: {
        method: 'POST',
        headers: {
          authorization: `Bearer ${localApiKey}`,
          'content-type': 'application/json'
        },
        body: JSON.stringify({
          model: 'deepseek-ai-v4-flash',
          messages: [{ role: 'user', content: 'must not reach upstream' }],
          stream: false
        })
      }
    }
  ] as const
  for (const item of cases) {
    upstreamHits.length = 0
    const response = await fetch(`${baseUrl}${item.path}`, item.init)
    const text = await response.text()
    assert.notEqual(response.status, 200, `DeepSeek 不应承接 ${item.name}，实际响应不应成功：${text}`)
    assert.equal(upstreamHits.length, 0, `DeepSeek ${item.name} 不应命中上游`)
  }
}

function assertDeepSeekSemanticParsing(): void {
  const jsonFrames = extractOpenAIJsonSemanticFrames({
    choices: [
      {
        message: {
          reasoning_content: 'semantic reasoning',
          content: 'semantic answer'
        },
        finish_reason: 'insufficient_system_resource'
      }
    ],
    usage: {
      prompt_tokens: 10,
      completion_tokens: 2,
      prompt_cache_hit_tokens: 4
    }
  }, 'chat_completions')
  assert(jsonFrames.some((frame) => frame.rawJsonPaths?.includes('choices.0.message.reasoning_content') && frame.text === 'semantic reasoning'), 'DeepSeek 非流式 reasoning_content 应进入语义帧')
  assert(jsonFrames.some((frame) => frame.finishReason === 'insufficient_system_resource'), 'DeepSeek 特殊 finish_reason 应进入完成语义帧')

  const sseEvent = parseOpenAISseEventText('data: {"choices":[{"index":0,"delta":{"reasoning_content":"semantic sse reasoning"},"finish_reason":null}]}\n')
  const sseFrames = extractOpenAISseSemanticFrames(sseEvent, 'chat_completions')
  assert(sseFrames.some((frame) => frame.rawJsonPaths?.includes('choices.0.delta.reasoning_content') && frame.text === 'semantic sse reasoning'), 'DeepSeek 流式 reasoning_content 应进入语义帧')
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

function assertDeepSeekDispatchCapability(groupId: string, accountId: string): void {
  const dispatchAccount = repositories.findOpenAIAccountForGroup(groupId, accountId, access.systemAccountId, {
    ignoreAvailability: true
  })
  assert(dispatchAccount, 'DeepSeek dispatch account 应可从分组选择')
  const request = {
    method: 'POST',
    path: '/v1/chat/completions',
    originalUrl: '/v1/chat/completions',
    headers: { 'content-type': 'application/json' },
    body: { stream: false }
  } as unknown as express.Request
  assert.equal(providerDriverForAccount(dispatchAccount)?.id, 'deepseek', `DeepSeek dispatch account 应匹配 deepseek driver: ${JSON.stringify({
    providerCode: dispatchAccount.providerCode,
    providerProtocolProfileId: dispatchAccount.providerProtocolProfileId,
    protocolCode: dispatchAccount.protocolCode,
    protocolVersion: dispatchAccount.protocolVersion,
    supportedEndpointModes: dispatchAccount.supportedEndpointModes,
    baseUrl: dispatchAccount.baseUrl
  })}`)
  assert.deepEqual(buildGatewayUpstreamUrlsForAccount(dispatchAccount, request), [`${dispatchAccount.baseUrl}/v1/chat/completions`])
  assert.equal(accountSupportsGatewayRequest(request, dispatchAccount), true, 'DeepSeek dispatch account 应支持 Chat Completions JSON')

  const getRequest = {
    method: 'GET',
    path: '/v1/chat/completions',
    originalUrl: '/v1/chat/completions',
    headers: {}
  } as unknown as express.Request
  assert.deepEqual(buildGatewayUpstreamUrlsForAccount(dispatchAccount, getRequest), [], 'DeepSeek 不应为 GET Chat Completions 构造上游 URL')
  assert.equal(accountSupportsGatewayRequest(getRequest, dispatchAccount), false, 'DeepSeek 不应支持 GET Chat Completions')

  const nestedPathRequest = {
    method: 'POST',
    path: '/v1/foo/chat/completions/bar',
    originalUrl: '/v1/foo/chat/completions/bar',
    headers: { 'content-type': 'application/json' },
    body: { stream: false }
  } as unknown as express.Request
  assert.deepEqual(buildGatewayUpstreamUrlsForAccount(dispatchAccount, nestedPathRequest), [], 'DeepSeek 不应为嵌套 Chat Completions 路径构造上游 URL')
  assert.equal(accountSupportsGatewayRequest(nestedPathRequest, dispatchAccount), false, 'DeepSeek 不应支持嵌套 Chat Completions 路径')
}

function assertDeepSeekCodexDispatchCapability(groupId: string, accountId: string): void {
  const dispatchAccount = repositories.findOpenAIAccountForGroup(groupId, accountId, access.systemAccountId, {
    ignoreAvailability: true
  })
  assert(dispatchAccount, 'DeepSeek Codex bridge dispatch account 应可从分组选择')
  const bridgeDispatchAccount = dispatchAccount
  const oldExplicitHybridRouteAccount = {
    ...bridgeDispatchAccount,
    modelMappings: (bridgeDispatchAccount.modelMappings ?? []).map((mapping) => ({
      ...mapping,
      runtimeSource: 'explicit_hybrid_route' as const,
      runtimeRouteRuleId: 'responses_to_deepseek_chat'
    }))
  } as unknown as typeof dispatchAccount
  assert.equal(providerDriverForAccount(bridgeDispatchAccount)?.id, 'deepseek')
  const codexResponsesRequest = {
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses?trace=driver-check',
    headers: { 'content-type': 'application/json' },
    body: { model: 'deepseek-v4-flash', stream: true }
  } as unknown as express.Request
  assert.deepEqual(
    buildGatewayUpstreamUrlsForAccount(bridgeDispatchAccount, codexResponsesRequest),
    [`${bridgeDispatchAccount.baseUrl}/v1/chat/completions?trace=driver-check`],
    'DeepSeek 普通账号应通过持久账号级模型映射把 /responses 改写到 /chat/completions'
  )
  assert.equal(
    accountSupportsGatewayRequest(codexResponsesRequest, bridgeDispatchAccount, { requestClientCompatibility: 'codex_responses' }),
    true,
    'DeepSeek 普通账号应在显式 Responses -> Chat 映射命中时承接 Codex Responses bridge'
  )
  assert.equal(
    accountSupportsGatewayRequest(codexResponsesRequest, oldExplicitHybridRouteAccount, { requestClientCompatibility: 'codex_responses' }),
    false,
    'DeepSeek 普通账号不应因旧显式混合路由标记注入承接 Responses -> Chat bridge'
  )
}

function createDeepSeekMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      upstreamHits.push({
        path: req.url?.split('?', 1)[0] ?? '',
        rawUrl: req.url ?? '',
        method: req.method ?? '',
        authorization: String(req.headers.authorization ?? ''),
        contentLength: String(req.headers['content-length'] ?? ''),
        transferEncoding: String(req.headers['transfer-encoding'] ?? ''),
        bodyText
      })
      const body = safeJson(bodyText)
      if (req.url?.split('?', 1)[0] !== '/v1/chat/completions') {
        res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'not found' } }))
        return
      }
      if (body.stream === true) {
        res.writeHead(200, {
          'content-type': 'text/event-stream; charset=utf-8',
          'cache-control': 'no-cache'
        })
        if (bodyText.includes('hello deepseek precommit stream failure')) {
          res.end(': keep-alive\n\n')
          return
        }
        if (bodyText.includes('hello deepseek codex bridge truncated')) {
          res.end(`data: ${JSON.stringify({
            id: 'chatcmpl-deepseek-sse',
            object: 'chat.completion.chunk',
            choices: [
              { index: 0, delta: { content: 'partial deepseek bridge' }, finish_reason: null }
            ]
          })}\n\n`)
          return
        }
        if (bodyText.includes('hello deepseek codex bridge error event')) {
          res.end('event: error\ndata: {"error":{"message":"deepseek mock stream error","code":"upstream_retryable_error"}}\n\n')
          return
        }
        if (bodyText.includes('hello deepseek codex bridge insufficient resource')) {
          res.write(`data: ${JSON.stringify({
            id: 'chatcmpl-deepseek-sse',
            object: 'chat.completion.chunk',
            choices: [
              { index: 0, delta: {}, finish_reason: 'insufficient_system_resource' }
            ],
            usage: {
              prompt_tokens: 12,
              completion_tokens: 0,
              total_tokens: 12
            }
          })}\n\n`)
          res.end('data: [DONE]\n\n')
          return
        }
        if (bodyText.includes('hello deepseek codex bridge string usage')) {
          res.write(`data: ${JSON.stringify({
            id: 'chatcmpl-deepseek-sse-string-usage',
            object: 'chat.completion.chunk',
            choices: [
              { index: 0, delta: { content: 'deepseek bridge string usage ok' }, finish_reason: 'stop' }
            ],
            usage: {
              prompt_tokens: '21',
              completion_tokens: '5',
              total_tokens: '26'
            }
          })}\n\n`)
          res.end('data: [DONE]\n\n')
          return
        }
        if (bodyText.includes('hello deepseek codex bridge fallback usage')) {
          res.write(`data: ${JSON.stringify({
            id: 'chatcmpl-deepseek-sse-fallback-usage',
            object: 'chat.completion.chunk',
            choices: [
              { index: 0, delta: { content: 'deepseek bridge fallback usage ok' }, finish_reason: 'stop' }
            ]
          })}\n\n`)
          res.end('data: [DONE]\n\n')
          return
        }
        res.write(': keep-alive\n\n')
        res.write(`data: ${JSON.stringify({
          id: 'chatcmpl-deepseek-sse',
          object: 'chat.completion.chunk',
          choices: [
            { index: 0, delta: { reasoning_content: 'deepseek sse reasoning' }, finish_reason: null }
          ]
        })}\n\n`)
        res.write(`data: ${JSON.stringify({
          id: 'chatcmpl-deepseek-sse',
          object: 'chat.completion.chunk',
          choices: [
            { index: 0, delta: { content: 'deepseek sse ok' }, finish_reason: null }
          ],
          usage: {
            prompt_tokens: 12,
            completion_tokens: 3,
            prompt_cache_hit_tokens: 5,
            prompt_cache_miss_tokens: 7
          }
        })}\n\n`)
        if (Array.isArray(body.tools)) {
          res.write(`data: ${JSON.stringify({
            id: 'chatcmpl-deepseek-sse',
            object: 'chat.completion.chunk',
            choices: [
              {
                index: 0,
                delta: {
                  tool_calls: [
                    {
                      index: 0,
                      id: 'call_deepseek_mock_tool',
                      type: 'function',
                      function: {
                        name: 'shell_command',
                        arguments: '{"command":"Get-Date"}'
                      }
                    }
                  ]
                },
                finish_reason: null
              }
            ]
          })}\n\n`)
        }
        res.end('data: [DONE]\n\n')
        return
      }
      const authorization = String(req.headers.authorization ?? '')
      if (
        bodyText.includes('hello deepseek json body interrupted')
        && authorization === 'Bearer sk-deepseek-interrupt-upstream'
      ) {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.flushHeaders()
        res.write('{"id":"chatcmpl-deepseek-partial","object":"chat.completion","choices":[')
        setTimeout(() => {
          res.destroy(new Error('deepseek mock json body interrupted'))
        }, 20).unref()
        return
      }
      if (
        bodyText.includes('hello deepseek invalid choices all bad')
        || (
          bodyText.includes('hello deepseek invalid choices')
          && authorization === 'Bearer sk-deepseek-upstream'
        )
      ) {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: '',
          object: '',
          model: 'deepseek-ai-v4-flash',
          choices: null,
          usage: {
            prompt_tokens: 11,
            completion_tokens: 0,
            total_tokens: 11
          }
        }))
        return
      }
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({
        id: 'chatcmpl-deepseek-json',
        object: 'chat.completion',
        choices: [
          {
            index: 0,
            message: {
              role: 'assistant',
              reasoning_content: 'deepseek json reasoning',
              content: 'deepseek json ok'
            },
            finish_reason: 'stop'
          }
        ],
        usage: {
          prompt_tokens: 1000,
          completion_tokens: 80,
          prompt_cache_hit_tokens: 640,
          prompt_cache_miss_tokens: 360
        }
      }))
    })
  })
}

function safeJson(text: string): Record<string, unknown> {
  try {
    return JSON.parse(text) as Record<string, unknown>
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
