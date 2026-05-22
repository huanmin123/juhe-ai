import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { BackgroundWorkerIpcQueuesRuntime } from '../background/background-ipc.js'
import type {
  DbServiceChildMessage,
  DbServiceOperation,
  DbServiceOperationResult,
  DbServiceParentMessage,
  DbServiceRuntimeQueueSnapshot,
  DbServiceRuntimeSnapshot,
  DbServiceServerRuntimeSnapshot,
  DbServiceServerRuntimeSnapshotScope
} from './db-service-types.js'

interface PendingRequest {
  resolve: (value: unknown) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
}

class DbServiceOperationFailedError extends Error {
}

class DbServiceRequestTimedOutError extends Error {
}

interface DbServiceState {
  pid?: number
  ready: boolean
  httpHost?: string
  httpPort?: number
  lastSnapshot?: DbServiceRuntimeSnapshot
  pendingRequestCount: number
  timedOutRequestCount: number
  failedRequestCount: number
  pendingProcessEventLoopRequestCount: number
  timedOutProcessEventLoopRequestCount: number
  failedProcessEventLoopRequestCount: number
  processEventLoopTimeoutStreak: number
  pendingServerRuntimeRequestCount: number
  timedOutServerRuntimeRequestCount: number
  failedServerRuntimeRequestCount: number
  unavailableCircuitOpenUntil?: string
}

const requestTimeoutMs = 5000
const invalidateTimeoutMs = 500
const maxPendingRequests = 10000
const unavailableCircuitOpenMs = 3000

let dbServiceProcess: ChildProcess | undefined
let dbServiceReady = false
let dbServicePid: number | undefined
let dbServiceHttpHost: string | undefined
let dbServiceHttpPort: number | undefined
let pendingRequests = new Map<string, PendingRequest>()
let pendingServerRuntimeRequests = new Map<string, PendingServerRuntimeRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let timedOutRequestCount = 0
let failedRequestCount = 0
let timedOutProcessEventLoopRequestCount = 0
let failedProcessEventLoopRequestCount = 0
let processEventLoopTimeoutStreak = 0
let timedOutServerRuntimeRequestCount = 0
let failedServerRuntimeRequestCount = 0
let lastSnapshot: DbServiceRuntimeSnapshot | undefined
let unavailableCircuitOpenUntilMs = 0
let dbServiceReadyHandler: (() => void) | undefined

interface PendingServerRuntimeRequest {
  resolve: (snapshot: DbServiceServerRuntimeSnapshot | undefined) => void
  timeout: NodeJS.Timeout
}

interface PendingProcessEventLoopRequest {
  resolve: (sample: ProcessEventLoopSample | undefined) => void
  timeout: NodeJS.Timeout
}

export function attachDbServiceProcess(child: ChildProcess, options: { onReady?: () => void } = {}): void {
  dbServiceProcess = child
  dbServicePid = child.pid ?? undefined
  dbServiceHttpHost = undefined
  dbServiceHttpPort = undefined
  dbServiceReady = false
  processEventLoopTimeoutStreak = 0
  dbServiceReadyHandler = options.onReady

  child.removeAllListeners('message')
  child.on('message', handleDbServiceMessage)
  child.once('exit', () => {
    if (dbServiceProcess === child) {
      dbServiceProcess = undefined
      dbServiceReady = false
      dbServicePid = undefined
      dbServiceHttpHost = undefined
      dbServiceHttpPort = undefined
      failPendingRequests(new Error('DB service 已退出'))
    }
  })
}

export async function requestDbService<T extends DbServiceOperation>(
  operation: T,
  options: { timeoutMs?: number } = {}
): Promise<DbServiceOperationResult<T>> {
  if (runtimeConfig.processRole !== 'server') {
    return await runLocalDbServiceOperation(operation)
  }

  if (unavailableCircuitOpenUntilMs > Date.now()) {
    throw new Error('DB service 暂时不可用，请稍后重试')
  }

  const child = dbServiceProcess
  if (!child || !child.connected || !dbServiceReady) {
    openUnavailableCircuit()
    if (child && !child.connected) {
      markDbServiceIpcBroken(new Error('DB service IPC 已断开'), child)
    }
    throw new Error('DB service 未就绪')
  }
  if (pendingRequests.size >= maxPendingRequests) {
    failedRequestCount += 1
    throw new Error('DB service 请求队列已满')
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
        const pending = pendingRequests.get(requestId)
        if (!pending) {
          return
        }
        timedOutRequestCount += 1
        pendingRequests.delete(requestId)
        const timeoutError = new DbServiceRequestTimedOutError('DB service 请求超时')
        pending.reject(timeoutError)
      }, options.timeoutMs ?? requestTimeoutMs)
      pendingRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout })
      const failSend = (error: unknown): void => {
        const pending = pendingRequests.get(requestId)
        if (!pending) {
          return
        }
        clearTimeout(pending.timeout)
        pendingRequests.delete(requestId)
        markDbServiceIpcBroken(error, child)
        pending.reject(error instanceof Error ? error : new Error(String(error)))
      }
      try {
        child.send(message, (error) => {
          if (error) {
            failSend(error)
          }
        })
      } catch (error) {
        failSend(error)
      }
    })
  } catch (error) {
    failedRequestCount += 1
    if (!(error instanceof DbServiceOperationFailedError) && !(error instanceof DbServiceRequestTimedOutError)) {
      openUnavailableCircuit()
    }
    throw error
  }
}

export function clearDbServiceGatewayRuntimeCache(): void {
  if (runtimeConfig.processRole === 'db-service') {
    void requestDbService(
      { type: 'clear_gateway_runtime_cache' },
      { timeoutMs: invalidateTimeoutMs }
    ).catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'db_service_local_cache_invalidation_failed'
      }), 'DB service 本地缓存失效失败')
    }).finally(() => {
      sendDbServiceChildMessage({ type: 'gateway_runtime_cache_invalidate' })
    })
    return
  }

  void requestDbService(
    { type: 'clear_gateway_runtime_cache' },
    { timeoutMs: invalidateTimeoutMs }
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
    httpHost: dbServiceHttpHost,
    httpPort: dbServiceHttpPort,
    lastSnapshot,
    pendingRequestCount: pendingRequests.size,
    timedOutRequestCount,
    failedRequestCount,
    pendingProcessEventLoopRequestCount: pendingProcessEventLoopRequests.size,
    timedOutProcessEventLoopRequestCount,
    failedProcessEventLoopRequestCount,
    processEventLoopTimeoutStreak,
    pendingServerRuntimeRequestCount: pendingServerRuntimeRequests.size,
    timedOutServerRuntimeRequestCount,
    failedServerRuntimeRequestCount,
    unavailableCircuitOpenUntil: unavailableCircuitOpenUntilMs > Date.now() ? new Date(unavailableCircuitOpenUntilMs).toISOString() : undefined
  }
}

export async function requestServerRuntimeSnapshot(timeoutMs = 1000): Promise<DbServiceServerRuntimeSnapshot | undefined> {
  return await requestServerRuntimeSnapshotByScope('full', timeoutMs)
}

export async function requestServerAccountConcurrencySnapshot(timeoutMs = 300): Promise<Record<string, number> | undefined> {
  const snapshot = await requestServerRuntimeSnapshotByScope('account_concurrency', timeoutMs)
  return snapshot?.accountConcurrency
}

export async function requestDbServiceProcessEventLoopSample(timeoutMs = 800): Promise<ProcessEventLoopSample | undefined> {
  const child = dbServiceProcess
  if (runtimeConfig.processRole !== 'server' || !child || !child.connected || !dbServiceReady) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<ProcessEventLoopSample | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      const pending = pendingProcessEventLoopRequests.get(requestId)
      if (!pending) {
        return
      }
      timedOutProcessEventLoopRequestCount += 1
      if (dbServiceProcess === child) {
        processEventLoopTimeoutStreak += 1
        logger.warn({
          event: 'db_service_process_event_loop_sample_timeout',
          pid: child.pid,
          timeoutMs,
          processEventLoopTimeoutStreak
        }, 'DB service 事件循环采样超时')
      }
      finishProcessEventLoopRequest(requestId, undefined)
    }, timeoutMs)
    pendingProcessEventLoopRequests.set(requestId, { resolve, timeout })
    try {
      child.send({
        type: 'db_service_process_event_loop_request',
        requestId
      } satisfies DbServiceParentMessage, (error) => {
        if (error) {
          failedProcessEventLoopRequestCount += 1
          markDbServiceIpcBroken(error, child)
          finishProcessEventLoopRequest(requestId, undefined)
        }
      })
    } catch (error) {
      failedProcessEventLoopRequestCount += 1
      markDbServiceIpcBroken(error, child)
      finishProcessEventLoopRequest(requestId, undefined)
    }
  })
}

export function handleDbServiceParentRuntimeMessage(message: unknown): boolean {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return false
  }

  const record = message as Partial<DbServiceParentMessage> & Record<string, unknown>
  if (record.type === 'db_service_process_event_loop_request' && typeof record.requestId === 'string') {
    const response: DbServiceChildMessage = {
      type: 'db_service_process_event_loop_response',
      requestId: record.requestId,
      sample: buildProcessEventLoopSample('db-service')
    }
    sendDbServiceChildMessage(response, () => exitDbServiceAfterChildIpcFailure(response.type))
    return true
  }

  if (record.type !== 'db_service_server_runtime_response' || typeof record.requestId !== 'string') {
    return false
  }

  const pending = pendingServerRuntimeRequests.get(record.requestId)
  if (!pending) {
    return true
  }
  finishServerRuntimeRequest(record.requestId, record.ok === true ? record.result as DbServiceServerRuntimeSnapshot : undefined)
  return true
}

function handleDbServiceMessage(message: unknown): void {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return
  }

  const record = message as Partial<DbServiceChildMessage> & Record<string, unknown>
  switch (record.type) {
    case 'db_service_ready':
      dbServiceReady = true
      unavailableCircuitOpenUntilMs = 0
      processEventLoopTimeoutStreak = 0
      dbServicePid = typeof record.pid === 'number' ? record.pid : dbServicePid
      dbServiceHttpHost = typeof record.httpHost === 'string' ? record.httpHost : dbServiceHttpHost
      dbServiceHttpPort = typeof record.httpPort === 'number' ? record.httpPort : dbServiceHttpPort
      dbServiceReadyHandler?.()
      lastSnapshot = {
        pid: dbServicePid ?? 0,
        ready: true,
        processRole: 'db-service',
        httpHost: dbServiceHttpHost,
        httpPort: dbServiceHttpPort,
        pendingRequestCount: pendingRequests.size,
        handledRequestCount: lastSnapshot?.handledRequestCount ?? 0,
        failedRequestCount: lastSnapshot?.failedRequestCount ?? 0,
        lastRequestAt: lastSnapshot?.lastRequestAt,
        lastError: lastSnapshot?.lastError
      }
      break
    case 'db_service_response':
      if (typeof record.requestId !== 'string') break
      finishPendingRequest(record.requestId, record)
      break
    case 'db_service_process_event_loop_response':
      if (typeof record.requestId !== 'string') break
      finishProcessEventLoopRequest(record.requestId, record.sample as ProcessEventLoopSample | undefined)
      break
    case 'db_service_server_runtime_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string') {
        void respondToServerRuntimeRequest(
          record.requestId,
          record.scope === 'account_concurrency' ? 'account_concurrency' : 'full'
        )
      }
      break
    case 'gateway_runtime_cache_invalidate':
      if (runtimeConfig.processRole === 'server') {
        void clearServerGatewayRuntimeCache()
      }
      break
    case 'background_worker_operation_logs':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.items)) {
        void forwardOperationLogsToWorker(record.items)
      }
      break
    case 'background_worker_record_maintenance':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.items)) {
        void forwardRecordMaintenanceJobsToWorker(record.items)
      }
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

  pending.reject(new DbServiceOperationFailedError(typeof response.errorMessage === 'string' ? response.errorMessage : 'DB service 请求失败'))
}

function failPendingRequests(error: Error): void {
  for (const [requestId, pending] of pendingRequests) {
    clearTimeout(pending.timeout)
    pending.reject(error)
    pendingRequests.delete(requestId)
  }
  for (const [requestId, pending] of pendingProcessEventLoopRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingProcessEventLoopRequests.delete(requestId)
  }
  for (const [requestId, pending] of pendingServerRuntimeRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingServerRuntimeRequests.delete(requestId)
  }
}

function finishProcessEventLoopRequest(requestId: string, sample: ProcessEventLoopSample | undefined): void {
  const pending = pendingProcessEventLoopRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingProcessEventLoopRequests.delete(requestId)
  if (sample) {
    processEventLoopTimeoutStreak = 0
  }
  pending.resolve(sample)
}

function markDbServiceIpcBroken(error: unknown, child = dbServiceProcess): void {
  if (child && dbServiceProcess === child) {
    dbServiceReady = false
    dbServicePid = child.pid ?? dbServicePid
  }
  failPendingRequests(error instanceof Error ? error : new Error(String(error)))
  if (child && !child.killed) {
    try {
      child.kill('SIGTERM')
    } catch (killError) {
      logger.warn(errorLogFields(killError, {
        event: 'db_service_ipc_broken_kill_failed',
        pid: child.pid
      }), '终止 IPC 异常 DB service 失败')
    }
  }
}

function sendDbServiceChildMessage(message: DbServiceChildMessage, onFailure?: () => void): void {
  if (!process.send) {
    onFailure?.()
    return
  }
  try {
    process.send(message, (error) => {
      if (error) {
        logger.warn(errorLogFields(error, {
          event: 'db_service_child_ipc_send_failed',
          messageType: message.type
        }), 'DB service 向父进程发送 IPC 消息失败')
        onFailure?.()
      }
    })
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'db_service_child_ipc_send_failed',
      messageType: message.type
    }), 'DB service 向父进程发送 IPC 消息失败')
    onFailure?.()
  }
}

function exitDbServiceAfterChildIpcFailure(messageType: string): void {
  logger.error({
    event: 'db_service_child_ipc_unavailable',
    messageType
  }, 'DB service 向父进程发送 IPC 消息失败，进程将退出等待 supervisor 重启')
  process.exit(1)
}

function openUnavailableCircuit(): void {
  unavailableCircuitOpenUntilMs = Math.max(unavailableCircuitOpenUntilMs, Date.now() + unavailableCircuitOpenMs)
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

async function respondToServerRuntimeRequest(requestId: string, scope: DbServiceServerRuntimeSnapshotScope = 'full'): Promise<void> {
  const child = dbServiceProcess
  if (!child) {
    return
  }

  try {
    const snapshot = scope === 'account_concurrency'
      ? await buildServerAccountConcurrencySnapshot()
      : await buildServerRuntimeSnapshot()
    sendToDbServiceProcess(child, {
      type: 'db_service_server_runtime_response',
      requestId,
      ok: true,
      result: snapshot
    })
  } catch (error) {
    sendToDbServiceProcess(child, {
      type: 'db_service_server_runtime_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

async function requestServerRuntimeSnapshotByScope(
  scope: DbServiceServerRuntimeSnapshotScope,
  timeoutMs: number
): Promise<DbServiceServerRuntimeSnapshot | undefined> {
  if (runtimeConfig.processRole !== 'db-service' || !process.send) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<DbServiceServerRuntimeSnapshot | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      const pending = pendingServerRuntimeRequests.get(requestId)
      if (!pending) {
        return
      }
      timedOutServerRuntimeRequestCount += 1
      pendingServerRuntimeRequests.delete(requestId)
      pending.resolve(undefined)
    }, timeoutMs)
    pendingServerRuntimeRequests.set(requestId, { resolve, timeout })
    sendDbServiceChildMessage({
      type: 'db_service_server_runtime_request',
      requestId,
      scope
    }, () => {
      failedServerRuntimeRequestCount += 1
      finishServerRuntimeRequest(requestId, undefined)
    })
  })
}

function finishServerRuntimeRequest(requestId: string, snapshot: DbServiceServerRuntimeSnapshot | undefined): void {
  const pending = pendingServerRuntimeRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingServerRuntimeRequests.delete(requestId)
  pending.resolve(snapshot)
}

function sendToDbServiceProcess(child: ChildProcess, message: DbServiceParentMessage): void {
  if (!child.connected) {
    markDbServiceIpcBroken(new Error('DB service IPC 已断开'), child)
    return
  }
  try {
    child.send(message, (error) => {
      if (error) {
        markDbServiceIpcBroken(error, child)
      }
    })
  } catch (error) {
    markDbServiceIpcBroken(error, child)
  }
}

async function buildServerRuntimeSnapshot(): Promise<DbServiceServerRuntimeSnapshot> {
  const [
    backgroundIpc,
    gatewaySideEffects,
    auditCapture,
    accountConcurrency
  ] = await Promise.all([
    import('../background/background-ipc.js'),
    import('../gateway/gateway-account-side-effects.service.js'),
    import('../gateway/audit-capture.service.js'),
    import('../../shared/account-concurrency.js')
  ])
  const workerSnapshot = await backgroundIpc.requestBackgroundWorkerSnapshot(1000).catch(() => undefined)
  const workerState = backgroundIpc.getBackgroundWorkerState()
  const dbServiceState = getDbServiceState()

  return {
    accountConcurrency: accountConcurrency.snapshotAccountConcurrency(),
    worker: {
      pid: workerSnapshot?.pid ?? workerState.pid,
      ready: workerSnapshot?.ready ?? workerState.ready,
      pendingMessageCount: workerState.pendingMessageCount,
      pendingMessageBytes: workerState.pendingMessageBytes,
      pendingQueues: backgroundPendingQueuesSnapshot(workerState.pendingQueues),
      pendingSnapshotRequestCount: workerState.pendingSnapshotRequestCount,
      timedOutSnapshotRequestCount: workerState.timedOutSnapshotRequestCount,
      rejectedSnapshotRequestCount: workerState.rejectedSnapshotRequestCount,
      pendingProcessEventLoopRequestCount: workerState.pendingProcessEventLoopRequestCount,
      timedOutProcessEventLoopRequestCount: workerState.timedOutProcessEventLoopRequestCount,
      failedProcessEventLoopRequestCount: workerState.failedProcessEventLoopRequestCount,
      snapshot: workerSnapshot
        ? {
          pid: workerSnapshot.pid,
          ready: workerSnapshot.ready,
          jobs: workerSnapshot.jobs.map((job) => ({ ...job })),
          usageRecordQueue: { ...workerSnapshot.usageRecordQueue },
          operationLogQueue: { ...workerSnapshot.operationLogQueue },
          recordMaintenanceQueue: { ...workerSnapshot.recordMaintenanceQueue },
          auditLogQueue: { ...workerSnapshot.auditLogQueue },
          runtimeLogIndexQueue: { ...workerSnapshot.runtimeLogIndexQueue },
          cooldownAccountRetestQueue: workerSnapshot.cooldownAccountRetestQueue
            ? { ...workerSnapshot.cooldownAccountRetestQueue }
            : undefined
        }
        : undefined
    },
    dbService: {
      pid: dbServiceState.pid,
      ready: dbServiceState.ready,
      pendingRequestCount: dbServiceState.pendingRequestCount,
      timedOutRequestCount,
      failedRequestCount,
      pendingProcessEventLoopRequestCount: dbServiceState.pendingProcessEventLoopRequestCount,
      timedOutProcessEventLoopRequestCount: dbServiceState.timedOutProcessEventLoopRequestCount,
      failedProcessEventLoopRequestCount: dbServiceState.failedProcessEventLoopRequestCount,
      processEventLoopTimeoutStreak: dbServiceState.processEventLoopTimeoutStreak,
      pendingServerRuntimeRequestCount: dbServiceState.pendingServerRuntimeRequestCount,
      timedOutServerRuntimeRequestCount: dbServiceState.timedOutServerRuntimeRequestCount,
      failedServerRuntimeRequestCount: dbServiceState.failedServerRuntimeRequestCount,
      unavailableCircuitOpenUntil: dbServiceState.unavailableCircuitOpenUntil,
      httpHost: dbServiceState.httpHost,
      httpPort: dbServiceState.httpPort
    },
    gatewayAccountSideEffects: { ...gatewaySideEffects.getGatewayAccountSideEffectState() },
    activeAuditCaptureCount: auditCapture.getActiveAuditCaptureCount()
  }
}

function backgroundPendingQueuesSnapshot(queues: BackgroundWorkerIpcQueuesRuntime): Record<string, DbServiceRuntimeQueueSnapshot> {
  return Object.fromEntries(Object.entries(queues).map(([key, value]) => [key, { ...value }])) as Record<string, DbServiceRuntimeQueueSnapshot>
}

async function buildServerAccountConcurrencySnapshot(): Promise<DbServiceServerRuntimeSnapshot> {
  const accountConcurrency = await import('../../shared/account-concurrency.js')
  return {
    accountConcurrency: accountConcurrency.snapshotAccountConcurrency()
  }
}

async function clearServerGatewayRuntimeCache(): Promise<void> {
  const gatewayCache = await import('../gateway/gateway-runtime-cache.service.js')
  gatewayCache.clearGatewayRuntimeCacheLocal()
}

async function forwardOperationLogsToWorker(items: unknown[]): Promise<void> {
  const backgroundIpc = await import('../background/background-ipc.js')
  const operationLogQueue = await import('../operation-logs/operation-log-queue.service.js')
  const operationLogs = items.filter(operationLogQueue.isOperationLogInput)
  if (operationLogs.length > 0 && !backgroundIpc.sendOperationLogsToWorker(operationLogs)) {
    logger.warn({
      event: 'db_service_operation_logs_forward_failed',
      itemCount: operationLogs.length
    }, 'DB service 转发操作日志到后台 worker 失败')
  }
}

async function forwardRecordMaintenanceJobsToWorker(items: unknown[]): Promise<void> {
  const backgroundIpc = await import('../background/background-ipc.js')
  const recordMaintenanceQueue = await import('../record-maintenance/record-maintenance-queue.service.js')
  const jobs = items.filter(recordMaintenanceQueue.isRecordMaintenanceJob)
  if (jobs.length > 0 && !backgroundIpc.sendRecordMaintenanceJobsToWorker(jobs)) {
    logger.warn({
      event: 'db_service_record_maintenance_forward_failed',
      itemCount: jobs.length
    }, 'DB service 转发数据维护任务到后台 worker 失败')
  }
}
