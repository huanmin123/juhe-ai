import assert from 'node:assert/strict'
import http from 'node:http'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  GEMINI_NATIVE_V1BETA_PROFILE_ID,
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE
} from '../../domain/provider-protocol.js'
import type { UpstreamAccount } from '../../modules/gateway/protocols/openai-v1/route-helpers.js'
import {
  nonStreamJsonProtocolValidationAllowed,
  protocolValidatedNonStreamResponse
} from '../../modules/gateway/response/finalization.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { gatewayAccountRuntimeKey } from '../../modules/gateway/runtime/account-runtime-keys.js'
import { gatewayHotQualityModelFamily } from '../../modules/gateway/runtime/hot-quality-runtime.service.js'
import type { HotQualityScope } from '../../modules/gateway/runtime/hot-quality-store.js'
import { fingerprintAccountApiKey } from '../../storage/account-api-key-rotation.js'
import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { logger } from '../../shared/logger.js'

const account = {
  id: 'multi-key-provenance-account',
  name: 'multi-key-provenance-account',
  providerCode: 'gpt',
  providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
  protocolCode: 'openai',
  protocolVersion: 'v1',
  type: 'api_key',
  selectedApiKeyFingerprint: 'key-a'
} as unknown as UpstreamAccount

const chatRequest = request('/v1/chat/completions')
const responsesRequest = request('/v1/responses')

for (const responseBodyText of [
  'garbage from an untrusted upstream',
  '[]',
  '{}',
  JSON.stringify({ error: { message: 'invalid key but HTTP 200' } }),
  JSON.stringify({ choices: null }),
  JSON.stringify({ choices: [] }),
  JSON.stringify({ choices: 'not-an-array' }),
  JSON.stringify({ choices: [{}] }),
  JSON.stringify({ choices: [{ error: { message: 'invalid key envelope' } }] }),
  JSON.stringify({ choices: [{ message: { error: { message: 'invalid key message envelope' } } }] })
]) {
  assert.equal(protocolValidatedNonStreamResponse({
    req: chatRequest,
    account,
    responseBodyText,
    statusCode: 200
  }), false, `HTTP 200 非协议成功体必须保持中性：${responseBodyText}`)
}

assert.equal(protocolValidatedNonStreamResponse({
  req: chatRequest,
  account,
  responseBodyText: JSON.stringify({
    id: 'chatcmpl-ok',
    object: 'chat.completion',
    choices: [{ index: 0, message: { role: 'assistant', content: 'ok' }, finish_reason: 'stop' }]
  }),
  statusCode: 200
}), true, '结构完整的 Chat completion 才能形成 Key success 证据')

assert.equal(protocolValidatedNonStreamResponse({
  req: responsesRequest,
  account,
  responseBodyText: JSON.stringify({ id: 'resp-ok', object: 'response', status: 'completed', output: [] }),
  statusCode: 200
}), true, '结构完整的 Responses JSON 可以形成 Key success 证据')

assert.equal(protocolValidatedNonStreamResponse({
  req: responsesRequest,
  account,
  responseBodyText: JSON.stringify({ id: 'resp-failed', object: 'response', status: 'failed', output: [] }),
  statusCode: 200
}), false, 'HTTP 200 response.failed 不得形成 Key success 证据')

assert.equal(protocolValidatedNonStreamResponse({
  req: chatRequest,
  account: { ...account, selectedApiKeyFingerprint: 'key-b' },
  responseBodyText: JSON.stringify({ choices: [{ message: { content: 'ok' } }] }),
  statusCode: 500
}), false, '多 Key 场景中完整非 2xx 响应也不得清理当前 Key 状态')

const anthropicAccount = {
  ...account,
  providerCode: 'anthropic',
  providerProtocolProfileId: ANTHROPIC_ANTHROPIC_V1_PROFILE_ID,
  protocolCode: 'anthropic'
} as unknown as UpstreamAccount
const geminiAccount = {
  ...account,
  providerCode: 'gemini',
  providerProtocolProfileId: GEMINI_NATIVE_V1BETA_PROFILE_ID,
  protocolCode: 'gemini',
  protocolVersion: 'v1beta'
} as unknown as UpstreamAccount

assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: requestWithBody('/v1/chat/completions', {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: 'safe text replay' }]
  }),
  account,
  upstreamResponse: { ok: true }
}), true, '普通 Chat 文本请求允许在下游提交前进行协议结构验证和请求内切号')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: requestWithBody('/v1/chat/completions', {
    model: 'gpt-image-1',
    messages: [{ role: 'user', content: 'generate image' }]
  }),
  account,
  upstreamResponse: { ok: true }
}), true, 'Chat 入口中的图片模型请求必须与文本共用提交前协议验证和候选切换')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: requestWithBody('/v1/messages', {
    model: 'claude-sonnet-4-5',
    messages: [{ role: 'user', content: 'safe anthropic text' }]
  }),
  account: anthropicAccount,
  upstreamResponse: { ok: true }
}), true, '不含服务端工具的 Anthropic Messages 文本请求允许请求内协议切号')
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: requestWithBody('/v1/messages', {
    model: 'claude-sonnet-4-5',
    messages: [{ role: 'user', content: 'hosted tool' }],
    tools: [{ type: 'web_search_20250305', name: 'web_search' }]
  }),
  account: anthropicAccount,
  upstreamResponse: { ok: true }
}), true, 'Anthropic 服务端工具未交付有效结果时必须与普通 Messages 共用候选切换')
for (const sideEffectPath of ['/v1/images/generations', '/v1/audio/speech', '/v1/files']) {
  assert.equal(nonStreamJsonProtocolValidationAllowed({
    req: requestWithBody(sideEffectPath, { model: 'gpt-5.5', input: 'side effect' }),
    account,
    upstreamResponse: { ok: true }
  }), false, `${sideEffectPath} 尚无提交前结构校验器时不得伪装成已验证协议；HTTP/transport 失败仍由统一候选切换处理`)
}
assert.equal(nonStreamJsonProtocolValidationAllowed({
  req: requestWithBody('/v1/chat/completions', {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: 'non-2xx' }]
  }),
  account,
  upstreamResponse: { ok: false }
}), false, '非 2xx 响应不得走 2xx 协议结构验证分支')

const endpointMatrix: Array<{ label: string; req: Request; account: UpstreamAccount; body: unknown }> = [
  { label: 'chat/completions', req: chatRequest, account, body: { choices: [{ message: { content: 'ok' } }] } },
  { label: 'responses', req: responsesRequest, account, body: { id: 'resp-ok', object: 'response', status: 'completed', output: [] } },
  { label: 'messages', req: request('/v1/messages'), account: anthropicAccount, body: { id: 'msg-ok', type: 'message', content: [] } },
  { label: 'models', req: requestWithHeaders('models', {}, 'GET'), account: anthropicAccount, body: { data: [] } },
  { label: 'message_token_counting', req: request('/v1/messages/count_tokens'), account: anthropicAccount, body: { input_tokens: 1 } },
  { label: 'generate_content', req: request('/v1beta/models/gemini-2.5-pro:generateContent'), account: geminiAccount, body: { candidates: [] } },
  { label: 'interactions', req: request('/v1beta/interactions'), account: geminiAccount, body: { id: 'interaction-ok' } },
  { label: 'count_tokens', req: request('/v1beta/models/gemini-2.5-pro:countTokens'), account: geminiAccount, body: { totalTokens: 1 } },
  { label: 'embed_content', req: request('/v1beta/models/gemini-2.5-pro:embedContent'), account: geminiAccount, body: { embedding: { values: [0.1] } } },
  { label: 'embeddings', req: request('/v1/embeddings'), account, body: { data: [{ embedding: [0.1] }] } },
  { label: 'images', req: request('/v1/images/generations'), account, body: { data: [{ b64_json: 'aW1hZ2U=' }] } }
]
for (const scenario of endpointMatrix) {
  assert.equal(protocolValidatedNonStreamResponse({
    req: scenario.req,
    account: scenario.account,
    responseBodyText: JSON.stringify(scenario.body),
    statusCode: 200
  }), true, `${scenario.label} 最小有效协议结构必须形成 validated success`)
  for (const invalidBody of [{ error: { message: 'opaque' } }, { ...scenario.body as Record<string, unknown>, error: { message: 'opaque' } }]) {
    assert.equal(protocolValidatedNonStreamResponse({
      req: scenario.req,
      account: scenario.account,
      responseBodyText: JSON.stringify(invalidBody),
      statusCode: 200
    }), false, `${scenario.label} 顶层 error envelope 不得形成 validated success`)
  }
}

await runGatewayProvenanceMock()

console.log('网关协议成功来源回归通过：HTTP 200 垃圾体不增加热质量成功或确认 Key 失败，协议完整成功才可确认')

function request(path: string): Request {
  return requestWithHeaders(path, {})
}

function requestWithBody(path: string, body: Record<string, unknown>, method = 'POST'): Request {
  return {
    ...requestWithHeaders(path, {}, method),
    body
  } as Request
}

function requestWithHeaders(path: string, headers: Record<string, string>, method = 'POST'): Request {
  return {
    method,
    path,
    originalUrl: path,
    headers,
    header(name: string) {
      return headers[name.toLowerCase()]
    }
  } as Request
}

async function runGatewayProvenanceMock(): Promise<void> {
  const tempRoot = resolve(tmpdir(), `juhe-ai-protocol-success-provenance-${Date.now()}-${Math.random().toString(16).slice(2)}`)
  runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
  runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
  runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
  runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
  runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
  runtimeConfig.secret = 'gateway-protocol-success-provenance-secret'
  runtimeConfig.processRole = 'db-service'
  runtimeConfig.runtimeMode = 'standalone'
  runtimeConfig.cacheDriver = 'memory'
  runtimeConfig.runtimeStateDriver = 'memory'
  runtimeConfig.upstreamUrlSecurity.allowPrivateBaseUrls = true
  runtimeConfig.log.consoleEnabled = false
  runtimeConfig.log.fileEnabled = false
  mkdirSync(tempRoot, { recursive: true })
  logger.level = 'silent'

  const [
    { openAIGatewayRouter },
    { requestContextMiddleware },
    databaseModule,
    repositories,
    readWorkerPool,
    usageRecordQueue,
    auditLogQueue,
    apiKeyFailureGuard,
    hotQuality,
    accountCircuit,
    clientIpErrorCircuit
  ] = await Promise.all([
    import('../../modules/gateway/routes.js'),
    import('../../shared/request-context.js'),
    import('../../storage/database.js'),
    import('../../storage/repositories.js'),
    import('../../storage/sqlite-read-worker-pool.js'),
    import('../../modules/gateway/usage/record-queue.service.js'),
    import('../../modules/audit-logs/audit-log-queue.service.js'),
    import('../../modules/gateway/runtime/account-api-key-failure-guard.service.js'),
    import('../../modules/gateway/runtime/hot-quality-runtime.service.js'),
    import('../../modules/gateway/runtime/account-circuit.service.js'),
    import('../../modules/gateway/runtime/client-ip-error-circuit.service.js')
  ])

  const app = express()
  app.use(requestContextMiddleware)
  app.use('/v1', express.raw({ type: () => true, limit: '2mb' }), captureGatewayRawBody, openAIGatewayRouter)
  const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const model = 'gpt-5.5'
  const upstreamHits: string[] = []
  let upstreamServer: http.Server | undefined
  let gatewayServer: http.Server | undefined
  let selectedKeyBAccount: UpstreamAccount | undefined

  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    hotQuality.resetGatewayHotQualityRuntimeForTest()
    apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()

    upstreamServer = http.createServer((req, res) => {
      const authorization = String(req.headers.authorization ?? '')
      upstreamHits.push(authorization)
      if (authorization === 'Bearer sk-provenance-a') {
        res.writeHead(418, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({ error: { message: 'opaque-key-switch' } }))
        return
      }
      assert(selectedKeyBAccount, 'Key B 账户夹具必须在上游请求前就绪')
      for (let failureIndex = 0; failureIndex < 3; failureIndex += 1) {
        const seededFailure = apiKeyFailureGuard.recordGatewayAccountApiKeyFailureGuard(selectedKeyBAccount, {
          status: 'temporary_unavailable',
          trafficSource: 'gateway',
          source: 'mock_pre_response_key_failure'
        })
        assert.equal(seededFailure.reason, 'gateway_local_only')
      }
      assert.equal(apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(selectedKeyBAccount.id).length, 1)
      res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
      if (req.url?.includes('mode=garbage')) {
        res.end('garbage from an untrusted upstream')
        return
      }
      res.end(JSON.stringify({
        id: 'chatcmpl-provenance-ok',
        object: 'chat.completion',
        choices: [{ index: 0, message: { role: 'assistant', content: 'validated' }, finish_reason: 'stop' }]
      }))
    })
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverPort(upstreamServer)}/v1`

    const group = repositories.createGroup({ name: '协议成功来源分组', providerCode: GPT_VENDOR_CODE, enabled: true }, access)
    const accountSummary = repositories.createAccount({
      providerCode: GPT_VENDOR_CODE,
      providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID,
      name: '协议成功来源多 Key 账户',
      type: 'api_key',
      credentials: {
        api_key: 'sk-provenance-a',
        api_keys: ['sk-provenance-a', 'sk-provenance-b'],
        api_key_strategy: 'round_robin',
        base_url: upstreamBaseUrl
      },
      groupId: group.id,
      status: 'active',
      schedulable: true,
      concurrencyLimit: 4,
      priority: 0,
      supportedModels: [model]
    }, access)
    assert.equal(repositories.recordAccountHealthCheckSuccess(accountSummary.id, {
      intervalHours: 12,
      jitterMinutes: 0,
      failureThreshold: 3,
      statusCode: 200
    }), true)
    repositories.updateSettings({
      temporaryUnschedulableRetryAttempts: 0,
      temporaryUnschedulableRetryIntervalSeconds: 0,
      noAvailableAccountWaitTimeoutSeconds: 10
    })
    selectedKeyBAccount = {
      ...accountSummary,
      selectedApiKeyFingerprint: fingerprintAccountApiKey('sk-provenance-b'),
      selectedApiKeyIndex: 1,
      apiKeyRuntimeStateDisabled: false
    } as unknown as UpstreamAccount
    const gatewayApiKey = createApiKeyRecordWithRouteStrategy(repositories, {
      name: '协议成功来源网关 Key',
      groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
      status: 'active'
    }, access)
    assert(gatewayApiKey.key)

    gatewayServer = http.createServer(app)
    await listen(gatewayServer)
    const gatewayBaseUrl = `http://127.0.0.1:${serverPort(gatewayServer)}`
    const scope: HotQualityScope = {
      accountRuntimeKey: gatewayAccountRuntimeKey(accountSummary.id),
      protocolProfile: GPT_OPENAI_V1_PROFILE_ID,
      requestLane: 'text',
      modelFamily: gatewayHotQualityModelFamily(model)
    }

    const garbage = await postChat(gatewayBaseUrl, gatewayApiKey.key, model, 'garbage')
    assert.equal(garbage.status, 200, garbage.text)
    assert.equal(garbage.text, 'garbage from an untrusted upstream')
    const afterGarbage = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(scope)
    assert(afterGarbage, '未知 2xx 后必须保留热质量作用域快照')
    assert.equal(afterGarbage.window5m.completedResponses, 0, '未知 2xx 不得增加 completedResponses')
    assert.equal(afterGarbage.window5m.explicitPolicyFailures, 0, '不可信状态码不得伪造成用户显式策略失败')
    assert.equal(afterGarbage.window5m.qualityAttempts, 0, '未知 2xx 与 opaque 非 2xx 均不得进入成功率分母')
    const afterGarbageKeyState = apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(accountSummary.id)
    assert.equal(afterGarbageKeyState.length, 1, `未知 2xx 不得清理已存在的当前 Key 失败态；hits=${JSON.stringify(upstreamHits)}`)
    assert.equal(afterGarbageKeyState[0]?.keyFingerprint, selectedKeyBAccount.selectedApiKeyFingerprint)
    assert.equal(apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuard(selectedKeyBAccount), true)
    accountCircuit.resetGatewayAccountCircuitStoreForTest()
    clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()

    const valid = await postChat(gatewayBaseUrl, gatewayApiKey.key, model, 'valid')
    assert.equal(valid.status, 200, `${valid.text}; hits=${JSON.stringify(upstreamHits)}`)
    assert.match(valid.text, /validated/)
    const afterValid = await hotQuality.getGatewayHotQualityRuntime().hotQualityStore.get(scope)
    assert(afterValid, '协议有效 2xx 后必须保留热质量作用域快照')
    assert.equal(afterValid.window5m.completedResponses, 1, '协议有效 2xx 应增加 completedResponses')
    assert.equal(afterValid.window5m.qualityAttempts, 1, '协议有效 2xx 应进入成功率分母')
    const keyRuntimeStates = apiKeyFailureGuard.localAccountApiKeyRuntimeStatesForDispatch(accountSummary.id)
    assert.equal(keyRuntimeStates.length, 0, '协议有效响应才可清理当前 Key 失败态')
    assert.deepEqual(upstreamHits, [
      'Bearer sk-provenance-a',
      'Bearer sk-provenance-b',
      'Bearer sk-provenance-b'
    ], '首轮必须 A -> B 有界切换，第二轮按 round-robin 游标真实命中 B 并返回协议有效响应')
  } finally {
    apiKeyFailureGuard.clearGatewayAccountApiKeyFailureGuardsForTest()
    hotQuality.resetGatewayHotQualityRuntimeForTest()
    accountCircuit.resetGatewayAccountCircuitStoreForTest()
    clientIpErrorCircuit.clearGatewayClientIpErrorCircuitForTest()
    usageRecordQueue.clearUsageRecordQueueForTest()
    auditLogQueue.clearAuditLogQueueForTest()
    await readWorkerPool.closeSqliteReadWorkerPool()
    databaseModule.closeStorageDatabases()
    await closeServer(gatewayServer)
    await closeServer(upstreamServer)
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

async function postChat(baseUrl: string, apiKey: string, model: string, mode: 'garbage' | 'valid'): Promise<{ status: number; text: string }> {
  const response = await fetch(`${baseUrl}/v1/chat/completions?mode=${mode}`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey}`,
      'content-type': 'application/json',
      'x-forwarded-for': mode === 'garbage' ? '198.51.100.10' : '198.51.100.11'
    },
    body: JSON.stringify({ model, messages: [{ role: 'user', content: mode }], stream: false })
  })
  return { status: response.status, text: await response.text() }
}

async function listen(server: http.Server): Promise<void> {
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.once('error', rejectPromise)
    server.listen(0, '127.0.0.1', () => resolvePromise())
  })
}

function serverPort(server: http.Server): number {
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('Mock 服务未监听')
  return address.port
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server) return
  await new Promise<void>((resolvePromise) => server.close(() => resolvePromise()))
}
