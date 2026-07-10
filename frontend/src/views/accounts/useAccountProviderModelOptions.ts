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

  function resetProviderModelOptions(): void {
    providerModelOptions.value = []
    providerModelsLoading.value = false
  }

  async function loadProviderModelOptions(providerCode: string): Promise<void> {
    const code = providerCode.trim()
    providerModelOptions.value = []
    if (!code) return
    const cacheKey = providerModelCacheKey(code)
    const cached = providerModelOptionsCache.get(cacheKey)
    if (cached) {
      providerModelOptions.value = cached
      providerModelsLoading.value = false
      return
    }
    providerModelsLoading.value = true
    try {
      const models = isHybridProviderCode(code)
        ? await api.providers.modelOptions(options.modelScopeParams.value)
        : await api.providers.models(code, options.modelScopeParams.value)
      const modelOptions = dedupeModelOptions(models.map((item) => ({
        label: item.model,
        value: item.model,
        supportedApiProtocols: item.supportedApiProtocols,
        supportedServiceTiers: item.supportedServiceTiers,
        supportedReasoningEfforts: item.supportedReasoningEfforts,
        defaultReasoningEffort: item.defaultReasoningEffort
      })))
      providerModelOptionsCache.set(cacheKey, modelOptions)
      if (options.currentProviderCode() === code) {
        providerModelOptions.value = modelOptions
      }
    } catch (error) {
      console.error(error)
      message.error(options.extractApiErrorMessage(error, '加载供应商模型失败'))
    } finally {
      if (options.currentProviderCode() === code) {
        providerModelsLoading.value = false
      }
    }
  }

  function providerModelCacheKey(providerCode: string): string {
    return `${providerCode}:${options.modelScopeParams.value?.systemAccountId ?? 'self'}:${options.isManagementView.value ? 'management' : 'self'}`
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
