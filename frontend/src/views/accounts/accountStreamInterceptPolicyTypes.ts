import type {
  StreamInterceptPolicyAccountState,
  StreamInterceptPolicyAccountSwitch,
  StreamInterceptPolicyDataHandling,
  StreamInterceptPolicyExecutionMode
} from '@/types/domain'

export interface AccountStreamInterceptRuleForm {
  enabled: boolean
  name: string
  priority: number | null
  executionMode: StreamInterceptPolicyExecutionMode
  eventTypes: string
  dataTypes: string
  errorCodes: string
  errorTypes: string
  textIncludes: string
  textExcludes: string
  jsonPathsExists: string
  dataHandling: StreamInterceptPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: StreamInterceptPolicyAccountSwitch
  accountState: StreamInterceptPolicyAccountState
  avoidanceTtlSeconds: number | null
  notes: string
}

export interface AccountStreamInterceptRulePayload {
  enabled: boolean
  name: string
  priority: number
  executionMode: StreamInterceptPolicyExecutionMode
  match: {
    eventTypes?: string[]
    dataTypes?: string[]
    errorCodes?: string[]
    errorTypes?: string[]
    textIncludes?: string[]
    textExcludes?: string[]
    jsonPathsExists?: string[]
  }
  dataHandling: StreamInterceptPolicyDataHandling
  retryEnabled: boolean
  accountSwitch: StreamInterceptPolicyAccountSwitch
  accountState: StreamInterceptPolicyAccountState
  avoidanceTtlSeconds?: number
  notes?: string
}

export interface AccountStreamInterceptValidationResult {
  valid: boolean
  message?: string
  index?: number
}
