import { existsSync, mkdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, relative, resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import {
  createAuditLogsBatchAsync,
  createOperationLogsBatchAsync,
  createPublicApiLogsBatchAsync,
  createUsageRecordsBatchAsync
} from '../../storage/repositories.js'
import { closeStorageDatabases, nowIso } from '../../storage/database.js'
import type { DatabaseClient } from '../../storage/database-client.js'
import { createPostgresDatabaseClient } from '../../storage/database-client.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'
import { aggregateUsageStatsBatchAsync } from '../../storage/usage-stats.repository.js'

interface ReadinessCheck {
  name: string
  ok: boolean
  latencyMs: number
  category?: string
  message?: string
}

interface ReadinessReport {
  mode: {
    runtimeMode: string
    databaseDriver: string
    cacheDriver: string
    runtimeStateDriver: string
    queueDriver: string
  }
  runId: string
  startedAt: string
  finishedAt: string
  durationMs: number
  checks: ReadinessCheck[]
  pass: boolean
  missingAdapterChecks: string[]
}

const reportPath = process.env.JUHE_PERFORMANCE_GATEWAY_READINESS_REPORT
  || resolve(backendRoot, '..', 'reports', `performance-gateway-persistence-readiness-${timestampForFile(new Date())}.json`)
const runId = `perf_gateway_readiness_${Date.now()}_${Math.random().toString(16).slice(2)}`
const tracePrefix = `trace-${runId}`

let exitCode = 0

try {
  assertPerformanceRuntime()
  const report = await runReadiness()
  writeReport(report)
  printReport(report)
  if (!report.pass) {
    exitCode = 1
  }
} catch (error) {
  exitCode = 1
  console.error(error instanceof Error ? error.stack ?? error.message : error)
} finally {
  closeStorageDatabases()
  await closePostgresPool().catch(() => undefined)
}

process.exit(exitCode)

async function runReadiness(): Promise<ReadinessReport> {
  const startedAt = new Date()
  const startedAtMs = performance.now()
  const client = createPostgresDatabaseClient(await getPostgresPool())
  await cleanupReadinessRows(client).catch(() => undefined)

  const checks: ReadinessCheck[] = []
  try {
    checks.push(await runCheck('usage_records_write', async () => {
      await createUsageRecordsBatchAsync([{
        id: `${runId}_usage`,
        traceId: `${tracePrefix}-usage`,
        trafficSource: 'gateway',
        systemAccountId: 'sys_admin',
        endpoint: '/v1/chat/completions',
        providerCode: 'gpt',
        usageSemantic: 'chat',
        model: 'gpt-5-mini',
        upstreamModel: 'gpt-5-mini',
        stream: false,
        statusCode: 200,
        success: true,
        firstTokenMs: 20,
        durationMs: 35,
        inputTokens: 8,
        outputTokens: 12,
        costUsd: 0,
        createdAt: nowIso()
      }])
    }))

    checks.push(await runCheck('audit_logs_write', async () => {
      const startedAtIso = nowIso()
      await createAuditLogsBatchAsync([{
        id: `${runId}_audit`,
        traceId: `${tracePrefix}-audit`,
        trafficSource: 'gateway',
        systemAccountId: 'sys_admin',
        method: 'POST',
        path: '/v1/chat/completions',
        model: 'gpt-5-mini',
        stream: false,
        auditOutcome: 'success',
        success: true,
        finalStatusCode: 200,
        sampleBucket: 1,
        sampleReason: 'performance_gateway_readiness',
        startedAt: startedAtIso,
        endedAt: startedAtIso,
        durationMs: 35,
        firstTokenMs: 20,
        attempts: [{
          id: `${runId}_audatt`,
          tempId: `${runId}_attempt_0`,
          attemptIndex: 0,
          upstreamMethod: 'POST',
          upstreamUrl: 'http://127.0.0.1:1/v1/chat/completions',
          upstreamStatusCode: 200,
          success: true,
          startedAt: startedAtIso,
          endedAt: startedAtIso,
          durationMs: 35
        }],
        payloads: [
          {
            id: `${runId}_audpay_req`,
            attemptTempId: `${runId}_attempt_0`,
            partType: 'client_request',
            sequenceIndex: 0,
            contentType: 'application/json',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ runId, direction: 'request' }),
            createdAt: startedAtIso
          },
          {
            id: `${runId}_audpay_res`,
            attemptTempId: `${runId}_attempt_0`,
            partType: 'upstream_response',
            sequenceIndex: 1,
            contentType: 'application/json',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ runId, direction: 'response' }),
            createdAt: startedAtIso
          }
        ],
        createdAt: startedAtIso
      }])
    }))

    checks.push(await runCheck('operation_logs_write', async () => {
      await createOperationLogsBatchAsync([{
        id: `${runId}_oplog`,
        traceId: `${tracePrefix}-operation`,
        actorSystemAccountId: 'sys_admin',
        actorUsername: 'admin',
        actorDisplayName: 'admin',
        actorRole: 'admin',
        module: 'performance_gateway_readiness',
        action: 'probe',
        operationKey: `${runId}.probe`,
        resourceType: 'system',
        resourceId: runId,
        resourceName: 'performance gateway readiness',
        summary: '高性能模式网关事实表 readiness 探测',
        createdAt: nowIso()
      }])
    }))

    checks.push(await runCheck('public_api_logs_write', async () => {
      const startedAtIso = nowIso()
      await createPublicApiLogsBatchAsync([{
        id: `${runId}_publog`,
        traceId: `${tracePrefix}-public-api`,
        method: 'GET',
        path: '/__aipublic__/health',
        statusCode: 200,
        success: true,
        startedAt: startedAtIso,
        endedAt: startedAtIso,
        createdAt: startedAtIso
      }])
    }))

    checks.push(await runCheck('usage_stats_aggregate', async () => {
      await aggregateUsageStatsBatchAsync(10)
    }))
  } finally {
    await cleanupReadinessRows(client).catch(() => undefined)
  }

  const finishedAt = new Date()
  const missingAdapterChecks = checks
    .filter((check) => check.category === 'postgres_adapter_missing')
    .map((check) => check.name)
  return {
    mode: {
      runtimeMode: runtimeConfig.runtimeMode,
      databaseDriver: runtimeConfig.databaseDriver,
      cacheDriver: runtimeConfig.cacheDriver,
      runtimeStateDriver: runtimeConfig.runtimeStateDriver,
      queueDriver: runtimeConfig.queueDriver
    },
    runId,
    startedAt: startedAt.toISOString(),
    finishedAt: finishedAt.toISOString(),
    durationMs: performance.now() - startedAtMs,
    checks,
    pass: checks.every((check) => check.ok),
    missingAdapterChecks
  }
}

async function runCheck(name: string, operation: () => void | Promise<void>): Promise<ReadinessCheck> {
  const startedAt = performance.now()
  try {
    await operation()
    return {
      name,
      ok: true,
      latencyMs: performance.now() - startedAt
    }
  } catch (error) {
    return {
      name,
      ok: false,
      latencyMs: performance.now() - startedAt,
      category: classifyReadinessError(error),
      message: error instanceof Error ? error.message : String(error)
    }
  }
}

function classifyReadinessError(error: unknown): string {
  const message = error instanceof Error ? error.message : String(error)
  if (
    message.includes('尚未接入 PostgreSQL')
    || message.includes('JUHE_AI_DATABASE_DRIVER=postgres 不能回退写入 SQLite')
    || message.includes('PostgreSQL 模式') && message.includes('暂不支持')
  ) {
    return 'postgres_adapter_missing'
  }
  if (message.includes('relation') && message.includes('does not exist')) {
    return 'postgres_schema_missing'
  }
  return 'unexpected_error'
}

async function cleanupReadinessRows(client: DatabaseClient): Promise<void> {
  const lower = tracePrefix
  const upper = `${tracePrefix}\uffff`
  const ids = [
    `${runId}_usage`,
    `${runId}_audit`,
    `${runId}_oplog`,
    `${runId}_publog`
  ]
  const auditBlobStorageKeys = (await client.query<{ storage_key?: string }>(`
    SELECT DISTINCT b.storage_key
    FROM juhe_dataset.audit_payload_blobs b
    WHERE b.id IN (
      SELECT headers_blob_id
      FROM juhe_dataset.audit_payload_refs
      WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)
      UNION
      SELECT body_blob_id
      FROM juhe_dataset.audit_payload_refs
      WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)
    )
  `, [lower, upper, lower, upper]))
    .map((row) => row.storage_key)
    .filter((value): value is string => Boolean(value))

  await client.execute(
    'DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)',
    [lower, upper]
  )
  await client.execute(
    'DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id IN (SELECT id FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?)',
    [lower, upper]
  )
  await client.execute('DELETE FROM juhe_dataset.audit_logs WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  if (auditBlobStorageKeys.length > 0) {
    await client.execute(
      `DELETE FROM juhe_dataset.audit_payload_blobs WHERE storage_key IN (${auditBlobStorageKeys.map(() => '?').join(', ')})`,
      auditBlobStorageKeys
    )
    deleteAuditBlobFiles(auditBlobStorageKeys)
  }
  await client.execute('DELETE FROM juhe_dataset.operation_log_summary_search_terms WHERE operation_log_id = ?', [`${runId}_oplog`])
  await client.execute('DELETE FROM juhe_dataset.operation_log_viewers WHERE operation_log_id = ?', [`${runId}_oplog`])
  await client.execute('DELETE FROM juhe_dataset.operation_log_targets WHERE operation_log_id = ?', [`${runId}_oplog`])
  await client.execute('DELETE FROM juhe_dataset.operation_logs WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  await client.execute('DELETE FROM juhe_dataset.public_api_logs WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  await client.execute('DELETE FROM juhe_usage.usage_record_shard_entries WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  await client.execute('DELETE FROM juhe_usage.usage_records WHERE trace_id >= ? AND trace_id < ?', [lower, upper])
  await client.execute('DELETE FROM juhe_stats.account_quality_minute_stats WHERE account_id = ?', [`${runId}_account`])
  for (const tableName of ['usage_stats_totals', 'usage_stats_minute', 'usage_stats_hourly', 'usage_stats_daily', 'usage_stats_weekly', 'usage_stats_monthly']) {
    await client.execute(`DELETE FROM juhe_stats.${tableName} WHERE scope_id = ANY(?)`, [ids])
  }
}

function deleteAuditBlobFiles(storageKeys: string[]): void {
  const auditBlobRoot = resolve(backendRoot, 'data', 'audit', 'blobs')
  for (const storageKey of storageKeys) {
    const target = resolve(auditBlobRoot, storageKey)
    const relativePath = relative(auditBlobRoot, target)
    if (relativePath.startsWith('..') || isAbsolute(relativePath)) continue
    if (existsSync(target)) {
      unlinkSync(target)
    }
  }
}

function assertPerformanceRuntime(): void {
  if (runtimeConfig.runtimeMode !== 'performance' || runtimeConfig.databaseDriver !== 'postgres') {
    throw new Error('performance gateway persistence readiness 必须在 JUHE_AI_RUNTIME_MODE=performance / JUHE_AI_DATABASE_DRIVER=postgres 下运行')
  }
}

function writeReport(report: ReadinessReport): void {
  mkdirSync(dirname(reportPath), { recursive: true })
  writeFileSync(reportPath, JSON.stringify(report, null, 2), 'utf8')
}

function printReport(report: ReadinessReport): void {
  console.log('高性能网关事实表 readiness 结果')
  for (const check of report.checks) {
    console.log(`- ${check.name}: ${check.ok ? 'ok' : `failed (${check.category})`} ${check.latencyMs.toFixed(2)}ms`)
  }
  if (report.missingAdapterChecks.length > 0) {
    console.log(`缺失 PG adapter: ${report.missingAdapterChecks.join(', ')}`)
  }
  console.log(`报告已写入：${reportPath}`)
}

function timestampForFile(value: Date): string {
  return value.toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
}
