import { createHash } from 'node:crypto'

import {
  canUseProcessLocalAppCacheAsFactSource,
  createSharedJsonCache,
  type SharedJsonCache
} from '../../shared/cache.js'
import { errorLogFields, logger } from '../../shared/logger.js'
import { stableStringify } from '../deduplication/deduplication.service.js'
import type { PageDataDomain } from './page-data-change.service.js'

export interface PageDataReadCacheStorage<V> {
  get(key: string): Promise<V | undefined>
  set(key: string, value: V): Promise<void>
  delete(key: string): Promise<void>
  clear(): Promise<void>
}

export interface PageDataReadCacheOptions {
  onStorageError?: (error: unknown, operation: 'get' | 'set' | 'delete' | 'clear', key?: string) => void
}

export class PageDataReadCache<V> {
  private readonly pendingLoads = new Map<string, Promise<V>>()
  private generation = 0
  private invalidationInFlight: Promise<void> = Promise.resolve()
  private bypassStorage = false

  constructor(
    private readonly storage: PageDataReadCacheStorage<V>,
    private readonly options: PageDataReadCacheOptions = {}
  ) {}

  async load(key: string, loader: () => Promise<V>): Promise<V> {
    await this.invalidationInFlight
    const generationBeforeRead = this.generation
    const pending = this.pendingLoads.get(key)
    if (pending) return await pending

    const cached = await this.read(key)
    if (generationBeforeRead !== this.generation) return await this.load(key, loader)
    if (cached !== undefined) return cached

    const pendingAfterRead = this.pendingLoads.get(key)
    if (pendingAfterRead) return await pendingAfterRead

    const generation = this.generation
    const load = (async () => {
      const value = await loader()
      if (generation === this.generation) await this.write(key, value)
      return value
    })()
    this.pendingLoads.set(key, load)
    try {
      return await load
    } finally {
      if (this.pendingLoads.get(key) === load) this.pendingLoads.delete(key)
    }
  }

  async invalidate(key: string): Promise<void> {
    return await this.queueInvalidation(
      [...this.pendingLoads.entries()].filter(([pendingKey]) => pendingKey === key).map(([, pending]) => pending),
      async () => {
        try {
          await this.storage.delete(key)
        } catch (error) {
          this.options.onStorageError?.(error, 'delete', key)
        }
      }
    )
  }

  async invalidateDomain(): Promise<void> {
    return await this.queueInvalidation([...this.pendingLoads.values()], async () => {
      try {
        await this.storage.clear()
        this.bypassStorage = false
      } catch (error) {
        this.bypassStorage = true
        this.options.onStorageError?.(error, 'clear')
      }
    })
  }

  markDomainInvalidated(): void {
    this.generation += 1
  }

  private async read(key: string): Promise<V | undefined> {
    if (this.bypassStorage) return undefined
    try {
      return await this.storage.get(key)
    } catch (error) {
      this.options.onStorageError?.(error, 'get', key)
      return undefined
    }
  }

  private async write(key: string, value: V): Promise<void> {
    if (this.bypassStorage) return
    try {
      await this.storage.set(key, value)
    } catch (error) {
      this.options.onStorageError?.(error, 'set', key)
    }
  }

  private queueInvalidation(pendingLoads: Promise<V>[], invalidateStorage: () => Promise<void>): Promise<void> {
    this.generation += 1
    const previous = this.invalidationInFlight
    const operation = (async () => {
      await previous
      await Promise.allSettled(pendingLoads)
      await invalidateStorage()
    })()
    this.invalidationInFlight = operation.catch(() => undefined)
    return operation
  }
}

interface DomainReadCacheOptions {
  max: number
  ttlMs: number
  version?: string
}

const domainCaches = new Map<PageDataDomain, Set<PageDataReadCache<object>>>()
const sharedDomainInvalidators = new Map<PageDataDomain, SharedJsonCache<object>>()

export function createPageDataDomainReadCache<V extends object>(
  domain: PageDataDomain,
  options: DomainReadCacheOptions
): PageDataReadCache<V> {
  const storage = createSharedJsonCache<V>({
    name: pageDataDomainCacheName(domain),
    max: options.max,
    ttlMs: options.ttlMs,
    version: options.version ?? 'v1'
  })
  const cache = new PageDataReadCache<V>(storage, {
    onStorageError: (error, operation, key) => {
      logger.warn(errorLogFields(error, {
        event: 'page_data_read_cache_storage_failed',
        domain,
        operation,
        cacheKey: key
      }), '页面数据后端缓存不可用，已回退到有界事实读取')
    }
  })
  const caches = domainCaches.get(domain) ?? new Set<PageDataReadCache<object>>()
  caches.add(cache as PageDataReadCache<object>)
  domainCaches.set(domain, caches)
  return cache
}

export async function invalidatePageDataReadCacheDomain(domain: PageDataDomain): Promise<void> {
  const caches = domainCaches.get(domain)
  if (canUseProcessLocalAppCacheAsFactSource()) {
    if (!caches?.size) return
    await Promise.all([...caches].map((cache) => cache.invalidateDomain()))
    return
  }

  await Promise.all([...(caches ?? [])].map((cache) => cache.invalidateDomain()))
  try {
    await sharedDomainInvalidator(domain).clear()
  } catch (error) {
    logger.warn(errorLogFields(error, {
      event: 'page_data_read_cache_storage_failed',
      domain,
      operation: 'clear'
    }), '页面数据后端共享缓存失效失败')
    throw error
  }
}

function sharedDomainInvalidator(domain: PageDataDomain): SharedJsonCache<object> {
  const current = sharedDomainInvalidators.get(domain)
  if (current) return current
  const cache = createSharedJsonCache<object>({
    name: pageDataDomainCacheName(domain),
    max: 1,
    ttlMs: 60_000,
    version: 'v1'
  })
  sharedDomainInvalidators.set(domain, cache)
  return cache
}

function pageDataDomainCacheName(domain: PageDataDomain): string {
  return `page_data_${domain.replace(/[^a-zA-Z0-9_-]/g, '_')}`
}

export function pageDataReadCacheKey(input: {
  scope: unknown
  route: string
  query?: unknown
  version?: number
}): string {
  const signature = stableStringify({
    scope: input.scope,
    route: input.route,
    query: input.query ?? null,
    version: input.version ?? 1
  })
  return createHash('sha256').update(signature).digest('hex')
}
