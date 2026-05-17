import {
  accountErrorActionValues,
  type AccountErrorAction,
  type AccountErrorHandlingRulePayload,
  type AccountErrorPolicyRuleForm,
  type AccountErrorPolicyValidationResult,
  type AccountErrorRecoveryStrategy
} from './accountErrorPolicyTypes'
import {
  buildDefaultAccountErrorPolicyRules,
  cloneAccountErrorPolicyRule,
  makeAccountErrorPolicyRule
} from './accountErrorPolicyRules'

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
  return Number.isInteger(code) && code >= 100 && code <= 599 ? code : null
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

const formatList = (value: unknown): string => splitList(value).join(', ')
const formatStatusCodes = (value: unknown): string => normalizeStatusCodes(value).join(', ')

const normalizeOptionalPositiveInt = (value: unknown): number | null => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue > 0 ? Math.trunc(numberValue) : null
}

const normalizeHour = (value: unknown, fallback = 0): number => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue >= 0 && numberValue <= 23 ? Math.trunc(numberValue) : fallback
}

const normalizeWeekday = (value: unknown, fallback = 1): number => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) && numberValue >= 0 && numberValue <= 6 ? Math.trunc(numberValue) : fallback
}

const normalizeAction = (value: unknown): AccountErrorAction => {
  return accountErrorActionValues.includes(value as AccountErrorAction) ? value as AccountErrorAction : 'temp_unschedulable'
}

const normalizeRecoveryStrategy = (value: unknown): AccountErrorRecoveryStrategy => {
  return value === 'duration' || value === 'weekly' || value === 'daily' ? value : 'daily'
}

const buildRuleFromPayload = (value: unknown, index: number): AccountErrorPolicyRuleForm | null => {
  if (!value || typeof value !== 'object') return null
  const entry = value as Record<string, unknown>
  const name = String(entry.name || entry.description || '自定义错误处理规则')
  return makeAccountErrorPolicyRule({
    enabled: entry.enabled !== false,
    name,
    priority: normalizeOptionalPositiveInt(entry.priority) ?? (100 + index),
    status_codes: formatStatusCodes(entry.status_codes ?? entry.statusCodes),
    error_codes: formatList(entry.error_codes ?? entry.provider_error_codes ?? entry.providerErrorCodes),
    error_types: formatList(entry.error_types ?? entry.provider_error_types ?? entry.providerErrorTypes),
    keywords: formatList(entry.keywords),
    action: normalizeAction(entry.action ?? entry.state),
    duration_minutes: normalizeOptionalPositiveInt(entry.duration_minutes ?? entry.durationMinutes),
    reset_strategy: normalizeRecoveryStrategy(entry.reset_strategy ?? entry.recovery_strategy ?? entry.strategy),
    duration_hours: normalizeOptionalPositiveInt(entry.duration_hours ?? entry.durationHours),
    daily_reset_hour: normalizeHour(entry.daily_reset_hour ?? entry.dailyResetHour, 0),
    weekly_reset_day: normalizeWeekday(entry.weekly_reset_day ?? entry.weeklyResetDay, 1),
    weekly_reset_hour: normalizeHour(entry.weekly_reset_hour ?? entry.weeklyResetHour, 0),
    description: typeof entry.description === 'string' && entry.description.trim() !== name.trim() ? entry.description.trim() : ''
  })
}

export const loadAccountErrorPolicyRules = (credentials?: Record<string, unknown>): AccountErrorPolicyRuleForm[] => {
  if (!credentials || !Array.isArray(credentials.error_handling_rules)) {
    return buildDefaultAccountErrorPolicyRules().map(cloneAccountErrorPolicyRule)
  }
  const loaded = credentials.error_handling_rules
    .map((item, index) => buildRuleFromPayload(item, index))
    .filter((rule): rule is AccountErrorPolicyRuleForm => rule !== null)
  return loaded.length > 0 ? loaded : buildDefaultAccountErrorPolicyRules().map(cloneAccountErrorPolicyRule)
}

export const validateAccountErrorPolicyRules = (rules: AccountErrorPolicyRuleForm[]): AccountErrorPolicyValidationResult => {
  const hasEnabledRule = rules.some((rule) => rule.enabled !== false)
  if (!hasEnabledRule) return { valid: false, message: '错误处理策略至少需要保留一条启用规则' }
  for (const [index, rule] of rules.entries()) {
    if (rule.enabled === false) continue
    const ruleIndex = index + 1
    const statusCodes = normalizeStatusCodes(rule.status_codes)
    if (hasInvalidStatusCodeItems(rule.status_codes)) return { valid: false, message: `第 ${ruleIndex} 条规则的状态码不合法`, index: ruleIndex }
    const hasMatcher = statusCodes.length > 0 || splitList(rule.error_codes).length > 0 || splitList(rule.error_types).length > 0 || splitList(rule.keywords).length > 0
    if (!hasMatcher) return { valid: false, message: `第 ${ruleIndex} 条规则至少需要一个匹配条件`, index: ruleIndex }
    if (rule.action === 'temp_unschedulable' && normalizeOptionalPositiveInt(rule.duration_minutes) === null) {
      return { valid: false, message: `第 ${ruleIndex} 条规则需要填写临时避让分钟数`, index: ruleIndex }
    }
    if (rule.action === 'rate_limited' && rule.reset_strategy === 'duration' && normalizeOptionalPositiveInt(rule.duration_hours) === null) {
      return { valid: false, message: `第 ${ruleIndex} 条限流规则需要填写恢复小时数`, index: ruleIndex }
    }
  }
  return { valid: true }
}

export const buildAccountErrorPolicyPayload = (rules: AccountErrorPolicyRuleForm[]): AccountErrorHandlingRulePayload[] => {
  return rules.map((rule, index) => {
    const statusCodes = normalizeStatusCodes(rule.status_codes)
    const errorCodes = splitList(rule.error_codes)
    const errorTypes = splitList(rule.error_types)
    const keywords = splitList(rule.keywords)
    const payload: AccountErrorHandlingRulePayload = {
      enabled: rule.enabled !== false,
      name: rule.name.trim() || rule.description.trim() || '自定义错误处理规则',
      priority: normalizeOptionalPositiveInt(rule.priority) ?? (index + 1) * 10,
      action: normalizeAction(rule.action)
    }
    if (statusCodes.length > 0) payload.status_codes = statusCodes
    if (errorCodes.length > 0) payload.error_codes = errorCodes
    if (errorTypes.length > 0) payload.error_types = errorTypes
    if (keywords.length > 0) payload.keywords = keywords
    if (rule.description.trim()) payload.description = rule.description.trim()
    if (payload.action === 'temp_unschedulable') {
      payload.duration_minutes = normalizeOptionalPositiveInt(rule.duration_minutes) ?? 10
    }
    if (payload.action === 'rate_limited') {
      payload.reset_strategy = normalizeRecoveryStrategy(rule.reset_strategy)
      if (payload.reset_strategy === 'duration') {
        payload.duration_hours = normalizeOptionalPositiveInt(rule.duration_hours) ?? 5
      } else if (payload.reset_strategy === 'weekly') {
        payload.weekly_reset_day = normalizeWeekday(rule.weekly_reset_day, 1)
        payload.weekly_reset_hour = normalizeHour(rule.weekly_reset_hour, 0)
      } else {
        payload.daily_reset_hour = normalizeHour(rule.daily_reset_hour, 0)
      }
    }
    return payload
  })
}

export const writeAccountErrorPolicyToCredentials = (credentials: Record<string, unknown>, rules: AccountErrorPolicyRuleForm[]): void => {
  credentials.error_handling_rules = buildAccountErrorPolicyPayload(rules)
}
