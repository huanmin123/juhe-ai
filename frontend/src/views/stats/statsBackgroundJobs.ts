import type { SystemMetricsRuntimeOverview } from '@/types/domain'

export type BackgroundJobRow = NonNullable<SystemMetricsRuntimeOverview['backgroundJobs']>[number]

export function backgroundJobStatusText(row: BackgroundJobRow): string {
  if (row.running) return '运行中'
  if (row.lastError) return '上次失败'
  if (row.lastWarning) return '部分失败'
  const latestProblemAt = latestTimestamp(row.lastErrorAt, row.lastWarningAt)
  if (latestProblemAt && isAfter(row.lastSuccessAt, latestProblemAt)) return '已恢复'
  if (row.lastErrorAt) return '曾失败'
  if (row.lastWarningAt) return '曾部分失败'
  return '空闲'
}

export function backgroundJobStatusColor(row: BackgroundJobRow): string {
  if (row.running) return 'processing'
  if (row.lastError || row.lastWarning) return 'warning'
  const latestProblemAt = latestTimestamp(row.lastErrorAt, row.lastWarningAt)
  if (latestProblemAt && !isAfter(row.lastSuccessAt, latestProblemAt)) return 'warning'
  return 'success'
}

function latestTimestamp(...values: Array<string | undefined>): string | undefined {
  return values.filter((value): value is string => Boolean(value)).sort().at(-1)
}

function isAfter(value: string | undefined, baseline: string): boolean {
  if (!value) return false
  const valueMs = Date.parse(value)
  const baselineMs = Date.parse(baseline)
  return Number.isFinite(valueMs) && Number.isFinite(baselineMs) && valueMs > baselineMs
}
