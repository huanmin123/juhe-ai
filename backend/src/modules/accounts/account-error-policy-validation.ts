export interface AccountErrorPolicyValidationResult {
  valid: boolean
  message?: string
}

export type AccountErrorHandlingRuleAction = 'retry_next' | 'temp_unschedulable' | 'rate_limited' | 'error_disabled'
export type AccountErrorHandlingRuleResetStrategy = 'duration' | 'daily' | 'weekly'

export interface AccountErrorHandlingRule {
  enabled: boolean
  name: string
  priority: number
  action: AccountErrorHandlingRuleAction
  status_codes?: number[]
  error_codes?: string[]
  error_types?: string[]
  keywords?: string[]
  reset_strategy?: AccountErrorHandlingRuleResetStrategy
  duration_hours?: number
  daily_reset_hour?: number
  weekly_reset_day?: number
  weekly_reset_hour?: number
  description?: string
}

export function validateAccountErrorHandlingRules(value: unknown): AccountErrorPolicyValidationResult {
  if (value === undefined) return { valid: true }
  try {
    normalizeAccountErrorHandlingRules(value)
  } catch (error) {
    return { valid: false, message: error instanceof Error ? error.message : '错误处理策略配置无效' }
  }
  return { valid: true }
}

export function normalizeAccountErrorHandlingRules(value: unknown): AccountErrorHandlingRule[] {
  if (value === undefined) return []
  if (!Array.isArray(value)) {
    throw new Error('错误处理策略规则格式无效')
  }
  return value.map((item, index) => normalizeAccountErrorHandlingRule(item, index + 1))
}

export function validateAccountCredentialsErrorHandlingRules(credentials: unknown): AccountErrorPolicyValidationResult {
  if (!credentials || typeof credentials !== 'object' || Array.isArray(credentials)) {
    return { valid: true }
  }
  const record = credentials as Record<string, unknown>
  if (!Object.prototype.hasOwnProperty.call(record, 'error_handling_rules')) {
    return { valid: true }
  }
  return validateAccountErrorHandlingRules(record.error_handling_rules)
}

export function accountErrorPolicyValidationMessage(result: AccountErrorPolicyValidationResult): string | undefined {
  return result.valid ? undefined : result.message ?? '错误处理策略配置无效'
}

function isSuccessStatusCode(code: number): boolean {
  return Number.isInteger(code) && code >= 200 && code <= 299
}

function normalizeAccountErrorHandlingRule(value: unknown, index: number): AccountErrorHandlingRule {
  if (!isRecord(value)) {
    throw new Error(`第 ${index} 条错误处理策略规则格式无效`)
  }
  if (value.source === 'system' || value.inherited === true || value.editable === false) {
    throw new Error(`第 ${index} 条错误处理策略规则不能写入系统继承规则`)
  }
  assertOnlyKeys(value, [
    'enabled',
    'name',
    'priority',
    'status_codes',
    'error_codes',
    'error_types',
    'keywords',
    'action',
    'reset_strategy',
    'duration_hours',
    'daily_reset_hour',
    'weekly_reset_day',
    'weekly_reset_hour',
    'description'
  ], `第 ${index} 条错误处理策略规则`)
  const rule: AccountErrorHandlingRule = {
    enabled: requiredBoolean(value.enabled, `第 ${index} 条规则启用状态`),
    name: requiredString(value.name, `第 ${index} 条规则名称`),
    priority: requiredPositiveInteger(value.priority, `第 ${index} 条规则优先级`),
    action: requiredAction(value.action, index),
    status_codes: optionalStatusCodes(value.status_codes, index),
    error_codes: optionalErrorCodeList(value.error_codes, `第 ${index} 条规则错误码`),
    error_types: optionalStringList(value.error_types, `第 ${index} 条规则错误类型`),
    keywords: optionalStringList(value.keywords, `第 ${index} 条规则关键字`),
    description: optionalText(value.description, `第 ${index} 条规则描述`)
  }
  if (rule.enabled && !hasMatcher(rule)) {
    throw new Error(`第 ${index} 条规则至少需要一个匹配条件`)
  }
  if (rule.action === 'rate_limited') {
    rule.reset_strategy = requiredResetStrategy(value.reset_strategy, index)
    if (rule.reset_strategy === 'duration') {
      rule.duration_hours = requiredPositiveInteger(value.duration_hours, `第 ${index} 条限流规则恢复小时数`)
    } else if (rule.reset_strategy === 'daily') {
      rule.daily_reset_hour = requiredHour(value.daily_reset_hour, `第 ${index} 条限流规则每日恢复小时`)
    } else {
      rule.weekly_reset_day = requiredWeekday(value.weekly_reset_day, `第 ${index} 条限流规则每周恢复日期`)
      rule.weekly_reset_hour = requiredHour(value.weekly_reset_hour, `第 ${index} 条限流规则每周恢复小时`)
    }
  }
  return rule
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function assertOnlyKeys(value: Record<string, unknown>, keys: readonly string[], label: string): void {
  const allowed = new Set(keys)
  const unexpected = Object.keys(value).find((key) => !allowed.has(key))
  if (unexpected) {
    throw new Error(`${label}包含不支持字段：${unexpected}`)
  }
}

function requiredBoolean(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') throw new Error(`${label}必须是布尔值`)
  return value
}

function requiredString(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label}不能为空`)
  return value.trim()
}

function requiredPositiveInteger(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value <= 0) {
    throw new Error(`${label}必须是大于 0 的整数`)
  }
  return value
}

function requiredHour(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 23) {
    throw new Error(`${label}必须是 0-23 的整数`)
  }
  return value
}

function requiredWeekday(value: unknown, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < 0 || value > 6) {
    throw new Error(`${label}必须是 0-6 的整数`)
  }
  return value
}

function requiredAction(value: unknown, index: number): AccountErrorHandlingRuleAction {
  if (value === 'retry_next' || value === 'temp_unschedulable' || value === 'rate_limited' || value === 'error_disabled') {
    return value
  }
  throw new Error(`第 ${index} 条规则错误处理动作无效`)
}

function requiredResetStrategy(value: unknown, index: number): AccountErrorHandlingRuleResetStrategy {
  if (value === 'duration' || value === 'daily' || value === 'weekly') {
    return value
  }
  throw new Error(`第 ${index} 条限流规则恢复策略无效`)
}

function optionalStatusCodes(value: unknown, index: number): number[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) throw new Error(`第 ${index} 条规则状态码必须是数字数组`)
  const output = value.map((item) => {
    if (typeof item !== 'number' || !Number.isInteger(item) || item < 100 || item > 599) {
      throw new Error(`第 ${index} 条规则状态码不合法`)
    }
    if (isSuccessStatusCode(item)) {
      throw new Error(`第 ${index} 条规则的状态码不能填写 2xx 成功状态码，例如 200`)
    }
    return item
  })
  return output.length ? [...new Set(output)] : undefined
}

function optionalStringList(value: unknown, label: string): string[] | undefined {
  if (value === undefined) return undefined
  if (!Array.isArray(value)) throw new Error(`${label}必须是字符串数组`)
  const output = value.map((item) => requiredString(item, label))
  return output.length ? [...new Set(output)] : undefined
}

function optionalErrorCodeList(value: unknown, label: string): string[] | undefined {
  const output = optionalStringList(value, label)
  if (output?.some((item) => /^\d+$/.test(item) && isSuccessStatusCode(Number(item)))) {
    throw new Error(`${label}不能填写 2xx 成功码，例如 200`)
  }
  return output
}

function optionalText(value: unknown, label: string): string | undefined {
  if (value === undefined) return undefined
  if (typeof value !== 'string') throw new Error(`${label}必须是字符串`)
  const text = value.trim()
  return text ? text : undefined
}

function hasMatcher(rule: AccountErrorHandlingRule): boolean {
  return Boolean(rule.status_codes?.length || rule.error_codes?.length || rule.error_types?.length || rule.keywords?.length)
}
