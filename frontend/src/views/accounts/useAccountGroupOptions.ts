import { onBeforeUnmount, ref, watch } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { rememberGroupLabels } from '@/shared/groupLabelCache'
import {
  removeLocalSelectPreferenceValues,
  type LocalSelectStorageKeyPart
} from '@/shared/selectLocalPreferenceCache'
import type { GroupOptionSummary } from '@/types/domain'

export interface AccountGroupOptionsScope {
  providerCode?: string
  systemAccountId?: string
  selectedIds?: Array<string | undefined>
}

export interface AccountGroupOptionsLoadOptions {
  useLocalWindow?: boolean
}

interface UseAccountGroupOptionsConfig {
  allowAllProviders?: boolean
  allowGlobalManagement?: boolean
  errorMessage?: string
  isManagementView: () => boolean
  limit?: number
  localCacheKeyParts?: (scope: Required<AccountGroupOptionsScope>) => LocalSelectStorageKeyPart[]
  onMissingSelectedIds?: (ids: string[]) => boolean | void
  preferenceKeys?: () => string[]
  scope: () => AccountGroupOptionsScope
  searchDelayMs?: number
}

export function useAccountGroupOptions(config: UseAccountGroupOptionsConfig) {
  const groups = ref<GroupOptionSummary[]>([])
  const keyword = ref('')
  const loading = ref(false)
  const limit = optionLimitValue(config.limit)
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined
  let lastMissingNoticeKey = ''

  watch(
    currentCatalogScopeKey,
    (nextScopeKey, previousScopeKey) => {
      if (nextScopeKey === previousScopeKey) return
      resetOptions()
    },
    { flush: 'sync' }
  )

  async function load(
    nextKeyword = keyword.value,
    force = false,
    scopeOverride?: Partial<AccountGroupOptionsScope>,
    _loadOptions: AccountGroupOptionsLoadOptions = {}
  ): Promise<void> {
    keyword.value = nextKeyword
    const scope = normalizedScope(scopeOverride)
    if ((config.isManagementView() && !scope.systemAccountId && !config.allowGlobalManagement) || (!config.allowAllProviders && !scope.providerCode && !scope.selectedIds.length)) {
      requestId += 1
      groups.value = []
      loadingKey = undefined
      loadingPromise = undefined
      loading.value = false
      return
    }

    const requestKeyword = normalizeOptionKeyword(nextKeyword)
    const managementView = config.isManagementView()
    const requestCatalogScopeKey = catalogScopeKey(managementView, scope)
    const requestKey = JSON.stringify([
      requestCatalogScopeKey,
      requestKeyword ?? '',
      scope.selectedIds
    ])
    if (!force && loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let nextGroups = managementView
          ? await api.groups.options(groupOptionParams(scope, requestKeyword, limit))
          : await api.myGroups.options(groupOptionParams(scope, requestKeyword, limit))
        if (!isCurrentRequest(currentRequestId, requestCatalogScopeKey, scopeOverride)) return
        nextGroups = await ensureSelectedGroupOptions(
          nextGroups,
          scope,
          managementView,
          () => isCurrentRequest(currentRequestId, requestCatalogScopeKey, scopeOverride)
        )
        if (!isCurrentRequest(currentRequestId, requestCatalogScopeKey, scopeOverride)) return
        rememberGroupLabels(nextGroups)
        groups.value = nextGroups
      } catch (error) {
        if (!isCurrentRequest(currentRequestId, requestCatalogScopeKey, scopeOverride)) return
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
      return
    }
    clearSearchTimer()
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

  function resetOptions(): void {
    requestId += 1
    clearSearchTimer()
    groups.value = []
    keyword.value = ''
    loading.value = false
    loadingKey = undefined
    loadingPromise = undefined
    lastMissingNoticeKey = ''
  }

  async function ensureSelectedGroupOptions(
    nextGroups: GroupOptionSummary[],
    scope: Required<AccountGroupOptionsScope>,
    managementView: boolean,
    isCurrent: () => boolean
  ): Promise<GroupOptionSummary[]> {
    const missingIds = scope.selectedIds.filter((id): id is string => Boolean(id && !nextGroups.some((group) => group.id === id)))
    if (!missingIds.length) return nextGroups
    if (!isCurrent()) return nextGroups
    try {
      const selectedGroups = managementView
        ? await api.groups.options(groupOptionParams(scope, undefined, limit, missingIds))
        : await api.myGroups.options(groupOptionParams(scope, undefined, limit, missingIds))
      if (!isCurrent()) return nextGroups
      const foundIds = new Set(selectedGroups.map((group) => group.id))
      const invalidSelectedIds = missingIds.filter((id) => !foundIds.has(id))
      handleMissingSelectedIds(invalidSelectedIds)
      return mergeOptionsById(selectedGroups, nextGroups)
    } catch {
      return nextGroups
    }
  }

  function handleMissingSelectedIds(ids: string[]): void {
    const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (!missingIds.length) return
    for (const preferenceKey of config.preferenceKeys?.() ?? ['groups']) {
      removeLocalSelectPreferenceValues(preferenceKey, missingIds)
    }
    const removedCurrentSelection = config.onMissingSelectedIds?.(missingIds) === true
    if (!removedCurrentSelection) return
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
        .map((id) => id?.trim())
        .filter((id): id is string => Boolean(id))
        .sort())]
    }
  }

  function currentCatalogScopeKey(): string {
    return catalogScopeKey(config.isManagementView(), normalizedScope())
  }

  function isCurrentRequest(
    currentRequestId: number,
    requestCatalogScopeKey: string,
    scopeOverride?: Partial<AccountGroupOptionsScope>
  ): boolean {
    return currentRequestId === requestId
      && (scopeOverride !== undefined || requestCatalogScopeKey === currentCatalogScopeKey())
  }

  onBeforeUnmount(resetOptions)

  return {
    clearSearchTimer,
    groups,
    handleDropdown,
    handleSearch,
    keyword,
    load,
    loading,
    reset: resetOptions,
    resetSearch
  }
}

function catalogScopeKey(isManagementView: boolean, scope: Required<AccountGroupOptionsScope>): string {
  return JSON.stringify([
    isManagementView ? `management:${scope.systemAccountId || 'all'}` : 'self',
    scope.providerCode
  ])
}

function groupOptionParams(scope: Required<AccountGroupOptionsScope>, keyword: string | undefined, limit: number, ids?: string[]) {
  return {
    systemAccountId: scope.systemAccountId || undefined,
    providerCode: scope.providerCode || undefined,
    ids,
    keyword,
    limit,
    manageableOnly: true,
    preferDefault: true,
    purpose: 'account' as const
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
