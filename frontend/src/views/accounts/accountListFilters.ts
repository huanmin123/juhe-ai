import type { AccountSummary } from '@/types/domain'
import { matchesSystemAccountFilter } from '@/utils/systemAccountFilter'
import type { AccountFilters } from './accountFormTypes'
import {
  asString,
  matchesSchedulableFilter,
  normalizeKeyword
} from './accountFormatters'

export function filterAccounts(input: {
  accounts: AccountSummary[]
  filters: AccountFilters
  groupNameForAccount: (accountId: string) => string | undefined
  isManagementView: boolean
}): AccountSummary[] {
  const keyword = normalizeKeyword(input.filters.keyword)
  return input.accounts.filter((account) => {
    const keywordMatched = !keyword || [
      account.name,
      account.providerCode,
      input.groupNameForAccount(account.id) ?? '',
      account.type,
      accountBaseUrl(account),
      account.id
    ].some((value) => {
      const normalizedValue = normalizeKeyword(value)
      return normalizedValue === keyword || normalizedValue.startsWith(keyword)
    })
    const typeMatched = input.filters.type === 'all' || account.type === input.filters.type
    const statusMatched = input.filters.status === 'all' || account.status === input.filters.status
    const schedulableMatched = matchesSchedulableFilter(account, input.filters.schedulable)
    const systemAccountMatched = matchesSystemAccountFilter(account, input.filters.systemAccountId, input.isManagementView)
    return keywordMatched && typeMatched && statusMatched && schedulableMatched && systemAccountMatched
  })
}

export function countActiveAccountFilters(filters: AccountFilters, isManagementView: boolean, allSystemAccountsValue: string): number {
  return [
    filters.type !== 'all',
    filters.status !== 'all',
    filters.schedulable !== 'all',
    isManagementView && filters.systemAccountId !== allSystemAccountsValue
  ].filter(Boolean).length
}

export function accountBaseUrl(account: AccountSummary): string {
  return asString(account.credentials.base_url)
}
