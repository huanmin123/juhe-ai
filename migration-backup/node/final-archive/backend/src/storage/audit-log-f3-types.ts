export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_succeeded' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'downstream_closed'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error' | 'gateway_metadata'
export type AuditPayloadCaptureStatus = 'complete' | 'summary_only' | 'hash_only' | 'expired' | 'overflow' | 'dropped'
export type AuditPayloadDropReason = 'transport_budget' | 'capacity_limit'
export type AuditLogLifecycleStatus = 'in_progress' | 'finalized'
export type PersistedAuditTrafficSource = 'gateway' | 'manual_account_test' | 'hybrid_scoring' | 'hybrid_quality_scoring'

export interface AuditLogRow { [key: string]: unknown }
export interface AuditLogListOptions {
  page?: number; pageSize?: number; traceId?: string; sessionId?: string; sessionClientType?: string
  outcome?: AuditOutcome | 'all'; statusCode?: number; path?: string; model?: string
  systemAccountId?: string; apiKeyId?: string; groupId?: string; accountId?: string; clientIp?: string
  errorGroupId?: string; trafficSource?: PersistedAuditTrafficSource; startAt?: string; endAt?: string
}
export interface AuditLogPayloadReadOptions { offset?: number; limit?: number; includeHeaders?: boolean; full?: boolean }
export interface AuditLogListItem {
  id: string; traceId: string; sessionId?: string; sessionClientType?: string; trafficSource: PersistedAuditTrafficSource
  systemAccountId?: string; systemAccountName?: string; apiKeyId?: string; apiKeyName?: string; groupId?: string; groupName?: string
  accountId?: string; accountName?: string; method: string; path: string; model?: string; upstreamModel?: string
  modelMappingApplied?: boolean; stream: boolean; auditOutcome: AuditOutcome; success: boolean; finalStatusCode?: number
  lifecycleStatus: AuditLogLifecycleStatus; durationMs?: number; httpDurationMs?: number; createdAt: string
}
export interface AuditLogAttemptSummary {
  id: string; attemptIndex: number; accountId?: string; accountName?: string; accountOwnerSystemAccountId?: string
  groupId?: string; groupName?: string; proxyUrl?: string; providerCode?: string; model?: string; upstreamModel?: string; pricingModel?: string
  modelMappingApplied?: boolean; modelMappingSource?: string; sourceEndpointFamily?: string; upstreamEndpointFamily?: string
  upstreamMethod: string; upstreamUrl: string; upstreamStatusCode?: number; success: boolean; errorPhase?: string; errorCode?: string
  errorMessage?: string; startedAt: string; endedAt?: string; durationMs?: number
}
export interface AuditLogPayloadSummary {
  id: string; attemptId?: string; partType: AuditPayloadPartType; sequenceIndex: number; contentType?: string; contentEncoding?: string
  headersSha256?: string; bodySha256?: string; sizeBytes: number; compressedSizeBytes: number; captureStatus: AuditPayloadCaptureStatus
  dropReason?: AuditPayloadDropReason; createdAt: string; hasHeaders: boolean; hasBody: boolean
}
export interface AuditErrorGroupSummary {
  id: string; fingerprint: string; windowStartedAt: string; windowEndedAt: string; systemAccountId?: string; systemAccountName?: string
  apiKeyId?: string; apiKeyName?: string; groupId?: string; groupName?: string; accountId?: string; accountName?: string; providerCode?: string
  path?: string; model?: string; statusCode?: number; errorPhase?: string; errorCode?: string; errorType?: string; requestFingerprint?: string
  errorFingerprint?: string; count: number; firstEventId?: string; lastEventId?: string; sampleEventId?: string; lastMessage?: string
  createdAt: string; updatedAt: string
}
export interface AuditLogDetail extends AuditLogListItem {
  conversationKey?: string; queryString?: string; errorMessage?: string; sampleBucket: number; sampleReason: string
  attemptCount: number; payloadCount: number; rawPayloadBytes: number; compressedPayloadBytes: number; compressionSavedBytes: number
  errorGroupId?: string; captureStatus: string; startedAt: string; endedAt: string; httpCompletedAt?: string; firstTokenMs?: number
  attempts: AuditLogAttemptSummary[]; errorGroup?: AuditErrorGroupSummary; payloads: AuditLogPayloadSummary[]
}
export interface AuditLogPayloadDetail extends AuditLogPayloadSummary {
  headers?: Record<string, string | string[]>; bodyText?: string; bodyBase64?: string; headersIncluded: boolean
  headersStorageStatus: AuditPayloadBlobStorageStatus; bodyStorageStatus: AuditPayloadBlobStorageStatus; bodyOffset: number; bodyLimit: number
  bodyBytesReturned: number; bodyTotalBytes: number; bodyNextOffset?: number; bodyTruncated: boolean
}
export type AuditPayloadBlobStorageStatus = 'not_saved' | 'metadata_missing' | 'file_missing' | 'available'
export interface AuditLogListResult { items: AuditLogListItem[]; total: number; hasMore: boolean; page: number; pageSize: number }
export interface AuditErrorGroupListOptions { page?: number; pageSize?: number; path?: string; model?: string; statusCode?: number; systemAccountId?: string; apiKeyId?: string; groupId?: string; accountId?: string }
export interface AuditErrorGroupListResult { items: AuditErrorGroupSummary[]; total: number; hasMore: boolean; page: number; pageSize: number }
