import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { mkdirSync, rmSync } from 'node:fs'
import http from 'node:http'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import express, { type NextFunction, type Request, type Response as ExpressResponse } from 'express'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-stream-first-output-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'stream-first-output.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'stream-first-output-secret'
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
  apiKeyRepository,
  settingsRepository,
  gatewayCache,
  accountSideEffects,
  usageRecordQueue,
  auditLogQueue,
  clientIpAvoidance
] = await Promise.all([
  import('../../modules/gateway/routes.js'),
  import('../../shared/request-context.js'),
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/api-key.repository.js'),
  import('../../storage/settings.repository.js'),
  import('../../modules/gateway/runtime/runtime-cache.service.js'),
  import('../../modules/gateway/runtime/account-side-effects.service.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/gateway/runtime/client-ip-account-avoidance.service.js')
])

type RawBodyRequest = Request & { rawBody?: Buffer }

const app = express()
app.use(requestContextMiddleware)
app.use('/v1', express.raw({ type: () => true, limit: '8mb' }), captureGatewayRawBody, openAIGatewayRouter)
let scenarioCredentialIndex = 0
let scenarioCredentialOwnerAccess: { systemAccountId: string; role: 'user' } | undefined

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

const preCommitFuzzServerRetryScenarios = [
  {
    scenario: 'server-retry-fuzz-created-eof-then-success',
    backupResponseId: 'resp_fuzz_created_backup',
    hiddenPrimaryNeedle: 'resp_fuzz_created_primary'
  },
  {
    scenario: 'server-retry-fuzz-in-progress-eof-then-success',
    backupResponseId: 'resp_fuzz_in_progress_backup',
    hiddenPrimaryNeedle: 'resp_fuzz_in_progress_primary'
  },
  {
    scenario: 'server-retry-fuzz-error-event-then-success',
    backupResponseId: 'resp_fuzz_error_event_backup',
    hiddenPrimaryNeedle: 'fuzz primary error event'
  },
  {
    scenario: 'server-retry-fuzz-failed-event-then-success',
    backupResponseId: 'resp_fuzz_failed_event_backup',
    hiddenPrimaryNeedle: 'fuzz primary failed event'
  }
] as const

async function main(): Promise<void> {
  let appServer: http.Server | undefined
  let upstreamServer: http.Server | undefined
  try {
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(true)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(true)
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
    const largeImageEventCredential = createScenarioCredential(upstreamBaseUrl, '大图事件后完成')
    const largeImagePartialCredential = createScenarioCredential(upstreamBaseUrl, '大图 partial 事件后完成')
    const largeImageApiEventCredential = createScenarioCredential(upstreamBaseUrl, 'Image API 大图完成')
    const largeImageApiSplitCredential = createScenarioCredential(upstreamBaseUrl, 'Image API 大图拆包完成')
    const largeImageApiEofCredential = createScenarioCredential(upstreamBaseUrl, 'Image API 大图 EOF')
    const missingTerminalCredential = createScenarioCredential(upstreamBaseUrl, '缺少终止事件')
    const heartbeatCredential = createScenarioCredential(upstreamBaseUrl, '心跳刷新')
    const clientCloseAfterTerminalCredential = createScenarioCredential(upstreamBaseUrl, '终止后客户端关闭')
    const overloadedBeforeOutputCredential = createScenarioCredential(upstreamBaseUrl, '容量错误未输出前重试')
    const slowDownCredential = createScenarioCredential(upstreamBaseUrl, 'slow_down 未输出前重试')
    const genericErrorEventCredential = createScenarioCredential(upstreamBaseUrl, '未知 error 事件默认重试')
    const cyberPolicyCredential = createScenarioCredential(upstreamBaseUrl, 'cyber_policy 未输出前重试')
    const contextWindowCredential = createScenarioCredential(upstreamBaseUrl, '上下文超限未输出前重试')
    const nonCodexErrorEventCredential = createScenarioCredential(upstreamBaseUrl, '普通客户端未输出前默认规则')
    const overloadedNoBoundaryCredential = createScenarioCredential(upstreamBaseUrl, '容量错误缺少收尾边界')
    const overloadedAfterOutputCredential = createScenarioCredential(upstreamBaseUrl, '输出后容量错误不拦截')
    const cyberPolicyAfterOutputCredential = createScenarioCredential(upstreamBaseUrl, '输出后 cyber_policy 重试')
    const outputItemThenFailureCredential = createScenarioCredential(upstreamBaseUrl, 'output item 后失败')
    const topLevelCodeMessageCredential = createScenarioCredential(upstreamBaseUrl, '顶层 code message 非失败')
    const jsonResponseForStreamCredential = createScenarioCredential(upstreamBaseUrl, 'stream 请求返回 JSON')
    const noFirstChunkServerRetryCredential = createTwoAccountScenarioCredential(upstreamBaseUrl, '首段等待服务端切号')
    const missingTerminalServerRetryCredential = createTwoAccountScenarioCredential(upstreamBaseUrl, '缺终止服务端切号')
    const failedEventServerRetryCredential = createTwoAccountScenarioCredential(upstreamBaseUrl, '失败事件服务端切号')
    const preCommitFuzzServerRetryCredential = createTwoAccountScenarioCredential(upstreamBaseUrl, '预提交失败 fuzz 服务端切号')
    const fourAccountServerRetryCredential = createMultiAccountScenarioCredential(upstreamBaseUrl, '超过三账号隐藏重试', 4)

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
    assert(streamText.includes('上游流式响应在输出前失败，请重试'), `Codex 首段等待超时应返回统一可重试文案：${streamText}`)
    assert(streamText.includes('"code":"upstream_retryable_error"'), `首段等待超时应改写为可重试错误码：${streamText}`)
    assert(durationMs < 15000, `首段等待超时没有及时结束，耗时 ${durationMs}ms`)

    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const noFirstChunkFailureState = databaseModule.getBusinessDatabase()
      .prepare('SELECT stream_failure_count FROM accounts WHERE id = ?')
      .get(noFirstChunkCredential.account.id) as { stream_failure_count?: number } | undefined
    const noFirstChunkRuntime = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[noFirstChunkCredential.account.id]
    assert.equal(noFirstChunkRuntime?.status, 'local_suppressed', '首段前失败未产生可见输出时应短期本地避让账号，避免后续请求反复命中')
    assert.equal(Number(noFirstChunkFailureState?.stream_failure_count ?? 0), 0, '首段前失败未产生可见输出，不应累计账号流失败计数')

    settingsRepository.updateSettings({
      streamCircuitBreakerEnabled: true,
      streamRequestTimeoutSeconds: 10,
      streamIdleTimeoutSeconds: 10,
      temporaryUnschedulableRetryAttempts: 0
    })
    gatewayCache.clearGatewayRuntimeCache()
    const noFirstChunkServerRetryResult = await requestStreamScenario(baseUrl, noFirstChunkServerRetryCredential.apiKey.key, 'server-retry-no-first-chunk-then-success')
    assert(noFirstChunkServerRetryResult.durationMs >= 9000 && noFirstChunkServerRetryResult.durationMs < 15000, `首段等待服务端切号应按 10s 左右超时后救回，耗时 ${noFirstChunkServerRetryResult.durationMs}ms`)
    assert(noFirstChunkServerRetryResult.streamText.includes('resp_no_first_chunk_backup'), `首段等待后应切备用账号完成：${noFirstChunkServerRetryResult.streamText}`)
    assert(noFirstChunkServerRetryResult.streamText.includes('response.completed'), `首段等待服务端切号后应完成：${noFirstChunkServerRetryResult.streamText}`)
    assert(!noFirstChunkServerRetryResult.streamText.includes('response.failed'), `首段等待服务端切号成功时客户端不应看到中间失败：${noFirstChunkServerRetryResult.streamText}`)
    assert(!noFirstChunkServerRetryResult.streamText.includes('upstream_retryable_error'), `首段等待服务端切号成功时不应消耗 Codex 客户端重试：${noFirstChunkServerRetryResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertFailedUsageRecordExists(noFirstChunkServerRetryCredential.primaryAccount.id)
    assertSuccessfulUsageRecord(noFirstChunkServerRetryCredential.backupAccount.id, { inputTokens: 2, outputTokens: 1 })
    const firstChunkThenIdleResult = await requestFirstChunkThenIdleTimeout(baseUrl, firstChunkIdleCredential.apiKey.key)
    assert(firstChunkThenIdleResult.streamText.includes('response.created'), `客户端未收到首段上游事件：${firstChunkThenIdleResult.streamText}`)
    assert(firstChunkThenIdleResult.streamText.includes('response.failed'), `客户端未收到首段后空闲失败事件：${firstChunkThenIdleResult.streamText}`)
    assert(firstChunkThenIdleResult.streamText.includes('上游流式响应在输出前失败，请重试'), `Codex 首段后空闲应返回统一可重试文案：${firstChunkThenIdleResult.streamText}`)
    assert(firstChunkThenIdleResult.streamText.includes('"code":"upstream_retryable_error"'), `首段后空闲应改写为可重试错误码：${firstChunkThenIdleResult.streamText}`)
    assert(
      firstChunkThenIdleResult.durationMs >= 900 && firstChunkThenIdleResult.durationMs < 5000,
      `首段后空闲超时没有按 1s 左右及时结束，耗时 ${firstChunkThenIdleResult.durationMs}ms`
    )

    const fragmentedSseEventResult = await requestFragmentedSseEventKeepalive(baseUrl, fragmentedSseEventCredential.apiKey.key)
    assert(fragmentedSseEventResult.streamText.includes('response.completed'), `碎片化 SSE 事件持续有原始字节时应等到上游完成：${fragmentedSseEventResult.streamText}`)
    assert(!fragmentedSseEventResult.streamText.includes('response.failed'), `碎片化 SSE 事件持续有原始字节时不应补发失败事件：${fragmentedSseEventResult.streamText}`)
    assert(
      fragmentedSseEventResult.durationMs >= 1200 && fragmentedSseEventResult.durationMs < 5000,
      `碎片化 SSE 事件没有持续等待到上游完成，耗时 ${fragmentedSseEventResult.durationMs}ms`
    )
    usageRecordQueue.flushAllUsageRecordQueue()
    assertSuccessfulUsageRecord(fragmentedSseEventCredential.account.id)

    const parserSkippedResult = await requestParserSkippedRawForward(baseUrl, parserSkippedCredential.apiKey.key)
    assert(!parserSkippedResult.streamText.includes('response.failed'), '解析跳过后仍有原始上游数据持续到来时不应补发失败事件')
    assert(
      parserSkippedResult.durationMs >= 1200 && parserSkippedResult.durationMs < 5000,
      `解析跳过后原样转发没有持续到上游 EOF，耗时 ${parserSkippedResult.durationMs}ms`
    )

    const largeImageTraceId = traceIdForSampledSuccessBucket('large-image-event')
    const largeImageEventResult = await requestStreamScenario(baseUrl, largeImageEventCredential.apiKey.key, 'large-image-event-then-completed', largeImageTraceId)
    assert(largeImageEventResult.streamText.includes('response.image_generation_call.completed'), `客户端未收到大图事件：${largeImageEventResult.streamText}`)
    assert(largeImageEventResult.streamText.includes('response.completed'), `大图事件后未继续识别完成事件：${largeImageEventResult.streamText}`)
    assert(!largeImageEventResult.streamText.includes('response.failed'), `大图事件后不应补发失败事件：${largeImageEventResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertSuccessfulUsageRecord(largeImageEventCredential.account.id)
    assertUsageRecordBodyOmitted(largeImageEventCredential.account.id, largeImageTraceId)
    auditLogQueue.flushAllAuditLogQueue()
    await assertImageStreamAuditBodyOmitted(largeImageTraceId, 'response.image_generation_call.completed')

    const largeImagePartialTraceId = traceIdForSampledSuccessBucket('large-image-partial')
    const largeImagePartialResult = await requestStreamScenario(baseUrl, largeImagePartialCredential.apiKey.key, 'large-image-partial-then-completed', largeImagePartialTraceId)
    assert(largeImagePartialResult.streamText.includes('response.image_generation_call.partial_image'), `客户端未收到大图 partial 事件：${largeImagePartialResult.streamText}`)
    assert(largeImagePartialResult.streamText.includes('response.completed'), `大图 partial 事件后未继续识别完成事件：${largeImagePartialResult.streamText}`)
    assert(!largeImagePartialResult.streamText.includes('response.failed'), `大图 partial 事件后不应补发失败事件：${largeImagePartialResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertSuccessfulUsageRecord(largeImagePartialCredential.account.id)
    assertUsageRecordBodyOmitted(largeImagePartialCredential.account.id, largeImagePartialTraceId)
    auditLogQueue.flushAllAuditLogQueue()
    await assertImageStreamAuditBodyOmitted(largeImagePartialTraceId, 'response.image_generation_call.partial_image')

    const largeImageApiTraceId = traceIdForSampledSuccessBucket('large-image-api-event')
    const largeImageApiEventResult = await requestStreamScenario(baseUrl, largeImageApiEventCredential.apiKey.key, 'large-image-api-event-completed', largeImageApiTraceId)
    assert(largeImageApiEventResult.streamText.includes('image_generation.completed'), `客户端未收到 Image API 大图完成事件：${largeImageApiEventResult.streamText}`)
    assert(!largeImageApiEventResult.streamText.includes('response.failed'), `Image API 大图完成事件不应补发失败事件：${largeImageApiEventResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertSuccessfulUsageRecord(largeImageApiEventCredential.account.id, { inputTokens: 1, outputTokens: 100 })
    assertUsageRecordBodyOmitted(largeImageApiEventCredential.account.id, largeImageApiTraceId)
    auditLogQueue.flushAllAuditLogQueue()
    await assertImageStreamAuditBodyOmitted(largeImageApiTraceId, 'image_generation.completed')

    const largeImageApiSplitTraceId = traceIdForSampledSuccessBucket('large-image-api-split')
    const largeImageApiSplitResult = await requestStreamScenario(baseUrl, largeImageApiSplitCredential.apiKey.key, 'large-image-api-event-split-completed', largeImageApiSplitTraceId)
    assert(largeImageApiSplitResult.streamText.includes('split_tail_marker'), 'Image API 大图终止事件拆包时不应在首个 chunk 后提前截断响应')
    assert(!largeImageApiSplitResult.streamText.includes('response.failed'), `拆包 Image API 大图完成事件不应补发失败事件：${largeImageApiSplitResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertSuccessfulUsageRecord(largeImageApiSplitCredential.account.id, { inputTokens: 3, outputTokens: 4 })
    assertUsageRecordBodyOmitted(largeImageApiSplitCredential.account.id, largeImageApiSplitTraceId)
    auditLogQueue.flushAllAuditLogQueue()
    await assertImageStreamAuditBodyOmitted(largeImageApiSplitTraceId, 'image_generation.completed')

    const largeImageApiEofTraceId = traceIdForSampledSuccessBucket('large-image-api-eof')
    const largeImageApiEofResult = await requestStreamScenario(baseUrl, largeImageApiEofCredential.apiKey.key, 'large-image-api-event-eof-no-boundary', largeImageApiEofTraceId)
    assert(largeImageApiEofResult.streamText.includes('image_generation.completed'), `客户端未收到无收尾边界 Image API 大图事件：${largeImageApiEofResult.streamText}`)
    assert(!largeImageApiEofResult.streamText.includes('response.failed'), `无收尾边界 Image API 大图完成事件不应补发失败事件：${largeImageApiEofResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertSuccessfulUsageRecord(largeImageApiEofCredential.account.id, { inputTokens: 1, outputTokens: 100 })
    assertUsageRecordBodyOmitted(largeImageApiEofCredential.account.id, largeImageApiEofTraceId)
    auditLogQueue.flushAllAuditLogQueue()
    await assertImageStreamAuditBodyOmitted(largeImageApiEofTraceId, 'image_generation.completed')

    const missingTerminalResult = await requestMissingTerminalEof(baseUrl, missingTerminalCredential.apiKey.key)
    assert(missingTerminalResult.streamText.includes('response.created'), `客户端未收到缺少终止事件场景的首段上游事件：${missingTerminalResult.streamText}`)
    assert(missingTerminalResult.streamText.includes('response.failed'), `缺少终止事件场景未收到网关失败事件：${missingTerminalResult.streamText}`)
    assert(missingTerminalResult.streamText.includes('上游流式响应在输出前失败，请重试'), `缺少终止事件场景应返回统一可重试文案：${missingTerminalResult.streamText}`)
    assert(missingTerminalResult.streamText.includes('"code":"upstream_retryable_error"'), `缺少终止事件应改写为可重试错误码：${missingTerminalResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const missingTerminalAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === missingTerminalCredential.account.id)
    const missingTerminalRuntime = accountSideEffects.snapshotGatewayAccountRuntimeAvailability()[missingTerminalCredential.account.id]
    assert.equal(missingTerminalRuntime?.status, 'local_suppressed', '缺少终止事件但未产生可见输出时应短期本地避让账号')
    assert.equal(missingTerminalAccount?.status, 'active', '缺少终止事件但仅有 response.created 时不应把账号置为临时不可调用')
    assert.equal(missingTerminalAccount?.streamFailureCount, 0, '缺少终止事件但未产生可见输出时不应累计账号流失败计数')

    const missingTerminalServerRetryResult = await requestStreamScenario(baseUrl, missingTerminalServerRetryCredential.apiKey.key, 'server-retry-missing-terminal-then-success')
    assert(missingTerminalServerRetryResult.streamText.includes('resp_server_retry_backup'), `缺少终止事件后应切备用账号完成：${missingTerminalServerRetryResult.streamText}`)
    assert(missingTerminalServerRetryResult.streamText.includes('response.completed'), `缺少终止事件服务端切号后应完成：${missingTerminalServerRetryResult.streamText}`)
    assert(!missingTerminalServerRetryResult.streamText.includes('resp_server_retry_primary'), `客户端不应看到首个失败账号的预提交事件：${missingTerminalServerRetryResult.streamText}`)
    assert(!missingTerminalServerRetryResult.streamText.includes('response.failed'), `服务端切号成功时客户端不应看到中间失败：${missingTerminalServerRetryResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertFailedUsageRecordExists(missingTerminalServerRetryCredential.primaryAccount.id)
    assertSuccessfulUsageRecord(missingTerminalServerRetryCredential.backupAccount.id, { inputTokens: 2, outputTokens: 1 })

    const heartbeatThenCompletedResult = await requestHeartbeatThenCompleted(baseUrl, heartbeatCredential.apiKey.key)
    assert(heartbeatThenCompletedResult.streamText.includes('response.created'), `客户端未收到首段上游事件：${heartbeatThenCompletedResult.streamText}`)
    assert(heartbeatThenCompletedResult.streamText.includes('response.completed'), `客户端未收到完成事件：${heartbeatThenCompletedResult.streamText}`)
    assert(!heartbeatThenCompletedResult.streamText.includes('response.failed'), `上游持续心跳时不应触发失败事件：${heartbeatThenCompletedResult.streamText}`)
    assert(heartbeatThenCompletedResult.durationMs < 5000, `持续心跳后完成没有及时结束，耗时 ${heartbeatThenCompletedResult.durationMs}ms`)

    await requestAndCloseAfterTerminal(baseUrl, clientCloseAfterTerminalCredential.apiKey.key)
    await waitForSuccessfulUsageRecord(clientCloseAfterTerminalCredential.account.id)
    auditLogQueue.flushAllAuditLogQueue()
    assertNoClientAbortedAuditLogForAccount(clientCloseAfterTerminalCredential.account.id)

    const overloadedBeforeOutputResult = await requestServerOverloadedBeforeOutput(baseUrl, overloadedBeforeOutputCredential.apiKey.key)
    assert(!overloadedBeforeOutputResult.streamText.includes('server_is_overloaded'), `未输出前不应把原始容量错误发给客户端：${overloadedBeforeOutputResult.streamText}`)
    assert(!overloadedBeforeOutputResult.streamText.includes('Our servers are currently overloaded'), `未输出前不应把原始容量错误文案发给客户端：${overloadedBeforeOutputResult.streamText}`)
    assert(overloadedBeforeOutputResult.streamText.includes('upstream_retryable_error'), `未输出前容量错误应按通用兜底改写为可重试错误：${overloadedBeforeOutputResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const overloadedBeforeOutputAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === overloadedBeforeOutputCredential.account.id)
    assert.equal(overloadedBeforeOutputAccount?.status, 'active', '未输出前容量错误不应把账号置为临时不可调用')
    assert.equal(overloadedBeforeOutputAccount?.streamFailureCount, 0, '未输出前容量错误不应累计账号流失败计数')
    auditLogQueue.flushAllAuditLogQueue()
    await assertResponseInspectionAuditMetadata(overloadedBeforeOutputCredential.account.id, {
      upstreamErrorCode: 'server_is_overloaded',
      rewriteErrorCode: 'server_is_overloaded',
      fallbackReason: 'before_downstream_write_response_failure',
      downstreamWritten: false
    })

    const failedEventServerRetryResult = await requestStreamScenario(baseUrl, failedEventServerRetryCredential.apiKey.key, 'server-retry-response-failed-then-success')
    assert(failedEventServerRetryResult.streamText.includes('resp_failed_retry_backup'), `流内失败后应切备用账号完成：${failedEventServerRetryResult.streamText}`)
    assert(failedEventServerRetryResult.streamText.includes('response.completed'), `流内失败服务端切号后应完成：${failedEventServerRetryResult.streamText}`)
    assert(!failedEventServerRetryResult.streamText.includes('server_is_overloaded'), `服务端切号成功时不应把原始流内失败下发客户端：${failedEventServerRetryResult.streamText}`)
    assert(!failedEventServerRetryResult.streamText.includes('response.failed'), `流内失败服务端切号成功时客户端不应看到失败事件：${failedEventServerRetryResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertFailedUsageRecordErrorCode(failedEventServerRetryCredential.primaryAccount.id, 'server_is_overloaded')
    assertSuccessfulUsageRecord(failedEventServerRetryCredential.backupAccount.id, { inputTokens: 3, outputTokens: 1 })
    const fourAccountServerRetryResult = await requestStreamScenario(baseUrl, fourAccountServerRetryCredential.apiKey.key, 'server-retry-fourth-account-then-success')
    assert(fourAccountServerRetryResult.streamText.includes('resp_fourth_retry_success'), `隐藏重试不应限制为前三个账号，应继续切到第 4 个账号完成：${fourAccountServerRetryResult.streamText}`)
    assert(fourAccountServerRetryResult.streamText.includes('response.completed'), `第 4 个账号救回应返回完成事件：${fourAccountServerRetryResult.streamText}`)
    assert(!fourAccountServerRetryResult.streamText.includes('upstream_retryable_error'), `第 4 个账号救回时不应消耗客户端重试：${fourAccountServerRetryResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    assertUsageRecordCountAtLeast(fourAccountServerRetryCredential.accounts[0].id, false, 1)
    assertUsageRecordCountAtLeast(fourAccountServerRetryCredential.accounts[1].id, false, 1)
    assertUsageRecordCountAtLeast(fourAccountServerRetryCredential.accounts[2].id, false, 1)
    assertSuccessfulUsageRecord(fourAccountServerRetryCredential.accounts[3].id, { inputTokens: 4, outputTokens: 1 })

    await assertPreCommitFuzzServerRetryScenarios(baseUrl, preCommitFuzzServerRetryCredential)

    const slowDownResult = await requestStreamFailureBeforeOutput(baseUrl, slowDownCredential.apiKey.key, 'slow-down-before-output')
    assert(!slowDownResult.streamText.includes('slow_down'), `未输出前 slow_down 不应把原始错误发给客户端：${slowDownResult.streamText}`)
    assert(slowDownResult.streamText.includes('upstream_retryable_error'), `未输出前 slow_down 应按通用兜底改写为可重试错误：${slowDownResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const slowDownAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === slowDownCredential.account.id)
    assert.equal(slowDownAccount?.status, 'active', '未输出前 slow_down 不应把账号置为临时不可调用')
    assert.equal(slowDownAccount?.streamFailureCount, 0, '未输出前 slow_down 不应累计账号流失败计数')

    const genericErrorEventResult = await requestStreamFailureBeforeOutput(baseUrl, genericErrorEventCredential.apiKey.key, 'generic-error-event-before-output')
    assert(!genericErrorEventResult.streamText.includes('internal_server_error'), `未知 error 事件不应把原始错误码发给客户端：${genericErrorEventResult.streamText}`)
    assert(genericErrorEventResult.streamText.includes('upstream_retryable_error'), `未知 error 事件应按写入下游前失败统一改写为可重试错误：${genericErrorEventResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const genericErrorEventAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === genericErrorEventCredential.account.id)
    assert.equal(genericErrorEventAccount?.status, 'active', '未知 error 事件未输出前不应把账号置为临时不可调用')
    assert.equal(genericErrorEventAccount?.streamFailureCount, 0, '未知 error 事件未输出前不应累计账号流失败计数')
    auditLogQueue.flushAllAuditLogQueue()
    await assertResponseInspectionAuditMetadata(genericErrorEventCredential.account.id, {
      upstreamErrorCode: 'internal_server_error',
      rewriteErrorCode: 'internal_server_error',
      fallbackReason: 'before_downstream_write_response_failure',
      downstreamWritten: false
    })

    const cyberPolicyResult = await requestStreamFailureBeforeOutput(baseUrl, cyberPolicyCredential.apiKey.key, 'cyber-policy-before-output')
    assert(!cyberPolicyResult.streamText.includes('cyber_policy'), `未输出前 cyber_policy 不应把原始错误码发给客户端：${cyberPolicyResult.streamText}`)
    assert(cyberPolicyResult.streamText.includes('upstream_retryable_error'), `未输出前 cyber_policy 应改写为可重试错误：${cyberPolicyResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const cyberPolicyAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === cyberPolicyCredential.account.id)
    assert.equal(cyberPolicyAccount?.status, 'active', '未输出前 cyber_policy 不应把账号置为临时不可调用')
    assert.equal(cyberPolicyAccount?.streamFailureCount, 0, '未输出前 cyber_policy 不应累计账号流失败计数')
    auditLogQueue.flushAllAuditLogQueue()
    await assertResponseInspectionAuditMetadata(cyberPolicyCredential.account.id, {
      upstreamErrorCode: 'cyber_policy',
      rewriteErrorCode: 'upstream_retryable_error',
      fallbackReason: 'before_downstream_write_response_failure',
      downstreamWritten: false
    })

    const cyberPolicyAfterOutputResult = await requestStreamScenario(baseUrl, cyberPolicyAfterOutputCredential.apiKey.key, 'cyber-policy-after-output')
    assert(!cyberPolicyAfterOutputResult.streamText.includes('hello'), `尚未写入下游时不应泄露同批次前序输出：${cyberPolicyAfterOutputResult.streamText}`)
    assert(!cyberPolicyAfterOutputResult.streamText.includes('cyber_policy'), `尚未写入下游时 cyber_policy 不应把原始错误码发给客户端：${cyberPolicyAfterOutputResult.streamText}`)
    assert(cyberPolicyAfterOutputResult.streamText.includes('upstream_retryable_error'), `尚未写入下游时 cyber_policy 应改写为可重试错误：${cyberPolicyAfterOutputResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const cyberPolicyAfterOutputAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === cyberPolicyAfterOutputCredential.account.id)
    assert.equal(cyberPolicyAfterOutputAccount?.status, 'active', '输出后 cyber_policy 不应把账号置为临时不可调用')
    assert.equal(cyberPolicyAfterOutputAccount?.streamFailureCount, 0, '输出后 cyber_policy 改写后不应累计账号流失败计数')
    auditLogQueue.flushAllAuditLogQueue()
    await assertResponseInspectionAuditMetadata(cyberPolicyAfterOutputCredential.account.id, {
      upstreamErrorCode: 'cyber_policy',
      rewriteErrorCode: 'upstream_retryable_error',
      fallbackReason: 'before_downstream_write_response_failure',
      downstreamWritten: false
    })

    const contextWindowResult = await requestStreamFailureBeforeOutput(baseUrl, contextWindowCredential.apiKey.key, 'context-window-before-output')
    assert(!contextWindowResult.streamText.includes('context_length_exceeded'), `未输出前 context_length_exceeded 不应把原始错误码发给客户端：${contextWindowResult.streamText}`)
    assert(contextWindowResult.streamText.includes('upstream_retryable_error'), `未输出前 context_length_exceeded 应改写为可重试错误：${contextWindowResult.streamText}`)

    const nonCodexErrorEventResult = await requestGenericStreamFailureBeforeOutput(baseUrl, nonCodexErrorEventCredential.apiKey.key, 'generic-error-event-before-output')
    assert(nonCodexErrorEventResult.streamText.includes('response.failed'), `普通客户端未输出前失败应返回普通失败事件：${nonCodexErrorEventResult.streamText}`)
    assert(!nonCodexErrorEventResult.streamText.includes('upstream_retryable_error'), `普通客户端未输出前失败不应伪造客户端专用可重试错误：${nonCodexErrorEventResult.streamText}`)

    const overloadedNoBoundaryResult = await requestStreamFailureBeforeOutput(baseUrl, overloadedNoBoundaryCredential.apiKey.key, 'server-overloaded-before-output-no-boundary')
    assert(!overloadedNoBoundaryResult.streamText.includes('server_is_overloaded'), `EOF 尾包未输出前不应把原始容量错误发给客户端：${overloadedNoBoundaryResult.streamText}`)
    assert(!overloadedNoBoundaryResult.streamText.includes('Our servers are currently overloaded'), `EOF 尾包未输出前不应把原始容量错误文案发给客户端：${overloadedNoBoundaryResult.streamText}`)
    assert(overloadedNoBoundaryResult.streamText.includes('upstream_retryable_error'), `EOF 尾包未输出前应按通用兜底改写为可重试错误：${overloadedNoBoundaryResult.streamText}`)

    const overloadedAfterOutputResult = await requestServerOverloadedAfterOutput(baseUrl, overloadedAfterOutputCredential.apiKey.key)
    assert(!overloadedAfterOutputResult.streamText.includes('hello'), `尚未写入下游时不应泄露同批次前序输出：${overloadedAfterOutputResult.streamText}`)
    assert(!overloadedAfterOutputResult.streamText.includes('server_is_overloaded'), `尚未写入下游时容量错误应按可重试失败改写：${overloadedAfterOutputResult.streamText}`)
    assert(!overloadedAfterOutputResult.streamText.includes('Our servers are currently overloaded'), `尚未写入下游时容量错误文案应按可重试失败改写：${overloadedAfterOutputResult.streamText}`)
    assert(overloadedAfterOutputResult.streamText.includes('upstream_retryable_error'), `尚未写入下游时容量错误应给下游明确可重试信号：${overloadedAfterOutputResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const overloadedAfterOutputAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === overloadedAfterOutputCredential.account.id)
    assert.equal(overloadedAfterOutputAccount?.streamFailureCount, 0, '真实网关流量输出后容量错误不应直接写入账号流失败计数')
    assert.equal(accountSideEffects.getGatewayAccountSideEffectState().precheckPendingAccountCount, 0, '单次输出后容量错误不应触发账号事前确认')

    const outputItemThenFailureResult = await requestStreamScenario(baseUrl, outputItemThenFailureCredential.apiKey.key, 'output-item-then-failure')
    assert(!outputItemThenFailureResult.streamText.includes('response.output_item.added'), `尚未写入下游时不应泄露同批次 output item：${outputItemThenFailureResult.streamText}`)
    assert(!outputItemThenFailureResult.streamText.includes('internal_server_error'), `尚未写入下游时 output item 后失败应改写原始错误：${outputItemThenFailureResult.streamText}`)
    assert(outputItemThenFailureResult.streamText.includes('upstream_retryable_error'), `尚未写入下游时 output item 后失败应给下游明确可重试信号：${outputItemThenFailureResult.streamText}`)
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    const outputItemThenFailureAccount = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === outputItemThenFailureCredential.account.id)
    assert.equal(outputItemThenFailureAccount?.streamFailureCount, 0, '真实网关流量 output item 后失败不应直接写入账号流失败计数')

    const topLevelCodeMessageResult = await requestStreamScenario(baseUrl, topLevelCodeMessageCredential.apiKey.key, 'top-level-code-message-non-error')
    assert(topLevelCodeMessageResult.streamText.includes('response.completed'), `顶层 code/message 普通事件应继续到完成：${topLevelCodeMessageResult.streamText}`)
    assert(topLevelCodeMessageResult.streamText.includes('"code":"diagnostic_code"'), `普通事件的 code 字段应原样透传：${topLevelCodeMessageResult.streamText}`)
    assert(!topLevelCodeMessageResult.streamText.includes('upstream_retryable_error'), `普通事件顶层 code/message 不应误判为失败：${topLevelCodeMessageResult.streamText}`)

    const jsonResponseForStreamResult = await requestJsonResponseForStreamRequest(baseUrl, jsonResponseForStreamCredential.apiKey.key)
    assert.equal(jsonResponseForStreamResult.contentType.includes('application/json'), true, `stream:true 但上游明确返回 JSON 时应按非流式响应转发：${jsonResponseForStreamResult.contentType}`)
    assert(jsonResponseForStreamResult.text.includes('json response ok'), `stream:true 的明确 JSON 响应应原样返回：${jsonResponseForStreamResult.text}`)
    assert(!jsonResponseForStreamResult.text.includes('response.failed'), `stream:true 的明确 JSON 响应不应被 SSE 解析器追加失败事件：${jsonResponseForStreamResult.text}`)

    console.log('流式超时回归通过：Codex 首段等待、首段后无新数据、碎片化 SSE 有原始字节时不误熔断、解析跳过后原样转发、图像大事件继续完成且审计不落正文、Image API 大图终止事件和无收尾边界识别、缺少终止事件未输出不计数、输出前流失败服务端优先切号、心跳刷新空闲计时、容量错误/slow_down 专属兜底、未知 error 事件兜底、context_length_exceeded/cyber_policy 可重试改写、普通客户端不伪造专用可重试码、输出后真实网关流量不直接写账号流失败计数、output item 输出判定、顶层 code/message 非失败、stream:true 明确 JSON 响应和 EOF 尾包场景符合预期')
  } finally {
    usageRecordQueue.flushAllUsageRecordQueue()
    await accountSideEffects.flushGatewayAccountSideEffectsForTest()
    auditLogQueue.flushAllAuditLogQueue()
    usageRecordQueue.setDbServiceUsageRecordLocalWriteAllowedForTest(false)
    auditLogQueue.setDbServiceAuditLogLocalWriteAllowedForTest(false)
    await closeServer(appServer)
    await closeServer(upstreamServer)
    try {
      databaseModule.getBusinessDatabase().close()
      databaseModule.closeStorageDatabases()
    } catch {
    }
    rmSync(tempRoot, { recursive: true, force: true })
  }
}

function createScenarioCredential(upstreamBaseUrl: string, label: string): {
  account: ReturnType<typeof repositories.createAccount>
  apiKey: ReturnType<typeof apiKeyRepository.createApiKeyRecord>
} {
  const access = scenarioCredentialAccess()
  const group = repositories.createGroup({ name: `流式超时回归分组-${label}`, providerCode: 'gpt', enabled: true }, access)
  scenarioCredentialIndex += 1
  const account = repositories.createAccount({
    providerCode: 'gpt',
    name: `流式超时回归账户-${label}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-stream-timeout-regression-${scenarioCredentialIndex}`,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    clientCompatibility: 'codex_responses'
  }, access)
  const apiKey = apiKeyRepository.createApiKeyRecord({
    name: `流式超时回归 Key-${label}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  return { account, apiKey }
}

function createTwoAccountScenarioCredential(upstreamBaseUrl: string, label: string): {
  primaryAccount: ReturnType<typeof repositories.createAccount>
  backupAccount: ReturnType<typeof repositories.createAccount>
  apiKey: ReturnType<typeof apiKeyRepository.createApiKeyRecord>
} {
  const access = scenarioCredentialAccess()
  const group = repositories.createGroup({ name: `流式超时回归双账号分组-${label}`, providerCode: 'gpt', enabled: true }, access)
  scenarioCredentialIndex += 1
  const primaryKey = `sk-stream-timeout-regression-${scenarioCredentialIndex}-primary`
  const backupKey = `sk-stream-timeout-regression-${scenarioCredentialIndex}-backup`
  const primaryAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `流式超时回归主账户-${label}`,
    type: 'api_key',
    credentials: {
      api_key: primaryKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 0,
    clientCompatibility: 'codex_responses'
  }, access)
  const backupAccount = repositories.createAccount({
    providerCode: 'gpt',
    name: `流式超时回归备用账户-${label}`,
    type: 'api_key',
    credentials: {
      api_key: backupKey,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: 10,
    clientCompatibility: 'codex_responses'
  }, access)
  const apiKey = apiKeyRepository.createApiKeyRecord({
    name: `流式超时回归双账号 Key-${label}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  return { primaryAccount, backupAccount, apiKey }
}

function createMultiAccountScenarioCredential(upstreamBaseUrl: string, label: string, count: number): {
  accounts: ReturnType<typeof repositories.createAccount>[]
  apiKey: ReturnType<typeof apiKeyRepository.createApiKeyRecord>
} {
  const access = scenarioCredentialAccess()
  const group = repositories.createGroup({ name: `流式超时回归多账号分组-${label}`, providerCode: 'gpt', enabled: true }, access)
  scenarioCredentialIndex += 1
  const accounts = Array.from({ length: count }, (_, index) => repositories.createAccount({
    providerCode: 'gpt',
    name: `流式超时回归多账号-${label}-${index + 1}`,
    type: 'api_key',
    credentials: {
      api_key: `sk-stream-timeout-regression-${scenarioCredentialIndex}-multi-${index + 1}`,
      base_url: upstreamBaseUrl
    },
    groupId: group.id,
    status: 'active',
    schedulable: true,
    priority: index * 10,
    clientCompatibility: 'codex_responses'
  }, access))
  const apiKey = apiKeyRepository.createApiKeyRecord({
    name: `流式超时回归多账号 Key-${label}`,
    groupBindings: [{ groupId: group.id, priority: 1, status: 'active' }],
    status: 'active'
  }, access)
  assert(apiKey.key, '临时 API Key 未返回明文密钥')
  return { accounts, apiKey }
}

function scenarioCredentialAccess(): { systemAccountId: string; role: 'user' } {
  if (!scenarioCredentialOwnerAccess) {
    const owner = repositories.createSystemAccount({
      username: 'stream_first_output_owner',
      displayName: '流式超时回归用户',
      password: 'password',
      role: 'user',
      status: 'active',
      mustChangePassword: false
    })
    scenarioCredentialOwnerAccess = { systemAccountId: owner.id, role: 'user' }
  }
  return scenarioCredentialOwnerAccess
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
        const body = JSON.parse(Buffer.concat(bodyChunks).toString('utf8')) as { input?: unknown; messages?: unknown }
        scenario = scenarioFromOpenAIRequestBody(body) ?? scenario
      } catch {
      }
      const upstreamAuthorization = String(req.headers.authorization ?? '')

      if (scenario === 'json-response-for-stream-request') {
        res.writeHead(200, { 'content-type': 'application/json; charset=utf-8' })
        res.end(JSON.stringify({
          id: 'resp_json_for_stream',
          object: 'response',
          output_text: 'json response ok',
          usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2 }
        }))
        return
      }

      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache',
        connection: 'keep-alive'
      })
      res.flushHeaders()
      if (scenario === 'server-retry-no-first-chunk-then-success') {
        if (upstreamAuthorization.includes('-primary')) {
          return
        }
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created","response":{"id":"resp_no_first_chunk_backup","status":"in_progress"}}\n\n')
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"ok"}\n\n')
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_no_first_chunk_backup","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'server-retry-missing-terminal-then-success') {
        if (upstreamAuthorization.includes('-primary')) {
          res.write('event: response.created\n')
          res.write('data: {"type":"response.created","response":{"id":"resp_server_retry_primary","status":"in_progress"}}\n\n')
          res.end()
          return
        }
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created","response":{"id":"resp_server_retry_backup","status":"in_progress"}}\n\n')
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"ok"}\n\n')
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_server_retry_backup","status":"completed","usage":{"input_tokens":2,"output_tokens":1}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'server-retry-response-failed-then-success') {
        if (upstreamAuthorization.includes('-primary')) {
          res.write('event: response.failed\n')
          res.write('data: {"type":"response.failed","response":{"id":"resp_failed_retry_primary","status":"failed","error":{"code":"server_is_overloaded","message":"primary overloaded"}}}\n\n')
          res.end()
          return
        }
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created","response":{"id":"resp_failed_retry_backup","status":"in_progress"}}\n\n')
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"ok"}\n\n')
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_failed_retry_backup","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'server-retry-fourth-account-then-success') {
        if (!upstreamAuthorization.includes('-multi-4')) {
          res.write('event: response.failed\n')
          res.write('data: {"type":"response.failed","response":{"id":"resp_fourth_retry_failed","status":"failed","error":{"code":"server_is_overloaded","message":"candidate overloaded"}}}\n\n')
          res.end()
          return
        }
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created","response":{"id":"resp_fourth_retry_success","status":"in_progress"}}\n\n')
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"ok"}\n\n')
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_fourth_retry_success","status":"completed","usage":{"input_tokens":4,"output_tokens":1}}}\n\n')
        res.end()
        return
      }
      if (scenario.startsWith('server-retry-fuzz-')) {
        if (upstreamAuthorization.includes('-primary')) {
          sendFuzzPrimaryFailure(res, scenario)
          return
        }
        sendFuzzBackupSuccess(res, scenario)
        return
      }
      if (scenario === 'no-first-chunk') {
        return
      }
      if (scenario === 'fragmented-sse-event-keepalive') {
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created"')
        const chunks = [
          ',"response":{"id":"resp_regression"',
          ',"status":"in_progress"',
          ',"metadata":{"fragment":1}',
          '}}\n\n',
          'event: response.completed\n',
          'data: {"type":"response.completed","response":{"id":"resp_regression","status":"completed","usage":{"input_tokens":1,"output_tokens":0}}}\n\n'
        ]
        let index = 0
        const interval = setInterval(() => {
          res.write(chunks[index])
          index += 1
          if (index >= chunks.length) {
            clearInterval(interval)
            res.end()
          }
        }, 350)
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
      if (scenario === 'large-image-event-then-completed') {
        const imagePayload = 'a'.repeat(300 * 1024)
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created","response":{"id":"resp_large_image","status":"in_progress"}}\n\n')
        res.write('event: response.image_generation_call.completed\n')
        res.write(`data: {"type":"response.image_generation_call.completed","item_id":"ig_large","result":"${imagePayload}"}\n\n`)
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_large_image","status":"completed","usage":{"input_tokens":1,"output_tokens":0}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'large-image-partial-then-completed') {
        const imagePayload = 'a'.repeat(300 * 1024)
        res.write('event: response.created\n')
        res.write('data: {"type":"response.created","response":{"id":"resp_large_image_partial","status":"in_progress"}}\n\n')
        res.write('event: response.image_generation_call.partial_image\n')
        res.write(`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_large_partial","partial_image_index":0,"partial_image_b64":"${imagePayload}"}\n\n`)
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_large_image_partial","status":"completed","usage":{"input_tokens":1,"output_tokens":0}}}\n\n')
        res.end()
        return
      }
      if (scenario === 'large-image-api-event-completed') {
        const imagePayload = 'a'.repeat(300 * 1024)
        res.write('event: image_generation.completed\n')
        res.write(`data: {"type":"image_generation.completed","b64_json":"${imagePayload}","usage":{"input_tokens":1,"output_tokens":100,"total_tokens":101}}\n\n`)
        res.end()
        return
      }
      if (scenario === 'large-image-api-event-split-completed') {
        res.write('event: image_generation.completed\n')
        res.write('data: {"type":"image_generation.completed","b64_json":"')
        setTimeout(() => {
          res.write(`${'a'.repeat(300 * 1024)}split_tail_marker","usage":{"input_tokens":3,"output_tokens":4,"total_tokens":7}}\n\n`)
          res.end()
        }, 80)
        return
      }
      if (scenario === 'large-image-api-event-eof-no-boundary') {
        const imagePayload = 'a'.repeat(300 * 1024)
        res.write('event: image_generation.completed\n')
        res.write(`data: {"type":"image_generation.completed","b64_json":"${imagePayload}","usage":{"input_tokens":1,"output_tokens":100,"total_tokens":101}}`)
        res.end()
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
      if (scenario === 'client-close-after-terminal') {
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"done"}\n\n')
        res.write('event: response.completed\n')
        res.write('data: {"type":"response.completed","response":{"id":"resp_client_close_after_terminal","status":"completed","usage":{"input_tokens":1,"output_tokens":1}}}\n\n')
        setTimeout(() => {
          res.end()
        }, 500)
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
      if (scenario === 'cyber-policy-before-output') {
        res.write('event: response.failed\n')
        res.write('data: {"type":"response.failed","response":{"id":"resp_cyber_policy","status":"failed","error":{"code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk."}}}\n\n')
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
      if (scenario === 'cyber-policy-after-output') {
        res.write('event: response.output_text.delta\n')
        res.write('data: {"type":"response.output_text.delta","delta":"hello"}\n\n')
        res.write('event: response.failed\n')
        res.write('data: {"type":"response.failed","response":{"id":"resp_cyber_policy_after_output","status":"failed","error":{"code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk."}}}\n\n')
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

function scenarioFromOpenAIRequestBody(body: { input?: unknown; messages?: unknown }): string | undefined {
  return scenarioTextFromOpenAIInput(body.input) ?? scenarioTextFromOpenAIMessages(body.messages)
}

function scenarioTextFromOpenAIInput(input: unknown): string | undefined {
  if (typeof input === 'string' && input.trim()) {
    return input
  }
  if (!Array.isArray(input)) {
    return undefined
  }
  for (const item of input) {
    if (!isRecord(item)) continue
    const text = scenarioTextFromOpenAIContent(item.content)
    if (text) return text
  }
  return undefined
}

function scenarioTextFromOpenAIMessages(messages: unknown): string | undefined {
  if (!Array.isArray(messages)) {
    return undefined
  }
  for (const message of messages) {
    if (!isRecord(message)) continue
    const text = scenarioTextFromOpenAIContent(message.content)
    if (text) return text
  }
  return undefined
}

function scenarioTextFromOpenAIContent(content: unknown): string | undefined {
  if (typeof content === 'string' && content.trim()) {
    return content
  }
  if (!Array.isArray(content)) {
    return undefined
  }
  for (const part of content) {
    if (!isRecord(part)) continue
    if (typeof part.text === 'string' && part.text.trim()) {
      return part.text
    }
  }
  return undefined
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

async function assertPreCommitFuzzServerRetryScenarios(
  baseUrl: string,
  credential: ReturnType<typeof createTwoAccountScenarioCredential>
): Promise<void> {
  for (const item of preCommitFuzzServerRetryScenarios) {
    clientIpAvoidance.clearClientIpAccountAvoidanceForTest()
    const result = await requestStreamScenario(baseUrl, credential.apiKey.key, item.scenario)
    assert(result.streamText.includes(item.backupResponseId), `预提交 fuzz 失败应切备用账号完成：${item.scenario} ${result.streamText}`)
    assert(result.streamText.includes('response.completed'), `预提交 fuzz 服务端切号后应完成：${item.scenario} ${result.streamText}`)
    assert(!result.streamText.includes(item.hiddenPrimaryNeedle), `预提交 fuzz 服务端切号成功时不应下发主账号失败内容：${item.scenario} ${result.streamText}`)
    assert(!result.streamText.includes('response.failed'), `预提交 fuzz 服务端切号成功时客户端不应看到中间失败：${item.scenario} ${result.streamText}`)
    assert(!result.streamText.includes('upstream_retryable_error'), `预提交 fuzz 服务端切号成功时不应消耗 Codex 客户端重试：${item.scenario} ${result.streamText}`)
  }
  usageRecordQueue.flushAllUsageRecordQueue()
  const primaryFailedCount = usageRecordCount(credential.primaryAccount.id, false)
  assert(primaryFailedCount >= 1, '预提交 fuzz 主账号至少应记录一次失败尝试')
  assert(
    primaryFailedCount < preCommitFuzzServerRetryScenarios.length,
    `预提交 fuzz 主账号失败后应短期避让，避免每个场景都重复命中；实际失败次数 ${primaryFailedCount}`
  )
  assertUsageRecordCountAtLeast(credential.backupAccount.id, true, preCommitFuzzServerRetryScenarios.length)
}

function sendFuzzPrimaryFailure(res: http.ServerResponse, scenario: string): void {
  if (scenario === 'server-retry-fuzz-created-eof-then-success') {
    res.write('event: response.created\n')
    res.write('data: {"type":"response.created","response":{"id":"resp_fuzz_created_primary","status":"in_progress"}}\n\n')
    res.end()
    return
  }
  if (scenario === 'server-retry-fuzz-in-progress-eof-then-success') {
    res.write('event: response.created\n')
    res.write('data: {"type":"response.created","response":{"id":"resp_fuzz_in_progress_primary","status":"in_progress"}}\n\n')
    res.write('event: response.in_progress\n')
    res.write('data: {"type":"response.in_progress","response":{"id":"resp_fuzz_in_progress_primary","status":"in_progress"}}\n\n')
    res.end()
    return
  }
  if (scenario === 'server-retry-fuzz-error-event-then-success') {
    res.write('event: error\n')
    res.write('data: {"type":"error","error":{"code":"internal_server_error","message":"fuzz primary error event"}}\n\n')
    res.end()
    return
  }
  if (scenario === 'server-retry-fuzz-failed-event-then-success') {
    res.write('event: response.failed\n')
    res.write('data: {"type":"response.failed","response":{"id":"resp_fuzz_failed_primary","status":"failed","error":{"code":"internal_server_error","message":"fuzz primary failed event"}}}\n\n')
    res.end()
    return
  }
  res.end()
}

function sendFuzzBackupSuccess(res: http.ServerResponse, scenario: string): void {
  const match = preCommitFuzzServerRetryScenarios.find((item) => item.scenario === scenario)
  const responseId = match?.backupResponseId ?? 'resp_fuzz_backup'
  res.write('event: response.created\n')
  res.write(`data: {"type":"response.created","response":{"id":"${responseId}","status":"in_progress"}}\n\n`)
  res.write('event: response.output_text.delta\n')
  res.write('data: {"type":"response.output_text.delta","delta":"ok"}\n\n')
  res.write('event: response.completed\n')
  res.write(`data: {"type":"response.completed","response":{"id":"${responseId}","status":"completed","usage":{"input_tokens":4,"output_tokens":1}}}\n\n`)
  res.end()
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

async function requestFragmentedSseEventKeepalive(baseUrl: string, apiKey: string): Promise<{ streamText: string; durationMs: number }> {
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
    headers: codexStreamHeaders(apiKey, 'fragmented-sse-event-keepalive'),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'fragmented-sse-event-keepalive',
      stream: true
    })
  })
  if (response.status !== 200) {
    throw new Error(`碎片化 SSE 事件保活场景状态码异常：${response.status} ${await response.text()}`)
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

async function requestAndCloseAfterTerminal(baseUrl: string, apiKey: string): Promise<void> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 10,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const traceId = traceIdForSampledSuccessBucket()
  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: {
      ...codexStreamHeaders(apiKey, traceId),
      'x-trace-id': traceId
    },
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'client-close-after-terminal',
      stream: true
    })
  })
  assert.equal(response.status, 200)
  assert(response.body, '终止后关闭场景应返回响应流')
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let streamText = ''
  while (true) {
    const result = await reader.read()
    if (result.done) {
      break
    }
    streamText += decoder.decode(result.value, { stream: true })
    if (streamText.includes('response.completed')) {
      await reader.cancel()
      break
    }
  }
  assert(streamText.includes('response.completed'), `终止后关闭场景未读到完成事件：${streamText}`)
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
  scenario: string,
  traceId?: string
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
    headers: {
      ...codexStreamHeaders(apiKey, traceId ?? scenario),
      ...(traceId ? { 'x-trace-id': traceId } : {})
    },
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

async function requestJsonResponseForStreamRequest(
  baseUrl: string,
  apiKey: string
): Promise<{ text: string; contentType: string }> {
  settingsRepository.updateSettings({
    streamCircuitBreakerEnabled: true,
    streamRequestTimeoutSeconds: 10,
    streamIdleTimeoutSeconds: 10,
    temporaryUnschedulableRetryAttempts: 0
  })
  gatewayCache.clearGatewayRuntimeCache()

  const response = await fetch(`${baseUrl}/v1/responses`, {
    method: 'POST',
    headers: codexStreamHeaders(apiKey, 'json-response-for-stream-request'),
    body: JSON.stringify({
      model: 'gpt-4o-mini',
      input: 'json-response-for-stream-request',
      stream: true
    })
  })
  assert.equal(response.status, 200)
  return {
    text: await response.text(),
    contentType: response.headers.get('content-type') ?? ''
  }
}

function assertFailedUsageRecordErrorCode(accountId: string, errorCode: string): void {
  const records = repositories.listUsageRecords(undefined, { result: 'failed', page: 1, pageSize: 50 })
  const record = records.items.find((item) => item.accountId === accountId && item.success === false)
  assert(record, `未找到账号 ${accountId} 的失败使用记录`)
  assert.equal(record.errorCode, errorCode, `失败使用记录错误码不正确：${record.errorCode}`)
}

function assertFailedUsageRecordExists(accountId: string): void {
  const records = repositories.listUsageRecords(undefined, { result: 'failed', page: 1, pageSize: 50 })
  const record = records.items.find((item) => item.accountId === accountId && item.success === false)
  assert(record, `未找到账号 ${accountId} 的失败使用记录`)
}

function assertUsageRecordCountAtLeast(accountId: string, success: boolean, expectedCount: number): void {
  const records = usageRecordsForAccount(accountId, success)
  assert(
    records.length >= expectedCount,
    `账号 ${accountId} ${success ? '成功' : '失败'}使用记录数量不足：期望至少 ${expectedCount}，实际 ${records.length}`
  )
}

function usageRecordCount(accountId: string, success: boolean): number {
  return usageRecordsForAccount(accountId, success).length
}

function usageRecordsForAccount(accountId: string, success: boolean) {
  return repositories
    .listUsageRecords(undefined, { page: 1, pageSize: 500 })
    .items
    .filter((item) => item.accountId === accountId && item.success === success)
}

function assertSuccessfulUsageRecord(
  accountId: string,
  expectedUsage?: {
    inputTokens?: number
    outputTokens?: number
  }
): void {
  const records = repositories.listUsageRecords(undefined, { result: 'success', page: 1, pageSize: 50 })
  const record = records.items.find((item) => item.accountId === accountId && item.success === true)
  const account = repositories.listAccounts(scenarioCredentialAccess()).find((item) => item.id === accountId)
  const accountRecords = repositories
    .listUsageRecords(undefined, { page: 1, pageSize: 200 })
    .items
    .filter((item) => item.accountId === accountId)
    .map((item) => `${item.success ? 'success' : 'failed'}:${item.statusCode ?? 'no_status'}:${item.errorCode ?? 'no_error'}`)
  assert(record, `账号 ${account?.name ?? accountId} (${accountId}) 未找到成功使用记录；该账号已有记录：${accountRecords.join(', ') || '无'}`)
  assert.equal(record.errorCode, undefined, `成功使用记录不应写错误码：${record.errorCode}`)
  assert.equal(record.statusCode, 200, `成功使用记录应保留 200 状态码：${record.statusCode}`)
  if (expectedUsage?.inputTokens !== undefined) {
    assert.equal(record.inputTokens, expectedUsage.inputTokens, `成功使用记录 input token 不正确：${record.inputTokens}`)
  }
  if (expectedUsage?.outputTokens !== undefined) {
    assert.equal(record.outputTokens, expectedUsage.outputTokens, `成功使用记录 output token 不正确：${record.outputTokens}`)
  }
}

async function waitForSuccessfulUsageRecord(accountId: string): Promise<void> {
  let lastError: unknown
  for (let attempt = 0; attempt < 20; attempt += 1) {
    usageRecordQueue.flushAllUsageRecordQueue()
    try {
      assertSuccessfulUsageRecord(accountId)
      return
    } catch (error) {
      lastError = error
    }
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 25))
  }
  throw lastError
}

function assertNoClientAbortedAuditLogForAccount(accountId: string): void {
  const row = databaseModule.getDatasetDatabase()
    .prepare(`
      SELECT COUNT(*) AS count
      FROM audit_logs audit_logs
      INNER JOIN audit_log_attempts attempts ON attempts.audit_log_id = audit_logs.id
      WHERE attempts.account_id = ?
        AND audit_logs.audit_outcome = 'client_aborted'
    `)
    .get(accountId) as { count?: number } | undefined
  assert.equal(Number(row?.count ?? 0), 0, '收到 response.completed 后客户端关闭不应写 client_aborted 审计')
}

function assertUsageRecordBodyOmitted(accountId: string, traceId: string): void {
  const records = repositories.listUsageRecords(undefined, { page: 1, pageSize: 100 })
  const record = records.items.find((item) => item.accountId === accountId && item.traceId === traceId)
  assert(record, `未找到图像流使用记录：${accountId} ${traceId}`)
  const detail = repositories.getUsageRecordDetail(record.id)
  assert(detail, `未找到图像流使用记录详情：${record.id}`)
  const requestSnapshot = detail.requestSnapshot as { body?: unknown; bodyOmission?: { reason?: unknown } } | undefined
  const responseSnapshot = detail.responseSnapshot as { bodyText?: unknown; bodyOmission?: { reason?: unknown } } | undefined
  assert.equal(requestSnapshot?.body, undefined, '图像流使用记录不应保留请求 body')
  assert.equal(responseSnapshot?.bodyText, undefined, '图像流使用记录不应保留响应 bodyText')
  assert.equal(requestSnapshot?.bodyOmission?.reason, 'image_stream_payload', '图像流使用记录请求快照应保留正文省略原因')
  assert.equal(responseSnapshot?.bodyOmission?.reason, 'image_stream_payload', '图像流使用记录响应快照应保留正文省略原因')
  const serialized = JSON.stringify({
    requestSnapshot: detail.requestSnapshot,
    responseSnapshot: detail.responseSnapshot
  })
  assert(!serialized.includes('a'.repeat(1024)), '图像流使用记录快照不应包含图片 base64 片段')
}

async function assertImageStreamAuditBodyOmitted(traceId: string, expectedEventType: string): Promise<void> {
  const logs = repositories.listAuditLogs({ traceId, page: 1, pageSize: 20 })
  assert.equal(logs.items.length, 1, `图像流成功采样审计日志数量不正确：${traceId}`)
  const detail = repositories.getAuditLogDetail(logs.items[0].id)
  assert(detail, `未找到图像流审计详情：${traceId}`)
  let bodyOmissionMetadata: Record<string, unknown> | undefined
  const unexpectedBodyPayloads: string[] = []
  for (const payload of detail.payloads) {
    const payloadDetail = await repositories.getAuditLogPayload(detail.id, payload.id)
    assert(payloadDetail, `未找到图像流审计 payload：${payload.id}`)
    if (payload.partType !== 'gateway_metadata') {
      if (payloadDetail.bodyText !== undefined || payloadDetail.bodyBase64 !== undefined || payloadDetail.bodyTotalBytes !== 0) {
        unexpectedBodyPayloads.push(`${payload.partType}:${payloadDetail.bodyTotalBytes}`)
      }
      continue
    }
    if (!payloadDetail.bodyText) continue
    assert(!payloadDetail.bodyText.includes('a'.repeat(1024)), '审计元信息不应包含图片 base64 片段')
    const body = JSON.parse(payloadDetail.bodyText) as { label?: string; metadata?: Record<string, unknown> }
    if (body.label === 'stream_body_omission') {
      bodyOmissionMetadata = body.metadata
    }
  }
  assert(bodyOmissionMetadata, '图像流审计应保留正文省略元信息')
  assert.equal(bodyOmissionMetadata.reason, 'image_stream_payload', '图像流审计省略原因不正确')
  assert.equal(bodyOmissionMetadata.imageOutputReceived, true, '图像流审计应标记已检测到图片输出')
  assert(Number(bodyOmissionMetadata.totalUpstreamBytes ?? 0) > 0, '图像流审计应保留上游字节计数')
  assert.equal(unexpectedBodyPayloads.length, 0, `图像流审计不应保留任何非元信息正文：${unexpectedBodyPayloads.join(',')}`)
  const recentTypes = Array.isArray(bodyOmissionMetadata.recentSseEventTypes) ? bodyOmissionMetadata.recentSseEventTypes : []
  assert(recentTypes.includes(expectedEventType), `图像流审计最近事件类型应包含 ${expectedEventType}，实际 ${recentTypes.join(',')}`)
}

function traceIdForSampledSuccessBucket(prefix = 'stream-client-close-after-terminal'): string {
  for (let index = 0; index < 100000; index += 1) {
    const traceId = `${prefix}-${index}`
    if (sampleBucketForTraceId(traceId) < 1000) {
      return traceId
    }
  }
  throw new Error('无法构造成功采样 traceId')
}

function sampleBucketForTraceId(traceId: string): number {
  const digest = createHash('sha256').update(traceId).digest()
  return digest.readUInt32BE(0) % 10000
}

async function assertResponseInspectionAuditMetadata(
  accountId: string,
  expected: {
    upstreamErrorCode: string
    rewriteErrorCode: string
    fallbackReason: string
    downstreamWritten: boolean
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
      if (body.metadata?.responsePolicyMatched === true) {
        metadata = body.metadata
        break
      }
    }
    if (metadata) break
  }
  assert(metadata, `未找到账号 ${accountId} 的响应检查审计日志`)
  assert.equal(metadata.responseInspectionIntercepted, true, '审计元信息应标记 responseInspectionIntercepted')
  assert.equal(metadata.fallbackReason, expected.fallbackReason, '审计元信息兜底原因不正确')
  assert.equal(metadata.upstreamErrorCode, expected.upstreamErrorCode, '审计元信息上游错误码不正确')
  assert.equal(metadata.rewriteErrorCode, expected.rewriteErrorCode, '审计元信息改写错误码不正确')
  assert.equal(metadata.downstreamWritten, expected.downstreamWritten, '审计元信息 downstreamWritten 不正确')
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
