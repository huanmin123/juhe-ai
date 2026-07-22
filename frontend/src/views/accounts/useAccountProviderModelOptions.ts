import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import type { AccountScopeParams } from './accountOperationScope'
import { invalidateAccountTestOptionsCache } from './accountTestOptionsCache'

interface UseAccountProviderModelOptionsOptions {
  currentProviderCode: () => string
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  modelScopeParams: ComputedRef<AccountScopeParams>
}

interface ProviderAccountModelResourceOptions {
  isManagementView: boolean
  providerCode: string
  scopeParams?: AccountScopeParams
  selectedIds?: string[]
  includeCapabilities?: boolean
}

export async function loadAccountProviderModelOptionsResource(
  options: ProviderAccountModelResourceOptions
): Promise<{ data: AccountModelSelectOption[]; state: 'ready' }> {
  const code = options.providerCode.trim()
  if (!code) return { data: [], state: 'ready' }
  const scopeParams = options.scopeParams ? { ...options.scopeParams } : undefined
  const selectedIds = normalizedSelectedModelIds(options.selectedIds)
  const data = await loadAccountModelOptions(code, scopeParams, selectedIds, undefined, options.includeCapabilities === true)
  return { data, state: 'ready' }
}

export function invalidateAccountProviderModelOptionsCache(providerCode?: string): void {
  invalidateAccountTestOptionsCache()
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
        const modelOptions = await loadAccountModelOptions(code, scopeParams, selectedIds, keyword, false)
        if (isCurrentRequest(requestId, cacheKey)) {
          providerModelOptions.value = modelOptions
        }
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
    return `${providerModelViewerScope()}:${providerCode}:${keyword ?? ''}:${selectedIds.join(',')}`
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

  function dedupeModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
    return dedupeAccountModelOptions(options)
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
  return [
    isManagementView ? 'admin' : 'self',
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
