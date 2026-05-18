import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type {
  DbServiceChildMessage,
  DbServiceOperation,
  DbServiceOperationResult,
  DbServiceParentMessage,
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

interface DbServiceState {
  pid?: number
  ready: boolean
  httpHost?: string
  httpPort?: number
  lastSnapshot?: DbServiceRuntimeSnapshot
  pendingRequestCount: number
  timedOutRequestCount: number
  failedRequestCount: number
  unavailableCircuitOpenUntil?: string
}

const requestTimeoutMs = 1500
const invalidateTimeoutMs = 500
const maxPendingRequests = 1000
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
let lastSnapshot: DbServiceRuntimeSnapshot | undefined
let unavailableCircuitOpenUntilMs = 0

interface PendingServerRuntimeRequest {
  resolve: (snapshot: DbServiceServerRuntimeSnapshot | undefined) => void
  timeout: NodeJS.Timeout
}

interface PendingProcessEventLoopRequest {
  resolve: (sample: ProcessEventLoopSample | undefined) => void
  timeout: NodeJS.Timeout
}

export function attachDbServiceProcess(child: ChildProcess): void {
  dbServiceProcess = child
  dbServicePid = child.pid ?? undefined
  dbServiceHttpHost = undefined
  dbServiceHttpPort = undefined
  dbServiceReady = false

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

  if (!dbServiceProcess || !dbServiceReady || pendingRequests.size >= maxPendingRequests) {
    openUnavailableCircuit()
    throw new Error('DB service 未就绪或请求队列已满')
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
    if (!(error instanceof DbServiceOperationFailedError)) {
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
      process.send?.({ type: 'gateway_runtime_cache_invalidate' } satisfies DbServiceChildMessage)
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
  if (runtimeConfig.processRole !== 'server' || !dbServiceProcess || !dbServiceReady) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<ProcessEventLoopSample | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      pendingProcessEventLoopRequests.delete(requestId)
      resolve(undefined)
    }, timeoutMs)
    pendingProcessEventLoopRequests.set(requestId, { resolve, timeout })
    dbServiceProcess?.send({
      type: 'db_service_process_event_loop_request',
      requestId
    } satisfies DbServiceParentMessage, (error) => {
      if (error) {
        finishProcessEventLoopRequest(requestId, undefined)
      }
    })
  })
}

export function handleDbServiceParentRuntimeMessage(message: unknown): boolean {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return false
  }

  const record = message as Partial<DbServiceParentMessage> & Record<string, unknown>
  if (record.type === 'db_service_process_event_loop_request' && typeof record.requestId === 'string') {
    process.send?.({
      type: 'db_service_process_event_loop_response',
      requestId: record.requestId,
      sample: buildProcessEventLoopSample('db-service')
    } satisfies DbServiceChildMessage)
    return true
  }

  if (record.type !== 'db_service_server_runtime_response' || typeof record.requestId !== 'string') {
    return false
  }

  const pending = pendingServerRuntimeRequests.get(record.requestId)
  if (!pending) {
    return true
  }
  clearTimeout(pending.timeout)
  pendingServerRuntimeRequests.delete(record.requestId)
  pending.resolve(record.ok === true ? record.result as DbServiceServerRuntimeSnapshot : undefined)
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
      dbServicePid = typeof record.pid === 'number' ? record.pid : dbServicePid
      dbServiceHttpHost = typeof record.httpHost === 'string' ? record.httpHost : dbServiceHttpHost
      dbServiceHttpPort = typeof record.httpPort === 'number' ? record.httpPort : dbServiceHttpPort
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
}

function finishProcessEventLoopRequest(requestId: string, sample: ProcessEventLoopSample | undefined): void {
  const pending = pendingProcessEventLoopRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingProcessEventLoopRequests.delete(requestId)
  pending.resolve(sample)
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
    child.send?.({
      type: 'db_service_server_runtime_response',
      requestId,
      ok: true,
      result: snapshot
    } satisfies DbServiceParentMessage)
  } catch (error) {
    child.send?.({
      type: 'db_service_server_runtime_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    } satisfies DbServiceParentMessage)
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
      pendingServerRuntimeRequests.delete(requestId)
      resolve(undefined)
    }, timeoutMs)
    pendingServerRuntimeRequests.set(requestId, { resolve, timeout })
    process.send?.({
      type: 'db_service_server_runtime_request',
      requestId,
      scope
    } satisfies DbServiceChildMessage)
  })
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
      snapshot: workerSnapshot
        ? {
          pid: workerSnapshot.pid,
          ready: workerSnapshot.ready,
          jobs: workerSnapshot.jobs.map((job) => ({ ...job })),
          usageRecordQueue: { ...workerSnapshot.usageRecordQueue },
          operationLogQueue: { ...workerSnapshot.operationLogQueue },
          recordMaintenanceQueue: { ...workerSnapshot.recordMaintenanceQueue },
          auditLogQueue: { ...workerSnapshot.auditLogQueue },
          runtimeLogIndexQueue: { ...workerSnapshot.runtimeLogIndexQueue }
        }
        : undefined
    },
    dbService: {
      pid: dbServiceState.pid,
      ready: dbServiceState.ready,
      pendingRequestCount: dbServiceState.pendingRequestCount,
      timedOutRequestCount,
      failedRequestCount,
      unavailableCircuitOpenUntil: dbServiceState.unavailableCircuitOpenUntil,
      httpHost: dbServiceState.httpHost,
      httpPort: dbServiceState.httpPort
    },
    gatewayAccountSideEffects: { ...gatewaySideEffects.getGatewayAccountSideEffectState() },
    activeAuditCaptureCount: auditCapture.getActiveAuditCaptureCount()
  }
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
  if (operationLogs.length > 0) {
    backgroundIpc.sendOperationLogsToWorker(operationLogs)
  }
}

async function forwardRecordMaintenanceJobsToWorker(items: unknown[]): Promise<void> {
  const backgroundIpc = await import('../background/background-ipc.js')
  const recordMaintenanceQueue = await import('../record-maintenance/record-maintenance-queue.service.js')
  const jobs = items.filter(recordMaintenanceQueue.isRecordMaintenanceJob)
  if (jobs.length > 0) {
    backgroundIpc.sendRecordMaintenanceJobsToWorker(jobs)
  }
}
