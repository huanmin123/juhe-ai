export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'disabled' | 'error' | 'rate_limited' | 'temporary_unavailable'

export interface ProviderDefinition {
  id: string
  code: ProviderCode
  name: string
  enabled: boolean
  baseUrl: string
  accountTypes: AccountType[]
  capabilities: string[]
}

export interface ProviderModelPricing {
  providerCode: ProviderCode
  model: string
  mode?: string
  releaseDate?: string
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  cacheWrite1hUsdPer1M?: number
  imageOutputUsdPer1M?: number
  outputUsdPerImage?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  maxTokens?: number
  supportsPromptCaching: boolean
  supportsServiceTier: boolean
  source: string
}

export interface AccountCredentials {
  api_key?: string
  base_url?: string
  organization_id?: string
  access_token?: string
  refresh_token?: string
  client_id?: string
  expires_at?: string
  account_id?: string
  [key: string]: unknown
}

export interface AccountUsageSummary {
  requestCount: number
  clientCount: number
  inputTokens: number
  outputTokens: number
  cacheReadTokens: number
  totalTokens: number
  totalCost: number
  lastUsedAt?: string
}

export interface GroupAccountStats {
  total: number
  available: number
  active: number
  disabled: number
  error: number
  rateLimited: number
  currentConcurrency: number
  concurrencyLimit: number
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
}

export interface AccountSummary {
  id: string
  providerCode: ProviderCode
  name: string
  notes?: string
  type: AccountType
  credentials: AccountCredentials
  status: AccountStatus
  concurrencyLimit: number
  currentConcurrency: number
  priority: number
  proxyProfileId?: string
  passthroughEnabled: boolean
  errorPolicyId?: string
  schedulable: boolean
  cooldownUntil?: string
  lastErrorMessage?: string
  lastUsedAt?: string
  usage: AccountUsageSummary
}

export interface AccountTestResult {
  accountId: string
  accountName: string
  providerCode: ProviderCode
  type: AccountType
  success: boolean
  statusCode?: number
  message: string
  modelsUrl?: string
  proxyUrl?: string
  tokenRefreshed?: boolean
}

export interface ErrorPolicySummary {
  id: string
  name: string
  enabled: boolean
  rules: Array<Record<string, unknown>>
}

export interface GroupSummary {
  id: string
  name: string
  providerCode: ProviderCode
  description?: string
  enabled: boolean
  accountIds: string[]
  accountStats: GroupAccountStats
}

export interface ApiKeySummary {
  id: string
  name: string
  keyPrefix: string
  key: string
  status: 'active' | 'disabled'
  groupId: string
  expiresAt?: string
}

export interface CreatedApiKey extends ApiKeySummary {}


export interface OpenAIAuthURLResult {
  authUrl: string
  sessionId: string
  state: string
  redirectUri: string
  clientId: string
}

export interface ProxyProfileSummary {
  id: string
  name: string
  type: 'http' | 'https' | 'socks5' | string
  host: string
  port: number
  username?: string
  enabled: boolean
  testStatus: string
  lastTestedAt?: string
}

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
  requestId: string
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

export interface SystemSettings {
  defaultAccountConcurrencyLimit?: number
  defaultTemporaryUnschedulableMinutes?: number
  temporaryUnschedulableRetryIntervalSeconds?: number
  temporaryUnschedulableRetryAttempts?: number
  streamCircuitBreakerEnabled?: boolean
  streamRequestTimeoutSeconds?: number
  streamIdleTimeoutSeconds?: number
  streamFailureThresholdCount?: number
  streamFailureThresholdWindowMinutes?: number
  [key: string]: unknown
}
