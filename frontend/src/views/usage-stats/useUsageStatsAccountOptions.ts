import { message } from '@/lib/antd'
import { ref, type Ref } from 'vue'

import { api } from '@/api/client'
import type { AccountOptionSummary } from '@/types/domain'
import { mergeOptionsById } from './usageStatsHelpers'

interface UseUsageStatsAccountOptionsOptions {
  isManagementView: () => boolean
  systemAccountId: () => string | undefined
  selectedIds: () => string[]
  pageActive: Ref<boolean>
}

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
    const requestKey = JSON.stringify([options.isManagementView(), systemAccountId ?? '', requestKeyword ?? '', selectedIds])
    if (!force && accountOptionsLoadingKey === requestKey && accountOptionsLoadingPromise) {
      return accountOptionsLoadingPromise
    }
    const requestSeq = ++accountOptionsRequestSeq
    accountOptionsLoading.value = true
    accountOptionsLoadingKey = requestKey
    accountOptionsLoadingPromise = (async () => {
      try {
        let nextOptions = options.isManagementView()
          ? await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: 50 })
          : await api.myAccounts.options({ keyword: requestKeyword, limit: 50 })
        nextOptions = await ensureSelectedAccountOptions(nextOptions, systemAccountId)
        applyOptions(nextOptions, requestSeq)
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
