import type { SecurityPolicyOptionSummary } from '@/types/domain'

interface SecurityPolicyOptionsApi {
  options(params?: {
    ids?: string[]
    keyword?: string
    limit?: number
    systemAccountId?: string
  }): Promise<SecurityPolicyOptionSummary[]>
}

interface SecurityPolicyOptionsResourceOptions {
  api: SecurityPolicyOptionsApi
  apply: (options: SecurityPolicyOptionSummary[]) => void
  isCurrent?: () => boolean
  keyword?: string
  selectedIds?: string[]
  systemAccountId?: string
}

/** options 窗口按需加载：先取名称窗口，再补齐已选缺失 ID（options 恒返回全部行，历史绑定天然可回显）。 */
export async function loadSecurityPolicyOptionsResource(options: SecurityPolicyOptionsResourceOptions): Promise<SecurityPolicyOptionSummary[]> {
  const keyword = options.keyword?.trim() || undefined
  const selectedIds = [...new Set((options.selectedIds ?? []).map((id) => id.trim()).filter(Boolean))].sort()
  const windowOptions = await options.api.options({ keyword, limit: 50, systemAccountId: options.systemAccountId })
  const missingIds = selectedIds.filter((id) => !windowOptions.some((item) => item.id === id))
  const result = missingIds.length
    ? mergeSecurityPolicyOptionsById(
        await options.api.options({ ids: missingIds, limit: Math.min(50, missingIds.length), systemAccountId: options.systemAccountId }),
        windowOptions
      )
    : windowOptions
  if (options.isCurrent?.() === false) return result
  options.apply(result)
  return result
}

function mergeSecurityPolicyOptionsById(leading: SecurityPolicyOptionSummary[], trailing: SecurityPolicyOptionSummary[]): SecurityPolicyOptionSummary[] {
  const merged = new Map<string, SecurityPolicyOptionSummary>()
  for (const item of [...leading, ...trailing]) merged.set(item.id, item)
  return [...merged.values()]
}
