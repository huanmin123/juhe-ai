import assert from 'node:assert/strict'
import { mkdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import type { Request } from 'express'

process.env.JUHE_AI_AUDIT_LOG_SUCCESS_HOT_RETENTION_HOURS = '0'
process.env.JUHE_AI_AUDIT_LOG_SUCCESS_SAMPLE_RATE = '0'
process.env.JUHE_AI_AUDIT_LOG_SUCCESS_RETENTION_DAYS = '0'

const { runtimeConfig } = await import('../../config/runtime.js')
const tempRoot = resolve(tmpdir(), `juhe-ai-audit-success-disabled-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.cacheDriver = 'memory'
runtimeConfig.secret = 'audit-success-disabled-regression-secret'
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
assert.equal(runtimeConfig.auditLog.successHotRetentionHours, 0)
assert.equal(runtimeConfig.auditLog.successSampleRate, 0)
assert.equal(runtimeConfig.auditLog.successRetentionDays, 0)

const [database, repositories, auditCapture, auditQueue] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../modules/gateway/audit/capture.service.js'),
  import('../../modules/audit-logs/audit-log-queue.service.js')
])

try {
  const successTraceId = 'trace-audit-success-all-zero'
  const successCapture = auditCapture.createAuditCapture({
    req: auditRequest(),
    traceId: successTraceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.now(),
    captureMode: 'metadata_only'
  })
  successCapture.finalize({ outcome: 'success', success: true, statusCode: 200 })
  auditQueue.flushAllAuditLogQueue()
  assert.equal(
    repositories.listAuditLogs({ traceId: successTraceId }).total,
    0,
    '成功审计三项全为 0 时，metadata-only 捕获实际 finalize 后也不得落库'
  )

  const failureTraceId = 'trace-audit-problem-all-zero'
  const failureCapture = auditCapture.createAuditCapture({
    req: auditRequest(),
    traceId: failureTraceId,
    clientIp: '127.0.0.1',
    startedAtMs: Date.now(),
    captureMode: 'metadata_only'
  })
  failureCapture.finalize({
    outcome: 'gateway_failed',
    success: false,
    statusCode: 503,
    errorCode: 'upstream_unavailable',
    errorMessage: 'simulated upstream failure'
  })
  auditQueue.flushAllAuditLogQueue()
  assert.equal(
    repositories.listAuditLogs({ traceId: failureTraceId }).total,
    1,
    '关闭成功审计不能阻断问题审计实际 finalize 落库'
  )

  console.log('audit log success-disabled finalize regression passed')
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
