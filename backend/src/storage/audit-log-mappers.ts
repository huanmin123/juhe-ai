import type {
  AuditErrorGroupSummary,
  AuditLogAttemptSummary,
  AuditLogPayloadSummary,
  AuditLogSummary,
  AuditOutcome,
  AuditPayloadCaptureStatus,
  AuditPayloadPartType
} from './audit-log-types.js'
import { sanitizeUrlCredentialsForLog } from '../shared/request-context.js'
import { loadAccountNameMap, loadApiKeyNameMap, loadGroupNameMap } from './repository-lookups.js'
import { optionalString } from './value-utils.js'

export type AuditLogRow = Record<string, unknown>

export function hydrateAuditRows(rows: AuditLogRow[]): AuditLogRow[] {
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

export function auditLogSummaryFromRow(row: AuditLogRow, systemAccountNames: Map<string, string>): AuditLogSummary {
  const systemAccountId = optionalString(row.system_account_id)
  const rawPayloadBytes = Number(row.raw_payload_bytes ?? 0)
  const compressedPayloadBytes = Number(row.compressed_payload_bytes ?? rawPayloadBytes)
  return {
    id: String(row.id),
    traceId: String(row.trace_id),
    trafficSource: auditTrafficSource(row.traffic_source),
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
    upstreamModel: optionalString(row.upstream_model),
    pricingModel: optionalString(row.pricing_model),
    modelMappingApplied: row.model_mapping_applied === 1,
    modelMappingSource: optionalString(row.model_mapping_source),
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

function auditTrafficSource(value: unknown): AuditLogSummary['trafficSource'] {
  if (
    value === 'gateway'
    || value === 'manual_account_test'
    || value === 'cooldown_retest'
    || value === 'hybrid_scoring'
    || value === 'hybrid_quality_scoring'
  ) {
    return value
  }
  throw new Error(`非法审计流量来源：${String(value)}`)
}

export function auditErrorGroupFromRow(row: AuditLogRow, systemAccountNames: Map<string, string>): AuditErrorGroupSummary {
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

export function auditLogAttemptFromRow(
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
    proxyUrl: sanitizeUrlCredentialsForLog(optionalString(row.proxy_url)),
    providerCode: optionalString(row.provider_code),
    upstreamMethod: String(row.upstream_method),
    upstreamUrl: sanitizeUrlCredentialsForLog(String(row.upstream_url)) ?? String(row.upstream_url),
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

export function auditLogPayloadSummaryFromRow(row: AuditLogRow): AuditLogPayloadSummary {
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

function numberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}
