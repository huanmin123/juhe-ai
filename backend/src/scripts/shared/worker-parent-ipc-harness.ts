export function installWorkerParentIpcHarness(): () => void {
  const originalSend = process.send
  process.send = ((message: unknown, callback?: (error: Error | null) => void) => {
    callback?.(null)
    setImmediate(() => {
      void handleWorkerParentIpcMessage(message)
    })
    return true
  }) as typeof process.send

  return () => {
    if (originalSend) {
      process.send = originalSend
    } else {
      delete (process as NodeJS.Process & { send?: typeof process.send }).send
    }
  }
}

async function handleWorkerParentIpcMessage(message: unknown): Promise<void> {
  if (!isDbServiceRequestMessage(message)) {
    return
  }
  const processWithMessageEmitter = process as NodeJS.Process & {
    emit(event: 'message', message: unknown): boolean
  }
  try {
    const { runtimeConfig } = await import('../../config/runtime.js')
    const { handleDbServiceOperation } = await import('../../modules/db-service/db-service-handlers.js')
    const previousProcessRole = runtimeConfig.processRole
    try {
      runtimeConfig.processRole = 'db-service'
      const result = await handleDbServiceOperation(message.operation)
      processWithMessageEmitter.emit('message', {
        type: 'background_worker_db_service_response',
        requestId: message.requestId,
        ok: true,
        result
      })
    } finally {
      runtimeConfig.processRole = previousProcessRole
    }
  } catch (error) {
    processWithMessageEmitter.emit('message', {
      type: 'background_worker_db_service_response',
      requestId: message.requestId,
      ok: false,
      errorMessage: error instanceof Error ? error.message : String(error)
    })
  }
}

function isDbServiceRequestMessage(value: unknown): value is {
  type: 'background_worker_db_service_request'
  requestId: string
  operation: Parameters<typeof import('../../modules/db-service/db-service-handlers.js')['handleDbServiceOperation']>[0]
} {
  if (typeof value !== 'object' || value === null) {
    return false
  }
  const record = value as Record<string, unknown>
  return record.type === 'background_worker_db_service_request'
    && typeof record.requestId === 'string'
    && typeof record.operation === 'object'
    && record.operation !== null
}
