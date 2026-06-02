import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig, type AuditFullBodyCaptureRuntimeConfig } from '../../config/runtime.js'
import { registerAuthorizationQuotaCacheInvalidator } from '../../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import { normalizeAuditFullBodyCaptureConfig, readAuditFullBodyCaptureConfig, setAuditLogFullBodyCaptureConfig, type AuditFullBodyCaptureConfigInput } from '../audit-logs/audit-log-settings.js'
import type { BackgroundWorkerIpcQueuesRuntime } from '../background/background-ipc.js'
import { invalidateGatewayAuthorizationQuotaSnapshot } from '../gateway/gateway-quota-snapshot-cache.service.js'
import type {
  AccountRuntimeAvailabilityClearResult,
  AccountRuntimeAvailabilityClearTarget,
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

class DbServiceRequestQueueFullError extends Error {
}

interface DbServiceState {
  pid?: number
  ready: boolean
  httpHost?: string
  httpPort?: number
  lastSnapshot?: DbServiceRuntimeSnapshot
  pendingRequestCount: number
  timedOutRequestCount: number
  rejectedRequestCount: number
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
const unavailableCircuitOpenMs = 3000
const maxPendingRequests = 2000

let dbServiceProcess: ChildProcess | undefined
let dbServiceReady = false
let dbServicePid: number | undefined
let dbServiceHttpHost: string | undefined
let dbServiceHttpPort: number | undefined
let pendingRequests = new Map<string, PendingRequest>()
let pendingServerRuntimeRequests = new Map<string, PendingServerRuntimeRequest>()
let pendingServerAuditFullBodyCaptureUpdateRequests = new Map<string, PendingServerAuditFullBodyCaptureUpdateRequest>()
let pendingServerAccountRuntimeClearRequests = new Map<string, PendingServerAccountRuntimeClearRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let timedOutRequestCount = 0
let rejectedRequestCount = 0
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

interface PendingServerAuditFullBodyCaptureUpdateRequest {
  resolve: (result: AuditFullBodyCaptureUpdateResult | undefined) => void
  timeout: NodeJS.Timeout
}

interface AuditFullBodyCaptureUpdateResult {
  fullBodyCaptureEnabled: boolean
  fullBodyCapture: AuditFullBodyCaptureRuntimeConfig
}

interface PendingServerAccountRuntimeClearRequest {
  resolve: (result: AccountRuntimeAvailabilityClearResult | undefined) => void
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
      failPendingRequests(new Error('本地数据库服务已退出'))
    }
  })
}

export async function requestDbService<T extends DbServiceOperation>(
  operation: T,
  options: { timeoutMs?: number } = {}
): Promise<DbServiceOperationResult<T>> {
  if (runtimeConfig.processRole === 'db-service') {
    return await runLocalDbServiceOperation(operation)
  }
  if (runtimeConfig.processRole !== 'server') {
    throw new Error(`${runtimeConfig.processRole} 角色不能本地执行 DB service 操作：${operation.type}`)
  }

  if (unavailableCircuitOpenUntilMs > Date.now()) {
    throw new Error('本地数据库服务暂时不可用，请稍后重试')
  }

  const child = dbServiceProcess
  if (!child || !child.connected || !dbServiceReady) {
    openUnavailableCircuit()
    if (child && !child.connected) {
      markDbServiceIpcBroken(new Error('本地数据库服务通信已断开'), child)
    }
    throw new Error('本地数据库服务未就绪，请稍后重试')
  }
  if (pendingRequests.size >= maxPendingRequests) {
    rejectedRequestCount += 1
    throw new DbServiceRequestQueueFullError('本地数据库服务请求队列已满，请稍后重试')
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
        const timeoutError = new DbServiceRequestTimedOutError('本地数据库服务请求超时，请稍后重试')
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
    if (!(error instanceof DbServiceOperationFailedError)
      && !(error instanceof DbServiceRequestTimedOutError)
      && !(error instanceof DbServiceRequestQueueFullError)) {
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
    rejectedRequestCount,
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

export function notifyClientIpPolicyCacheInvalidated(): void {
  if (runtimeConfig.processRole === 'server') {
    void clearServerClientIpPolicyCache()
    return
  }
  if (runtimeConfig.processRole === 'db-service' && typeof process.send === 'function') {
    sendDbServiceChildMessage({ type: 'client_ip_policy_cache_invalidate' })
  }
}

function notifyServerAuthorizationQuotaCacheInvalidated(): void {
  if (runtimeConfig.processRole === 'server') {
    invalidateGatewayAuthorizationQuotaSnapshot()
    void clearServerAuthorizationQuotaRuntimeCache()
    return
  }
  if (runtimeConfig.processRole === 'db-service' && typeof process.send === 'function') {
    sendDbServiceChildMessage({ type: 'authorization_quota_cache_invalidate' })
  }
}

export async function requestServerAccountRuntimeSnapshot(timeoutMs = 300): Promise<Pick<DbServiceServerRuntimeSnapshot, 'accountConcurrency' | 'accountRuntimeAvailability'> | undefined> {
  const snapshot = await requestServerRuntimeSnapshotByScope('account_runtime', timeoutMs)
  return snapshot
    ? {
        accountConcurrency: snapshot.accountConcurrency,
        accountRuntimeAvailability: snapshot.accountRuntimeAvailability
      }
    : undefined
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

export async function updateServerAuditFullBodyCaptureEnabled(
  enabled: boolean,
  timeoutMs = 1000
): Promise<AuditFullBodyCaptureUpdateResult | undefined> {
  return updateServerAuditFullBodyCaptureConfig({
    enabled,
    scope: 'global',
    includeSuccess: false
  }, timeoutMs)
}

export async function updateServerAuditFullBodyCaptureConfig(
  config: AuditFullBodyCaptureConfigInput,
  timeoutMs = 1000
): Promise<AuditFullBodyCaptureUpdateResult | undefined> {
  const normalizedConfig = normalizeAuditFullBodyCaptureConfig(config)
  if (runtimeConfig.processRole !== 'db-service' || !process.send) {
    const settings = setAuditLogFullBodyCaptureConfig(normalizedConfig)
    return { fullBodyCaptureEnabled: settings.fullBodyCaptureEnabled, fullBodyCapture: settings.fullBodyCapture }
  }

  const requestId = randomUUID()
  return await new Promise<AuditFullBodyCaptureUpdateResult | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      const pending = pendingServerAuditFullBodyCaptureUpdateRequests.get(requestId)
      if (!pending) {
        return
      }
      pendingServerAuditFullBodyCaptureUpdateRequests.delete(requestId)
      pending.resolve(undefined)
    }, timeoutMs)
    pendingServerAuditFullBodyCaptureUpdateRequests.set(requestId, { resolve, timeout })
    sendDbServiceChildMessage({
      type: 'db_service_server_audit_full_body_capture_update_request',
      requestId,
      config: normalizedConfig
    }, () => {
      finishServerAuditFullBodyCaptureUpdateRequest(requestId, undefined)
    })
  })
}

export async function clearServerAccountRuntimeAvailability(
  target: AccountRuntimeAvailabilityClearTarget,
  timeoutMs = 1000
): Promise<AccountRuntimeAvailabilityClearResult | undefined> {
  const normalizedTarget = normalizeAccountRuntimeClearTarget(target)
  if (!normalizedTarget) {
    return undefined
  }
  if (runtimeConfig.processRole !== 'db-service' || !process.send) {
    const gatewaySideEffects = await import('../gateway/gateway-account-side-effects.service.js')
    return gatewaySideEffects.clearGatewayAccountRuntimeAvailability(normalizedTarget)
  }

  const requestId = randomUUID()
  return await new Promise<AccountRuntimeAvailabilityClearResult | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      const pending = pendingServerAccountRuntimeClearRequests.get(requestId)
      if (!pending) {
        return
      }
      pendingServerAccountRuntimeClearRequests.delete(requestId)
      pending.resolve(undefined)
    }, timeoutMs)
    pendingServerAccountRuntimeClearRequests.set(requestId, { resolve, timeout })
    sendDbServiceChildMessage({
      type: 'db_service_server_account_runtime_clear_request',
      requestId,
      target: normalizedTarget
    }, () => {
      finishServerAccountRuntimeClearRequest(requestId, undefined)
    })
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

  if (record.type === 'db_service_server_audit_full_body_capture_update_response' && typeof record.requestId === 'string') {
    const result = record.ok === true && isAuditFullBodyCaptureUpdateResult(record.result)
      ? record.result
      : undefined
    if (result) {
      setAuditLogFullBodyCaptureConfig(result.fullBodyCapture)
    }
    finishServerAuditFullBodyCaptureUpdateRequest(record.requestId, result)
    return true
  }

  if (record.type === 'db_service_server_account_runtime_clear_response' && typeof record.requestId === 'string') {
    finishServerAccountRuntimeClearRequest(
      record.requestId,
      record.ok === true ? record.result as AccountRuntimeAvailabilityClearResult : undefined
    )
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
          record.scope === 'account_concurrency' || record.scope === 'account_runtime' ? record.scope : 'full'
        )
      }
      break
    case 'db_service_server_audit_full_body_capture_update_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && isAuditFullBodyCaptureConfig(record.config)) {
        respondToServerAuditFullBodyCaptureUpdateRequest(record.requestId, record.config)
      }
      break
    case 'db_service_server_account_runtime_clear_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && isAccountRuntimeClearTarget(record.target)) {
        void respondToServerAccountRuntimeClearRequest(record.requestId, record.target)
      }
      break
    case 'gateway_runtime_cache_invalidate':
      if (runtimeConfig.processRole === 'server') {
        void clearServerGatewayRuntimeCache()
      }
      break
    case 'authorization_quota_cache_invalidate':
      if (runtimeConfig.processRole === 'server') {
        invalidateGatewayAuthorizationQuotaSnapshot()
        void clearServerAuthorizationQuotaRuntimeCache()
      }
      break
    case 'client_ip_policy_cache_invalidate':
      if (runtimeConfig.processRole === 'server') {
        void clearServerClientIpPolicyCache()
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

  pending.reject(new DbServiceOperationFailedError(typeof response.errorMessage === 'string' ? response.errorMessage : '本地数据库服务请求失败'))
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
  for (const [requestId, pending] of pendingServerAuditFullBodyCaptureUpdateRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingServerAuditFullBodyCaptureUpdateRequests.delete(requestId)
  }
  for (const [requestId, pending] of pendingServerAccountRuntimeClearRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingServerAccountRuntimeClearRequests.delete(requestId)
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
      : scope === 'account_runtime'
        ? await buildServerAccountRuntimeSnapshot()
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

function finishServerAuditFullBodyCaptureUpdateRequest(
  requestId: string,
  result: AuditFullBodyCaptureUpdateResult | undefined
): void {
  const pending = pendingServerAuditFullBodyCaptureUpdateRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingServerAuditFullBodyCaptureUpdateRequests.delete(requestId)
  pending.resolve(result)
}

function finishServerAccountRuntimeClearRequest(
  requestId: string,
  result: AccountRuntimeAvailabilityClearResult | undefined
): void {
  const pending = pendingServerAccountRuntimeClearRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingServerAccountRuntimeClearRequests.delete(requestId)
  pending.resolve(result)
}

function sendToDbServiceProcess(child: ChildProcess, message: DbServiceParentMessage): void {
  if (!child.connected) {
    markDbServiceIpcBroken(new Error('本地数据库服务通信已断开'), child)
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
  const fullBodyCapture = readAuditFullBodyCaptureConfig()

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
      timedOutRequestCount: dbServiceState.timedOutRequestCount,
      rejectedRequestCount: dbServiceState.rejectedRequestCount,
      failedRequestCount: dbServiceState.failedRequestCount,
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
    activeAuditCaptureCount: auditCapture.getActiveAuditCaptureCount(),
    audit: {
      fullBodyCaptureEnabled: fullBodyCapture.enabled,
      fullBodyCapture
    }
  }
}

function respondToServerAuditFullBodyCaptureUpdateRequest(requestId: string, config: AuditFullBodyCaptureRuntimeConfig): void {
  const child = dbServiceProcess
  if (!child) {
    return
  }

  try {
    const settings = setAuditLogFullBodyCaptureConfig(config)
    sendToDbServiceProcess(child, {
      type: 'db_service_server_audit_full_body_capture_update_response',
      requestId,
      ok: true,
      result: {
        fullBodyCaptureEnabled: settings.fullBodyCaptureEnabled,
        fullBodyCapture: settings.fullBodyCapture
      }
    })
  } catch (error) {
    sendToDbServiceProcess(child, {
      type: 'db_service_server_audit_full_body_capture_update_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

async function respondToServerAccountRuntimeClearRequest(
  requestId: string,
  target: AccountRuntimeAvailabilityClearTarget
): Promise<void> {
  const child = dbServiceProcess
  if (!child) {
    return
  }

  try {
    const gatewaySideEffects = await import('../gateway/gateway-account-side-effects.service.js')
    const result = gatewaySideEffects.clearGatewayAccountRuntimeAvailability(target)
    sendToDbServiceProcess(child, {
      type: 'db_service_server_account_runtime_clear_response',
      requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendToDbServiceProcess(child, {
      type: 'db_service_server_account_runtime_clear_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

function isAuditFullBodyCaptureUpdateResult(value: unknown): value is AuditFullBodyCaptureUpdateResult {
  return typeof value === 'object'
    && value !== null
    && !Array.isArray(value)
    && typeof (value as Record<string, unknown>).fullBodyCaptureEnabled === 'boolean'
    && isAuditFullBodyCaptureConfig((value as Record<string, unknown>).fullBodyCapture)
}

function isAuditFullBodyCaptureConfig(value: unknown): value is AuditFullBodyCaptureRuntimeConfig {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return false
  }
  const record = value as Record<string, unknown>
  if (typeof record.enabled !== 'boolean') {
    return false
  }
  if (record.scope !== 'global' && record.scope !== 'account') {
    return false
  }
  if (typeof record.includeSuccess !== 'boolean') {
    return false
  }
  if (record.accountId !== undefined && typeof record.accountId !== 'string') {
    return false
  }
  if (record.expiresAt !== undefined && typeof record.expiresAt !== 'string') {
    return false
  }
  if (record.updatedAt !== undefined && typeof record.updatedAt !== 'string') {
    return false
  }
  return true
}

function isAccountRuntimeClearTarget(value: unknown): value is AccountRuntimeAvailabilityClearTarget {
  return Boolean(normalizeAccountRuntimeClearTarget(value))
}

function normalizeAccountRuntimeClearTarget(value: unknown): AccountRuntimeAvailabilityClearTarget | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  const record = value as Record<string, unknown>
  if (typeof record.accountId !== 'string' || !record.accountId.trim()) {
    return undefined
  }
  const target: AccountRuntimeAvailabilityClearTarget = {
    accountId: record.accountId.trim()
  }
  if (typeof record.authorizedBinding === 'object' && record.authorizedBinding !== null && !Array.isArray(record.authorizedBinding)) {
    const binding = record.authorizedBinding as Record<string, unknown>
    const systemAccountId = typeof binding.systemAccountId === 'string' ? binding.systemAccountId.trim() : ''
    const groupId = typeof binding.groupId === 'string' ? binding.groupId.trim() : ''
    const accountAuthorizationId = typeof binding.accountAuthorizationId === 'string' ? binding.accountAuthorizationId.trim() : ''
    if (systemAccountId && groupId && accountAuthorizationId) {
      target.authorizedBinding = {
        systemAccountId,
        groupId,
        accountAuthorizationId
      }
    }
  }
  return target
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

async function buildServerAccountRuntimeSnapshot(): Promise<DbServiceServerRuntimeSnapshot> {
  const [accountConcurrency, gatewaySideEffects] = await Promise.all([
    import('../../shared/account-concurrency.js'),
    import('../gateway/gateway-account-side-effects.service.js')
  ])
  return {
    accountConcurrency: accountConcurrency.snapshotAccountConcurrency(),
    accountRuntimeAvailability: gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()
  }
}

async function clearServerGatewayRuntimeCache(): Promise<void> {
  const [gatewayCache, oauthRefresh] = await Promise.all([
    import('../gateway/gateway-runtime-cache.service.js'),
    import('../openai-oauth/openai-oauth-access-token-refresh.service.js')
  ])
  gatewayCache.clearGatewayRuntimeCacheLocal()
  oauthRefresh.clearOpenAIOAuthRecentRefreshCache()
}

async function clearServerAuthorizationQuotaRuntimeCache(): Promise<void> {
  const authorizationQuota = await import('../gateway/authorization-quota.service.js')
  authorizationQuota.clearAuthorizationQuotaCache()
}

async function clearServerClientIpPolicyCache(): Promise<void> {
  const policyCache = await import('../gateway/client-ip-policy-cache.service.js')
  policyCache.clearClientIpPolicyCacheLocal()
}

registerAuthorizationQuotaCacheInvalidator(notifyServerAuthorizationQuotaCacheInvalidated)

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
