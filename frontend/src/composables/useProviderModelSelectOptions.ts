import { computed, ref, type ComputedRef } from 'vue'

import { api, pageDataApi, type ListParams } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { ProviderModelApiProtocol, ProviderModelOption } from '@/types/domain'

export interface ProviderModelSelectOption {
  label: string
  value: string
  providerCodes: string[]
  supportedApiProtocols: ProviderModelApiProtocol[]
}

interface UseProviderModelSelectOptionsOptions {
  scopeParams?: ComputedRef<ListParams | undefined>
  protocol?: 'openai' | 'anthropic' | 'gemini'
  onLoadError?: (error: unknown) => void
}

const providerModelResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export function useProviderModelSelectOptions(options: UseProviderModelSelectOptionsOptions = {}) {
  const providerModelOptions = ref<ProviderModelOption[]>([])
  const loading = ref(false)
  const loadFailed = ref(false)
  let loadedScopeKey: string | undefined
  let latestRequestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined

  const selectOptions = computed<ProviderModelSelectOption[]>(() => {
    const grouped = new Map<string, { model: string; providerCodes: Set<string>; supportedApiProtocols: Set<ProviderModelApiProtocol> }>()
    for (const option of providerModelOptions.value) {
      const model = option.model.trim()
      const providerCode = option.providerCode.trim()
      if (!model || !providerCode) continue
      const key = model
      const existing = grouped.get(key) ?? {
        model,
        providerCodes: new Set<string>(),
        supportedApiProtocols: new Set<ProviderModelApiProtocol>()
      }
      existing.providerCodes.add(providerCode)
      for (const protocol of option.supportedApiProtocols ?? []) {
        existing.supportedApiProtocols.add(protocol)
      }
      grouped.set(key, existing)
    }
    return [...grouped.values()]
      .sort((left, right) => left.model.localeCompare(right.model))
      .map((item) => {
        const providerCodes = [...item.providerCodes].sort()
        return {
          label: `${item.model}（${providerCodes.join('、')}）`,
          value: item.model,
          providerCodes,
          supportedApiProtocols: [...item.supportedApiProtocols].sort()
        }
      })
  })
  const optionValues = computed(() => new Set(selectOptions.value.map((option) => option.value)))

  async function loadModelOptions(force = false): Promise<void> {
    const scopeKey = modelOptionsScopeKey()
    if (!force && loadingKey === scopeKey && loadingPromise) return loadingPromise

    const requestId = latestRequestId + 1
    latestRequestId = requestId
    if (loadedScopeKey !== scopeKey) {
      providerModelOptions.value = []
    }
    loadedScopeKey = undefined
    loading.value = true
    loadFailed.value = false
    const scopeParams = options.scopeParams?.value
      ? { ...options.scopeParams.value }
      : undefined
    const promise = (async () => {
      try {
        if (force) {
          await providerModelResourceCache.invalidate('providers.catalog', providerModelViewerScope(), '/providers/models/options')
        }
        const result = await providerModelResourceCache.load<ProviderModelOption[]>({
          cacheKey: {
            scope: providerModelViewerScope(),
            route: '/providers/models/options',
            query: { ...scopeParams, protocol: options.protocol },
            version: 1
          },
          domain: 'providers.catalog',
          viewScope: scopeParams?.systemAccountId ? 'admin' : 'self',
          ...(scopeParams?.systemAccountId ? { targetSystemAccountId: scopeParams.systemAccountId } : {}),
          loadNetwork: () => api.providers.modelOptions({
            ...scopeParams,
            protocol: options.protocol
          })
        })
        const nextOptions = result.data
        if (isCurrentRequest(requestId, scopeKey)) {
          providerModelOptions.value = nextOptions
          loadedScopeKey = scopeKey
        }
        void result.confirmation?.then((outcome) => {
          if (outcome.data && isCurrentRequest(requestId, scopeKey)) {
            providerModelOptions.value = outcome.data
            loadedScopeKey = scopeKey
          }
        })
      } catch (error) {
        if (!isCurrentRequest(requestId, scopeKey)) return
        console.error(error)
        loadFailed.value = true
        options.onLoadError?.(error)
      } finally {
        if (requestId === latestRequestId) {
          loading.value = false
          loadingKey = undefined
          loadingPromise = undefined
        }
      }
    })()
    loadingKey = scopeKey
    loadingPromise = promise
    return promise
  }

  function resetModelOptions(): void {
    latestRequestId += 1
    providerModelOptions.value = []
    loading.value = false
    loadFailed.value = false
    loadedScopeKey = undefined
    loadingKey = undefined
    loadingPromise = undefined
  }

  function modelOptionsScopeKey(): string {
    return `${providerModelViewerScope()}:${options.protocol ?? 'all'}`
  }

  function providerModelViewerScope(): string {
    const viewer = authState.currentUser.value
    const targetSystemAccountId = options.scopeParams?.value?.systemAccountId
    return [
      targetSystemAccountId ? 'admin' : 'self',
      viewer?.id ?? 'anonymous',
      viewer?.role ?? 'anonymous',
      targetSystemAccountId ?? 'self'
    ].join(':')
  }

  function isCurrentRequest(requestId: number, scopeKey: string): boolean {
    return requestId === latestRequestId && modelOptionsScopeKey() === scopeKey
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
    return optionValues.value.has(model.trim())
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
