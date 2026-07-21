import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, ref } from 'vue'

import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { rememberAccountLabels, rememberAccountSelection } from '@/shared/accountLabelCache'
import { mergeSelectedGroupOptions, rememberGroupLabels } from '@/shared/groupLabelCache'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { AccountOptionSummary, GroupOptionSummary } from '@/types/domain'
import { normalizeSearchKeyword, selectedAccountFromOptions, selectedGroupFromOptions } from './authorizationOptionHelpers'
import { createAuthorizationSearchScheduler } from './authorizationSearchScheduler'
import { ensureSelectedAccountOption, ensureSelectedGroupOption } from './authorizationSelectedOptionLoaders'
import type { AuthorizationUsageResourceFilters } from './authorizationUsageFilters'

const authorizationUsageOptionResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

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
  const searchKeyword = ref('')
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

  async function loadAuthorizableResourceOptions(keyword = searchKeyword.value) {
    searchKeyword.value = keyword
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
    resourceOptionsLoading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        const resourceType: 'account' | 'group' = filters.resourceType === 'account' ? 'account' : 'group'
        const route = resourceType === 'account'
          ? (isManagementView.value ? '/accounts/options' : '/my-accounts/options')
          : (isManagementView.value ? '/groups/options' : '/my-groups/options')
        const result = await authorizationUsageOptionResourceCache.load<AccountOptionSummary[] | GroupOptionSummary[]>({
          cacheKey: {
            scope: authorizationUsageOptionScope(isManagementView.value, ownerSystemAccountId),
            route,
            query: { keyword: normalizedKeyword, ownerSystemAccountId, resourceId: filters.resourceId, limit: resourceOptionLimit },
            version: 1
          },
          domain: filters.resourceType === 'account' ? 'accounts.options' : 'groups.static',
          viewScope: isManagementView.value ? 'admin' : 'self',
          ...(isManagementView.value && ownerSystemAccountId ? { targetSystemAccountId: ownerSystemAccountId } : {}),
          loadNetwork: async () => {
            if (resourceType === 'account') {
              let nextAccounts = isManagementView.value
                ? await api.accounts.options({ systemAccountId: ownerSystemAccountId, keyword: normalizedKeyword, limit: resourceOptionLimit })
                : await api.myAccounts.options({ keyword: normalizedKeyword, limit: resourceOptionLimit })
              return await ensureSelectedAccountOption(nextAccounts, filters.resourceId, ownerSystemAccountId, isManagementView.value)
            }
            let nextGroups = isManagementView.value
              ? await api.groups.authorizationOptions({ systemAccountId: ownerSystemAccountId, keyword: normalizedKeyword, limit: resourceOptionLimit })
              : await api.myGroups.authorizationOptions({ keyword: normalizedKeyword, limit: resourceOptionLimit })
            return await ensureSelectedGroupOption(nextGroups, filters.resourceId, ownerSystemAccountId, isManagementView.value)
          }
        })
        applyResourceOptions(resourceType, result.data, currentRequestId)
        void result.confirmation?.then((outcome) => {
          if (outcome.data) applyResourceOptions(resourceType, outcome.data, currentRequestId)
        })
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

  function applyResourceOptions(
    resourceType: 'account' | 'group',
    nextOptions: AccountOptionSummary[] | GroupOptionSummary[],
    currentRequestId: number
  ): void {
    if (currentRequestId !== requestId || filters.resourceType !== resourceType) return
    if (resourceType === 'account') {
      const nextAccounts = nextOptions as AccountOptionSummary[]
      rememberAccountLabels(nextAccounts)
      syncResourceAccount(nextAccounts)
      accounts.value = nextAccounts
      groups.value = []
      return
    }
    const nextGroups = nextOptions as GroupOptionSummary[]
    rememberGroupLabels(nextGroups)
    syncResourceGroup(nextGroups)
    groups.value = nextGroups
    accounts.value = []
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

  const resourceOptionsSearch = createAuthorizationSearchScheduler({
    delayMs: searchDelayMs,
    keyword: searchKeyword,
    load: loadAuthorizableResourceOptions
  })
  const handleResourceOptionsSearch = resourceOptionsSearch.schedule
  const clearSearchTimer = resourceOptionsSearch.clear

  function resetResourceOptionsSearch() {
    searchKeyword.value = ''
    clearSearchTimer()
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

function authorizationUsageOptionScope(isManagementView: boolean, ownerSystemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    ownerSystemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}
