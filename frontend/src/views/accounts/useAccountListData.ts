import { message } from '@/lib/antd'
import { computed, reactive, ref, watch, type ComputedRef } from 'vue'

import { api, type AccountListParams, type AccountListSortParam } from '@/api/client'
import type { ResponsiveDataListSort } from '@/components/responsiveDataListSorting'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import type { AccountSummary, ProviderDefinition, ProxyProfileOptionSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { AccountFilters } from './accountFormTypes'
import { ACCOUNT_PAGE_SIZE, FALLBACK_PROVIDER } from './accountOptions'
import { countActiveAccountFilters } from './accountListFilters'
import { normalizeAccountTableSorts } from './accountTableColumns'
import { canBatchManageAccount } from './accountRules'
import { useAccountMobilePagination } from './useAccountMobilePagination'

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
  filters: { keyword: '', groupId: '', type: 'all', status: [], systemAccountId: allSystemAccountsValue },
  pagination: { current: 1, pageSize: ACCOUNT_PAGE_SIZE },
  sorts: [{ field: 'priority', order: 'asc' }]
})

export function useAccountListData(options: UseAccountListDataOptions) {
  const pageStateCache = usePageStateCache<AccountsPageState>(undefined, defaultAccountsPageState, { version: 4 })
  const initialPageState = pageStateCache.read()
  const loading = ref(false)
  const accounts = ref<AccountSummary[]>([])
  const providers = ref<ProviderDefinition[]>([])
  const proxies = ref<ProxyProfileOptionSummary[]>([])
  const accountOptionsLoaded = ref(false)
  const accountOptionsScopeKey = ref('')
  const accountOptionsInFlight = new Map<string, Promise<void>>()
  let accountListRequestId = 0
  let loadingRequestId = 0
  const accountSorts = ref<AccountListSortParam[]>(initialPageState.sorts)
  const filters = reactive<AccountFilters>({ ...initialPageState.filters })
  const accountPagination = reactive({
    current: initialPageState.pagination.current,
    pageSize: initialPageState.pagination.pageSize,
    total: 0
  })
  const {
    handleDropdown: handleSystemAccountOptionsDropdown,
    handleSearch: handleSystemAccountOptionsSearch,
    load: loadSystemAccountOptions,
    loading: systemAccountOptionsLoading,
    resetSearch: resetSystemAccountOptionsSearch,
    systemAccounts
  } = useRemoteSystemAccountOptions({
    enabled: () => options.isManagementView.value,
    selectedIds: () => [filters.systemAccountId]
  })

  const accountScopeParams = computed(() => {
    const systemAccountId = options.scopedSystemAccountId(filters.systemAccountId)
    return systemAccountId ? { systemAccountId } : undefined
  })
  const filteredAccounts = computed(() => accounts.value)
  const activeAdvancedFilterCount = computed(() => countActiveAccountFilters(filters, options.isManagementView.value, allSystemAccountsValue))

  const {
    mobileHasMore: mobileHasMoreAccounts,
    mobileLoadingMore,
    mobileRefreshing,
    tablePagination: accountTablePagination,
    handleTableChange: handleAccountTableChange,
    loadMoreMobile: loadMoreMobileAccounts,
    refreshMobile: refreshMobileAccounts,
    resetPagination: resetAccountListPagination
  } = useAccountMobilePagination(ACCOUNT_PAGE_SIZE, () => accountPagination.total, loadData, accountPagination)
  const mobileVisibleAccounts = computed(() => filteredAccounts.value)

  async function loadData(loadOptions: { append?: boolean; quiet?: boolean; forceOptions?: boolean } = {}): Promise<boolean> {
    const systemAccountId = options.isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined
    const requestId = accountListRequestId + 1
    accountListRequestId = requestId
    if (!loadOptions.quiet) {
      loading.value = true
      loadingRequestId = requestId
    }
    void loadAccountOptions(systemAccountId, loadOptions.forceOptions === true).catch((error) => {
      console.error(error)
      message.error('加载账户辅助信息失败')
    })
    try {
      const initialAccountList = await fetchAccountList(systemAccountId)
      if (requestId !== accountListRequestId) return false
      const accountList = await resolvedAccountListPage(initialAccountList, systemAccountId, loadOptions, () => requestId === accountListRequestId)
      if (requestId !== accountListRequestId) return false
      applyAccountList(accountList, loadOptions)
      return true
    } catch (error) {
      if (requestId !== accountListRequestId) return false
      console.error(error)
      message.error('加载账户失败')
      return false
    } finally {
      if (!loadOptions.quiet && loadingRequestId === requestId) {
        loading.value = false
      }
    }
  }

  function fetchAccountList(systemAccountId?: string) {
    return options.isManagementView.value ? api.accounts.list(accountListParams(systemAccountId)) : api.myAccounts.list(accountListParams())
  }

  async function resolvedAccountListPage(
    accountList: Awaited<ReturnType<typeof fetchAccountList>>,
    systemAccountId: string | undefined,
    loadOptions: { append?: boolean },
    isCurrentRequest: () => boolean
  ): Promise<Awaited<ReturnType<typeof fetchAccountList>>> {
    if (loadOptions.append || accountList.page <= 1 || accountList.items.length > 0 || accountList.hasMore !== false) {
      return accountList
    }
    if (!isCurrentRequest()) {
      return accountList
    }
    accountPagination.current = 1
    return fetchAccountList(systemAccountId)
  }

  function applyAccountList(accountList: Awaited<ReturnType<typeof fetchAccountList>>, loadOptions: { append?: boolean }) {
    accountPagination.current = accountList.page
    accountPagination.pageSize = accountList.pageSize
    accountPagination.total = accountList.total
    accounts.value = loadOptions.append ? [...accounts.value, ...accountList.items] : accountList.items
    const selectableAccountIds = new Set(accounts.value.filter(canBatchManageAccount).map((account) => account.id))
    options.onLoaded?.(selectableAccountIds)
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

  async function handleAccountTableChangeAndLoad(paginationInfo: unknown): Promise<void> {
    handleAccountTableChange(paginationInfo)
    await loadData()
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
    accountPagination.current = defaults.pagination.current
    accountPagination.pageSize = defaults.pagination.pageSize
    resetSystemAccountOptionsSearch()
    resetAccountListPagination()
    pageStateCache.clear()
    void loadData()
  }

  function resetAccountPagination() {
    accountPagination.current = 1
    resetAccountListPagination()
  }

  function snapshotPageState(): AccountsPageState {
    return {
      filters: { ...filters, status: [...filters.status] },
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
        options.isManagementView.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
        api.proxies.options(),
        loadSystemAccountOptions()
      ])
      if (currentScopeKey() !== scopeKey || accountOptionsInFlight.get(scopeKey) !== requestRef.current) {
        return
      }
      providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
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

  function accountListParams(systemAccountId?: string): AccountListParams {
    return {
      systemAccountId,
      sorts: accountSorts.value,
      page: accountPagination.current,
      pageSize: accountPagination.pageSize,
      keyword: filters.keyword.trim() || undefined,
      groupId: options.isManagementView.value && !systemAccountId ? undefined : filters.groupId || undefined,
      type: filters.type,
      status: filters.status
    }
  }

  watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

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
    refreshData,
    applyFilters,
    handleAccountTableChangeAndLoad,
    handleAccountSortChange,
    handleSystemAccountFilterChange,
    resetAccountPagination,
    resetFilters
  }
}
