import type { AccountEffectiveAvailabilityStatus, AccountListItem, AccountStatus } from '@/types/domain'
import { matchesSystemAccountFilter } from '@/utils/systemAccountFilter'
import type { AccountFilters, AccountStatusFilterValue } from './accountFormTypes'
import { normalizeKeyword } from './accountBasicFormatters'
import { isAccountScheduleOutOfWindow } from './accountStatusPresentation'

export function filterAccounts(input: {
  accounts: AccountListItem[]
  filters: AccountFilters
  isManagementView: boolean
}): AccountListItem[] {
  const keyword = normalizeKeyword(input.filters.keyword)
  return input.accounts.filter((account) => {
    const normalizedName = normalizeKeyword(account.name)
    const keywordMatched = !keyword || normalizedName.includes(keyword)
    const providerMatched = !input.filters.providerCode || input.filters.providerCode === 'all' || account.providerCode === input.filters.providerCode
    const typeMatched = !input.filters.type || input.filters.type === 'all' || account.type === input.filters.type
    const statusMatched = input.filters.status.length === 0 || input.filters.status.some((status) => accountMatchesStatusFilter(account, status))
    const groupMatched = !input.filters.groupId || account.boundGroupId === input.filters.groupId
    const tagMatched = input.filters.tagIds.length === 0 || (account.tags ?? []).some((tag) => input.filters.tagIds.includes(tag.id))
    const systemAccountMatched = matchesSystemAccountFilter(account, input.filters.systemAccountId, input.isManagementView)
    return keywordMatched && providerMatched && typeMatched && statusMatched && groupMatched && tagMatched && systemAccountMatched
  })
}

function accountMatchesStatusFilter(account: AccountListItem, status: AccountFilters['status'][number]): boolean {
  return accountFilterStatuses(account).has(status)
}

function accountFilterStatuses(account: AccountListItem): Set<AccountStatusFilterValue> {
  const availabilityStatus = account.effectiveAvailability?.status
  const derivedStatus = availabilityStatus ? statusFilterForEffectiveAvailability(availabilityStatus) : undefined
  if (derivedStatus) {
    return new Set([derivedStatus])
  }
  if (account.authorizationQuotaExceeded) {
    return new Set(['rate_limited'])
  }
  // 时间计划闸门：status='active' 且 gate=0 只归入「非时间段」，既不算正常也不算停用。
  if (isAccountScheduleOutOfWindow(account)) {
    return new Set(['out_of_window'])
  }
  if (account.status === 'active' && account.effectiveAvailability?.available === false) {
    return new Set()
  }
  return new Set([account.status])
}

function statusFilterForEffectiveAvailability(status: AccountEffectiveAvailabilityStatus): AccountStatusFilterValue | undefined {
  if (status === 'available' || status === 'runtime_degraded') return 'active'
  // 时间计划闸门（时间计划闸门设计 §5.4）：时段外是正常状态的子集，独立筛选值。
  if (status === 'schedule_out_of_window') return 'out_of_window'
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
    || status === 'instance_temporary_unavailable'
    || status === 'instance_cooldown'
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
    || status === 'permission_denied'
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

/** 状态筛选可选项校验：真实账户状态 + 时间计划闸门派生值「非时间段」。 */
const accountStatusValues = new Set<AccountStatus>([
  'active',
  'pending_test',
  'disabled',
  'error',
  'rate_limited',
  'temporary_unavailable',
  'quality_isolated'
])

export function isAccountStatusFilterValue(value: string): value is AccountStatusFilterValue {
  return value === 'out_of_window' || accountStatusValues.has(value as AccountStatus)
}

/**
 * 拆分账户状态筛选为列表接口参数（时间计划闸门设计 §5.4）：
 * - 真实账户状态走 `status`（多值 OR）；
 * - 「非时间段」不是账户状态，走 `schedulable=out_of_window`；
 * - 两者同时选中时按列表接口既有的 AND 语义叠加。
 */
export function accountStatusFilterQuery(values: AccountStatusFilterValue[]): {
  status?: AccountStatus[]
  schedulable?: 'out_of_window'
} {
  const statuses = values.filter((value): value is AccountStatus => value !== 'out_of_window')
  return {
    status: statuses.length ? statuses : undefined,
    schedulable: values.includes('out_of_window') ? 'out_of_window' : undefined
  }
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
