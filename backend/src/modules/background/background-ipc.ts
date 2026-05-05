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
  retentionDays?: number
}

export interface BackgroundWorkerRuntimeSnapshot {
  pid: number
  ready: boolean
  processRole: 'worker'
  usageRecordQueue: BackgroundWorkerQueueRuntime
  auditLogQueue: BackgroundWorkerQueueRuntime
  runtimeLogIndexQueue: BackgroundWorkerQueueRuntime
}

type BackgroundWorkerMessage =
  | { type: 'background_worker_ready'; pid: number }
  | { type: 'background_worker_usage_records'; items: UsageRecordInput[] }
  | { type: 'background_worker_audit_logs'; items: AuditLogInput[] }
  | { type: 'background_worker_runtime_log_line'; line: string }
  | { type: 'background_worker_status_request'; requestId: string }
  | { type: 'background_worker_status_response'; requestId: string; snapshot: BackgroundWorkerRuntimeSnapshot }

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

let workerProcess: ChildProcess | undefined
let workerReady = false
let workerPid: number | undefined
let workerMessageQueue: BackgroundWorkerMessage[] = []
let workerMessageQueueBytes = 0
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
  return sendBackgroundWorkerMessage({
    type: 'background_worker_usage_records',
    items
  })
}

export function sendAuditLogsToWorker(items: AuditLogInput[]): boolean {
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
    pendingMessageCount: workerMessageQueue.length,
    pendingMessageBytes: workerMessageQueueBytes
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
    default:
      break
  }
}

function queueWorkerMessage(message: BackgroundWorkerMessage): void {
  const messageBytes = estimateWorkerMessageBytes(message)
  workerMessageQueue.push(message)
  workerMessageQueueBytes += messageBytes
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
  const message = workerMessageQueue.shift()
  if (message) {
    workerMessageQueueBytes = Math.max(0, workerMessageQueueBytes - estimateWorkerMessageBytes(message))
  }
  return message
}

function requeueWorkerMessageFirst(message: BackgroundWorkerMessage): void {
  workerMessageQueue.unshift(message)
  workerMessageQueueBytes += estimateWorkerMessageBytes(message)
  enforceWorkerMessageQueueLimits()
}

function enforceWorkerMessageQueueLimits(): void {
  while (workerMessageQueue.length > maxPendingMessages || workerMessageQueueBytes > maxPendingMessageBytes) {
    const dropped = shiftWorkerMessage()
    if (!dropped) {
      break
    }
    pendingPendingMessagesWarningCount += 1
    process.stderr.write(`[background-worker] 消息队列已满，已丢弃最早消息 ${pendingPendingMessagesWarningCount} 次\n`)
  }
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
  try {
    return Buffer.byteLength(JSON.stringify(value) ?? '', 'utf8')
  } catch {
    return 1024
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

function failPendingRequests(): void {
  for (const [requestId, pending] of pendingRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(lastSnapshot)
    pendingRequests.delete(requestId)
  }
}
