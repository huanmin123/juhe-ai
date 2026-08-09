import {
  accountFilterStatuses,
  accountMatchesStatusFilters
} from '../../domain/account-status-classification.js'
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
import {
  hydrateAccountListPage,
  hydrateAccountRuntimeStatusFilterCandidates
} from './account-status-snapshot.service.js'

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

/**
 * 仅用于当前 HTTP 响应的 Server-Timing；不进入 JSON payload，也不作为缓存或
 * 业务判断依据。将慢请求拆开后，才能区分前缀 SQL、运行态批量水合和最终展示水合。
 */
export interface AccountRuntimeStatusFilterTiming {
  candidateListDurationMs: number
  candidateHydrationDurationMs: number
  candidatePredicateDurationMs: number
  finalHydrationDurationMs: number
  finalTagDurationMs: number
}

export class AccountRuntimeStatusFilterScanLimitError extends Error {}

const initialSparseCandidatePrefixSize = 200
const maxRuntimeStatusHydrationBatchSize = runtimeConfig.background.accountRuntimeStatusHydrationBatchSize
const timingByResult = new WeakMap<AccountManagementListResult, AccountRuntimeStatusFilterTiming>()

export function takeAccountRuntimeStatusFilterTiming(
  result: AccountManagementListResult
): AccountRuntimeStatusFilterTiming | undefined {
  const timing = timingByResult.get(result)
  timingByResult.delete(result)
  return timing
}

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
  const output: AccountManagementListPage['items'] = []
  const outputStatusSeeds = [] as AccountManagementListPage['statusSeeds']
  const seenCandidateIds = new Set<string>()
  let matchedCount = 0
  let exhausted = false
  let generatedAt = new Date().toISOString()
  let candidateWindow = initialAccountRuntimeStatusCandidateWindow(listOptions)
  let candidateListDurationMs = 0
  let candidateHydrationDurationMs = 0
  let candidatePredicateDurationMs = 0
  let finalHydrationDurationMs = 0
  let finalTagDurationMs = 0

  while (output.length <= pageSize && !exhausted && candidateWindow) {
    const candidateListStartedAt = performance.now()
    const basePage = await listAccountManagementCandidatePrefixAsync(access, {
      ...listOptions,
      ...candidateSourceOptions,
      page: 1
    }, candidateWindow.pageSize)
    candidateListDurationMs += performance.now() - candidateListStartedAt
    const freshBasePage = accountRuntimeStatusFreshCandidatePage(basePage, seenCandidateIds)
    const candidateHydrationStartedAt = performance.now()
    const hydratedPage = await hydrateAccountRuntimeStatusCandidatePage(freshBasePage)
    candidateHydrationDurationMs += performance.now() - candidateHydrationStartedAt
    generatedAt = hydratedPage.generatedAt
    const baseItemsById = new Map(freshBasePage.items.map((item) => [item.id, item]))
    const statusSeedsById = new Map(freshBasePage.statusSeeds.map((seed) => [seed.id, seed]))
    let latestMatchedCount = 0
    const candidatePredicateStartedAt = performance.now()
    for (const account of hydratedPage.items) {
      if (!accountMatchesStatusFilters(account, statusFilters)) continue
      if (!accountMatchesSchedulableFilter(account, listOptions.schedulable)) continue
      latestMatchedCount += 1
      if (matchedCount < skipTarget) {
        matchedCount += 1
        continue
      }
      const item = baseItemsById.get(account.id)
      const statusSeed = statusSeedsById.get(account.id)
      if (!item || !statusSeed) {
        throw new Error(`账户 ${account.id} 缺少运行态状态筛选种子`)
      }
      output.push(item)
      outputStatusSeeds.push(statusSeed)
      matchedCount += 1
      if (output.length > pageSize) break
    }
    candidatePredicateDurationMs += performance.now() - candidatePredicateStartedAt
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
  const statusSeeds = outputStatusSeeds.slice(0, pageSize)
  if (items.length === 0) {
    const result: AccountManagementListResult = {
      items: [],
      total: pagedTotalUpperBound(requestedPage, pageSize, 0, hasMore),
      hasMore,
      page: requestedPage,
      pageSize,
      generatedAt
    }
    timingByResult.set(result, accountRuntimeStatusFilterTiming({
      candidateListDurationMs,
      candidateHydrationDurationMs,
      candidatePredicateDurationMs,
      finalHydrationDurationMs,
      finalTagDurationMs
    }))
    return result
  }
  const finalHydrationStartedAt = performance.now()
  const hydratedPage = await hydrateAccountListPage(access, {
    items,
    statusSeeds,
    total: pagedTotalUpperBound(requestedPage, pageSize, items.length, hasMore),
    hasMore,
    page: requestedPage,
    pageSize
  })
  finalHydrationDurationMs += performance.now() - finalHydrationStartedAt
  const finalTagsStartedAt = performance.now()
  const tagsByAccount = await loadAccountTagsByAccountIdsAsync(items.map((item) => item.id))
  finalTagDurationMs += performance.now() - finalTagsStartedAt
  const result: AccountManagementListResult = {
    ...hydratedPage,
    items: hydratedPage.items.map((item) => ({
      ...item,
      tags: tagsByAccount.get(item.id) ?? []
    })),
    generatedAt: hydratedPage.generatedAt
  }
  timingByResult.set(result, accountRuntimeStatusFilterTiming({
    candidateListDurationMs,
    candidateHydrationDurationMs,
    candidatePredicateDurationMs,
    finalHydrationDurationMs,
    finalTagDurationMs
  }))
  return result
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
  page: AccountManagementListPage
): Promise<{
  generatedAt: string
  items: Awaited<ReturnType<typeof hydrateAccountRuntimeStatusFilterCandidates>>['items']
}> {
  const items: Awaited<ReturnType<typeof hydrateAccountRuntimeStatusFilterCandidates>>['items'] = []
  let generatedAt = new Date().toISOString()
  for (const candidateItems of chunkValues(page.items, maxRuntimeStatusHydrationBatchSize)) {
    const candidateIds = new Set(candidateItems.map((item) => item.id))
    const hydrated = await hydrateAccountRuntimeStatusFilterCandidates({
      statusSeeds: page.statusSeeds.filter((seed) => candidateIds.has(seed.id))
    })
    items.push(...hydrated.items)
    generatedAt = hydrated.generatedAt
  }
  return { items, generatedAt }
}

function accountMatchesSchedulableFilter(
  account: Pick<Awaited<ReturnType<typeof hydrateAccountRuntimeStatusFilterCandidates>>['items'][number], 'status' | 'authorizationQuotaExceeded' | 'effectiveAvailability'>,
  filter: ReturnType<typeof normalizeAccountListOptions>['schedulable']
): boolean {
  if (filter === 'all') return true
  const statuses = accountFilterStatuses(account)
  const cooling = statuses.has('rate_limited') || statuses.has('temporary_unavailable')
  if (filter === 'cooling') return cooling
  if (filter === 'enabled') return account.effectiveAvailability.available
  return !account.effectiveAvailability.available && !cooling
}

function accountRuntimeStatusFilterTiming(
  timing: AccountRuntimeStatusFilterTiming
): AccountRuntimeStatusFilterTiming {
  return {
    candidateListDurationMs: Math.max(0, timing.candidateListDurationMs),
    candidateHydrationDurationMs: Math.max(0, timing.candidateHydrationDurationMs),
    candidatePredicateDurationMs: Math.max(0, timing.candidatePredicateDurationMs),
    finalHydrationDurationMs: Math.max(0, timing.finalHydrationDurationMs),
    finalTagDurationMs: Math.max(0, timing.finalTagDurationMs)
  }
}
