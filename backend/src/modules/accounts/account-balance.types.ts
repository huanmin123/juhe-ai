export type AccountBalanceBuiltinAdapter = 'sub2api' | 'newapi' | 'openai_billing' | 'litellm' | 'user_balance'
export type AccountBalanceAdapter = 'builtin' | 'custom'

export type AccountBalanceStatus =
  | 'pending'
  | 'refreshing'
  | 'fresh'
  | 'unlimited'
  | 'unsupported'
  | 'failed'

export type AccountBalanceScope = 'key' | 'account' | 'unknown'
export type AccountBalanceAggregation = 'sum' | 'shared' | 'unknown'

/** Per-Key balance state. Never contains the raw credential. */
export interface AccountBalanceKeySnapshot {
  keyFingerprint: string
  maskedKey: string
  status: AccountBalanceStatus
  remainingUsd?: string
  rawUnit?: 'usd' | 'cny' | 'quota'
  scope?: AccountBalanceScope
  basis?: AccountBalanceSnapshot['basis']
  errorMessage?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
}

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
  /** Account configuration generation that produced this snapshot. */
  configRevision?: number
  remainingUsd?: string
  rawRemaining?: string
  rawUnit?: 'usd' | 'cny' | 'quota'
  basis?: 'api_key_quota' | 'budget' | 'subscription' | 'wallet' | 'custom'
  errorMessage?: string
  lastAttemptAt?: string
  lastSuccessAt?: string
  consecutiveTransientFailures?: number
  lastTransientErrorMessage?: string
  lastTransientFailureAt?: string
  /** Balance ownership semantics used to decide whether an account may sum Keys. */
  scope?: AccountBalanceScope
  aggregation?: AccountBalanceAggregation
  keyCount?: number
  queriedKeyCount?: number
  /** Stored for the owner/admin detail endpoint; stripped from list responses. */
  keyBalances?: AccountBalanceKeySnapshot[]
}
