import type { AccountGroupBindStatus, AccountStatus, AccountTrafficMigrationSourceStatus, AccountType, AuthorizationStatus, ProviderCode, ResourceAccessType } from './base'
import type { AccountUsageSummary } from './usage-stats'

export interface AccountCredentials {
  api_key?: string
  base_url?: string
  access_token?: string
  refresh_token?: string
  client_id?: string
  expires_at?: string
  account_id?: string
  [key: string]: unknown
}

export interface ResourcePermissions {
  canUse: boolean
  canEdit: boolean
  canDelete: boolean
  canAuthorize: boolean
  canViewCredentials: boolean
  canManageAccounts?: boolean
}

export interface AccountOAuthUsageWindow {
  utilization: number
  resetsAt?: string
  remainingSeconds: number
  windowMinutes?: number
}

export interface AccountOAuthUsageSnapshot {
  kind: 'openai_codex'
  source?: string
  updatedAt?: string
  refreshStatus?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
  nextRefreshAfter?: string
  lastErrorMessage?: string
  fiveHour?: AccountOAuthUsageWindow
  sevenDay?: AccountOAuthUsageWindow
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
  systemAccountId?: string
  systemAccountName?: string
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
  accountExpiresAt?: string
  cooldownUntil?: string
  lastErrorMessage?: string
  lastUsedAt?: string
  todayUsage: AccountUsageSummary
  usage: AccountUsageSummary
  oauthUsage?: AccountOAuthUsageSnapshot
  accessType?: ResourceAccessType
  accountAuthorizationId?: string
  boundGroupId?: string
  boundGroupName?: string
  groupBindStatus?: AccountGroupBindStatus
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  permissions?: ResourcePermissions
  authorizationUsageAvailable?: boolean
  authorizationCount?: number
  authorizationTeamCount?: number
}

export interface AccountTestResult {
  accountId: string
  accountName: string
  providerCode: ProviderCode
  type: AccountType
  success: boolean
  statusCode?: number
  message: string
  model?: string
  requestUrl?: string
  requestBody?: unknown
  responseHeaders?: Record<string, string | string[]>
  responseBody?: unknown
  responseText?: string
  outputText?: string
  modelsUrl?: string
  proxyUrl?: string
  tokenRefreshed?: boolean
  durationMs?: number
  accountStatusChanged?: boolean
  accountStatus?: AccountStatus
  errorPolicyAction?: 'none' | 'retry_next' | 'cooldown' | 'disable' | 'default_cooldown'
  errorPolicyReason?: string
}

export interface AccountTrafficMigrationResult {
  sourceAccount: AccountSummary
  targetAccount: AccountSummary
  migratedSessionCount: number
  sourceStatus: AccountTrafficMigrationSourceStatus
  sourceCooldownUntil?: string | null
}

export interface ErrorPolicySummary {
  id: string
  name: string
  enabled: boolean
  rules: Array<Record<string, unknown>>
}

export interface GroupSummary {
  id: string
  systemAccountId?: string
  systemAccountName?: string
  name: string
  providerCode: ProviderCode
  description?: string
  enabled: boolean
  isDefault: boolean
  accountIds: string[]
  accountStats: GroupAccountStats
  accessType?: ResourceAccessType
  groupAuthorizationId?: string
  ownerSystemAccountId?: string
  ownerSystemAccountName?: string
  authorizationStatus?: AuthorizationStatus
  permissions?: ResourcePermissions
}
