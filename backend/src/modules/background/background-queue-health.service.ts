import type { DbServiceRuntimeQueueSnapshot, DbServiceServerRuntimeSnapshot } from '../db-service/db-service-types.js'

export type BackgroundQueueHealthStatus = 'normal' | 'backlogged' | 'degraded' | 'unavailable'

export interface BackgroundQueueHealthItem {
  key: string
  label: string
  source: 'worker_local' | 'server_ipc'
  status: BackgroundQueueHealthStatus
  reasons: string[]
  queueLength: number | null
  queueBytes: number | null
  droppedCount: number | null
  droppedOverflowCount: number | null
  droppedOversizeCount: number | null
  droppedSuccessCount: number | null
  droppedFailureCount: number | null
  rejectedCount: number | null
  flushFailureCount: number | null
  flushLastError?: string
}

export interface BackgroundQueueHealthSnapshot {
  available: boolean
  workerSnapshotAvailable: boolean
  serverIpcQueueAvailable: boolean
  status: BackgroundQueueHealthStatus
  reasons: string[]
  summary: {
    degradedCount: number
    backloggedCount: number
    unavailableCount: number
    droppedCount: number
    rejectedCount: number
    flushFailureCount: number
    queuedCount: number
    queuedBytes: number
  }
  workerQueues: BackgroundQueueHealthItem[]
  serverIpcQueues: BackgroundQueueHealthItem[]
}

interface WorkerQueueSpec {
  key: string
  label: string
  snapshotKey: 'usageRecordQueue' | 'auditLogQueue' | 'operationLogQueue' | 'recordMaintenanceQueue' | 'runtimeLogIndexQueue'
}

interface IpcQueueSpec {
  key: string
  label: string
  snapshotKey:
    | 'usageRecords'
    | 'auditLogs'
    | 'operationLogs'
    | 'recordMaintenance'
    | 'runtimeLogLines'
    | 'statusRequests'
    | 'processEventLoopRequests'
    | 'processEventLoopResponses'
    | 'gatewayRuntimeCacheInvalidations'
    | 'other'
}

const queueLengthBacklogWarning = 1000
const queueBytesBacklogWarning = 8 * 1024 * 1024

const workerQueueSpecs: WorkerQueueSpec[] = [
  { key: 'usageRecords', label: '使用记录', snapshotKey: 'usageRecordQueue' },
  { key: 'auditLogs', label: '审计日志', snapshotKey: 'auditLogQueue' },
  { key: 'operationLogs', label: '操作日志', snapshotKey: 'operationLogQueue' },
  { key: 'recordMaintenance', label: '数据维护', snapshotKey: 'recordMaintenanceQueue' },
  { key: 'runtimeLogIndex', label: '运行日志索引', snapshotKey: 'runtimeLogIndexQueue' }
]

const ipcQueueSpecs: IpcQueueSpec[] = [
  { key: 'usageRecords', label: '使用记录 IPC', snapshotKey: 'usageRecords' },
  { key: 'auditLogs', label: '审计日志 IPC', snapshotKey: 'auditLogs' },
  { key: 'operationLogs', label: '操作日志 IPC', snapshotKey: 'operationLogs' },
  { key: 'recordMaintenance', label: '数据维护 IPC', snapshotKey: 'recordMaintenance' },
  { key: 'runtimeLogLines', label: '运行日志 IPC', snapshotKey: 'runtimeLogLines' },
  { key: 'statusRequests', label: '后台快照请求 IPC', snapshotKey: 'statusRequests' },
  { key: 'processEventLoopRequests', label: '事件循环采样请求 IPC', snapshotKey: 'processEventLoopRequests' },
  { key: 'processEventLoopResponses', label: '事件循环采样响应 IPC', snapshotKey: 'processEventLoopResponses' },
  { key: 'gatewayRuntimeCacheInvalidations', label: '网关缓存失效 IPC', snapshotKey: 'gatewayRuntimeCacheInvalidations' },
  { key: 'other', label: '其他后台 IPC', snapshotKey: 'other' }
]

export function buildBackgroundQueueHealthSnapshot(
  serverRuntime: DbServiceServerRuntimeSnapshot | undefined
): BackgroundQueueHealthSnapshot {
  const workerSnapshot = serverRuntime?.worker?.snapshot
  const serverIpcQueues = serverRuntime?.worker?.pendingQueues
  const workerQueues = workerQueueSpecs.map((spec) => buildQueueHealthItem({
    key: spec.key,
    label: spec.label,
    source: 'worker_local',
    snapshot: workerSnapshot?.[spec.snapshotKey]
  }))
  const ipcQueues = ipcQueueSpecs.map((spec) => buildQueueHealthItem({
    key: spec.key,
    label: spec.label,
    source: 'server_ipc',
    snapshot: serverIpcQueues?.[spec.snapshotKey]
  }))
  const allQueues = [...workerQueues, ...ipcQueues]
  const summary = summarizeQueueHealthItems(allQueues)
  const available = Boolean(serverRuntime)
  const workerSnapshotAvailable = Boolean(workerSnapshot)
  const serverIpcQueueAvailable = Boolean(serverIpcQueues)
  const reasons: string[] = []
  if (!available) reasons.push('server_runtime_unavailable')
  if (available && !workerSnapshotAvailable) reasons.push('worker_snapshot_unavailable')
  if (available && !serverIpcQueueAvailable) reasons.push('server_ipc_queue_unavailable')
  if (summary.degradedCount > 0) reasons.push('queue_degraded')
  if (summary.backloggedCount > 0) reasons.push('queue_backlogged')
  if (summary.unavailableCount > 0) reasons.push('queue_unavailable')

  return {
    available,
    workerSnapshotAvailable,
    serverIpcQueueAvailable,
    status: overallQueueHealthStatus(available, summary),
    reasons,
    summary,
    workerQueues,
    serverIpcQueues: ipcQueues
  }
}

function buildQueueHealthItem(input: {
  key: string
  label: string
  source: 'worker_local' | 'server_ipc'
  snapshot?: DbServiceRuntimeQueueSnapshot
}): BackgroundQueueHealthItem {
  const snapshot = input.snapshot
  if (!snapshot) {
    return {
      key: input.key,
      label: input.label,
      source: input.source,
      status: 'unavailable',
      reasons: ['queue_unavailable'],
      queueLength: null,
      queueBytes: null,
      droppedCount: null,
      droppedOverflowCount: null,
      droppedOversizeCount: null,
      droppedSuccessCount: null,
      droppedFailureCount: null,
      rejectedCount: null,
      flushFailureCount: null
    }
  }

  const queueLength = nullableNumber(snapshot.queueLength)
  const queueBytes = nullableNumber(snapshot.queueBytes)
  const droppedCount = totalDroppedCount(snapshot)
  const rejectedCount = nullableNumber(snapshot.rejectedCount)
  const flushFailureCount = nullableNumber(snapshot.flushFailureCount)
  const flushLastError = typeof snapshot.flushLastError === 'string' && snapshot.flushLastError.trim()
    ? snapshot.flushLastError
    : undefined
  const reasons: string[] = []
  if ((droppedCount ?? 0) > 0) reasons.push('queue_dropped')
  if ((rejectedCount ?? 0) > 0) reasons.push('ipc_rejected')
  if ((flushFailureCount ?? 0) > 0 || flushLastError) reasons.push('queue_flush_failed')
  if (isQueueBacklogged(queueLength, queueBytes)) reasons.push('queue_backlogged')

  return {
    key: input.key,
    label: input.label,
    source: input.source,
    status: itemQueueHealthStatus(reasons),
    reasons,
    queueLength,
    queueBytes,
    droppedCount,
    droppedOverflowCount: nullableNumber(snapshot.droppedOverflowCount),
    droppedOversizeCount: nullableNumber(snapshot.droppedOversizeCount),
    droppedSuccessCount: nullableNumber(snapshot.droppedSuccessCount),
    droppedFailureCount: nullableNumber(snapshot.droppedFailureCount),
    rejectedCount,
    flushFailureCount,
    flushLastError
  }
}

function itemQueueHealthStatus(reasons: string[]): BackgroundQueueHealthStatus {
  if (
    reasons.includes('queue_dropped')
    || reasons.includes('ipc_rejected')
    || reasons.includes('queue_flush_failed')
  ) {
    return 'degraded'
  }
  if (reasons.includes('queue_backlogged')) {
    return 'backlogged'
  }
  return 'normal'
}

function overallQueueHealthStatus(
  available: boolean,
  summary: BackgroundQueueHealthSnapshot['summary']
): BackgroundQueueHealthStatus {
  if (!available || summary.unavailableCount > 0) {
    return 'unavailable'
  }
  if (summary.degradedCount > 0) {
    return 'degraded'
  }
  if (summary.backloggedCount > 0) {
    return 'backlogged'
  }
  return 'normal'
}

function summarizeQueueHealthItems(items: BackgroundQueueHealthItem[]): BackgroundQueueHealthSnapshot['summary'] {
  return items.reduce<BackgroundQueueHealthSnapshot['summary']>((summary, item) => {
    if (item.status === 'degraded') summary.degradedCount += 1
    if (item.status === 'backlogged') summary.backloggedCount += 1
    if (item.status === 'unavailable') summary.unavailableCount += 1
    summary.droppedCount += item.droppedCount ?? 0
    summary.rejectedCount += item.rejectedCount ?? 0
    summary.flushFailureCount += item.flushFailureCount ?? 0
    summary.queuedCount += item.queueLength ?? 0
    summary.queuedBytes += item.queueBytes ?? 0
    return summary
  }, {
    degradedCount: 0,
    backloggedCount: 0,
    unavailableCount: 0,
    droppedCount: 0,
    rejectedCount: 0,
    flushFailureCount: 0,
    queuedCount: 0,
    queuedBytes: 0
  })
}

function totalDroppedCount(snapshot: DbServiceRuntimeQueueSnapshot): number | null {
  const explicit = nullableNumber(snapshot.droppedCount)
  const successDropped = nullableNumber(snapshot.droppedSuccessCount)
  const failureDropped = nullableNumber(snapshot.droppedFailureCount)
  const outcomeDropped = successDropped !== null || failureDropped !== null
    ? (successDropped ?? 0) + (failureDropped ?? 0)
    : null
  if (successDropped !== null || failureDropped !== null) {
    return Math.max(explicit ?? 0, outcomeDropped ?? 0)
  }
  const overflowDropped = nullableNumber(snapshot.droppedOverflowCount)
  const oversizeDropped = nullableNumber(snapshot.droppedOversizeCount)
  const reasonDropped = overflowDropped !== null || oversizeDropped !== null
    ? (overflowDropped ?? 0) + (oversizeDropped ?? 0)
    : null
  if (overflowDropped !== null || oversizeDropped !== null) {
    return Math.max(explicit ?? 0, reasonDropped ?? 0)
  }
  if (explicit !== null) {
    return explicit
  }
  return null
}

function isQueueBacklogged(queueLength: number | null, queueBytes: number | null): boolean {
  return (queueLength ?? 0) >= queueLengthBacklogWarning || (queueBytes ?? 0) >= queueBytesBacklogWarning
}

function nullableNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}
