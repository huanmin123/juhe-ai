import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import { estimateJsonLikeBytes } from '../../shared/queue-size.js'
import type { AuditLogInput, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { GatewayQuotaSnapshot } from '../gateway/gateway-quota-snapshot-cache.service.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type { RuntimeLogLineIndexOptions } from '../runtime-logs/runtime-log-index-queue.service.js'
import type { AccountRuntimeAvailabilityClearTarget } from '../db-service/db-service-types.js'
import type { WorkerScheduledJobRuntimeSnapshot } from './worker-scheduler.js'

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
  jobs: WorkerScheduledJobRuntimeSnapshot[]
  usageRecordQueue: BackgroundWorkerQueueRuntime
  operationLogQueue: BackgroundWorkerQueueRuntime
  recordMaintenanceQueue: BackgroundWorkerQueueRuntime
  auditLogQueue: BackgroundWorkerQueueRuntime
  runtimeLogIndexQueue: BackgroundWorkerRuntimeLogQueueRuntime
  cooldownAccountRetestQueue?: BackgroundWorkerRetryQueueRuntime
  manualAccountTestQueue?: BackgroundWorkerRetryQueueRuntime
}

type BackgroundWorkerMessage =
  | { type: 'background_worker_ready'; pid: number }
  | { type: 'background_worker_usage_records'; items: UsageRecordInput[] }
  | { type: 'background_worker_audit_logs'; items: AuditLogInput[] }
  | { type: 'background_worker_operation_logs'; items: OperationLogInput[] }
  | { type: 'background_worker_record_maintenance'; items: RecordMaintenanceJob[] }
  | { type: 'background_worker_account_test_tasks'; taskIds: string[] }
  | { type: 'background_worker_account_test_cancel'; taskId: string }
  | ({ type: 'background_worker_runtime_log_line'; line: string } & RuntimeLogLineIndexOptions)
  | { type: 'background_worker_status_request'; requestId: string }
  | { type: 'background_worker_status_response'; requestId: string; snapshot: BackgroundWorkerRuntimeSnapshot }
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

interface PendingProcessEventLoopRequest {
  resolve: (samples: ProcessEventLoopSample[] | undefined) => void
  timeout: NodeJS.Timeout
}

interface BackgroundWorkerState {
  pid?: number
  ready: boolean
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

class HeadIndexedQueue<T> {
  private items: T[] = []
  private headIndex = 0

  get length(): number {
    return this.items.length - this.headIndex
  }

  push(item: T): void {
    this.items.push(item)
  }

  unshift(item: T): void {
    if (this.headIndex > 0) {
      this.headIndex -= 1
      this.items[this.headIndex] = item
      return
    }
    this.items.unshift(item)
  }

  shift(): T | undefined {
    if (this.length <= 0) {
      return undefined
    }
    const item = this.items[this.headIndex]
    this.headIndex += 1
    this.compactConsumedItems()
    return item
  }

  at(index: number): T | undefined {
    if (index < 0 || index >= this.length) {
      return undefined
    }
    return this.items[this.headIndex + index]
  }

  set(index: number, item: T): void {
    if (index < 0 || index >= this.length) {
      return
    }
    this.items[this.headIndex + index] = item
  }

  findIndex(predicate: (item: T) => boolean): number {
    for (let index = 0; index < this.length; index += 1) {
      const item = this.at(index)
      if (item !== undefined && predicate(item)) {
        return index
      }
    }
    return -1
  }

  removeAt(index: number): T | undefined {
    if (index < 0 || index >= this.length) {
      return undefined
    }
    const physicalIndex = this.headIndex + index
    if (physicalIndex === this.headIndex) {
      return this.shift()
    }
    const [item] = this.items.splice(physicalIndex, 1)
    return item
  }

  private compactConsumedItems(): void {
    if (this.headIndex === 0) {
      return
    }
    if (this.headIndex >= this.items.length) {
      this.items = []
      this.headIndex = 0
      return
    }
    if (this.headIndex > 64 && this.headIndex * 2 > this.items.length) {
      this.items = this.items.slice(this.headIndex)
      this.headIndex = 0
    }
  }
}

let workerProcess: ChildProcess | undefined
let workerReady = false
let workerPid: number | undefined
const usageRecordMessageQueueMaxMessages = 10_000
const usageRecordMessageQueueMaxBytes = 64 * 1024 * 1024
const regularWorkerMessageQueueMaxMessages = 5_000
const regularWorkerMessageQueueMaxBytes = 64 * 1024 * 1024
const usageRecordWorkerMessageMaxBytes = 8 * 1024 * 1024
const regularWorkerMessageMaxBytes = 8 * 1024 * 1024
const workerMessageEstimateMaxBytes = Math.max(usageRecordWorkerMessageMaxBytes, regularWorkerMessageMaxBytes) + 1
const workerMessageEstimateMaxNodes = 20_000
const auditWorkerMessageMaxBytes = 2 * 1024 * 1024
const auditWorkerPayloadBodyInlineMaxBytes = 512 * 1024
const usageRecordMessageQueue = new HeadIndexedQueue<Extract<BackgroundWorkerMessage, { type: 'background_worker_usage_records' }>>()
const regularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
let usageRecordMessageQueueBytes = 0
let regularWorkerMessageQueueBytes = 0
let sendingMessage = false
let sendingWorkerMessage: BackgroundWorkerMessage | undefined
const pendingQueueRuntime = emptyIpcQueuesRuntime()
const workerMessageBytesCache = new WeakMap<object, number>()
let pendingRequests = new Map<string, PendingRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let timedOutSnapshotRequestCount = 0
let rejectedSnapshotRequestCount = 0
let timedOutProcessEventLoopRequestCount = 0
let failedProcessEventLoopRequestCount = 0
let lastSnapshot: BackgroundWorkerRuntimeSnapshot | undefined
let backgroundWorkerReadyHandler: (() => void) | undefined

if (runtimeConfig.processRole === 'worker') {
  process.on('message', handleParentMessage)
  process.once('disconnect', () => {
    markParentIpcBroken(new Error('后台 worker 父进程 IPC 已断开'))
  })
}

export function attachBackgroundWorkerProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  workerProcess = child
  workerPid = child.pid ?? undefined
  workerReady = false
  backgroundWorkerReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', handleWorkerMessage)
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

export function sendUsageRecordsToWorker(items: UsageRecordInput[]): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  return sendBackgroundWorkerMessage({
    type: 'background_worker_usage_records',
    items
  })
}

export function sendAuditLogsToWorker(items: AuditLogInput[]): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  return sendBackgroundWorkerMessage({
    type: 'background_worker_audit_logs',
    items: trimAuditLogsForWorkerIpc(items)
  })
}

export function sendOperationLogsToWorker(items: OperationLogInput[]): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  return sendBackgroundWorkerMessage({
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
  return sendBackgroundWorkerMessage({
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

export function getBackgroundWorkerState(): BackgroundWorkerState {
  return {
    pid: workerPid,
    ready: workerReady,
    lastSnapshot,
    pendingMessageCount: usageRecordMessageQueue.length + regularWorkerMessageQueue.length,
    pendingMessageBytes: usageRecordMessageQueueBytes + regularWorkerMessageQueueBytes,
    pendingQueues: buildPendingQueuesRuntime(),
    pendingSnapshotRequestCount: pendingRequests.size,
    timedOutSnapshotRequestCount,
    rejectedSnapshotRequestCount,
    pendingProcessEventLoopRequestCount: pendingProcessEventLoopRequests.size,
    timedOutProcessEventLoopRequestCount,
    failedProcessEventLoopRequestCount
  }
}

function handleWorkerMessage(message: unknown): void {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return
  }

  const record = message as Partial<BackgroundWorkerMessage> & Record<string, unknown>
  switch (record.type) {
    case 'background_worker_ready':
      workerReady = true
      workerPid = typeof record.pid === 'number' ? record.pid : workerPid
      backgroundWorkerReadyHandler?.()
      flushWorkerMessageQueue()
      break
    case 'background_worker_status_response':
      if (typeof record.requestId !== 'string') break
      finishPendingRequest(record.requestId, record.snapshot as BackgroundWorkerRuntimeSnapshot | undefined)
      if (record.snapshot && typeof record.snapshot === 'object') {
        lastSnapshot = record.snapshot as BackgroundWorkerRuntimeSnapshot
      }
      break
    case 'background_worker_process_event_loop_response':
      if (typeof record.requestId !== 'string' || !Array.isArray(record.samples)) break
      finishProcessEventLoopRequest(record.requestId, nonEmptyProcessEventLoopSamples(record.samples))
      break
    case 'background_worker_process_event_loop_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string') {
        void respondToProcessEventLoopRequest(record.requestId)
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
  if (record.type !== 'background_worker_process_event_loop_response' || typeof record.requestId !== 'string' || !Array.isArray(record.samples)) {
    return
  }
  finishProcessEventLoopRequest(record.requestId, nonEmptyProcessEventLoopSamples(record.samples))
}

function queueWorkerMessage(inputMessage: BackgroundWorkerMessage): boolean {
  const message = coalesceWorkerMessage(inputMessage)
  if (!message) {
    flushWorkerMessageQueue()
    return true
  }
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  if (!canQueueWorkerMessage(message, messageBytes)) {
    pendingQueueRuntime[queueKey].rejectedCount = (pendingQueueRuntime[queueKey].rejectedCount ?? 0) + 1
    return false
  }
  if (message.type === 'background_worker_usage_records') {
    usageRecordMessageQueue.push(message)
    usageRecordMessageQueueBytes += messageBytes
  } else {
    regularWorkerMessageQueue.push(message)
    regularWorkerMessageQueueBytes += messageBytes
  }
  addPendingQueueRuntimeMessage(queueKey, messageBytes)

  flushWorkerMessageQueue()
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

function canQueueWorkerMessage(message: BackgroundWorkerMessage, messageBytes: number): boolean {
  if (message.type === 'background_worker_usage_records') {
    return usageRecordMessageQueue.length < usageRecordMessageQueueMaxMessages
      && messageBytes <= usageRecordWorkerMessageMaxBytes
      && usageRecordMessageQueueBytes + messageBytes <= usageRecordMessageQueueMaxBytes
  }
  if (message.type === 'background_worker_audit_logs') {
    return regularWorkerMessageQueue.length < regularWorkerMessageQueueMaxMessages
      && messageBytes <= auditWorkerMessageMaxBytes
      && regularWorkerMessageQueueBytes + messageBytes <= regularWorkerMessageQueueMaxBytes
  }
  return regularWorkerMessageQueue.length < regularWorkerMessageQueueMaxMessages
    && messageBytes <= regularWorkerMessageMaxBytes
    && regularWorkerMessageQueueBytes + messageBytes <= regularWorkerMessageQueueMaxBytes
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

function shiftWorkerMessage(): BackgroundWorkerMessage | undefined {
  let queueKey: IpcQueueKey | undefined
  const usageMessage = usageRecordMessageQueue.shift()
  const message = usageMessage ?? regularWorkerMessageQueue.shift()
  if (message) {
    queueKey = ipcQueueKeyForMessage(message)
    const messageBytes = estimateWorkerMessageBytes(message)
    if (message.type === 'background_worker_usage_records') {
      usageRecordMessageQueueBytes = Math.max(0, usageRecordMessageQueueBytes - messageBytes)
    } else {
      regularWorkerMessageQueueBytes = Math.max(0, regularWorkerMessageQueueBytes - messageBytes)
    }
    removePendingQueueRuntimeMessage(queueKey, messageBytes)
  }
  return message
}

function requeueWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  const queueKey = ipcQueueKeyForMessage(message)
  if (message.type === 'background_worker_usage_records') {
    usageRecordMessageQueue.unshift(message)
    usageRecordMessageQueueBytes += messageBytes
  } else {
    regularWorkerMessageQueue.unshift(message)
    regularWorkerMessageQueueBytes += messageBytes
  }
  addPendingQueueRuntimeMessage(queueKey, messageBytes)
}

function buildPendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  return clonePendingQueueRuntime(pendingQueueRuntime)
}

function addPendingQueueRuntimeMessage(key: IpcQueueKey, bytes: number): void {
  const queue = pendingQueueRuntime[key]
  queue.queueLength += 1
  queue.queueBytes = (queue.queueBytes ?? 0) + bytes
}

function removePendingQueueRuntimeMessage(key: IpcQueueKey, bytes: number): void {
  const queue = pendingQueueRuntime[key]
  queue.queueLength = Math.max(0, queue.queueLength - 1)
  queue.queueBytes = Math.max(0, (queue.queueBytes ?? 0) - bytes)
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

function trimAuditLogsForWorkerIpc(items: AuditLogInput[]): AuditLogInput[] {
  return items.map(trimAuditLogForWorkerIpc)
}

function trimAuditLogForWorkerIpc(item: AuditLogInput): AuditLogInput {
  let next = trimAuditLogTopLevelStringsForWorkerIpc(item)
  next = trimAuditLogPayloadBodiesForWorkerIpc(next)
  if (estimateAuditLogBytes(next) <= auditWorkerMessageMaxBytes) {
    return next
  }
  next = trimAuditLogPayloadHeadersForWorkerIpc(next)
  if (estimateAuditLogBytes(next) <= auditWorkerMessageMaxBytes) {
    return next
  }
  next = trimAuditLogAttemptsForWorkerIpc(next)
  if (estimateAuditLogBytes(next) <= auditWorkerMessageMaxBytes) {
    return next
  }
  return trimAuditLogPayloadsForWorkerIpc(next)
}

function trimAuditLogTopLevelStringsForWorkerIpc(item: AuditLogInput): AuditLogInput {
  return {
    ...item,
    path: truncateAuditIpcString(item.path, 2048),
    queryString: truncateOptionalAuditIpcString(item.queryString, 4096),
    model: truncateOptionalAuditIpcString(item.model, 512),
    clientIp: truncateOptionalAuditIpcString(item.clientIp, 256),
    userAgent: truncateOptionalAuditIpcString(item.userAgent, 2048),
    errorPhase: truncateOptionalAuditIpcString(item.errorPhase, 256),
    errorCode: truncateOptionalAuditIpcString(item.errorCode, 512),
    errorMessage: truncateOptionalAuditIpcString(item.errorMessage, 4096),
    sampleReason: truncateAuditIpcString(item.sampleReason, 1024)
  }
}

function trimAuditLogPayloadBodiesForWorkerIpc(item: AuditLogInput): AuditLogInput {
  let bytes = 2048 + auditAttemptsBytes(item.attempts)
  let changed = false
  const payloads: AuditLogInput['payloads'] = item.payloads.map((payload) => {
    const headerBytes = payload.headers ? estimateJsonBytes(payload.headers) : 0
    const bodyBytes = auditPayloadBodyBytes(payload.body)
    const nextBytes = headerBytes + bodyBytes + 512
    const shouldDropBody = bodyBytes > auditWorkerPayloadBodyInlineMaxBytes || bytes + nextBytes > auditWorkerMessageMaxBytes
    bytes += shouldDropBody ? headerBytes + 512 : nextBytes
    if (!shouldDropBody) {
      return payload
    }
    changed = true
    return trimAuditPayloadBodyForWorkerIpc(payload, bodyBytes)
  })
  return changed ? markAuditLogDroppedForWorkerIpc(item, payloads) : item
}

function trimAuditLogPayloadHeadersForWorkerIpc(item: AuditLogInput): AuditLogInput {
  let changed = false
  const payloads: AuditLogInput['payloads'] = item.payloads.map((payload) => {
    if (!payload.headers) {
      return payload
    }
    changed = true
    return {
      ...payload,
      headers: undefined,
      captureStatus: payload.captureStatus === 'complete' || payload.captureStatus === undefined
        ? 'dropped' as const
        : payload.captureStatus
    }
  })
  return changed ? markAuditLogDroppedForWorkerIpc(item, payloads) : item
}

function trimAuditLogAttemptsForWorkerIpc(item: AuditLogInput): AuditLogInput {
  const attempts = item.attempts.map((attempt) => ({
    ...attempt,
    proxyUrl: truncateOptionalAuditIpcString(attempt.proxyUrl, 2048),
    upstreamUrl: truncateAuditIpcString(attempt.upstreamUrl, 4096),
    errorPhase: truncateOptionalAuditIpcString(attempt.errorPhase, 256),
    errorCode: truncateOptionalAuditIpcString(attempt.errorCode, 512),
    errorMessage: truncateOptionalAuditIpcString(attempt.errorMessage, 4096)
  }))
  const maxAttempts = 16
  const limitedAttempts = attempts.length <= maxAttempts
    ? attempts
    : [
        ...attempts.slice(0, maxAttempts - 1),
        attempts[attempts.length - 1]
      ]
  return limitedAttempts === item.attempts
    ? item
    : {
        ...item,
        attempts: limitedAttempts,
        captureStatus: item.captureStatus === 'overflow' ? 'overflow' : 'dropped'
      }
}

function trimAuditLogPayloadsForWorkerIpc(item: AuditLogInput): AuditLogInput {
  const maxPayloads = 32
  const payloads = item.payloads.map((payload) => trimAuditPayloadForWorkerIpc(payload))
  const limitedPayloads = payloads.length <= maxPayloads
    ? payloads
    : [
        ...payloads.slice(0, maxPayloads - 1),
        payloads[payloads.length - 1]
      ]
  return markAuditLogDroppedForWorkerIpc(item, limitedPayloads)
}

function trimAuditPayloadBodyForWorkerIpc(
  payload: AuditLogInput['payloads'][number],
  bodyBytes = auditPayloadBodyBytes(payload.body)
): AuditLogInput['payloads'][number] {
  return {
    ...payload,
    body: undefined,
    contentEncoding: undefined,
    rawBodySizeBytes: payload.rawBodySizeBytes ?? bodyBytes,
    captureStatus: payload.bodySha256 ? 'hash_only' as const : 'dropped' as const
  }
}

function trimAuditPayloadForWorkerIpc(payload: AuditLogInput['payloads'][number]): AuditLogInput['payloads'][number] {
  return {
    ...trimAuditPayloadBodyForWorkerIpc(payload),
    headers: undefined,
    contentType: truncateOptionalAuditIpcString(payload.contentType, 512),
    attemptTempId: truncateOptionalAuditIpcString(payload.attemptTempId, 128)
  }
}

function markAuditLogDroppedForWorkerIpc(
  item: AuditLogInput,
  payloads: AuditLogInput['payloads']
): AuditLogInput {
  return {
    ...item,
    payloads,
    captureStatus: item.captureStatus === 'overflow' ? 'overflow' : 'dropped'
  }
}

function auditPayloadBodyBytes(body: Buffer | string | undefined): number {
  if (body === undefined) return 0
  return Buffer.isBuffer(body) ? body.byteLength : estimateJsonBytes(body)
}

function truncateOptionalAuditIpcString(value: string | undefined, maxBytes: number): string | undefined {
  return typeof value === 'string' ? truncateAuditIpcString(value, maxBytes) : undefined
}

function truncateAuditIpcString(value: string, maxBytes: number): string {
  if (Buffer.byteLength(value, 'utf8') <= maxBytes) {
    return value
  }
  return `${Buffer.from(value).subarray(0, Math.max(0, maxBytes - 32)).toString('utf8')}...[truncated]`
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

function estimateAuditLogBytes(input: AuditLogInput): number {
  const payloadBytes = input.payloads.reduce((sum, payload) => {
    const body = payload.body
    const bodyBytes = Buffer.isBuffer(body) ? body.byteLength : typeof body === 'string' ? estimateJsonBytes(body) : 0
    const headerBytes = payload.headers ? estimateJsonBytes(payload.headers) : 0
    return Math.min(workerMessageEstimateMaxBytes, sum + bodyBytes + headerBytes + 512)
  }, 0)
  return Math.min(workerMessageEstimateMaxBytes, payloadBytes + auditAttemptsBytes(input.attempts) + estimateAuditTopLevelBytes(input) + 2048)
}

function estimateAuditTopLevelBytes(input: AuditLogInput): number {
  return estimateJsonBytes({
    traceId: input.traceId,
    trafficSource: input.trafficSource,
    systemAccountId: input.systemAccountId,
    apiKeyId: input.apiKeyId,
    groupId: input.groupId,
    accountId: input.accountId,
    providerCode: input.providerCode,
    method: input.method,
    path: input.path,
    queryString: input.queryString,
    model: input.model,
    stream: input.stream,
    clientIp: input.clientIp,
    userAgent: input.userAgent,
    auditOutcome: input.auditOutcome,
    success: input.success,
    finalStatusCode: input.finalStatusCode,
    errorPhase: input.errorPhase,
    errorCode: input.errorCode,
    errorMessage: input.errorMessage,
    sampleBucket: input.sampleBucket,
    sampleReason: input.sampleReason,
    captureStatus: input.captureStatus,
    startedAt: input.startedAt,
    endedAt: input.endedAt,
    durationMs: input.durationMs,
    firstTokenMs: input.firstTokenMs,
    createdAt: input.createdAt
  })
}

function auditAttemptsBytes(attempts: AuditLogInput['attempts']): number {
  return attempts.reduce((sum, attempt) => Math.min(workerMessageEstimateMaxBytes, sum + estimateJsonBytes(attempt) + 128), 0)
}

function estimateJsonBytes(value: unknown): number {
  return estimateJsonLikeBytes(value, {
    maxBytes: workerMessageEstimateMaxBytes,
    maxNodes: workerMessageEstimateMaxNodes
  })
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

function failPendingRequests(): void {
  for (const [requestId, pending] of pendingRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingRequests.delete(requestId)
  }
  failPendingProcessEventLoopRequests()
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

function markParentIpcBroken(error: unknown): void {
  failPendingProcessEventLoopRequests()
  process.stderr.write(`[background-worker] 父进程 IPC 已断开：${error instanceof Error ? error.message : String(error)}\n`)
  process.exit(1)
}

async function clearServerGatewayRuntimeCache(): Promise<void> {
  const dbServiceIpc = await import('../db-service/db-service-ipc.js')
  const gatewayCache = await import('../gateway/gateway-runtime-cache.service.js')
  gatewayCache.clearGatewayRuntimeCacheLocal()
  dbServiceIpc.clearDbServiceGatewayRuntimeCache()
}

async function clearServerAccountRuntimeAvailability(target: AccountRuntimeAvailabilityClearTarget): Promise<void> {
  const gatewaySideEffects = await import('../gateway/gateway-account-side-effects.service.js')
  gatewaySideEffects.clearGatewayAccountRuntimeAvailability(target)
}

async function replaceServerGatewayQuotaSnapshot(snapshot: GatewayQuotaSnapshot): Promise<void> {
  const quotaSnapshotCache = await import('../gateway/gateway-quota-snapshot-cache.service.js')
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

async function respondToProcessEventLoopRequest(requestId: string): Promise<void> {
  const samples = [
    buildProcessEventLoopSample('server')
  ]
  const dbServiceSample = await dbServiceProcessEventLoopSample()
  if (dbServiceSample) {
    samples.push(dbServiceSample)
  }

  const child = workerProcess
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
        markWorkerIpcBroken(error, child)
      }
    })
  } catch (error) {
    markWorkerIpcBroken(error, child)
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
