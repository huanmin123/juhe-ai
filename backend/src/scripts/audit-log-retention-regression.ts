import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { gunzipSync } from 'node:zlib'
import type { Request } from 'express'

import { backendRoot, runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'
import type { AuditLogInput } from '../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-log-retention-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'audit-log-retention.sqlite3')
runtimeConfig.recordDatabasePath = join(tempRoot, 'audit-log-retention-records.sqlite3')
runtimeConfig.secret = 'audit-log-retention-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../storage/database.js'),
  import('../storage/repositories.js')
])
const auditCapture = await import('../modules/gateway/audit-capture.service.js')
const auditQueue = await import('../modules/audit-logs/audit-log-queue.service.js')

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

  const overflowTraceId = 'trace-overflow-retained'
  finalizeOverflowFailedRequest(overflowTraceId)
  auditQueue.flushAllAuditLogQueue()
  const overflowEvents = repositories.listAuditLogs({ traceId: overflowTraceId })
  assert.equal(overflowEvents.total, 1, 'active capture 超限时应保留失败事件')
  assert.equal(overflowEvents.items[0]?.captureStatus, 'overflow', 'active capture 超限事件应标记为 overflow')
  assert.equal(overflowEvents.items[0]?.payloadCount, 0, 'active capture 超限事件不应伪装成完整原文')

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
  runtimeConfig.processRole = 'server'
  try {
    auditQueue.enqueueAuditLog({
      ...auditLog('audit_server_no_worker_fallback', 'trace-server-no-worker-fallback', JSON.stringify({ error: 'worker unavailable' })),
      auditOutcome: 'gateway_failed',
      finalStatusCode: 502,
      errorPhase: 'gateway',
      errorCode: 'worker_unavailable',
      errorMessage: 'worker 不可用时应回落到本地审计队列',
      attempts: [],
      payloads: []
    })
    auditQueue.flushAllAuditLogQueue()
  } finally {
    runtimeConfig.processRole = previousProcessRole
  }
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-server-no-worker-fallback' }).total, 1, 'server 无可用 worker 时审计日志应回落本地队列写入')

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

  const recordDatabase = databaseModule.getRecordDatabase()
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

  const detail = repositories.getAuditLogDetail('audit_retention_1')
  const payload = detail?.payloads.find((item) => item.partType === 'gateway_error')
  assert(payload, '事件详情应包含 gateway_error payload 引用')
  const payloadDetail = repositories.getAuditLogPayload('audit_retention_1', payload.id)
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
    databaseModule.getRecordDatabase().close()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function cleanupTemporaryAuditBlobs(): void {
  try {
    const rows = databaseModule.getRecordDatabase()
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

function auditRequest(rawBody = Buffer.from(JSON.stringify({ model: 'gpt-5.4-mini', input: 'hello' }), 'utf8')): Request & { rawBody?: Buffer } {
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
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
