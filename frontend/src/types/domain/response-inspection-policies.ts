export type ResponseInspectionPolicyAction =
  | 'observe'
  | 'drop_event'
  | 'retry_no_avoidance'
  | 'retry_next_account'
  | 'avoid_account_ttl'
  | 'avoid_upstream_bucket_ttl'

export type ResponseInspectionPolicyScopeType = 'protocol' | 'provider'
export type ResponseInspectionPolicyClientProfile =
  | 'codex'
  | 'generic_openai'
  | 'claude_code'
  | 'generic_anthropic'
  | 'generic_gemini'
  | 'gemini_cli'

export interface ResponseInspectionPolicyMatch {
  clientProfiles?: ResponseInspectionPolicyClientProfile[]
  outputTextIncludes?: string[]
  outputTextExcludes?: string[]
  errorCodes?: string[]
  errorTypes?: string[]
  errorMessageIncludes?: string[]
  finishReasons?: string[]
  jsonPathsExists?: string[]
  rawTextIncludes?: string[]
}

export interface ResponseInspectionPolicySummary {
  id: string
  defaultRule: boolean
  editable: boolean
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: string
  providerCode?: string
  match: ResponseInspectionPolicyMatch
  action: ResponseInspectionPolicyAction
  notes?: string
  createdAt?: string
  updatedAt?: string
}

export interface ResponseInspectionPolicyListResult {
  defaultRules: ResponseInspectionPolicySummary[]
  policies: ResponseInspectionPolicySummary[]
}
