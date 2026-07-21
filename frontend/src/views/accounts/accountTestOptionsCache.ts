import { api, pageDataApi } from '@/api/client'
import type { AccountTestModelCapabilities, AccountTestOptions } from '@/api/domains/accounts'
import type { AccountTestOptionsParams, RequestControlOptions } from '@/api/contracts'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
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

const accountTestOptionsCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))
let cacheGeneration = 0

export function invalidateAccountTestOptionsCache(): void {
  cacheGeneration += 1
  void accountTestOptionsCache.invalidate('accounts.options')
}

export async function loadAccountTestOptionsCached(input: AccountTestOptionsLoadInput): Promise<AccountTestOptions> {
  const scopeParams = accountOperationScopeParams(input.account, input.scopeParams)
  const params = { ...scopeParams, ...input.params }
  const loader = () => input.isManagementView
    ? api.accounts.testOptions(input.account.id, params, input.options)
    : api.myAccounts.testOptions(input.account.id, input.params, input.options)
  const configRevision = input.account.configRevision
  if (!Number.isInteger(configRevision) || Number(configRevision) < 1) {
    return await loadAbortable(loader, input.options?.signal)
  }
  const viewScope = input.isManagementView ? 'admin' as const : 'self' as const
  const route = input.isManagementView
    ? `/accounts/${input.account.id}/test-options`
    : `/my-accounts/${input.account.id}/test-options`
  return await loadCachedAbortable(route, input.options?.signal, async () => {
    const result = await accountTestOptionsCache.load<AccountTestOptions>({
      cacheKey: {
        scope: resolveAccountTestOptionsCacheKey(input, scopeParams, Number(configRevision)),
        route,
        query: {
          accountId: input.account.id,
          configRevision: Number(configRevision),
          keyword: input.params?.keyword,
          limit: input.params?.limit,
          selectedIds: input.params?.selectedIds,
          systemAccountId: scopeParams?.systemAccountId
        },
        version: `${cacheGeneration}:1`
      },
      domain: 'accounts.options',
      viewScope,
      ...(viewScope === 'admin' && scopeParams?.systemAccountId
        ? { targetSystemAccountId: scopeParams.systemAccountId }
        : {}),
      loadNetwork: loader
    })
    return result.data
  })
}

export async function loadAccountTestModelCapabilitiesCached(
  input: AccountTestModelCapabilitiesLoadInput
): Promise<AccountTestModelCapabilities> {
  const scopeParams = accountOperationScopeParams(input.account, input.scopeParams)
  const loader = () => input.isManagementView
    ? api.accounts.testModelCapabilities(input.account.id, input.modelId, scopeParams, input.options)
    : api.myAccounts.testModelCapabilities(input.account.id, input.modelId, input.options)
  const configRevision = input.account.configRevision
  if (!Number.isInteger(configRevision) || Number(configRevision) < 1) {
    return await loadAbortable(loader, input.options?.signal)
  }
  const viewScope = input.isManagementView ? 'admin' as const : 'self' as const
  const route = input.isManagementView
    ? `/accounts/${input.account.id}/test-options/models/${encodeURIComponent(input.modelId)}`
    : `/my-accounts/${input.account.id}/test-options/models/${encodeURIComponent(input.modelId)}`
  return await loadCachedAbortable(route, input.options?.signal, async () => {
    const result = await accountTestOptionsCache.load<AccountTestModelCapabilities>({
      cacheKey: {
        scope: `${resolveAccountTestOptionsCacheKey(input, scopeParams, Number(configRevision))}:${input.modelId}`,
        route,
        query: {
          accountId: input.account.id,
          configRevision: Number(configRevision),
          modelId: input.modelId,
          systemAccountId: scopeParams?.systemAccountId
        },
        version: `${cacheGeneration}:1`
      },
      domain: 'accounts.options',
      viewScope,
      ...(viewScope === 'admin' && scopeParams?.systemAccountId
        ? { targetSystemAccountId: scopeParams.systemAccountId }
        : {}),
      loadNetwork: loader
    })
    return result.data
  })
}

async function loadCachedAbortable<T>(
  route: string,
  signal: AbortSignal | undefined,
  load: () => Promise<T>
): Promise<T> {
  try {
    const result = await load()
    if (signal?.aborted) {
      await accountTestOptionsCache.invalidate('accounts.options', undefined, route)
      throw abortError()
    }
    return result
  } catch (error) {
    if (!signal?.aborted) throw error
    await accountTestOptionsCache.invalidate('accounts.options', undefined, route)
    throw abortError()
  }
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

function resolveAccountTestOptionsCacheKey(
  input: AccountTestOptionsLoadInput,
  scopeParams: AccountScopeParams,
  configRevision: number
): string {
  const userId = authState.currentUser.value?.id ?? 'anonymous'
  const viewScope = input.isManagementView
    ? `management:${scopeParams?.systemAccountId ?? 'all'}`
    : 'self'
  const role = authState.currentUser.value?.role ?? 'anonymous'
  return `${userId}:${role}:${viewScope}:${input.account.id}:${configRevision}`
}
