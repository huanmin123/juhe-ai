import type { RouteLocationNormalizedLoaded, Router } from 'vue-router'

export function singleRouteQueryValue(value: unknown): string | undefined {
  if (Array.isArray(value)) {
    return typeof value[0] === 'string' ? value[0] : undefined
  }
  return typeof value === 'string' ? value : undefined
}

export function trimmedRouteQueryValue(value: unknown): string | undefined {
  const text = singleRouteQueryValue(value)?.trim()
  return text || undefined
}

export function hasRouteTraceId(route: RouteLocationNormalizedLoaded): boolean {
  return Boolean(trimmedRouteQueryValue(route.query.traceId))
}

export async function removeRouteTraceIdQuery(router: Router, route: RouteLocationNormalizedLoaded): Promise<boolean> {
  if (!hasRouteTraceId(route)) return false
  const query = { ...route.query }
  delete query.traceId
  await router.replace({ query })
  return true
}
