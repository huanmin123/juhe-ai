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
