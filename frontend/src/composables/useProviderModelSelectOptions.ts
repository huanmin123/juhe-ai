import { computed, ref, type ComputedRef } from 'vue'

import { api, type ListParams } from '@/api/client'
import type { ProviderModelApiProtocol, ProviderModelOption } from '@/types/domain'

export interface ProviderModelSelectOption {
  label: string
  value: string
  providerCodes: string[]
  supportedApiProtocols: ProviderModelApiProtocol[]
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

export function useProviderModelSelectOptions(options: UseProviderModelSelectOptionsOptions = {}) {
  const providerModelOptions = ref<ProviderModelOption[]>([])
  const loading = ref(false)
  const loadFailed = ref(false)
  let loadedScopeKey: string | undefined
  let latestRequestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined

  const selectOptions = computed<ProviderModelSelectOption[]>(() => {
    const grouped = new Map<string, {
      name: string
      providerCodes: Set<string>
      supportedApiProtocols: Set<ProviderModelApiProtocol>
    }>()
    for (const option of providerModelOptions.value) {
      const id = option.id.trim()
      const name = option.name.trim()
      if (!id || !name) continue
      const existing = grouped.get(id) ?? {
        name,
        providerCodes: new Set<string>(),
        supportedApiProtocols: new Set<ProviderModelApiProtocol>()
      }
      if (option.providerCode?.trim()) existing.providerCodes.add(option.providerCode.trim())
      for (const protocol of option.supportedApiProtocols) existing.supportedApiProtocols.add(protocol)
      grouped.set(id, existing)
    }
    return [...grouped.entries()]
      .sort((left, right) => left[1].name.localeCompare(right[1].name, 'en'))
      .map(([value, item]) => {
        const providerCodes = [...item.providerCodes].sort()
        return {
          label: providerCodes.length ? `${item.name}（${providerCodes.join('、')}）` : item.name,
          value,
          providerCodes,
          supportedApiProtocols: [...item.supportedApiProtocols].sort()
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
      .map((id) => id.trim()).filter(Boolean))]
    const query = {
      ...scopeParams,
      ...(providerCode ? { providerCode } : {}),
      ...(options.protocol ? { protocol: options.protocol } : {}),
      ...(params.keyword?.trim() ? { keyword: params.keyword.trim() } : {}),
      limit: Math.min(50, Math.max(1, params.limit ?? 50))
    }
    const promise = (async () => {
      try {
        const selectedIdBatches = chunkedSelectedModelIds(selectedIds)
        const requestBatches = selectedIdBatches.length ? selectedIdBatches : [[]]
        const requestProviderCodes = providerCodes.length ? providerCodes : [undefined]
        const results = await Promise.all(requestProviderCodes.flatMap((code) => requestBatches.map((batch) => (
          api.providers.modelOptions({
            ...query,
            ...(code ? { providerCode: code } : {}),
            ...(batch.length ? { selectedIds: batch } : {})
          })
        ))))
        const byId = new Map<string, ProviderModelOption>()
        for (const item of results.flat()) byId.set(item.id, item)
        const result = [...byId.values()]
        if (isCurrentRequest(requestId, contextKey)) {
          providerModelOptions.value = result
          loadedScopeKey = scopeKey
        }
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
      options.scopeParams?.value?.systemAccountId ?? 'self',
      options.providerCode?.value?.trim() ?? '',
      [...(options.providerCodes?.value ?? [])].map((code) => code.trim()).filter(Boolean).sort(),
      options.protocol ?? '',
      [...(options.selectedIds?.value ?? [])].map((id) => id.trim()).filter(Boolean).sort()
    ])
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

function chunkedSelectedModelIds(values: string[]): string[][] {
  const output: string[][] = []
  for (let index = 0; index < values.length; index += 50) {
    output.push(values.slice(index, index + 50))
  }
  return output
}

export function filterModelOption(input: string, option?: ProviderModelSelectOption): boolean {
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  return [option?.label, option?.value, ...(option?.providerCodes ?? [])]
    .some((value) => value?.toLowerCase().includes(keyword))
}
