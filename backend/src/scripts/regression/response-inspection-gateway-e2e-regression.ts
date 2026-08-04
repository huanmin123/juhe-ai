import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { gzipSync } from 'node:zlib'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import express from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import {
  GPT_OPENAI_V1_PROFILE_ID,
  GPT_VENDOR_CODE,
  OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID,
  OPENAI_COMPATIBLE_PROVIDER_CODE,
  OPENAI_PROTOCOL_CODE
} from '../../domain/provider-protocol.js'
import { captureGatewayRawBody } from '../../modules/gateway/request/body-middleware.js'
import { logger } from '../../shared/logger.js'

type ScenarioName =
  | 'chat_json'
  | 'chat_malformed'
  | 'chat_sse'
  | 'responses_json'
  | 'responses_sse'
  | 'stream_requested_json'
  | 'codex_compaction_sse'
  | 'codex_compaction_interrupted_sse'
  | 'codex_incomplete_sse'
  | 'codex_broken_gzip_sse'
  | 'codex_encrypted_content_recovery_sse'
  | 'codex_encrypted_content_recovery_exhausted_sse'

interface UpstreamHit {
  path: string
  authorization: string
  bodyText: string
}

const tempRoot = resolve(tmpdir(), `juhe-ai-response-inspection-gateway-e2e-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'response-inspection-gateway-e2e.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'response-inspection-gateway-e2e-secret'
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
  responseInspectionPolicies,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  sqliteReadWorkerPool
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/response-inspection-policy.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../storage/sqlite-read-worker-pool.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const upstreamHits: UpstreamHit[] = []
const requestedScenario = process.env.JUHE_AI_RESPONSE_INSPECTION_SCENARIO ?? process.argv[2]

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)

try {
  gatewayCache.clearGatewayRuntimeCache()
  let upstreamServer: http.Server | undefined
  let appServer: http.Server | undefined
  try {
    upstreamServer = createMockOpenAIUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    for (const providerCode of [OPENAI_COMPATIBLE_PROVIDER_CODE, GPT_VENDOR_CODE]) {
      responseInspectionPolicies.createResponseInspectionPolicy({
        name: `回归广告污染文本 ${providerCode}`,
        enabled: true,
        priority: 1,
        scopeType: 'provider',
        protocolCode: OPENAI_PROTOCOL_CODE,
        providerCode,
        match: {
          outputTextIncludes: ['公益服务器压力很大', 'dc.hhhl.cc', 'UniverseFederation']
        },
        action: 'retry_next_account',
        notes: 'response inspection gateway e2e regression'
      })
    }

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    if (requestedScenario === 'codex_encrypted_content_recovery_sse') {
      await runCodexEncryptedContentRecoveryScenario(baseUrl, upstreamBaseUrl)
    } else if (requestedScenario === 'codex_encrypted_content_recovery_exhausted_sse') {
      await runCodexEncryptedContentRecoveryScenario(baseUrl, upstreamBaseUrl, true)
    } else if (requestedScenario === 'codex_compaction_sse') {
      await runScenario(baseUrl, upstreamBaseUrl, 'codex_compaction_sse')
    } else if (requestedScenario === 'codex_compaction_interrupted_sse') {
      await runCodexCompactionInterruptedScenario(baseUrl, upstreamBaseUrl)
    } else {
      await runScenario(baseUrl, upstreamBaseUrl, 'chat_json')
      await runScenario(baseUrl, upstreamBaseUrl, 'chat_malformed')
      await runScenario(baseUrl, upstreamBaseUrl, 'chat_sse')
      await runScenario(baseUrl, upstreamBaseUrl, 'responses_json')
      await runScenario(baseUrl, upstreamBaseUrl, 'responses_sse')
      await runScenario(baseUrl, upstreamBaseUrl, 'stream_requested_json')
      await runScenario(baseUrl, upstreamBaseUrl, 'codex_compaction_sse')
      await runCodexCompactionInterruptedScenario(baseUrl, upstreamBaseUrl)
      await runScenario(baseUrl, upstreamBaseUrl, 'codex_incomplete_sse')
      await runScenario(baseUrl, upstreamBaseUrl, 'codex_broken_gzip_sse')
      await runCodexBrokenGzipExhaustedScenario(baseUrl, upstreamBaseUrl)
      await runCodexEncryptedContentRecoveryScenario(baseUrl, upstreamBaseUrl)
      await runCodexEncryptedContentRecoveryScenario(baseUrl, upstreamBaseUrl, true)
    }

    console.log('response inspection gateway e2e regression passed')
  } finally {
    await closeServer(appServer)
    await closeServer(upstreamServer)
  }
} finally {
  accountSideEffects.clearGatewayLocalAccountSuppressionsForTest()
  usageRecordQueue.clearUsageRecordQueueForTest()
  auditLogQueue.clearAuditLogQueueForTest()
  await sqliteReadWorkerPool.closeSqliteReadWorkerPool()
  databaseModule.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

async function runScenario(baseUrl: string, upstreamBaseUrl: string, scenario: ScenarioName): Promise<void> {
  upstreamHits.length = 0
  const accountProvider = providerForScenario(scenario)
  const group = repositories.createGroup({
    name: `响应检查 E2E ${scenario}`,
    providerCode: accountProvider.providerCode,
    enabled: true
  }, access)
  const pollutedAccount = repositories.createAccount({
    providerCode: accountProvider.providerCode,
    providerProtocolProfileId: accountProvider.providerProtocolProfileId,
    name: `响应检查污染账号 ${scenario}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-upstream-polluted-${scenario}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    priority: 0
  }, access)
  const cleanAccount = repositories.createAccount({
    providerCode: accountProvider.providerCode,
    providerProtocolProfileId: accountProvider.providerProtocolProfileId,
    name: `响应检查干净账号 ${scenario}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-upstream-clean-${scenario}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['chat_json', 'chat_sse', 'responses_json', 'responses_sse']
    },
    groupId: group.id,
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    priority: 10
  }, access)
  activateAccount(pollutedAccount.id)
  activateAccount(cleanAccount.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: `响应检查 E2E Key ${scenario}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')

  const endpoint = scenario.startsWith('chat') || scenario === 'stream_requested_json'
    ? '/v1/chat/completions'
    : '/v1/responses'
  const stream = scenario === 'chat_sse'
    || scenario === 'responses_sse'
    || scenario === 'stream_requested_json'
    || isCodexScenario(scenario)
  const headers: Record<string, string> = {
    authorization: `Bearer ${apiKey.key}`,
    'content-type': 'application/json'
  }
  if (isCodexScenario(scenario)) {
    headers.accept = 'text/event-stream'
    headers['x-codex-turn-metadata'] = JSON.stringify({
      turn_id: `turn_${scenario}`,
      session_id: `session_${scenario}`,
      thread_id: `thread_${scenario}`
    })
  }
  const response = await fetch(`${baseUrl}${endpoint}`, {
    method: 'POST',
    headers,
    body: JSON.stringify(requestBodyForScenario(scenario, stream))
  })
  const responseText = await response.text()
  if (scenario === 'chat_malformed') {
    assert.equal(response.status, 502, `畸形 2xx 协议响应必须暴露为网关协议错误：${responseText}`)
    assert.equal(upstreamHits.length, 1, '畸形 2xx 协议响应不得因内置校验切换账号')
    assert.equal(JSON.parse(responseText).error?.code, 'upstream_protocol_error', '畸形 2xx 必须返回可识别的协议错误码')
    const runtimeSnapshot = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()
    assert.equal(runtimeSnapshot[pollutedAccount.id], undefined, '通用客户端显式策略未命中时不得写账户运行态')
    return
  }
  if (scenario === 'codex_incomplete_sse') {
    assert.equal(response.status, 200, `Codex incomplete 无用户规则时不得被内部改写：${responseText}`)
    assert.equal(upstreamHits.length, 1, 'Codex incomplete 无用户规则时不得触发内部切号')
    assert.match(responseText, /response\.incomplete/, `Codex incomplete 应保持协议事件：${responseText}`)
    assert.match(responseText, /mock incomplete primary/, `Codex incomplete 应保持上游原始协议载荷：${responseText}`)
    const runtimeSnapshot = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()
    assert.equal(runtimeSnapshot[pollutedAccount.id], undefined, 'Codex incomplete 无用户规则时不得写账户运行态')
    return
  }
  assert.equal(response.status, 200, `${scenario} 应在显式污染拦截策略命中后切到干净账号成功，实际 HTTP ${response.status}: ${responseText}`)
  assert.equal(upstreamHits.length, 2, `${scenario} 应先命中污染账号再服务端切到干净账号：${JSON.stringify({ upstreamHits, responseText })}`)
  assert.equal(upstreamHits[0]?.authorization, `Bearer sk-upstream-polluted-${scenario}`, `${scenario} 第一次请求应命中污染账号`)
  assert.equal(upstreamHits[1]?.authorization, `Bearer sk-upstream-clean-${scenario}`, `${scenario} 第二次请求应命中干净账号`)
  assert.equal(upstreamHits.some((hit) => hit.bodyText.includes('公益服务器压力很大')), false, `${scenario} 客户端请求体不应携带污染文本`)
  assert(!responseText.includes('公益服务器压力很大'), `${scenario} 最终响应不应透出污染广告`)
  assert(!responseText.includes('dc.hhhl.cc'), `${scenario} 最终响应不应透出污染链接`)
  if (scenario === 'codex_compaction_sse') {
    assert(upstreamHits[0]?.bodyText.includes('compaction_trigger'), `${scenario} 上游请求应保留 compact trigger`)
    assert(!responseText.includes('bad compact visible text'), `${scenario} 坏账号 compact output 不应泄露给客户端：${responseText}`)
    assert(!responseText.includes('item_bad_compaction_missing_content'), `${scenario} 坏账号形状不合法的 compaction output item 不应泄露给客户端：${responseText}`)
    assert(responseText.includes('mock-clean-compaction'), `${scenario} 最终响应应来自干净 compact 账号：${responseText}`)
    assert(responseText.includes('"type":"compaction"'), `${scenario} 最终响应必须包含 Codex 可接受的 compaction item：${responseText}`)
  } else if (scenario === 'codex_broken_gzip_sse') {
    assert(!responseText.includes('response.failed'), `${scenario} 坏账号破损 gzip 不应转换成失败事件泄露给客户端：${responseText}`)
    assert(!responseText.includes('upstream_retryable_error'), `${scenario} 坏账号破损 gzip 不应泄露客户端重试错误码：${responseText}`)
    assert(!responseText.includes('incorrect header check'), `${scenario} zlib 解码细节不应泄露给客户端：${responseText}`)
    assert(!responseText.includes('error decoding response body'), `${scenario} Codex 传输解码错误不应泄露给客户端：${responseText}`)
    assert(responseText.includes(`clean ${scenario}`), `${scenario} 最终响应应来自干净账号：${responseText}`)
    assert(responseText.includes('response.completed'), `${scenario} 最终响应应完整完成：${responseText}`)
    assert.equal(response.headers.get('content-encoding'), null, `${scenario} 网关转发已解压 SSE 时不得保留 content-encoding`)
    assert.equal(response.headers.get('content-length'), null, `${scenario} 网关转发已解压 SSE 时不得保留上游 content-length`)
  } else {
    assert(responseText.includes(`clean ${scenario}`), `${scenario} 最终响应应来自干净账号：${responseText}`)
  }
  if (stream) {
    assert.match(response.headers.get('content-type') ?? '', /text\/event-stream|application\/json/, `${scenario} 流式或上游 JSON 回退应有明确 content-type`)
  } else {
    assert.match(response.headers.get('content-type') ?? '', /application\/json/, `${scenario} 非流式客户端应收到 JSON`)
  }
}

async function runCodexBrokenGzipExhaustedScenario(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const scenario: ScenarioName = 'codex_broken_gzip_sse'
  upstreamHits.length = 0
  const accountProvider = providerForScenario(scenario)
  const group = repositories.createGroup({
    name: '响应检查 E2E codex broken gzip exhausted',
    providerCode: accountProvider.providerCode,
    enabled: true
  }, access)
  const exhaustedAccount = repositories.createAccount({
    providerCode: accountProvider.providerCode,
    providerProtocolProfileId: accountProvider.providerProtocolProfileId,
    name: '响应检查破损 gzip 单账号',
    type: 'api_key',
    credentials: {
      api_key: `sk-upstream-polluted-${scenario}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    groupId: group.id,
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    priority: 0
  }, access)
  activateAccount(exhaustedAccount.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '响应检查 E2E Key codex broken gzip exhausted',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')

  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: 'turn_codex_broken_gzip_exhausted',
        session_id: 'session_codex_broken_gzip_exhausted',
        thread_id: 'thread_codex_broken_gzip_exhausted'
      })
    },
    body: JSON.stringify(requestBodyForScenario(scenario, true))
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, `codex_broken_gzip_exhausted 应返回 Codex 可重试 SSE，实际 HTTP ${response.status}: ${responseText}`)
  assert.equal(upstreamHits.length, 1, 'codex_broken_gzip_exhausted 只有单账号时不应重复请求同一坏账号')
  assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/, 'codex_broken_gzip_exhausted 应返回 SSE')
  assert.equal(response.headers.get('content-encoding'), null, 'codex_broken_gzip_exhausted 失败 SSE 不应保留 content-encoding')
  assert(responseText.includes('response.failed'), `codex_broken_gzip_exhausted 应返回失败事件：${responseText}`)
  assert(responseText.includes('upstream_retryable_error'), `codex_broken_gzip_exhausted 应返回 Codex 可重试错误码：${responseText}`)
  assert(responseText.includes('上游流式响应在输出前失败，请重试'), `codex_broken_gzip_exhausted 应返回统一客户端可见消息：${responseText}`)
  assert(!responseText.includes('incorrect header check'), `codex_broken_gzip_exhausted 不应泄露 zlib 解码细节：${responseText}`)
  assert(!responseText.includes('error decoding response body'), `codex_broken_gzip_exhausted 不应泄露 Codex 传输解码文本：${responseText}`)
  assert(!responseText.includes('not a valid gzip'), `codex_broken_gzip_exhausted 不应泄露 mock 上游原始破损内容：${responseText}`)
}

async function runCodexCompactionInterruptedScenario(baseUrl: string, upstreamBaseUrl: string): Promise<void> {
  const scenario: ScenarioName = 'codex_compaction_interrupted_sse'
  upstreamHits.length = 0
  repositories.updateSettings({ textStreamIdleTimeoutSeconds: 1 })
  const accountProvider = providerForScenario(scenario)
  const group = repositories.createGroup({
    name: '响应检查 E2E codex compact interruption',
    providerCode: accountProvider.providerCode,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: accountProvider.providerCode,
    providerProtocolProfileId: accountProvider.providerProtocolProfileId,
    name: '响应检查 compact interruption 单账号',
    type: 'api_key',
    credentials: {
      api_key: `sk-upstream-clean-${scenario}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    groupId: group.id,
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    priority: 0
  }, access)
  activateAccount(account.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '响应检查 E2E Key codex compact interruption',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: `turn_${scenario}`,
        session_id: `session_${scenario}`,
        thread_id: `thread_${scenario}`
      })
    },
    body: JSON.stringify(requestBodyForScenario(scenario, true))
  })
  const responseText = await response.text()
  const elapsedMs = Date.now() - startedAt
  assert.equal(response.status, 200, `${scenario} 应返回 Codex SSE 终态，实际 HTTP ${response.status}: ${responseText}`)
  assert.equal(upstreamHits.length, 1, `${scenario} 单账号不得切换或重复请求：${JSON.stringify(upstreamHits)}`)
  assert(elapsedMs >= 1_000, `${scenario} 应等待超过普通 1s idle 后再收到上游中断，实际 ${elapsedMs}ms`)
  assert.match(responseText, /data: \{"type":"juhe_ai\.keepalive"\}\n\n/, `${scenario} 客户端应收到 Codex data keepalive：${responseText}`)
  assert.equal((responseText.match(/event: response\.failed\n/g) ?? []).length, 1, `${scenario} 必须只发送一个 response.failed：${responseText}`)
  assert.doesNotMatch(responseText, /response\.completed/, `${scenario} 中断不得发送 response.completed：${responseText}`)
  assert.doesNotMatch(responseText, /raw-upstream-compaction-chunk|upstream-compaction-secret/, `${scenario} 不得泄露上游原始内容：${responseText}`)
  repositories.updateSettings({ textStreamIdleTimeoutSeconds: 30 })
}

async function runCodexEncryptedContentRecoveryScenario(
  baseUrl: string,
  upstreamBaseUrl: string,
  expectRecoveryExhausted = false
): Promise<void> {
  const scenario: ScenarioName = expectRecoveryExhausted
    ? 'codex_encrypted_content_recovery_exhausted_sse'
    : 'codex_encrypted_content_recovery_sse'
  upstreamHits.length = 0
  const accountProvider = providerForScenario(scenario)
  const group = repositories.createGroup({
    name: 'Codex 密文恢复 E2E',
    providerCode: accountProvider.providerCode,
    enabled: true
  }, access)
  const account = repositories.createAccount({
    providerCode: accountProvider.providerCode,
    providerProtocolProfileId: accountProvider.providerProtocolProfileId,
    name: 'Codex 密文恢复单账号',
    type: 'api_key',
    credentials: {
      api_key: `sk-upstream-clean-${scenario}`,
      base_url: upstreamBaseUrl,
      supported_endpoint_modes: ['responses_json', 'responses_sse']
    },
    groupId: group.id,
    schedulable: true,
    supportedModels: ['gpt-5.5'],
    priority: 0
  }, access)
  activateAccount(account.id)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: 'Codex 密文恢复 E2E Key',
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '回归 API Key 未返回明文密钥')

  const requestBody = {
    model: 'gpt-5.5',
    stream: true,
    input: [
      {
        type: 'reasoning',
        summary: [],
        encrypted_content: 'fixture-rejected-reasoning-content'
      },
      {
        type: 'agent_message',
        author: '/root/subtask',
        content: [
          { type: 'input_text', text: 'subtask plaintext remains available' },
          { type: 'encrypted_content', encrypted_content: 'fixture-rejected-agent-message-content' }
        ]
      },
      {
        type: 'message',
        role: 'user',
        content: [{ type: 'input_text', text: `run ${scenario}` }]
      }
    ]
  }
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      authorization: `Bearer ${apiKey.key}`,
      'content-type': 'application/json',
      accept: 'text/event-stream',
      'x-codex-turn-metadata': JSON.stringify({
        turn_id: `turn_${scenario}`,
        session_id: `session_${scenario}`,
        thread_id: `thread_${scenario}`
      })
    },
    body: JSON.stringify(requestBody)
  })
  const responseText = await response.text()
  assert.equal(response.status, 200, `密文恢复应保留 Codex SSE 协议响应：${responseText}`)
  assert.equal(upstreamHits.length, 2, `精确密文错误只能额外重试一次：${JSON.stringify(upstreamHits)}`)
  assert.equal(upstreamHits[0]?.authorization, `Bearer sk-upstream-clean-${scenario}`)
  assert.equal(upstreamHits[1]?.authorization, `Bearer sk-upstream-clean-${scenario}`)
  const firstInput = JSON.parse(upstreamHits[0]?.bodyText ?? '{}').input as Array<Record<string, unknown>>
  const secondInput = JSON.parse(upstreamHits[1]?.bodyText ?? '{}').input as Array<Record<string, unknown>>
  assert.equal(firstInput.some((item) => item.type === 'reasoning' && typeof item.encrypted_content === 'string'), true, '首次请求必须保留原始密文')
  assert.equal(secondInput.some((item) => item.type === 'reasoning' && typeof item.encrypted_content === 'string'), false, '恢复请求不得保留 reasoning 密文')
  const recoveredAgentMessage = secondInput.find((item) => item.type === 'agent_message')
  assert.deepEqual(recoveredAgentMessage?.content, [{ type: 'input_text', text: 'subtask plaintext remains available' }], '恢复请求必须仅移除 agent_message 密文')
  assert.doesNotMatch(responseText, /resp-rejected-encrypted-content/, '上游首个 response.created 不得泄露给下游')
  if (expectRecoveryExhausted) {
    assert.doesNotMatch(responseText, /resp-rejected-cleaned-content/, '第二次上游失败的 response.created 不得泄露给下游')
    assert.match(responseText, /response\.failed/, '第二次失败必须由网关转换为本地失败事件')
    assert.doesNotMatch(responseText, /response\.completed/, '清洗后的第二次失败不得伪造完成事件')
    return
  }
  assert.match(responseText, /clean codex_encrypted_content_recovery_sse/, `恢复请求必须完成：${responseText}`)
  assert.match(responseText, /response\.completed/, `恢复请求必须返回完成终态：${responseText}`)
}

function activateAccount(accountId: string): void {
  assert.equal(repositories.recordAccountHealthCheckSuccess(accountId, {
    intervalHours: 12,
    jitterMinutes: 0,
    failureThreshold: 3,
    statusCode: 200
  }), true, `后台健康检查应激活待检查账户 ${accountId}`)
}

function requestBodyForScenario(scenario: ScenarioName, stream: boolean): Record<string, unknown> {
  if (scenario === 'codex_compaction_sse' || scenario === 'codex_compaction_interrupted_sse') {
    return {
      model: 'gpt-5.5',
      input: [
        {
          type: 'message',
          role: 'user',
          content: [{ type: 'input_text', text: `run ${scenario}` }]
        },
        { type: 'compaction_trigger' }
      ],
      stream
    }
  }
  if (scenario === 'codex_incomplete_sse') {
    return {
      model: 'gpt-5.5',
      input: `run ${scenario}`,
      stream
    }
  }
  if (scenario === 'codex_broken_gzip_sse') {
    return {
      model: 'gpt-5.5',
      input: `run ${scenario}`,
      stream
    }
  }
  if (scenario.startsWith('responses')) {
    return {
      model: 'gpt-5.5',
      input: `run ${scenario}`,
      stream
    }
  }
  return {
    model: 'gpt-5.5',
    messages: [{ role: 'user', content: `run ${scenario}` }],
    stream
  }
}

function createMockOpenAIUpstream(): http.Server {
  return http.createServer((req, res) => {
    const chunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => chunks.push(chunk))
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8')
      const authorization = String(req.headers.authorization ?? '')
      const path = req.url?.split('?', 1)[0] ?? ''
      upstreamHits.push({ path, authorization, bodyText })
      const scenario = scenarioFromAuthorization(authorization)
      const polluted = authorization.includes('polluted')
      if (path === '/v1/chat/completions') {
        if (scenario === 'chat_sse' && !polluted) {
          sendChatSse(res, scenario, false)
          return
        }
        if (scenario === 'chat_sse' && polluted) {
          sendChatSse(res, scenario, true)
          return
        }
        sendChatJson(res, scenario, polluted)
        return
      }
      if (path === '/v1/responses') {
        if (
          scenario === 'codex_encrypted_content_recovery_sse'
          || scenario === 'codex_encrypted_content_recovery_exhausted_sse'
        ) {
          sendCodexEncryptedContentRecoverySse(res, bodyText, scenario)
          return
        }
        if (scenario === 'codex_compaction_sse') {
          sendCodexCompactionSse(res, polluted)
          return
        }
        if (scenario === 'codex_compaction_interrupted_sse') {
          sendCodexCompactionInterruptedSse(res)
          return
        }
        if (scenario === 'codex_incomplete_sse' && polluted) {
          sendCodexIncompleteSse(res)
          return
        }
        if (scenario === 'codex_broken_gzip_sse' && polluted) {
          sendBrokenGzipResponsesSse(res)
          return
        }
        if (scenario === 'codex_broken_gzip_sse') {
          sendGzipResponsesSse(res, scenario)
          return
        }
        if (scenario === 'codex_incomplete_sse') {
          sendResponsesSse(res, scenario, false)
          return
        }
        if (scenario === 'responses_sse') {
          sendResponsesSse(res, scenario, polluted)
          return
        }
        sendResponsesJson(res, scenario, polluted)
        return
      }
      res.writeHead(404, { 'content-type': 'application/json; charset=utf-8' })
      res.end(JSON.stringify({ error: { message: 'mock upstream path not found' } }))
    })
  })
}

function scenarioFromAuthorization(authorization: string): ScenarioName {
  const match = authorization.match(/sk-upstream-(?:polluted|clean)-([a-z_]+)/)
  assert(match?.[1], `无法从上游 Authorization 识别回归场景：${authorization}`)
  return match[1] as ScenarioName
}

function isCodexScenario(scenario: ScenarioName): boolean {
  return scenario === 'codex_compaction_sse'
    || scenario === 'codex_compaction_interrupted_sse'
    || scenario === 'codex_incomplete_sse'
    || scenario === 'codex_broken_gzip_sse'
    || scenario === 'codex_encrypted_content_recovery_sse'
    || scenario === 'codex_encrypted_content_recovery_exhausted_sse'
}

function providerForScenario(scenario: ScenarioName): { providerCode: string; providerProtocolProfileId: string } {
  return isCodexScenario(scenario)
    ? { providerCode: GPT_VENDOR_CODE, providerProtocolProfileId: GPT_OPENAI_V1_PROFILE_ID }
    : { providerCode: OPENAI_COMPATIBLE_PROVIDER_CODE, providerProtocolProfileId: OPENAI_COMPATIBLE_OPENAI_V1_PROFILE_ID }
}

function pollutedText(): string {
  return '公益服务器压力很大，欢迎加入 https://dc.hhhl.cc/chat/room/amlc1bekzi TG https://t.me/UniverseFederation'
}

function sendChatJson(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  if (scenario === 'chat_malformed' && polluted) {
    res.end(JSON.stringify({ id: 'chatcmpl-chat_malformed', object: 'chat.completion', choices: [] }))
    return
  }
  res.end(JSON.stringify({
    id: `chatcmpl-${scenario}`,
    object: 'chat.completion',
    choices: [
      {
        index: 0,
        message: { role: 'assistant', content: polluted ? pollutedText() : `clean ${scenario}` },
        finish_reason: 'stop'
      }
    ],
    usage: { prompt_tokens: 3, completion_tokens: 4, total_tokens: 7 }
  }))
}

function sendChatSse(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-${scenario}`,
    object: 'chat.completion.chunk',
    choices: [{ index: 0, delta: { content: polluted ? pollutedText() : `clean ${scenario}` } }]
  })}\n\n`)
  res.write(`data: ${JSON.stringify({
    id: `chatcmpl-${scenario}`,
    object: 'chat.completion.chunk',
    choices: [{ index: 0, delta: {}, finish_reason: 'stop' }],
    usage: { prompt_tokens: 3, completion_tokens: 4, total_tokens: 7 }
  })}\n\n`)
  res.end('data: [DONE]\n\n')
}

function sendResponsesJson(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
  const outputText = polluted ? pollutedText() : `clean ${scenario}`
  res.end(JSON.stringify({
    id: `resp-${scenario}`,
    object: 'response',
    status: 'completed',
    output_text: outputText,
    output: [
      {
        id: `msg-${scenario}`,
        type: 'message',
        role: 'assistant',
        status: 'completed',
        content: [{ type: 'output_text', text: outputText }]
      }
    ],
    usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
  }))
}

function sendResponsesSse(res: http.ServerResponse, scenario: ScenarioName, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`event: response.output_text.delta\ndata: ${JSON.stringify({
    type: 'response.output_text.delta',
    delta: polluted ? pollutedText() : `clean ${scenario}`
  })}\n\n`)
  res.write(`event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: {
      id: `resp-${scenario}`,
      status: 'completed',
      usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
    }
  })}\n\n`)
  res.end()
}

function sendCodexEncryptedContentRecoverySse(
  res: http.ServerResponse,
  bodyText: string,
  scenario: ScenarioName
): void {
  const requestBody = JSON.parse(bodyText) as { input?: Array<{ type?: string; encrypted_content?: unknown; content?: unknown }> }
  const rejectedReasoning = requestBody.input?.some((item) => (
    item.type === 'reasoning' && typeof item.encrypted_content === 'string'
  )) === true
  const rejectedAgentMessage = requestBody.input?.some((item) => (
    item.type === 'agent_message'
      && Array.isArray(item.content)
      && item.content.some((content) => (
        typeof content === 'object'
        && content !== null
        && (content as { type?: unknown; encrypted_content?: unknown }).type === 'encrypted_content'
        && typeof (content as { encrypted_content?: unknown }).encrypted_content === 'string'
      ))
  )) === true
  if (rejectedReasoning || rejectedAgentMessage) {
    sendCodexEncryptedContentRecoveryFailure(res, 'resp-rejected-encrypted-content')
    return
  }
  if (scenario === 'codex_encrypted_content_recovery_exhausted_sse') {
    sendCodexEncryptedContentRecoveryFailure(res, 'resp-rejected-cleaned-content')
    return
  }
  sendResponsesSse(res, 'codex_encrypted_content_recovery_sse', false)
}

function sendCodexEncryptedContentRecoveryFailure(res: http.ServerResponse, responseId: string): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`event: response.created\ndata: ${JSON.stringify({
    type: 'response.created',
    response: { id: responseId, status: 'in_progress' }
  })}\n\n`)
  res.end(`event: error\ndata: ${JSON.stringify({
    type: 'error',
    code: 'thinking_signature_invalid',
    message: 'Encrypted function output content could not be decrypted or decoded.'
  })}\n\n`)
}

function sendCodexCompactionSse(res: http.ServerResponse, polluted: boolean): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  if (polluted) {
    res.write(`event: response.output_item.done\ndata: ${JSON.stringify({
      type: 'response.output_item.done',
      output_index: 0,
      item: {
        id: 'item_bad_message',
        type: 'message',
        status: 'completed',
        content: [{ type: 'output_text', text: 'bad compact visible text' }]
      }
    })}\n\n`)
    res.write(`event: response.output_item.done\ndata: ${JSON.stringify({
      type: 'response.output_item.done',
      output_index: 1,
      item: {
        id: 'item_bad_compaction_missing_content',
        type: 'compaction',
        status: 'completed',
        encrypted_content: null
      }
    })}\n\n`)
  } else {
    res.write(`event: response.output_item.done\ndata: ${JSON.stringify({
      type: 'response.output_item.done',
      output_index: 0,
      item: {
        id: 'item_clean_compaction',
        type: 'compaction',
        status: 'completed',
        encrypted_content: 'mock-clean-compaction'
      }
    })}\n\n`)
  }
  res.write(`event: response.completed\ndata: ${JSON.stringify({
    type: 'response.completed',
    response: {
      id: polluted ? 'resp_bad_codex_compaction' : 'resp_clean_codex_compaction',
      status: 'completed'
    }
  })}\n\n`)
  res.end()
}

function sendCodexCompactionInterruptedSse(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(': raw-upstream-compaction-chunk\n\n')
  const interruptionTimer = setTimeout(() => res.destroy(new Error('upstream-compaction-secret')), 1_200)
  interruptionTimer.unref()
}

function sendCodexIncompleteSse(res: http.ServerResponse): void {
  res.writeHead(200, { 'content-type': 'text/event-stream; charset=utf-8' })
  res.write(`event: response.incomplete\ndata: ${JSON.stringify({
    type: 'response.incomplete',
    response: {
      id: 'resp_incomplete_primary',
      status: 'incomplete',
      incomplete_details: {
        reason: 'mock incomplete primary'
      }
    }
  })}\n\n`)
  res.end()
}

function sendBrokenGzipResponsesSse(res: http.ServerResponse): void {
  const body = Buffer.from('this is not a valid gzip payload', 'utf8')
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'content-encoding': 'gzip',
    'content-length': String(body.length)
  })
  res.end(body)
}

function sendGzipResponsesSse(res: http.ServerResponse, scenario: ScenarioName): void {
  const body = Buffer.from([
    `event: response.output_text.delta\ndata: ${JSON.stringify({
      type: 'response.output_text.delta',
      delta: `clean ${scenario}`
    })}`,
    `event: response.completed\ndata: ${JSON.stringify({
      type: 'response.completed',
      response: {
        id: `resp-${scenario}`,
        status: 'completed',
        usage: { input_tokens: 3, output_tokens: 4, total_tokens: 7 }
      }
    })}`,
    ''
  ].join('\n\n'), 'utf8')
  const compressed = gzipSync(body)
  res.writeHead(200, {
    'content-type': 'text/event-stream; charset=utf-8',
    'content-encoding': 'gzip',
    'content-length': String(compressed.length)
  })
  res.end(compressed)
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
