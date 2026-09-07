import { runtimeConfig } from '../../config/runtime.js'
import { createPostgresRedisStorageRuntime } from './postgres-redis-runtime.js'
import { createSqliteMemoryStorageRuntime } from './sqlite-memory-runtime.js'
import type { StorageRuntime } from './storage-runtime.js'

let storageRuntime: StorageRuntime | undefined

export function getStorageRuntime(): StorageRuntime {
  storageRuntime ??= createStorageRuntime()
  return storageRuntime
}

export function createStorageRuntime(): StorageRuntime {
  if (runtimeConfig.runtimeMode === 'performance') {
    return createPostgresRedisStorageRuntime()
  }
  return createSqliteMemoryStorageRuntime()
}

export type {
  StorageAdapterStatus,
  StorageDriverDescriptor,
  StoragePortDescriptor,
  StorageRuntime,
  StorageRuntimeMode
} from './storage-runtime.js'
