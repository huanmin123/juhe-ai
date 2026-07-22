import { onBeforeUnmount, ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import {
  removeLocalSelectPreferenceValues,
  type LocalSelectStorageKeyPart
} from '@/shared/selectLocalPreferenceCache'
import type { SystemAccountPrincipalSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

interface RemoteSystemAccountOptionsConfig {
  enabled?: () => boolean
  errorMessage?: string
  limit?: number
  cacheTtlMs?: number
  localCacheKeyParts?: () => LocalSelectStorageKeyPart[]
  onMissingSelectedIds?: (ids: string[]) => void
  preferenceKeys?: () => string[]
  searchDelayMs?: number
  selectedIds?: () => Array<string | undefined>
}

export function useRemoteSystemAccountOptions(config: RemoteSystemAccountOptionsConfig = {}) {
  const systemAccounts = ref<SystemAccountPrincipalSummary[]>([])
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
      systemAccounts.value = []
      loadingKey = undefined
      loadingPromise = undefined
      return
    }
    const selectedIds = normalizedSelectedIds()
    const requestKeyword = normalizeOptionKeyword(nextKeyword)
    const requestKey = JSON.stringify([requestKeyword ?? '', selectedIds])
    if (loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestId = ++requestId
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let options = await api.systemAccounts.options({
          keyword: requestKeyword,
          limit
        })
        options = await ensureSelectedSystemAccountOptions(options, selectedIds)
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
    try {
      const selectedOptions = await api.systemAccounts.options({
        ids: missingSelectedIds,
        limit: Math.min(50, Math.max(limit, missingSelectedIds.length))
      })
      const foundIds = new Set(selectedOptions.map((option) => option.id))
      const invalidSelectedIds = missingSelectedIds.filter((id) => !foundIds.has(id))
      handleMissingSelectedIds(invalidSelectedIds)
      return mergeOptionsById(selectedOptions, options)
    } catch {
      return options
    }
  }

  function handleMissingSelectedIds(ids: string[]): void {
    const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
    if (!missingIds.length) return
    for (const preferenceKey of config.preferenceKeys?.() ?? ['system-principal:system_account']) {
      removeLocalSelectPreferenceValues(preferenceKey, missingIds)
    }
    config.onMissingSelectedIds?.(missingIds)
    const noticeKey = missingIds.join('|')
    if (noticeKey !== lastMissingNoticeKey) {
      lastMissingNoticeKey = noticeKey
      message.warning('已移除不存在或无权访问的系统账户，请重新选择')
    }
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

function optionLimitValue(value?: number): number {
  const limit = Number(value ?? 50)
  return Number.isFinite(limit) ? Math.min(50, Math.max(1, Math.trunc(limit))) : 50
}
