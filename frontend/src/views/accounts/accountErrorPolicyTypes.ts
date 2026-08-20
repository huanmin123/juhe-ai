export type AccountErrorAction = 'retry_next' | 'rate_limited' | 'temp_unschedulable' | 'error_disabled'
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
  reset_strategy: AccountErrorRecoveryStrategy
  duration_hours: number | null
  daily_reset_hour: number | null
  weekly_reset_day: number | null
  weekly_reset_hour: number | null
  description: string
}

/** A system rule rendered in the same list but never included in save payloads. */
export interface AccountErrorPolicyInheritedRule extends AccountErrorPolicyRuleForm {
  id: string
  source: 'system'
  inherited: true
  editable: false
}

/**
 * 新建和克隆账户尚未拥有高级详情 DTO 时的只读展示预览。保存后必须以
 * 后端 effectiveErrorHandlingRules 为准；该对象从不进入账户凭据 payload。
 */
export function systemInheritedErrorPolicyRulesPreview(): AccountErrorPolicyInheritedRule[] {
  return [{
    id: 'system.upstream_insufficient_quota',
    source: 'system',
    inherited: true,
    editable: false,
    enabled: true,
    name: '上游额度不足',
    priority: 1,
    status_codes: '403',
    error_codes: [
      'insufficient_user_quota',
      'insufficient_quota',
      'insufficient_balance',
      'quota_exceeded',
      'quota_exhausted',
      'wallet_balance_exhausted',
      'pre_consume_token_quota_failed'
    ].join(', '),
    error_types: '',
    keywords: '余额不足, 额度不足, insufficient balance, insufficient quota, credit balance too low, wallet balance exhausted',
    action: 'error_disabled',
    reset_strategy: 'duration',
    duration_hours: null,
    daily_reset_hour: null,
    weekly_reset_day: null,
    weekly_reset_hour: null,
    description: '仅在 HTTP 403 且上游明确表示余额或额度不足时将账户标记为异常。'
  }]
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
  reset_strategy?: AccountErrorRecoveryStrategy
  duration_hours?: number
  daily_reset_hour?: number
  weekly_reset_day?: number
  weekly_reset_hour?: number
  description?: string
}

export const accountErrorActionValues: AccountErrorAction[] = [
  'retry_next',
  'rate_limited',
  'temp_unschedulable',
  'error_disabled'
]

export const accountErrorActionOptions = [
  { label: '只切号', value: 'retry_next', description: '本次请求切换下一个账号，不改变账号状态。' },
  { label: '限流', value: 'rate_limited', description: '按恢复策略暂停账号，到期后自动恢复。' },
  { label: '临时不可调用', value: 'temp_unschedulable', description: '进入系统统一恢复通道，到期后由后台复测恢复。' },
  { label: '异常', value: 'error_disabled', description: '只有显式配置这个动作才会把账号置为异常。' }
]

export const accountErrorRecoveryStrategyOptions = [
  { label: '固定时长', value: 'duration' },
  { label: '每天固定时间', value: 'daily' },
  { label: '每周固定时间', value: 'weekly' }
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
