import { computed, ref, type ComputedRef } from 'vue'

import { api, pageDataApi, type ListParams } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { ProviderModelOption } from '@/types/domain'

export interface ProviderModelSelectOption {
  label: string
  value: string
  providerCodes: string[]
}

interface UseProviderModelSelectOptionsOptions {
  scopeParams?: ComputedRef<ListParams | undefined>
  providerCode?: ComputedRef<string | undefined>
  providerCodes?: ComputedRef<string[]>
  protocol?: 'openai' | 'anthropic' | 'gemini'
  selectedIds?: ComputedRef<string[]>
  onLoadError?: (error: unknown) => void
}

interface ModelLoadParams {
  force?: boolean
  keyword?: string
  limit?: number
  selectedIds?: string[]
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
    const grouped = new Map<string, { name: string; providerCodes: Set<string> }>()
    for (const option of providerModelOptions.value) {
      const id = option.id.trim()
      const name = option.name.trim()
      if (!id || !name) continue
      const existing = grouped.get(id) ?? { name, providerCodes: new Set<string>() }
      if (option.providerCode?.trim()) existing.providerCodes.add(option.providerCode.trim())
      grouped.set(id, existing)
    }
    return [...grouped.entries()]
      .sort((left, right) => left[1].name.localeCompare(right[1].name, 'en'))
      .map(([value, item]) => {
        const providerCodes = [...item.providerCodes].sort()
        return {
          label: providerCodes.length ? `${item.name}（${providerCodes.join('、')}）` : item.name,
          value,
          providerCodes
        }
      })
  })
  const optionValues = computed(() => new Set(selectOptions.value.map((option) => option.value)))

  async function loadModelOptions(input: ModelLoadParams | boolean = {}): Promise<void> {
    const params = typeof input === 'boolean' ? { force: input } : input
    const contextKey = modelOptionsContextKey()
    const scopeKey = modelOptionsScopeKey(params)
    if (typeof input !== 'boolean' && loadingKey === scopeKey && loadingPromise) return loadingPromise
    const requestId = ++latestRequestId
    if (loadedScopeKey !== scopeKey) providerModelOptions.value = []
    loadedScopeKey = undefined
    loading.value = true
    loadFailed.value = false
    const scopeParams = options.scopeParams?.value ? { ...options.scopeParams.value } : undefined
    const providerCodes = [...new Set([
      ...(options.providerCodes?.value ?? []),
      ...(options.providerCode?.value ? [options.providerCode.value] : [])
    ].map((code) => code.trim()).filter(Boolean))].sort()
    const providerCode = providerCodes.length === 1 ? providerCodes[0] : undefined
    const selectedIds = [...new Set([...(options.selectedIds?.value ?? []), ...(params.selectedIds ?? [])]
      .map((id) => id.trim()).filter(Boolean))].slice(0, 50)
    const query = {
      ...scopeParams,
      ...(providerCode ? { providerCode } : {}),
      ...(options.protocol ? { protocol: options.protocol } : {}),
      ...(params.keyword?.trim() ? { keyword: params.keyword.trim() } : {}),
      limit: Math.min(50, Math.max(1, params.limit ?? 50)),
      ...(selectedIds.length ? { selectedIds } : {})
    }
    const promise = (async () => {
      try {
        if (params.force) {
          await providerModelResourceCache.invalidate('providers.catalog', providerModelViewerScope(), '/providers/models/options')
        }
        const result = await providerModelResourceCache.load<ProviderModelOption[]>({
          cacheKey: {
            scope: providerModelViewerScope(),
            route: '/providers/models/options',
            query: { ...query, providerCodes },
            version: 2
          },
          domain: 'providers.catalog',
          viewScope: scopeParams?.systemAccountId ? 'admin' : 'self',
          ...(scopeParams?.systemAccountId ? { targetSystemAccountId: scopeParams.systemAccountId } : {}),
          loadNetwork: async () => {
            if (providerCodes.length <= 1) return api.providers.modelOptions(query)
            const results = await Promise.all(providerCodes.map((code) => api.providers.modelOptions({
              ...query,
              providerCode: code
            })))
            const byId = new Map<string, ProviderModelOption>()
            for (const item of results.flat()) byId.set(item.id, item)
            return [...byId.values()]
          }
        })
        if (isCurrentRequest(requestId, contextKey)) {
          providerModelOptions.value = result.data
          loadedScopeKey = scopeKey
        }
        void result.confirmation?.then((outcome) => {
          if (outcome.data && isCurrentRequest(requestId, contextKey)) {
            providerModelOptions.value = outcome.data
            loadedScopeKey = scopeKey
          }
        })
      } catch (error) {
        if (!isCurrentRequest(requestId, contextKey)) return
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

  function modelOptionsScopeKey(params: ModelLoadParams): string {
    return JSON.stringify([
      modelOptionsContextKey(),
      params.keyword?.trim() ?? '',
      params.limit ?? 50,
      params.force === true,
      [...(options.selectedIds?.value ?? []), ...(params.selectedIds ?? [])].sort()
    ])
  }

  function modelOptionsContextKey(): string {
    return JSON.stringify([
      providerModelViewerScope(),
      options.providerCode?.value?.trim() ?? '',
      [...(options.providerCodes?.value ?? [])].map((code) => code.trim()).filter(Boolean).sort(),
      options.protocol ?? '',
      [...(options.selectedIds?.value ?? [])].map((id) => id.trim()).filter(Boolean).sort()
    ])
  }

  function providerModelViewerScope(): string {
    const viewer = authState.currentUser.value
    const targetSystemAccountId = options.scopeParams?.value?.systemAccountId
    return [targetSystemAccountId ? 'admin' : 'self', viewer?.id ?? 'anonymous', viewer?.role ?? 'anonymous', targetSystemAccountId ?? 'self'].join(':')
  }

  function isCurrentRequest(requestId: number, contextKey: string): boolean {
    return requestId === latestRequestId && modelOptionsContextKey() === contextKey
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
  return [option?.label, option?.value, ...(option?.providerCodes ?? [])]
    .some((value) => value?.toLowerCase().includes(keyword))
}
