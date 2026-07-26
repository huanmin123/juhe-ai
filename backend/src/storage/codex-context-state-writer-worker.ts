import { runtimeConfig } from '../config/runtime.js'
import { logger } from '../shared/logger.js'
import {
  cleanupExpiredCodexContextStates,
  cleanupExpiredCodexContextStatesInShard,
  readCodexContextCompactStateRow,
  readCodexContextResponseStateRow,
  saveCodexContextCompactStateIndexRows,
  saveCodexContextCompactStateIndexRow,
  saveCodexContextResponseStateIndexRows,
  saveCodexContextResponseStateIndexRow,
  settleCodexContextStorageCleanup,
  touchCodexContextCompactStateRows,
  touchCodexContextCompactStateRow,
  touchCodexContextSessionStates,
  touchCodexContextResponseStateRows,
  touchCodexContextSessionState,
  upsertCodexContextCompactSessionIndexes,
  upsertCodexContextCompactSessionIndex,
  upsertCodexContextResponseSessionIndexes,
  upsertCodexContextResponseSessionIndex
} from './codex-context-state.repository.js'
import { closeStorageDatabases } from './database.js'
import type {
  CodexContextStateWriterOperation,
  CodexContextStateWriterWorkerMessage,
  CodexContextStateWriterWorkerResponse
} from './codex-context-state-writer-pool.types.js'
import { serializeCodexContextWriterFatalDiagnostic } from './codex-context-state-writer-diagnostics.js'

if (runtimeConfig.processRole !== 'db-service') {
  runtimeConfig.processRole = 'db-service'
}
logger.level = runtimeConfig.log.consoleEnabled ? logger.level : 'silent'
let fatalExitStarted = false

process.once('uncaughtException', (error) => exitAfterFatalDiagnostic('uncaught_exception', error))
process.once('unhandledRejection', (reason) => exitAfterFatalDiagnostic('unhandled_rejection', reason))

process.on('message', (message: CodexContextStateWriterWorkerMessage) => {
  try {
    const result = handleCodexContextStateWriterOperation(message.operation)
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

function exitAfterFatalDiagnostic(kind: string, error: unknown): void {
  if (fatalExitStarted) return
  fatalExitStarted = true
  const line = serializeCodexContextWriterFatalDiagnostic({
    kind,
    error,
    maxBytes: 8 * 1024,
    secrets: [runtimeConfig.secret]
  })
  closeStorageDatabases()
  const forceExit = setTimeout(() => process.exit(1), 100)
  forceExit.unref()
  try {
    process.stderr.write(line, () => process.exit(1))
  } catch {
    process.exit(1)
  }
}

function handleCodexContextStateWriterOperation(operation: CodexContextStateWriterOperation): unknown {
  switch (operation.type) {
    case 'save_response_session':
      upsertCodexContextResponseSessionIndex(operation.input)
      return operation.input
    case 'save_response_sessions':
      upsertCodexContextResponseSessionIndexes(operation.inputs)
      return { saved: operation.inputs.length }
    case 'save_response_row':
      return saveCodexContextResponseStateIndexRow(operation.input)
    case 'save_response_rows':
      saveCodexContextResponseStateIndexRows(operation.inputs)
      return { saved: operation.inputs.length }
    case 'save_compact_session':
      upsertCodexContextCompactSessionIndex(operation.input)
      return operation.input
    case 'save_compact_sessions':
      upsertCodexContextCompactSessionIndexes(operation.inputs)
      return { saved: operation.inputs.length }
    case 'save_compact_row':
      return saveCodexContextCompactStateIndexRow(operation.input)
    case 'save_compact_rows':
      saveCodexContextCompactStateIndexRows(operation.inputs)
      return { saved: operation.inputs.length }
    case 'read_response_row':
      return readCodexContextResponseStateRow(operation.responseId)
    case 'read_compact_row':
      return readCodexContextCompactStateRow(operation.compactId)
    case 'touch_session':
      touchCodexContextSessionState(operation.sessionId, operation.now, operation.refreshExpiresAt)
      return { touched: true }
    case 'touch_sessions':
      touchCodexContextSessionStates(operation.touches)
      return { touched: operation.touches.length }
    case 'touch_response_rows':
      touchCodexContextResponseStateRows(operation.responseIds, operation.now, operation.refreshExpiresAt)
      return { touched: operation.responseIds.length }
    case 'touch_compact_row':
      touchCodexContextCompactStateRow(operation.compactId, operation.now, operation.refreshExpiresAt)
      return { touched: true }
    case 'touch_compact_rows':
      touchCodexContextCompactStateRows(operation.touches)
      return { touched: operation.touches.length }
    case 'cleanup_expired_states':
      return cleanupExpiredCodexContextStates({
        expiredBefore: operation.expiredBefore,
        limit: operation.limit
      })
    case 'cleanup_expired_states_shard':
      return cleanupExpiredCodexContextStatesInShard({
        shardIndex: operation.shardIndex,
        expiredBefore: operation.expiredBefore,
        limit: operation.limit
      })
    case 'settle_storage_cleanup':
      return settleCodexContextStorageCleanup(operation)
    default:
      return assertNever(operation)
  }
}

function sendResponse(message: CodexContextStateWriterWorkerResponse): void {
  if (typeof process.send === 'function') {
    process.send(message)
  }
}

function assertNever(value: never): never {
  throw new Error(`未知 Responses bridge state writer 操作：${JSON.stringify(value)}`)
}
