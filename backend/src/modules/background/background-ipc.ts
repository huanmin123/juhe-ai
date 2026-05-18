import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { AuditLogInput, OperationLogInput, UsageRecordInput } from '../../storage/repositories.js'
import type { RecordMaintenanceJob } from '../record-maintenance/record-maintenance-queue.service.js'
import type { RuntimeLogLineIndexOptions } from '../runtime-logs/runtime-log-index-queue.service.js'
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
}

type BackgroundWorkerMessage =
  | { type: 'background_worker_ready'; pid: number }
  | { type: 'background_worker_usage_records'; items: UsageRecordInput[] }
  | { type: 'background_worker_audit_logs'; items: AuditLogInput[] }
  | { type: 'background_worker_operation_logs'; items: OperationLogInput[] }
  | { type: 'background_worker_record_maintenance'; items: RecordMaintenanceJob[] }
  | ({ type: 'background_worker_runtime_log_line'; line: string } & RuntimeLogLineIndexOptions)
  | { type: 'background_worker_status_request'; requestId: string }
  | { type: 'background_worker_status_response'; requestId: string; snapshot: BackgroundWorkerRuntimeSnapshot }
  | { type: 'background_worker_process_event_loop_request'; requestId: string }
  | { type: 'background_worker_process_event_loop_response'; requestId: string; samples: ProcessEventLoopSample[] }
  | { type: 'gateway_runtime_cache_invalidate' }

interface PendingRequest {
  resolve: (snapshot: BackgroundWorkerRuntimeSnapshot | undefined) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
}

interface PendingProcessEventLoopRequest {
  resolve: (samples: ProcessEventLoopSample[]) => void
  timeout: NodeJS.Timeout
}

interface BackgroundWorkerState {
  pid?: number
  ready: boolean
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount: number
  pendingMessageBytes: number
  pendingQueues: BackgroundWorkerIpcQueuesRuntime
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
const usageRecordMessageQueue = new HeadIndexedQueue<Extract<BackgroundWorkerMessage, { type: 'background_worker_usage_records' }>>()
const regularWorkerMessageQueue = new HeadIndexedQueue<BackgroundWorkerMessage>()
let usageRecordMessageQueueBytes = 0
let regularWorkerMessageQueueBytes = 0
let sendingMessage = false
let sendingWorkerMessage: BackgroundWorkerMessage | undefined
let pendingPendingMessagesWarningCount = 0
const pendingMessageDropCounts = emptyIpcQueueCounts()
const pendingMessageRejectCounts = emptyIpcQueueCounts()
let pendingRequests = new Map<string, PendingRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let lastSnapshot: BackgroundWorkerRuntimeSnapshot | undefined
let backgroundWorkerReadyHandler: (() => void) | undefined

const maxPendingMessages = 5000
const maxPendingMessageBytes = 128 * 1024 * 1024

if (runtimeConfig.processRole === 'worker') {
  process.on('message', handleParentMessage)
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
    items
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
    return lastSnapshot
  }

  const requestId = randomUUID()
  return await new Promise<BackgroundWorkerRuntimeSnapshot | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      pendingRequests.delete(requestId)
      resolve(lastSnapshot)
    }, timeoutMs)
    pendingRequests.set(requestId, { resolve, reject, timeout })
    queueWorkerMessage({
      type: 'background_worker_status_request',
      requestId
    })
  })
}

export async function requestServerProcessEventLoopSamples(timeoutMs = 1000): Promise<ProcessEventLoopSample[]> {
  if (runtimeConfig.processRole !== 'worker' || typeof process.send !== 'function') {
    return []
  }

  const sendToParent = process.send.bind(process)
  const requestId = randomUUID()
  return await new Promise<ProcessEventLoopSample[]>((resolve) => {
    const timeout = setTimeout(() => {
      pendingProcessEventLoopRequests.delete(requestId)
      resolve([])
    }, timeoutMs)
    pendingProcessEventLoopRequests.set(requestId, { resolve, timeout })
    sendToParent({
      type: 'background_worker_process_event_loop_request',
      requestId
    } satisfies BackgroundWorkerMessage, (error) => {
      if (error) {
        finishProcessEventLoopRequest(requestId, [])
      }
    })
  })
}

export function getBackgroundWorkerState(): BackgroundWorkerState {
  return {
    pid: workerPid,
    ready: workerReady,
    lastSnapshot,
    pendingMessageCount: usageRecordMessageQueue.length + regularWorkerMessageQueue.length,
    pendingMessageBytes: usageRecordMessageQueueBytes + regularWorkerMessageQueueBytes,
    pendingQueues: buildPendingQueuesRuntime()
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
      finishProcessEventLoopRequest(record.requestId, record.samples as ProcessEventLoopSample[])
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
  finishProcessEventLoopRequest(record.requestId, record.samples as ProcessEventLoopSample[])
}

function queueWorkerMessage(message: BackgroundWorkerMessage): boolean {
  const messageBytes = estimateWorkerMessageBytes(message)
  if (message.type === 'background_worker_usage_records') {
    usageRecordMessageQueue.push(message)
    usageRecordMessageQueueBytes += messageBytes
  } else {
    regularWorkerMessageQueue.push(message)
    regularWorkerMessageQueueBytes += messageBytes
  }
  const droppedMessages = enforceWorkerMessageQueueLimits()
  const currentMessageDropped = droppedMessages.includes(message)
  if (currentMessageDropped) {
    return false
  }
  if (isWorkerQueueStillOverLimit(message)) {
    removeNewestWorkerMessage(message)
    recordPendingMessageRejected(message)
    pendingPendingMessagesWarningCount += 1
    process.stderr.write(`[background-worker] 消息队列已满，已拒绝 ${describeDroppedWorkerMessage(message)} ${pendingPendingMessagesWarningCount} 次\n`)
    return false
  }

  flushWorkerMessageQueue()
  return true
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
  const message = usageRecordMessageQueue.shift() ?? regularWorkerMessageQueue.shift()
  if (message) {
    const messageBytes = estimateWorkerMessageBytes(message)
    if (message.type === 'background_worker_usage_records') {
      usageRecordMessageQueueBytes = Math.max(0, usageRecordMessageQueueBytes - messageBytes)
    } else {
      regularWorkerMessageQueueBytes = Math.max(0, regularWorkerMessageQueueBytes - messageBytes)
    }
  }
  return message
}

function requeueWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  if (message.type === 'background_worker_usage_records') {
    usageRecordMessageQueue.unshift(message)
    usageRecordMessageQueueBytes += messageBytes
  } else {
    regularWorkerMessageQueue.unshift(message)
    regularWorkerMessageQueueBytes += messageBytes
  }
  enforceWorkerMessageQueueLimits()
}

function isWorkerQueueStillOverLimit(message: BackgroundWorkerMessage): boolean {
  if (message.type === 'background_worker_usage_records') {
    return usageRecordMessageQueue.length > maxPendingMessages || usageRecordMessageQueueBytes > maxPendingMessageBytes
  }
  return regularWorkerMessageQueue.length > maxPendingMessages || regularWorkerMessageQueueBytes > maxPendingMessageBytes
}

function removeNewestWorkerMessage(message: BackgroundWorkerMessage): void {
  if (message.type === 'background_worker_usage_records') {
    const dropped = usageRecordMessageQueue.removeAt(usageRecordMessageQueue.length - 1)
    if (dropped) {
      usageRecordMessageQueueBytes = Math.max(0, usageRecordMessageQueueBytes - estimateWorkerMessageBytes(dropped))
    }
    return
  }
  removeRegularWorkerMessageAt(regularWorkerMessageQueue.length - 1)
}

function enforceWorkerMessageQueueLimits(): BackgroundWorkerMessage[] {
  const droppedMessages: BackgroundWorkerMessage[] = []
  while (usageRecordMessageQueue.length > maxPendingMessages || usageRecordMessageQueueBytes > maxPendingMessageBytes) {
    const dropped = usageRecordMessageQueue.shift()
    if (!dropped) {
      break
    }
    usageRecordMessageQueueBytes = Math.max(0, usageRecordMessageQueueBytes - estimateWorkerMessageBytes(dropped))
    droppedMessages.push(dropped)
    recordPendingMessageDropped(dropped)
    pendingPendingMessagesWarningCount += 1
    process.stderr.write(`[background-worker] 消息队列已满，已丢弃 ${describeDroppedWorkerMessage(dropped)} ${pendingPendingMessagesWarningCount} 次\n`)
  }

  while (regularWorkerMessageQueue.length > maxPendingMessages || regularWorkerMessageQueueBytes > maxPendingMessageBytes) {
    const dropped = shiftDroppableRegularWorkerMessage()
    if (!dropped) {
      break
    }
    droppedMessages.push(dropped)
    recordPendingMessageDropped(dropped)
    pendingPendingMessagesWarningCount += 1
    process.stderr.write(`[background-worker] 消息队列已满，已丢弃 ${describeDroppedWorkerMessage(dropped)} ${pendingPendingMessagesWarningCount} 次\n`)
  }

  return droppedMessages
}

function shiftDroppableRegularWorkerMessage(): BackgroundWorkerMessage | undefined {
  const droppedSuccessAudit = shiftSuccessAuditWorkerMessage()
  if (droppedSuccessAudit) return droppedSuccessAudit

  const runtimeLogIndex = regularWorkerMessageQueue.findIndex((message) => message.type === 'background_worker_runtime_log_line')
  if (runtimeLogIndex >= 0) {
    return removeRegularWorkerMessageAt(runtimeLogIndex)
  }

  const nonAuditIndex = regularWorkerMessageQueue.findIndex((message) => message.type !== 'background_worker_audit_logs' && message.type !== 'background_worker_operation_logs' && message.type !== 'background_worker_record_maintenance')
  if (nonAuditIndex >= 0) {
    return removeRegularWorkerMessageAt(nonAuditIndex)
  }

  return undefined
}

function shiftSuccessAuditWorkerMessage(): BackgroundWorkerMessage | undefined {
  const messageIndex = regularWorkerMessageQueue.findIndex((message) => message.type === 'background_worker_audit_logs' && message.items.some(isSuccessAuditSample))
  if (messageIndex < 0) return undefined

  const message = regularWorkerMessageQueue.at(messageIndex)
  if (!message || message.type !== 'background_worker_audit_logs') return undefined

  const droppedItems = message.items.filter(isSuccessAuditSample)
  const retainedItems = message.items.filter((item) => !isSuccessAuditSample(item))
  if (retainedItems.length === 0) {
    return removeRegularWorkerMessageAt(messageIndex)
  }

  const originalBytes = estimateWorkerMessageBytes(message)
  const retainedMessage: BackgroundWorkerMessage = { ...message, items: retainedItems }
  regularWorkerMessageQueue.set(messageIndex, retainedMessage)
  regularWorkerMessageQueueBytes = Math.max(0, regularWorkerMessageQueueBytes - originalBytes + estimateWorkerMessageBytes(retainedMessage))
  return { ...message, items: droppedItems }
}

function removeRegularWorkerMessageAt(index: number): BackgroundWorkerMessage | undefined {
  const message = regularWorkerMessageQueue.removeAt(index)
  if (message) {
    regularWorkerMessageQueueBytes = Math.max(0, regularWorkerMessageQueueBytes - estimateWorkerMessageBytes(message))
  }
  return message
}

function isSuccessAuditSample(input: AuditLogInput): boolean {
  return input.auditOutcome === 'success' && input.success === true
}

function describeDroppedWorkerMessage(message: BackgroundWorkerMessage): string {
  if (message.type !== 'background_worker_audit_logs') {
    return `${message.type} 消息`
  }
  const successCount = message.items.filter(isSuccessAuditSample).length
  const retainedCount = message.items.length - successCount
  return `审计日志消息（成功样本 ${successCount} 条，需保留事件 ${retainedCount} 条）`
}

function buildPendingQueuesRuntime(): BackgroundWorkerIpcQueuesRuntime {
  const runtime = emptyIpcQueuesRuntime()
  addQueueRuntimeMessages(runtime, usageRecordMessageQueue)
  addQueueRuntimeMessages(runtime, regularWorkerMessageQueue)
  for (const key of ipcQueueKeys()) {
    runtime[key].droppedCount = pendingMessageDropCounts[key]
    runtime[key].rejectedCount = pendingMessageRejectCounts[key]
  }
  return runtime
}

function addQueueRuntimeMessages(runtime: BackgroundWorkerIpcQueuesRuntime, queue: HeadIndexedQueue<BackgroundWorkerMessage>): void {
  for (let index = 0; index < queue.length; index += 1) {
    const message = queue.at(index)
    if (!message) continue
    const item = runtime[ipcQueueKeyForMessage(message)]
    item.queueLength += 1
    item.queueBytes = (item.queueBytes ?? 0) + estimateWorkerMessageBytes(message)
  }
}

function recordPendingMessageDropped(message: BackgroundWorkerMessage): void {
  pendingMessageDropCounts[ipcQueueKeyForMessage(message)] += 1
}

function recordPendingMessageRejected(message: BackgroundWorkerMessage): void {
  pendingMessageRejectCounts[ipcQueueKeyForMessage(message)] += 1
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
    case 'background_worker_runtime_log_line':
      return 'runtimeLogLines'
    case 'background_worker_status_request':
      return 'statusRequests'
    case 'background_worker_process_event_loop_request':
      return 'processEventLoopRequests'
    case 'background_worker_process_event_loop_response':
      return 'processEventLoopResponses'
    case 'gateway_runtime_cache_invalidate':
      return 'gatewayRuntimeCacheInvalidations'
    default:
      return 'other'
  }
}

function emptyIpcQueueCounts(): Record<IpcQueueKey, number> {
  return Object.fromEntries(ipcQueueKeys().map((key) => [key, 0])) as Record<IpcQueueKey, number>
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
  switch (message.type) {
    case 'background_worker_runtime_log_line':
      return Buffer.byteLength(message.line, 'utf8')
        + Buffer.byteLength(message.sourceKey ?? '', 'utf8')
        + Buffer.byteLength(message.logFile ?? '', 'utf8')
        + 192
    case 'background_worker_usage_records':
      return message.items.reduce((sum, item) => sum + estimateJsonBytes(item) + 256, 128)
    case 'background_worker_audit_logs':
      return message.items.reduce((sum, item) => sum + estimateAuditLogBytes(item), 128)
    case 'background_worker_operation_logs':
      return message.items.reduce((sum, item) => sum + estimateJsonBytes(item) + 256, 128)
    case 'background_worker_record_maintenance':
      return message.items.reduce((sum, item) => sum + estimateJsonBytes(item) + 256, 128)
    case 'background_worker_status_request':
    case 'background_worker_status_response':
    case 'background_worker_ready':
    case 'gateway_runtime_cache_invalidate':
      return 512
    default:
      return 512
  }
}

function estimateAuditLogBytes(input: AuditLogInput): number {
  const payloadBytes = input.payloads.reduce((sum, payload) => {
    const body = payload.body
    const bodyBytes = Buffer.isBuffer(body) ? body.byteLength : typeof body === 'string' ? Buffer.byteLength(body, 'utf8') : 0
    const headerBytes = payload.headers ? estimateJsonBytes(payload.headers) : 0
    return sum + bodyBytes + headerBytes + 512
  }, 0)
  return payloadBytes + input.attempts.length * 512 + 2048
}

function estimateJsonBytes(value: unknown): number {
  return estimateJsonLikeBytes(value)
}

function estimateJsonLikeBytes(value: unknown, seen = new WeakSet<object>()): number {
  if (value === null || value === undefined) return 4
  if (typeof value === 'string') return Buffer.byteLength(value, 'utf8') + 2
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint') {
    return String(value).length
  }
  if (Buffer.isBuffer(value)) return value.byteLength
  if (value instanceof Date) return value.toISOString().length + 2
  if (Array.isArray(value)) {
    if (seen.has(value)) return 16
    seen.add(value)
    let bytes = 2
    for (const item of value) {
      bytes += estimateJsonLikeBytes(item, seen) + 1
    }
    return bytes
  }
  if (typeof value === 'object') {
    if (seen.has(value)) return 16
    seen.add(value)
    let bytes = 2
    for (const [key, item] of Object.entries(value as Record<string, unknown>)) {
      bytes += Buffer.byteLength(key, 'utf8') + 3 + estimateJsonLikeBytes(item, seen) + 1
    }
    return bytes
  }
  return 16
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
    pending.resolve(lastSnapshot)
    pendingRequests.delete(requestId)
  }
}

function finishProcessEventLoopRequest(requestId: string, samples: ProcessEventLoopSample[]): void {
  const pending = pendingProcessEventLoopRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingProcessEventLoopRequests.delete(requestId)
  pending.resolve(samples)
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

async function clearServerGatewayRuntimeCache(): Promise<void> {
  const dbServiceIpc = await import('../db-service/db-service-ipc.js')
  const gatewayCache = await import('../gateway/gateway-runtime-cache.service.js')
  gatewayCache.clearGatewayRuntimeCacheLocal()
  dbServiceIpc.clearDbServiceGatewayRuntimeCache()
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
