import { strict as assert } from 'node:assert'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import {
  createAuditLogsBatchAsync,
  getAuditLogDetailAsync
} from '../../storage/audit-logs.repository.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '审计日志 PG smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `audit_log_pg_smoke_${Date.now()}_${Math.random().toString(16).slice(2)}`
const auditLogId = `audit_${marker}`
const startedAtMs = Date.now() - 321
const httpCompletedAtMs = startedAtMs + 123
const endedAtMs = httpCompletedAtMs + 198

try {
  await createAuditLogsBatchAsync([{
    id: auditLogId,
    traceId: `trace_${marker}`,
    trafficSource: 'gateway',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.6-sol',
    stream: false,
    auditOutcome: 'success',
    success: true,
    finalStatusCode: 200,
    sampleBucket: 1,
    sampleReason: 'postgres_smoke',
    captureStatus: 'complete',
    startedAt: new Date(startedAtMs).toISOString(),
    httpCompletedAt: new Date(httpCompletedAtMs).toISOString(),
    httpDurationMs: httpCompletedAtMs - startedAtMs,
    endedAt: new Date(endedAtMs).toISOString(),
    durationMs: endedAtMs - startedAtMs,
    attempts: [],
    payloads: []
  }])

  const detail = await getAuditLogDetailAsync(auditLogId)
  assert(detail, 'PG 审计日志应可通过 repository 读回')
  assert.equal(detail.httpCompletedAt, new Date(httpCompletedAtMs).toISOString(), 'PG 审计日志应读回 http_completed_at')
  assert.equal(detail.httpDurationMs, httpCompletedAtMs - startedAtMs, 'PG 审计日志应读回 http_duration_ms')
  assert.equal(detail.durationMs, endedAtMs - startedAtMs, 'PG 审计固化耗时应与 HTTP 客户端耗时保持独立')

  console.log(JSON.stringify({
    message: '审计日志 PG smoke 通过',
    auditLogId,
    httpCompletedAt: detail.httpCompletedAt,
    httpDurationMs: detail.httpDurationMs,
    durationMs: detail.durationMs
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function cleanupSmokeRows(): Promise<void> {
  const pool = await getPostgresPool()
  await pool.query('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = $1', [auditLogId])
  await pool.query('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = $1', [auditLogId])
  await pool.query('DELETE FROM juhe_dataset.audit_logs WHERE id = $1', [auditLogId])
}
