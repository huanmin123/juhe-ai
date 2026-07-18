import { message } from '@/lib/antd'
import { ref, type Ref } from 'vue'

import { api, pageDataApi } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { getDefaultPageDataResourceCache } from '@/shared/pageDataResourceCache'
import type { AccountOptionSummary } from '@/types/domain'
import { mergeOptionsById } from './usageStatsHelpers'

interface UseUsageStatsAccountOptionsOptions {
  isManagementView: () => boolean
  systemAccountId: () => string | undefined
  selectedIds: () => string[]
  pageActive: Ref<boolean>
}

const usageStatsAccountOptionsResourceCache = getDefaultPageDataResourceCache((request) => pageDataApi.confirm(request))

export function useUsageStatsAccountOptions(options: UseUsageStatsAccountOptionsOptions) {
  const accountOptionRows = ref<AccountOptionSummary[]>([])
  const accountOptionsLoading = ref(false)
  const accountOptionsKeyword = ref('')
  let accountOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
  let accountOptionsRequestSeq = 0
  let accountOptionsLoadingKey: string | undefined
  let accountOptionsLoadingPromise: Promise<void> | undefined

  async function loadAccountOptions(keyword = accountOptionsKeyword.value, force = false): Promise<void> {
    accountOptionsKeyword.value = keyword
    const systemAccountId = options.isManagementView() ? options.systemAccountId() : undefined
    const requestKeyword = keyword.trim() || undefined
    const selectedIds = [...options.selectedIds()].sort()
    const scope = accountOptionsScope(options.isManagementView(), systemAccountId)
    const route = options.isManagementView() ? '/accounts/options' : '/my-accounts/options'
    const requestKey = JSON.stringify([scope, requestKeyword ?? '', selectedIds])
    if (!force && accountOptionsLoadingKey === requestKey && accountOptionsLoadingPromise) {
      return accountOptionsLoadingPromise
    }
    const requestSeq = ++accountOptionsRequestSeq
    accountOptionsLoading.value = true
    accountOptionsLoadingKey = requestKey
    accountOptionsLoadingPromise = (async () => {
      try {
        if (force) await usageStatsAccountOptionsResourceCache.invalidate('accounts.options', scope, route)
        const result = await usageStatsAccountOptionsResourceCache.load<AccountOptionSummary[]>({
          cacheKey: {
            scope,
            route,
            query: { keyword: requestKeyword, selectedIds, systemAccountId, limit: 50 },
            version: 1
          },
          domain: 'accounts.options',
          viewScope: options.isManagementView() ? 'admin' : 'self',
          ...(options.isManagementView() && systemAccountId ? { targetSystemAccountId: systemAccountId } : {}),
          loadNetwork: async () => {
            let nextOptions = options.isManagementView()
              ? await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: 50 })
              : await api.myAccounts.options({ keyword: requestKeyword, limit: 50 })
            nextOptions = await ensureSelectedAccountOptions(nextOptions, systemAccountId)
            return nextOptions
          }
        })
        applyOptions(result.data, requestSeq)
        void result.confirmation?.then((outcome) => {
          if (outcome.data) applyOptions(outcome.data, requestSeq)
        })
      } catch (error) {
        if (requestSeq !== accountOptionsRequestSeq) return
        console.error(error)
        message.error('账户筛选项加载失败')
      } finally {
        if (accountOptionsLoadingKey === requestKey) {
          accountOptionsLoadingKey = undefined
          accountOptionsLoadingPromise = undefined
        }
        if (requestSeq === accountOptionsRequestSeq) {
          accountOptionsLoading.value = false
        }
      }
    })()
    return accountOptionsLoadingPromise
  }

  function applyOptions(nextOptions: AccountOptionSummary[], requestSeq: number): void {
    if (requestSeq !== accountOptionsRequestSeq) return
    accountOptionRows.value = nextOptions
  }

  async function ensureSelectedAccountOptions(nextOptions: AccountOptionSummary[], systemAccountId: string | undefined): Promise<AccountOptionSummary[]> {
    const selectedIds = [...new Set(options.selectedIds())]
    const missingIds = selectedIds.filter((id) => !nextOptions.some((account) => account.id === id))
    if (!missingIds.length) return nextOptions
    try {
      const selectedOptions = options.isManagementView()
        ? await api.accounts.options({ systemAccountId, ids: missingIds, limit: 50 })
        : await api.myAccounts.options({ ids: missingIds, limit: 50 })
      return mergeOptionsById(selectedOptions, nextOptions)
    } catch {
      return nextOptions
    }
  }

  function handleAccountOptionsSearch(value: string) {
    accountOptionsKeyword.value = value
    clearAccountOptionsSearchTimer()
    accountOptionsSearchTimer = window.setTimeout(() => {
      accountOptionsSearchTimer = undefined
      if (!options.pageActive.value) return
      void loadAccountOptions(accountOptionsKeyword.value)
    }, 250)
  }

  function handleAccountOptionsDropdown(open: boolean) {
    if (open) {
      void loadAccountOptions()
    }
  }

  function clearAccountOptionsSearchTimer() {
    if (accountOptionsSearchTimer && typeof window !== 'undefined') {
      window.clearTimeout(accountOptionsSearchTimer)
      accountOptionsSearchTimer = undefined
    }
  }

  return {
    accountOptionRows,
    accountOptionsLoading,
    accountOptionsKeyword,
    loadAccountOptions,
    ensureSelectedAccountOptions,
    handleAccountOptionsSearch,
    handleAccountOptionsDropdown,
    clearAccountOptionsSearchTimer
  }
}

function accountOptionsScope(isManagementView: boolean, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return [
    isManagementView ? 'admin' : 'self',
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    systemAccountId ?? (isManagementView ? 'all' : 'self')
  ].join(':')
}
