import { ref, type Ref } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type { AccountOptionSummary } from '@/types/domain'
import { accountSelectionForId, type AccountSelection } from '@/shared/accountLabelCache'

type UseAuditLogAccountOptionsParams = {
  accountSelection: Ref<AccountSelection | undefined>
  selectedSystemAccountId: () => string | undefined
  selectedAccountId: () => string
}

export function useAuditLogAccountOptions(params: UseAuditLogAccountOptionsParams) {
  const options = ref<AccountOptionSummary[]>([])
  const loading = ref(false)
  const keyword = ref('')
  let searchTimer: ReturnType<typeof window.setTimeout> | undefined
  let requestSeq = 0
  let loadingKey: string | undefined
  let loadingPromise: Promise<void> | undefined

  function handleSearch(value: string): void {
    keyword.value = value
    clearSearchTimer()
    searchTimer = window.setTimeout(() => {
      searchTimer = undefined
      void load(keyword.value)
    }, 250)
  }

  function handleDropdown(open: boolean): void {
    if (open) {
      void load()
    }
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

  async function load(nextKeyword = keyword.value, force = false): Promise<void> {
    keyword.value = nextKeyword
    const requestKeyword = nextKeyword.trim() || undefined
    const systemAccountId = params.selectedSystemAccountId()
    if (!systemAccountId) {
      requestSeq += 1
      options.value = []
      params.accountSelection.value = undefined
      loading.value = false
      loadingKey = undefined
      loadingPromise = undefined
      return
    }
    const selectedIds = [params.selectedAccountId()].filter(Boolean)
    const requestKey = JSON.stringify([systemAccountId, requestKeyword ?? '', selectedIds])
    if (!force && loadingKey === requestKey && loadingPromise) {
      return loadingPromise
    }
    const currentRequestSeq = ++requestSeq
    loading.value = true
    loadingKey = requestKey
    loadingPromise = (async () => {
      try {
        let nextOptions = await api.accounts.options({ systemAccountId, keyword: requestKeyword, limit: 50 })
        nextOptions = await ensureSelectedAccountOption(nextOptions, systemAccountId)
        applyOptions(nextOptions, currentRequestSeq, requestKey, requestKeyword)
      } catch (error) {
        if (!isCurrentRequest(currentRequestSeq, requestKey, requestKeyword)) return
        console.error(error)
        message.error('AI账户筛选项加载失败')
      } finally {
        if (loadingKey === requestKey) {
          loadingKey = undefined
          loadingPromise = undefined
        }
        if (currentRequestSeq === requestSeq) {
          loading.value = false
        }
      }
    })()
    return loadingPromise
  }

  function applyOptions(
    nextOptions: AccountOptionSummary[],
    currentRequestSeq: number,
    requestKey: string,
    requestKeyword?: string
  ): void {
    if (!isCurrentRequest(currentRequestSeq, requestKey, requestKeyword)) return
    options.value = nextOptions
    syncSelectedAccountFromOptions(nextOptions)
  }

  async function ensureSelectedAccountOption(currentOptions: AccountOptionSummary[], systemAccountId: string): Promise<AccountOptionSummary[]> {
    const selectedIds = [params.selectedAccountId()].filter(Boolean)
    const missingIds = selectedIds.filter((id) => !currentOptions.some((account) => account.id === id))
    if (!missingIds.length) return currentOptions
    try {
      const selectedOptions = await api.accounts.options({ systemAccountId, ids: missingIds, limit: 50 })
      return mergeOptionsById(selectedOptions, currentOptions)
    } catch {
      return currentOptions
    }
  }

  function isCurrentRequest(currentRequestSeq: number, requestKey: string, requestKeyword?: string): boolean {
    return currentRequestSeq === requestSeq
      && requestKey === JSON.stringify([
        params.selectedSystemAccountId(),
        requestKeyword ?? '',
        [params.selectedAccountId()].filter(Boolean)
      ])
  }

  function syncSelectedAccountFromOptions(currentOptions: AccountOptionSummary[]): void {
    const selectedAccountId = params.selectedAccountId()
    if (!selectedAccountId || params.accountSelection.value) return
    params.accountSelection.value = accountSelectionForId(selectedAccountId, currentOptions)
  }

  return {
    clearSearchTimer,
    handleDropdown,
    handleSearch,
    load,
    loading,
    options,
    resetSearch
  }
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const seen = new Set<string>()
  const output: T[] = []
  for (const item of [...leading, ...trailing]) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    output.push(item)
  }
  return output
}
