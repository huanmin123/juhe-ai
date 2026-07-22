import { watch, type WatchStopHandle } from 'vue'
import type { RouteLocationNormalizedLoaded, Router } from 'vue-router'

import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'

export interface UsageRecordTraceRouteController {
  clearRouteTraceIdForManualState: () => void
  routeTraceId: () => string | undefined
  stop: WatchStopHandle
}

export function useUsageRecordTraceRoute(input: {
  applyRouteTraceId: (traceId: string) => void
  currentTraceId: () => string
  onManualRouteTraceCleared: () => void
  restoreAfterRouteTraceCleared: () => void
  route: RouteLocationNormalizedLoaded
  router: Router
}): UsageRecordTraceRouteController {
  let skipNextRouteTraceRestore = false

  function routeTraceId(): string | undefined {
    return trimmedRouteQueryValue(input.route.query.traceId)
  }

  function clearRouteTraceIdForManualState(): void {
    if (!routeTraceId()) return
    skipNextRouteTraceRestore = true
    void removeRouteTraceIdQuery(input.router, input.route).catch((error) => {
      skipNextRouteTraceRestore = false
      console.error(error)
    })
  }

  const stop = watch(
    () => input.route.query.traceId,
    () => {
      const traceId = routeTraceId()
      if (!traceId) {
        if (skipNextRouteTraceRestore) {
          skipNextRouteTraceRestore = false
          input.onManualRouteTraceCleared()
          return
        }
        input.restoreAfterRouteTraceCleared()
        return
      }
      if (traceId === input.currentTraceId().trim()) return
      input.applyRouteTraceId(traceId)
    }
  )

  return {
    clearRouteTraceIdForManualState,
    routeTraceId,
    stop
  }
}
