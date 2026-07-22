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

export type ResponseInspectionPolicyProtocolCode = 'openai' | 'anthropic' | 'gemini'

export interface ResponseInspectionPolicyOverview {
  id: string
  defaultRule: boolean
  editable: boolean
  name: string
  enabled: boolean
  priority: number
  scopeType: ResponseInspectionPolicyScopeType
  protocolCode: ResponseInspectionPolicyProtocolCode
  providerCode?: string
  providerName?: string
  action: ResponseInspectionPolicyAction
  updatedAt?: string
}

export interface ResponseInspectionPolicyDetail extends ResponseInspectionPolicyOverview {
  match: ResponseInspectionPolicyMatch
  notes?: string
  createdAt?: string
}

export interface ResponseInspectionPolicyProviderOption {
  code: string
  name: string
  protocolCode: ResponseInspectionPolicyProtocolCode
}

export interface ResponseInspectionPolicyListResult {
  defaultRules: ResponseInspectionPolicyOverview[]
  policies: ResponseInspectionPolicyOverview[]
}
