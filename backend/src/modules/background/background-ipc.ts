import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import type { AuditLogInput, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { GatewayQuotaSnapshot } from '../gateway/quota/quota-snapshot-cache.service.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type { RuntimeLogLineIndexOptions } from '../runtime-logs/runtime-log-index-queue.service.js'
import type { AccountRuntimeAvailabilityClearTarget } from '../db-service/db-service-types.js'
import type { WorkerScheduledJobRuntimeSnapshot } from './worker-scheduler.js'
import { auditWorkerMessageMaxBytes, estimateAuditLogBytes, trimAuditLogsForWorkerIpc } from './background-ipc-audit-trim.js'
import { HeadIndexedQueue } from './ipc-head-queue.js'

export type BackgroundWorkerProcessRole = 'worker' | 'metrics-worker' | 'ingest-worker'

export interface BackgroundWorkerQueueRuntime {
  queueLength: number
  queueBytes?: number
  flushLastSuccessAt?: string
  flushLastError?: string
  droppedCount?: number
  droppedSuccessCount?: number
  droppedFailureCount?: number
  droppedOverflowCount?: number
  droppedOversizeCount?: number
  retainedOverflowWarningCount?: number
  flushFailureCount?: number
  successHotRetentionHours?: number
  successRetentionDays?: number
  failureRetentionDays?: number
  errorGroupRetentionDays?: number
}

export interface BackgroundWorkerRuntimeLogQueueRuntime extends BackgroundWorkerQueueRuntime {
  retentionDays: number
}

export interface BackgroundWorkerRetryQueueRuntime {
  name: string
  pendingCount: number
  runningCount: number
  nextRunAt?: string
}

export interface BackgroundWorkerIpcQueueRuntime extends BackgroundWorkerQueueRuntime {
  rejectedCount?: number
}

export interface BackgroundWorkerIpcQueuesRuntime {
  usageRecords: BackgroundWorkerIpcQueueRuntime
  auditLogs: BackgroundWorkerIpcQueueRuntime
  operationLogs: BackgroundWorkerIpcQueueRuntime
  recordMaintenance: BackgroundWorkerIpcQueueRuntime
  runtimeLogLines: BackgroundWorkerIpcQueueRuntime
  statusRequests: BackgroundWorkerIpcQueueRuntime
  processEventLoopRequests: BackgroundWorkerIpcQueueRuntime
  processEventLoopResponses: BackgroundWorkerIpcQueueRuntime
  gatewayRuntimeCacheInvalidations: BackgroundWorkerIpcQueueRuntime
  other: BackgroundWorkerIpcQueueRuntime
}

export interface BackgroundWorkerRuntimeSnapshot {
  pid: number
  ready: boolean
  processRole: 'worker'
  workerRole: BackgroundWorkerProcessRole
  jobs: WorkerScheduledJobRuntimeSnapshot[]
  usageRecordQueue: BackgroundWorkerQueueRuntime
  operationLogQueue: BackgroundWorkerQueueRuntime
  recordMaintenanceQueue: BackgroundWorkerQueueRuntime
  auditLogQueue: BackgroundWorkerQueueRuntime
  runtimeLogIndexQueue: BackgroundWorkerRuntimeLogQueueRuntime
  cooldownAccountRetestQueue?: BackgroundWorkerRetryQueueRuntime
  accountQualityFailurePrecheckQueue?: BackgroundWorkerRetryQueueRuntime
  manualAccountTestQueue?: BackgroundWorkerRetryQueueRuntime
}

export interface BackgroundWorkerRoleState {
  pid?: number
  ready: boolean
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount?: number
  pendingMessageBytes?: number
  pendingQueues?: BackgroundWorkerIpcQueuesRuntime
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
}

export interface BackgroundWorkerIngestDrainStatus {
  pid?: number
  ready: boolean
  snapshot?: BackgroundWorkerRuntimeSnapshot
  pendingQueues: BackgroundWorkerIpcQueuesRuntime
}

type BackgroundWorkerMessage =
  | { type: 'background_worker_ready'; pid: number; workerRole?: BackgroundWorkerProcessRole }
  | { type: 'background_worker_usage_records'; items: UsageRecordInput[] }
  | { type: 'background_worker_audit_logs'; items: AuditLogInput[] }
  | { type: 'background_worker_operation_logs'; items: OperationLogInput[] }
  | { type: 'background_worker_record_maintenance'; items: RecordMaintenanceJob[] }
  | { type: 'background_worker_account_test_tasks'; taskIds: string[] }
  | { type: 'background_worker_account_test_cancel'; taskId: string }
  | ({ type: 'background_worker_runtime_log_line'; line: string } & RuntimeLogLineIndexOptions)
  | { type: 'background_worker_status_request'; requestId: string }
  | { type: 'background_worker_status_response'; requestId: string; snapshot: BackgroundWorkerRuntimeSnapshot }
  | { type: 'background_worker_ingest_status_request'; requestId: string }
  | { type: 'background_worker_ingest_status_response'; requestId: string; status?: BackgroundWorkerIngestDrainStatus }
  | { type: 'background_worker_process_event_loop_request'; requestId: string }
  | { type: 'background_worker_process_event_loop_response'; requestId: string; samples: ProcessEventLoopSample[] }
  | { type: 'server_account_runtime_clear'; target: AccountRuntimeAvailabilityClearTarget }
  | { type: 'gateway_runtime_cache_invalidate' }
  | { type: 'gateway_quota_snapshot_update'; snapshot: GatewayQuotaSnapshot }

interface PendingRequest {
  resolve: (snapshot: BackgroundWorkerRuntimeSnapshot | undefined) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
}

interface PendingIngestStatusRequest {
  resolve: (status: BackgroundWorkerIngestDrainStatus | undefined) => void
  timeout: NodeJS.Timeout
}

interface PendingProcessEventLoopRequest {
  resolve: (samples: ProcessEventLoopSample[] | undefined) => void
  timeout: NodeJS.Timeout
}

interface BackgroundWorkerState {
  pid?: number
  ready: boolean
  metricsWorker?: BackgroundWorkerRoleState
  ingestWorker?: BackgroundWorkerRoleState
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount: number
  pendingMessageBytes: number
  pendingQueues: BackgroundWorkerIpcQueuesRuntime
  pendingSnapshotRequestCount: number
  timedOutSnapshotRequestCount: number
  rejectedSnapshotRequestCount: number
  pendingProcessEventLoopRequestCount: number
  timedOutProcessEventLoopRequestCount: number
  failedProcessEventLoopRequestCount: number
}

let workerProcess: ChildProcess | undefined
let workerReady = false
let workerPid: number | undefined
let metricsWorkerProcess: ChildProcess | undefined
let metricsWorkerReady = false
let metricsWorkerPid: number | undefined
let ingestWorkerProcess: ChildProcess | undefined
let ingestWorkerReady = false
let ingestWorkerPid: number | undefined
const usageRecordMessageQueueMaxMessages = 10_000
const usageRecordMessageQueueMaxBytes = 64 * 1024 * 1024
const regularWorkerMessageQueueMaxMessages = 5_000
const regularWorkerMessageQueueMaxBytes = 64 * 1024 * 1024
const usageRecordWorkerMessageMaxBytes = 8 * 1024 * 1024
const regularWorkerMessageMaxBytes = 8 * 1024 * 1024
const workerMessageEstimateMaxBytes = Math.max(usageRecordWorkerMessageMaxBytes, regularWorkerMessageMaxBytes) + 1
const workerMessageEstimateMaxNodes = 20_000
const regularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
const ingestUsageRecordMessageQueue = new HeadIndexedQueue<Extract<BackgroundWorkerMessage, { type: 'background_worker_usage_records' }>>()
const ingestRegularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
let regularWorkerMessageQueueBytes = 0
let ingestUsageRecordMessageQueueBytes = 0
let ingestRegularWorkerMessageQueueBytes = 0
let sendingMessage = false
let sendingWorkerMessage: BackgroundWorkerMessage | undefined
let sendingIngestMessage = false
let sendingIngestWorkerMessage: BackgroundWorkerMessage | undefined
const pendingQueueRuntime = emptyIpcQueuesRuntime()
const ingestPendingQueueRuntime = emptyIpcQueuesRuntime()
const workerMessageBytesCache = new WeakMap<object, number>()
let pendingRequests = new Map<string, PendingRequest>()
let metricsPendingRequests = new Map<string, PendingRequest>()
let ingestPendingRequests = new Map<string, PendingRequest>()
let pendingParentIngestStatusRequests = new Map<string, PendingIngestStatusRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let timedOutSnapshotRequestCount = 0
let rejectedSnapshotRequestCount = 0
let metricsTimedOutSnapshotRequestCount = 0
let metricsRejectedSnapshotRequestCount = 0
let ingestTimedOutSnapshotRequestCount = 0
let ingestRejectedSnapshotRequestCount = 0
let timedOutProcessEventLoopRequestCount = 0
let failedProcessEventLoopRequestCount = 0
let lastSnapshot: BackgroundWorkerRuntimeSnapshot | undefined
let metricsLastSnapshot: BackgroundWorkerRuntimeSnapshot | undefined
let ingestLastSnapshot: BackgroundWorkerRuntimeSnapshot | undefined
let backgroundWorkerReadyHandler: (() => void) | undefined
let metricsWorkerReadyHandler: (() => void) | undefined
let ingestWorkerReadyHandler: (() => void) | undefined

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
  if (runtimeConfig.processRole === 'worker') {
    return lastSnapshot
  }

  if (!workerProcess) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<BackgroundWorkerRuntimeSnapshot | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      pendingRequests.delete(requestId)
      timedOutSnapshotRequestCount += 1
      resolve(undefined)
    }, timeoutMs)
    pendingRequests.set(requestId, { resolve, reject, timeout })
    const queued = queueWorkerMessage({
      type: 'background_worker_status_request',
      requestId
    })
    if (!queued) {
      rejectedSnapshotRequestCount += 1
      finishPendingRequest(requestId, undefined)
    }
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
  if (runtimeConfig.processRole === 'worker') {
    return runtimeConfig.workerRole === 'metrics-worker' ? metricsLastSnapshot : undefined
  }

  const child = metricsWorkerProcess
  if (!child || !child.connected || !metricsWorkerReady) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<BackgroundWorkerRuntimeSnapshot | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      metricsPendingRequests.delete(requestId)
      metricsTimedOutSnapshotRequestCount += 1
      resolve(undefined)
    }, timeoutMs)
    metricsPendingRequests.set(requestId, { resolve, reject, timeout })
    try {
      child.send({
        type: 'background_worker_status_request',
        requestId
      } satisfies BackgroundWorkerMessage, (error) => {
        if (error) {
          metricsRejectedSnapshotRequestCount += 1
          finishMetricsPendingRequest(requestId, undefined)
          markMetricsWorkerIpcBroken(error, child)
        }
      })
    } catch (error) {
      metricsRejectedSnapshotRequestCount += 1
      finishMetricsPendingRequest(requestId, undefined)
      markMetricsWorkerIpcBroken(error, child)
    }
  })
}

export async function requestIngestWorkerSnapshot(timeoutMs = 5000): Promise<BackgroundWorkerRuntimeSnapshot | undefined> {
  if (runtimeConfig.processRole === 'worker') {
    return runtimeConfig.workerRole === 'ingest-worker' ? ingestLastSnapshot : undefined
  }

  const child = ingestWorkerProcess
  if (!child || !child.connected || !ingestWorkerReady) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<BackgroundWorkerRuntimeSnapshot | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      ingestPendingRequests.delete(requestId)
      ingestTimedOutSnapshotRequestCount += 1
      resolve(undefined)
    }, timeoutMs)
    ingestPendingRequests.set(requestId, { resolve, reject, timeout })
    try {
      child.send({
        type: 'background_worker_status_request',
        requestId
      } satisfies BackgroundWorkerMessage, (error) => {
        if (error) {
          ingestRejectedSnapshotRequestCount += 1
          finishIngestPendingRequest(requestId, undefined)
          markIngestWorkerIpcBroken(error, child)
        }
      })
    } catch (error) {
      ingestRejectedSnapshotRequestCount += 1
      finishIngestPendingRequest(requestId, undefined)
      markIngestWorkerIpcBroken(error, child)
    }
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
      pendingParentIngestStatusRequests.delete(requestId)
      resolve(undefined)
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
      const pending = pendingProcessEventLoopRequests.get(requestId)
      if (!pending) {
        return
      }
      timedOutProcessEventLoopRequestCount += 1
      pendingProcessEventLoopRequests.delete(requestId)
      pending.resolve(undefined)
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
  const child = workerProcess
  if (runtimeConfig.processRole !== 'server' || !child || !child.connected || !workerReady) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<ProcessEventLoopSample[] | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      const pending = pendingProcessEventLoopRequests.get(requestId)
      if (!pending) {
        return
      }
      timedOutProcessEventLoopRequestCount += 1
      pendingProcessEventLoopRequests.delete(requestId)
      pending.resolve(undefined)
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
          markWorkerIpcBroken(error, child)
        }
      })
    } catch (error) {
      failedProcessEventLoopRequestCount += 1
      finishProcessEventLoopRequest(requestId, undefined)
      markWorkerIpcBroken(error, child)
    }
  })
}

async function requestIngestWorkerProcessEventLoopSamples(timeoutMs = 800): Promise<ProcessEventLoopSample[] | undefined> {
  const child = ingestWorkerProcess
  if (runtimeConfig.processRole !== 'server' || !child || !child.connected || !ingestWorkerReady) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<ProcessEventLoopSample[] | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      const pending = pendingProcessEventLoopRequests.get(requestId)
      if (!pending) {
        return
      }
      timedOutProcessEventLoopRequestCount += 1
      pendingProcessEventLoopRequests.delete(requestId)
      pending.resolve(undefined)
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
          markIngestWorkerIpcBroken(error, child)
        }
      })
    } catch (error) {
      failedProcessEventLoopRequestCount += 1
      finishProcessEventLoopRequest(requestId, undefined)
      markIngestWorkerIpcBroken(error, child)
    }
  })
}

export function getBackgroundWorkerState(): BackgroundWorkerState {
  return {
    pid: workerPid,
    ready: workerReady,
    metricsWorker: getMetricsWorkerState(),
    ingestWorker: getIngestWorkerState(),
    lastSnapshot,
    pendingMessageCount: regularWorkerMessageQueue.length + ingestUsageRecordMessageQueue.length + ingestRegularWorkerMessageQueue.length,
    pendingMessageBytes: regularWorkerMessageQueueBytes + ingestUsageRecordMessageQueueBytes + ingestRegularWorkerMessageQueueBytes,
    pendingQueues: buildAggregatePendingQueuesRuntime(),
    pendingSnapshotRequestCount: pendingRequests.size,
    timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount,
    pendingProcessEventLoopRequestCount: pendingProcessEventLoopRequests.size,
    timedOutProcessEventLoopRequestCount,
    failedProcessEventLoopRequestCount
  }
}

function getMetricsWorkerState(): BackgroundWorkerRoleState {
  return {
    pid: metricsWorkerPid,
    ready: metricsWorkerReady,
    lastSnapshot: metricsLastSnapshot,
    pendingSnapshotRequestCount: metricsPendingRequests.size,
    timedOutSnapshotRequestCount: metricsTimedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: metricsRejectedSnapshotRequestCount
  }
}

function getIngestWorkerState(): BackgroundWorkerRoleState {
  return {
    pid: ingestWorkerPid,
    ready: ingestWorkerReady,
    lastSnapshot: ingestLastSnapshot,
    pendingMessageCount: ingestUsageRecordMessageQueue.length + ingestRegularWorkerMessageQueue.length,
    pendingMessageBytes: ingestUsageRecordMessageQueueBytes + ingestRegularWorkerMessageQueueBytes,
    pendingQueues: buildIngestPendingQueuesRuntime(),
    pendingSnapshotRequestCount: ingestPendingRequests.size,
    timedOutSnapshotRequestCount: ingestTimedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount: ingestRejectedSnapshotRequestCount
  }
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
  }
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
  const queueIndex = regularWorkerMessageQueue.findIndex((queued) => (
    queued.type === 'background_worker_record_maintenance'
    && queued.items.some((item) => recordMaintenanceJobCoalescingKey(item) === key)
  ))
  if (queueIndex < 0) {
    return false
  }
  const current = regularWorkerMessageQueue.at(queueIndex)
  if (!current || current.type !== 'background_worker_record_maintenance') {
    return false
  }
  const currentBytes = estimateWorkerMessageBytes(current)
  const nextItems = compactRecordMaintenanceJobsForCoalescing(current.items.map((item) => (
    recordMaintenanceJobCoalescingKey(item) === key ? job : item
  )))
  const nextMessage: BackgroundWorkerMessage = { ...current, items: nextItems }
  const nextBytes = estimateWorkerMessageBytes(nextMessage)
  const nextQueueBytes = regularWorkerMessageQueueBytes - currentBytes + nextBytes
  if (nextBytes > regularWorkerMessageMaxBytes || nextQueueBytes > regularWorkerMessageQueueMaxBytes) {
    return false
  }
  regularWorkerMessageQueue.set(queueIndex, nextMessage)
  regularWorkerMessageQueueBytes = Math.max(0, regularWorkerMessageQueueBytes - currentBytes + nextBytes)
  const runtime = pendingQueueRuntime.recordMaintenance
  runtime.queueBytes = Math.max(0, (runtime.queueBytes ?? 0) - currentBytes + nextBytes)
  return true
}

function recordMaintenanceJobCoalescingKey(job: RecordMaintenanceJob): string | undefined {
  return job.type === 'account_usage_snapshot_upsert'
    ? `${job.accountId}\u0000${job.kind}`
    : undefined
}

function canQueueWorkerMessage(targetRole: 'worker' | 'ingest-worker', message: BackgroundWorkerMessage, messageBytes: number): boolean {
  if (message.type === 'background_worker_usage_records') {
    return targetRole === 'ingest-worker'
      && ingestUsageRecordMessageQueue.length < usageRecordMessageQueueMaxMessages
      && messageBytes <= usageRecordWorkerMessageMaxBytes
      && ingestUsageRecordMessageQueueBytes + messageBytes <= usageRecordMessageQueueMaxBytes
  }
  const regularQueueLength = targetRole === 'ingest-worker'
    ? ingestRegularWorkerMessageQueue.length
    : regularWorkerMessageQueue.length
  const regularQueueBytes = targetRole === 'ingest-worker'
    ? ingestRegularWorkerMessageQueueBytes
    : regularWorkerMessageQueueBytes
  if (message.type === 'background_worker_audit_logs') {
    return regularQueueLength < regularWorkerMessageQueueMaxMessages
      && messageBytes <= auditWorkerMessageMaxBytes
      && regularQueueBytes + messageBytes <= regularWorkerMessageQueueMaxBytes
  }
  return regularQueueLength < regularWorkerMessageQueueMaxMessages
    && messageBytes <= regularWorkerMessageMaxBytes
    && regularQueueBytes + messageBytes <= regularWorkerMessageQueueMaxBytes
}

function workerMessageTargetRole(message: BackgroundWorkerMessage): 'worker' | 'ingest-worker' {
  switch (message.type) {
    case 'background_worker_usage_records':
    case 'background_worker_audit_logs':
    case 'background_worker_operation_logs':
    case 'background_worker_runtime_log_line':
      return 'ingest-worker'
    default:
      return 'worker'
  }
}

function flushTargetWorkerMessageQueue(role: 'worker' | 'ingest-worker'): void {
  if (role === 'ingest-worker') {
    flushIngestWorkerMessageQueue()
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

function buildIngestPendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return clonePendingQueueRuntime(ingestPendingQueueRuntime)
}

function buildAggregatePendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return mergePendingQueuesRuntime(pendingQueueRuntime, ingestPendingQueueRuntime)
}

function addPendingQueueRuntimeMessage(targetRole: 'worker' | 'ingest-worker', key: IpcQueueKey, bytes: number): void {
  const queue = pendingQueueRuntimeForTarget(targetRole)[key]
  queue.queueLength += 1
  queue.queueBytes = (queue.queueBytes ?? 0) + bytes
}

function removePendingQueueRuntimeMessage(targetRole: 'worker' | 'ingest-worker', key: IpcQueueKey, bytes: number): void {
  const queue = pendingQueueRuntimeForTarget(targetRole)[key]
  queue.queueLength = Math.max(0, queue.queueLength - 1)
  queue.queueBytes = Math.max(0, (queue.queueBytes ?? 0) - bytes)
}

function pendingQueueRuntimeForTarget(targetRole: 'worker' | 'ingest-worker'): BackgroundWorkerIpcQueuesRuntime {
  return targetRole === 'ingest-worker' ? ingestPendingQueueRuntime : pendingQueueRuntime
}

function clonePendingQueueRuntime(input: BackgroundWorkerIpcQueuesRuntime): BackgroundWorkerIpcQueuesRuntime {
  return {
    usageRecords: { ...input.usageRecords },
    auditLogs: { ...input.auditLogs },
    operationLogs: { ...input.operationLogs },
    recordMaintenance: { ...input.recordMaintenance },
    runtimeLogLines: { ...input.runtimeLogLines },
    statusRequests: { ...input.statusRequests },
    processEventLoopRequests: { ...input.processEventLoopRequests },
    processEventLoopResponses: { ...input.processEventLoopResponses },
    gatewayRuntimeCacheInvalidations: { ...input.gatewayRuntimeCacheInvalidations },
    other: { ...input.other }
  }
}

type IpcQueueKey = keyof BackgroundWorkerIpcQueuesRuntime

function ipcQueueKeyForMessage(message: BackgroundWorkerMessage): IpcQueueKey {
  switch (message.type) {
    case 'background_worker_usage_records':
      return 'usageRecords'
    case 'background_worker_audit_logs':
      return 'auditLogs'
    case 'background_worker_operation_logs':
      return 'operationLogs'
    case 'background_worker_record_maintenance':
      return 'recordMaintenance'
    case 'background_worker_account_test_tasks':
    case 'background_worker_account_test_cancel':
      return 'other'
    case 'background_worker_runtime_log_line':
      return 'runtimeLogLines'
    case 'background_worker_status_request':
      return 'statusRequests'
    case 'background_worker_process_event_loop_request':
      return 'processEventLoopRequests'
    case 'background_worker_process_event_loop_response':
      return 'processEventLoopResponses'
    case 'server_account_runtime_clear':
      return 'other'
    case 'gateway_runtime_cache_invalidate':
      return 'gatewayRuntimeCacheInvalidations'
    case 'gateway_quota_snapshot_update':
      return 'other'
    default:
      return 'other'
  }
}

function emptyIpcQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  const emptyQueueRuntime = (): BackgroundWorkerIpcQueueRuntime => ({
    queueLength: 0,
    queueBytes: 0,
    droppedCount: 0,
    rejectedCount: 0
  })
  return {
    usageRecords: emptyQueueRuntime(),
    auditLogs: emptyQueueRuntime(),
    operationLogs: emptyQueueRuntime(),
    recordMaintenance: emptyQueueRuntime(),
    runtimeLogLines: emptyQueueRuntime(),
    statusRequests: emptyQueueRuntime(),
    processEventLoopRequests: emptyQueueRuntime(),
    processEventLoopResponses: emptyQueueRuntime(),
    gatewayRuntimeCacheInvalidations: emptyQueueRuntime(),
    other: emptyQueueRuntime()
  }
}

function ipcQueueKeys(): IpcQueueKey[] {
  return [
    'usageRecords',
    'auditLogs',
    'operationLogs',
    'recordMaintenance',
    'runtimeLogLines',
    'statusRequests',
    'processEventLoopRequests',
    'processEventLoopResponses',
    'gatewayRuntimeCacheInvalidations',
    'other'
  ]
}

function estimateWorkerMessageBytes(message: BackgroundWorkerMessage): number {
  if (typeof message === 'object' && message !== null) {
    const cached = workerMessageBytesCache.get(message)
    if (cached !== undefined) {
      return cached
    }
  }

  let bytes: number
  switch (message.type) {
    case 'background_worker_runtime_log_line':
      bytes = Buffer.byteLength(message.line, 'utf8')
        + Buffer.byteLength(message.sourceKey ?? '', 'utf8')
        + Buffer.byteLength(message.logFile ?? '', 'utf8')
        + 192
      break
    case 'background_worker_usage_records':
      bytes = message.items.reduce((sum, item) => Math.min(workerMessageEstimateMaxBytes, sum + estimateJsonBytes(item) + 256), 128)
      break
    case 'background_worker_audit_logs':
      bytes = message.items.reduce((sum, item) => Math.min(workerMessageEstimateMaxBytes, sum + estimateAuditLogBytes(item)), 128)
      break
    case 'background_worker_operation_logs':
      bytes = message.items.reduce((sum, item) => Math.min(workerMessageEstimateMaxBytes, sum + estimateJsonBytes(item) + 256), 128)
      break
    case 'background_worker_record_maintenance':
      bytes = message.items.reduce((sum, item) => Math.min(workerMessageEstimateMaxBytes, sum + estimateJsonBytes(item) + 256), 128)
      break
    case 'background_worker_account_test_tasks':
      bytes = message.taskIds.reduce((sum, taskId) => Math.min(workerMessageEstimateMaxBytes, sum + Buffer.byteLength(taskId, 'utf8') + 64), 128)
      break
    case 'background_worker_account_test_cancel':
      bytes = Buffer.byteLength(message.taskId, 'utf8') + 128
      break
    case 'background_worker_status_request':
    case 'background_worker_status_response':
    case 'background_worker_ready':
    case 'server_account_runtime_clear':
    case 'gateway_runtime_cache_invalidate':
    case 'gateway_quota_snapshot_update':
      bytes = 512
      break
    default:
      bytes = 512
      break
  }
  if (typeof message === 'object' && message !== null) {
    workerMessageBytesCache.set(message, bytes)
  }
  return bytes
}

function estimateJsonBytes(value: unknown): number {
  return estimateJsonLikeBytes(value, {
    maxBytes: workerMessageEstimateMaxBytes,
    maxNodes: workerMessageEstimateMaxNodes
  })
}

function markWorkerReady(role: BackgroundWorkerProcessRole, record: Partial<BackgroundWorkerMessage> & Record<string, unknown>): void {
  if (role === 'metrics-worker') {
    metricsWorkerReady = true
    metricsWorkerPid = typeof record.pid === 'number' ? record.pid : metricsWorkerPid
    metricsWorkerReadyHandler?.()
    return
  }
  if (role === 'ingest-worker') {
    ingestWorkerReady = true
    ingestWorkerPid = typeof record.pid === 'number' ? record.pid : ingestWorkerPid
    ingestWorkerReadyHandler?.()
    flushIngestWorkerMessageQueue()
    return
  }

  workerReady = true
  workerPid = typeof record.pid === 'number' ? record.pid : workerPid
  backgroundWorkerReadyHandler?.()
  flushWorkerMessageQueue()
}

function mergePendingQueuesRuntime(left: BackgroundWorkerIpcQueuesRuntime, right: BackgroundWorkerIpcQueuesRuntime): BackgroundWorkerIpcQueuesRuntime {
  const output = emptyIpcQueuesRuntime()
  for (const key of ipcQueueKeys()) {
    output[key] = {
      queueLength: left[key].queueLength + right[key].queueLength,
      queueBytes: (left[key].queueBytes ?? 0) + (right[key].queueBytes ?? 0),
      droppedCount: (left[key].droppedCount ?? 0) + (right[key].droppedCount ?? 0),
      rejectedCount: (left[key].rejectedCount ?? 0) + (right[key].rejectedCount ?? 0)
    }
  }
  return output
}

function finishWorkerStatusResponse(role: BackgroundWorkerProcessRole, requestId: string, snapshot: BackgroundWorkerRuntimeSnapshot | undefined): void {
  if (role === 'metrics-worker') {
    finishMetricsPendingRequest(requestId, snapshot)
    if (snapshot && typeof snapshot === 'object') {
      metricsLastSnapshot = snapshot
    }
    return
  }
  if (role === 'ingest-worker') {
    finishIngestPendingRequest(requestId, snapshot)
    if (snapshot && typeof snapshot === 'object') {
      ingestLastSnapshot = snapshot
    }
    return
  }

  finishPendingRequest(requestId, snapshot)
  if (snapshot && typeof snapshot === 'object') {
    lastSnapshot = snapshot
  }
}

function finishPendingRequest(requestId: string, snapshot: BackgroundWorkerRuntimeSnapshot | undefined): void {
  const pending = pendingRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingRequests.delete(requestId)
  pending.resolve(snapshot)
}

function finishMetricsPendingRequest(requestId: string, snapshot: BackgroundWorkerRuntimeSnapshot | undefined): void {
  const pending = metricsPendingRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  metricsPendingRequests.delete(requestId)
  pending.resolve(snapshot)
}

function finishIngestPendingRequest(requestId: string, snapshot: BackgroundWorkerRuntimeSnapshot | undefined): void {
  const pending = ingestPendingRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  ingestPendingRequests.delete(requestId)
  pending.resolve(snapshot)
}

function finishParentIngestStatusRequest(requestId: string, status: BackgroundWorkerIngestDrainStatus | undefined): void {
  const pending = pendingParentIngestStatusRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingParentIngestStatusRequests.delete(requestId)
  pending.resolve(status)
}

function failPendingRequests(): void {
  for (const [requestId, pending] of pendingRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingRequests.delete(requestId)
  }
  failPendingProcessEventLoopRequests()
}

function failMetricsPendingRequests(): void {
  for (const [requestId, pending] of metricsPendingRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    metricsPendingRequests.delete(requestId)
  }
}

function failIngestPendingRequests(): void {
  for (const [requestId, pending] of ingestPendingRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    ingestPendingRequests.delete(requestId)
  }
}

function failPendingProcessEventLoopRequests(): void {
  for (const [requestId, pending] of pendingProcessEventLoopRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingProcessEventLoopRequests.delete(requestId)
  }
}

function finishProcessEventLoopRequest(requestId: string, samples: ProcessEventLoopSample[] | undefined): void {
  const pending = pendingProcessEventLoopRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingProcessEventLoopRequests.delete(requestId)
  pending.resolve(samples)
}

function nonEmptyProcessEventLoopSamples(samples: unknown[]): ProcessEventLoopSample[] | undefined {
  return samples.length > 0 ? samples as ProcessEventLoopSample[] : undefined
}

function markWorkerIpcBroken(error: unknown, child = workerProcess): void {
  const isCurrentChild = child === undefined || workerProcess === child
  if (isCurrentChild) {
    workerReady = false
    workerPid = child?.pid ?? workerPid
    failPendingRequests()
  }
  if (child && !child.killed) {
    try {
      child.kill('SIGTERM')
    } catch (killError) {
      process.stderr.write(`[background-worker] 终止 IPC 异常 worker 失败：${killError instanceof Error ? killError.message : String(killError)}\n`)
    }
  }
  if (error) {
    process.stderr.write(`[background-worker] worker IPC 已断开：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function markMetricsWorkerIpcBroken(error: unknown, child = metricsWorkerProcess): void {
  const isCurrentChild = child === undefined || metricsWorkerProcess === child
  if (isCurrentChild) {
    metricsWorkerReady = false
    metricsWorkerPid = child?.pid ?? metricsWorkerPid
    failMetricsPendingRequests()
  }
  if (child && !child.killed) {
    try {
      child.kill('SIGTERM')
    } catch (killError) {
      process.stderr.write(`[background-worker] 终止 IPC 异常 metrics-worker 失败：${killError instanceof Error ? killError.message : String(killError)}\n`)
    }
  }
  if (error) {
    process.stderr.write(`[background-worker] metrics-worker IPC 已断开：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function markIngestWorkerIpcBroken(error: unknown, child = ingestWorkerProcess): void {
  const isCurrentChild = child === undefined || ingestWorkerProcess === child
  if (isCurrentChild) {
    ingestWorkerReady = false
    ingestWorkerPid = child?.pid ?? ingestWorkerPid
    failIngestPendingRequests()
  }
  if (child && !child.killed) {
    try {
      child.kill('SIGTERM')
    } catch (killError) {
      process.stderr.write(`[background-worker] 终止 IPC 异常 ingest-worker 失败：${killError instanceof Error ? killError.message : String(killError)}\n`)
    }
  }
  if (error) {
    process.stderr.write(`[background-worker] ingest-worker IPC 已断开：${error instanceof Error ? error.message : String(error)}\n`)
  }
}

function markParentIpcBroken(error: unknown): void {
  failPendingProcessEventLoopRequests()
  process.stderr.write(`[background-worker] 父进程 IPC 已断开：${error instanceof Error ? error.message : String(error)}\n`)
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
        if (child === ingestWorkerProcess) {
          markIngestWorkerIpcBroken(error, child)
        } else if (child === metricsWorkerProcess) {
          markMetricsWorkerIpcBroken(error, child)
        } else {
          markWorkerIpcBroken(error, child)
        }
      }
    })
  } catch (error) {
    if (child === ingestWorkerProcess) {
      markIngestWorkerIpcBroken(error, child)
    } else if (child === metricsWorkerProcess) {
      markMetricsWorkerIpcBroken(error, child)
    } else {
      markWorkerIpcBroken(error, child)
    }
  }
}

async function replaceServerGatewayQuotaSnapshot(snapshot: GatewayQuotaSnapshot): Promise<void> {
  const quotaSnapshotCache = await import('../gateway/quota/quota-snapshot-cache.service.js')
  quotaSnapshotCache.replaceGatewayQuotaSnapshot(snapshot)
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
  if (targetChild !== workerProcess) {
    const workerSamples = await requestWorkerProcessEventLoopSamples()
    if (workerSamples) {
      samples.push(...workerSamples)
    }
  }
  if (targetChild !== ingestWorkerProcess) {
    const ingestWorkerSamples = await requestIngestWorkerProcessEventLoopSamples()
    if (ingestWorkerSamples) {
      samples.push(...ingestWorkerSamples)
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
        if (child === metricsWorkerProcess) {
          markMetricsWorkerIpcBroken(error, child)
        } else if (child === ingestWorkerProcess) {
          markIngestWorkerIpcBroken(error, child)
        } else {
          markWorkerIpcBroken(error, child)
        }
      }
    })
  } catch (error) {
    if (child === metricsWorkerProcess) {
      markMetricsWorkerIpcBroken(error, child)
    } else if (child === ingestWorkerProcess) {
      markIngestWorkerIpcBroken(error, child)
    } else {
      markWorkerIpcBroken(error, child)
    }
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
