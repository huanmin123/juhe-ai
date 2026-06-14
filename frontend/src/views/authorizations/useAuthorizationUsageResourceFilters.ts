import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, ref } from 'vue'

import { api } from '@/api/client'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { accountSelectionForId, rememberAccountLabels, rememberAccountSelection, type AccountSelection } from '@/shared/accountLabelCache'
import { mergeSelectedGroupOptions, rememberGroupLabels, type GroupSelection } from '@/shared/groupLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountOptionSummary, GroupOptionSummary } from '@/types/domain'
import type { AuthorizationUsageResourceFilters } from './authorizationUsageFilters'

export function useAuthorizationUsageResourceFilters(filters: AuthorizationUsageResourceFilters) {
  const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
  const accounts = ref<AccountOptionSummary[]>([])
  const groups = ref<GroupOptionSummary[]>([])
  const resourceOptionsLoading = ref(false)
  const resourceOptionLimit = 50
  const searchDelayMs = 250
  const accountOptionCache = createShortLivedQueryCache<AccountOptionSummary[]>({ ttlMs: 10_000 })
  const groupOptionCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: 10_000 })
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchKeyword = ''
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined
  const selectedResourceOwnerSystemAccountId = computed(() => {
    return isManagementView.value ? scopedSystemAccountId(filters.resourceOwnerSystemAccountId) : undefined
  })
  const resourceGroupDisabled = computed(() => {
    return isManagementView.value && filters.resourceType === 'group' && !selectedResourceOwnerSystemAccountId.value
  })
  const ownAuthorizableAccounts = computed(() => accounts.value.filter((account) => account.permissions?.canAuthorize !== false))
  const ownAuthorizableGroups = computed(() => groups.value.filter((group) => group.permissions?.canAuthorize !== false))
  const resourceOptions = computed(() => {
    if (filters.resourceType === 'all') return []
    if (resourceGroupDisabled.value) return []
    if (filters.resourceType === 'account') {
      return ownAuthorizableAccounts.value
        .filter((account) => matchesSelectedResourceOwner(account))
        .map((account) => ({ label: account.name, value: account.id }))
    }
    return mergeSelectedGroupOptions(ownAuthorizableGroups.value
      .filter((group) => matchesSelectedResourceOwner(group))
      .map((group) => ({ label: group.name, value: group.id })), [filters.resourceId], [filters.resourceGroup])
  })

  async function loadAuthorizableResourceOptions(keyword = searchKeyword) {
    searchKeyword = keyword
    if (filters.resourceType === 'all') {
      accounts.value = []
      groups.value = []
      filters.resourceAccount = undefined
      filters.resourceGroup = undefined
      loadingKey = undefined
      loadingPromise = undefined
      return
    }
    if (resourceGroupDisabled.value) {
      requestId += 1
      accounts.value = []
      groups.value = []
      resetResourceId()
      resourceOptionsLoading.value = false
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
    if (filters.resourceType === 'account') {
      const cachedAccounts = accountOptionCache.get(requestKey)
      if (cachedAccounts) {
        loadingKey = undefined
        loadingPromise = undefined
        resourceOptionsLoading.value = false
        accounts.value = cachedAccounts
        rememberAccountLabels(cachedAccounts)
        syncResourceAccount(cachedAccounts)
        groups.value = []
        return
      }
    } else {
      const cachedGroups = groupOptionCache.get(requestKey)
      if (cachedGroups) {
        loadingKey = undefined
        loadingPromise = undefined
        resourceOptionsLoading.value = false
        rememberGroupLabels(cachedGroups)
        syncResourceGroup(cachedGroups)
        groups.value = cachedGroups
        accounts.value = []
        return
      }
    }
    resourceOptionsLoading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        if (filters.resourceType === 'account') {
          let nextAccounts = isManagementView.value
            ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: normalizedKeyword, limit: resourceOptionLimit })
            : await api.myAccounts.options({ keyword: normalizedKeyword, limit: resourceOptionLimit })
          nextAccounts = await ensureSelectedAccountOption(nextAccounts, ownerSystemAccountId)
          rememberAccountLabels(nextAccounts)
          syncResourceAccount(nextAccounts)
          accountOptionCache.set(requestKey, nextAccounts)
          if (currentRequestId !== requestId) return
          accounts.value = nextAccounts
          groups.value = []
        } else {
          let nextGroups = isManagementView.value
            ? await api.groups.options({ systemAccountId: ownerSystemAccountId, keyword: normalizedKeyword, limit: resourceOptionLimit })
            : await api.myGroups.options({ keyword: normalizedKeyword, limit: resourceOptionLimit })
          nextGroups = await ensureSelectedGroupOption(nextGroups, ownerSystemAccountId)
          rememberGroupLabels(nextGroups)
          syncResourceGroup(nextGroups)
          groupOptionCache.set(requestKey, nextGroups)
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
    filters.resourceAccount = undefined
    filters.resourceGroup = undefined
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
        ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, ids: [selectedId], limit: 1 })
        : await api.myAccounts.options({ ids: [selectedId], limit: 1 })
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
        ? await api.groups.options({ systemAccountId: ownerSystemAccountId, ids: [selectedId], limit: 1 })
        : await api.myGroups.options({ ids: [selectedId], limit: 1 })
      return mergeOptionsById(selected, nextGroups)
    } catch {
      return nextGroups
    }
  }

  function syncResourceGroup(nextGroups = groups.value): void {
    if (filters.resourceType !== 'group') {
      filters.resourceGroup = undefined
      return
    }
    filters.resourceAccount = undefined
    filters.resourceGroup = selectedGroupFromOptions(filters.resourceId, nextGroups, filters.resourceGroup)
  }

  function syncResourceAccount(nextAccounts = accounts.value): void {
    if (filters.resourceType !== 'account') {
      filters.resourceAccount = undefined
      return
    }
    filters.resourceGroup = undefined
    filters.resourceAccount = selectedAccountFromOptions(filters.resourceId, nextAccounts, filters.resourceAccount)
    rememberAccountSelection(filters.resourceAccount)
  }

  function selectedGroupFromOptions(id: string | undefined, nextGroups: GroupOptionSummary[], fallback?: GroupSelection): GroupSelection | undefined {
    const normalizedId = id?.trim()
    if (!normalizedId) return undefined
    const group = nextGroups.find((item) => item.id === normalizedId)
    if (group) return { id: group.id, name: group.name }
    return fallback?.id === normalizedId ? fallback : undefined
  }

  function selectedAccountFromOptions(id: string | undefined, nextAccounts: AccountOptionSummary[], fallback?: AccountSelection): AccountSelection | undefined {
    const normalizedId = id?.trim()
    if (!normalizedId) return undefined
    return accountSelectionForId(normalizedId, nextAccounts) ?? (fallback?.id === normalizedId ? fallback : undefined)
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
    resourceGroupDisabled,
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
