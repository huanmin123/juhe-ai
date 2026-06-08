import {
  accountErrorActionOptions,
  type AccountErrorAction,
  type AccountErrorPolicyRuleForm
} from './accountErrorPolicyTypes'

export const accountErrorActionSelectOptions = accountErrorActionOptions.map((item) => ({
  label: item.label,
  value: item.value
}))

export function accountErrorRuleKey(index: number): string {
  return `rule-${index}`
}

export function accountErrorActionLabel(action: AccountErrorAction): string {
  return accountErrorActionOptions.find((item) => item.value === action)?.label ?? action
}

export function accountErrorActionColor(action: AccountErrorAction): string {
  if (action === 'retry_next') return 'blue'
  if (action === 'rate_limited') return 'orange'
  if (action === 'error_disabled') return 'red'
  return 'gold'
}

function compactAccountErrorValue(value: string): string {
  return value.trim().replace(/\s+/g, ' ')
}

export function accountErrorRuleConditionSummary(rule: AccountErrorPolicyRuleForm): string {
  const parts = [
    compactAccountErrorValue(rule.status_codes) ? `状态 ${compactAccountErrorValue(rule.status_codes)}` : '',
    compactAccountErrorValue(rule.error_codes) ? `码 ${compactAccountErrorValue(rule.error_codes)}` : '',
    compactAccountErrorValue(rule.error_types) ? `类型 ${compactAccountErrorValue(rule.error_types)}` : '',
    compactAccountErrorValue(rule.keywords) ? `关键词 ${compactAccountErrorValue(rule.keywords)}` : ''
  ].filter(Boolean)
  return parts.length > 0 ? parts.join(' / ') : '未配置匹配条件'
}
