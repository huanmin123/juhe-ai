import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import type { AuditLogInput, UsageRecordInput } from '../../storage/repositories.js'

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

export interface BackgroundWorkerRuntimeSnapshot {
  pid: number
  ready: boolean
  processRole: 'worker'
  usageRecordQueue: BackgroundWorkerQueueRuntime
  auditLogQueue: BackgroundWorkerQueueRuntime
  runtimeLogIndexQueue: BackgroundWorkerRuntimeLogQueueRuntime
}

type BackgroundWorkerMessage =
  | { type: 'background_worker_ready'; pid: number }
  | { type: 'background_worker_usage_records'; items: UsageRecordInput[] }
  | { type: 'background_worker_audit_logs'; items: AuditLogInput[] }
  | { type: 'background_worker_runtime_log_line'; line: string }
  | { type: 'background_worker_status_request'; requestId: string }
  | { type: 'background_worker_status_response'; requestId: string; snapshot: BackgroundWorkerRuntimeSnapshot }
  | { type: 'gateway_runtime_cache_invalidate' }

interface PendingRequest {
  resolve: (snapshot: BackgroundWorkerRuntimeSnapshot | undefined) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
}

interface BackgroundWorkerState {
  pid?: number
  ready: boolean
  lastSnapshot?: BackgroundWorkerRuntimeSnapshot
  pendingMessageCount: number
  pendingMessageBytes: number
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
let pendingRequests = new Map<string, PendingRequest>()
let lastSnapshot: BackgroundWorkerRuntimeSnapshot | undefined

const maxPendingMessages = 5000
const maxPendingMessageBytes = 128 * 1024 * 1024

export function attachBackgroundWorkerProcess(child: ChildProcess): void {
  workerProcess = child
  workerPid = child.pid ?? undefined
  workerReady = false

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

export function sendRuntimeLogLineToWorker(line: string): boolean {
  return sendBackgroundWorkerMessage({
    type: 'background_worker_runtime_log_line',
    line
  })
}

export function sendBackgroundWorkerMessage(message: BackgroundWorkerMessage): boolean {
  if (runtimeConfig.processRole === 'worker') {
    return false
  }

  queueWorkerMessage(message)
  return true
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

export function getBackgroundWorkerState(): BackgroundWorkerState {
  return {
    pid: workerPid,
    ready: workerReady,
    lastSnapshot,
    pendingMessageCount: usageRecordMessageQueue.length + regularWorkerMessageQueue.length,
    pendingMessageBytes: usageRecordMessageQueueBytes + regularWorkerMessageQueueBytes
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
      flushWorkerMessageQueue()
      break
    case 'background_worker_status_response':
      if (typeof record.requestId !== 'string') break
      finishPendingRequest(record.requestId, record.snapshot as BackgroundWorkerRuntimeSnapshot | undefined)
      if (record.snapshot && typeof record.snapshot === 'object') {
        lastSnapshot = record.snapshot as BackgroundWorkerRuntimeSnapshot
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

function queueWorkerMessage(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  if (message.type === 'background_worker_usage_records') {
    usageRecordMessageQueue.push(message)
    usageRecordMessageQueueBytes += messageBytes
  } else {
    regularWorkerMessageQueue.push(message)
    regularWorkerMessageQueueBytes += messageBytes
  }
  enforceWorkerMessageQueueLimits()

  flushWorkerMessageQueue()
}

function flushWorkerMessageQueue(): void {
  if (sendingMessage || !workerProcess || !workerReady) {
    return
  }

  const message = shiftWorkerMessage()
  if (!message) {
    return
  }

  sendingMessage = true
  sendingWorkerMessage = message
  try {
    const accepted = workerProcess.send(message, (error) => {
      sendingMessage = false
      sendingWorkerMessage = undefined
      if (error) {
        requeueWorkerMessageFirst(message)
        workerReady = false
        workerPid = workerProcess?.pid ?? workerPid
        return
      }
      flushWorkerMessageQueue()
    })
    if (!accepted) {
      // 由 callback 继续驱动后续发送。
    }
  } catch (error) {
    sendingMessage = false
    sendingWorkerMessage = undefined
    requeueWorkerMessageFirst(message)
    workerReady = false
    workerPid = workerProcess?.pid ?? workerPid
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

function enforceWorkerMessageQueueLimits(): void {
  while (usageRecordMessageQueue.length > maxPendingMessages || usageRecordMessageQueueBytes > maxPendingMessageBytes) {
    const dropped = usageRecordMessageQueue.shift()
    if (!dropped) {
      break
    }
    usageRecordMessageQueueBytes = Math.max(0, usageRecordMessageQueueBytes - estimateWorkerMessageBytes(dropped))
    pendingPendingMessagesWarningCount += 1
    process.stderr.write(`[background-worker] 消息队列已满，已丢弃 ${describeDroppedWorkerMessage(dropped)} ${pendingPendingMessagesWarningCount} 次\n`)
  }

  while (regularWorkerMessageQueue.length > maxPendingMessages || regularWorkerMessageQueueBytes > maxPendingMessageBytes) {
    const dropped = shiftDroppableRegularWorkerMessage()
    if (!dropped) {
      break
    }
    pendingPendingMessagesWarningCount += 1
    process.stderr.write(`[background-worker] 消息队列已满，已丢弃 ${describeDroppedWorkerMessage(dropped)} ${pendingPendingMessagesWarningCount} 次\n`)
  }
}

function shiftDroppableRegularWorkerMessage(): BackgroundWorkerMessage | undefined {
  const droppedSuccessAudit = shiftSuccessAuditWorkerMessage()
  if (droppedSuccessAudit) return droppedSuccessAudit

  const runtimeLogIndex = regularWorkerMessageQueue.findIndex((message) => message.type === 'background_worker_runtime_log_line')
  if (runtimeLogIndex >= 0) {
    return removeRegularWorkerMessageAt(runtimeLogIndex)
  }

  const nonAuditIndex = regularWorkerMessageQueue.findIndex((message) => message.type !== 'background_worker_audit_logs')
  if (nonAuditIndex >= 0) {
    return removeRegularWorkerMessageAt(nonAuditIndex)
  }

  return removeRegularWorkerMessageAt(0)
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

function estimateWorkerMessageBytes(message: BackgroundWorkerMessage): number {
  switch (message.type) {
    case 'background_worker_runtime_log_line':
      return Buffer.byteLength(message.line, 'utf8') + 128
    case 'background_worker_usage_records':
      return message.items.reduce((sum, item) => sum + estimateJsonBytes(item) + 256, 128)
    case 'background_worker_audit_logs':
      return message.items.reduce((sum, item) => sum + estimateAuditLogBytes(item), 128)
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

async function clearServerGatewayRuntimeCache(): Promise<void> {
  const dbServiceIpc = await import('../db-service/db-service-ipc.js')
  const gatewayCache = await import('../gateway/gateway-runtime-cache.service.js')
  gatewayCache.clearGatewayRuntimeCacheLocal()
  dbServiceIpc.clearDbServiceGatewayRuntimeCache()
}
