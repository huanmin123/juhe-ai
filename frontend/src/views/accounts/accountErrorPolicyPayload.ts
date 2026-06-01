import {
  accountErrorActionValues,
  type AccountErrorAction,
  type AccountErrorHandlingRulePayload,
  type AccountErrorPolicyRuleForm,
  type AccountErrorPolicyValidationResult,
  type AccountErrorRecoveryStrategy
} from './accountErrorPolicyTypes'
import { makeAccountErrorPolicyRule } from './accountErrorPolicyRules'

const listSeparators = /[,;，；\n]/

const splitList = (value: unknown): string[] => {
  if (Array.isArray(value)) {
    return value.map((item) => String(item).trim()).filter(Boolean)
  }
  if (typeof value !== 'string') {
    return value == null ? [] : [String(value).trim()].filter(Boolean)
  }
  return value.split(listSeparators).map((item) => item.trim()).filter(Boolean)
}

const getStatusCodeItems = (value: unknown): unknown[] => {
  if (Array.isArray(value)) return value
  if (typeof value === 'string') return value.split(listSeparators)
  return value == null ? [] : [value]
}

const normalizeStatusCodeItem = (item: unknown): number | null => {
  const text = String(item).trim()
  if (!/^\d+$/.test(text)) return null
  const code = Number(text)
  return Number.isInteger(code) && code >= 100 && code <= 599 && !isSuccessStatusCode(code) ? code : null
}

const normalizeStatusCodes = (value: unknown): number[] => {
  const seen = new Set<number>()
  const output: number[] = []
  for (const item of getStatusCodeItems(value)) {
    const code = normalizeStatusCodeItem(item)
    if (code === null || seen.has(code)) continue
    seen.add(code)
    output.push(code)
  }
  return output
}

const hasInvalidStatusCodeItems = (value: unknown): boolean => {
  return getStatusCodeItems(value)
    .map((item) => String(item).trim())
    .filter(Boolean)
    .some((item) => normalizeStatusCodeItem(item) === null)
}

const hasSuccessStatusCodeItems = (value: unknown): boolean => {
  return getStatusCodeItems(value)
    .map((item) => String(item).trim())
    .filter(Boolean)
    .some((item) => /^\d+$/.test(item) && isSuccessStatusCode(Number(item)))
}

const hasSuccessErrorCodeItems = (value: unknown): boolean => {
  return splitList(value).some((item) => /^\d+$/.test(item) && isSuccessStatusCode(Number(item)))
}

const isSuccessStatusCode = (code: number): boolean => code >= 200 && code <= 299

const formatList = (value: unknown): string => splitList(value).join(', ')
const formatStatusCodes = (value: unknown): string => normalizeStatusCodes(value).join(', ')

const normalizeOptionalPositiveInt = (value: unknown): number | null => {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value > 0 ? value : null
}

const normalizeOptionalHour = (value: unknown): number | null => {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value >= 0 && value <= 23 ? value : null
}

const normalizeOptionalWeekday = (value: unknown): number | null => {
  return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value) && value >= 0 && value <= 6 ? value : null
}

const normalizeOptionalAction = (value: unknown): AccountErrorAction | null => {
  return accountErrorActionValues.includes(value as AccountErrorAction) ? value as AccountErrorAction : null
}

const normalizeOptionalRecoveryStrategy = (value: unknown): AccountErrorRecoveryStrategy | null => {
  return value === 'duration' || value === 'weekly' || value === 'daily' ? value : null
}

function payloadString(value: unknown, label: string): string {
  if (typeof value !== 'string' || !value.trim()) throw new Error(`${label}无效`)
  return value.trim()
}

function payloadBoolean(value: unknown, label: string): boolean {
  if (typeof value !== 'boolean') throw new Error(`${label}无效`)
  return value
}

function payloadPositiveInt(value: unknown, label: string): number {
  const numberValue = payloadOptionalPositiveInt(value, label)
  if (numberValue === null) throw new Error(`${label}无效`)
  return numberValue
}

function payloadOptionalPositiveInt(value: unknown, label: string): number | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value <= 0) throw new Error(`${label}无效`)
  return value
}

function payloadOptionalHour(value: unknown, label: string): number | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value < 0 || value > 23) throw new Error(`${label}无效`)
  return value
}

function payloadHour(value: unknown, label: string): number {
  const numberValue = payloadOptionalHour(value, label)
  if (numberValue === null) throw new Error(`${label}无效`)
  return numberValue
}

function payloadOptionalWeekday(value: unknown, label: string): number | null {
  if (value === undefined || value === null) return null
  if (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value < 0 || value > 6) throw new Error(`${label}无效`)
  return value
}

function payloadWeekday(value: unknown, label: string): number {
  const numberValue = payloadOptionalWeekday(value, label)
  if (numberValue === null) throw new Error(`${label}无效`)
  return numberValue
}

function payloadAction(value: unknown): AccountErrorAction {
  if (!accountErrorActionValues.includes(value as AccountErrorAction)) throw new Error('错误处理动作无效')
  return value as AccountErrorAction
}

function payloadRecoveryStrategy(value: unknown): AccountErrorRecoveryStrategy {
  if (value !== 'duration' && value !== 'weekly' && value !== 'daily') throw new Error('恢复策略无效')
  return value
}

function formatPayloadStringList(value: unknown, label: string): string {
  if (value === undefined) return ''
  if (!Array.isArray(value)) throw new Error(`${label}必须是字符串数组`)
  return value.map((item) => payloadString(item, label)).join(', ')
}

function formatPayloadStatusCodes(value: unknown): string {
  if (value === undefined) return ''
  if (!Array.isArray(value)) throw new Error('状态码必须是数字数组')
  return value.map((item) => {
    if (typeof item !== 'number' || !Number.isInteger(item) || item < 100 || item > 599 || isSuccessStatusCode(item)) {
      throw new Error('状态码无效')
    }
    return String(item)
  }).join(', ')
}

const buildRuleFromPayload = (value: unknown): AccountErrorPolicyRuleForm => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('错误处理策略规则格式无效')
  const entry = value as Record<string, unknown>
  const name = payloadString(entry.name, '规则名称')
  const action = payloadAction(entry.action)
  const resetStrategy = action === 'rate_limited'
    ? payloadRecoveryStrategy(entry.reset_strategy)
    : entry.reset_strategy === undefined ? 'daily' : payloadRecoveryStrategy(entry.reset_strategy)
  return makeAccountErrorPolicyRule({
    enabled: payloadBoolean(entry.enabled, '启用状态'),
    name,
    priority: payloadPositiveInt(entry.priority, '优先级'),
    status_codes: formatPayloadStatusCodes(entry.status_codes),
    error_codes: formatPayloadStringList(entry.error_codes, '错误码'),
    error_types: formatPayloadStringList(entry.error_types, '错误类型'),
    keywords: formatPayloadStringList(entry.keywords, '关键字'),
    action,
    durationMinutes: payloadOptionalPositiveInt(entry.durationMinutes, '临时避让分钟数'),
    reset_strategy: resetStrategy,
    duration_hours: payloadOptionalPositiveInt(entry.duration_hours, '恢复小时数'),
    daily_reset_hour: payloadOptionalHour(entry.daily_reset_hour, '每日恢复小时'),
    weekly_reset_day: payloadOptionalWeekday(entry.weekly_reset_day, '每周恢复日期'),
    weekly_reset_hour: payloadOptionalHour(entry.weekly_reset_hour, '每周恢复小时'),
    description: typeof entry.description === 'string' && entry.description.trim() !== name.trim() ? entry.description.trim() : ''
  })
}

export const loadAccountErrorPolicyRules = (credentials?: Record<string, unknown>): AccountErrorPolicyRuleForm[] => {
  if (!credentials || !Array.isArray(credentials.error_handling_rules)) {
    return []
  }
  return credentials.error_handling_rules.map(buildRuleFromPayload)
}

export const validateAccountErrorPolicyRules = (rules: AccountErrorPolicyRuleForm[]): AccountErrorPolicyValidationResult => {
  for (const [index, rule] of rules.entries()) {
    const ruleIndex = index + 1
    if (typeof rule.enabled !== 'boolean') return { valid: false, message: `第 ${ruleIndex} 条规则启用状态无效`, index: ruleIndex }
    if (typeof rule.name !== 'string' || !rule.name.trim()) return { valid: false, message: `第 ${ruleIndex} 条规则名称不能为空`, index: ruleIndex }
    if (normalizeOptionalPositiveInt(rule.priority) === null) return { valid: false, message: `第 ${ruleIndex} 条规则优先级必须是大于 0 的整数`, index: ruleIndex }
    const action = normalizeOptionalAction(rule.action)
    if (!action) return { valid: false, message: `第 ${ruleIndex} 条规则错误处理动作无效`, index: ruleIndex }
    const statusCodes = normalizeStatusCodes(rule.status_codes)
    if (hasSuccessStatusCodeItems(rule.status_codes)) return { valid: false, message: `第 ${ruleIndex} 条规则的状态码不能填写 2xx 成功状态码，例如 200`, index: ruleIndex }
    if (hasInvalidStatusCodeItems(rule.status_codes)) return { valid: false, message: `第 ${ruleIndex} 条规则的状态码不合法`, index: ruleIndex }
    if (hasSuccessErrorCodeItems(rule.error_codes)) return { valid: false, message: `第 ${ruleIndex} 条规则的错误码不能填写 2xx 成功码，例如 200`, index: ruleIndex }
    const hasMatcher = statusCodes.length > 0 || splitList(rule.error_codes).length > 0 || splitList(rule.error_types).length > 0 || splitList(rule.keywords).length > 0
    if (rule.enabled && !hasMatcher) return { valid: false, message: `第 ${ruleIndex} 条规则至少需要一个匹配条件`, index: ruleIndex }
    if (action === 'temp_unschedulable' && normalizeOptionalPositiveInt(rule.durationMinutes) === null) {
      return { valid: false, message: `第 ${ruleIndex} 条规则需要填写临时避让分钟数`, index: ruleIndex }
    }
    if (action === 'rate_limited') {
      const resetStrategy = normalizeOptionalRecoveryStrategy(rule.reset_strategy)
      if (!resetStrategy) return { valid: false, message: `第 ${ruleIndex} 条限流规则恢复策略无效`, index: ruleIndex }
      if (resetStrategy === 'duration' && normalizeOptionalPositiveInt(rule.duration_hours) === null) {
        return { valid: false, message: `第 ${ruleIndex} 条限流规则需要填写恢复小时数`, index: ruleIndex }
      }
      if (resetStrategy === 'daily' && normalizeOptionalHour(rule.daily_reset_hour) === null) {
        return { valid: false, message: `第 ${ruleIndex} 条限流规则每日恢复小时必须是 0-23 的整数`, index: ruleIndex }
      }
      if (resetStrategy === 'weekly' && normalizeOptionalWeekday(rule.weekly_reset_day) === null) {
        return { valid: false, message: `第 ${ruleIndex} 条限流规则每周恢复日期必须是 0-6 的整数`, index: ruleIndex }
      }
      if (resetStrategy === 'weekly' && normalizeOptionalHour(rule.weekly_reset_hour) === null) {
        return { valid: false, message: `第 ${ruleIndex} 条限流规则每周恢复小时必须是 0-23 的整数`, index: ruleIndex }
      }
    }
  }
  return { valid: true }
}

export const buildAccountErrorPolicyPayload = (rules: AccountErrorPolicyRuleForm[]): AccountErrorHandlingRulePayload[] => {
  return rules.map((rule) => {
    if (hasSuccessStatusCodeItems(rule.status_codes)) throw new Error('状态码不能填写 2xx 成功状态码')
    if (hasInvalidStatusCodeItems(rule.status_codes)) throw new Error('状态码不合法')
    if (hasSuccessErrorCodeItems(rule.error_codes)) throw new Error('错误码不能填写 2xx 成功码')
    const statusCodes = normalizeStatusCodes(rule.status_codes)
    const errorCodes = splitList(rule.error_codes)
    const errorTypes = splitList(rule.error_types)
    const keywords = splitList(rule.keywords)
    const action = payloadAction(rule.action)
    const description = typeof rule.description === 'string' ? rule.description.trim() : ''
    const payload: AccountErrorHandlingRulePayload = {
      enabled: payloadBoolean(rule.enabled, '启用状态'),
      name: payloadString(rule.name, '规则名称'),
      priority: payloadPositiveInt(rule.priority, '优先级'),
      action
    }
    if (statusCodes.length > 0) payload.status_codes = statusCodes
    if (errorCodes.length > 0) payload.error_codes = errorCodes
    if (errorTypes.length > 0) payload.error_types = errorTypes
    if (keywords.length > 0) payload.keywords = keywords
    if (description) payload.description = description
    if (payload.action === 'temp_unschedulable') {
      payload.durationMinutes = payloadPositiveInt(rule.durationMinutes, '临时避让分钟数')
    }
    if (payload.action === 'rate_limited') {
      payload.reset_strategy = payloadRecoveryStrategy(rule.reset_strategy)
      if (payload.reset_strategy === 'duration') {
        payload.duration_hours = payloadPositiveInt(rule.duration_hours, '恢复小时数')
      } else if (payload.reset_strategy === 'weekly') {
        payload.weekly_reset_day = payloadWeekday(rule.weekly_reset_day, '每周恢复日期')
        payload.weekly_reset_hour = payloadHour(rule.weekly_reset_hour, '每周恢复小时')
      } else {
        payload.daily_reset_hour = payloadHour(rule.daily_reset_hour, '每日恢复小时')
      }
    }
    return payload
  })
}

export const writeAccountErrorPolicyToCredentials = (credentials: Record<string, unknown>, rules: AccountErrorPolicyRuleForm[]): void => {
  const payload = buildAccountErrorPolicyPayload(rules)
  if (payload.length > 0) {
    credentials.error_handling_rules = payload
  } else {
    delete credentials.error_handling_rules
  }
}
