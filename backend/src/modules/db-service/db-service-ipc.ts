import { randomUUID } from 'node:crypto'
import type { ChildProcess } from 'node:child_process'

import { runtimeConfig } from '../../config/runtime.js'
import {
  applyGatewayApiKeyValidationCacheInvalidationFromIpcAsync,
  registerAuthorizationQuotaCacheInvalidator,
  registerGatewayApiKeyValidationServerInvalidator
} from '../../shared/gateway-cache-invalidation.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { getRequestId, getTraceId } from '../../shared/request-context.js'
import { buildProcessEventLoopSample, type ProcessEventLoopSample } from '../../shared/process-event-loop-monitor.js'
import type { BackgroundWorkerIpcQueuesRuntime } from '../background/background-ipc.js'
import { invalidateGatewayAuthorizationQuotaSnapshot } from '../gateway/quota/quota-snapshot-cache.service.js'
import type {
  AccountRuntimeAvailabilityClearResult,
  AccountRuntimeAvailabilityClearTarget,
  DbServiceChildMessage,
  DbServiceOperation,
  DbServiceOperationResult,
  DbServiceParentMessage,
  DbServiceRequestPriority,
  DbServiceRuntimeQueueSnapshot,
  DbServiceRuntimeSnapshot,
  DbServiceServerRuntimeSnapshot,
  DbServiceServerRuntimeSnapshotScope,
  DbServiceSystemMetricsRuntimeSnapshot,
  OpenAIAccountTrafficMigrationRuntimeRequest,
  OpenAIAccountTrafficMigrationRuntimeResult,
  OpenAIAccountTrafficMigrationRuntimeScope
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

export interface RequestDbServiceOptions {
  timeoutMs?: number
  priority?: DbServiceRequestPriority
  traceId?: string
}

interface DbServiceState {
  pid?: number
  ready: boolean
  httpHost?: string
  httpPort?: number
  lastSnapshot?: DbServiceRuntimeSnapshot
  pendingRequestCount: number
  pendingDatasetWriteRequestCount: number
  oldestDatasetWriteRequestMs: number
  timedOutDatasetWriteRequestCount: number
  rejectedDatasetWriteRequestCount: number
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
const invalidateTimeoutMs = 10_000
const unavailableCircuitOpenMs = 3000
const maxPendingRequests = 2000
const maxPendingDatasetWriteRequests = 1000
const maxPendingStatsWriteRequests = 1000

let dbServiceProcess: ChildProcess | undefined
let dbServiceReady = false
let dbServicePid: number | undefined
let dbServiceHttpHost: string | undefined
let dbServiceHttpPort: number | undefined
let pendingRequests = new Map<string, PendingRequest>()
let pendingServerRuntimeRequests = new Map<string, PendingServerRuntimeRequest>()
let pendingServerAccountRuntimeClearRequests = new Map<string, PendingServerAccountRuntimeClearRequest>()
let pendingOpenAIAccountTrafficMigrationRuntimeRequests = new Map<string, PendingOpenAIAccountTrafficMigrationRuntimeRequest>()
let pendingProcessEventLoopRequests = new Map<string, PendingProcessEventLoopRequest>()
let pendingDatasetWriteRequests = new Map<string, PendingDatasetWriteRequest>()
let pendingStatsWriteRequests = new Map<string, PendingDatasetWriteRequest>()
let pendingGatewayApiKeyCacheInvalidationRequests = new Map<string, PendingRequest>()
let timedOutRequestCount = 0
let rejectedRequestCount = 0
let failedRequestCount = 0
let timedOutDatasetWriteRequestCount = 0
let rejectedDatasetWriteRequestCount = 0
let timedOutProcessEventLoopRequestCount = 0
let failedProcessEventLoopRequestCount = 0
let processEventLoopTimeoutStreak = 0
let timedOutServerRuntimeRequestCount = 0
let failedServerRuntimeRequestCount = 0
let lastSnapshot: DbServiceRuntimeSnapshot | undefined
let unavailableCircuitOpenUntilMs = 0
let dbServiceReadyHandler: (() => void) | undefined
let dbServiceUnavailableHandler: (() => void) | undefined

type DbServiceServerRuntimeResponseSnapshot = DbServiceServerRuntimeSnapshot | DbServiceSystemMetricsRuntimeSnapshot

interface PendingServerRuntimeRequest {
  resolve: (snapshot: DbServiceServerRuntimeResponseSnapshot | undefined) => void
  timeout: NodeJS.Timeout
}

interface PendingServerAccountRuntimeClearRequest {
  resolve: (result: AccountRuntimeAvailabilityClearResult | undefined) => void
  timeout: NodeJS.Timeout
}

interface PendingOpenAIAccountTrafficMigrationRuntimeRequest {
  resolve: (result: OpenAIAccountTrafficMigrationRuntimeResult | undefined) => void
  timeout: NodeJS.Timeout
}

interface PendingProcessEventLoopRequest {
  resolve: (sample: ProcessEventLoopSample | undefined) => void
  timeout: NodeJS.Timeout
}

interface PendingDatasetWriteRequest {
  resolve: (result: unknown) => void
  reject: (error: Error) => void
  timeout: NodeJS.Timeout
  createdAt: number
}

registerGatewayApiKeyValidationServerInvalidator(requestServerGatewayApiKeyCacheInvalidationAsync)

export function attachDbServiceProcess(child: ChildProcess, options: { onReady?: () => void; onUnavailable?: () => void } = {}): void {
  dbServiceProcess = child
  dbServicePid = child.pid ?? undefined
  dbServiceHttpHost = undefined
  dbServiceHttpPort = undefined
  dbServiceReady = false
  processEventLoopTimeoutStreak = 0
  dbServiceReadyHandler = options.onReady
  dbServiceUnavailableHandler = options.onUnavailable

  child.removeAllListeners('message')
  child.on('message', handleDbServiceMessage)
  child.once('exit', () => {
    if (dbServiceProcess === child) {
      const wasReady = dbServiceReady
      dbServiceProcess = undefined
      dbServiceReady = false
      dbServicePid = undefined
      dbServiceHttpHost = undefined
      dbServiceHttpPort = undefined
      if (wasReady) notifyDbServiceUnavailable()
      failPendingRequests(new Error('本地数据库服务已退出'))
    }
  })
}

export async function requestDbService<T extends DbServiceOperation>(
  operation: T,
  options: RequestDbServiceOptions = {}
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
  const jobId = randomUUID()
  const rootRequestId = getRequestId()
  const requestId = rootRequestId ?? jobId
  const timeoutMs = options.timeoutMs ?? requestTimeoutMs
  const message: Extract<DbServiceParentMessage, { type: 'db_service_request' }> = {
    type: 'db_service_request',
    requestId,
    jobId,
    operationId: jobId,
    parentId: rootRequestId,
    traceId: options.traceId ?? getTraceId(),
    operation,
    deadlineAtMs: Date.now() + timeoutMs
  }
  if (options.priority) {
    message.priority = options.priority
  }
  try {
    return await new Promise<DbServiceOperationResult<T>>((resolve, reject) => {
      const timeout = setTimeout(() => {
        const pending = pendingRequests.get(jobId)
        if (!pending) {
          return
        }
        timedOutRequestCount += 1
        pendingRequests.delete(jobId)
        const timeoutError = new DbServiceRequestTimedOutError('本地数据库服务请求超时，请稍后重试')
        pending.reject(timeoutError)
      }, timeoutMs)
      pendingRequests.set(jobId, { resolve: resolve as (value: unknown) => void, reject, timeout })
      const failSend = (error: unknown): void => {
        const pending = pendingRequests.get(jobId)
        if (!pending) {
          return
        }
        clearTimeout(pending.timeout)
        pendingRequests.delete(jobId)
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
    sendDbServiceChildMessage({ type: 'gateway_runtime_cache_invalidate' })
    void requestDbService(
      { type: 'clear_gateway_runtime_cache' },
      { timeoutMs: invalidateTimeoutMs }
    ).catch((error) => {
      logger.warn(errorLogFields(error, {
        event: 'db_service_local_cache_invalidation_failed'
      }), 'DB service 本地缓存失效失败')
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
    pendingDatasetWriteRequestCount: pendingDatasetWriteRequests.size,
    oldestDatasetWriteRequestMs: oldestDbServiceDatasetWriteRequestMs(),
    timedOutDatasetWriteRequestCount,
    rejectedDatasetWriteRequestCount,
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

export async function requestServerSystemMetricsRuntimeSnapshot(
  timeoutMs = 1000
): Promise<DbServiceSystemMetricsRuntimeSnapshot | undefined> {
  return await requestServerRuntimeSnapshotByScope('system_metrics', timeoutMs)
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
  if (runtimeConfig.processRole === 'db-service') {
    void clearServerClientIpPolicyCache()
    if (typeof process.send === 'function') {
      sendDbServiceChildMessage({ type: 'client_ip_policy_cache_invalidate' })
    }
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

export async function clearServerAccountRuntimeAvailability(
  target: AccountRuntimeAvailabilityClearTarget,
  timeoutMs = 1000
): Promise<AccountRuntimeAvailabilityClearResult | undefined> {
  const normalizedTarget = normalizeAccountRuntimeClearTarget(target)
  if (!normalizedTarget) {
    return undefined
  }
  if (runtimeConfig.processRole !== 'db-service') {
    const gatewaySideEffects = await import('../gateway/runtime/account-side-effects.service.js')
    return gatewaySideEffects.clearGatewayAccountRuntimeAvailability(normalizedTarget)
  }
  if (!process.send) {
    assertDbServiceParentIpcAvailable('clearServerAccountRuntimeAvailability')
    return undefined
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

export async function migrateServerOpenAIAccountTrafficRuntime(
  input: OpenAIAccountTrafficMigrationRuntimeRequest,
  timeoutMs = 1000
): Promise<OpenAIAccountTrafficMigrationRuntimeResult | undefined> {
  const normalizedInput = normalizeOpenAIAccountTrafficMigrationRuntimeInput(input)
  if (!normalizedInput) {
    return undefined
  }
  if (runtimeConfig.processRole !== 'db-service') {
    return migrateOpenAIAccountTrafficRuntimeLocal(normalizedInput)
  }
  if (!process.send) {
    assertDbServiceParentIpcAvailable('migrateServerOpenAIAccountTrafficRuntime')
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<OpenAIAccountTrafficMigrationRuntimeResult | undefined>((resolve) => {
    const timeout = setTimeout(() => {
      const pending = pendingOpenAIAccountTrafficMigrationRuntimeRequests.get(requestId)
      if (!pending) {
        return
      }
      pendingOpenAIAccountTrafficMigrationRuntimeRequests.delete(requestId)
      pending.resolve(undefined)
    }, timeoutMs)
    pendingOpenAIAccountTrafficMigrationRuntimeRequests.set(requestId, { resolve, timeout })
    sendDbServiceChildMessage({
      type: 'db_service_openai_traffic_migration_runtime_request',
      requestId,
      input: normalizedInput
    }, () => {
      finishOpenAIAccountTrafficMigrationRuntimeRequest(requestId, undefined)
    })
  })
}

export async function requestDbServiceDatasetWrite<T extends import('../background/background-dataset-writer.js').BackgroundDatasetWriteOperation>(
  operation: T,
  timeoutMs = 30_000
): Promise<import('../background/background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T> | undefined> {
  if (runtimeConfig.processRole === 'server') {
    const backgroundIpc = await import('../background/background-ipc.js')
    return await backgroundIpc.requestBackgroundWorkerDatasetWrite(operation, timeoutMs) as import('../background/background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T> | undefined
  }
  if (runtimeConfig.processRole !== 'db-service' || typeof process.send !== 'function') {
    return undefined
  }

  if (pendingDatasetWriteRequests.size >= maxPendingDatasetWriteRequests) {
    rejectedDatasetWriteRequestCount += 1
    logger.warn({
      event: 'db_service_dataset_write_pending_full',
      operationType: operation.type,
      pendingCount: pendingDatasetWriteRequests.size,
      maxPendingCount: maxPendingDatasetWriteRequests
    }, 'DB service dataset-writer pending 请求已达上限，已拒绝本次请求')
    throw new Error('后台 dataset-writer pending 请求过多，请稍后重试')
  }
  const requestId = randomUUID()
  return await new Promise<import('../background/background-dataset-writer.js').BackgroundDatasetWriteOperationResult<T> | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      const pending = pendingDatasetWriteRequests.get(requestId)
      if (!pending) {
        return
      }
      timedOutDatasetWriteRequestCount += 1
      pendingDatasetWriteRequests.delete(requestId)
      pending.reject(new Error('后台 dataset-writer 请求超时'))
    }, timeoutMs)
    pendingDatasetWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout, createdAt: Date.now() })
    sendDbServiceChildMessage({
      type: 'background_worker_dataset_write_request',
      requestId,
      operation
    }, () => {
      finishDatasetWriteRequest(requestId, undefined)
    })
  })
}

export async function requestDbServiceStatsWrite<T extends import('../background/background-stats-writer.js').BackgroundStatsWriteOperation>(
  operation: T,
  timeoutMs = 10_000
): Promise<import('../background/background-stats-writer.js').BackgroundStatsWriteOperationResult<T> | undefined> {
  if (runtimeConfig.processRole === 'server') {
    const backgroundIpc = await import('../background/background-ipc.js')
    return await backgroundIpc.requestBackgroundWorkerStatsWrite(operation, timeoutMs)
  }
  if (runtimeConfig.processRole !== 'db-service' || typeof process.send !== 'function') return undefined
  if (pendingStatsWriteRequests.size >= maxPendingStatsWriteRequests) {
    throw new Error('后台 stats-writer pending 请求过多，请稍后重试')
  }
  const requestId = randomUUID()
  return await new Promise<import('../background/background-stats-writer.js').BackgroundStatsWriteOperationResult<T> | undefined>((resolve, reject) => {
    const timeout = setTimeout(() => {
      const pending = pendingStatsWriteRequests.get(requestId)
      if (!pending) return
      pendingStatsWriteRequests.delete(requestId)
      pending.reject(new Error('后台 stats-writer 请求超时'))
    }, timeoutMs)
    pendingStatsWriteRequests.set(requestId, { resolve: resolve as (value: unknown) => void, reject, timeout, createdAt: Date.now() })
    sendDbServiceChildMessage({
      type: 'background_worker_stats_write_request',
      requestId,
      operation
    }, () => finishStatsWriteRequest(requestId, undefined))
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
      sample: buildProcessEventLoopSample()
    }
    sendDbServiceChildMessage(response, () => exitDbServiceAfterChildIpcFailure(response.type))
    return true
  }

  if (record.type === 'db_service_server_account_runtime_clear_response' && typeof record.requestId === 'string') {
    finishServerAccountRuntimeClearRequest(
      record.requestId,
      record.ok === true ? record.result as AccountRuntimeAvailabilityClearResult : undefined
    )
    return true
  }

  if (record.type === 'db_service_openai_traffic_migration_runtime_response' && typeof record.requestId === 'string') {
    finishOpenAIAccountTrafficMigrationRuntimeRequest(
      record.requestId,
      record.ok === true ? record.result as OpenAIAccountTrafficMigrationRuntimeResult : undefined
    )
    return true
  }

  if (record.type === 'background_worker_dataset_write_response' && typeof record.requestId === 'string') {
    finishDatasetWriteRequest(
      record.requestId,
      record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : 'dataset-writer 请求失败' }
    )
    return true
  }

  if (record.type === 'background_worker_stats_write_response' && typeof record.requestId === 'string') {
    finishStatsWriteRequest(
      record.requestId,
      record.ok === true ? { ok: true, result: record.result } : { ok: false, errorMessage: typeof record.errorMessage === 'string' ? record.errorMessage : 'stats-writer 请求失败' }
    )
    return true
  }

  if (record.type === 'db_service_gateway_api_key_cache_invalidation_response' && typeof record.requestId === 'string') {
    finishGatewayApiKeyCacheInvalidationRequest(record.requestId, record.ok === true
      ? { ok: true }
      : {
          ok: false,
          errorMessage: typeof record.errorMessage === 'string'
            ? record.errorMessage
            : 'server API Key validation cache 失效失败'
        })
    return true
  }

  if (record.type !== 'db_service_server_runtime_response' || typeof record.requestId !== 'string') {
    return false
  }

  const pending = pendingServerRuntimeRequests.get(record.requestId)
  if (!pending) {
    return true
  }
  finishServerRuntimeRequest(record.requestId, record.ok === true ? record.result as DbServiceServerRuntimeResponseSnapshot : undefined)
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
      finishPendingRequest(typeof record.jobId === 'string' ? record.jobId : record.requestId, record)
      break
    case 'db_service_process_event_loop_response':
      if (typeof record.requestId !== 'string') break
      finishProcessEventLoopRequest(record.requestId, record.sample as ProcessEventLoopSample | undefined)
      break
    case 'db_service_server_runtime_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string') {
        void respondToServerRuntimeRequest(
          record.requestId,
          record.scope === 'account_concurrency'
          || record.scope === 'account_runtime'
          || record.scope === 'system_metrics'
            ? record.scope
            : 'full'
        )
      }
      break
    case 'db_service_server_account_runtime_clear_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && isAccountRuntimeClearTarget(record.target)) {
        void respondToServerAccountRuntimeClearRequest(record.requestId, record.target)
      }
      break
    case 'db_service_openai_traffic_migration_runtime_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string') {
        const input = normalizeOpenAIAccountTrafficMigrationRuntimeInput(record.input)
        if (input) {
          void respondToOpenAIAccountTrafficMigrationRuntimeRequest(record.requestId, input)
        }
      }
      break
    case 'background_worker_dataset_write_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && record.operation) {
        void respondToDatasetWriteRequest(record.requestId, record.operation as import('../background/background-dataset-writer.js').BackgroundDatasetWriteOperation)
      }
      break
    case 'background_worker_stats_write_request':
      if (runtimeConfig.processRole === 'server' && typeof record.requestId === 'string' && record.operation) {
        void respondToStatsWriteRequest(record.requestId, record.operation as import('../background/background-stats-writer.js').BackgroundStatsWriteOperation)
      }
      break
    case 'gateway_runtime_cache_invalidate':
      if (runtimeConfig.processRole === 'server') {
        void clearServerGatewayRuntimeCache()
      }
      break
    case 'db_service_gateway_api_key_cache_invalidation_request':
      if (
        runtimeConfig.processRole === 'server'
        && typeof record.requestId === 'string'
        && (record.apiKeyId === undefined || typeof record.apiKeyId === 'string')
        && Array.isArray(record.keyHashes)
      ) {
        void respondToGatewayApiKeyCacheInvalidationRequest(
          record.requestId,
          record.apiKeyId,
          record.keyHashes.filter((value): value is string => typeof value === 'string')
        )
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
    case 'background_worker_usage_records':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.items)) {
        void forwardUsageRecordsToWorker(record.items)
      }
      break
    case 'background_worker_audit_logs':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.items)) {
        void forwardAuditLogsToWorker(record.items)
      }
      break
    case 'background_worker_operation_logs':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.items)) {
        void forwardOperationLogsToWorker(record.items)
      }
      break
    case 'background_worker_public_api_logs':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.items)) {
        void forwardPublicApiLogsToWorker(record.items)
      }
      break
    case 'background_worker_record_maintenance':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.items)) {
        void forwardRecordMaintenanceJobsToWorker(record.items)
      }
      break
    case 'background_worker_account_test_tasks':
      if (runtimeConfig.processRole === 'server' && Array.isArray(record.taskIds)) {
        void forwardAccountTestTasksToWorker(record.taskIds)
      }
      break
    case 'background_worker_account_test_cancel':
      if (runtimeConfig.processRole === 'server' && typeof record.taskId === 'string') {
        void forwardAccountTestCancelToWorker(record.taskId)
      }
      break
    case 'background_worker_account_health_check_trigger':
      if (runtimeConfig.processRole === 'server' && typeof record.accountId === 'string') {
        void forwardAccountHealthCheckTriggerToWorker(record.accountId, record.reason, record.traceId, record.sourceFence)
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
  for (const [requestId, pending] of pendingServerAccountRuntimeClearRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingServerAccountRuntimeClearRequests.delete(requestId)
  }
  for (const [requestId, pending] of pendingOpenAIAccountTrafficMigrationRuntimeRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingOpenAIAccountTrafficMigrationRuntimeRequests.delete(requestId)
  }
  for (const [requestId, pending] of pendingDatasetWriteRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingDatasetWriteRequests.delete(requestId)
  }
  for (const [requestId, pending] of pendingStatsWriteRequests) {
    clearTimeout(pending.timeout)
    pending.resolve(undefined)
    pendingStatsWriteRequests.delete(requestId)
  }
  for (const [requestId, pending] of pendingGatewayApiKeyCacheInvalidationRequests) {
    clearTimeout(pending.timeout)
    pending.reject(error)
    pendingGatewayApiKeyCacheInvalidationRequests.delete(requestId)
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
    const wasReady = dbServiceReady
    dbServiceReady = false
    dbServicePid = child.pid ?? dbServicePid
    if (wasReady) notifyDbServiceUnavailable()
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

function notifyDbServiceUnavailable(): void {
  try {
    dbServiceUnavailableHandler?.()
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'db_service_ipc_unavailable_callback_failed'
    }), 'DB service IPC unavailable 回调执行失败')
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
        : scope === 'system_metrics'
          ? await buildServerSystemMetricsRuntimeSnapshot()
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

function requestServerRuntimeSnapshotByScope(
  scope: 'system_metrics',
  timeoutMs: number
): Promise<DbServiceSystemMetricsRuntimeSnapshot | undefined>
function requestServerRuntimeSnapshotByScope(
  scope: Exclude<DbServiceServerRuntimeSnapshotScope, 'system_metrics'>,
  timeoutMs: number
): Promise<DbServiceServerRuntimeSnapshot | undefined>
async function requestServerRuntimeSnapshotByScope(
  scope: DbServiceServerRuntimeSnapshotScope,
  timeoutMs: number
): Promise<DbServiceServerRuntimeResponseSnapshot | undefined> {
  if (runtimeConfig.processRole !== 'db-service' || !process.send) {
    return undefined
  }

  const requestId = randomUUID()
  return await new Promise<DbServiceServerRuntimeResponseSnapshot | undefined>((resolve) => {
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

function finishServerRuntimeRequest(requestId: string, snapshot: DbServiceServerRuntimeResponseSnapshot | undefined): void {
  const pending = pendingServerRuntimeRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingServerRuntimeRequests.delete(requestId)
  pending.resolve(snapshot)
}

async function requestServerGatewayApiKeyCacheInvalidationAsync(
  apiKeyId: string | undefined,
  keyHashes: readonly string[]
): Promise<void> {
  if (runtimeConfig.processRole !== 'db-service') return
  if (!process.send) {
    throw new Error('DB service API Key validation cache 失效 IPC 通道不可用')
  }
  const requestId = randomUUID()
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => {
      const pending = pendingGatewayApiKeyCacheInvalidationRequests.get(requestId)
      if (!pending) return
      pendingGatewayApiKeyCacheInvalidationRequests.delete(requestId)
      pending.reject(new Error('server API Key validation cache 失效确认超时'))
    }, invalidateTimeoutMs)
    pendingGatewayApiKeyCacheInvalidationRequests.set(requestId, { resolve: () => resolve(), reject, timeout })
    sendDbServiceChildMessage({
      type: 'db_service_gateway_api_key_cache_invalidation_request',
      requestId,
      apiKeyId,
      keyHashes: [...keyHashes]
    }, () => {
      finishGatewayApiKeyCacheInvalidationRequest(requestId, {
        ok: false,
        errorMessage: 'DB service 无法向 server 发布 API Key validation cache 失效'
      })
    })
  })
}

function finishGatewayApiKeyCacheInvalidationRequest(
  requestId: string,
  response: { ok: true } | { ok: false; errorMessage: string }
): void {
  const pending = pendingGatewayApiKeyCacheInvalidationRequests.get(requestId)
  if (!pending) return
  clearTimeout(pending.timeout)
  pendingGatewayApiKeyCacheInvalidationRequests.delete(requestId)
  if (response.ok) pending.resolve(undefined)
  else pending.reject(new Error(response.errorMessage))
}

async function respondToGatewayApiKeyCacheInvalidationRequest(
  requestId: string,
  apiKeyId: string | undefined,
  keyHashes: readonly string[]
): Promise<void> {
  const child = dbServiceProcess
  if (!child) return
  try {
    await applyGatewayApiKeyValidationCacheInvalidationFromIpcAsync(apiKeyId, keyHashes)
    sendToDbServiceProcess(child, {
      type: 'db_service_gateway_api_key_cache_invalidation_response',
      requestId,
      ok: true
    })
  } catch (error) {
    sendToDbServiceProcess(child, {
      type: 'db_service_gateway_api_key_cache_invalidation_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
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

function finishOpenAIAccountTrafficMigrationRuntimeRequest(
  requestId: string,
  result: OpenAIAccountTrafficMigrationRuntimeResult | undefined
): void {
  const pending = pendingOpenAIAccountTrafficMigrationRuntimeRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingOpenAIAccountTrafficMigrationRuntimeRequests.delete(requestId)
  pending.resolve(result)
}

function finishDatasetWriteRequest(
  requestId: string,
  response: { ok: true; result: unknown } | { ok: false; errorMessage: string } | undefined
): void {
  const pending = pendingDatasetWriteRequests.get(requestId)
  if (!pending) {
    return
  }

  clearTimeout(pending.timeout)
  pendingDatasetWriteRequests.delete(requestId)
  if (!response) {
    pending.resolve(undefined)
    return
  }
  if (response.ok) {
    pending.resolve(response.result)
    return
  }
  pending.reject(new Error(response.errorMessage))
}

function finishStatsWriteRequest(
  requestId: string,
  response: { ok: true; result: unknown } | { ok: false; errorMessage: string } | undefined
): void {
  const pending = pendingStatsWriteRequests.get(requestId)
  if (!pending) return
  clearTimeout(pending.timeout)
  pendingStatsWriteRequests.delete(requestId)
  if (!response) {
    pending.resolve(undefined)
  } else if (response.ok) {
    pending.resolve(response.result)
  } else {
    pending.reject(new Error(response.errorMessage))
  }
}

function oldestDbServiceDatasetWriteRequestMs(): number {
  let oldestAt = 0
  for (const pending of pendingDatasetWriteRequests.values()) {
    if (oldestAt === 0 || pending.createdAt < oldestAt) {
      oldestAt = pending.createdAt
    }
  }
  return oldestAt === 0 ? 0 : Math.max(0, Date.now() - oldestAt)
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
    systemMetrics,
    auditCapture,
    auditLogTransport,
    auditLogQueue,
    gatewayUsageFinalization,
    accountConcurrency,
    backgroundIpc,
    gatewaySideEffects
  ] = await Promise.all([
    buildServerSystemMetricsRuntimeSnapshot(),
    import('../gateway/audit/capture.service.js'),
    import('../audit-logs/audit-log-transport.service.js'),
    import('../audit-logs/audit-log-queue.service.js'),
    import('../gateway/usage/failure-finalization.service.js'),
    import('../../shared/account-concurrency.js'),
    import('../background/background-ipc.js'),
    import('../gateway/runtime/account-side-effects.service.js')
  ])
  const workerState = backgroundIpc.getBackgroundWorkerState()
  const dbServiceState = getDbServiceState()
  return {
    ...systemMetrics,
    ingestWorker: systemMetrics.ingestWorker
      ? {
        ...systemMetrics.ingestWorker,
        pid: systemMetrics.ingestWorker.snapshot?.pid ?? workerState.ingestWorker?.pid,
        pendingMessageCount: workerState.ingestWorker?.pendingMessageCount,
        pendingMessageBytes: workerState.ingestWorker?.pendingMessageBytes,
        pendingSnapshotRequestCount: workerState.ingestWorker?.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: workerState.ingestWorker?.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: workerState.ingestWorker?.rejectedSnapshotRequestCount
      }
      : undefined,
    statsWorker: systemMetrics.statsWorker
      ? {
        ...systemMetrics.statsWorker,
        pid: systemMetrics.statsWorker.snapshot?.pid ?? workerState.statsWorker?.pid,
        pendingSnapshotRequestCount: workerState.statsWorker?.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: workerState.statsWorker?.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: workerState.statsWorker?.rejectedSnapshotRequestCount
      }
      : undefined,
    opsWorker: systemMetrics.opsWorker
      ? {
        ...systemMetrics.opsWorker,
        pid: systemMetrics.opsWorker.snapshot?.pid ?? workerState.opsWorker?.pid,
        pendingMessageCount: workerState.opsWorker?.pendingMessageCount,
        pendingMessageBytes: workerState.opsWorker?.pendingMessageBytes,
        pendingSnapshotRequestCount: workerState.opsWorker?.pendingSnapshotRequestCount,
        timedOutSnapshotRequestCount: workerState.opsWorker?.timedOutSnapshotRequestCount,
        rejectedSnapshotRequestCount: workerState.opsWorker?.rejectedSnapshotRequestCount
      }
      : undefined,
    dbService: {
      ...systemMetrics.dbService,
      pid: dbServiceState.pid,
      ready: dbServiceState.ready,
      pendingRequestCount: dbServiceState.pendingRequestCount,
      timedOutRequestCount: dbServiceState.timedOutRequestCount,
      rejectedRequestCount: dbServiceState.rejectedRequestCount,
      failedRequestCount: dbServiceState.failedRequestCount,
      lastQueueWaitMs: dbServiceState.lastSnapshot?.lastQueueWaitMs,
      maxQueueWaitMs: dbServiceState.lastSnapshot?.maxQueueWaitMs,
      processEventLoopTimeoutStreak: dbServiceState.processEventLoopTimeoutStreak,
      unavailableCircuitOpenUntil: dbServiceState.unavailableCircuitOpenUntil,
      httpHost: dbServiceState.httpHost,
      httpPort: dbServiceState.httpPort,
      sqliteReadWorkerPool: dbServiceState.lastSnapshot?.sqliteReadWorkerPool
    },
    accountConcurrency: accountConcurrency.snapshotAccountConcurrency(),
    gatewayAccountSideEffects: { ...gatewaySideEffects.getGatewayAccountSideEffectState() },
    activeAuditCaptureCount: auditCapture.getActiveAuditCaptureCount(),
    gatewayUsageFinalization: gatewayUsageFinalization.getGatewayUsageFinalizationRuntime(),
    auditLogTransport: {
      ...auditLogTransport.getAuditLogTransportRuntime(),
      pendingDispatchCount: auditLogQueue.getAuditLogServerDispatchPendingCount()
    }
  }
}

async function buildServerSystemMetricsRuntimeSnapshot(): Promise<DbServiceSystemMetricsRuntimeSnapshot> {
  const [
    backgroundIpc,
    gatewaySideEffects,
    highConcurrencyQueue,
    accountBalanceSnapshotCleanup
  ] = await Promise.all([
    import('../background/background-ipc.js'),
    import('../gateway/runtime/account-side-effects.service.js'),
    import('../gateway/runtime/high-concurrency-queue.service.js'),
    import('../accounts/account-balance-snapshot-cleanup.service.js')
  ])
  const [
    ingestWorkerSnapshot,
    statsWorkerSnapshot,
    opsWorkerSnapshot
  ] = await Promise.all([
    backgroundIpc.requestIngestWorkerSnapshot(1500).catch(() => undefined),
    backgroundIpc.requestStatsWorkerSnapshot(1500).catch(() => undefined),
    backgroundIpc.requestOpsWorkerSnapshot(1500).catch(() => undefined)
  ])
  const workerState = backgroundIpc.getBackgroundWorkerState()
  const dbServiceState = getDbServiceState()
  const ingestWorkerState = workerState.ingestWorker
  const statsWorkerState = workerState.statsWorker
  const opsWorkerState = workerState.opsWorker
  const gatewayAccountSideEffectState = gatewaySideEffects.getGatewayAccountSideEffectState()

  return {
    observedAt: new Date().toISOString(),
    accountBalanceSnapshotCleanup: accountBalanceSnapshotCleanup.getAccountBalanceSnapshotCleanupRuntime(),
    ingestWorker: {
      ready: ingestWorkerSnapshot?.ready ?? ingestWorkerState?.ready ?? false,
      pendingQueues: ingestWorkerState?.pendingQueues
        ? backgroundPendingQueuesSnapshot(ingestWorkerState.pendingQueues)
        : undefined,
      pendingWriteRequestCount: ingestWorkerState?.pendingWriteRequestCount,
      oldestPendingWriteMs: ingestWorkerState?.oldestPendingWriteMs,
      snapshot: ingestWorkerSnapshot
        ? {
          pid: ingestWorkerSnapshot.pid,
          ready: ingestWorkerSnapshot.ready,
          workerRole: ingestWorkerSnapshot.workerRole,
          jobs: ingestWorkerSnapshot.jobs.map((job) => ({ ...job })),
          usageRecordQueue: { ...ingestWorkerSnapshot.usageRecordQueue },
          operationLogQueue: { ...ingestWorkerSnapshot.operationLogQueue },
          publicApiLogQueue: { ...ingestWorkerSnapshot.publicApiLogQueue },
          auditLogQueue: { ...ingestWorkerSnapshot.auditLogQueue },
          recordMaintenanceQueue: { ...ingestWorkerSnapshot.recordMaintenanceQueue }
        }
        : undefined
    },
    statsWorker: {
      ready: statsWorkerSnapshot?.ready ?? statsWorkerState?.ready ?? false,
      pendingWriteRequestCount: statsWorkerState?.pendingWriteRequestCount,
      oldestPendingWriteMs: statsWorkerState?.oldestPendingWriteMs,
      snapshot: statsWorkerSnapshot
        ? {
          pid: statsWorkerSnapshot.pid,
          ready: statsWorkerSnapshot.ready,
          workerRole: statsWorkerSnapshot.workerRole,
          jobs: statsWorkerSnapshot.jobs.map((job) => ({ ...job })),
          recordMaintenanceQueue: { ...statsWorkerSnapshot.recordMaintenanceQueue },
          accountQualityFailurePrecheckQueue: statsWorkerSnapshot.accountQualityFailurePrecheckQueue
            ? { ...statsWorkerSnapshot.accountQualityFailurePrecheckQueue }
            : undefined
        }
        : undefined
    },
    opsWorker: {
      ready: opsWorkerSnapshot?.ready ?? opsWorkerState?.ready ?? false,
      pendingQueues: opsWorkerState?.pendingQueues
        ? backgroundPendingQueuesSnapshot(opsWorkerState.pendingQueues)
        : undefined,
      snapshot: opsWorkerSnapshot
        ? {
          pid: opsWorkerSnapshot.pid,
          ready: opsWorkerSnapshot.ready,
          workerRole: opsWorkerSnapshot.workerRole,
          jobs: opsWorkerSnapshot.jobs.map((job) => ({ ...job })),
          accountHealthCheckQueue: opsWorkerSnapshot.accountHealthCheckQueue
            ? { ...opsWorkerSnapshot.accountHealthCheckQueue }
            : undefined,
          cooldownAccountRetestQueue: opsWorkerSnapshot.cooldownAccountRetestQueue
            ? { ...opsWorkerSnapshot.cooldownAccountRetestQueue }
            : undefined,
          accountApiKeyCooldownRetestQueue: opsWorkerSnapshot.accountApiKeyCooldownRetestQueue
            ? { ...opsWorkerSnapshot.accountApiKeyCooldownRetestQueue }
            : undefined,
          normalRouteSpeedFirstRecoveryProbeQueue: opsWorkerSnapshot.normalRouteSpeedFirstRecoveryProbeQueue
            ? { ...opsWorkerSnapshot.normalRouteSpeedFirstRecoveryProbeQueue }
            : undefined,
          accountQualityFailurePrecheckQueue: opsWorkerSnapshot.accountQualityFailurePrecheckQueue
            ? { ...opsWorkerSnapshot.accountQualityFailurePrecheckQueue }
            : undefined,
          manualAccountTestQueue: opsWorkerSnapshot.manualAccountTestQueue
            ? { ...opsWorkerSnapshot.manualAccountTestQueue }
            : undefined
        }
        : undefined
    },
    dbService: {
      pendingDatasetWriteRequestCount: dbServiceState.pendingDatasetWriteRequestCount,
      oldestDatasetWriteRequestMs: dbServiceState.oldestDatasetWriteRequestMs,
      timedOutDatasetWriteRequestCount: dbServiceState.timedOutDatasetWriteRequestCount,
      rejectedDatasetWriteRequestCount: dbServiceState.rejectedDatasetWriteRequestCount,
      queuedRequestCount: dbServiceState.lastSnapshot?.queuedRequestCount,
      queuedRequestBytes: dbServiceState.lastSnapshot?.queuedRequestBytes,
      queuedHighRequestCount: dbServiceState.lastSnapshot?.queuedHighRequestCount,
      queuedNormalRequestCount: dbServiceState.lastSnapshot?.queuedNormalRequestCount,
      queuedLowRequestCount: dbServiceState.lastSnapshot?.queuedLowRequestCount,
      oldestQueuedMs: dbServiceState.lastSnapshot?.oldestQueuedMs,
      queueRejectedCount: dbServiceState.lastSnapshot?.queueRejectedCount,
      queueExpiredCount: dbServiceState.lastSnapshot?.queueExpiredCount,
      activeConcurrentRequestCount: dbServiceState.lastSnapshot?.activeConcurrentRequestCount,
      maxActiveConcurrentRequestCount: dbServiceState.lastSnapshot?.maxActiveConcurrentRequestCount,
      lastExecMs: dbServiceState.lastSnapshot?.lastExecMs,
      maxExecMs: dbServiceState.lastSnapshot?.maxExecMs,
      slowOpCount: dbServiceState.lastSnapshot?.slowOpCount,
      lastSlowOpType: dbServiceState.lastSnapshot?.lastSlowOpType,
      lastSlowOpAt: dbServiceState.lastSnapshot?.lastSlowOpAt,
      pendingProcessEventLoopRequestCount: dbServiceState.pendingProcessEventLoopRequestCount,
      timedOutProcessEventLoopRequestCount: dbServiceState.timedOutProcessEventLoopRequestCount,
      failedProcessEventLoopRequestCount: dbServiceState.failedProcessEventLoopRequestCount,
      pendingServerRuntimeRequestCount: dbServiceState.pendingServerRuntimeRequestCount,
      timedOutServerRuntimeRequestCount: dbServiceState.timedOutServerRuntimeRequestCount,
      failedServerRuntimeRequestCount: dbServiceState.failedServerRuntimeRequestCount,
      codexContextStateWriterPool: dbServiceState.lastSnapshot?.codexContextStateWriterPool
    },
    highConcurrencyQueues: highConcurrencyQueue.highConcurrencyGroupQueueSnapshot(),
    gatewayAccountSideEffects: {
      queueLength: gatewayAccountSideEffectState.queueLength,
      processing: gatewayAccountSideEffectState.processing,
      completedCount: gatewayAccountSideEffectState.completedCount,
      coalescedCount: gatewayAccountSideEffectState.coalescedCount,
      canceledBySuccessCount: gatewayAccountSideEffectState.canceledBySuccessCount,
      skippedHealthySuccessCount: gatewayAccountSideEffectState.skippedHealthySuccessCount,
      failedAttemptCount: gatewayAccountSideEffectState.failedAttemptCount,
      droppedCount: gatewayAccountSideEffectState.droppedCount,
      expiredCount: gatewayAccountSideEffectState.expiredCount,
      localSuppressedAccountCount: gatewayAccountSideEffectState.localSuppressedAccountCount,
      degradedAccountCount: gatewayAccountSideEffectState.degradedAccountCount,
      precheckPendingAccountCount: gatewayAccountSideEffectState.precheckPendingAccountCount,
      nextAttemptAt: gatewayAccountSideEffectState.nextAttemptAt
    }
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
    const gatewaySideEffects = await import('../gateway/runtime/account-side-effects.service.js')
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

async function respondToOpenAIAccountTrafficMigrationRuntimeRequest(
  requestId: string,
  input: OpenAIAccountTrafficMigrationRuntimeRequest
): Promise<void> {
  const child = dbServiceProcess
  if (!child) {
    return
  }

  try {
    const result = await migrateOpenAIAccountTrafficRuntimeLocal(input)
    sendToDbServiceProcess(child, {
      type: 'db_service_openai_traffic_migration_runtime_response',
      requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendToDbServiceProcess(child, {
      type: 'db_service_openai_traffic_migration_runtime_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

async function respondToDatasetWriteRequest(
  requestId: string,
  operation: import('../background/background-dataset-writer.js').BackgroundDatasetWriteOperation
): Promise<void> {
  const child = dbServiceProcess
  if (!child) {
    return
  }

  try {
    const backgroundIpc = await import('../background/background-ipc.js')
    const result = await backgroundIpc.requestBackgroundWorkerDatasetWrite(operation)
    if (result === undefined) {
      throw new Error(`dataset-writer 不可用，无法执行数据集写操作：${operation.type}`)
    }
    sendToDbServiceProcess(child, {
      type: 'background_worker_dataset_write_response',
      requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendToDbServiceProcess(child, {
      type: 'background_worker_dataset_write_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

async function respondToStatsWriteRequest(
  requestId: string,
  operation: import('../background/background-stats-writer.js').BackgroundStatsWriteOperation
): Promise<void> {
  const child = dbServiceProcess
  if (!child) return
  try {
    const backgroundIpc = await import('../background/background-ipc.js')
    const result = await backgroundIpc.requestBackgroundWorkerStatsWrite(operation)
    if (result === undefined) throw new Error(`stats-writer 不可用，无法执行统计写操作：${operation.type}`)
    sendToDbServiceProcess(child, {
      type: 'background_worker_stats_write_response',
      requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendToDbServiceProcess(child, {
      type: 'background_worker_stats_write_response',
      requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

async function migrateOpenAIAccountTrafficRuntimeLocal(
  input: OpenAIAccountTrafficMigrationRuntimeRequest
): Promise<OpenAIAccountTrafficMigrationRuntimeResult> {
  const sessionAffinity = await import('../gateway/runtime/session-affinity.service.js')
  const affinityResult = await sessionAffinity.migrateOpenAIAccountSessionAffinityAsync(
    input.sourceAccountId,
    input.targetAccountId,
    input.affinityScope,
    { preferMigratedSessions: input.preferMigratedSessions === true }
  )
  await sessionAffinity.rememberOpenAIAccountTrafficMigrationPreferenceAsync(
    input.sourceAccountId,
    input.targetAccountId,
    input.preferenceScope,
    { throwOnRedisError: true }
  )
  return affinityResult
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

function normalizeOpenAIAccountTrafficMigrationRuntimeInput(value: unknown): OpenAIAccountTrafficMigrationRuntimeRequest | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  const record = value as Record<string, unknown>
  const sourceAccountId = normalizedString(record.sourceAccountId)
  const targetAccountId = normalizedString(record.targetAccountId)
  if (!sourceAccountId || !targetAccountId || sourceAccountId === targetAccountId) {
    return undefined
  }
  const affinityScope = normalizeOpenAIAccountTrafficMigrationRuntimeScope(record.affinityScope, false)
  const preferenceScope = normalizeOpenAIAccountTrafficMigrationRuntimeScope(record.preferenceScope, true)
  return {
    sourceAccountId,
    targetAccountId,
    ...(affinityScope ? { affinityScope } : {}),
    ...(preferenceScope ? { preferenceScope } : {}),
    ...(record.preferMigratedSessions === true ? { preferMigratedSessions: true } : {})
  }
}

function normalizeOpenAIAccountTrafficMigrationRuntimeScope(value: unknown, requireGroupId: boolean): Partial<OpenAIAccountTrafficMigrationRuntimeScope> | undefined {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return undefined
  }
  const record = value as Record<string, unknown>
  const systemAccountId = normalizedString(record.systemAccountId)
  const groupId = normalizedString(record.groupId)
  const apiKeyId = normalizedString(record.apiKeyId)
  if (!systemAccountId || (requireGroupId && !groupId)) {
    return undefined
  }
  return {
    systemAccountId,
    ...(apiKeyId ? { apiKeyId } : {}),
    ...(groupId ? { groupId } : {})
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

async function buildServerAccountRuntimeSnapshot(): Promise<DbServiceServerRuntimeSnapshot> {
  const [accountConcurrency, gatewaySideEffects] = await Promise.all([
    import('../../shared/account-concurrency.js'),
    import('../gateway/runtime/account-side-effects.service.js')
  ])
  return {
    accountConcurrency: accountConcurrency.snapshotAccountConcurrency(),
    accountRuntimeAvailability: gatewaySideEffects.snapshotGatewayAccountRuntimeAvailability()
  }
}

async function clearServerGatewayRuntimeCache(): Promise<void> {
  const [gatewayCache, oauthRefresh] = await Promise.all([
    import('../gateway/runtime/runtime-cache.service.js'),
    import('../openai-oauth/openai-oauth-access-token-refresh.service.js')
  ])
  gatewayCache.clearGatewayRuntimeCacheLocal()
  oauthRefresh.clearOpenAIOAuthRecentRefreshCache()
}

async function clearServerAuthorizationQuotaRuntimeCache(): Promise<void> {
  const authorizationQuota = await import('../gateway/quota/authorization-quota.service.js')
  authorizationQuota.clearAuthorizationQuotaCache()
}

async function clearServerClientIpPolicyCache(): Promise<void> {
  const policyCache = await import('../gateway/runtime/client-ip-policy-cache.service.js')
  try {
    await policyCache.reloadClientIpPolicyCacheLocal({ bypassSharedCache: true })
  } catch (error) {
    await policyCache.clearClientIpPolicyCacheLocal()
    logger.warn(errorLogFields(error, {
      event: 'client_ip_policy_snapshot_reload_failed'
    }), 'IP 封禁策略快照重载失败，已清空 server 本地快照')
  }
}

registerAuthorizationQuotaCacheInvalidator(notifyServerAuthorizationQuotaCacheInvalidated)

async function forwardUsageRecordsToWorker(items: unknown[]): Promise<void> {
  if (rejectRedisStreamLocalQueueForward('background_worker_usage_records', items.length)) return
  const backgroundIpc = await import('../background/background-ipc.js')
  const usageRecordQueue = await import('../gateway/usage/record-queue.service.js')
  const usageRecords = items.filter(usageRecordQueue.isUsageRecordInput)
  if (usageRecords.length > 0 && !backgroundIpc.sendUsageRecordsToWorker(usageRecords)) {
    logger.warn({
      event: 'db_service_usage_records_forward_failed',
      itemCount: usageRecords.length
    }, 'DB service 转发使用记录到后台 worker 失败')
  }
}

async function forwardAuditLogsToWorker(items: unknown[]): Promise<void> {
  if (rejectRedisStreamLocalQueueForward('background_worker_audit_logs', items.length)) return
  const backgroundIpc = await import('../background/background-ipc.js')
  const auditLogQueue = await import('../audit-logs/audit-log-queue.service.js')
  const auditLogs = items.filter(auditLogQueue.isAuditLogInput)
  if (auditLogs.length > 0 && !backgroundIpc.sendAuditLogsToWorker(auditLogs)) {
    logger.warn({
      event: 'db_service_audit_logs_forward_failed',
      itemCount: auditLogs.length
    }, 'DB service 转发审计日志到后台 worker 失败')
  }
}

async function forwardOperationLogsToWorker(items: unknown[]): Promise<void> {
  if (rejectRedisStreamLocalQueueForward('background_worker_operation_logs', items.length)) return
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

async function forwardPublicApiLogsToWorker(items: unknown[]): Promise<void> {
  if (rejectRedisStreamLocalQueueForward('background_worker_public_api_logs', items.length)) return
  const backgroundIpc = await import('../background/background-ipc.js')
  const publicApiLogQueue = await import('../public-api-logs/public-api-log-queue.service.js')
  const publicApiLogs = items.filter(publicApiLogQueue.isPublicApiLogInput)
  if (publicApiLogs.length > 0 && !backgroundIpc.sendPublicApiLogsToWorker(publicApiLogs)) {
    logger.warn({
      event: 'db_service_public_api_logs_forward_failed',
      itemCount: publicApiLogs.length
    }, 'DB service 转发公开接口日志到后台 worker 失败')
  }
}

async function forwardRecordMaintenanceJobsToWorker(items: unknown[]): Promise<void> {
  if (rejectRedisStreamLocalQueueForward('background_worker_record_maintenance', items.length)) return
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

function rejectRedisStreamLocalQueueForward(messageType: string, itemCount: number): boolean {
  if (runtimeConfig.queueDriver !== 'redis_stream') return false
  logger.error({
    event: 'db_service_redis_stream_local_queue_forward_rejected',
    messageType,
    itemCount
  }, 'Redis Stream queue driver 下禁止 DB service 通过父进程 IPC 转发记录类本地队列消息')
  return true
}

function assertDbServiceParentIpcAvailable(operation: string): void {
  if (runtimeConfig.runtimeMode === 'performance') {
    throw new Error(`高性能模式 DB service 缺少父进程 IPC，禁止回退当前进程本地 runtime：${operation}`)
  }
}

async function forwardAccountTestTasksToWorker(taskIds: unknown[]): Promise<void> {
  const backgroundIpc = await import('../background/background-ipc.js')
  const normalizedIds = taskIds.map(normalizedString).filter((taskId): taskId is string => Boolean(taskId))
  if (normalizedIds.length > 0 && !backgroundIpc.sendAccountTestTasksToWorker(normalizedIds)) {
    logger.warn({
      event: 'db_service_account_test_tasks_forward_failed',
      itemCount: normalizedIds.length
    }, 'DB service 转发账号测试任务到后台 worker 失败')
  }
}

async function forwardAccountTestCancelToWorker(taskId: string): Promise<void> {
  const backgroundIpc = await import('../background/background-ipc.js')
  const normalizedId = normalizedString(taskId)
  if (normalizedId && !backgroundIpc.sendAccountTestCancelToWorker(normalizedId)) {
    logger.warn({
      event: 'db_service_account_test_cancel_forward_failed',
      taskId: normalizedId
    }, 'DB service 转发账号测试取消到后台 worker 失败')
  }
}

async function forwardAccountHealthCheckTriggerToWorker(accountId: string, reason: unknown, traceId: unknown, sourceFence: unknown): Promise<void> {
  const backgroundIpc = await import('../background/background-ipc.js')
  const { isAccountHealthCheckTriggerReason, normalizeCodexSourceProbeFence } = await import('../accounts/account-health-check-trigger.js')
  const normalizedId = normalizedString(accountId)
  if (
    normalizedId
    && isAccountHealthCheckTriggerReason(reason)
    && !backgroundIpc.sendAccountHealthCheckTriggerToWorker(
      normalizedId,
      reason,
      typeof traceId === 'string' ? traceId : undefined,
      normalizeCodexSourceProbeFence(sourceFence)
    )
  ) {
    logger.warn({
      event: 'db_service_account_health_check_trigger_forward_failed',
      accountId: normalizedId
    }, 'DB service 转发账户健康检查触发消息失败，等待周期任务兜底')
  }
}

function normalizedString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  return text || undefined
}
