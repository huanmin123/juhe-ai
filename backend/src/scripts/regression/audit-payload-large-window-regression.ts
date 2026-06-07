import { strict as assert } from 'node:assert'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-payload-large-window-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'audit-payload-large-window-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [databaseModule, repositories] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js')
])

const now = '2026-01-01T00:00:00.000Z'
const largeBody = JSON.stringify({ payload: 'x'.repeat(1_200_000) })
const largeBodyBytes = Buffer.byteLength(largeBody, 'utf8')

try {
  repositories.createAuditLogsBatch([auditLog('audit_large_plain_window', 'trace-audit-large-window', largeBody)])

  const bodyBlob = databaseModule.getDatasetDatabase()
    .prepare('SELECT id, raw_size_bytes, compressed_size_bytes, compression, storage_key FROM audit_payload_blobs WHERE raw_size_bytes = ? LIMIT 1')
    .get(largeBodyBytes) as { id: string; raw_size_bytes: number; compressed_size_bytes: number; compression: string; storage_key: string } | undefined
  assert(bodyBlob, '超大审计正文应写入 payload blob')
  assert.equal(bodyBlob.compression, 'none', '超过单次读取窗口的 payload 应保持 plain，避免后半段读取从头解压 gzip')
  assert.equal(bodyBlob.compressed_size_bytes, bodyBlob.raw_size_bytes, 'plain payload 的压缩大小应等于原始大小')

  const detail = repositories.getAuditLogDetail('audit_large_plain_window')
  const payload = detail?.payloads.find((item) => item.partType === 'gateway_error')
  assert(payload, '审计详情应包含超大 gateway_error payload 引用')

  const offset = 1_100_000
  const limit = 128
  const payloadWindow = await repositories.getAuditLogPayload('audit_large_plain_window', payload.id, { offset, limit })
  assert.equal(payloadWindow?.bodyOffset, offset, 'payload 窗口应保留请求 offset')
  assert.equal(payloadWindow?.bodyBytesReturned, limit, 'payload 窗口应只返回请求的有限字节数')
  assert.equal(payloadWindow?.bodyText, 'x'.repeat(limit), 'plain payload 后半段窗口应按 offset 直接返回正确内容')
  assert.equal(payloadWindow?.bodyTruncated, true, '后半段窗口之后仍有内容时应标记截断')
  assert.equal(payloadWindow?.bodyNextOffset, offset + limit, '后半段窗口应返回下一次读取 offset')

  console.log('审计 payload 超大窗口回归通过：1MB+ payload 保持 plain 并支持 offset 窗口读取')
} finally {
  cleanupTemporaryAuditBlobs()
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
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
    finalStatusCode: 502,
    errorPhase: 'upstream',
    errorCode: 'large_payload_window',
    errorMessage: 'large audit payload window regression',
    sampleBucket: 9999,
    sampleReason: 'full_capture',
    captureStatus: 'complete',
    startedAt: now,
    endedAt: now,
    durationMs: 100,
    attempts: [
      {
        id: `${id}_attempt`,
        tempId: `${id}_attempt_tmp`,
        attemptIndex: 1,
        providerCode: 'gpt',
        upstreamMethod: 'POST',
        upstreamUrl: 'https://api.openai.com/v1/responses',
        upstreamStatusCode: 502,
        success: false,
        errorPhase: 'upstream',
        errorCode: 'large_payload_window',
        errorMessage: 'large audit payload window regression',
        startedAt: now,
        endedAt: now,
        durationMs: 80
      }
    ],
    payloads: [
      {
        id: `${id}_payload`,
        attemptTempId: `${id}_attempt_tmp`,
        partType: 'gateway_error',
        sequenceIndex: 0,
        contentType: 'application/json',
        headers: { 'content-type': 'application/json' },
        body,
        createdAt: now
      }
    ],
    createdAt: now
  }
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
