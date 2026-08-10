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
  oldestQueuedMs: number | null
  lastFlushMs: number | null
  maxFlushMs: number | null
  slowFlushCount: number | null
  lastSlowFlushAt?: string
  writerPoolEnabled: boolean | null
  writerPoolWorkerCount: number | null
  writerPoolQueueLength: number | null
  writerPoolActiveJobs: number | null
  writerPoolHandledJobs: number | null
  writerPoolFailedJobs: number | null
  writerPoolRejectedJobs: number | null
  writerPoolOldestQueuedMs: number | null
  writerPoolMaxQueueWaitMs: number | null
  writerPoolMaxRunMs: number | null
  pendingWriteRequestCount: number | null
  oldestPendingWriteMs: number | null
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
    pendingWriteRequestCount: number
    writerPoolQueuedCount: number
    writerPoolActiveJobs: number
  }
  workerQueues: BackgroundQueueHealthItem[]
  serverIpcQueues: BackgroundQueueHealthItem[]
}

interface WorkerQueueSpec {
  key: string
  label: string
  workerRole: 'ingest-worker' | 'stats-worker'
  snapshotKey: 'usageRecordQueue' | 'operationLogQueue' | 'publicApiLogQueue' | 'recordMaintenanceQueue'
}

type IngestWorkerRuntimeSnapshot = NonNullable<NonNullable<DbServiceServerRuntimeSnapshot['ingestWorker']>['snapshot']>
type StatsWorkerRuntimeSnapshot = NonNullable<NonNullable<DbServiceServerRuntimeSnapshot['statsWorker']>['snapshot']>
type BackgroundQueueHealthRuntimeSnapshot = Pick<
  DbServiceServerRuntimeSnapshot,
  'ingestWorker' | 'statsWorker' | 'opsWorker'
>

interface IpcQueueSpec {
  key: string
  label: string
  snapshotKey:
    | 'usageRecords'
    | 'operationLogs'
    | 'publicApiLogs'
    | 'recordMaintenance'
    | 'statusRequests'
    | 'processEventLoopRequests'
    | 'processEventLoopResponses'
    | 'gatewayRuntimeCacheInvalidations'
    | 'other'
}

const queueLengthBacklogWarning = 1000
const queueBytesBacklogWarning = 8 * 1024 * 1024

const workerQueueSpecs: WorkerQueueSpec[] = [
  { key: 'usageRecords', label: '使用记录', workerRole: 'ingest-worker', snapshotKey: 'usageRecordQueue' },
  { key: 'operationLogs', label: '操作日志', workerRole: 'ingest-worker', snapshotKey: 'operationLogQueue' },
  { key: 'publicApiLogs', label: '公开接口日志', workerRole: 'ingest-worker', snapshotKey: 'publicApiLogQueue' },
  { key: 'recordMaintenanceIngest', label: '数据维护 ingest', workerRole: 'ingest-worker', snapshotKey: 'recordMaintenanceQueue' },
  { key: 'recordMaintenanceStats', label: '数据维护 stats', workerRole: 'stats-worker', snapshotKey: 'recordMaintenanceQueue' }
]

const ipcQueueSpecs: IpcQueueSpec[] = [
  { key: 'usageRecords', label: '使用记录 IPC', snapshotKey: 'usageRecords' },
  { key: 'operationLogs', label: '操作日志 IPC', snapshotKey: 'operationLogs' },
  { key: 'publicApiLogs', label: '公开接口日志 IPC', snapshotKey: 'publicApiLogs' },
  { key: 'recordMaintenance', label: '数据维护 IPC', snapshotKey: 'recordMaintenance' },
  { key: 'statusRequests', label: '后台快照请求 IPC', snapshotKey: 'statusRequests' },
  { key: 'processEventLoopRequests', label: '事件循环采样请求 IPC', snapshotKey: 'processEventLoopRequests' },
  { key: 'processEventLoopResponses', label: '事件循环采样响应 IPC', snapshotKey: 'processEventLoopResponses' },
  { key: 'gatewayRuntimeCacheInvalidations', label: '网关缓存失效 IPC', snapshotKey: 'gatewayRuntimeCacheInvalidations' },
  { key: 'other', label: '其他后台 IPC', snapshotKey: 'other' }
]

export function buildBackgroundQueueHealthSnapshot(
  serverRuntime: BackgroundQueueHealthRuntimeSnapshot | undefined
): BackgroundQueueHealthSnapshot {
  const ingestWorkerSnapshot = serverRuntime?.ingestWorker?.snapshot
  const statsWorkerSnapshot = serverRuntime?.statsWorker?.snapshot
  const serverIpcQueues = mergeServerIpcQueues(
    serverRuntime?.ingestWorker?.pendingQueues,
    serverRuntime?.opsWorker?.pendingQueues
  )
  const workerQueues = workerQueueSpecs.map((spec) => buildQueueHealthItem({
    key: spec.key,
    label: spec.label,
    source: 'worker_local',
    snapshot: workerQueueSnapshot(spec.snapshotKey, {
      ingestWorkerSnapshot,
      statsWorkerSnapshot,
      workerRole: spec.workerRole
    }),
    roleState: rolePendingWriteStateForQueue(spec, serverRuntime)
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
  const workerSnapshotAvailable = Boolean(ingestWorkerSnapshot && statsWorkerSnapshot)
  const serverIpcQueueAvailable = Boolean(serverIpcQueues)
  const reasons: string[] = []
  if (!available) reasons.push('server_runtime_unavailable')
  if (available && !ingestWorkerSnapshot) reasons.push('ingest_worker_snapshot_unavailable')
  if (available && !statsWorkerSnapshot) reasons.push('stats_worker_snapshot_unavailable')
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

function rolePendingWriteStateForQueue(
  spec: WorkerQueueSpec,
  serverRuntime: BackgroundQueueHealthRuntimeSnapshot | undefined
): { pendingWriteRequestCount?: number; oldestPendingWriteMs?: number } | undefined {
  if (spec.workerRole === 'ingest-worker' && spec.key === 'usageRecords') {
    return serverRuntime?.ingestWorker
  }
  if (spec.workerRole === 'stats-worker' && spec.key === 'recordMaintenanceStats') {
    return serverRuntime?.statsWorker
  }
  return undefined
}

function workerQueueSnapshot(
  snapshotKey: WorkerQueueSpec['snapshotKey'],
  input: {
    ingestWorkerSnapshot?: IngestWorkerRuntimeSnapshot
    statsWorkerSnapshot?: StatsWorkerRuntimeSnapshot
    workerRole: WorkerQueueSpec['workerRole']
  }
): DbServiceRuntimeQueueSnapshot | undefined {
  if (input.workerRole === 'stats-worker') {
    return snapshotKey === 'recordMaintenanceQueue'
      ? input.statsWorkerSnapshot?.recordMaintenanceQueue
      : undefined
  }
  return input.ingestWorkerSnapshot?.[snapshotKey]
}

function mergeServerIpcQueues(
  ...inputs: Array<Record<string, DbServiceRuntimeQueueSnapshot> | undefined>
): Record<string, DbServiceRuntimeQueueSnapshot> | undefined {
  const availableInputs = inputs.filter((input): input is Record<string, DbServiceRuntimeQueueSnapshot> => Boolean(input))
  if (availableInputs.length === 0) return undefined
  const output: Record<string, DbServiceRuntimeQueueSnapshot> = {}
  for (const spec of ipcQueueSpecs) {
    output[spec.snapshotKey] = mergeQueueSnapshots(availableInputs.map((input) => input[spec.snapshotKey]))
  }
  return output
}

function mergeQueueSnapshots(snapshots: Array<DbServiceRuntimeQueueSnapshot | undefined>): DbServiceRuntimeQueueSnapshot {
  return {
    queueLength: sumSnapshotNumbers(snapshots, 'queueLength'),
    queueBytes: sumSnapshotNumbers(snapshots, 'queueBytes'),
    droppedCount: sumSnapshotNumbers(snapshots, 'droppedCount'),
    rejectedCount: sumSnapshotNumbers(snapshots, 'rejectedCount')
  }
}

function sumSnapshotNumbers(
  snapshots: Array<DbServiceRuntimeQueueSnapshot | undefined>,
  key: keyof DbServiceRuntimeQueueSnapshot
): number {
  return snapshots.reduce((total, snapshot) => {
    const value = snapshot?.[key]
    return typeof value === 'number' && Number.isFinite(value) ? total + value : total
  }, 0)
}

function buildQueueHealthItem(input: {
  key: string
  label: string
  source: 'worker_local' | 'server_ipc'
  snapshot?: DbServiceRuntimeQueueSnapshot
  roleState?: {
    pendingWriteRequestCount?: number
    oldestPendingWriteMs?: number
  }
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
      flushFailureCount: null,
      oldestQueuedMs: null,
      lastFlushMs: null,
      maxFlushMs: null,
      slowFlushCount: null,
      writerPoolEnabled: null,
      writerPoolWorkerCount: null,
      writerPoolQueueLength: null,
      writerPoolActiveJobs: null,
      writerPoolHandledJobs: null,
      writerPoolFailedJobs: null,
      writerPoolRejectedJobs: null,
      writerPoolOldestQueuedMs: null,
      writerPoolMaxQueueWaitMs: null,
      writerPoolMaxRunMs: null,
      pendingWriteRequestCount: null,
      oldestPendingWriteMs: null,
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
  const oldestQueuedMs = nullableNumber(snapshot.oldestQueuedMs)
  const lastFlushMs = nullableNumber(snapshot.lastFlushMs)
  const maxFlushMs = nullableNumber(snapshot.maxFlushMs)
  const slowFlushCount = nullableNumber(snapshot.slowFlushCount)
  const lastSlowFlushAt = typeof snapshot.lastSlowFlushAt === 'string' && snapshot.lastSlowFlushAt.trim()
    ? snapshot.lastSlowFlushAt
    : undefined
  const writerPoolEnabled = typeof snapshot.writerPoolEnabled === 'boolean' ? snapshot.writerPoolEnabled : null
  const writerPoolQueueLength = nullableNumber(snapshot.writerPoolQueueLength)
  const writerPoolActiveJobs = nullableNumber(snapshot.writerPoolActiveJobs)
  const writerPoolFailedJobs = nullableNumber(snapshot.writerPoolFailedJobs)
  const writerPoolRejectedJobs = nullableNumber(snapshot.writerPoolRejectedJobs)
  const writerPoolOldestQueuedMs = nullableNumber(snapshot.writerPoolOldestQueuedMs)
  const pendingWriteRequestCount = nullableNumber(input.roleState?.pendingWriteRequestCount)
  const oldestPendingWriteMs = nullableNumber(input.roleState?.oldestPendingWriteMs)
  const reasons: string[] = []
  if ((droppedCount ?? 0) > 0) reasons.push('queue_dropped')
  if ((rejectedCount ?? 0) > 0) reasons.push('ipc_rejected')
  if ((flushFailureCount ?? 0) > 0 || flushLastError) reasons.push('queue_flush_failed')
  if ((slowFlushCount ?? 0) > 0) reasons.push('queue_slow_flush')
  if ((writerPoolFailedJobs ?? 0) > 0 || (writerPoolRejectedJobs ?? 0) > 0) reasons.push('writer_pool_degraded')
  if (isQueueBacklogged(writerPoolQueueLength, 0) || (writerPoolOldestQueuedMs ?? 0) >= 5000) reasons.push('writer_pool_backlogged')
  if ((pendingWriteRequestCount ?? 0) > 0 && (oldestPendingWriteMs ?? 0) >= 5000) reasons.push('pending_write_backlogged')
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
    flushLastError,
    oldestQueuedMs,
    lastFlushMs,
    maxFlushMs,
    slowFlushCount,
    lastSlowFlushAt,
    writerPoolEnabled,
    writerPoolWorkerCount: nullableNumber(snapshot.writerPoolWorkerCount),
    writerPoolQueueLength,
    writerPoolActiveJobs,
    writerPoolHandledJobs: nullableNumber(snapshot.writerPoolHandledJobs),
    writerPoolFailedJobs,
    writerPoolRejectedJobs,
    writerPoolOldestQueuedMs,
    writerPoolMaxQueueWaitMs: nullableNumber(snapshot.writerPoolMaxQueueWaitMs),
    writerPoolMaxRunMs: nullableNumber(snapshot.writerPoolMaxRunMs),
    pendingWriteRequestCount,
    oldestPendingWriteMs,
  }
}

function itemQueueHealthStatus(reasons: string[]): BackgroundQueueHealthStatus {
  if (
    reasons.includes('queue_dropped')
    || reasons.includes('ipc_rejected')
    || reasons.includes('queue_flush_failed')
    || reasons.includes('queue_slow_flush')
    || reasons.includes('writer_pool_degraded')
  ) {
    return 'degraded'
  }
  if (
    reasons.includes('queue_backlogged')
    || reasons.includes('writer_pool_backlogged')
    || reasons.includes('pending_write_backlogged')
  ) {
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
    summary.pendingWriteRequestCount += item.pendingWriteRequestCount ?? 0
    summary.writerPoolQueuedCount += item.writerPoolQueueLength ?? 0
    summary.writerPoolActiveJobs += item.writerPoolActiveJobs ?? 0
    return summary
  }, {
    degradedCount: 0,
    backloggedCount: 0,
    unavailableCount: 0,
    droppedCount: 0,
    rejectedCount: 0,
    flushFailureCount: 0,
    queuedCount: 0,
    queuedBytes: 0,
    pendingWriteRequestCount: 0,
    writerPoolQueuedCount: 0,
    writerPoolActiveJobs: 0
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
