import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-log-async-flush-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'audit-log-async-flush-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

assertAuditShutdownHookUsesAsyncFlush()

const [databaseModule, repositories, auditQueue] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

const now = '2026-01-01T00:00:00.000Z'
const repeatedBody = JSON.stringify({ payload: 'async-compress-' + 'x'.repeat(16 * 1024) })
const repeatedBodyBytes = Buffer.byteLength(repeatedBody, 'utf8')

try {
  auditQueue.clearAuditLogQueueForTest()
  auditQueue.enqueueAuditLogsLocal([
    auditLog('audit_async_flush_1', 'trace-audit-async-flush-1', repeatedBody),
    auditLog('audit_async_flush_2', 'trace-audit-async-flush-2', repeatedBody)
  ])
  await auditQueue.flushAuditLogQueueAsync({ drain: true, retryOnFailure: false })

  const runtime = auditQueue.getAuditLogQueueRuntime()
  assert.equal(runtime.queueLength, 0, '异步 flush 后审计队列应清空')
  assert.equal(runtime.flushLastError, undefined, '异步 flush 不应留下错误')
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-audit-async-flush-1' }).total, 1, '第一条异步审计日志应写入')
  assert.equal(repositories.listAuditLogs({ traceId: 'trace-audit-async-flush-2' }).total, 1, '第二条异步审计日志应写入')

  const bodyBlob = databaseModule.getDatasetDatabase()
    .prepare(`
      SELECT id, raw_size_bytes, compressed_size_bytes, compression, ref_count
      FROM audit_payload_blobs
      WHERE raw_size_bytes = ? AND content_type = 'application/json'
      LIMIT 1
    `)
    .get(repeatedBodyBytes) as { id: string; raw_size_bytes: number; compressed_size_bytes: number; compression: string; ref_count: number } | undefined
  assert(bodyBlob, '异步 flush 应写入审计 body blob')
  assert.equal(bodyBlob.compression, 'gzip', '可压缩审计 body 应通过异步 gzip 压缩')
  assert(bodyBlob.compressed_size_bytes < bodyBlob.raw_size_bytes, '异步 gzip 后压缩大小应小于原始大小')
  assert.equal(bodyBlob.ref_count, 2, '同一批次重复 body 应复用 blob 并累加引用数')

  const detail = repositories.getAuditLogDetail('audit_async_flush_1')
  const payload = detail?.payloads.find((item) => item.partType === 'gateway_error')
  assert(payload, '异步写入的审计详情应包含 payload 引用')
  const payloadWindow = await repositories.getAuditLogPayload('audit_async_flush_1', payload.id, { offset: 0, limit: repeatedBodyBytes })
  assert.equal(payloadWindow?.bodyText, repeatedBody, '异步压缩写入后应能按窗口读回原始 body')

  console.log('审计日志异步 flush 回归通过：worker 路径使用异步 gzip/写盘，重复 payload 仍去重并可窗口读取')
} finally {
  auditQueue.clearAuditLogQueueForTest()
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
    errorCode: 'async_flush',
    errorMessage: 'audit async flush regression',
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
        errorCode: 'async_flush',
        errorMessage: 'audit async flush regression',
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

function assertAuditShutdownHookUsesAsyncFlush(): void {
  const workerSource = readFileSync(new URL('../../worker.ts', import.meta.url), 'utf8')
  const auditQueueSource = readFileSync(new URL('../../modules/audit-logs/audit-log-queue.service.ts', import.meta.url), 'utf8')

  assert(workerSource.includes('installAuditLogQueueShutdownHooks()'), 'worker 应通过审计队列异步关闭钩子 flush 审计日志')
  assert(!workerSource.includes('flushAllAuditLogQueue,'), 'worker 入口不应再导入同步 flushAllAuditLogQueue')
  assert(!/process\.once\(\s*['"]exit['"]\s*,\s*flushAllAuditLogQueue\s*\)/.test(workerSource), 'worker 入口不应在 exit 钩子里同步写入审计日志')
  assert(auditQueueSource.includes('flushAllAuditLogQueueAsync'), '审计队列应保留异步 drain flush')
  assert(auditQueueSource.includes('void flushAuditLogQueueForShutdown()'), 'beforeExit 应触发异步审计 flush')
  assert(!/process\.once\(\s*['"]exit['"]/.test(auditQueueSource), '审计队列不应注册 exit 同步钩子，避免退出路径同步写 SQLite / payload 文件')
}
