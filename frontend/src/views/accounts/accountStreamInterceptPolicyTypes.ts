import type { StreamInterceptPolicyAction } from '@/types/domain'

export interface AccountStreamInterceptRuleForm {
  enabled: boolean
  name: string
  priority: number | null
  eventTypes: string
  dataTypes: string
  errorCodes: string
  errorTypes: string
  textIncludes: string
  textExcludes: string
  jsonPathsExists: string
  action: StreamInterceptPolicyAction
  avoidanceTtlSeconds: number | null
  notes: string
}

export interface AccountStreamInterceptRulePayload {
  enabled: boolean
  name: string
  priority: number
  match: {
    eventTypes?: string[]
    dataTypes?: string[]
    errorCodes?: string[]
    errorTypes?: string[]
    textIncludes?: string[]
    textExcludes?: string[]
    jsonPathsExists?: string[]
  }
  action: StreamInterceptPolicyAction
  avoidanceTtlSeconds?: number
  notes?: string
}

export interface AccountStreamInterceptValidationResult {
  valid: boolean
  message?: string
  index?: number
}
