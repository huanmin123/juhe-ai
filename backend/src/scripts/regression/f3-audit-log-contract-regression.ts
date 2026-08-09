import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

/**
 * F3 前 Node 审计基线：只冻结当前 Node contract，供后续 Go 对照。
 * 这是 F3 前 Node 基线，不代表 Go 实现，也不把当前 Node queue driver 当作未来要求。
 */

function source(relativePath: string): string {
  return readFileSync(fileURLToPath(new URL(`../../${relativePath}`, import.meta.url)), 'utf8')
}

function includes(text: string, fragment: string, label: string): void {
  assert(text.includes(fragment), `${label} 缺少源码证据：${fragment}`)
}

function matches(text: string, pattern: RegExp, label: string): void {
  assert(pattern.test(text), `${label} 缺少源码匹配：${pattern}`)
}

const types = source('storage/audit-log-types.ts')
const capture = source('modules/gateway/audit/capture.service.ts')
const queue = source('modules/audit-logs/audit-log-queue.service.ts')
const sqliteWriter = source('storage/audit-logs.repository.ts')
const errorGroups = source('storage/audit-log-error-groups.repository.ts')
const payloadBlobs = source('storage/audit-log-payload-blobs.ts')
const retention = source('storage/audit-log-retention.repository.ts')
const hotRetention = source('modules/background/audit-hot-retention-cleanup.service.ts')
const fullRetention = source('modules/background/data-retention-cleanup.service.ts')
const routes = source('modules/audit-logs/audit-logs.routes.ts')
const datasetSchema = source('storage/schema/dataset-schema.ts')

// AuditLogInput：枚举和迁移必须保留的关键字段。
for (const contract of [
  "export type AuditLogLifecycleStatus = 'in_progress' | 'finalized'",
  "export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_succeeded' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'downstream_closed'",
  "export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error' | 'gateway_metadata'",
  'export interface AuditLogInput',
  'traceId: string',
  'lifecycleStatus?: AuditLogLifecycleStatus',
  'trafficSource?: AuditTrafficSource',
  'method: string',
  'path: string',
  'auditOutcome: AuditOutcome',
  'success: boolean',
  'sampleBucket: number',
  'sampleReason: string',
  'startedAt: string',
  'endedAt: string',
  'attempts: AuditLogAttemptInput[]',
  'payloads: AuditLogPayloadInput[]'
]) {
  includes(types, contract, 'AuditLogInput/枚举字段')
}

// Gateway capture：流式 in_progress 与 finalized 必须使用同一个 auditLogId，并都 enqueue。
matches(capture, /id:\s*this\.auditLogId[\s\S]{0,500}lifecycleStatus:\s*'finalized'/, '网关终态 capture')
matches(capture, /id:\s*this\.auditLogId[\s\S]{0,500}lifecycleStatus:\s*'in_progress'/, '网关流式进行中 capture')
assert((capture.match(/enqueueAuditLog\(/g) ?? []).length >= 2, '网关 capture 必须存在 in_progress/finalized 两条 enqueue 证据')
includes(capture, "lifecycleStatus: 'in_progress'", '网关流式生命周期')
includes(capture, "lifecycleStatus: 'finalized'", '网关终态生命周期')

// Node queue 当前对照：Redis Stream、worker IPC、本地队列三条路径都必须可见。
for (const contract of [
  'enqueueAuditLogToRedisStream',
  'sendAuditLogsToWorker',
  'enqueueAuditLogLocal',
  'shouldEnqueueAuditLogToRedisStream',
  'shouldDispatchAuditLogToIngestWorker',
  'function enqueueAuditLogsLocal'
]) {
  includes(queue, contract, 'Node audit queue 路径')
}
includes(queue, '高性能模式禁止回退 IPC 或本地队列', 'Redis queue 非回退语义')

// SQLite / PostgreSQL writer 主表、attempt、payload ref、blob 与 error-group 引用。
for (const sql of [
  'INSERT INTO audit_logs (',
  'INSERT INTO audit_log_attempts (',
  'INSERT INTO audit_payload_refs (',
  'headers_blob_id',
  'body_blob_id',
  'error_group_id',
  'UPDATE audit_logs SET error_group_id = ? WHERE id = ?',
  'INSERT INTO juhe_dataset.audit_logs (',
  'INSERT INTO juhe_dataset.audit_log_attempts (',
  'INSERT INTO juhe_dataset.audit_payload_refs ('
]) {
  includes(sqliteWriter, sql, 'SQLite/PG audit writer SQL')
}
includes(payloadBlobs, 'INSERT INTO audit_payload_blobs (', 'SQLite payload blob writer')
includes(sqliteWriter, 'INSERT INTO juhe_dataset.audit_payload_blobs (', 'PostgreSQL payload blob writer')
includes(errorGroups, 'INSERT INTO audit_error_groups (', 'SQLite error-group writer')
includes(errorGroups, 'INSERT INTO juhe_dataset.audit_error_groups (', 'PostgreSQL error-group writer')
for (const table of ['audit_logs', 'audit_log_attempts', 'audit_payload_blobs', 'audit_payload_refs', 'audit_error_groups']) {
  includes(datasetSchema, `CREATE TABLE IF NOT EXISTS ${table}`, 'SQLite audit schema')
}

// Hot/full retention 入口，以及 payload/blob/error-group 清理引用。
for (const entry of [
  'cleanupAuditSuccessHotRetentionAsync',
  'cleanupAuditLogsByRetentionAsync',
  'cleanupAuditLogsBeforeAsync',
  'cleanupAuditPayloadBlobsBeforeAsync',
  'cleanupUnreferencedAuditPayloadBlobsAsync'
]) {
  const found = [retention, hotRetention, fullRetention, payloadBlobs].some((text) => text.includes(entry))
  assert(found, `Node retention 入口缺少源码证据：${entry}`)
}
includes(hotRetention, 'successHotCutoffCreatedAt', 'hot retention cutoff')
includes(fullRetention, 'auditLogSuccessHotHours', 'full retention audit settings')
includes(fullRetention, 'auditLogFailureDays', 'failure/full retention audit settings')

// Node audit read routes：保留当前管理读取面作为迁移前对照。
for (const route of [
  "auditLogsRouter.use(requireAdmin)",
  "auditLogsRouter.get('/',",
  "auditLogsRouter.get('/search-hot',",
  "auditLogsRouter.get('/runtime',",
  "auditLogsRouter.get('/error-groups',",
  "auditLogsRouter.get('/error-groups/:id/events',",
  "auditLogsRouter.get('/:id',",
  "auditLogsRouter.get('/:id/payloads/:payloadId',"
]) {
  includes(routes, route, 'Node audit read routes')
}

console.log('F3 前 Node 审计契约基线通过：字段、网关同 ID 生命周期、队列/存储/retention/read routes 均有源码证据。')
console.log('声明：这是 F3 前 Node 基线，不代表 Go 实现；当前 Node queue 路径仅作迁移前对照。')
