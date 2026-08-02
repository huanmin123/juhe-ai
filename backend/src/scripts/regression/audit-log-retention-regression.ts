import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { EventEmitter } from 'node:events'
import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { gunzipSync } from 'node:zlib'
import type { Request, Response } from 'express'

import { createApiKeyRecordWithRouteStrategy } from '../shared/route-strategy-fixture.js'
import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-log-retention-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'audit-log-retention.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'audit-log-retention-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, backgroundIpc, usageRecordQueue, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/background/background-ipc.js'),
  import('../../modules/gateway/usage/record-queue.service.js'),
  import('../../storage/usage-record-shards.js')
])
const auditCapture = await import('../../modules/gateway/audit/capture.service.js')
const auditQueue = await import('../../modules/audit-logs/audit-log-queue.service.js')
const gatewayBodyMiddleware = await import('../../modules/gateway/request/body-middleware.js')

assertAuditPayloadCleanupUsesAsyncFiles()

const now = '2026-05-11T00:00:00.000Z'
const repeatedBody = JSON.stringify({
  error: 'rate limited',
  detail: 'x'.repeat(8192)
})
const largeFailedRequestBody = Buffer.from(JSON.stringify({
  model: 'gpt-5.4-mini',
  input: 'failed-payload-prefix-' + 'x'.repeat(2 * 1024 * 1024 + 128 * 1024),
  metadata: { regressionCase: 'large_failed_payload_summary' }
}), 'utf8')
const largeSuccessRequestBody = Buffer.from(JSON.stringify({
  model: 'gpt-5.4-mini',
  input: 'success-payload-prefix-' + 'y'.repeat(600 * 1024),
  metadata: { regressionCase: 'large_success_payload_summary' }
}), 'utf8')

try {
  assertHttpCompletionTiming()
  assertCaptureCancellationLifecycle()
  const unsampledTraceId = traceIdForBucket((bucket) => bucket >= 1000)
  const sampledTraceId = traceIdForBucket((bucket) => bucket < 1000)
  finalizeSuccessfulRequest(unsampledTraceId)
  auditQueue.flushAllAuditLogQueue()
  const unsampledEvents = repositories.listAuditLogs({ traceId: unsampledTraceId })
  assert.equal(unsampledEvents.total, 1, '未命中 10% 稳定采样的成功请求应按热保留写入 audit_logs')
  const unsampledDetail = repositories.getAuditLogDetail(unsampledEvents.items[0]?.id ?? '')
  assert.equal(unsampledDetail?.sampleReason, 'success_hot_full_retention', '未采样成功请求应标记为成功热保留')

  finalizeSuccessfulRequest(sampledTraceId)
  auditQueue.flushAllAuditLogQueue()
  assert.equal(repositories.listAuditLogs({ traceId: sampledTraceId }).total, 1, '命中 10% 稳定采样的成功请求应写入 audit_logs')

  const sensitiveHeaderTraceId = 'trace-sensitive-header-raw-audit'
  finalizeSensitiveHeaderAudit(sensitiveHeaderTraceId)
  auditQueue.flushAllAuditLogQueue()
  const sensitiveHeaderAuditId = repositories.listAuditLogs({ traceId: sensitiveHeaderTraceId }).items[0]?.id ?? ''
  const sensitiveHeaderDetail = repositories.getAuditLogDetail(sensitiveHeaderAuditId)
  assert(sensitiveHeaderDetail, '敏感 header 原文审计事件应写入详情')
  assert(sensitiveHeaderDetail.queryString?.includes('audit-query-token'), '审计主记录 queryString 应保留敏感查询参数原文')
  assert.equal(sensitiveHeaderDetail.queryString?.includes('token=%5Bredacted%5D'), false, '审计主记录 queryString 不应写入脱敏占位')
  const sensitiveHeaderSerializedDetail = JSON.stringify(sensitiveHeaderDetail)
  assert(sensitiveHeaderSerializedDetail.includes('audit-upstream-query-token'), '审计 attempt upstreamUrl 应保留敏感查询参数原文')
  assert.equal(sensitiveHeaderSerializedDetail.includes('token=%5Bredacted%5D'), false, '审计 attempt upstreamUrl 不应写入脱敏占位')
  const sensitiveHeaderPayloads = await Promise.all(sensitiveHeaderDetail.payloads.map((payload) => repositories.getAuditLogPayload(sensitiveHeaderAuditId, payload.id)))
  const sensitiveHeaderSerialized = JSON.stringify(sensitiveHeaderPayloads)
  for (const marker of [
    'Bearer client-secret-token',
    'Bearer upstream-secret-token',
    'session-secret-cookie',
    'upstream-set-cookie-secret',
    'gateway-set-cookie-secret',
    'upstream-x-api-key-secret',
    'audit-google-key-secret',
    'upstream-google-key-secret',
    'sk-account-secret',
    'raw-client-session-id',
    'raw-client-turn-state',
    'raw-body-session-id',
    'raw-upstream-session-id',
    'raw-response-session-id'
  ]) {
    assert(sensitiveHeaderSerialized.includes(marker), `原始审计 payload 应保留原文：${marker}`)
  }
  assert.equal(sensitiveHeaderSerialized.includes('[redacted]'), false, '原始审计 payload 不应写入脱敏占位')

  const proxyCredentialTraceId = 'trace-proxy-credential-raw'
  finalizeProxyCredentialAudit(proxyCredentialTraceId)
  auditQueue.flushAllAuditLogQueue()
  const proxyCredentialAuditId = repositories.listAuditLogs({ traceId: proxyCredentialTraceId }).items[0]?.id ?? ''
  const proxyCredentialDetail = repositories.getAuditLogDetail(proxyCredentialAuditId)
  assert(proxyCredentialDetail, '代理凭据原文审计事件应写入详情')
  const proxyCredentialRow = databaseModule.getDatasetDatabase()
    .prepare('SELECT proxy_url FROM audit_log_attempts WHERE audit_log_id = ?')
    .get(proxyCredentialAuditId) as { proxy_url?: string } | undefined
  const proxyCredentialSerialized = JSON.stringify({
    row: proxyCredentialRow,
    detail: proxyCredentialDetail
  })
  assert(proxyCredentialSerialized.includes('proxy-user'), 'audit_log_attempts.proxy_url 应保留代理用户名')
  assert(proxyCredentialSerialized.includes('proxy-pass'), 'audit_log_attempts.proxy_url 应保留代理密码')
  assert(proxyCredentialSerialized.includes('http://proxy-user:proxy-pass@127.0.0.1:18080'), '审计详情应保留代理 URL 原文')

  const sensitiveUsageRecordId = usageRecordShards.generateUsageRecordId(now, 'usage_sensitive_headers')
  usageRecordQueue.enqueueUsageRecord({
    id: sensitiveUsageRecordId,
    traceId: 'trace-usage-sensitive-headers',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: 'group_default',
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    success: false,
    statusCode: 502,
    requestSnapshot: {
      method: 'POST',
      path: '/v1/responses',
      originalUrl: '/v1/responses?token=usage-query-token&safe=ok',
      traceId: 'trace-usage-sensitive-headers',
      headers: {
        authorization: 'Bearer usage-client-token',
        'api-key': 'usage-api-key-secret',
        'openai-api-key': 'usage-openai-api-key-secret',
        'x-goog-api-key': 'usage-google-api-key-secret',
        'content-type': 'application/json'
      }
    },
    responseSnapshot: {
      upstreamUrl: 'https://api.openai.com/v1/responses',
      statusCode: 502,
      headers: {
        'set-cookie': 'usage-response-cookie',
        'x-api-key': 'usage-response-key',
        'content-type': 'application/json'
      },
      lastUpstreamAttempt: {
        accountId: 'account_sensitive_header',
        accountName: 'Sensitive Header Account',
        upstreamUrl: 'https://api.openai.com/v1/responses?api_key=usage-upstream-query-key',
        statusCode: 502,
        headers: {
          authorization: 'Bearer usage-last-upstream-token',
          'proxy-authorization': 'Basic usage-proxy-token',
          'x-goog-api-key': 'usage-last-upstream-google-key',
          'content-type': 'application/json'
        }
      }
    }
  })
  usageRecordQueue.flushAllUsageRecordQueue()
  const sensitiveUsageRecord = repositories.getUsageRecordDetail(sensitiveUsageRecordId)
  assert(sensitiveUsageRecord, '使用记录敏感信息原文样本应能按分片 ID 读取')
  const sensitiveUsageSerialized = JSON.stringify({
    requestSnapshot: sensitiveUsageRecord?.requestSnapshot,
    responseSnapshot: sensitiveUsageRecord?.responseSnapshot
  })
  assertAllPresent(sensitiveUsageSerialized, [
    'usage-client-token',
    'usage-api-key-secret',
    'usage-openai-api-key-secret',
    'usage-google-api-key-secret',
    'usage-response-cookie',
    'usage-response-key',
    'usage-last-upstream-token',
    'usage-proxy-token',
    'usage-last-upstream-google-key',
  ], '使用记录 header snapshot 应保留原文')
  assertAllPresent(sensitiveUsageSerialized, [
    'usage-query-token',
    'usage-upstream-query-key'
  ], '使用记录 URL 查询参数仍按原文保留')
  assert(sensitiveUsageSerialized.includes('safe=ok'), '使用记录请求 snapshot 应保留安全查询参数')
  assert.equal(sensitiveUsageSerialized.includes('[redacted]'), false, '使用记录 header snapshot 不应写入脱敏占位')

  const overflowTraceId = 'trace-overflow-retained'
  finalizeOverflowFailedRequest(overflowTraceId)
  auditQueue.flushAllAuditLogQueue()
  const overflowEvents = repositories.listAuditLogs({ traceId: overflowTraceId })
  assert.equal(overflowEvents.total, 1, 'active capture 超限时应保留失败事件')
  const overflowDetail = repositories.getAuditLogDetail(overflowEvents.items[0]?.id ?? '')
  assert.equal(overflowDetail?.captureStatus, 'overflow', 'server 常驻正文超过 64MiB 后事件必须标记 overflow')
  assert((overflowDetail?.payloadCount ?? 0) > 0, '超大失败 body 应保留摘要 payload')
  assert.equal(overflowDetail?.payloads.some((payload) => payload.partType === 'client_request'), false, '超限后不得继续持有 client_request 正文')
  const overflowMetadata = overflowDetail?.payloads.find((payload) => payload.partType === 'gateway_metadata')
  assert.equal(overflowMetadata?.captureStatus, 'overflow', '超限后应保留轻量 overflow metadata')

  auditQueue.recordDroppedAuditCapture({
    traceId: 'trace-body-rejected-retained',
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: 1024 * 1024,
    reason: 'gateway_body_rejected',
    method: 'POST',
    path: '/v1/responses',
    queryString: 'token=body-rejected-query-token&api_key=body-rejected-api-key&safe=ok',
    statusCode: 413,
    errorPhase: 'gateway',
    errorCode: 'entity.too.large',
    errorMessage: '请求体过大',
    contentType: 'application/json'
  })
  auditQueue.flushAllAuditLogQueue()
  const rejectedEvents = repositories.listAuditLogs({ traceId: 'trace-body-rejected-retained' })
  assert.equal(rejectedEvents.total, 1, '请求体被网关拒绝时也应保留失败事件')
  const rejectedDetail = repositories.getAuditLogDetail(rejectedEvents.items[0]?.id ?? '')
  assert.equal(rejectedDetail?.captureStatus, 'overflow', '请求体被网关拒绝时应标记为 overflow')
  const rejectedClientPayload = rejectedDetail?.payloads.find((payload) => payload.partType === 'client_request')
  assert(rejectedClientPayload, '请求体被网关拒绝时应保留无正文客户端请求 payload')
  assert.equal(rejectedClientPayload.captureStatus, 'overflow', '被拒绝正文的 payload 应标记为 overflow')
  assert.equal(rejectedClientPayload.sizeBytes, 1024 * 1024, '被拒绝正文的 payload 应保留原始字节数')
  assert.equal(rejectedClientPayload.contentType, 'application/json', '被拒绝正文的 payload 可保留 content type')
  assert.equal(rejectedClientPayload.hasHeaders, false, '被拒绝正文的 payload 不应保存 headers')
  assert.equal(rejectedClientPayload.hasBody, false, '被拒绝正文的 payload 不应保存 body')
  assert(rejectedDetail?.queryString?.includes('body-rejected-query-token'), '早期拒绝审计 queryString 应保留 token 参数原文')
  assert(rejectedDetail?.queryString?.includes('body-rejected-api-key'), '早期拒绝审计 queryString 应保留 api_key 参数原文')
  assert.equal(rejectedDetail?.queryString?.includes('token=%5Bredacted%5D'), false, '早期拒绝审计 queryString 不应写入 token 脱敏占位')
  assert.equal(rejectedDetail?.queryString?.includes('api_key=%5Bredacted%5D'), false, '早期拒绝审计 queryString 不应写入 api_key 脱敏占位')

  auditQueue.recordDroppedAuditCapture({
    traceId: 'trace-body-overloaded-without-overflow-payload',
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: 512 * 1024,
    reason: 'gateway_body_rejected',
    method: 'POST',
    path: '/v1/responses',
    statusCode: 503,
    errorPhase: 'gateway',
    errorCode: 'gateway_body_in_flight_limit_exceeded',
    errorMessage: '网关请求体在途总量过高，请稍后重试',
    contentType: 'application/json'
  })
  auditQueue.flushAllAuditLogQueue()
  const overloadedEvents = repositories.listAuditLogs({ traceId: 'trace-body-overloaded-without-overflow-payload' })
  const overloadedDetail = repositories.getAuditLogDetail(overloadedEvents.items[0]?.id ?? '')
  assert.equal(
    overloadedDetail?.payloads.some((payload) => payload.captureStatus === 'overflow'),
    false,
    '非 413 body rejection 不应生成 overflow payload'
  )

  await gatewayBodyMiddleware.recordGatewayBodyRejection({
    method: 'POST',
    path: '/v1/responses',
    originalUrl: '/v1/responses',
    headers: { 'content-type': 'application/json' }
  } as never, {
    statusCode: 413,
    responsePayload: {
      error: {
        message: '请求体过大',
        type: 'request_too_large'
      }
    },
    rawBodyBytes: 2 * 1024 * 1024 + 8,
    reason: 'gateway_body_size_limit',
    errorCode: 'request_too_large',
    errorMessage: '请求体过大',
    limitBytes: 2 * 1024 * 1024,
    limitScope: 'text'
  })
  auditQueue.flushAllAuditLogQueue()
  const limitedEvents = repositories.listAuditLogs({ path: '/v1/responses', statusCode: 413 })
  const limitedDetail = limitedEvents.items
    .map((event) => repositories.getAuditLogDetail(event.id))
    .find((detail) => detail?.errorMessage?.includes('limitScope=text'))
  assert.match(
    limitedDetail?.errorMessage ?? '',
    /rawBodyBytes=2097160, limitBytes=2097152, limitScope=text/,
    '正文超限审计错误描述应包含实际字节数、上限和作用域'
  )

  const largeFailedTraceId = 'trace-large-failed-payload-summary'
  finalizeFailedRequestWithBody(largeFailedTraceId, largeFailedRequestBody)
  auditQueue.flushAllAuditLogQueue()
  const largeFailedEvents = repositories.listAuditLogs({ traceId: largeFailedTraceId })
  assert.equal(largeFailedEvents.total, 1, '失败大请求应保留审计事件')
  const largeFailedEvent = largeFailedEvents.items[0]
  const largeFailedDetail = repositories.getAuditLogDetail(largeFailedEvent.id)
  assert((largeFailedDetail?.rawPayloadBytes ?? 0) > largeFailedRequestBody.byteLength, '失败大请求报表原始字节应按原始 body 计入')
  assert((largeFailedDetail?.compressedPayloadBytes ?? 0) < (largeFailedDetail?.rawPayloadBytes ?? 0), '失败大请求报表落盘字节应小于原始逻辑字节')
  assertAuditPayloadByteColumns(largeFailedEvent.id, {
    compressedLessThanRaw: true,
    rawGreaterThan: largeFailedRequestBody.byteLength
  })
  const largeFailedClientPayload = largeFailedDetail?.payloads.find((payload) => payload.partType === 'client_request')
  assert(largeFailedClientPayload, '失败大请求应保留客户端请求 payload 摘要')
  assert.equal(largeFailedClientPayload.captureStatus, 'summary_only', '超过 2MB 的失败请求 body 应转为摘要保全')
  assert.equal(largeFailedClientPayload.bodySha256, sha256Buffer(largeFailedRequestBody), '失败大请求摘要 payload 的 bodySha256 应指向原始 body')
  assert(largeFailedClientPayload.sizeBytes > largeFailedRequestBody.byteLength, 'payload 原始大小应按原始 body 加 headers 计入')
  const largeFailedPayloadDetail = await repositories.getAuditLogPayload(largeFailedEvent.id, largeFailedClientPayload.id, { limit: 1024 * 1024 })
  const largeFailedSummary = JSON.parse(largeFailedPayloadDetail?.bodyText ?? '{}') as Record<string, unknown>
  assert.equal(largeFailedSummary.type, 'audit_payload_summary', '失败大请求读取正文应返回摘要 JSON')
  assert.equal(largeFailedSummary.originalSha256, sha256Buffer(largeFailedRequestBody), '失败大请求摘要 JSON 应记录原始 body hash')
  assert.equal(largeFailedSummary.originalSizeBytes, largeFailedRequestBody.byteLength, '摘要 JSON 应记录原始 body 大小')
  assert(typeof largeFailedSummary.headBase64 === 'string' && largeFailedSummary.headBase64.length > 0, '摘要 JSON 应保留头部窗口')
  assert(typeof largeFailedSummary.tailBase64 === 'string' && largeFailedSummary.tailBase64.length > 0, '摘要 JSON 应保留尾部窗口')
  assert((largeFailedPayloadDetail?.bodyTotalBytes ?? 0) < largeFailedRequestBody.byteLength, '摘要 blob 不应保存完整失败大 body')
  assert.equal(largeFailedSummary.json, undefined, '审计摘要不应为了展示解析原始 JSON Body')

  const largeSuccessTraceId = traceIdForBucket((bucket) => bucket < 1000, 'trace-large-success-summary')
  finalizeSuccessfulRequestWithBody(largeSuccessTraceId, largeSuccessRequestBody)
  auditQueue.flushAllAuditLogQueue()
  const largeSuccessEvents = repositories.listAuditLogs({ traceId: largeSuccessTraceId })
  assert.equal(largeSuccessEvents.total, 1, '命中采样的成功大请求应保留审计事件')
  const largeSuccessDetail = repositories.getAuditLogDetail(largeSuccessEvents.items[0]?.id ?? '')
  const largeSuccessClientPayload = largeSuccessDetail?.payloads.find((payload) => payload.partType === 'client_request')
  assert(largeSuccessClientPayload, '成功大请求应保留客户端请求 payload 摘要')
  assert.equal(largeSuccessClientPayload.captureStatus, 'summary_only', '超过 512KB 的成功样本 body 应转为摘要保全')
  assert.equal(largeSuccessClientPayload.bodySha256, sha256Buffer(largeSuccessRequestBody), '成功摘要 payload 的 bodySha256 应指向原始 body')
  assert(largeSuccessClientPayload.sizeBytes > largeSuccessRequestBody.byteLength, '成功摘要 payload 原始大小应按原始 body 加 headers 计入')
  assertAuditPayloadByteColumns(largeSuccessEvents.items[0]?.id ?? '', {
    compressedLessThanRaw: true,
    rawGreaterThan: largeSuccessRequestBody.byteLength
  })

  const hotTrimSuccessTraceId = traceIdForBucket((bucket) => bucket >= 1000, 'trace-success-hot-trim')
  finalizeSuccessfulRequestWithBody(hotTrimSuccessTraceId, largeSuccessRequestBody)
  auditQueue.flushAllAuditLogQueue()
  const hotTrimSuccessEvents = repositories.listAuditLogs({ traceId: hotTrimSuccessTraceId })
  assert.equal(hotTrimSuccessEvents.total, 1, '未采样成功大请求应先按 1 小时热保留写入审计')
  const hotTrimSuccessDetail = repositories.getAuditLogDetail(hotTrimSuccessEvents.items[0]?.id ?? '')
  assert.equal(hotTrimSuccessDetail?.sampleReason, 'success_hot_full_retention', '未采样成功大请求应标记为成功热保留')

  const hotTrimmed = await repositories.cleanupAuditLogsByRetentionAsync({
    successHotCutoffCreatedAt: '2999-01-01T00:00:00.000Z',
    successCutoffCreatedAt: '2000-01-01T00:00:00.000Z',
    failureCutoffCreatedAt: '2000-01-01T00:00:00.000Z',
    errorGroupCutoffUpdatedAt: '2000-01-01T00:00:00.000Z',
    limit: 100
  })
  assert(hotTrimmed >= 1, '热窗口后置清理应降级未命中长期采样的普通成功请求详情')
  assert.equal(repositories.listAuditLogs({ traceId: hotTrimSuccessTraceId }).total, 1, '未采样成功请求超过热窗口后应保留轻量 envelope')
  const hotTrimmedDetail = repositories.getAuditLogDetail(hotTrimSuccessEvents.items[0]?.id ?? '')
  assert.equal(hotTrimmedDetail?.captureStatus, 'metadata_only', '热窗口后应标记 metadata_only')
  assert.equal(hotTrimmedDetail?.payloads.length, 0, '热窗口后应删除 payload 详情')
  assert.equal(hotTrimmedDetail?.attempts.length, 0, '热窗口后应删除 attempt 详情')
  assert((repositories.getAuditLogDetail(largeSuccessEvents.items[0]?.id ?? '')?.payloads.length ?? 0) > 0, '命中长期采样的成功请求不应被热窗口清理降级')
  assert((repositories.getAuditLogDetail(sensitiveHeaderAuditId)?.payloads.length ?? 0) > 0, '失败请求不应被成功热窗口清理降级')

  const nonPersistedSources = [
    'account_health_check',
    'runtime_recovery_probe',
    'cooldown_retest'
  ] as const
  const queueLengthBeforeNonPersisted = auditQueue.getAuditLogQueueRuntime().queueLength
  for (const [index, trafficSource] of nonPersistedSources.entries()) {
    auditQueue.enqueueAuditLog({
      ...auditLog(`audit_non_persisted_${index}`, `trace-non-persisted-${trafficSource}`, JSON.stringify({ trafficSource })),
      trafficSource
    })
  }
  assert.equal(auditQueue.getAuditLogQueueRuntime().queueLength, queueLengthBeforeNonPersisted, '后台来源不应进入本地审计队列')
  auditQueue.flushAllAuditLogQueue()
  for (const trafficSource of nonPersistedSources) {
    assert.equal(repositories.listAuditLogs({ traceId: `trace-non-persisted-${trafficSource}` }).total, 0, `后台来源不得写入 audit_logs：${trafficSource}`)
  }
  for (const trafficSource of ['hybrid_scoring', 'hybrid_quality_scoring'] as const) {
    auditQueue.enqueueAuditLog({
      ...auditLog(`audit_persisted_${trafficSource}`, `trace-persisted-${trafficSource}`, JSON.stringify({ trafficSource })),
      trafficSource,
      auditOutcome: 'success',
      success: true,
      finalStatusCode: 200,
      errorPhase: undefined,
      errorCode: undefined,
      errorMessage: undefined,
      sampleReason: 'success_hot_full_retention'
    })
  }
  auditQueue.flushAllAuditLogQueue()
  for (const trafficSource of ['hybrid_scoring', 'hybrid_quality_scoring'] as const) {
    assert.equal(repositories.listAuditLogs({ traceId: `trace-persisted-${trafficSource}` }).total, 1, `用户混合请求来源应写入 audit_logs：${trafficSource}`)
  }
  repositories.createAuditLogsBatch([{
    ...auditLog('audit_non_persisted_repository_gate', 'trace-non-persisted-repository-gate', JSON.stringify({ source: 'repository' })),
    trafficSource: 'cooldown_retest'
  }])
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-non-persisted-repository-gate' }).total, 0, '仓储层必须阻断绕过队列的后台来源')

  const legacyAuditId = 'audit_legacy_non_persisted_read_filter'
  repositories.createAuditLogsBatch([{
    ...auditLog(legacyAuditId, 'trace-legacy-non-persisted-read-filter', JSON.stringify({ legacy: true })),
    auditOutcome: 'success',
    success: true,
    finalStatusCode: 200,
    errorPhase: undefined,
    errorCode: undefined,
    errorMessage: undefined,
    sampleReason: 'success_hot_full_retention'
  }])
  databaseModule.getDatasetDatabase()
    .prepare('UPDATE audit_logs SET traffic_source = ? WHERE id = ?')
    .run('account_health_check', legacyAuditId)
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-legacy-non-persisted-read-filter' }).total, 0, '历史后台来源不应进入审计列表')
  assert.equal(repositories.getAuditLogDetail(legacyAuditId), undefined, '历史后台来源详情应按不存在处理')

  const previousProcessRole = runtimeConfig.processRole
  const pendingWorkerMessagesBefore = backgroundIpc.getBackgroundWorkerState().pendingMessageCount
  runtimeConfig.processRole = 'server'
  try {
    auditQueue.enqueueAuditLog({
      ...auditLog('audit_server_no_worker_ipc', 'trace-server-no-worker-ipc', JSON.stringify({ error: 'worker unavailable' })),
      auditOutcome: 'gateway_failed',
      finalStatusCode: 502,
      errorPhase: 'gateway',
      errorCode: 'worker_unavailable',
      errorMessage: 'worker 未就绪时主进程只能投递 IPC 队列，不能本地 SQLite 写入',
      attempts: [],
      payloads: []
    })
    auditQueue.flushAllAuditLogQueue()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-server-no-worker-ipc' }).total, 0, 'server 无可用 worker 时审计日志不能回落本地队列写入')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, pendingWorkerMessagesBefore + 1, 'server 无可用 worker 时审计日志应进入 IPC 待投递队列')

  const pendingUsageWorkerMessagesBefore = backgroundIpc.getBackgroundWorkerState().pendingMessageCount
  const localUsageQueueLengthBefore = usageRecordQueue.getUsageRecordQueueRuntime().queueLength
  runtimeConfig.processRole = 'server'
  try {
    usageRecordQueue.enqueueUsageRecord({
      traceId: 'trace-usage-server-no-worker-ipc',
      trafficSource: 'gateway',
      systemAccountId: 'sys_admin',
      groupId: 'group_default',
      endpoint: '/v1/responses',
      providerCode: 'gpt',
      success: false,
      statusCode: 503,
      errorCode: 'worker_unavailable',
      errorMessage: 'worker 未就绪时主进程只能投递 IPC 队列，不能本地 SQLite 写入'
    })
    usageRecordQueue.flushAllUsageRecordQueue()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
  assert.equal(repositories.listUsageRecords(undefined, { pageSize: 10 }).items.some((item) => item.traceId === 'trace-usage-server-no-worker-ipc'), false, 'server 无可用 worker 时使用记录不能回落本地队列写入')
  assert.equal(usageRecordQueue.getUsageRecordQueueRuntime().queueLength, localUsageQueueLengthBefore, 'server 无可用 worker 时使用记录不能进入本地待写队列')
  assert.equal(backgroundIpc.getBackgroundWorkerState().pendingMessageCount, pendingUsageWorkerMessagesBefore + 1, 'server 无可用 worker 时使用记录应进入 IPC 待投递队列')

  const truncatedUsageRecordId = usageRecordShards.generateUsageRecordId(now, 'usage_snapshot_truncated')
  usageRecordQueue.enqueueUsageRecord({
    id: truncatedUsageRecordId,
    traceId: 'trace-usage-snapshot-truncated',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: 'group_default',
    endpoint: '/v1/responses',
    providerCode: 'gpt',
    success: false,
    statusCode: 502,
    errorCode: 'large_snapshot',
    errorMessage: '大响应快照应被截断',
    responseSnapshot: {
      upstreamUrl: 'https://api.openai.com/v1/responses',
      statusCode: 502,
      bodyText: 'x'.repeat(200 * 1024)
    }
  })
  usageRecordQueue.flushAllUsageRecordQueue()
  const truncatedUsageRecord = repositories.getUsageRecordDetail(truncatedUsageRecordId)
  const truncatedBodyText = truncatedUsageRecord?.responseSnapshot?.bodyText
  assert.equal(typeof truncatedBodyText, 'string', '使用记录响应快照应保留可读 bodyText')
  assert((truncatedBodyText as string).length < 40 * 1024, '使用记录响应快照 bodyText 应在入队前截断')
  assert((truncatedBodyText as string).includes('[truncated'), '使用记录响应快照应标记截断')

  repositories.createAuditLogsBatch([
    {
      ...auditLog('audit_deleted_api_key', 'trace-deleted-api-key', JSON.stringify({ error: 'api key deleted before worker flush' })),
      apiKeyId: 'api_key_deleted_before_worker_flush',
      auditOutcome: 'gateway_failed',
      finalStatusCode: 500,
      errorPhase: 'gateway',
      errorCode: 'deleted_api_key_flush',
      errorMessage: 'API Key 已删除但审计事件仍应保留',
      attempts: [],
      payloads: []
    }
  ])
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-deleted-api-key' }).total, 1, 'API Key 被删除后异步 flush 的审计事件仍应保留')

  const apiKeyAccess = { systemAccountId: 'sys_admin', role: 'admin' as const }
  const apiKeyGroup = repositories.createGroup({
    name: '审计删除保留 API Key 分组',
    providerCode: 'gpt'
  }, apiKeyAccess)
  const apiKey = createApiKeyRecordWithRouteStrategy(repositories, {
    name: '审计删除保留 API Key',
    groupBindings: [{ groupId: apiKeyGroup.id, priority: 1, status: 'active' }],
  }, apiKeyAccess)
  repositories.createAuditLogsBatch([
    {
      ...auditLog('audit_api_key_delete_retained', 'trace-api-key-delete-retained', JSON.stringify({ error: 'api key deleted after audit' })),
      apiKeyId: apiKey.id,
      groupId: apiKeyGroup.id,
      auditOutcome: 'gateway_failed',
      finalStatusCode: 500,
      errorPhase: 'gateway',
      errorCode: 'api_key_deleted_after_audit',
      errorMessage: '删除 API Key 后审计事件仍应保留',
      attempts: [],
      payloads: []
    }
  ])
  assert.equal(repositories.deleteApiKey(apiKey.id, { systemAccountId: 'sys_admin', role: 'admin' }), true, '测试 API Key 应可删除')
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-api-key-delete-retained' }).total, 1, '删除 API Key 不应删除已有审计事件')

  repositories.createAuditLogsBatch([
    auditLog('audit_retention_1', 'trace-retention-1', repeatedBody),
    auditLog('audit_retention_2', 'trace-retention-2', repeatedBody)
  ])

  const datasetDatabase = databaseModule.getDatasetDatabase()
  const blobRows = datasetDatabase
    .prepare('SELECT id, sha256, raw_size_bytes, compressed_size_bytes, compression, storage_key, ref_count FROM audit_payload_blobs ORDER BY created_at ASC')
    .all() as Array<{
      id: string
      sha256: string
      raw_size_bytes: number
      compressed_size_bytes: number
      compression: string
      storage_key: string
      ref_count: number
    }>
  const bodyBlob = blobRows.find((row) => row.raw_size_bytes === Buffer.byteLength(repeatedBody, 'utf8'))
  assert(bodyBlob, '重复响应正文应写入 payload blob')
  assert.equal(bodyBlob.ref_count, 2, '相同正文应复用同一个 blob，并累计引用次数')
  assert.equal(bodyBlob.compression, 'gzip', '大 JSON payload 应压缩为 gzip')
  assert(bodyBlob.compressed_size_bytes < bodyBlob.raw_size_bytes, '压缩后大小应小于原文大小')

  const blobPath = resolve(tempRoot, 'audit', 'blobs', bodyBlob.storage_key)
  assert(existsSync(blobPath), 'payload blob 文件应存在')
  assert.equal(gunzipSync(readFileSync(blobPath)).toString('utf8'), repeatedBody, 'gzip blob 解压后应还原原文')

  const list = repositories.listAuditLogs({ outcome: 'upstream_failed', pageSize: 10 })
  assert.equal(list.total, 2, '应写入两条失败审计事件')
  assert(list.items.every((item) => item.auditOutcome === 'upstream_failed'), '失败事件应按 upstream_failed 保留')
  const failureDetails = list.items.map((item) => repositories.getAuditLogDetail(item.id))
  assert(failureDetails.every((item) => item?.errorGroupId), '重复失败事件应关联错误组')
  assert.equal(new Set(failureDetails.map((item) => item?.errorGroupId)).size, 1, '同一窗口内重复错误应聚合到同一个错误组')

  const groups = repositories.listAuditErrorGroups({ pageSize: 10, statusCode: 429 })
  assert.equal(groups.total, 1, '应产生一个错误聚合组')
  assert.equal(groups.items[0]?.count, 2, '错误聚合组应累计 occurrence 次数')

  const events = repositories.listAuditErrorGroupEvents(groups.items[0].id, { pageSize: 10 })
  assert.equal(events.total, 2, '错误聚合组应可反查每次 occurrence')

  const retainedErrorGroupId = groups.items[0].id
  datasetDatabase.prepare('UPDATE audit_error_groups SET updated_at = ? WHERE id = ?').run('2000-01-01T00:00:00.000Z', retainedErrorGroupId)
  await repositories.cleanupAuditLogsByRetentionAsync({
    successHotCutoffCreatedAt: '2000-01-01T00:00:00.000Z',
    successCutoffCreatedAt: '2000-01-01T00:00:00.000Z',
    failureCutoffCreatedAt: '2000-01-01T00:00:00.000Z',
    errorGroupCutoffUpdatedAt: '2001-01-01T00:00:00.000Z',
    limit: 100
  })
  assert.equal(repositories.listAuditErrorGroups({ pageSize: 10, statusCode: 429 }).total, 1, '仍被失败审计日志引用的错误聚合组不应被独立清理')
  assert.equal(repositories.listAuditErrorGroupEvents(retainedErrorGroupId, { pageSize: 10 }).total, 2, '保留中的错误聚合组应继续能反查 occurrence')

  const detail = repositories.getAuditLogDetail('audit_retention_1')
  const payload = detail?.payloads.find((item) => item.partType === 'gateway_error')
  assert(payload, '事件详情应包含 gateway_error payload 引用')
  const payloadDetail = await repositories.getAuditLogPayload('audit_retention_1', payload.id, {
    limit: repeatedBody.length
  })
  assert.equal(payloadDetail?.bodyText, repeatedBody, 'payload 读取接口应透明解压并返回正文')

  const deleted = await repositories.cleanupAuditLogsByRetentionAsync({
    successHotCutoffCreatedAt: '2999-01-01T00:00:00.000Z',
    successCutoffCreatedAt: '2999-01-01T00:00:00.000Z',
    failureCutoffCreatedAt: '2999-01-01T00:00:00.000Z',
    errorGroupCutoffUpdatedAt: '2999-01-01T00:00:00.000Z',
    limit: 100
  })
  assert(deleted >= 1, '清理应删除过期事件、错误组和无引用 blob')
  assert.equal(repositories.listAuditLogs({ pageSize: 10 }).total, 0, '过期审计事件应被清理')
  assert.equal(repositories.listAuditErrorGroups({ pageSize: 10 }).total, 0, '过期错误聚合组应被清理')
  const remainingBlobRow = datasetDatabase.prepare('SELECT COUNT(*) AS total FROM audit_payload_blobs').get() as { total: number }
  assert.equal(remainingBlobRow.total, 0, '无引用 blob 元数据应被清理')
  assert(!existsSync(blobPath), '无引用 blob 文件应被删除')

  console.log('审计日志保全策略回归通过：成功采样、压缩、去重、错误聚合、payload 读取和清理均符合预期')
} finally {
  try {
    cleanupTemporaryAuditBlobs()
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function cleanupTemporaryAuditBlobs(): void {
  try {
    const rows = databaseModule.getDatasetDatabase()
      .prepare('SELECT storage_key FROM audit_payload_blobs')
      .all() as Array<{ storage_key?: string }>
    for (const row of rows) {
      if (!row.storage_key) continue
      rmSync(resolve(tempRoot, 'audit', 'blobs', row.storage_key), { force: true })
    }
  } catch {
  }
}

function assertAuditPayloadCleanupUsesAsyncFiles(): void {
  const payloadBlobSource = readFileSync(new URL('../../storage/audit-log-payload-blobs.ts', import.meta.url), 'utf8')
  const auditRetentionSource = readFileSync(new URL('../../storage/audit-log-retention.repository.ts', import.meta.url), 'utf8')
  const datasetSchemaSource = readFileSync(new URL('../../storage/schema/dataset-schema.ts', import.meta.url), 'utf8')
  const auditRepositorySource = readFileSync(new URL('../../storage/audit-logs.repository.ts', import.meta.url), 'utf8')
  const dataRetentionSource = readFileSync(new URL('../../modules/background/data-retention-cleanup.service.ts', import.meta.url), 'utf8')
  const maintenanceCleanupJobsSource = readFileSync(new URL('../../modules/background/maintenance-cleanup-jobs.ts', import.meta.url), 'utf8')
  const recordMaintenanceSource = readFileSync(new URL('../../modules/record-maintenance/record-maintenance-queue.service.ts', import.meta.url), 'utf8')
  assert(payloadBlobSource.includes('cleanupUnreferencedAuditPayloadBlobsAsync'), '审计 payload blob 应提供异步清理入口')
  assert.match(payloadBlobSource, /listAuditPayloadBlobRowsBefore[\s\S]*NOT EXISTS[\s\S]*audit_payload_refs[\s\S]*headers_blob_id = b\.id OR r\.body_blob_id = b\.id/, '按时间清理审计 payload blob 时必须跳过仍被 audit_payload_refs 引用的 blob')
  assert(payloadBlobSource.includes('auditBlobCleanupDeleteConcurrency'), '审计 payload blob 异步清理应限制单轮文件删除并发')
  assert(datasetSchemaSource.includes('idx_audit_payload_refs_attempt'), 'audit_payload_refs.attempt_id 外键反查必须建索引，避免删除 audit_log_attempts 时全表扫描 payload refs')
  assert.match(datasetSchemaSource, /CREATE INDEX IF NOT EXISTS idx_audit_logs_persisted_created\s+ON audit_logs\(created_at, id\)\s+WHERE traffic_source NOT IN \('account_health_check', 'runtime_recovery_probe', 'cooldown_retest'\)/, 'SQLite 审计日志默认来源过滤必须有持久化来源部分索引')
  assert.match(datasetSchemaSource, /CREATE INDEX IF NOT EXISTS idx_audit_logs_system_persisted_created\s+ON audit_logs\(system_account_id, created_at, id\)\s+WHERE traffic_source NOT IN \('account_health_check', 'runtime_recovery_probe', 'cooldown_retest'\)/, 'SQLite 审计日志用户范围来源过滤必须有持久化来源部分索引')
  assert.match(datasetSchemaSource, /CREATE INDEX IF NOT EXISTS idx_audit_payload_refs_attempt ON audit_payload_refs\(attempt_id\) WHERE attempt_id IS NOT NULL/, 'attempt_id 外键反查索引应使用部分索引，保持空值场景索引很小')
  assert.match(payloadBlobSource, /deleteUnreferencedAuditPayloadBlobRowsPostgresAsync[\s\S]*FOR UPDATE OF b SKIP LOCKED/, 'PG 审计 payload blob 清理必须锁定有界候选并跳过竞争行')
  assert.match(payloadBlobSource, /client\.transaction[\s\S]*DELETE FROM juhe_dataset\.audit_payload_blobs b[\s\S]*NOT EXISTS[\s\S]*RETURNING b\.id, b\.storage_key/, 'PG 审计 payload blob 清理必须在短事务内复查引用并返回已提交删除的 metadata')
  assert.match(payloadBlobSource, /deleteUnreferencedAuditPayloadBlobRowsPostgresAsync[\s\S]*deleteBlobFilesAsync\(rows\.map/, 'PG 审计 payload blob 只能在 metadata 删除事务确认提交后清理文件，COMMIT 结果不确定时必须保留文件')
  assert(payloadBlobSource.includes('auditBlobUnreferencedGraceMs'), 'PG 通用无引用 blob 清理必须保留两阶段写入宽限期')
  assert.match(payloadBlobSource, /resolve\(dirname\(runtimeConfig\.datasetDatabasePath\), 'audit', 'blobs'\)/, '审计 payload blob 必须跟随共享 dataset 数据库目录，不能落入 release 目录')
  assert.match(payloadBlobSource, /deleteUnreferencedAuditPayloadBlobRowsSqlite[\s\S]*DELETE FROM audit_payload_blobs[\s\S]*NOT EXISTS[\s\S]*RETURNING id, storage_key/, 'SQLite 审计 payload 清理必须先原子删除未引用 metadata 并返回真实 storage key')
  assert(auditRetentionSource.includes('postgresAuditRetentionDeleteSubBatchLimit'), 'PG 审计日志保留清理必须限制单事务审计日志 id 批次')
  assert(auditRetentionSource.includes('postgresAuditRetentionSelectBatchLimit = 100'), 'PG 审计日志保留清理单轮候选必须有生产安全上限')
  assert(auditRetentionSource.includes('postgresAuditRetentionDeleteSubBatchLimit = 10'), 'PG 审计日志保留清理删除子批次必须保持小事务')
  assert(auditRetentionSource.includes('postgresAuditRetentionLimit(limit)'), 'PG 审计日志保留清理必须对调用方传入 limit 做硬上限裁剪')
  assert.match(auditRetentionSource, /trimAuditLogDetailsByWhereAsync[\s\S]*DELETE FROM juhe_dataset\.audit_payload_refs[\s\S]*DELETE FROM juhe_dataset\.audit_log_attempts[\s\S]*UPDATE juhe_dataset\.audit_logs[\s\S]*capture_status = 'metadata_only'/, 'PG 成功热保留清理必须只解绑详情并将父记录降级为 metadata_only')
  assert.match(auditRetentionSource, /for \(const chunk of chunkStringIds\(ids, postgresAuditRetentionDeleteSubBatchLimit\)\)[\s\S]*client\.transaction[\s\S]*audit_log_attempts WHERE audit_log_id = ANY\(\?::text\[\]\)', \[chunk\]/, 'PG 审计日志保留清理必须按 chunk 开短事务删除 attempts')
  assert.doesNotMatch(auditRetentionSource, /audit_log_attempts WHERE audit_log_id = ANY\(\?::text\[\]\)', \[ids\]/, 'PG 审计日志保留清理不能用整批 ids 一次删除 attempts')
  assert(recordMaintenanceSource.includes('auditRetainedDataCleanupBatchSizeLimit = 100'), '审计保留维护任务必须限制从 data-retention 继承的大批量参数')
  assert(recordMaintenanceSource.includes('auditRetainedDataCleanupMaxBatchesLimit = 3'), '审计保留维护任务必须限制单次追赶批次数，避免长时间占住 record-maintenance consumer')
  assert(recordMaintenanceSource.includes('Math.min(positiveBatchSize(input.batchSize), auditRetainedDataCleanupBatchSizeLimit)'), '审计保留维护任务必须裁剪 batchSize 后再清理')
  assert(recordMaintenanceSource.includes('Math.min(normalizeMaxBatches(input.maxBatches), auditRetainedDataCleanupMaxBatchesLimit)'), '审计保留维护任务必须裁剪 maxBatches 后再清理')
  assert(auditRepositorySource.includes('cleanupAuditLogsByRetentionAsync'), '审计日志保留清理应提供异步入口')
  assert.match(auditRepositorySource, /databaseTransactionDefinitelyRolledBack\(error\)[\s\S]*cleanupCreatedAuditBlobFilesAsync\(\[\.\.\.createdStorageKeys\]\)/, 'PG 审计写入明确回滚后必须清理本次新建的无引用文件')
  assert(auditRepositorySource.includes('audit_payload_commit_outcome_uncertain_files_retained'), 'PG 审计写入的 COMMIT 结果不确定时必须保守保留已创建文件')
  assert.match(
    auditRepositorySource,
    /await client\.transaction\(async \(tx\) => \{[\s\S]*persistPostgresAuditPayloadBlobMetadata\(tx[\s\S]*writePostgresAuditPayloadBlobFiles\([\s\S]*insertPostgresAuditPayloadRefsBatch\(tx/,
    'PG 审计 payload 必须在同一事务行锁下完成元数据、文件和引用，防止 retention 穿插删除复用 blob'
  )
  assert(dataRetentionSource.includes('cleanupAuditLogsByRetentionAsync'), '数据保留 worker 应调用审计日志异步保留清理入口')
  assert.match(maintenanceCleanupJobsSource, /runAuditHotRetentionCleanup\(\)[\s\S]*runtimeConfig\.databaseDriver === 'postgres'[\s\S]*return[\s\S]*cleanupExpiredAuditHotRetentionData\(\)/, 'PG 高性能模式审计保留清理只能走 record-maintenance 小批路径，避免每分钟热清理竞争同一批 audit 表')
  assert(recordMaintenanceSource.includes('auditRetainedDataCleanupBatchPauseMs'), '审计保留维护任务多批追赶必须有批间暂停')
  assert.match(recordMaintenanceSource, /if \(deleted < batchSize\)[\s\S]*break[\s\S]*await delay\(auditRetainedDataCleanupBatchPauseMs\)/, '审计保留维护任务满批后必须暂停再继续下一批')
}

function finalizeSuccessfulRequest(traceId: string): void {
  finalizeSuccessfulRequestWithBody(traceId, Buffer.from(JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' }), 'utf8'))
}

function assertHttpCompletionTiming(): void {
  const traceId = 'trace-audit-http-completion-timing'
  const activeCaptureCountBefore = auditCapture.getActiveAuditCaptureCount()
  const response = Object.assign(new EventEmitter(), {
    writableFinished: false,
    destroyed: false
  }) as unknown as Response
  const startedAtMs = Date.now() - 25
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(),
    res: response,
    traceId,
    clientIp: '127.0.0.1',
    startedAtMs
  })
  assert.equal(auditCapture.getActiveAuditCaptureCount(), activeCaptureCountBefore + 1, 'HTTP 请求处理中应计入活动审计捕获')
  capture.finalize({
    outcome: 'success',
    success: true,
    statusCode: 200
  })
  assert.equal(repositories.listAuditLogs({ traceId }).total, 0, 'HTTP 未完成前审计不得提前固化错误的客户端时间')
  assert.equal(auditCapture.getActiveAuditCaptureCount(), activeCaptureCountBefore + 1, '等待 HTTP 完成的审计仍应计入活动捕获')
  response.emit('finish')
  assert.equal(auditCapture.getActiveAuditCaptureCount(), activeCaptureCountBefore, 'HTTP finish 后审计活动计数应归还')
  auditQueue.flushAllAuditLogQueue()
  const detail = repositories.getAuditLogDetail(repositories.listAuditLogs({ traceId }).items[0]?.id ?? '')
  assert(detail?.httpCompletedAt, 'HTTP finish 后审计应保存返回客户端时间')
  assert((detail?.httpDurationMs ?? 0) >= 25, 'HTTP 客户端耗时应从请求开始计算')
  assert((detail?.durationMs ?? 0) >= (detail?.httpDurationMs ?? 0), '审计完成耗时不得早于 HTTP 客户端耗时')
}

function assertCaptureCancellationLifecycle(): void {
  const routesSource = readFileSync(join(backendRoot, 'src', 'modules', 'gateway', 'routes.ts'), 'utf8')
  assert.match(
    routesSource,
    /try \{[\s\S]*preflight = await prepareOpenAIGatewayDispatchContext\([\s\S]*\} catch \(error\) \{[\s\S]*auditCapture\.cancel\(\)[\s\S]*throw error[\s\S]*if \(!preflight\) \{[\s\S]*auditCapture\.cancel\(\)/,
    '初始 preflight 抛错或无上下文返回时必须取消未 finalize 的审计捕获'
  )

  const activeCaptureCountBefore = auditCapture.getActiveAuditCaptureCount()
  const response = Object.assign(new EventEmitter(), {
    writableFinished: false,
    destroyed: false
  }) as unknown as Response
  const canceledTraceId = 'trace-audit-preflight-canceled'
  const canceledCapture = auditCapture.createAuditCapture({
    req: auditRequest(),
    res: response,
    traceId: canceledTraceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.now()
  })
  assert.equal(auditCapture.getActiveAuditCaptureCount(), activeCaptureCountBefore + 1, 'preflight 开始后应登记活动捕获')
  canceledCapture.cancel()
  canceledCapture.cancel()
  assert.equal(auditCapture.getActiveAuditCaptureCount(), activeCaptureCountBefore, 'preflight 抛错取消必须幂等归还活动捕获')
  response.emit('finish')
  canceledCapture.finalize({ outcome: 'gateway_failed', success: false, statusCode: 500 })
  auditQueue.flushAllAuditLogQueue()
  assert.equal(repositories.listAuditLogs({ traceId: canceledTraceId }).total, 0, '已取消捕获不得在迟到 finish/finalize 后重新写入')

  const finalizedResponse = Object.assign(new EventEmitter(), {
    writableFinished: false,
    destroyed: false
  }) as unknown as Response
  const finalizedCapture = auditCapture.createAuditCapture({
    req: auditRequest(),
    res: finalizedResponse,
    traceId: 'trace-audit-finalize-wins-over-cancel',
    clientIp: '127.0.0.1',
    startedAtMs: Date.now()
  })
  finalizedCapture.finalize({ outcome: 'success', success: true, statusCode: 200 })
  finalizedCapture.cancel()
  assert.equal(auditCapture.getActiveAuditCaptureCount(), activeCaptureCountBefore + 1, '已请求 finalize 的捕获不得被迟到 cancel 提前丢弃')
  finalizedResponse.emit('finish')
  assert.equal(auditCapture.getActiveAuditCaptureCount(), activeCaptureCountBefore, 'finalize 等到 HTTP finish 后必须只归还一次活动捕获')
}

function finalizeSuccessfulRequestWithBody(traceId: string, body: Buffer<ArrayBufferLike>): void {
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(body),
    traceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.parse(now)
  })
  capture.finalize({
    outcome: 'success',
    success: true,
    statusCode: 200,
    responseHeaders: { 'content-type': 'application/json' },
    responseBody: JSON.stringify({ ok: true })
  })
}

function finalizeFailedRequestWithBody(traceId: string, body: Buffer<ArrayBufferLike>): void {
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(body),
    traceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.parse(now)
  })
  capture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: 500,
    responseHeaders: { 'content-type': 'application/json' },
    responseBody: JSON.stringify({ error: 'large request failed' }),
    errorPhase: 'gateway',
    errorCode: 'large_request_failed',
    errorMessage: 'large request failed'
  })
}

function finalizeOverflowFailedRequest(traceId: string): void {
  const previousProcessRole = runtimeConfig.processRole
  let capture: ReturnType<typeof auditCapture.createAuditCapture>
  try {
    runtimeConfig.processRole = 'server'
    capture = auditCapture.createAuditCapture({
      req: auditRequest(Buffer.alloc(65 * 1024 * 1024, 'x')),
      traceId,
      clientIp: '127.0.0.1',
      startedAtMs: Date.parse(now)
    })
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
  capture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: 500,
    errorPhase: 'gateway',
    errorCode: 'active_capture_overflow',
    errorMessage: 'active capture overflow'
  })
}

function finalizeSensitiveHeaderAudit(traceId: string): void {
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(
      Buffer.from(JSON.stringify({
        model: 'gpt-5.4-mini',
        input: 'hello',
        session_id: 'raw-body-session-id'
      }), 'utf8'),
      {
        authorization: 'Bearer client-secret-token',
        cookie: 'session=session-secret-cookie',
        'x-goog-api-key': 'audit-google-key-secret',
        'session-id': 'raw-client-session-id',
        'x-codex-turn-state': 'raw-client-turn-state',
        'content-type': 'application/json'
      },
      '/v1/responses?token=audit-query-token&safe=ok'
    ),
    traceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.parse(now)
  })
  const attemptId = capture.startAttempt({
    account: {
      id: 'account_sensitive_header',
      systemAccountId: 'sys_admin',
      accountOwnerSystemAccountId: 'sys_admin',
      groupOwnerSystemAccountId: 'sys_admin',
      accountAccessType: 'owner',
      groupAccessType: 'owner',
      name: 'Sensitive Header Account',
      type: 'api_key',
      status: 'active',
      concurrencyLimit: 1,
      priority: 0,
      superPriorityEnabled: false,
      fallbackEnabled: false,
      baseUrl: 'https://api.openai.com',
      apiKey: 'sk-account-secret'
    } as Parameters<typeof capture.startAttempt>[0]['account'],
    attemptIndex: 0,
    upstreamUrl: 'https://api.openai.com/v1/responses?token=audit-upstream-query-token&safe=ok',
    method: 'POST',
    headers: new Headers({
      authorization: 'Bearer upstream-secret-token',
      'x-api-key': 'upstream-x-api-key-secret',
      'x-goog-api-key': 'upstream-google-key-secret',
      'openai-api-key': 'sk-account-secret',
      'content-type': 'application/json'
    }),
    body: JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello', session_id: 'raw-upstream-session-id' })
  })
  capture.completeAttempt(attemptId, {
    success: false,
    statusCode: 502,
    responseHeaders: new Headers({
      'set-cookie': 'upstream-set-cookie-secret',
      'content-type': 'application/json'
    }),
    responseBody: JSON.stringify({ error: 'upstream failed', session_id: 'raw-response-session-id' }),
    errorPhase: 'upstream_response',
    errorMessage: '上游失败'
  })
  capture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: 502,
    responseHeaders: { 'set-cookie': 'gateway-set-cookie-secret', 'content-type': 'application/json' },
    responseBody: JSON.stringify({ error: 'gateway failed' }),
    errorPhase: 'upstream_response',
    errorMessage: '上游失败'
  })
}

function finalizeProxyCredentialAudit(traceId: string): void {
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(),
    traceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.parse(now)
  })
  const account = {
    ...auditOpenAIAccount('account_proxy_credential'),
    proxyUrl: 'http://proxy-user:proxy-pass@127.0.0.1:18080'
  } as ReturnType<typeof auditOpenAIAccount>
  const attemptId = capture.startAttempt({
    account,
    attemptIndex: 0,
    upstreamUrl: 'https://api.openai.com/v1/responses',
    method: 'POST',
    headers: new Headers({ 'content-type': 'application/json' }),
    body: JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' })
  })
  capture.completeAttempt(attemptId, {
    success: false,
    statusCode: 502,
    responseHeaders: new Headers({ 'content-type': 'application/json' }),
    responseBody: JSON.stringify({ error: 'proxy failed' }),
    errorPhase: 'upstream_request',
    errorMessage: '代理失败'
  })
  capture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: 502,
    responseHeaders: { 'content-type': 'application/json' },
    responseBody: JSON.stringify({ error: 'proxy failed' }),
    errorPhase: 'upstream_request',
    errorMessage: '代理失败'
  })
}

function auditRequest(
  rawBody: Buffer<ArrayBufferLike> = Buffer.from(JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' }), 'utf8'),
  headerOverrides: Record<string, string> = {},
  originalUrl = '/v1/responses'
): Request & { rawBody?: Buffer } {
  const headers: Record<string, string> = { 'content-type': 'application/json', ...headerOverrides }
  return {
    method: 'POST',
    originalUrl,
    path: '/v1/responses',
    body: { model: 'gpt-5.4-mini', input: 'hello' },
    rawBody,
    headers,
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as Request & { rawBody?: Buffer }
}

function auditOpenAIAccount(accountId: string): Parameters<ReturnType<typeof auditCapture.createAuditCapture>['startAttempt']>[0]['account'] {
  return {
    id: accountId,
    systemAccountId: 'sys_admin',
    accountOwnerSystemAccountId: 'sys_admin',
    groupOwnerSystemAccountId: 'sys_admin',
    accountAccessType: 'owner',
    groupAccessType: 'owner',
    name: accountId,
    type: 'api_key',
    status: 'active',
    concurrencyLimit: 1,
    priority: 0,
    superPriorityEnabled: false,
    fallbackEnabled: false,
    baseUrl: 'https://api.openai.com',
    apiKey: 'sk-test'
  } as Parameters<ReturnType<typeof auditCapture.createAuditCapture>['startAttempt']>[0]['account']
}

function traceIdForBucket(predicate: (bucket: number) => boolean, prefix = 'trace-sampling'): string {
  for (let index = 0; index < 100000; index += 1) {
    const traceId = `${prefix}-${index}`
    if (predicate(sampleBucketForTraceId(traceId))) {
      return traceId
    }
  }
  throw new Error('无法构造采样 traceId')
}

function sampleBucketForTraceId(traceId: string): number {
  const digest = createHash('sha256').update(traceId).digest()
  return digest.readUInt32BE(0) % 10000
}

function sha256Buffer(buffer: Buffer<ArrayBufferLike>): string {
  return createHash('sha256').update(buffer).digest('hex')
}

function assertAuditPayloadByteColumns(
  auditLogId: string,
  options: { compressedLessThanRaw?: boolean; rawGreaterThan?: number } = {}
): void {
  const database = databaseModule.getDatasetDatabase()
  const logRow = database
    .prepare('SELECT raw_payload_bytes, compressed_payload_bytes, compression_saved_bytes FROM audit_logs WHERE id = ?')
    .get(auditLogId) as {
      raw_payload_bytes: number
      compressed_payload_bytes: number
      compression_saved_bytes: number
    } | undefined
  assert(logRow, '审计主记录应存在以校验 payload 字节口径')
  const refRow = database
    .prepare('SELECT COALESCE(SUM(raw_size_bytes), 0) AS raw_size_bytes, COALESCE(SUM(compressed_size_bytes), 0) AS compressed_size_bytes FROM audit_payload_refs WHERE audit_log_id = ?')
    .get(auditLogId) as { raw_size_bytes: number; compressed_size_bytes: number }
  const rawPayloadBytes = Number(logRow.raw_payload_bytes)
  const compressedPayloadBytes = Number(logRow.compressed_payload_bytes)
  assert.equal(rawPayloadBytes, Number(refRow.raw_size_bytes), 'raw_payload_bytes 应等于 payload refs 的原始逻辑字节汇总')
  assert.equal(compressedPayloadBytes, Number(refRow.compressed_size_bytes), 'compressed_payload_bytes 应等于 payload refs 的落盘压缩字节汇总')
  assert.equal(Number(logRow.compression_saved_bytes), Math.max(0, rawPayloadBytes - compressedPayloadBytes), 'compression_saved_bytes 应由两个独立口径计算')
  if (options.rawGreaterThan !== undefined) {
    assert(rawPayloadBytes > options.rawGreaterThan, 'raw_payload_bytes 应包含原始 body 字节和 headers 字节')
  }
  if (options.compressedLessThanRaw) {
    assert(compressedPayloadBytes < rawPayloadBytes, '摘要/压缩场景 compressed_payload_bytes 应小于 raw_payload_bytes')
  }
}

function assertAllAbsent(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(!text.includes(marker), `${message}：${marker}`)
  }
}

function assertAllPresent(text: string, markers: string[], message: string): void {
  for (const marker of markers) {
    assert(text.includes(marker), `${message}：${marker}`)
  }
}

function auditLog(id: string, traceId: string, body: string): AuditLogInput {
  return {
    id,
    traceId,
    systemAccountId: 'sys_admin',
    providerCode: 'gpt',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.4-mini',
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 429,
    errorPhase: 'upstream',
    errorCode: 'rate_limit_exceeded',
    errorMessage: 'Rate limit exceeded for request 1234567890',
    sampleBucket: 9999,
    sampleReason: 'full_capture',
    captureStatus: 'complete',
    startedAt: now,
    endedAt: now,
    durationMs: 120,
    attempts: [
      {
        id: `${id}_attempt`,
        tempId: `${id}_attempt_tmp`,
        attemptIndex: 1,
        accountId: undefined,
        providerCode: 'gpt',
        upstreamMethod: 'POST',
        upstreamUrl: 'https://api.openai.com/v1/responses',
        upstreamStatusCode: 429,
        success: false,
        errorPhase: 'upstream',
        errorCode: 'rate_limit_exceeded',
        errorMessage: 'Rate limit exceeded for request 1234567890',
        startedAt: now,
        endedAt: now,
        durationMs: 100
      }
    ],
    payloads: [
      {
        id: `${id}_client_payload`,
        partType: 'client_request',
        sequenceIndex: 0,
        contentType: 'application/json',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' }),
        createdAt: now
      },
      {
        id: `${id}_error_payload`,
        attemptTempId: `${id}_attempt_tmp`,
        partType: 'gateway_error',
        sequenceIndex: 1,
        contentType: 'application/json',
        headers: { 'content-type': 'application/json' },
        body,
        createdAt: now
      }
    ],
    createdAt: now
  }
}
