import { createHash } from 'node:crypto'
import type { DatabaseSync } from 'node:sqlite'

import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { appendAuditHotSearchEntries, appendAuditHotSearchEntriesAsync } from './audit-log-hot-search-files.js'
import {
  applyAuditPayloadBlobPersistencePlan,
  auditPayloadBodyDetail,
  cleanupCreatedAuditBlobFiles,
  cleanupCreatedAuditBlobFilesAsync,
  cleanupUnreferencedAuditPayloadBlobs,
  cleanupUnreferencedAuditPayloadBlobsAsync,
  persistAuditPayloadBlob,
  persistAuditPayloadBlobAsync,
  prepareAuditPayloadBlobStatements,
  prepareAuditPayloadBlob,
  prepareAuditPayloadBlobAsync,
  planAuditPayloadBlobPersistence,
  readAuditHeadersBlobDetail,
  readAuditPayloadBlobWindow,
  writeAuditPayloadBlobFileForPlan,
  type AuditPayloadBlobStorageStatus,
  type AuditPayloadBlobPersistencePlan,
  type PreparedAuditPayloadBlob
} from './audit-log-payload-blobs.js'
import {
  auditErrorGroupFromRow,
  auditLogAttemptFromRow,
  auditLogPayloadSummaryFromRow,
  auditLogSummaryFromRow,
  hydrateAuditRows,
  type AuditLogRow
} from './audit-log-mappers.js'
import { chunkValues, pagedTotalUpperBound, sqlPlaceholders, takePageRows } from './query-utils.js'
import { loadAccountNameMap, loadGroupNameMap, loadSystemAccountNameMapByIds } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

export { cleanupUnreferencedAuditPayloadBlobs, cleanupUnreferencedAuditPayloadBlobsAsync } from './audit-log-payload-blobs.js'
export type { AuditPayloadBlobStorageStatus } from './audit-log-payload-blobs.js'

export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'client_aborted'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error' | 'gateway_metadata'
export type AuditPayloadCaptureStatus = 'complete' | 'summary_only' | 'hash_only' | 'expired' | 'overflow' | 'dropped'
export type AuditTrafficSource = 'gateway' | 'manual_account_test' | 'cooldown_retest'

export interface AuditLogPayloadInput {
  id?: string
  attemptTempId?: string
  partType: AuditPayloadPartType
  sequenceIndex?: number
  contentType?: string
  contentEncoding?: string
  headers?: Record<string, string | string[]>
  body?: Buffer | string
  bodySha256?: string
  rawBodySizeBytes?: number
  captureStatus?: AuditPayloadCaptureStatus
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
  trafficSource?: AuditTrafficSource
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
  trafficSource: AuditTrafficSource
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
  headersStorageStatus: AuditPayloadBlobStorageStatus
  bodyStorageStatus: AuditPayloadBlobStorageStatus
  bodyOffset: number
  bodyLimit: number
  bodyBytesReturned: number
  bodyTotalBytes: number
  bodyNextOffset?: number
  bodyTruncated: boolean
}

export interface AuditLogSuccessHotRetentionCleanupResult {
  auditLogs: number
  auditPayloadBlobs: number
}

export interface AuditLogListOptions {
  page?: number
  pageSize?: number
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
  trafficSource?: AuditTrafficSource
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

type AuditLogFilterValue = string | number

interface PreparedAuditPayload {
  id: string
  attemptTempId?: string
  partType: AuditPayloadPartType
  sequenceIndex: number
  contentType?: string
  contentEncoding?: string
  headersBlob?: PreparedAuditPayloadBlob
  bodyBlob?: PreparedAuditPayloadBlob
  headersBlobPlan?: AuditPayloadBlobPersistencePlan
  bodyBlobPlan?: AuditPayloadBlobPersistencePlan
  headersSha256?: string
  bodySha256?: string
  rawSizeBytes: number
  compressedSizeBytes: number
  captureStatus: AuditPayloadCaptureStatus
  createdAt: string
}

type AuditErrorGroupStatement = ReturnType<DatabaseSync['prepare']>

interface AuditErrorGroupStatements {
  selectExisting: AuditErrorGroupStatement
  updateExisting: AuditErrorGroupStatement
  insertGroup: AuditErrorGroupStatement
}

const auditLogDefaultPageSize = 100
const auditLogMaxPageSize = 100
const errorGroupDefaultPageSize = 100
const errorGroupMaxPageSize = 100
const auditLogMaxListWindowRows = 1001
const auditErrorGroupWindowMs = 5 * 60 * 1000
const auditHeadersContentType = 'application/json; audit=headers'

export function createAuditLogsBatch(inputs: AuditLogInput[]): void {
  if (inputs.length === 0) return

  const database = getDatasetDatabase()
  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, group_id, account_id, provider_code, method, path, query_string,
      model, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
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
  const insertedHotSearchLogs: AuditLogInput[] = []
  const existingLogIds = loadExistingAuditLogIds(database, inputs)
  const seenLogIds = new Set<string>()
  const payloadBlobStatements = prepareAuditPayloadBlobStatements(database)
  const errorGroupStatements = prepareAuditErrorGroupStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const input of inputs) {
      const id = input.id ?? newId('audit')
      if (existingLogIds.has(id) || seenLogIds.has(id)) {
        continue
      }
      seenLogIds.add(id)
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
      const rawPayloadBytes = payloads.reduce((sum, payload) => sum + payload.rawSizeBytes, 0)
      const compressedPayloadBytes = payloads.reduce((sum, payload) => sum + payload.compressedSizeBytes, 0)
      const compressionSavedBytes = Math.max(0, rawPayloadBytes - compressedPayloadBytes)
      const errorGroupId = upsertAuditErrorGroup(input, id, payloads, createdAt, errorGroupStatements)

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        normalizeAuditTrafficSource(input.trafficSource),
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
        rawPayloadBytes,
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
      insertedHotSearchLogs.push({ ...input, id, createdAt })

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
        const headersBlobId = persistAuditPayloadBlob(database, payload.headersBlob, createdAt, createdStorageKeys, payloadBlobStatements)
        const bodyBlobId = persistAuditPayloadBlob(database, payload.bodyBlob, createdAt, createdStorageKeys, payloadBlobStatements)
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
    appendAuditHotSearchEntries(insertedHotSearchLogs)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    cleanupCreatedAuditBlobFiles(createdStorageKeys)
    throw error
  }
}

export async function createAuditLogsBatchAsync(inputs: AuditLogInput[]): Promise<void> {
  if (inputs.length === 0) return

  const database = getDatasetDatabase()
  const existingLogIds = loadExistingAuditLogIds(database, inputs)
  const seenLogIds = new Set<string>()
  const preparedLogs: Array<{
    input: AuditLogInput
    id: string
    createdAt: string
    attemptIds: Map<string, string>
    preparedAttempts: Array<AuditLogAttemptInput & { id: string }>
    payloads: PreparedAuditPayload[]
    rawPayloadBytes: number
    compressedPayloadBytes: number
    compressionSavedBytes: number
  }> = []

  for (const input of inputs) {
    const id = input.id ?? newId('audit')
    if (existingLogIds.has(id) || seenLogIds.has(id)) {
      continue
    }
    seenLogIds.add(id)
    const createdAt = input.createdAt ?? nowIso()
    const attemptIds = new Map<string, string>()
    const preparedAttempts = input.attempts.map((attempt) => {
      const attemptId = attempt.id ?? newId('audatt')
      if (attempt.tempId) {
        attemptIds.set(attempt.tempId, attemptId)
      }
      return { ...attempt, id: attemptId }
    })
    const payloads = await Promise.all(input.payloads.map((payload, index) => preparePayloadInputAsync(payload, index, createdAt)))
    const rawPayloadBytes = payloads.reduce((sum, payload) => sum + payload.rawSizeBytes, 0)
    const compressedPayloadBytes = payloads.reduce((sum, payload) => sum + payload.compressedSizeBytes, 0)
    preparedLogs.push({
      input,
      id,
      createdAt,
      attemptIds,
      preparedAttempts,
      payloads,
      rawPayloadBytes,
      compressedPayloadBytes,
      compressionSavedBytes: Math.max(0, rawPayloadBytes - compressedPayloadBytes)
    })
  }
  if (preparedLogs.length === 0) return
  const payloadBlobStatements = prepareAuditPayloadBlobStatements(database)
  const batchBlobPlans = new Map<string, AuditPayloadBlobPersistencePlan>()
  for (const prepared of preparedLogs) {
    for (const payload of prepared.payloads) {
      payload.headersBlobPlan = planAuditPayloadBlobPersistenceForBatch(database, payload.headersBlob, payloadBlobStatements, batchBlobPlans)
      payload.bodyBlobPlan = planAuditPayloadBlobPersistenceForBatch(database, payload.bodyBlob, payloadBlobStatements, batchBlobPlans)
    }
  }

  const plannedStorageKeys = new Set<string>()
  try {
    for (const prepared of preparedLogs) {
      for (const payload of prepared.payloads) {
        await writeAuditPayloadBlobFileForPlan(payload.headersBlob, payload.headersBlobPlan)
        if (payload.headersBlobPlan?.shouldWriteFile && payload.headersBlobPlan.storageKey) {
          plannedStorageKeys.add(payload.headersBlobPlan.storageKey)
        }
        await writeAuditPayloadBlobFileForPlan(payload.bodyBlob, payload.bodyBlobPlan)
        if (payload.bodyBlobPlan?.shouldWriteFile && payload.bodyBlobPlan.storageKey) {
          plannedStorageKeys.add(payload.bodyBlobPlan.storageKey)
        }
      }
    }
  } catch (error) {
    await cleanupCreatedAuditBlobFilesAsync([...plannedStorageKeys])
    throw error
  }

  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, group_id, account_id, provider_code, method, path, query_string,
      model, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
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
  const insertedHotSearchLogs: AuditLogInput[] = []
  const errorGroupStatements = prepareAuditErrorGroupStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  try {
    for (const prepared of preparedLogs) {
      const { input, id, createdAt, attemptIds, preparedAttempts, payloads, rawPayloadBytes, compressedPayloadBytes, compressionSavedBytes } = prepared
      const errorGroupId = upsertAuditErrorGroup(input, id, payloads, createdAt, errorGroupStatements)

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        normalizeAuditTrafficSource(input.trafficSource),
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
        rawPayloadBytes,
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
      insertedHotSearchLogs.push({ ...input, id, createdAt })

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
        const headersBlobId = applyAuditPayloadBlobPersistencePlan(payload.headersBlob, payload.headersBlobPlan, createdAt, createdStorageKeys, payloadBlobStatements)
        const bodyBlobId = applyAuditPayloadBlobPersistencePlan(payload.bodyBlob, payload.bodyBlobPlan, createdAt, createdStorageKeys, payloadBlobStatements)
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
    await appendAuditHotSearchEntriesAsync(insertedHotSearchLogs)
  } catch (error) {
    try {
      rollbackDatabaseTransaction(database, transactionStarted)
    } catch {
    }
    await cleanupCreatedAuditBlobFilesAsync(createdStorageKeys)
    throw error
  }
}

function loadExistingAuditLogIds(database: ReturnType<typeof getDatasetDatabase>, inputs: AuditLogInput[]): Set<string> {
  const ids = [...new Set(inputs.map((input) => input.id).filter((id): id is string => Boolean(id?.trim())))]
  const existingIds = new Set<string>()
  for (const chunk of chunkValues(ids, 900)) {
    const rows = database
      .prepare(`SELECT id FROM audit_logs WHERE id IN (${sqlPlaceholders(chunk.length)})`)
      .all(...chunk) as Array<{ id?: string }>
    for (const row of rows) {
      if (row.id) {
        existingIds.add(row.id)
      }
    }
  }
  return existingIds
}

export function listAuditLogs(options: AuditLogListOptions = {}): AuditLogListResult {
  const filters = buildAuditLogFilters(options)
  const pageSize = normalizePageSize(options.pageSize, auditLogDefaultPageSize, auditLogMaxPageSize)
  const page = normalizePage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const database = getDatasetDatabase()
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
  const systemAccountNames = loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
  const items = rowsWithNames.map((row) => auditLogSummaryFromRow(row, systemAccountNames))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function listAuditLogsByIds(ids: string[]): AuditLogSummary[] {
  const uniqueIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (uniqueIds.length === 0) return []
  const database = getDatasetDatabase()
  const rows: AuditLogRow[] = []
  for (const chunk of chunkValues(uniqueIds, 900)) {
    rows.push(...database
      .prepare(`
        SELECT al.*
        FROM audit_logs al
        WHERE al.id IN (${sqlPlaceholders(chunk.length)})
      `)
      .all(...chunk) as AuditLogRow[])
  }
  const order = new Map(uniqueIds.map((id, index) => [id, index]))
  rows.sort((left, right) => {
    const leftOrder = order.get(String(left.id ?? '')) ?? Number.MAX_SAFE_INTEGER
    const rightOrder = order.get(String(right.id ?? '')) ?? Number.MAX_SAFE_INTEGER
    return leftOrder - rightOrder
  })
  const rowsWithNames = hydrateAuditRows(rows)
  const systemAccountNames = loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
  return rowsWithNames.map((row) => auditLogSummaryFromRow(row, systemAccountNames))
}

export function listAuditErrorGroups(options: AuditErrorGroupListOptions = {}): AuditErrorGroupListResult {
  const filters = buildAuditErrorGroupFilters(options)
  const pageSize = normalizePageSize(options.pageSize, errorGroupDefaultPageSize, errorGroupMaxPageSize)
  const page = normalizePage(options.page, pageSize)
  const offset = (page - 1) * pageSize
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`
      SELECT ${auditErrorGroupListSelectColumns('aeg')}
      FROM audit_error_groups aeg
      ${filters.clause}
      ORDER BY aeg.updated_at DESC, aeg.id DESC
      LIMIT ? OFFSET ?
    `)
    .all(...filters.params, pageSize + 1, offset) as AuditLogRow[]
  const pageRows = takePageRows(rows, pageSize)
  const rowsWithNames = hydrateAuditRows(pageRows.rows)
  const systemAccountNames = loadSystemAccountNameMapByIds(rowsWithNames.map((row) => optionalString(row.system_account_id)))
  const items = rowsWithNames.map((row) => auditErrorGroupFromRow(row, systemAccountNames))
  return {
    items,
    total: pagedTotalUpperBound(page, pageSize, items.length, pageRows.hasMore),
    hasMore: pageRows.hasMore,
    page,
    pageSize
  }
}

export function listAuditErrorGroupEvents(errorGroupId: string, options: AuditLogListOptions = {}): AuditLogListResult {
  return listAuditLogs({ ...options, errorGroupId })
}

export function getAuditLogDetail(id: string): AuditLogDetail | undefined {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT
        al.*
      FROM audit_logs al
      WHERE al.id = ?
    `)
    .get(id) as AuditLogRow | undefined
  if (!row) return undefined
  const namedRow = hydrateAuditRows([row])[0] ?? row

  const attemptRows = getDatasetDatabase()
    .prepare('SELECT * FROM audit_log_attempts WHERE audit_log_id = ? ORDER BY attempt_index ASC, id ASC')
    .all(id) as AuditLogRow[]
  const payloadRows = getAuditPayloadRows(id)
  const systemAccountNames = loadSystemAccountNameMapByIds([optionalString(namedRow.system_account_id)])
  const errorGroupId = optionalString(namedRow.error_group_id)
  const accountNames = loadAccountNameMap(attemptRows.map((attempt) => String(attempt.account_id ?? '')).filter(Boolean))
  const groupNames = loadGroupNameMap(attemptRows.map((attempt) => String(attempt.group_id ?? '')).filter(Boolean))
  return {
    ...auditLogSummaryFromRow(namedRow, systemAccountNames),
    attempts: attemptRows.map((attempt) => auditLogAttemptFromRow(attempt, accountNames, groupNames)),
    errorGroup: errorGroupId ? getAuditErrorGroupById(errorGroupId) : undefined,
    payloads: payloadRows.map(auditLogPayloadSummaryFromRow)
  }
}

function getAuditErrorGroupById(id: string): AuditErrorGroupSummary | undefined {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT
        aeg.*
      FROM audit_error_groups aeg
      WHERE aeg.id = ?
    `)
    .get(id) as AuditLogRow | undefined
  const namedRow = row ? hydrateAuditRows([row])[0] : undefined
  const systemAccountNames = loadSystemAccountNameMapByIds([optionalString(namedRow?.system_account_id)])
  return namedRow ? auditErrorGroupFromRow(namedRow, systemAccountNames) : undefined
}

export async function getAuditLogPayload(
  auditLogId: string,
  payloadId: string,
  options: AuditLogPayloadReadOptions = {}
): Promise<AuditLogPayloadDetail | undefined> {
  const row = getDatasetDatabase()
    .prepare(`
      SELECT *
      FROM audit_payload_refs
      WHERE audit_log_id = ? AND id = ?
    `)
    .get(auditLogId, payloadId) as AuditLogRow | undefined
  if (row) {
    const summary = auditLogPayloadSummaryFromRow(row)
    const headers = await readAuditHeadersBlobDetail(optionalString(row.headers_blob_id))
    const bodyWindow = await readAuditPayloadBlobWindow(optionalString(row.body_blob_id), options)
    return {
      ...summary,
      headers: headers.headers,
      ...auditPayloadBodyDetail(bodyWindow.bytes),
      headersStorageStatus: headers.storageStatus,
      bodyStorageStatus: bodyWindow.storageStatus,
      bodyOffset: bodyWindow.offset,
      bodyLimit: bodyWindow.limit,
      bodyBytesReturned: bodyWindow.bytes?.byteLength ?? 0,
      bodyTotalBytes: bodyWindow.totalBytes,
      bodyNextOffset: bodyWindow.nextOffset,
      bodyTruncated: bodyWindow.truncated
    }
  }
}

export function cleanupAuditLogsBefore(cutoffCreatedAt: string, limit = 1000): number {
  const deleted = deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  cleanupUnreferencedAuditPayloadBlobs(limit)
  return deleted
}

export async function cleanupAuditLogsBeforeAsync(cutoffCreatedAt: string, limit = 1000): Promise<number> {
  const deleted = deleteAuditLogsByWhere('created_at < ?', [cutoffCreatedAt], limit)
  await cleanupUnreferencedAuditPayloadBlobsAsync(limit)
  return deleted
}

export async function cleanupAuditSuccessHotRetentionAsync(input: {
  successHotCutoffCreatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): Promise<AuditLogSuccessHotRetentionCleanupResult> {
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const deletedLogs = deleteAuditLogsByWhere(
    successHotRetentionDeleteWhereClause,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold],
    limit
  )
  const deletedBlobs = await cleanupUnreferencedAuditPayloadBlobsAsync(limit)
  return {
    auditLogs: deletedLogs,
    auditPayloadBlobs: deletedBlobs
  }
}

export function cleanupAuditLogsByRetention(input: {
  successHotCutoffCreatedAt: string
  successCutoffCreatedAt: string
  failureCutoffCreatedAt: string
  errorGroupCutoffUpdatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): number {
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const deletedLogs = deleteAuditLogsByWhere(
    `((${successHotRetentionDeleteWhereClause}) OR (audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold, input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = cleanupUnreferencedAuditPayloadBlobs(limit)
  return deletedLogs + deletedGroups + deletedBlobs
}

export async function cleanupAuditLogsByRetentionAsync(input: {
  successHotCutoffCreatedAt: string
  successCutoffCreatedAt: string
  failureCutoffCreatedAt: string
  errorGroupCutoffUpdatedAt: string
  successSampleBucketThreshold?: number
  limit?: number
}): Promise<number> {
  const limit = input.limit ?? 1000
  const successSampleBucketThreshold = normalizeSuccessSampleBucketThreshold(input.successSampleBucketThreshold)
  const deletedLogs = deleteAuditLogsByWhere(
    `((${successHotRetentionDeleteWhereClause}) OR (audit_outcome = 'success' AND created_at < ?) OR (audit_outcome <> 'success' AND created_at < ?))`,
    [input.successHotCutoffCreatedAt, successSampleBucketThreshold, input.successCutoffCreatedAt, input.failureCutoffCreatedAt],
    limit
  )
  const deletedGroups = cleanupAuditErrorGroupsBefore(input.errorGroupCutoffUpdatedAt, limit)
  const deletedBlobs = await cleanupUnreferencedAuditPayloadBlobsAsync(limit)
  return deletedLogs + deletedGroups + deletedBlobs
}

const successHotRetentionDeleteWhereClause = "audit_outcome = 'success' AND created_at < ? AND sample_bucket >= ? AND sample_reason NOT IN ('full_capture_success', 'targeted_full_capture_success')"

function normalizeSuccessSampleBucketThreshold(value: number | undefined): number {
  if (!Number.isFinite(value)) return 1000
  return Math.min(Math.max(Math.trunc(value ?? 1000), 0), 10000)
}

function deleteAuditLogsByWhere(whereClause: string, params: AuditLogFilterValue[], limit: number): number {
  const database = getDatasetDatabase()
  const rows = database
    .prepare(`SELECT id FROM audit_logs WHERE ${whereClause} ORDER BY created_at ASC, id ASC LIMIT ?`)
    .all(...params, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0

  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_logs WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

function cleanupAuditErrorGroupsBefore(cutoffUpdatedAt: string, limit: number): number {
  const database = getDatasetDatabase()
  const unreferencedGroupWhere = `
    updated_at < ?
    AND NOT EXISTS (
      SELECT 1
      FROM audit_logs
      WHERE audit_logs.error_group_id = audit_error_groups.id
    )
  `
  const rows = database
    .prepare(`SELECT id FROM audit_error_groups WHERE ${unreferencedGroupWhere} ORDER BY updated_at ASC, id ASC LIMIT ?`)
    .all(cutoffUpdatedAt, Math.max(1, Math.trunc(limit))) as AuditLogRow[]
  const ids = rows.map((row) => String(row.id ?? '')).filter(Boolean)
  if (ids.length === 0) return 0
  const placeholders = ids.map(() => '?').join(',')
  const result = database.prepare(`DELETE FROM audit_error_groups WHERE id IN (${placeholders})`).run(...ids)
  return Number(result.changes ?? 0)
}

function preparePayloadInput(payload: AuditLogPayloadInput, fallbackIndex: number, fallbackCreatedAt: string): PreparedAuditPayload {
  const headersBlob = payload.headers
    ? prepareAuditPayloadBlob(Buffer.from(stableJsonStringify(payload.headers), 'utf8'), auditHeadersContentType)
    : undefined
  const bodyBuffer = bodyToBuffer(payload.body)
  const bodyBlob = prepareAuditPayloadBlob(bodyBuffer, payload.contentType, payload.contentEncoding)
  const rawBodySizeBytes = normalizePayloadSizeBytes(payload.rawBodySizeBytes, bodyBlob?.rawSizeBytes ?? 0)
  const rawSizeBytes = (headersBlob?.rawSizeBytes ?? 0) + rawBodySizeBytes
  const compressedSizeBytes = (headersBlob?.compressedSizeBytes ?? 0) + (bodyBlob?.compressedSizeBytes ?? 0)
  const bodySha256 = payload.bodySha256 ?? bodyBlob?.sha256
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
    bodySha256,
    rawSizeBytes,
    compressedSizeBytes,
    captureStatus: payload.captureStatus ?? 'complete',
    createdAt: payload.createdAt ?? fallbackCreatedAt
  }
}

async function preparePayloadInputAsync(payload: AuditLogPayloadInput, fallbackIndex: number, fallbackCreatedAt: string): Promise<PreparedAuditPayload> {
  const headersBlob = payload.headers
    ? await prepareAuditPayloadBlobAsync(Buffer.from(stableJsonStringify(payload.headers), 'utf8'), auditHeadersContentType)
    : undefined
  const bodyBuffer = bodyToBuffer(payload.body)
  const bodyBlob = await prepareAuditPayloadBlobAsync(bodyBuffer, payload.contentType, payload.contentEncoding)
  const rawBodySizeBytes = normalizePayloadSizeBytes(payload.rawBodySizeBytes, bodyBlob?.rawSizeBytes ?? 0)
  const rawSizeBytes = (headersBlob?.rawSizeBytes ?? 0) + rawBodySizeBytes
  const compressedSizeBytes = (headersBlob?.compressedSizeBytes ?? 0) + (bodyBlob?.compressedSizeBytes ?? 0)
  const bodySha256 = payload.bodySha256 ?? bodyBlob?.sha256
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
    bodySha256,
    rawSizeBytes,
    compressedSizeBytes,
    captureStatus: payload.captureStatus ?? 'complete',
    createdAt: payload.createdAt ?? fallbackCreatedAt
  }
}

function planAuditPayloadBlobPersistenceForBatch(
  database: ReturnType<typeof getDatasetDatabase>,
  blob: PreparedAuditPayloadBlob | undefined,
  statements: ReturnType<typeof prepareAuditPayloadBlobStatements>,
  batchPlans: Map<string, AuditPayloadBlobPersistencePlan>
): AuditPayloadBlobPersistencePlan | undefined {
  if (!blob) return undefined
  const key = auditPayloadBlobBatchKey(blob)
  const existingPlan = batchPlans.get(key)
  if (existingPlan) {
    return {
      ...existingPlan,
      existing: true,
      shouldWriteFile: false
    }
  }
  const plan = planAuditPayloadBlobPersistence(database, blob, statements)
  if (plan) {
    batchPlans.set(key, plan)
  }
  return plan
}

function auditPayloadBlobBatchKey(blob: PreparedAuditPayloadBlob): string {
  return `${blob.sha256}\u0000${blob.rawSizeBytes}\u0000${blob.contentType}`
}

function prepareAuditErrorGroupStatements(database: ReturnType<typeof getDatasetDatabase>): AuditErrorGroupStatements {
  return {
    selectExisting: database.prepare('SELECT id FROM audit_error_groups WHERE fingerprint = ? AND window_started_at = ?'),
    updateExisting: database.prepare(`
      UPDATE audit_error_groups
      SET count = count + 1,
          window_ended_at = ?,
          last_event_id = ?,
          sample_event_id = COALESCE(sample_event_id, ?),
          last_message = ?,
          updated_at = ?
      WHERE id = ?
    `),
    insertGroup: database.prepare(`
      INSERT INTO audit_error_groups (
        id, fingerprint, window_started_at, window_ended_at, system_account_id, api_key_id, group_id, account_id,
        provider_code, path, model, status_code, error_phase, error_code, error_type, request_fingerprint,
        error_fingerprint, count, first_event_id, last_event_id, sample_event_id, last_message, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
    `)
  }
}

function upsertAuditErrorGroup(
  input: AuditLogInput,
  auditLogId: string,
  payloads: PreparedAuditPayload[],
  timestamp: string,
  statements: AuditErrorGroupStatements
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
    trafficSource: normalizeAuditTrafficSource(input.trafficSource),
    path: input.path,
    model: input.model ?? '',
    statusCode: input.finalStatusCode ?? '',
    errorPhase: input.errorPhase ?? '',
    errorCode: input.errorCode ?? '',
    requestFingerprint,
    errorFingerprint
  }))
  const existing = statements.selectExisting.get(fingerprint, windowStartedAt) as AuditLogRow | undefined
  const existingId = optionalString(existing?.id)
  if (existingId) {
    statements.updateExisting.run(windowEndedAt, auditLogId, auditLogId, input.errorMessage ?? null, timestamp, existingId)
    return existingId
  }

  const id = newId('audgrp')
  statements.insertGroup.run(
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

function normalizeAuditTrafficSource(value: unknown): AuditTrafficSource {
  if (value === undefined) return 'gateway'
  if (value === 'gateway' || value === 'manual_account_test' || value === 'cooldown_retest') {
    return value
  }
  throw new Error(`非法审计流量来源：${String(value)}`)
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
  return getDatasetDatabase()
    .prepare('SELECT * FROM audit_payload_refs WHERE audit_log_id = ? ORDER BY sequence_index ASC, id ASC')
    .all(auditLogId) as AuditLogRow[]
}

function buildAuditLogFilters(options: AuditLogListOptions): { clause: string; params: AuditLogFilterValue[] } {
  const clauses: string[] = []
  const params: AuditLogFilterValue[] = []

  pushPrefixFilter(clauses, params, 'al.trace_id', options.traceId)
  pushExactFilter(clauses, params, 'al.path', options.path)
  pushExactFilter(clauses, params, 'al.model', options.model)
  pushPrefixFilter(clauses, params, 'al.client_ip', options.clientIp)
  if (options.outcome && options.outcome !== 'all') {
    clauses.push('al.audit_outcome = ?')
    params.push(options.outcome)
  }
  if (isHttpStatusCode(options.statusCode)) {
    clauses.push('al.final_status_code = ?')
    params.push(options.statusCode)
  }
  if (options.trafficSource) {
    clauses.push('al.traffic_source = ?')
    params.push(options.trafficSource)
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

function buildAuditErrorGroupFilters(options: AuditErrorGroupListOptions): { clause: string; params: AuditLogFilterValue[] } {
  const clauses: string[] = []
  const params: AuditLogFilterValue[] = []

  pushExactFilter(clauses, params, 'aeg.path', options.path)
  pushExactFilter(clauses, params, 'aeg.model', options.model)
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

function pushExactFilter(clauses: string[], params: AuditLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} = ?`)
  params.push(text)
}

function pushPrefixFilter(clauses: string[], params: AuditLogFilterValue[], column: string, value?: string): void {
  const text = value?.trim()
  if (!text) return
  clauses.push(`${column} >= ? AND ${column} < ?`)
  params.push(text, `${text}\uffff`)
}

function auditErrorGroupListSelectColumns(alias: string): string {
  return [
    'id',
    'fingerprint',
    'window_started_at',
    'window_ended_at',
    'system_account_id',
    'api_key_id',
    'group_id',
    'account_id',
    'provider_code',
    'path',
    'model',
    'status_code',
    'error_phase',
    'error_code',
    'error_type',
    'request_fingerprint',
    'error_fingerprint',
    'count',
    'first_event_id',
    'last_event_id',
    'sample_event_id',
    'last_message',
    'created_at',
    'updated_at'
  ].map((column) => `${alias}.${column}`).join(', ')
}

function normalizePage(value: unknown, pageSize: number): number {
  const maxPage = Math.max(1, Math.floor((auditLogMaxListWindowRows - 1) / Math.max(1, Math.trunc(pageSize))))
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(maxPage, Math.max(1, value))
    : 1
}

function normalizePageSize(value: unknown, fallback: number, max: number): number {
  return typeof value === 'number' && Number.isInteger(value)
    ? Math.min(max, Math.max(1, value))
    : fallback
}

function bodyToBuffer(body: Buffer | string | undefined): Buffer | undefined {
  if (body === undefined) return undefined
  return Buffer.isBuffer(body) ? body : Buffer.from(body, 'utf8')
}

function normalizePayloadSizeBytes(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value)
    ? Math.max(0, Math.trunc(value))
    : fallback
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

function isHttpStatusCode(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 100 && value <= 599
}
