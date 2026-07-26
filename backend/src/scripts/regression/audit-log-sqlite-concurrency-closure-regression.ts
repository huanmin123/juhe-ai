import { strict as assert } from 'node:assert'
import { readdirSync, rmSync, statSync, truncateSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

import { backendRoot, runtimeConfig } from '../../config/runtime.js'
import { logger } from '../../shared/logger.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'

const marker = `audit_sqlite_closure_${Date.now()}_${Math.random().toString(16).slice(2)}`
const tempRoot = resolve(tmpdir(), marker)
const auditBlobRoot = resolve(backendRoot, 'data', 'audit', 'blobs')
runtimeConfig.databasePath = join(tempRoot, 'business.sqlite3')
runtimeConfig.datasetDatabasePath = join(tempRoot, 'dataset.sqlite3')
runtimeConfig.statsDatabasePath = join(tempRoot, 'stats.sqlite3')
runtimeConfig.secret = `${marker}_secret`
runtimeConfig.databaseDriver = 'sqlite'
runtimeConfig.log.consoleEnabled = false
runtimeConfig.log.fileEnabled = false
runtimeConfig.processRole = 'worker'
logger.level = 'silent'

const [databaseModule, auditRepository, payloadBlobs] = await Promise.all([
  import('../../storage/database.js'),
  import('../../storage/audit-logs.repository.js'),
  import('../../storage/audit-log-payload-blobs.js')
])

const auditIds = new Set<string>()
let blobIds: string[] = []

try {
  await verifyDuplicateAuditDoesNotInflateErrorGroup()
  await verifyDuplicatePayloadRefCountsActualSlots()
  await verifyDuplicateAttemptDoesNotCrossAttach()
  await verifyCompressionIdentityAndFileRepair()
  assert.equal(findTemporaryBlobFiles().length, 0, 'SQLite 原子写入不得遗留 .tmp 文件')
  console.log('SQLite 审计并发闭环回归通过：重复 ID、compression 协商、引用计数与文件修复均保持一致')
} finally {
  cleanupRows()
  try {
    databaseModule.closeStorageDatabases()
  } catch {
  }
  rmSync(tempRoot, { recursive: true, force: true })
}

async function verifyDuplicateAuditDoesNotInflateErrorGroup(): Promise<void> {
  const id = `audit_${marker}_duplicate_error`
  const first = auditLog({ id, outcome: 'upstream_failed', errorCode: 'SQLITE_DUPLICATE_ERROR' })
  auditIds.add(id)
  await auditRepository.createAuditLogsBatchAsync([first])
  await auditRepository.createAuditLogsBatchAsync([{ ...first, traceId: `${first.traceId}_retry` }])
  const database = databaseModule.getDatasetDatabase()
  const group = database.prepare(`
    SELECT groups.count
    FROM audit_error_groups groups
    JOIN audit_logs logs ON logs.error_group_id = groups.id
    WHERE logs.id = ?
  `).get(id) as { count?: number } | undefined
  assert.equal(Number(group?.count ?? 0), 1, '重复 audit id 不得提前虚增错误组 count')
}

async function verifyDuplicatePayloadRefCountsActualSlots(): Promise<void> {
  const payloadId = `payload_${marker}_duplicate`
  const first = auditLog({ id: `audit_${marker}_ref_a`, payloadId })
  const second = auditLog({ id: `audit_${marker}_ref_b`, payloadId })
  auditIds.add(String(first.id))
  auditIds.add(String(second.id))
  await auditRepository.createAuditLogsBatchAsync([first, second])
  const database = databaseModule.getDatasetDatabase()
  const refCount = database.prepare('SELECT COUNT(*) AS count FROM audit_payload_refs WHERE id = ?').get(payloadId) as { count?: number }
  assert.equal(Number(refCount.count ?? 0), 1, '重复 payload ref id 只能插入一次')
  assertReferencedBlobCountsMatchSlots()
}

async function verifyDuplicateAttemptDoesNotCrossAttach(): Promise<void> {
  const sharedAttemptId = `attempt_${marker}_duplicate`
  const first = auditLog({ id: `audit_${marker}_attempt_a`, attemptId: sharedAttemptId })
  const second = auditLog({ id: `audit_${marker}_attempt_b`, attemptId: sharedAttemptId })
  auditIds.add(String(first.id))
  auditIds.add(String(second.id))
  await auditRepository.createAuditLogsBatchAsync([first, second])
  const database = databaseModule.getDatasetDatabase()
  const secondRef = database.prepare('SELECT attempt_id FROM audit_payload_refs WHERE audit_log_id = ?').get(String(second.id)) as { attempt_id?: string | null } | undefined
  assert.equal(secondRef?.attempt_id ?? null, null, '重复 attempt id 不得把后一个日志的 payload 挂到前一个日志 attempt')
}

async function verifyCompressionIdentityAndFileRepair(): Promise<void> {
  const body = JSON.stringify({ marker, input: 'x'.repeat(16 * 1024) })
  const gzipLog = auditLog({ id: `audit_${marker}_compression_gzip`, body })
  const noneLog = auditLog({ id: `audit_${marker}_compression_none`, body, contentEncoding: 'gzip' })
  auditIds.add(String(gzipLog.id))
  auditIds.add(String(noneLog.id))
  await auditRepository.createAuditLogsBatchAsync([gzipLog, noneLog])

  const database = databaseModule.getDatasetDatabase()
  const rows = database.prepare(`
    SELECT DISTINCT blobs.id, blobs.storage_key, blobs.compression, blobs.compressed_size_bytes
    FROM audit_payload_refs refs
    JOIN audit_payload_blobs blobs ON blobs.id = refs.body_blob_id
    WHERE refs.audit_log_id IN (?, ?)
    ORDER BY blobs.compression
  `).all(String(gzipLog.id), String(noneLog.id)) as Array<{ id: string; storage_key: string; compression: string; compressed_size_bytes: number }>
  assert.equal(rows.length, 1, '相同原文必须复用同一个物理 blob')
  const gzipRow = rows[0]
  assert.equal(gzipRow?.compression, 'gzip', '复用 blob 必须保持既有 metadata compression')
  assert(gzipRow, '应生成可复用的 gzip blob')
  const gzipPath = resolve(auditBlobRoot, gzipRow.storage_key)
  truncateSync(gzipPath, 7)
  const repairLog = auditLog({ id: `audit_${marker}_compression_repair`, body })
  auditIds.add(String(repairLog.id))
  await auditRepository.createAuditLogsBatchAsync([repairLog])
  assert.equal(statSync(gzipPath).size, Number(gzipRow.compressed_size_bytes), '已有 SQLite blob 尺寸异常时必须原子修复')
  assertReferencedBlobCountsMatchSlots()
}

function assertReferencedBlobCountsMatchSlots(): void {
  const database = databaseModule.getDatasetDatabase()
  const rows = database.prepare(`
    SELECT blobs.id, blobs.ref_count,
      (SELECT COUNT(*) FROM audit_payload_refs refs WHERE refs.headers_blob_id = blobs.id) +
      (SELECT COUNT(*) FROM audit_payload_refs refs WHERE refs.body_blob_id = blobs.id) AS actual_ref_count
    FROM audit_payload_blobs blobs
    WHERE blobs.id IN (
      SELECT headers_blob_id FROM audit_payload_refs WHERE audit_log_id IN (${placeholders(auditIds.size)})
      UNION
      SELECT body_blob_id FROM audit_payload_refs WHERE audit_log_id IN (${placeholders(auditIds.size)})
    )
  `).all(...auditIds, ...auditIds) as Array<{ id: string; ref_count: number; actual_ref_count: number }>
  assert(rows.length > 0, '应存在本轮引用 blob')
  blobIds = [...new Set([...blobIds, ...rows.map((row) => row.id)])]
  for (const row of rows) {
    assert.equal(Number(row.ref_count), Number(row.actual_ref_count), `SQLite blob ${row.id} ref_count 必须等于真实引用槽位数`)
  }
}

function auditLog(options: {
  id: string
  payloadId?: string
  attemptId?: string
  body?: string
  contentEncoding?: string
  outcome?: 'success' | 'upstream_failed'
  errorCode?: string
}): AuditLogInput {
  const timestamp = '2026-07-26T08:00:00.000Z'
  const outcome = options.outcome ?? 'success'
  const attemptTempId = `${options.id}_attempt_temp`
  return {
    id: options.id,
    traceId: `trace_${options.id}`,
    trafficSource: 'gateway',
    providerCode: 'openai',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.6-sol',
    auditOutcome: outcome,
    success: outcome === 'success',
    finalStatusCode: outcome === 'success' ? 200 : 503,
    errorPhase: outcome === 'success' ? undefined : 'upstream_response',
    errorCode: options.errorCode,
    errorMessage: options.errorCode ? `${options.errorCode} stable message` : undefined,
    sampleBucket: 1,
    sampleReason: 'sqlite_concurrency_closure',
    captureStatus: 'complete',
    startedAt: timestamp,
    endedAt: timestamp,
    durationMs: 1,
    createdAt: timestamp,
    attempts: options.attemptId ? [{
      id: options.attemptId,
      tempId: attemptTempId,
      attemptIndex: 0,
      upstreamMethod: 'POST',
      upstreamUrl: 'https://mock.invalid/v1/responses',
      success: true,
      startedAt: timestamp,
      endedAt: timestamp,
      durationMs: 1
    }] : [],
    payloads: [{
      id: options.payloadId ?? `${options.id}_payload`,
      attemptTempId: options.attemptId ? attemptTempId : undefined,
      partType: 'client_request',
      sequenceIndex: 0,
      contentType: 'application/json',
      contentEncoding: options.contentEncoding,
      headers: { 'content-type': 'application/json', 'x-marker': marker },
      body: options.body ?? JSON.stringify({ marker, id: options.id }),
      createdAt: timestamp
    }]
  }
}

function cleanupRows(): void {
  if (auditIds.size === 0) return
  const database = databaseModule.getDatasetDatabase()
  const ids = [...auditIds]
  const groupRows = database.prepare(`SELECT DISTINCT error_group_id AS id FROM audit_logs WHERE id IN (${placeholders(ids.length)}) AND error_group_id IS NOT NULL`).all(...ids) as Array<{ id?: string }>
  database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders(ids.length)})`).run(...ids)
  const groupIds = groupRows.map((row) => row.id).filter((id): id is string => Boolean(id))
  if (groupIds.length > 0) database.prepare(`DELETE FROM audit_error_groups WHERE id IN (${placeholders(groupIds.length)})`).run(...groupIds)
  if (blobIds.length > 0) payloadBlobs.cleanupUnreferencedAuditPayloadBlobsByIds(blobIds, blobIds.length)
}

function findTemporaryBlobFiles(directory = auditBlobRoot): string[] {
  let entries
  try {
    entries = readdirSync(directory, { withFileTypes: true })
  } catch {
    return []
  }
  return entries.flatMap((entry) => {
    const path = resolve(directory, entry.name)
    return entry.isDirectory() ? findTemporaryBlobFiles(path) : entry.name.endsWith('.tmp') ? [path] : []
  })
}

function placeholders(count: number): string {
  return Array.from({ length: count }, () => '?').join(',')
}
