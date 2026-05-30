import { onBeforeUnmount, ref, shallowRef } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import {
  localSelectStorageKey,
  readLocalSelectOptionWindow,
  removeLocalSelectOptionWindowValues,
  removeLocalSelectPreferenceValues,
  writeLocalSelectOptionWindow,
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
  const optionCache = createShortLivedQueryCache<T[]>({ ttlMs: config.cacheTtlMs ?? 10_000 })
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined
  let lastMissingNoticeKey = ''

  async function load(nextKeyword = keyword.value): Promise<void> {
    if (config.enabled?.() === false) {
      options.value = []
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
    const optionWindowKey = localOptionWindowKey(requestKeyword)
    const localWindowOptions = readLocalSelectOptionWindow<T>(optionWindowKey)
    if (localWindowOptions?.length) {
      options.value = localWindowOptions
      loading.value = false
    }
    const cachedOptions = optionCache.get(requestKey)
    if (cachedOptions) {
      loadingKey = undefined
      loadingPromise = undefined
      loading.value = false
      writeLocalSelectOptionWindow(optionWindowKey, cachedOptions)
      options.value = cachedOptions
      return
    }
    loading.value = !localWindowOptions?.length
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let nextOptions = await fetchOptions<T>(config.kind, config.isManagementView(), {
          keyword: requestKeyword,
          limit
        })
        nextOptions = await ensureSelectedOptions(nextOptions, selectedIds, optionWindowKey)
        optionCache.set(requestKey, nextOptions)
        writeLocalSelectOptionWindow(optionWindowKey, nextOptions)
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

  async function ensureSelectedOptions(nextOptions: T[], selectedIds: string[], optionWindowKey: string): Promise<T[]> {
    const missingSelectedIds = selectedIds.filter((id) => !nextOptions.some((option) => option.id === id))
    if (!missingSelectedIds.length) return nextOptions
    try {
      const selectedOptions = await fetchOptions<T>(config.kind, config.isManagementView(), {
        ids: missingSelectedIds,
        limit: Math.min(50, Math.max(limit, missingSelectedIds.length))
      })
      const foundIds = new Set(selectedOptions.map((option) => option.id))
      const invalidSelectedIds = missingSelectedIds.filter((id) => !foundIds.has(id))
      handleMissingSelectedIds(invalidSelectedIds, optionWindowKey)
      return mergeOptionsById(selectedOptions, nextOptions)
    } catch {
      return nextOptions
    }
  }

  function handleMissingSelectedIds(ids: string[], optionWindowKey: string): void {
    const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (!missingIds.length) return
    removeLocalSelectOptionWindowValues(optionWindowKey, missingIds)
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

  function localOptionWindowKey(requestKeyword: string | undefined): string {
    return localSelectStorageKey([
      'authorization-principal-options',
      config.kind,
      config.isManagementView() ? 'management' : 'self',
      ...(config.localCacheKeyParts?.() ?? []),
      requestKeyword ?? ''
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
