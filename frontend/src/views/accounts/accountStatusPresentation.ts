import { formatDateTime } from '@/shared/formatters'
import type { AccountListItem, AccountStatus } from '@/types/domain'
import type { AccountCapabilityDomain } from './accountCapabilityDomains'
import { accountDiagnosticMessageWithoutRepeatedFields } from './accountDiagnosticMessages'

/**
 * 时间计划闸门（时间计划闸门与账户状态解耦设计 §5.4）：`status='active'` 且 `gate=0`
 * 表示账户本身正常、只是当前不在允许时段，不参与调度；不属于停用。
 * `effectiveAvailability` 在列表投影里恒产出，缺失时按 `availabilityScheduleGate` 兜底。
 */
export function isAccountScheduleOutOfWindow(
  account: Pick<AccountListItem, 'status' | 'availabilityScheduleGate' | 'effectiveAvailability'>
): boolean {
  if (account.effectiveAvailability) return account.effectiveAvailability.status === 'schedule_out_of_window'
  return account.status === 'active' && account.availabilityScheduleGate === 0
}

/**
 * 探测模式探针（总开关设计 §2）：行缺省（旧数据/未返回）按开启处理。
 * D13 后所有场景统一适用后台自动探测开关。
 */
export function accountBackgroundProbeDisabled(account: {
  backgroundProbeEnabled?: boolean
  capabilityDomain?: AccountCapabilityDomain
}): boolean {
  return account.backgroundProbeEnabled === false
}

/** 手动模式「手动」标签（总开关设计 §9）：所有场景统一适用。 */
export function accountManualModeTag(account: Pick<AccountListItem, 'backgroundProbeEnabled' | 'capabilityDomain'>):
  { color: string; label: string } | undefined {
  if (!accountBackgroundProbeDisabled(account)) return undefined
  return { color: 'default', label: '手动' }
}

export function accountManualModeTooltipText(account: Pick<AccountListItem, 'backgroundProbeEnabled' | 'capabilityDomain'>): string {
  return accountBackgroundProbeDisabled(account)
    ? '手动模式：系统不主动探测，仅真实流量；特供 / 锁死等自动恢复功能不可用；异常需手动处理'
    : ''
}

/** 手动模式二态健康状态文案（总开关设计 §4/§9）：只显示正常/异常/停用，过程态不展示。 */
export function accountManualModeStatusText(status: AccountStatus): string {
  if (status === 'active') return '正常'
  if (status === 'disabled') return '停用'
  return '异常'
}

export function accountManualModeStatusColor(status: AccountStatus): string {
  if (status === 'active') return 'green'
  if (status === 'disabled') return 'default'
  return 'red'
}

export function accountStatusTooltipLines(account: AccountListItem): string[] {
  const manualMode = accountBackgroundProbeDisabled(account)
  const presentation = account.availabilityPresentation
  const effective = account.effectiveAvailability
  const observation = presentation?.probe?.lastObservation
  // 时间计划闸门是独立的调度事实，与手动模式二态健康映射正交：时段外仍显示「非时间段」。
  const scheduleOutOfWindow = isAccountScheduleOutOfWindow(account)
  const lines: string[] = [
    `状态：${
      scheduleOutOfWindow
        ? '非时间段'
        : manualMode
          ? accountManualModeStatusText(account.status)
          : presentation?.label ?? effective?.label ?? accountStatusFallbackLabel(account)
    }`
  ]

  if (observation) {
    // 手动模式无新检查：历史检查时间标注「关闭前」（总开关设计 §9）。
    lines.push(`最近检查：${formatDateTime(observation.attemptedAt)}（${observation.result === 'success' ? '成功' : '失败'}）${manualMode ? '（关闭前）' : ''}`)
    if (typeof observation.httpStatus === 'number') lines.push(`HTTP 状态：${observation.httpStatus}`)
    if (observation.errorCode) lines.push(`错误码：${observation.errorCode}`)
  }
  const schedule = presentation?.probe?.schedule
  if (manualMode) {
    // 手动模式不输出「下次检查」，改显主动探测关闭提示（总开关设计 §9）。
    lines.push('主动探测：已关闭（仅真实流量，手动恢复）')
  } else {
    if (!observation && schedule && schedule.state !== 'none') lines.push('最近检查：尚未执行')
    if (schedule?.state === 'scheduled' && schedule.nextAttemptAt) {
      lines.push(`下次检查：${formatDateTime(schedule.nextAttemptAt)}`)
    } else if (schedule?.state === 'due_waiting') {
      lines.push(schedule.nextAttemptAt
        ? `下次检查：等待执行（原计划 ${formatDateTime(schedule.nextAttemptAt)}）`
        : '下次检查：等待执行')
    } else if (schedule?.state === 'running') {
      lines.push('下次检查：正在检查')
    } else if (schedule?.state === 'none' && presentation?.probe) {
      lines.push('下次检查：暂无计划')
    } else if (presentation?.probe && !observation) {
      lines.push('下次检查：暂无计划')
    }
  }

  const reason = observation?.result === 'failed'
    ? (observation.reason ?? presentation?.reason ?? effective?.reason)
    : (presentation?.reason ?? observation?.reason ?? effective?.reason)
  // 时段外的说明由 accountStatusTooltipLines 统一给出口径，避免与后端 reason 重复。
  const normalizedReason = scheduleOutOfWindow ? '' : normalizeAccountStatusReason(reason, observation)
  if (normalizedReason) lines.push(`原因：${normalizedReason}`)
  if (observation?.traceId) lines.push(`traceId：${observation.traceId}`)
  return lines
}

function normalizeAccountStatusReason(
  reason: string | undefined,
  observation: { httpStatus?: number; errorCode?: string } | undefined
): string {
  let value = reason?.trim() ?? ''
  if (!value) return ''

  value = value.replace(/请联系授权人或管理员查看完整诊断/g, '')
  return accountDiagnosticMessageWithoutRepeatedFields(value, {
    statusCode: observation?.httpStatus,
    errorCode: observation?.errorCode
  })
}

function accountStatusFallbackLabel(account: AccountListItem): string {
  // 状态回退文案：列表投影缺失 label 时，时段外仍按闸门事实展示，不能退化成「可调度」。
  if (account.effectiveAvailability?.status === 'schedule_out_of_window') return '非时间段'
  const status = account.status
  if (status === 'active') return '可调度'
  if (status === 'pending_test') return '待检查'
  if (status === 'disabled') return '停用'
  if (status === 'error') return '异常'
  if (status === 'rate_limited') return '限流中'
  if (status === 'temporary_unavailable') return '临时不可调用'
  if (status === 'quality_isolated') return '质量隔离'
  return '状态异常'
}

export function accountStatusTooltipTraceId(account: AccountListItem): string | undefined {
  return account.availabilityPresentation?.probe?.lastObservation?.traceId
}
