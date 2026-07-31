import { formatDateTime } from '@/shared/formatters'
import type { AccountListItem } from '@/types/domain'
import { accountDiagnosticMessageWithoutRepeatedFields } from './accountDiagnosticMessages'

export function accountStatusTooltipLines(account: AccountListItem): string[] {
  const presentation = account.availabilityPresentation
  const effective = account.effectiveAvailability
  const observation = presentation?.probe?.lastObservation
  const lines: string[] = [`状态：${presentation?.label ?? effective?.label ?? accountStatusFallbackLabel(account.status)}`]

  if (observation) {
    lines.push(`最近检查：${formatDateTime(observation.attemptedAt)}（${observation.result === 'success' ? '成功' : '失败'}）`)
    if (typeof observation.httpStatus === 'number') lines.push(`HTTP 状态：${observation.httpStatus}`)
    if (observation.errorCode) lines.push(`错误码：${observation.errorCode}`)
  }
  const schedule = presentation?.probe?.schedule
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
