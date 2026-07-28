export interface UsageRecordLogSnapshot {
  [key: string]: unknown
}

export interface UsageRecordCostBreakdown {
  currency?: 'USD'
  billingPolicy?: string
  lineItems?: UsageRecordCostLineItem[]
  inputCostUsd?: number
  outputCostUsd?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cacheReadCostUsd?: number
  cacheReadUsdPer1M?: number
  cacheWriteCostUsd?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hCostUsd?: number
  cacheWrite1hUsdPer1M?: number
  thinkingTokens?: number
  inputImageCostUsd?: number
  outputImageCostUsd?: number
  inputImageUsdPer1M?: number
  outputImageUsdPer1M?: number
  inputAudioCostUsd?: number
  outputAudioCostUsd?: number
  inputAudioUsdPer1M?: number
  outputAudioUsdPer1M?: number
  outputImageUnitCostUsd?: number
  outputUsdPerImage?: number
  accountChargeUsd?: number
  multiplier: 1
  serviceTierPricingSource: 'default' | 'tier_specific' | 'multiplier' | 'mixed' | 'unknown'
  serviceTierMultiplier?: number
}

export interface UsageRecordCostLineItem {
  key: string
  kind: 'input' | 'output' | 'cache_read' | 'cache_write' | 'cache_write_1h' | 'image_input' | 'image_output' | 'audio_input' | 'audio_output' | 'image_output_unit' | 'other'
  label: string
  quantity: number
  unit: 'token' | 'image' | 'request' | 'second' | 'minute' | 'token_hour'
  unitSize: number
  unitPriceUsd: number
  costUsd: number
}

export type UsageRecordTrafficSource = 'gateway' | 'manual_account_test' | 'account_health_check' | 'runtime_recovery_probe' | 'cooldown_retest' | 'hybrid_scoring' | 'hybrid_quality_scoring'
export type UsageFailureAttribution = 'account_upstream' | 'account_dependency' | 'opaque_upstream' | 'gateway_capacity' | 'gateway_policy' | 'client_lifecycle'

export interface UsageRecordSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  traceId: string
  trafficSource: UsageRecordTrafficSource
  clientIp?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  endpoint?: string
  providerCode?: string
  providerProtocolProfileId?: string
  usageSemantic?: string
  model?: string
  upstreamModel?: string
  pricingModel?: string
  requestedServiceTier?: string
  effectiveServiceTier?: string
  reportedServiceTier?: string
  billedServiceTier?: string
  requestedReasoningEffort?: UsageRecordReasoningEffort
  effectiveReasoningEffort?: UsageRecordReasoningEffort
  modelMappingApplied?: boolean
  modelMappingSource?: string
  sourceEndpointFamily?: string
  upstreamEndpointFamily?: string
  stream: boolean
  statusCode?: number
  success: boolean
  failureAttribution?: UsageFailureAttribution
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  cacheReadCostUsd?: number
  cacheWriteTokens?: number
  cacheWrite1hTokens?: number
  cacheWriteCostUsd?: number
  thinkingTokens?: number
  inputImageTokens?: number
  outputImageTokens?: number
  inputAudioTokens?: number
  outputAudioTokens?: number
  outputImageCount?: number
  costUsd?: number
  costBreakdown?: UsageRecordCostBreakdown
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: UsageRecordLogSnapshot
  responseSnapshot?: UsageRecordLogSnapshot
  createdAt: string
}

/** Paged table rows contain only values rendered by the list; heavy detail payloads are not list data. */
export type UsageRecordListItem = Pick<UsageRecordSummary,
  | 'id'
  | 'systemAccountId'
  | 'systemAccountName'
  | 'traceId'
  | 'trafficSource'
  | 'clientIp'
  | 'apiKeyId'
  | 'apiKeyName'
  | 'groupId'
  | 'groupName'
  | 'accountId'
  | 'accountName'
  | 'endpoint'
  | 'model'
  | 'upstreamModel'
  | 'billedServiceTier'
  | 'effectiveReasoningEffort'
  | 'modelMappingApplied'
  | 'stream'
  | 'statusCode'
  | 'success'
  | 'firstTokenMs'
  | 'durationMs'
  | 'inputTokens'
  | 'outputTokens'
  | 'cacheReadTokens'
  | 'costUsd'
  | 'createdAt'
>

export type UsageRecordReasoningEffort = string

export interface UsageRecordListResult {
  items: UsageRecordListItem[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}
