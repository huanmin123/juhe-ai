import { api } from '@/api/client'
import type { AccountTestModelCapabilities, AccountTestOptions } from '@/api/domains/accounts'
import type { AccountTestOptionsParams, RequestControlOptions } from '@/api/contracts'
import { authState } from '@/composables/useAuth'
import { createShortLivedRequestCache } from '@/shared/shortLivedRequestCache'
import type { AccountSummary } from '@/types/domain'
import { accountOperationScopeParams, type AccountScopeParams } from './accountOperationScope'

interface AccountTestOptionsLoadInput {
  account: AccountSummary
  isManagementView: boolean
  options?: RequestControlOptions
  params?: AccountTestOptionsParams
  scopeParams?: AccountScopeParams
}

interface AccountTestModelCapabilitiesLoadInput extends AccountTestOptionsLoadInput {
  modelId: string
}

const accountTestOptionsCache = createShortLivedRequestCache<AccountTestOptions>({
  maxEntries: 100,
  ttlMs: 5 * 60_000
})
const accountTestModelCapabilitiesCache = createShortLivedRequestCache<AccountTestModelCapabilities>({
  maxEntries: 200,
  ttlMs: 5 * 60_000
})
let cacheGeneration = 0

export function invalidateAccountTestOptionsCache(): void {
  cacheGeneration += 1
  accountTestOptionsCache.clear()
  accountTestModelCapabilitiesCache.clear()
}

export async function loadAccountTestOptionsCached(input: AccountTestOptionsLoadInput): Promise<AccountTestOptions> {
  const scopeParams = accountOperationScopeParams(input.account, input.scopeParams)
  const params = { ...scopeParams, ...input.params }
  const loader = () => input.isManagementView
    ? api.accounts.testOptions(input.account.id, params, input.options)
    : api.myAccounts.testOptions(input.account.id, input.params, input.options)
  const configRevision = normalizedConfigRevision(input.account.configRevision)
  if (configRevision === undefined) return await loadAbortable(loader, input.options?.signal)

  const cacheKey = [
    resolveAccountTestOptionsCacheScope(input, scopeParams, configRevision),
    input.params?.keyword?.trim() ?? '',
    input.params?.limit ?? '',
    [...(input.params?.selectedIds ?? [])].sort().join(',')
  ].join(':')
  return await loadAbortable(
    () => accountTestOptionsCache.load(cacheKey, loader),
    input.options?.signal
  )
}

export async function loadAccountTestModelCapabilitiesCached(
  input: AccountTestModelCapabilitiesLoadInput
): Promise<AccountTestModelCapabilities> {
  const scopeParams = accountOperationScopeParams(input.account, input.scopeParams)
  const loader = () => input.isManagementView
    ? api.accounts.testModelCapabilities(input.account.id, input.modelId, scopeParams, input.options)
    : api.myAccounts.testModelCapabilities(input.account.id, input.modelId, input.options)
  const configRevision = normalizedConfigRevision(input.account.configRevision)
  if (configRevision === undefined) return await loadAbortable(loader, input.options?.signal)

  const cacheKey = [
    resolveAccountTestOptionsCacheScope(input, scopeParams, configRevision),
    'capabilities',
    input.modelId.trim()
  ].join(':')
  return await loadAbortable(
    () => accountTestModelCapabilitiesCache.load(cacheKey, loader),
    input.options?.signal
  )
}

async function loadAbortable<T>(load: () => Promise<T>, signal?: AbortSignal): Promise<T> {
  try {
    const result = await load()
    if (signal?.aborted) throw abortError()
    return result
  } catch (error) {
    if (signal?.aborted) throw abortError()
    throw error
  }
}

function abortError(): DOMException {
  return new DOMException('请求已取消', 'AbortError')
}

function normalizedConfigRevision(value: number | undefined): number | undefined {
  return Number.isInteger(value) && Number(value) >= 1 ? Number(value) : undefined
}

function resolveAccountTestOptionsCacheScope(
  input: AccountTestOptionsLoadInput,
  scopeParams: AccountScopeParams,
  configRevision: number
): string {
  const viewer = authState.currentUser.value
  const viewScope = input.isManagementView
    ? `management:${scopeParams?.systemAccountId ?? 'all'}`
    : 'self'
  return [
    cacheGeneration,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    viewScope,
    input.account.id,
    configRevision
  ].join(':')
}
