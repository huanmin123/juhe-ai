import { pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { RouteStrategyOptionSummary } from '@/types/domain'

interface RouteStrategyOptionsApi {
  options(params?: {
    ids?: string[]
    keyword?: string
    limit?: number
    activeOnly?: boolean
    systemAccountId?: string
  }): Promise<RouteStrategyOptionSummary[]>
}

interface RouteStrategyOptionsResourceOptions {
  api: RouteStrategyOptionsApi
  apply: (options: RouteStrategyOptionSummary[]) => void
  force?: boolean
  isCurrent?: () => boolean
  isManagementView: boolean
  keyword?: string
  selectedIds?: string[]
  systemAccountId?: string
}

const routeStrategyOptionsResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export async function loadRouteStrategyOptionsResource(options: RouteStrategyOptionsResourceOptions): Promise<RouteStrategyOptionSummary[]> {
  const keyword = options.keyword?.trim() || undefined
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].sort()
  const route = options.isManagementView ? '/route-strategies/options' : '/my-route-strategies/options'
  const scope = routeStrategyScope(options.isManagementView, options.systemAccountId)
  if (options.force) await routeStrategyOptionsResourceCache.invalidate('routeStrategies.options', scope, route)
  const result = await routeStrategyOptionsResourceCache.load<RouteStrategyOptionSummary[]>({
    cacheKey: {
      scope,
      route,
      query: { keyword, selectedIds, activeOnly: false, systemAccountId: options.systemAccountId, limit: 50 },
      version: 1
    },
    domain: 'routeStrategies.options',
    viewScope: options.isManagementView ? 'admin' : 'self',
    ...(options.isManagementView && options.systemAccountId ? { targetSystemAccountId: options.systemAccountId } : {}),
    loadNetwork: async () => {
      const windowOptions = await options.api.options({ keyword, limit: 50, activeOnly: false, systemAccountId: options.systemAccountId })
      const missingIds = selectedIds.filter((id) => !windowOptions.some((item) => item.id === id))
      if (!missingIds.length) return windowOptions
      const selectedOptions = await options.api.options({ ids: missingIds, limit: missingIds.length, activeOnly: false, systemAccountId: options.systemAccountId })
      return mergeRouteStrategyOptionsById(selectedOptions, windowOptions)
    }
  })
  applyIfCurrent(options, result.data)
  void result.confirmation?.then((outcome) => {
    if (outcome.data) applyIfCurrent(options, outcome.data)
  })
  return result.data
}

function applyIfCurrent(options: RouteStrategyOptionsResourceOptions, value: RouteStrategyOptionSummary[]): void {
  if (options.isCurrent?.() === false) return
  options.apply(value)
}

function routeStrategyScope(isManagementView: boolean, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    systemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}

function mergeRouteStrategyOptionsById(leading: RouteStrategyOptionSummary[], trailing: RouteStrategyOptionSummary[]): RouteStrategyOptionSummary[] {
  const merged = new Map<string, RouteStrategyOptionSummary>()
  for (const item of [...leading, ...trailing]) merged.set(item.id, item)
  return [...merged.values()]
}
