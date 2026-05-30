import { onBeforeUnmount, ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { rememberGroupLabels } from '@/shared/groupLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import {
  localSelectStorageKey,
  readLocalSelectOptionWindow,
  removeLocalSelectOptionWindowValues,
  removeLocalSelectPreferenceValues,
  writeLocalSelectOptionWindow,
  type LocalSelectStorageKeyPart
} from '@/shared/selectLocalPreferenceCache'
import type { GroupOptionSummary } from '@/types/domain'

export interface AccountGroupOptionsScope {
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
  localCacheKeyParts?: (scope: Required<AccountGroupOptionsScope>) => LocalSelectStorageKeyPart[]
  onMissingSelectedIds?: (ids: string[]) => void
  preferenceKeys?: () => string[]
  scope: () => AccountGroupOptionsScope
  searchDelayMs?: number
}

export function useAccountGroupOptions(config: UseAccountGroupOptionsConfig) {
  const groups = ref<GroupOptionSummary[]>([])
  const keyword = ref('')
  const loading = ref(false)
  const limit = optionLimitValue(config.limit)
  const optionCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: config.cacheTtlMs ?? 10_000 })
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined
  let lastMissingNoticeKey = ''

  async function load(nextKeyword = keyword.value, force = false, scopeOverride?: Partial<AccountGroupOptionsScope>): Promise<void> {
    keyword.value = nextKeyword
    const scope = normalizedScope(scopeOverride)
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
    const optionWindowKey = localOptionWindowKey(scope, requestKeyword)
    const localWindowGroups = !force ? readLocalSelectOptionWindow<GroupOptionSummary>(optionWindowKey) : undefined
    if (localWindowGroups?.length) {
      rememberGroupLabels(localWindowGroups)
      groups.value = localWindowGroups
      loading.value = false
    }
    if (!force) {
      const cachedGroups = optionCache.get(requestKey)
      if (cachedGroups) {
        loadingKey = undefined
        loadingPromise = undefined
        loading.value = false
        rememberGroupLabels(cachedGroups)
        writeLocalSelectOptionWindow(optionWindowKey, cachedGroups)
        groups.value = cachedGroups
        return
      }
    }

    loading.value = !localWindowGroups?.length
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let nextGroups = config.isManagementView()
          ? await api.groups.options(groupOptionParams(scope, requestKeyword, limit))
          : await api.myGroups.options(groupOptionParams(scope, requestKeyword, limit))
        nextGroups = await ensureSelectedGroupOptions(nextGroups, scope, optionWindowKey)
        rememberGroupLabels(nextGroups)
        optionCache.set(requestKey, nextGroups)
        writeLocalSelectOptionWindow(optionWindowKey, nextGroups)
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

  async function ensureSelectedGroupOptions(nextGroups: GroupOptionSummary[], scope: Required<AccountGroupOptionsScope>, optionWindowKey: string): Promise<GroupOptionSummary[]> {
    const missingIds = scope.selectedIds.filter((id): id is string => Boolean(id && !nextGroups.some((group) => group.id === id)))
    if (!missingIds.length) return nextGroups
    try {
      const selectedGroups = config.isManagementView()
        ? await api.groups.options(groupOptionParams(scope, undefined, limit, missingIds))
        : await api.myGroups.options(groupOptionParams(scope, undefined, limit, missingIds))
      const foundIds = new Set(selectedGroups.map((group) => group.id))
      const invalidSelectedIds = missingIds.filter((id) => !foundIds.has(id))
      handleMissingSelectedIds(invalidSelectedIds, optionWindowKey)
      return mergeOptionsById(selectedGroups, nextGroups)
    } catch {
      return nextGroups
    }
  }

  function handleMissingSelectedIds(ids: string[], optionWindowKey: string): void {
    const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (!missingIds.length) return
    removeLocalSelectOptionWindowValues(optionWindowKey, missingIds)
    for (const preferenceKey of config.preferenceKeys?.() ?? ['groups']) {
      removeLocalSelectPreferenceValues(preferenceKey, missingIds)
    }
    config.onMissingSelectedIds?.(missingIds)
    const noticeKey = missingIds.join('|')
    if (noticeKey !== lastMissingNoticeKey) {
      lastMissingNoticeKey = noticeKey
      message.warning('已移除不存在或无权访问的分组，请重新选择')
    }
  }

  function normalizedScope(scopeOverride?: Partial<AccountGroupOptionsScope>): Required<AccountGroupOptionsScope> {
    const configuredScope = config.scope()
    const scope = {
      ...configuredScope,
      ...scopeOverride,
      selectedIds: scopeOverride?.selectedIds ?? configuredScope.selectedIds
    }
    return {
      providerCode: scope.providerCode?.trim() ?? '',
      systemAccountId: scope.systemAccountId?.trim() ?? '',
      selectedIds: [...new Set((scope.selectedIds ?? [])
        .filter((id): id is string => Boolean(id?.trim()))
        .sort())]
    }
  }

  function localOptionWindowKey(scope: Required<AccountGroupOptionsScope>, requestKeyword: string | undefined): string {
    return localSelectStorageKey([
      'group-options',
      config.isManagementView() ? 'management' : 'self',
      scope.systemAccountId || 'all',
      scope.providerCode || 'all',
      ...(config.localCacheKeyParts?.(scope) ?? []),
      requestKeyword ?? ''
    ])
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

function groupOptionParams(scope: Required<AccountGroupOptionsScope>, keyword: string | undefined, limit: number, ids?: string[]) {
  return {
    systemAccountId: scope.systemAccountId || undefined,
    providerCode: scope.providerCode || undefined,
    ids,
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

function optionLimitValue(value?: number): number {
  const limit = Number(value ?? 50)
  return Number.isFinite(limit) ? Math.min(50, Math.max(1, Math.trunc(limit))) : 50
}
