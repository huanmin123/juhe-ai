export type StreamInterceptPolicyExecutionMode = 'intercept' | 'dry_run'
export type StreamInterceptPolicyDataHandling = 'discard_event' | 'discard_stream' | 'replace_with_failure'
export type StreamInterceptPolicyAccountSwitch = 'none' | 'request_next_account' | 'avoid_account_ttl' | 'avoid_upstream_bucket_ttl'
export type StreamInterceptPolicyAccountState = 'none' | 'runtime_avoidance'

export interface StreamInterceptPolicyMatch {
  eventTypes?: string[]
  dataTypes?: string[]
  errorCodes?: string[]
  errorTypes?: string[]
  textIncludes?: string[]
  textExcludes?: string[]
  jsonPathsExists?: string[]
}

export interface StreamInterceptPolicySummary {
  id: string
  builtIn: boolean
  editable: boolean
  name: string
  enabled: boolean
  executionMode: StreamInterceptPolicyExecutionMode
  priority: number
  match: StreamInterceptPolicyMatch
  dataHandling: StreamInterceptPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: StreamInterceptPolicyAccountSwitch
  accountState: StreamInterceptPolicyAccountState
  avoidanceTtlSeconds?: number
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface StreamInterceptPolicyListResult {
  presets: StreamInterceptPolicySummary[]
  policies: StreamInterceptPolicySummary[]
}
