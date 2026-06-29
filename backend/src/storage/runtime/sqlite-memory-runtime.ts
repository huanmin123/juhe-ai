import { storagePortDescriptors, type StorageRuntime } from './storage-runtime.js'

export function createSqliteMemoryStorageRuntime(): StorageRuntime {
  return {
    mode: 'standalone',
    drivers: {
      database: 'sqlite',
      cache: 'memory',
      runtimeState: 'memory',
      queue: 'memory'
    },
    ports: storagePortDescriptors
  }
}
