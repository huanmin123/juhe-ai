import { LRUCache } from 'lru-cache'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import { getRedisClient, type RedisCommandClient } from './redis-client.js'

export interface AppCacheOptions<K extends {}, V extends {}> {
  name: string
  max: number
  ttlMs: number
  updateAgeOnGet?: boolean
  dispose?: (value: V, key: K) => void
  onClear?: () => void
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

export interface SharedJsonCacheOptions<V extends {}> {
  name: string
  max: number
  ttlMs: number
  version?: string
}

export interface SharedJsonCache<V extends {}> {
  readonly name: string
  get(key: string): Promise<V | undefined>
  set(key: string, value: V, options?: { ttlMs?: number }): Promise<void>
  delete(key: string): Promise<void>
  clear(): Promise<void>
}

export function clearSharedJsonCacheInBackground<V extends {}>(
  cache: SharedJsonCache<V>,
  event: string,
  message: string
): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  cache.clear().catch((error) => {
    logger.warn(errorLogFields(error, {
      event,
      cacheName: cache.name
    }), message)
  })
}

const caches = new Set<AppCache<{}, {}>>()

export function createAppCache<K extends {}, V extends {}>(options: AppCacheOptions<K, V>): AppCache<K, V> {
  let store = createStore(options)
  const dropStoreIfDisabled = () => {
    if (canUseProcessLocalAppCacheAsFactSource()) return
    if (store.size > 0) {
      store = createStore(options)
      options.onClear?.()
    }
  }
  const cache: AppCache<K, V> = {
    name: options.name,
    get: (key) => {
      dropStoreIfDisabled()
      return canUseProcessLocalAppCacheAsFactSource() ? store.get(key) : undefined
    },
    set: (key, value, setOptions) => {
      dropStoreIfDisabled()
      if (!canUseProcessLocalAppCacheAsFactSource()) return
      store.set(key, value, setOptions?.ttlMs === undefined ? undefined : { ttl: setOptions.ttlMs })
    },
    delete: (key) => {
      dropStoreIfDisabled()
      if (!canUseProcessLocalAppCacheAsFactSource()) return
      store.delete(key)
    },
    clear: () => {
      store = createStore(options)
      options.onClear?.()
    },
    values: () => {
      dropStoreIfDisabled()
      return canUseProcessLocalAppCacheAsFactSource() ? store.values() : emptyIterator<V>()
    },
    entries: () => {
      dropStoreIfDisabled()
      return canUseProcessLocalAppCacheAsFactSource() ? store.entries() : emptyIterator<[K, V]>()
    }
  }
  caches.add(cache as AppCache<{}, {}>)
  return cache
}

export function createProcessLocalResourceCache<K extends {}, V extends {}>(options: AppCacheOptions<K, V>): AppCache<K, V> {
  let store = createStore(options)
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
      store = createStore(options)
      options.onClear?.()
    },
    values: () => store.values(),
    entries: () => store.entries()
  }
  caches.add(cache as AppCache<{}, {}>)
  return cache
}

function emptyIterator<T>(): IterableIterator<T> {
  return [][Symbol.iterator]() as IterableIterator<T>
}

function createStore<K extends {}, V extends {}>(options: AppCacheOptions<K, V>): LRUCache<K, V> {
  return new LRUCache<K, V>({
    max: options.max,
    ttl: options.ttlMs,
    updateAgeOnGet: options.updateAgeOnGet ?? false,
    dispose: options.dispose
      ? (value, key) => {
          options.dispose?.(value, key)
        }
      : undefined
  })
}

export function clearAllAppCaches(): void {
  for (const cache of caches) {
    cache.clear()
  }
}

export function canUseProcessLocalAppCacheAsFactSource(): boolean {
  return runtimeConfig.cacheDriver !== 'redis'
}

export function createSharedJsonCache<V extends {}>(options: SharedJsonCacheOptions<V>): SharedJsonCache<V> {
  return new DriverSharedJsonCache(options)
}

class DriverSharedJsonCache<V extends {}> implements SharedJsonCache<V> {
  readonly name: string
  private readonly memoryCache: MemorySharedJsonCache<V>
  private redisCache: RedisSharedJsonCache<V> | undefined

  constructor(private readonly options: SharedJsonCacheOptions<V>) {
    this.name = options.name
    this.memoryCache = new MemorySharedJsonCache(options)
  }

  async get(key: string): Promise<V | undefined> {
    return this.cache().get(key)
  }

  async set(key: string, value: V, options?: { ttlMs?: number }): Promise<void> {
    await this.cache().set(key, value, options)
  }

  async delete(key: string): Promise<void> {
    await this.cache().delete(key)
  }

  async clear(): Promise<void> {
    await this.cache().clear()
  }

  private cache(): SharedJsonCache<V> {
    if (runtimeConfig.cacheDriver !== 'redis') return this.memoryCache
    this.redisCache ??= new RedisSharedJsonCache(this.options)
    return this.redisCache
  }
}

class MemorySharedJsonCache<V extends {}> implements SharedJsonCache<V> {
  readonly name: string
  private store: LRUCache<string, V>

  constructor(private readonly options: SharedJsonCacheOptions<V>) {
    this.name = options.name
    this.store = createSharedStore(options)
  }

  async get(key: string): Promise<V | undefined> {
    return this.store.get(key)
  }

  async set(key: string, value: V, options?: { ttlMs?: number }): Promise<void> {
    this.store.set(key, value, options?.ttlMs === undefined ? undefined : { ttl: normalizeTtlMs(options.ttlMs) })
  }

  async delete(key: string): Promise<void> {
    this.store.delete(key)
  }

  async clear(): Promise<void> {
    this.store = createSharedStore(this.options)
  }
}

class RedisSharedJsonCache<V extends {}> implements SharedJsonCache<V> {
  readonly name: string
  private readonly keyPrefix: string
  private readonly indexKeyPrefix: string
  private readonly versionKey: string

  constructor(private readonly options: SharedJsonCacheOptions<V>) {
    const cacheUrl = runtimeConfig.redis.cacheUrl
    if (!cacheUrl) {
      throw new Error('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置')
    }
    this.name = options.name
    const safeName = sanitizeRedisKeyPart(options.name)
    this.keyPrefix = `juhe-ai:cache:${safeName}:`
    this.indexKeyPrefix = `juhe-ai:cache-index:${safeName}:`
    this.versionKey = `juhe-ai:cache-version:${safeName}`
  }

  async get(key: string): Promise<V | undefined> {
    const location = await this.redisLocation(key)
    const rawValue = await (await this.client()).get(location.key)
    if (rawValue === null) return undefined
    try {
      return JSON.parse(rawValue) as V
    } catch {
      await this.delete(key)
      return undefined
    }
  }

  async set(key: string, value: V, options?: { ttlMs?: number }): Promise<void> {
    const ttlMs = normalizeTtlMs(options?.ttlMs ?? this.options.ttlMs)
    const location = await this.redisLocation(key)
    const client = await this.client()
    await client.set(
      location.key,
      JSON.stringify(value),
      { PX: ttlMs }
    )
    await this.trackKeyAndTrim(client, location.indexKey, location.key, ttlMs)
  }

  async delete(key: string): Promise<void> {
    const location = await this.redisLocation(key)
    const client = await this.client()
    await client.del(location.key)
    await client.sendCommand(['ZREM', location.indexKey, location.key])
  }

  async clear(): Promise<void> {
    await (await this.client()).set(this.versionKey, nextCacheVersion(), { PX: 30 * 24 * 60 * 60 * 1000 })
  }

  private client(): Promise<RedisCommandClient> {
    const cacheUrl = runtimeConfig.redis.cacheUrl
    if (!cacheUrl) {
      throw new Error('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置')
    }
    return getRedisClient(cacheUrl)
  }

  private async redisLocation(key: string): Promise<{ key: string; indexKey: string }> {
    const version = await this.namespaceVersion()
    return {
      key: `${this.keyPrefix}${version}:${key}`,
      indexKey: `${this.indexKeyPrefix}${version}`
    }
  }

  private async namespaceVersion(): Promise<string> {
    const client = await this.client()
    const existing = await client.get(this.versionKey)
    if (existing) return existing
    const version = this.options.version?.trim() || nextCacheVersion()
    const inserted = await client.set(this.versionKey, version, { NX: true, PX: 30 * 24 * 60 * 60 * 1000 })
    return inserted === 'OK' ? version : (await client.get(this.versionKey)) ?? version
  }

  private async trackKeyAndTrim(client: RedisCommandClient, indexKey: string, key: string, ttlMs: number): Promise<void> {
    await client.sendCommand(['ZADD', indexKey, String(Date.now()), key])
    await client.sendCommand(['PEXPIRE', indexKey, String(Math.max(ttlMs, 60_000))])
    const maxEntries = normalizeMaxEntries(this.options.max)
    const count = numberField(await client.sendCommand(['ZCARD', indexKey]))
    const overflow = count - maxEntries
    if (overflow <= 0) return
    const staleKeys = stringArray(await client.sendCommand(['ZRANGE', indexKey, '0', String(overflow - 1)]))
    if (!staleKeys.length) return
    await client.sendCommand(['DEL', ...staleKeys])
    await client.sendCommand(['ZREM', indexKey, ...staleKeys])
  }
}

function createSharedStore<V extends {}>(options: SharedJsonCacheOptions<V>): LRUCache<string, V> {
  return new LRUCache<string, V>({
    max: normalizeMaxEntries(options.max),
    ttl: normalizeTtlMs(options.ttlMs)
  })
}

function normalizeMaxEntries(max: number): number {
  return Number.isFinite(max) ? Math.max(1, Math.trunc(max)) : 1
}

function sanitizeRedisKeyPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'default'
}

function normalizeTtlMs(ttlMs: number): number {
  return Number.isFinite(ttlMs) ? Math.max(1, Math.trunc(ttlMs)) : 1
}

function numberField(value: unknown): number {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.max(0, Math.trunc(value))
  if (typeof value === 'bigint') return Number(value)
  if (typeof value === 'string' && value.trim()) {
    const numeric = Number(value)
    return Number.isFinite(numeric) ? Math.max(0, Math.trunc(numeric)) : 0
  }
  return 0
}

function stringArray(value: unknown): string[] {
  return Array.isArray(value)
    ? value.map((item) => String(item ?? '')).filter(Boolean)
    : []
}

function nextCacheVersion(): string {
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`
}
