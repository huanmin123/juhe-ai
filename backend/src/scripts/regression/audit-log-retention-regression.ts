import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { gunzipSync } from 'node:zlib'
import type { Request } from 'express'

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
runtimeConfig.audit.fullBodyCaptureEnabled = false
runtimeConfig.audit.fullBodyCapture = { enabled: false, scope: 'global', includeSuccess: false }
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, backgroundIpc, usageRecordQueue, usageRecordShards] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/background/background-ipc.js'),
  import('../../modules/gateway/usage-record-queue.service.js'),
  import('../../storage/usage-record-shards.js')
])
const auditCapture = await import('../../modules/gateway/audit-capture.service.js')
const auditQueue = await import('../../modules/audit-logs/audit-log-queue.service.js')
const auditSettings = await import('../../modules/audit-logs/audit-log-settings.js')

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
  assert.equal(runtimeConfig.audit.fullBodyCaptureEnabled, false, '回归默认应关闭全量捕获开关，超限 body 走摘要保全')

  const unsampledTraceId = traceIdForBucket((bucket) => bucket >= 1000)
  const sampledTraceId = traceIdForBucket((bucket) => bucket < 1000)
  finalizeSuccessfulRequest(unsampledTraceId)
  auditQueue.flushAllAuditLogQueue()
  assert.equal(repositories.listAuditLogs({ traceId: unsampledTraceId }).total, 0, '未命中 10% 稳定采样的成功请求不应写入 audit_logs')

  finalizeSuccessfulRequest(sampledTraceId)
  auditQueue.flushAllAuditLogQueue()
  assert.equal(repositories.listAuditLogs({ traceId: sampledTraceId }).total, 1, '命中 10% 稳定采样的成功请求应写入 audit_logs')

  const sensitiveHeaderTraceId = 'trace-sensitive-header-redaction'
  finalizeSensitiveHeaderAudit(sensitiveHeaderTraceId)
  auditQueue.flushAllAuditLogQueue()
  const sensitiveHeaderAuditId = repositories.listAuditLogs({ traceId: sensitiveHeaderTraceId }).items[0]?.id ?? ''
  const sensitiveHeaderDetail = repositories.getAuditLogDetail(sensitiveHeaderAuditId)
  assert(sensitiveHeaderDetail, '敏感 header 脱敏审计事件应写入详情')
  assert.equal(sensitiveHeaderDetail.queryString?.includes('audit-query-token'), false, '审计主记录 queryString 不应保留敏感查询参数')
  assert(sensitiveHeaderDetail.queryString?.includes('token=%5Bredacted%5D'), '审计主记录 queryString 应保留查询参数脱敏占位')
  const sensitiveHeaderSerializedDetail = JSON.stringify(sensitiveHeaderDetail)
  assert.equal(sensitiveHeaderSerializedDetail.includes('audit-upstream-query-token'), false, '审计 attempt upstreamUrl 不应保留敏感查询参数')
  assert(sensitiveHeaderSerializedDetail.includes('token=%5Bredacted%5D'), '审计 attempt upstreamUrl 应保留查询参数脱敏占位')
  const sensitiveHeaderPayloads = await Promise.all(sensitiveHeaderDetail.payloads.map((payload) => repositories.getAuditLogPayload(sensitiveHeaderAuditId, payload.id)))
  const sensitiveHeaderSerialized = JSON.stringify(sensitiveHeaderPayloads)
  assert.equal(sensitiveHeaderSerialized.includes('Bearer client-secret-token'), false, '客户端 Authorization 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('Bearer upstream-secret-token'), false, '上游 Authorization 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('session-secret-cookie'), false, 'Cookie 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('upstream-set-cookie-secret'), false, 'Set-Cookie 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('gateway-set-cookie-secret'), false, '网关 Set-Cookie 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('upstream-x-api-key-secret'), false, 'X-API-Key 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('audit-google-key-secret'), false, '客户端 X-Goog-API-Key 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('upstream-google-key-secret'), false, '上游 X-Goog-API-Key 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('sk-account-secret'), false, '账号 API Key 不应进入审计 payload')
  assert(sensitiveHeaderSerialized.includes('[redacted]'), '审计 payload 中的敏感 header 应保留脱敏占位')

  const proxyCredentialTraceId = 'trace-proxy-credential-redaction'
  finalizeProxyCredentialAudit(proxyCredentialTraceId)
  auditQueue.flushAllAuditLogQueue()
  const proxyCredentialAuditId = repositories.listAuditLogs({ traceId: proxyCredentialTraceId }).items[0]?.id ?? ''
  const proxyCredentialDetail = repositories.getAuditLogDetail(proxyCredentialAuditId)
  assert(proxyCredentialDetail, '代理凭据脱敏审计事件应写入详情')
  const proxyCredentialRow = databaseModule.getDatasetDatabase()
    .prepare('SELECT proxy_url FROM audit_log_attempts WHERE audit_log_id = ?')
    .get(proxyCredentialAuditId) as { proxy_url?: string } | undefined
  const proxyCredentialSerialized = JSON.stringify({
    row: proxyCredentialRow,
    detail: proxyCredentialDetail
  })
  assert.equal(proxyCredentialSerialized.includes('proxy-user'), false, 'audit_log_attempts.proxy_url 不应保留代理用户名')
  assert.equal(proxyCredentialSerialized.includes('proxy-pass'), false, 'audit_log_attempts.proxy_url 不应保留代理密码')
  assert(proxyCredentialSerialized.includes('[redacted]@127.0.0.1:18080'), '审计详情应保留代理已配置信号和目标地址')

  const sensitiveUsageRecordId = usageRecordShards.generateUsageRecordId(now, 'usage_sensitive_headers')
  usageRecordQueue.enqueueUsageRecord({
    id: sensitiveUsageRecordId,
    traceId: 'trace-usage-sensitive-headers',
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    groupId: 'group_default',
    endpoint: '/v1/responses',
    providerCode: 'openai',
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
  assert(sensitiveUsageRecord, '使用记录敏感信息脱敏样本应能按分片 ID 读取')
  const sensitiveUsageSerialized = JSON.stringify({
    requestSnapshot: sensitiveUsageRecord?.requestSnapshot,
    responseSnapshot: sensitiveUsageRecord?.responseSnapshot
  })
  assert.equal(sensitiveUsageSerialized.includes('usage-client-token'), false, '使用记录请求 snapshot 不应保留 Authorization')
  assert.equal(sensitiveUsageSerialized.includes('usage-api-key-secret'), false, '使用记录请求 snapshot 不应保留 API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-openai-api-key-secret'), false, '使用记录请求 snapshot 不应保留 OpenAI-API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-google-api-key-secret'), false, '使用记录请求 snapshot 不应保留 X-Goog-API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-response-cookie'), false, '使用记录响应 snapshot 不应保留 Set-Cookie')
  assert.equal(sensitiveUsageSerialized.includes('usage-response-key'), false, '使用记录响应 snapshot 不应保留 X-API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-last-upstream-token'), false, '使用记录 lastUpstreamAttempt 不应保留 Authorization')
  assert.equal(sensitiveUsageSerialized.includes('usage-proxy-token'), false, '使用记录 lastUpstreamAttempt 不应保留 Proxy-Authorization')
  assert.equal(sensitiveUsageSerialized.includes('usage-last-upstream-google-key'), false, '使用记录 lastUpstreamAttempt 不应保留 X-Goog-API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-query-token'), false, '使用记录请求 snapshot 不应保留敏感查询参数')
  assert.equal(sensitiveUsageSerialized.includes('usage-upstream-query-key'), false, '使用记录响应 snapshot 不应保留上游 URL 敏感查询参数')
  assert(sensitiveUsageSerialized.includes('[redacted]'), '使用记录 snapshot 中的敏感 header 应保留脱敏占位')

  const overflowTraceId = 'trace-overflow-retained'
  finalizeOverflowFailedRequest(overflowTraceId)
  auditQueue.flushAllAuditLogQueue()
  const overflowEvents = repositories.listAuditLogs({ traceId: overflowTraceId })
  assert.equal(overflowEvents.total, 1, 'active capture 超限时应保留失败事件')
  assert.equal(overflowEvents.items[0]?.captureStatus, 'complete', '超大失败 body 摘要化后事件主状态应保持完整')
  assert(overflowEvents.items[0]?.payloadCount > 0, '超大失败 body 应保留摘要 payload')
  const overflowDetail = repositories.getAuditLogDetail(overflowEvents.items[0]?.id ?? '')
  const overflowClientPayload = overflowDetail?.payloads.find((payload) => payload.partType === 'client_request')
  assert.equal(overflowClientPayload?.captureStatus, 'summary_only', '超大失败 body 不应保留完整原文，应保留摘要')

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
    errorMessage: '请求体过大'
  })
  auditQueue.flushAllAuditLogQueue()
  const rejectedEvents = repositories.listAuditLogs({ traceId: 'trace-body-rejected-retained' })
  assert.equal(rejectedEvents.total, 1, '请求体被网关拒绝时也应保留失败事件')
  assert.equal(rejectedEvents.items[0]?.captureStatus, 'overflow', '请求体被网关拒绝时应标记为 overflow')
  const rejectedDetail = repositories.getAuditLogDetail(rejectedEvents.items[0]?.id ?? '')
  assert.equal(rejectedDetail?.queryString?.includes('body-rejected-query-token'), false, '早期拒绝审计 queryString 不应保留 token 参数')
  assert.equal(rejectedDetail?.queryString?.includes('body-rejected-api-key'), false, '早期拒绝审计 queryString 不应保留 api_key 参数')
  assert(rejectedDetail?.queryString?.includes('token=%5Bredacted%5D'), '早期拒绝审计 queryString 应保留 token 脱敏占位')
  assert(rejectedDetail?.queryString?.includes('api_key=%5Bredacted%5D'), '早期拒绝审计 queryString 应保留 api_key 脱敏占位')

  const largeFailedTraceId = 'trace-large-failed-payload-summary'
  finalizeFailedRequestWithBody(largeFailedTraceId, largeFailedRequestBody)
  auditQueue.flushAllAuditLogQueue()
  const largeFailedEvents = repositories.listAuditLogs({ traceId: largeFailedTraceId })
  assert.equal(largeFailedEvents.total, 1, '失败大请求应保留审计事件')
  const largeFailedEvent = largeFailedEvents.items[0]
  assert(largeFailedEvent.rawPayloadBytes > largeFailedRequestBody.byteLength, '失败大请求报表原始字节应按原始 body 计入')
  assert(largeFailedEvent.compressedPayloadBytes < largeFailedEvent.rawPayloadBytes, '失败大请求报表落盘字节应小于原始逻辑字节')
  assertAuditPayloadByteColumns(largeFailedEvent.id, {
    compressedLessThanRaw: true,
    rawGreaterThan: largeFailedRequestBody.byteLength
  })
  const largeFailedDetail = repositories.getAuditLogDetail(largeFailedEvent.id)
  const largeFailedClientPayload = largeFailedDetail?.payloads.find((payload) => payload.partType === 'client_request')
  assert(largeFailedClientPayload, '失败大请求应保留客户端请求 payload 摘要')
  assert.equal(largeFailedClientPayload.captureStatus, 'summary_only', '超过 2MB 的失败请求 body 应转为摘要保全')
  assert.notEqual(largeFailedClientPayload.bodySha256, sha256Buffer(largeFailedRequestBody), '超过 1MB 的失败大请求摘要不应在网关主进程同步计算原始 bodySha256')
  assert(largeFailedClientPayload.sizeBytes > largeFailedRequestBody.byteLength, 'payload 原始大小应按原始 body 加 headers 计入')
  const largeFailedPayloadDetail = await repositories.getAuditLogPayload(largeFailedEvent.id, largeFailedClientPayload.id, { limit: 1024 * 1024 })
  const largeFailedSummary = JSON.parse(largeFailedPayloadDetail?.bodyText ?? '{}') as Record<string, unknown>
  assert.equal(largeFailedSummary.type, 'audit_payload_summary', '失败大请求读取正文应返回摘要 JSON')
  assert.equal(largeFailedSummary.originalSha256, undefined, '超过 1MB 的失败大请求摘要 JSON 不应记录同步计算的完整 body hash')
  assert.equal(largeFailedSummary.originalSizeBytes, largeFailedRequestBody.byteLength, '摘要 JSON 应记录原始 body 大小')
  assert(typeof largeFailedSummary.headBase64 === 'string' && largeFailedSummary.headBase64.length > 0, '摘要 JSON 应保留头部窗口')
  assert(typeof largeFailedSummary.tailBase64 === 'string' && largeFailedSummary.tailBase64.length > 0, '摘要 JSON 应保留尾部窗口')
  assert((largeFailedPayloadDetail?.bodyTotalBytes ?? 0) < largeFailedRequestBody.byteLength, '摘要 blob 不应保存完整失败大 body')
  assert(JSON.stringify(largeFailedSummary.json ?? {}).includes('model'), '摘要 JSON 应包含 JSON 结构信息')

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

  const previousFullBodyCapture = { ...runtimeConfig.audit.fullBodyCapture }
  const previousFullBodyCaptureEnabled = runtimeConfig.audit.fullBodyCaptureEnabled
  auditSettings.setAuditLogFullBodyCaptureConfig({
    enabled: true,
    scope: 'global',
    includeSuccess: false
  })
  try {
    const fullBodyCaptureTraceId = 'trace-full-body-capture-large'
    finalizeFailedRequestWithBody(fullBodyCaptureTraceId, largeFailedRequestBody)
    auditQueue.flushAllAuditLogQueue()
    const fullBodyCaptureEvents = repositories.listAuditLogs({ traceId: fullBodyCaptureTraceId })
    assert.equal(fullBodyCaptureEvents.total, 1, '临时全量捕获开启后仍应保留审计事件')
    const fullBodyCaptureDetail = repositories.getAuditLogDetail(fullBodyCaptureEvents.items[0]?.id ?? '')
    const fullBodyClientPayload = fullBodyCaptureDetail?.payloads.find((payload) => payload.partType === 'client_request')
    assert(fullBodyClientPayload, '临时全量捕获开启后应保留客户端请求 payload')
    assert.equal(fullBodyClientPayload.captureStatus, 'complete', '临时全量捕获开启后大 body 不应转为摘要')
    assert.equal(fullBodyClientPayload.bodySha256, sha256Buffer(largeFailedRequestBody), '临时全量捕获开启后 bodySha256 仍应指向原始 body')
    assert.equal(fullBodyClientPayload.sizeBytes, largeFailedRequestBody.byteLength + headerBytes({ 'content-type': 'application/json' }), '临时全量捕获开启后 payload 原始大小应等于完整 body 加 headers')
    assert.equal(fullBodyClientPayload.compressedSizeBytes, fullBodyClientPayload.sizeBytes, '临时全量捕获开启后超过压缩窗口的大 body 应按未压缩落盘字节计入')
    assertAuditPayloadByteColumns(fullBodyCaptureEvents.items[0]?.id ?? '', {
      compressedEqualsRaw: true,
      rawGreaterThan: largeFailedRequestBody.byteLength
    })
    const fullBodyPayloadDetail = await repositories.getAuditLogPayload(fullBodyCaptureEvents.items[0]?.id ?? '', fullBodyClientPayload.id, { limit: 1024 * 1024 })
    assert.equal(fullBodyPayloadDetail?.bodyTotalBytes, largeFailedRequestBody.byteLength, '临时全量捕获开启后应保存完整 body 原文大小')
    assert.equal(fullBodyPayloadDetail?.bodyTruncated, true, '完整大 body 读取接口仍应按窗口返回')
  } finally {
    runtimeConfig.audit.fullBodyCapture = previousFullBodyCapture
    runtimeConfig.audit.fullBodyCaptureEnabled = previousFullBodyCaptureEnabled
  }

  const previousTargetedCapture = { ...runtimeConfig.audit.fullBodyCapture }
  const previousTargetedCaptureEnabled = runtimeConfig.audit.fullBodyCaptureEnabled
  auditSettings.setAuditLogFullBodyCaptureConfig({
    enabled: true,
    scope: 'account',
    accountId: 'account_targeted_full_capture',
    includeSuccess: true,
    durationMinutes: 15
  })
  try {
    const targetedSuccessTraceId = traceIdForBucket((bucket) => bucket >= 1000, 'trace-targeted-success-full-capture')
    finalizeSuccessfulAccountRequestWithBody(targetedSuccessTraceId, largeSuccessRequestBody, 'account_targeted_full_capture')
    auditQueue.flushAllAuditLogQueue()
    const targetedSuccessEvents = repositories.listAuditLogs({ traceId: targetedSuccessTraceId })
    assert.equal(targetedSuccessEvents.total, 1, '定向临时全量捕获开启后，命中账户的未采样 200 成功请求也应进入审计')
    assert.equal(targetedSuccessEvents.items[0]?.sampleReason, 'targeted_full_capture_success', '定向 200 成功请求应记录定向捕获采样原因')
    const targetedSuccessDetail = repositories.getAuditLogDetail(targetedSuccessEvents.items[0]?.id ?? '')
    const targetedClientPayload = targetedSuccessDetail?.payloads.find((payload) => payload.partType === 'client_request')
    const targetedUpstreamRequestPayload = targetedSuccessDetail?.payloads.find((payload) => payload.partType === 'upstream_request')
    assert(targetedClientPayload, '定向 200 成功请求应补充客户端请求 payload')
    assert(targetedUpstreamRequestPayload, '定向 200 成功请求应捕获命中账户的上游请求 payload')
    assert.equal(targetedClientPayload.captureStatus, 'complete', '定向全量捕获下成功大 body 不应转为摘要')
    const targetedPayloadDetail = await repositories.getAuditLogPayload(targetedSuccessEvents.items[0]?.id ?? '', targetedClientPayload.id, { limit: 1024 * 1024 })
    assert.equal(targetedPayloadDetail?.bodyTotalBytes, largeSuccessRequestBody.byteLength, '定向全量捕获应保存成功大 body 完整原文大小')

    const nonTargetSuccessTraceId = traceIdForBucket((bucket) => bucket >= 1000, 'trace-targeted-success-non-target')
    finalizeSuccessfulAccountRequestWithBody(nonTargetSuccessTraceId, largeSuccessRequestBody, 'account_other_full_capture')
    auditQueue.flushAllAuditLogQueue()
    assert.equal(repositories.listAuditLogs({ traceId: nonTargetSuccessTraceId }).total, 0, '定向临时全量捕获不应放大非目标账户的未采样 200 成功请求')
  } finally {
    runtimeConfig.audit.fullBodyCapture = previousTargetedCapture
    runtimeConfig.audit.fullBodyCaptureEnabled = previousTargetedCaptureEnabled
  }

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
      providerCode: 'openai',
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
    providerCode: 'openai',
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
    providerCode: 'openai'
  }, apiKeyAccess)
  const apiKey = repositories.createApiKeyRecord({
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

  const blobPath = resolve(backendRoot, 'data', 'audit', 'blobs', bodyBlob.storage_key)
  assert(existsSync(blobPath), 'payload blob 文件应存在')
  assert.equal(gunzipSync(readFileSync(blobPath)).toString('utf8'), repeatedBody, 'gzip blob 解压后应还原原文')

  const list = repositories.listAuditLogs({ outcome: 'upstream_failed', pageSize: 10 })
  assert.equal(list.total, 2, '应写入两条失败审计事件')
  assert(list.items.every((item) => item.auditOutcome === 'upstream_failed'), '失败事件应按 upstream_failed 保留')
  assert(list.items.every((item) => item.errorGroupId), '重复失败事件应关联错误组')
  assert.equal(new Set(list.items.map((item) => item.errorGroupId)).size, 1, '同一窗口内重复错误应聚合到同一个错误组')

  const groups = repositories.listAuditErrorGroups({ pageSize: 10, statusCode: 429 })
  assert.equal(groups.total, 1, '应产生一个错误聚合组')
  assert.equal(groups.items[0]?.count, 2, '错误聚合组应累计 occurrence 次数')

  const events = repositories.listAuditErrorGroupEvents(groups.items[0].id, { pageSize: 10 })
  assert.equal(events.total, 2, '错误聚合组应可反查每次 occurrence')

  const retainedErrorGroupId = groups.items[0].id
  datasetDatabase.prepare('UPDATE audit_error_groups SET updated_at = ? WHERE id = ?').run('2000-01-01T00:00:00.000Z', retainedErrorGroupId)
  await repositories.cleanupAuditLogsByRetentionAsync({
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
      rmSync(resolve(backendRoot, 'data', 'audit', 'blobs', row.storage_key), { force: true })
    }
  } catch {
  }
}

function assertAuditPayloadCleanupUsesAsyncFiles(): void {
  const payloadBlobSource = readFileSync(new URL('../../storage/audit-log-payload-blobs.ts', import.meta.url), 'utf8')
  const auditRepositorySource = readFileSync(new URL('../../storage/audit-logs.repository.ts', import.meta.url), 'utf8')
  const dataRetentionSource = readFileSync(new URL('../../modules/background/data-retention-cleanup.service.ts', import.meta.url), 'utf8')
  assert(payloadBlobSource.includes('cleanupUnreferencedAuditPayloadBlobsAsync'), '审计 payload blob 应提供异步清理入口')
  assert(payloadBlobSource.includes('auditBlobCleanupDeleteConcurrency'), '审计 payload blob 异步清理应限制单轮文件删除并发')
  assert(payloadBlobSource.includes('await Promise.all(chunk.map((storageKey) => deleteBlobFileAsync(storageKey)))'), '审计 payload blob 异步清理应使用异步文件删除，不能批量 unlinkSync')
  assert(auditRepositorySource.includes('cleanupAuditLogsByRetentionAsync'), '审计日志保留清理应提供异步入口')
  assert(dataRetentionSource.includes('cleanupAuditLogsByRetentionAsync'), '数据保留 worker 应调用审计日志异步保留清理入口')
}

function finalizeSuccessfulRequest(traceId: string): void {
  finalizeSuccessfulRequestWithBody(traceId, Buffer.from(JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' }), 'utf8'))
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

function finalizeSuccessfulAccountRequestWithBody(traceId: string, body: Buffer<ArrayBufferLike>, accountId: string): void {
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(body),
    traceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.parse(now)
  })
  const attemptId = capture.startAttempt({
    account: auditOpenAIAccount(accountId),
    attemptIndex: 0,
    upstreamUrl: 'https://api.openai.com/v1/responses?token=audit-upstream-query-token&safe=ok',
    method: 'POST',
    headers: new Headers({ 'content-type': 'application/json' }),
    body
  })
  capture.completeAttempt(attemptId, {
    success: true,
    statusCode: 200,
    responseHeaders: new Headers({ 'content-type': 'application/json' }),
    responseBody: JSON.stringify({ ok: true })
  })
  capture.finalize({
    outcome: 'success',
    success: true,
    statusCode: 200,
    accountId,
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
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(Buffer.alloc(65 * 1024 * 1024, 'x')),
    traceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.parse(now)
  })
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
      undefined,
      {
        authorization: 'Bearer client-secret-token',
        cookie: 'session=session-secret-cookie',
        'x-goog-api-key': 'audit-google-key-secret',
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
    upstreamUrl: 'https://api.openai.com/v1/responses',
    method: 'POST',
    headers: new Headers({
      authorization: 'Bearer upstream-secret-token',
      'x-api-key': 'upstream-x-api-key-secret',
      'x-goog-api-key': 'upstream-google-key-secret',
      'content-type': 'application/json'
    }),
    body: JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' })
  })
  capture.completeAttempt(attemptId, {
    success: false,
    statusCode: 502,
    responseHeaders: new Headers({
      'set-cookie': 'upstream-set-cookie-secret',
      'content-type': 'application/json'
    }),
    responseBody: JSON.stringify({ error: 'upstream failed' }),
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
  options: { compressedLessThanRaw?: boolean; compressedEqualsRaw?: boolean; rawGreaterThan?: number } = {}
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
  if (options.compressedEqualsRaw) {
    assert.equal(compressedPayloadBytes, rawPayloadBytes, '全量未压缩场景 compressed_payload_bytes 应等于 raw_payload_bytes')
  }
}

function headerBytes(headers: Record<string, string | string[]>): number {
  return Buffer.byteLength(stableJsonStringify(headers), 'utf8')
}

function stableJsonStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableJsonStringify).join(',')}]`
  }
  if (value && typeof value === 'object') {
    const object = value as Record<string, unknown>
    return `{${Object.keys(object)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${stableJsonStringify(object[key])}`)
      .join(',')}}`
  }
  return JSON.stringify(value)
}

function auditLog(id: string, traceId: string, body: string): AuditLogInput {
  return {
    id,
    traceId,
    systemAccountId: 'sys_admin',
    providerCode: 'openai',
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
        providerCode: 'openai',
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
