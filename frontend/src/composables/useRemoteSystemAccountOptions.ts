import { onBeforeUnmount, ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { SystemAccountPrincipalSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

interface RemoteSystemAccountOptionsConfig {
  enabled?: () => boolean
  errorMessage?: string
  limit?: number
  cacheTtlMs?: number
  searchDelayMs?: number
  selectedIds?: () => Array<string | undefined>
}

export function useRemoteSystemAccountOptions(config: RemoteSystemAccountOptionsConfig = {}) {
  const systemAccounts = ref<SystemAccountPrincipalSummary[]>([])
  const loading = ref(false)
  const keyword = ref('')
  const limit = config.limit ?? 50
  const optionCache = createShortLivedQueryCache<SystemAccountPrincipalSummary[]>({ ttlMs: config.cacheTtlMs ?? 10_000 })
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined

  async function load(nextKeyword = keyword.value): Promise<void> {
    if (config.enabled?.() === false) {
      systemAccounts.value = []
      loadingKey = undefined
      loadingPromise = undefined
      return
    }
    const selectedIds = normalizedSelectedIds()
    const requestKey = JSON.stringify([normalizeOptionKeyword(nextKeyword) ?? '', selectedIds])
    if (loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    const cachedOptions = optionCache.get(requestKey)
    if (cachedOptions) {
      loadingKey = undefined
      loadingPromise = undefined
      loading.value = false
      systemAccounts.value = cachedOptions
      return
    }
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let options = await api.systemAccounts.options({
          keyword: normalizeOptionKeyword(nextKeyword),
          limit
        })
        options = await ensureSelectedSystemAccountOptions(options, selectedIds)
        optionCache.set(requestKey, options)
        if (currentRequestId !== requestId) return
        systemAccounts.value = options
      } catch (error) {
        if (currentRequestId !== requestId) return
        console.error(error)
        message.error(config.errorMessage ?? '加载系统账户筛选项失败')
      } finally {
        if (loadingKey === requestKey) {
          loadingKey = undefined
          loadingPromise = undefined
        }
        if (currentRequestId === requestId) {
          loading.value = false
        }
      }
    })()
    return loadingPromise
  }

  function handleDropdown(open: boolean): void {
    if (open) {
      void load()
    }
  }

  function handleSearch(value: string): void {
    keyword.value = value
    clearSearchTimer()
    searchTimer = window.setTimeout(() => {
      searchTimer = undefined
      void load(keyword.value)
    }, searchDelayMs)
  }

  function resetSearch(): void {
    keyword.value = ''
    clearSearchTimer()
  }

  function clearSearchTimer(): void {
    if (searchTimer && typeof window !== 'undefined') {
      window.clearTimeout(searchTimer)
      searchTimer = undefined
    }
  }

  async function ensureSelectedSystemAccountOptions(options: SystemAccountPrincipalSummary[], selectedIds: string[]): Promise<SystemAccountPrincipalSummary[]> {
    const missingSelectedIds = selectedIds.filter((id) => !options.some((account) => account.id === id))
    if (!missingSelectedIds.length) return options
    const selectedOptions = await Promise.all(missingSelectedIds.map(async (id) => {
      try {
        return await api.systemAccounts.options({ keyword: id, limit: 1 })
      } catch {
        return []
      }
    }))
    return mergeOptionsById(selectedOptions.flat(), options)
  }

  function normalizedSelectedIds(): string[] {
    return [...new Set((config.selectedIds?.() ?? [])
      .filter((id): id is string => Boolean(id && id !== allSystemAccountsValue))
      .sort())]
  }

  onBeforeUnmount(clearSearchTimer)

  return {
    clearSearchTimer,
    handleDropdown,
    handleSearch,
    keyword,
    load,
    loading,
    resetSearch,
    systemAccounts
  }
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

function normalizeOptionKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
}
