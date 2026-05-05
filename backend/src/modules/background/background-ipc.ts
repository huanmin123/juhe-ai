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
}

let workerProcess: ChildProcess | undefined
let workerReady = false
let workerPid: number | undefined
let workerMessageQueue: BackgroundWorkerMessage[] = []
let sendingMessage = false
let sendingWorkerMessage: BackgroundWorkerMessage | undefined
let pendingPendingMessagesWarningCount = 0
let pendingRequests = new Map<string, PendingRequest>()
let lastSnapshot: BackgroundWorkerRuntimeSnapshot | undefined

const maxPendingMessages = 5000

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
        workerMessageQueue.unshift(sendingWorkerMessage)
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
    pendingMessageCount: workerMessageQueue.length
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
  if (workerMessageQueue.length >= maxPendingMessages) {
    workerMessageQueue.shift()
    pendingPendingMessagesWarningCount += 1
    process.stderr.write(`[background-worker] 消息队列已满，已丢弃最早消息 ${pendingPendingMessagesWarningCount} 次\n`)
  }

  workerMessageQueue.push(message)
  flushWorkerMessageQueue()
}

function flushWorkerMessageQueue(): void {
  if (sendingMessage || !workerProcess || !workerReady) {
    return
  }

  const message = workerMessageQueue.shift()
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
        workerMessageQueue.unshift(message)
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
    workerMessageQueue.unshift(message)
    workerReady = false
    workerPid = workerProcess?.pid ?? workerPid
    process.stderr.write(`[background-worker] 向 worker 发送消息失败：${error instanceof Error ? error.message : String(error)}\n`)
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
