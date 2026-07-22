import { toValue, type MaybeRefOrGetter } from 'vue'

import { rememberGroupLabel } from '@/shared/groupLabelCache'
import type { AccountSummary, GroupOptionSummary, ProviderDefinition } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import type { AccountFilters } from './accountFormTypes'

interface UseAccountFilterInteractionsOptions {
  accounts: MaybeRefOrGetter<AccountSummary[]>
  applyFilters: () => void
  availableProviders: MaybeRefOrGetter<ProviderDefinition[]>
  clearSelection: () => void
  filterGroupOptions: MaybeRefOrGetter<GroupOptionSummary[]>
  filters: AccountFilters
  handleAccountListSystemAccountFilterChange: () => void
  resetAccountListFilters: () => void
  resetFilterAccountTagOptions: () => void
  resetFilterGroupOptionsSearch: () => void
  resetGroupOptionsSearch: () => void
}

export function useAccountFilterInteractions(options: UseAccountFilterInteractionsOptions) {
  function rememberAccountGroupLabels(items: AccountSummary[]): void {
    for (const account of items) {
      rememberGroupLabel(account.boundGroupId, account.boundGroupName)
    }
  }

  function syncFilterGroupSelection(): void {
    const filters = options.filters
    if (!filters.groupId) {
      filters.group = undefined
      return
    }
    const group = toValue(options.filterGroupOptions).find((item) => item.id === filters.groupId)
    if (group) {
      filters.group = { id: group.id, name: group.name }
      return
    }
    const account = toValue(options.accounts).find((item) => item.boundGroupId === filters.groupId && item.boundGroupName)
    if (account?.boundGroupName) {
      filters.group = { id: filters.groupId, name: account.boundGroupName }
    }
  }

  function handleProviderFilterChange(value: string): void {
    const filters = options.filters
    filters.providerCode = value || 'all'
    if (filters.providerCode !== 'all') {
      filters.groupId = ''
      filters.group = undefined
    }
    const provider = filters.providerCode === 'all'
      ? undefined
      : toValue(options.availableProviders).find((item) => item.code === filters.providerCode)
    const providerAccountTypes = provider?.protocolProfiles.length
      ? provider.protocolProfiles.flatMap((profile) => profile.accountTypes)
      : provider?.accountTypes ?? []
    if (provider && filters.type !== 'all' && !providerAccountTypes.includes(filters.type)) {
      filters.type = 'all'
    }
    options.resetFilterGroupOptionsSearch()
    options.applyFilters()
  }

  function handleAccountTypeFilterChange(value: string): void {
    options.filters.type = value || 'all'
    options.applyFilters()
  }

  function resetFilters(): void {
    options.clearSelection()
    options.resetGroupOptionsSearch()
    options.resetFilterGroupOptionsSearch()
    options.resetFilterAccountTagOptions()
    options.resetAccountListFilters()
  }

  function handleSystemAccountFilterChange(): void {
    const filters = options.filters
    options.clearSelection()
    filters.groupId = ''
    filters.group = undefined
    filters.tagIds = []
    if (filters.systemAccountId === allSystemAccountsValue) {
      filters.systemAccount = undefined
    }
    options.resetGroupOptionsSearch()
    options.resetFilterGroupOptionsSearch()
    options.resetFilterAccountTagOptions()
    options.handleAccountListSystemAccountFilterChange()
  }

  return {
    handleAccountTypeFilterChange,
    handleProviderFilterChange,
    handleSystemAccountFilterChange,
    rememberAccountGroupLabels,
    resetFilters,
    syncFilterGroupSelection
  }
}
