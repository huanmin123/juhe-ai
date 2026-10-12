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
import type { AccountCapabilityDomain } from '@/views/accounts/accountCapabilityDomains'
import type { AiPerformanceAccountOption, AiPerformanceBaseResult, AiPerformanceOverview, AiPerformanceSeriesResult } from '@/types/domain'
import { chartColors, orderedAiPerformanceSeries } from './aiPerformanceChartOptions'

type AiPerformanceAccountRow = AiPerformanceOverview['accounts'][number]
type AiPerformanceSeriesRow = AiPerformanceOverview['hourlySeries'][number]

interface UseAiPerformanceAccountSelectionOptions {
  isManagementView: Ref<boolean>
  isPageActive: () => boolean
  base: Ref<AiPerformanceBaseResult | undefined>
  series: Ref<AiPerformanceSeriesResult | undefined>
  loadMissingSeries: (accountIds: string[]) => void
  clearSeries: () => void
  requestRender: () => void
  selectedSystemAccountId: () => string | undefined
  // 当前能力域筛选；undefined = 全部（不下发 domain）。进入账户候选请求 key，保证跨域响应互相失效。
  capabilityDomain: () => AccountCapabilityDomain | undefined
}

export function useAiPerformanceAccountSelection(options: UseAiPerformanceAccountSelectionOptions) {
  const addedAccountIds = ref<string[]>([])
  const addedAccountSelections = ref<AccountSelection[]>([])
  const activeAccountIds = ref<string[]>([])
  const accounts = ref<AiPerformanceAccountOption[]>([])
  const accountsLoading = ref(false)
  const accountSearchKeyword = ref('')
  let accountSearchTimer: number | undefined
  let accountSearchSeq = 0
  let loadingAccountOptionsKey: string | undefined
  let loadingAccountOptionsPromise: Promise<void> | undefined

  const responseAccounts = computed(() => dedupePerformanceAccounts([
    ...(options.base.value?.accounts ?? []),
    ...(options.series.value?.accounts ?? [])
      .filter((account) => addedAccountIds.value.includes(account.id))
  ]))
  const responseAccountById = computed(() => new Map(responseAccounts.value.map((account) => [account.id, account])))
  const accountOptionById = computed(() => new Map(accounts.value.map((account) => [account.id, account])))
  const addedAccountSelectionById = computed(() => new Map(addedAccountSelections.value.map((account) => [account.id, account])))
  const defaultAccountIdSet = computed(() => new Set((options.base.value?.accounts ?? []).map((account) => account.id)))
  const overviewAccounts = computed(() => dedupePerformanceAccounts([
    ...responseAccounts.value,
    ...addedAccountIds.value
      .map((id) => placeholderPerformanceAccount(id))
      .filter((account): account is AiPerformanceAccountRow => Boolean(account))
  ]))
  const overviewHourlySeries = computed(() => {
    const responseSeries = [
      ...(options.base.value?.hourlySeries ?? []),
      ...(options.series.value?.hourlySeries ?? []).filter((series) => addedAccountIds.value.includes(series.accountId))
    ]
    const responseSeriesIds = new Set(responseSeries.map((series) => series.accountId))
    const placeholderSeries = addedAccountIds.value
      .filter((id) => !responseSeriesIds.has(id))
      .map((id) => placeholderPerformanceSeries(id))
      .filter((series): series is AiPerformanceSeriesRow => Boolean(series))
    return [...responseSeries, ...placeholderSeries]
  })
  const displayOverview = computed<AiPerformanceOverview | undefined>(() => {
    const currentOverview = options.base.value
    if (!currentOverview) return undefined
    return {
      ...currentOverview,
      accounts: overviewAccounts.value,
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
    return {
      ...currentOverview,
      accounts: visibleAccounts.value,
      hourlySeries: visibleHourlySeries.value
    }
  })
  const addedAccountIdSet = computed(() => new Set(addedAccountIds.value))
  const accountFilterItems = computed(() => {
    const currentOverview = displayOverview.value
    if (!currentOverview) return []
    const nameCounts = currentOverview.accounts.reduce((counts, account) => {
      counts.set(account.name, (counts.get(account.name) ?? 0) + 1)
      return counts
    }, new Map<string, number>())
    const accountById = new Map(currentOverview.accounts.map((account) => [account.id, account]))
    return orderedAiPerformanceSeries(currentOverview).map((series, index) => {
      const account = accountById.get(series.accountId)
      const label = performanceAccountLabel(account, series, nameCounts)
      return {
        account: account ?? {
          id: series.accountId,
          name: series.accountName,
          providerCode: series.providerCode
        },
        label,
        color: chartColors[index % chartColors.length],
        // selected 字段语义是真实可见性：未隐藏任何账户（activeAccountIds 为空的规范形）时全部可见。
        selected: activeAccountIds.value.length ? activeAccountIdSet.value.has(series.accountId) : true,
        removable: addedAccountIdSet.value.has(series.accountId) && !defaultAccountIdSet.value.has(series.accountId),
        isDefault: defaultAccountIdSet.value.has(series.accountId)
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
          domain: request.domain,
          limit: 50
        }
        const result = options.isManagementView.value
          ? await api.stats.aiPerformanceAccounts(accountParams)
          : await api.myStats.aiPerformanceAccounts(accountParams)
        applyAccountOptions(result, requestSeq, request.key)
      } catch (error) {
        if (!isCurrentAccountOptionsRequest(requestSeq, request.key)) return
        console.error(error)
        message.error(extractApiErrorMessage(error, 'AI 账户列表加载失败'))
      } finally {
        if (requestSeq === accountSearchSeq && loadingAccountOptionsKey === request.key) {
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

  function applyAccountOptions(nextAccounts: AiPerformanceAccountOption[], requestSeq: number, requestKey: string): void {
    if (!isCurrentAccountOptionsRequest(requestSeq, requestKey)) return
    accounts.value = nextAccounts
  }

  function isCurrentAccountOptionsRequest(requestSeq: number, requestKey: string): boolean {
    return requestSeq === accountSearchSeq && requestKey === currentAccountOptionsRequest().key
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
    if (newlyAddedIds.length) options.loadMissingSeries(acceptedIds)
    options.requestRender()
  }

  function removeAddedAccount(id: string) {
    if (!addedAccountIdSet.value.has(id)) return
    addedAccountIds.value = addedAccountIds.value.filter((accountId) => accountId !== id)
    addedAccountSelections.value = addedAccountSelections.value.filter((selection) => selection.id !== id)
    activeAccountIds.value = activeAccountIds.value.filter((accountId) => accountId !== id)
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
    options.clearSeries()
  }

  function invalidateAccountOptions() {
    clearAccountSearchTimer()
    accountSearchSeq += 1
    accounts.value = []
    accountsLoading.value = false
    loadingAccountOptionsKey = undefined
    loadingAccountOptionsPromise = undefined
  }

  // 显隐语义：点击账户 = 隐藏可见项 / 显示隐藏项，与用量统计页趋势账户一致。
  // activeAccountIds 表示“可见子集”，空数组是“全部可见”的规范形；
  // 隐藏最后一条可见线时结果集合为空，按规范形等价于恢复全部显示。
  function toggleAccountFilter(id: string): boolean {
    if (!overviewAccounts.value.some((account) => account.id === id)) return false
    const allIds = overviewAccounts.value.map((account) => account.id)
    const visibleIdSet = activeAccountIds.value.length
      ? new Set(activeAccountIds.value)
      : new Set(allIds)
    if (visibleIdSet.has(id)) {
      visibleIdSet.delete(id)
    } else {
      visibleIdSet.add(id)
    }
    const nextVisibleIds = allIds.filter((accountId) => visibleIdSet.has(accountId))
    activeAccountIds.value = nextVisibleIds.length === allIds.length ? [] : nextVisibleIds
    options.requestRender()
    return true
  }

  function clearAccountSearchTimer() {
    if (accountSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(accountSearchTimer)
      accountSearchTimer = undefined
    }
  }

  function pruneAccountState() {
    const currentOverview = options.base.value
    if (!currentOverview) return
    syncAddedAccountSelections()
    const visibleIds = new Set([...overviewAccounts.value.map((account) => account.id), ...addedAccountIds.value])
    activeAccountIds.value = activeAccountIds.value.filter((id) => visibleIds.has(id))
  }

  function currentAccountOptionsRequest(): { key: string; systemAccountId?: string; keyword: string; accountIds: string[]; domain?: AccountCapabilityDomain } {
    const keyword = accountSearchKeyword.value.trim()
    const systemAccountId = options.selectedSystemAccountId()
    const accountIds = [...addedAccountIds.value]
    const domain = options.capabilityDomain()
    return {
      key: JSON.stringify([options.isManagementView.value ? 'management' : 'self', systemAccountId ?? '', keyword, domain ?? '', accountIds]),
      systemAccountId,
      keyword,
      accountIds,
      domain
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
      providerCode: option.providerCode,
      systemAccountName: option?.systemAccountName,
      ownerSystemAccountName: option?.ownerSystemAccountName ?? selection?.ownerSystemAccountName,
      accessType: option?.accessType ?? selection?.accessType
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
    activeAccountIds,
    addedAccountIds,
    addedAccountSelections,
    clearAccountSearchTimer,
    clearAccountState,
    handleAccountDropdownVisibleChange,
    handleAccountSearch,
    handleAddedAccountsChange,
    hasActiveAccountFilter,
    invalidateAccountOptions,
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
