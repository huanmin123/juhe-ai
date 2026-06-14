<template>
  <a-card class="page-card responsive-page-card">
    <RuntimeLogPageContent
      v-model:event-filter="eventFilter"
      v-model:grep-column-settings="grepColumnSettings"
      v-model:grep-keyword-filter="grepKeywordFilter"
      v-model:grep-time-range="grepTimeRange"
      v-model:index-column-settings="indexColumnSettings"
      v-model:index-time-range="indexTimeRange"
      v-model:keyword-filter="keywordFilter"
      v-model:level-filter="levelFilter"
      v-model:trace-id-filter="traceIdFilter"
      v-model:view-mode="viewMode"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :disabled-grep-date="disabledGrepDate"
      :disabled-index-date="disabledIndexDate"
      :event-options="eventOptions"
      :filter-event-option="filterEventOption"
      :grep-active-filter-count="grepActiveFilterCount"
      :grep-columns="grepManagedColumns"
      :grep-range-limit-text="grepRangeLimitText"
      :grep-records="grepRecords"
      :grep-result="grepResult"
      :index-columns="indexManagedColumns"
      :level-options="levelOptions"
      :loading="loading"
      :mobile-has-more="mobileHasMore"
      :mobile-loading-more="mobileLoadingMore"
      :pagination="tablePagination"
      :queue-health-alert-description="queueHealthAlertDescription"
      :queue-health-alert-visible="queueHealthAlertVisible"
      :records="records"
      :runtime-log-columns="runtimeLogColumns"
      :runtime-logs-alert-description="runtimeLogsAlertDescription"
      :runtime-logs-alert-visible="runtimeLogsAlertVisible"
      :view-mode-options="viewModeOptions"
      @apply-index="applyIndexFilters"
      @change="handleTableChange"
      @grep-detail="openRuntimeGrepDetail"
      @grep-mobile-refresh="searchGrepLogs"
      @grep-range-change="handleGrepRangeChange"
      @index-detail="openRuntimeLogDetail"
      @index-mobile-refresh="refreshMobileRecords"
      @index-range-change="handleIndexRangeChange"
      @mobile-load-more="loadMoreMobileRecords"
      @mode-change="handleModeChange"
      @refresh-index="refreshIndexLogs"
      @reset-grep="resetGrepSearch"
      @reset-grep-column-settings="resetGrepColumnSettings"
      @reset-index="resetFilters"
      @reset-index-column-settings="resetIndexColumnSettings"
      @search-grep="searchGrepLogs"
      @trace="searchTrace"
      @update:grep-column-settings="updateGrepColumnSettings"
      @update:index-column-settings="updateIndexColumnSettings"
    />

    <RuntimeLogDetailDrawer
      v-model:grep-open="grepDetailOpen"
      v-model:index-open="detailOpen"
      :grep-item="selectedGrepItem"
      :log="selectedLog"
      @copy-text="copyDetailText"
      @search-trace="searchTrace"
    />
  </a-card>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import type { Dayjs } from 'dayjs'
import { useRoute, useRouter } from 'vue-router'

import type { RuntimeLogLevel } from '@/types/domain'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { copyTextToClipboard } from '@/shared/clipboard'
import {
  buildRuntimeLogEventOptions,
  filterRuntimeLogEventOption,
  isRuntimeLogQueueHealthAlertVisible,
  isRuntimeLogsAlertVisible,
  runtimeLogGrepRangeLimitText,
  runtimeLogQueueHealthAlertDescription,
  runtimeLogsAlertDescription as buildRuntimeLogsAlertDescription
} from './runtimeLogFacets'
import {
  runtimeLogColumns,
  runtimeLogLevelOptions,
  runtimeLogViewModeOptions
} from './runtimeLogTableColumns'
import {
  isIndexDateDisabled,
  normalizeOptionalTimeRange,
  parseOptionalTimeRange,
  type RuntimeLogTimeRangeValue
} from './runtimeLogTimeRanges'
import RuntimeLogDetailDrawer from './RuntimeLogDetailDrawer.vue'
import RuntimeLogPageContent from './RuntimeLogPageContent.vue'
import {
  resolveRuntimeLogInitialPageState,
  useRuntimeLogRouteTraceState
} from './useRuntimeLogRouteTraceState'
import { useRuntimeLogGrepSearchState } from './useRuntimeLogGrepSearchState'
import { useRuntimeLogFacetsState } from './useRuntimeLogFacetsState'
import { useRuntimeLogDetailState } from './useRuntimeLogDetailState'
import { useRuntimeLogIndexSearchState } from './useRuntimeLogIndexSearchState'
import { useRuntimeLogTraceSearch, type RuntimeLogTraceSearchViewMode } from './useRuntimeLogTraceSearch'

type RuntimeLogViewMode = RuntimeLogTraceSearchViewMode
type RuntimeLogsPageState = {
  eventFilter?: string
  grepKeywordFilter: string
  grepTimeRange?: [string, string]
  indexTimeRange?: [string, string]
  keywordFilter: string
  levelFilter: RuntimeLogLevel | 'all'
  pagination: { current: number; pageSize: number }
  traceIdFilter: string
  viewMode: RuntimeLogViewMode
}
const pageSize = 100
const defaultRuntimeLogsPageState = (): RuntimeLogsPageState => {
  return {
    eventFilter: undefined,
    grepKeywordFilter: '',
    grepTimeRange: undefined,
    indexTimeRange: undefined,
    keywordFilter: '',
    levelFilter: 'all',
    pagination: { current: 1, pageSize },
    traceIdFilter: '',
    viewMode: 'index'
  }
}
const pageStateCache = usePageStateCache<RuntimeLogsPageState>(undefined, defaultRuntimeLogsPageState, { version: 5 })
const route = useRoute()
const router = useRouter()
const effectiveInitialPageState = resolveRuntimeLogInitialPageState<RuntimeLogsPageState>({
  defaultPageState: defaultRuntimeLogsPageState,
  pageStateCache,
  route,
  withTraceId: (state, traceId) => ({ ...state, traceIdFilter: traceId })
})

const {
  cancelRuntimeLogFacetsRequest,
  facets,
  loadRuntimeLogFacets
} = useRuntimeLogFacetsState()
const indexTimeRange = ref<RuntimeLogTimeRangeValue>(parseOptionalTimeRange(effectiveInitialPageState.indexTimeRange))
const {
  closeTransientDetails,
  detailOpen,
  grepDetailOpen,
  openRuntimeGrepDetail,
  openRuntimeLogDetail,
  selectedGrepItem,
  selectedLog
} = useRuntimeLogDetailState()
const viewMode = ref<RuntimeLogViewMode>(effectiveInitialPageState.viewMode === 'grep' ? 'grep' : 'index')
const traceIdFilter = ref(effectiveInitialPageState.traceIdFilter)
const levelFilter = ref<RuntimeLogLevel | 'all'>(effectiveInitialPageState.levelFilter)
const eventFilter = ref<string | undefined>(effectiveInitialPageState.eventFilter)
const keywordFilter = ref(effectiveInitialPageState.keywordFilter)
const {
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
} = useRuntimeLogIndexSearchState({
  eventFilter,
  indexTimeRange,
  initialPagination: effectiveInitialPageState.pagination,
  keywordFilter,
  levelFilter,
  loadRuntimeLogFacets,
  pageSize,
  traceIdFilter
})

const viewModeOptions = runtimeLogViewModeOptions
const levelOptions = runtimeLogLevelOptions
const {
  managedColumns: indexManagedColumns,
  columnSettings: indexColumnSettings,
  updateColumnSettings: updateIndexColumnSettings,
  resetColumnSettings: resetIndexColumnSettings
} = useTableColumnSettings('runtime-logs:index', runtimeLogColumns, {
  requiredKeys: ['message'],
  minVisible: 1
})
const {
  managedColumns: grepManagedColumns,
  columnSettings: grepColumnSettings,
  updateColumnSettings: updateGrepColumnSettings,
  resetColumnSettings: resetGrepColumnSettings
} = useTableColumnSettings('runtime-logs:grep', runtimeLogColumns, {
  requiredKeys: ['message'],
  minVisible: 1
})

const eventOptions = computed(() => buildRuntimeLogEventOptions(facets.value?.events))
const grepRuntime = computed(() => facets.value?.grep)
const grepRangeLimitText = computed(() => runtimeLogGrepRangeLimitText(grepRuntime.value))
const runtimeLogsAlertVisible = computed(() => isRuntimeLogsAlertVisible(facets.value))
const runtimeLogsAlertDescription = computed(() => buildRuntimeLogsAlertDescription(facets.value))
const queueHealthAlertVisible = computed(() => isRuntimeLogQueueHealthAlertVisible(facets.value))
const queueHealthAlertDescription = computed(() => runtimeLogQueueHealthAlertDescription(facets.value))

const activeFilterCount = computed(() => {
  let count = 0
  if (traceIdFilter.value.trim()) count += 1
  if (levelFilter.value !== 'all') count += 1
  if (eventFilter.value) count += 1
  if (keywordFilter.value.trim()) count += 1
  if (normalizeOptionalTimeRange(indexTimeRange.value)) count += 1
  return count
})
const advancedFilterCount = computed(() => {
  let count = 0
  if (keywordFilter.value.trim()) count += 1
  if (normalizeOptionalTimeRange(indexTimeRange.value)) count += 1
  return count
})

const {
  applyGrepSearchState,
  defaultGrepRange,
  disabledGrepDate,
  grepActiveFilterCount,
  grepKeywordFilter,
  grepRecords,
  grepResult,
  grepTimeRange,
  handleGrepRangeChange,
  normalizeGrepRange,
  resetGrepSearch,
  searchGrepLogs
} = useRuntimeLogGrepSearchState({
  clearRouteTraceIdForManualState,
  grepRuntime,
  initialKeywordFilter: effectiveInitialPageState.grepKeywordFilter,
  initialTimeRange: effectiveInitialPageState.grepTimeRange,
  isRouteTraceActive: () => Boolean(routeTraceId()),
  loading,
  schedulePageStateWrite: () => pageStateCache.scheduleWrite(snapshotPageState)
})

function disabledIndexDate(current: Dayjs): boolean {
  return isIndexDateDisabled(current, facets.value)
}

function handleIndexRangeChange(): void {
  indexTimeRange.value = normalizeOptionalTimeRange(indexTimeRange.value)
  applyIndexFilters()
}

function handleModeChange(value: string | number): void {
  const nextMode: RuntimeLogViewMode = value === 'grep' ? 'grep' : 'index'
  clearRouteTraceIdForManualState()
  viewMode.value = nextMode
  if (nextMode === 'index') {
    if (!records.value.length) {
      void loadData()
    }
    return
  }
  void loadRuntimeLogFacets().then(() => {
    grepTimeRange.value = grepTimeRange.value ? normalizeGrepRange(grepTimeRange.value) : defaultGrepRange()
    if (grepKeywordFilter.value.trim()) {
      void searchGrepLogs()
    }
  })
}

function applyIndexFilters(): void {
  clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

function applyPageState(state: RuntimeLogsPageState): void {
  viewMode.value = state.viewMode === 'grep' ? 'grep' : 'index'
  traceIdFilter.value = state.traceIdFilter
  applyGrepSearchState(state)
  levelFilter.value = state.levelFilter
  eventFilter.value = state.eventFilter
  keywordFilter.value = state.keywordFilter
  indexTimeRange.value = parseOptionalTimeRange(state.indexTimeRange)
  pagination.current = state.pagination.current
  pagination.pageSize = state.pagination.pageSize
}

function resetFilters(): void {
  clearRouteTraceIdForManualState()
  const defaults = defaultRuntimeLogsPageState()
  traceIdFilter.value = defaults.traceIdFilter
  levelFilter.value = defaults.levelFilter
  eventFilter.value = defaults.eventFilter
  keywordFilter.value = defaults.keywordFilter
  indexTimeRange.value = parseOptionalTimeRange(defaults.indexTimeRange)
  resetPagination()
  pageStateCache.clear()
  void loadData({ refreshFacets: true })
}

function filterEventOption(input: string, option?: { label?: string; rawEvent?: string; value?: string }): boolean {
  return filterRuntimeLogEventOption(input, option)
}

function refreshIndexLogs(): void {
  void loadData({ refreshFacets: true })
}

const { searchTrace } = useRuntimeLogTraceSearch({
  clearRouteTraceIdForManualState,
  loadData,
  resetPagination,
  traceIdFilter,
  viewMode
})

function copyDetailText(value: string, successMessage?: string): void {
  void copyTextToClipboard(value, successMessage)
}

function routeTraceId(): string | undefined {
  return runtimeLogRouteTraceState.currentRouteTraceId()
}

function clearRouteTraceIdForManualState(): void {
  runtimeLogRouteTraceState.clearRouteTraceIdForManualState()
}

function snapshotPageState(): RuntimeLogsPageState {
  const range = normalizeOptionalTimeRange(indexTimeRange.value)
  return {
    eventFilter: eventFilter.value,
    grepKeywordFilter: grepKeywordFilter.value,
    grepTimeRange: grepTimeRange.value ? [grepTimeRange.value[0].toISOString(), grepTimeRange.value[1].toISOString()] : undefined,
    indexTimeRange: range ? [range[0].toISOString(), range[1].toISOString()] : undefined,
    keywordFilter: keywordFilter.value,
    levelFilter: levelFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    traceIdFilter: traceIdFilter.value,
    viewMode: viewMode.value
  }
}

const runtimeLogRouteTraceState = useRuntimeLogRouteTraceState<RuntimeLogsPageState>({
  applyPageState,
  defaultPageState: defaultRuntimeLogsPageState,
  getCurrentTraceIdFilter: () => traceIdFilter.value,
  loadRestoredPageState: () => loadCurrentRuntimeLogState({ refreshFacets: true }),
  loadRouteTraceState: () => {
    void loadData()
  },
  pageStateCache,
  resetPagination,
  route,
  router,
  snapshotPageState,
  withTraceId: (state, traceId) => ({ ...state, traceIdFilter: traceId })
})

function loadCurrentRuntimeLogState(options: { refreshFacets?: boolean } = {}): void {
  if (viewMode.value === 'grep') {
    void loadRuntimeLogFacets().then(() => {
      grepTimeRange.value = grepTimeRange.value ? normalizeGrepRange(grepTimeRange.value) : defaultGrepRange()
      if (grepKeywordFilter.value.trim()) {
        void searchGrepLogs()
      }
    })
    return
  }
  void loadData({ refreshFacets: options.refreshFacets === true }).then(() => {
    grepTimeRange.value = grepTimeRange.value ? normalizeGrepRange(grepTimeRange.value) : defaultGrepRange()
  })
}

onMounted(loadCurrentRuntimeLogState)

onDeactivated(() => {
  closeTransientDetails()
  cancelRuntimeLogFacetsRequest()
})
onBeforeUnmount(cancelRuntimeLogFacetsRequest)
</script>
