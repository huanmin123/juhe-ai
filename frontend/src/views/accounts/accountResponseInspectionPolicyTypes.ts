import type { ResponseInspectionPolicyAction } from '@/types/domain'

export interface AccountResponseInspectionRuleForm {
  enabled: boolean
  name: string
  priority: number | null
  outputTextIncludes: string
  outputTextExcludes: string
  errorCodes: string
  errorTypes: string
  errorMessageIncludes: string
  finishReasons: string
  jsonPathsExists: string
  rawTextIncludes: string
  action: ResponseInspectionPolicyAction
  notes: string
}

export interface AccountResponseInspectionRulePayload {
  enabled: boolean
  name: string
  priority: number
  match: {
    outputTextIncludes?: string[]
    outputTextExcludes?: string[]
    errorCodes?: string[]
    errorTypes?: string[]
    errorMessageIncludes?: string[]
    finishReasons?: string[]
    jsonPathsExists?: string[]
    rawTextIncludes?: string[]
  }
  action: ResponseInspectionPolicyAction
  notes?: string
}

export interface AccountResponseInspectionPolicyValidationResult {
  valid: boolean
  message?: string
  index?: number
}
