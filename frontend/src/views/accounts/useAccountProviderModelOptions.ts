import { message } from '@/lib/antd'
import { ref, type ComputedRef } from 'vue'

import { api } from '@/api/client'
import { isHybridProviderCode } from '@/shared/providerProtocol'
import type { AccountModelSelectOption } from './accountEditFormPayload'
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
        ? await api.providers.modelOptions(options.createScopeParams.value)
        : await api.providers.models(code)
      const modelOptions = dedupeModelOptions(models.map((item) => ({
        label: item.model,
        value: item.model
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
    return `${providerCode}:${options.createScopeParams.value?.systemAccountId ?? 'self'}:${options.isManagementView.value ? 'management' : 'self'}`
  }

  function dedupeModelOptions(options: AccountModelSelectOption[]): AccountModelSelectOption[] {
    const seen = new Set<string>()
    const output: AccountModelSelectOption[] = []
    for (const option of options) {
      const model = option.value.trim()
      if (!model) continue
      if (seen.has(model)) continue
      seen.add(model)
      output.push({ label: model, value: model })
    }
    return output
  }

  return {
    loadProviderModelOptions,
    providerModelOptions,
    providerModelsLoading,
    resetProviderModelOptions
  }
}
