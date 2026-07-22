import { computed, ref, watch, type Ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import {
  accountSelectionForId,
  accountSelectOptionLabel,
  rememberAccountSelection,
  rememberAccountSelections,
  type AccountSelection
} from '@/shared/accountLabelCache'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountStatus, AiPerformanceAccountOption, AiPerformanceOverview } from '@/types/domain'
import { chartColors, orderedAiPerformanceSeries } from './aiPerformanceChartOptions'

type AiPerformanceAccountRow = AiPerformanceOverview['accounts'][number]
type AiPerformanceSeriesRow = AiPerformanceOverview['hourlySeries'][number]

interface UseAiPerformanceAccountSelectionOptions {
  isManagementView: Ref<boolean>
  isPageActive: () => boolean
  overview: Ref<AiPerformanceOverview | undefined>
  reloadPerformance: () => void
  requestRender: () => void
  selectedSystemAccountId: () => string | undefined
}

export function useAiPerformanceAccountSelection(options: UseAiPerformanceAccountSelectionOptions) {
  const addedAccountIds = ref<string[]>([])
  const addedAccountSelections = ref<AccountSelection[]>([])
  const activeAccountIds = ref<string[]>([])
  const accounts = ref<AiPerformanceAccountOption[]>([])
  const accountsLoading = ref(false)
  const accountSearchKeyword = ref('')
  let accountSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let accountSearchSeq = 0
  let loadingAccountOptionsKey: string | undefined
  let loadingAccountOptionsPromise: Promise<void> | undefined

  const responseAccounts = computed(() => options.overview.value?.accounts ?? [])
  const responseAccountById = computed(() => new Map(responseAccounts.value.map((account) => [account.id, account])))
  const accountOptionById = computed(() => new Map(accounts.value.map((account) => [account.id, account])))
  const addedAccountSelectionById = computed(() => new Map(addedAccountSelections.value.map((account) => [account.id, account])))
  const defaultAccountIdSet = computed(() => new Set([
    ...(options.overview.value?.defaultAccounts ?? []).map((account) => account.id),
    ...responseAccounts.value.filter((account) => account.defaultVisible).map((account) => account.id)
  ]))
  const overviewAccounts = computed(() => dedupePerformanceAccounts([
    ...responseAccounts.value,
    ...addedAccountIds.value
      .map((id) => placeholderPerformanceAccount(id))
      .filter((account): account is AiPerformanceAccountRow => Boolean(account))
  ]))
  const overviewHourlySeries = computed(() => {
    const responseSeries = options.overview.value?.hourlySeries ?? []
    const responseSeriesIds = new Set(responseSeries.map((series) => series.accountId))
    const placeholderSeries = addedAccountIds.value
      .filter((id) => !responseSeriesIds.has(id))
      .map((id) => placeholderPerformanceSeries(id))
      .filter((series): series is AiPerformanceSeriesRow => Boolean(series))
    return [...responseSeries, ...placeholderSeries]
  })
  const displayOverview = computed<AiPerformanceOverview | undefined>(() => {
    const currentOverview = options.overview.value
    if (!currentOverview) return undefined
    const accountIds = new Set(overviewAccounts.value.map((account) => account.id))
    return {
      ...currentOverview,
      accounts: overviewAccounts.value,
      defaultAccounts: currentOverview.defaultAccounts.filter((account) => accountIds.has(account.id)),
      selectedAccounts: overviewAccounts.value.filter((account) => account.selected && accountIds.has(account.id)),
      hourlySeries: overviewHourlySeries.value
    }
  })
  const activeAccountIdSet = computed(() => new Set(activeAccountIds.value))
  const hasActiveAccountFilter = computed(() => activeAccountIds.value.length > 0)
  const visibleAccounts = computed(() => {
    if (!hasActiveAccountFilter.value) return overviewAccounts.value
    return overviewAccounts.value.filter((account) => activeAccountIdSet.value.has(account.id))
  })
  const visibleAccountIdSet = computed(() => new Set(visibleAccounts.value.map((account) => account.id)))
  const visibleHourlySeries = computed(() => overviewHourlySeries.value.filter((series) => visibleAccountIdSet.value.has(series.accountId)))
  const visibleOverview = computed<AiPerformanceOverview | undefined>(() => {
    const currentOverview = displayOverview.value
    if (!currentOverview) return undefined
    const visibleIds = visibleAccountIdSet.value
    return {
      ...currentOverview,
      accounts: visibleAccounts.value,
      defaultAccounts: currentOverview.defaultAccounts.filter((account) => visibleIds.has(account.id)),
      selectedAccounts: currentOverview.selectedAccounts.filter((account) => visibleIds.has(account.id)),
      hourlySeries: visibleHourlySeries.value
    }
  })
  const accountPickerHiddenValues = computed(() => [
    ...overviewAccounts.value.map((account) => account.id),
    ...addedAccountIds.value
  ])
  const addedAccountIdSet = computed(() => new Set(addedAccountIds.value))
  const accountFilterItems = computed(() => {
    const currentOverview = displayOverview.value
    if (!currentOverview) return []
    const nameCounts = currentOverview.accounts.reduce((counts, account) => {
      counts.set(account.name, (counts.get(account.name) ?? 0) + 1)
      return counts
    }, new Map<string, number>())
    const accountById = new Map(currentOverview.accounts.map((account) => [account.id, account]))
    const activeIds = activeAccountIdSet.value
    return orderedAiPerformanceSeries(currentOverview).map((series, index) => {
      const account = accountById.get(series.accountId)
      const label = performanceAccountLabel(account, series, nameCounts)
      return {
        account: account ?? {
          id: series.accountId,
          name: series.accountName,
          status: 'active' as AccountStatus,
          providerCode: series.providerCode,
          systemAccountId: series.systemAccountId,
          requestCountLast7d: 0,
          selected: false,
          defaultVisible: false
        },
        label,
        color: chartColors[index % chartColors.length],
        selected: activeIds.has(series.accountId),
        removable: addedAccountIdSet.value.has(series.accountId) && !defaultAccountIdSet.value.has(series.accountId)
      }
    })
  })
  const seriesColorByAccountId = computed(() => new Map(accountFilterItems.value.map((item) => [item.account.id, item.color])))

  async function loadAccounts() {
    const request = currentAccountOptionsRequest()
    if (loadingAccountOptionsKey === request.key && loadingAccountOptionsPromise) {
      return loadingAccountOptionsPromise
    }
    const requestSeq = ++accountSearchSeq
    accountsLoading.value = true
    loadingAccountOptionsKey = request.key
    const loadingPromise = (async () => {
      try {
        const accountParams = {
          systemAccountId: request.systemAccountId,
          keyword: request.keyword,
          accountIds: request.accountIds,
          limit: 50
        }
        const result = options.isManagementView.value
          ? await api.stats.aiPerformanceAccounts(accountParams)
          : await api.myStats.aiPerformanceAccounts(accountParams)
        applyAccountOptions(result, requestSeq)
      } catch (error) {
        console.error(error)
        message.error(extractApiErrorMessage(error, 'AI 账户列表加载失败'))
      } finally {
        if (loadingAccountOptionsKey === request.key) {
          loadingAccountOptionsPromise = undefined
          loadingAccountOptionsKey = undefined
        }
        if (requestSeq === accountSearchSeq) {
          accountsLoading.value = false
        }
      }
    })()
    loadingAccountOptionsPromise = loadingPromise
    return loadingPromise
  }

  function applyAccountOptions(nextAccounts: AiPerformanceAccountOption[], requestSeq: number): void {
    if (requestSeq !== accountSearchSeq) return
    accounts.value = nextAccounts
  }

  function handleAddedAccountsChange(value: string[], previousValue: string[]) {
    accountSearchKeyword.value = ''
    const previousIds = new Set(previousValue)
    const acceptedIds = value.filter((id) => !defaultAccountIdSet.value.has(id))
    for (const id of acceptedIds) {
      rememberAddedAccountSelection(id)
    }
    addedAccountIds.value = acceptedIds
    syncAddedAccountSelections()
    const newlyAddedIds = acceptedIds.filter((id) => !previousIds.has(id))
    const visibleIds = new Set([...overviewAccounts.value.map((account) => account.id), ...acceptedIds])
    const nextActiveIds = activeAccountIds.value.filter((id) => visibleIds.has(id))
    activeAccountIds.value = hasActiveAccountFilter.value
      ? [...new Set([...nextActiveIds, ...newlyAddedIds])]
      : nextActiveIds
    void loadAccounts()
    if (newlyAddedIds.length || acceptedIds.length !== previousValue.length) {
      options.reloadPerformance()
    } else {
      options.requestRender()
    }
  }

  function removeAddedAccount(id: string) {
    if (!addedAccountIdSet.value.has(id)) return
    addedAccountIds.value = addedAccountIds.value.filter((accountId) => accountId !== id)
    addedAccountSelections.value = addedAccountSelections.value.filter((selection) => selection.id !== id)
    activeAccountIds.value = activeAccountIds.value.filter((accountId) => accountId !== id)
    void loadAccounts()
    options.reloadPerformance()
    options.requestRender()
  }

  function handleAccountSearch(value: string) {
    accountSearchKeyword.value = value
    clearAccountSearchTimer()
    accountSearchTimer = window.setTimeout(() => {
      accountSearchTimer = undefined
      if (!options.isPageActive()) return
      void loadAccounts()
    }, 250)
  }

  function handleAccountDropdownVisibleChange(open: boolean) {
    if (open) {
      void loadAccounts()
    }
  }

  function clearAccountState() {
    clearAccountSearchTimer()
    addedAccountIds.value = []
    addedAccountSelections.value = []
    activeAccountIds.value = []
    accountSearchKeyword.value = ''
  }

  function toggleAccountFilter(id: string) {
    if (!overviewAccounts.value.some((account) => account.id === id)) return
    activeAccountIds.value = activeAccountIds.value.includes(id)
      ? activeAccountIds.value.filter((accountId) => accountId !== id)
      : [...activeAccountIds.value, id]
    options.requestRender()
  }

  function clearAccountSearchTimer() {
    if (accountSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(accountSearchTimer)
      accountSearchTimer = undefined
    }
  }

  function pruneAccountState() {
    const currentOverview = options.overview.value
    if (!currentOverview) return
    syncAddedAccountSelections()
    const visibleIds = new Set([...overviewAccounts.value.map((account) => account.id), ...addedAccountIds.value])
    activeAccountIds.value = activeAccountIds.value.filter((id) => visibleIds.has(id))
  }

  function currentAccountOptionsRequest(): { key: string; systemAccountId?: string; keyword: string; accountIds: string[] } {
    const keyword = accountSearchKeyword.value.trim()
    const systemAccountId = options.selectedSystemAccountId()
    const accountIds = [...addedAccountIds.value]
    return {
      key: JSON.stringify([options.isManagementView.value ? 'management' : 'self', systemAccountId ?? '', keyword, accountIds]),
      systemAccountId,
      keyword,
      accountIds
    }
  }

  function dedupePerformanceAccounts(items: AiPerformanceAccountRow[]): AiPerformanceAccountRow[] {
    const seen = new Set<string>()
    const result: AiPerformanceAccountRow[] = []
    for (const item of items) {
      if (seen.has(item.id)) continue
      seen.add(item.id)
      result.push(item)
    }
    return result
  }

  function placeholderPerformanceAccount(id: string): AiPerformanceAccountRow | undefined {
    const responseAccount = responseAccountById.value.get(id)
    if (responseAccount) return responseAccount
    const option = accountOptionById.value.get(id)
    const selection = addedAccountSelectionById.value.get(id)
    const name = option?.name?.trim() || selection?.name?.trim()
    if (!name || !option?.providerCode) return undefined
    return {
      id,
      name,
      status: option?.status ?? 'active',
      providerCode: option.providerCode,
      systemAccountId: option?.systemAccountId ?? options.selectedSystemAccountId() ?? '',
      systemAccountName: option?.systemAccountName,
      ownerSystemAccountId: option?.ownerSystemAccountId,
      ownerSystemAccountName: option?.ownerSystemAccountName ?? selection?.ownerSystemAccountName,
      accessType: option?.accessType ?? selection?.accessType,
      requestCountLast7d: option?.requestCountLast7d ?? 0,
      selected: true,
      defaultVisible: false
    }
  }

  function performanceAccountLabel(
    account: AiPerformanceAccountRow | undefined,
    series: AiPerformanceSeriesRow,
    nameCounts: Map<string, number>
  ): string {
    const accountName = account?.name ?? series.accountName
    if (account?.accessType === 'authorized') {
      return accountSelectOptionLabel(account)
    }
    if (options.isManagementView.value && account?.systemAccountName) {
      return `${accountName}（${account.systemAccountName}）`
    }
    if ((nameCounts.get(accountName) ?? 0) > 1 && account?.providerCode) {
      return `${accountName}（${providerDisplayName(account.providerCode)}）`
    }
    return accountName
  }

  function placeholderPerformanceSeries(id: string): AiPerformanceSeriesRow | undefined {
    const account = responseAccountById.value.get(id) ?? placeholderPerformanceAccount(id)
    if (!account) return undefined
    return {
      accountId: id,
      accountName: account.name,
      providerCode: account.providerCode,
      systemAccountId: account.systemAccountId,
      points: []
    }
  }

  function rememberAddedAccountSelection(id: string) {
    const selection = accountSelectionForId(id, [...accounts.value, ...overviewAccounts.value])
    rememberAccountSelection(selection)
    if (!selection || addedAccountSelections.value.some((item) => item.id === selection.id)) return
    addedAccountSelections.value = [...addedAccountSelections.value, selection]
  }

  function syncAddedAccountSelections() {
    const existing = new Map(addedAccountSelections.value.map((selection) => [selection.id, selection]))
    addedAccountSelections.value = addedAccountIds.value
      .map((id) => accountSelectionForId(id, [...accounts.value, ...overviewAccounts.value]) ?? existing.get(id))
      .filter((selection): selection is AccountSelection => Boolean(selection))
    rememberAccountSelections(addedAccountSelections.value)
  }

  watch(addedAccountSelections, (selections) => rememberAccountSelections(selections), { deep: true, immediate: true })

  return {
    accounts,
    accountsLoading,
    accountFilterItems,
    accountPickerHiddenValues,
    activeAccountIds,
    addedAccountIds,
    addedAccountSelections,
    clearAccountSearchTimer,
    clearAccountState,
    handleAccountDropdownVisibleChange,
    handleAccountSearch,
    handleAddedAccountsChange,
    hasActiveAccountFilter,
    loadAccounts,
    pruneAccountState,
    removeAddedAccount,
    seriesColorByAccountId,
    toggleAccountFilter,
    visibleAccounts,
    visibleHourlySeries,
    visibleOverview
  }
}
