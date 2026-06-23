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
import type { DbServiceParentMessage } from './modules/db-service/db-service-types.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { createSystemApiApp } from './modules/system-api/system-api-app.js'
import { isCodexContextStateWriterPoolOperation } from './storage/codex-context-state-writer-pool.js'
import { datasetDatabasePath, statsDatabasePath, usageCatalogDatabasePath } from './storage/database.js'
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
let dbServiceRequestQueueDraining = false
let dbServiceRequestQueueDrainScheduled = false
let lastQueueWaitMs = 0
let maxQueueWaitMs = 0

async function startDbService(): Promise<void> {
  installProcessLogHandlers()
  startProcessEventLoopMonitor()
  startLogMaintenance()
  setRuntimeLogLineSink(() => {})
  setDbServiceQueueRuntimeProvider(buildDbServiceQueueRuntimeMetrics)

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
  queuedDbServiceRequests[priority].push({
    message,
    priority,
    enqueuedAt: Date.now()
  })
  scheduleDbServiceRequestQueueDrain()
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
    let queued = shiftNextDbServiceRequest()
    while (queued) {
      recordDbServiceQueueWait(queued)
      if (shouldDispatchDbServiceRequestConcurrently(queued.message)) {
        void respondToDbServiceRequest(queued.message)
      } else {
        await respondToDbServiceRequest(queued.message)
      }
      await yieldDbServiceRequestQueue()
      queued = shiftNextDbServiceRequest()
    }
  } finally {
    dbServiceRequestQueueDraining = false
    if (hasQueuedDbServiceRequests()) {
      scheduleDbServiceRequestQueueDrain()
    }
  }
}

function shiftNextDbServiceRequest(): QueuedDbServiceRequest | undefined {
  return queuedDbServiceRequests.high.shift()
    ?? queuedDbServiceRequests.normal.shift()
    ?? queuedDbServiceRequests.low.shift()
}

function hasQueuedDbServiceRequests(): boolean {
  return queuedDbServiceRequests.high.length > 0
    || queuedDbServiceRequests.normal.length > 0
    || queuedDbServiceRequests.low.length > 0
}

function shouldDispatchDbServiceRequestConcurrently(message: DbServiceRequestParentMessage): boolean {
  return isCodexContextStateWriterPoolOperation(message.operation)
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
    queuedHighRequestCount,
    queuedNormalRequestCount,
    queuedLowRequestCount,
    oldestQueuedMs: oldestQueuedDbServiceRequestMs(),
    lastQueueWaitMs,
    maxQueueWaitMs
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
