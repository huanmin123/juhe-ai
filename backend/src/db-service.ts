import type { Server } from 'node:http'

import { runtimeConfig } from './config/runtime.js'
import { dbServiceOperationAccessMode, shouldQueueDbServiceOperationForDriver } from './modules/db-service/db-service-operation-access-mode.js'
import { dbServiceOperationPriority, type DbServiceOperationPriority } from './modules/db-service/db-service-request-priority.js'
import { handleDbServiceParentRuntimeMessage } from './modules/db-service/db-service-ipc.js'
import {
  handleDbServiceOperation,
  setDbServiceHttpEndpoint,
  setDbServiceQueueRuntimeProvider,
  type DbServiceQueueRuntimeMetrics
} from './modules/db-service/db-service-handlers.js'
import type { DbServiceParentMessage } from './modules/db-service/db-service-types.js'
import { enqueueRuntimeLogLine } from './modules/runtime-logs/runtime-log-index-queue.service.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { createSystemApiApp } from './modules/system-api/system-api-app.js'
import { shutdownChatGenerationRegistry } from './modules/chat/chat-generation-runtime.js'
import { isCodexContextStateWriterPoolOperation } from './storage/codex-context-state-writer-pool.js'
import { closeStorageDatabases, datasetDatabasePath, getBusinessDatabase, statsDatabasePath, usageCatalogDatabasePath } from './storage/database.js'
import { closeLogger, errorLogFields, installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'
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
let dbServiceStopping = false
let dbServiceShutdownPromise: Promise<void> | undefined

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
  const { initializePageDataChangeRuntime } = await import('./modules/page-data/page-data-change.runtime.js')
  await initializePageDataChangeRuntime()

  const httpEndpoint = await startDbServiceHttpServer()
  setDbServiceHttpEndpoint({ host: httpEndpoint.host, port: httpEndpoint.port })

  process.on('message', (message: unknown) => {
    void handleParentMessage(message)
  })
  process.on('SIGINT', () => void requestDbServiceShutdown(httpEndpoint, 0))
  process.on('SIGTERM', () => void requestDbServiceShutdown(httpEndpoint, 0))

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

function requestDbServiceShutdown(httpEndpoint: DbServiceHttpEndpoint, exitCode: number): Promise<void> {
  if (dbServiceShutdownPromise) {
    logger.info({ event: 'db_service_shutdown_already_running' }, 'DB service 已在退出，复用现有退出流程')
    return dbServiceShutdownPromise
  }
  dbServiceStopping = true
  dbServiceShutdownPromise = shutdownDbService(httpEndpoint, exitCode)
  return dbServiceShutdownPromise
}

async function shutdownDbService(httpEndpoint: DbServiceHttpEndpoint, exitCode: number): Promise<void> {
  const httpClosed = new Promise<void>((resolve, reject) => {
    httpEndpoint.server.close((error) => error ? reject(error) : resolve())
    httpEndpoint.server.closeIdleConnections?.()
  }).catch((error) => {
    logger.error(errorLogFields(error, { event: 'db_service_http_shutdown_failed' }), 'DB service 退出时关闭内部 HTTP 服务失败')
  })
  rejectQueuedDbServiceRequestsForShutdown()
  const requestsDrained = await waitForActiveDbServiceRequests(3_000)
  if (!requestsDrained) {
    logger.warn({ event: 'db_service_request_drain_timeout', activeConcurrentRequestCount, dbServiceRequestQueueDraining }, 'DB service 退出时等待在途请求超时')
  }
  try {
    await shutdownChatGenerationRegistry({ timeoutMs: 7_000 })
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'db_service_chat_generation_shutdown_failed' }), 'DB service 退出时排空 AI 问答生成任务失败')
  }
  await Promise.race([httpClosed, new Promise<void>((resolve) => setTimeout(resolve, 2_000))])
  try {
    closeStorageDatabases()
  } catch (error) {
    logger.error(errorLogFields(error, { event: 'db_service_storage_shutdown_failed' }), 'DB service 退出时关闭数据库连接失败')
  }
  await closeLogger()
  process.exit(exitCode)
}

function rejectQueuedDbServiceRequestsForShutdown(): void {
  clearDbServiceRequestQueueExpiryTimer()
  for (const priority of Object.keys(queuedDbServiceRequests) as DbServiceOperationPriority[]) {
    for (const request of queuedDbServiceRequests[priority].splice(0)) {
      rejectDbServiceRequest(request.message, '本地数据库服务正在退出')
    }
  }
  queuedDbServiceRequestBytes = 0
}

async function waitForActiveDbServiceRequests(timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + Math.max(0, timeoutMs)
  while ((activeConcurrentRequestCount > 0 || dbServiceRequestQueueDraining) && Date.now() < deadline) {
    await new Promise<void>((resolve) => setTimeout(resolve, 10))
  }
  return activeConcurrentRequestCount === 0 && !dbServiceRequestQueueDraining
}

async function handleParentMessage(message: unknown): Promise<void> {
  if (handleDbServiceParentRuntimeMessage(message)) {
    return
  }

  if (!isDbServiceParentMessage(message)) {
    return
  }

  if (dbServiceStopping) {
    rejectDbServiceRequest(message, '本地数据库服务正在退出')
    return
  }

  if (isDbServiceRequestMessageExpired(message)) {
    queueExpiredCount += 1
    rejectDbServiceRequest(message, '本地数据库服务请求已过期，请稍后重试')
    return
  }

  if (shouldQueueDbServiceRequest(message)) {
    enqueueDbServiceRequest(message)
    return
  }

  dispatchDbServiceRequestImmediately(message)
}

function shouldQueueDbServiceRequest(message: DbServiceRequestParentMessage): boolean {
  return shouldQueueDbServiceOperationForDriver(message.operation, runtimeConfig.databaseDriver)
}

function dispatchDbServiceRequestImmediately(message: DbServiceRequestParentMessage): void {
  activeConcurrentRequestCount += 1
  maxActiveConcurrentRequestCount = Math.max(maxActiveConcurrentRequestCount, activeConcurrentRequestCount)
  void respondToDbServiceRequest(message).finally(() => {
    activeConcurrentRequestCount = Math.max(0, activeConcurrentRequestCount - 1)
    if (hasDispatchableDbServiceRequests()) {
      scheduleDbServiceRequestQueueDrain()
    }
  })
}

function enqueueDbServiceRequest(message: DbServiceRequestParentMessage): void {
  const priority = dbServiceRequestPriorityForMessage(message)
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

function dbServiceRequestPriorityForMessage(message: DbServiceRequestParentMessage): DbServiceOperationPriority {
  return normalizeDbServiceRequestPriority(message.priority) ?? dbServiceOperationPriority(message.operation)
}

function normalizeDbServiceRequestPriority(value: unknown): DbServiceOperationPriority | undefined {
  if (value === 'high' || value === 'normal' || value === 'low') {
    return value
  }
  return undefined
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
  const accessMode = dbServiceOperationAccessMode(message.operation)
  return runtimeConfig.databaseDriver === 'postgres'
    || accessMode === 'read'
    || accessMode === 'runtime'
}

async function yieldDbServiceRequestQueue(): Promise<void> {
  await new Promise<void>((resolve) => {
    setImmediate(resolve)
  })
}

async function respondToDbServiceRequest(message: DbServiceRequestParentMessage): Promise<void> {
  const startedAt = Date.now()
  const operationType = typeof message.operation.type === 'string' ? message.operation.type : 'unknown'
  const traceId = 'traceId' in message.operation && typeof message.operation.traceId === 'string'
    ? message.operation.traceId
    : undefined
  logger.info({
    event: 'db_service.request.start',
    service: 'juhe-ai',
    role: 'db-service',
    traceId,
    requestId: message.requestId,
    operation: operationType,
    databaseDriver: runtimeConfig.databaseDriver
  }, 'DB service 请求开始')
  try {
    const result = await handleDbServiceOperation(message.operation)
    logger.info({
      event: 'db_service.request.complete',
      service: 'juhe-ai',
      role: 'db-service',
      traceId,
      requestId: message.requestId,
      operation: operationType,
      outcome: 'success',
      durationMs: Date.now() - startedAt
    }, 'DB service 请求完成')
    sendDbServiceMessage({
      type: 'db_service_response',
      requestId: message.requestId,
      ok: true,
      result
    })
  } catch (error) {
    logger.error(errorLogFields(error, {
      event: 'db_service.request.failed',
      service: 'juhe-ai',
      role: 'db-service',
      traceId,
      requestId: message.requestId,
      operation: operationType,
      outcome: 'unexpected_failure',
      durationMs: Date.now() - startedAt
    }), 'DB service 请求失败')
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
  return isDbServiceRequestMessageExpired(request.message)
}

function isDbServiceRequestMessageExpired(message: DbServiceRequestParentMessage): boolean {
  const deadlineAtMs = message.deadlineAtMs
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
