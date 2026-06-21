export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'client_aborted'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error' | 'gateway_metadata'
export type AuditTrafficSource = 'gateway' | 'manual_account_test' | 'cooldown_retest' | 'hybrid_scoring'
export type AuditPayloadBlobStorageStatus = 'not_saved' | 'metadata_missing' | 'file_missing' | 'available'

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
  upstreamModel?: string
  pricingModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
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
  captureStatus: string
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

export interface AuditLogRuntime {
  runtimeAvailable: boolean
  workerSnapshotAvailable: boolean
  auditLogQueueAvailable: boolean
  activeCaptureAvailable: boolean
  unavailableReason?: string
  queueLength: number | null
  queueBytes: number | null
  flushLastSuccessAt?: string
  flushLastError?: string
  droppedSuccessCount: number | null
  droppedFailureCount: number | null
  droppedOverflowCount: number | null
  droppedOversizeCount: number | null
  activeCaptureCount: number | null
  worker: {
    available: boolean
    snapshotAvailable: boolean
    pid?: number
    ready: boolean | null
    pendingMessageCount: number | null
  }
  settings: {
    enabled: boolean
    successSampleRate: number
    flushIntervalSeconds: number
    batchSize: number
    queueMaxItems: number
    queueMaxBytes: number
    activeCaptureMaxBytes: number
    successHotRetentionHours: number
    successRetentionDays: number
    failureRetentionDays: number
    errorGroupRetentionDays: number
  }
}

export interface AuditLogListResult {
  items: AuditLogSummary[]
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
