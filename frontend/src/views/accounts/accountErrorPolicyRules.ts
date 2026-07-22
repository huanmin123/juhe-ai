import type { AccountErrorPolicyPreset, AccountErrorPolicyRuleForm } from './accountErrorPolicyTypes'

export const makeAccountErrorPolicyRule = (patch: Partial<AccountErrorPolicyRuleForm>): AccountErrorPolicyRuleForm => ({
  enabled: true,
  name: '',
  priority: 1,
  status_codes: '',
  error_codes: '',
  error_types: '',
  keywords: '',
  action: 'temp_unschedulable',
  reset_strategy: 'daily',
  duration_hours: 5,
  daily_reset_hour: 0,
  weekly_reset_day: 1,
  weekly_reset_hour: 0,
  description: '',
  ...patch
})

export const createBlankAccountErrorRule = (priority = 1): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: '自定义错误处理规则',
  priority,
  action: 'temp_unschedulable'
})

export const cloneAccountErrorPolicyRule = (rule: AccountErrorPolicyRuleForm): AccountErrorPolicyRuleForm => ({ ...rule })

const tempRule = (name: string, priority: number, codes: string, description: string): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name,
  priority,
  status_codes: codes,
  action: 'temp_unschedulable',
  description
})

const temp529Rule = (): AccountErrorPolicyRuleForm => tempRule(
  '529 临时不可调用',
  8,
  '529',
  '529 响应，进入临时不可调用统一恢复通道'
)

const upstreamQuota403Rule = (): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: '403 上游额度不足',
  priority: 1,
  status_codes: '403',
  error_codes: 'insufficient_quota',
  keywords: 'quota exceeded, billing hard limit, 用户额度不足, 剩余额度',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  description: '403 且上游明确提示额度不足时，按限流处理到次日恢复'
})

const balance402Rule = (): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: '402 服务商余额不足',
  priority: 2,
  status_codes: '402',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  description: '余额不足或支付要求，切换其他账号并在固定时间恢复'
})

const dailyLimit429Rule = (): AccountErrorPolicyRuleForm => makeAccountErrorPolicyRule({
  name: '429 日额度耗尽',
  priority: 3,
  status_codes: '429',
  keywords: 'DAILY_LIMIT_EXCEEDED, daily usage limit exceeded, daily quota, 日额度, 每日额度',
  action: 'rate_limited',
  reset_strategy: 'daily',
  daily_reset_hour: 0,
  description: '同为 429 时，仅包含日额度关键词才按限流到次日恢复'
})

export const accountErrorPolicyPresets: AccountErrorPolicyPreset[] = [
  { key: 'quota_403', label: '403 额度不足', rule: upstreamQuota403Rule() },
  { key: 'balance_402', label: '402 余额不足', rule: balance402Rule() },
  { key: 'daily_429', label: '429 日额度', rule: dailyLimit429Rule() },
  { key: 'auth_401', label: '401 认证失败', rule: makeAccountErrorPolicyRule({ name: '401 认证失败', priority: 4, status_codes: '401', action: 'error_disabled', description: '非 OAuth 账号认证失败时标记异常；OAuth 账号慎用，避免覆盖刷新逻辑' }) },
  { key: 'forbidden_403', label: '403 不可访问', rule: makeAccountErrorPolicyRule({ name: '403 不可访问', priority: 5, status_codes: '403', action: 'error_disabled', description: '权限不足、账号被禁用或供应商拒绝访问时标记异常' }) },
  { key: 'method_405', label: '405 不支持', rule: makeAccountErrorPolicyRule({ name: '405 方法不支持', priority: 6, status_codes: '405', action: 'error_disabled', description: '上游接口不支持当前请求方法或端点时标记异常' }) },
  { key: 'temporary_429', label: '429 临时限流', rule: tempRule('429 临时限流', 7, '429', '普通 429 进入临时不可调用统一恢复通道；如需保留响应头限流逻辑可不启用此规则') },
  { key: 'temporary_529', label: '529 临时不可调用', rule: temp529Rule() },
  { key: 'server_503', label: '503 维护', rule: tempRule('503 服务不可用', 9, '503', '上游维护或暂不可用，进入临时不可调用统一恢复通道') },
  { key: 'gateway_502', label: '502 网关', rule: tempRule('502 网关错误', 10, '502', '上游网关错误，进入临时不可调用统一恢复通道') },
  { key: 'server_500', label: '500 错误', rule: tempRule('500 上游错误', 11, '500', '上游内部错误，进入临时不可调用统一恢复通道') }
]

export const normalizeAccountErrorPolicyPriorities = (rules: AccountErrorPolicyRuleForm[]): AccountErrorPolicyRuleForm[] => {
  return rules.map((rule, index) => ({ ...rule, priority: index + 1 }))
}

export const getNextAccountErrorRulePriority = (rules: AccountErrorPolicyRuleForm[]): number => {
  const used = new Set(rules
    .map((rule) => rule.priority)
    .filter((priority): priority is number => typeof priority === 'number' && Number.isInteger(priority) && priority > 0 && priority <= 9999))
  for (let priority = 1; priority <= 9999; priority += 1) {
    if (!used.has(priority)) return priority
  }
  return 9999
}
