import type { Ref } from 'vue'

export type RuntimeLogTraceSearchViewMode = 'index' | 'grep'

type UseRuntimeLogTraceSearchOptions = {
  clearRouteTraceIdForManualState: () => void
  loadData: () => Promise<unknown>
  resetPagination: () => void
  traceIdFilter: Ref<string>
  viewMode: Ref<RuntimeLogTraceSearchViewMode>
}

export function useRuntimeLogTraceSearch(options: UseRuntimeLogTraceSearchOptions) {
  function searchTrace(traceId?: string): void {
    const text = traceId?.trim()
    if (!text) return
    options.clearRouteTraceIdForManualState()
    options.viewMode.value = 'index'
    options.traceIdFilter.value = text
    options.resetPagination()
    void options.loadData()
  }

  return {
    searchTrace
  }
}
