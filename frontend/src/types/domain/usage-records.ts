export interface UsageRecordLogSnapshot {
  [key: string]: unknown
}

export interface UsageRecordCostBreakdown {
  inputCostUsd?: number
  outputCostUsd?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cacheReadCostUsd?: number
  accountChargeUsd?: number
  multiplier: 1
}

export interface UsageRecordSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  traceId: string
  clientIp?: string
  apiKeyId?: string
  apiKeyName?: string
  groupId?: string
  groupName?: string
  accountId?: string
  accountName?: string
  endpoint?: string
  providerCode?: string
  model?: string
  stream: boolean
  statusCode?: number
  success: boolean
  firstTokenMs?: number
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  costBreakdown?: UsageRecordCostBreakdown
  errorCode?: string
  errorMessage?: string
  requestSnapshot?: UsageRecordLogSnapshot
  responseSnapshot?: UsageRecordLogSnapshot
  createdAt: string
}

export interface UsageRecordListResult {
  items: UsageRecordSummary[]
  total: number
  page: number
  pageSize: number
}
