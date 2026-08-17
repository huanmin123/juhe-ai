import type { SystemMetricsRuntimeOverview } from '@/types/domain'
import { serverDateTimeTimestamp } from '@/shared/formatters'

export type BackgroundJobRow = NonNullable<SystemMetricsRuntimeOverview['backgroundJobs']>[number]

export function backgroundJobStatusText(row: BackgroundJobRow): string {
  if (row.timedOut) return '超时取消中'
  if (row.running) return '运行中'
  if (row.queuedForLane) return '等待资源'
  if (row.pending) return '待补跑'
  if (row.leaseState === 'lost') return '租约丢失'
  if (row.leaseState === 'busy') return '其他实例执行'
  if (row.lastOutcome === 'timeout') return '上次超时'
  if (row.lastOutcome === 'skipped') return '上次跳过'
  if (row.lastError) return '上次失败'
  if (row.lastWarning) return '部分失败'
  const latestProblemAt = latestTimestamp(row.lastErrorAt, row.lastWarningAt)
  if (latestProblemAt && isAfter(row.lastSuccessAt, latestProblemAt)) return '已恢复'
  if (row.lastErrorAt) return '曾失败'
  if (row.lastWarningAt) return '曾部分失败'
  return '空闲'
}

export function backgroundJobStatusColor(row: BackgroundJobRow): string {
  if (row.timedOut || row.leaseState === 'lost') return 'error'
  if (row.running || row.queuedForLane || row.pending) return 'processing'
  if (row.leaseState === 'busy' || row.lastOutcome === 'skipped' || row.lastOutcome === 'timeout') return 'warning'
  if (row.lastError || row.lastWarning) return 'warning'
  const latestProblemAt = latestTimestamp(row.lastErrorAt, row.lastWarningAt)
  if (latestProblemAt && !isAfter(row.lastSuccessAt, latestProblemAt)) return 'warning'
  return 'success'
}

function latestTimestamp(...values: Array<string | undefined>): string | undefined {
  return values
    .filter((value): value is string => Boolean(value))
    .map((value) => ({ value, timestamp: serverDateTimeTimestamp(value) }))
    .filter((item): item is { value: string; timestamp: number } => item.timestamp !== undefined)
    .sort((left, right) => left.timestamp - right.timestamp)
    .at(-1)?.value
}

function isAfter(value: string | undefined, baseline: string): boolean {
  if (!value) return false
  const valueMs = serverDateTimeTimestamp(value)
  const baselineMs = serverDateTimeTimestamp(baseline)
  return valueMs !== undefined && baselineMs !== undefined && valueMs > baselineMs
}
