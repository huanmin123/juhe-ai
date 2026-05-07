import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import type {
  DbServiceChildMessage,
  DbServiceOperation,
  DbServiceOperationResult,
  DbServiceParentMessage,
  DbServiceRuntimeSnapshot
} from './db-service-types.js'

interface PendingRequest {
  resolve: (value: unknown) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
}

interface DbServiceState {
  pid?: number
  ready: boolean
  lastSnapshot?: DbServiceRuntimeSnapshot
  pendingRequestCount: number
  timedOutRequestCount: number
  failedRequestCount: number
}

const requestTimeoutMs = 1500
const invalidateTimeoutMs = 500
const maxPendingRequests = 1000

let dbServiceProcess: ChildProcess | undefined
let dbServiceReady = false
let dbServicePid: number | undefined
let pendingRequests = new Map<string, PendingRequest>()
let timedOutRequestCount = 0
let failedRequestCount = 0
let lastSnapshot: DbServiceRuntimeSnapshot | undefined

export function attachDbServiceProcess(child: ChildProcess): void {
  dbServiceProcess = child
  dbServicePid = child.pid ?? undefined
  dbServiceReady = false

  child.removeAllListeners('message')
  child.on('message', handleDbServiceMessage)
  child.once('exit', () => {
    if (dbServiceProcess === child) {
      dbServiceProcess = undefined
      dbServiceReady = false
      dbServicePid = undefined
      failPendingRequests(new Error('DB service 已退出'))
    }
  })
}

export async function requestDbService<T extends DbServiceOperation>(
  operation: T,
  options: { timeoutMs?: number; fallbackToLocal?: boolean } = {}
): Promise<DbServiceOperationResult<T>> {
  if (runtimeConfig.processRole === 'db-service') {
    return await runLocalDbServiceOperation(operation)
  }

  if (!dbServiceProcess || !dbServiceReady || pendingRequests.size >= maxPendingRequests) {
    return await fallbackDbServiceOperation(operation, options.fallbackToLocal !== false, 'DB service 未就绪或请求队列已满')
  }

  const requestId = randomUUID()
  const message: DbServiceParentMessage = {
    type: 'db_service_request',
    requestId,
    operation
  }
  try {
    return await new Promise<DbServiceOperationResult<T>>((resolve, reject) => {
      const timeout = setTimeout(() => {
        pendingRequests.delete(requestId)
        timedOutRequestCount += 1
        reject(new Error('DB service 请求超时'))
      }, options.timeoutMs ?? requestTimeoutMs)
      pendingRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout })
      dbServiceProcess?.send(message, (error) => {
        if (!error) {
          return
        }
        const pending = pendingRequests.get(requestId)
        if (!pending) {
          return
        }
        clearTimeout(pending.timeout)
        pendingRequests.delete(requestId)
        pending.reject(error)
      })
    })
  } catch (error) {
    failedRequestCount += 1
    return await fallbackDbServiceOperation(operation, options.fallbackToLocal !== false, error)
  }
}

export function clearDbServiceGatewayRuntimeCache(): void {
  void requestDbService(
    { type: 'clear_gateway_runtime_cache' },
    { timeoutMs: invalidateTimeoutMs, fallbackToLocal: false }
  ).catch((error) => {
    logger.warn(errorLogFields(error, {
      event: 'db_service_cache_invalidation_failed'
    }), 'DB service 缓存失效通知失败')
  })
}

export function getDbServiceState(): DbServiceState {
  return {
    pid: dbServicePid,
    ready: dbServiceReady,
    lastSnapshot,
    pendingRequestCount: pendingRequests.size,
    timedOutRequestCount,
    failedRequestCount
  }
}

function handleDbServiceMessage(message: unknown): void {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return
  }

  const record = message as Partial<DbServiceChildMessage> & Record<string, unknown>
  switch (record.type) {
    case 'db_service_ready':
      dbServiceReady = true
      dbServicePid = typeof record.pid === 'number' ? record.pid : dbServicePid
      break
    case 'db_service_response':
      if (typeof record.requestId !== 'string') break
      finishPendingRequest(record.requestId, record)
      break
    default:
      break
  }
}

function finishPendingRequest(requestId: string, response: Partial<DbServiceChildMessage> & Record<string, unknown>): void {
  const pending = pendingRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingRequests.delete(requestId)

  if (response.ok === true) {
    if (isRuntimeSnapshot(response.result)) {
      lastSnapshot = response.result
    }
    pending.resolve(response.result)
    return
  }

  pending.reject(new Error(typeof response.errorMessage === 'string' ? response.errorMessage : 'DB service 请求失败'))
}

function failPendingRequests(error: Error): void {
  for (const [requestId, pending] of pendingRequests) {
    clearTimeout(pending.timeout)
    pending.reject(error)
    pendingRequests.delete(requestId)
  }
}

async function fallbackDbServiceOperation<T extends DbServiceOperation>(
  operation: T,
  fallbackToLocal: boolean,
  reason: unknown
): Promise<DbServiceOperationResult<T>> {
  if (!fallbackToLocal) {
    throw reason instanceof Error ? reason : new Error(String(reason))
  }
  if (operation.type !== 'status') {
    logger.warn(errorLogFields(reason, {
      event: 'db_service_fallback_to_local',
      operationType: operation.type
    }), 'DB service 不可用，临时降级到主进程本地读取')
  }
  return await runLocalDbServiceOperation(operation)
}

function isRuntimeSnapshot(value: unknown): value is DbServiceRuntimeSnapshot {
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && (value as Record<string, unknown>).processRole === 'db-service'
}

async function runLocalDbServiceOperation<T extends DbServiceOperation>(operation: T): Promise<DbServiceOperationResult<T>> {
  const { handleDbServiceOperation } = await import('./db-service-handlers.js')
  return await handleDbServiceOperation(operation)
}
