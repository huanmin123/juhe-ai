import { pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { GroupOptionSummary } from '@/types/domain'

export interface GroupOptionsResourceApi {
  options(params?: {
    ids?: string[]
    keyword?: string
    limit?: number
    manageableOnly?: boolean
    providerCode?: string
    systemAccountId?: string
  }): Promise<GroupOptionSummary[]>
}

interface GroupOptionsResourceOptions {
  api: GroupOptionsResourceApi
  apply: (groups: GroupOptionSummary[]) => void
  force?: boolean
  isCurrent?: () => boolean
  isManagementView: boolean
  keyword?: string
  selectedIds?: string[]
  systemAccountId?: string
}

const groupOptionsResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export async function loadGroupOptionsResource(options: GroupOptionsResourceOptions): Promise<GroupOptionSummary[]> {
  const keyword = options.keyword?.trim() || undefined
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].sort()
  const route = options.isManagementView ? '/groups/options' : '/my-groups/options'
  const scope = groupOptionsScope(options.isManagementView, options.systemAccountId)
  if (options.force) await groupOptionsResourceCache.invalidate('groups.static', scope, route)
  const result = await groupOptionsResourceCache.load<GroupOptionSummary[]>({
    cacheKey: {
      scope,
      route,
      query: { keyword, selectedIds, manageableOnly: true, systemAccountId: options.systemAccountId, limit: 50 },
      version: 1
    },
    domain: 'groups.static',
    viewScope: options.isManagementView ? 'admin' : 'self',
    ...(options.isManagementView && options.systemAccountId
      ? { targetSystemAccountId: options.systemAccountId }
      : {}),
    loadNetwork: async () => {
      const windowGroups = await options.api.options({
        keyword,
        limit: 50,
        manageableOnly: true,
        systemAccountId: options.systemAccountId
      })
      const missingIds = selectedIds.filter((id) => !windowGroups.some((group) => group.id === id))
      if (!missingIds.length) return windowGroups
      const selectedGroups = await options.api.options({
        ids: missingIds,
        limit: missingIds.length,
        manageableOnly: true,
        systemAccountId: options.systemAccountId
      })
      return mergeGroupOptionsById(selectedGroups, windowGroups)
    }
  })
  applyIfCurrent(options, result.data)
  void result.confirmation?.then((outcome) => {
    if (outcome.data) applyIfCurrent(options, outcome.data)
  })
  return result.data
}

function applyIfCurrent(options: GroupOptionsResourceOptions, value: GroupOptionSummary[]): void {
  if (options.isCurrent?.() === false) return
  options.apply(value)
}

function groupOptionsScope(isManagementView: boolean, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    systemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}

function mergeGroupOptionsById(leading: GroupOptionSummary[], trailing: GroupOptionSummary[]): GroupOptionSummary[] {
  const merged = new Map<string, GroupOptionSummary>()
  for (const item of [...leading, ...trailing]) merged.set(item.id, item)
  return [...merged.values()]
}
