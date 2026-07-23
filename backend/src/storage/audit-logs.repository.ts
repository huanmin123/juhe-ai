import { beginDatabaseTransaction, commitDatabaseTransaction, getDatasetDatabase, newId, nowIso, rollbackDatabaseTransaction } from './database.js'
import { appendAuditHotSearchEntries, appendAuditHotSearchEntriesAsync } from './audit-log-hot-search-files.js'
import { prepareAuditErrorGroupStatements, upsertAuditErrorGroup, upsertAuditErrorGroupAsync } from './audit-log-error-groups.repository.js'
import {
  applyAuditPayloadBlobPersistencePlan,
  cleanupCreatedAuditBlobFiles,
  cleanupCreatedAuditBlobFilesAsync,
  persistAuditPayloadBlob,
  prepareAuditPayloadBlobStatements,
  writeAuditPayloadBlobFileForPlan,
  type AuditPayloadBlobPersistencePlan,
  type PreparedAuditPayloadBlob
} from './audit-log-payload-blobs.js'
import {
  planAuditPayloadBlobPersistenceForBatch,
  preparePayloadInput,
  preparePayloadInputAsync,
  type PreparedAuditPayload
} from './audit-log-payload-input.js'
import { normalizeAuditTrafficSource } from './audit-log-traffic-source.js'
import type {
  AuditLogAttemptInput,
  AuditLogInput
} from './audit-log-types.js'
import { chunkValues, sqlPlaceholders } from './query-utils.js'
import { runtimeConfig } from '../config/runtime.js'
import { createPostgresDatabaseClient, type DatabaseClient } from './database-client.js'
import { getPostgresPool } from './postgres-client.js'
import { resolveCatalogPricingModelAsync } from '../modules/model-pricing/model-catalog.service.js'
import { errorLogFields, logger } from '../shared/logger.js'

const auditPayloadBlobWriteConcurrency = 4

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

interface AuditPayloadBlobWriteTask {
  blob: PreparedAuditPayloadBlob | undefined
  plan: AuditPayloadBlobPersistencePlan | undefined
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
export type { AuditPayloadBlobStorageStatus } from './audit-log-payload-blobs.js'
export type {
  AuditErrorGroupListOptions,
  AuditErrorGroupListResult,
  AuditErrorGroupSummary,
  AuditLogAttemptInput,
  AuditLogAttemptSummary,
  AuditLogDetail,
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
  AuditTrafficSource
} from './audit-log-types.js'

export function createAuditLogsBatch(inputs: AuditLogInput[]): void {
  if (inputs.length === 0) return

  const database = getDatasetDatabase()
  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, group_id, account_id, provider_code, method, path, query_string,
      model, upstream_model, pricing_model, model_mapping_applied, model_mapping_source, source_endpoint_family, upstream_endpoint_family, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
      compressed_payload_bytes, compression_saved_bytes, error_group_id, capture_status, started_at, ended_at,
      duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
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
      const trafficSource = normalizeAuditTrafficSource(input.trafficSource)
      const errorGroupId = upsertAuditErrorGroup(input, id, payloads, createdAt, trafficSource, errorGroupStatements)

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        trafficSource,
        input.systemAccountId ?? null,
        input.apiKeyId ?? null,
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
        errorGroupId,
        input.captureStatus ?? 'complete',
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
  if (runtimeConfig.databaseDriver === 'postgres') {
    await createAuditLogsBatchPostgres(inputs)
    return
  }

  const database = getDatasetDatabase()
  const existingLogIds = loadExistingAuditLogIds(database, inputs)
  const seenLogIds = new Set<string>()
  const preparedLogs: PreparedAuditLogForWrite[] = []

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
    await writeAuditPayloadBlobFilesForPreparedLogs(preparedLogs, plannedStorageKeys)
  } catch (error) {
    await cleanupCreatedAuditBlobFilesAsync([...plannedStorageKeys])
    throw error
  }

  const insertLog = database.prepare(`
    INSERT INTO audit_logs (
      id, trace_id, traffic_source, system_account_id, api_key_id, group_id, account_id, provider_code, method, path, query_string,
      model, upstream_model, pricing_model, model_mapping_applied, model_mapping_source, source_endpoint_family, upstream_endpoint_family, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
      compressed_payload_bytes, compression_saved_bytes, error_group_id, capture_status, started_at, ended_at,
      duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
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
      const trafficSource = normalizeAuditTrafficSource(input.trafficSource)
      const errorGroupId = upsertAuditErrorGroup(input, id, payloads, createdAt, trafficSource, errorGroupStatements)

      const insertLogResult = insertLog.run(
        id,
        input.traceId,
        trafficSource,
        input.systemAccountId ?? null,
        input.apiKeyId ?? null,
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
        errorGroupId,
        input.captureStatus ?? 'complete',
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

async function createAuditLogsBatchPostgres(inputs: AuditLogInput[]): Promise<void> {
  const client = createPostgresDatabaseClient(await getPostgresPool())
  const seenLogIds = new Set<string>()
  const preparedLogs: PreparedAuditLogForWrite[] = []
  const enrichedInputs = await enrichPostgresAuditLogPricing(inputs)

  for (const input of enrichedInputs) {
    const id = input.id ?? newId('audit')
    if (seenLogIds.has(id)) continue
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

  const plannedStorageKeys = new Set<string>()
  const blobPlans = await planPostgresAuditPayloadBlobPersistence(client, preparedLogs)
  try {
    await writePostgresAuditPayloadBlobFiles(blobPlans, plannedStorageKeys)
  } catch (error) {
    await cleanupCreatedAuditBlobFilesAsync([...plannedStorageKeys])
    throw error
  }

  const createdStorageKeys: string[] = []
  const insertedHotSearchLogs: AuditLogInput[] = []
  try {
    await client.transaction(async (tx) => {
      for (const prepared of preparedLogs) {
        const inserted = await insertPostgresAuditLog(tx, prepared)
        if (!inserted) continue
        const trafficSource = normalizeAuditTrafficSource(prepared.input.trafficSource)
        const errorGroupId = await upsertAuditErrorGroupAsync(tx, prepared.input, prepared.id, prepared.payloads, prepared.createdAt, trafficSource)
        if (errorGroupId) {
          await updatePostgresAuditLogErrorGroup(tx, prepared.id, errorGroupId)
        }
        insertedHotSearchLogs.push({ ...prepared.input, id: prepared.id, createdAt: prepared.createdAt })
        await insertPostgresAuditLogAttempts(tx, prepared)
        await insertPostgresAuditPayloadRefs(tx, prepared, blobPlans, createdStorageKeys)
      }
    })
    await appendAuditHotSearchEntriesAsync(insertedHotSearchLogs)
  } catch (error) {
    await cleanupCreatedAuditBlobFilesAsync([...new Set([...createdStorageKeys, ...plannedStorageKeys])])
    throw error
  }
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
  const plannedByFingerprint = new Map<string, AuditPayloadBlobPersistencePlan>()
  for (const prepared of preparedLogs) {
    for (const payload of prepared.payloads) {
      await planPostgresAuditPayloadBlob(client, payload.headersBlob, plans, plannedByFingerprint)
      await planPostgresAuditPayloadBlob(client, payload.bodyBlob, plans, plannedByFingerprint)
    }
  }
  return plans
}

async function planPostgresAuditPayloadBlob(
  client: DatabaseClient,
  blob: PreparedAuditPayloadBlob | undefined,
  plans: PostgresAuditPayloadBlobPlans,
  plannedByFingerprint: Map<string, AuditPayloadBlobPersistencePlan>
): Promise<void> {
  if (!blob) return
  const fingerprint = `${blob.sha256}\u0000${blob.rawSizeBytes}\u0000${blob.contentType}`
  const existingPlan = plannedByFingerprint.get(fingerprint)
  if (existingPlan) {
    plans.byBlob.set(blob, existingPlan)
    return
  }
  const row = await client.one<{ id?: string; storage_key?: string }>(`
    SELECT id, storage_key
    FROM juhe_dataset.audit_payload_blobs
    WHERE sha256 = ? AND raw_size_bytes = ? AND content_type = ?
    LIMIT 1
  `, [blob.sha256, blob.rawSizeBytes, blob.contentType])
  const plan = row?.id
    ? {
        blobId: row.id,
        storageKey: row.storage_key ?? '',
        existing: true,
        shouldWriteFile: Boolean(row.storage_key)
      }
    : {
        blobId: newId('audblob'),
        storageKey: '',
        existing: false,
        shouldWriteFile: true
      }
  if (!plan.existing) {
    plan.storageKey = storageKeyForPostgresAuditPayloadBlob(plan.blobId, blob.compression)
  }
  plannedByFingerprint.set(fingerprint, plan)
  plans.byBlob.set(blob, plan)
  if (plan.shouldWriteFile) {
    plans.writeTasks.push({ blob, plan })
  }
}

async function writePostgresAuditPayloadBlobFiles(
  plans: PostgresAuditPayloadBlobPlans,
  plannedStorageKeys: Set<string>
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
        if (!task.plan.existing && task.plan.shouldWriteFile && task.plan.storageKey) {
          plannedStorageKeys.add(task.plan.storageKey)
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
      id, trace_id, traffic_source, system_account_id, api_key_id, group_id, account_id, provider_code, method, path, query_string,
      model, upstream_model, pricing_model, model_mapping_applied, model_mapping_source, source_endpoint_family, upstream_endpoint_family, stream, client_ip, user_agent, audit_outcome, success, final_status_code, error_phase, error_code,
      error_message, sample_bucket, sample_reason, attempt_count, payload_count, raw_payload_bytes,
      compressed_payload_bytes, compression_saved_bytes, error_group_id, capture_status, started_at, ended_at,
      duration_ms, http_completed_at, http_duration_ms, first_token_ms, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(id) DO NOTHING
  `, [
    id,
    input.traceId,
    trafficSource,
    input.systemAccountId ?? null,
    input.apiKeyId ?? null,
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

async function insertPostgresAuditLogAttempts(client: DatabaseClient, prepared: PreparedAuditLogForWrite): Promise<void> {
  for (const attempt of prepared.preparedAttempts) {
    await client.execute(`
      INSERT INTO juhe_dataset.audit_log_attempts (
        id, audit_log_id, attempt_index, account_id, account_owner_system_account_id, group_id, proxy_url, provider_code,
        attempt_model, attempt_upstream_model, attempt_pricing_model, attempt_model_mapping_applied, attempt_model_mapping_source,
        attempt_source_endpoint_family, attempt_upstream_endpoint_family,
        upstream_method, upstream_url, upstream_status_code, success, error_phase, error_code, error_message,
        started_at, ended_at, duration_ms
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(id) DO NOTHING
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
  }
}

async function insertPostgresAuditPayloadRefs(
  client: DatabaseClient,
  prepared: PreparedAuditLogForWrite,
  plans: PostgresAuditPayloadBlobPlans,
  createdStorageKeys: string[]
): Promise<void> {
  for (const payload of prepared.payloads) {
    const headersBlobId = await applyPostgresAuditPayloadBlobPersistencePlan(client, payload.headersBlob, postgresAuditPayloadBlobPlan(plans, payload.headersBlob), prepared.createdAt, createdStorageKeys)
    const bodyBlobId = await applyPostgresAuditPayloadBlobPersistencePlan(client, payload.bodyBlob, postgresAuditPayloadBlobPlan(plans, payload.bodyBlob), prepared.createdAt, createdStorageKeys)
    await client.execute(`
      INSERT INTO juhe_dataset.audit_payload_refs (
        id, audit_log_id, attempt_id, part_type, sequence_index, content_type, content_encoding, headers_blob_id,
        body_blob_id, headers_sha256, body_sha256, raw_size_bytes, compressed_size_bytes, capture_status, created_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
      ON CONFLICT(id) DO NOTHING
    `, [
      payload.id,
      prepared.id,
      payload.attemptTempId ? prepared.attemptIds.get(payload.attemptTempId) ?? null : null,
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
    ])
  }
}

function postgresAuditPayloadBlobPlan(
  plans: PostgresAuditPayloadBlobPlans,
  blob: PreparedAuditPayloadBlob | undefined
): AuditPayloadBlobPersistencePlan | undefined {
  return blob ? plans.byBlob.get(blob) : undefined
}

async function applyPostgresAuditPayloadBlobPersistencePlan(
  client: DatabaseClient,
  blob: PreparedAuditPayloadBlob | undefined,
  plan: AuditPayloadBlobPersistencePlan | undefined,
  timestamp: string,
  createdStorageKeys: string[]
): Promise<string | null> {
  if (!blob || !plan) return null
  if (plan.existing) {
    await client.execute('UPDATE juhe_dataset.audit_payload_blobs SET ref_count = ref_count + 1, last_seen_at = ? WHERE id = ?', [timestamp, plan.blobId])
    return plan.blobId
  }
  const row = await client.one<{ id?: string }>(`
    INSERT INTO juhe_dataset.audit_payload_blobs (
      id, sha256, raw_size_bytes, compressed_size_bytes, content_type, content_encoding, compression,
      storage_key, ref_count, first_seen_at, last_seen_at, created_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
    ON CONFLICT(sha256, raw_size_bytes, content_type) DO UPDATE SET
      ref_count = audit_payload_blobs.ref_count + 1,
      last_seen_at = EXCLUDED.last_seen_at
    RETURNING id
  `, [
    plan.blobId,
    blob.sha256,
    blob.rawSizeBytes,
    blob.compressedSizeBytes,
    blob.contentType,
    blob.contentEncoding ?? null,
    blob.compression,
    plan.storageKey,
    timestamp,
    timestamp,
    timestamp
  ])
  const resolvedId = row?.id ?? plan.blobId
  if (resolvedId === plan.blobId) {
    createdStorageKeys.push(plan.storageKey)
  }
  plan.blobId = resolvedId
  plan.existing = true
  return resolvedId
}

function storageKeyForPostgresAuditPayloadBlob(id: string, compression: PreparedAuditPayloadBlob['compression']): string {
  const suffix = compression === 'gzip' ? 'gz' : 'blob'
  return `${id.slice(0, 2)}/${id}.${suffix}`
}

async function writeAuditPayloadBlobFilesForPreparedLogs(
  preparedLogs: PreparedAuditLogForWrite[],
  plannedStorageKeys: Set<string>
): Promise<void> {
  const tasks: AuditPayloadBlobWriteTask[] = []
  for (const prepared of preparedLogs) {
    for (const payload of prepared.payloads) {
      tasks.push({ blob: payload.headersBlob, plan: payload.headersBlobPlan })
      tasks.push({ blob: payload.bodyBlob, plan: payload.bodyBlobPlan })
    }
  }
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
        if (task.plan?.shouldWriteFile && task.plan.storageKey) {
          plannedStorageKeys.add(task.plan.storageKey)
        }
      } catch (error) {
        firstError ??= error
      }
    }
  })
  await Promise.all(workers)
  if (firstError) {
    throw firstError
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
