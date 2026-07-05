import { api } from '@/api/client'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountTagSummary } from '@/types/domain'
import type { AccountScopeParams } from './accountOperationScope'

interface AccountTagOptionsLoadInput {
  force?: boolean
  isManagementView: boolean
  scopeParams?: AccountScopeParams
}

const accountTagOptionsCache = createShortLivedQueryCache<AccountTagSummary[]>({ ttlMs: 30_000 })
const accountTagOptionsInFlight = new Map<string, Promise<AccountTagSummary[]>>()

export function resolveAccountTagOptionsScopeKey(isManagementView: boolean, scopeParams?: AccountScopeParams): string | undefined {
  if (!isManagementView) return 'self'
  const systemAccountId = scopeParams?.systemAccountId?.trim()
  return systemAccountId ? `management:${systemAccountId}` : undefined
}

export function readAccountTagOptionsCache(scopeKey: string): AccountTagSummary[] | undefined {
  return accountTagOptionsCache.get(scopeKey)
}

export function writeAccountTagOptionsCache(scopeKey: string, options: AccountTagSummary[]): void {
  accountTagOptionsCache.set(scopeKey, [...options])
}

export function invalidateAccountTagOptionsCache(scopeKey?: string): void {
  if (!scopeKey) {
    accountTagOptionsCache.clear()
    accountTagOptionsInFlight.clear()
    return
  }
  accountTagOptionsCache.remove(scopeKey)
  accountTagOptionsInFlight.delete(scopeKey)
}

export async function loadAccountTagOptionsCached(input: AccountTagOptionsLoadInput): Promise<AccountTagSummary[]> {
  const scopeKey = resolveAccountTagOptionsScopeKey(input.isManagementView, input.scopeParams)
  if (!scopeKey) return []

  const pending = accountTagOptionsInFlight.get(scopeKey)
  if (pending) return pending
  if (!input.force) {
    const cached = accountTagOptionsCache.get(scopeKey)
    if (cached) return cached
  }

  let request: Promise<AccountTagSummary[]>
  request = (input.isManagementView
    ? api.accounts.tags(input.scopeParams)
    : api.myAccounts.tags()
  ).then((options) => {
    accountTagOptionsCache.set(scopeKey, options)
    return options
  }).finally(() => {
    if (accountTagOptionsInFlight.get(scopeKey) === request) {
      accountTagOptionsInFlight.delete(scopeKey)
    }
  })
  accountTagOptionsInFlight.set(scopeKey, request)
  return request
}
