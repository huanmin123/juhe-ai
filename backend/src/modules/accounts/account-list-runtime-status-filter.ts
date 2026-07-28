import { accountFilterStatuses, accountMatchesStatusFilters } from '../../domain/account-status-classification.js'
import type { AccountListItem } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountStatusFilterValues, normalizeAccountListOptions, type AccountListOptions } from '../../storage/account-list-options.js'
import {
  listAccountManagementItemsPageAsync,
  type AccountManagementListResult
} from '../../storage/account-management-list.repository.js'
import { pagedTotalUpperBound, pageUpperBoundForWindow } from '../../storage/query-utils.js'
import { hydrateAccountListPage } from './account-status-snapshot.service.js'

export function accountListNeedsRuntimeStatusFilter(options: AccountListOptions): boolean {
  const normalized = normalizeAccountListOptions(options)
  return accountStatusFilterValues(normalized.status).length > 0 || normalized.schedulable !== 'all'
}

export async function listAccountsPageWithRuntimeStatusFilter(
  access: AccessScope | undefined,
  options: AccountListOptions
): Promise<AccountManagementListResult | undefined> {
  const listOptions = normalizeAccountListOptions(options)
  if (!accountListNeedsRuntimeStatusFilter(listOptions)) return undefined

  const statusFilters = accountStatusFilterValues(listOptions.status)
  const pageSize = listOptions.pageSize
  const sourcePageSize = Math.min(200, Math.max(50, pageSize * 4))
  const skipTarget = (listOptions.page - 1) * pageSize
  const sourcePageLimit = pageUpperBoundForWindow(sourcePageSize)
  const output: AccountListItem[] = []
  let matchedCount = 0
  let sourcePage = 1
  let exhausted = false
  let generatedAt = new Date().toISOString()

  while (output.length <= pageSize && !exhausted && sourcePage <= sourcePageLimit) {
    const basePage = await listAccountManagementItemsPageAsync(access, {
      ...listOptions,
      page: sourcePage,
      pageSize: sourcePageSize,
      status: undefined,
      schedulable: 'all'
    })
    const hydratedPage = await hydrateAccountListPage(access, basePage)
    generatedAt = hydratedPage.generatedAt
    for (const account of hydratedPage.items) {
      if (!accountMatchesStatusFilters(account, statusFilters)) continue
      if (!accountMatchesSchedulableFilter(account, listOptions.schedulable)) continue
      if (matchedCount < skipTarget) {
        matchedCount += 1
        continue
      }
      output.push(account)
      matchedCount += 1
      if (output.length > pageSize) break
    }
    exhausted = !basePage.hasMore
    sourcePage += 1
  }

  const hasMore = output.length > pageSize || !exhausted
  const items = output.slice(0, pageSize)
  return {
    items,
    total: pagedTotalUpperBound(listOptions.page, pageSize, items.length, hasMore),
    hasMore,
    page: listOptions.page,
    pageSize,
    generatedAt
  }
}

function accountMatchesSchedulableFilter(
  account: AccountListItem,
  filter: ReturnType<typeof normalizeAccountListOptions>['schedulable']
): boolean {
  if (filter === 'all') return true
  const statuses = accountFilterStatuses(account)
  const cooling = statuses.has('rate_limited') || statuses.has('temporary_unavailable')
  if (filter === 'cooling') return cooling
  if (filter === 'enabled') return account.effectiveAvailability.available
  return !account.effectiveAvailability.available && !cooling
}
