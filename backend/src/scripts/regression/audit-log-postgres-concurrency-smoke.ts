import { strict as assert } from 'node:assert'
import { createHash } from 'node:crypto'
import { readdir, stat, writeFile } from 'node:fs/promises'
import { relative, resolve, sep } from 'node:path'

import { runtimeConfig } from '../../config/runtime.js'
import { closeRedisClients } from '../../shared/redis-client.js'
import { cleanupUnreferencedAuditPayloadBlobsByIdsAsync } from '../../storage/audit-log-payload-blobs.js'
import { createAuditLogsBatchAsync } from '../../storage/audit-logs.repository.js'
import type { AuditLogInput } from '../../storage/audit-log-types.js'
import { closePostgresPool, getPostgresPool } from '../../storage/postgres-client.js'

assert.equal(runtimeConfig.databaseDriver, 'postgres', '审计日志 PG 并发 smoke 需要 JUHE_AI_DATABASE_DRIVER=postgres')

const marker = `audit_log_pg_concurrency_${Date.now()}_${Math.random().toString(16).slice(2)}`
const auditBlobRoot = runtimeConfig.auditPayloadBlobRoot
const batchCount = 8
const rowsPerBatch = 25
const auditLogIds = new Set<string>()
let blobIds: string[] = []

try {
  const filesBefore = await listAuditBlobFiles()
  const deadlocksBefore = await currentDatabaseDeadlocks()

  await runSharedPayloadWorkload()
  await runReverseDuplicateAuditIdWorkload()
  await runReverseErrorGroupWorkload()
  await runDuplicatePayloadRefWorkload()
  await runDuplicateAttemptWorkload()
  await runCompressionIdentityWorkload()
  await verifyExistingBlobFileRepair()

  const deadlocksAfter = await currentDatabaseDeadlocks()
  assert.equal(deadlocksAfter - deadlocksBefore, 0, '反向并发写入不应产生 PostgreSQL deadlock')

  const refRows = await loadReferencedBlobCounts()
  blobIds = refRows.map((row) => row.id)
  assert(refRows.length >= 2, '并发审计 smoke 应生成 headers/body payload blob')
  for (const row of refRows) {
    assert(Number(row.actual_ref_count) > 0, `payload blob ${row.id} 必须存在真实 headers/body 引用槽位`)
    assert.equal(Number(row.ref_count), 0, `payload blob ${row.id} 不应在 PG 热路径同步更新兼容 ref_count`)
  }

  await verifyDuplicateAuditIdsOnlyPersistWinnerPayloads()
  await verifyDuplicatePayloadRefOnlyCountsActualInsert()
  await verifyDuplicateAttemptDoesNotCrossAttach()
  await verifyCompressionIdentity()
  await verifyErrorGroupTimeBounds()
  await verifyNoAddedOrphanFiles(filesBefore)

  console.log(JSON.stringify({
    message: '审计日志 PostgreSQL 并发闭环 smoke 通过',
    batches: batchCount,
    sharedRows: batchCount * rowsPerBatch,
    totalAuditIds: auditLogIds.size,
    blobs: refRows.length,
    deadlocksDelta: deadlocksAfter - deadlocksBefore
  }))
} finally {
  await cleanupSmokeRows()
  await closeRedisClients()
  await closePostgresPool()
}

async function runSharedPayloadWorkload(): Promise<void> {
  await Promise.all(Array.from({ length: batchCount }, (_, batchIndex) => {
    const inputs = Array.from({ length: rowsPerBatch }, (_, rowIndex) => sharedPayloadAuditLog(batchIndex, rowIndex))
    trackAuditLogs(inputs)
    return createAuditLogsBatchAsync(inputs)
  }))
}

async function runReverseDuplicateAuditIdWorkload(): Promise<void> {
  const ids = [`audit_${marker}_duplicate_0`, `audit_${marker}_duplicate_1`]
  const forward = ids.map((id, index) => duplicateAuditLog(id, 'forward', index))
  const reverse = ids.map((id, index) => duplicateAuditLog(id, 'reverse', index)).reverse()
  trackAuditLogs(forward)
  await Promise.all([
    createAuditLogsBatchAsync(forward),
    createAuditLogsBatchAsync(reverse)
  ])
}

async function runReverseErrorGroupWorkload(): Promise<void> {
  const windowStart = Math.floor(Date.now() / (5 * 60 * 1000)) * (5 * 60 * 1000)
  const earlyAt = new Date(windowStart + 10_000).toISOString()
  const lateAt = new Date(windowStart + 20_000).toISOString()
  const forward = [
    errorAuditLog(`audit_${marker}_error_a_early`, 'SMOKE_ERROR_A', earlyAt),
    errorAuditLog(`audit_${marker}_error_b_early`, 'SMOKE_ERROR_B', earlyAt)
  ]
  const reverse = [
    errorAuditLog(`audit_${marker}_error_b_late`, 'SMOKE_ERROR_B', lateAt),
    errorAuditLog(`audit_${marker}_error_a_late`, 'SMOKE_ERROR_A', lateAt)
  ]
  trackAuditLogs(forward)
  trackAuditLogs(reverse)
  await Promise.all([
    createAuditLogsBatchAsync(forward),
    createAuditLogsBatchAsync(reverse)
  ])
}

async function runDuplicatePayloadRefWorkload(): Promise<void> {
  const payloadId = `audit_payload_${marker}_duplicate_ref`
  const first = successAuditLog({
    id: `audit_${marker}_duplicate_ref_a`,
    payloadId,
    body: duplicatePayloadRefBody()
  })
  const second = successAuditLog({
    id: `audit_${marker}_duplicate_ref_b`,
    payloadId,
    body: duplicatePayloadRefBody()
  })
  trackAuditLogs([first, second])
  await Promise.all([
    createAuditLogsBatchAsync([first]),
    createAuditLogsBatchAsync([second])
  ])
}

async function runDuplicateAttemptWorkload(): Promise<void> {
  const attemptId = `audit_attempt_${marker}_duplicate`
  const first = auditLogWithAttempt(`audit_${marker}_duplicate_attempt_a`, attemptId)
  const second = auditLogWithAttempt(`audit_${marker}_duplicate_attempt_b`, attemptId)
  trackAuditLogs([first, second])
  await createAuditLogsBatchAsync([first, second])
}

async function runCompressionIdentityWorkload(): Promise<void> {
  const body = compressionIdentityBody()
  const gzipLog = successAuditLog({
    id: `audit_${marker}_compression_gzip`,
    payloadId: `audit_payload_${marker}_compression_gzip`,
    body
  })
  const noneLog = successAuditLog({
    id: `audit_${marker}_compression_none`,
    payloadId: `audit_payload_${marker}_compression_none`,
    body
  })
  noneLog.payloads[0] = { ...noneLog.payloads[0], contentEncoding: 'gzip' }
  trackAuditLogs([gzipLog, noneLog])
  await createAuditLogsBatchAsync([gzipLog, noneLog])
}

async function verifyExistingBlobFileRepair(): Promise<void> {
  const sourceAuditId = `audit_${marker}_0_0`
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT blobs.id, blobs.storage_key, blobs.compressed_size_bytes
    FROM juhe_dataset.audit_payload_refs refs
    JOIN juhe_dataset.audit_payload_blobs blobs ON blobs.id = refs.body_blob_id
    WHERE refs.audit_log_id = $1
    LIMIT 1
  `, [sourceAuditId])
  const row = result.rows[0] as { id?: string; storage_key?: string; compressed_size_bytes?: string | number } | undefined
  assert(row?.storage_key, '应能定位共享 body blob 文件')
  const filePath = resolve(auditBlobRoot, row.storage_key)
  await writeFile(filePath, Buffer.from('corrupt'))

  const repairLog = sharedPayloadAuditLog('repair', 0)
  trackAuditLogs([repairLog])
  await createAuditLogsBatchAsync([repairLog])

  const repaired = await stat(filePath)
  assert.equal(repaired.size, Number(row.compressed_size_bytes), `已有 blob ${row.id ?? ''} 文件尺寸异常时必须原子修复`)
}

async function loadReferencedBlobCounts(): Promise<Array<{ id: string; ref_count: string | number; actual_ref_count: string | number }>> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT
      blobs.id,
      blobs.ref_count,
      (
        SELECT COUNT(*)
        FROM juhe_dataset.audit_payload_refs refs
        WHERE refs.headers_blob_id = blobs.id
      ) + (
        SELECT COUNT(*)
        FROM juhe_dataset.audit_payload_refs refs
        WHERE refs.body_blob_id = blobs.id
      ) AS actual_ref_count
    FROM juhe_dataset.audit_payload_blobs blobs
    WHERE blobs.id IN (
      SELECT headers_blob_id
      FROM juhe_dataset.audit_payload_refs
      WHERE audit_log_id = ANY($1::text[])
      UNION
      SELECT body_blob_id
      FROM juhe_dataset.audit_payload_refs
      WHERE audit_log_id = ANY($1::text[])
    )
    ORDER BY blobs.id
  `, [[...auditLogIds]])
  return result.rows as Array<{ id: string; ref_count: string | number; actual_ref_count: string | number }>
}

async function verifyDuplicateAuditIdsOnlyPersistWinnerPayloads(): Promise<void> {
  const pool = await getPostgresPool()
  const duplicateIds = [`audit_${marker}_duplicate_0`, `audit_${marker}_duplicate_1`]
  const refs = await pool.query(`
    SELECT COUNT(*) AS count
    FROM juhe_dataset.audit_payload_refs
    WHERE audit_log_id = ANY($1::text[])
  `, [duplicateIds])
  assert.equal(Number((refs.rows[0] as { count?: string | number } | undefined)?.count ?? 0), duplicateIds.length, '重复 audit id 只能保留实际插入日志的 payload ref')

  const candidateBodyHashes = ['forward', 'reverse'].flatMap((side) => duplicateIds.map((_, index) => sha256Text(duplicateAuditBody(side, index))))
  const blobs = await pool.query(`
    SELECT COUNT(*) AS count
    FROM juhe_dataset.audit_payload_blobs
    WHERE sha256 = ANY($1::text[])
  `, [candidateBodyHashes])
  assert.equal(Number((blobs.rows[0] as { count?: string | number } | undefined)?.count ?? 0), duplicateIds.length, '未插入 audit log 的候选 payload 不应创建 blob 元数据')
}

async function verifyDuplicatePayloadRefOnlyCountsActualInsert(): Promise<void> {
  const pool = await getPostgresPool()
  const payloadId = `audit_payload_${marker}_duplicate_ref`
  const result = await pool.query('SELECT COUNT(*) AS count FROM juhe_dataset.audit_payload_refs WHERE id = $1', [payloadId])
  assert.equal(Number((result.rows[0] as { count?: string | number } | undefined)?.count ?? 0), 1, '重复 payload ref id 只能计入一次真实插入')
}

async function verifyDuplicateAttemptDoesNotCrossAttach(): Promise<void> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT refs.attempt_id
    FROM juhe_dataset.audit_payload_refs refs
    WHERE refs.audit_log_id = $1
    LIMIT 1
  `, [`audit_${marker}_duplicate_attempt_b`])
  assert.equal((result.rows[0] as { attempt_id?: string | null } | undefined)?.attempt_id ?? null, null, '重复 attempt id 不得把后一个日志 payload 挂到前一个日志 attempt')
}

async function verifyCompressionIdentity(): Promise<void> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT blobs.id, blobs.compression, COUNT(*) AS ref_count
    FROM juhe_dataset.audit_payload_refs refs
    JOIN juhe_dataset.audit_payload_blobs blobs ON blobs.id = refs.body_blob_id
    WHERE refs.audit_log_id = ANY($1::text[])
    GROUP BY blobs.id, blobs.compression
  `, [[`audit_${marker}_compression_gzip`, `audit_${marker}_compression_none`]])
  const rows = result.rows as Array<{ id: string; compression: string; ref_count: string | number }>
  assert.equal(rows.length, 1, 'PG 相同原文必须复用同一个物理 blob')
  assert.equal(rows[0]?.compression, 'gzip', 'PG 复用 blob 必须保持既有 metadata compression')
  assert.equal(Number(rows[0]?.ref_count ?? 0), 2, '不同输入 encoding 的两个 payload ref 必须共同引用同一物理 blob')
}

async function verifyErrorGroupTimeBounds(): Promise<void> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT groups.error_code, groups.count, groups.first_event_id, groups.last_event_id, groups.created_at, groups.updated_at
    FROM juhe_dataset.audit_error_groups groups
    WHERE groups.id IN (
      SELECT DISTINCT error_group_id
      FROM juhe_dataset.audit_logs
      WHERE id = ANY($1::text[])
        AND error_group_id IS NOT NULL
    )
    ORDER BY groups.error_code
  `, [[
    `audit_${marker}_error_a_early`,
    `audit_${marker}_error_a_late`,
    `audit_${marker}_error_b_early`,
    `audit_${marker}_error_b_late`
  ]])
  const rows = result.rows as Array<{
    error_code: string
    count: string | number
    first_event_id: string
    last_event_id: string
    created_at: string | Date
    updated_at: string | Date
  }>
  assert.equal(rows.length, 2, '两个错误指纹应形成两个稳定错误组')
  for (const row of rows) {
    const suffix = row.error_code === 'SMOKE_ERROR_A' ? 'a' : 'b'
    assert.equal(Number(row.count), 2, `${row.error_code} 应聚合两个并发事件`)
    assert.equal(row.first_event_id, `audit_${marker}_error_${suffix}_early`, `${row.error_code} first_event_id 应保持最早事件`)
    assert.equal(row.last_event_id, `audit_${marker}_error_${suffix}_late`, `${row.error_code} last_event_id 应保持最晚事件`)
    assert(new Date(row.created_at).getTime() <= new Date(row.updated_at).getTime(), `${row.error_code} 时间边界不得倒置`)
  }
}

async function verifyNoAddedOrphanFiles(filesBefore: Set<string>): Promise<void> {
  const filesAfter = await listAuditBlobFiles()
  const addedFiles = [...filesAfter].filter((storageKey) => !filesBefore.has(storageKey))
  assert.equal(addedFiles.some((storageKey) => storageKey.endsWith('.tmp') || storageKey.endsWith('.trash')), false, '原子 blob 写入和 fencing 清理不得遗留临时文件')
  if (addedFiles.length === 0) return
  const pool = await getPostgresPool()
  const result = await pool.query('SELECT storage_key FROM juhe_dataset.audit_payload_blobs WHERE storage_key = ANY($1::text[])', [addedFiles])
  const storedKeys = new Set((result.rows as Array<{ storage_key: string }>).map((row) => normalizeStorageKey(row.storage_key)))
  for (const storageKey of addedFiles) {
    assert(storedKeys.has(storageKey), `新增 payload 文件 ${storageKey} 必须有对应元数据`)
  }
}

function sharedPayloadAuditLog(batchIndex: number | string, rowIndex: number): AuditLogInput {
  return successAuditLog({
    id: `audit_${marker}_${batchIndex}_${rowIndex}`,
    payloadId: `audit_payload_${marker}_${batchIndex}_${rowIndex}`,
    body: sharedPayloadBody()
  })
}

function duplicateAuditLog(id: string, side: string, index: number): AuditLogInput {
  return successAuditLog({
    id,
    payloadId: `audit_payload_${marker}_duplicate_${side}_${index}`,
    body: duplicateAuditBody(side, index)
  })
}

function errorAuditLog(id: string, errorCode: string, timestamp: string): AuditLogInput {
  const input = successAuditLog({
    id,
    payloadId: `${id}_payload`,
    body: JSON.stringify({ marker, errorCode, input: 'shared error request' }),
    timestamp
  })
  return {
    ...input,
    auditOutcome: 'upstream_failed',
    success: false,
    finalStatusCode: 503,
    errorPhase: 'upstream_response',
    errorCode,
    errorMessage: `${errorCode} stable smoke error`
  }
}

function auditLogWithAttempt(id: string, attemptId: string): AuditLogInput {
  const input = successAuditLog({
    id,
    payloadId: `${id}_payload`,
    body: JSON.stringify({ marker, id, input: 'duplicate attempt candidate' })
  })
  const timestamp = input.createdAt ?? new Date().toISOString()
  const attemptTempId = `${id}_attempt_temp`
  return {
    ...input,
    attempts: [{
      id: attemptId,
      tempId: attemptTempId,
      attemptIndex: 0,
      upstreamMethod: 'POST',
      upstreamUrl: 'https://mock.invalid/v1/responses',
      success: true,
      startedAt: timestamp,
      endedAt: timestamp,
      durationMs: 1
    }],
    payloads: input.payloads.map((payload) => ({ ...payload, attemptTempId }))
  }
}

function successAuditLog(options: { id: string; payloadId: string; body: string; timestamp?: string }): AuditLogInput {
  const timestamp = options.timestamp ?? new Date().toISOString()
  return {
    id: options.id,
    traceId: `trace_${options.id}`,
    trafficSource: 'gateway',
    providerCode: 'openai',
    method: 'POST',
    path: '/v1/responses',
    model: 'gpt-5.6-sol',
    stream: false,
    auditOutcome: 'success',
    success: true,
    finalStatusCode: 200,
    sampleBucket: 1,
    sampleReason: 'postgres_concurrency_smoke',
    captureStatus: 'complete',
    startedAt: timestamp,
    httpCompletedAt: timestamp,
    httpDurationMs: 1,
    endedAt: timestamp,
    durationMs: 1,
    createdAt: timestamp,
    attempts: [],
    payloads: [{
      id: options.payloadId,
      partType: 'client_request',
      sequenceIndex: 0,
      contentType: 'application/json',
      headers: { 'content-type': 'application/json', 'x-fixture': marker },
      body: options.body,
      createdAt: timestamp
    }]
  }
}

function sharedPayloadBody(): string {
  return JSON.stringify({ marker, model: 'gpt-5.6-sol', input: 'shared payload body' })
}

function duplicateAuditBody(side: string, index: number): string {
  return JSON.stringify({ marker, side, index, input: 'duplicate audit id candidate' })
}

function duplicatePayloadRefBody(): string {
  return JSON.stringify({ marker, input: 'duplicate payload ref candidate' })
}

function compressionIdentityBody(): string {
  return JSON.stringify({ marker, input: 'x'.repeat(16 * 1024) })
}

function trackAuditLogs(inputs: AuditLogInput[]): void {
  for (const input of inputs) {
    if (input.id) auditLogIds.add(input.id)
  }
}

function sha256Text(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

async function listAuditBlobFiles(directory = auditBlobRoot): Promise<Set<string>> {
  const files = new Set<string>()
  await collectAuditBlobFiles(directory, files)
  return files
}

async function collectAuditBlobFiles(directory: string, files: Set<string>): Promise<void> {
  let entries
  try {
    entries = await readdir(directory, { withFileTypes: true })
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return
    throw error
  }
  for (const entry of entries) {
    const path = resolve(directory, entry.name)
    if (entry.isDirectory()) {
      await collectAuditBlobFiles(path, files)
    } else if (entry.isFile()) {
      files.add(normalizeStorageKey(relative(auditBlobRoot, path)))
    }
  }
}

function normalizeStorageKey(value: string): string {
  return value.split(sep).join('/')
}

async function currentDatabaseDeadlocks(): Promise<number> {
  const pool = await getPostgresPool()
  const result = await pool.query(`
    SELECT deadlocks
    FROM pg_stat_database
    WHERE datname = current_database()
    LIMIT 1
  `)
  return Number((result.rows[0] as { deadlocks?: string | number } | undefined)?.deadlocks ?? 0)
}

async function cleanupSmokeRows(): Promise<void> {
  if (auditLogIds.size === 0) return
  const ids = [...auditLogIds]
  const pool = await getPostgresPool()
  const blobResult = await pool.query(`
    SELECT headers_blob_id AS id FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY($1::text[])
    UNION
    SELECT body_blob_id AS id FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY($1::text[])
  `, [ids])
  blobIds = [...new Set([
    ...blobIds,
    ...(blobResult.rows as Array<{ id?: string }>).map((row) => row.id).filter((id): id is string => Boolean(id))
  ])]
  const groupResult = await pool.query('SELECT DISTINCT error_group_id AS id FROM juhe_dataset.audit_logs WHERE id = ANY($1::text[]) AND error_group_id IS NOT NULL', [ids])
  const groupIds = (groupResult.rows as Array<{ id?: string }>).map((row) => row.id).filter((id): id is string => Boolean(id))

  await pool.query('DELETE FROM juhe_dataset.audit_payload_refs WHERE audit_log_id = ANY($1::text[])', [ids])
  await pool.query('DELETE FROM juhe_dataset.audit_log_attempts WHERE audit_log_id = ANY($1::text[])', [ids])
  await pool.query('DELETE FROM juhe_dataset.audit_logs WHERE id = ANY($1::text[])', [ids])
  if (groupIds.length > 0) {
    await pool.query('DELETE FROM juhe_dataset.audit_error_groups WHERE id = ANY($1::text[])', [groupIds])
  }
  if (blobIds.length > 0) {
    await cleanupUnreferencedAuditPayloadBlobsByIdsAsync(blobIds, blobIds.length)
  }
}
