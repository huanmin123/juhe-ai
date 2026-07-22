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

export async function loadGroupOptionsResource(options: GroupOptionsResourceOptions): Promise<GroupOptionSummary[]> {
  const keyword = options.keyword?.trim() || undefined
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].sort()
  const windowGroups = await options.api.options({
    keyword,
    limit: 50,
    manageableOnly: true,
    systemAccountId: options.systemAccountId
  })
  const missingIds = selectedIds.filter((id) => !windowGroups.some((group) => group.id === id))
  const selectedGroups = missingIds.length
    ? await options.api.options({
        ids: missingIds,
        limit: missingIds.length,
        manageableOnly: true,
        systemAccountId: options.systemAccountId
      })
    : []
  const result = mergeGroupOptionsById(selectedGroups, windowGroups)
  applyIfCurrent(options, result)
  return result
}

function applyIfCurrent(options: GroupOptionsResourceOptions, value: GroupOptionSummary[]): void {
  if (options.isCurrent?.() === false) return
  options.apply(value)
}

function mergeGroupOptionsById(leading: GroupOptionSummary[], trailing: GroupOptionSummary[]): GroupOptionSummary[] {
  const merged = new Map<string, GroupOptionSummary>()
  for (const item of [...leading, ...trailing]) merged.set(item.id, item)
  return [...merged.values()]
}
