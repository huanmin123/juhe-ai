import { computed, onBeforeUnmount, onDeactivated } from 'vue'
import { useRoute } from 'vue-router'

import { authState } from './useAuth'

interface PageStateCacheOptions<T> {
  debounceMs?: number
  sanitize?: (value: unknown, fallback: T) => T
  storage?: 'local' | 'session'
  version?: number
}

interface PageStateCacheEnvelope<T> {
  state: T
  version: number
}

const pageStateStoragePrefix = 'juhe-ai:page-state:'

function storageFor(type: 'local' | 'session'): Storage | undefined {
  if (typeof window === 'undefined') return undefined
  try {
    return type === 'session' ? window.sessionStorage : window.localStorage
  } catch {
    return undefined
  }
}

function userCacheKey(): string {
  const user = authState.currentUser.value
  return user?.id || user?.username || 'anonymous'
}

function normalizePageKey(value: string): string {
  return value.replace(/[^a-zA-Z0-9/_-]/g, '_')
}

export function usePageStateCache<T extends object>(
  pageKey: string | undefined,
  defaults: () => T,
  options: PageStateCacheOptions<T> = {}
) {
  const route = useRoute()
  const version = options.version ?? 1
  const storageType = options.storage ?? 'local'
  const debounceMs = options.debounceMs ?? 200
  const fixedPageKey = normalizePageKey(pageKey || route.path)
  let writeTimer: ReturnType<typeof window.setTimeout> | undefined
  let pendingSnapshot: (() => T) | undefined

  const cacheKey = computed(() => {
    return `${pageStateStoragePrefix}${userCacheKey()}:${fixedPageKey}:v${version}`
  })

  function read(): T {
    const fallback = defaults()
    const storage = storageFor(storageType)
    if (!storage) return fallback
    try {
      const text = storage.getItem(cacheKey.value)
      if (!text) return fallback
      const envelope = JSON.parse(text) as Partial<PageStateCacheEnvelope<T>>
      if (envelope.version !== version || !envelope.state || typeof envelope.state !== 'object') {
        return fallback
      }
      return options.sanitize ? options.sanitize(envelope.state, fallback) : { ...fallback, ...envelope.state }
    } catch {
      return fallback
    }
  }

  function write(state: T): void {
    const storage = storageFor(storageType)
    if (!storage) return
    try {
      const envelope: PageStateCacheEnvelope<T> = { version, state }
      storage.setItem(cacheKey.value, JSON.stringify(envelope))
    } catch {
      // 页面状态缓存只是体验增强，本地存储不可用时不影响页面本身。
    }
  }

  function scheduleWrite(snapshot: () => T): void {
    if (typeof window === 'undefined') return
    pendingSnapshot = snapshot
    if (writeTimer) {
      window.clearTimeout(writeTimer)
    }
    writeTimer = window.setTimeout(() => {
      flushPendingWrite()
    }, debounceMs)
  }

  function clear(): void {
    cancelPendingWrite()
    const storage = storageFor(storageType)
    if (!storage) return
    try {
      storage.removeItem(cacheKey.value)
    } catch {
      // 忽略本地缓存清理失败，页面内存状态仍会按调用方重置。
    }
  }

  function cancelPendingWrite(): void {
    if (writeTimer && typeof window !== 'undefined') {
      window.clearTimeout(writeTimer)
      writeTimer = undefined
    }
    pendingSnapshot = undefined
  }

  function flushPendingWrite(): void {
    if (writeTimer && typeof window !== 'undefined') {
      window.clearTimeout(writeTimer)
      writeTimer = undefined
    }
    const snapshot = pendingSnapshot
    pendingSnapshot = undefined
    if (snapshot) {
      write(snapshot())
    }
  }

  onDeactivated(flushPendingWrite)
  onBeforeUnmount(flushPendingWrite)

  return {
    cancelPendingWrite,
    clear,
    flushPendingWrite,
    read,
    scheduleWrite,
    write
  }
}
