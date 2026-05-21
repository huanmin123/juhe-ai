import type { AccountSummary } from '@/types/domain'
import { matchesSystemAccountFilter } from '@/utils/systemAccountFilter'
import type { AccountFilters } from './accountFormTypes'
import { normalizeKeyword } from './accountFormatters'

export function filterAccounts(input: {
  accounts: AccountSummary[]
  filters: AccountFilters
  isManagementView: boolean
}): AccountSummary[] {
  const keyword = normalizeKeyword(input.filters.keyword)
  return input.accounts.filter((account) => {
    const normalizedName = normalizeKeyword(account.name)
    const keywordMatched = !keyword || normalizedName === keyword || normalizedName.startsWith(keyword)
    const typeMatched = input.filters.type === 'all' || account.type === input.filters.type
    const statusMatched = input.filters.status.length === 0 || input.filters.status.includes(account.status)
    const groupMatched = !input.filters.groupId || account.boundGroupId === input.filters.groupId
    const systemAccountMatched = matchesSystemAccountFilter(account, input.filters.systemAccountId, input.isManagementView)
    return keywordMatched && typeMatched && statusMatched && groupMatched && systemAccountMatched
  })
}

export function countActiveAccountFilters(filters: AccountFilters, isManagementView: boolean, allSystemAccountsValue: string): number {
  return [
    filters.type !== 'all',
    filters.status.length > 0,
    Boolean(filters.groupId),
    isManagementView && filters.systemAccountId !== allSystemAccountsValue
  ].filter(Boolean).length
}
