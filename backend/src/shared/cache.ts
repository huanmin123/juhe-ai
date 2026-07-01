import { LRUCache } from 'lru-cache'

import { runtimeConfig } from '../config/runtime.js'
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

const caches = new Set<AppCache<{}, {}>>()

export function createAppCache<K extends {}, V extends {}>(options: AppCacheOptions<K, V>): AppCache<K, V> {
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

export function throwIfRedisCacheIsRequired(error: unknown): void {
  if (runtimeConfig.cacheDriver !== 'redis') return
  throw error instanceof Error ? error : new Error(String(error))
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
  private readonly versionKey: string

  constructor(private readonly options: SharedJsonCacheOptions<V>) {
    const cacheUrl = runtimeConfig.redis.cacheUrl
    if (!cacheUrl) {
      throw new Error('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置')
    }
    this.name = options.name
    const safeName = sanitizeRedisKeyPart(options.name)
    this.keyPrefix = `juhe-ai:cache:${safeName}:`
    this.versionKey = `juhe-ai:cache-version:${safeName}`
  }

  async get(key: string): Promise<V | undefined> {
    const rawValue = await (await this.client()).get(await this.redisKey(key))
    if (rawValue === null) return undefined
    try {
      return JSON.parse(rawValue) as V
    } catch {
      await this.delete(key)
      return undefined
    }
  }

  async set(key: string, value: V, options?: { ttlMs?: number }): Promise<void> {
    await (await this.client()).set(
      await this.redisKey(key),
      JSON.stringify(value),
      { PX: normalizeTtlMs(options?.ttlMs ?? this.options.ttlMs) }
    )
  }

  async delete(key: string): Promise<void> {
    await (await this.client()).del(await this.redisKey(key))
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

  private async redisKey(key: string): Promise<string> {
    return `${this.keyPrefix}${await this.namespaceVersion()}:${key}`
  }

  private async namespaceVersion(): Promise<string> {
    const client = await this.client()
    const existing = await client.get(this.versionKey)
    if (existing) return existing
    const version = this.options.version?.trim() || nextCacheVersion()
    const inserted = await client.set(this.versionKey, version, { NX: true, PX: 30 * 24 * 60 * 60 * 1000 })
    return inserted === 'OK' ? version : (await client.get(this.versionKey)) ?? version
  }
}

function createSharedStore<V extends {}>(options: SharedJsonCacheOptions<V>): LRUCache<string, V> {
  return new LRUCache<string, V>({
    max: Math.max(1, Math.trunc(options.max)),
    ttl: normalizeTtlMs(options.ttlMs)
  })
}

function sanitizeRedisKeyPart(value: string): string {
  return value.trim().replace(/[^a-zA-Z0-9:_-]/g, '_') || 'default'
}

function normalizeTtlMs(ttlMs: number): number {
  return Number.isFinite(ttlMs) ? Math.max(1, Math.trunc(ttlMs)) : 1
}

function nextCacheVersion(): string {
  return `${Date.now()}-${Math.random().toString(16).slice(2)}`
}
