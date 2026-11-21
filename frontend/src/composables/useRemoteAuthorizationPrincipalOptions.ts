import { onBeforeUnmount, ref, shallowRef } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { removeLocalSelectPreferenceValues } from '@/shared/selectLocalPreferenceCache'
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
  let searchTimer: number | undefined
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
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        const isManagementView = config.isManagementView()
        let nextOptions = await fetchOptions<T>(config.kind, isManagementView, {
          keyword: requestKeyword,
          limit
        })
        if (currentRequestId !== requestId) return
        nextOptions = await ensureSelectedOptions(nextOptions, selectedIds, isManagementView, () => currentRequestId === requestId)
        if (currentRequestId !== requestId) return
        options.value = nextOptions
      } catch (error) {
        if (currentRequestId !== requestId) return
        console.error(error)
        message.error(config.errorMessage ?? '加载授权候选项失败')
      } finally {
        if (currentRequestId === requestId && loadingKey === requestKey) {
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

  function invalidate(): void {
    clearSearchTimer()
    requestId += 1
    loadingKey = undefined
    loadingPromise = undefined
    loading.value = false
    options.value = []
  }

  function clearSearchTimer(): void {
    if (searchTimer && typeof window !== 'undefined') {
      window.clearTimeout(searchTimer)
      searchTimer = undefined
    }
  }

  async function ensureSelectedOptions(
    nextOptions: T[],
    selectedIds: string[],
    isManagementView: boolean,
    isCurrent: () => boolean
  ): Promise<T[]> {
    const missingSelectedIds = selectedIds.filter((id) => !nextOptions.some((option) => option.id === id))
    if (!missingSelectedIds.length || !isCurrent()) return nextOptions
    try {
      const selectedOptions = await fetchOptions<T>(config.kind, isManagementView, {
        ids: missingSelectedIds,
        limit: Math.min(50, Math.max(limit, missingSelectedIds.length))
      })
      if (!isCurrent()) return nextOptions
      const foundIds = new Set(selectedOptions.map((option) => option.id))
      const invalidSelectedIds = missingSelectedIds.filter((id) => !foundIds.has(id))
      if (!isCurrent()) return nextOptions
      handleMissingSelectedIds(invalidSelectedIds)
      return mergeOptionsById(selectedOptions, nextOptions)
    } catch {
      return nextOptions
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
      .filter((id): id is string => Boolean(id))
      .map((id) => id.trim())
      .filter((id) => id && id !== allSystemAccountsValue)
      .sort())]
  }

  onBeforeUnmount(invalidate)

  return {
    clearSearchTimer,
    handleDropdown,
    handleSearch,
    invalidate,
    keyword,
    load,
    loading,
    options,
    resetSearch
  }
}

// 授权身份候选项改走保留域：团队用 teams 列表/详情，账户用系统账户 options；
// 个人视图没有远端候选（被授权用户即自己），由当前登录用户本地构造。
async function fetchOptions<T extends AuthorizationPrincipalOption>(
  kind: AuthorizationPrincipalKind,
  isManagementView: boolean,
  params: { ids?: string[]; keyword?: string; limit: number }
): Promise<T[]> {
  if (kind === 'team') {
    if (params.ids?.length) {
      const details = await Promise.all(params.ids.map(async (id) => (isManagementView
        ? await api.systemTeams.detail(id)
        : await api.myTeams.detail(id))))
      return details as unknown as T[]
    }
    const result = isManagementView
      ? await api.systemTeams.list({ keyword: params.keyword, page: 1, pageSize: params.limit })
      : await api.myTeams.list({ keyword: params.keyword, page: 1, pageSize: params.limit })
    return result.items as unknown as T[]
  }
  if (isManagementView) {
    return (await api.systemAccounts.options({ ids: params.ids, keyword: params.keyword, limit: params.limit })) as unknown as T[]
  }
  const self = selfPrincipalOption()
  if (!self) return []
  if (params.ids?.length) {
    return params.ids.includes(self.id) ? [self as unknown as T] : []
  }
  if (params.keyword && !principalMatchesKeyword(self, params.keyword)) return []
  return [self as unknown as T]
}

function selfPrincipalOption(): SystemAccountPrincipalSummary | undefined {
  const user = authState.currentUser.value
  if (!user) return undefined
  return { id: user.id, username: user.username, displayName: user.displayName, status: 'active' }
}

function principalMatchesKeyword(option: SystemAccountPrincipalSummary, keyword: string): boolean {
  const normalized = keyword.trim().toLowerCase()
  if (!normalized) return true
  return option.username.toLowerCase().includes(normalized)
    || option.displayName.toLowerCase().includes(normalized)
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
