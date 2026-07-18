import { pageDataApi } from '@/api/client'
import type { PageDataDomain } from '@/api/domains/pageData'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'

type StatsPageDataDomain = Extract<PageDataDomain, 'stats.overview' | 'stats.accountUsage' | 'stats.aiPerformance'>

interface StatsPageDataResourceOptions<T> {
  apply: (data: T, phase: 'initial' | 'confirmation') => void
  domain: StatsPageDataDomain
  force?: boolean
  isCurrent?: () => boolean
  isManagementView: boolean
  loadNetwork: () => Promise<T>
  query: unknown
  route: string
  targetSystemAccountId?: string
}

const statsPageDataResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export async function loadStatsPageDataResource<T>(options: StatsPageDataResourceOptions<T>): Promise<T> {
  const scope = statsPageDataScope(options.isManagementView, options.targetSystemAccountId)
  if (options.force) await statsPageDataResourceCache.invalidate(options.domain, scope, options.route)
  const result = await statsPageDataResourceCache.load<T>({
    cacheKey: {
      scope,
      route: options.route,
      query: options.query,
      version: 1
    },
    domain: options.domain,
    viewScope: options.isManagementView ? 'admin' : 'self',
    ...(options.isManagementView && options.targetSystemAccountId
      ? { targetSystemAccountId: options.targetSystemAccountId }
      : {}),
    loadNetwork: options.loadNetwork
  })
  applyIfCurrent(options, result.data, 'initial')
  void result.confirmation?.then((outcome) => {
    if (outcome.data !== undefined) applyIfCurrent(options, outcome.data, 'confirmation')
  })
  return result.data
}

function applyIfCurrent<T>(options: StatsPageDataResourceOptions<T>, data: T, phase: 'initial' | 'confirmation'): void {
  if (options.isCurrent?.() === false) return
  options.apply(data, phase)
}

function statsPageDataScope(isManagementView: boolean, targetSystemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    targetSystemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}
