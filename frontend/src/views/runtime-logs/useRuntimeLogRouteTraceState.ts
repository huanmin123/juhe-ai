import { watch, type WatchStopHandle } from 'vue'
import type { RouteLocationNormalizedLoaded, Router } from 'vue-router'

import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'

type RuntimeLogPageStateCache<T extends object> = {
  cancelPendingWrite: () => void
  flushPendingWrite: () => void
  read: () => T
  scheduleWrite: (snapshot: () => T) => void
}

type ResolveRuntimeLogInitialPageStateOptions<T extends object> = {
  defaultPageState: () => T
  pageStateCache: Pick<RuntimeLogPageStateCache<T>, 'read'>
  route: RouteLocationNormalizedLoaded
  withTraceId: (state: T, traceId: string) => T
}

type UseRuntimeLogRouteTraceStateOptions<T extends object> = {
  applyPageState: (state: T) => void
  defaultPageState: () => T
  getCurrentTraceIdFilter: () => string
  loadRouteTraceState: () => void
  loadRestoredPageState: () => void
  pageStateCache: RuntimeLogPageStateCache<T>
  resetPagination: () => void
  route: RouteLocationNormalizedLoaded
  router: Router
  snapshotPageState: () => T
  withTraceId: (state: T, traceId: string) => T
}

export function runtimeLogRouteTraceId(route: RouteLocationNormalizedLoaded): string | undefined {
  return trimmedRouteQueryValue(route.query.traceId)
}

export function resolveRuntimeLogInitialPageState<T extends object>(
  options: ResolveRuntimeLogInitialPageStateOptions<T>
): T {
  const initialTraceId = runtimeLogRouteTraceId(options.route)
  if (initialTraceId) {
    return options.withTraceId(options.defaultPageState(), initialTraceId)
  }
  return options.pageStateCache.read()
}

export function useRuntimeLogRouteTraceState<T extends object>(
  options: UseRuntimeLogRouteTraceStateOptions<T>
) {
  let skipNextRouteTraceRestore = false

  function currentRouteTraceId(): string | undefined {
    return runtimeLogRouteTraceId(options.route)
  }

  function applyRouteTraceId(traceId: string): void {
    options.pageStateCache.flushPendingWrite()
    options.applyPageState(options.withTraceId(options.defaultPageState(), traceId))
    options.resetPagination()
    options.loadRouteTraceState()
  }

  function restorePageStateAfterRouteTraceCleared(): void {
    options.applyPageState(options.pageStateCache.read())
    options.loadRestoredPageState()
  }

  function clearRouteTraceIdForManualState(): void {
    if (!currentRouteTraceId()) return
    skipNextRouteTraceRestore = true
    void removeRouteTraceIdQuery(options.router, options.route).catch((error) => {
      skipNextRouteTraceRestore = false
      console.error(error)
    })
  }

  const stopPageStateWatch = watch(options.snapshotPageState, () => {
    if (currentRouteTraceId()) {
      options.pageStateCache.cancelPendingWrite()
      return
    }
    options.pageStateCache.scheduleWrite(options.snapshotPageState)
  }, { deep: true })

  const stopRouteTraceWatch = watch(
    () => options.route.query.traceId,
    () => {
      const traceId = currentRouteTraceId()
      if (!traceId) {
        if (skipNextRouteTraceRestore) {
          skipNextRouteTraceRestore = false
          options.pageStateCache.scheduleWrite(options.snapshotPageState)
          return
        }
        restorePageStateAfterRouteTraceCleared()
        return
      }
      if (traceId === options.getCurrentTraceIdFilter().trim()) return
      applyRouteTraceId(traceId)
    }
  )

  function stop(): void {
    stopPageStateWatch()
    stopRouteTraceWatch()
  }

  return {
    clearRouteTraceIdForManualState,
    currentRouteTraceId,
    stop: stop as WatchStopHandle
  }
}
