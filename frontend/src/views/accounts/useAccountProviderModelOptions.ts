import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { PageDataLoadResult } from '@/shared/pageDataCache'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import type { AccountScopeParams } from './accountOperationScope'
import { invalidateAccountTestOptionsCache } from './accountTestOptionsCache'

interface UseAccountProviderModelOptionsOptions {
  currentProviderCode: () => string
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  modelScopeParams: ComputedRef<AccountScopeParams>
}

const providerModelResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))
const providerCacheGenerations = new Map<string, number>()
let providerCacheGlobalGeneration = 0

interface ProviderAccountModelResourceOptions {
  isManagementView: boolean
  providerCode: string
  scopeParams?: AccountScopeParams
}

export async function loadAccountProviderModelOptionsResource(
  options: ProviderAccountModelResourceOptions
): Promise<PageDataLoadResult<AccountModelSelectOption[]>> {
  const code = options.providerCode.trim()
  if (!code) return { data: [], source: 'network', confirmed: false, cached: false, superseded: false }
  const scopeParams = options.scopeParams ? { ...options.scopeParams } : undefined
  const route = isHybridProviderCode(code) ? '/providers/models/options' : `/providers/${code}/models`
  const scope = buildProviderModelViewerScope(options.isManagementView, scopeParams?.systemAccountId)
  return providerModelResourceCache.load<AccountModelSelectOption[]>({
    cacheKey: {
      scope,
      route,
      query: {
        providerCode: code,
        systemAccountId: scopeParams?.systemAccountId,
        view: options.isManagementView ? 'management' : 'self'
      },
      version: `${providerCacheGlobalGeneration}:${providerCacheGenerations.get(code) ?? 0}:1`
    },
    domain: 'providers.catalog',
    viewScope: options.isManagementView ? 'admin' : 'self',
    ...(options.isManagementView && scopeParams?.systemAccountId
      ? { targetSystemAccountId: scopeParams.systemAccountId }
      : {}),
    loadNetwork: async () => {
      const models = isHybridProviderCode(code)
        ? await api.providers.modelOptions(scopeParams)
        : await api.providers.models(code, scopeParams)
      return dedupeAccountModelOptions(models.map((item) => ({
        label: item.model,
        value: item.model,
        supportedApiProtocols: item.supportedApiProtocols,
        supportedServiceTiers: item.supportedServiceTiers,
        supportedReasoningEfforts: item.supportedReasoningEfforts,
        defaultReasoningEffort: item.defaultReasoningEffort
      })))
    }
  })
}

export function invalidateAccountProviderModelOptionsCache(providerCode?: string): void {
  invalidateAccountTestOptionsCache()
  const code = providerCode?.trim()
  if (!code) {
    providerCacheGlobalGeneration += 1
    providerCacheGenerations.clear()
    void providerModelResourceCache.invalidate('providers.catalog')
    return
  }
  providerCacheGenerations.set(code, (providerCacheGenerations.get(code) ?? 0) + 1)
  const route = isHybridProviderCode(code) ? '/providers/models/options' : `/providers/${code}/models`
  void providerModelResourceCache.invalidate('providers.catalog', undefined, route)
}

export function useAccountProviderModelOptions(options: UseAccountProviderModelOptionsOptions) {
  const providerModelOptions = ref<AccountModelSelectOption[]>([])
  const providerModelsLoading = ref(false)
  let latestRequestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined

  function resetProviderModelOptions(): void {
    latestRequestId += 1
    providerModelOptions.value = []
    providerModelsLoading.value = false
    loadingKey = undefined
    loadingPromise = undefined
  }

  async function loadProviderModelOptions(providerCode: string): Promise<void> {
    const code = providerCode.trim()
    if (!code) {
      resetProviderModelOptions()
      return
    }
    const cacheKey = providerModelCacheKey(code)
    if (loadingKey === cacheKey && loadingPromise) return loadingPromise

    const requestId = latestRequestId + 1
    latestRequestId = requestId
    loadingKey = undefined
    loadingPromise = undefined
    providerModelOptions.value = []
    const scopeParams = options.modelScopeParams.value
      ? { ...options.modelScopeParams.value }
      : undefined
    providerModelsLoading.value = true
    const promise = (async () => {
      try {
        const route = isHybridProviderCode(code) ? '/providers/models/options' : `/providers/${code}/models`
        const result = await providerModelResourceCache.load<AccountModelSelectOption[]>({
          cacheKey: {
            scope: providerModelViewerScope(),
            route,
            query: {
              providerCode: code,
              systemAccountId: scopeParams?.systemAccountId,
              view: options.isManagementView.value ? 'management' : 'self'
            },
            version: `${providerCacheGlobalGeneration}:${providerCacheGenerations.get(code) ?? 0}:1`
          },
          domain: 'providers.catalog',
          viewScope: options.isManagementView.value ? 'admin' : 'self',
          ...(options.isManagementView.value && scopeParams?.systemAccountId
            ? { targetSystemAccountId: scopeParams.systemAccountId }
            : {}),
          loadNetwork: async () => {
            const models = isHybridProviderCode(code)
              ? await api.providers.modelOptions(scopeParams)
              : await api.providers.models(code, scopeParams)
            return dedupeModelOptions(models.map((item) => ({
              label: item.model,
              value: item.model,
              supportedApiProtocols: item.supportedApiProtocols,
              supportedServiceTiers: item.supportedServiceTiers,
              supportedReasoningEfforts: item.supportedReasoningEfforts,
              defaultReasoningEffort: item.defaultReasoningEffort
            })))
          }
        })
        const modelOptions = result.data
        if (isCurrentRequest(requestId, cacheKey)) {
          providerModelOptions.value = modelOptions
        }
        void result.confirmation?.then((outcome) => {
          if (outcome.data && isCurrentRequest(requestId, cacheKey)) {
            providerModelOptions.value = outcome.data
          }
        })
      } catch (error) {
        if (!isCurrentRequest(requestId, cacheKey)) return
        console.error(error)
        message.error(options.extractApiErrorMessage(error, '加载供应商模型失败'))
      } finally {
        if (requestId === latestRequestId) {
          providerModelsLoading.value = false
          loadingKey = undefined
          loadingPromise = undefined
        }
      }
    })()
    loadingKey = cacheKey
    loadingPromise = promise
    return promise
  }

  function providerModelCacheKey(providerCode: string): string {
    return `${providerModelViewerScope()}:${providerCode}:${providerCacheGlobalGeneration}:${providerCacheGenerations.get(providerCode) ?? 0}`
  }

  function providerModelViewerScope(): string {
    return buildProviderModelViewerScope(options.isManagementView.value, options.modelScopeParams.value?.systemAccountId)
  }

  function isCurrentRequest(requestId: number, cacheKey: string): boolean {
    const currentProviderCode = options.currentProviderCode().trim()
    return requestId === latestRequestId
      && Boolean(currentProviderCode)
      && providerModelCacheKey(currentProviderCode) === cacheKey
  }

  function dedupeModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
    return dedupeAccountModelOptions(options)
  }

  return {
    loadProviderModelOptions,
    providerModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  }
}

function buildProviderModelViewerScope(isManagementView: boolean, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    systemAccountId ?? 'self'
  ].join(':')
}

function dedupeAccountModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
  const seen = new Set<string>()
  const output: AccountModelSelectOption[] = []
  for (const option of options) {
    const model = option.value.trim()
    if (!model) continue
    if (seen.has(model)) {
      const existing = output.find((item) => item.value === model)
      if (existing) {
        existing.supportedApiProtocols = mergeModelProtocols(existing.supportedApiProtocols, option.supportedApiProtocols)
        existing.supportedServiceTiers = mergeOptionalLists(existing.supportedServiceTiers, option.supportedServiceTiers)
        existing.supportedReasoningEfforts = mergeOptionalLists(existing.supportedReasoningEfforts, option.supportedReasoningEfforts)
        existing.defaultReasoningEffort ??= option.defaultReasoningEffort
      }
      continue
    }
    seen.add(model)
    output.push({
      label: model,
      value: model,
      supportedApiProtocols: mergeModelProtocols(undefined, option.supportedApiProtocols),
      supportedServiceTiers: cloneOptionalList(option.supportedServiceTiers),
      supportedReasoningEfforts: cloneOptionalList(option.supportedReasoningEfforts),
      defaultReasoningEffort: option.defaultReasoningEffort
    })
  }
  return output
}

function cloneOptionalList<TValue>(value: TValue[] | undefined): TValue[] | undefined {
  return value ? [...value] : undefined
}

function mergeOptionalLists<TValue>(left: TValue[] | undefined, right: TValue[] | undefined): TValue[] | undefined {
  const output = [...(left ?? [])]
  const seen = new Set(output)
  for (const value of right ?? []) {
    if (seen.has(value)) continue
    seen.add(value)
    output.push(value)
  }
  return output.length ? output : undefined
}

function mergeModelProtocols(
  left: AccountModelSelectOption['supportedApiProtocols'],
  right: AccountModelSelectOption['supportedApiProtocols']
): AccountModelSelectOption['supportedApiProtocols'] {
  const output = [...(left ?? [])]
  const seen = new Set(output)
  for (const protocol of right ?? []) {
    if (seen.has(protocol)) continue
    seen.add(protocol)
    output.push(protocol)
  }
  return output.length ? output : undefined
}
