import { formatDateTime } from '@/shared/formatters'
import type { AccountListItem, AccountStatus } from '@/types/domain'
import { capabilityDomainWithoutBackgroundProbe, type AccountCapabilityDomain } from './accountCapabilityDomains'
import { accountDiagnosticMessageWithoutRepeatedFields } from './accountDiagnosticMessages'

/**
 * 探测模式探针（总开关设计 §2）：行缺省（旧数据/未返回）按开启处理。
 * 开关不适用域仅 audio/video（二轮复审裁决：image 域存在后台探测，适用并显示
 * 「手动」标签）。
 */
export function accountBackgroundProbeDisabled(account: {
  backgroundProbeEnabled?: boolean
  capabilityDomain?: AccountCapabilityDomain
}): boolean {
  return account.backgroundProbeEnabled === false && !capabilityDomainWithoutBackgroundProbe(account.capabilityDomain)
}

/** 手动模式「手动」标签（总开关设计 §9）：灰色 default，audio/video 媒体域不适用（image 适用）。 */
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
  const lines: string[] = [
    `状态：${
      manualMode
        ? accountManualModeStatusText(account.status)
        : presentation?.label ?? effective?.label ?? accountStatusFallbackLabel(account.status)
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
  const normalizedReason = normalizeAccountStatusReason(reason, observation)
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

function accountStatusFallbackLabel(status: AccountListItem['status']): string {
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
