import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, ref } from 'vue'

import { api } from '@/api/client'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import type { AccountOptionSummary, GroupOptionSummary } from '@/types/domain'
import type { AuthorizationFilterResourceType } from './authorizationTableColumns'

export type AuthorizationUsageResourceFilters = {
  resourceOwnerSystemAccountId: string
  resourceType: AuthorizationFilterResourceType
  resourceId?: string
}

export function useAuthorizationUsageResourceFilters(filters: AuthorizationUsageResourceFilters) {
  const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
  const accounts = ref<AccountOptionSummary[]>([])
  const groups = ref<GroupOptionSummary[]>([])
  const resourceOptionsLoading = ref(false)
  const resourceOptionLimit = 50
  const searchDelayMs = 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchKeyword = ''
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined
  const selectedResourceOwnerSystemAccountId = computed(() => {
    return isManagementView.value ? scopedSystemAccountId(filters.resourceOwnerSystemAccountId) : undefined
  })
  const ownAuthorizableAccounts = computed(() => accounts.value.filter((account) => account.permissions?.canAuthorize !== false))
  const ownAuthorizableGroups = computed(() => groups.value.filter((group) => group.permissions?.canAuthorize !== false))
  const resourceOptions = computed(() => {
    if (filters.resourceType === 'all') return []
    if (filters.resourceType === 'account') {
      return ownAuthorizableAccounts.value
        .filter((account) => matchesSelectedResourceOwner(account))
        .map((account) => ({ label: account.name, value: account.id }))
    }
    return ownAuthorizableGroups.value
      .filter((group) => matchesSelectedResourceOwner(group))
      .map((group) => ({ label: group.name, value: group.id }))
  })

  async function loadAuthorizableResourceOptions(keyword = searchKeyword) {
    searchKeyword = keyword
    if (filters.resourceType === 'all') {
      accounts.value = []
      groups.value = []
      loadingKey = undefined
      loadingPromise = undefined
      return
    }
    const ownerSystemAccountId = selectedResourceOwnerSystemAccountId.value
    const normalizedKeyword = normalizeSearchKeyword(keyword)
    const requestKey = JSON.stringify([filters.resourceType, ownerSystemAccountId ?? '', normalizedKeyword ?? '', filters.resourceId ?? ''])
    if (loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    resourceOptionsLoading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        if (filters.resourceType === 'account') {
          let nextAccounts = isManagementView.value
            ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: normalizedKeyword, limit: resourceOptionLimit })
            : await api.myAccounts.options({ keyword: normalizedKeyword, limit: resourceOptionLimit })
          nextAccounts = await ensureSelectedAccountOption(nextAccounts, ownerSystemAccountId)
          if (currentRequestId !== requestId) return
          accounts.value = nextAccounts
          groups.value = []
        } else {
          let nextGroups = isManagementView.value
            ? await api.groups.options({ systemAccountId: ownerSystemAccountId, keyword: normalizedKeyword, limit: resourceOptionLimit })
            : await api.myGroups.options({ keyword: normalizedKeyword, limit: resourceOptionLimit })
          nextGroups = await ensureSelectedGroupOption(nextGroups, ownerSystemAccountId)
          if (currentRequestId !== requestId) return
          groups.value = nextGroups
          accounts.value = []
        }
      } catch (error) {
        if (currentRequestId !== requestId) return
        console.error(error)
        message.error(filters.resourceType === 'account' ? '加载 AI 账户失败' : '加载分组失败')
      } finally {
        if (loadingKey === requestKey) {
          loadingKey = undefined
          loadingPromise = undefined
        }
        if (currentRequestId === requestId) {
          resourceOptionsLoading.value = false
        }
      }
    })()
    return loadingPromise
  }

  function resetResourceId() {
    filters.resourceId = undefined
  }

  function handleResourceOptionsDropdown(open: boolean) {
    if (open) {
      void loadAuthorizableResourceOptions()
    }
  }

  function handleResourceOptionsSearch(value: string) {
    searchKeyword = value
    clearSearchTimer()
    searchTimer = window.setTimeout(() => {
      searchTimer = undefined
      void loadAuthorizableResourceOptions(searchKeyword)
    }, searchDelayMs)
  }

  function resetResourceOptionsSearch() {
    searchKeyword = ''
    clearSearchTimer()
  }

  function clearSearchTimer() {
    if (searchTimer && typeof window !== 'undefined') {
      window.clearTimeout(searchTimer)
      searchTimer = undefined
    }
  }

  async function ensureSelectedAccountOption(nextAccounts: AccountOptionSummary[], ownerSystemAccountId: string | undefined): Promise<AccountOptionSummary[]> {
    const selectedId = filters.resourceId?.trim()
    if (!selectedId || nextAccounts.some((account) => account.id === selectedId)) return nextAccounts
    try {
      const selected = isManagementView.value
        ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: selectedId, limit: 1 })
        : await api.myAccounts.options({ keyword: selectedId, limit: 1 })
      return mergeOptionsById(selected, nextAccounts)
    } catch {
      return nextAccounts
    }
  }

  async function ensureSelectedGroupOption(nextGroups: GroupOptionSummary[], ownerSystemAccountId: string | undefined): Promise<GroupOptionSummary[]> {
    const selectedId = filters.resourceId?.trim()
    if (!selectedId || nextGroups.some((group) => group.id === selectedId)) return nextGroups
    try {
      const selected = isManagementView.value
        ? await api.groups.options({ systemAccountId: ownerSystemAccountId, keyword: selectedId, limit: 1 })
        : await api.myGroups.options({ keyword: selectedId, limit: 1 })
      return mergeOptionsById(selected, nextGroups)
    } catch {
      return nextGroups
    }
  }

  function matchesSelectedResourceOwner(resource: Pick<AccountOptionSummary | GroupOptionSummary, 'ownerSystemAccountId' | 'systemAccountId'>): boolean {
    const ownerSystemAccountId = selectedResourceOwnerSystemAccountId.value
    if (!ownerSystemAccountId) return true
    return (resource.ownerSystemAccountId ?? resource.systemAccountId) === ownerSystemAccountId
  }

  onBeforeUnmount(clearSearchTimer)

  return {
    isManagementView,
    scopedSystemAccountId,
    accounts,
    groups,
    selectedResourceOwnerSystemAccountId,
    resourceOptions,
    resourceOptionsLoading,
    handleResourceOptionsDropdown,
    handleResourceOptionsSearch,
    loadAuthorizableResourceOptions,
    resetResourceId,
    resetResourceOptionsSearch
  }
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

function normalizeSearchKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
}
