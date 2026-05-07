import { runtimeConfig } from './config/runtime.js'
import { handleDbServiceOperation } from './modules/db-service/db-service-handlers.js'
import type { DbServiceParentMessage } from './modules/db-service/db-service-types.js'
import { setRuntimeLogLineSink } from './modules/runtime-logs/runtime-log-stream.js'
import { getDatabase } from './storage/database.js'
import { installProcessLogHandlers, logger, startLogMaintenance } from './shared/logger.js'

getDatabase()
installProcessLogHandlers()
startLogMaintenance()
setRuntimeLogLineSink(() => {})

process.on('message', (message: unknown) => {
  void handleParentMessage(message)
})

sendDbServiceMessage({
  type: 'db_service_ready',
  pid: process.pid
})

logger.info({
  event: 'db_service_started',
  pid: process.pid,
  processRole: runtimeConfig.processRole,
  databasePath: runtimeConfig.databasePath
}, '数据库服务已启动')

async function handleParentMessage(message: unknown): Promise<void> {
  if (!isDbServiceParentMessage(message)) {
    return
  }

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
  process.send(message)
}

function isDbServiceParentMessage(message: unknown): message is DbServiceParentMessage {
  if (typeof message !== 'object' || message === null || Array.isArray(message)) {
    return false
  }
  const record = message as Partial<DbServiceParentMessage>
  return record.type === 'db_service_request'
    && typeof record.requestId === 'string'
    && typeof record.operation === 'object'
    && record.operation !== null
}
