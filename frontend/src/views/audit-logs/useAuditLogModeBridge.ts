import { computed, type Ref } from 'vue'

import type { AuditLogListItem } from '@/types/domain'

type AuditLogViewMode = 'list' | 'search'
type TablePaginationState = Record<string, unknown>

export function useAuditLogModeBridge(input: {
  activeFilterCount: Ref<number>
  applyFilters: () => void
  clearRouteTraceIdForManualState: () => void
  handleTableChange: (paginationInfo: unknown) => void
  hotSearchActiveFilterCount: Ref<number>
  hotSearchKeywordFilter: Ref<string>
  hotSearchLoading: Ref<boolean>
  hotSearchRecords: Ref<AuditLogListItem[]>
  hotSearchResult: Ref<unknown>
  hotSearchTablePagination: Ref<TablePaginationState>
  loadData: (options?: { forceOptions?: boolean }) => unknown
  loading: Ref<boolean>
  loadMoreMobileRecords: () => void
  mobileHasMore: Ref<boolean>
  mobileLoadingMore: Ref<boolean>
  records: Ref<AuditLogListItem[]>
  refreshMobileRecords: () => void
  resetFilters: () => void
  resetHotSearch: () => void
  searchHotAuditLogs: () => void | Promise<void>
  tablePagination: Ref<TablePaginationState>
  traceIdFilter: Ref<string>
  viewMode: Ref<AuditLogViewMode>
  advancedFilterCount: Ref<number>
}) {
  const toolbarKeyword = computed({
    get: () => input.viewMode.value === 'search' ? input.hotSearchKeywordFilter.value : input.traceIdFilter.value,
    set: (value: string) => {
      if (input.viewMode.value === 'search') {
        input.hotSearchKeywordFilter.value = value
      } else {
        input.traceIdFilter.value = value
      }
    }
  })
  const toolbarSearchPlaceholder = computed(() => input.viewMode.value === 'search'
    ? '搜索最近1小时审计原始请求'
    : '输入完整 traceId，精确查找请求')
  const toolbarFilterTitle = computed(() => input.viewMode.value === 'search' ? '最近内容搜索' : '审计筛选')
  const toolbarActiveFilterCount = computed(() => input.viewMode.value === 'search' ? input.hotSearchActiveFilterCount.value : input.activeFilterCount.value)
  const toolbarAdvancedFilterCount = computed(() => input.viewMode.value === 'search' ? 0 : input.advancedFilterCount.value)
  const currentRecords = computed(() => input.viewMode.value === 'search' ? input.hotSearchRecords.value : input.records.value)
  const currentLoading = computed(() => input.viewMode.value === 'search' ? input.hotSearchLoading.value : input.loading.value)
  const currentMobileHasMore = computed(() => input.viewMode.value === 'search' ? false : input.mobileHasMore.value)
  const currentMobileLoadingMore = computed(() => input.viewMode.value === 'search' ? false : input.mobileLoadingMore.value)
  const currentTablePagination = computed(() => input.viewMode.value === 'search' ? input.hotSearchTablePagination.value : input.tablePagination.value)

  function applyCurrentMode(): void {
    if (input.viewMode.value === 'search') {
      input.clearRouteTraceIdForManualState()
      void input.searchHotAuditLogs()
      return
    }
    input.applyFilters()
  }

  function refreshCurrentMode(): void {
    if (input.viewMode.value === 'search') {
      void input.searchHotAuditLogs()
      return
    }
    void input.loadData({ forceOptions: true })
  }

  function resetCurrentMode(): void {
    if (input.viewMode.value === 'search') {
      input.clearRouteTraceIdForManualState()
      input.resetHotSearch()
      return
    }
    input.resetFilters()
  }

  function handleViewModeChange(): void {
    if (input.viewMode.value === 'search') {
      input.clearRouteTraceIdForManualState()
      if (input.hotSearchKeywordFilter.value.trim() && !input.hotSearchResult.value) {
        void input.searchHotAuditLogs()
      }
      return
    }
    void input.loadData({ forceOptions: true })
  }

  function handleCurrentTableChange(paginationInfo: unknown): void {
    if (input.viewMode.value === 'search') return
    input.handleTableChange(paginationInfo)
  }

  function loadMoreCurrentMobileRecords(): void {
    if (input.viewMode.value === 'search') return
    input.loadMoreMobileRecords()
  }

  function refreshCurrentMobileRecords(): void {
    if (input.viewMode.value === 'search') {
      void input.searchHotAuditLogs()
      return
    }
    input.refreshMobileRecords()
  }

  return {
    applyCurrentMode,
    currentLoading,
    currentMobileHasMore,
    currentMobileLoadingMore,
    currentRecords,
    currentTablePagination,
    handleCurrentTableChange,
    handleViewModeChange,
    loadMoreCurrentMobileRecords,
    refreshCurrentMobileRecords,
    refreshCurrentMode,
    resetCurrentMode,
    toolbarActiveFilterCount,
    toolbarAdvancedFilterCount,
    toolbarFilterTitle,
    toolbarKeyword,
    toolbarSearchPlaceholder
  }
}
