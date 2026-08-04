import {
  accountFilterStatuses,
  accountMatchesStatusFilters
} from '../../domain/account-status-classification.js'
import type { AccountListItem } from '../../domain/types.js'
import { runtimeConfig } from '../../config/runtime.js'
import type { AccessScope } from '../../storage/access-scope.js'
import { accountStatusFilterValues, normalizeAccountListOptions, type AccountListOptions } from '../../storage/account-list-options.js'
import { loadAccountTagsByAccountIdsAsync } from '../../storage/account-tags.repository.js'
import {
  listAccountManagementCandidatePrefixAsync,
  maxAccountManagementCandidatePrefixSize,
  type AccountManagementListPage,
  type AccountManagementListResult
} from '../../storage/account-management-list.repository.js'
import {
  chunkValues,
  pagedTotalUpperBound
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
}

export class AccountRuntimeStatusFilterScanLimitError extends Error {}

const initialSparseCandidatePrefixSize = 200
const maxRuntimeStatusHydrationBatchSize = runtimeConfig.background.accountRuntimeStatusHydrationBatchSize

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
  const requestedPage = accountRuntimeStatusRequestedPage(options.page)
  const skipTarget = (requestedPage - 1) * pageSize
  const requiredMatchCount = skipTarget + pageSize + 1
  const candidateSourceOptions = accountRuntimeStatusCandidateSourceOptions(listOptions)
  const output: AccountListItem[] = []
  const seenCandidateIds = new Set<string>()
  let matchedCount = 0
  let exhausted = false
  let generatedAt = new Date().toISOString()
  let candidateWindow = initialAccountRuntimeStatusCandidateWindow(listOptions)

  while (output.length <= pageSize && !exhausted && candidateWindow) {
    const basePage = await listAccountManagementCandidatePrefixAsync(access, {
      ...listOptions,
      ...candidateSourceOptions,
      page: 1
    }, candidateWindow.pageSize)
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
      latestMatchedCount
    })
    if (!nextWindow) {
      throw new AccountRuntimeStatusFilterScanLimitError(
        `运行态筛选单次最多检查 ${maxAccountManagementCandidatePrefixSize} 个账户，请缩小筛选范围`
      )
    }
    candidateWindow = nextWindow
  }

  const hasMore = output.length > pageSize
  const items = output.slice(0, pageSize)
  const tagsByAccount = await loadAccountTagsByAccountIdsAsync(items.map((item) => item.id))
  return {
    items: items.map((item) => ({
      ...item,
      tags: tagsByAccount.get(item.id) ?? []
    })),
    total: pagedTotalUpperBound(requestedPage, pageSize, items.length, hasMore),
    hasMore,
    page: requestedPage,
    pageSize,
    generatedAt
  }
}

export function accountRuntimeStatusCandidateSourceOptions(
  _options: AccountListOptions
): Pick<AccountListOptions, 'status' | 'schedulable'> {
  // Runtime, authorization and expiry facts can override every persisted status.
  // Candidate SQL must stay conservative; the adaptive window keeps dense filters cheap.
  return {
    status: undefined,
    schedulable: 'all'
  }
}

export function initialAccountRuntimeStatusCandidateWindow(
  options: AccountListOptions
): AccountRuntimeStatusCandidateWindow {
  const normalized = normalizeAccountListOptions(options)
  const requestedPage = accountRuntimeStatusRequestedPage(options.page)
  const requestedMatchEnd = requestedPage * normalized.pageSize
  if (!Number.isSafeInteger(requestedMatchEnd) || requestedMatchEnd > maxAccountManagementCandidatePrefixSize) {
    throw new AccountRuntimeStatusFilterScanLimitError(
      `运行态筛选单次最多检查 ${maxAccountManagementCandidatePrefixSize} 个账户，请缩小筛选范围`
    )
  }
  const requiredMatchCount = requestedMatchEnd + 1
  return {
    page: 1,
    pageSize: Math.min(initialSparseCandidatePrefixSize, Math.max(1, requiredMatchCount))
  }
}

export function nextAccountRuntimeStatusCandidateWindow(
  current: AccountRuntimeStatusCandidateWindow,
  progress: AccountRuntimeStatusCandidateProgress
): AccountRuntimeStatusCandidateWindow | undefined {
  if (progress.totalMatchedCount >= progress.requiredMatchCount) return undefined
  const remainingMatches = Math.max(1, progress.requiredMatchCount - progress.totalMatchedCount)
  const observedYield = progress.latestCandidateCount > 0
    ? progress.latestMatchedCount / progress.latestCandidateCount
    : 0
  const estimatedAdditionalCandidates = observedYield > 0
    ? Math.max(1, Math.ceil(remainingMatches / observedYield))
    : current.pageSize < initialSparseCandidatePrefixSize
      ? initialSparseCandidatePrefixSize - current.pageSize
      : current.pageSize
  const desiredPrefixSize = current.pageSize + estimatedAdditionalCandidates
  const maximumGrowthSize = current.pageSize < initialSparseCandidatePrefixSize
    ? initialSparseCandidatePrefixSize
    : Math.min(maxAccountManagementCandidatePrefixSize, current.pageSize * 2)
  const nextPrefixSize = Math.min(maxAccountManagementCandidatePrefixSize, maximumGrowthSize, desiredPrefixSize)
  if (nextPrefixSize <= current.pageSize) return undefined
  return {
    page: 1,
    pageSize: Math.max(current.pageSize + 1, nextPrefixSize)
  }
}

function accountRuntimeStatusRequestedPage(value: unknown): number {
  if (typeof value === 'number' && Number.isInteger(value) && !Number.isSafeInteger(value)) {
    throw new AccountRuntimeStatusFilterScanLimitError('运行态筛选页码超出安全整数范围')
  }
  return typeof value === 'number' && Number.isSafeInteger(value)
    ? Math.max(1, value)
    : 1
}

function accountRuntimeStatusFreshCandidatePage(
  page: AccountManagementListPage,
  seenCandidateIds: Set<string>
): AccountManagementListPage {
  const statusSeedsById = new Map(page.statusSeeds.map((seed) => [seed.id, seed]))
  const items = [] as AccountManagementListPage['items']
  const statusSeeds = [] as AccountManagementListPage['statusSeeds']
  for (const item of page.items) {
    if (seenCandidateIds.has(item.id)) continue
    seenCandidateIds.add(item.id)
    items.push(item)
    const statusSeed = statusSeedsById.get(item.id)
    if (statusSeed) statusSeeds.push(statusSeed)
  }
  return { ...page, items, statusSeeds }
}

async function hydrateAccountRuntimeStatusCandidatePage(
  access: AccessScope | undefined,
  page: AccountManagementListPage
): Promise<AccountManagementListResult> {
  const items: AccountListItem[] = []
  let generatedAt = new Date().toISOString()
  for (const candidateItems of chunkValues(page.items, maxRuntimeStatusHydrationBatchSize)) {
    const candidateIds = new Set(candidateItems.map((item) => item.id))
    const hydrated = await hydrateAccountListPage(access, {
      ...page,
      items: candidateItems,
      statusSeeds: page.statusSeeds.filter((seed) => candidateIds.has(seed.id))
    })
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
