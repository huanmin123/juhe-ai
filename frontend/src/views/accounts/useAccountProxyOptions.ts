import { getCurrentInstance, onBeforeUnmount, ref, watch } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { rememberProxyLabels } from '@/shared/proxyLabelCache'
import type { ProxyProfileOptionSummary } from '@/types/domain'

export interface AccountProxyOptionsScope {
  selectedIds?: Array<string | undefined>
}

interface UseAccountProxyOptionsConfig {
  errorMessage?: string
  limit?: number
  scope?: () => AccountProxyOptionsScope
  searchDelayMs?: number
}

export function useAccountProxyOptions(config: UseAccountProxyOptionsConfig = {}) {
  const proxies = ref<ProxyProfileOptionSummary[]>([])
  const keyword = ref('')
  const loading = ref(false)
  const limit = optionLimitValue(config.limit)
  const searchDelayMs = config.searchDelayMs ?? 250
  let requestId = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined
  let searchTimer: ReturnType<typeof setTimeout> | undefined
  let activeIdentity = proxyOptionIdentityKey()

  async function load(
    nextKeyword = keyword.value,
    force = false,
    scopeOverride?: Partial<AccountProxyOptionsScope>
  ): Promise<void> {
    keyword.value = nextKeyword
    ensureIdentityScope()
    const scope = normalizedScope(scopeOverride)
    const requestKeyword = normalizeOptionKeyword(nextKeyword)
    const requestKey = JSON.stringify([
      activeIdentity,
      limit,
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
        const selectedIds: string[] = scope.selectedIds
        const nextProxies = await api.proxies.options({
          keyword: requestKeyword,
          limit,
          ...(selectedIds.length > 0 ? { selectedIds } : {})
        })
        if (currentRequestId !== requestId || activeIdentity !== proxyOptionIdentityKey()) return
        rememberProxyLabels(nextProxies)
        proxies.value = nextProxies
      } catch (error) {
        if (currentRequestId !== requestId || activeIdentity !== proxyOptionIdentityKey()) return
        console.error(error)
        message.error(config.errorMessage ?? '加载代理选项失败')
      } finally {
        if (currentRequestId === requestId) {
          if (loadingKey === requestKey) {
            loadingKey = undefined
            loadingPromise = undefined
          }
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
    invalidateInFlightRequest()
    clearSearchTimer()
  }

  function handleSearch(value: string): void {
    keyword.value = value
    invalidateInFlightRequest()
    clearSearchTimer()
    searchTimer = setTimeout(() => {
      searchTimer = undefined
      void load(keyword.value)
    }, searchDelayMs)
  }

  function resetSearch(): void {
    keyword.value = ''
    invalidateInFlightRequest()
    clearSearchTimer()
  }

  function clearSearchTimer(): void {
    if (searchTimer) {
      clearTimeout(searchTimer)
      searchTimer = undefined
    }
  }

  function normalizedScope(scopeOverride?: Partial<AccountProxyOptionsScope>): { selectedIds: string[] } {
    const configuredScope = config.scope?.() ?? {}
    const scope = {
      ...configuredScope,
      ...scopeOverride,
      selectedIds: scopeOverride?.selectedIds ?? configuredScope.selectedIds
    }
    return {
      selectedIds: [...new Set((scope.selectedIds ?? [])
        .map((id) => id?.trim())
        .filter((id): id is string => Boolean(id))
        .sort())]
    }
  }

  function ensureIdentityScope(): void {
    const nextIdentity = proxyOptionIdentityKey()
    const identityChanged = nextIdentity !== activeIdentity
    if (identityChanged) activeIdentity = nextIdentity
    if (!identityChanged) return
    proxies.value = []
    invalidateInFlightRequest()
  }

  function invalidateInFlightRequest(): void {
    // A newer keyword, cache hit, or auth scope owns the visible loading state.
    requestId += 1
    loadingKey = undefined
    loadingPromise = undefined
    loading.value = false
  }

  if (getCurrentInstance()) {
    const stopWatchingAuthRevision = watch(
      () => authState.revision.value,
      ensureIdentityScope,
      { flush: 'sync' }
    )
    onBeforeUnmount(() => {
      clearSearchTimer()
      stopWatchingAuthRevision()
    })
  }

  return {
    clearSearchTimer,
    handleDropdown,
    handleSearch,
    keyword,
    load,
    loading,
    proxies,
    resetSearch
  }
}

function proxyOptionIdentityKey(): string {
  const viewer = authState.currentUser.value
  return [
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous'
  ].join(':')
}

function normalizeOptionKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
}

function optionLimitValue(value?: number): number {
  const limit = Number(value ?? 50)
  return Number.isFinite(limit) ? Math.min(50, Math.max(1, Math.trunc(limit))) : 50
}
