import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { providerModelsToOptions, type AccountModelSelectOption } from './accountEditFormPayload'
import type { AccountScopeParams } from './accountOperationScope'

interface UseAccountProviderModelOptionsOptions {
  createScopeParams: ComputedRef<AccountScopeParams>
  currentProviderCode: () => string
  extractApiErrorMessage: (error: unknown, fallback: string) => string
  isManagementView: ComputedRef<boolean>
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
  const mappingTargetModelOptions = ref<AccountModelSelectOption[]>([])
  const providerModelsLoading = ref(false)

  function resetProviderModelOptions(): void {
    providerModelOptions.value = []
    mappingTargetModelOptions.value = []
    providerModelsLoading.value = false
  }

  async function loadProviderModelOptions(providerCode: string): Promise<void> {
    const code = providerCode.trim()
    providerModelOptions.value = []
    mappingTargetModelOptions.value = []
    if (!code) return
    const cacheKey = providerModelCacheKey(code)
    const cached = providerModelOptionsCache.get(cacheKey)
    if (cached) {
      providerModelOptions.value = cached
      mappingTargetModelOptions.value = cached
      providerModelsLoading.value = false
      return
    }
    providerModelsLoading.value = true
    try {
      const models = await api.providers.models(code)
      const modelOptions = providerModelsToOptions(models)
      providerModelOptionsCache.set(cacheKey, modelOptions)
      if (options.currentProviderCode() === code) {
        providerModelOptions.value = modelOptions
        mappingTargetModelOptions.value = modelOptions
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
    return `${providerCode}:${options.createScopeParams.value?.systemAccountId ?? 'self'}:${options.isManagementView.value ? 'management' : 'self'}`
  }

  return {
    loadProviderModelOptions,
    mappingTargetModelOptions,
    providerModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  }
}
