export type AccountErrorAction = 'rate_limited' | 'temp_unschedulable' | 'error_disabled'
export type AccountErrorRecoveryStrategy = 'duration' | 'daily' | 'weekly'

export interface AccountErrorPolicyRuleForm {
  enabled: boolean
  name: string
  priority: number | null
  status_codes: string
  error_codes: string
  error_types: string
  keywords: string
  action: AccountErrorAction
  duration_minutes: number | null
  reset_strategy: AccountErrorRecoveryStrategy
  duration_hours: number | null
  daily_reset_hour: number | null
  weekly_reset_day: number | null
  weekly_reset_hour: number | null
  reset_timezone: string
  description: string
}

export interface AccountErrorPolicyPreset {
  key: string
  label: string
  rule: AccountErrorPolicyRuleForm
}

export interface AccountErrorPolicyValidationResult {
  valid: boolean
  message?: string
  index?: number
}

export interface AccountErrorHandlingRulePayload {
  enabled: boolean
  name: string
  priority: number
  status_codes?: number[]
  error_codes?: string[]
  error_types?: string[]
  keywords?: string[]
  action: AccountErrorAction
  duration_minutes?: number
  reset_strategy?: AccountErrorRecoveryStrategy
  duration_hours?: number
  daily_reset_hour?: number
  weekly_reset_day?: number
  weekly_reset_hour?: number
  reset_timezone?: string
  description?: string
}

const defaultTimezone = 'Asia/Shanghai'
const listSeparators = /[,;，；\n]/

export const accountErrorActionValues: AccountErrorAction[] = [
  'rate_limited',
  'temp_unschedulable',
  'error_disabled'
]

export const accountErrorActionOptions = [
  { label: '限流', value: 'rate_limited', description: '按恢复策略暂停账号，到期后自动恢复。' },
  { label: '临时不可调用', value: 'temp_unschedulable', description: '短暂避让指定分钟数，到期后自动恢复。' },
  { label: '错误', value: 'error_disabled', description: '只有显式配置这个动作才会把账号置为错误。' }
]

export const accountErrorRecoveryStrategyOptions = [
  { label: '固定时长', value: 'duration' },
  { label: '每天固定时间', value: 'daily' },
  { label: '每周固定时间', value: 'weekly' }
]

export const accountErrorTimezoneOptions = [
  'Asia/Shanghai',
  'UTC',
  'Asia/Tokyo',
  'Asia/Seoul',
  'Asia/Singapore',
  'Asia/Kolkata',
  'Europe/London',
  'Europe/Paris',
  'Europe/Berlin',
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'Australia/Sydney'
]

export const accountErrorHourOptions = Array.from({ length: 24 }, (_, index) => ({ label: `${String(index).padStart(2, '0')}:00`, value: index }))

export const accountErrorWeekdayOptions = [
  { label: '周一', value: 1 },
  { label: '周二', value: 2 },
  { label: '周三', value: 3 },
  { label: '周四', value: 4 },
  { label: '周五', value: 5 },
  { label: '周六', value: 6 },
  { label: '周日', value: 0 }
]

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

const makeRule = (patch: Partial<AccountErrorPolicyRuleForm>): AccountErrorPolicyRuleForm => ({
  enabled: true,
  name: '',
  priority: 100,
  status_codes: '',
  error_codes: '',
  error_types: '',
  keywords: '',
  action: 'temp_unschedulable',
  duration_minutes: 10,
  reset_strategy: 'daily',
  duration_hours: 5,
  daily_reset_hour: 0,
  weekly_reset_day: 1,
  weekly_reset_hour: 0,
  reset_timezone: defaultTimezone,
  description: '',
  ...patch
})

export const createBlankAccountErrorRule = (priority = 100): AccountErrorPolicyRuleForm => makeRule({
  name: '自定义错误处理规则',
  priority,
  action: 'temp_unschedulable',
  duration_minutes: 10
})

export const cloneAccountErrorPolicyRule = (rule: AccountErrorPolicyRuleForm): AccountErrorPolicyRuleForm => ({ ...rule })

const tempRule = (name: string, priority: number, codes: string, durationMinutes: number, description: string): AccountErrorPolicyRuleForm => makeRule({
  name,
  priority,
  status_codes: codes,
  action: 'temp_unschedulable',
  duration_minutes: durationMinutes,
  description
})

const temp529Rule = (): AccountErrorPolicyRuleForm => tempRule(
  '529 临时不可调用',
  100,
  '529',
  10,
  '529 响应，按临时不可调用短暂避让后自动恢复'
)

const newAPIQuotaRule = (): AccountErrorPolicyRuleForm => makeRule({
  name: 'OpenAI/NewAPI 用户额度不足',
  priority: 10,
  status_codes: '403',
  error_codes: 'insufficient_user_quota',
  error_types: 'new_api_error',
  keywords: '用户额度不足, 剩余额度, insufficient_user_quota',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  reset_timezone: defaultTimezone,
  description: '403 但语义是用户额度不足，按限流处理到次日恢复'
})

const balance402Rule = (): AccountErrorPolicyRuleForm => makeRule({
  name: '402 服务商余额不足',
  priority: 20,
  status_codes: '402',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  reset_timezone: defaultTimezone,
  description: '余额不足或支付要求，切换其他账号并在固定时间恢复'
})

const dailyLimit429Rule = (): AccountErrorPolicyRuleForm => makeRule({
  name: '429 日额度耗尽',
  priority: 30,
  status_codes: '429',
  keywords: 'DAILY_LIMIT_EXCEEDED, daily usage limit exceeded, daily quota, 日额度, 每日额度',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  reset_timezone: defaultTimezone,
  description: '同为 429 时，仅包含日额度关键词才按限流到次日恢复'
})

export const buildDefaultAccountErrorPolicyRules = (): AccountErrorPolicyRuleForm[] => [
  tempRule('429 临时限流', 40, '429', 10, '普通 429 只短暂避让；余额不足/日额度耗尽请按账号单独添加 402/403/429 余额预设'),
  temp529Rule(),
  tempRule('503 服务不可用', 110, '503', 10, '上游维护或暂不可用，临时避让 10 分钟'),
  tempRule('502 网关错误', 120, '502', 10, '上游网关错误，临时避让 10 分钟'),
  tempRule('500 上游错误', 130, '500', 5, '上游内部错误，短暂避让 5 分钟')
]

export const accountErrorPolicyPresets: AccountErrorPolicyPreset[] = [
  { key: 'quota_403', label: '403 余额不足', rule: newAPIQuotaRule() },
  { key: 'balance_402', label: '402 余额不足', rule: balance402Rule() },
  { key: 'daily_429', label: '429 日额度', rule: dailyLimit429Rule() },
  { key: 'auth_401', label: '401 认证失败', rule: makeRule({ name: '401 认证失败', priority: 200, status_codes: '401', action: 'error_disabled', description: '非 OAuth 账号认证失败时标记错误；OAuth 账号慎用，避免覆盖刷新逻辑' }) },
  { key: 'forbidden_403', label: '403 不可访问', rule: makeRule({ name: '403 不可访问', priority: 210, status_codes: '403', action: 'error_disabled', description: '权限不足、账号被禁用或供应商拒绝访问时标记错误' }) },
  { key: 'method_405', label: '405 不支持', rule: makeRule({ name: '405 方法不支持', priority: 220, status_codes: '405', action: 'error_disabled', description: '上游接口不支持当前请求方法或端点时标记错误' }) },
  { key: 'temporary_429', label: '429 临时限流', rule: tempRule('429 临时限流', 230, '429', 10, '普通 429 短暂避让；如需保留响应头限流逻辑可不启用此规则') },
  { key: 'temporary_529', label: '529 临时不可调用', rule: temp529Rule() },
  { key: 'server_503', label: '503 维护', rule: tempRule('503 服务不可用', 240, '503', 10, '上游维护或暂不可用，临时避让 10 分钟') },
  { key: 'gateway_502', label: '502 网关', rule: tempRule('502 网关错误', 250, '502', 10, '上游网关错误，临时避让 10 分钟') },
  { key: 'server_500', label: '500 错误', rule: tempRule('500 上游错误', 260, '500', 5, '上游内部错误，短暂避让 5 分钟') }
]

const buildRuleFromPayload = (value: unknown, index: number): AccountErrorPolicyRuleForm | null => {
  if (!value || typeof value !== 'object') return null
  const entry = value as Record<string, unknown>
  const name = String(entry.name || entry.description || '自定义错误处理规则')
  return makeRule({
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
    reset_timezone: String(entry.reset_timezone || entry.timezone || defaultTimezone),
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

export const normalizeAccountErrorPolicyPriorities = (rules: AccountErrorPolicyRuleForm[]): AccountErrorPolicyRuleForm[] => {
  return rules.map((rule, index) => ({ ...rule, priority: (index + 1) * 10 }))
}

export const getNextAccountErrorRulePriority = (rules: AccountErrorPolicyRuleForm[]): number => {
  const maxPriority = rules.reduce((max, rule) => {
    const priority = Number(rule.priority)
    return Number.isFinite(priority) && priority > max ? priority : max
  }, 0)
  return Math.max(10, Math.trunc(maxPriority / 10) * 10 + 10)
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
        payload.reset_timezone = rule.reset_timezone || defaultTimezone
      } else {
        payload.daily_reset_hour = normalizeHour(rule.daily_reset_hour, 0)
        payload.reset_timezone = rule.reset_timezone || defaultTimezone
      }
    }
    return payload
  })
}

export const writeAccountErrorPolicyToCredentials = (credentials: Record<string, unknown>, rules: AccountErrorPolicyRuleForm[]): void => {
  credentials.error_handling_rules = buildAccountErrorPolicyPayload(rules)
}

