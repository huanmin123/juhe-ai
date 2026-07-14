export interface UsageRecordLogSnapshot {
  [key: string]: unknown
}

export interface UsageRecordCostBreakdown {
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

export type UsageRecordTrafficSource = 'gateway' | 'manual_account_test' | 'account_health_check' | 'runtime_recovery_probe' | 'cooldown_retest' | 'hybrid_scoring' | 'hybrid_quality_scoring'
export type UsageFailureAttribution = 'account_upstream' | 'account_dependency' | 'gateway_capacity' | 'gateway_policy' | 'client_lifecycle'

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
  requestedServiceTier?: 'default' | 'priority' | 'flex'
  effectiveServiceTier?: 'default' | 'priority' | 'flex'
  reportedServiceTier?: 'default' | 'priority' | 'flex'
  billedServiceTier?: 'default' | 'priority' | 'flex'
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

export type UsageRecordReasoningEffort = 'none' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
  hasMore: boolean
  page: number
  pageSize: number
}
