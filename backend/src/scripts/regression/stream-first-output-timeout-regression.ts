import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-stream-first-output-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'stream-first-output.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'stream-first-output-records.sqlite3')
runtimeConfig.secret = 'stream-first-output-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  { openAIGatewayRouter },
  { requestContextMiddleware },
  databaseModule,
  repositories,
  apiKeyRepository,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue
] = await Promise.all([
  import('../../modules/gateway/openai-gateway.routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/api-key.repository.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/gateway-runtime-cache.service.js'),
  import('../../modules/gateway/gateway-account-side-effects.service.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

type RawBodyRequest = Request & { rawBody?: Buffer }

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
let scenarioCredentialIndex = 0

function codexStreamHeaders(apiKey: string, turnId: string): Record<string, string> {
  return {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    accept: 'text/event-stream',
    'x-codex-turn-metadata': JSON.stringify({
      turn_id: turnId,
      session_id: `session-${turnId}`,
      thread_id: `thread-${turnId}`
    })
  }
}

function genericStreamHeaders(apiKey: string): Record<string, string> {
  return {
    authorization: `Bearer ${apiKey}`,
    'content-type': 'application/json',
    accept: 'text/event-stream'
  }
}

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  try {
    settingsRepository.updateSettings({
      streamCircuitBreakerEnabled: true,
      streamRequestTimeoutSeconds: 10,
      streamIdleTimeoutSeconds: 10,
      temporaryUnschedulableRetryAttempts: 0
    })
    gatewayCache.clearGatewayRuntimeCache()

    upstreamServer = createStreamTimeoutRegressionUpstream()
    await listen(upstreamServer)
    const upstreamBaseUrl = `http://127.0.0.1:${serverAddress(upstreamServer).port}/v1`

    const noFirstChunkCredential = createScenarioCredential(upstreamBaseUrl, '首段等待')
    const firstChunkIdleCredential = createScenarioCredential(upstreamBaseUrl, '首段后空闲')
    const fragmentedSseEventCredential = createScenarioCredential(upstreamBaseUrl, '完整事件等待')
    const parserSkippedCredential = createScenarioCredential(upstreamBaseUrl, '解析跳过后原样转发')
    const missingTerminalCredential = createScenarioCredential(upstreamBaseUrl, '缺少终止事件')
    const heartbeatCredential = createScenarioCredential(upstreamBaseUrl, '心跳刷新')
    const overloadedBeforeOutputCredential = createScenarioCredential(upstreamBaseUrl, '容量错误未输出前重试')
    const slowDownCredential = createScenarioCredential(upstreamBaseUrl, 'slow_down 未输出前重试')
    const genericErrorEventCredential = createScenarioCredential(upstreamBaseUrl, '未知 error 事件默认重试')
    const contextWindowCredential = createScenarioCredential(upstreamBaseUrl, '上下文超限终止错误不改写')
    const nonCodexErrorEventCredential = createScenarioCredential(upstreamBaseUrl, '非 Codex 未输出前不改写')
    const overloadedNoBoundaryCredential = createScenarioCredential(upstreamBaseUrl, '容量错误缺少收尾边界')
    const overloadedAfterOutputCredential = createScenarioCredential(upstreamBaseUrl, '输出后容量错误不拦截')
    const outputItemThenFailureCredential = createScenarioCredential(upstreamBaseUrl, 'output item 后失败')
    const topLevelCodeMessageCredential = createScenarioCredential(upstreamBaseUrl, '顶层 code message 非失败')

    appServer = http.createServer(app)
    await listen(appServer)
    const baseUrl = `http://127.0.0.1:${serverAddress(appServer).port}`

    const startedAt = Date.now()
    const response = await fetch(`${baseUrl}/v1/responses`, {
      method: 'POST',
      headers: codexStreamHeaders(noFirstChunkCredential.apiKey.key, 'no-first-chunk'),
      body: JSON.stringify({
        model: 'gpt-4o-mini',
        input: 'no-first-chunk',
        stream: true
      })
    })
    assert.equal(response.status, 200)
    assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
    const streamText = await response.text()
    const durationMs = Date.now() - startedAt
    assert(streamText.includes('response.failed'), `客户端未收到网关失败事件：${streamText}`)
    assert(streamText.includes('未返回首段数据'), `失败事件未说明首段等待超时：${streamText}`)
    assert(streamText.includes('"code":"upstream_retryable_error"'), `首段等待超时应改写为可重试错误码：${streamText}`)
    assert(durationMs < 15000, `首段等待超时没有及时结束，耗时 ${durationMs}ms`)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const noFirstChunkFailureState = databaseModule.getDatabase()
      .prepare('SELECT stream_failure_count FROM accounts WHERE id = ?')
      .get(noFirstChunkCredential.account.id) as { stream_failure_count?: number } | undefined
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().localSuppressedAccountCount, 0, '首段前失败未产生可见输出，不应本地屏蔽账号')
    assert.equal(Number(noFirstChunkFailureState?.stream_failure_count ?? 0), 0, '首段前失败未产生可见输出，不应累计账号流失败计数')

    const firstChunkThenIdleResult = await requestFirstChunkThenIdleTimeout(baseUrl, firstChunkIdleCredential.apiKey.key)
    assert(firstChunkThenIdleResult.streamText.includes('response.created'), `客户端未收到首段上游事件：${firstChunkThenIdleResult.streamText}`)
    assert(firstChunkThenIdleResult.streamText.includes('response.failed'), `客户端未收到首段后空闲失败事件：${firstChunkThenIdleResult.streamText}`)
    assert(firstChunkThenIdleResult.streamText.includes('未返回任何新数据'), `失败事件未说明流式无新数据超时：${firstChunkThenIdleResult.streamText}`)
    assert(firstChunkThenIdleResult.streamText.includes('"code":"upstream_retryable_error"'), `首段后空闲应改写为可重试错误码：${firstChunkThenIdleResult.streamText}`)
    assert(
      firstChunkThenIdleResult.durationMs >= 900 && firstChunkThenIdleResult.durationMs < 5000,
      `首段后空闲超时没有按 1s 左右及时结束，耗时 ${firstChunkThenIdleResult.durationMs}ms`
    )

    const fragmentedSseEventResult = await requestFragmentedSseEventTimeout(baseUrl, fragmentedSseEventCredential.apiKey.key)
    assert(fragmentedSseEventResult.streamText.includes('response.failed'), `客户端未收到完整 SSE 事件等待失败事件：${fragmentedSseEventResult.streamText}`)
    assert(fragmentedSseEventResult.streamText.includes('未形成完整 SSE 事件'), `失败事件未说明完整 SSE 事件等待超时：${fragmentedSseEventResult.streamText}`)
    assert(fragmentedSseEventResult.streamText.includes('"code":"upstream_retryable_error"'), `完整 SSE 事件等待应改写为可重试错误码：${fragmentedSseEventResult.streamText}`)
    assert(
      fragmentedSseEventResult.durationMs >= 900 && fragmentedSseEventResult.durationMs < 5000,
      `完整 SSE 事件等待超时没有按 1s 左右及时结束，耗时 ${fragmentedSseEventResult.durationMs}ms`
    )
    usageRecordQueue.flushAllUsageRecordQueue()
    assertFailedUsageRecordErrorCode(fragmentedSseEventCredential.account.id, 'upstream_retryable_error')

    const parserSkippedResult = await requestParserSkippedRawForward(baseUrl, parserSkippedCredential.apiKey.key)
    assert(!parserSkippedResult.streamText.includes('response.failed'), '解析跳过后仍有原始上游数据持续到来时不应补发失败事件')
    assert(
      parserSkippedResult.durationMs >= 1200 && parserSkippedResult.durationMs < 5000,
      `解析跳过后原样转发没有持续到上游 EOF，耗时 ${parserSkippedResult.durationMs}ms`
    )

    const missingTerminalResult = await requestMissingTerminalEof(baseUrl, missingTerminalCredential.apiKey.key)
    assert(missingTerminalResult.streamText.includes('response.created'), `客户端未收到缺少终止事件场景的首段上游事件：${missingTerminalResult.streamText}`)
    assert(missingTerminalResult.streamText.includes('response.failed'), `缺少终止事件场景未收到网关失败事件：${missingTerminalResult.streamText}`)
    assert(missingTerminalResult.streamText.includes('OpenAI 终止事件前结束'), `缺少终止事件场景失败原因不正确：${missingTerminalResult.streamText}`)
    assert(missingTerminalResult.streamText.includes('"code":"upstream_retryable_error"'), `缺少终止事件应改写为可重试错误码：${missingTerminalResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const missingTerminalAccount = repositories.listAccounts().find((item) => item.id === missingTerminalCredential.account.id)
    assert.equal(missingTerminalAccount?.status, 'active', '缺少终止事件但仅有 response.created 时不应把账号置为临时不可调用')
    assert.equal(missingTerminalAccount?.streamFailureCount, 0, '缺少终止事件但未产生可见输出时不应累计账号流失败计数')

    const heartbeatThenCompletedResult = await requestHeartbeatThenCompleted(baseUrl, heartbeatCredential.apiKey.key)
    assert(heartbeatThenCompletedResult.streamText.includes('response.created'), `客户端未收到首段上游事件：${heartbeatThenCompletedResult.streamText}`)
    assert(heartbeatThenCompletedResult.streamText.includes('response.completed'), `客户端未收到完成事件：${heartbeatThenCompletedResult.streamText}`)
    assert(!heartbeatThenCompletedResult.streamText.includes('response.failed'), `上游持续心跳时不应触发失败事件：${heartbeatThenCompletedResult.streamText}`)
    assert(heartbeatThenCompletedResult.durationMs < 5000, `持续心跳后完成没有及时结束，耗时 ${heartbeatThenCompletedResult.durationMs}ms`)

    const overloadedBeforeOutputResult = await requestServerOverloadedBeforeOutput(baseUrl, overloadedBeforeOutputCredential.apiKey.key)
    assert(!overloadedBeforeOutputResult.streamText.includes('server_is_overloaded'), `未输出前不应把原始容量错误发给客户端：${overloadedBeforeOutputResult.streamText}`)
    assert(overloadedBeforeOutputResult.streamText.includes('upstream_retryable_error'), `未输出前容量错误应按通用兜底改写为可重试错误：${overloadedBeforeOutputResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const overloadedBeforeOutputAccount = repositories.listAccounts().find((item) => item.id === overloadedBeforeOutputCredential.account.id)
    assert.equal(overloadedBeforeOutputAccount?.status, 'active', '未输出前容量错误不应把账号置为临时不可调用')
    assert.equal(overloadedBeforeOutputAccount?.streamFailureCount, 0, '未输出前容量错误不应累计账号流失败计数')
    auditLogQueue.flushAllAuditLogQueue()
    await assertStreamInterceptAuditMetadata(overloadedBeforeOutputCredential.account.id, {
      upstreamErrorCode: 'server_is_overloaded',
      rewriteErrorCode: 'upstream_retryable_error',
      fallbackReason: 'before_output_stream_failure',
      outputSeen: false
    })

    const slowDownResult = await requestStreamFailureBeforeOutput(baseUrl, slowDownCredential.apiKey.key, 'slow-down-before-output')
    assert(!slowDownResult.streamText.includes('slow_down'), `未输出前 slow_down 不应把原始错误发给客户端：${slowDownResult.streamText}`)
    assert(slowDownResult.streamText.includes('upstream_retryable_error'), `未输出前 slow_down 应按通用兜底改写为可重试错误：${slowDownResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const slowDownAccount = repositories.listAccounts().find((item) => item.id === slowDownCredential.account.id)
    assert.equal(slowDownAccount?.status, 'active', '未输出前 slow_down 不应把账号置为临时不可调用')
    assert.equal(slowDownAccount?.streamFailureCount, 0, '未输出前 slow_down 不应累计账号流失败计数')

    const genericErrorEventResult = await requestStreamFailureBeforeOutput(baseUrl, genericErrorEventCredential.apiKey.key, 'generic-error-event-before-output')
    assert(!genericErrorEventResult.streamText.includes('internal_server_error'), `未知 error 事件不应把原始错误码发给客户端：${genericErrorEventResult.streamText}`)
    assert(genericErrorEventResult.streamText.includes('upstream_retryable_error'), `未知 error 事件应按可见输出前失败统一改写为可重试错误：${genericErrorEventResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const genericErrorEventAccount = repositories.listAccounts().find((item) => item.id === genericErrorEventCredential.account.id)
    assert.equal(genericErrorEventAccount?.status, 'active', '未知 error 事件未输出前不应把账号置为临时不可调用')
    assert.equal(genericErrorEventAccount?.streamFailureCount, 0, '未知 error 事件未输出前不应累计账号流失败计数')
    auditLogQueue.flushAllAuditLogQueue()
    await assertStreamInterceptAuditMetadata(genericErrorEventCredential.account.id, {
      upstreamErrorCode: 'internal_server_error',
      rewriteErrorCode: 'upstream_retryable_error',
      fallbackReason: 'before_output_stream_failure',
      outputSeen: false
    })

    const contextWindowResult = await requestStreamFailureBeforeOutput(baseUrl, contextWindowCredential.apiKey.key, 'context-window-before-output')
    assert(contextWindowResult.streamText.includes('context_length_exceeded'), `Codex 终止类错误应保留原始错误码：${contextWindowResult.streamText}`)
    assert(!contextWindowResult.streamText.includes('upstream_retryable_error'), `Codex 终止类错误不应伪造成可重试错误：${contextWindowResult.streamText}`)

    const nonCodexErrorEventResult = await requestGenericStreamFailureBeforeOutput(baseUrl, nonCodexErrorEventCredential.apiKey.key, 'generic-error-event-before-output')
    assert(nonCodexErrorEventResult.streamText.includes('internal_server_error'), `非 Codex 未输出前失败应保留原始错误码：${nonCodexErrorEventResult.streamText}`)
    assert(!nonCodexErrorEventResult.streamText.includes('upstream_retryable_error'), `非 Codex 未输出前失败不应伪造 Codex 可重试错误：${nonCodexErrorEventResult.streamText}`)

    const overloadedNoBoundaryResult = await requestStreamFailureBeforeOutput(baseUrl, overloadedNoBoundaryCredential.apiKey.key, 'server-overloaded-before-output-no-boundary')
    assert(!overloadedNoBoundaryResult.streamText.includes('server_is_overloaded'), `EOF 尾包未输出前不应把原始容量错误发给客户端：${overloadedNoBoundaryResult.streamText}`)
    assert(overloadedNoBoundaryResult.streamText.includes('upstream_retryable_error'), `EOF 尾包未输出前应按通用兜底改写为可重试错误：${overloadedNoBoundaryResult.streamText}`)

    const overloadedAfterOutputResult = await requestServerOverloadedAfterOutput(baseUrl, overloadedAfterOutputCredential.apiKey.key)
    assert(overloadedAfterOutputResult.streamText.includes('hello'), `输出后容量错误场景应保留已输出内容：${overloadedAfterOutputResult.streamText}`)
    assert(overloadedAfterOutputResult.streamText.includes('server_is_overloaded'), `输出后不应改写原始容量错误：${overloadedAfterOutputResult.streamText}`)
    assert(!overloadedAfterOutputResult.streamText.includes('upstream_retryable_error'), `输出后不应伪造可重试错误：${overloadedAfterOutputResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const overloadedAfterOutputAccount = repositories.listAccounts().find((item) => item.id === overloadedAfterOutputCredential.account.id)
    assert.equal(overloadedAfterOutputAccount?.streamFailureCount, 1, '输出后容量错误应按通用流失败累计计数')
    assertFailedUsageRecordHasTokenCost(overloadedAfterOutputCredential.account.id)

    const outputItemThenFailureResult = await requestStreamScenario(baseUrl, outputItemThenFailureCredential.apiKey.key, 'output-item-then-failure')
    assert(outputItemThenFailureResult.streamText.includes('response.output_item.added'), `output item 场景应保留已输出事件：${outputItemThenFailureResult.streamText}`)
    assert(outputItemThenFailureResult.streamText.includes('internal_server_error'), `output item 后失败不应改写原始错误：${outputItemThenFailureResult.streamText}`)
    assert(!outputItemThenFailureResult.streamText.includes('upstream_retryable_error'), `output item 后失败不应伪造可重试错误：${outputItemThenFailureResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const outputItemThenFailureAccount = repositories.listAccounts().find((item) => item.id === outputItemThenFailureCredential.account.id)
    assert.equal(outputItemThenFailureAccount?.streamFailureCount, 1, 'output item 已算可见输出，后续失败应累计账号流失败计数')

    const topLevelCodeMessageResult = await requestStreamScenario(baseUrl, topLevelCodeMessageCredential.apiKey.key, 'top-level-code-message-non-error')
    assert(topLevelCodeMessageResult.streamText.includes('response.completed'), `顶层 code/message 普通事件应继续到完成：${topLevelCodeMessageResult.streamText}`)
    assert(topLevelCodeMessageResult.streamText.includes('"code":"diagnostic_code"'), `普通事件的 code 字段应原样透传：${topLevelCodeMessageResult.streamText}`)
    assert(!topLevelCodeMessageResult.streamText.includes('upstream_retryable_error'), `普通事件顶层 code/message 不应误判为失败：${topLevelCodeMessageResult.streamText}`)

    console.log('流式超时回归通过：Codex 首段等待、首段后无新数据、完整 SSE 事件等待、解析跳过后原样转发、缺少终止事件未输出不计数、心跳刷新空闲计时、容量错误/slow_down 专属兜底、未知 error 事件兜底、Codex 终止类错误不改写、非 Codex 不伪造可重试码、输出后通用流失败计数、output item 输出判定、顶层 code/message 非失败和 EOF 尾包场景符合预期')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getDatabase().close()
      databaseModule.getRecordDatabase().close()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function createScenarioCredential(upstreamBaseUrl: string, label: string): {
  account: ReturnType<typeof repositories.createAccount>
  apiKey: ReturnType<typeof apiKeyRepository.createApiKeyRecord>
} {
  const group = repositories.createGroup({ name: `流式超时回归分组-${label}`, providerCode: 'openai', enabled: true })
  scenarioCredentialIndex += 1
  const account = repositories.createAccount({
    providerCode: 'openai',
    name: `流式超时回归账户-${label}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-stream-timeout-regression-${scenarioCredentialIndex}`,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true
  })
  const apiKey = apiKeyRepository.createApiKeyRecord({
    name: `流式超时回归 Key-${label}`,
    groupId: group.id,
    status: 'active'
  })
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  return { account, apiKey }
}

function createStreamTimeoutRegressionUpstream(): http.Server {
  return http.createServer((req, res) => {
    if (req.url !== '/v1/responses') {
      res.writeHead(200, { 'content-type': 'application/json' })
      res.end(JSON.stringify({ object: 'list', data: [] }))
      return
    }

    const bodyChunks: Buffer[] = []
    req.on('data', (chunk: Buffer) => bodyChunks.push(chunk))
    req.on('end', () => {
      let scenario = 'no-first-chunk'
      try {
        const body = JSON.parse(Buffer.concat(bodyChunks).toString('utf8')) as { input?: unknown }
        scenario = typeof body.input === 'string' ? body.input : scenario
      } catch {
      }

      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache',
        connection: 'keep-alive'
      })
      res.flushHeaders()
      if (scenario === 'no-first-chunk') {
        return
      }
      if (scenario === 'fragmented-sse-event-timeout') {
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created"')
        const chunks = [
          ',"response":{"id":"resp_regression"',
          ',"status":"in_progress"',
          ',"metadata":{"fragment":1}',
          ',"metadata":{"fragment":2}'
        ]
        let index = 0
        const interval = setInterval(() => {
          res.write(chunks[index % chunks.length])
          index += 1
        }, 200)
        res.on('close', () => {
          clearInterval(interval)
        })
        return
      }
      if (scenario === 'parser-skipped-raw-forward') {
        res.write('data: ' + 'x'.repeat(270 * 1024))
        let written = 0
        const interval = setInterval(() => {
          written += 1
          res.write('x'.repeat(1024))
          if (written >= 10) {
            clearInterval(interval)
            res.end()
          }
        }, 150)
        res.on('close', () => {
          clearInterval(interval)
        })
        return
      }
      res.write('event: response.created\n')
      res.write('data: {"type":"response.created","response":{"id":"resp_regression","status":"in_progress"}}\n\n')
      if (scenario === 'missing-terminal-eof') {
        res.end()
        return
      }
      if (scenario === 'first-chunk-then-idle') {
        return
      }
      if (scenario === 'heartbeat-then-completed') {
        const interval = setInterval(() => {
          res.write(': keep-alive\n\n')
        }, 100)
        const doneTimer = setTimeout(() => {
          clearInterval(interval)
          res.write('event: response.completed\n')
          res.write('data: {"type":"response.completed","response":{"id":"resp_regression","status":"completed","usage":{"input_tokens":1,"output_tokens":0}}}\n\n')
          res.end()
        }, 650)
        res.on('close', () => {
          clearInterval(interval)
          clearTimeout(doneTimer)
        })
        return
      }
      if (scenario === 'server-overloaded-before-output') {
        res.write('event: error\n')
        res.write('data: {"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}\n\n')
        setTimeout(() => {
          res.write('event: response.failed\n')
          res.write('data: {"type":"response.failed","response":{"id":"resp_overloaded","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}\n\n')
          res.end()
        }, 100)
        return
      }
      if (scenario === 'slow-down-before-output') {
        res.write('event: response.failed\n')
        res.write('data: {"type":"response.failed","response":{"id":"resp_slow_down","status":"failed","error":{"code":"slow_down","message":"Please slow down and try again later."}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'generic-error-event-before-output') {
        res.write('event: error\n')
        res.write('data: {"type":"error","code":"internal_server_error","message":"unexpected EOF","sequence_number":0}\n\n')
        res.end()
        return
      }
      if (scenario === 'context-window-before-output') {
        res.write('event: response.failed\n')
        res.write('data: {"type":"response.failed","response":{"id":"resp_context","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'server-overloaded-before-output-no-boundary') {
        res.write('event: response.failed\n')
        res.write('data: {"type":"response.failed","response":{"id":"resp_overloaded_no_boundary","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}')
        res.end()
        return
      }
      if (scenario === 'server-overloaded-after-output') {
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"hello"}\n\n')
        res.write('event: response.failed\n')
        res.write('data: {"type":"response.failed","response":{"id":"resp_overloaded_after_output","status":"failed","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'output-item-then-failure') {
        res.write('event: response.output_item.added\n')
        res.write('data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"call_regression","name":"tool","arguments":"{}"}}\n\n')
        res.write('event: response.failed\n')
        res.write('data: {"type":"response.failed","response":{"id":"resp_output_item_failure","status":"failed","error":{"code":"internal_server_error","message":"failed after output item"}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'top-level-code-message-non-error') {
        res.write('event: response.in_progress\n')
        res.write('data: {"type":"response.in_progress","code":"diagnostic_code","message":"not an error","response":{"id":"resp_code_message","status":"in_progress"}}\n\n')
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_code_message","status":"completed","usage":{"input_tokens":1,"output_tokens":0}}}\n\n')
        res.end()
        return
      }
    })
  })
}

async function requestFirstChunkThenIdleTimeout(baseUrl: string, apiKey: string): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 1,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, 'first-chunk-then-idle'),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'first-chunk-then-idle',
      stream: true
    })
  })
  if (response.status !== 200) {
    throw new Error(`首段后空闲场景状态码异常：${response.status} ${await response.text()}`)
  }
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  const streamText = await response.text()
  return {
    streamText,
    durationMs: Date.now() - startedAt
  }
}

async function requestFragmentedSseEventTimeout(baseUrl: string, apiKey: string): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 1,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, 'fragmented-sse-event-timeout'),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'fragmented-sse-event-timeout',
      stream: true
    })
  })
  if (response.status !== 200) {
    throw new Error(`完整 SSE 事件等待场景状态码异常：${response.status} ${await response.text()}`)
  }
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  const streamText = await response.text()
  return {
    streamText,
    durationMs: Date.now() - startedAt
  }
}

async function requestParserSkippedRawForward(baseUrl: string, apiKey: string): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 1,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, 'parser-skipped-raw-forward'),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'parser-skipped-raw-forward',
      stream: true
    })
  })
  if (response.status !== 200) {
    throw new Error(`解析跳过后原样转发场景状态码异常：${response.status} ${await response.text()}`)
  }
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  const streamText = await response.text()
  return {
    streamText,
    durationMs: Date.now() - startedAt
  }
}

async function requestMissingTerminalEof(baseUrl: string, apiKey: string): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 1,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, 'missing-terminal-eof'),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'missing-terminal-eof',
      stream: true
    })
  })
  if (response.status !== 200) {
    throw new Error(`缺少终止事件场景状态码异常：${response.status} ${await response.text()}`)
  }
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  const startedAt = Date.now()
  const streamText = await response.text()
  return {
    streamText,
    durationMs: Date.now() - startedAt
  }
}

async function requestHeartbeatThenCompleted(baseUrl: string, apiKey: string): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 1,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, 'heartbeat-then-completed'),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'heartbeat-then-completed',
      stream: true
    })
  })
  if (response.status !== 200) {
    throw new Error(`心跳刷新场景状态码异常：${response.status} ${await response.text()}`)
  }
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  const streamText = await response.text()
  return {
    streamText,
    durationMs: Date.now() - startedAt
  }
}

function captureGatewayRawBody(req: RawBodyRequest, _res: ExpressResponse, next: NextFunction): void {
  const rawBody = Buffer.isBuffer(req.body) ? Buffer.from(req.body) : Buffer.alloc(0)
  req.rawBody = rawBody
  const contentType = req.headers['content-type'] ?? ''
  if (rawBody.length > 0 && String(contentType).toLowerCase().includes('json')) {
    try {
      req.body = JSON.parse(rawBody.toString('utf8')) as unknown
    } catch {
      req.body = undefined
    }
  } else {
    req.body = undefined
  }
  next()
}

async function requestStreamFailureBeforeOutput(
  baseUrl: string,
  apiKey: string,
  scenario: string
): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 10,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, scenario),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: scenario,
      stream: true
    })
  })
  assert.equal(response.status, 200)
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  return {
    streamText: await response.text(),
    durationMs: Date.now() - startedAt
  }
}

async function requestGenericStreamFailureBeforeOutput(
  baseUrl: string,
  apiKey: string,
  scenario: string
): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 10,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: genericStreamHeaders(apiKey),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: scenario,
      stream: true
    })
  })
  assert.equal(response.status, 200)
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  return {
    streamText: await response.text(),
    durationMs: Date.now() - startedAt
  }
}

async function requestServerOverloadedBeforeOutput(
  baseUrl: string,
  apiKey: string
): Promise<{ streamText: string; durationMs: number }> {
  return requestStreamFailureBeforeOutput(baseUrl, apiKey, 'server-overloaded-before-output')
}

async function requestServerOverloadedAfterOutput(baseUrl: string, apiKey: string): Promise<{ streamText: string; durationMs: number }> {
  return requestStreamScenario(baseUrl, apiKey, 'server-overloaded-after-output')
}

async function requestStreamScenario(
  baseUrl: string,
  apiKey: string,
  scenario: string
): Promise<{ streamText: string; durationMs: number }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 10,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const startedAt = Date.now()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, scenario),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: scenario,
      stream: true
    })
  })
  assert.equal(response.status, 200)
  assert(response.headers.get('content-type')?.includes('text/event-stream'), '网关应保持 SSE content-type')
  return {
    streamText: await response.text(),
    durationMs: Date.now() - startedAt
  }
}

function assertFailedUsageRecordErrorCode(accountId: string, errorCode: string): void {
  const records = repositories.listUsageRecords(undefined, { result: 'failed', page: 1, pageSize: 50 })
  const record = records.items.find((item) => item.accountId === accountId && item.success === false)
  assert(record, `未找到账号 ${accountId} 的失败使用记录`)
  assert.equal(record.errorCode, errorCode, `失败使用记录错误码不正确：${record.errorCode}`)
}

function assertFailedUsageRecordHasTokenCost(accountId: string): void {
  const records = repositories.listUsageRecords(undefined, { result: 'failed', page: 1, pageSize: 50 })
  const record = records.items.find((item) => item.accountId === accountId && item.success === false)
  assert(record, `未找到账号 ${accountId} 的失败使用记录`)
  assert((record.inputTokens ?? 0) > 0, `输出后失败应估算输入 token：${record.inputTokens}`)
  assert((record.outputTokens ?? 0) > 0, `输出后失败应估算输出 token：${record.outputTokens}`)
  assert((record.costUsd ?? 0) > 0, `输出后失败应估算成本：${record.costUsd}`)
}

async function assertStreamInterceptAuditMetadata(
  accountId: string,
  expected: {
    upstreamErrorCode: string
    rewriteErrorCode: string
    fallbackReason: string
    outputSeen: boolean
  }
): Promise<void> {
  const logs = repositories.listAuditLogs({ accountId, outcome: 'stream_failed', page: 1, pageSize: 20 })
  let metadata: Record<string, unknown> | undefined
  for (const item of logs.items) {
    const detail = repositories.getAuditLogDetail(item.id)
    if (!detail) continue
    for (const payload of detail.payloads) {
      if (payload.partType !== 'gateway_metadata') continue
      const payloadDetail = await repositories.getAuditLogPayload(detail.id, payload.id)
      if (!payloadDetail?.bodyText) continue
      const body = JSON.parse(payloadDetail.bodyText) as { metadata?: Record<string, unknown> }
      if (body.metadata?.streamIntercepted === true) {
        metadata = body.metadata
        break
      }
    }
    if (metadata) break
  }
  assert(metadata, `未找到账号 ${accountId} 的流式拦截审计日志`)
  assert.equal(metadata.streamIntercepted, true, '审计元信息应标记 streamIntercepted')
  assert.equal(metadata.fallbackReason, expected.fallbackReason, '审计元信息兜底原因不正确')
  assert.equal(metadata.upstreamErrorCode, expected.upstreamErrorCode, '审计元信息上游错误码不正确')
  assert.equal(metadata.rewriteErrorCode, expected.rewriteErrorCode, '审计元信息改写错误码不正确')
  assert.equal(metadata.outputSeen, expected.outputSeen, '审计元信息 outputSeen 不正确')
}

function listen(server: http.Server): Promise<void> {
  if (!server.listening) {
    server.listen(0, '127.0.0.1')
  }
  if (server.listening) return Promise.resolve()
  return new Promise((resolvePromise, rejectPromise) => {
    server.once('listening', resolvePromise)
    server.once('error', rejectPromise)
  })
}

function serverAddress(server: http.Server): { port: number } {
  const address = server.address()
  if (!address || typeof address === 'string') {
    throw new Error('服务地址不可用')
  }
  return { port: address.port }
}

async function closeServer(server: http.Server | undefined): Promise<void> {
  if (!server?.listening) return
  await new Promise<void>((resolvePromise, rejectPromise) => {
    server.close((error) => error ? rejectPromise(error) : resolvePromise())
  })
}

main().catch((error) => {
  console.error('\n流式超时回归失败')
  console.error(error instanceof Error ? error.message : error)
  process.exitCode = 1
})
