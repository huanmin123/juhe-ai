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

export function useAccountProviderModelOptions(options: UseAccountProviderModelOptionsOptions) {
  const providerModelOptions = ref<AccountModelSelectOption[]>([])
  const mappingTargetModelOptions = ref<AccountModelSelectOption[]>([])
  const providerModelsLoading = ref(false)
  const providerModelOptionsCache = new Map<string, AccountModelSelectOption[]>()
  const mappingTargetModelOptionsCache = new Map<string, AccountModelSelectOption[]>()

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
    const cachedPublic = providerModelOptionsCache.get(cacheKey)
    const cachedMappingTargets = mappingTargetModelOptionsCache.get(cacheKey)
    if (cachedPublic && cachedMappingTargets) {
      providerModelOptions.value = cachedPublic
      mappingTargetModelOptions.value = cachedMappingTargets
      providerModelsLoading.value = false
      return
    }
    providerModelsLoading.value = true
    try {
      const [models, mappingTargetModels] = await Promise.all([
        cachedPublic ? Promise.resolve(undefined) : api.providers.models(code),
        cachedMappingTargets ? Promise.resolve(undefined) : api.providers.models(code, { includeMappingTargets: true })
      ])
      const modelOptions = cachedPublic ?? providerModelsToOptions(models ?? [])
      const mappingTargetOptions = cachedMappingTargets ?? providerModelsToOptions(mappingTargetModels ?? [])
      providerModelOptionsCache.set(cacheKey, modelOptions)
      mappingTargetModelOptionsCache.set(cacheKey, mappingTargetOptions)
      if (options.currentProviderCode() === code) {
        providerModelOptions.value = modelOptions
        mappingTargetModelOptions.value = mappingTargetOptions
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
