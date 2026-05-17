import type { AccountErrorPolicyPreset, AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'

export const makeAccountErrorPolicyRule = (patch: Partial<AccountErrorPolicyRuleForm>): AccountErrorPolicyRuleForm => ({
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
  description: '',
  ...patch
})

export const createBlankAccountErrorRule = (priority = 100): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: '自定义错误处理规则',
  priority,
  action: 'temp_unschedulable',
  duration_minutes: 10
})

export const cloneAccountErrorPolicyRule = (rule: AccountErrorPolicyRuleForm): AccountErrorPolicyRuleForm => ({ ...rule })

const tempRule = (name: string, priority: number, codes: string, durationMinutes: number, description: string): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
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

const newAPIQuotaRule = (): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: 'OpenAI/NewAPI 用户额度不足',
  priority: 10,
  status_codes: '403',
  error_codes: 'insufficient_user_quota',
  error_types: 'new_api_error',
  keywords: '用户额度不足, 剩余额度, insufficient_user_quota',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  description: '403 但语义是用户额度不足，按限流处理到次日恢复'
})

const balance402Rule = (): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: '402 服务商余额不足',
  priority: 20,
  status_codes: '402',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  description: '余额不足或支付要求，切换其他账号并在固定时间恢复'
})

const dailyLimit429Rule = (): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: '429 日额度耗尽',
  priority: 30,
  status_codes: '429',
  keywords: 'DAILY_LIMIT_EXCEEDED, daily usage limit exceeded, daily quota, 日额度, 每日额度',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
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
  { key: 'auth_401', label: '401 认证失败', rule: makeAccountErrorPolicyRule({ name: '401 认证失败', priority: 200, status_codes: '401', action: 'error_disabled', description: '非 OAuth 账号认证失败时标记异常；OAuth 账号慎用，避免覆盖刷新逻辑' }) },
  { key: 'forbidden_403', label: '403 不可访问', rule: makeAccountErrorPolicyRule({ name: '403 不可访问', priority: 210, status_codes: '403', action: 'error_disabled', description: '权限不足、账号被禁用或供应商拒绝访问时标记异常' }) },
  { key: 'method_405', label: '405 不支持', rule: makeAccountErrorPolicyRule({ name: '405 方法不支持', priority: 220, status_codes: '405', action: 'error_disabled', description: '上游接口不支持当前请求方法或端点时标记异常' }) },
  { key: 'temporary_429', label: '429 临时限流', rule: tempRule('429 临时限流', 230, '429', 10, '普通 429 短暂避让；如需保留响应头限流逻辑可不启用此规则') },
  { key: 'temporary_529', label: '529 临时不可调用', rule: temp529Rule() },
  { key: 'server_503', label: '503 维护', rule: tempRule('503 服务不可用', 240, '503', 10, '上游维护或暂不可用，临时避让 10 分钟') },
  { key: 'gateway_502', label: '502 网关', rule: tempRule('502 网关错误', 250, '502', 10, '上游网关错误，临时避让 10 分钟') },
  { key: 'server_500', label: '500 错误', rule: tempRule('500 上游错误', 260, '500', 5, '上游内部错误，短暂避让 5 分钟') }
]

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
