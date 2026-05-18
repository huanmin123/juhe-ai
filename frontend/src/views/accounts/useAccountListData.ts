import { message } from '@/lib/antd'
import { computed, reactive, ref, watch, type ComputedRef } from 'vue'

import { api, type AccountListParams, type AccountListSortParam } from '@/api/client'
import type { ResponsiveDataListSort } from '@/components/responsiveDataListSorting'
import { usePageStateCache } from '@/composables/usePageStateCache'
import type { AccountSummary, GroupSummary, ProviderDefinition, ProxyProfileOptionSummary, SystemAccountSummary } from '@/types/domain'
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
  filters: { keyword: '', type: 'all', status: 'all', schedulable: 'all', systemAccountId: allSystemAccountsValue },
  pagination: { current: 1, pageSize: ACCOUNT_PAGE_SIZE },
  sorts: [{ field: 'priority', order: 'asc' }]
})

export function useAccountListData(options: UseAccountListDataOptions) {
  const pageStateCache = usePageStateCache<AccountsPageState>(undefined, defaultAccountsPageState)
  const initialPageState = pageStateCache.read()
  const loading = ref(false)
  const accounts = ref<AccountSummary[]>([])
  const providers = ref<ProviderDefinition[]>([])
  const proxies = ref<ProxyProfileOptionSummary[]>([])
  const groups = ref<GroupSummary[]>([])
  const systemAccounts = ref<SystemAccountSummary[]>([])
  const accountOptionsLoaded = ref(false)
  const accountOptionsScopeKey = ref('')
  const accountSorts = ref<AccountListSortParam[]>(initialPageState.sorts)
  const filters = reactive<AccountFilters>({ ...initialPageState.filters })
  const accountPagination = reactive({
    current: initialPageState.pagination.current,
    pageSize: initialPageState.pagination.pageSize,
    total: 0
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

  async function loadData(loadOptions: { append?: boolean; quiet?: boolean; forceOptions?: boolean } = {}): Promise<void> {
    if (!loadOptions.quiet) {
      loading.value = true
    }
    try {
      const systemAccountId = options.isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined
      const [initialAccountList] = await Promise.all([
        fetchAccountList(systemAccountId),
        loadAccountOptions(systemAccountId, loadOptions.forceOptions === true)
      ])
      const accountList = await resolvedAccountListPage(initialAccountList, systemAccountId, loadOptions)
      applyAccountList(accountList, loadOptions)
    } catch (error) {
      console.error(error)
      message.error('加载账户失败')
    } finally {
      if (!loadOptions.quiet) {
        loading.value = false
      }
    }
  }

  function fetchAccountList(systemAccountId?: string) {
    return options.isManagementView.value ? api.accounts.list(accountListParams(systemAccountId)) : api.myAccounts.list(accountListParams())
  }

  async function resolvedAccountListPage(accountList: Awaited<ReturnType<typeof fetchAccountList>>, systemAccountId: string | undefined, loadOptions: { append?: boolean }): Promise<Awaited<ReturnType<typeof fetchAccountList>>> {
    if (loadOptions.append || accountList.page <= 1 || accountList.items.length > 0 || accountList.total === 0) {
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
    resetAccountPagination()
    void loadData({ forceOptions: true })
  }

  function resetFilters() {
    const defaults = defaultAccountsPageState()
    Object.assign(filters, defaults.filters)
    accountSorts.value = defaults.sorts
    accountPagination.current = defaults.pagination.current
    accountPagination.pageSize = defaults.pagination.pageSize
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
      filters: { ...filters },
      pagination: { current: accountPagination.current, pageSize: accountPagination.pageSize },
      sorts: accountSorts.value
    }
  }

  async function loadAccountOptions(systemAccountId: string | undefined, force = false): Promise<void> {
    const scopeKey = options.isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self'
    if (!force && accountOptionsLoaded.value && accountOptionsScopeKey.value === scopeKey) {
      return
    }

    const [providerList, proxyList, groupList, systemAccountList] = await Promise.all([
      options.isManagementView.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
      api.proxies.options(),
      options.isManagementView.value ? api.groups.list({ systemAccountId }) : api.myGroups.list(),
      options.isManagementView.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
    ])
    providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
    proxies.value = proxyList
    groups.value = groupList
    systemAccounts.value = systemAccountList
    accountOptionsLoaded.value = true
    accountOptionsScopeKey.value = scopeKey
  }

  function accountListParams(systemAccountId?: string): AccountListParams {
    return {
      systemAccountId,
      sorts: accountSorts.value,
      page: accountPagination.current,
      pageSize: accountPagination.pageSize,
      keyword: filters.keyword.trim() || undefined,
      type: filters.type,
      status: filters.status,
      schedulable: filters.schedulable
    }
  }

  watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

  return {
    loading,
    accounts,
    providers,
    proxies,
    groups,
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
