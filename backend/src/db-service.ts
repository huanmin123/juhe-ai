import type { Server } from 'node:http'

import { runtimeConfig } from './config/runtime.js'
import { dbServiceOperationPriority, type DbServiceOperationPriority } from './modules/db-service/db-service-request-priority.js'
import { handleDbServiceParentRuntimeMessage } from './modules/db-service/db-service-ipc.js'
import {
  handleDbServiceOperation,
  setDbServiceHttpEndpoint,
  setDbServiceQueueRuntimeProvider,
  type DbServiceQueueRuntimeMetrics
} from './modules/db-service/db-service-handlers.js'
import type { DbServiceOperation, DbServiceParentMessage } from './modules/db-service/db-service-types.js'
import { enqueueRuntimeLogLine } from './modules/runtime-logs/runtime-log-index-queue.service.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { createSystemApiApp } from './modules/system-api/system-api-app.js'
import { isCodexContextStateWriterPoolOperation } from './storage/codex-context-state-writer-pool.js'
import { datasetDatabasePath, getBusinessDatabase, statsDatabasePath, usageCatalogDatabasePath } from './storage/database.js'
import { errorLogFields, installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'
import { startProcessEventLoopMonitor } from './shared/process-event-loop-monitor.js'

const systemApiPrefix = '/__aisys__/api'
const publicApiPrefix = '/__aipublic__'

void startDbService().catch((error) => {
  logger.fatal(errorLogFields(error, {
    event: 'db_service_start_failed',
    host: runtimeConfig.dbServiceHttpHost,
    port: runtimeConfig.dbServiceHttpPort
  }), '数据库服务启动失败')
  process.exit(1)
})

interface DbServiceHttpEndpoint {
  server: Server
  host: string
  port: number
}

const queuedDbServiceRequests: Record<DbServiceOperationPriority, QueuedDbServiceRequest[]> = {
  high: [],
  normal: [],
  low: []
}
const dbServiceRequestQueueMaxRequests = 4000
const dbServiceRequestQueueMaxBytes = 128 * 1024 * 1024
const dbServiceHighDispatchesBeforeNormal = 8
const dbServiceHighDispatchesBeforeLow = 16
const dbServiceConcurrentRequestMaxActive = 8
const postgresConcurrentDbServiceOperationTypes = new Set<DbServiceOperation['type']>([
  'list_public_global_settings',
  'validate_gateway_api_key',
  'read_gateway_settings',
  'resolve_group_usage_access',
  'list_openai_accounts_for_group',
  'list_openai_accounts_for_group_result',
  'list_recoverable_unavailable_openai_accounts_for_group',
  'read_gateway_runtime',
  'list_provider_model_catalog',
  'check_api_key_quota',
  'check_authorization_quota',
  'check_authorization_quota_batch',
  'find_openai_oauth_account_for_refresh',
  'list_active_client_ip_policies',
  'list_active_response_inspection_policies',
  'status'
])
let queuedDbServiceRequestBytes = 0
let dbServiceRequestQueueDraining = false
let dbServiceRequestQueueDrainScheduled = false
let dbServiceRequestQueueExpiryTimer: NodeJS.Timeout | undefined
let dbServiceRequestQueueExpiryTimerAt = 0
let lastQueueWaitMs = 0
let maxQueueWaitMs = 0
let queueRejectedCount = 0
let queueExpiredCount = 0
let activeConcurrentRequestCount = 0
let maxActiveConcurrentRequestCount = 0
let highDispatchStreak = 0

async function startDbService(): Promise<void> {
  installProcessLogHandlers()
  startProcessEventLoopMonitor()
  startLogMaintenance()
  setRuntimeLogLineSink(runtimeConfig.queueDriver === 'redis_stream'
    ? (line, options) => enqueueRuntimeLogLine(line, options)
    : () => {})
  setDbServiceQueueRuntimeProvider(buildDbServiceQueueRuntimeMetrics)
  if (runtimeConfig.databaseDriver === 'sqlite') {
    getBusinessDatabase()
  }

  const httpEndpoint = await startDbServiceHttpServer()
  setDbServiceHttpEndpoint({ host: httpEndpoint.host, port: httpEndpoint.port })

  process.on('message', (message: unknown) => {
    void handleParentMessage(message)
  })

  sendDbServiceMessage({
    type: 'db_service_ready',
    pid: process.pid,
    httpHost: httpEndpoint.host,
    httpPort: httpEndpoint.port
  })

  logger.info({
    event: 'db_service_started',
    pid: process.pid,
    processRole: runtimeConfig.processRole,
    databasePath: runtimeConfig.databasePath,
    datasetDatabasePath: datasetDatabasePath(),
    usageCatalogDatabasePath: usageCatalogDatabasePath(),
    statsDatabasePath: statsDatabasePath(),
    httpHost: httpEndpoint.host,
    httpPort: httpEndpoint.port
  }, `数据库服务已启动，内部系统 API 监听 http://${httpEndpoint.host}:${httpEndpoint.port}`)
}

async function handleParentMessage(message: unknown): Promise<void> {
  if (handleDbServiceParentRuntimeMessage(message)) {
    return
  }

  if (!isDbServiceParentMessage(message)) {
    return
  }

  enqueueDbServiceRequest(message)
}

function enqueueDbServiceRequest(message: DbServiceRequestParentMessage): void {
  const priority = dbServiceOperationPriority(message.operation)
  const estimatedBytes = estimateDbServiceQueuedRequestBytes(message)
  purgeExpiredDbServiceRequests()
  if (!canQueueDbServiceRequest(estimatedBytes)) {
    queueRejectedCount += 1
    logger.warn({
      event: 'db_service_child_queue_full',
      operationType: message.operation.type,
      priority,
      queuedRequestCount: queuedDbServiceRequestCount(),
      queuedRequestBytes: queuedDbServiceRequestBytes,
      estimatedBytes,
      maxQueuedRequestCount: dbServiceRequestQueueMaxRequests,
      maxQueuedRequestBytes: dbServiceRequestQueueMaxBytes
    }, 'DB service 子进程请求队列已满，已拒绝本次请求')
    rejectDbServiceRequest(message, '本地数据库服务请求队列已满，请稍后重试')
    return
  }
  queuedDbServiceRequests[priority].push({
    message,
    priority,
    enqueuedAt: Date.now(),
    estimatedBytes
  })
  queuedDbServiceRequestBytes += estimatedBytes
  scheduleDbServiceRequestQueueDrain()
  scheduleDbServiceRequestQueueExpirySweep()
}

function scheduleDbServiceRequestQueueDrain(): void {
  if (dbServiceRequestQueueDraining || dbServiceRequestQueueDrainScheduled) {
    return
  }
  dbServiceRequestQueueDrainScheduled = true
  setImmediate(() => {
    void drainDbServiceRequestQueue()
  })
}

async function drainDbServiceRequestQueue(): Promise<void> {
  if (dbServiceRequestQueueDraining) {
    return
  }
  dbServiceRequestQueueDrainScheduled = false
  dbServiceRequestQueueDraining = true
  try {
    let queued = shiftNextDispatchableDbServiceRequest()
    while (queued) {
      if (isQueuedDbServiceRequestExpired(queued)) {
        queueExpiredCount += 1
        rejectDbServiceRequest(queued.message, '本地数据库服务请求已过期，请稍后重试')
        await yieldDbServiceRequestQueue()
        queued = shiftNextDispatchableDbServiceRequest()
        continue
      }
      recordDbServiceQueueWait(queued)
      if (shouldDispatchDbServiceRequestConcurrently(queued.message)) {
        activeConcurrentRequestCount += 1
        maxActiveConcurrentRequestCount = Math.max(maxActiveConcurrentRequestCount, activeConcurrentRequestCount)
        void respondToDbServiceRequest(queued.message).finally(() => {
          activeConcurrentRequestCount = Math.max(0, activeConcurrentRequestCount - 1)
          if (hasQueuedDbServiceRequests()) {
            scheduleDbServiceRequestQueueDrain()
          }
        })
      } else {
        await respondToDbServiceRequest(queued.message)
      }
      await yieldDbServiceRequestQueue()
      queued = shiftNextDispatchableDbServiceRequest()
    }
  } finally {
    dbServiceRequestQueueDraining = false
    if (hasDispatchableDbServiceRequests()) {
      scheduleDbServiceRequestQueueDrain()
    } else {
      scheduleDbServiceRequestQueueExpirySweep()
    }
  }
}

function scheduleDbServiceRequestQueueExpirySweep(): void {
  const nextDeadlineAt = nextQueuedDbServiceRequestDeadlineAt()
  if (!nextDeadlineAt) {
    clearDbServiceRequestQueueExpiryTimer()
    return
  }
  if (dbServiceRequestQueueExpiryTimer && dbServiceRequestQueueExpiryTimerAt <= nextDeadlineAt) {
    return
  }
  clearDbServiceRequestQueueExpiryTimer()
  dbServiceRequestQueueExpiryTimerAt = nextDeadlineAt
  const delayMs = Math.max(1, nextDeadlineAt - Date.now())
  dbServiceRequestQueueExpiryTimer = setTimeout(() => {
    dbServiceRequestQueueExpiryTimer = undefined
    dbServiceRequestQueueExpiryTimerAt = 0
    purgeExpiredDbServiceRequests()
    if (hasDispatchableDbServiceRequests()) {
      scheduleDbServiceRequestQueueDrain()
    } else {
      scheduleDbServiceRequestQueueExpirySweep()
    }
  }, delayMs)
  dbServiceRequestQueueExpiryTimer.unref()
}

function clearDbServiceRequestQueueExpiryTimer(): void {
  if (!dbServiceRequestQueueExpiryTimer) {
    dbServiceRequestQueueExpiryTimerAt = 0
    return
  }
  clearTimeout(dbServiceRequestQueueExpiryTimer)
  dbServiceRequestQueueExpiryTimer = undefined
  dbServiceRequestQueueExpiryTimerAt = 0
}

function nextQueuedDbServiceRequestDeadlineAt(): number {
  let nextDeadlineAt = 0
  for (const queue of Object.values(queuedDbServiceRequests)) {
    for (const request of queue) {
      const deadlineAtMs = request.message.deadlineAtMs
      if (typeof deadlineAtMs !== 'number' || !Number.isFinite(deadlineAtMs)) {
        continue
      }
      if (nextDeadlineAt === 0 || deadlineAtMs < nextDeadlineAt) {
        nextDeadlineAt = deadlineAtMs
      }
    }
  }
  return nextDeadlineAt
}

function purgeExpiredDbServiceRequests(): number {
  let purgedCount = 0
  for (const priority of Object.keys(queuedDbServiceRequests) as DbServiceOperationPriority[]) {
    const queue = queuedDbServiceRequests[priority]
    for (let index = queue.length - 1; index >= 0; index -= 1) {
      const request = queue[index]
      if (!request || !isQueuedDbServiceRequestExpired(request)) {
        continue
      }
      const [expired] = queue.splice(index, 1)
      if (!expired) {
        continue
      }
      queuedDbServiceRequestBytes = Math.max(0, queuedDbServiceRequestBytes - expired.estimatedBytes)
      queueExpiredCount += 1
      purgedCount += 1
      rejectDbServiceRequest(expired.message, '本地数据库服务请求已过期，请稍后重试')
    }
  }
  return purgedCount
}

function shiftNextDispatchableDbServiceRequest(): QueuedDbServiceRequest | undefined {
  for (const priority of dbServiceRequestPriorityOrder()) {
    const index = queuedDbServiceRequests[priority].findIndex(canShiftQueuedDbServiceRequest)
    if (index >= 0) {
      return shiftDbServiceRequestFromQueueAt(priority, index)
    }
  }
  return undefined
}

function shiftNextDbServiceRequest(): QueuedDbServiceRequest | undefined {
  for (const priority of dbServiceRequestPriorityOrder()) {
    const request = shiftDbServiceRequestFromQueue(priority)
    if (request) {
      return request
    }
  }
  return undefined
}

function dbServiceRequestPriorityOrder(): DbServiceOperationPriority[] {
  const lowReady = queuedDbServiceRequests.low.length > 0
  const normalReady = queuedDbServiceRequests.normal.length > 0
  if (lowReady && highDispatchStreak >= dbServiceHighDispatchesBeforeLow) {
    return ['low', 'high', 'normal']
  }
  if (normalReady && highDispatchStreak >= dbServiceHighDispatchesBeforeNormal) {
    return ['normal', 'high', 'low']
  }
  return ['high', 'normal', 'low']
}

function hasQueuedDbServiceRequests(): boolean {
  return queuedDbServiceRequests.high.length > 0
    || queuedDbServiceRequests.normal.length > 0
    || queuedDbServiceRequests.low.length > 0
}

function hasDispatchableDbServiceRequests(): boolean {
  return dbServiceRequestPriorityOrder().some((priority) => {
    return queuedDbServiceRequests[priority].some(canShiftQueuedDbServiceRequest)
  })
}

function canShiftQueuedDbServiceRequest(request: QueuedDbServiceRequest): boolean {
  if (isQueuedDbServiceRequestExpired(request)) {
    return true
  }
  return !shouldDispatchDbServiceRequestConcurrently(request.message)
    || activeConcurrentRequestCount < dbServiceConcurrentRequestMaxActive
}

function shouldDispatchDbServiceRequestConcurrently(message: DbServiceRequestParentMessage): boolean {
  if (runtimeConfig.databaseDriver === 'sqlite' && isCodexContextStateWriterPoolOperation(message.operation)) {
    return true
  }
  return runtimeConfig.databaseDriver === 'postgres'
    && postgresConcurrentDbServiceOperationTypes.has(message.operation.type)
}

async function yieldDbServiceRequestQueue(): Promise<void> {
  await new Promise<void>((resolve) => {
    setImmediate(resolve)
  })
}

async function respondToDbServiceRequest(message: DbServiceRequestParentMessage): Promise<void> {
  try {
    const result = await handleDbServiceOperation(message.operation)
    sendDbServiceMessage({
      type: 'db_service_response',
      requestId: message.requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendDbServiceMessage({
      type: 'db_service_response',
      requestId: message.requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

function rejectDbServiceRequest(message: DbServiceRequestParentMessage, errorMessage: string): void {
  sendDbServiceMessage({
    type: 'db_service_response',
    requestId: message.requestId,
    ok: false,
    errorMessage
  })
}

function sendDbServiceMessage(message: Record<string, unknown>): void {
  if (!process.send) {
    return
  }
  try {
    process.send(message, (error) => {
      if (error) {
        logger.error(errorLogFields(error, {
          event: 'db_service_child_ipc_send_failed',
          messageType: typeof message.type === 'string' ? message.type : undefined
        }), '数据库服务向父进程发送 IPC 消息失败，进程将退出等待 supervisor 重启')
        process.exit(1)
      }
    })
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'db_service_child_ipc_send_failed',
      messageType: typeof message.type === 'string' ? message.type : undefined
    }), '数据库服务向父进程发送 IPC 消息失败，进程将退出等待 supervisor 重启')
    process.exit(1)
  }
}

type DbServiceRequestParentMessage = Extract<DbServiceParentMessage, { type: 'db_service_request' }>

interface QueuedDbServiceRequest {
  message: DbServiceRequestParentMessage
  priority: DbServiceOperationPriority
  enqueuedAt: number
  estimatedBytes: number
}

function recordDbServiceQueueWait(request: QueuedDbServiceRequest): void {
  const waitMs = Math.max(0, Date.now() - request.enqueuedAt)
  lastQueueWaitMs = waitMs
  maxQueueWaitMs = Math.max(maxQueueWaitMs, waitMs)
}

function buildDbServiceQueueRuntimeMetrics(): DbServiceQueueRuntimeMetrics {
  const queuedHighRequestCount = queuedDbServiceRequests.high.length
  const queuedNormalRequestCount = queuedDbServiceRequests.normal.length
  const queuedLowRequestCount = queuedDbServiceRequests.low.length
  return {
    queuedRequestCount: queuedHighRequestCount + queuedNormalRequestCount + queuedLowRequestCount,
    queuedRequestBytes: queuedDbServiceRequestBytes,
    queuedHighRequestCount,
    queuedNormalRequestCount,
    queuedLowRequestCount,
    oldestQueuedMs: oldestQueuedDbServiceRequestMs(),
    lastQueueWaitMs,
    maxQueueWaitMs,
    queueRejectedCount,
    queueExpiredCount,
    activeConcurrentRequestCount,
    maxActiveConcurrentRequestCount
  }
}

function oldestQueuedDbServiceRequestMs(): number {
  let oldestAt = 0
  for (const queue of Object.values(queuedDbServiceRequests)) {
    for (const request of queue) {
      if (oldestAt === 0 || request.enqueuedAt < oldestAt) {
        oldestAt = request.enqueuedAt
      }
    }
  }
  return oldestAt === 0 ? 0 : Math.max(0, Date.now() - oldestAt)
}

function canQueueDbServiceRequest(estimatedBytes: number): boolean {
  return queuedDbServiceRequestCount() < dbServiceRequestQueueMaxRequests
    && queuedDbServiceRequestBytes + estimatedBytes <= dbServiceRequestQueueMaxBytes
}

function queuedDbServiceRequestCount(): number {
  return queuedDbServiceRequests.high.length
    + queuedDbServiceRequests.normal.length
    + queuedDbServiceRequests.low.length
}

function shiftDbServiceRequestFromQueue(priority: DbServiceOperationPriority): QueuedDbServiceRequest | undefined {
  return shiftDbServiceRequestFromQueueAt(priority, 0)
}

function shiftDbServiceRequestFromQueueAt(priority: DbServiceOperationPriority, index: number): QueuedDbServiceRequest | undefined {
  if (index < 0 || index >= queuedDbServiceRequests[priority].length) {
    return undefined
  }
  const [request] = queuedDbServiceRequests[priority].splice(index, 1)
  if (!request) {
    return undefined
  }
  queuedDbServiceRequestBytes = Math.max(0, queuedDbServiceRequestBytes - request.estimatedBytes)
  highDispatchStreak = priority === 'high' ? highDispatchStreak + 1 : 0
  return request
}

function isQueuedDbServiceRequestExpired(request: QueuedDbServiceRequest): boolean {
  const deadlineAtMs = request.message.deadlineAtMs
  return typeof deadlineAtMs === 'number' && Number.isFinite(deadlineAtMs) && deadlineAtMs <= Date.now()
}

function estimateDbServiceQueuedRequestBytes(message: DbServiceRequestParentMessage): number {
  try {
    return Buffer.byteLength(JSON.stringify(message.operation), 'utf8') + message.requestId.length + 256
  } catch {
    return 1024
  }
}

function isDbServiceParentMessage(message: unknown): message is DbServiceRequestParentMessage {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return false
  }
  const record = message as Partial<DbServiceParentMessage>
  return record.type === 'db_service_request'
    && typeof record.requestId === 'string'
    && typeof record.operation === 'object'
    && record.operation !== null
}

async function startDbServiceHttpServer(): Promise<DbServiceHttpEndpoint> {
  const app = createSystemApiApp({ systemApiPrefix, publicApiPrefix, trustProxy: 1 })
  const host = runtimeConfig.dbServiceHttpHost
  const configuredPort = runtimeConfig.dbServiceHttpPort
  const server = app.listen(configuredPort, host)

  return await new Promise<DbServiceHttpEndpoint>((resolve, reject) => {
    const handleError = (error: Error): void => {
      reject(error)
    }
    server.once('error', handleError)
    server.once('listening', () => {
      server.off('error', handleError)
      const address = server.address()
      if (!address || typeof address === 'string') {
        reject(new Error('DB service 内部 HTTP 监听地址无效'))
        return
      }
      resolve({
        server,
        host,
        port: address.port
      })
    })
  })
}
