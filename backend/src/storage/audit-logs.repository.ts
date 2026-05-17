import { createHash } from 'node:crypto'
import { createReadStream, existsSync, mkdirSync, unlinkSync, writeFileSync } from 'node:fs'
import { dirname, isAbsolute, relative, resolve } from 'node:path'
import { createGunzip, gzipSync } from 'node:zlib'

import { backendRoot } from '../config/runtime.js'
import { beginDatabaseTransaction, commitDatabaseTransaction, getRecordDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { compatiblePagedTotal, takePageRows } from './query-utils.js'
import { loadAccountNameMap, loadApiKeyNameMap, loadGroupNameMap, loadSystemAccountNameMap } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'client_aborted'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error' | 'gateway_metadata'
export type AuditPayloadCaptureStatus = 'complete' | 'summary_only' | 'hash_only' | 'expired' | 'overflow' | 'dropped'

export interface AuditLogPayloadInput {
  id?: string
  attemptTempId?: string
  partType: AuditPayloadPartType
  sequenceIndex?: number
  contentType?: string
  contentEncoding?: string
  headers?: Record<string, string | string[]>
  body?: Buffer | string
  createdAt?: string
}

export interface AuditLogAttemptInput {
  id?: string
  tempId?: string
  attemptIndex: number
  accountId?: string
  accountOwnerSystemAccountId?: string
  groupId?: string
  proxyUrl?: string
  providerCode?: string
  upstreamMethod: string
  upstreamUrl: string
  upstreamStatusCode?: number
  success?: boolean
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt?: string
  durationMs?: number
}

export interface AuditLogInput {
  id?: string
  traceId: string
  systemAccountId?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  providerCode?: string
  method: string
  path: string
  queryString?: string
  model?: string
  stream?: boolean
  clientIp?: string
  userAgent?: string
  auditOutcome: AuditOutcome
  success: boolean
  finalStatusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  sampleBucket: number
  sampleReason: string
  captureStatus?: 'complete' | 'dropped' | 'overflow'
  startedAt: string
  endedAt: string
  durationMs?: number
  firstTokenMs?: number
  attempts: AuditLogAttemptInput[]
  payloads: AuditLogPayloadInput[]
  createdAt?: string
}

export interface AuditLogSummary {
  id: string
  traceId: string
  systemAccountId?: string
  systemAccountName?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  providerCode?: string
  method: string
  path: string
  queryString?: string
  model?: string
  stream: boolean
  clientIp?: string
  userAgent?: string
  auditOutcome: AuditOutcome
  success: boolean
  finalStatusCode?: number
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  sampleBucket: number
  sampleReason: string
  attemptCount: number
  payloadCount: number
  payloadBytes: number
  rawPayloadBytes: number
  compressedPayloadBytes: number
  compressionSavedBytes: number
  errorGroupId?: string
  captureStatus: string
  startedAt: string
  endedAt: string
  durationMs?: number
  firstTokenMs?: number
  createdAt: string
}

export interface AuditLogAttemptSummary {
  id: string
  attemptIndex: number
  accountId?: string
  accountName?: string
  accountOwnerSystemAccountId?: string
  groupId?: string
  groupName?: string
  proxyUrl?: string
  providerCode?: string
  upstreamMethod: string
  upstreamUrl: string
  upstreamStatusCode?: number
  success: boolean
  errorPhase?: string
  errorCode?: string
  errorMessage?: string
  startedAt: string
  endedAt?: string
  durationMs?: number
}

export interface AuditLogPayloadSummary {
  id: string
  attemptId?: string
  partType: AuditPayloadPartType
  sequenceIndex: number
  contentType?: string
  contentEncoding?: string
  headersSha256?: string
  bodySha256?: string
  sizeBytes: number
  compressedSizeBytes: number
  captureStatus: AuditPayloadCaptureStatus
  createdAt: string
  hasHeaders: boolean
  hasBody: boolean
}

export interface AuditLogDetail extends AuditLogSummary {
  attempts: AuditLogAttemptSummary[]
  errorGroup?: AuditErrorGroupSummary
  payloads: AuditLogPayloadSummary[]
}

export interface AuditLogPayloadDetail extends AuditLogPayloadSummary {
  headers?: Record<string, string | string[]>
  bodyText?: string
  bodyBase64?: string
  bodyOffset: number
  bodyLimit: number
  bodyBytesReturned: number
  bodyTotalBytes: number
  bodyNextOffset?: number
  bodyTruncated: boolean
}

export interface AuditLogListOptions {
  page?: number
  pageSize?: number
  limit?: number
  traceId?: string
  outcome?: AuditOutcome | 'all'
  statusCode?: number
  path?: string
  model?: string
  systemAccountId?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  clientIp?: string
  errorGroupId?: string
}

export interface AuditLogPayloadReadOptions {
  offset?: number
  limit?: number
}

export interface AuditLogListResult {
  items: AuditLogSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface AuditErrorGroupSummary {
  id: string
  fingerprint: string
  windowStartedAt: string
  windowEndedAt: string
  systemAccountId?: string
  systemAccountName?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  providerCode?: string
  path?: string
  model?: string
  statusCode?: number
  errorPhase?: string
  errorCode?: string
  errorType?: string
  requestFingerprint?: string
  errorFingerprint?: string
  count: number
  firstEventId?: string
  lastEventId?: string
  sampleEventId?: string
  lastMessage?: string
  createdAt: string
  updatedAt: string
}

export interface AuditErrorGroupListOptions {
  page?: number
  pageSize?: number
  limit?: number
  path?: string
  model?: string
  statusCode?: number
  systemAccountId?: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
}

export interface AuditErrorGroupListResult {
  items: AuditErrorGroupSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

type AuditLogRow = Record<string, unknown>
type AuditLogFilterValue = string | number
type StoredCompression = 'none' | 'gzip'

interface PreparedPayloadBlob {
  sha256: string
  rawSizeBytes: number
  compressedSizeBytes: number
  contentType: string
  contentEncoding?: string
  compression: StoredCompression
  bytes: Buffer
}

interface PreparedAuditPayload {
  id: string
  attemptTempId?: string
  partType: AuditPayloadPartType
  sequenceIndex: number
  contentType?: string
  contentEncoding?: string
  headersBlob?: PreparedPayloadBlob
  bodyBlob?: PreparedPayloadBlob
  headersSha256?: string
  bodySha256?: string
  rawSizeBytes: number
  compressedSizeBytes: number
  captureStatus: AuditPayloadCaptureStatus
  createdAt: string
}

interface StoredPayloadBlobMeta {
  storageKey: string
  compression: StoredCompression
  rawSizeBytes: number
  compressedSizeBytes: number
}

interface PayloadBlobWindow {
  bytes?: Buffer
  offset: number
  limit: number
  totalBytes: number
  nextOffset?: number
  truncated: boolean
}

const auditLogDefaultPageSize = 100
const auditLogMaxPageSize = 100
const errorGroupDefaultPageSize = 100
const errorGroupMaxPageSize = 100
const auditBlobRoot = resolve(backendRoot, 'data', 'audit', 'blobs')
const auditBlobCompressionThresholdBytes = 4 * 1024
const auditPayloadDefaultReadLimitBytes = 256 * 1024
const auditPayloadMaxReadLimitBytes = 1024 * 1024
const auditErrorGroupWindowMs = 5 * 60 * 1000
const auditHeadersContentType = 'application/json; audit=headers'

export function createAuditLogsBatch(inputs: AuditLogInput[]): void {
  if (inputs.length === 0) return

  const database = getRecordDatabase()
  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, system_account_id, api_key_id, group_id, account_id, provider_code, method, path, query_string,
      model, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, payload_bytes, raw_payload_bytes,
      compressed_payload_bytes, compression_saved_bytes, error_group_id, capture_status, started_at, ended_at,
      duration_ms, first_token_ms, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const insertAttempt = database.prepare(`
    INSERT INTO audit_log_attempts (
      id, audit_log_id, attempt_index, account_id, account_owner_system_account_id, group_id, proxy_url, provider_code,
      upstream_method, upstream_url, upstream_status_code, success, error_phase, error_code, error_message,
      started_at, ended_at, duration_ms
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const insertPayloadRef = database.prepare(`
    INSERT INTO audit_payload_refs (
      id, audit_log_id, attempt_id, part_type, sequence_index, content_type, content_encoding, headers_blob_id,
      body_blob_id, headers_sha256, body_sha256, raw_size_bytes, compressed_size_bytes, capture_status, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)

  const createdStorageKeys: string[] = []
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      const id = input.id ?? newId('audit')
      if (auditLogExists(database, id)) {
        continue
      }
      const createdAt = input.createdAt ?? nowIso()
      const attemptIds = new Map<string, string>()
      const preparedAttempts = input.attempts.map((attempt) => {
        const attemptId = attempt.id ?? newId('audatt')
        if (attempt.tempId) {
          attemptIds.set(attempt.tempId, attemptId)
        }
        return { ...attempt, id: attemptId }
      })
      const payloads = input.payloads.map((payload, index) => preparePayloadInput(payload, index, createdAt))
      const payloadBytes = payloads.reduce((sum, payload) => sum + payload.rawSizeBytes, 0)
      const compressedPayloadBytes = payloads.reduce((sum, payload) => sum + payload.compressedSizeBytes, 0)
      const compressionSavedBytes = Math.max(0, payloadBytes - compressedPayloadBytes)
      const errorGroupId = upsertAuditErrorGroup(database, input, id, payloads, createdAt)

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        input.systemAccountId ?? null,
        input.apiKeyId ?? null,
        input.groupId ?? null,
        input.accountId ?? null,
        input.providerCode ?? null,
        input.method,
        input.path,
        input.queryString ?? null,
        input.model ?? null,
        input.stream ? 1 : 0,
        input.clientIp ?? null,
        input.userAgent ?? null,
        input.auditOutcome,
        input.success ? 1 : 0,
        input.finalStatusCode ?? null,
        input.errorPhase ?? null,
        input.errorCode ?? null,
        input.errorMessage ?? null,
        input.sampleBucket,
        input.sampleReason,
        preparedAttempts.length,
        payloads.length,
        payloadBytes,
        payloadBytes,
        compressedPayloadBytes,
        compressionSavedBytes,
        errorGroupId,
        input.captureStatus ?? 'complete',
        input.startedAt,
        input.endedAt,
        input.durationMs ?? null,
        input.firstTokenMs ?? null,
        createdAt
      )
      if (Number(insertLogResult.changes ?? 0) === 0) {
        continue
      }

      for (const attempt of preparedAttempts) {
        insertAttempt.run(
          attempt.id,
          id,
          attempt.attemptIndex,
          attempt.accountId ?? null,
          attempt.accountOwnerSystemAccountId ?? null,
          attempt.groupId ?? null,
          attempt.proxyUrl ?? null,
          attempt.providerCode ?? null,
          attempt.upstreamMethod,
          attempt.upstreamUrl,
          attempt.upstreamStatusCode ?? null,
          attempt.success ? 1 : 0,
          attempt.errorPhase ?? null,
          attempt.errorCode ?? null,
          attempt.errorMessage ?? null,
          attempt.startedAt,
          attempt.endedAt ?? null,
          attempt.durationMs ?? null
        )
      }

      for (const payload of payloads) {
        const headersBlobId = persistPayloadBlob(database, payload.headersBlob, createdAt, createdStorageKeys)
        const bodyBlobId = persistPayloadBlob(database, payload.bodyBlob, createdAt, createdStorageKeys)
        insertPayloadRef.run(
          payload.id,
          id,
          payload.attemptTempId ? attemptIds.get(payload.attemptTempId) ?? null : null,
          payload.partType,
          payload.sequenceIndex,
          payload.contentType ?? null,
          payload.contentEncoding ?? null,
          headersBlobId,
          bodyBlobId,
          payload.headersSha256 ?? null,
          payload.bodySha256 ?? null,
          payload.rawSizeBytes,
          payload.compressedSizeBytes,
          payload.captureStatus,
          payload.createdAt
        )
      }
    }

    commitDatabaseTransaction(database, transactionStarted)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    cleanupCreatedBlobFiles(createdStorageKeys)
    throw error
  }
}

function auditLogExists(database: ReturnType<typeof getRecordDatabase>, id: string): boolean {
  const row = database.prepare('SELECT id FROM audit_logs WHERE id = ?').get(id) as unknown as { id?: string } | undefined
  return Boolean(row?.id)
}

export function listAuditLogs(options: AuditLogListOptions = {}): AuditLogListResult {
  const filters = buildAuditLogFilters(options)
  const pageSize = normalizePageSize(options.pageSize ?? options.limit, auditLogDefaultPageSize, auditLogMaxPageSize)
  const page = normalizePage(options.page)
  const offset = (page - 1) * pageSize
  const database = getRecordDatabase()
  const rows = database
    .prepare(`
      SELECT
        al.*
      FROM audit_logs al
      ${filters.clause}
      ORDER BY al.created_at DESC, al.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as AuditLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const rowsWithNames = hydrateAuditRows(pageRows.rows)
  const systemAccountNames = loadSystemAccountNameMap()
  const items = rowsWithNames.map((row) => auditLogSummaryFromRow(row, systemAccountNames))
  return {
    items,
    total: compatiblePagedTotal(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function listAuditErrorGroups(options: AuditErrorGroupListOptions = {}): AuditErrorGroupListResult {
  const filters = buildAuditErrorGroupFilters(options)
  const pageSize = normalizePageSize(options.pageSize ?? options.limit, errorGroupDefaultPageSize, errorGroupMaxPageSize)
  const page = normalizePage(options.page)
  const offset = (page - 1) * pageSize
  const database = getRecordDatabase()
  const rows = database
    .prepare(`
      SELECT
        aeg.*
      FROM audit_error_groups aeg
      ${filters.clause}
      ORDER BY aeg.updated_at DESC, aeg.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as AuditLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const rowsWithNames = hydrateAuditRows(pageRows.rows)
  const systemAccountNames = loadSystemAccountNameMap()
  const items = rowsWithNames.map((row) => auditErrorGroupFromRow(row, systemAccountNames))
  return {
    items,
    total: compatiblePagedTotal(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function listAuditErrorGroupEvents(errorGroupId: string, options: AuditLogListOptions = {}): AuditLogListResult {
  return listAuditLogs({ ...options, errorGroupId })
}

export function getAuditLogDetail(id: string): AuditLogDetail | undefined {
  const row = getRecordDatabase()
    .prepare(`
      SELECT
        al.*
      FROM audit_logs al
      WHERE al.id = ?
    `)
    .get(id) as AuditLogRow | undefined
  if (!row) return undefined
  const namedRow = hydrateAuditRows([row])[0] ?? row

  const attemptRows = getRecordDatabase()
    .prepare('SELECT * FROM audit_log_attempts WHERE audit_log_id = ? ORDER BY attempt_index ASC, id ASC')
    .all(id) as AuditLogRow[]
  const payloadRows = getAuditPayloadRows(id)
  const systemAccountNames = loadSystemAccountNameMap()
  const errorGroupId = optionalString(namedRow.error_group_id)
  const accountNames = loadAccountNameMap(attemptRows.map((attempt) => String(attempt.account_id ?? '')).filter(Boolean))
  const groupNames = loadGroupNameMap(attemptRows.map((attempt) => String(attempt.group_id ?? '')).filter(Boolean))
  return {
    ...auditLogSummaryFromRow(namedRow, systemAccountNames),
    attempts: attemptRows.map((attempt) => auditLogAttemptFromRow(attempt, accountNames, groupNames)),
    errorGroup: errorGroupId ? getAuditErrorGroupById(errorGroupId, systemAccountNames) : undefined,
    payloads: payloadRows.map(auditLogPayloadSummaryFromRow)
  }
}

function getAuditErrorGroupById(id: string, systemAccountNames: Map<string, string>): AuditErrorGroupSummary | undefined {
  const row = getRecordDatabase()
    .prepare(`
      SELECT
        aeg.*
      FROM audit_error_groups aeg
      WHERE aeg.id = ?
    `)
    .get(id) as AuditLogRow | undefined
  const namedRow = row ? hydrateAuditRows([row])[0] : undefined
  return namedRow ? auditErrorGroupFromRow(namedRow, systemAccountNames) : undefined
}

export async function getAuditLogPayload(
  auditLogId: string,
  payloadId: string,
  options: AuditLogPayloadReadOptions = {}
): Promise<AuditLogPayloadDetail | undefined> {
  const row = getRecordDatabase()
    .prepare(`
      SELECT *
      FROM audit_payload_refs
      WHERE audit_log_id = ? AND id = ?
    `)
    .get(auditLogId, payloadId) as AuditLogRow | undefined
  if (row) {
    const summary = auditLogPayloadSummaryFromRow(row)
    const headers = await readHeadersBlob(optionalString(row.headers_blob_id))
    const bodyWindow = await readPayloadBlobWindow(optionalString(row.body_blob_id), options)
    return {
      ...summary,
      headers,
      ...bodyDetail(bodyWindow.bytes),
      bodyOffset: bodyWindow.offset,
      bodyLimit: bodyWindow.limit,
      bodyBytesReturned: bodyWindow.bytes?.byteLength ?? 0,
      bodyTotalBytes: bodyWindow.totalBytes,
      bodyNextOffset: bodyWindow.nextOffset,
      bodyTruncated: bodyWindow.truncated
    }
  }
}

export function cleanupAuditLogsBefore(cutoffCreatedAt: string, limit?: number): number {
  const deleted = deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  cleanupUnreferencedAuditPayloadBlobs(limit)
  return deleted
}

export function cleanupAuditLogsByRetention(input: {
  successCutoffCreatedAt: string
  failureCutoffCreatedAt: string
  errorGroupCutoffUpdatedAt: string
  limit?: number
}): number {
  const deletedLogs = deleteAuditLogsByWhere(
    "((audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))",
    [input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    input.limit
  )
  const deletedGroups = cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, input.limit)
  const deletedBlobs = cleanupUnreferencedAuditPayloadBlobs(input.limit)
  return deletedLogs + deletedGroups + deletedBlobs
}

export function cleanupUnreferencedAuditPayloadBlobs(limit = 1000): number {
  const database = getRecordDatabase()
  const rows = database
    .prepare(`
      SELECT b.id, b.storage_key
      FROM audit_payload_blobs b
      WHERE NOT EXISTS (
        SELECT 1
        FROM audit_payload_refs r
        WHERE r.headers_blob_id = b.id OR r.body_blob_id = b.id
      )
      ORDER BY b.created_at ASC, b.id ASC
      LIMIT ?
    `)
    .all(Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  if (rows.length === 0) return 0

  const ids = rows.map((row) => String(row.id)).filter(Boolean)
  for (const row of rows) {
    deleteBlobFile(optionalString(row.storage_key))
  }
  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_payload_blobs WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

function deleteAuditLogsByWhere(whereClause: string, params: AuditLogFilterValue[], limit?: number): number {
  const database = getRecordDatabase()
  if (!limit) {
    const result = database.prepare(`DELETE FROM audit_logs WHERE ${whereClause}`).run(...params)
    return Number(result.changes ?? 0)
  }

  const rows = database
    .prepare(`SELECT id FROM audit_logs WHERE ${whereClause} ORDER BY created_at ASC, id ASC LIMIT ?`)
    .all(...params, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

function cleanupAuditErrorGroupsBefore(cutoffUpdatedAt: string, limit?: number): number {
  const database = getRecordDatabase()
  if (!limit) {
    const result = database.prepare('DELETE FROM audit_error_groups WHERE updated_at < ?').run(cutoffUpdatedAt)
    return Number(result.changes ?? 0)
  }
  const rows = database
    .prepare('SELECT id FROM audit_error_groups WHERE updated_at < ? ORDER BY updated_at ASC, id ASC LIMIT ?')
    .all(cutoffUpdatedAt, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0
  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_error_groups WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

function preparePayloadInput(payload: AuditLogPayloadInput, fallbackIndex: number, fallbackCreatedAt: string): PreparedAuditPayload {
  const headersBlob = payload.headers ? prepareBlob(Buffer.from(stableJsonStringify(payload.headers), 'utf8'), auditHeadersContentType) : undefined
  const bodyBlob = prepareBlob(bodyToBuffer(payload.body), payload.contentType, payload.contentEncoding)
  const rawSizeBytes = (headersBlob?.rawSizeBytes ?? 0) + (bodyBlob?.rawSizeBytes ?? 0)
  const compressedSizeBytes = (headersBlob?.compressedSizeBytes ?? 0) + (bodyBlob?.compressedSizeBytes ?? 0)
  return {
    id: payload.id ?? newId('audpay'),
    attemptTempId: payload.attemptTempId,
    partType: payload.partType,
    sequenceIndex: payload.sequenceIndex ?? fallbackIndex,
    contentType: payload.contentType,
    contentEncoding: payload.contentEncoding,
    headersBlob,
    bodyBlob,
    headersSha256: headersBlob?.sha256,
    bodySha256: bodyBlob?.sha256,
    rawSizeBytes,
    compressedSizeBytes,
    captureStatus: 'complete',
    createdAt: payload.createdAt ?? fallbackCreatedAt
  }
}

function prepareBlob(input: Buffer | undefined, contentType?: string, contentEncoding?: string): PreparedPayloadBlob | undefined {
  if (!input) return undefined
  const rawSizeBytes = input.byteLength
  const sha256 = createHash('sha256').update(input).digest('hex')
  const normalizedContentType = normalizeBlobContentType(contentType)
  const compressed = compressPayloadBytes(input, normalizedContentType, contentEncoding)
  return {
    sha256,
    rawSizeBytes,
    compressedSizeBytes: compressed.bytes.byteLength,
    contentType: normalizedContentType,
    contentEncoding,
    compression: compressed.compression,
    bytes: compressed.bytes
  }
}

function persistPayloadBlob(
  database: ReturnType<typeof getRecordDatabase>,
  blob: PreparedPayloadBlob | undefined,
  timestamp: string,
  createdStorageKeys: string[]
): string | null {
  if (!blob) return null

  const existing = database
    .prepare('SELECT id, storage_key FROM audit_payload_blobs WHERE sha256 = ? AND raw_size_bytes = ? AND content_type = ?')
    .get(blob.sha256, blob.rawSizeBytes, blob.contentType) as AuditLogRow | undefined
  const existingId = optionalString(existing?.id)
  if (existingId) {
    database
      .prepare('UPDATE audit_payload_blobs SET ref_count = ref_count + 1, last_seen_at = ? WHERE id = ?')
      .run(timestamp, existingId)
    writeBlobFileIfMissing(optionalString(existing?.storage_key), blob.bytes)
    return existingId
  }

  const id = newId('audblob')
  const storageKey = storageKeyForBlob(id, blob.compression)
  writeBlobFile(storageKey, blob.bytes)
  createdStorageKeys.push(storageKey)
  database
    .prepare(`
      INSERT INTO audit_payload_blobs (
        id, sha256, raw_size_bytes, compressed_size_bytes, content_type, content_encoding, compression,
        storage_key, ref_count, first_seen_at, last_seen_at, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
    `)
    .run(
      id,
      blob.sha256,
      blob.rawSizeBytes,
      blob.compressedSizeBytes,
      blob.contentType,
      blob.contentEncoding ?? null,
      blob.compression,
      storageKey,
      timestamp,
      timestamp,
      timestamp
    )
  return id
}

function compressPayloadBytes(input: Buffer, contentType: string, contentEncoding?: string): { bytes: Buffer; compression: StoredCompression } {
  if (input.byteLength < auditBlobCompressionThresholdBytes || !isCompressiblePayload(contentType, contentEncoding)) {
    return { bytes: input, compression: 'none' }
  }
  try {
    const compressed = gzipSync(input)
    return compressed.byteLength < input.byteLength
      ? { bytes: compressed, compression: 'gzip' }
      : { bytes: input, compression: 'none' }
  } catch {
    return { bytes: input, compression: 'none' }
  }
}

function isCompressiblePayload(contentType: string, contentEncoding?: string): boolean {
  const encoding = contentEncoding?.trim().toLowerCase()
  if (encoding && encoding !== 'identity') {
    return false
  }
  const type = contentType.toLowerCase()
  return type.includes('json')
    || type.includes('text')
    || type.includes('xml')
    || type.includes('event-stream')
    || type.includes('javascript')
    || type.includes('x-www-form-urlencoded')
}

function upsertAuditErrorGroup(
  database: ReturnType<typeof getRecordDatabase>,
  input: AuditLogInput,
  auditLogId: string,
  payloads: PreparedAuditPayload[],
  timestamp: string
): string | null {
  if (input.auditOutcome === 'success') {
    return null
  }
  const requestFingerprint = auditRequestFingerprint(input, payloads)
  const errorFingerprint = auditErrorFingerprint(input)
  const windowStartedAt = auditErrorWindowStart(timestamp)
  const windowEndedAt = new Date(Date.parse(windowStartedAt) + auditErrorGroupWindowMs).toISOString()
  const fingerprint = sha256Text(stableJsonStringify({
    systemAccountId: input.systemAccountId ?? '',
    apiKeyId: input.apiKeyId ?? '',
    groupId: input.groupId ?? '',
    accountId: input.accountId ?? '',
    providerCode: input.providerCode ?? '',
    path: input.path,
    model: input.model ?? '',
    statusCode: input.finalStatusCode ?? '',
    errorPhase: input.errorPhase ?? '',
    errorCode: input.errorCode ?? '',
    requestFingerprint,
    errorFingerprint
  }))
  const existing = database
    .prepare('SELECT id FROM audit_error_groups WHERE fingerprint = ? AND window_started_at = ?')
    .get(fingerprint, windowStartedAt) as AuditLogRow | undefined
  const existingId = optionalString(existing?.id)
  if (existingId) {
    database
      .prepare(`
        UPDATE audit_error_groups
        SET count = count + 1,
            window_ended_at = ?,
            last_event_id = ?,
            sample_event_id = COALESCE(sample_event_id, ?),
            last_message = ?,
            updated_at = ?
        WHERE id = ?
      `)
      .run(windowEndedAt, auditLogId, auditLogId, input.errorMessage ?? null, timestamp, existingId)
    return existingId
  }

  const id = newId('audgrp')
  database
    .prepare(`
      INSERT INTO audit_error_groups (
        id, fingerprint, window_started_at, window_ended_at, system_account_id, api_key_id, group_id, account_id,
        provider_code, path, model, status_code, error_phase, error_code, error_type, request_fingerprint,
        error_fingerprint, count, first_event_id, last_event_id, sample_event_id, last_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
    `)
    .run(
      id,
      fingerprint,
      windowStartedAt,
      windowEndedAt,
      input.systemAccountId ?? null,
      input.apiKeyId ?? null,
      input.groupId ?? null,
      input.accountId ?? null,
      input.providerCode ?? null,
      input.path,
      input.model ?? null,
      input.finalStatusCode ?? null,
      input.errorPhase ?? null,
      input.errorCode ?? null,
      input.auditOutcome,
      requestFingerprint,
      errorFingerprint,
      auditLogId,
      auditLogId,
      auditLogId,
      input.errorMessage ?? null,
      timestamp,
      timestamp
    )
  return id
}

function auditRequestFingerprint(input: AuditLogInput, payloads: PreparedAuditPayload[]): string {
  const clientRequest = payloads.find((payload) => payload.partType === 'client_request')
  return sha256Text(stableJsonStringify({
    method: input.method,
    path: input.path,
    model: input.model ?? '',
    stream: input.stream === true,
    bodySha256: clientRequest?.bodySha256 ?? ''
  }))
}

function auditErrorFingerprint(input: AuditLogInput): string {
  const failedAttempt = input.attempts.find((attempt) => attempt.success === false)
  return sha256Text(stableJsonStringify({
    outcome: input.auditOutcome,
    statusCode: input.finalStatusCode ?? failedAttempt?.upstreamStatusCode ?? '',
    phase: input.errorPhase ?? failedAttempt?.errorPhase ?? '',
    code: input.errorCode ?? failedAttempt?.errorCode ?? '',
    message: normalizeErrorMessage(input.errorMessage ?? failedAttempt?.errorMessage ?? '')
  }))
}

function normalizeErrorMessage(value: string): string {
  return value
    .slice(0, 500)
    .replace(/[0-9a-f]{16,}/gi, '{hex}')
    .replace(/\d{3,}/g, '{num}')
}

function auditErrorWindowStart(timestamp: string): string {
  const time = Date.parse(timestamp)
  const safeTime = Number.isFinite(time) ? time : Date.now()
  return new Date(Math.floor(safeTime / auditErrorGroupWindowMs) * auditErrorGroupWindowMs).toISOString()
}

function getAuditPayloadRows(auditLogId: string): AuditLogRow[] {
  return getRecordDatabase()
    .prepare('SELECT * FROM audit_payload_refs WHERE audit_log_id = ? ORDER BY sequence_index ASC, id ASC')
    .all(auditLogId) as AuditLogRow[]
}

function buildAuditLogFilters(options: AuditLogListOptions): { clause: string; params: AuditLogFilterValue[] } {
  const clauses: string[] = []
  const params: AuditLogFilterValue[] = []
  const pushTextFilter = (column: string, value?: string) => {
    const text = value?.trim()
    if (!text) return
    clauses.push(`${column} LIKE ?`)
    params.push(`%${text}%`)
  }

  pushTextFilter('al.trace_id', options.traceId)
  pushTextFilter('al.path', options.path)
  pushTextFilter('al.model', options.model)
  pushTextFilter('al.client_ip', options.clientIp)
  if (options.outcome && options.outcome !== 'all') {
    clauses.push('al.audit_outcome = ?')
    params.push(options.outcome)
  }
  if (isHttpStatusCode(options.statusCode)) {
    clauses.push('al.final_status_code = ?')
    params.push(options.statusCode)
  }
  for (const [column, value] of [
    ['al.system_account_id', options.systemAccountId],
    ['al.api_key_id', options.apiKeyId],
    ['al.group_id', options.groupId],
    ['al.account_id', options.accountId],
    ['al.error_group_id', options.errorGroupId]
  ] as const) {
    if (value?.trim()) {
      clauses.push(`${column} = ?`)
      params.push(value.trim())
    }
  }

  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function hydrateAuditRows(rows: AuditLogRow[]): AuditLogRow[] {
  if (!rows.length) return rows
  const apiKeyNames = loadApiKeyNameMap(rows.map((row) => optionalString(row.api_key_id) ?? ''))
  const groupNames = loadGroupNameMap(rows.map((row) => optionalString(row.group_id) ?? ''))
  const accountNames = loadAccountNameMap(rows.map((row) => optionalString(row.account_id) ?? ''))
  return rows.map((row) => ({
    ...row,
    api_key_name: optionalString(row.api_key_name) ?? (row.api_key_id ? apiKeyNames.get(String(row.api_key_id)) : undefined),
    group_name: optionalString(row.group_name) ?? (row.group_id ? groupNames.get(String(row.group_id)) : undefined),
    account_name: optionalString(row.account_name) ?? (row.account_id ? accountNames.get(String(row.account_id)) : undefined)
  }))
}

function buildAuditErrorGroupFilters(options: AuditErrorGroupListOptions): { clause: string; params: AuditLogFilterValue[] } {
  const clauses: string[] = []
  const params: AuditLogFilterValue[] = []
  const pushTextFilter = (column: string, value?: string) => {
    const text = value?.trim()
    if (!text) return
    clauses.push(`${column} LIKE ?`)
    params.push(`%${text}%`)
  }

  pushTextFilter('aeg.path', options.path)
  pushTextFilter('aeg.model', options.model)
  if (isHttpStatusCode(options.statusCode)) {
    clauses.push('aeg.status_code = ?')
    params.push(options.statusCode)
  }
  for (const [column, value] of [
    ['aeg.system_account_id', options.systemAccountId],
    ['aeg.api_key_id', options.apiKeyId],
    ['aeg.group_id', options.groupId],
    ['aeg.account_id', options.accountId]
  ] as const) {
    if (value?.trim()) {
      clauses.push(`${column} = ?`)
      params.push(value.trim())
    }
  }

  return {
    clause: clauses.length ? `WHERE ${clauses.join(' AND ')}` : '',
    params
  }
}

function normalizePage(value: unknown): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.max(1, value)
    : 1
}

function normalizePageSize(value: unknown, fallback: number, max: number): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(max, Math.max(1, value))
    : fallback
}

function auditLogSummaryFromRow(row: AuditLogRow, systemAccountNames: Map<string, string>): AuditLogSummary {
  const systemAccountId = optionalString(row.system_account_id)
  const rawPayloadBytes = Number(row.raw_payload_bytes ?? row.payload_bytes ?? 0)
  const compressedPayloadBytes = Number(row.compressed_payload_bytes ?? rawPayloadBytes)
  return {
    id: String(row.id),
    traceId: String(row.trace_id),
    systemAccountId,
    systemAccountName: systemAccountId ? systemAccountNames.get(systemAccountId) : undefined,
    apiKeyId: optionalString(row.api_key_id),
    apiKeyName: optionalString(row.api_key_name),
    groupId: optionalString(row.group_id),
    groupName: optionalString(row.group_name),
    accountId: optionalString(row.account_id),
    accountName: optionalString(row.account_name),
    providerCode: optionalString(row.provider_code),
    method: String(row.method),
    path: String(row.path),
    queryString: optionalString(row.query_string),
    model: optionalString(row.model),
    stream: row.stream === 1,
    clientIp: optionalString(row.client_ip),
    userAgent: optionalString(row.user_agent),
    auditOutcome: String(row.audit_outcome) as AuditOutcome,
    success: row.success === 1,
    finalStatusCode: numberValue(row.final_status_code),
    errorPhase: optionalString(row.error_phase),
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    sampleBucket: Number(row.sample_bucket ?? 0),
    sampleReason: String(row.sample_reason),
    attemptCount: Number(row.attempt_count ?? 0),
    payloadCount: Number(row.payload_count ?? 0),
    payloadBytes: Number(row.payload_bytes ?? rawPayloadBytes),
    rawPayloadBytes,
    compressedPayloadBytes,
    compressionSavedBytes: Number(row.compression_saved_bytes ?? Math.max(0, rawPayloadBytes - compressedPayloadBytes)),
    errorGroupId: optionalString(row.error_group_id),
    captureStatus: String(row.capture_status),
    startedAt: String(row.started_at),
    endedAt: String(row.ended_at),
    durationMs: numberValue(row.duration_ms),
    firstTokenMs: numberValue(row.first_token_ms),
    createdAt: String(row.created_at)
  }
}

function auditErrorGroupFromRow(row: AuditLogRow, systemAccountNames: Map<string, string>): AuditErrorGroupSummary {
  const systemAccountId = optionalString(row.system_account_id)
  return {
    id: String(row.id),
    fingerprint: String(row.fingerprint),
    windowStartedAt: String(row.window_started_at),
    windowEndedAt: String(row.window_ended_at),
    systemAccountId,
    systemAccountName: systemAccountId ? systemAccountNames.get(systemAccountId) : undefined,
    apiKeyId: optionalString(row.api_key_id),
    apiKeyName: optionalString(row.api_key_name),
    groupId: optionalString(row.group_id),
    groupName: optionalString(row.group_name),
    accountId: optionalString(row.account_id),
    accountName: optionalString(row.account_name),
    providerCode: optionalString(row.provider_code),
    path: optionalString(row.path),
    model: optionalString(row.model),
    statusCode: numberValue(row.status_code),
    errorPhase: optionalString(row.error_phase),
    errorCode: optionalString(row.error_code),
    errorType: optionalString(row.error_type),
    requestFingerprint: optionalString(row.request_fingerprint),
    errorFingerprint: optionalString(row.error_fingerprint),
    count: Number(row.count ?? 0),
    firstEventId: optionalString(row.first_event_id),
    lastEventId: optionalString(row.last_event_id),
    sampleEventId: optionalString(row.sample_event_id),
    lastMessage: optionalString(row.last_message),
    createdAt: String(row.created_at),
    updatedAt: String(row.updated_at)
  }
}

function auditLogAttemptFromRow(
  row: AuditLogRow,
  accountNames: Map<string, string>,
  groupNames: Map<string, string>
): AuditLogAttemptSummary {
  const accountId = optionalString(row.account_id)
  const groupId = optionalString(row.group_id)
  return {
    id: String(row.id),
    attemptIndex: Number(row.attempt_index ?? 0),
    accountId,
    accountName: accountId ? accountNames.get(accountId) : undefined,
    accountOwnerSystemAccountId: optionalString(row.account_owner_system_account_id),
    groupId,
    groupName: groupId ? groupNames.get(groupId) : undefined,
    proxyUrl: optionalString(row.proxy_url),
    providerCode: optionalString(row.provider_code),
    upstreamMethod: String(row.upstream_method),
    upstreamUrl: String(row.upstream_url),
    upstreamStatusCode: numberValue(row.upstream_status_code),
    success: row.success === 1,
    errorPhase: optionalString(row.error_phase),
    errorCode: optionalString(row.error_code),
    errorMessage: optionalString(row.error_message),
    startedAt: String(row.started_at),
    endedAt: optionalString(row.ended_at),
    durationMs: numberValue(row.duration_ms)
  }
}

function auditLogPayloadSummaryFromRow(row: AuditLogRow): AuditLogPayloadSummary {
  const sizeBytes = Number(row.raw_size_bytes ?? 0)
  return {
    id: String(row.id),
    attemptId: optionalString(row.attempt_id),
    partType: String(row.part_type) as AuditPayloadPartType,
    sequenceIndex: Number(row.sequence_index ?? 0),
    contentType: optionalString(row.content_type),
    contentEncoding: optionalString(row.content_encoding),
    headersSha256: optionalString(row.headers_sha256),
    bodySha256: optionalString(row.body_sha256),
    sizeBytes,
    compressedSizeBytes: Number(row.compressed_size_bytes ?? sizeBytes),
    captureStatus: String(row.capture_status ?? 'complete') as AuditPayloadCaptureStatus,
    createdAt: String(row.created_at),
    hasHeaders: Boolean(optionalString(row.headers_blob_id)),
    hasBody: Boolean(optionalString(row.body_blob_id))
  }
}

function bodyToBuffer(body: Buffer | string | undefined): Buffer | undefined {
  if (body === undefined) return undefined
  return Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
}

function isUtf8Text(buffer: Buffer): boolean {
  return buffer.toString('utf8').includes('\uFFFD') === false
}

function stableJsonStringify(value: unknown): string {
  if (Array.isArray(value)) {
    return `[${value.map(stableJsonStringify).join(',')}]`
  }
  if (value && typeof value === 'object') {
    const object = value as Record<string, unknown>
    return `{${Object.keys(object)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${stableJsonStringify(object[key])}`)
      .join(',')}}`
  }
  return JSON.stringify(value)
}

function sha256Text(value: string): string {
  return createHash('sha256').update(value).digest('hex')
}

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}

function normalizeBlobContentType(value?: string): string {
  const text = value?.trim()
  return text || 'application/octet-stream'
}

function storageKeyForBlob(id: string, compression: StoredCompression): string {
  const suffix = compression === 'gzip' ? 'gz' : 'blob'
  return `${id.slice(0, 2)}/${id}.${suffix}`
}

function blobFilePath(storageKey: string): string {
  const target = resolve(auditBlobRoot, storageKey)
  const relativePath = relative(auditBlobRoot, target)
  if (relativePath.startsWith('..') || isAbsolute(relativePath)) {
    throw new Error('审计 payload 存储路径非法')
  }
  return target
}

function writeBlobFile(storageKey: string, bytes: Buffer): void {
  const filePath = blobFilePath(storageKey)
  mkdirSync(dirname(filePath), { recursive: true })
  writeFileSync(filePath, bytes)
}

function writeBlobFileIfMissing(storageKey: string | undefined, bytes: Buffer): void {
  if (!storageKey) return
  const filePath = blobFilePath(storageKey)
  if (existsSync(filePath)) return
  mkdirSync(dirname(filePath), { recursive: true })
  writeFileSync(filePath, bytes)
}

function deleteBlobFile(storageKey: string | undefined): void {
  if (!storageKey) return
  try {
    const filePath = blobFilePath(storageKey)
    if (existsSync(filePath)) {
      unlinkSync(filePath)
    }
  } catch {
  }
}

function cleanupCreatedBlobFiles(storageKeys: string[]): void {
  for (const storageKey of storageKeys) {
    deleteBlobFile(storageKey)
  }
}

async function readPayloadBlobWindow(
  blobId: string | undefined,
  options: AuditLogPayloadReadOptions
): Promise<PayloadBlobWindow> {
  const offset = normalizePayloadReadOffset(options.offset)
  const limit = normalizePayloadReadLimit(options.limit)
  if (!blobId) {
    return emptyPayloadBlobWindow(offset, limit)
  }
  const meta = loadPayloadBlobMeta(blobId)
  if (!meta) {
    return emptyPayloadBlobWindow(offset, limit)
  }
  const filePath = blobFilePath(meta.storageKey)
  if (!existsSync(filePath)) {
    return emptyPayloadBlobWindow(offset, limit, meta.rawSizeBytes)
  }
  const bytes = meta.compression === 'gzip'
    ? await readGzipPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
    : await readPlainPayloadWindow(filePath, offset, limit, meta.rawSizeBytes)
  const nextOffset = offset + (bytes?.byteLength ?? 0)
  const truncated = nextOffset < meta.rawSizeBytes
  return {
    bytes,
    offset,
    limit,
    totalBytes: meta.rawSizeBytes,
    nextOffset: truncated ? nextOffset : undefined,
    truncated
  }
}

function loadPayloadBlobMeta(blobId: string): StoredPayloadBlobMeta | undefined {
  const row = getRecordDatabase()
    .prepare('SELECT storage_key, compression, raw_size_bytes, compressed_size_bytes FROM audit_payload_blobs WHERE id = ?')
    .get(blobId) as AuditLogRow | undefined
  const storageKey = optionalString(row?.storage_key)
  if (!storageKey) return undefined
  return {
    storageKey,
    compression: optionalString(row?.compression) === 'gzip' ? 'gzip' : 'none',
    rawSizeBytes: Math.max(0, Number(row?.raw_size_bytes ?? 0)),
    compressedSizeBytes: Math.max(0, Number(row?.compressed_size_bytes ?? 0))
  }
}

async function readHeadersBlob(blobId: string | undefined): Promise<Record<string, string | string[]> | undefined> {
  const bytes = (await readPayloadBlobWindow(blobId, {
    offset: 0,
    limit: auditPayloadMaxReadLimitBytes
  })).bytes
  if (!bytes) return undefined
  try {
    return JSON.parse(bytes.toString('utf8')) as Record<string, string | string[]>
  } catch {
    return undefined
  }
}

function normalizePayloadReadOffset(value: unknown): number {
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number) || number <= 0) return 0
  return Math.trunc(number)
}

function normalizePayloadReadLimit(value: unknown): number {
  const number = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(number) || number <= 0) return auditPayloadDefaultReadLimitBytes
  return Math.min(auditPayloadMaxReadLimitBytes, Math.max(1, Math.trunc(number)))
}

function emptyPayloadBlobWindow(offset: number, limit: number, totalBytes = 0): PayloadBlobWindow {
  return {
    offset,
    limit,
    totalBytes,
    truncated: offset < totalBytes
  }
}

async function readPlainPayloadWindow(
  filePath: string,
  offset: number,
  limit: number,
  totalBytes: number
): Promise<Buffer | undefined> {
  if (offset >= totalBytes || limit <= 0) return undefined
  const end = Math.min(totalBytes - 1, offset + limit - 1)
  return readStreamWindow(createReadStream(filePath, { start: offset, end }), limit)
}

async function readGzipPayloadWindow(
  filePath: string,
  offset: number,
  limit: number,
  totalBytes: number
): Promise<Buffer | undefined> {
  if (offset >= totalBytes || limit <= 0) return undefined
  const source = createReadStream(filePath)
  return readStreamWindow(source.pipe(createGunzip()), limit, offset, [source])
}

function readStreamWindow(
  stream: NodeJS.ReadableStream,
  limit: number,
  skipBytes = 0,
  linkedStreams: NodeJS.ReadableStream[] = []
): Promise<Buffer | undefined> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = []
    let skipped = 0
    let collected = 0
    let settled = false

    const cleanup = (): void => {
      stream.off('data', onData)
      stream.off('end', onEnd)
      stream.off('error', onError)
      stream.off('close', onClose)
    }
    const finish = (): void => {
      if (settled) return
      settled = true
      cleanup()
      if ('destroy' in stream && typeof stream.destroy === 'function') {
        stream.destroy()
      }
      for (const linkedStream of linkedStreams) {
        if ('destroy' in linkedStream && typeof linkedStream.destroy === 'function') {
          linkedStream.destroy()
        }
      }
      resolve(chunks.length > 0 ? Buffer.concat(chunks, collected) : undefined)
    }
    const onData = (chunk: Buffer | string): void => {
      let buffer = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
      if (skipBytes > skipped) {
        const remainingSkip = skipBytes - skipped
        if (buffer.byteLength <= remainingSkip) {
          skipped += buffer.byteLength
          return
        }
        buffer = buffer.subarray(remainingSkip)
        skipped = skipBytes
      }
      if (buffer.byteLength === 0) return
      const remaining = limit - collected
      if (remaining <= 0) {
        finish()
        return
      }
      const slice = buffer.byteLength > remaining ? buffer.subarray(0, remaining) : buffer
      chunks.push(slice)
      collected += slice.byteLength
      if (collected >= limit) {
        finish()
      }
    }
    const onEnd = (): void => finish()
    const onClose = (): void => finish()
    const onError = (error: Error): void => {
      if (settled) return
      settled = true
      cleanup()
      reject(error)
    }

    stream.on('data', onData)
    stream.once('end', onEnd)
    stream.once('error', onError)
    stream.once('close', onClose)
  })
}

function bodyDetail(buffer: Buffer | undefined): { bodyText?: string; bodyBase64?: string } {
  if (!buffer) return {}
  return isUtf8Text(buffer)
    ? { bodyText: buffer.toString('utf8') }
    : { bodyBase64: buffer.toString('base64') }
}
