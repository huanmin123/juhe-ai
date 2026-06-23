import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'
import { closeStorageDatabases } from './database.js'
import { writeUsageRecordShardRows } from './usage-record-shards.js'
import type {
  UsageRecordWriterOperation,
  UsageRecordWriterWorkerMessage,
  UsageRecordWriterWorkerResponse
} from './usage-record-writer-pool.types.js'

runtimeConfig.processRole = 'worker'
runtimeConfig.workerRole = 'ingest-worker'
logger.level = runtimeConfig.log.consoleEnabled ? logger.level : 'silent'

process.on('message', (message: UsageRecordWriterWorkerMessage) => {
  try {
    const result = handleUsageRecordWriterOperation(message.operation)
    sendResponse({
      requestId: message.requestId,
      ok: true,
      result
    })
  } catch (error) {
    sendResponse({
      requestId: message.requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
})

process.once('exit', () => {
  closeStorageDatabases()
})

process.once('disconnect', () => {
  closeStorageDatabases()
  process.exit(0)
})

function handleUsageRecordWriterOperation(operation: UsageRecordWriterOperation): unknown {
  if (operation.type === 'write_usage_records') {
    return writeUsageRecordShardRows(operation.location, operation.rows, { registerLocation: false })
  }
  throw new Error(`未知 usage record writer 操作：${JSON.stringify(operation)}`)
}

function sendResponse(message: UsageRecordWriterWorkerResponse): void {
  if (typeof process.send === 'function') {
    process.send(message)
  }
}
