import type { SchedulingPreferenceOptionSummary } from '@/types/domain'

interface SchedulingPreferenceOptionsApi {
  options(params?: {
    ids?: string[]
    keyword?: string
    limit?: number
    activeOnly?: boolean
    systemAccountId?: string
  }): Promise<SchedulingPreferenceOptionSummary[]>
}

interface SchedulingPreferenceOptionsResourceOptions {
  api: SchedulingPreferenceOptionsApi
  apply: (options: SchedulingPreferenceOptionSummary[]) => void
  isCurrent?: () => boolean
  keyword?: string
  selectedIds?: string[]
  systemAccountId?: string
}

/** options 窗口按需加载：先取名称窗口，再补齐已选缺失 ID（activeOnly=false 保证停用/历史绑定仍可回显）。 */
export async function loadSchedulingPreferenceOptionsResource(options: SchedulingPreferenceOptionsResourceOptions): Promise<SchedulingPreferenceOptionSummary[]> {
  const keyword = options.keyword?.trim() || undefined
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].sort()
  const windowOptions = await options.api.options({ keyword, limit: 50, activeOnly: false, systemAccountId: options.systemAccountId })
  const missingIds = selectedIds.filter((id) => !windowOptions.some((item) => item.id === id))
  const result = missingIds.length
    ? mergeSchedulingPreferenceOptionsById(
        await options.api.options({ ids: missingIds, limit: Math.min(50, missingIds.length), activeOnly: false, systemAccountId: options.systemAccountId }),
        windowOptions
      )
    : windowOptions
  if (options.isCurrent?.() === false) return result
  options.apply(result)
  return result
}

function mergeSchedulingPreferenceOptionsById(leading: SchedulingPreferenceOptionSummary[], trailing: SchedulingPreferenceOptionSummary[]): SchedulingPreferenceOptionSummary[] {
  const merged = new Map<string, SchedulingPreferenceOptionSummary>()
  for (const item of [...leading, ...trailing]) merged.set(item.id, item)
  return [...merged.values()]
}
