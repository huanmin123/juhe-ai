import { createShortLivedQueryCache } from './shortLivedQueryCache'

interface ShortLivedRequestCacheOptions {
  maxEntries?: number
  ttlMs?: number
}

export function createShortLivedRequestCache<T>(options: ShortLivedRequestCacheOptions = {}) {
  const cache = createShortLivedQueryCache<T>({
    maxEntries: options.maxEntries,
    ttlMs: options.ttlMs
  })
  const inflight = new Map<string, Promise<T>>()

  async function load(key: string, loader: () => Promise<T>, force = false): Promise<T> {
    const cached = force ? undefined : cache.get(key)
    if (cached !== undefined) return cached

    const running = force ? undefined : inflight.get(key)
    if (running) return running

    const request = loader()
    inflight.set(key, request)
    try {
      const value = await request
      cache.set(key, value)
      return value
    } finally {
      if (inflight.get(key) === request) {
        inflight.delete(key)
      }
    }
  }

  function remove(key: string): void {
    cache.remove(key)
    inflight.delete(key)
  }

  function clear(): void {
    cache.clear()
    inflight.clear()
  }

  return {
    clear,
    load,
    remove
  }
}
