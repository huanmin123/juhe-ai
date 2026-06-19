import type { AuditPayloadBlobStorageStatus } from './audit-log-payload-blobs.js'

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
  upstreamModel?: string
  pricingModel?: string
  modelMappingApplied?: boolean
  modelMappingSource?: string
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
