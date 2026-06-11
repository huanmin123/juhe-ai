import { message } from '@/lib/antd'
import { computed, reactive, ref, watch, type ComputedRef } from 'vue'

import { api, type AccountListParams, type AccountListSortParam } from '@/api/client'
import type { ResponsiveDataListSort } from '@/components/responsiveDataListSorting'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { formatNumber } from '@/shared/formatters'
import { rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import type { AccountSummary, ProviderDefinition, ProxyProfileOptionSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { AccountFilters } from './accountFormTypes'
import { ACCOUNT_PAGE_SIZE, FALLBACK_PROVIDERS } from './accountOptions'
import { countActiveAccountFilters } from './accountListFilters'
import { normalizeAccountTableSorts } from './accountTableColumns'
import { canSelectAccountForBatch } from './accountRules'

interface AccountsPageState {
  filters: AccountFilters
  pagination: { current: number; pageSize: number }
  sorts: AccountListSortParam[]
}

interface UseAccountListDataOptions {
  isManagementView: ComputedRef<boolean>
  scopedSystemAccountId: (filterValue?: string) => string | undefined
  onLoaded?: (selectableAccountIds: Set<string>) => void
}

const defaultAccountsPageState = (): AccountsPageState => ({
  filters: { keyword: '', providerCode: 'all', type: 'all', groupId: '', group: undefined, tagIds: [], status: [], systemAccountId: allSystemAccountsValue, systemAccount: undefined },
  pagination: { current: 1, pageSize: ACCOUNT_PAGE_SIZE },
  sorts: [{ field: 'priority', order: 'asc' }]
})

export function useAccountListData(options: UseAccountListDataOptions) {
  const pageStateCache = usePageStateCache<AccountsPageState>(undefined, defaultAccountsPageState, { version: 8 })
  const initialPageState = pageStateCache.read()
  rememberGroupSelection(initialPageState.filters.group)
  rememberPrincipalSelection(initialPageState.filters.systemAccount)
  const providers = ref<ProviderDefinition[]>([])
  const proxies = ref<ProxyProfileOptionSummary[]>([])
  const accountOptionsLoaded = ref(false)
  const accountOptionsScopeKey = ref('')
  const accountOptionsInFlight = new Map<string, Promise<void>>()
  const accountSorts = ref<AccountListSortParam[]>(initialPageState.sorts)
  const filters = reactive<AccountFilters>({ ...initialPageState.filters })
  const {
    handleDropdown: handleSystemAccountOptionsDropdown,
    handleSearch: handleSystemAccountOptionsSearch,
    loading: systemAccountOptionsLoading,
    resetSearch: resetSystemAccountOptionsSearch,
    systemAccounts
  } = useRemoteSystemAccountOptions({
    enabled: () => options.isManagementView.value,
    onMissingSelectedIds: (ids) => {
      if (!ids.includes(filters.systemAccountId)) return
      filters.systemAccountId = allSystemAccountsValue
      filters.systemAccount = undefined
      resetAccountPagination()
      void loadData({ forceOptions: true })
    },
    selectedIds: () => [filters.systemAccountId]
  })

  const accountScopeParams = computed(() => {
    const systemAccountId = options.scopedSystemAccountId(filters.systemAccountId)
    return systemAccountId ? { systemAccountId } : undefined
  })
  const activeAdvancedFilterCount = computed(() => countActiveAccountFilters(filters, options.isManagementView.value, allSystemAccountsValue))

  const {
    items: accounts,
    loading,
    mobileHasMore: mobileHasMoreAccounts,
    mobileLoadingMore,
    pagination: accountPagination,
    tablePagination: accountTablePagination,
    handleTableChange: handleAccountTableChange,
    loadData,
    loadMoreMobile: loadMoreMobileAccounts,
    removeItems: removeAccountItems,
    refreshMobile: refreshMobileAccounts,
    resetPagination: resetAccountListPagination
  } = useResponsivePagedList<AccountSummary, { forceOptions?: boolean }>({
    pageSize: ACCOUNT_PAGE_SIZE,
    initialPagination: initialPageState.pagination,
    showTotal: (total, range, context) => context?.hasMore
      ? `已加载到第 ${formatNumber(range?.[1] ?? Math.max(0, total - 1))} 个账户，还有更多`
      : `共 ${formatNumber(total)} 个账户`,
    fetchPage: async (_loadOptions, pageState) => {
      const systemAccountId = options.isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined
      const accountList = await fetchAccountList(systemAccountId, pageState)
      return {
        items: accountList.items,
        page: accountList.page,
        pageSize: accountList.pageSize,
        total: accountList.total,
        hasMore: accountList.hasMore
      }
    },
    onLoaded: () => {
      const selectableAccountIds = new Set(accounts.value.filter(canSelectAccountForBatch).map((account) => account.id))
      options.onLoaded?.(selectableAccountIds)
    },
    onError: (error) => {
      console.error(error)
      message.error('加载账户失败')
    }
  })
  const filteredAccounts = computed(() => accounts.value)
  const mobileRefreshing = computed(() => loading.value)
  const mobileVisibleAccounts = computed(() => filteredAccounts.value)

  function fetchAccountList(systemAccountId: string | undefined, pageState: { current: number; pageSize: number }) {
    return options.isManagementView.value
      ? api.accounts.list(accountListParams(systemAccountId, pageState))
      : api.myAccounts.list(accountListParams(undefined, pageState))
  }

  function refreshData() {
    resetSystemAccountOptionsSearch()
    void loadData({ forceOptions: true })
  }

  function applyFilters() {
    filters.keyword = filters.keyword.trim()
    resetAccountPagination()
    void loadData()
  }

  function handleAccountTableChangeAndLoad(paginationInfo: unknown): void {
    handleAccountTableChange(paginationInfo)
  }

  async function handleAccountSortChange(sorts: ResponsiveDataListSort[]) {
    accountSorts.value = normalizeAccountTableSorts(sorts)
    resetAccountPagination()
    await loadData()
  }

  function handleSystemAccountFilterChange() {
    resetSystemAccountOptionsSearch()
    resetAccountPagination()
    void loadData({ forceOptions: true })
  }

  function resetFilters() {
    const defaults = defaultAccountsPageState()
    Object.assign(filters, defaults.filters)
    accountSorts.value = defaults.sorts
    resetSystemAccountOptionsSearch()
    resetAccountListPagination()
    pageStateCache.clear()
    void loadData()
  }

  function resetAccountPagination() {
    accountPagination.current = 1
    resetAccountListPagination()
  }

  function focusCreatedAccount(account: AccountSummary): void {
    filters.keyword = account.name
    filters.providerCode = 'all'
    filters.type = 'all'
    filters.groupId = ''
    filters.group = undefined
    filters.tagIds = []
    filters.status = []
    resetAccountPagination()
  }

  function removeLoadedAccount(accountId: string): boolean {
    return removeAccountItems((account) => account.id === accountId) > 0
  }

  function snapshotPageState(): AccountsPageState {
    return {
      filters: { ...filters, tagIds: [...filters.tagIds], status: [...filters.status] },
      pagination: { current: accountPagination.current, pageSize: accountPagination.pageSize },
      sorts: accountSorts.value
    }
  }

  async function loadAccountOptions(systemAccountId: string | undefined, force = false): Promise<void> {
    const scopeKey = options.isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self'
    if (!force && accountOptionsLoaded.value && accountOptionsScopeKey.value === scopeKey) {
      return
    }
    const currentScopeKey = () => options.isManagementView.value
      ? `management:${accountScopeParams.value?.systemAccountId ?? 'all'}`
      : 'self'
    const existingRequest = !force ? accountOptionsInFlight.get(scopeKey) : undefined
    if (existingRequest) {
      return existingRequest
    }

    const requestRef: { current?: Promise<void> } = {}
    const request = (async () => {
      const [providerList, proxyList] = await Promise.all([
        api.providers.options(),
        api.proxies.options({ limit: 50 })
      ])
      if (currentScopeKey() !== scopeKey || accountOptionsInFlight.get(scopeKey) !== requestRef.current) {
        return
      }
      providers.value = providerList.length ? providerList : FALLBACK_PROVIDERS
      proxies.value = proxyList
      accountOptionsLoaded.value = true
      accountOptionsScopeKey.value = scopeKey
    })().finally(() => {
      if (accountOptionsInFlight.get(scopeKey) === requestRef.current) {
        accountOptionsInFlight.delete(scopeKey)
      }
    })
    requestRef.current = request
    accountOptionsInFlight.set(scopeKey, request)
    return request
  }

  function accountListParams(systemAccountId: string | undefined, pageState: { current: number; pageSize: number }): AccountListParams {
    return {
      systemAccountId,
      sorts: accountSorts.value,
      page: pageState.current,
      pageSize: pageState.pageSize,
      keyword: filters.keyword.trim() || undefined,
      providerCode: filters.providerCode && filters.providerCode !== 'all' ? filters.providerCode : undefined,
      type: filters.type && filters.type !== 'all' ? filters.type : undefined,
      groupId: filters.groupId || undefined,
      tagIds: filters.tagIds,
      status: filters.status
    }
  }

  watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
  watch(() => filters.group, (group) => rememberGroupSelection(group), { deep: true, immediate: true })
  watch(() => filters.systemAccount, (account) => rememberPrincipalSelection(account), { deep: true, immediate: true })

  return {
    loading,
    accounts,
    providers,
    proxies,
    systemAccounts,
    filters,
    accountSorts,
    accountPagination,
    accountScopeParams,
    filteredAccounts,
    activeAdvancedFilterCount,
    mobileHasMoreAccounts,
    mobileLoadingMore,
    mobileRefreshing,
    mobileVisibleAccounts,
    accountTablePagination,
    systemAccountOptionsLoading,
    handleSystemAccountOptionsDropdown,
    handleSystemAccountOptionsSearch,
    loadMoreMobileAccounts,
    refreshMobileAccounts,
    loadData,
    loadAccountOptions,
    refreshData,
    applyFilters,
    handleAccountTableChangeAndLoad,
    handleAccountSortChange,
    handleSystemAccountFilterChange,
    focusCreatedAccount,
    removeLoadedAccount,
    resetAccountPagination,
    resetFilters
  }
}
