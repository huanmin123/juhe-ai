import { z } from 'zod'

export interface AccountStreamInterceptPolicyValidationResult {
  valid: boolean
  message?: string
}

const textListSchema = z.array(z.string().trim().min(1).max(200)).max(50).optional()
const actionSchema = z.enum([
  'observe',
  'drop_event',
  'fail_stream',
  'retry_no_avoidance',
  'retry_next_account',
  'avoid_account_ttl',
  'avoid_upstream_bucket_ttl'
])

const accountStreamInterceptRuleSchema = z.object({
  enabled: z.boolean().optional(),
  name: z.string().trim().min(1).max(100).optional(),
  priority: z.coerce.number().int().min(1).max(9999).optional(),
  match: z.object({
    eventTypes: textListSchema,
    dataTypes: textListSchema,
    errorCodes: textListSchema,
    errorTypes: textListSchema,
    textIncludes: textListSchema,
    textExcludes: textListSchema,
    jsonPathsExists: textListSchema
  }).partial().optional(),
  action: actionSchema,
  avoidanceTtlSeconds: z.coerce.number().int().min(1).max(86400).optional(),
  notes: z.string().trim().max(1000).optional()
}).strict().superRefine((value, context) => {
  if ((value.action === 'avoid_account_ttl' || value.action === 'avoid_upstream_bucket_ttl') && value.avoidanceTtlSeconds === undefined) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      path: ['avoidanceTtlSeconds'],
      message: '短期避让模板需要配置避让秒数'
    })
  }
})

export function validateAccountStreamInterceptRules(value: unknown): AccountStreamInterceptPolicyValidationResult {
  if (value === undefined) {
    return { valid: true }
  }
  if (!Array.isArray(value)) {
    return { valid: false, message: '账户流式拦截规则必须是数组' }
  }
  if (value.length > 20) {
    return { valid: false, message: '账户流式拦截规则不能超过 20 条' }
  }
  for (const [index, item] of value.entries()) {
    const parsed = accountStreamInterceptRuleSchema.safeParse(item)
    const ruleIndex = index + 1
    if (!parsed.success) {
      return { valid: false, message: `第 ${ruleIndex} 条流式拦截规则参数无效` }
    }
    const rule = parsed.data
    if (rule.enabled === false) continue
    if (!hasMatcher(rule.match)) {
      return { valid: false, message: `第 ${ruleIndex} 条流式拦截规则至少需要一个匹配条件` }
    }
  }
  return { valid: true }
}

export function accountStreamInterceptValidationMessage(result: AccountStreamInterceptPolicyValidationResult): string | undefined {
  return result.valid ? undefined : result.message ?? '账户流式拦截规则配置不完整'
}

function hasMatcher(match: Record<string, unknown> | undefined): boolean {
  if (!match) return false
  const fields = [
    match.eventTypes,
    match.dataTypes,
    match.errorCodes,
    match.errorTypes,
    match.textIncludes,
    match.jsonPathsExists
  ]
  return fields.some((value) => Array.isArray(value) && value.some((item) => typeof item === 'string' && item.trim()))
}
