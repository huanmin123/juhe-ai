import { message } from '@/lib/antd'
import { computed, reactive, ref, watch, type ComputedRef } from 'vue'

import { api, type AccountListParams, type AccountListSortParam } from '@/api/client'
import type { ResponsiveDataListSort } from '@/components/responsiveDataListSorting'
import { authState } from '@/composables/useAuth'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { formatNumber } from '@/shared/formatters'
import { rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import type { AccountBalanceSnapshot, AccountListItem, AccountListResult, AccountMutationResult, AccountSummary, ProviderDefinition } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { AccountFilters } from './accountFormTypes'
import { ACCOUNT_PAGE_SIZE, FALLBACK_PROVIDERS } from './accountOptions'
import { countActiveAccountFilters } from './accountListFilters'
import { normalizeAccountTableSorts } from './accountTableColumns'
import { canSelectAccountForBatch } from './accountRules'
import {
  accountListHasAccumulatedPageWindow,
  accountListPageWindowChanged,
  mergeAccountListPageWithRevisionOverlays,
  replaceAccountBalanceSnapshot,
  replaceAccountListRow,
  type AccountListRevisionOverlay
} from './accountListMutations'

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

interface AccountListLoadOptions extends Record<string, unknown> {
  forceData?: boolean
  requestIdentity?: number
}

interface ProviderDefinitionRequest {
  requestId: number
  promise: Promise<ProviderDefinition | undefined>
}

interface AccountOptionsRequest {
  requestId: number
  promise: Promise<void>
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
  const providerDefinitions = ref<ProviderDefinition[]>([])
  const providerDefinitionsLoading = ref(false)
  const providerDefinitionsLoaded = ref(false)
  const providerDefinitionsScopeKey = ref('')
  const providerDefinitionsInFlight = new Map<string, ProviderDefinitionRequest>()
  let providerDefinitionsRequestId = 0
  const accountOptionsLoaded = ref(false)
  const accountOptionsScopeKey = ref('')
  const accountOptionsInFlight = new Map<string, AccountOptionsRequest>()
  let accountOptionsRequestId = 0
  const accountSorts = ref<AccountListSortParam[]>(initialPageState.sorts)
  const filters = reactive<AccountFilters>({ ...initialPageState.filters })
  const {
    handleDropdown: handleSystemAccountOptionsDropdown,
    handleSearch: handleSystemAccountOptionsSearch,
    invalidate: invalidateSystemAccountOptions,
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
  let listMutationRevision = 0
  const listRevisionOverlays = new Map<string, AccountListRevisionOverlay>()
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
    invalidatePendingLoads,
    removeItems: removeAccountItems,
    refreshMobile: refreshMobileAccountsCached,
    resetPagination: resetAccountListPagination,
  } = useResponsivePagedList<AccountListItem, AccountListLoadOptions>({
    pageSize: ACCOUNT_PAGE_SIZE,
    initialPagination: initialPageState.pagination,
    showTotal: (total, range, context) => context?.hasMore
      ? `已加载到第 ${formatNumber(range?.[1] ?? Math.max(0, total - 1))} 个账户，还有更多`
      : `共 ${formatNumber(total)} 个账户`,
    fetchPage: async (_loadOptions, pageState) => {
      const requestAuthRevision = authState.revision.value
      const requestMutationRevision = listMutationRevision
      const systemAccountId = options.isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined
      const accountList = await fetchAccountList(systemAccountId, pageState)
      return {
        get superseded() {
          return requestAuthRevision !== authState.revision.value
            || requestMutationRevision !== listMutationRevision
        },
        items: accountList.items,
        page: accountList.page,
        pageSize: accountList.pageSize,
        total: accountList.total,
        hasMore: accountList.hasMore
      }
    },
    requestSignature: (_loadOptions, pageState) => {
      const systemAccountId = options.isManagementView.value ? accountScopeParams.value?.systemAccountId : undefined
      return [
        authState.revision.value,
        options.isManagementView.value ? 'management' : 'self',
        _loadOptions.requestIdentity,
        accountListParams(systemAccountId, pageState)
      ]
    },
    transformItems: (nextItems, _loadOptions, _result, currentItems) => (
      mergeAccountListPageWithRevisionOverlays(nextItems, currentItems, listRevisionOverlays)
    ),
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
  function fetchAccountList(
    systemAccountId: string | undefined,
    pageState: { current: number; pageSize: number },
  ): Promise<AccountListResult> {
    const params = accountListParams(systemAccountId, pageState)
    const loadNetwork = () => options.isManagementView.value
      ? api.accounts.list(params)
      : api.myAccounts.list(accountListParams(undefined, pageState))
    return loadNetwork()
  }

  function refreshData() {
    resetSystemAccountOptionsSearch()
    void loadData({ forceData: true })
  }

  async function refreshMobileAccounts(): Promise<void> {
    await refreshMobileAccountsCached({ forceData: true })
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
    if (!accounts.value.some((account) => account.id === accountId)) return false
    listMutationRevision += 1
    return removeAccountItems((account) => account.id === accountId) > 0
  }

  function updateLoadedAccountBalance(accountId: string, snapshot: AccountBalanceSnapshot | undefined): boolean {
    const nextAccounts = replaceAccountBalanceSnapshot(accounts.value, accountId, snapshot)
    if (nextAccounts === accounts.value) return false
    listMutationRevision += 1
    accounts.value = nextAccounts
    return true
  }

  function updateLoadedAccountRevision(accountId: string, configRevision: number): boolean {
    const accountIndex = accounts.value.findIndex((account) => account.id === accountId)
    if (accountIndex < 0) return false
    const current = accounts.value[accountIndex]
    if (Number.isInteger(current.configRevision) && Number(current.configRevision) >= configRevision) return false
    listMutationRevision += 1
    const nextAccounts = [...accounts.value]
    const updated = { ...current, configRevision }
    nextAccounts[accountIndex] = updated
    accounts.value = nextAccounts
    listRevisionOverlays.set(accountId, { configRevision, row: updated })
    return true
  }

  function markAccountMutation(mutation: AccountMutationResult): boolean {
    const current = accounts.value.find((account) => account.id === mutation.id)
    const revision = Number(mutation.configRevision)
    const currentRevision = Number(current?.configRevision)
    const advancesRevision = Number.isInteger(revision)
      && revision >= 1
      && (!Number.isInteger(currentRevision) || revision > currentRevision)
    if (!mutation.authorizationInstancesAffected && mutation.changedFields.length === 0 && !advancesRevision) return false
    listMutationRevision += 1
    if (!current || !Number.isInteger(revision) || revision < 1) return true
    const overlayRow = advancesRevision ? { ...current, configRevision: revision } : current
    if (advancesRevision) {
      const nextAccounts = [...accounts.value]
      nextAccounts[nextAccounts.findIndex((account) => account.id === mutation.id)] = overlayRow
      accounts.value = nextAccounts
    }
    listRevisionOverlays.set(mutation.id, { configRevision: revision, row: overlayRow })
    return true
  }

  function accountUpdateAffectsPageWindow(account: AccountListItem): boolean {
    const current = accounts.value.find((item) => item.id === account.id)
    return Boolean(current && accountListPageWindowChanged(current, account, {
      filters,
      isManagementView: options.isManagementView.value,
      sorts: accountSorts.value
    }))
  }

  async function reloadAccountPageAfterMutation(): Promise<boolean> {
    if (accountListHasAccumulatedPageWindow(
      accounts.value.length,
      accountPagination.current,
      accountPagination.pageSize
    )) {
      resetAccountPagination()
    }
    return loadData({ forceData: true, quiet: true, requestIdentity: listMutationRevision })
  }

  function updateLoadedAccount(account: AccountListItem): boolean {
    const current = accounts.value.find((item) => item.id === account.id)
    if (!current) return false
    const nextAccounts = replaceAccountListRow(accounts.value, account)
    if (nextAccounts === accounts.value) return false
    const updated = nextAccounts.find((item) => item.id === account.id)
    if (!updated) return false
    const pageWindowChanged = accountListPageWindowChanged(current, updated, {
      filters,
      isManagementView: options.isManagementView.value,
      sorts: accountSorts.value
    })
    listMutationRevision += 1
    accounts.value = nextAccounts
    const updatedRevision = Number(updated.configRevision)
    if (Number.isInteger(updatedRevision) && updatedRevision >= 1) {
      listRevisionOverlays.set(account.id, { configRevision: updatedRevision, row: updated })
    }
    if (pageWindowChanged) void reloadAccountPageAfterMutation()
    return true
  }

  function snapshotPageState(): AccountsPageState {
    return {
      filters: { ...filters, tagIds: [...filters.tagIds], status: [...filters.status] },
      pagination: { current: accountPagination.current, pageSize: accountPagination.pageSize },
      sorts: accountSorts.value
    }
  }

  async function loadAccountOptions(systemAccountId: string | undefined, force = false): Promise<void> {
    const scopeKey = accountProviderScopeKey(systemAccountId)
    if (!force && accountOptionsLoaded.value && accountOptionsScopeKey.value === scopeKey) {
      return
    }
    const existingRequest = !force ? accountOptionsInFlight.get(scopeKey) : undefined
    if (existingRequest?.requestId === accountOptionsRequestId) return existingRequest.promise

    if (accountOptionsScopeKey.value !== scopeKey) {
      providers.value = []
      accountOptionsLoaded.value = false
      accountOptionsScopeKey.value = ''
    }

    const requestId = ++accountOptionsRequestId
    const request = (async () => {
      let providerApplied = false
      try {
        await loadProviderOptionsResource({
          apply: (nextProviders) => {
            if (!isCurrentAccountOptionsRequest(requestId, scopeKey)) return
            providers.value = nextProviders.length ? nextProviders : FALLBACK_PROVIDERS
            providerApplied = true
          },
          force,
          includeDefinitions: false,
          isCurrent: () => isCurrentAccountOptionsRequest(requestId, scopeKey),
          isManagementView: options.isManagementView.value,
          systemAccountId
        })
        if (!isCurrentAccountOptionsRequest(requestId, scopeKey)) return
        accountOptionsLoaded.value = providerApplied
        accountOptionsScopeKey.value = providerApplied ? scopeKey : ''
      } catch (error) {
        if (!isCurrentAccountOptionsRequest(requestId, scopeKey)) return
        throw error
      }
    })().finally(() => {
      const activeRequest = accountOptionsInFlight.get(scopeKey)
      if (activeRequest?.requestId === requestId) accountOptionsInFlight.delete(scopeKey)
    })
    accountOptionsInFlight.set(scopeKey, { requestId, promise: request })
    return request
  }

  async function ensureProviderDefinition(
    providerCode: string,
    systemAccountId?: string,
    force = false
  ): Promise<ProviderDefinition | undefined> {
    const code = providerCode.trim()
    if (!code) return undefined
    const usesCurrentListScope = options.isManagementView.value && systemAccountId === undefined
    const targetSystemAccountId = options.isManagementView.value
      ? systemAccountId ?? accountScopeParams.value?.systemAccountId
      : undefined
    const scopeKey = accountProviderScopeKey(targetSystemAccountId)
    const requestKey = JSON.stringify([scopeKey, code])
    const listScopeAnchorKey = usesCurrentListScope ? currentAccountProviderScopeKey() : undefined
    if (!force && providerDefinitionsScopeKey.value === scopeKey) {
      const cached = providerDefinitions.value.find((provider) => provider.code === code)
      if (cached) return cached
    }
    const existingRequest = !force ? providerDefinitionsInFlight.get(requestKey) : undefined
    if (existingRequest?.requestId === providerDefinitionsRequestId) return existingRequest.promise

    if (providerDefinitionsScopeKey.value !== scopeKey) {
      providerDefinitions.value = []
      providerDefinitionsLoaded.value = false
      providerDefinitionsScopeKey.value = ''
    }
    const requestId = ++providerDefinitionsRequestId
    providerDefinitionsLoading.value = true
    const request = (async () => {
      try {
        const definition = await api.providers.detail(
          code,
          options.isManagementView.value && targetSystemAccountId
            ? { systemAccountId: targetSystemAccountId }
            : undefined
        )
        if (!isCurrentProviderDefinitionsRequest(requestId, scopeKey, targetSystemAccountId, listScopeAnchorKey)) return undefined
        providerDefinitions.value = [
          ...providerDefinitions.value.filter((provider) => provider.code !== definition.code),
          definition
        ]
        providerDefinitionsLoaded.value = true
        providerDefinitionsScopeKey.value = scopeKey
        return definition
      } catch (error) {
        if (!isCurrentProviderDefinitionsRequest(requestId, scopeKey, targetSystemAccountId, listScopeAnchorKey)) return undefined
        throw error
      } finally {
        const activeRequest = providerDefinitionsInFlight.get(requestKey)
        if (activeRequest?.requestId === requestId) providerDefinitionsInFlight.delete(requestKey)
        if (requestId === providerDefinitionsRequestId) providerDefinitionsLoading.value = false
      }
    })()
    providerDefinitionsInFlight.set(requestKey, { requestId, promise: request })
    return request
  }

  function isCurrentAccountOptionsRequest(requestId: number, scopeKey: string): boolean {
    return requestId === accountOptionsRequestId && scopeKey === currentAccountProviderScopeKey()
  }

  function isCurrentProviderDefinitionsRequest(
    requestId: number,
    scopeKey: string,
    systemAccountId: string | undefined,
    listScopeAnchorKey?: string
  ): boolean {
    return requestId === providerDefinitionsRequestId
      && scopeKey === accountProviderScopeKey(systemAccountId)
      && (!listScopeAnchorKey || listScopeAnchorKey === currentAccountProviderScopeKey())
  }

  function currentAccountProviderScopeKey(): string {
    const systemAccountId = options.isManagementView.value
      ? accountScopeParams.value?.systemAccountId
      : undefined
    return accountProviderScopeKey(systemAccountId)
  }

  function accountProviderScopeKey(systemAccountId: string | undefined): string {
    const viewer = authState.currentUser.value
    return JSON.stringify([
      authState.revision.value,
      viewer?.id ?? 'anonymous',
      viewer?.role ?? 'anonymous',
      options.isManagementView.value ? 'management' : 'self',
      options.isManagementView.value ? systemAccountId ?? 'all' : 'self'
    ])
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
  watch(() => authState.revision.value, () => {
    invalidatePendingLoads()
    invalidateSystemAccountOptions()
    listMutationRevision += 1
    listRevisionOverlays.clear()
    accounts.value = []
    resetAccountListPagination()
    accountPagination.total = 0
    const defaults = defaultAccountsPageState()
    Object.assign(filters, defaults.filters)
    accountSorts.value = defaults.sorts
    pageStateCache.clear()
    systemAccounts.value = []
    providers.value = []
    accountOptionsRequestId += 1
    accountOptionsInFlight.clear()
    accountOptionsLoaded.value = false
    accountOptionsScopeKey.value = ''
    providerDefinitions.value = []
    providerDefinitionsRequestId += 1
    providerDefinitionsInFlight.clear()
    providerDefinitionsLoading.value = false
    providerDefinitionsLoaded.value = false
    providerDefinitionsScopeKey.value = ''
  })
  watch(() => filters.group, (group) => rememberGroupSelection(group), { deep: true, immediate: true })
  watch(() => filters.systemAccount, (account) => rememberPrincipalSelection(account), { deep: true, immediate: true })

  return {
    loading,
    accounts,
    providers,
    providerDefinitions,
    providerDefinitionsLoading,
    providerDefinitionsLoaded,
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
    ensureProviderDefinition,
    refreshData,
    applyFilters,
    handleAccountTableChangeAndLoad,
    handleAccountSortChange,
    handleSystemAccountFilterChange,
    focusCreatedAccount,
    removeLoadedAccount,
    accountUpdateAffectsPageWindow,
    markAccountMutation,
    reloadAccountPageAfterMutation,
    updateLoadedAccount,
    updateLoadedAccountBalance,
    updateLoadedAccountRevision,
    resetAccountPagination,
    resetFilters
  }
}
