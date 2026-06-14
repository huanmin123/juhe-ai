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
    const providerMatched = !input.filters.providerCode || input.filters.providerCode === 'all' || account.providerCode === input.filters.providerCode
    const typeMatched = !input.filters.type || input.filters.type === 'all' || account.type === input.filters.type
    const statusMatched = input.filters.status.length === 0 || input.filters.status.some((status) => accountMatchesStatusFilter(account, status))
    const groupMatched = !input.filters.groupId || account.boundGroupId === input.filters.groupId
    const tagMatched = input.filters.tagIds.length === 0 || (account.tags ?? []).some((tag) => input.filters.tagIds.includes(tag.id))
    const systemAccountMatched = matchesSystemAccountFilter(account, input.filters.systemAccountId, input.isManagementView)
    return keywordMatched && providerMatched && typeMatched && statusMatched && groupMatched && tagMatched && systemAccountMatched
  })
}

function accountMatchesStatusFilter(account: AccountSummary, status: AccountFilters['status'][number]): boolean {
  if (status === 'active') {
    return account.status === 'active' && account.effectiveAvailability?.available !== false
  }
  if (status === 'rate_limited') {
    return account.status === 'rate_limited'
      || account.effectiveAvailability?.status === 'authorization_quota_exceeded'
      || account.authorizationQuotaExceeded === true
  }
  return account.status === status
}

export function countActiveAccountFilters(filters: AccountFilters, isManagementView: boolean, allSystemAccountsValue: string): number {
  return [
    Boolean(filters.providerCode && filters.providerCode !== 'all'),
    Boolean(filters.type && filters.type !== 'all'),
    filters.status.length > 0,
    Boolean(filters.groupId),
    filters.tagIds.length > 0,
    isManagementView && filters.systemAccountId !== allSystemAccountsValue
  ].filter(Boolean).length
}
