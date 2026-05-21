import { onBeforeUnmount, ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { GroupOptionSummary } from '@/types/domain'

interface AccountGroupOptionsScope {
  providerCode?: string
  systemAccountId?: string
  selectedIds?: Array<string | undefined>
}

interface UseAccountGroupOptionsConfig {
  allowAllProviders?: boolean
  errorMessage?: string
  isManagementView: () => boolean
  limit?: number
  cacheTtlMs?: number
  scope: () => AccountGroupOptionsScope
  searchDelayMs?: number
}

export function useAccountGroupOptions(config: UseAccountGroupOptionsConfig) {
  const groups = ref<GroupOptionSummary[]>([])
  const keyword = ref('')
  const loading = ref(false)
  const limit = config.limit ?? 50
  const optionCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: config.cacheTtlMs ?? 10_000 })
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined

  async function load(nextKeyword = keyword.value, force = false): Promise<void> {
    keyword.value = nextKeyword
    const scope = normalizedScope()
    if ((config.isManagementView() && !scope.systemAccountId) || (!config.allowAllProviders && !scope.providerCode && !scope.selectedIds.length)) {
      requestId += 1
      groups.value = []
      loadingKey = undefined
      loadingPromise = undefined
      loading.value = false
      return
    }

    const requestKeyword = normalizeOptionKeyword(nextKeyword)
    const requestKey = JSON.stringify([
      config.isManagementView() ? `management:${scope.systemAccountId ?? 'all'}` : 'self',
      scope.providerCode ?? '',
      requestKeyword ?? '',
      scope.selectedIds
    ])
    if (!force && loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    if (!force) {
      const cachedGroups = optionCache.get(requestKey)
      if (cachedGroups) {
        loadingKey = undefined
        loadingPromise = undefined
        loading.value = false
        groups.value = cachedGroups
        return
      }
    }

    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let nextGroups = config.isManagementView()
          ? await api.groups.options(groupOptionParams(scope, requestKeyword, limit))
          : await api.myGroups.options(groupOptionParams(scope, requestKeyword, limit))
        nextGroups = await ensureSelectedGroupOptions(nextGroups, scope)
        optionCache.set(requestKey, nextGroups)
        if (currentRequestId !== requestId) return
        groups.value = nextGroups
      } catch (error) {
        if (currentRequestId !== requestId) return
        console.error(error)
        message.error(config.errorMessage ?? '加载分组选项失败')
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

  async function ensureSelectedGroupOptions(nextGroups: GroupOptionSummary[], scope: Required<AccountGroupOptionsScope>): Promise<GroupOptionSummary[]> {
    const missingIds = scope.selectedIds.filter((id) => !nextGroups.some((group) => group.id === id))
    if (!missingIds.length) return nextGroups
    const selectedGroups = await Promise.all(missingIds.map(async (id) => {
      try {
        return config.isManagementView()
          ? await api.groups.options(groupOptionParams(scope, id, limit))
          : await api.myGroups.options(groupOptionParams(scope, id, limit))
      } catch {
        return []
      }
    }))
    return mergeOptionsById(selectedGroups.flat(), nextGroups)
  }

  function normalizedScope(): Required<AccountGroupOptionsScope> {
    const scope = config.scope()
    return {
      providerCode: scope.providerCode?.trim() ?? '',
      systemAccountId: scope.systemAccountId?.trim() ?? '',
      selectedIds: [...new Set((scope.selectedIds ?? [])
        .filter((id): id is string => Boolean(id?.trim()))
        .sort())]
    }
  }

  onBeforeUnmount(clearSearchTimer)

  return {
    clearSearchTimer,
    groups,
    handleDropdown,
    handleSearch,
    keyword,
    load,
    loading,
    resetSearch
  }
}

function groupOptionParams(scope: Required<AccountGroupOptionsScope>, keyword: string | undefined, limit: number) {
  return {
    systemAccountId: scope.systemAccountId || undefined,
    providerCode: scope.providerCode || undefined,
    keyword,
    limit,
    manageableOnly: true,
    preferDefault: true
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
