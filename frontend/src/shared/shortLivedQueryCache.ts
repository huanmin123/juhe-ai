interface ShortLivedQueryCacheOptions {
  maxEntries?: number
  ttlMs?: number
}

interface CacheEntry<T> {
  expiresAt: number
  value: T
}

export function createShortLivedQueryCache<T>(options: ShortLivedQueryCacheOptions = {}) {
  const ttlMs = Math.max(0, Math.trunc(options.ttlMs ?? 10_000))
  const maxEntries = Math.max(1, Math.trunc(options.maxEntries ?? 30))
  const entries = new Map<string, CacheEntry<T>>()

  function get(key: string): T | undefined {
    const entry = entries.get(key)
    if (!entry) return undefined
    if (entry.expiresAt <= Date.now()) {
      entries.delete(key)
      return undefined
    }
    return entry.value
  }

  function set(key: string, value: T): void {
    if (ttlMs <= 0) return
    entries.set(key, { expiresAt: Date.now() + ttlMs, value })
    trim()
  }

  function clear(): void {
    entries.clear()
  }

  function trim(): void {
    while (entries.size > maxEntries) {
      const oldest = entries.keys().next()
      if (oldest.done) return
      entries.delete(oldest.value)
    }
  }

  return {
    clear,
    get,
    set
  }
}
