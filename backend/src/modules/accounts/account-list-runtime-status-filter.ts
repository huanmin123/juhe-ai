import { accountMatchesStatusFilters } from '../../domain/account-status-classification.js'
import type { AccountSummary } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountStatusFilterValues, normalizeAccountListOptions, type AccountListOptions } from '../../storage/account-list-options.js'
import { pagedTotalUpperBound } from '../../storage/query-utils.js'
import { listAccountsPage, type AccountListResult } from '../../storage/repositories.js'
import { requestServerAccountRuntimeSnapshot } from '../db-service/db-service-ipc.js'
import { applyServerAccountConcurrencyToAccountList, type AccountRuntimeSnapshotStatus } from '../gateway/runtime/runtime-snapshot.service.js'

export async function applyAccountListRuntimeStatusFilter(
  access: AccessScope | undefined,
  options: AccountListOptions,
  result: AccountListResult & { runtimeSnapshot?: AccountRuntimeSnapshotStatus }
): Promise<AccountListResult & { runtimeSnapshot?: AccountRuntimeSnapshotStatus }> {
  const listOptions = normalizeAccountListOptions(options)
  const statusFilters = accountStatusFilterValues(listOptions.status)
  if (!statusFilters.some(statusFilterCanBeChangedByRuntime)) return result
  if (result.runtimeSnapshot?.accountRuntimeAvailabilityAvailable !== true) return result
  const serverRuntimeSnapshot = await requestServerAccountRuntimeSnapshot(80).catch(() => undefined)
  const runtimeBlockedAccountCount = runtimeBlockedAccountIds(serverRuntimeSnapshot?.accountRuntimeAvailability).size

  const pageSize = listOptions.pageSize
  const sourcePageSize = Math.max(pageSize, Math.min(500, pageSize * 4))
  const skipTarget = (listOptions.page - 1) * pageSize
  const maxSourcePages = Math.max(1, Math.ceil((skipTarget + pageSize + runtimeBlockedAccountCount + 1) / sourcePageSize) + 1)
  const output: AccountSummary[] = []
  let matchedCount = 0
  let sourcePage = 1
  let hasMore = false
  const outputRuntimeSnapshot: AccountRuntimeSnapshotStatus = {
    accountConcurrencyAvailable: true,
    accountRuntimeAvailabilityAvailable: true
  }

  while (!hasMore && sourcePage <= maxSourcePages) {
    const candidatePage = listAccountsPage(access, {
      ...listOptions,
      status: undefined,
      page: sourcePage,
      pageSize: sourcePageSize
    })
    const hydratedPage = await applyServerAccountConcurrencyToAccountList(candidatePage)
    outputRuntimeSnapshot.accountConcurrencyAvailable &&= hydratedPage.runtimeSnapshot.accountConcurrencyAvailable
    outputRuntimeSnapshot.accountRuntimeAvailabilityAvailable &&= hydratedPage.runtimeSnapshot.accountRuntimeAvailabilityAvailable
    for (const account of hydratedPage.items) {
      if (!accountMatchesStatusFilters(account, statusFilters)) continue
      if (matchedCount < skipTarget) {
        matchedCount += 1
        continue
      }
      output.push(account)
      matchedCount += 1
      if (output.length > pageSize) {
        hasMore = true
        break
      }
    }
    if (!candidatePage.hasMore) break
    sourcePage += 1
  }

  const items = output.slice(0, pageSize)
  return {
    ...result,
    items,
    total: pagedTotalUpperBound(listOptions.page, pageSize, items.length, hasMore),
    hasMore,
    page: listOptions.page,
    pageSize,
    runtimeSnapshot: outputRuntimeSnapshot
  }
}

function statusFilterCanBeChangedByRuntime(status: string): boolean {
  return status === 'active' || status === 'temporary_unavailable'
}

function runtimeBlockedAccountIds(runtimeAvailability: Record<string, { status?: string }> | undefined): Set<string> {
  const output = new Set<string>()
  if (!runtimeAvailability) return output
  for (const [runtimeKey, runtime] of Object.entries(runtimeAvailability)) {
    if (!runtime || runtime.status === 'normal') continue
    const accountId = runtimeKey.includes(':authorized:')
      ? runtimeKey.slice(0, runtimeKey.indexOf(':authorized:'))
      : runtimeKey
    if (accountId) output.add(accountId)
  }
  return output
}
