import {
  accountFilterStatuses,
  accountMatchesStatusFilters,
  isAccountStatus
} from '../../domain/account-status-classification.js'
import type { AccountListItem } from '../../domain/types.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountStatusFilterValues, normalizeAccountListOptions, type AccountListOptions } from '../../storage/account-list-options.js'
import {
  listAccountManagementItemsPageAsync,
  type AccountManagementListPage,
  type AccountManagementListResult
} from '../../storage/account-management-list.repository.js'
import {
  chunkValues,
  defaultListWindowRows,
  pagedTotalUpperBound,
  pageUpperBoundForWindow
} from '../../storage/query-utils.js'
import { hydrateAccountListPage } from './account-status-snapshot.service.js'

export interface AccountRuntimeStatusCandidateWindow {
  page: number
  pageSize: number
}

export interface AccountRuntimeStatusCandidateProgress {
  requiredMatchCount: number
  totalMatchedCount: number
  latestCandidateCount: number
  latestMatchedCount: number
  prefixQueryCount: number
}

const maxCandidateBatchSize = 200
const maxRuntimeStatusHydrationBatchSize = 100
const maxCandidateRows = defaultListWindowRows - 1
const maxAdaptivePrefixQueryCount = 3

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
  const skipTarget = (listOptions.page - 1) * pageSize
  const requiredMatchCount = skipTarget + pageSize + 1
  const candidateSourceOptions = accountRuntimeStatusCandidateSourceOptions(listOptions)
  const output: AccountListItem[] = []
  const seenCandidateIds = new Set<string>()
  let matchedCount = 0
  let exhausted = false
  let generatedAt = new Date().toISOString()
  let candidateWindow = initialAccountRuntimeStatusCandidateWindow(listOptions)
  let prefixQueryCount = 0

  while (output.length <= pageSize && !exhausted && candidateWindow) {
    const basePage = await listAccountManagementItemsPageAsync(access, {
      ...listOptions,
      ...candidateSourceOptions,
      page: candidateWindow.page,
      pageSize: candidateWindow.pageSize
    })
    if (candidateWindow.page === 1) prefixQueryCount += 1
    const freshBasePage = accountRuntimeStatusFreshCandidatePage(basePage, seenCandidateIds)
    const hydratedPage = await hydrateAccountRuntimeStatusCandidatePage(access, freshBasePage)
    generatedAt = hydratedPage.generatedAt
    let latestMatchedCount = 0
    for (const account of hydratedPage.items) {
      if (!accountMatchesStatusFilters(account, statusFilters)) continue
      if (!accountMatchesSchedulableFilter(account, listOptions.schedulable)) continue
      latestMatchedCount += 1
      if (matchedCount < skipTarget) {
        matchedCount += 1
        continue
      }
      output.push(account)
      matchedCount += 1
      if (output.length > pageSize) break
    }
    exhausted = !basePage.hasMore
    if (output.length > pageSize || exhausted) break
    const nextWindow = nextAccountRuntimeStatusCandidateWindow(candidateWindow, {
      requiredMatchCount,
      totalMatchedCount: matchedCount,
      latestCandidateCount: freshBasePage.items.length,
      latestMatchedCount,
      prefixQueryCount
    })
    if (!nextWindow) break
    candidateWindow = nextWindow
  }

  const hasMore = listOptions.page < pageUpperBoundForWindow(pageSize)
    && (output.length > pageSize || !exhausted)
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

export function accountRuntimeStatusCandidateSourceOptions(
  options: AccountListOptions
): Pick<AccountListOptions, 'status' | 'schedulable'> {
  const normalized = normalizeAccountListOptions(options)
  const statuses = accountStatusFilterValues(normalized.status).filter(isAccountStatus)
  let status: string | undefined
  if (statuses.length > 0 && !statuses.includes('rate_limited')) {
    const sourceStatuses = new Set(statuses)
    if (sourceStatuses.has('temporary_unavailable')) sourceStatuses.add('active')
    status = [...sourceStatuses].join(',')
  }
  return {
    status,
    schedulable: normalized.schedulable === 'enabled' ? 'enabled' : 'all'
  }
}

export function initialAccountRuntimeStatusCandidateWindow(
  options: AccountListOptions
): AccountRuntimeStatusCandidateWindow {
  const normalized = normalizeAccountListOptions(options)
  const requiredMatchCount = (normalized.page - 1) * normalized.pageSize + normalized.pageSize + 1
  return {
    page: 1,
    pageSize: Math.min(maxCandidateBatchSize, Math.max(1, requiredMatchCount))
  }
}

export function nextAccountRuntimeStatusCandidateWindow(
  current: AccountRuntimeStatusCandidateWindow,
  progress: AccountRuntimeStatusCandidateProgress
): AccountRuntimeStatusCandidateWindow | undefined {
  if (progress.totalMatchedCount >= progress.requiredMatchCount) return undefined
  if (current.page === 1 && current.pageSize < maxCandidateBatchSize) {
    const remainingMatches = Math.max(1, progress.requiredMatchCount - progress.totalMatchedCount)
    const observedYield = progress.latestCandidateCount > 0
      ? progress.latestMatchedCount / progress.latestCandidateCount
      : 0
    const forceFullPrefix = observedYield <= 0 || progress.prefixQueryCount >= maxAdaptivePrefixQueryCount
    const estimatedAdditionalCandidates = forceFullPrefix
      ? maxCandidateBatchSize - current.pageSize
      : Math.max(1, Math.ceil(remainingMatches / observedYield))
    return {
      page: 1,
      pageSize: Math.min(
        maxCandidateBatchSize,
        Math.max(current.pageSize + 1, current.pageSize + estimatedAdditionalCandidates)
      )
    }
  }
  const nextPage = current.page + 1
  if ((nextPage - 1) * maxCandidateBatchSize >= maxCandidateRows) return undefined
  return { page: nextPage, pageSize: maxCandidateBatchSize }
}

function accountRuntimeStatusFreshCandidatePage(
  page: AccountManagementListPage,
  seenCandidateIds: Set<string>
): AccountManagementListPage {
  const items = page.items.filter((item) => {
    if (seenCandidateIds.has(item.id)) return false
    seenCandidateIds.add(item.id)
    return true
  })
  return { ...page, items }
}

async function hydrateAccountRuntimeStatusCandidatePage(
  access: AccessScope | undefined,
  page: AccountManagementListPage
): Promise<AccountManagementListResult> {
  const items: AccountListItem[] = []
  let generatedAt = new Date().toISOString()
  for (const candidateItems of chunkValues(page.items, maxRuntimeStatusHydrationBatchSize)) {
    const hydrated = await hydrateAccountListPage(access, { ...page, items: candidateItems })
    items.push(...hydrated.items)
    generatedAt = hydrated.generatedAt
  }
  return { ...page, items, generatedAt }
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
