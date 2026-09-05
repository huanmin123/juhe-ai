import { LRUCache } from 'lru-cache'

import { runtimeConfig } from '../config/runtime.js'
import { errorLogFields, logger } from './logger.js'
import { runRedisOperationWithDeadline, type RedisCommandClient } from './redis-client.js'
import { redisNamespacedKey } from './redis-namespace.js'

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

export interface SharedJsonCacheOperationOptions {
  signal?: AbortSignal
  deadlineAtMs?: number
}

export interface SharedJsonCache<V extends {}> {
  readonly name: string
  get(key: string, options?: SharedJsonCacheOperationOptions): Promise<V | undefined>
  set(key: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<void>
  delete(key: string, options?: SharedJsonCacheOperationOptions): Promise<void>
  clear(options?: SharedJsonCacheOperationOptions): Promise<void>
  acquireLease(key: string, options: { ttlMs: number; token: string } & SharedJsonCacheOperationOptions): Promise<boolean>
  renewLease(key: string, token: string, options: { ttlMs: number } & SharedJsonCacheOperationOptions): Promise<boolean>
  setIfLeaseOwner(key: string, token: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<boolean>
  releaseLease(key: string, token: string, options?: SharedJsonCacheOperationOptions): Promise<void>
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

function sharedCacheWritesDisabledForCurrentProcess(): boolean {
  return runtimeConfig.runtimeMode === 'performance' && runtimeConfig.performanceNodeRole === 'control-replica'
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

  async get(key: string, options?: SharedJsonCacheOperationOptions): Promise<V | undefined> {
    return this.cache().get(key, options)
  }

  async set(key: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<void> {
    await this.cache().set(key, value, options)
  }

  async delete(key: string, options?: SharedJsonCacheOperationOptions): Promise<void> {
    await this.cache().delete(key, options)
  }

  async clear(options?: SharedJsonCacheOperationOptions): Promise<void> {
    await this.cache().clear(options)
  }

  async acquireLease(key: string, options: { ttlMs: number; token: string } & SharedJsonCacheOperationOptions): Promise<boolean> {
    return await this.cache().acquireLease(key, options)
  }

  async renewLease(key: string, token: string, options: { ttlMs: number } & SharedJsonCacheOperationOptions): Promise<boolean> {
    return await this.cache().renewLease(key, token, options)
  }

  async setIfLeaseOwner(key: string, token: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<boolean> {
    return await this.cache().setIfLeaseOwner(key, token, value, options)
  }

  async releaseLease(key: string, token: string, options?: SharedJsonCacheOperationOptions): Promise<void> {
    await this.cache().releaseLease(key, token, options)
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
  private readonly leases = new Map<string, { token: string; expiresAt: number }>()

  constructor(private readonly options: SharedJsonCacheOptions<V>) {
    this.name = options.name
    this.store = createSharedStore(options)
  }

  async get(key: string, options?: SharedJsonCacheOperationOptions): Promise<V | undefined> {
    options?.signal?.throwIfAborted()
    return this.store.get(key)
  }

  async set(key: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<void> {
    options?.signal?.throwIfAborted()
    this.store.set(key, value, options?.ttlMs === undefined ? undefined : { ttl: normalizeTtlMs(options.ttlMs) })
  }

  async delete(key: string, options?: SharedJsonCacheOperationOptions): Promise<void> {
    options?.signal?.throwIfAborted()
    this.store.delete(key)
  }

  async clear(options?: SharedJsonCacheOperationOptions): Promise<void> {
    options?.signal?.throwIfAborted()
    this.store = createSharedStore(this.options)
  }

  async acquireLease(key: string, options: { ttlMs: number; token: string } & SharedJsonCacheOperationOptions): Promise<boolean> {
    options.signal?.throwIfAborted()
    const current = this.leases.get(key)
    if (current && current.expiresAt > Date.now()) return false
    this.leases.set(key, { token: options.token, expiresAt: Date.now() + normalizeTtlMs(options.ttlMs) })
    return true
  }

  async renewLease(key: string, token: string, options: { ttlMs: number } & SharedJsonCacheOperationOptions): Promise<boolean> {
    options.signal?.throwIfAborted()
    const current = this.leases.get(key)
    if (!current || current.token !== token || current.expiresAt <= Date.now()) return false
    current.expiresAt = Date.now() + normalizeTtlMs(options.ttlMs)
    return true
  }

  async setIfLeaseOwner(key: string, token: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<boolean> {
    options?.signal?.throwIfAborted()
    const current = this.leases.get(key)
    if (!current || current.token !== token || current.expiresAt <= Date.now()) return false
    this.store.set(key, value, options?.ttlMs === undefined ? undefined : { ttl: normalizeTtlMs(options.ttlMs) })
    return true
  }

  async releaseLease(key: string, token: string, options?: SharedJsonCacheOperationOptions): Promise<void> {
    options?.signal?.throwIfAborted()
    if (this.leases.get(key)?.token === token) this.leases.delete(key)
  }
}

class RedisSharedJsonCache<V extends {}> implements SharedJsonCache<V> {
  readonly name: string
  private readonly keyPrefix: string
  private readonly indexKeyPrefix: string
  private readonly versionKey: string
  private readonly leaseKeyPrefix: string

  constructor(private readonly options: SharedJsonCacheOptions<V>) {
    const cacheUrl = runtimeConfig.redis.cacheUrl
    if (!cacheUrl) {
      throw new Error('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置')
    }
    this.name = options.name
    const safeName = sanitizeRedisKeyPart(options.name)
    this.keyPrefix = redisNamespacedKey(`juhe-ai:cache:${safeName}:`)
    this.indexKeyPrefix = redisNamespacedKey(`juhe-ai:cache-index:${safeName}:`)
    this.versionKey = redisNamespacedKey(`juhe-ai:cache-version:${safeName}`)
    this.leaseKeyPrefix = redisNamespacedKey(`juhe-ai:cache-lease:${safeName}:`)
  }

  async get(key: string, options?: SharedJsonCacheOperationOptions): Promise<V | undefined> {
    const location = await this.redisLocation(key, options, !sharedCacheWritesDisabledForCurrentProcess())
    const rawValue = await this.runRedis('共享缓存读取', options, (client) => client.get(location.key))
    if (rawValue === null) return undefined
    try {
      return JSON.parse(rawValue) as V
    } catch {
      if (!sharedCacheWritesDisabledForCurrentProcess()) await this.delete(key, options)
      return undefined
    }
  }

  async set(key: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<void> {
    if (sharedCacheWritesDisabledForCurrentProcess()) return
    const ttlMs = normalizeTtlMs(options?.ttlMs ?? this.options.ttlMs)
    const location = await this.redisLocation(key, options)
    await this.runRedis('共享缓存写入', options, async (client) => {
      await client.set(location.key, JSON.stringify(value), { PX: ttlMs })
      await this.trackKeyAndTrim(client, location.indexKey, location.key, ttlMs)
    })
  }

  async delete(key: string, options?: SharedJsonCacheOperationOptions): Promise<void> {
    if (sharedCacheWritesDisabledForCurrentProcess()) return
    const location = await this.redisLocation(key, options)
    await this.runRedis('共享缓存删除', options, async (client) => {
      await client.del(location.key)
      await client.sendCommand(['ZREM', location.indexKey, location.key])
    })
  }

  async clear(options?: SharedJsonCacheOperationOptions): Promise<void> {
    if (sharedCacheWritesDisabledForCurrentProcess()) return
    await this.runRedis('共享缓存清空', options, async (client) => {
      const currentVersion = await this.namespaceVersionWithClient(client)
      const indexKey = `${this.indexKeyPrefix}${currentVersion}`
      const indexedKeys = stringArray(await client.sendCommand(['ZRANGE', indexKey, '0', '-1']))
      if (indexedKeys.length > 0) {
        await client.sendCommand(['DEL', ...indexedKeys])
      }
      await client.del(indexKey)
      await client.set(this.versionKey, nextCacheVersion(), { PX: 30 * 24 * 60 * 60 * 1000 })
    })
  }

  async acquireLease(key: string, options: { ttlMs: number; token: string } & SharedJsonCacheOperationOptions): Promise<boolean> {
    if (sharedCacheWritesDisabledForCurrentProcess()) return false
    const result = await this.runRedis('共享缓存租约获取', options, (client) => client.set(
      `${this.leaseKeyPrefix}${key}`, options.token, { PX: normalizeTtlMs(options.ttlMs), NX: true }
    ))
    return result === 'OK'
  }

  async renewLease(key: string, token: string, options: { ttlMs: number } & SharedJsonCacheOperationOptions): Promise<boolean> {
    if (sharedCacheWritesDisabledForCurrentProcess()) return false
    const result = await this.runRedis('共享缓存租约续期', options, (client) => client.eval(renewSharedCacheLeaseScript, {
      keys: [`${this.leaseKeyPrefix}${key}`],
      arguments: [token, String(normalizeTtlMs(options.ttlMs))]
    }))
    return Number(result) === 1
  }

  async setIfLeaseOwner(key: string, token: string, value: V, options?: { ttlMs?: number } & SharedJsonCacheOperationOptions): Promise<boolean> {
    if (sharedCacheWritesDisabledForCurrentProcess()) return false
    const ttlMs = normalizeTtlMs(options?.ttlMs ?? this.options.ttlMs)
    const location = await this.redisLocation(key, options)
    return await this.runRedis('共享缓存租约写入', options, async (client) => {
      const result = await client.eval(setSharedCacheEntryIfLeaseOwnerScript, {
        keys: [`${this.leaseKeyPrefix}${key}`, location.key],
        arguments: [token, JSON.stringify(value), String(ttlMs)]
      })
      if (Number(result) !== 1) return false
      await this.trackKeyAndTrim(client, location.indexKey, location.key, ttlMs)
      return true
    })
  }

  async releaseLease(key: string, token: string, options?: SharedJsonCacheOperationOptions): Promise<void> {
    if (sharedCacheWritesDisabledForCurrentProcess()) return
    await this.runRedis('共享缓存租约释放', options, (client) => client.eval(releaseSharedCacheLeaseScript, {
      keys: [`${this.leaseKeyPrefix}${key}`],
      arguments: [token]
    }).then(() => undefined))
  }

  private runRedis<T>(operationName: string, options: SharedJsonCacheOperationOptions | undefined, operation: (client: RedisCommandClient) => Promise<T>): Promise<T> {
    const cacheUrl = runtimeConfig.redis.cacheUrl
    if (!cacheUrl) {
      throw new Error('JUHE_AI_REDIS_CACHE_URL 在 Redis cache driver 下必须配置')
    }
    return runRedisOperationWithDeadline(cacheUrl, {
      operationName,
      timeoutMs: 3_000,
      signal: options?.signal,
      deadlineAtMs: options?.deadlineAtMs
    }, operation)
  }

  private async redisLocation(key: string, options?: SharedJsonCacheOperationOptions, createVersion = true): Promise<{ key: string; indexKey: string }> {
    const version = await this.namespaceVersion(options, createVersion)
    return {
      key: `${this.keyPrefix}${version}:${key}`,
      indexKey: `${this.indexKeyPrefix}${version}`
    }
  }

  private async namespaceVersion(options?: SharedJsonCacheOperationOptions, createVersion = true): Promise<string> {
    return await this.runRedis('共享缓存版本读取', options, (client) => this.namespaceVersionWithClient(client, createVersion))
  }

  private async namespaceVersionWithClient(client: RedisCommandClient, createVersion = true): Promise<string> {
    const existing = await client.get(this.versionKey)
    if (existing) return existing
    const version = this.options.version?.trim() || nextCacheVersion()
    if (!createVersion) return this.options.version?.trim() || 'read-only-miss'
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

const releaseSharedCacheLeaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

const renewSharedCacheLeaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`

const setSharedCacheEntryIfLeaseOwnerScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
  return 1
end
return 0
`
