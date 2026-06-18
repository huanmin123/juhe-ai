import type { Server } from 'node:http'

import { runtimeConfig } from './config/runtime.js'
import { dbServiceOperationPriority, type DbServiceOperationPriority } from './modules/db-service/db-service-request-priority.js'
import { handleDbServiceParentRuntimeMessage } from './modules/db-service/db-service-ipc.js'
import { handleDbServiceOperation, setDbServiceHttpEndpoint } from './modules/db-service/db-service-handlers.js'
import type { DbServiceParentMessage } from './modules/db-service/db-service-types.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { createSystemApiApp } from './modules/system-api/system-api-app.js'
import { datasetDatabasePath, statsDatabasePath } from './storage/database.js'
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

const queuedDbServiceRequests: Record<DbServiceOperationPriority, DbServiceRequestParentMessage[]> = {
  high: [],
  normal: [],
  low: []
}
let dbServiceRequestQueueDraining = false
let dbServiceRequestQueueDrainScheduled = false

async function startDbService(): Promise<void> {
  installProcessLogHandlers()
  startProcessEventLoopMonitor()
  startLogMaintenance()
  setRuntimeLogLineSink(() => {})

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
  queuedDbServiceRequests[priority].push(message)
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
    let message = shiftNextDbServiceRequest()
    while (message) {
      await respondToDbServiceRequest(message)
      await yieldDbServiceRequestQueue()
      message = shiftNextDbServiceRequest()
    }
  } finally {
    dbServiceRequestQueueDraining = false
    if (hasQueuedDbServiceRequests()) {
      scheduleDbServiceRequestQueueDrain()
    }
  }
}

function shiftNextDbServiceRequest(): DbServiceRequestParentMessage | undefined {
  return queuedDbServiceRequests.high.shift()
    ?? queuedDbServiceRequests.normal.shift()
    ?? queuedDbServiceRequests.low.shift()
}

function hasQueuedDbServiceRequests(): boolean {
  return queuedDbServiceRequests.high.length > 0
    || queuedDbServiceRequests.normal.length > 0
    || queuedDbServiceRequests.low.length > 0
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
