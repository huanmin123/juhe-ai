export type ProviderCode = string
export type AccountType = string
export type AccountStatus = 'active' | 'disabled' | 'error'

export interface ProviderDefinition {
  id: string
  code: ProviderCode
  name: string
  enabled: boolean
  baseUrl: string
  accountTypes: AccountType[]
  capabilities: string[]
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

export interface GroupSummary {
  id: string
  name: string
  description?: string
  enabled: boolean
  accountIds: string[]
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

export interface UsageRecordSummary {
  id: string
  requestId: string
  apiKeyId?: string
  groupId?: string
  accountId?: string
  providerCode?: string
  model?: string
  stream: boolean
  statusCode?: number
  success: boolean
  durationMs?: number
  inputTokens?: number
  outputTokens?: number
  cacheReadTokens?: number
  costUsd?: number
  errorCode?: string
  errorMessage?: string
  createdAt: string
}

export interface SystemSettings {
  defaultOpenAIBaseUrl?: string
  defaultAccountConcurrencyLimit?: number
  streamCircuitBreakerEnabled?: boolean
  streamIdleTimeoutSeconds?: number
  streamFailureAction?: 'cooldown' | 'disable' | 'none'
  streamAccountCooldownMinutes?: number
  streamFailureThresholdCount?: number
  streamFailureThresholdWindowMinutes?: number
  overloadCooldownEnabled?: boolean
  overloadCooldownMinutes?: number
  [key: string]: unknown
}
