import { ref, type Ref } from 'vue'
import { message } from '@/lib/antd'

import { extractApiErrorMessage } from '@/shared/apiError'
import { rememberGroupLabels, type GroupSelection } from '@/shared/groupLabelCache'
import { removeLocalSelectPreferenceValues } from '@/shared/selectLocalPreferenceCache'
import type { GroupOptionSummary } from '@/types/domain'

interface UsageRecordGroupOptionsApi {
  options(params: {
    systemAccountId?: string
    keyword?: string
    ids?: string[]
    limit?: number
  }): Promise<GroupOptionSummary[]>
}

interface UseUsageRecordGroupOptionsInput {
  groupFilterSelection: Ref<GroupSelection | undefined>
  groupsApi: UsageRecordGroupOptionsApi
  isManagementView: Ref<boolean>
  onSelectedGroupMissing: () => void
  selectedGroupId: () => string | undefined
  systemAccountId: () => string | undefined
}

export function useUsageRecordGroupOptions(input: UseUsageRecordGroupOptionsInput) {
  const groups = ref<GroupOptionSummary[]>([])
  const loading = ref(false)
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let keyword = ''
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined

  async function load(nextKeyword = keyword, force = false): Promise<void> {
    keyword = nextKeyword
    const systemAccountId = input.isManagementView.value ? input.systemAccountId() : undefined
    const requestKeyword = normalizeOptionKeyword(nextKeyword)
    const selectedIds = [input.selectedGroupId()].filter((id): id is string => Boolean(id))
    const requestKey = JSON.stringify([
      input.isManagementView.value,
      systemAccountId ?? '',
      requestKeyword ?? '',
      selectedIds
    ])
    if (!force && loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let nextGroups = await input.groupsApi.options({ systemAccountId, keyword: requestKeyword, limit: 50 })
        nextGroups = await ensureSelectedGroupOptions(nextGroups, systemAccountId)
        applyGroups(nextGroups, currentRequestId)
      } catch (error) {
        if (currentRequestId !== requestId) return
        console.error(error)
        message.error(extractApiErrorMessage(error, '加载分组选项失败'))
      } finally {
        if (loadingKey === requestKey) {
          loadingKey = undefined
          loadingPromise = undefined
        }
        if (currentRequestId === requestId) {
          loading.value = false
        }
      }
    })()
    return loadingPromise
  }

  function applyGroups(nextGroups: GroupOptionSummary[], currentRequestId: number): void {
    if (currentRequestId !== requestId) return
    rememberGroupLabels(nextGroups)
    syncSelectedGroupSelection(nextGroups)
    groups.value = nextGroups
  }

  function handleDropdown(open: boolean): void {
    if (open) {
      void load()
    }
  }

  function handleSearch(value: string): void {
    keyword = value
    clearSearchTimer()
    searchTimer = window.setTimeout(() => {
      searchTimer = undefined
      void load(keyword)
    }, 250)
  }

  function resetSearch(): void {
    keyword = ''
    clearSearchTimer()
  }

  function clearSearchTimer(): void {
    if (searchTimer && typeof window !== 'undefined') {
      window.clearTimeout(searchTimer)
      searchTimer = undefined
    }
  }

  function selectedGroupSelection(id: string | undefined): GroupSelection | undefined {
    const normalizedId = id?.trim()
    if (!normalizedId) return undefined
    const group = groups.value.find((item) => item.id === normalizedId)
    if (group) return { id: group.id, name: group.name }
    if (input.groupFilterSelection.value?.id === normalizedId) return input.groupFilterSelection.value
    return undefined
  }

  function syncSelectedGroupSelection(nextGroups = groups.value): void {
    const groupFilterId = input.selectedGroupId()
    if (!groupFilterId) return
    input.groupFilterSelection.value = selectedGroupFromOptions(groupFilterId, nextGroups, input.groupFilterSelection.value)
  }

  async function ensureSelectedGroupOptions(
    nextGroups: GroupOptionSummary[],
    systemAccountId: string | undefined
  ): Promise<GroupOptionSummary[]> {
    const selectedIds = [input.selectedGroupId()]
      .map((id) => id?.trim())
      .filter((id): id is string => Boolean(id))
    const missingIds = [...new Set(selectedIds)].filter((id) => !nextGroups.some((group) => group.id === id))
    if (!missingIds.length) return nextGroups
    const selectedGroups = await Promise.all(missingIds.map(async (id) => {
      try {
        return await input.groupsApi.options({ systemAccountId, ids: [id], limit: 1 })
      } catch {
        return []
      }
    }))
    const foundIds = new Set(selectedGroups.flat().map((group) => group.id))
    handleMissingGroupOptions(missingIds.filter((id) => !foundIds.has(id)))
    return mergeOptionsById(selectedGroups.flat(), nextGroups)
  }

  function handleMissingGroupOptions(ids: string[]): void {
    const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (!missingIds.length) return
    removeLocalSelectPreferenceValues('groups', missingIds)
    const selectedGroupId = input.selectedGroupId()?.trim()
    if (selectedGroupId && missingIds.includes(selectedGroupId)) {
      input.groupFilterSelection.value = undefined
      input.onSelectedGroupMissing()
      message.warning('已移除不存在或无权访问的分组，请重新选择')
    }
  }

  return {
    clearSearchTimer,
    groups,
    handleDropdown,
    handleSearch,
    load,
    loading,
    resetSearch,
    selectedGroupSelection,
    syncSelectedGroupSelection
  }
}

function selectedGroupFromOptions(
  id: string | undefined,
  nextGroups: GroupOptionSummary[],
  fallback?: GroupSelection
): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = nextGroups.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  return fallback?.id === normalizedId ? fallback : undefined
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

function normalizeOptionKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
}
