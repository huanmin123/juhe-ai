export type AccountBalanceBuiltinAdapter = 'sub2api' | 'newapi' | 'litellm' | 'user_balance'
export type AccountBalanceAdapter = 'builtin' | 'custom'

export type AccountBalanceStatus =
  | 'pending'
  | 'refreshing'
  | 'fresh'
  | 'unlimited'
  | 'unsupported'
  | 'failed'

export interface AccountBalanceCustomConfig {
  path: string
  remainingPointer?: string
  totalPointer?: string
  usedPointer?: string
  divisor?: string
}

export interface AccountBalanceQueryConfig {
  adapter: AccountBalanceAdapter
  intervalMinutes: number
  preferredBuiltinAdapter?: AccountBalanceBuiltinAdapter
  custom?: AccountBalanceCustomConfig
}

export interface AccountBalanceSnapshot {
  status: AccountBalanceStatus
  remainingUsd?: string
  rawRemaining?: string
  rawUnit?: 'usd' | 'quota'
  basis?: 'api_key_quota' | 'budget' | 'subscription' | 'wallet' | 'custom'
  errorMessage?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
  consecutiveTransientFailures?: number
  lastTransientErrorMessage?: string
  lastTransientFailureAt?: string
}
