import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { forwardSupervisorOutput } from '../../shared/supervisor-output.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { ActiveClientIpPolicy } from '../../storage/client-ip-stats.repository.js'
import type { AuditLogInput, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import type { GatewayQuotaSnapshot } from '../gateway/quota/quota-snapshot-cache.service.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import { dbServiceOperationAccessMode } from '../db-service/db-service-operation-access-mode.js'
import type { AccountRuntimeAvailabilityClearTarget } from '../db-service/db-service-types.js'
import type { AccountHealthCheckTriggerReason } from '../accounts/account-health-check-trigger.js'
import { auditWorkerMessageMaxBytes, trimAuditLogsForWorkerIpc } from './background-ipc-audit-trim.js'
import { estimateWorkerMessageBytes, regularWorkerMessageMaxBytes, usageRecordWorkerMessageMaxBytes } from './background-ipc-message-size.js'
import {
  clonePendingQueueRuntime,
  emptyIpcQueuesRuntime,
  ipcQueueKeyForMessage,
  mergePendingQueuesRuntime,
  type IpcQueueKey
} from './background-ipc-queue-runtime.js'
import type {
  BackgroundWorkerDbServiceRequestOptions,
  BackgroundWorkerIngestDrainStatus,
  BackgroundWorkerIpcQueuesRuntime,
  BackgroundWorkerMessage,
  BackgroundWorkerProcessRole,
  BackgroundWorkerRuntimeSnapshot,
  BackgroundWorkerState,
  PendingIngestStatusRequest,
  PendingProcessEventLoopRequest
} from './background-ipc.types.js'
import { failIpcPendingRequests, finishIpcPendingRequest, timeoutIpcPendingRequest } from './background-ipc-pending-requests.js'
import { buildBackgroundWorkerStateSnapshot } from './background-ipc-state-snapshot.js'
import {
  failWorkerSnapshotPendingRequests,
  finishWorkerSnapshotResponse,
  requestDirectWorkerSnapshot,
  requestQueuedWorkerSnapshot,
  requestRoleWorkerSnapshot as requestSnapshotRoleWorkerSnapshot,
  snapshotRequestStats
} from './background-ipc-snapshot-requests.js'
import {
  roleForBackgroundWorkerChild,
  terminateBrokenWorkerIpc,
  workerPidFromBrokenChild,
  workerPidFromReadyRecord,
  writeParentIpcBrokenLog
} from './background-ipc-worker-runtime.js'
import {
  isSnapshotRoleWorker,
  processEventLoopWorkerRoles,
  workerMessageTargetRole,
  type BackgroundWorkerQueueTargetRole,
  type BackgroundWorkerSnapshotRole
} from './background-ipc-worker-roles.js'
import { HeadIndexedQueue } from './ipc-head-queue.js'
import { isPageDataChangeEvent } from '../page-data/page-data-change.service.js'

export type {
  BackgroundWorkerIngestDrainStatus,
  BackgroundWorkerIpcQueueRuntime,
  BackgroundWorkerIpcQueuesRuntime,
  BackgroundWorkerProcessRole,
  BackgroundWorkerQueueRuntime,
  BackgroundWorkerRetryQueueRuntime,
  BackgroundWorkerRoleState,
  BackgroundWorkerRuntimeLogQueueRuntime,
  BackgroundWorkerRuntimeSnapshot
} from './background-ipc.types.js'

let workerProcess: ChildProcess | undefined
let workerReady = false
let workerPid: number | undefined
let ingestWorkerProcess: ChildProcess | undefined
let ingestWorkerReady = false
let ingestWorkerPid: number | undefined
let statsWorkerProcess: ChildProcess | undefined
let statsWorkerReady = false
let statsWorkerPid: number | undefined
let opsWorkerProcess: ChildProcess | undefined
let opsWorkerReady = false
let opsWorkerPid: number | undefined
const usageRecordMessageQueueMaxMessages = 10_000
const usageRecordMessageQueueMaxBytes = 64 * 1024 * 1024
const regularWorkerMessageQueueMaxMessages = 5_000
const regularWorkerMessageQueueMaxBytes = 64 * 1024 * 1024
const pendingDatasetWriteRequestMaxCount = 1000
const pendingStatsWriteRequestMaxCount = 1000
const pendingBackgroundDbServiceRequestMaxCount = 1000
const ingestUsageBurstBeforeRegular = 8
const regularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
const ingestUsageRecordMessageQueue = new HeadIndexedQueue<Extract<BackgroundWorkerMessage, { type: 'background_worker_usage_records' }>>()
const ingestRegularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
const opsWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
let regularWorkerMessageQueueBytes = 0
let ingestUsageRecordMessageQueueBytes = 0
let ingestRegularWorkerMessageQueueBytes = 0
let opsWorkerMessageQueueBytes = 0
let sendingMessage = false
let sendingWorkerMessage: BackgroundWorkerMessage | undefined
let sendingIngestMessage = false
let sendingIngestWorkerMessage: BackgroundWorkerMessage | undefined
let consecutiveIngestUsageMessages = 0
let sendingOpsMessage = false
let sendingOpsWorkerMessage: BackgroundWorkerMessage | undefined
const pendingQueueRuntime = emptyIpcQueuesRuntime()
const ingestPendingQueueRuntime = emptyIpcQueuesRuntime()
const opsPendingQueueRuntime = emptyIpcQueuesRuntime()
let pendingParentIngestStatusRequests = new Map<string, PendingIngestStatusRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let pendingDatasetWriteRequests = new Map<string, PendingDatasetWriteRequest>()
let pendingStatsWriteRequests = new Map<string, PendingStatsWriteRequest>()
let rejectedDatasetWriteRequestCount = 0
let timedOutDatasetWriteRequestCount = 0
let rejectedStatsWriteRequestCount = 0
let timedOutStatsWriteRequestCount = 0
let rejectedBackgroundDbServiceRequestCount = 0
let timedOutBackgroundDbServiceRequestCount = 0
let timedOutProcessEventLoopRequestCount = 0
let failedProcessEventLoopRequestCount = 0
let backgroundWorkerReadyHandler: (() => void) | undefined
let ingestWorkerReadyHandler: (() => void) | undefined
let statsWorkerReadyHandler: (() => void) | undefined
let opsWorkerReadyHandler: (() => void) | undefined

if (runtimeConfig.processRole === 'worker') {
  process.on('message', handleParentMessage)
  process.once('disconnect', () => {
    markParentIpcBroken(new Error('后台 worker 父进程 IPC 已断开'))
  })
}

export function attachBackgroundWorkerProcess(child: ChildProcess, options: { role?: BackgroundWorkerProcessRole; onReady?: () => void } = {}): void {
  const role = options.role ?? 'worker'
  if (role === 'ingest-worker') {
    attachIngestWorkerProcess(child, options)
    return
  }
  if (role === 'stats-worker') {
    attachStatsWorkerProcess(child, options)
    return
  }
  if (role === 'ops-worker') {
    attachOpsWorkerProcess(child, options)
    return
  }

  workerProcess = child
  workerPid = child.pid ?? undefined
  workerReady = false
  backgroundWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, role, child))
  child.once('exit', () => {
    if (workerProcess === child) {
      workerProcess = undefined
      workerReady = false
      workerPid = undefined
      sendingMessage = false
      if (sendingWorkerMessage) {
        requeueWorkerMessageFirst(sendingWorkerMessage)
        sendingWorkerMessage = undefined
      }
      failPendingRequests()
    }
  })

  flushWorkerMessageQueue()
}

function attachIngestWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  ingestWorkerProcess = child
  ingestWorkerPid = child.pid ?? undefined
  ingestWorkerReady = false
  ingestWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, 'ingest-worker', child))
  child.once('exit', () => {
    if (ingestWorkerProcess === child) {
      ingestWorkerProcess = undefined
      ingestWorkerReady = false
      ingestWorkerPid = undefined
      sendingIngestMessage = false
      if (sendingIngestWorkerMessage) {
        requeueIngestWorkerMessageFirst(sendingIngestWorkerMessage)
        sendingIngestWorkerMessage = undefined
      }
      failIngestPendingRequests()
      failPendingProcessEventLoopRequests()
    }
  })

  flushIngestWorkerMessageQueue()
}

function attachStatsWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  statsWorkerProcess = child
  statsWorkerPid = child.pid ?? undefined
  statsWorkerReady = false
  statsWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, 'stats-worker', child))
  child.once('exit', () => {
    if (statsWorkerProcess === child) {
      statsWorkerProcess = undefined
      statsWorkerReady = false
      statsWorkerPid = undefined
      failStatsPendingRequests()
    }
  })
}

function attachOpsWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  opsWorkerProcess = child
  opsWorkerPid = child.pid ?? undefined
  opsWorkerReady = false
  opsWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, 'ops-worker', child))
  child.once('exit', () => {
    if (opsWorkerProcess === child) {
      opsWorkerProcess = undefined
      opsWorkerReady = false
      opsWorkerPid = undefined
      sendingOpsMessage = false
      if (sendingOpsWorkerMessage) {
        requeueOpsWorkerMessageFirst(sendingOpsWorkerMessage)
        sendingOpsWorkerMessage = undefined
      }
      failOpsPendingRequests()
    }
  })

  flushOpsWorkerMessageQueue()
}

export function sendUsageRecordsToWorker(items: UsageRecordInput[]): boolean {
  return sendBackgroundWorkerMessageToWorker({
    type: 'background_worker_usage_records',
    items
  })
}

export function sendAuditLogsToWorker(items: AuditLogInput[]): boolean {
  return sendBackgroundWorkerMessageToWorker({
    type: 'background_worker_audit_logs',
    items: trimAuditLogsForWorkerIpc(items)
  })
}

export function sendOperationLogsToWorker(items: OperationLogInput[]): boolean {
  return sendBackgroundWorkerMessageToWorker({
    type: 'background_worker_operation_logs',
    items
  })
}

export function sendPublicApiLogsToWorker(items: PublicApiLogInput[]): boolean {
  return sendBackgroundWorkerMessageToWorker({
    type: 'background_worker_public_api_logs',
    items
  })
}

export function sendRecordMaintenanceJobsToWorker(items: RecordMaintenanceJob[]): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  return sendBackgroundWorkerMessage({
    type: 'background_worker_record_maintenance',
    items
  })
}

export function sendAccountTestTasksToWorker(taskIds: string[]): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  const normalizedIds = taskIds.map(normalizedString).filter((id): id is string => Boolean(id))
  if (normalizedIds.length === 0) {
    return true
  }
  return sendBackgroundWorkerMessage({
    type: 'background_worker_account_test_tasks',
    taskIds: normalizedIds
  })
}

export function sendAccountTestCancelToWorker(taskId: string): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  const normalizedId = normalizedString(taskId)
  if (!normalizedId) {
    return false
  }
  return sendBackgroundWorkerMessage({
    type: 'background_worker_account_test_cancel',
    taskId: normalizedId
  })
}

export function sendAccountHealthCheckTriggerToWorker(
  accountId: string,
  reason: AccountHealthCheckTriggerReason
): boolean {
  if (runtimeConfig.processRole === 'worker') return false
  const normalizedId = normalizedString(accountId)
  if (!normalizedId) return false
  return sendBackgroundWorkerMessage({
    type: 'background_worker_account_health_check_trigger',
    accountId: normalizedId,
    reason
  })
}

export function sendGatewayQuotaSnapshotToServer(snapshot: GatewayQuotaSnapshot): void {
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return
  }
  try {
    process.send({
      type: 'gateway_quota_snapshot_update',
      snapshot
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markParentIpcBroken(error)
      }
    })
  } catch (error) {
    markParentIpcBroken(error)
  }
}

export function sendClientIpPolicySnapshotToServer(policies: ActiveClientIpPolicy[]): void {
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return
  }
  try {
    process.send({
      type: 'client_ip_policy_snapshot_update',
      policies
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markParentIpcBroken(error)
      }
    })
  } catch (error) {
    markParentIpcBroken(error)
  }
}

export function sendAccountRuntimeClearToServer(target: AccountRuntimeAvailabilityClearTarget): void {
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return
  }
  try {
    process.send({
      type: 'server_account_runtime_clear',
      target
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markParentIpcBroken(error)
      }
    })
  } catch (error) {
    markParentIpcBroken(error)
  }
}

export function sendBackgroundWorkerMessage(message: BackgroundWorkerMessage): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  return queueWorkerMessage(message)
}

function sendBackgroundWorkerMessageToParent(message: BackgroundWorkerMessage): boolean {
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return false
  }
  if (runtimeConfig.queueDriver === 'redis_stream' && isRedisStreamManagedIngestQueueMessage(message)) {
    rejectRedisStreamLocalQueueMessage(message, 'sendBackgroundWorkerMessageToParent')
    return false
  }
  try {
    process.send(message, (error) => {
      if (error) {
        markParentIpcBroken(error)
      }
    })
    return true
  } catch (error) {
    markParentIpcBroken(error)
    return false
  }
}

export async function requestBackgroundWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestQueuedWorkerSnapshot({
    queueWorkerMessage,
    timeoutMs,
    workerProcess
  })
}

function sendBackgroundWorkerMessageToWorker(message: BackgroundWorkerMessage): boolean {
  if (runtimeConfig.processRole === 'worker') {
    if (runtimeConfig.workerRole === 'ingest-worker') {
      return false
    }
    return sendBackgroundWorkerMessageToParent(message)
  }
  return sendBackgroundWorkerMessage(message)
}

export async function requestIngestWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestDirectWorkerSnapshot('ingest-worker', {
    child: ingestWorkerProcess,
    markIpcBroken: (error, child) => markIngestWorkerIpcBroken(error, child),
    ready: ingestWorkerReady,
    timeoutMs
  })
}

export async function requestStatsWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestSnapshotRoleWorkerSnapshot('stats-worker', {
    child: statsWorkerProcess,
    markIpcBroken: (error, child) => markRoleWorkerIpcBroken('stats-worker', error, child),
    ready: statsWorkerReady,
    timeoutMs
  })
}

export async function requestOpsWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestSnapshotRoleWorkerSnapshot('ops-worker', {
    child: opsWorkerProcess,
    markIpcBroken: (error, child) => markRoleWorkerIpcBroken('ops-worker', error, child),
    ready: opsWorkerReady,
    timeoutMs
  })
}

export async function requestIngestWorkerDrainStatus(timeoutMs = 1000): Promise<BackgroundWorkerIngestDrainStatus | undefined> {
  if (runtimeConfig.processRole === 'server') {
    return await buildIngestWorkerDrainStatus(timeoutMs)
  }
  if (runtimeConfig.processRole !== 'worker' || runtimeConfig.workerRole === 'ingest-worker' || typeof process.send !== 'function') {
    return undefined
  }

  const requestId = randomUUID()
  const sendToParent = process.send.bind(process)
  return await new Promise<BackgroundWorkerIngestDrainStatus | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      timeoutIpcPendingRequest(pendingParentIngestStatusRequests, requestId)
    }, timeoutMs)
    pendingParentIngestStatusRequests.set(requestId, { resolve, timeout })
    try {
      sendToParent({
        type: 'background_worker_ingest_status_request',
        requestId
      } satisfies BackgroundWorkerMessage, (error) => {
        if (error) {
          finishParentIngestStatusRequest(requestId, undefined)
          markParentIpcBroken(error)
        }
      })
    } catch (error) {
      finishParentIngestStatusRequest(requestId, undefined)
      markParentIpcBroken(error)
    }
  })
}

export async function requestServerProcessEventLoopSamples(timeoutMs = 1000): Promise<ProcessEventLoopSample[] | undefined> {
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return undefined
  }

  const sendToParent = process.send.bind(process)
  const requestId = randomUUID()
  return await new Promise<ProcessEventLoopSample[] | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      if (timeoutIpcPendingRequest(pendingProcessEventLoopRequests, requestId)) {
        timedOutProcessEventLoopRequestCount += 1
      }
    }, timeoutMs)
    pendingProcessEventLoopRequests.set(requestId, { resolve, timeout })
    try {
      sendToParent({
        type: 'background_worker_process_event_loop_request',
        requestId
      } satisfies BackgroundWorkerMessage, (error) => {
        if (error) {
          failedProcessEventLoopRequestCount += 1
          finishProcessEventLoopRequest(requestId, undefined)
          markParentIpcBroken(error)
        }
      })
    } catch (error) {
      failedProcessEventLoopRequestCount += 1
      finishProcessEventLoopRequest(requestId, undefined)
      markParentIpcBroken(error)
    }
  })
}

async function requestWorkerProcessEventLoopSamples(timeoutMs = 800): Promise<ProcessEventLoopSample[] | undefined> {
  return await requestChildProcessEventLoopSamples('worker', timeoutMs)
}

async function requestIngestWorkerProcessEventLoopSamples(timeoutMs = 800): Promise<ProcessEventLoopSample[] | undefined> {
  return await requestChildProcessEventLoopSamples('ingest-worker', timeoutMs)
}

async function requestChildProcessEventLoopSamples(
  role: BackgroundWorkerProcessRole,
  timeoutMs = 800
): Promise<ProcessEventLoopSample[] | undefined> {
  const child = processForRole(role)
  if (runtimeConfig.processRole !== 'server' || !child || !child.connected || !readyForRole(role)) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<ProcessEventLoopSample[] | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      if (timeoutIpcPendingRequest(pendingProcessEventLoopRequests, requestId)) {
        timedOutProcessEventLoopRequestCount += 1
      }
    }, timeoutMs)
    pendingProcessEventLoopRequests.set(requestId, { resolve, timeout })
    try {
      child.send({
        type: 'background_worker_process_event_loop_request',
        requestId
      } satisfies BackgroundWorkerMessage, (error) => {
        if (error) {
          failedProcessEventLoopRequestCount += 1
          finishProcessEventLoopRequest(requestId, undefined)
          markIpcBrokenForChild(role, error, child)
        }
      })
    } catch (error) {
      failedProcessEventLoopRequestCount += 1
      finishProcessEventLoopRequest(requestId, undefined)
      markIpcBrokenForChild(role, error, child)
    }
  })
}

export function getBackgroundWorkerState(): BackgroundWorkerState {
  const workerSnapshotStats = snapshotRequestStats('worker')
  const ingestSnapshotStats = snapshotRequestStats('ingest-worker')
  const statsSnapshotStats = snapshotRequestStats('stats-worker')
  const opsSnapshotStats = snapshotRequestStats('ops-worker')
  return buildBackgroundWorkerStateSnapshot({
    pid: workerPid,
    ready: workerReady,
    lastSnapshot: workerSnapshotStats.lastSnapshot,
    pendingMessageCounts: {
      regularWorker: regularWorkerMessageQueue.length,
      ingestUsageRecord: ingestUsageRecordMessageQueue.length,
      ingestRegularWorker: ingestRegularWorkerMessageQueue.length,
      opsWorker: opsWorkerMessageQueue.length
    },
    pendingMessageBytes: {
      regularWorker: regularWorkerMessageQueueBytes,
      ingestUsageRecord: ingestUsageRecordMessageQueueBytes,
      ingestRegularWorker: ingestRegularWorkerMessageQueueBytes,
      opsWorker: opsWorkerMessageQueueBytes
    },
    pendingQueues: buildAggregatePendingQueuesRuntime(),
    pendingSnapshotRequestCount: workerSnapshotStats.pendingSnapshotRequestCount,
    timedOutSnapshotRequestCount: workerSnapshotStats.timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: workerSnapshotStats.rejectedSnapshotRequestCount,
    pendingDbServiceRequestCount: pendingBackgroundDbServiceRequests.size,
    oldestPendingDbServiceRequestMs: oldestPendingRequestMs(pendingBackgroundDbServiceRequests),
    rejectedDbServiceRequestCount: rejectedBackgroundDbServiceRequestCount,
    timedOutDbServiceRequestCount: timedOutBackgroundDbServiceRequestCount,
    pendingProcessEventLoopRequestCount: pendingProcessEventLoopRequests.size,
    timedOutProcessEventLoopRequestCount,
    failedProcessEventLoopRequestCount,
    roles: {
      ingestWorker: {
        pid: ingestWorkerPid,
        ready: ingestWorkerReady,
        lastSnapshot: ingestSnapshotStats.lastSnapshot,
        pendingMessageCount: ingestUsageRecordMessageQueue.length + ingestRegularWorkerMessageQueue.length,
        pendingMessageBytes: ingestUsageRecordMessageQueueBytes + ingestRegularWorkerMessageQueueBytes,
        pendingQueues: buildIngestPendingQueuesRuntime(),
        pendingWriteRequestCount: pendingDatasetWriteRequests.size,
        oldestPendingWriteMs: oldestPendingRequestMs(pendingDatasetWriteRequests),
        rejectedWriteRequestCount: rejectedDatasetWriteRequestCount,
        timedOutWriteRequestCount: timedOutDatasetWriteRequestCount,
        pendingSnapshotRequestCount: ingestSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: ingestSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: ingestSnapshotStats.rejectedSnapshotRequestCount
      },
      statsWorker: {
        pid: statsWorkerPid,
        ready: statsWorkerReady,
        lastSnapshot: statsSnapshotStats.lastSnapshot,
        pendingWriteRequestCount: pendingStatsWriteRequests.size,
        oldestPendingWriteMs: oldestPendingRequestMs(pendingStatsWriteRequests),
        rejectedWriteRequestCount: rejectedStatsWriteRequestCount,
        timedOutWriteRequestCount: timedOutStatsWriteRequestCount,
        pendingSnapshotRequestCount: statsSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: statsSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: statsSnapshotStats.rejectedSnapshotRequestCount
      },
      opsWorker: {
        pid: opsWorkerPid,
        ready: opsWorkerReady,
        lastSnapshot: opsSnapshotStats.lastSnapshot,
        pendingMessageCount: opsWorkerMessageQueue.length,
        pendingMessageBytes: opsWorkerMessageQueueBytes,
        pendingQueues: buildOpsPendingQueuesRuntime(),
        pendingSnapshotRequestCount: opsSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: opsSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: opsSnapshotStats.rejectedSnapshotRequestCount
      }
    }
  })
}

function handleWorkerMessage(message: unknown, role: BackgroundWorkerProcessRole = 'worker', child: ChildProcess | undefined = workerProcess): void {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return
  }

  const record = message as Partial<BackgroundWorkerMessage> & Record<string, unknown>
  switch (record.type) {
    case 'background_worker_ready':
      markWorkerReady(role, record)
      break
    case 'background_worker_status_response':
      if (typeof record.requestId !== 'string') break
      finishWorkerStatusResponse(role, record.requestId, record.snapshot as BackgroundWorkerRuntimeSnapshot | undefined)
      break
    case 'background_worker_usage_records':
    case 'background_worker_audit_logs':
    case 'background_worker_operation_logs':
    case 'background_worker_public_api_logs':
    case 'background_worker_record_maintenance':
      if (runtimeConfig.processRole === 'server') {
        queueWorkerMessage(record as BackgroundWorkerMessage)
      }
      break
    case 'background_worker_ingest_status_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string') {
        void respondToIngestStatusRequest(record.requestId, child)
      }
      break
    case 'background_worker_db_service_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && record.operation) {
        void respondToDbServiceRequest(
          record.requestId,
          record.operation,
          child,
          isBackgroundWorkerDbServiceRequestOptions(record.options) ? record.options : undefined
        )
      }
      break
    case 'background_worker_dataset_write_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && record.operation) {
        void forwardDatasetWriteRequest(record.requestId, record.operation as import('./background-dataset-writer.js').BackgroundDatasetWriteOperation, child)
      }
      break
    case 'background_worker_dataset_write_response':
      if (typeof record.requestId !== 'string') break
      finishDatasetWriteRequest(record.requestId, record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : 'dataset-writer 请求失败' })
      break
    case 'background_worker_stats_write_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && record.operation) {
        void forwardStatsWriteRequest(record.requestId, record.operation as import('./background-stats-writer.js').BackgroundStatsWriteOperation, child)
      }
      break
    case 'background_worker_stats_write_response':
      if (typeof record.requestId !== 'string') break
      finishStatsWriteRequest(record.requestId, record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : 'stats-writer 请求失败' })
      break
    case 'background_worker_process_event_loop_response':
      if (typeof record.requestId !== 'string' || !Array.isArray(record.samples)) break
      finishProcessEventLoopRequest(record.requestId, nonEmptyProcessEventLoopSamples(record.samples))
      break
    case 'background_worker_process_event_loop_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string') {
        void respondToProcessEventLoopRequest(record.requestId, child)
      }
      break
    case 'gateway_runtime_cache_invalidate':
      if (runtimeConfig.processRole !== 'worker') {
        void clearServerGatewayRuntimeCache()
      }
      break
    case 'server_account_runtime_clear':
      if (runtimeConfig.processRole === 'server' && isAccountRuntimeClearTarget(record.target)) {
        void clearServerAccountRuntimeAvailability(record.target)
      }
      break
    case 'gateway_quota_snapshot_update':
      if (runtimeConfig.processRole === 'server' && isGatewayQuotaSnapshot(record.snapshot)) {
        void replaceServerGatewayQuotaSnapshot(record.snapshot)
      }
      break
    case 'client_ip_policy_snapshot_update':
      if (runtimeConfig.processRole === 'server' && isActiveClientIpPolicyArray(record.policies)) {
        void replaceServerClientIpPolicySnapshot(record.policies).catch((error) => {
          logger.warn(errorLogFields(error, {
            event: 'client_ip_policy_snapshot_update_failed',
            policyCount: record.policies?.length
          }), '客户端 IP 策略 IPC 处理失败')
        })
      }
      break
    case 'page_data_change_publish':
      if (runtimeConfig.processRole === 'server' && isPageDataChangeEvent(record.event)) {
        void import('../page-data/page-data-change.runtime.js')
          .then(({ publishPageDataChange }) => publishPageDataChange(record.event as import('../page-data/page-data-change.service.js').PageDataChangeEvent))
          .catch((error) => {
            logger.warn(errorLogFields(error, { event: 'page_data_change_worker_ipc_forward_failed' }), '转发 worker 页面数据变更失败')
          })
      }
      break
    case 'page_data_change_dirty':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && Array.isArray(record.domains)) {
        void (async () => {
          try {
            const { sendPageDataDirtyDomainsToDbService } = await import('../db-service/db-service-ipc.js')
            await sendPageDataDirtyDomainsToDbService(record.domains as import('../page-data/page-data-change.service.js').PageDataDomain[])
            sendPageDataDirtyAckToWorker(child, { type: 'page_data_change_dirty_ack', requestId: record.requestId as string, ok: true })
          } catch (error) {
            logger.warn(errorLogFields(error, { event: 'page_data_change_dirty_worker_ipc_forward_failed' }), '转发 worker 页面数据 dirty domain 失败')
            sendPageDataDirtyAckToWorker(child, {
              type: 'page_data_change_dirty_ack',
              requestId: record.requestId as string,
              ok: false,
              errorMessage: error instanceof Error ? error.message : String(error)
            })
          }
        })()
      }
      break
    default:
      break
  }
}

function handleParentMessage(message: unknown): void {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return
  }

  const record = message as Partial<BackgroundWorkerMessage> & Record<string, unknown>
  if (record.type === 'background_worker_process_event_loop_response' && typeof record.requestId === 'string' && Array.isArray(record.samples)) {
    finishProcessEventLoopRequest(record.requestId, nonEmptyProcessEventLoopSamples(record.samples))
    return
  }
  if (record.type === 'background_worker_ingest_status_response' && typeof record.requestId === 'string') {
    finishParentIngestStatusRequest(record.requestId, record.status as BackgroundWorkerIngestDrainStatus | undefined)
    return
  }
  if (record.type === 'background_worker_db_service_response' && typeof record.requestId === 'string') {
    finishBackgroundDbServiceRequest(record.requestId, record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : '本地数据库服务请求失败' })
    return
  }
  if (record.type === 'background_worker_dataset_write_response' && typeof record.requestId === 'string') {
    finishDatasetWriteRequest(record.requestId, record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : 'dataset-writer 请求失败' })
    return
  }
  if (record.type === 'background_worker_stats_write_response' && typeof record.requestId === 'string') {
    finishStatsWriteRequest(record.requestId, record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : 'stats-writer 请求失败' })
    return
  }
  if (record.type === 'page_data_change_dirty_ack' && typeof record.requestId === 'string') {
    void import('../page-data/page-data-change.runtime.js')
      .then(({ acceptPageDataDirtyDomainsParentAck }) => {
        acceptPageDataDirtyDomainsParentAck(record.requestId as string, record.ok === true, typeof record.errorMessage === 'string' ? record.errorMessage : undefined)
      })
  }
}

function sendPageDataDirtyAckToWorker(
  child: ChildProcess | undefined,
  message: Extract<BackgroundWorkerMessage, { type: 'page_data_change_dirty_ack' }>
): void {
  if (!child?.connected) return
  try {
    child.send(message, (error) => {
      if (error) logger.warn(errorLogFields(error, { event: 'page_data_change_dirty_worker_ack_failed' }), '回传 worker 页面数据 dirty domain ACK 失败')
    })
  } catch (error) {
    logger.warn(errorLogFields(error, { event: 'page_data_change_dirty_worker_ack_failed' }), '回传 worker 页面数据 dirty domain ACK 失败')
  }
}

interface PendingDatasetWriteRequest {
  resolve: (result: unknown) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
  createdAt: number
}

interface PendingStatsWriteRequest {
  resolve: (result: unknown) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
  createdAt: number
}

export async function requestBackgroundWorkerDatasetWrite<T extends import('./background-dataset-writer.js').BackgroundDatasetWriteOperation>(
  operation: T,
  timeoutMs = 30_000
): Promise<import('./background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T> | undefined> {
  if (runtimeConfig.processRole === 'worker') {
    if (runtimeConfig.workerRole === 'ingest-worker') {
      const { handleDatasetWriteOperation } = await import('./background-dataset-writer.js')
      return await handleDatasetWriteOperation(operation) as import('./background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T>
    }
    if (typeof process.send !== 'function') {
      return undefined
    }
    if (pendingDatasetWriteRequests.size >= pendingDatasetWriteRequestMaxCount) {
      rejectedDatasetWriteRequestCount += 1
      rejectPendingBackgroundRequest('dataset-writer', pendingDatasetWriteRequests.size, pendingDatasetWriteRequestMaxCount, operation.type)
      throw new Error('后台 dataset-writer pending 请求过多，请稍后重试')
    }
    const requestId = randomUUID()
    return await new Promise<import('./background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T> | undefined>((resolve, reject) => {
      const timeout = setTimeout(() => {
        const pending = pendingDatasetWriteRequests.get(requestId)
        if (!pending) {
          return
        }
        timedOutDatasetWriteRequestCount += 1
        pendingDatasetWriteRequests.delete(requestId)
        pending.reject(new Error('后台 dataset-writer 请求超时'))
      }, timeoutMs)
      pendingDatasetWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout, createdAt: Date.now() })
      sendToParentOrServer({
        type: 'background_worker_dataset_write_request',
        requestId,
        operation
      }, (error) => {
        finishDatasetWriteRequest(requestId, undefined)
        markParentIpcBroken(error)
      })
    })
  }

  if (runtimeConfig.processRole === 'db-service') {
    const { requestDbServiceDatasetWrite } = await import('../db-service/db-service-ipc.js')
    return await requestDbServiceDatasetWrite(operation, timeoutMs) as import('./background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T> | undefined
  }

  if (runtimeConfig.processRole !== 'server') {
    return undefined
  }

  const child = ingestWorkerProcess
  if (!child || !child.connected || !ingestWorkerReady) {
    return undefined
  }
  if (pendingDatasetWriteRequests.size >= pendingDatasetWriteRequestMaxCount) {
    rejectedDatasetWriteRequestCount += 1
    rejectPendingBackgroundRequest('dataset-writer', pendingDatasetWriteRequests.size, pendingDatasetWriteRequestMaxCount, operation.type)
    return undefined
  }
  const requestId = randomUUID()
  return await new Promise<import('./background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T> | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      timedOutDatasetWriteRequestCount += 1
      finishDatasetWriteRequest(requestId, undefined)
    }, timeoutMs)
    pendingDatasetWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject: () => resolve(undefined), timeout, createdAt: Date.now() })
    try {
      child.send({
        type: 'background_worker_dataset_write_request',
        requestId,
        operation
      } satisfies BackgroundWorkerMessage, (error) => {
        if (error) {
          finishDatasetWriteRequest(requestId, undefined)
          markIpcBrokenForChild('ingest-worker', error, child)
        }
      })
    } catch (error) {
      finishDatasetWriteRequest(requestId, undefined)
      markIpcBrokenForChild('ingest-worker', error, child)
    }
  })
}

function finishDatasetWriteRequest(requestId: string, response: { ok: true; result: unknown } | { ok: false; errorMessage: string } | undefined): void {
  const pending = pendingDatasetWriteRequests.get(requestId)
  if (!pending) {
    return
  }
  clearTimeout(pending.timeout)
  pendingDatasetWriteRequests.delete(requestId)
  if (!response) {
    pending.resolve(undefined)
    return
  }
  if (response.ok) {
    pending.resolve(response.result)
    return
  }
  pending.reject(new Error(response.errorMessage))
}

export async function requestBackgroundWorkerStatsWrite<T extends import('./background-stats-writer.js').BackgroundStatsWriteOperation>(
  operation: T,
  timeoutMs = 10_000
): Promise<import('./background-stats-writer.js').BackgroundStatsWriteOperationResult<T> | undefined> {
  if (runtimeConfig.processRole === 'worker') {
    if (runtimeConfig.workerRole === 'stats-worker') {
      const { handleStatsWriteOperation } = await import('./background-stats-writer.js')
      return await handleStatsWriteOperation(operation) as import('./background-stats-writer.js').BackgroundStatsWriteOperationResult<T>
    }
    if (typeof process.send !== 'function') {
      return undefined
    }
    if (pendingStatsWriteRequests.size >= pendingStatsWriteRequestMaxCount) {
      rejectedStatsWriteRequestCount += 1
      rejectPendingBackgroundRequest('stats-writer', pendingStatsWriteRequests.size, pendingStatsWriteRequestMaxCount, operation.type)
      throw new Error('后台 stats-writer pending 请求过多，请稍后重试')
    }
    const requestId = randomUUID()
    return await new Promise<import('./background-stats-writer.js').BackgroundStatsWriteOperationResult<T> | undefined>((resolve, reject) => {
      const timeout = setTimeout(() => {
        const pending = pendingStatsWriteRequests.get(requestId)
        if (!pending) {
          return
        }
        timedOutStatsWriteRequestCount += 1
        pendingStatsWriteRequests.delete(requestId)
        pending.reject(new Error('后台 stats-writer 请求超时'))
      }, timeoutMs)
      pendingStatsWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout, createdAt: Date.now() })
      sendToParentOrServer({
        type: 'background_worker_stats_write_request',
        requestId,
        operation
      }, (error) => {
        finishStatsWriteRequest(requestId, undefined)
        markParentIpcBroken(error)
      })
    })
  }

  if (runtimeConfig.processRole !== 'server') {
    return undefined
  }

  const child = statsWorkerProcess
  if (!child || !child.connected || !statsWorkerReady) {
    return undefined
  }
  if (pendingStatsWriteRequests.size >= pendingStatsWriteRequestMaxCount) {
    rejectedStatsWriteRequestCount += 1
    rejectPendingBackgroundRequest('stats-writer', pendingStatsWriteRequests.size, pendingStatsWriteRequestMaxCount, operation.type)
    return undefined
  }
  const requestId = randomUUID()
  return await new Promise<import('./background-stats-writer.js').BackgroundStatsWriteOperationResult<T> | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      timedOutStatsWriteRequestCount += 1
      finishStatsWriteRequest(requestId, undefined)
    }, timeoutMs)
    pendingStatsWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject: () => resolve(undefined), timeout, createdAt: Date.now() })
    try {
      child.send({
        type: 'background_worker_stats_write_request',
        requestId,
        operation
      } satisfies BackgroundWorkerMessage, (error) => {
        if (error) {
          finishStatsWriteRequest(requestId, undefined)
          markRoleWorkerIpcBroken('stats-worker', error, child)
        }
      })
    } catch (error) {
      finishStatsWriteRequest(requestId, undefined)
      markRoleWorkerIpcBroken('stats-worker', error, child)
    }
  })
}

function finishStatsWriteRequest(requestId: string, response: { ok: true; result: unknown } | { ok: false; errorMessage: string } | undefined): void {
  const pending = pendingStatsWriteRequests.get(requestId)
  if (!pending) {
    return
  }
  clearTimeout(pending.timeout)
  pendingStatsWriteRequests.delete(requestId)
  if (!response) {
    pending.resolve(undefined)
    return
  }
  if (response.ok) {
    pending.resolve(response.result)
    return
  }
  pending.reject(new Error(response.errorMessage))
}

function oldestPendingRequestMs(
  requests: Map<string, { createdAt: number }>
): number {
  let oldestAt = 0
  for (const request of requests.values()) {
    if (oldestAt === 0 || request.createdAt < oldestAt) {
      oldestAt = request.createdAt
    }
  }
  return oldestAt === 0 ? 0 : Math.max(0, Date.now() - oldestAt)
}

function rejectPendingBackgroundRequest(
  channel: 'dataset-writer' | 'stats-writer' | 'background-db-service',
  pendingCount: number,
  maxPendingCount: number,
  operationType: string
): void {
  logger.warn({
    event: 'background_direct_ipc_pending_full',
    channel,
    operationType,
    pendingCount,
    maxPendingCount
  }, '后台直连 IPC pending 请求已达上限，已拒绝本次请求')
}

function queueWorkerMessage(inputMessage: BackgroundWorkerMessage): boolean {
  if (runtimeConfig.queueDriver === 'redis_stream' && isRedisStreamManagedIngestQueueMessage(inputMessage)) {
    rejectRedisStreamLocalQueueMessage(inputMessage, 'queueWorkerMessage')
    return false
  }
  const message = coalesceWorkerMessage(inputMessage)
  if (!message) {
    flushWorkerMessageQueue()
    return true
  }
  const targetRole = workerMessageTargetRole(message)
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  if (!canQueueWorkerMessage(targetRole, message, messageBytes, queueKey)) {
    const runtime = pendingQueueRuntimeForTarget(targetRole)
    runtime[queueKey].rejectedCount = (runtime[queueKey].rejectedCount ?? 0) + 1
    return false
  }
  if (targetRole === 'ingest-worker') {
    if (message.type === 'background_worker_usage_records') {
      ingestUsageRecordMessageQueue.push(message)
      ingestUsageRecordMessageQueueBytes += messageBytes
    } else {
      ingestRegularWorkerMessageQueue.push(message)
      ingestRegularWorkerMessageQueueBytes += messageBytes
    }
  } else if (targetRole === 'ops-worker') {
    opsWorkerMessageQueue.push(message)
    opsWorkerMessageQueueBytes += messageBytes
  } else {
    regularWorkerMessageQueue.push(message)
    regularWorkerMessageQueueBytes += messageBytes
  }
  addPendingQueueRuntimeMessage(targetRole, queueKey, messageBytes)

  flushTargetWorkerMessageQueue(targetRole)
  return true
}

function coalesceWorkerMessage(message: BackgroundWorkerMessage): BackgroundWorkerMessage | undefined {
  if (message.type === 'background_worker_audit_logs') {
    return coalesceAuditLogMessage(message)
  }
  if (message.type !== 'background_worker_record_maintenance') {
    return message
  }
  const compactedItems = compactRecordMaintenanceJobsForCoalescing(message.items)
  const remainingItems: RecordMaintenanceJob[] = []
  for (const job of compactedItems) {
    if (coalesceRecordMaintenanceJobIntoPendingQueue(job)) {
      continue
    }
    remainingItems.push(job)
  }
  if (remainingItems.length === 0) {
    return undefined
  }
  return remainingItems.length === message.items.length
    ? message
    : { ...message, items: remainingItems }
}

function coalesceAuditLogMessage(
  message: Extract<BackgroundWorkerMessage, { type: 'background_worker_audit_logs' }>
): BackgroundWorkerMessage | undefined {
  if (message.items.length === 0) {
    return undefined
  }
  const queueIndex = ingestRegularWorkerMessageQueue.findIndex((queued) => queued.type === 'background_worker_audit_logs')
  if (queueIndex < 0) {
    return message
  }
  const current = ingestRegularWorkerMessageQueue.at(queueIndex)
  if (!current || current.type !== 'background_worker_audit_logs') {
    return message
  }
  const currentBytes = estimateWorkerMessageBytes(current)
  const nextMessage: BackgroundWorkerMessage = {
    ...current,
    items: [...current.items, ...message.items]
  }
  const nextBytes = estimateWorkerMessageBytes(nextMessage)
  const runtime = ingestPendingQueueRuntime.auditLogs
  const nextQueueBytes = (runtime.queueBytes ?? 0) - currentBytes + nextBytes
  if (nextBytes > auditWorkerMessageMaxBytes || nextQueueBytes > regularWorkerMessageQueueMaxBytes) {
    return message
  }
  ingestRegularWorkerMessageQueue.set(queueIndex, nextMessage)
  ingestRegularWorkerMessageQueueBytes = Math.max(0, ingestRegularWorkerMessageQueueBytes - currentBytes + nextBytes)
  runtime.queueBytes = Math.max(0, (runtime.queueBytes ?? 0) - currentBytes + nextBytes)
  return undefined
}

function compactRecordMaintenanceJobsForCoalescing(items: RecordMaintenanceJob[]): RecordMaintenanceJob[] {
  const lastIndexByKey = new Map<string, number>()
  items.forEach((job, index) => {
    const key = recordMaintenanceJobCoalescingKey(job)
    if (key) {
      lastIndexByKey.set(key, index)
    }
  })
  if (lastIndexByKey.size === 0) {
    return items
  }
  return items.filter((job, index) => {
    const key = recordMaintenanceJobCoalescingKey(job)
    return !key || lastIndexByKey.get(key) === index
  })
}

function coalesceRecordMaintenanceJobIntoPendingQueue(job: RecordMaintenanceJob): boolean {
  const key = recordMaintenanceJobCoalescingKey(job)
  if (!key) {
    return false
  }
  const queueIndex = ingestRegularWorkerMessageQueue.findIndex((queued) => (
    queued.type === 'background_worker_record_maintenance'
    && queued.items.some((item) => recordMaintenanceJobCoalescingKey(item) === key)
  ))
  if (queueIndex < 0) {
    return false
  }
  const current = ingestRegularWorkerMessageQueue.at(queueIndex)
  if (!current || current.type !== 'background_worker_record_maintenance') {
    return false
  }
  const currentBytes = estimateWorkerMessageBytes(current)
  const nextItems = compactRecordMaintenanceJobsForCoalescing(current.items.map((item) => (
    recordMaintenanceJobCoalescingKey(item) === key ? job : item
  )))
  const nextMessage: BackgroundWorkerMessage = { ...current, items: nextItems }
  const nextBytes = estimateWorkerMessageBytes(nextMessage)
  const runtime = ingestPendingQueueRuntime.recordMaintenance
  const nextQueueBytes = (runtime.queueBytes ?? 0) - currentBytes + nextBytes
  if (nextBytes > regularWorkerMessageMaxBytes || nextQueueBytes > regularWorkerMessageQueueMaxBytes) {
    return false
  }
  ingestRegularWorkerMessageQueue.set(queueIndex, nextMessage)
  ingestRegularWorkerMessageQueueBytes = Math.max(0, ingestRegularWorkerMessageQueueBytes - currentBytes + nextBytes)
  runtime.queueBytes = Math.max(0, (runtime.queueBytes ?? 0) - currentBytes + nextBytes)
  return true
}

function recordMaintenanceJobCoalescingKey(job: RecordMaintenanceJob): string | undefined {
  return job.type === 'account_usage_snapshot_upsert'
    ? `${job.accountId}\u0000${job.kind}`
    : undefined
}

function canQueueWorkerMessage(
  targetRole: BackgroundWorkerQueueTargetRole,
  message: BackgroundWorkerMessage,
  messageBytes: number,
  queueKey: IpcQueueKey
): boolean {
  if (message.type === 'background_worker_usage_records') {
    return targetRole === 'ingest-worker'
      && ingestUsageRecordMessageQueue.length < usageRecordMessageQueueMaxMessages
      && messageBytes <= usageRecordWorkerMessageMaxBytes
      && ingestUsageRecordMessageQueueBytes + messageBytes <= usageRecordMessageQueueMaxBytes
  }
  const regularQueueRuntime = regularQueueCapacityRuntimeForMessage(targetRole, queueKey)
  const regularQueueLength = regularQueueRuntime.queueLength
  const regularQueueBytes = regularQueueRuntime.queueBytes
  if (message.type === 'background_worker_audit_logs') {
    return regularQueueLength < regularWorkerMessageQueueMaxMessages
      && messageBytes <= auditWorkerMessageMaxBytes
      && regularQueueBytes + messageBytes <= regularWorkerMessageQueueMaxBytes
  }
  if (message.type === 'background_worker_public_api_logs') {
    return regularQueueLength < regularWorkerMessageQueueMaxMessages
      && messageBytes <= regularWorkerMessageMaxBytes
      && regularQueueBytes + messageBytes <= regularWorkerMessageQueueMaxBytes
  }
  return regularQueueLength < regularWorkerMessageQueueMaxMessages
    && messageBytes <= regularWorkerMessageMaxBytes
    && regularQueueBytes + messageBytes <= regularWorkerMessageQueueMaxBytes
}

function regularQueueCapacityRuntimeForMessage(
  targetRole: BackgroundWorkerQueueTargetRole,
  queueKey: IpcQueueKey
): { queueLength: number; queueBytes: number } {
  if (targetRole === 'ingest-worker') {
    return queueKey === 'recordMaintenance'
      ? {
          queueLength: ingestPendingQueueRuntime.recordMaintenance.queueLength,
          queueBytes: ingestPendingQueueRuntime.recordMaintenance.queueBytes ?? 0
        }
      : ingestHighPriorityRegularQueueRuntime()
  }
  if (targetRole === 'ops-worker') {
    return { queueLength: opsWorkerMessageQueue.length, queueBytes: opsWorkerMessageQueueBytes }
  }
  return { queueLength: regularWorkerMessageQueue.length, queueBytes: regularWorkerMessageQueueBytes }
}

function ingestHighPriorityRegularQueueRuntime(): { queueLength: number; queueBytes: number } {
  const runtime = buildIngestPendingQueuesRuntime()
  runtime.usageRecords.queueLength = 0
  runtime.usageRecords.queueBytes = 0
  runtime.recordMaintenance.queueLength = 0
  runtime.recordMaintenance.queueBytes = 0
  return {
    queueLength: Object.values(runtime).reduce((total, queue) => total + queue.queueLength, 0),
    queueBytes: Object.values(runtime).reduce((total, queue) => total + (queue.queueBytes ?? 0), 0)
  }
}

function flushTargetWorkerMessageQueue(role: BackgroundWorkerQueueTargetRole): void {
  if (role === 'ingest-worker') {
    flushIngestWorkerMessageQueue()
    return
  }
  if (role === 'ops-worker') {
    flushOpsWorkerMessageQueue()
    return
  }
  flushWorkerMessageQueue()
}

function flushWorkerMessageQueue(): void {
  const child = workerProcess
  if (sendingMessage || !child || !workerReady) {
    return
  }

  const message = shiftWorkerMessage()
  if (!message) {
    return
  }

  sendingMessage = true
  sendingWorkerMessage = message
  try {
    const accepted = child.send(message, (error) => {
      const stillSendingThisMessage = sendingWorkerMessage === message
      if (stillSendingThisMessage) {
        sendingMessage = false
        sendingWorkerMessage = undefined
      }
      if (error) {
        if (stillSendingThisMessage) {
          requeueWorkerMessageFirst(message)
        }
        markWorkerIpcBroken(error, child)
        return
      }
      if (stillSendingThisMessage) {
        flushWorkerMessageQueue()
      }
    })
    if (!accepted) {
      // 由 callback 继续驱动后续发送。
    }
  } catch (error) {
    sendingMessage = false
    sendingWorkerMessage = undefined
    requeueWorkerMessageFirst(message)
    markWorkerIpcBroken(error, child)
    forwardSupervisorOutput(process.stderr, `[background-worker] 向 worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function flushIngestWorkerMessageQueue(): void {
  const child = ingestWorkerProcess
  if (sendingIngestMessage || !child || !ingestWorkerReady) {
    return
  }

  const message = shiftIngestWorkerMessage()
  if (!message) {
    return
  }

  sendingIngestMessage = true
  sendingIngestWorkerMessage = message
  try {
    const accepted = child.send(message, (error) => {
      const stillSendingThisMessage = sendingIngestWorkerMessage === message
      if (stillSendingThisMessage) {
        sendingIngestMessage = false
        sendingIngestWorkerMessage = undefined
      }
      if (error) {
        if (stillSendingThisMessage) {
          requeueIngestWorkerMessageFirst(message)
        }
        markIngestWorkerIpcBroken(error, child)
        return
      }
      if (stillSendingThisMessage) {
        flushIngestWorkerMessageQueue()
      }
    })
    if (!accepted) {
      // 由 callback 继续驱动后续发送。
    }
  } catch (error) {
    sendingIngestMessage = false
    sendingIngestWorkerMessage = undefined
    requeueIngestWorkerMessageFirst(message)
    markIngestWorkerIpcBroken(error, child)
    forwardSupervisorOutput(process.stderr, `[background-worker] 向 ingest-worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function flushOpsWorkerMessageQueue(): void {
  const child = opsWorkerProcess
  if (sendingOpsMessage || !child || !opsWorkerReady) {
    return
  }

  const message = shiftOpsWorkerMessage()
  if (!message) {
    return
  }

  sendingOpsMessage = true
  sendingOpsWorkerMessage = message
  try {
    child.send(message, (error) => {
      const stillSendingThisMessage = sendingOpsWorkerMessage === message
      if (stillSendingThisMessage) {
        sendingOpsMessage = false
        sendingOpsWorkerMessage = undefined
      }
      if (error) {
        if (stillSendingThisMessage) {
          requeueOpsWorkerMessageFirst(message)
        }
        markRoleWorkerIpcBroken('ops-worker', error, child)
        return
      }
      if (stillSendingThisMessage) {
        flushOpsWorkerMessageQueue()
      }
    })
  } catch (error) {
    sendingOpsMessage = false
    sendingOpsWorkerMessage = undefined
    requeueOpsWorkerMessageFirst(message)
    markRoleWorkerIpcBroken('ops-worker', error, child)
    forwardSupervisorOutput(process.stderr, `[background-worker] 向 ops-worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function shiftWorkerMessage(): BackgroundWorkerMessage | undefined {
  let queueKey: IpcQueueKey | undefined
  const message = regularWorkerMessageQueue.shift()
  if (message) {
    queueKey = ipcQueueKeyForMessage(message)
    const messageBytes = estimateWorkerMessageBytes(message)
    regularWorkerMessageQueueBytes = Math.max(0, regularWorkerMessageQueueBytes - messageBytes)
    removePendingQueueRuntimeMessage('worker', queueKey, messageBytes)
  }
  return message
}

function shiftIngestWorkerMessage(): BackgroundWorkerMessage | undefined {
  let queueKey: IpcQueueKey | undefined
  let message: BackgroundWorkerMessage | undefined
  if (shouldShiftIngestRegularBeforeUsage()) {
    message = shiftIngestRegularWorkerMessage()
    if (message) {
      consecutiveIngestUsageMessages = 0
    }
  }
  if (!message) {
    const usageMessage = ingestUsageRecordMessageQueue.shift()
    if (usageMessage) {
      message = usageMessage
      consecutiveIngestUsageMessages += 1
    } else {
      message = shiftIngestRegularWorkerMessage()
      if (message) {
        consecutiveIngestUsageMessages = 0
      }
    }
  }
  if (message) {
    queueKey = ipcQueueKeyForMessage(message)
    const messageBytes = estimateWorkerMessageBytes(message)
    if (message.type === 'background_worker_usage_records') {
      ingestUsageRecordMessageQueueBytes = Math.max(0, ingestUsageRecordMessageQueueBytes - messageBytes)
    } else {
      ingestRegularWorkerMessageQueueBytes = Math.max(0, ingestRegularWorkerMessageQueueBytes - messageBytes)
    }
    removePendingQueueRuntimeMessage('ingest-worker', queueKey, messageBytes)
  }
  return message
}

function shouldShiftIngestRegularBeforeUsage(): boolean {
  return consecutiveIngestUsageMessages >= ingestUsageBurstBeforeRegular
    && ingestRegularWorkerMessageQueue.length > 0
}

function shiftIngestRegularWorkerMessage(): BackgroundWorkerMessage | undefined {
  const highPriorityIndex = ingestRegularWorkerMessageQueue.findIndex((message) => (
    ipcQueueKeyForMessage(message) !== 'recordMaintenance'
  ))
  if (highPriorityIndex >= 0) {
    return ingestRegularWorkerMessageQueue.removeAt(highPriorityIndex)
  }
  return ingestRegularWorkerMessageQueue.shift()
}

function shiftOpsWorkerMessage(): BackgroundWorkerMessage | undefined {
  const message = opsWorkerMessageQueue.shift()
  if (message) {
    const queueKey = ipcQueueKeyForMessage(message)
    const messageBytes = estimateWorkerMessageBytes(message)
    opsWorkerMessageQueueBytes = Math.max(0, opsWorkerMessageQueueBytes - messageBytes)
    removePendingQueueRuntimeMessage('ops-worker', queueKey, messageBytes)
  }
  return message
}

function requeueWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  regularWorkerMessageQueue.unshift(message)
  regularWorkerMessageQueueBytes += messageBytes
  addPendingQueueRuntimeMessage('worker', queueKey, messageBytes)
}

function requeueIngestWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  if (runtimeConfig.queueDriver === 'redis_stream' && isRedisStreamManagedIngestQueueMessage(message)) {
    rejectRedisStreamLocalQueueMessage(message, 'requeueIngestWorkerMessageFirst')
    return
  }
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  if (message.type === 'background_worker_usage_records') {
    ingestUsageRecordMessageQueue.unshift(message)
    ingestUsageRecordMessageQueueBytes += messageBytes
  } else {
    ingestRegularWorkerMessageQueue.unshift(message)
    ingestRegularWorkerMessageQueueBytes += messageBytes
  }
  addPendingQueueRuntimeMessage('ingest-worker', queueKey, messageBytes)
}

function isRedisStreamManagedIngestQueueMessage(message: BackgroundWorkerMessage): boolean {
  switch (message.type) {
    case 'background_worker_usage_records':
    case 'background_worker_audit_logs':
    case 'background_worker_operation_logs':
    case 'background_worker_public_api_logs':
    case 'background_worker_record_maintenance':
      return true
    default:
      return false
  }
}

function rejectRedisStreamLocalQueueMessage(message: BackgroundWorkerMessage, operation: string): void {
  const queueKey = ipcQueueKeyForMessage(message)
  const queue = ingestPendingQueueRuntime[queueKey]
  queue.rejectedCount = (queue.rejectedCount ?? 0) + 1
  logger.error({
    event: 'redis_stream_local_ipc_queue_rejected',
    operation,
    messageType: message.type,
    queueKey
  }, 'Redis Stream queue driver 下禁止使用后台 IPC 本地队列，记录类数据必须写入 Redis Stream')
}

function requeueOpsWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  opsWorkerMessageQueue.unshift(message)
  opsWorkerMessageQueueBytes += messageBytes
  addPendingQueueRuntimeMessage('ops-worker', queueKey, messageBytes)
}

function buildIngestPendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  const runtime = clonePendingQueueRuntime(ingestPendingQueueRuntime)
  const oldestCreatedAt = oldestIngestUsageRecordMessageCreatedAt()
  if (oldestCreatedAt) {
    runtime.usageRecords.oldestCreatedAt = oldestCreatedAt
  }
  return runtime
}

function buildOpsPendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return clonePendingQueueRuntime(opsPendingQueueRuntime)
}

function buildAggregatePendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return mergePendingQueuesRuntime(
    pendingQueueRuntime,
    mergePendingQueuesRuntime(ingestPendingQueueRuntime, opsPendingQueueRuntime)
  )
}

function oldestIngestUsageRecordMessageCreatedAt(): string | undefined {
  let oldest: string | undefined
  for (let index = 0; index < ingestUsageRecordMessageQueue.length; index += 1) {
    const message = ingestUsageRecordMessageQueue.at(index)
    if (!message) continue
    for (const item of message.items) {
      const createdAt = item.createdAt?.trim()
      if (!createdAt) continue
      if (!oldest || createdAt < oldest) {
        oldest = createdAt
      }
    }
  }
  return oldest
}

function addPendingQueueRuntimeMessage(targetRole: BackgroundWorkerQueueTargetRole, key: IpcQueueKey, bytes: number): void {
  const queue = pendingQueueRuntimeForTarget(targetRole)[key]
  queue.queueLength += 1
  queue.queueBytes = (queue.queueBytes ?? 0) + bytes
}

function removePendingQueueRuntimeMessage(targetRole: BackgroundWorkerQueueTargetRole, key: IpcQueueKey, bytes: number): void {
  const queue = pendingQueueRuntimeForTarget(targetRole)[key]
  queue.queueLength = Math.max(0, queue.queueLength - 1)
  queue.queueBytes = Math.max(0, (queue.queueBytes ?? 0) - bytes)
}

function pendingQueueRuntimeForTarget(targetRole: BackgroundWorkerQueueTargetRole): BackgroundWorkerIpcQueuesRuntime {
  if (targetRole === 'ingest-worker') return ingestPendingQueueRuntime
  if (targetRole === 'ops-worker') return opsPendingQueueRuntime
  return pendingQueueRuntime
}

function processForRole(role: BackgroundWorkerProcessRole): ChildProcess | undefined {
  switch (role) {
    case 'ingest-worker':
      return ingestWorkerProcess
    case 'stats-worker':
      return statsWorkerProcess
    case 'ops-worker':
      return opsWorkerProcess
    default:
      return workerProcess
  }
}

function readyForRole(role: BackgroundWorkerProcessRole): boolean {
  switch (role) {
    case 'ingest-worker':
      return ingestWorkerReady
    case 'stats-worker':
      return statsWorkerReady
    case 'ops-worker':
      return opsWorkerReady
    default:
      return workerReady
  }
}

function setReadyForRole(role: BackgroundWorkerProcessRole, ready: boolean): void {
  switch (role) {
    case 'stats-worker':
      statsWorkerReady = ready
      break
    case 'ops-worker':
      opsWorkerReady = ready
      break
    default:
      break
  }
}

function setPidForRole(role: BackgroundWorkerProcessRole, pid: number | undefined): void {
  switch (role) {
    case 'stats-worker':
      statsWorkerPid = pid ?? statsWorkerPid
      break
    case 'ops-worker':
      opsWorkerPid = pid ?? opsWorkerPid
      break
    default:
      break
  }
}

function markWorkerReady(role: BackgroundWorkerProcessRole, record: Partial<BackgroundWorkerMessage> & Record<string, unknown>): void {
  if (role === 'ingest-worker') {
    ingestWorkerReady = true
    ingestWorkerPid = workerPidFromReadyRecord(record, ingestWorkerPid)
    ingestWorkerReadyHandler?.()
    flushIngestWorkerMessageQueue()
    return
  }
  if (role === 'stats-worker') {
    statsWorkerReady = true
    statsWorkerPid = workerPidFromReadyRecord(record, statsWorkerPid)
    statsWorkerReadyHandler?.()
    return
  }
  if (role === 'ops-worker') {
    opsWorkerReady = true
    opsWorkerPid = workerPidFromReadyRecord(record, opsWorkerPid)
    opsWorkerReadyHandler?.()
    flushOpsWorkerMessageQueue()
    return
  }

  workerReady = true
  workerPid = workerPidFromReadyRecord(record, workerPid)
  backgroundWorkerReadyHandler?.()
  flushWorkerMessageQueue()
}

function finishWorkerStatusResponse(role: BackgroundWorkerProcessRole, requestId: string, snapshot: BackgroundWorkerRuntimeSnapshot | undefined): void {
  finishWorkerSnapshotResponse(role, requestId, snapshot)
}

function finishParentIngestStatusRequest(requestId: string, status: BackgroundWorkerIngestDrainStatus | undefined): void {
  finishIpcPendingRequest(pendingParentIngestStatusRequests, requestId, status)
}

function failPendingRequests(): void {
  failWorkerSnapshotPendingRequests('worker')
  failPendingProcessEventLoopRequests()
}

function failIngestPendingRequests(): void {
  failWorkerSnapshotPendingRequests('ingest-worker')
  failDatasetPendingRequests()
}

function failStatsPendingRequests(): void {
  failWorkerSnapshotPendingRequests('stats-worker')
}

function failDatasetPendingRequests(): void {
  for (const [requestId, pending] of pendingDatasetWriteRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingDatasetWriteRequests.delete(requestId)
  }
}

function failOpsPendingRequests(): void {
  failWorkerSnapshotPendingRequests('ops-worker')
}

function failPendingProcessEventLoopRequests(): void {
  failIpcPendingRequests(pendingProcessEventLoopRequests)
}

function finishProcessEventLoopRequest(requestId: string, samples: ProcessEventLoopSample[] | undefined): void {
  finishIpcPendingRequest(pendingProcessEventLoopRequests, requestId, samples)
}

function nonEmptyProcessEventLoopSamples(samples: unknown[]): ProcessEventLoopSample[] | undefined {
  return samples.length > 0 ? samples as ProcessEventLoopSample[] : undefined
}

function markWorkerIpcBroken(error: unknown, child = workerProcess): void {
  const isCurrentChild = child === undefined || workerProcess === child
  if (isCurrentChild) {
    workerReady = false
    workerPid = workerPidFromBrokenChild(child, workerPid)
    failPendingRequests()
  }
  terminateBrokenWorkerIpc('worker', error, child)
}

function markIngestWorkerIpcBroken(error: unknown, child = ingestWorkerProcess): void {
  const isCurrentChild = child === undefined || ingestWorkerProcess === child
  if (isCurrentChild) {
    ingestWorkerReady = false
    ingestWorkerPid = workerPidFromBrokenChild(child, ingestWorkerPid)
    failIngestPendingRequests()
  }
  terminateBrokenWorkerIpc('ingest-worker', error, child)
}

function markRoleWorkerIpcBroken(
  role: BackgroundWorkerSnapshotRole,
  error: unknown,
  child = processForRole(role)
): void {
  const isCurrentChild = child === undefined || processForRole(role) === child
  if (isCurrentChild) {
    setReadyForRole(role, false)
    setPidForRole(role, workerPidFromBrokenChild(child, undefined))
    failWorkerSnapshotPendingRequests(role)
  }
  terminateBrokenWorkerIpc(role, error, child)
}

function roleForChild(child: ChildProcess | undefined): BackgroundWorkerProcessRole {
  return roleForBackgroundWorkerChild(child, {
    ingestWorkerProcess,
    statsWorkerProcess,
    opsWorkerProcess
  })
}

function markIpcBrokenForChild(role: BackgroundWorkerProcessRole, error: unknown, child: ChildProcess | undefined): void {
  if (role === 'ingest-worker') {
    markIngestWorkerIpcBroken(error, child)
    return
  }
  if (isSnapshotRoleWorker(role)) {
    markRoleWorkerIpcBroken(role, error, child)
    return
  }
  markWorkerIpcBroken(error, child)
}

function markParentIpcBroken(error: unknown): void {
  failPendingProcessEventLoopRequests()
  writeParentIpcBrokenLog(error)
  process.exit(1)
}

async function clearServerGatewayRuntimeCache(): Promise<void> {
  const dbServiceIpc = await import('../db-service/db-service-ipc.js')
  const gatewayCache = await import('../gateway/runtime/runtime-cache.service.js')
  gatewayCache.clearGatewayRuntimeCacheLocal()
  dbServiceIpc.clearDbServiceGatewayRuntimeCache()
}

async function clearServerAccountRuntimeAvailability(target: AccountRuntimeAvailabilityClearTarget): Promise<void> {
  const gatewaySideEffects = await import('../gateway/runtime/account-side-effects.service.js')
  gatewaySideEffects.clearGatewayAutomaticAccountRuntimeAvailability(target)
}

async function buildIngestWorkerDrainStatus(timeoutMs = 1000): Promise<BackgroundWorkerIngestDrainStatus> {
  const snapshot = await requestIngestWorkerSnapshot(timeoutMs).catch(() => undefined)
  return {
    pid: snapshot?.pid ?? ingestWorkerPid,
    ready: snapshot?.ready ?? ingestWorkerReady,
    snapshot,
    pendingQueues: buildIngestPendingQueuesRuntime()
  }
}

async function respondToIngestStatusRequest(requestId: string, targetChild: ChildProcess | undefined): Promise<void> {
  const child = targetChild
  if (!child || !child.connected) {
    return
  }
  const status = await buildIngestWorkerDrainStatus(5000).catch(() => undefined)
  try {
    child.send({
      type: 'background_worker_ingest_status_response',
      requestId,
      status
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markIpcBrokenForChild(roleForChild(child), error, child)
      }
    })
  } catch (error) {
    markIpcBrokenForChild(roleForChild(child), error, child)
  }
}

async function replaceServerGatewayQuotaSnapshot(snapshot: GatewayQuotaSnapshot): Promise<void> {
  const quotaSnapshotCache = await import('../gateway/quota/quota-snapshot-cache.service.js')
  quotaSnapshotCache.replaceGatewayQuotaSnapshot(snapshot)
}

async function replaceServerClientIpPolicySnapshot(policies: ActiveClientIpPolicy[]): Promise<void> {
  const policyCache = await import('../gateway/runtime/client-ip-policy-cache.service.js')
  await policyCache.replaceClientIpPolicySharedSnapshotAsync(policies)
}

function isAccountRuntimeClearTarget(value: unknown): value is AccountRuntimeAvailabilityClearTarget {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  if (typeof record.accountId !== 'string' || !record.accountId.trim()) {
    return false
  }
  if (record.authorizedBinding === undefined) {
    return true
  }
  if (typeof record.authorizedBinding !== 'object' || record.authorizedBinding === null || Array.isArray(record.authorizedBinding)) {
    return false
  }
  const binding = record.authorizedBinding as Record<string, unknown>
  return optionalString(binding.systemAccountId) !== undefined
    || optionalString(binding.groupId) !== undefined
    || optionalString(binding.accountAuthorizationId) !== undefined
}

function isGatewayQuotaSnapshot(value: unknown): value is GatewayQuotaSnapshot {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return typeof record.generatedAt === 'string'
    && Array.isArray(record.costEntries)
    && Array.isArray(record.authorizationEntries)
}

function isActiveClientIpPolicyArray(value: unknown): value is ActiveClientIpPolicy[] {
  return Array.isArray(value) && value.every(isActiveClientIpPolicy)
}

function isActiveClientIpPolicy(value: unknown): value is ActiveClientIpPolicy {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return typeof record.id === 'string'
    && typeof record.ipHash === 'string'
    && (record.policyType === 'blacklist' || record.policyType === 'allowlist')
    && typeof record.aggregateIpKey === 'string'
    && typeof record.clientIp === 'string'
    && (record.reason === undefined || typeof record.reason === 'string')
    && (record.expiresAt === undefined || typeof record.expiresAt === 'string')
}

function optionalString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}

function normalizedString(value: unknown): string | undefined {
  return optionalString(value)
}

async function respondToProcessEventLoopRequest(requestId: string, targetChild = workerProcess): Promise<void> {
  const samples = [
    buildProcessEventLoopSample('server')
  ]
  const dbServiceSample = await dbServiceProcessEventLoopSample()
  if (dbServiceSample) {
    samples.push(dbServiceSample)
  }
  for (const role of processEventLoopWorkerRoles()) {
    if (targetChild === processForRole(role)) {
      continue
    }
    const workerSamples = await requestChildProcessEventLoopSamples(role)
    if (workerSamples) {
      samples.push(...workerSamples)
    }
  }

  const child = targetChild
  if (!child || !child.connected) {
    return
  }
  try {
    child.send({
      type: 'background_worker_process_event_loop_response',
      requestId,
      samples
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markIpcBrokenForChild(roleForChild(child), error, child)
      }
    })
  } catch (error) {
    markIpcBrokenForChild(roleForChild(child), error, child)
  }
}

async function dbServiceProcessEventLoopSample(): Promise<ProcessEventLoopSample | undefined> {
  try {
    const dbServiceIpc = await import('../db-service/db-service-ipc.js')
    return await dbServiceIpc.requestDbServiceProcessEventLoopSample(800)
  } catch {
    return undefined
  }
}

async function respondToDbServiceRequest(
  requestId: string,
  operation: import('../db-service/db-service-types.js').DbServiceOperation,
  targetChild: ChildProcess | undefined,
  options: BackgroundWorkerDbServiceRequestOptions | undefined
): Promise<void> {
  const child = targetChild
  if (!child || !child.connected) {
    return
  }
  try {
    const { requestDbService } = await import('../db-service/db-service-ipc.js')
    const result = await requestDbService(operation, backgroundDbServiceRequestOptionsForOperation(operation, options))
    child.send({
      type: 'background_worker_db_service_response',
      requestId,
      ok: true,
      result
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markIpcBrokenForChild(roleForChild(child), error, child)
      }
    })
  } catch (error) {
    try {
      child.send({
        type: 'background_worker_db_service_response',
        requestId,
        ok: false,
        errorMessage: error instanceof Error ? error.message : String(error)
      } satisfies BackgroundWorkerMessage, (sendError) => {
        if (sendError) {
          markIpcBrokenForChild(roleForChild(child), sendError, child)
        }
      })
    } catch (sendError) {
      markIpcBrokenForChild(roleForChild(child), sendError, child)
    }
  }
}

async function forwardDatasetWriteRequest(
  requestId: string,
  operation: import('./background-dataset-writer.js').BackgroundDatasetWriteOperation,
  requesterChild: ChildProcess | undefined
): Promise<void> {
  const requester = requesterChild
  if (!requester || !requester.connected) {
    return
  }
  try {
    const result = await requestBackgroundWorkerDatasetWrite(operation)
    if (result === undefined) {
      throw new Error(`dataset-writer 不可用，无法执行数据集写操作：${operation.type}`)
    }
    requester.send({
      type: 'background_worker_dataset_write_response',
      requestId,
      ok: true,
      result
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markIpcBrokenForChild(roleForChild(requester), error, requester)
      }
    })
  } catch (error) {
    try {
      requester.send({
        type: 'background_worker_dataset_write_response',
        requestId,
        ok: false,
        errorMessage: error instanceof Error ? error.message : String(error)
      } satisfies BackgroundWorkerMessage, (sendError) => {
        if (sendError) {
          markIpcBrokenForChild(roleForChild(requester), sendError, requester)
        }
      })
    } catch (sendError) {
      markIpcBrokenForChild(roleForChild(requester), sendError, requester)
    }
  }
}

async function forwardStatsWriteRequest(
  requestId: string,
  operation: import('./background-stats-writer.js').BackgroundStatsWriteOperation,
  requesterChild: ChildProcess | undefined
): Promise<void> {
  const requester = requesterChild
  if (!requester || !requester.connected) {
    return
  }
  try {
    const result = await requestBackgroundWorkerStatsWrite(operation)
    if (result === undefined) {
      throw new Error(`stats-writer 不可用，无法执行统计写操作：${operation.type}`)
    }
    requester.send({
      type: 'background_worker_stats_write_response',
      requestId,
      ok: true,
      result
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        markIpcBrokenForChild(roleForChild(requester), error, requester)
      }
    })
  } catch (error) {
    try {
      requester.send({
        type: 'background_worker_stats_write_response',
        requestId,
        ok: false,
        errorMessage: error instanceof Error ? error.message : String(error)
      } satisfies BackgroundWorkerMessage, (sendError) => {
        if (sendError) {
          markIpcBrokenForChild(roleForChild(requester), sendError, requester)
        }
      })
    } catch (sendError) {
      markIpcBrokenForChild(roleForChild(requester), sendError, requester)
    }
  }
}

interface PendingDbServiceRequest {
  resolve: (result: unknown) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
  createdAt: number
}

const pendingBackgroundDbServiceRequests = new Map<string, PendingDbServiceRequest>()

export async function requestBackgroundWorkerDbService<T extends import('../db-service/db-service-types.js').DbServiceOperation>(
  operation: T,
  inputOptions: number | BackgroundWorkerDbServiceRequestOptions = {}
): Promise<import('../db-service/db-service-types.js').DbServiceOperationResult<T> | undefined> {
  const options = normalizeBackgroundWorkerDbServiceRequestOptions(inputOptions)
  const timeoutMs = options.timeoutMs ?? 5000
  if (runtimeConfig.processRole === 'server' || runtimeConfig.processRole === 'db-service') {
    const { requestDbService } = await import('../db-service/db-service-ipc.js')
    return await requestDbService(operation, backgroundDbServiceRequestOptionsForOperation(operation, options))
  }
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return undefined
  }

  if (pendingBackgroundDbServiceRequests.size >= pendingBackgroundDbServiceRequestMaxCount) {
    rejectedBackgroundDbServiceRequestCount += 1
    rejectPendingBackgroundRequest('background-db-service', pendingBackgroundDbServiceRequests.size, pendingBackgroundDbServiceRequestMaxCount, operation.type)
    throw new Error('后台 DB service pending 请求过多，请稍后重试')
  }
  const requestId = randomUUID()
  return await new Promise<import('../db-service/db-service-types.js').DbServiceOperationResult<T> | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      const pending = pendingBackgroundDbServiceRequests.get(requestId)
      if (!pending) {
        return
      }
      timedOutBackgroundDbServiceRequestCount += 1
      pendingBackgroundDbServiceRequests.delete(requestId)
      pending.reject(new Error(`后台 DB service 请求超时：${operation.type}`))
    }, timeoutMs)
    pendingBackgroundDbServiceRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout, createdAt: Date.now() })
    sendToParentOrServer({
      type: 'background_worker_db_service_request',
      requestId,
      operation,
      options
    }, (error) => {
      finishBackgroundDbServiceRequest(requestId, undefined)
      markParentIpcBroken(error)
    })
  })
}

function normalizeBackgroundWorkerDbServiceRequestOptions(
  input: number | BackgroundWorkerDbServiceRequestOptions
): BackgroundWorkerDbServiceRequestOptions {
  if (typeof input === 'number') {
    return { timeoutMs: input }
  }
  return { ...input }
}

function backgroundDbServiceRequestOptionsForOperation(
  operation: import('../db-service/db-service-types.js').DbServiceOperation,
  options: BackgroundWorkerDbServiceRequestOptions | undefined
): BackgroundWorkerDbServiceRequestOptions {
  return {
    ...options,
    priority: options?.priority ?? backgroundDbServiceRequestPriorityForOperation(operation)
  }
}

function backgroundDbServiceRequestPriorityForOperation(
  operation: import('../db-service/db-service-types.js').DbServiceOperation
): BackgroundWorkerDbServiceRequestOptions['priority'] {
  const accessMode = dbServiceOperationAccessMode(operation)
  return accessMode === 'write' || accessMode === 'maintenance' ? 'low' : undefined
}

function isBackgroundWorkerDbServiceRequestOptions(value: unknown): value is BackgroundWorkerDbServiceRequestOptions {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  return (record.timeoutMs === undefined || typeof record.timeoutMs === 'number')
    && (record.priority === undefined || record.priority === 'high' || record.priority === 'normal' || record.priority === 'low')
}

function finishBackgroundDbServiceRequest(requestId: string, response: { ok: true; result: unknown } | { ok: false; errorMessage: string } | undefined): void {
  const pending = pendingBackgroundDbServiceRequests.get(requestId)
  if (!pending) {
    return
  }
  clearTimeout(pending.timeout)
  pendingBackgroundDbServiceRequests.delete(requestId)
  if (!response) {
    pending.resolve(undefined)
    return
  }
  if (response.ok) {
    pending.resolve(response.result)
    return
  }
  pending.reject(new Error(response.errorMessage))
}

function sendToParentOrServer(message: BackgroundWorkerMessage, onFailure?: (error: unknown) => void): void {
  if (typeof process.send !== 'function') {
    onFailure?.(new Error('process.send unavailable'))
    return
  }
  try {
    process.send(message, (error) => {
      if (error) {
        onFailure?.(error)
      }
    })
  } catch (error) {
    onFailure?.(error)
  }
}
