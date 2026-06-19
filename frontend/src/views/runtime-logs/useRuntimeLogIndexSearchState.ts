import { message } from '@/lib/antd'
import type { Ref } from 'vue'

import { api } from '@/api/client'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import type { RuntimeLogLevel, RuntimeLogSummary } from '@/types/domain'
import { normalizeOptionalTimeRange, type RuntimeLogTimeRangeValue } from './runtimeLogTimeRanges'

type RuntimeLogIndexSearchLoadOptions = {
  refreshFacets?: boolean
}

type UseRuntimeLogIndexSearchStateOptions = {
  eventFilter: Ref<string | undefined>
  indexTimeRange: Ref<RuntimeLogTimeRangeValue>
  initialPagination?: { current?: number; pageSize?: number; total?: number }
  keywordFilter: Ref<string>
  levelFilter: Ref<RuntimeLogLevel | 'all'>
  loadRuntimeLogFacets: (force?: boolean) => Promise<void>
  pageSize: number
  traceIdFilter: Ref<string>
}

export function useRuntimeLogIndexSearchState(options: UseRuntimeLogIndexSearchStateOptions) {
  const {
    items: records,
    loading,
    mobileHasMore,
    mobileLoadingMore,
    pagination,
    tablePagination,
    handleTableChange,
    loadData,
    loadMoreMobile: loadMoreMobileRecords,
    refreshMobile: refreshMobileRecords,
    resetPagination
  } = useResponsivePagedList<RuntimeLogSummary, RuntimeLogIndexSearchLoadOptions>({
    pageSize: options.pageSize,
    initialPagination: options.initialPagination,
    showTotal: (total, range, context) => context?.hasMore
      ? `已加载到第 ${range?.[1] ?? total - 1} 条运行日志，还有更多`
      : `共 ${total} 条运行日志`,
    fetchPage: async (loadOptions, pageState) => {
      void options.loadRuntimeLogFacets(loadOptions.refreshFacets === true)
      return await api.runtimeLogs.list(runtimeLogRequestParams(pageState))
    },
    requestSignature: (_loadOptions, pageState) => runtimeLogRequestParams(pageState),
    onError: (error) => {
      console.error(error)
      message.error('加载运行日志失败')
    }
  })

  return {
    records,
    loading,
    mobileHasMore,
    mobileLoadingMore,
    pagination,
    tablePagination,
    handleTableChange,
    loadData,
    loadMoreMobileRecords,
    refreshMobileRecords,
    resetPagination
  }

  function runtimeLogRequestParams(pageState: { current: number; pageSize: number }) {
    const traceId = options.traceIdFilter.value.trim()
    const range = normalizeOptionalTimeRange(options.indexTimeRange.value)
    return {
      page: pageState.current,
      pageSize: pageState.pageSize,
      traceId: traceId || undefined,
      level: options.levelFilter.value,
      event: options.eventFilter.value || undefined,
      keyword: options.keywordFilter.value || undefined,
      startAt: range?.[0].toISOString(),
      endAt: range?.[1].toISOString()
    }
  }
}
