import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { ActiveClientIpPolicy } from '../../storage/client-ip-stats.repository.js'
import type { AuditLogInput, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { PublicApiLogInput } from '../../storage/public-api-logs.repository.js'
import type { GatewayQuotaSnapshot } from '../gateway/quota/quota-snapshot-cache.service.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type { RuntimeLogLineIndexOptions } from '../runtime-logs/runtime-log-index-queue.service.js'
import type { AccountRuntimeAvailabilityClearTarget } from '../db-service/db-service-types.js'
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
  nonMetricsProcessEventLoopWorkerRoles,
  workerMessageTargetRole,
  type BackgroundWorkerQueueTargetRole,
  type BackgroundWorkerSnapshotRole
} from './background-ipc-worker-roles.js'
import { HeadIndexedQueue } from './ipc-head-queue.js'

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
let metricsWorkerProcess: ChildProcess | undefined
let metricsWorkerReady = false
let metricsWorkerPid: number | undefined
let ingestWorkerProcess: ChildProcess | undefined
let ingestWorkerReady = false
let ingestWorkerPid: number | undefined
let statsWorkerProcess: ChildProcess | undefined
let statsWorkerReady = false
let statsWorkerPid: number | undefined
let snapshotWorkerProcess: ChildProcess | undefined
let snapshotWorkerReady = false
let snapshotWorkerPid: number | undefined
let probeWorkerProcess: ChildProcess | undefined
let probeWorkerReady = false
let probeWorkerPid: number | undefined
let maintenanceWorkerProcess: ChildProcess | undefined
let maintenanceWorkerReady = false
let maintenanceWorkerPid: number | undefined
const usageRecordMessageQueueMaxMessages = 10_000
const usageRecordMessageQueueMaxBytes = 64 * 1024 * 1024
const regularWorkerMessageQueueMaxMessages = 5_000
const regularWorkerMessageQueueMaxBytes = 64 * 1024 * 1024
const regularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
const ingestUsageRecordMessageQueue = new HeadIndexedQueue<Extract<BackgroundWorkerMessage, { type: 'background_worker_usage_records' }>>()
const ingestRegularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
const probeWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
const maintenanceWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
let regularWorkerMessageQueueBytes = 0
let ingestUsageRecordMessageQueueBytes = 0
let ingestRegularWorkerMessageQueueBytes = 0
let probeWorkerMessageQueueBytes = 0
let maintenanceWorkerMessageQueueBytes = 0
let sendingMessage = false
let sendingWorkerMessage: BackgroundWorkerMessage | undefined
let sendingIngestMessage = false
let sendingIngestWorkerMessage: BackgroundWorkerMessage | undefined
let sendingProbeMessage = false
let sendingProbeWorkerMessage: BackgroundWorkerMessage | undefined
let sendingMaintenanceMessage = false
let sendingMaintenanceWorkerMessage: BackgroundWorkerMessage | undefined
const pendingQueueRuntime = emptyIpcQueuesRuntime()
const ingestPendingQueueRuntime = emptyIpcQueuesRuntime()
const probePendingQueueRuntime = emptyIpcQueuesRuntime()
const maintenancePendingQueueRuntime = emptyIpcQueuesRuntime()
let pendingParentIngestStatusRequests = new Map<string, PendingIngestStatusRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let pendingStatsWriteRequests = new Map<string, PendingStatsWriteRequest>()
let timedOutProcessEventLoopRequestCount = 0
let failedProcessEventLoopRequestCount = 0
let backgroundWorkerReadyHandler: (() => void) | undefined
let metricsWorkerReadyHandler: (() => void) | undefined
let ingestWorkerReadyHandler: (() => void) | undefined
let statsWorkerReadyHandler: (() => void) | undefined
let snapshotWorkerReadyHandler: (() => void) | undefined
let probeWorkerReadyHandler: (() => void) | undefined
let maintenanceWorkerReadyHandler: (() => void) | undefined

if (runtimeConfig.processRole === 'worker') {
  process.on('message', handleParentMessage)
  process.once('disconnect', () => {
    markParentIpcBroken(new Error('后台 worker 父进程 IPC 已断开'))
  })
}

export function attachBackgroundWorkerProcess(child: ChildProcess, options: { role?: BackgroundWorkerProcessRole; onReady?: () => void } = {}): void {
  const role = options.role ?? 'worker'
  if (role === 'metrics-worker') {
    attachMetricsWorkerProcess(child, options)
    return
  }
  if (role === 'ingest-worker') {
    attachIngestWorkerProcess(child, options)
    return
  }
  if (role === 'stats-worker') {
    attachStatsWorkerProcess(child, options)
    return
  }
  if (role === 'snapshot-worker') {
    attachSnapshotWorkerProcess(child, options)
    return
  }
  if (role === 'probe-worker') {
    attachProbeWorkerProcess(child, options)
    return
  }
  if (role === 'maintenance-worker') {
    attachMaintenanceWorkerProcess(child, options)
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

function attachMetricsWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  metricsWorkerProcess = child
  metricsWorkerPid = child.pid ?? undefined
  metricsWorkerReady = false
  metricsWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, 'metrics-worker', child))
  child.once('exit', () => {
    if (metricsWorkerProcess === child) {
      metricsWorkerProcess = undefined
      metricsWorkerReady = false
      metricsWorkerPid = undefined
      failMetricsPendingRequests()
    }
  })
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

function attachSnapshotWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  snapshotWorkerProcess = child
  snapshotWorkerPid = child.pid ?? undefined
  snapshotWorkerReady = false
  snapshotWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, 'snapshot-worker', child))
  child.once('exit', () => {
    if (snapshotWorkerProcess === child) {
      snapshotWorkerProcess = undefined
      snapshotWorkerReady = false
      snapshotWorkerPid = undefined
      failSnapshotPendingRequests()
    }
  })
}

function attachProbeWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  probeWorkerProcess = child
  probeWorkerPid = child.pid ?? undefined
  probeWorkerReady = false
  probeWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, 'probe-worker', child))
  child.once('exit', () => {
    if (probeWorkerProcess === child) {
      probeWorkerProcess = undefined
      probeWorkerReady = false
      probeWorkerPid = undefined
      sendingProbeMessage = false
      if (sendingProbeWorkerMessage) {
        requeueProbeWorkerMessageFirst(sendingProbeWorkerMessage)
        sendingProbeWorkerMessage = undefined
      }
      failProbePendingRequests()
    }
  })

  flushProbeWorkerMessageQueue()
}

function attachMaintenanceWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  maintenanceWorkerProcess = child
  maintenanceWorkerPid = child.pid ?? undefined
  maintenanceWorkerReady = false
  maintenanceWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', (message: unknown) => handleWorkerMessage(message, 'maintenance-worker', child))
  child.once('exit', () => {
    if (maintenanceWorkerProcess === child) {
      maintenanceWorkerProcess = undefined
      maintenanceWorkerReady = false
      maintenanceWorkerPid = undefined
      sendingMaintenanceMessage = false
      if (sendingMaintenanceWorkerMessage) {
        requeueMaintenanceWorkerMessageFirst(sendingMaintenanceWorkerMessage)
        sendingMaintenanceWorkerMessage = undefined
      }
      failMaintenancePendingRequests()
    }
  })

  flushMaintenanceWorkerMessageQueue()
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

export function sendRuntimeLogLineToWorker(line: string, options: RuntimeLogLineIndexOptions = {}): boolean {
  return sendBackgroundWorkerMessageToWorker({
    type: 'background_worker_runtime_log_line',
    line,
    sourceKey: options.sourceKey,
    logFile: options.logFile,
    logOffset: options.logOffset,
    lineNumber: options.lineNumber
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

export async function requestMetricsWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestDirectWorkerSnapshot('metrics-worker', {
    child: metricsWorkerProcess,
    markIpcBroken: (error, child) => markMetricsWorkerIpcBroken(error, child),
    ready: metricsWorkerReady,
    timeoutMs
  })
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

export async function requestSnapshotWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestSnapshotRoleWorkerSnapshot('snapshot-worker', {
    child: snapshotWorkerProcess,
    markIpcBroken: (error, child) => markRoleWorkerIpcBroken('snapshot-worker', error, child),
    ready: snapshotWorkerReady,
    timeoutMs
  })
}

export async function requestProbeWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestSnapshotRoleWorkerSnapshot('probe-worker', {
    child: probeWorkerProcess,
    markIpcBroken: (error, child) => markRoleWorkerIpcBroken('probe-worker', error, child),
    ready: probeWorkerReady,
    timeoutMs
  })
}

export async function requestMaintenanceWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  return await requestSnapshotRoleWorkerSnapshot('maintenance-worker', {
    child: maintenanceWorkerProcess,
    markIpcBroken: (error, child) => markRoleWorkerIpcBroken('maintenance-worker', error, child),
    ready: maintenanceWorkerReady,
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
  role: Exclude<BackgroundWorkerProcessRole, 'metrics-worker'>,
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
  const metricsSnapshotStats = snapshotRequestStats('metrics-worker')
  const ingestSnapshotStats = snapshotRequestStats('ingest-worker')
  const statsSnapshotStats = snapshotRequestStats('stats-worker')
  const snapshotWorkerSnapshotStats = snapshotRequestStats('snapshot-worker')
  const probeSnapshotStats = snapshotRequestStats('probe-worker')
  const maintenanceSnapshotStats = snapshotRequestStats('maintenance-worker')
  return buildBackgroundWorkerStateSnapshot({
    pid: workerPid,
    ready: workerReady,
    lastSnapshot: workerSnapshotStats.lastSnapshot,
    pendingMessageCounts: {
      regularWorker: regularWorkerMessageQueue.length,
      ingestUsageRecord: ingestUsageRecordMessageQueue.length,
      ingestRegularWorker: ingestRegularWorkerMessageQueue.length,
      probeWorker: probeWorkerMessageQueue.length,
      maintenanceWorker: maintenanceWorkerMessageQueue.length
    },
    pendingMessageBytes: {
      regularWorker: regularWorkerMessageQueueBytes,
      ingestUsageRecord: ingestUsageRecordMessageQueueBytes,
      ingestRegularWorker: ingestRegularWorkerMessageQueueBytes,
      probeWorker: probeWorkerMessageQueueBytes,
      maintenanceWorker: maintenanceWorkerMessageQueueBytes
    },
    pendingQueues: buildAggregatePendingQueuesRuntime(),
    pendingSnapshotRequestCount: workerSnapshotStats.pendingSnapshotRequestCount,
    timedOutSnapshotRequestCount: workerSnapshotStats.timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: workerSnapshotStats.rejectedSnapshotRequestCount,
    pendingProcessEventLoopRequestCount: pendingProcessEventLoopRequests.size,
    timedOutProcessEventLoopRequestCount,
    failedProcessEventLoopRequestCount,
    roles: {
      metricsWorker: {
        pid: metricsWorkerPid,
        ready: metricsWorkerReady,
        lastSnapshot: metricsSnapshotStats.lastSnapshot,
        pendingSnapshotRequestCount: metricsSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: metricsSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: metricsSnapshotStats.rejectedSnapshotRequestCount
      },
      ingestWorker: {
        pid: ingestWorkerPid,
        ready: ingestWorkerReady,
        lastSnapshot: ingestSnapshotStats.lastSnapshot,
        pendingMessageCount: ingestUsageRecordMessageQueue.length + ingestRegularWorkerMessageQueue.length,
        pendingMessageBytes: ingestUsageRecordMessageQueueBytes + ingestRegularWorkerMessageQueueBytes,
        pendingQueues: buildIngestPendingQueuesRuntime(),
        pendingSnapshotRequestCount: ingestSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: ingestSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: ingestSnapshotStats.rejectedSnapshotRequestCount
      },
      statsWorker: {
        pid: statsWorkerPid,
        ready: statsWorkerReady,
        lastSnapshot: statsSnapshotStats.lastSnapshot,
        pendingSnapshotRequestCount: statsSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: statsSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: statsSnapshotStats.rejectedSnapshotRequestCount
      },
      snapshotWorker: {
        pid: snapshotWorkerPid,
        ready: snapshotWorkerReady,
        lastSnapshot: snapshotWorkerSnapshotStats.lastSnapshot,
        pendingSnapshotRequestCount: snapshotWorkerSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: snapshotWorkerSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: snapshotWorkerSnapshotStats.rejectedSnapshotRequestCount
      },
      probeWorker: {
        pid: probeWorkerPid,
        ready: probeWorkerReady,
        lastSnapshot: probeSnapshotStats.lastSnapshot,
        pendingMessageCount: probeWorkerMessageQueue.length,
        pendingMessageBytes: probeWorkerMessageQueueBytes,
        pendingQueues: buildProbePendingQueuesRuntime(),
        pendingSnapshotRequestCount: probeSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: probeSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: probeSnapshotStats.rejectedSnapshotRequestCount
      },
      maintenanceWorker: {
        pid: maintenanceWorkerPid,
        ready: maintenanceWorkerReady,
        lastSnapshot: maintenanceSnapshotStats.lastSnapshot,
        pendingMessageCount: maintenanceWorkerMessageQueue.length,
        pendingMessageBytes: maintenanceWorkerMessageQueueBytes,
        pendingQueues: buildMaintenancePendingQueuesRuntime(),
        pendingSnapshotRequestCount: maintenanceSnapshotStats.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: maintenanceSnapshotStats.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: maintenanceSnapshotStats.rejectedSnapshotRequestCount
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
    case 'background_worker_runtime_log_line':
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
        void respondToDbServiceRequest(record.requestId, record.operation, child)
      }
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
        void replaceServerClientIpPolicySnapshot(record.policies)
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
  if (record.type === 'background_worker_stats_write_response' && typeof record.requestId === 'string') {
    finishStatsWriteRequest(record.requestId, record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : 'stats-writer 请求失败' })
  }
}

interface PendingStatsWriteRequest {
  resolve: (result: unknown) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
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
    const requestId = randomUUID()
    return await new Promise<import('./background-stats-writer.js').BackgroundStatsWriteOperationResult<T> | undefined>((resolve, reject) => {
      const timeout = setTimeout(() => {
        const pending = pendingStatsWriteRequests.get(requestId)
        if (!pending) {
          return
        }
        pendingStatsWriteRequests.delete(requestId)
        pending.reject(new Error('后台 stats-writer 请求超时'))
      }, timeoutMs)
      pendingStatsWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout })
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
  const requestId = randomUUID()
  return await new Promise<import('./background-stats-writer.js').BackgroundStatsWriteOperationResult<T> | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      finishStatsWriteRequest(requestId, undefined)
    }, timeoutMs)
    pendingStatsWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject: () => resolve(undefined), timeout })
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

function queueWorkerMessage(inputMessage: BackgroundWorkerMessage): boolean {
  const message = coalesceWorkerMessage(inputMessage)
  if (!message) {
    flushWorkerMessageQueue()
    return true
  }
  const targetRole = workerMessageTargetRole(message)
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  if (!canQueueWorkerMessage(targetRole, message, messageBytes)) {
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
  } else if (targetRole === 'probe-worker') {
    probeWorkerMessageQueue.push(message)
    probeWorkerMessageQueueBytes += messageBytes
  } else if (targetRole === 'maintenance-worker') {
    maintenanceWorkerMessageQueue.push(message)
    maintenanceWorkerMessageQueueBytes += messageBytes
  } else {
    regularWorkerMessageQueue.push(message)
    regularWorkerMessageQueueBytes += messageBytes
  }
  addPendingQueueRuntimeMessage(targetRole, queueKey, messageBytes)

  flushTargetWorkerMessageQueue(targetRole)
  return true
}

function coalesceWorkerMessage(message: BackgroundWorkerMessage): BackgroundWorkerMessage | undefined {
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
  const queueIndex = maintenanceWorkerMessageQueue.findIndex((queued) => (
    queued.type === 'background_worker_record_maintenance'
    && queued.items.some((item) => recordMaintenanceJobCoalescingKey(item) === key)
  ))
  if (queueIndex < 0) {
    return false
  }
  const current = maintenanceWorkerMessageQueue.at(queueIndex)
  if (!current || current.type !== 'background_worker_record_maintenance') {
    return false
  }
  const currentBytes = estimateWorkerMessageBytes(current)
  const nextItems = compactRecordMaintenanceJobsForCoalescing(current.items.map((item) => (
    recordMaintenanceJobCoalescingKey(item) === key ? job : item
  )))
  const nextMessage: BackgroundWorkerMessage = { ...current, items: nextItems }
  const nextBytes = estimateWorkerMessageBytes(nextMessage)
  const nextQueueBytes = maintenanceWorkerMessageQueueBytes - currentBytes + nextBytes
  if (nextBytes > regularWorkerMessageMaxBytes || nextQueueBytes > regularWorkerMessageQueueMaxBytes) {
    return false
  }
  maintenanceWorkerMessageQueue.set(queueIndex, nextMessage)
  maintenanceWorkerMessageQueueBytes = Math.max(0, maintenanceWorkerMessageQueueBytes - currentBytes + nextBytes)
  const runtime = maintenancePendingQueueRuntime.recordMaintenance
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
  messageBytes: number
): boolean {
  if (message.type === 'background_worker_usage_records') {
    return targetRole === 'ingest-worker'
      && ingestUsageRecordMessageQueue.length < usageRecordMessageQueueMaxMessages
      && messageBytes <= usageRecordWorkerMessageMaxBytes
      && ingestUsageRecordMessageQueueBytes + messageBytes <= usageRecordMessageQueueMaxBytes
  }
  const regularQueueLength = targetRole === 'ingest-worker'
    ? ingestRegularWorkerMessageQueue.length
    : targetRole === 'probe-worker'
      ? probeWorkerMessageQueue.length
      : targetRole === 'maintenance-worker'
        ? maintenanceWorkerMessageQueue.length
    : regularWorkerMessageQueue.length
  const regularQueueBytes = targetRole === 'ingest-worker'
    ? ingestRegularWorkerMessageQueueBytes
    : targetRole === 'probe-worker'
      ? probeWorkerMessageQueueBytes
      : targetRole === 'maintenance-worker'
        ? maintenanceWorkerMessageQueueBytes
    : regularWorkerMessageQueueBytes
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

function flushTargetWorkerMessageQueue(role: BackgroundWorkerQueueTargetRole): void {
  if (role === 'ingest-worker') {
    flushIngestWorkerMessageQueue()
    return
  }
  if (role === 'probe-worker') {
    flushProbeWorkerMessageQueue()
    return
  }
  if (role === 'maintenance-worker') {
    flushMaintenanceWorkerMessageQueue()
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
    process.stderr.write(`[background-worker] 向 worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
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
    process.stderr.write(`[background-worker] 向 ingest-worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function flushProbeWorkerMessageQueue(): void {
  const child = probeWorkerProcess
  if (sendingProbeMessage || !child || !probeWorkerReady) {
    return
  }

  const message = shiftProbeWorkerMessage()
  if (!message) {
    return
  }

  sendingProbeMessage = true
  sendingProbeWorkerMessage = message
  try {
    child.send(message, (error) => {
      const stillSendingThisMessage = sendingProbeWorkerMessage === message
      if (stillSendingThisMessage) {
        sendingProbeMessage = false
        sendingProbeWorkerMessage = undefined
      }
      if (error) {
        if (stillSendingThisMessage) {
          requeueProbeWorkerMessageFirst(message)
        }
        markRoleWorkerIpcBroken('probe-worker', error, child)
        return
      }
      if (stillSendingThisMessage) {
        flushProbeWorkerMessageQueue()
      }
    })
  } catch (error) {
    sendingProbeMessage = false
    sendingProbeWorkerMessage = undefined
    requeueProbeWorkerMessageFirst(message)
    markRoleWorkerIpcBroken('probe-worker', error, child)
    process.stderr.write(`[background-worker] 向 probe-worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function flushMaintenanceWorkerMessageQueue(): void {
  const child = maintenanceWorkerProcess
  if (sendingMaintenanceMessage || !child || !maintenanceWorkerReady) {
    return
  }

  const message = shiftMaintenanceWorkerMessage()
  if (!message) {
    return
  }

  sendingMaintenanceMessage = true
  sendingMaintenanceWorkerMessage = message
  try {
    child.send(message, (error) => {
      const stillSendingThisMessage = sendingMaintenanceWorkerMessage === message
      if (stillSendingThisMessage) {
        sendingMaintenanceMessage = false
        sendingMaintenanceWorkerMessage = undefined
      }
      if (error) {
        if (stillSendingThisMessage) {
          requeueMaintenanceWorkerMessageFirst(message)
        }
        markRoleWorkerIpcBroken('maintenance-worker', error, child)
        return
      }
      if (stillSendingThisMessage) {
        flushMaintenanceWorkerMessageQueue()
      }
    })
  } catch (error) {
    sendingMaintenanceMessage = false
    sendingMaintenanceWorkerMessage = undefined
    requeueMaintenanceWorkerMessageFirst(message)
    markRoleWorkerIpcBroken('maintenance-worker', error, child)
    process.stderr.write(`[background-worker] 向 maintenance-worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
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
  const usageMessage = ingestUsageRecordMessageQueue.shift()
  const message = usageMessage ?? ingestRegularWorkerMessageQueue.shift()
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

function shiftProbeWorkerMessage(): BackgroundWorkerMessage | undefined {
  const message = probeWorkerMessageQueue.shift()
  if (message) {
    const queueKey = ipcQueueKeyForMessage(message)
    const messageBytes = estimateWorkerMessageBytes(message)
    probeWorkerMessageQueueBytes = Math.max(0, probeWorkerMessageQueueBytes - messageBytes)
    removePendingQueueRuntimeMessage('probe-worker', queueKey, messageBytes)
  }
  return message
}

function shiftMaintenanceWorkerMessage(): BackgroundWorkerMessage | undefined {
  const message = maintenanceWorkerMessageQueue.shift()
  if (message) {
    const queueKey = ipcQueueKeyForMessage(message)
    const messageBytes = estimateWorkerMessageBytes(message)
    maintenanceWorkerMessageQueueBytes = Math.max(0, maintenanceWorkerMessageQueueBytes - messageBytes)
    removePendingQueueRuntimeMessage('maintenance-worker', queueKey, messageBytes)
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

function requeueProbeWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  probeWorkerMessageQueue.unshift(message)
  probeWorkerMessageQueueBytes += messageBytes
  addPendingQueueRuntimeMessage('probe-worker', queueKey, messageBytes)
}

function requeueMaintenanceWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  maintenanceWorkerMessageQueue.unshift(message)
  maintenanceWorkerMessageQueueBytes += messageBytes
  addPendingQueueRuntimeMessage('maintenance-worker', queueKey, messageBytes)
}

function buildIngestPendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return clonePendingQueueRuntime(ingestPendingQueueRuntime)
}

function buildProbePendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return clonePendingQueueRuntime(probePendingQueueRuntime)
}

function buildMaintenancePendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return clonePendingQueueRuntime(maintenancePendingQueueRuntime)
}

function buildAggregatePendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return mergePendingQueuesRuntime(
    mergePendingQueuesRuntime(pendingQueueRuntime, ingestPendingQueueRuntime),
    mergePendingQueuesRuntime(probePendingQueueRuntime, maintenancePendingQueueRuntime)
  )
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
  if (targetRole === 'probe-worker') return probePendingQueueRuntime
  if (targetRole === 'maintenance-worker') return maintenancePendingQueueRuntime
  return pendingQueueRuntime
}

function processForRole(role: BackgroundWorkerProcessRole): ChildProcess | undefined {
  switch (role) {
    case 'metrics-worker':
      return metricsWorkerProcess
    case 'ingest-worker':
      return ingestWorkerProcess
    case 'stats-worker':
      return statsWorkerProcess
    case 'snapshot-worker':
      return snapshotWorkerProcess
    case 'probe-worker':
      return probeWorkerProcess
    case 'maintenance-worker':
      return maintenanceWorkerProcess
    default:
      return workerProcess
  }
}

function readyForRole(role: BackgroundWorkerProcessRole): boolean {
  switch (role) {
    case 'metrics-worker':
      return metricsWorkerReady
    case 'ingest-worker':
      return ingestWorkerReady
    case 'stats-worker':
      return statsWorkerReady
    case 'snapshot-worker':
      return snapshotWorkerReady
    case 'probe-worker':
      return probeWorkerReady
    case 'maintenance-worker':
      return maintenanceWorkerReady
    default:
      return workerReady
  }
}

function setReadyForRole(role: BackgroundWorkerProcessRole, ready: boolean): void {
  switch (role) {
    case 'stats-worker':
      statsWorkerReady = ready
      break
    case 'snapshot-worker':
      snapshotWorkerReady = ready
      break
    case 'probe-worker':
      probeWorkerReady = ready
      break
    case 'maintenance-worker':
      maintenanceWorkerReady = ready
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
    case 'snapshot-worker':
      snapshotWorkerPid = pid ?? snapshotWorkerPid
      break
    case 'probe-worker':
      probeWorkerPid = pid ?? probeWorkerPid
      break
    case 'maintenance-worker':
      maintenanceWorkerPid = pid ?? maintenanceWorkerPid
      break
    default:
      break
  }
}

function markWorkerReady(role: BackgroundWorkerProcessRole, record: Partial<BackgroundWorkerMessage> & Record<string, unknown>): void {
  if (role === 'metrics-worker') {
    metricsWorkerReady = true
    metricsWorkerPid = workerPidFromReadyRecord(record, metricsWorkerPid)
    metricsWorkerReadyHandler?.()
    return
  }
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
  if (role === 'snapshot-worker') {
    snapshotWorkerReady = true
    snapshotWorkerPid = workerPidFromReadyRecord(record, snapshotWorkerPid)
    snapshotWorkerReadyHandler?.()
    return
  }
  if (role === 'probe-worker') {
    probeWorkerReady = true
    probeWorkerPid = workerPidFromReadyRecord(record, probeWorkerPid)
    probeWorkerReadyHandler?.()
    flushProbeWorkerMessageQueue()
    return
  }
  if (role === 'maintenance-worker') {
    maintenanceWorkerReady = true
    maintenanceWorkerPid = workerPidFromReadyRecord(record, maintenanceWorkerPid)
    maintenanceWorkerReadyHandler?.()
    flushMaintenanceWorkerMessageQueue()
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

function failMetricsPendingRequests(): void {
  failWorkerSnapshotPendingRequests('metrics-worker')
}

function failIngestPendingRequests(): void {
  failWorkerSnapshotPendingRequests('ingest-worker')
}

function failStatsPendingRequests(): void {
  failWorkerSnapshotPendingRequests('stats-worker')
}

function failSnapshotPendingRequests(): void {
  failWorkerSnapshotPendingRequests('snapshot-worker')
}

function failProbePendingRequests(): void {
  failWorkerSnapshotPendingRequests('probe-worker')
}

function failMaintenancePendingRequests(): void {
  failWorkerSnapshotPendingRequests('maintenance-worker')
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

function markMetricsWorkerIpcBroken(error: unknown, child = metricsWorkerProcess): void {
  const isCurrentChild = child === undefined || metricsWorkerProcess === child
  if (isCurrentChild) {
    metricsWorkerReady = false
    metricsWorkerPid = workerPidFromBrokenChild(child, metricsWorkerPid)
    failMetricsPendingRequests()
  }
  terminateBrokenWorkerIpc('metrics-worker', error, child)
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
    metricsWorkerProcess,
    ingestWorkerProcess,
    statsWorkerProcess,
    snapshotWorkerProcess,
    probeWorkerProcess,
    maintenanceWorkerProcess
  })
}

function markIpcBrokenForChild(role: BackgroundWorkerProcessRole, error: unknown, child: ChildProcess | undefined): void {
  if (role === 'metrics-worker') {
    markMetricsWorkerIpcBroken(error, child)
    return
  }
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
  gatewaySideEffects.clearGatewayAccountRuntimeAvailability(target)
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
  const status = await buildIngestWorkerDrainStatus(1000).catch(() => undefined)
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
  policyCache.replaceClientIpPolicyCacheLocal(policies)
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
  for (const role of nonMetricsProcessEventLoopWorkerRoles()) {
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

async function respondToDbServiceRequest(requestId: string, operation: import('../db-service/db-service-types.js').DbServiceOperation, targetChild: ChildProcess | undefined): Promise<void> {
  const child = targetChild
  if (!child || !child.connected) {
    return
  }
  try {
    const { requestDbService } = await import('../db-service/db-service-ipc.js')
    const result = await requestDbService(operation)
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
}

const pendingBackgroundDbServiceRequests = new Map<string, PendingDbServiceRequest>()

export async function requestBackgroundWorkerDbService<T extends import('../db-service/db-service-types.js').DbServiceOperation>(
  operation: T,
  timeoutMs = 5000
): Promise<import('../db-service/db-service-types.js').DbServiceOperationResult<T> | undefined> {
  if (runtimeConfig.processRole === 'server') {
    const { requestDbService } = await import('../db-service/db-service-ipc.js')
    return await requestDbService(operation, { timeoutMs })
  }
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<import('../db-service/db-service-types.js').DbServiceOperationResult<T> | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      const pending = pendingBackgroundDbServiceRequests.get(requestId)
      if (!pending) {
        return
      }
      pendingBackgroundDbServiceRequests.delete(requestId)
      pending.reject(new Error('后台 DB service 请求超时'))
    }, timeoutMs)
    pendingBackgroundDbServiceRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout })
    sendToParentOrServer({
      type: 'background_worker_db_service_request',
      requestId,
      operation
    }, (error) => {
      finishBackgroundDbServiceRequest(requestId, undefined)
      markParentIpcBroken(error)
    })
  })
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
