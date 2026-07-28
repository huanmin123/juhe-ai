import type { RouteStrategyGroupOption } from '@/types/domain'

export interface GroupOptionsResourceApi {
  routeStrategyOptions(params?: {
    ids?: string[]
    keyword?: string
    limit?: number
    providerCode?: string
    systemAccountId?: string
  }): Promise<RouteStrategyGroupOption[]>
}

interface GroupOptionsResourceOptions {
  api: GroupOptionsResourceApi
  apply: (groups: RouteStrategyGroupOption[]) => void
  force?: boolean
  isCurrent?: () => boolean
  isManagementView: boolean
  keyword?: string
  selectedIds?: string[]
  selectedOptions?: RouteStrategyGroupOption[]
  systemAccountId?: string
}

export async function loadGroupOptionsResource(options: GroupOptionsResourceOptions): Promise<RouteStrategyGroupOption[]> {
  const keyword = options.keyword?.trim() || undefined
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].sort()
  const windowGroups = await options.api.routeStrategyOptions({
    keyword,
    limit: 50,
    systemAccountId: options.systemAccountId
  })
  const knownSelectedGroups = (options.selectedOptions ?? []).filter((group) => selectedIds.includes(group.id))
  const groupsWithKnownSelections = mergeGroupOptionsById(knownSelectedGroups, windowGroups)
  const missingIds = selectedIds.filter((id) => !groupsWithKnownSelections.some((group) => group.id === id))
  const result = missingIds.length
    ? mergeGroupOptionsById(await options.api.routeStrategyOptions({
        ids: missingIds,
        limit: missingIds.length,
        systemAccountId: options.systemAccountId
      }), groupsWithKnownSelections)
    : groupsWithKnownSelections
  applyIfCurrent(options, result)
  return result
}

function applyIfCurrent(options: GroupOptionsResourceOptions, value: RouteStrategyGroupOption[]): void {
  if (options.isCurrent?.() === false) return
  options.apply(value)
}

function mergeGroupOptionsById(leading: RouteStrategyGroupOption[], trailing: RouteStrategyGroupOption[]): RouteStrategyGroupOption[] {
  const merged = new Map<string, RouteStrategyGroupOption>()
  for (const item of [...leading, ...trailing]) merged.set(item.id, item)
  return [...merged.values()]
}
