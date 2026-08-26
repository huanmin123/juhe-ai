import { z } from 'zod'

import type { ResponseInspectionPolicyAction, ResponseInspectionPolicyMatch } from '../../storage/response-inspection-policy.repository.js'

export interface AccountResponseInspectionPolicyValidationResult {
  valid: boolean
  message?: string
}

const textListSchema = z.array(z.string().trim().min(1).max(200)).max(50).optional()
const clientProfileListSchema = z.array(z.enum(['codex', 'generic_openai', 'claude_code', 'generic_anthropic', 'generic_gemini', 'gemini_cli'])).max(6).optional()
const actionSchema = z.enum([
  'observe',
  'drop_event',
  'retry_no_avoidance',
  'retry_next_account',
  'avoid_account_ttl',
  'avoid_upstream_bucket_ttl'
])

const accountResponseInspectionRuleSchema = z.object({
  enabled: z.boolean(),
  name: z.string().trim().min(1).max(100),
  priority: z.number().int().min(1).max(9999),
  match: z.object({
    clientProfiles: clientProfileListSchema,
    outputTextIncludes: textListSchema,
    outputTextExcludes: textListSchema,
    errorCodes: textListSchema,
    errorTypes: textListSchema,
    errorMessageIncludes: textListSchema,
    finishReasons: textListSchema,
    jsonPathsExists: textListSchema,
    rawTextIncludes: textListSchema
  }).strict(),
  action: actionSchema,
  notes: z.string().trim().max(1000).optional()
}).strict()

export type AccountResponseInspectionRule = z.infer<typeof accountResponseInspectionRuleSchema>

export function normalizeAccountResponseInspectionRules(value: unknown): AccountResponseInspectionRule[] {
  if (value === undefined) {
    return []
  }
  if (!Array.isArray(value)) {
    throw new Error('账户响应检查规则必须是数组')
  }
  if (value.length > 20) {
    throw new Error('账户响应检查规则不能超过 20 条')
  }
  return value.map((item, index) => {
    const parsed = accountResponseInspectionRuleSchema.safeParse(item)
    const ruleIndex = index + 1
    if (!parsed.success) {
      throw new Error(`第 ${ruleIndex} 条响应检查规则参数无效`)
    }
    const rule = parsed.data
    if (rule.enabled !== false && !hasMatcher(rule.match)) {
      throw new Error(`第 ${ruleIndex} 条响应检查规则至少需要一个匹配条件`)
    }
    return rule
  })
}

export function validateAccountResponseInspectionRules(value: unknown): AccountResponseInspectionPolicyValidationResult {
  try {
    normalizeAccountResponseInspectionRules(value)
  } catch (error) {
    return { valid: false, message: error instanceof Error ? error.message : '账户响应检查规则配置不完整' }
  }
  return { valid: true }
}

export function validateAccountCredentialsResponseInspectionRules(credentials: unknown): AccountResponseInspectionPolicyValidationResult {
  if (!credentials || typeof credentials !== 'object' || Array.isArray(credentials)) {
    return { valid: true }
  }
  const record = credentials as Record<string, unknown>
  if (!Object.prototype.hasOwnProperty.call(record, 'response_inspection_rules')) {
    return { valid: true }
  }
  return validateAccountResponseInspectionRules(record.response_inspection_rules)
}

export function accountResponseInspectionPolicyValidationMessage(result: AccountResponseInspectionPolicyValidationResult): string | undefined {
  return result.valid ? undefined : result.message ?? '账户响应检查规则配置不完整'
}

function hasMatcher(match: ResponseInspectionPolicyMatch | undefined): boolean {
  if (!match) return false
  const fields = [
    match.outputTextIncludes,
    match.errorCodes,
    match.errorTypes,
    match.errorMessageIncludes,
    match.finishReasons,
    match.jsonPathsExists,
    match.rawTextIncludes
  ]
  return fields.some((value) => Array.isArray(value) && value.some((item) => typeof item === 'string' && item.trim()))
}

export function normalizeAccountResponseInspectionAction(value: unknown): ResponseInspectionPolicyAction | undefined {
  return actionSchema.safeParse(value).success ? value as ResponseInspectionPolicyAction : undefined
}
