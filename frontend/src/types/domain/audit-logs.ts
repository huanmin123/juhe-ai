export type AuditOutcome = 'success' | 'success_after_retry' | 'gateway_failed' | 'upstream_failed' | 'stream_failed' | 'client_aborted'
export type AuditPayloadPartType = 'client_request' | 'upstream_request' | 'upstream_response' | 'gateway_response' | 'gateway_error'

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
  createdAt: string
  hasHeaders: boolean
  hasBody: boolean
}

export interface AuditLogDetail extends AuditLogSummary {
  attempts: AuditLogAttemptSummary[]
  payloads: AuditLogPayloadSummary[]
}

export interface AuditLogPayloadDetail extends AuditLogPayloadSummary {
  headers?: Record<string, string | string[]>
  bodyText?: string
  bodyBase64?: string
}

export interface AuditLogRuntime {
  queueLength: number
  queueBytes: number
  flushLastSuccessAt?: string
  flushLastError?: string
  droppedSuccessCount: number
  droppedFailureCount: number
  droppedOverflowCount: number
  droppedOversizeCount: number
  activeCaptureCount: number
  settings: {
    enabled: boolean
    successSampleRate: number
    flushIntervalSeconds: number
    batchSize: number
    queueMaxItems: number
    queueMaxBytes: number
    activeCaptureMaxBytes: number
    retentionDays: number
  }
}

export interface AuditLogListResult {
  items: AuditLogSummary[]
  total: number
  page: number
  pageSize: number
}
