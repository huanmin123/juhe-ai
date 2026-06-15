import { computed, ref } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type {
  AuditLogHotSearchResult,
  AuditLogSummary
} from '@/types/domain'
import {
  auditLogHotSearchActiveFilterCount,
  normalizeHotSearchKeywordInput
} from './auditLogFilters'

type SearchCompletedHandler = () => void | Promise<void>

type AuditLogHotSearchStateOptions = {
  initialKeyword: string
  pageSize: () => number
  onSearchCompleted?: SearchCompletedHandler
}

export function useAuditLogHotSearchState(options: AuditLogHotSearchStateOptions) {
  const hotSearchKeywordFilter = ref(options.initialKeyword)
  const hotSearchResult = ref<AuditLogHotSearchResult>()
  const hotSearchRecords = ref<AuditLogSummary[]>([])
  const hotSearchLoading = ref(false)
  let hotSearchRequestSeq = 0

  const hotSearchActiveFilterCount = computed(() => auditLogHotSearchActiveFilterCount(hotSearchKeywordFilter.value))
  const hotSearchTablePagination = computed(() => {
    const hasMore = hotSearchResult.value?.hasMore === true
    const count = hotSearchRecords.value.length
    return {
      current: 1,
      pageSize: options.pageSize(),
      total: hasMore ? count + 1 : count,
      showSizeChanger: false,
      showTotal: () => hasMore
        ? `已显示前 ${count} 条匹配审计，还有更多`
        : `共 ${count} 条匹配审计`
    }
  })

  function resetHotSearch(): void {
    hotSearchKeywordFilter.value = ''
    hotSearchRecords.value = []
    hotSearchResult.value = undefined
  }

  function cancelHotSearchRequest(): void {
    hotSearchRequestSeq += 1
  }

  async function searchHotAuditLogs(): Promise<void> {
    const keyword = normalizeHotSearchKeywordInput(hotSearchKeywordFilter.value)
    if (hotSearchKeywordFilter.value !== keyword) {
      hotSearchKeywordFilter.value = keyword
    }
    const requestId = ++hotSearchRequestSeq
    if (!keyword) {
      hotSearchRecords.value = []
      hotSearchResult.value = undefined
      return
    }
    hotSearchLoading.value = true
    try {
      const result = await api.auditLogs.searchHot({
        keywords: keyword,
        limit: options.pageSize()
      })
      if (requestId !== hotSearchRequestSeq) return
      hotSearchResult.value = result
      hotSearchRecords.value = result.items
      void options.onSearchCompleted?.()
    } catch (error) {
      if (requestId !== hotSearchRequestSeq) return
      console.error(error)
      message.error('搜索最近审计内容失败')
    } finally {
      if (requestId === hotSearchRequestSeq) {
        hotSearchLoading.value = false
      }
    }
  }

  return {
    cancelHotSearchRequest,
    hotSearchActiveFilterCount,
    hotSearchKeywordFilter,
    hotSearchLoading,
    hotSearchRecords,
    hotSearchResult,
    hotSearchTablePagination,
    resetHotSearch,
    searchHotAuditLogs
  }
}
