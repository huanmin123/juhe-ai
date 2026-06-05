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
