import { createShortLivedRequestCache } from './shortLivedRequestCache'

interface LoadEntityDetailInput<T> {
  force?: boolean
  id: string
  load: () => Promise<T>
  namespace: string
  scope?: string
}

const entityDetailCache = createShortLivedRequestCache<unknown>({
  maxEntries: 120,
  ttlMs: 5_000
})

export function entityDetailCacheKey(namespace: string, id: string, scope = ''): string {
  return JSON.stringify([namespace, scope, id])
}

export async function loadEntityDetailCached<T>(input: LoadEntityDetailInput<T>): Promise<T> {
  return entityDetailCache.load(entityDetailCacheKey(input.namespace, input.id, input.scope), input.load, input.force) as Promise<T>
}

export function invalidateEntityDetailCache(namespace: string, id: string, scope = ''): void {
  entityDetailCache.remove(entityDetailCacheKey(namespace, id, scope))
}
