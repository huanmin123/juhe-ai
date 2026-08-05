import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { appendAuditHotSearchEntries, appendAuditHotSearchEntriesAsync } from './audit-log-hot-search-files.js'
import { auditErrorGroupLockKey, prepareAuditErrorGroupStatements, upsertAuditErrorGroup, upsertAuditErrorGroupAsync } from './audit-log-error-groups.repository.js'
import {
  cleanupCreatedAuditBlobFiles,
  cleanupCreatedAuditBlobFilesAsync,
  incrementAuditPayloadBlobReference,
  persistAuditPayloadBlob,
  prepareAuditPayloadBlobStatements,
  writeAuditPayloadBlobFileForPlan,
  type AuditPayloadBlobPersistencePlan,
  type PreparedAuditPayloadBlob
} from './audit-log-payload-blobs.js'
import {
  preparePayloadInput,
  preparePayloadInputAsync,
  type PreparedAuditPayload
} from './audit-log-payload-input.js'
import { normalizeAuditTrafficSource, normalizePersistedAuditTrafficSource } from './audit-log-traffic-source.js'
import type {
  AuditLogAttemptInput,
  AuditLogInput
} from './audit-log-types.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, databaseTransactionDefinitelyRolledBack, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { resolveCatalogPricingModelAsync } from '../modules/model-pricing/model-catalog.service.js'
import { errorLogFields, logger } from '../shared/logger.js'

const auditPayloadBlobWriteConcurrency = runtimeConfig.background.auditPayloadBlobWriteConcurrency
const auditLogFinalizationConflictClause = `
    ON CONFLICT(id) DO UPDATE SET
      trace_id = excluded.trace_id,
      traffic_source = excluded.traffic_source,
      system_account_id = excluded.system_account_id,
      api_key_id = excluded.api_key_id,
      conversation_key = excluded.conversation_key,
      session_id = excluded.session_id,
      session_client_type = excluded.session_client_type,
      group_id = excluded.group_id,
      account_id = excluded.account_id,
      provider_code = excluded.provider_code,
      method = excluded.method,
      path = excluded.path,
      query_string = excluded.query_string,
      model = excluded.model,
      upstream_model = excluded.upstream_model,
      pricing_model = excluded.pricing_model,
      model_mapping_applied = excluded.model_mapping_applied,
      model_mapping_source = excluded.model_mapping_source,
      source_endpoint_family = excluded.source_endpoint_family,
      upstream_endpoint_family = excluded.upstream_endpoint_family,
      stream = excluded.stream,
      client_ip = excluded.client_ip,
      user_agent = excluded.user_agent,
      audit_outcome = excluded.audit_outcome,
      success = excluded.success,
      final_status_code = excluded.final_status_code,
      error_phase = excluded.error_phase,
      error_code = excluded.error_code,
      error_message = excluded.error_message,
      sample_bucket = excluded.sample_bucket,
      sample_reason = excluded.sample_reason,
      attempt_count = excluded.attempt_count,
      payload_count = excluded.payload_count,
      raw_payload_bytes = excluded.raw_payload_bytes,
      compressed_payload_bytes = excluded.compressed_payload_bytes,
      compression_saved_bytes = excluded.compression_saved_bytes,
      error_group_id = excluded.error_group_id,
      capture_status = excluded.capture_status,
      lifecycle_status = excluded.lifecycle_status,
      started_at = excluded.started_at,
      ended_at = excluded.ended_at,
      duration_ms = excluded.duration_ms,
      http_completed_at = excluded.http_completed_at,
      http_duration_ms = excluded.http_duration_ms,
      first_token_ms = excluded.first_token_ms,
      created_at = excluded.created_at
    WHERE lifecycle_status = 'in_progress'
      AND excluded.lifecycle_status = 'finalized'
`

interface PreparedAuditLogForWrite {
  input: AuditLogInput
  id: string
  createdAt: string
  attemptIds: Map<string, string>
  preparedAttempts: Array<AuditLogAttemptInput & { id: string }>
  payloads: PreparedAuditPayload[]
  rawPayloadBytes: number
  compressedPayloadBytes: number
  compressionSavedBytes: number
}

function filterPersistedAuditLogInputs(inputs: AuditLogInput[]): AuditLogInput[] {
  const persisted: AuditLogInput[] = []
  for (const input of inputs) {
    const trafficSource = normalizePersistedAuditTrafficSource(input.trafficSource, input)
    if (trafficSource) persisted.push({ ...input, trafficSource })
  }
  return persisted
}

export { cleanupUnreferencedAuditPayloadBlobs, cleanupUnreferencedAuditPayloadBlobsAsync } from './audit-log-payload-blobs.js'
export {
  cleanupAuditLogsBefore,
  cleanupAuditLogsBeforeAsync,
  cleanupAuditLogsByRetention,
  cleanupAuditLogsByRetentionAsync,
  cleanupAuditSuccessHotRetentionAsync
} from './audit-log-retention.repository.js'
export {
  getAuditLogDetail,
  getAuditLogDetailAsync,
  getAuditLogPayload,
  listAuditErrorGroupEventsAsync,
  listAuditErrorGroupEvents,
  listAuditErrorGroupsAsync,
  listAuditErrorGroups,
  listAuditLogsAsync,
  listAuditLogs,
  listAuditLogsByIdsAsync,
  listAuditLogsByIds
} from './audit-log-read.repository.js'
export {
  getAuditLogDetailSupplement,
  getAuditLogDetailSupplementAsync
} from './audit-log-detail-supplement.repository.js'
export type { AuditPayloadBlobStorageStatus } from './audit-log-payload-blobs.js'
export type {
  AuditErrorGroupListOptions,
  AuditErrorGroupListResult,
  AuditErrorGroupSummary,
  AuditLogAttemptInput,
  AuditLogAttemptSummary,
  AuditLogDetail,
  AuditLogDetailAttemptSupplement,
  AuditLogDetailPayloadSupplement,
  AuditLogDetailSupplement,
  AuditLogInput,
  AuditLogListItem,
  AuditLogListOptions,
  AuditLogListResult,
  AuditLogPayloadDetail,
  AuditLogPayloadInput,
  AuditLogPayloadReadOptions,
  AuditLogPayloadSummary,
  AuditLogSuccessHotRetentionCleanupResult,
  AuditLogSummary,
  AuditOutcome,
  AuditPayloadCaptureStatus,
  AuditPayloadPartType,
  AuditTrafficSource,
  PersistedAuditTrafficSource
} from './audit-log-types.js'

export function createAuditLogsBatch(inputs: AuditLogInput[]): void {
  const persistedInputs = filterPersistedAuditLogInputs(inputs)
  if (persistedInputs.length === 0) return

  const database = getDatasetDatabase()
  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, conversation_key, session_id, session_client_type,
      group_id, account_id, provider_code, method, path, query_string,
      model, upstream_model, pricing_model, model_mapping_applied, model_mapping_source, source_endpoint_family, upstream_endpoint_family, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
      compressed_payload_bytes, compression_saved_bytes, error_group_id, capture_status, lifecycle_status, started_at, ended_at,
      duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at
    ) VALUES (${sqlPlaceholders(47)})
    ${auditLogFinalizationConflictClause}
  `)
  const insertAttempt = database.prepare(`
    INSERT INTO audit_log_attempts (
      id, audit_log_id, attempt_index, account_id, account_owner_system_account_id, group_id, proxy_url, provider_code,
      attempt_model, attempt_upstream_model, attempt_pricing_model, attempt_model_mapping_applied, attempt_model_mapping_source,
      attempt_source_endpoint_family, attempt_upstream_endpoint_family,
      upstream_method, upstream_url, upstream_status_code, success, error_phase, error_code, error_message,
      started_at, ended_at, duration_ms
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const insertPayloadRef = database.prepare(`
    INSERT INTO audit_payload_refs (
      id, audit_log_id, attempt_id, part_type, sequence_index, content_type, content_encoding, headers_blob_id,
      body_blob_id, headers_sha256, body_sha256, raw_size_bytes, compressed_size_bytes, capture_status, drop_reason, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const updateLogErrorGroup = database.prepare('UPDATE audit_logs SET error_group_id = ? WHERE id = ?')

  const createdStorageKeys: string[] = []
  const unusedCreatedStorageKeys: string[] = []
  const insertedHotSearchLogs: AuditLogInput[] = []
  const existingLogIds = loadExistingAuditLogIds(database, inputs)
  const seenLogIds = new Set<string>()
  const payloadBlobStatements = prepareAuditPayloadBlobStatements(database)
  const errorGroupStatements = prepareAuditErrorGroupStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  let transactionCommitted = false
  try {
    for (const input of persistedInputs) {
      const id = input.id ?? newId('audit')
      if (input.lifecycleStatus === 'in_progress' && (existingLogIds.has(id) || seenLogIds.has(id))) {
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
      const trafficSource = normalizeAuditTrafficSource(input.trafficSource)

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        trafficSource,
        input.systemAccountId ?? null,
        input.apiKeyId ?? null,
        input.conversationKey ?? null,
        input.sessionId ?? null,
        input.sessionClientType ?? null,
        input.groupId ?? null,
        input.accountId ?? null,
        input.providerCode ?? null,
        input.method,
        input.path,
        input.queryString ?? null,
        input.model ?? null,
        input.upstreamModel ?? null,
        input.pricingModel ?? null,
        input.modelMappingApplied ? 1 : 0,
        input.modelMappingSource ?? null,
        input.sourceEndpointFamily ?? null,
        input.upstreamEndpointFamily ?? null,
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
        null,
        input.captureStatus ?? 'complete',
        input.lifecycleStatus ?? 'finalized',
        input.startedAt,
        input.endedAt,
        input.durationMs ?? null,
        input.httpCompletedAt ?? null,
        input.httpDurationMs ?? null,
        input.firstTokenMs ?? null,
        createdAt
      )
      if (Number(insertLogResult.changes ?? 0) === 0) {
        continue
      }
      insertedHotSearchLogs.push({ ...input, id, createdAt })
      const errorGroupId = upsertAuditErrorGroup(input, id, payloads, createdAt, trafficSource, errorGroupStatements)
      if (errorGroupId) updateLogErrorGroup.run(errorGroupId, id)

      const insertedAttemptIds = new Set<string>()
      for (const attempt of preparedAttempts) {
        const result = insertAttempt.run(
          attempt.id,
          id,
          attempt.attemptIndex,
          attempt.accountId ?? null,
          attempt.accountOwnerSystemAccountId ?? null,
          attempt.groupId ?? null,
          attempt.proxyUrl ?? null,
          attempt.providerCode ?? null,
          attempt.model ?? null,
          attempt.upstreamModel ?? null,
          attempt.pricingModel ?? null,
          attempt.modelMappingApplied ? 1 : 0,
          attempt.modelMappingSource ?? null,
          attempt.sourceEndpointFamily ?? null,
          attempt.upstreamEndpointFamily ?? null,
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
        if (Number(result.changes ?? 0) > 0) insertedAttemptIds.add(attempt.id)
      }

      for (const payload of payloads) {
        const headersBlobId = persistAuditPayloadBlob(database, payload.headersBlob, createdAt, createdStorageKeys, payloadBlobStatements)
        const bodyBlobId = persistAuditPayloadBlob(database, payload.bodyBlob, createdAt, createdStorageKeys, payloadBlobStatements)
        const attemptId = payload.attemptTempId ? attemptIds.get(payload.attemptTempId) : undefined
        const result = insertPayloadRef.run(
          payload.id,
          id,
          attemptId && insertedAttemptIds.has(attemptId) ? attemptId : null,
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
          payload.dropReason ?? null,
          payload.createdAt
        )
        if (Number(result.changes ?? 0) > 0) {
          incrementAuditPayloadBlobReference(headersBlobId, payload.createdAt, payloadBlobStatements)
          incrementAuditPayloadBlobReference(bodyBlobId, payload.createdAt, payloadBlobStatements)
        }
      }
    }

    unusedCreatedStorageKeys.push(...deleteUnusedCreatedAuditPayloadBlobsSqlite(database, createdStorageKeys))
    commitDatabaseTransaction(database, transactionStarted)
    transactionCommitted = true
    cleanupCreatedAuditBlobFiles(unusedCreatedStorageKeys)
    appendAuditHotSearchEntries(insertedHotSearchLogs)
  } catch (error) {
    if (!transactionCommitted) {
      try {
        rollbackDatabaseTransaction(database, transactionStarted)
      } catch {
      }
      cleanupCreatedAuditBlobFiles(createdStorageKeys)
    }
    throw error
  }
}

export async function createAuditLogsBatchAsync(inputs: AuditLogInput[]): Promise<void> {
  const persistedInputs = filterPersistedAuditLogInputs(inputs)
  if (persistedInputs.length === 0) return
  if (runtimeConfig.databaseDriver === 'postgres') {
    await createAuditLogsBatchPostgres(persistedInputs)
    return
  }

  const database = getDatasetDatabase()
  const existingLogIds = loadExistingAuditLogIds(database, persistedInputs)
  const seenLogIds = new Set<string>()
  const preparedLogs: PreparedAuditLogForWrite[] = []

  for (const input of persistedInputs) {
    const id = input.id ?? newId('audit')
    if (input.lifecycleStatus === 'in_progress' && (existingLogIds.has(id) || seenLogIds.has(id))) {
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

  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, conversation_key, session_id, session_client_type,
      group_id, account_id, provider_code, method, path, query_string,
      model, upstream_model, pricing_model, model_mapping_applied, model_mapping_source, source_endpoint_family, upstream_endpoint_family, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
      compressed_payload_bytes, compression_saved_bytes, error_group_id, capture_status, lifecycle_status, started_at, ended_at,
      duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at
    ) VALUES (${sqlPlaceholders(47)})
    ${auditLogFinalizationConflictClause}
  `)
  const insertAttempt = database.prepare(`
    INSERT INTO audit_log_attempts (
      id, audit_log_id, attempt_index, account_id, account_owner_system_account_id, group_id, proxy_url, provider_code,
      attempt_model, attempt_upstream_model, attempt_pricing_model, attempt_model_mapping_applied, attempt_model_mapping_source,
      attempt_source_endpoint_family, attempt_upstream_endpoint_family,
      upstream_method, upstream_url, upstream_status_code, success, error_phase, error_code, error_message,
      started_at, ended_at, duration_ms
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const insertPayloadRef = database.prepare(`
    INSERT INTO audit_payload_refs (
      id, audit_log_id, attempt_id, part_type, sequence_index, content_type, content_encoding, headers_blob_id,
      body_blob_id, headers_sha256, body_sha256, raw_size_bytes, compressed_size_bytes, capture_status, drop_reason, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `)
  const updateLogErrorGroup = database.prepare('UPDATE audit_logs SET error_group_id = ? WHERE id = ?')

  const createdStorageKeys: string[] = []
  const unusedCreatedStorageKeys: string[] = []
  const insertedHotSearchLogs: AuditLogInput[] = []
  const errorGroupStatements = prepareAuditErrorGroupStatements(database)
  const transactionStarted = beginDatabaseTransaction(database)
  let transactionCommitted = false
  try {
    for (const prepared of preparedLogs) {
      const { input, id, createdAt, attemptIds, preparedAttempts, payloads, rawPayloadBytes, compressedPayloadBytes, compressionSavedBytes } = prepared
      const trafficSource = normalizeAuditTrafficSource(input.trafficSource)

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        trafficSource,
        input.systemAccountId ?? null,
        input.apiKeyId ?? null,
        input.conversationKey ?? null,
        input.sessionId ?? null,
        input.sessionClientType ?? null,
        input.groupId ?? null,
        input.accountId ?? null,
        input.providerCode ?? null,
        input.method,
        input.path,
        input.queryString ?? null,
        input.model ?? null,
        input.upstreamModel ?? null,
        input.pricingModel ?? null,
        input.modelMappingApplied ? 1 : 0,
        input.modelMappingSource ?? null,
        input.sourceEndpointFamily ?? null,
        input.upstreamEndpointFamily ?? null,
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
        null,
        input.captureStatus ?? 'complete',
        input.lifecycleStatus ?? 'finalized',
        input.startedAt,
        input.endedAt,
        input.durationMs ?? null,
        input.httpCompletedAt ?? null,
        input.httpDurationMs ?? null,
        input.firstTokenMs ?? null,
        createdAt
      )
      if (Number(insertLogResult.changes ?? 0) === 0) {
        continue
      }
      insertedHotSearchLogs.push({ ...input, id, createdAt })
      const errorGroupId = upsertAuditErrorGroup(input, id, payloads, createdAt, trafficSource, errorGroupStatements)
      if (errorGroupId) updateLogErrorGroup.run(errorGroupId, id)

      const insertedAttemptIds = new Set<string>()
      for (const attempt of preparedAttempts) {
        const result = insertAttempt.run(
          attempt.id,
          id,
          attempt.attemptIndex,
          attempt.accountId ?? null,
          attempt.accountOwnerSystemAccountId ?? null,
          attempt.groupId ?? null,
          attempt.proxyUrl ?? null,
          attempt.providerCode ?? null,
          attempt.model ?? null,
          attempt.upstreamModel ?? null,
          attempt.pricingModel ?? null,
          attempt.modelMappingApplied ? 1 : 0,
          attempt.modelMappingSource ?? null,
          attempt.sourceEndpointFamily ?? null,
          attempt.upstreamEndpointFamily ?? null,
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
        if (Number(result.changes ?? 0) > 0) insertedAttemptIds.add(attempt.id)
      }

      for (const payload of payloads) {
        const headersBlobId = persistAuditPayloadBlob(database, payload.headersBlob, createdAt, createdStorageKeys, payloadBlobStatements)
        const bodyBlobId = persistAuditPayloadBlob(database, payload.bodyBlob, createdAt, createdStorageKeys, payloadBlobStatements)
        const attemptId = payload.attemptTempId ? attemptIds.get(payload.attemptTempId) : undefined
        const result = insertPayloadRef.run(
          payload.id,
          id,
          attemptId && insertedAttemptIds.has(attemptId) ? attemptId : null,
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
          payload.dropReason ?? null,
          payload.createdAt
        )
        if (Number(result.changes ?? 0) > 0) {
          incrementAuditPayloadBlobReference(headersBlobId, payload.createdAt, payloadBlobStatements)
          incrementAuditPayloadBlobReference(bodyBlobId, payload.createdAt, payloadBlobStatements)
        }
      }
    }

    unusedCreatedStorageKeys.push(...deleteUnusedCreatedAuditPayloadBlobsSqlite(database, createdStorageKeys))
    commitDatabaseTransaction(database, transactionStarted)
    transactionCommitted = true
    await cleanupCreatedAuditBlobFilesAsync(unusedCreatedStorageKeys).catch((error) => {
      logger.warn(errorLogFields(error, { event: 'audit_payload_sqlite_orphan_file_cleanup_failed' }), 'SQLite 审计 payload 未引用文件清理失败')
    })
    await appendAuditHotSearchEntriesAsync(insertedHotSearchLogs)
  } catch (error) {
    if (!transactionCommitted) {
      try {
        rollbackDatabaseTransaction(database, transactionStarted)
      } catch {
      }
      await cleanupCreatedAuditBlobFilesAsync(createdStorageKeys)
    }
    throw error
  }
}

async function createAuditLogsBatchPostgres(inputs: AuditLogInput[]): Promise<void> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const seenLogIds = new Set<string>()
  const preparedLogs: PreparedAuditLogForWrite[] = []
  const enrichedInputs = await enrichPostgresAuditLogPricing(inputs)

  for (const input of enrichedInputs) {
    const id = input.id ?? newId('audit')
    if (input.lifecycleStatus === 'in_progress' && seenLogIds.has(id)) continue
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

  const createdStorageKeys = new Set<string>()
  const unusedCreatedStorageKeys: string[] = []
  const insertedHotSearchLogs: AuditLogInput[] = []
  let transactionCommitted = false
  try {
    const createdBlobPlans = new Set<AuditPayloadBlobPersistencePlan>()
    await client.transaction(async (tx) => {
      const blobPlans = await planPostgresAuditPayloadBlobPersistence(tx, preparedLogs)
      await persistPostgresAuditPayloadBlobMetadata(tx, preparedLogs, blobPlans, createdBlobPlans)
      // Keep metadata row locks until files and refs are both durable. Retention uses
      // FOR UPDATE SKIP LOCKED, so it cannot delete a reused blob between these steps.
      await writePostgresAuditPayloadBlobFiles(blobPlans, createdBlobPlans, createdStorageKeys)
      reconcilePostgresAuditPayloadCompressionStats(preparedLogs, blobPlans)

      const insertedPreparedLogs: PreparedAuditLogForWrite[] = []
      const orderedPreparedLogs = preparedLogs.slice().sort(comparePreparedAuditLogsById)
      for (const prepared of orderedPreparedLogs) {
        const inserted = await insertPostgresAuditLog(tx, prepared)
        if (!inserted) continue
        insertedPreparedLogs.push(prepared)
      }
      const errorGroupLogs = insertedPreparedLogs
        .map((prepared) => {
          const trafficSource = normalizeAuditTrafficSource(prepared.input.trafficSource)
          return {
            prepared,
            trafficSource,
            lockKey: auditErrorGroupLockKey(prepared.input, prepared.payloads, prepared.createdAt, trafficSource)
          }
        })
        .filter((entry): entry is typeof entry & { lockKey: string } => Boolean(entry.lockKey))
        .sort((left, right) => compareText(left.lockKey, right.lockKey) || comparePreparedAuditLogsById(left.prepared, right.prepared))
      for (const { prepared, trafficSource } of errorGroupLogs) {
        const errorGroupId = await upsertAuditErrorGroupAsync(tx, prepared.input, prepared.id, prepared.payloads, prepared.createdAt, trafficSource)
        if (errorGroupId) {
          await updatePostgresAuditLogErrorGroup(tx, prepared.id, errorGroupId)
        }
      }

      const insertedAttemptOwners = await insertPostgresAuditLogAttemptsBatch(tx, insertedPreparedLogs)
      // PostgreSQL 以 audit_payload_refs 为唯一事实源；兼容字段 ref_count 不在热路径更新，避免公共 headers blob 形成全局行锁热点。
      const referencedPlans = new Set<AuditPayloadBlobPersistencePlan>()
      await insertPostgresAuditPayloadRefsBatch(tx, insertedPreparedLogs, blobPlans, insertedAttemptOwners, referencedPlans)
      unusedCreatedStorageKeys.push(...await deleteUnusedCreatedPostgresAuditPayloadBlobs(tx, createdBlobPlans, referencedPlans))

      for (const prepared of insertedPreparedLogs) {
        insertedHotSearchLogs.push({ ...prepared.input, id: prepared.id, createdAt: prepared.createdAt })
      }
    })
    transactionCommitted = true
    await cleanupCreatedAuditBlobFilesAsync([...new Set(unusedCreatedStorageKeys)]).catch((error) => {
      logger.warn(errorLogFields(error, { event: 'audit_payload_orphan_file_cleanup_failed' }), '审计 payload 并发去重产生的孤立文件清理失败，等待后续维护清理')
    })
    await appendAuditHotSearchEntriesAsync(insertedHotSearchLogs)
  } catch (error) {
    if (!transactionCommitted && createdStorageKeys.size > 0) {
      if (databaseTransactionDefinitelyRolledBack(error)) {
        await cleanupCreatedAuditBlobFilesAsync([...createdStorageKeys]).catch((cleanupError) => {
          logger.warn(errorLogFields(cleanupError, { event: 'audit_payload_rolled_back_file_cleanup_failed' }), '审计 payload 事务已回滚，但新建文件清理失败')
        })
      } else {
        // PostgreSQL 在网络中断等场景下可能已经提交但客户端未收到 COMMIT ACK。
        // 此处保守保留文件，避免误删已提交引用；无引用文件由后续维护清理回收。
        logger.warn({
          event: 'audit_payload_commit_outcome_uncertain_files_retained',
          retainedFileCount: createdStorageKeys.size
        }, '审计 payload 写入失败，保留已创建文件等待引用对账或维护清理')
      }
    }
    throw error
  }
}

function reconcilePostgresAuditPayloadCompressionStats(
  preparedLogs: PreparedAuditLogForWrite[],
  plans: PostgresAuditPayloadBlobPlans
): void {
  for (const prepared of preparedLogs) {
    for (const payload of prepared.payloads) {
      payload.compressedSizeBytes = (postgresAuditPayloadBlobPlan(plans, payload.headersBlob)?.compressedSizeBytes ?? 0)
        + (postgresAuditPayloadBlobPlan(plans, payload.bodyBlob)?.compressedSizeBytes ?? 0)
    }
    prepared.compressedPayloadBytes = prepared.payloads.reduce((sum, payload) => sum + payload.compressedSizeBytes, 0)
    prepared.compressionSavedBytes = Math.max(0, prepared.rawPayloadBytes - prepared.compressedPayloadBytes)
  }
}

function comparePreparedAuditLogsById(left: PreparedAuditLogForWrite, right: PreparedAuditLogForWrite): number {
  return compareText(left.id, right.id)
}

function compareText(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0
}

async function enrichPostgresAuditLogPricing(inputs: AuditLogInput[]): Promise<AuditLogInput[]> {
  const pricingModelCache = new Map<string, Promise<string | undefined>>()
  const resolvePricingModel = async (input: { providerCode: string; model?: string; systemAccountId?: string }): Promise<string | undefined> => {
    const key = [input.providerCode, input.systemAccountId ?? '', input.model ?? ''].join('\u0000')
    let pending = pricingModelCache.get(key)
    if (!pending) {
      pending = resolveCatalogPricingModelAsync(input)
      pricingModelCache.set(key, pending)
    }
    return await pending
  }
  return await Promise.all(inputs.map((input) => enrichSinglePostgresAuditLogPricing(input, resolvePricingModel)))
}

async function enrichSinglePostgresAuditLogPricing(
  input: AuditLogInput,
  resolvePricingModel: (input: { providerCode: string; model?: string; systemAccountId?: string }) => Promise<string | undefined>
): Promise<AuditLogInput> {
  if (input.pricingModel || !input.providerCode) return input
  const upstreamModel = input.upstreamModel?.trim()
  const requestedModel = input.model?.trim()
  const model = upstreamModel || requestedModel
  if (!model) return input
  const systemAccountId = input.attempts.find((attempt) => attempt.accountOwnerSystemAccountId)?.accountOwnerSystemAccountId
    || input.systemAccountId
  try {
    const pricingModel = await resolvePricingModel({
      providerCode: input.providerCode,
      systemAccountId,
      model
    })
    return pricingModel ? { ...input, pricingModel } : input
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'audit_log_pricing_model_enrichment_failed',
      traceId: input.traceId,
      providerCode: input.providerCode,
      model: input.model,
      upstreamModel: input.upstreamModel
    }), '审计日志写入前补齐计价模型失败，保留原始审计记录')
    return input
  }
}

interface PostgresAuditPayloadBlobPlans {
  byBlob: WeakMap<PreparedAuditPayloadBlob, AuditPayloadBlobPersistencePlan>
  writeTasks: Array<{ blob: PreparedAuditPayloadBlob; plan: AuditPayloadBlobPersistencePlan }>
}

async function planPostgresAuditPayloadBlobPersistence(
  client: DatabaseClient,
  preparedLogs: PreparedAuditLogForWrite[]
): Promise<PostgresAuditPayloadBlobPlans> {
  const plans: PostgresAuditPayloadBlobPlans = {
    byBlob: new WeakMap(),
    writeTasks: []
  }
  const blobsByFingerprint = new Map<string, PreparedAuditPayloadBlob[]>()
  for (const prepared of preparedLogs) {
    for (const payload of prepared.payloads) {
      collectPostgresAuditPayloadBlobForPlanning(blobsByFingerprint, payload.headersBlob)
      collectPostgresAuditPayloadBlobForPlanning(blobsByFingerprint, payload.bodyBlob)
    }
  }
  for (const [, blobs] of [...blobsByFingerprint.entries()].sort((left, right) => compareText(left[0], right[0]))) {
    const blob = blobs[0]
    if (!blob) continue
    const plan = await planPostgresAuditPayloadBlob(client, blob)
    for (const matchingBlob of blobs) {
      plans.byBlob.set(matchingBlob, plan)
    }
    if (plan.shouldWriteFile) {
      plans.writeTasks.push({ blob, plan })
    }
  }
  return plans
}

function collectPostgresAuditPayloadBlobForPlanning(
  blobsByFingerprint: Map<string, PreparedAuditPayloadBlob[]>,
  blob: PreparedAuditPayloadBlob | undefined
): void {
  if (!blob) return
  const fingerprint = `${blob.sha256}\u0000${blob.rawSizeBytes}\u0000${blob.contentType}`
  const blobs = blobsByFingerprint.get(fingerprint) ?? []
  blobs.push(blob)
  blobsByFingerprint.set(fingerprint, blobs)
}

async function planPostgresAuditPayloadBlob(
  client: DatabaseClient,
  blob: PreparedAuditPayloadBlob
): Promise<AuditPayloadBlobPersistencePlan> {
  const row = await client.one<{ id?: string; storage_key?: string; compression?: string; compressed_size_bytes?: number }>(`
    SELECT id, storage_key, compression, compressed_size_bytes
    FROM juhe_dataset.audit_payload_blobs
    WHERE sha256 = ? AND raw_size_bytes = ? AND content_type = ?
    LIMIT 1
  `, [blob.sha256, blob.rawSizeBytes, blob.contentType])
  const plan: AuditPayloadBlobPersistencePlan = row?.id
    ? {
        blobId: row.id,
        storageKey: row.storage_key ?? '',
        existing: true,
        shouldWriteFile: Boolean(row.storage_key),
        compression: row.compression === 'gzip' ? 'gzip' : 'none',
        compressedSizeBytes: Math.max(0, Math.trunc(Number(row.compressed_size_bytes ?? 0)))
      }
    : {
        blobId: newId('audblob'),
        storageKey: '',
        existing: false,
        shouldWriteFile: true,
        compression: blob.compression,
        compressedSizeBytes: blob.compressedSizeBytes
      }
  if (!plan.existing) {
    plan.storageKey = storageKeyForPostgresAuditPayloadBlob(plan.blobId, blob.compression)
  }
  return plan
}

async function writePostgresAuditPayloadBlobFiles(
  plans: PostgresAuditPayloadBlobPlans,
  createdPlans: Set<AuditPayloadBlobPersistencePlan>,
  createdStorageKeys: Set<string>
): Promise<void> {
  const tasks = plans.writeTasks
  let cursor = 0
  let firstError: unknown
  const workerCount = Math.min(auditPayloadBlobWriteConcurrency, Math.max(1, tasks.length))
  const workers = Array.from({ length: workerCount }, async () => {
    while (cursor < tasks.length) {
      const task = tasks[cursor]
      cursor += 1
      if (!task) continue
      try {
        await writeAuditPayloadBlobFileForPlan(task.blob, task.plan)
        if (createdPlans.has(task.plan) && task.plan.storageKey) {
          createdStorageKeys.add(task.plan.storageKey)
        }
      } catch (error) {
        firstError ??= error
      }
    }
  })
  await Promise.all(workers)
  if (firstError) throw firstError
}

async function insertPostgresAuditLog(client: DatabaseClient, prepared: PreparedAuditLogForWrite): Promise<boolean> {
  const { input, id, createdAt, preparedAttempts, payloads, rawPayloadBytes, compressedPayloadBytes, compressionSavedBytes } = prepared
  const trafficSource = normalizeAuditTrafficSource(input.trafficSource)
  const result = await client.execute(`
    INSERT INTO juhe_dataset.audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, conversation_key, session_id, session_client_type,
      group_id, account_id, provider_code, method, path, query_string,
      model, upstream_model, pricing_model, model_mapping_applied, model_mapping_source, source_endpoint_family, upstream_endpoint_family, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
      compressed_payload_bytes, compression_saved_bytes, error_group_id, capture_status, lifecycle_status, started_at, ended_at,
      duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at
    ) VALUES (${sqlPlaceholders(47)})
    ${auditLogFinalizationConflictClause}
  `, [
    id,
    input.traceId,
    trafficSource,
    input.systemAccountId ?? null,
    input.apiKeyId ?? null,
    input.conversationKey ?? null,
    input.sessionId ?? null,
    input.sessionClientType ?? null,
    input.groupId ?? null,
    input.accountId ?? null,
    input.providerCode ?? null,
    input.method,
    input.path,
    input.queryString ?? null,
    input.model ?? null,
    input.upstreamModel ?? null,
    input.pricingModel ?? null,
    input.modelMappingApplied ? 1 : 0,
    input.modelMappingSource ?? null,
    input.sourceEndpointFamily ?? null,
    input.upstreamEndpointFamily ?? null,
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
    null,
    input.captureStatus ?? 'complete',
    input.lifecycleStatus ?? 'finalized',
    input.startedAt,
    input.endedAt,
    input.durationMs ?? null,
    input.httpCompletedAt ?? null,
    input.httpDurationMs ?? null,
    input.firstTokenMs ?? null,
    createdAt
  ])
  return result.changes > 0
}

async function updatePostgresAuditLogErrorGroup(client: DatabaseClient, auditLogId: string, errorGroupId: string): Promise<void> {
  await client.execute('UPDATE juhe_dataset.audit_logs SET error_group_id = ? WHERE id = ?', [errorGroupId, auditLogId])
}

async function insertPostgresAuditLogAttemptsBatch(
  client: DatabaseClient,
  preparedLogs: PreparedAuditLogForWrite[]
): Promise<Map<string, string>> {
  const insertedAttemptOwners = new Map<string, string>()
  const attempts = preparedLogs
    .flatMap((prepared) => prepared.preparedAttempts.map((attempt) => ({ prepared, attempt })))
    .sort((left, right) => compareText(left.attempt.id, right.attempt.id) || comparePreparedAuditLogsById(left.prepared, right.prepared))
  for (const { prepared, attempt } of attempts) {
    const inserted = await client.one<{ id?: string }>(`
      INSERT INTO juhe_dataset.audit_log_attempts (
        id, audit_log_id, attempt_index, account_id, account_owner_system_account_id, group_id, proxy_url, provider_code,
        attempt_model, attempt_upstream_model, attempt_pricing_model, attempt_model_mapping_applied, attempt_model_mapping_source,
        attempt_source_endpoint_family, attempt_upstream_endpoint_family,
        upstream_method, upstream_url, upstream_status_code, success, error_phase, error_code, error_message,
        started_at, ended_at, duration_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(id) DO NOTHING
      RETURNING id
    `, [
      attempt.id,
      prepared.id,
      attempt.attemptIndex,
      attempt.accountId ?? null,
      attempt.accountOwnerSystemAccountId ?? null,
      attempt.groupId ?? null,
      attempt.proxyUrl ?? null,
      attempt.providerCode ?? null,
      attempt.model ?? null,
      attempt.upstreamModel ?? null,
      attempt.pricingModel ?? null,
      attempt.modelMappingApplied ? 1 : 0,
      attempt.modelMappingSource ?? null,
      attempt.sourceEndpointFamily ?? null,
      attempt.upstreamEndpointFamily ?? null,
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
    ])
    if (inserted?.id) insertedAttemptOwners.set(inserted.id, prepared.id)
  }
  return insertedAttemptOwners
}

async function insertPostgresAuditPayloadRefsBatch(
  client: DatabaseClient,
  preparedLogs: PreparedAuditLogForWrite[],
  plans: PostgresAuditPayloadBlobPlans,
  insertedAttemptOwners: Map<string, string>,
  referencedPlans: Set<AuditPayloadBlobPersistencePlan>
): Promise<void> {
  const payloadRefs = preparedLogs
    .flatMap((prepared) => prepared.payloads.map((payload) => ({ prepared, payload })))
    .sort((left, right) => compareText(left.payload.id, right.payload.id) || comparePreparedAuditLogsById(left.prepared, right.prepared))
  for (const { prepared, payload } of payloadRefs) {
    const attemptId = payload.attemptTempId ? prepared.attemptIds.get(payload.attemptTempId) : undefined
    const headersBlobId = postgresAuditPayloadBlobPlan(plans, payload.headersBlob)?.blobId ?? null
    const bodyBlobId = postgresAuditPayloadBlobPlan(plans, payload.bodyBlob)?.blobId ?? null
    const inserted = await client.one<{ id?: string }>(`
      INSERT INTO juhe_dataset.audit_payload_refs (
        id, audit_log_id, attempt_id, part_type, sequence_index, content_type, content_encoding, headers_blob_id,
        body_blob_id, headers_sha256, body_sha256, raw_size_bytes, compressed_size_bytes, capture_status, drop_reason, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(id) DO NOTHING
      RETURNING id
    `, [
      payload.id,
      prepared.id,
      attemptId && insertedAttemptOwners.get(attemptId) === prepared.id ? attemptId : null,
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
      payload.dropReason ?? null,
      payload.createdAt
    ])
    if (!inserted?.id) continue
    recordPostgresAuditPayloadBlobReference(referencedPlans, postgresAuditPayloadBlobPlan(plans, payload.headersBlob))
    recordPostgresAuditPayloadBlobReference(referencedPlans, postgresAuditPayloadBlobPlan(plans, payload.bodyBlob))
  }
}

interface PostgresAuditPayloadBlobMetadataEntry {
  blob: PreparedAuditPayloadBlob
  plan: AuditPayloadBlobPersistencePlan
  fingerprint: string
  firstSeenAt: string
  lastSeenAt: string
}

async function persistPostgresAuditPayloadBlobMetadata(
  client: DatabaseClient,
  preparedLogs: PreparedAuditLogForWrite[],
  plans: PostgresAuditPayloadBlobPlans,
  createdPlans: Set<AuditPayloadBlobPersistencePlan>
): Promise<void> {
  const entries = new Map<AuditPayloadBlobPersistencePlan, PostgresAuditPayloadBlobMetadataEntry>()
  for (const prepared of preparedLogs) {
    for (const payload of prepared.payloads) {
      for (const blob of [payload.headersBlob, payload.bodyBlob]) {
        const plan = postgresAuditPayloadBlobPlan(plans, blob)
        if (!blob || !plan) continue
        const existing = entries.get(plan)
        if (existing) {
          if (prepared.createdAt < existing.firstSeenAt) existing.firstSeenAt = prepared.createdAt
          if (prepared.createdAt > existing.lastSeenAt) existing.lastSeenAt = prepared.createdAt
          continue
        }
        entries.set(plan, {
          blob,
          plan,
          fingerprint: `${blob.sha256}\u0000${blob.rawSizeBytes}\u0000${blob.contentType}`,
          firstSeenAt: prepared.createdAt,
          lastSeenAt: prepared.createdAt
        })
      }
    }
  }

  const orderedEntries = [...entries.values()].sort((left, right) => compareText(left.fingerprint, right.fingerprint))
  for (const entry of orderedEntries) {
    const candidateBlobId = entry.plan.existing ? newId('audblob') : entry.plan.blobId
    const candidateStorageKey = entry.plan.existing
      ? storageKeyForPostgresAuditPayloadBlob(candidateBlobId, entry.blob.compression)
      : entry.plan.storageKey
    const resolved = await resolvePostgresAuditPayloadBlobMetadata(client, entry, candidateBlobId, candidateStorageKey)
    if (resolved.created) createdPlans.add(entry.plan)
    entry.plan.blobId = resolved.id
    entry.plan.storageKey = resolved.storageKey
    entry.plan.existing = true
    entry.plan.compression = resolved.compression
    entry.plan.compressedSizeBytes = resolved.compressedSizeBytes
  }
}

interface ResolvedPostgresAuditPayloadBlobMetadata {
  id: string
  storageKey: string
  compression: PreparedAuditPayloadBlob['compression']
  compressedSizeBytes: number
  created: boolean
}

async function resolvePostgresAuditPayloadBlobMetadata(
  client: DatabaseClient,
  entry: PostgresAuditPayloadBlobMetadataEntry,
  candidateBlobId: string,
  candidateStorageKey: string
): Promise<ResolvedPostgresAuditPayloadBlobMetadata> {
  for (let attempt = 0; attempt < 4; attempt += 1) {
    const inserted = await client.one<{ id?: string; storage_key?: string; compression?: string; compressed_size_bytes?: number }>(`
      INSERT INTO juhe_dataset.audit_payload_blobs (
        id, sha256, raw_size_bytes, compressed_size_bytes, content_type, content_encoding, compression,
        storage_key, ref_count, first_seen_at, last_seen_at, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(sha256, raw_size_bytes, content_type) DO NOTHING
      RETURNING id, storage_key, compression, compressed_size_bytes
    `, [
      candidateBlobId,
      entry.blob.sha256,
      entry.blob.rawSizeBytes,
      entry.blob.compressedSizeBytes,
      entry.blob.contentType,
      entry.blob.contentEncoding ?? null,
      entry.blob.compression,
      candidateStorageKey,
      0,
      entry.firstSeenAt,
      entry.lastSeenAt,
      entry.firstSeenAt
    ])
    if (inserted?.id && inserted.storage_key) {
      return resolvedPostgresAuditPayloadBlobMetadata(inserted, true)
    }

    const existing = await client.one<{ id?: string; storage_key?: string; compression?: string; compressed_size_bytes?: number }>(`
      SELECT id, storage_key, compression, compressed_size_bytes
      FROM juhe_dataset.audit_payload_blobs
      WHERE sha256 = ? AND raw_size_bytes = ? AND content_type = ?
      FOR KEY SHARE
    `, [entry.blob.sha256, entry.blob.rawSizeBytes, entry.blob.contentType])
    if (existing?.id && existing.storage_key) {
      return resolvedPostgresAuditPayloadBlobMetadata(existing, false)
    }
  }
  throw new Error(`审计 payload 元数据并发解析失败：${entry.blob.sha256}`)
}

function resolvedPostgresAuditPayloadBlobMetadata(
  row: { id?: string; storage_key?: string; compression?: string; compressed_size_bytes?: number },
  created: boolean
): ResolvedPostgresAuditPayloadBlobMetadata {
  if (!row.id || !row.storage_key) throw new Error('审计 payload 元数据缺少 id 或 storage_key')
  return {
    id: row.id,
    storageKey: row.storage_key,
    compression: row.compression === 'gzip' ? 'gzip' : 'none',
    compressedSizeBytes: Math.max(0, Math.trunc(Number(row.compressed_size_bytes ?? 0))),
    created
  }
}

function recordPostgresAuditPayloadBlobReference(
  referencedPlans: Set<AuditPayloadBlobPersistencePlan>,
  plan: AuditPayloadBlobPersistencePlan | undefined
): void {
  if (plan) referencedPlans.add(plan)
}

async function deleteUnusedCreatedPostgresAuditPayloadBlobs(
  client: DatabaseClient,
  createdPlans: Set<AuditPayloadBlobPersistencePlan>,
  referencedPlans: Set<AuditPayloadBlobPersistencePlan>
): Promise<string[]> {
  const ids = [...createdPlans]
    .filter((plan) => !referencedPlans.has(plan))
    .map((plan) => plan.blobId)
    .sort(compareText)
  if (ids.length === 0) return []
  const rows = await client.query<{ storage_key?: string }>(`
    DELETE FROM juhe_dataset.audit_payload_blobs b
    WHERE b.id = ANY(?::text[])
      AND b.ref_count = 0
      AND NOT EXISTS (
        SELECT 1
        FROM juhe_dataset.audit_payload_refs r
        WHERE r.headers_blob_id = b.id OR r.body_blob_id = b.id
      )
    RETURNING b.storage_key
  `, [ids])
  return rows.map((row) => row.storage_key?.trim()).filter((value): value is string => Boolean(value))
}

function postgresAuditPayloadBlobPlan(
  plans: PostgresAuditPayloadBlobPlans,
  blob: PreparedAuditPayloadBlob | undefined
): AuditPayloadBlobPersistencePlan | undefined {
  return blob ? plans.byBlob.get(blob) : undefined
}

function storageKeyForPostgresAuditPayloadBlob(id: string, compression: PreparedAuditPayloadBlob['compression']): string {
  const suffix = compression === 'gzip' ? 'gz' : 'blob'
  return `${id.slice(0, 2)}/${id}.${suffix}`
}

function deleteUnusedCreatedAuditPayloadBlobsSqlite(
  database: ReturnType<typeof getDatasetDatabase>,
  storageKeys: string[]
): string[] {
  const deletedStorageKeys: string[] = []
  for (const chunk of chunkValues([...new Set(storageKeys)], 900)) {
    if (chunk.length === 0) continue
    const rows = database.prepare(`
      DELETE FROM audit_payload_blobs
      WHERE storage_key IN (${sqlPlaceholders(chunk.length)})
        AND NOT EXISTS (
          SELECT 1
          FROM audit_payload_refs refs
          WHERE refs.headers_blob_id = audit_payload_blobs.id OR refs.body_blob_id = audit_payload_blobs.id
        )
      RETURNING storage_key
    `).all(...chunk) as Array<{ storage_key?: string }>
    for (const row of rows) {
      if (row.storage_key) deletedStorageKeys.push(row.storage_key)
    }
  }
  return deletedStorageKeys
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
