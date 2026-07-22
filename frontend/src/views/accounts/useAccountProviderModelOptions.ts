import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { PageDataLoadResult } from '@/shared/pageDataCache'
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
  selectedIds?: string[]
  includeCapabilities?: boolean
}

export async function loadAccountProviderModelOptionsResource(
  options: ProviderAccountModelResourceOptions
): Promise<PageDataLoadResult<AccountModelSelectOption[]>> {
  const code = options.providerCode.trim()
  if (!code) return { data: [], source: 'network', confirmed: false, cached: false, superseded: false }
  const scopeParams = options.scopeParams ? { ...options.scopeParams } : undefined
  const selectedIds = normalizedSelectedModelIds(options.selectedIds)
  const route = '/providers/models/options'
  const scope = buildProviderModelViewerScope(options.isManagementView, scopeParams?.systemAccountId)
  return providerModelResourceCache.load<AccountModelSelectOption[]>({
    cacheKey: {
      scope,
      route,
      query: {
        providerCode: code,
        systemAccountId: scopeParams?.systemAccountId,
        selectedIds,
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
      return loadAccountModelOptions(code, scopeParams, selectedIds, undefined, options.includeCapabilities === true)
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
  const route = '/providers/models/options'
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

  async function loadProviderModelOptions(providerCode: string, request: { keyword?: string; selectedIds?: string[] } = {}): Promise<void> {
    const code = providerCode.trim()
    if (!code) {
      resetProviderModelOptions()
      return
    }
    const selectedIds = normalizedSelectedModelIds(request.selectedIds)
    const keyword = request.keyword?.trim() || undefined
    const cacheKey = providerModelCacheKey(code, keyword, selectedIds)
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
        const route = '/providers/models/options'
        const result = await providerModelResourceCache.load<AccountModelSelectOption[]>({
          cacheKey: {
            scope: providerModelViewerScope(),
            route,
            query: {
              providerCode: code,
              systemAccountId: scopeParams?.systemAccountId,
              selectedIds,
              keyword,
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
            return loadAccountModelOptions(code, scopeParams, selectedIds, keyword, false)
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

  function providerModelCacheKey(providerCode: string, keyword?: string, selectedIds: string[] = []): string {
    return `${providerModelViewerScope()}:${providerCode}:${keyword ?? ''}:${selectedIds.join(',')}:${providerCacheGlobalGeneration}:${providerCacheGenerations.get(providerCode) ?? 0}`
  }

  function providerModelViewerScope(): string {
    return buildProviderModelViewerScope(options.isManagementView.value, options.modelScopeParams.value?.systemAccountId)
  }

  function isCurrentRequest(requestId: number, cacheKey: string): boolean {
    const currentProviderCode = options.currentProviderCode().trim()
    return requestId === latestRequestId
      && Boolean(currentProviderCode)
      && cacheKey.startsWith(`${providerModelViewerScope()}:${currentProviderCode}:`)
  }

  async function loadSelectedModelCapabilities(providerCode: string, modelIds: string[]): Promise<void> {
    const code = providerCode.trim()
    const selectedIds = normalizedSelectedModelIds(modelIds)
    if (!code || !selectedIds.length) return
    const scopeParams = options.modelScopeParams.value ? { ...options.modelScopeParams.value } : undefined
    const capabilities = await mapWithConcurrency(selectedIds, 4, (modelId) => (
      api.providers.modelCapabilities(code, modelId, scopeParams)
    ))
    const capabilitiesById = new Map(capabilities.map((item) => [item.id, item]))
    const existingById = new Map(providerModelOptions.value.map((item) => [item.value, item]))
    for (const modelId of selectedIds) {
      const capability = capabilitiesById.get(modelId)
      if (!capability) continue
      existingById.set(modelId, {
        label: capability.name,
        value: capability.id,
        supportedApiProtocols: capability.supportedApiProtocols,
        supportedServiceTiers: capability.supportedServiceTiers,
        supportedReasoningEfforts: capability.supportedReasoningEfforts,
        defaultReasoningEffort: capability.defaultReasoningEffort
      })
    }
    providerModelOptions.value = [...existingById.values()]
  }

  return {
    loadProviderModelOptions,
    loadSelectedModelCapabilities,
    providerModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  }
}

async function mapWithConcurrency<TInput, TOutput>(
  input: TInput[],
  concurrency: number,
  mapper: (item: TInput) => Promise<TOutput>
): Promise<TOutput[]> {
  const output = new Array<TOutput>(input.length)
  let nextIndex = 0
  await Promise.all(Array.from({ length: Math.min(concurrency, input.length) }, async () => {
    while (nextIndex < input.length) {
      const index = nextIndex
      nextIndex += 1
      output[index] = await mapper(input[index])
    }
  }))
  return output
}

async function loadAccountModelOptions(
  providerCode: string,
  scopeParams: AccountScopeParams | undefined,
  selectedIds: string[],
  keyword?: string,
  includeCapabilities = false
): Promise<AccountModelSelectOption[]> {
  const [models, capabilities] = await Promise.all([
    api.providers.modelOptions({
      ...scopeParams,
      providerCode,
      limit: 50,
      ...(keyword ? { keyword } : {}),
      ...(selectedIds.length ? { selectedIds } : {})
    }),
    includeCapabilities
      ? mapWithConcurrency(selectedIds, 4, (modelId) => api.providers.modelCapabilities(providerCode, modelId, scopeParams))
      : Promise.resolve([])
  ])
  const capabilitiesById = new Map(capabilities.map((item) => [item.id, item]))
  return dedupeAccountModelOptions(models.map((item) => {
    const capability = capabilitiesById.get(item.id)
    return {
      label: item.name,
      value: item.id,
      supportedApiProtocols: capability?.supportedApiProtocols,
      supportedServiceTiers: capability?.supportedServiceTiers,
      supportedReasoningEfforts: capability?.supportedReasoningEfforts,
      defaultReasoningEffort: capability?.defaultReasoningEffort
    }
  }))
}

function normalizedSelectedModelIds(values: string[] | undefined): string[] {
  return [...new Set((values ?? []).map((value) => value.trim()).filter(Boolean))].slice(0, 50)
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
    if (!model || seen.has(model)) continue
    seen.add(model)
    output.push({
      label: option.label.trim() || model,
      value: model,
      ...(option.supportedApiProtocols?.length ? { supportedApiProtocols: [...option.supportedApiProtocols] } : {}),
      ...(option.supportedServiceTiers?.length ? { supportedServiceTiers: [...option.supportedServiceTiers] } : {}),
      ...(option.supportedReasoningEfforts?.length ? { supportedReasoningEfforts: [...option.supportedReasoningEfforts] } : {}),
      ...(option.defaultReasoningEffort ? { defaultReasoningEffort: option.defaultReasoningEffort } : {})
    })
  }
  return output
}
