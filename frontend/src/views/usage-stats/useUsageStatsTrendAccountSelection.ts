import { accountSelectionForId, accountSelectOptionLabel, rememberAccountSelection, rememberAccountSelections, type AccountSelection } from '@/shared/accountLabelCache'
import type { AccountOptionSummary, AccountUsageStatsOverview, AccountUsageStatsRow } from '@/types/domain'
import { computed, ref, watch, type Ref } from 'vue'

import { dedupeRowsById, metricText, metricValue, placeholderTrendRow, usageTrendDateKeys } from './usageStatsHelpers'
import { chartColors, orderedUsageRows } from './usageTrendChartOptions'
import type { UsageTrendMetric } from './usageTrendMetrics'

interface UseUsageStatsTrendAccountsOptions {
  overview: Ref<AccountUsageStatsOverview | undefined>
  rows: Ref<AccountUsageStatsRow[]>
  accountOptionRows: Ref<AccountOptionSummary[]>
  addedTrendAccountIds: Ref<string[]>
  selectedRange: Ref<readonly [string, string]>
  selectedMetric: Ref<UsageTrendMetric>
  rangeLabel: Ref<string>
  isManagementView: () => boolean
  providerName: (providerCode?: string) => string
}

export function useUsageStatsTrendAccountSelection(options: UseUsageStatsTrendAccountsOptions) {
  const selectedTrendAccountIds = ref<string[]>([])
  const addedTrendAccountSelections = ref<AccountSelection[]>([])
  const rowsById = computed(() => new Map(options.rows.value.map((row) => [row.id, row])))
  const accountOptionById = computed(() => new Map(options.accountOptionRows.value.map((account) => [account.id, account])))
  const addedTrendSelectionById = computed(() => new Map(addedTrendAccountSelections.value.map((account) => [account.id, account])))
  const defaultTrendRows = computed(() => (options.overview.value?.defaultTrendAccountIds ?? [])
    .map((id) => rowsById.value.get(id))
    .filter((row): row is AccountUsageStatsRow => Boolean(row)))
  const defaultTrendAccountIdSet = computed(() => new Set(options.overview.value?.defaultTrendAccountIds ?? defaultTrendRows.value.map((account) => account.id)))
  const addedTrendAccountIdSet = computed(() => new Set(options.addedTrendAccountIds.value))
  const addedTrendRows = computed(() => {
    const dateKeys = usageTrendDateKeys(options.selectedRange.value)
    return options.addedTrendAccountIds.value
      .map((id) => rowsById.value.get(id) ?? placeholderTrendRow(id, {
        accountOptionById: accountOptionById.value,
        addedTrendSelectionById: addedTrendSelectionById.value,
        dateKeys
      }))
      .filter((row): row is AccountUsageStatsRow => Boolean(row))
  })
  const trendAccountRows = computed(() => dedupeRowsById([...defaultTrendRows.value, ...addedTrendRows.value]))
  const accountPickerHiddenValues = computed(() => [
    ...trendAccountRows.value.map((account) => account.id),
    ...options.addedTrendAccountIds.value
  ])
  const selectedTrendRows = computed(() => {
    const selectedIds = new Set(selectedTrendAccountIds.value)
    return trendAccountRows.value.filter((row) => selectedIds.has(row.id))
  })
  const visibleTrendRows = computed(() => selectedTrendAccountIds.value.length ? selectedTrendRows.value : trendAccountRows.value)
  const hasSelectedTrendAccounts = computed(() => selectedTrendAccountIds.value.length > 0)
  const displayRows = computed(() => hasSelectedTrendAccounts.value ? orderedUsageRows(selectedTrendRows.value) : options.rows.value)
  const accountUsageEmptyDescription = computed(() => hasSelectedTrendAccounts.value
    ? '当前已选账户在日期范围内暂无用量。'
    : '当前日期范围暂无账户用量，等待后台聚合后会显示结果。')
  const trendEmptyDescription = computed(() => visibleTrendRows.value.length
    ? `${options.rangeLabel.value} 暂无${metricText(options.selectedMetric.value)}消耗趋势`
    : '暂无可展示账户')
  const hasTrendData = computed(() => visibleTrendRows.value.some((row) => row.dailyUsage.some((point) => metricValue(point, options.selectedMetric.value) > 0)))
  const accountFilterItems = computed(() => {
    const selectedIds = new Set(selectedTrendAccountIds.value)
    return trendAccountRows.value.map((account, index) => ({
      account,
      label: trendAccountLabel(account),
      color: chartColors[index % chartColors.length],
      selected: selectedIds.has(account.id),
      removable: addedTrendAccountIdSet.value.has(account.id) && !defaultTrendAccountIdSet.value.has(account.id)
    }))
  })

  function toggleTrendAccount(id: string): boolean {
    if (!trendAccountRows.value.some((row) => row.id === id)) return false
    selectedTrendAccountIds.value = selectedTrendAccountIds.value.includes(id)
      ? selectedTrendAccountIds.value.filter((accountId) => accountId !== id)
      : [...selectedTrendAccountIds.value, id]
    return true
  }

  function updateAddedTrendAccounts(value: string[], previousValue: string[]): void {
    const previousIds = new Set(previousValue)
    const acceptedIds = value.filter((id) => !defaultTrendAccountIdSet.value.has(id))
    for (const id of acceptedIds) {
      rememberAddedTrendAccountSelection(id)
    }
    options.addedTrendAccountIds.value = acceptedIds
    syncAddedTrendAccountSelections()
    const newlyAddedIds = acceptedIds.filter((id) => !previousIds.has(id))
    const visibleIds = new Set([...defaultTrendAccountIdSet.value, ...acceptedIds])
    const nextSelectedIds = selectedTrendAccountIds.value.filter((id) => visibleIds.has(id))
    selectedTrendAccountIds.value = selectedTrendAccountIds.value.length
      ? [...new Set([...nextSelectedIds, ...newlyAddedIds])]
      : nextSelectedIds
  }

  function removeAddedTrendAccount(id: string): boolean {
    if (!addedTrendAccountIdSet.value.has(id)) return false
    options.addedTrendAccountIds.value = options.addedTrendAccountIds.value.filter((accountId) => accountId !== id)
    addedTrendAccountSelections.value = addedTrendAccountSelections.value.filter((selection) => selection.id !== id)
    selectedTrendAccountIds.value = selectedTrendAccountIds.value.filter((accountId) => accountId !== id)
    return true
  }

  function clearTrendAccountState(): void {
    selectedTrendAccountIds.value = []
    options.addedTrendAccountIds.value = []
    addedTrendAccountSelections.value = []
  }

  function pruneSelectedTrendAccounts(currentRows: AccountUsageStatsRow[]): void {
    const currentIds = new Set(currentRows.map((row) => row.id))
    syncAddedTrendAccountSelections()
    const defaultIds = new Set((options.overview.value?.defaultTrendAccountIds ?? []).filter((id) => currentIds.has(id)))
    const visibleIds = new Set([...defaultIds, ...options.addedTrendAccountIds.value])
    selectedTrendAccountIds.value = selectedTrendAccountIds.value.filter((id) => visibleIds.has(id))
  }

  function trendAccountLabel(account: AccountUsageStatsRow): string {
    if (account.accessType === 'authorized') {
      return accountSelectOptionLabel(account)
    }
    const sameNameCount = options.rows.value.filter((row) => row.name === account.name).length
    if (sameNameCount <= 1) return account.name
    const suffix = options.isManagementView() && account.systemAccountName
      ? account.systemAccountName
      : options.providerName(account.providerCode)
    return `${account.name}（${suffix}）`
  }

  function rememberAddedTrendAccountSelection(id: string): void {
    const selection = accountSelectionForId(id, [...options.accountOptionRows.value, ...options.rows.value])
    rememberAccountSelection(selection)
    if (!selection || addedTrendAccountSelections.value.some((item) => item.id === selection.id)) return
    addedTrendAccountSelections.value = [...addedTrendAccountSelections.value, selection]
  }

  function syncAddedTrendAccountSelections(): void {
    const existing = new Map(addedTrendAccountSelections.value.map((selection) => [selection.id, selection]))
    addedTrendAccountSelections.value = options.addedTrendAccountIds.value
      .map((id) => accountSelectionForId(id, [...options.accountOptionRows.value, ...options.rows.value]) ?? existing.get(id))
      .filter((selection): selection is AccountSelection => Boolean(selection))
    rememberAccountSelections(addedTrendAccountSelections.value)
  }

  watch(addedTrendAccountSelections, (selections) => rememberAccountSelections(selections), { deep: true, immediate: true })

  return {
    accountFilterItems,
    accountPickerHiddenValues,
    accountUsageEmptyDescription,
    addedTrendAccountIds: options.addedTrendAccountIds,
    addedTrendAccountSelections,
    displayRows,
    hasTrendData,
    hasSelectedTrendAccounts,
    selectedTrendAccountIds,
    trendEmptyDescription,
    visibleTrendRows,
    clearTrendAccountState,
    pruneSelectedTrendAccounts,
    removeAddedTrendAccount,
    toggleTrendAccount,
    updateAddedTrendAccounts
  }
}
