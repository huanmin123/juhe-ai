import { onBeforeUnmount, ref, shallowRef } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import {
  removeLocalSelectPreferenceValues,
  type LocalSelectStorageKeyPart
} from '@/shared/selectLocalPreferenceCache'
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
  cacheTtlMs?: number
  localCacheKeyParts?: () => LocalSelectStorageKeyPart[]
  onMissingSelectedIds?: (ids: string[]) => void
  preferenceKeys?: () => string[]
  searchDelayMs?: number
  selectedIds?: () => Array<string | undefined>
}

export function useRemoteAuthorizationPrincipalOptions<T extends AuthorizationPrincipalOption>(config: RemoteAuthorizationPrincipalOptionsConfig) {
  const options = shallowRef<T[]>([])
  const loading = ref(false)
  const keyword = ref('')
  const limit = optionLimitValue(config.limit)
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined
  let lastMissingNoticeKey = ''

  async function load(nextKeyword = keyword.value): Promise<void> {
    if (config.enabled?.() === false) {
      requestId += 1
      options.value = []
      loading.value = false
      loadingKey = undefined
      loadingPromise = undefined
      return
    }
    const selectedIds = normalizedSelectedIds()
    const requestKeyword = normalizeOptionKeyword(nextKeyword)
    const requestKey = JSON.stringify([config.kind, config.isManagementView(), requestKeyword ?? '', selectedIds])
    if (loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        const isManagementView = config.isManagementView()
        let nextOptions = await fetchOptions<T>(config.kind, isManagementView, {
          keyword: requestKeyword,
          limit
        })
        const ensured = await ensureSelectedOptions(nextOptions, selectedIds, isManagementView)
        if (!isCurrentRequest(currentRequestId, requestKey, requestKeyword)) return
        handleMissingSelectedIds(ensured.invalidSelectedIds)
        options.value = ensured.options
      } catch (error) {
        if (!isCurrentRequest(currentRequestId, requestKey, requestKeyword)) return
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

  async function ensureSelectedOptions(nextOptions: T[], selectedIds: string[], isManagementView: boolean): Promise<{
    invalidSelectedIds: string[]
    options: T[]
  }> {
    const missingSelectedIds = selectedIds.filter((id) => !nextOptions.some((option) => option.id === id))
    if (!missingSelectedIds.length) return { invalidSelectedIds: [], options: nextOptions }
    try {
      const selectedOptions = await fetchOptions<T>(config.kind, isManagementView, {
        ids: missingSelectedIds,
        limit: Math.min(50, Math.max(limit, missingSelectedIds.length))
      })
      const foundIds = new Set(selectedOptions.map((option) => option.id))
      const invalidSelectedIds = missingSelectedIds.filter((id) => !foundIds.has(id))
      return {
        invalidSelectedIds,
        options: mergeOptionsById(selectedOptions, nextOptions)
      }
    } catch {
      return { invalidSelectedIds: [], options: nextOptions }
    }
  }

  function handleMissingSelectedIds(ids: string[]): void {
    const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (!missingIds.length) return
    for (const preferenceKey of config.preferenceKeys?.() ?? [`authorization-principal:${config.kind}`]) {
      removeLocalSelectPreferenceValues(preferenceKey, missingIds)
    }
    config.onMissingSelectedIds?.(missingIds)
    const noticeKey = missingIds.join('|')
    if (noticeKey !== lastMissingNoticeKey) {
      lastMissingNoticeKey = noticeKey
      message.warning('已移除不存在或无权访问的授权对象，请重新选择')
    }
  }

  function normalizedSelectedIds(): string[] {
    return [...new Set((config.selectedIds?.() ?? [])
      .filter((id): id is string => Boolean(id && id !== allSystemAccountsValue))
      .sort())]
  }

  function isCurrentRequest(currentRequestId: number, requestKey: string, requestKeyword?: string): boolean {
    return currentRequestId === requestId
      && requestKey === JSON.stringify([
        config.kind,
        config.isManagementView(),
        requestKeyword ?? '',
        normalizedSelectedIds()
      ])
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

async function fetchOptions<T extends AuthorizationPrincipalOption>(
  kind: AuthorizationPrincipalKind,
  isManagementView: boolean,
  params: { ids?: string[]; keyword?: string; limit: number }
): Promise<T[]> {
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

function optionLimitValue(value?: number): number {
  const limit = Number(value ?? 50)
  return Number.isFinite(limit) ? Math.min(50, Math.max(1, Math.trunc(limit))) : 50
}
