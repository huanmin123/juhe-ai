import type { SystemMetricsOverview } from '@/types/domain'

type BackgroundRuntimeRow = NonNullable<SystemMetricsOverview['backgroundJobs']>[number]

export type BackgroundQueueType = 'retry' | 'local' | 'ipc' | 'request' | 'gateway' | 'concurrency' | 'redis' | 'writer'

export interface BackgroundQueueRow {
  key: string
  name: string
  queueType: BackgroundQueueType
  workerRole?: BackgroundRuntimeRow['workerRole']
  pendingCount?: number
  runningCount?: number
  consumers?: number
  queueLength?: number
  queueBytes?: number
  completedCount?: number
  droppedCount?: number
  rejectedCount?: number
  expiredCount?: number
  timedOutCount?: number
  failedCount?: number
  flushFailureCount?: number
  oldestQueuedMs?: number
  writerPoolQueueLength?: number
  writerPoolActiveJobs?: number
  writerPoolFailedJobs?: number
  writerPoolRejectedJobs?: number
  writerPoolOldestQueuedMs?: number
  pendingWriteRequestCount?: number
  pendingWriteOldestQueuedMs?: number
  nextRunAt?: string
  flushLastSuccessAt?: string
  lastError?: string
}

export function buildBackgroundQueueRows(metrics?: SystemMetricsOverview): BackgroundQueueRow[] {
  return (metrics?.backgroundJobs ?? [])
    .flatMap(backgroundQueueRowsFromRuntimeRow)
    .sort(compareBackgroundQueueRows)
}

export function backgroundQueueBacklog(row: BackgroundQueueRow): number {
  return row.queueType === 'retry'
    ? numberValue(row.pendingCount)
    : numberValue(row.queueLength) + numberValue(row.writerPoolQueueLength) + numberValue(row.pendingWriteRequestCount)
}

export function backgroundQueueProblemCount(row: BackgroundQueueRow): number {
  return backgroundQueueHistoricalProblemCount(row) + (row.lastError ? 1 : 0)
}

export function backgroundQueueHistoricalProblemCount(row: BackgroundQueueRow): number {
  return numberValue(row.flushFailureCount)
    + numberValue(row.droppedCount)
    + numberValue(row.rejectedCount)
    + numberValue(row.expiredCount)
    + (isDiagnosticRequestQueue(row) ? 0 : numberValue(row.timedOutCount) + numberValue(row.failedCount))
    + numberValue(row.writerPoolFailedJobs)
    + numberValue(row.writerPoolRejectedJobs)
}

export function backgroundQueueDiagnosticCount(row: BackgroundQueueRow): number {
  return isDiagnosticRequestQueue(row)
    ? numberValue(row.timedOutCount) + numberValue(row.failedCount)
    : 0
}

export function backgroundQueueStatusText(row: BackgroundQueueRow): string {
  if (row.lastError) return '异常'
  if (numberValue(backgroundQueueActiveCount(row)) > 0) return '运行中'
  if (backgroundQueueBacklog(row) > 0) return row.queueType === 'retry' ? '待执行' : '积压'
  if (backgroundQueueHistoricalProblemCount(row) > 0 || backgroundQueueDiagnosticCount(row) > 0) return '曾失败'
  return '空闲'
}

export function backgroundQueueStatusColor(row: BackgroundQueueRow): string {
  if (row.lastError) return 'error'
  if (numberValue(backgroundQueueActiveCount(row)) > 0) return 'processing'
  if (backgroundQueueBacklog(row) > 0 || backgroundQueueHistoricalProblemCount(row) > 0 || backgroundQueueDiagnosticCount(row) > 0) return 'warning'
  return 'success'
}

export function backgroundQueueActiveCount(row: BackgroundQueueRow): number | undefined {
  if (row.consumers !== undefined) return numberValue(row.consumers)
  if (row.runningCount === undefined && row.writerPoolActiveJobs === undefined) return undefined
  return numberValue(row.runningCount) + numberValue(row.writerPoolActiveJobs)
}

function backgroundQueueRowsFromRuntimeRow(row: BackgroundRuntimeRow): BackgroundQueueRow[] {
  const rows: BackgroundQueueRow[] = []
  if (row.retryQueue) {
    rows.push({
      key: `retry:${row.retryQueue.name || row.name}`,
      name: row.retryQueue.name || row.name,
      queueType: 'retry',
      workerRole: row.workerRole,
      pendingCount: optionalNumberValue(row.retryQueue.pendingCount),
      runningCount: optionalNumberValue(row.retryQueue.runningCount),
      nextRunAt: row.retryQueue.nextRunAt
    })
  }
  if (row.localQueue) {
    const localQueue = row.localQueue as Record<string, unknown>
    rows.push({
      key: `local:${row.localQueue.name || row.name}`,
      name: row.localQueue.name || row.name,
      queueType: localQueueType(localQueue.queueType),
      workerRole: row.workerRole,
      queueLength: optionalNumberValue(row.localQueue.queueLength),
      queueBytes: optionalNumberValue(row.localQueue.queueBytes),
      completedCount: optionalNumberValue(row.localQueue.completedCount),
      droppedCount: optionalNumberValue(row.localQueue.droppedCount),
      rejectedCount: optionalNumberValue(localQueue.rejectedCount),
      expiredCount: optionalNumberValue(localQueue.expiredCount),
      timedOutCount: optionalNumberValue(localQueue.timedOutCount),
      failedCount: optionalNumberValue(localQueue.failedCount),
      flushFailureCount: optionalNumberValue(row.localQueue.flushFailureCount),
      oldestQueuedMs: optionalNumberValue(row.localQueue.oldestQueuedMs),
      writerPoolQueueLength: optionalNumberValue(row.localQueue.writerPoolQueueLength),
      writerPoolActiveJobs: optionalNumberValue(row.localQueue.writerPoolActiveJobs),
      writerPoolFailedJobs: optionalNumberValue(row.localQueue.writerPoolFailedJobs),
      writerPoolRejectedJobs: optionalNumberValue(row.localQueue.writerPoolRejectedJobs),
      writerPoolOldestQueuedMs: optionalNumberValue(row.localQueue.writerPoolOldestQueuedMs),
      pendingWriteRequestCount: optionalNumberValue(localQueue.pendingWriteRequestCount),
      pendingWriteOldestQueuedMs: optionalNumberValue(localQueue.pendingWriteOldestQueuedMs),
      runningCount: optionalNumberValue(localQueue.runningCount),
      consumers: optionalNumberValue(localQueue.consumers),
      nextRunAt: stringValue(localQueue.nextRunAt),
      flushLastSuccessAt: row.localQueue.flushLastSuccessAt,
      lastError: typeof row.localQueue.flushLastError === 'string' ? row.localQueue.flushLastError : undefined
    })
  }
  return rows
}

function compareBackgroundQueueRows(left: BackgroundQueueRow, right: BackgroundQueueRow): number {
  const leftProblemCount = backgroundQueueProblemCount(left)
  const rightProblemCount = backgroundQueueProblemCount(right)
  if (leftProblemCount !== rightProblemCount) return rightProblemCount - leftProblemCount
  const leftBacklog = backgroundQueueBacklog(left) + numberValue(left.runningCount)
  const rightBacklog = backgroundQueueBacklog(right) + numberValue(right.runningCount)
  if (leftBacklog !== rightBacklog) return rightBacklog - leftBacklog
  return left.name.localeCompare(right.name)
}

function localQueueType(value: unknown): BackgroundQueueType {
  return value === 'ipc'
    || value === 'request'
    || value === 'gateway'
    || value === 'concurrency'
    || value === 'redis'
    || value === 'writer'
    ? value
    : 'local'
}

function isDiagnosticRequestQueue(row: BackgroundQueueRow): boolean {
  return row.queueType === 'request'
    && row.workerRole === 'db-service'
    && (
      row.name === 'DB service 事件循环采样 pending'
      || row.name === 'DB service server runtime snapshot pending'
    )
}

function stringValue(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function numberValue(value: unknown): number {
  const numericValue = typeof value === 'string' ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : 0
}

function optionalNumberValue(value: unknown): number | undefined {
  const numericValue = typeof value === 'string' && value.trim() ? Number(value.trim()) : value
  return typeof numericValue === 'number' && Number.isFinite(numericValue) ? numericValue : undefined
}
