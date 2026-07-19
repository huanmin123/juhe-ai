import { formatDateTime } from '@/shared/formatters'
import type { AccountSummary } from '@/types/domain'

export function accountStatusTooltipLines(account: AccountSummary): string[] {
  const presentation = account.availabilityPresentation
  const effective = account.effectiveAvailability
  const observation = presentation?.probe?.lastObservation
  const lines: string[] = [`状态：${presentation?.label ?? effective?.label ?? accountStatusFallbackLabel(account.status)}`]

  if (observation) {
    lines.push(`最近检查：${formatDateTime(observation.attemptedAt)}（${observation.result === 'success' ? '成功' : '失败'}）`)
  }
  const schedule = presentation?.probe?.schedule
  if (schedule?.state === 'scheduled' && schedule.nextAttemptAt) {
    lines.push(`下次检查：${formatDateTime(schedule.nextAttemptAt)}`)
  } else if (schedule?.state === 'due_waiting') {
    lines.push(schedule.nextAttemptAt
      ? `下次检查：等待执行（原计划 ${formatDateTime(schedule.nextAttemptAt)}）`
      : '下次检查：等待执行')
  } else if (schedule?.state === 'running') {
    lines.push('下次检查：正在检查')
  } else if (presentation?.probe && !observation) {
    lines.push('下次检查：暂无计划')
  }

  const reason = observation?.result === 'failed'
    ? (observation.reason ?? presentation?.reason ?? effective?.reason)
    : (presentation?.reason ?? observation?.reason ?? effective?.reason)
  if (reason) lines.push(`原因：${reason}`)
  if (observation?.traceId) lines.push(`traceId：${observation.traceId}`)
  return lines
}

function accountStatusFallbackLabel(status: AccountSummary['status']): string {
  if (status === 'active') return '正常'
  if (status === 'pending_test') return '待检查'
  if (status === 'disabled') return '停用'
  if (status === 'error') return '异常'
  if (status === 'rate_limited') return '限流中'
  if (status === 'temporary_unavailable') return '临时不可调用'
  return '未知状态'
}

export function accountStatusTooltipTraceId(account: AccountSummary): string | undefined {
  return account.availabilityPresentation?.probe?.lastObservation?.traceId
}
