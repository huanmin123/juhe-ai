import type {
  RouteStrategyListResult,
  RouteStrategyListSnapshotItem,
  RouteStrategyListSnapshotResult
} from '@/types/domain'

export type RouteStrategyListSnapshotState =
  | { status: 'pending' }
  | { status: 'ready'; item: RouteStrategyListSnapshotItem }
  | { status: 'error' }

interface RouteStrategyListProgressiveStateOptions {
  currentScopeKey: () => string
  currentVisibleIds: () => string[]
  setListLoading: (loading: boolean) => void
  applyList: (result: RouteStrategyListResult) => boolean | void
  applySnapshotStates: (states: Map<string, RouteStrategyListSnapshotState>) => void
  onListError: (error: unknown) => void
}

interface RouteStrategyListProgressiveLoadRequest {
  scopeKey: string
  list: () => Promise<RouteStrategyListResult>
  snapshot: (ids: string[]) => Promise<RouteStrategyListSnapshotResult>
}

export function createRouteStrategyListProgressiveState(options: RouteStrategyListProgressiveStateOptions) {
  let generation = 0
  let disposed = false

  async function load(request: RouteStrategyListProgressiveLoadRequest): Promise<boolean> {
    if (disposed) return false
    const requestGeneration = ++generation
    options.setListLoading(true)
    try {
      const result = await request.list()
      if (!isCurrent(requestGeneration, request.scopeKey)) return false
      if (options.applyList(result) === false) return false

      const ids = normalizedIds(options.currentVisibleIds())
      const idSignature = routeStrategyListIdSignature(ids)
      options.applySnapshotStates(snapshotStates(ids, 'pending'))
      if (ids.length) {
        void loadSnapshot(request, requestGeneration, ids, idSignature)
      }
      return true
    } catch (error) {
      if (!isCurrent(requestGeneration, request.scopeKey)) return false
      options.onListError(error)
      return false
    } finally {
      if (!disposed && requestGeneration === generation) {
        options.setListLoading(false)
      }
    }
  }

  async function loadSnapshot(
    request: RouteStrategyListProgressiveLoadRequest,
    requestGeneration: number,
    ids: string[],
    idSignature: string
  ): Promise<void> {
    try {
      const result = await request.snapshot(ids)
      if (!isSnapshotCurrent(requestGeneration, request.scopeKey, idSignature)) return
      const byId = new Map(result.items.map((item) => [item.id, item]))
      const states = new Map<string, RouteStrategyListSnapshotState>()
      for (const id of ids) {
        const item = byId.get(id)
        states.set(id, item ? { status: 'ready', item } : { status: 'error' })
      }
      options.applySnapshotStates(states)
    } catch {
      if (!isSnapshotCurrent(requestGeneration, request.scopeKey, idSignature)) return
      options.applySnapshotStates(snapshotStates(ids, 'error'))
    }
  }

  function isCurrent(requestGeneration: number, scopeKey: string): boolean {
    return !disposed
      && requestGeneration === generation
      && scopeKey === options.currentScopeKey()
  }

  function isSnapshotCurrent(requestGeneration: number, scopeKey: string, idSignature: string): boolean {
    return isCurrent(requestGeneration, scopeKey)
      && idSignature === routeStrategyListIdSignature(options.currentVisibleIds())
  }

  function dispose(): void {
    disposed = true
    generation += 1
    options.setListLoading(false)
  }

  return { dispose, load }
}

export function routeStrategyListIdSignature(ids: string[]): string {
  return JSON.stringify(normalizedIds(ids))
}

export function routeStrategyListFallbackPage(result: RouteStrategyListResult): number | undefined {
  return result.page > 1 && result.items.length === 0 && result.hasMore === false
    ? result.page - 1
    : undefined
}

function normalizedIds(ids: string[]): string[] {
  return [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
}

function snapshotStates(ids: string[], status: 'pending' | 'error'): Map<string, RouteStrategyListSnapshotState> {
  return new Map(normalizedIds(ids).map((id) => [id, { status }]))
}
