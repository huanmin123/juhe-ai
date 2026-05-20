import { onBeforeUnmount, ref, shallowRef } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import type { SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

type AuthorizationPrincipalKind = 'account' | 'team'
type AuthorizationPrincipalOption = SystemAccountPrincipalSummary | SystemTeamPrincipalSummary

interface RemoteAuthorizationPrincipalOptionsConfig {
  enabled?: () => boolean
  errorMessage?: string
  isManagementView: () => boolean
  kind: AuthorizationPrincipalKind
  limit?: number
  searchDelayMs?: number
  selectedIds?: () => Array<string | undefined>
}

export function useRemoteAuthorizationPrincipalOptions<T extends AuthorizationPrincipalOption>(config: RemoteAuthorizationPrincipalOptionsConfig) {
  const options = shallowRef<T[]>([])
  const loading = ref(false)
  const keyword = ref('')
  const limit = config.limit ?? 50
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined

  async function load(nextKeyword = keyword.value): Promise<void> {
    if (config.enabled?.() === false) {
      options.value = []
      loadingKey = undefined
      loadingPromise = undefined
      return
    }
    const selectedIds = normalizedSelectedIds()
    const requestKey = JSON.stringify([config.kind, config.isManagementView(), normalizeOptionKeyword(nextKeyword) ?? '', selectedIds])
    if (loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let nextOptions = await fetchOptions<T>(config.kind, config.isManagementView(), normalizeOptionKeyword(nextKeyword), limit)
        nextOptions = await ensureSelectedOptions(nextOptions, selectedIds)
        if (currentRequestId !== requestId) return
        options.value = nextOptions
      } catch (error) {
        if (currentRequestId !== requestId) return
        console.error(error)
        message.error(config.errorMessage ?? '加载授权候选项失败')
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

  async function ensureSelectedOptions(nextOptions: T[], selectedIds: string[]): Promise<T[]> {
    const missingSelectedIds = selectedIds.filter((id) => !nextOptions.some((option) => option.id === id))
    if (!missingSelectedIds.length) return nextOptions
    const selectedOptions = await Promise.all(missingSelectedIds.map(async (id) => {
      try {
        return await fetchOptions<T>(config.kind, config.isManagementView(), id, 1)
      } catch {
        return []
      }
    }))
    return mergeOptionsById(selectedOptions.flat(), nextOptions)
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
    options,
    resetSearch
  }
}

async function fetchOptions<T extends AuthorizationPrincipalOption>(kind: AuthorizationPrincipalKind, isManagementView: boolean, keyword: string | undefined, limit: number): Promise<T[]> {
  const params = { keyword, limit }
  if (kind === 'team') {
    const options = isManagementView
      ? await api.authorizationOptions.granteeTeams(params)
      : await api.myAuthorizationOptions.granteeTeams(params)
    return options as T[]
  }
  const options = isManagementView
    ? await api.authorizationOptions.granteeAccounts(params)
    : await api.myAuthorizationOptions.granteeAccounts(params)
  return options as T[]
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
