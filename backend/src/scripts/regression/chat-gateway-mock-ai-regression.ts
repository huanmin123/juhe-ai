import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import sharp from 'sharp'

import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE
} from '../../domain/provider-protocol.js'
import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import { buildChatSystemInstructions } from '../../modules/chat/chat-system-instructions.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'

const backendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')
const projectRoot = resolve(backendRoot, '..')
const tempRoot = resolve(tmpdir(), `juhe-ai-chat-mock-${Date.now()}-${Math.random().toString(16).slice(2)}`)
const upstreamAuthorizations: string[] = []
const upstreamBodies: Array<Record<string, unknown>> = []
const upstreamPaths: string[] = []
const upstreamTraceIds: string[] = []
let upstream: http.Server | undefined
let backend: ChildProcess | undefined
const realCredentialFile = process.env.JUHE_AI_CHAT_REAL_CREDENTIAL_FILE?.trim()
const realCredential = realCredentialFile ? readRealCredential(realCredentialFile) : undefined
const testModel = process.env.JUHE_AI_CHAT_REAL_MODEL?.trim() || realCredential?.models.find((item) => item === 'gpt-5.5') || realCredential?.models[0] || 'gpt-5.5'
const chatOnlyTestModel = 'chat-only-model'

runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.chatDatabasePath = join(tempRoot, 'chat.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.codexContextRoot = join(tempRoot, 'codex-context')
runtimeConfig.codexContextStateShardRoot = join(tempRoot, 'codex-context', 'state-shards')
runtimeConfig.chatAssetsRoot = join(tempRoot, 'chat-assets')
runtimeConfig.secret = 'chat-mock-secret'
runtimeConfig.processRole = 'db-service'
runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const databaseModule = await import('../../storage/database.js')
const repositories = await import('../../storage/repositories.js')
const modelCatalogService = await import('../../modules/model-pricing/model-catalog.service.js')

try {
  if (!realCredential) {
    upstream = createMockUpstream()
    upstream.listen(0, '127.0.0.1')
    await onceListening(upstream)
  }
  const upstreamBaseUrl = realCredential?.baseUrl ?? `http://127.0.0.1:${serverPort(upstream!)}/v1`
  const access = { systemAccountId: 'sys_admin', role: 'user' as const }
  const group = repositories.createGroup({ name: 'AI 问答 Mock 分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
  const account = repositories.createAccount({
    providerCode: GPT_VENDOR_CODE,
    providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
    name: 'AI 问答 Mock 账户',
    type: 'api_key',
    credentials: { api_key: realCredential?.apiKey ?? 'sk-chat-upstream', base_url: upstreamBaseUrl },
    groupId: group.id,
    supportedModels: realCredential?.models ?? [testModel],
    healthCheckModel: testModel,
    status: 'active',
    schedulable: true
  }, access)
  assert(repositories.recordAccountHealthCheckSuccess(account.id, { intervalHours: 24, jitterMinutes: 0, failureThreshold: 3, statusCode: 200 }))
  const gatewayKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'AI 问答 Mock Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(gatewayKey.key)
  let chatOnlyGatewayKeyId: string | undefined
  let chatOnlyGroupId: string | undefined
  if (!realCredential) {
    modelCatalogService.saveCustomProviderModel({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      model: chatOnlyTestModel,
      scope: 'personal',
      systemAccountId: access.systemAccountId,
      status: 'active',
      supportedApiProtocols: ['chat_completions'],
      inputUsdPer1M: 0.001,
      outputUsdPer1M: 0.001,
      actorSystemAccountId: access.systemAccountId
    })
    const chatOnlyGroup = repositories.createGroup({ name: 'AI 问答 Chat-only 分组', providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE, enabled: true }, access)
    chatOnlyGroupId = chatOnlyGroup.id
    const chatOnlyAccount = repositories.createAccount({
      providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE,
      providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
      name: 'AI 问答 Chat-only 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-chat-only-upstream',
        base_url: upstreamBaseUrl,
        supported_endpoint_modes: ['chat_json', 'chat_sse']
      },
      groupId: chatOnlyGroup.id,
      supportedModels: [chatOnlyTestModel],
      healthCheckModel: chatOnlyTestModel,
      status: 'active',
      schedulable: true
    }, access)
    assert.equal(chatOnlyAccount.clientCompatibility, 'openai_standard')
    assert.deepEqual(chatOnlyAccount.credentials.supported_endpoint_modes, ['chat_json', 'chat_sse'])
    assert(repositories.recordAccountHealthCheckSuccess(chatOnlyAccount.id, { intervalHours: 24, jitterMinutes: 0, failureThreshold: 3, statusCode: 200 }))
    const chatOnlyGatewayKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: 'AI 问答 Chat-only Key',
      groupBindings: [{ groupId: chatOnlyGroup.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(chatOnlyGatewayKey.key)
    chatOnlyGatewayKeyId = chatOnlyGatewayKey.id
    const chatOnlySelection = repositories.listOpenAIAccountsForGroupResult(chatOnlyGroup.id, access.systemAccountId, {
      requestedModel: chatOnlyTestModel,
      requestedEndpointFamily: 'chat_completions'
    })
    assert.equal(chatOnlySelection.accounts.length, 1, `Chat-only 账户应进入调度候选：${JSON.stringify(chatOnlySelection.diagnostics)}`)
    assert.deepEqual(chatOnlySelection.accounts[0]?.supportedEndpointModes, ['chat_json', 'chat_sse'])
  }
  const session = repositories.createSession('sys_admin', 1)
  const cookie = `juhe_ai_session=${session.token}`
  if (Number(process.env.JUHE_AI_CHAT_UI_BULK_MESSAGES ?? 0) > 0) {
    await seedBulkChatMessages(gatewayKey.id, Math.min(5000, Number(process.env.JUHE_AI_CHAT_UI_BULK_MESSAGES)))
  }
  databaseModule.closeStorageDatabases()

  const port = await freePort()
  const baseUrl = `http://127.0.0.1:${port}`
  backend = startBackend(port)
  await waitForReady(baseUrl, cookie, backend)

  const keys = await apiJson<{ data: Array<{ id: string }> }>(baseUrl, '/__aisys__/api/my-chat/api-keys', cookie)
  assert(keys.data.some((item) => item.id === gatewayKey.id), 'AI 问答应列出当前用户自己的可用 API Key')
  const invalidCreate = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations`, { method: 'POST', headers: { cookie, 'content-type': 'application/json' }, body: '{}' })
  assert.equal(invalidCreate.status, 400, 'AI 问答参数校验失败必须返回 400 而不是全局 500')
  const created = await apiJson<{ data: { id: string } }>(baseUrl, '/__aisys__/api/my-chat/conversations', cookie, { apiKeyId: gatewayKey.id })
  const conversationId = created.data.id
  const models = await apiJson<{ data: Array<{ id: string; supportedApiProtocols: string[] }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/models`, cookie)
  const selectedModel = models.data.find((item) => item.id === testModel)
  assert(selectedModel, 'AI 问答模型列表应来自绑定 Key 的真实网关 /v1/models')
  assert(selectedModel.supportedApiProtocols.includes('responses'), '聊天模型能力必须包含当前 API Key 实际可用的 Responses 路由')
  assert(models.data.every((item) => realCredential?.models.includes(item.id) ?? item.id === testModel), '聊天模型列表不得暴露当前账户实际不支持的网关目录模型')

  const streamResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
    method: 'POST',
    headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream', 'x-trace-id': 'chat-mock-trace-responses' },
    body: JSON.stringify({ clientMessageId: 'mock-client-1', content: realCredential ? '请用一句中文回答：7天聊天记录保留策略的核心目的是什么？' : '请返回 Mock Markdown', model: testModel })
  })
  const streamText = await streamResponse.text()
  assert.equal(streamResponse.status, 200, streamText)
  assert.match(streamText, /event: message\.started/)
  assert.match(streamText, /event: message\.delta/)
  if (!realCredential) {
    assert.match(streamText, /event: tool\.started/)
    assert.match(streamText, /event: tool\.completed/)
    assert.match(streamText, /"delta":"Mock "/)
    assert.match(streamText, /Markdown/)
  }
  assert.match(streamText, /event: message\.completed/)
  if (!realCredential) {
    assert(chatOnlyGatewayKeyId)
    assert(chatOnlyGroupId)
    const chatConversation = await apiJson<{ data: { id: string } }>(baseUrl, '/__aisys__/api/my-chat/conversations', cookie, { apiKeyId: chatOnlyGatewayKeyId })
    const chatModels = await apiJson<{ data: Array<{ id: string; supportedApiProtocols: string[] }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${chatConversation.data.id}/models`, cookie)
    const chatOnlyModel = chatModels.data.find((item) => item.id === chatOnlyTestModel)
    assert(chatOnlyModel, 'Chat-only 会话必须列出绑定账户模型')
    assert.deepEqual(chatOnlyModel.supportedApiProtocols, ['chat_completions'], '聊天模型能力必须只暴露当前 API Key 真正可用的协议')
    const chatStreamResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${chatConversation.data.id}/stream`, {
      method: 'POST',
      headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream', 'x-trace-id': 'chat-mock-trace-chat' },
      body: JSON.stringify({ clientMessageId: 'mock-client-chat-only', content: '请返回 Chat Markdown', model: chatOnlyTestModel })
    })
    const chatStreamText = await chatStreamResponse.text()
    assert.equal(chatStreamResponse.status, 200, chatStreamText)
    const chatBody = upstreamBodies.find((body) => Array.isArray(body.messages))
    assert(chatBody, `Chat Completions 请求必须命中 mock 上游，当前路径 ${JSON.stringify(upstreamPaths)}`)
    const chatMessages = Array.isArray(chatBody?.messages) ? chatBody.messages as Array<{ role?: string; content?: string }> : []
    assert.equal(chatMessages.filter((message) => message.role === 'system').length, 1, 'Chat Completions 必须且只能注入一条 system 消息')
    assert.equal(chatMessages[0]?.content, buildChatSystemInstructions({ toolsEnabled: false }).text)
    assert.equal(Object.hasOwn(chatBody ?? {}, 'instructions'), false, 'Chat Completions 不得发送 Responses instructions 字段')
    assert.equal(Object.hasOwn(chatBody ?? {}, 'tools'), false, 'Chat-only 模型不得发送 Responses Hosted Tools')
    assert.match(chatStreamText, /event: message\.completed/, 'Chat Completions 全链路必须完成')
  }
  const duplicateResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
    method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({
      clientMessageId: 'mock-client-1',
      content: '重复请求必须在协议与图片校验前命中',
      contentBlocks: [{ type: 'input_image', assetId: `chat_asset_${'a'.repeat(32)}` }],
      model: chatOnlyTestModel
    })
  })
  assert.equal(duplicateResponse.status, 409, '相同 clientMessageId 必须返回冲突且不再次调用模型')
  if (!realCredential) {
    const expectedInstructions = buildChatSystemInstructions({ toolsEnabled: true }).text
    const observedInstructions = upstreamBodies[0]?.instructions
    const observedTools = Array.isArray(upstreamBodies[0]?.tools) ? upstreamBodies[0].tools as Array<{ type?: string }> : []
    assert.deepEqual({
      instructionsMatch: observedInstructions === expectedInstructions,
      toolDisciplineCount: typeof observedInstructions === 'string' ? observedInstructions.match(/重复调用名称相同/g)?.length ?? 0 : 0,
      webSearchToolCount: observedTools.filter((tool) => tool.type === 'web_search').length,
      upstreamCalls: upstreamAuthorizations.length
    }, {
      instructionsMatch: true,
      toolDisciplineCount: 1,
      webSearchToolCount: 1,
      upstreamCalls: 2
    }, 'Responses 必须唯一注入工具提示，且 AI 问答双协议只能产生预期上游请求')
    assert.deepEqual(upstreamAuthorizations, ['Bearer sk-chat-upstream', 'Bearer sk-chat-only-upstream'], 'AI 问答双协议都必须经过网关并使用各自 AI 账户凭据访问上游')
    assert.deepEqual(upstreamPaths.slice(0, 2), ['POST /v1/responses', 'POST /v1/chat/completions'], 'Mock AI 必须真实覆盖 Responses 与 Chat Completions 两条 HTTP 路径')
    assert.deepEqual(upstreamTraceIds.slice(0, 2), ['chat-mock-trace-responses', 'chat-mock-trace-chat'], 'Chat route 必须把外层 trace 原样传给内部网关')
  }

  const stored = await apiJson<{ data: Array<{ id: string; turnId: string; sequenceNo: number; clientMessageId?: string; role: string; status: string; contentText: string; traceId?: string; contentBlocks: Array<{ type: string; id?: string; status?: string; text?: string; assetId?: string; order?: number }> }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
  assert.deepEqual(stored.data.map((item) => [item.role, item.status]), [['user', 'completed'], ['assistant', 'completed']])
  assert.equal(stored.data[1]?.traceId, 'chat-mock-trace-responses', '聊天消息 trace 必须与网关 usage/audit trace 一致')
  if (!realCredential) {
    assert.deepEqual(stored.data[1].contentBlocks.map((block) => [block.type, block.id, block.status]), [['tool_call', 'search_1', 'completed']])
    assert.match(stored.data[1].contentText, /\|项目\|结果\|/)
    assert.match(stored.data[1].contentText, /\$E=mc\^2\$/)
    assert.match(stored.data[1].contentText, /```mermaid/)

    const outOfRangeCursor = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/messages?beforeSequenceNo=2147483648`, { headers: { cookie } })
    const outOfRangePayload = parseOptionalJson(await outOfRangeCursor.text())
    assert.deepEqual([outOfRangeCursor.status, outOfRangePayload?.code], [400, 'chat_invalid_request'], '消息游标超过 PostgreSQL int4 必须在 HTTP 边界拒绝')

    const upstreamCallsBeforeConflict = upstreamAuthorizations.length
    const longReplaceTurnId = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({ clientMessageId: 'mock-client-long-replace', replaceTurnId: 'x'.repeat(101), content: '过长替换参数', model: testModel })
    })
    const longReplacePayload = parseOptionalJson(await longReplaceTurnId.text())
    assert.deepEqual([longReplaceTurnId.status, longReplacePayload?.code, upstreamAuthorizations.length], [400, 'chat_invalid_request', upstreamCallsBeforeConflict], 'replaceTurnId 最多 100 字符且校验失败不能调用上游')
    const replaceConflict = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({ clientMessageId: 'mock-client-replace-conflict', replaceTurnId: 'turn_missing', content: '冲突草稿', contentBlocks: [{ type: 'input_text', text: '冲突草稿' }], model: testModel })
    })
    const replaceConflictPayload = parseOptionalJson(await replaceConflict.text())
    assert.deepEqual([replaceConflict.status, replaceConflictPayload?.code, upstreamAuthorizations.length], [409, 'chat_replace_conflict', upstreamCallsBeforeConflict], '替换冲突必须返回稳定机器码且不能调用上游')

    const originalTurnId = stored.data[0]!.turnId
    const originalSequenceNumbers = stored.data.map((item) => item.sequenceNo)
    const upstreamCallsBeforeReplace = upstreamAuthorizations.length
    const replaceResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({
        clientMessageId: 'mock-client-replace-success',
        replaceTurnId: originalTurnId,
        content: '修正后的 Markdown 问题',
        contentBlocks: [{ type: 'input_text', text: '修正后的 Markdown 问题' }],
        model: testModel
      })
    })
    const replaceStream = await replaceResponse.text()
    assert.equal(replaceResponse.status, 200, replaceStream)
    assert.match(replaceStream, /event: message\.started/)
    assert.match(replaceStream, /event: message\.completed/)
    assert.equal(upstreamAuthorizations.length, upstreamCallsBeforeReplace + 1, '成功替换只能调用一次上游')
    const replaced = await apiJson<typeof stored>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
    assert.equal(replaced.data.length, 2, '替换后旧轮次必须完整消失')
    assert.equal(replaced.data.some((item) => item.turnId === originalTurnId), false)
    assert.deepEqual(replaced.data.map((item) => item.sequenceNo), originalSequenceNumbers, '新轮次必须复用旧轮次的两个序号')
    assert.equal(replaced.data[0]?.contentText, '修正后的 Markdown 问题')
    assert.deepEqual(replaced.data[0]?.contentBlocks, [{ type: 'input_text', text: '修正后的 Markdown 问题', order: 0 }])

    const imageConversation = await apiJson<{ data: { id: string } }>(baseUrl, '/__aisys__/api/my-chat/conversations', cookie, { apiKeyId: gatewayKey.id })
    const imageAssets = await Promise.all(Array.from({ length: 4 }, (_, index) => uploadChatImage(
      baseUrl,
      imageConversation.data.id,
      cookie,
      `交错图片-${index + 1}.png`
    )))
    assert.equal(new Set(imageAssets.map((asset) => asset.id)).size, 4, '每次 multipart 上传必须创建独立图片资产')
    const imageContentResponse = await fetch(
      `${baseUrl}/__aisys__/api/my-chat/conversations/${imageConversation.data.id}/assets/${imageAssets[0]!.id}/content`,
      { headers: { cookie } }
    )
    assert.equal(imageContentResponse.status, 200)
    assert.match(String(imageContentResponse.headers.get('content-type')), /^image\/(?:png|jpeg|webp)$/)
    assert((await imageContentResponse.arrayBuffer()).byteLength > 0, '已上传图片必须可通过私有内容接口读取')
    const imageResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${imageConversation.data.id}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({
        clientMessageId: 'mock-client-image-markers',
        content: '图片前\n[图片]\n图片后',
        contentBlocks: [
          { type: 'input_text', text: '图片 1 前' },
          { type: 'input_image', assetId: imageAssets[0]!.id },
          { type: 'input_text', text: '图片 2 前' },
          { type: 'input_image', assetId: imageAssets[1]!.id },
          { type: 'input_text', text: '图片 3 前' },
          { type: 'input_image', assetId: imageAssets[2]!.id },
          { type: 'input_text', text: '图片 4 前' },
          { type: 'input_image', assetId: imageAssets[3]!.id },
          { type: 'input_text', text: '图片 4 后' }
        ],
        model: testModel
      })
    })
    assert.equal(imageResponse.status, 200, await imageResponse.text())
    const imageStored = await apiJson<typeof stored>(baseUrl, `/__aisys__/api/my-chat/conversations/${imageConversation.data.id}/messages`, cookie)
    assert.deepEqual(imageStored.data[0]?.contentBlocks, [
      { type: 'input_text', text: '图片 1 前', order: 0 },
      { type: 'input_image', assetId: imageAssets[0]!.id, order: 1 },
      { type: 'input_text', text: '图片 2 前', order: 2 },
      { type: 'input_image', assetId: imageAssets[1]!.id, order: 3 },
      { type: 'input_text', text: '图片 3 前', order: 4 },
      { type: 'input_image', assetId: imageAssets[2]!.id, order: 5 },
      { type: 'input_text', text: '图片 4 前', order: 6 },
      { type: 'input_image', assetId: imageAssets[3]!.id, order: 7 },
      { type: 'input_text', text: '图片 4 后', order: 8 }
    ], '4 张图片与前后文字交错的 9 个块必须通过 HTTP 契约并保存文本与资产顺序')
    assert.equal(JSON.stringify(imageStored.data[0]?.contentBlocks).includes('data:image/'), false, '图片 Data URL 不得落库')
    const { createSqliteDatabaseClient } = await import('../../storage/database-client.js')
    const { getChatAsset } = await import('../../storage/chat-assets.repository.js')
    const chatClient = createSqliteDatabaseClient(databaseModule.getChatDatabase())
    for (let attempt = 0; attempt < 40; attempt += 1) {
      const observed = await Promise.all(imageAssets.map((asset) => getChatAsset(chatClient, {
        assetId: asset.id,
        systemAccountId: 'sys_admin',
        conversationId: imageConversation.data.id
      })))
      if (observed.every((asset) => asset?.observationStatus === 'ready')) break
      await new Promise((resolveWait) => setTimeout(resolveWait, 50))
    }
    const observedAssets = await Promise.all(imageAssets.map((asset) => getChatAsset(chatClient, {
      assetId: asset.id,
      systemAccountId: 'sys_admin',
      conversationId: imageConversation.data.id
    })))
    assert(observedAssets.every((asset) => asset?.observationStatus === 'ready' && asset.observation?.summary), '首轮完成后必须异步生成四张图片的隐藏说明')
    const followupResponse = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${imageConversation.data.id}/stream`, {
      method: 'POST', headers: { cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
      body: JSON.stringify({ clientMessageId: 'mock-client-image-followup', content: '你还记得这四张图片吗？', model: testModel })
    })
    assert.equal(followupResponse.status, 200, await followupResponse.text())
    const followupRequest = [...upstreamBodies].reverse().find((body) => body.stream === true && JSON.stringify(body).includes('你还记得这四张图片吗'))
    assert.ok(followupRequest, '图片后续对话必须再次进入上游')
    assert.match(JSON.stringify(followupRequest), /一张用于回归测试的交错图片/, '后续上下文必须在原位置使用隐藏图片说明')
    assert.doesNotMatch(JSON.stringify(followupRequest), /data:image\//, '图片说明完成后不得再次把历史图片 Base64 发给模型')
  } else {
    assert(stored.data[1].contentText.trim().length > 0, '真实模型必须返回非空中文回答')
    await verifyRealContextCompression({ baseUrl, cookie, conversationId, gatewayKeySecret: String(gatewayKey.key), model: testModel })
    await verifyRealImageMemory({ baseUrl, cookie, apiKeyId: gatewayKey.id, model: testModel })
  }
  console.log(`AI 问答 ${realCredential ? '真实模型' : 'Mock AI'} 全链路回归通过：登录态、Key 绑定、模型列表、现有网关、流式事件和消息终态均正确`)
  if (process.env.JUHE_AI_CHAT_UI_KEEP_ALIVE === '1') {
    console.log(`CHAT_UI_URL=${baseUrl}/__aisys__/my-chat`)
    console.log('CHAT_UI_LOGIN=admin/admin')
    console.log(`CHAT_UI_COOKIE=${cookie}`)
    await new Promise<void>((resolveStop) => {
      process.once('SIGINT', resolveStop)
      process.once('SIGTERM', resolveStop)
    })
  }
} finally {
  await stopProcess(backend)
  await closeServer(upstream)
  databaseModule.closeStorageDatabases()
  await removeTempRoot(tempRoot)
}

function readRealCredential(path: string): { baseUrl: string; apiKey: string; models: string[] } {
  const lines = readFileSync(path, 'utf8').split(/\r?\n/).map((line) => line.trim()).filter(Boolean)
  const baseUrl = lines.find((line) => /^https?:\/\//i.test(line))
  const apiKey = lines.find((line) => /^sk-/i.test(line))
  const models = [...new Set(lines.flatMap((line) => line.match(/gpt-[\w.-]+/gi) ?? []))]
  if (!baseUrl || !apiKey || models.length === 0) throw new Error('真实模型凭据文件缺少 Base URL、API Key 或模型列表')
  return { baseUrl: baseUrl.replace(/\/$/, ''), apiKey, models }
}

async function seedBulkChatMessages(apiKeyId: string, count: number): Promise<void> {
  const { createSqliteDatabaseClient } = await import('../../storage/database-client.js')
  const { createChatConversation } = await import('../../storage/chat.repository.js')
  const client = createSqliteDatabaseClient(databaseModule.getChatDatabase())
  const conversation = await createChatConversation(client, {
    id: 'chat_bulk_conversation', systemAccountId: 'sys_admin', apiKeyId, apiKeyNameSnapshot: 'AI 问答 Mock Key', now: '2099-01-01T00:00:00.000Z'
  })
  await client.transaction(async (tx) => {
    for (let index = 1; index <= count; index += 1) {
      await tx.execute(`
        INSERT INTO chat_messages (
          id, conversation_id, system_account_id, turn_id, sequence_no, client_message_id,
          role, status, content_text, content_bytes, model, created_at, completed_at, expires_at
        ) VALUES (?, ?, 'sys_admin', ?, ?, ?, ?, 'completed', ?, ?, 'gpt-5.5', '2099-01-01T00:00:00.000Z', '2099-01-01T00:00:00.000Z', '2099-01-08T00:00:00.000Z')
      `, [`bulk_${index}`, conversation.id, `bulk_turn_${Math.ceil(index / 2)}`, index, index % 2 === 1 ? `bulk_client_${index}` : null, index % 2 === 1 ? 'user' : 'assistant', `合成消息 ${index}`, Buffer.byteLength(`合成消息 ${index}`, 'utf8')])
    }
    await tx.execute(`UPDATE chat_conversations SET next_sequence_no = ?, title = '5000 条虚拟列表测试' WHERE id = ?`, [count + 1, conversation.id])
  })
}

function createMockUpstream(): http.Server {
  return http.createServer((req, res) => {
    upstreamPaths.push(`${req.method ?? ''} ${req.url ?? ''}`)
    if (req.method !== 'POST' || (req.url !== '/v1/responses' && req.url !== '/v1/chat/completions')) { res.writeHead(404).end(); return }
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      upstreamAuthorizations.push(String(req.headers.authorization ?? ''))
      upstreamTraceIds.push(String(req.headers['x-trace-id'] ?? ''))
      const body = JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown> & { stream?: boolean; tools?: Array<{ type?: string }> }
      upstreamBodies.push(body)
      if (body.stream === false) {
        const purpose = String(req.headers['x-juhe-ai-purpose'] ?? '')
        const outputText = purpose === 'chat_context_compaction'
          ? JSON.stringify({ durableMemory: ['测试记忆'], currentGoal: '继续测试', constraints: [], decisions: [], completed: [], pending: ['继续测试'], importantToolResults: [], imageMemories: [], recentUserIntent: '继续测试', uncertainties: [] })
          : JSON.stringify({ summary: '一张用于回归测试的交错图片', ocr: [], objects: ['测试图片'], questionRelevantFacts: ['图片用于验证多模态顺序'], uncertainties: [] })
        res.writeHead(200, { 'content-type': 'application/json' })
        res.end(JSON.stringify({ id: 'resp_background_mock', output_text: outputText, output: [{ type: 'message', content: [{ type: 'output_text', text: outputText }] }] }))
        return
      }
      assert.equal(body.stream, true)
      if (req.url === '/v1/chat/completions') {
        const created = Math.floor(Date.now() / 1000)
        const chunk = { id: 'chatcmpl_chat_mock', object: 'chat.completion.chunk', created, model: chatOnlyTestModel, choices: [{ index: 0, delta: { role: 'assistant', content: 'Chat **Markdown**' }, finish_reason: null }] }
        const done = { id: 'chatcmpl_chat_mock', object: 'chat.completion.chunk', created, model: chatOnlyTestModel, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }], usage: { prompt_tokens: 8, completion_tokens: 2, total_tokens: 10 } }
        res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache' })
        res.end(`data: ${JSON.stringify(chunk)}\n\ndata: ${JSON.stringify(done)}\n\ndata: [DONE]\n\n`)
        return
      }
      assert(body.tools?.some((tool) => tool.type === 'web_search'))
      res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8', 'cache-control': 'no-cache' })
      writeMockResponseEvent(res, 'response.output_item.added', { type: 'response.output_item.added', item: { id: 'search_1', type: 'web_search_call', status: 'in_progress' } })
      writeMockResponseEvent(res, 'response.output_item.done', { type: 'response.output_item.done', item: { id: 'search_1', type: 'web_search_call', status: 'completed' } })
      writeMockResponseEvent(res, 'response.output_text.delta', { type: 'response.output_text.delta', delta: 'Mock ' })
      setTimeout(() => {
        const richMarkdown = '**Markdown**\n\n|项目|结果|\n|---|---|\n|流式|通过|\n\n公式：$E=mc^2$\n\n```mermaid\ngraph LR\nA-->B\n```'
        writeMockResponseEvent(res, 'response.output_text.delta', { type: 'response.output_text.delta', delta: richMarkdown })
        writeMockResponseEvent(res, 'response.completed', { type: 'response.completed', response: { id: 'resp_mock', status: 'completed', usage: { input_tokens: 8, output_tokens: 4, total_tokens: 12 } } })
        res.end()
      }, 20)
    })
  })
}

async function uploadChatImage(
  baseUrl: string,
  conversationId: string,
  cookie: string,
  filename: string,
  label?: string
): Promise<{ id: string; fileName: string; mimeType: string; width: number; height: number; byteSize: number }> {
  const png = label
    ? await sharp(Buffer.from(`<svg width="900" height="320" xmlns="http://www.w3.org/2000/svg"><rect width="900" height="320" fill="white"/><text x="450" y="185" font-family="Arial" font-size="92" font-weight="700" text-anchor="middle" fill="black">${label}</text></svg>`)).png().toBuffer()
    : await sharp({ create: { width: 32, height: 24, channels: 4, background: { r: 38, g: 132, b: 255, alpha: 1 } } }).png().toBuffer()
  const form = new FormData()
  form.append('file', new Blob([png], { type: 'image/png' }), filename)
  const response = await fetch(`${baseUrl}/__aisys__/api/my-chat/conversations/${conversationId}/assets`, {
    method: 'POST',
    headers: { cookie },
    body: form
  })
  const text = await response.text()
  assert.equal(response.status, 201, `图片上传 HTTP ${response.status}: ${text}`)
  const payload = JSON.parse(text) as { data: { id: string; fileName: string; mimeType: string; width: number; height: number; byteSize: number } }
  assert.match(payload.data.id, /^chat_asset_[a-f0-9]{32}$/)
  assert.equal(payload.data.fileName, filename)
  assert(payload.data.width > 0 && payload.data.height > 0 && payload.data.byteSize > 0)
  return payload.data
}

async function verifyRealContextCompression(input: {
  baseUrl: string
  cookie: string
  conversationId: string
  gatewayKeySecret: string
  model: string
}): Promise<void> {
  const { createSqliteDatabaseClient } = await import('../../storage/database-client.js')
  const { acceptChatTurn, completeChatTurn } = await import('../../storage/chat.repository.js')
  const { compactChatContextOnce } = await import('../../modules/chat/chat-context-compaction.js')
  const { loadChatModelContext } = await import('../../storage/chat-context.repository.js')
  const client = createSqliteDatabaseClient(databaseModule.getChatDatabase())
  const memoryCode = 'MEMORY-4821'
  const now = new Date()
  const acceptedMemory = await acceptChatTurn(client, {
    conversationId: input.conversationId,
    systemAccountId: 'sys_admin',
    clientMessageId: `real-memory-${Date.now()}`,
    userContent: `请牢牢记住验收代号 ${memoryCode}，后面我会再问。${'这是用于真实压缩验收的背景资料。'.repeat(400)}`,
    model: input.model,
    now: new Date(now.getTime() + 1_000).toISOString(),
    storageQuotaBytes: 2 * 1024 * 1024 * 1024
  })
  await completeChatTurn(client, {
    conversationId: input.conversationId,
    systemAccountId: 'sys_admin',
    turnId: acceptedMemory.turnId,
    assistantContent: `已经记录验收代号 ${memoryCode}。`,
    finishReason: 'stop',
    traceId: 'real-memory-seed',
    now: new Date(now.getTime() + 2_000).toISOString()
  })
  const acceptedTail = await acceptChatTurn(client, {
    conversationId: input.conversationId,
    systemAccountId: 'sys_admin',
    clientMessageId: `real-tail-${Date.now()}`,
    userContent: '最近一轮只用于确认 checkpoint 后仍保留原文尾部。',
    model: input.model,
    now: new Date(now.getTime() + 3_000).toISOString(),
    storageQuotaBytes: 2 * 1024 * 1024 * 1024
  })
  await completeChatTurn(client, {
    conversationId: input.conversationId,
    systemAccountId: 'sys_admin',
    turnId: acceptedTail.turnId,
    assistantContent: '最近一轮原文尾部已准备。',
    finishReason: 'stop',
    traceId: 'real-tail-seed',
    now: new Date(now.getTime() + 4_000).toISOString()
  })
  const compacted = await compactChatContextOnce({
    client,
    conversationId: input.conversationId,
    systemAccountId: 'sys_admin',
    apiKeySecret: input.gatewayKeySecret,
    gatewayBaseUrl: input.baseUrl,
    model: input.model,
    protocol: 'responses',
    effectiveContextLimitTokens: 200_000
  })
  assert.equal(compacted.status, 'installed', `真实模型上下文压缩失败：${JSON.stringify(compacted)}`)
  const context = await loadChatModelContext(client, {
    conversationId: input.conversationId,
    systemAccountId: 'sys_admin',
    now: new Date(now.getTime() + 5_000).toISOString(),
    maxRows: 100,
    maxBytes: 4 * 1024 * 1024
  })
  assert.match(JSON.stringify(context?.entries), new RegExp(memoryCode), '真实模型 checkpoint 必须保留早期验收代号')
  assert.equal(context?.suffix.length, 2, '真实模型压缩后必须保留最近一整轮原文')
  const response = await fetch(`${input.baseUrl}/__aisys__/api/my-chat/conversations/${input.conversationId}/stream`, {
    method: 'POST',
    headers: { cookie: input.cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId: `real-recall-${Date.now()}`, content: '我之前要求你牢牢记住的验收代号是什么？只回答代号。', model: input.model })
  })
  const stream = await response.text()
  assert.equal(response.status, 200, stream)
  assert.match(await latestAssistantContent(input.baseUrl, input.cookie, input.conversationId), new RegExp(memoryCode), '真实模型必须能在压缩后回忆早期关键信息')
}

async function verifyRealImageMemory(input: { baseUrl: string; cookie: string; apiKeyId: string; model: string }): Promise<void> {
  const imageCode = 'VISION-731'
  const conversation = await apiJson<{ data: { id: string } }>(input.baseUrl, '/__aisys__/api/my-chat/conversations', input.cookie, { apiKeyId: input.apiKeyId })
  const asset = await uploadChatImage(input.baseUrl, conversation.data.id, input.cookie, '真实图片记忆.png', imageCode)
  const first = await fetch(`${input.baseUrl}/__aisys__/api/my-chat/conversations/${conversation.data.id}/stream`, {
    method: 'POST',
    headers: { cookie: input.cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({
      clientMessageId: `real-image-${Date.now()}`,
      content: '读取图片中央的大号验收代号，只回答代号。',
      contentBlocks: [{ type: 'input_text', text: '读取图片中央的大号验收代号，只回答代号。' }, { type: 'input_image', assetId: asset.id }],
      model: input.model
    })
  })
  const firstStream = await first.text()
  assert.equal(first.status, 200, firstStream)
  assert.match(await latestAssistantContent(input.baseUrl, input.cookie, conversation.data.id), new RegExp(imageCode), '真实模型必须识别上传图片中的验收代号')
  const { createSqliteDatabaseClient } = await import('../../storage/database-client.js')
  const { getChatAsset } = await import('../../storage/chat-assets.repository.js')
  const chatClient = createSqliteDatabaseClient(databaseModule.getChatDatabase())
  for (let attempt = 0; attempt < 400; attempt += 1) {
    const stored = await getChatAsset(chatClient, { assetId: asset.id, systemAccountId: 'sys_admin', conversationId: conversation.data.id })
    if (stored?.observationStatus === 'ready') break
    await new Promise((resolveWait) => setTimeout(resolveWait, 250))
  }
  const observed = await getChatAsset(chatClient, { assetId: asset.id, systemAccountId: 'sys_admin', conversationId: conversation.data.id })
  assert.equal(observed?.observationStatus, 'ready', `真实图片隐藏说明未完成：${observed?.observationStatus}`)
  assert.match(JSON.stringify(observed?.observation), new RegExp(imageCode), '真实图片隐藏说明必须保留图片验收代号')
  const followup = await fetch(`${input.baseUrl}/__aisys__/api/my-chat/conversations/${conversation.data.id}/stream`, {
    method: 'POST',
    headers: { cookie: input.cookie, 'content-type': 'application/json', accept: 'text/event-stream' },
    body: JSON.stringify({ clientMessageId: `real-image-followup-${Date.now()}`, content: '刚才图片里的验收代号是什么？只回答代号。', model: input.model })
  })
  const followupStream = await followup.text()
  assert.equal(followup.status, 200, followupStream)
  assert.match(await latestAssistantContent(input.baseUrl, input.cookie, conversation.data.id), new RegExp(imageCode), '真实模型必须通过隐藏图片说明回忆图片内容')
}

async function latestAssistantContent(baseUrl: string, cookie: string, conversationId: string): Promise<string> {
  const messages = await apiJson<{ data: Array<{ role: string; contentText: string; sequenceNo: number }> }>(baseUrl, `/__aisys__/api/my-chat/conversations/${conversationId}/messages`, cookie)
  return [...messages.data].reverse().find((message) => message.role === 'assistant')?.contentText ?? ''
}

function writeMockResponseEvent(res: http.ServerResponse, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
}

function startBackend(port: number): ChildProcess {
  return spawn('pnpm', ['--filter', 'juhe-ai-backend', 'exec', 'tsx', 'src/server.ts'], {
    cwd: projectRoot,
    env: { ...process.env, NODE_ENV: '', JUHE_AI_HOST: '127.0.0.1', JUHE_AI_PORT: String(port), JUHE_AI_DB_SERVICE_HTTP_HOST: '127.0.0.1', JUHE_AI_DB_SERVICE_HTTP_PORT: '0', JUHE_AI_DATABASE_PATH: runtimeConfig.databasePath, JUHE_AI_CHAT_DATABASE_PATH: runtimeConfig.chatDatabasePath, JUHE_AI_CHAT_ASSETS_ROOT: runtimeConfig.chatAssetsRoot, JUHE_AI_DATASET_DATABASE_PATH: runtimeConfig.datasetDatabasePath, JUHE_AI_USAGE_CATALOG_DATABASE_PATH: runtimeConfig.usageCatalogDatabasePath, JUHE_AI_STATS_DATABASE_PATH: runtimeConfig.statsDatabasePath, JUHE_AI_USAGE_SHARD_ROOT: runtimeConfig.usageShardRoot, JUHE_AI_CODEX_CONTEXT_ROOT: runtimeConfig.codexContextRoot, JUHE_AI_CODEX_CONTEXT_STATE_SHARD_ROOT: runtimeConfig.codexContextStateShardRoot, JUHE_AI_SECRET: runtimeConfig.secret, JUHE_AI_ALLOW_PRIVATE_UPSTREAM_BASE_URLS: 'true', JUHE_AI_LOG_LEVEL: 'warn', JUHE_AI_LOG_CONSOLE_ENABLED: 'false', JUHE_AI_LOG_FILE_ENABLED: 'false' },
    shell: process.platform === 'win32', stdio: ['ignore', 'pipe', 'pipe']
  })
}

async function apiJson<T>(baseUrl: string, path: string, cookie: string, body?: unknown): Promise<T> {
  const response = await fetch(`${baseUrl}${path}`, { method: body === undefined ? 'GET' : 'POST', headers: { cookie, ...(body === undefined ? {} : { 'content-type': 'application/json' }) }, body: body === undefined ? undefined : JSON.stringify(body) })
  const text = await response.text()
  assert.equal(response.ok, true, `${path} HTTP ${response.status}: ${text}`)
  return JSON.parse(text) as T
}
function parseOptionalJson(text: string): { code?: unknown; message?: unknown } | undefined { try { return JSON.parse(text) as { code?: unknown; message?: unknown } } catch { return undefined } }
async function waitForReady(baseUrl: string, cookie: string, child: ChildProcess): Promise<void> { const start = Date.now(); while (Date.now() - start < 30_000) { if (child.exitCode !== null) throw new Error(`临时后端退出：${child.exitCode}`); try { const response = await fetch(`${baseUrl}/__aisys__/api/auth/me`, { headers: { cookie } }); if (response.ok) return } catch {} await sleep(200) } throw new Error('临时后端等待超时') }
async function freePort(): Promise<number> { return new Promise((resolvePort, reject) => { const server = net.createServer(); server.once('error', reject); server.listen(0, '127.0.0.1', () => { const address = server.address(); if (!address || typeof address === 'string') { reject(new Error('无法分配端口')); return } const port = address.port; server.close((error) => error ? reject(error) : resolvePort(port)) }) }) }
function onceListening(server: http.Server): Promise<void> { return new Promise((resolveListen, reject) => { server.once('listening', resolveListen); server.once('error', reject) }) }
function serverPort(server: http.Server): number { const address = server.address(); if (!address || typeof address === 'string') throw new Error('Mock 端口不可用'); return address.port }
function sleep(ms: number): Promise<void> { return new Promise((resolveSleep) => setTimeout(resolveSleep, ms)) }
async function closeServer(server?: http.Server): Promise<void> { if (!server?.listening) return; await new Promise<void>((resolveClose) => server.close(() => resolveClose())) }
async function stopProcess(child?: ChildProcess): Promise<void> {
  if (!child || child.exitCode !== null) return
  if (process.platform === 'win32' && child.pid) {
    await new Promise<void>((resolveKill) => {
      const killer = spawn('taskkill', ['/pid', String(child.pid), '/t', '/f'], { stdio: 'ignore' })
      killer.once('error', () => resolveKill())
      killer.once('exit', () => resolveKill())
    })
  } else child.kill('SIGTERM')
  await Promise.race([new Promise<void>((resolveExit) => child.once('exit', () => resolveExit())), sleep(5000)])
  await sleep(500)
}
async function removeTempRoot(path: string): Promise<void> {
  for (let attempt = 0; attempt < 10; attempt += 1) {
    try { rmSync(path, { recursive: true, force: true }); return } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== 'EBUSY' || attempt === 9) throw error
      await sleep(300)
    }
  }
}
