import type { AccountEffectiveAvailabilityStatus, AccountStatus, AccountSummary } from '@/types/domain'
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
  return accountFilterStatuses(account).has(status)
}

function accountFilterStatuses(account: AccountSummary): Set<AccountStatus> {
  const availabilityStatus = account.effectiveAvailability?.status
  const derivedStatus = availabilityStatus ? statusFilterForEffectiveAvailability(availabilityStatus) : undefined
  if (derivedStatus) {
    return new Set([derivedStatus])
  }
  if (account.authorizationQuotaExceeded) {
    return new Set(['rate_limited'])
  }
  if (account.status === 'active' && account.effectiveAvailability?.available === false) {
    return new Set()
  }
  return new Set([account.status])
}

function statusFilterForEffectiveAvailability(status: AccountEffectiveAvailabilityStatus): AccountStatus | undefined {
  if (status === 'available') return 'active'
  if (status === 'source_pending_test' || status === 'instance_pending_test') return 'pending_test'
  if (status === 'source_error' || status === 'instance_error') return 'error'
  if (
    status === 'authorization_quota_exceeded'
    || status === 'source_rate_limited'
    || status === 'instance_rate_limited'
  ) {
    return 'rate_limited'
  }
  if (
    status === 'source_temporary_unavailable'
    || status === 'source_cooldown'
    || status === 'source_schedule_inactive'
    || status === 'instance_temporary_unavailable'
    || status === 'instance_cooldown'
    || status === 'instance_schedule_inactive'
    || status === 'api_key_pool_unavailable'
    || status === 'runtime_local_suppressed'
    || status === 'runtime_half_open'
    || status === 'runtime_precheck_pending'
    || status === 'runtime_precheck_failed'
  ) {
    return 'temporary_unavailable'
  }
  if (
    status === 'authorization_expired'
    || status === 'authorization_paused'
    || status === 'authorization_unavailable'
    || status === 'binding_missing'
    || status === 'source_deleted'
    || status === 'source_expired'
    || status === 'source_disabled'
    || status === 'source_unschedulable'
    || status === 'instance_expired'
    || status === 'instance_disabled'
    || status === 'instance_unschedulable'
  ) {
    return 'disabled'
  }
  return undefined
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
