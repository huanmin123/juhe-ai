import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

process.env.JUHE_AI_AUDIT_LOG_ENABLED = 'false'

const { runtimeConfig } = await import('../../config/runtime.js')
const tempRoot = resolve(tmpdir(), `juhe-ai-audit-disabled-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.secret = 'audit-disabled-regression-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
assert.equal(runtimeConfig.auditLog.enabled, false)

const [database, repositories, auditCapture, auditQueue, auditSettings, auditTransport] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/audit/capture.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js'),
  import('../../modules/audit-logs/audit-log-settings.js'),
  import('../../modules/audit-logs/audit-log-transport.service.js')
])

try {
  assert.equal(auditSettings.readAuditLogSettings().enabled, false)

  const successCapture = auditCapture.createAuditCapture({
    req: auditRequest(),
    traceId: 'trace-audit-disabled-success',
    clientIp: '127.0.0.1',
    startedAtMs: Date.now(),
    captureMode: 'metadata_only'
  })
  successCapture.finalize({ outcome: 'success', success: true, statusCode: 200 })

  const failureCapture = auditCapture.createAuditCapture({
    req: auditRequest(),
    traceId: 'trace-audit-disabled-failure',
    clientIp: '127.0.0.1',
    startedAtMs: Date.now(),
    captureMode: 'metadata_only'
  })
  failureCapture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: 503,
    errorCode: 'simulated_failure',
    errorMessage: 'simulated failure while audit disabled'
  })

  auditQueue.recordDroppedAuditCapture({
    traceId: 'trace-audit-disabled-dropped',
    auditOutcome: 'gateway_failed',
    success: false,
    bytes: 12,
    reason: 'gateway_auth_rejected',
    method: 'POST',
    path: '/v1/responses',
    statusCode: 401,
    errorCode: 'unauthorized'
  })

  const directInput = {
    id: 'audit_disabled_direct',
    traceId: 'trace-audit-disabled-direct',
    auditOutcome: 'gateway_failed',
    success: false,
    method: 'POST',
    path: '/v1/responses',
    sampleBucket: 0,
    sampleReason: 'disabled_direct',
    captureStatus: 'complete',
    startedAt: new Date().toISOString(),
    endedAt: new Date().toISOString(),
    attempts: [],
    payloads: []
  } as never
  auditQueue.enqueueAuditLog(directInput)
  auditQueue.enqueueAuditLogsLocal([directInput])
  runtimeConfig.queueDriver = 'redis_stream'
  auditQueue.startAuditLogRedisStreamConsumer()
  assert.equal(await auditQueue.getAuditLogRedisStreamRuntime(), undefined, '审计关闭后运行态查询不得创建 Redis Stream 连接')
  auditQueue.flushAllAuditLogQueue()
  await assert.rejects(
    () => auditTransport.prepareAuditLogForIpcInWorker(directInput),
    /原始审计已关闭/,
    '关闭审计时 transport worker 不得接收直接任务'
  )
  await assert.rejects(
    () => auditTransport.encodeAuditLogForRedisStreamInWorker(directInput),
    /原始审计已关闭/,
    '关闭审计时 transport worker 不得接收 Redis 编码任务'
  )

  assert.equal(auditCapture.getActiveAuditCaptureCount(), 0)
  assert.equal(auditQueue.getAuditLogQueueRuntime().queueLength, 0)
  assert.equal(auditQueue.getAuditLogServerDispatchPendingCount(), 0)
  assert.equal(auditTransport.getAuditLogTransportRuntime().queuedJobs, 0)
  assert.equal(auditTransport.getAuditLogTransportRuntime().workerCount, 0)
  assert.equal(auditTransport.getAuditLogTransportRuntime().activeJobs, 0)
  assert.equal(auditTransport.getAuditLogTransportRuntime().completedCount, 0)
  assert.equal(auditTransport.getAuditLogTransportRuntime().failedCount, 0)
  assert.equal(auditTransport.getAuditLogTransportRuntime().rejectedCount, 0)
  assert.equal(repositories.listAuditLogs({}).total, 0)
  console.log('audit log disabled regression passed')
} finally {
  database.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

function auditRequest(): Request & { rawBody?: Buffer } {
  const headers: Record<string, string> = { 'content-type': 'application/json' }
  return {
    method: 'POST',
    originalUrl: '/v1/responses',
    path: '/v1/responses',
    body: { model: 'gpt-5.6-sol', input: 'hello' },
    rawBody: Buffer.from(JSON.stringify({ model: 'gpt-5.6-sol', input: 'hello' })),
    headers,
    header(name: string): string | undefined {
      return headers[name.toLowerCase()]
    }
  } as Request & { rawBody?: Buffer }
}
