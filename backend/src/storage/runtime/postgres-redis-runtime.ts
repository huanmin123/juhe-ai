import { storagePortDescriptors, type StorageRuntime } from './storage-runtime.js'

export function createPostgresRedisStorageRuntime(): StorageRuntime {
  return {
    mode: 'performance',
    drivers: {
      database: 'postgres',
      cache: 'redis',
      runtimeState: 'redis',
      queue: 'redis_stream'
    },
    ports: storagePortDescriptors
  }
}
