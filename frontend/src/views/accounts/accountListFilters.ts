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
  isAdmin: boolean
}): AccountSummary[] {
  const keyword = normalizeKeyword(input.filters.keyword)
  return input.accounts.filter((account) => {
    const keywordMatched = !keyword || [
      account.name,
      account.notes ?? '',
      account.providerCode,
      input.groupNameForAccount(account.id) ?? '',
      account.type,
      accountBaseUrl(account),
      account.id
    ].some((value) => normalizeKeyword(value).includes(keyword))
    const typeMatched = input.filters.type === 'all' || account.type === input.filters.type
    const statusMatched = input.filters.status === 'all' || account.status === input.filters.status
    const schedulableMatched = matchesSchedulableFilter(account, input.filters.schedulable)
    const systemAccountMatched = matchesSystemAccountFilter(account, input.filters.systemAccountId, input.isAdmin)
    return keywordMatched && typeMatched && statusMatched && schedulableMatched && systemAccountMatched
  })
}

export function countActiveAccountFilters(filters: AccountFilters, isAdmin: boolean, allSystemAccountsValue: string): number {
  return [
    filters.type !== 'all',
    filters.status !== 'all',
    filters.schedulable !== 'all',
    isAdmin && filters.systemAccountId !== allSystemAccountsValue
  ].filter(Boolean).length
}

export function accountBaseUrl(account: AccountSummary): string {
  return asString(account.credentials.base_url)
}
