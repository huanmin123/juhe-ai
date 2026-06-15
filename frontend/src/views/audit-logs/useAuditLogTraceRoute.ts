import { watch, type WatchStopHandle } from 'vue'
import type { RouteLocationNormalizedLoaded, Router } from 'vue-router'

import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'

export interface AuditLogTraceRouteController {
  clearRouteTraceIdForManualState: () => void
  routeTraceId: () => string | undefined
  stop: WatchStopHandle
}

export function auditLogRouteTraceId(route: RouteLocationNormalizedLoaded): string | undefined {
  return trimmedRouteQueryValue(route.query.traceId)
}

export function useAuditLogTraceRoute(input: {
  applyRouteTraceId: (traceId: string) => void
  currentTraceId: () => string
  onManualRouteTraceCleared: () => void
  restoreAfterRouteTraceCleared: () => void
  route: RouteLocationNormalizedLoaded
  router: Router
}): AuditLogTraceRouteController {
  let skipNextRouteTraceRestore = false

  function routeTraceId(): string | undefined {
    return auditLogRouteTraceId(input.route)
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
