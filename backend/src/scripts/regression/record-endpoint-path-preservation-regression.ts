import { strict as assert } from 'node:assert'
import { mkdirSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput, UsageRecordInput } from '../../storage/repositories.js'

const tempRoot = resolve(tmpdir(), `juhe-ai-record-endpoint-path-preservation-${Date.now()}-${Math.random().toString(16).slice(2)}`)
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.usageCatalogDatabasePath = join(tempRoot, 'usage-catalog.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.usageShardRoot = join(tempRoot, 'usage-shards')
runtimeConfig.secret = 'record-endpoint-path-preservation-secret'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
mkdirSync(tempRoot, { recursive: true })
logger.level = 'silent'

const [
  databaseModule,
  repositories,
  usageRecordShards,
  requestMetadata
] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/repositories.js'),
  import('../../storage/usage-record-shards.js'),
  import('../../modules/gateway/request/metadata.js')
])

const access = { systemAccountId: 'sys_admin', role: 'admin' as const }
const responseIdPath = '/v1/responses/resp_endpoint_path_preservation'
const responseIdEndpoint = `GET ${responseIdPath}`
const compactPath = '/v1/responses/compact'
const compactEndpoint = `POST ${compactPath}`
const baseResponsesEndpoint = 'POST /v1/responses'
const queryString = 'include[]=input&stream=false'

try {
  assert.equal(
    requestMetadata.requestEndpoint({
      method: 'GET',
      originalUrl: `${responseIdPath}?${queryString}`,
      path: responseIdPath
    } as any),
    responseIdEndpoint,
    'gateway requestEndpoint must keep the Responses response-id path segment'
  )
  assert.equal(
    requestMetadata.requestEndpoint({
      method: 'POST',
      originalUrl: `${compactPath}?${queryString}`,
      path: compactPath
    } as any),
    compactEndpoint,
    'gateway requestEndpoint must keep the Responses compact path segment'
  )
  const gatewayRoutesSource = readFileSync(new URL('../../modules/gateway/routes.ts', import.meta.url), 'utf8')
  assert.doesNotMatch(
    gatewayRoutesSource,
    /\$\{req\.method\.toUpperCase\(\)\}\s+\$\{requestEndpoint\(req\)\}/,
    'gateway runtime endpoint log must not prepend the HTTP method twice'
  )

  const baseUsageId = usageRecordShards.generateUsageRecordId('2026-01-04T00:00:00.000Z', 'endpoint-base')
  const childUsageId = usageRecordShards.generateUsageRecordId('2026-01-04T00:00:01.000Z', 'endpoint-child')
  const compactUsageId = usageRecordShards.generateUsageRecordId('2026-01-04T00:00:02.000Z', 'endpoint-compact')
  repositories.createUsageRecordsBatch([
    usageRecord(baseUsageId, 'trace-endpoint-path-usage-base', baseResponsesEndpoint, '2026-01-04T00:00:00.000Z'),
    usageRecord(childUsageId, 'trace-endpoint-path-usage-child', responseIdEndpoint, '2026-01-04T00:00:01.000Z'),
    usageRecord(compactUsageId, 'trace-endpoint-path-usage-compact', compactEndpoint, '2026-01-04T00:00:02.000Z')
  ])

  const usageList = repositories.listUsageRecords(access, { page: 1, pageSize: 10 })
  const usageEndpointByTrace = new Map(usageList.items.map((item) => [item.traceId, item.endpoint]))
  assert.equal(usageEndpointByTrace.get('trace-endpoint-path-usage-base'), baseResponsesEndpoint, 'usage list must keep base Responses endpoint')
  assert.equal(usageEndpointByTrace.get('trace-endpoint-path-usage-child'), responseIdEndpoint, 'usage list must keep Responses child endpoint')
  assert.equal(usageEndpointByTrace.get('trace-endpoint-path-usage-compact'), compactEndpoint, 'usage list must keep Responses compact endpoint')
  assert.equal(
    repositories.getUsageRecordDetail(childUsageId, access)?.endpoint,
    responseIdEndpoint,
    'usage detail must not collapse /v1/responses/{id} to /v1/responses'
  )
  assert.equal(
    repositories.getUsageRecordDetail(compactUsageId, access)?.endpoint,
    compactEndpoint,
    'usage detail must not collapse /v1/responses/compact to /v1/responses or /v1/chat/completions'
  )

  repositories.createAuditLogsBatch([
    auditLog('audit_endpoint_path_base', 'trace-endpoint-path-audit-base', 'POST', '/v1/responses', undefined, '2026-01-04T00:00:03.000Z'),
    auditLog('audit_endpoint_path_child', 'trace-endpoint-path-audit-child', 'GET', responseIdPath, queryString, '2026-01-04T00:00:04.000Z'),
    auditLog('audit_endpoint_path_compact', 'trace-endpoint-path-audit-compact', 'POST', compactPath, queryString, '2026-01-04T00:00:05.000Z')
  ])

  const auditList = repositories.listAuditLogs({ page: 1, pageSize: 10 })
  const auditPathByTrace = new Map(auditList.items.map((item) => [item.traceId, `${item.method} ${item.path}`]))
  assert.equal(auditPathByTrace.get('trace-endpoint-path-audit-base'), baseResponsesEndpoint, 'audit list must keep base Responses path')
  assert.equal(auditPathByTrace.get('trace-endpoint-path-audit-child'), responseIdEndpoint, 'audit list must keep Responses child path')
  assert.equal(auditPathByTrace.get('trace-endpoint-path-audit-compact'), compactEndpoint, 'audit list must keep Responses compact path')

  const childAuditDetail = repositories.getAuditLogDetail('audit_endpoint_path_child')
  assert.equal(childAuditDetail?.path, responseIdPath, 'audit detail must keep the full child path')
  assert.equal(childAuditDetail?.queryString, queryString, 'audit detail must keep query separately without truncating path')
  const compactAuditDetail = repositories.getAuditLogDetail('audit_endpoint_path_compact')
  assert.equal(compactAuditDetail?.path, compactPath, 'audit detail must keep the compact path')
  assert.equal(compactAuditDetail?.queryString, queryString, 'audit compact detail must keep query separately without truncating path')

  const childAuditFilter = repositories.listAuditLogs({ path: responseIdPath, page: 1, pageSize: 10 })
  assert.deepEqual(childAuditFilter.items.map((item) => item.id), ['audit_endpoint_path_child'], 'audit path filter must match the full child path')
  const compactAuditFilter = repositories.listAuditLogs({ path: compactPath, page: 1, pageSize: 10 })
  assert.deepEqual(compactAuditFilter.items.map((item) => item.id), ['audit_endpoint_path_compact'], 'audit path filter must match the full compact path')
  const compactAuditFilterWithMethodAndQuery = repositories.listAuditLogs({ path: `${compactEndpoint}?${queryString}`, page: 1, pageSize: 10 })
  assert.deepEqual(
    compactAuditFilterWithMethodAndQuery.items.map((item) => item.id),
    ['audit_endpoint_path_compact'],
    'audit path filter must accept copied endpoint text with method and query while matching the full path'
  )
  const baseAuditFilter = repositories.listAuditLogs({ path: '/v1/responses', page: 1, pageSize: 10 })
  assert.deepEqual(baseAuditFilter.items.map((item) => item.id), ['audit_endpoint_path_base'], 'audit path filter must not collapse child paths into /v1/responses')

  console.log('record endpoint path preservation regression passed')
} finally {
  try {
    databaseModule.getBusinessDatabase().close()
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

function usageRecord(id: string, traceId: string, endpoint: string, createdAt: string): UsageRecordInput {
  return {
    id,
    systemAccountId: 'sys_admin',
    traceId,
    trafficSource: 'gateway',
    endpoint,
    providerCode: 'gpt',
    model: 'gpt-5.1',
    stream: endpoint.startsWith('POST '),
    statusCode: 200,
    success: true,
    durationMs: 20,
    inputTokens: 1,
    outputTokens: 1,
    createdAt
  }
}

function auditLog(
  id: string,
  traceId: string,
  method: string,
  path: string,
  queryString: string | undefined,
  timestamp: string
): AuditLogInput {
  return {
    id,
    traceId,
    trafficSource: 'gateway',
    systemAccountId: 'sys_admin',
    providerCode: 'gpt',
    method,
    path,
    queryString,
    model: 'gpt-5.1',
    stream: false,
    auditOutcome: 'success',
    success: true,
    finalStatusCode: 200,
    sampleBucket: 0,
    sampleReason: 'endpoint_path_preservation',
    captureStatus: 'complete',
    startedAt: timestamp,
    endedAt: timestamp,
    durationMs: 20,
    attempts: [],
    payloads: [],
    createdAt: timestamp
  }
}
