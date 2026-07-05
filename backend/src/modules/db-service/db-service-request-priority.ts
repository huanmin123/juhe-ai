import type { DbServiceOperation, DbServiceRequestPriority } from './db-service-types.js'
import { dbServiceOperationAccessMode } from './db-service-operation-access-mode.js'

export type DbServiceOperationPriority = DbServiceRequestPriority

export function dbServiceOperationPriority(operation: DbServiceOperation): DbServiceOperationPriority {
  return dbServiceOperationAccessMode(operation) === 'maintenance' ? 'low' : 'high'
}
