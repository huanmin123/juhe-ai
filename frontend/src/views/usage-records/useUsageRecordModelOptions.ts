import { message } from '@/lib/antd'
import { computed, onScopeDispose, watch, type ComputedRef } from 'vue'

import type { ListParams } from '@/api/client'
import { useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'
import type { ProviderModelOption } from '@/types/domain'

interface UseUsageRecordModelOptionsOptions {
  scopeParams: ComputedRef<ListParams | undefined>
  selectedModel: ComputedRef<string>
}

const searchDebounceMs = 200
const modelOptionLimit = 50

export function useUsageRecordModelOptions(options: UseUsageRecordModelOptionsOptions) {
  const resource = useProviderModelSelectOptions({
    scopeParams: options.scopeParams,
    onLoadError: (error) => {
      console.error(error)
      message.warning('加载模型筛选选项失败')
    }
  })
  let loadingQueryKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let requestGeneration = 0
  let searchTimer: ReturnType<typeof setTimeout> | undefined
  let searchWaiters: Array<() => void> = []

  async function loadIfNeeded(keyword = '', force = false): Promise<void> {
    const queryKey = currentQueryKey(keyword)
    if (!force && loadingQueryKey === queryKey && loadingPromise) return await loadingPromise

    const generation = requestGeneration + 1
    requestGeneration = generation
    const refreshCurrentSearch = force || keyword.trim().length > 0
    const promise = (async () => {
      await resource.loadModelOptions({
        force: refreshCurrentSearch,
        ...(keyword.trim() ? { keyword: keyword.trim() } : {}),
        limit: modelOptionLimit,
        selectedIds: selectedModelIds()
      })
      if (generation !== requestGeneration || queryKey !== currentQueryKey(keyword)) return
      if (resource.loadFailed.value) return
    })().finally(() => {
      if (generation !== requestGeneration) return
      loadingQueryKey = undefined
      loadingPromise = undefined
    })
    loadingQueryKey = queryKey
    loadingPromise = promise
    return await promise
  }

  async function handleDropdown(open: boolean): Promise<void> {
    if (!open) return
    await loadIfNeeded('', true)
  }

  function handleSearch(value: string): Promise<void> {
    clearSearchTimer()
    return new Promise((resolve) => {
      searchWaiters.push(resolve)
      searchTimer = setTimeout(() => {
        searchTimer = undefined
        const waiters = searchWaiters
        searchWaiters = []
        void loadIfNeeded(value).finally(() => {
          waiters.forEach((finish) => finish())
        })
      }, searchDebounceMs)
    })
  }

  function resetModelOptions(): void {
    requestGeneration += 1
    loadingQueryKey = undefined
    loadingPromise = undefined
    clearSearchTimer()
    resource.resetModelOptions()
  }

  function clearSearchTimer(): void {
    if (searchTimer !== undefined) {
      clearTimeout(searchTimer)
      searchTimer = undefined
    }
    const waiters = searchWaiters
    searchWaiters = []
    waiters.forEach((finish) => finish())
  }

  function currentScopeKey(): string {
    return options.scopeParams.value?.systemAccountId?.trim() || 'self'
  }

  function currentQueryKey(keyword: string): string {
    return JSON.stringify([
      currentScopeKey(),
      keyword.trim().toLowerCase(),
      selectedModelIds()
    ])
  }

  function selectedModelIds(): string[] {
    const model = options.selectedModel.value.trim()
    return model ? [model] : []
  }

  watch(currentScopeKey, resetModelOptions, { flush: 'sync' })
  onScopeDispose(clearSearchTimer)

  const modelOptions = computed<ProviderModelOption[]>(() => {
    const byId = new Map<string, ProviderModelOption>()
    for (const item of resource.providerModelOptions.value) {
      const id = item.id.trim()
      const name = item.name.trim()
      if (!id || !name || byId.has(id)) continue
      byId.set(id, { ...item, id, name })
    }
    return [...byId.values()]
  })

  return {
    handleDropdown,
    handleSearch,
    modelOptions,
    modelOptionsLoading: resource.loading,
    loadModelOptions: (force = false) => loadIfNeeded('', force),
    resetModelOptions
  }
}
