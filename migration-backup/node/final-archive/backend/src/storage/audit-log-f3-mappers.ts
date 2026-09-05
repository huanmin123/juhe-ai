import { sanitizeUrlCredentialsForLog } from '../shared/request-context.js'
import type { AuditErrorGroupSummary, AuditLogAttemptSummary, AuditLogDetail, AuditLogListItem, AuditLogPayloadSummary, AuditLogRow, AuditOutcome, PersistedAuditTrafficSource, AuditPayloadCaptureStatus, AuditPayloadDropReason, AuditPayloadPartType } from './audit-log-f3-types.js'
export type { AuditLogRow } from './audit-log-f3-types.js'

const text = (v: unknown): string | undefined => typeof v === 'string' && v.trim() ? v : undefined
const num = (v: unknown): number | undefined => typeof v === 'number' && Number.isFinite(v) ? v : undefined
const source = (v: unknown): PersistedAuditTrafficSource => {
  if (v === 'gateway' || v === 'manual_account_test' || v === 'hybrid_scoring' || v === 'hybrid_quality_scoring') return v
  throw new Error(`F3 审计日志包含不可持久化来源：${String(v)}`)
}
const bool = (v: unknown): boolean => v === true || v === 1 || v === '1'

export function listItem(row: AuditLogRow): AuditLogListItem {
  return { id: String(row.id), traceId: String(row.trace_id), sessionId: text(row.session_id), sessionClientType: text(row.session_client_type), trafficSource: source(row.traffic_source), systemAccountId: text(row.system_account_id), systemAccountName: text(row.system_account_name), apiKeyId: text(row.api_key_id), apiKeyName: text(row.api_key_name), groupId: text(row.group_id), groupName: text(row.group_name), accountId: text(row.account_id), accountName: text(row.account_name), method: String(row.method), path: String(row.path), model: text(row.model), upstreamModel: text(row.upstream_model), modelMappingApplied: bool(row.model_mapping_applied), stream: bool(row.stream), auditOutcome: String(row.audit_outcome) as AuditOutcome, success: bool(row.success), finalStatusCode: num(row.final_status_code), lifecycleStatus: (text(row.lifecycle_status) ?? 'finalized') as 'in_progress' | 'finalized', durationMs: num(row.duration_ms), httpDurationMs: num(row.http_duration_ms), createdAt: String(row.created_at) }
}
export function detail(row: AuditLogRow, attempts: AuditLogAttemptSummary[], payloads: AuditLogPayloadSummary[], errorGroup?: AuditErrorGroupSummary): AuditLogDetail {
  const raw = Number(row.raw_payload_bytes ?? 0); const compressed = Number(row.compressed_payload_bytes ?? raw)
  return { ...listItem(row), conversationKey: text(row.conversation_key), queryString: text(row.query_string), errorMessage: text(row.error_message), sampleBucket: Number(row.sample_bucket ?? 0), sampleReason: String(row.sample_reason ?? ''), attemptCount: Number(row.attempt_count ?? attempts.length), payloadCount: Number(row.payload_count ?? payloads.length), rawPayloadBytes: raw, compressedPayloadBytes: compressed, compressionSavedBytes: Number(row.compression_saved_bytes ?? Math.max(0, raw - compressed)), errorGroupId: text(row.error_group_id), captureStatus: String(row.capture_status ?? ''), startedAt: String(row.started_at), endedAt: String(row.ended_at), httpCompletedAt: text(row.http_completed_at), firstTokenMs: num(row.first_token_ms), attempts, payloads, errorGroup }
}
export function attempt(row: AuditLogRow): AuditLogAttemptSummary {
  const accountId = text(row.account_id); const groupId = text(row.group_id)
  return { id: String(row.id), attemptIndex: Number(row.attempt_index ?? 0), accountId, accountName: text(row.account_name), accountOwnerSystemAccountId: text(row.account_owner_system_account_id), groupId, groupName: text(row.group_name), proxyUrl: sanitizeUrlCredentialsForLog(text(row.proxy_url)), providerCode: text(row.provider_code), model: text(row.attempt_model), upstreamModel: text(row.attempt_upstream_model), pricingModel: text(row.attempt_pricing_model), modelMappingApplied: bool(row.attempt_model_mapping_applied), modelMappingSource: text(row.attempt_model_mapping_source), sourceEndpointFamily: text(row.attempt_source_endpoint_family), upstreamEndpointFamily: text(row.attempt_upstream_endpoint_family), upstreamMethod: String(row.upstream_method), upstreamUrl: sanitizeUrlCredentialsForLog(String(row.upstream_url)) ?? String(row.upstream_url), upstreamStatusCode: num(row.upstream_status_code), success: bool(row.success), errorPhase: text(row.error_phase), errorCode: text(row.error_code), errorMessage: text(row.error_message), startedAt: String(row.started_at), endedAt: text(row.ended_at), durationMs: num(row.duration_ms) }
}
export function payload(row: AuditLogRow): AuditLogPayloadSummary {
  const size = Number(row.raw_size_bytes ?? 0)
  return { id: String(row.id), attemptId: text(row.attempt_id), partType: String(row.part_type) as AuditPayloadPartType, sequenceIndex: Number(row.sequence_index ?? 0), contentType: text(row.content_type), contentEncoding: text(row.content_encoding), headersSha256: text(row.headers_sha256), bodySha256: text(row.body_sha256), sizeBytes: size, compressedSizeBytes: Number(row.compressed_size_bytes ?? size), captureStatus: String(row.capture_status ?? 'complete') as AuditPayloadCaptureStatus, dropReason: text(row.drop_reason) as AuditPayloadDropReason | undefined, createdAt: String(row.created_at), hasHeaders: Boolean(text(row.headers_blob_id)), hasBody: Boolean(text(row.body_blob_id)) }
}
export function errorGroup(row: AuditLogRow): AuditErrorGroupSummary {
  return { id: String(row.id), fingerprint: String(row.fingerprint), windowStartedAt: String(row.window_started_at), windowEndedAt: String(row.window_ended_at), systemAccountId: text(row.system_account_id), systemAccountName: text(row.system_account_name), apiKeyId: text(row.api_key_id), apiKeyName: text(row.api_key_name), groupId: text(row.group_id), groupName: text(row.group_name), accountId: text(row.account_id), accountName: text(row.account_name), providerCode: text(row.provider_code), path: text(row.path), model: text(row.model), statusCode: num(row.status_code), errorPhase: text(row.error_phase), errorCode: text(row.error_code), errorType: text(row.error_type), requestFingerprint: text(row.request_fingerprint), errorFingerprint: text(row.error_fingerprint), count: Number(row.count ?? 0), firstEventId: text(row.first_event_id), lastEventId: text(row.last_event_id), sampleEventId: text(row.sample_event_id), lastMessage: text(row.last_message), createdAt: String(row.created_at), updatedAt: String(row.updated_at) }
}
export const auditLogListItemFromRow = (row: AuditLogRow, _names?: Map<string, string>) => listItem(row)
export const auditLogSummaryFromRow = (row: AuditLogRow, _names?: Map<string, string>) => {
  const rawPayloadBytes = Number(row.raw_payload_bytes ?? 0)
  const compressedPayloadBytes = Number(row.compressed_payload_bytes ?? rawPayloadBytes)
  return {
    ...listItem(row),
    queryString: text(row.query_string),
    errorMessage: text(row.error_message),
    sampleBucket: Number(row.sample_bucket ?? 0),
    sampleReason: String(row.sample_reason ?? ''),
    attemptCount: Number(row.attempt_count ?? 0),
    payloadCount: Number(row.payload_count ?? 0),
    rawPayloadBytes,
    compressedPayloadBytes,
    compressionSavedBytes: Number(row.compression_saved_bytes ?? Math.max(0, rawPayloadBytes - compressedPayloadBytes)),
    errorGroupId: text(row.error_group_id),
    captureStatus: String(row.capture_status ?? ''),
    startedAt: String(row.started_at),
    endedAt: String(row.ended_at),
    httpCompletedAt: text(row.http_completed_at),
    firstTokenMs: num(row.first_token_ms)
  }
}
export const auditLogAttemptFromRow = (row: AuditLogRow, _accounts?: Map<string, string>, _groups?: Map<string, string>) => attempt(row)
export const auditLogPayloadSummaryFromRow = payload
export const auditErrorGroupFromRow = (row: AuditLogRow, _names?: Map<string, string>) => errorGroup(row)
