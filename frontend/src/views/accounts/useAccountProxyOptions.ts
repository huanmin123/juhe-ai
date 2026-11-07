import { getCurrentInstance, onBeforeUnmount, ref, watch } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { rememberProxyLabels, rememberProxyPoolLabels } from '@/shared/proxyLabelCache'
import type { ProxyOptionItem, ProxyOptionKind, ProxyPoolOptionSummary, ProxyProfileOptionSummary } from '@/types/domain'

export interface AccountProxyOptionsScope {
  selectedIds?: Array<string | undefined>
  selectedPoolIds?: Array<string | undefined>
}

interface UseAccountProxyOptionsConfig {
  errorMessage?: string
  limit?: number
  /** 账户下拉 kinds（代理池设计 §8.2）：缺省 profile 保持既有调用方行为。 */
  kinds?: ProxyOptionKind[]
  scope?: () => AccountProxyOptionsScope
  searchDelayMs?: number
}

export function useAccountProxyOptions(config: UseAccountProxyOptionsConfig = {}) {
  const proxies = ref<ProxyProfileOptionSummary[]>([])
  const pools = ref<ProxyPoolOptionSummary[]>([])
  const keyword = ref('')
  const loading = ref(false)
  const limit = optionLimitValue(config.limit)
  const kinds: ProxyOptionKind[] = config.kinds?.length ? config.kinds : ['profile']
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
      kinds,
      requestKeyword ?? '',
      scope.selectedIds,
      scope.selectedPoolIds
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
        const selectedPoolIds: string[] = scope.selectedPoolIds
        const nextOptions = await api.proxies.options({
          keyword: requestKeyword,
          limit,
          ...(kinds.length > 1 || kinds[0] !== 'profile' ? { kinds } : {}),
          ...(selectedIds.length > 0 ? { selectedIds } : {}),
          ...(selectedPoolIds.length > 0 ? { selectedPoolIds } : {})
        })
        if (currentRequestId !== requestId || activeIdentity !== proxyOptionIdentityKey()) return
        const { profiles, poolOptions } = splitProxyOptionItems(nextOptions)
        rememberProxyLabels(profiles)
        rememberProxyPoolLabels(poolOptions)
        proxies.value = profiles
        pools.value = poolOptions
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

  function normalizedScope(scopeOverride?: Partial<AccountProxyOptionsScope>): { selectedIds: string[]; selectedPoolIds: string[] } {
    const configuredScope = config.scope?.() ?? {}
    const scope = {
      ...configuredScope,
      ...scopeOverride,
      selectedIds: scopeOverride?.selectedIds ?? configuredScope.selectedIds,
      selectedPoolIds: scopeOverride?.selectedPoolIds ?? configuredScope.selectedPoolIds
    }
    const uniqueSorted = (ids: Array<string | undefined> | undefined): string[] => [...new Set((ids ?? [])
      .map((id) => id?.trim())
      .filter((id): id is string => Boolean(id))
      .sort())]
    return {
      selectedIds: uniqueSorted(scope.selectedIds),
      selectedPoolIds: uniqueSorted(scope.selectedPoolIds)
    }
  }

  function ensureIdentityScope(): void {
    const nextIdentity = proxyOptionIdentityKey()
    const identityChanged = nextIdentity !== activeIdentity
    if (identityChanged) activeIdentity = nextIdentity
    if (!identityChanged) return
    proxies.value = []
    pools.value = []
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
    pools,
    proxies,
    resetSearch
  }
}

/** options 响应按 kind 判别拆分（§8.2）：kind 缺省按 profile 兼容既有后端响应。 */
export function splitProxyOptionItems(items: ProxyOptionItem[]): {
  profiles: ProxyProfileOptionSummary[]
  poolOptions: ProxyPoolOptionSummary[]
} {
  const profiles: ProxyProfileOptionSummary[] = []
  const poolOptions: ProxyPoolOptionSummary[] = []
  for (const item of items ?? []) {
    if (item.kind === 'pool') {
      poolOptions.push(item)
      continue
    }
    profiles.push({
      id: item.id,
      name: item.name,
      type: item.type,
      enabled: item.enabled
    })
  }
  return { profiles, poolOptions }
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
