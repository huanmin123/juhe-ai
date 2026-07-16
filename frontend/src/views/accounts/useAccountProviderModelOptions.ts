import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import type { AccountModelSelectOption } from './accountEditFormPayload'
import type { AccountScopeParams } from './accountOperationScope'

interface UseAccountProviderModelOptionsOptions {
  currentProviderCode: () => string
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
  modelScopeParams: ComputedRef<AccountScopeParams>
}

const providerModelOptionsCache = new Map<string, AccountModelSelectOption[]>()

export function invalidateAccountProviderModelOptionsCache(providerCode?: string): void {
  const code = providerCode?.trim()
  if (!code) {
    providerModelOptionsCache.clear()
    return
  }
  for (const key of providerModelOptionsCache.keys()) {
    if (key.startsWith(`${code}:`)) {
      providerModelOptionsCache.delete(key)
    }
  }
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
    const cached = providerModelOptionsCache.get(cacheKey)
    if (cached) {
      providerModelOptions.value = cached
      providerModelsLoading.value = false
      return
    }

    const scopeParams = options.modelScopeParams.value
      ? { ...options.modelScopeParams.value }
      : undefined
    providerModelsLoading.value = true
    const promise = (async () => {
      try {
        const models = isHybridProviderCode(code)
          ? await api.providers.modelOptions(scopeParams)
          : await api.providers.models(code, scopeParams)
        const modelOptions = dedupeModelOptions(models.map((item) => ({
          label: item.model,
          value: item.model,
          supportedApiProtocols: item.supportedApiProtocols,
          supportedServiceTiers: item.supportedServiceTiers,
          supportedReasoningEfforts: item.supportedReasoningEfforts,
          defaultReasoningEffort: item.defaultReasoningEffort
        })))
        providerModelOptionsCache.set(cacheKey, modelOptions)
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

  function providerModelCacheKey(providerCode: string): string {
    return `${providerCode}:${options.modelScopeParams.value?.systemAccountId ?? 'self'}:${options.isManagementView.value ? 'management' : 'self'}`
  }

  function isCurrentRequest(requestId: number, cacheKey: string): boolean {
    const currentProviderCode = options.currentProviderCode().trim()
    return requestId === latestRequestId
      && Boolean(currentProviderCode)
      && providerModelCacheKey(currentProviderCode) === cacheKey
  }

  function dedupeModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
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

  return {
    loadProviderModelOptions,
    providerModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  }
}
