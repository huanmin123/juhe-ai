export type StreamInterceptPolicyAction =
  | 'observe'
  | 'drop_event'
  | 'fail_stream'
  | 'retry_no_avoidance'
  | 'retry_next_account'
  | 'avoid_account_ttl'
  | 'avoid_upstream_bucket_ttl'

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
  defaultRule: boolean
  editable: boolean
  name: string
  enabled: boolean
  priority: number
  match: StreamInterceptPolicyMatch
  action: StreamInterceptPolicyAction
  avoidanceTtlSeconds?: number
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface StreamInterceptPolicyListResult {
  defaultRules: StreamInterceptPolicySummary[]
  policies: StreamInterceptPolicySummary[]
}
