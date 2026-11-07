import { onBeforeUnmount, ref } from 'vue'

import { api } from '@/api/client'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ProxyPoolMemberOptionSummary } from '@/types/domain'

// 池编辑器成员选择器（代理池设计 §8.1 member-options / §9.1）：
// 只返回 id/name/type/enabled/testStatus，不展示 host/port/密码。

export function useProxyPoolMemberOptions(config: { errorMessage?: string; limit?: number } = {}) {
  const options = ref<ProxyPoolMemberOptionSummary[]>([])
  const keyword = ref('')
  const loading = ref(false)
  const limit = config.limit ?? 50
  let requestId = 0
  let searchTimer: ReturnType<typeof setTimeout> | undefined

  async function load(nextKeyword = keyword.value, force = false): Promise<void> {
    keyword.value = nextKeyword
    const requestKeyword = keyword.value.trim() || undefined
    if (!force && loading.value) return
    const currentRequestId = ++requestId
    loading.value = true
    try {
      const nextOptions = await api.proxyPools.memberOptions({
        keyword: requestKeyword,
        limit
      })
      if (currentRequestId !== requestId) return
      options.value = nextOptions
    } catch (error) {
      if (currentRequestId !== requestId) return
      console.error(error)
      message.error(extractApiErrorMessage(error, config.errorMessage ?? '加载代理成员选项失败'))
    } finally {
      if (currentRequestId === requestId) {
        loading.value = false
      }
    }
  }

  function handleDropdown(open: boolean): void {
    if (open) {
      void load(keyword.value, true)
      return
    }
    cancelPendingSearch()
  }

  function handleSearch(value: string): void {
    keyword.value = value
    cancelPendingSearch()
    searchTimer = setTimeout(() => {
      searchTimer = undefined
      void load(keyword.value, true)
    }, 250)
  }

  function cancelPendingSearch(): void {
    if (searchTimer) {
      clearTimeout(searchTimer)
      searchTimer = undefined
    }
  }

  onBeforeUnmount(cancelPendingSearch)

  return {
    keyword,
    loading,
    options,
    handleDropdown,
    handleSearch,
    load
  }
}
