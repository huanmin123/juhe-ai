import { computed, ref, type ComputedRef } from 'vue'

import { api, type ListParams } from '@/api/client'
import type { ProviderModelOption } from '@/types/domain'

export interface ProviderModelSelectOption {
  label: string
  value: string
  providerCodes: string[]
}

interface UseProviderModelSelectOptionsOptions {
  scopeParams?: ComputedRef<ListParams | undefined>
  protocol?: 'openai'
  onLoadError?: (error: unknown) => void
}

export function useProviderModelSelectOptions(options: UseProviderModelSelectOptionsOptions = {}) {
  const providerModelOptions = ref<ProviderModelOption[]>([])
  const loading = ref(false)
  const loadFailed = ref(false)
  let loadedScopeKey: string | undefined
  let loadingPromise: Promise<void> | undefined

  const selectOptions = computed<ProviderModelSelectOption[]>(() => {
    const grouped = new Map<string, { model: string; providerCodes: Set<string> }>()
    for (const option of providerModelOptions.value) {
      const model = option.model.trim()
      const providerCode = option.providerCode.trim()
      if (!model || !providerCode) continue
      const key = model.toLowerCase()
      const existing = grouped.get(key) ?? { model, providerCodes: new Set<string>() }
      existing.providerCodes.add(providerCode)
      grouped.set(key, existing)
    }
    return [...grouped.values()]
      .sort((left, right) => left.model.localeCompare(right.model))
      .map((item) => {
        const providerCodes = [...item.providerCodes].sort()
        return {
          label: `${item.model}（${providerCodes.join('、')}）`,
          value: item.model,
          providerCodes
        }
      })
  })
  const optionValues = computed(() => new Set(selectOptions.value.map((option) => option.value.toLowerCase())))

  async function loadModelOptions(force = false): Promise<void> {
    const scopeKey = modelOptionsScopeKey()
    if (!force && loadedScopeKey === scopeKey) return
    if (!force && loadingPromise) return loadingPromise
    loading.value = true
    loadFailed.value = false
    loadingPromise = (async () => {
      try {
        providerModelOptions.value = await api.providers.modelOptions({
          ...options.scopeParams?.value,
          protocol: options.protocol
        })
        loadedScopeKey = scopeKey
      } catch (error) {
        console.error(error)
        loadFailed.value = true
        options.onLoadError?.(error)
      } finally {
        loading.value = false
        loadingPromise = undefined
      }
    })()
    return loadingPromise
  }

  function resetModelOptions(): void {
    providerModelOptions.value = []
    loading.value = false
    loadFailed.value = false
    loadedScopeKey = undefined
    loadingPromise = undefined
  }

  function modelOptionsScopeKey(): string {
    return `${options.scopeParams?.value?.systemAccountId ?? ''}:${options.protocol ?? 'all'}`
  }

  return {
    filterModelOption,
    hasModel,
    loadFailed,
    loading,
    loadModelOptions,
    optionValues,
    providerModelOptions,
    resetModelOptions,
    selectOptions
  }

  function hasModel(model: string): boolean {
    return optionValues.value.has(model.trim().toLowerCase())
  }
}

export function filterModelOption(input: string, option?: ProviderModelSelectOption): boolean {
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  const label = option?.label.toLowerCase() ?? ''
  const value = option?.value.toLowerCase() ?? ''
  const providers = option?.providerCodes.join(' ').toLowerCase() ?? ''
  return label.includes(keyword) || value.includes(keyword) || providers.includes(keyword)
}
