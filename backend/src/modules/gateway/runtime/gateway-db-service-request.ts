import { runtimeConfig } from '../../../config/runtime.js'
import { requestDbService } from '../../db-service/db-service-ipc.js'
import type { DbServiceOperation, DbServiceOperationResult } from '../../db-service/db-service-types.js'

export async function requestGatewayDbService<T extends DbServiceOperation>(
  operation: T,
  options: { timeoutMs?: number } = {}
): Promise<DbServiceOperationResult<T>> {
  if (runtimeConfig.processRole === 'worker') {
    const { requestBackgroundWorkerDbService } = await import('../../background/background-ipc.js')
    const result = await requestBackgroundWorkerDbService(operation, options.timeoutMs)
    if (result === undefined) {
      throw new Error(`worker 角色无法通过后台 IPC 访问 DB service：${operation.type}`)
    }
    return result
  }
  return await requestDbService(operation, options)
}
