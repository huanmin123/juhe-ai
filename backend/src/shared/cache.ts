import { LRUCache } from 'lru-cache'

export interface AppCacheOptions<K extends {}, V extends {}> {
  name: string
  max: number
  ttlMs: number
  updateAgeOnGet?: boolean
}

export interface AppCache<K extends {}, V extends {}> {
  readonly name: string
  get(key: K): V | undefined
  set(key: K, value: V, options?: { ttlMs?: number }): void
  delete(key: K): void
  clear(): void
  values(): IterableIterator<V>
  entries(): IterableIterator<[K, V]>
}

const caches = new Set<AppCache<{}, {}>>()

export function createAppCache<K extends {}, V extends {}>(options: AppCacheOptions<K, V>): AppCache<K, V> {
  const store = new LRUCache<K, V>({
    max: options.max,
    ttl: options.ttlMs,
    updateAgeOnGet: options.updateAgeOnGet ?? false
  })
  const cache: AppCache<K, V> = {
    name: options.name,
    get: (key) => store.get(key),
    set: (key, value, setOptions) => {
      store.set(key, value, setOptions?.ttlMs === undefined ? undefined : { ttl: setOptions.ttlMs })
    },
    delete: (key) => {
      store.delete(key)
    },
    clear: () => {
      store.clear()
    },
    values: () => store.values(),
    entries: () => store.entries()
  }
  caches.add(cache as AppCache<{}, {}>)
  return cache
}

export function clearAllAppCaches(): void {
  for (const cache of caches) {
    cache.clear()
  }
}
