import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { runtimeConfig } from '../../config/runtime.js'
import {
  auditLogAttemptSupplementSelectColumns,
  auditLogDetailSupplementSelectColumns,
  auditLogPayloadSupplementSelectColumns
} from '../../storage/audit-log-detail-supplement.repository.js'
import { auditLogListSelectColumns } from '../../storage/audit-log-list-query.js'
import { auditLogDetailSupplementFromRow } from '../../storage/audit-log-mappers.js'

const repositorySource = readFileSync(fileURLToPath(new URL('../../storage/audit-log-detail-supplement.repository.ts', import.meta.url)), 'utf8')
const routeSource = readFileSync(fileURLToPath(new URL('../../modules/audit-logs/audit-logs.routes.ts', import.meta.url)), 'utf8')
const workerSource = readFileSync(fileURLToPath(new URL('../../storage/sqlite-read-worker.ts', import.meta.url)), 'utf8')

const listColumns = projectedColumns(auditLogListSelectColumns('al'), 'al')
const supplementColumns = projectedColumns(auditLogDetailSupplementSelectColumns('al'), 'al')
assert.deepEqual(supplementColumns, [
  'conversation_key',
  'query_string',
  'error_message',
  'sample_bucket',
  'sample_reason',
  'started_at',
  'ended_at',
  'http_completed_at'
], '详情补充 SQL 必须使用固定字段白名单')
assert.deepEqual(
  supplementColumns.filter((column) => listColumns.includes(column)),
  [],
  '详情补充 SQL 不得重新读取列表已有字段'
)

assert.deepEqual(projectedColumns(auditLogAttemptSupplementSelectColumns('attempts'), 'attempts'), [
  'id', 'attempt_index', 'account_id', 'upstream_url', 'upstream_status_code',
  'success', 'error_message', 'started_at', 'ended_at', 'duration_ms'
], '尝试明细 SQL 必须使用固定字段白名单')
assert.deepEqual(projectedColumns(auditLogPayloadSupplementSelectColumns('payloads'), 'payloads'), [
  'id', 'attempt_id', 'part_type', 'sequence_index', 'raw_size_bytes', 'capture_status',
  'created_at', 'headers_blob_id', 'body_blob_id'
], 'payload 元数据 SQL 必须使用固定字段白名单')
assert.doesNotMatch(repositorySource, /SELECT\s+(?:al|attempts|payloads)\.\*/i, '详情补充仓储不得 SELECT *')

const supplement = auditLogDetailSupplementFromRow({
  conversation_key: 'conversation-1',
  query_string: 'include=usage',
  error_message: 'failed',
  sample_bucket: 5,
  sample_reason: 'problem',
  started_at: '2026-07-29T00:00:00.000Z',
  ended_at: '2026-07-29T00:00:01.000Z',
  http_completed_at: '2026-07-29T00:00:01.100Z',
  id: 'must-not-leak',
  trace_id: 'must-not-leak',
  method: 'POST',
  path: '/v1/responses',
  created_at: 'must-not-leak'
})
assert.deepEqual(Object.keys(supplement), [
  'queryString', 'errorMessage', 'sampleBucket', 'sampleReason', 'startedAt',
  'endedAt', 'httpCompletedAt', 'conversationKey'
], '详情补充 DTO 不得泄漏列表字段')

assert.match(routeSource, /getAuditLogDetailSupplementAsync\(req\.params\.id\)/, '管理详情路由必须调用补充字段仓储')
assert.doesNotMatch(routeSource, /getAuditLogDetailAsync\(req\.params\.id\)/, '管理详情路由不得调用完整内部详情仓储')
assert.match(workerSource, /case 'get_audit_log_detail_supplement_read_only':[\s\S]{0,120}getAuditLogDetailSupplement\(operation\.id\)/, 'SQLite read worker 必须有专用补充字段操作')

const tempRoot = resolve(tmpdir(), `juhe-ai-audit-detail-supplement-${Date.now()}-${Math.random().toString(16).slice(2)}`)
mkdirSync(tempRoot, { recursive: true })
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = 'audit-log-detail-supplement-regression-secret'
runtimeConfig.processRole = 'db-service'
runtimeConfig.workerRole = 'worker'
runtimeConfig.sqliteReadWorkerPoolSize = 1
runtimeConfig.sqliteReadWorkerQueueMaxItems = 8

const repositories = await import('../../storage/repositories.js')
const database = await import('../../storage/database.js')
const readWorkerPool = await import('../../storage/sqlite-read-worker-pool.js')
try {
  repositories.createAuditLogsBatch([{
    id: 'audit-detail-supplement-1',
    traceId: 'trace-detail-supplement-1',
    conversationKey: 'conversation-detail-supplement-1',
    trafficSource: 'gateway',
    providerCode: 'gpt',
    method: 'POST',
    path: '/v1/responses',
    queryString: 'include=usage',
    model: 'gpt-5',
    upstreamModel: 'gpt-5-upstream',
    pricingModel: 'gpt-5-pricing',
    modelMappingApplied: true,
    modelMappingSource: 'account',
    sourceEndpointFamily: 'responses',
    upstreamEndpointFamily: 'responses',
    stream: true,
    clientIp: '127.0.0.1',
    userAgent: 'audit-detail-regression',
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 502,
    errorPhase: 'upstream',
    errorCode: 'upstream_failed',
    errorMessage: 'upstream failed',
    sampleBucket: 7,
    sampleReason: 'problem',
    captureStatus: 'complete',
    startedAt: '2026-07-29T00:00:00.000Z',
    endedAt: '2026-07-29T00:00:01.000Z',
    durationMs: 1000,
    httpCompletedAt: '2026-07-29T00:00:00.900Z',
    httpDurationMs: 900,
    firstTokenMs: 250,
    attempts: [{
      id: 'audit-attempt-supplement-1',
      attemptIndex: 0,
      providerCode: 'gpt',
      model: 'gpt-5',
      upstreamModel: 'gpt-5-upstream',
      upstreamMethod: 'POST',
      upstreamUrl: 'https://example.test/v1/responses',
      upstreamStatusCode: 502,
      success: false,
      errorPhase: 'upstream',
      errorCode: 'upstream_failed',
      errorMessage: 'upstream failed',
      startedAt: '2026-07-29T00:00:00.000Z',
      endedAt: '2026-07-29T00:00:01.000Z',
      durationMs: 1000
    }],
    payloads: []
  }])

  const storedSupplement = repositories.getAuditLogDetailSupplement('audit-detail-supplement-1')
  assert(storedSupplement, 'SQLite 专用详情补充仓储应读回记录')
  assert.equal(storedSupplement.conversationKey, 'conversation-detail-supplement-1')
  assert.equal(storedSupplement.queryString, 'include=usage')
  assert.equal(storedSupplement.errorMessage, 'upstream failed')
  assert.equal(storedSupplement.attempts.length, 1)
  assert.equal(storedSupplement.attempts[0]?.upstreamStatusCode, 502)
  assert(!('id' in storedSupplement), '详情补充响应不得包含列表已有 id')
  assert(!('traceId' in storedSupplement), '详情补充响应不得包含列表已有 traceId')
  assert(!('method' in storedSupplement), '详情补充响应不得包含列表已有 method')
  assert(!('path' in storedSupplement), '详情补充响应不得包含列表已有 path')
  assert(!('createdAt' in storedSupplement), '详情补充响应不得包含列表已有 createdAt')

  assert.equal(readWorkerPool.sqliteReadWorkerPoolEnabled(), true, 'DB service + SQLite 必须启用审计详情 read worker')
  const handledJobsBefore = readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs
  const asyncSupplement = await repositories.getAuditLogDetailSupplementAsync('audit-detail-supplement-1')
  assert(asyncSupplement, '异步详情补充仓储应通过 read worker 读回记录')
  assert.equal(asyncSupplement.conversationKey, 'conversation-detail-supplement-1')
  assert.equal(asyncSupplement.attempts[0]?.upstreamStatusCode, 502)
  assert(!('id' in asyncSupplement), 'read worker 详情补充响应不得包含列表已有 id')
  assert(
    readWorkerPool.getSqliteReadWorkerPoolRuntime().handledJobs > handledJobsBefore,
    '审计详情补充异步读取必须进入 SQLite read worker'
  )

  const completeDetail = repositories.getAuditLogDetail('audit-detail-supplement-1')
  assert(completeDetail, '原有完整详情仓储必须继续兼容内部调用')
  assert.equal(completeDetail.id, 'audit-detail-supplement-1')
  assert.equal(completeDetail.traceId, 'trace-detail-supplement-1')
  assert.equal(completeDetail.conversationKey, 'conversation-detail-supplement-1')
} finally {
  await readWorkerPool.closeSqliteReadWorkerPool().catch(() => undefined)
  database.closeStorageDatabases()
  rmSync(tempRoot, { recursive: true, force: true })
}

console.log('审计日志详情补充回归通过：SQL、DTO、路由与 read-worker 均只读取并返回列表缺失字段，完整内部详情保持兼容')

function projectedColumns(projection: string, alias: string): string[] {
  return projection.split(', ').map((column) => column.replace(new RegExp(`^${alias}\\.`), ''))
}
