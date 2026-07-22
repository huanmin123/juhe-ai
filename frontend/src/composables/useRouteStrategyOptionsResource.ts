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

export async function loadRouteStrategyOptionsResource(options: RouteStrategyOptionsResourceOptions): Promise<RouteStrategyOptionSummary[]> {
  const keyword = options.keyword?.trim() || undefined
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].sort()
  const windowOptions = await options.api.options({ keyword, limit: 50, activeOnly: false, systemAccountId: options.systemAccountId })
  const missingIds = selectedIds.filter((id) => !windowOptions.some((item) => item.id === id))
  const selectedOptions = missingIds.length
    ? await options.api.options({ ids: missingIds, limit: missingIds.length, activeOnly: false, systemAccountId: options.systemAccountId })
    : []
  const result = mergeRouteStrategyOptionsById(selectedOptions, windowOptions)
  applyIfCurrent(options, result)
  return result
}

function applyIfCurrent(options: RouteStrategyOptionsResourceOptions, value: RouteStrategyOptionSummary[]): void {
  if (options.isCurrent?.() === false) return
  options.apply(value)
}

function mergeRouteStrategyOptionsById(leading: RouteStrategyOptionSummary[], trailing: RouteStrategyOptionSummary[]): RouteStrategyOptionSummary[] {
  const merged = new Map<string, RouteStrategyOptionSummary>()
  for (const item of [...leading, ...trailing]) merged.set(item.id, item)
  return [...merged.values()]
}
