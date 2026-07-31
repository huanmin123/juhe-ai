// Historical rows can still contain client_aborted even though Node no longer writes or filters that terminal value.
export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_succeeded' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'downstream_closed' | 'client_aborted'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error' | 'gateway_metadata'
export type AuditTrafficSource = 'gateway' | 'manual_account_test' | 'account_health_check' | 'runtime_recovery_probe' | 'cooldown_retest' | 'hybrid_scoring' | 'hybrid_quality_scoring'
export type AuditPayloadBlobStorageStatus = 'not_saved' | 'metadata_missing' | 'file_missing' | 'available'

export interface AuditLogSummary {
  id: string
  traceId: string
  sessionId?: string
  sessionClientType?: string
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
  upstreamModel?: string
  pricingModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
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
  httpCompletedAt?: string
  httpDurationMs?: number
  firstTokenMs?: number
  createdAt: string
}

export type AuditLogListItem = Pick<AuditLogSummary,
  | 'id' | 'traceId' | 'sessionId' | 'sessionClientType'
  | 'trafficSource'
  | 'systemAccountId' | 'systemAccountName'
  | 'apiKeyId' | 'apiKeyName' | 'groupId' | 'groupName'
  | 'accountId' | 'accountName'
  | 'method' | 'path' | 'model' | 'upstreamModel' | 'modelMappingApplied'
  | 'stream' | 'auditOutcome' | 'success' | 'finalStatusCode'
  | 'durationMs' | 'httpDurationMs' | 'createdAt'
>

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
  model?: string
  upstreamModel?: string
  pricingModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
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
  captureStatus: string
  createdAt: string
  hasHeaders: boolean
  hasBody: boolean
}

export interface AuditLogDetail extends AuditLogSummary {
  conversationKey?: string
  attempts: AuditLogAttemptSummary[]
  errorGroup?: AuditErrorGroupSummary
  payloads: AuditLogPayloadSummary[]
}

export type AuditLogDetailAttemptSupplement = Pick<AuditLogAttemptSummary,
  | 'id' | 'attemptIndex' | 'accountId' | 'accountName'
  | 'upstreamUrl' | 'upstreamStatusCode' | 'success' | 'errorMessage'
  | 'startedAt' | 'endedAt' | 'durationMs'
>

export type AuditLogDetailPayloadSupplement = Pick<AuditLogPayloadSummary,
  | 'id' | 'attemptId' | 'partType' | 'sequenceIndex'
  | 'sizeBytes' | 'captureStatus' | 'createdAt' | 'hasHeaders' | 'hasBody'
>

export interface AuditLogDetailSupplement extends Pick<AuditLogSummary,
  | 'queryString' | 'errorMessage'
  | 'sampleBucket' | 'sampleReason'
  | 'startedAt' | 'endedAt' | 'httpCompletedAt'
> {
  conversationKey?: string
  attempts: AuditLogDetailAttemptSupplement[]
  payloads: AuditLogDetailPayloadSupplement[]
}

export interface AuditLogDisplayDetail extends AuditLogListItem, AuditLogDetailSupplement {}

export interface AuditLogPayloadDetail extends AuditLogPayloadSummary {
  headers?: Record<string, string | string[]>
  bodyText?: string
  bodyBase64?: string
  headersIncluded: boolean
  headersStorageStatus: AuditPayloadBlobStorageStatus
  bodyStorageStatus: AuditPayloadBlobStorageStatus
  bodyOffset: number
  bodyLimit: number
  bodyBytesReturned: number
  bodyTotalBytes: number
  bodyNextOffset?: number
  bodyTruncated: boolean
}

export interface AuditLogListResult {
  items: AuditLogListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}

export interface AuditLogHotSearchResult extends AuditLogListResult {
  available: boolean
  elapsedMs: number
  keywords: string[]
  startAt: string
  endAt: string
  limit: number
  truncated: boolean
  scannedFileCount: number
  message?: string
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
