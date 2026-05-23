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
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories, backgroundIpc, usageRecordQueue] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/background/background-ipc.js'),
  import('../../modules/gateway/usage-record-queue.service.js')
])
const auditCapture = await import('../../modules/gateway/audit-capture.service.js')
const auditQueue = await import('../../modules/audit-logs/audit-log-queue.service.js')

const now = '2026-05-11T00:00:00.000Z'
const repeatedBody = JSON.stringify({
  error: 'rate limited',
  detail: 'x'.repeat(8192)
})

try {
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
  const sensitiveHeaderPayloads = await Promise.all(sensitiveHeaderDetail.payloads.map((payload) => repositories.getAuditLogPayload(sensitiveHeaderAuditId, payload.id)))
  const sensitiveHeaderSerialized = JSON.stringify(sensitiveHeaderPayloads)
  assert.equal(sensitiveHeaderSerialized.includes('Bearer client-secret-token'), false, '客户端 Authorization 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('Bearer upstream-secret-token'), false, '上游 Authorization 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('session-secret-cookie'), false, 'Cookie 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('upstream-set-cookie-secret'), false, 'Set-Cookie 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('gateway-set-cookie-secret'), false, '网关 Set-Cookie 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('upstream-x-api-key-secret'), false, 'X-API-Key 不应进入审计 payload')
  assert.equal(sensitiveHeaderSerialized.includes('sk-account-secret'), false, '账号 API Key 不应进入审计 payload')
  assert(sensitiveHeaderSerialized.includes('[redacted]'), '审计 payload 中的敏感 header 应保留脱敏占位')

  usageRecordQueue.enqueueUsageRecord({
    id: 'usage_sensitive_headers',
    traceId: 'trace-usage-sensitive-headers',
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
          'content-type': 'application/json'
        }
      }
    }
  })
  usageRecordQueue.flushAllUsageRecordQueue()
  const sensitiveUsageRecord = repositories.getUsageRecordDetail('usage_sensitive_headers')
  const sensitiveUsageSerialized = JSON.stringify({
    requestSnapshot: sensitiveUsageRecord?.requestSnapshot,
    responseSnapshot: sensitiveUsageRecord?.responseSnapshot
  })
  assert.equal(sensitiveUsageSerialized.includes('usage-client-token'), false, '使用记录请求 snapshot 不应保留 Authorization')
  assert.equal(sensitiveUsageSerialized.includes('usage-api-key-secret'), false, '使用记录请求 snapshot 不应保留 API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-openai-api-key-secret'), false, '使用记录请求 snapshot 不应保留 OpenAI-API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-response-cookie'), false, '使用记录响应 snapshot 不应保留 Set-Cookie')
  assert.equal(sensitiveUsageSerialized.includes('usage-response-key'), false, '使用记录响应 snapshot 不应保留 X-API-Key')
  assert.equal(sensitiveUsageSerialized.includes('usage-last-upstream-token'), false, '使用记录 lastUpstreamAttempt 不应保留 Authorization')
  assert.equal(sensitiveUsageSerialized.includes('usage-proxy-token'), false, '使用记录 lastUpstreamAttempt 不应保留 Proxy-Authorization')
  assert.equal(sensitiveUsageSerialized.includes('usage-query-token'), false, '使用记录请求 snapshot 不应保留敏感查询参数')
  assert.equal(sensitiveUsageSerialized.includes('usage-upstream-query-key'), false, '使用记录响应 snapshot 不应保留上游 URL 敏感查询参数')
  assert(sensitiveUsageSerialized.includes('[redacted]'), '使用记录 snapshot 中的敏感 header 应保留脱敏占位')

  const overflowTraceId = 'trace-overflow-retained'
  finalizeOverflowFailedRequest(overflowTraceId)
  auditQueue.flushAllAuditLogQueue()
  const overflowEvents = repositories.listAuditLogs({ traceId: overflowTraceId })
  assert.equal(overflowEvents.total, 1, 'active capture 超限时应保留失败事件')
  assert.equal(overflowEvents.items[0]?.captureStatus, 'overflow', 'active capture 超限事件应标记为 overflow')
  assert.equal(overflowEvents.items[0]?.payloadCount, 0, 'active capture 超限事件不应伪装成完整原文')
  assert.equal(repositories.getAuditLogDetail(overflowEvents.items[0]?.id ?? '')?.payloads.length, 0, 'active capture 超限时不应短暂保留大 payload')

  auditQueue.recordDroppedAuditCapture({
    traceId: 'trace-body-rejected-retained',
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: 1024 * 1024,
    reason: 'gateway_body_rejected',
    method: 'POST',
    path: '/v1/responses',
    statusCode: 413,
    errorPhase: 'gateway',
    errorCode: 'entity.too.large',
    errorMessage: '请求体过大'
  })
  auditQueue.flushAllAuditLogQueue()
  const rejectedEvents = repositories.listAuditLogs({ traceId: 'trace-body-rejected-retained' })
  assert.equal(rejectedEvents.total, 1, '请求体被网关拒绝时也应保留失败事件')
  assert.equal(rejectedEvents.items[0]?.captureStatus, 'overflow', '请求体被网关拒绝时应标记为 overflow')

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

  usageRecordQueue.enqueueUsageRecord({
    id: 'usage_snapshot_truncated',
    traceId: 'trace-usage-snapshot-truncated',
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
  const truncatedUsageRecord = repositories.getUsageRecordDetail('usage_snapshot_truncated')
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

  const apiKey = repositories.createApiKeyRecord({ name: '审计删除保留 API Key' }, { systemAccountId: 'sys_admin', role: 'admin' })
  repositories.createAuditLogsBatch([
    {
      ...auditLog('audit_api_key_delete_retained', 'trace-api-key-delete-retained', JSON.stringify({ error: 'api key deleted after audit' })),
      apiKeyId: apiKey.id,
      groupId: apiKey.groupId,
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

  const recordDatabase = databaseModule.getDatasetDatabase()
  const blobRows = recordDatabase
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
  recordDatabase.prepare('UPDATE audit_error_groups SET updated_at = ? WHERE id = ?').run('2000-01-01T00:00:00.000Z', retainedErrorGroupId)
  repositories.cleanupAuditLogsByRetention({
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

  const deleted = repositories.cleanupAuditLogsByRetention({
    successCutoffCreatedAt: '2999-01-01T00:00:00.000Z',
    failureCutoffCreatedAt: '2999-01-01T00:00:00.000Z',
    errorGroupCutoffUpdatedAt: '2999-01-01T00:00:00.000Z',
    limit: 100
  })
  assert(deleted >= 1, '清理应删除过期事件、错误组和无引用 blob')
  assert.equal(repositories.listAuditLogs({ pageSize: 10 }).total, 0, '过期审计事件应被清理')
  assert.equal(repositories.listAuditErrorGroups({ pageSize: 10 }).total, 0, '过期错误聚合组应被清理')
  const remainingBlobRow = recordDatabase.prepare('SELECT COUNT(*) AS total FROM audit_payload_blobs').get() as { total: number }
  assert.equal(remainingBlobRow.total, 0, '无引用 blob 元数据应被清理')
  assert(!existsSync(blobPath), '无引用 blob 文件应被删除')

  console.log('审计日志保全策略回归通过：成功采样、压缩、去重、错误聚合、payload 读取和清理均符合预期')
} finally {
  try {
    cleanupTemporaryAuditBlobs()
    databaseModule.getDatabase().close()
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

function finalizeSuccessfulRequest(traceId: string): void {
  const capture = auditCapture.createAuditCapture({
    req: auditRequest(),
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

function auditRequest(
  rawBody = Buffer.from(JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' }), 'utf8'),
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

function traceIdForBucket(predicate: (bucket: number) => boolean): string {
  for (let index = 0; index < 100000; index += 1) {
    const traceId = `trace-sampling-${index}`
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
