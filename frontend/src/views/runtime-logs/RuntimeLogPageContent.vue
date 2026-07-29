<template>
  <RuntimeLogFilterToolbar
    :active-filter-count="activeFilterCount"
    :advanced-filter-count="advancedFilterCount"
    :disabled-grep-date="disabledGrepDate"
    :disabled-index-date="disabledIndexDate"
    :event-filter="eventFilter"
    :event-options="eventOptions"
    :filter-event-option="filterEventOption"
    :grep-active-filter-count="grepActiveFilterCount"
    :grep-column-settings="grepColumnSettings"
    :grep-keyword-filter="grepKeywordFilter"
    :grep-range-limit-text="grepRangeLimitText"
    :grep-time-range="grepTimeRange"
    :index-column-settings="indexColumnSettings"
    :index-time-range="indexTimeRange"
    :keyword-filter="keywordFilter"
    :level-filter="levelFilter"
    :level-options="levelOptions"
    :loading="loading"
    :runtime-log-columns="runtimeLogColumns"
    :trace-id-filter="traceIdFilter"
    :view-mode="viewMode"
    :view-mode-options="viewModeOptions"
    @apply-index="emit('applyIndex')"
    @facets-open="emit('facetsOpen')"
    @grep-range-change="emit('grepRangeChange')"
    @index-range-change="emit('indexRangeChange')"
    @mode-change="emit('modeChange', $event)"
    @refresh-index="emit('refreshIndex')"
    @reset-grep="emit('resetGrep')"
    @reset-grep-column-settings="emit('resetGrepColumnSettings')"
    @reset-index="emit('resetIndex')"
    @reset-index-column-settings="emit('resetIndexColumnSettings')"
    @search-grep="emit('searchGrep')"
    @update:event-filter="emit('update:eventFilter', $event)"
    @update:grep-column-settings="emit('update:grepColumnSettings', $event)"
    @update:grep-keyword-filter="emit('update:grepKeywordFilter', $event)"
    @update:grep-time-range="emit('update:grepTimeRange', $event)"
    @update:index-column-settings="emit('update:indexColumnSettings', $event)"
    @update:index-time-range="emit('update:indexTimeRange', $event)"
    @update:keyword-filter="emit('update:keywordFilter', $event)"
    @update:level-filter="emit('update:levelFilter', $event)"
    @update:trace-id-filter="emit('update:traceIdFilter', $event)"
    @update:view-mode="emit('update:viewMode', $event)"
  />

  <RuntimeLogListSection
    :grep-columns="grepColumns"
    :grep-keyword-filter="grepKeywordFilter"
    :grep-records="grepRecords"
    :grep-result="grepResult"
    :index-columns="indexColumns"
    :loading="loading"
    :mobile-has-more="mobileHasMore"
    :mobile-loading-more="mobileLoadingMore"
    :pagination="pagination"
    :records="records"
    :view-mode="viewMode"
    @change="emit('change', $event)"
    @grep-detail="emit('grepDetail', $event)"
    @grep-mobile-refresh="emit('grepMobileRefresh')"
    @index-detail="emit('indexDetail', $event)"
    @index-mobile-refresh="emit('indexMobileRefresh')"
    @mobile-load-more="emit('mobileLoadMore')"
    @trace="emit('trace', $event)"
  />
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'

import type { TableColumnSetting } from '@/components/tableColumnSettings'
import type { RuntimeLogGrepItem, RuntimeLogGrepResult, RuntimeLogLevel, RuntimeLogSummary } from '@/types/domain'
import type { RuntimeLogEventOption } from './runtimeLogFacets'
import type { RuntimeLogTimeRangeValue } from './runtimeLogTimeRanges'
import RuntimeLogFilterToolbar from './RuntimeLogFilterToolbar.vue'
import RuntimeLogListSection from './RuntimeLogListSection.vue'

type RuntimeLogViewMode = 'index' | 'grep'
type RuntimeLogLevelFilter = RuntimeLogLevel | 'all'
type RuntimeLogListRecord = RuntimeLogSummary | RuntimeLogGrepItem
type RuntimeLogOption<T extends string = string> = {
  label: string
  value: T
}

defineProps<{
  activeFilterCount: number
  advancedFilterCount: number
  disabledGrepDate: (current: Dayjs) => boolean
  disabledIndexDate: (current: Dayjs) => boolean
  eventFilter?: string
  eventOptions: RuntimeLogEventOption[]
  filterEventOption: (input: string, option?: { label?: string; rawEvent?: string; value?: string }) => boolean
  grepActiveFilterCount: number
  grepColumnSettings: TableColumnSetting[]
  grepColumns: Array<Record<string, any>>
  grepKeywordFilter: string
  grepRangeLimitText: string
  grepRecords: RuntimeLogGrepItem[]
  grepResult?: RuntimeLogGrepResult
  grepTimeRange?: [Dayjs, Dayjs]
  indexColumnSettings: TableColumnSetting[]
  indexColumns: Array<Record<string, any>>
  indexTimeRange: RuntimeLogTimeRangeValue
  keywordFilter: string
  levelFilter: RuntimeLogLevelFilter
  levelOptions: RuntimeLogOption[]
  loading: boolean
  mobileHasMore: boolean
  mobileLoadingMore: boolean
  pagination?: false | Record<string, unknown>
  records: RuntimeLogSummary[]
  runtimeLogColumns: Array<Record<string, any>>
  traceIdFilter: string
  viewMode: RuntimeLogViewMode
  viewModeOptions: RuntimeLogOption[]
}>()

const emit = defineEmits<{
  (event: 'applyIndex'): void
  (event: 'change', paginationInfo: unknown): void
  (event: 'facetsOpen'): void
  (event: 'grepDetail', record: RuntimeLogListRecord): void
  (event: 'grepMobileRefresh'): void
  (event: 'grepRangeChange'): void
  (event: 'indexDetail', record: RuntimeLogListRecord): void
  (event: 'indexMobileRefresh'): void
  (event: 'indexRangeChange'): void
  (event: 'mobileLoadMore'): void
  (event: 'modeChange', value: string | number): void
  (event: 'refreshIndex'): void
  (event: 'resetGrep'): void
  (event: 'resetGrepColumnSettings'): void
  (event: 'resetIndex'): void
  (event: 'resetIndexColumnSettings'): void
  (event: 'searchGrep'): void
  (event: 'trace', traceId?: string): void
  (event: 'update:eventFilter', value?: string): void
  (event: 'update:grepColumnSettings', value: TableColumnSetting[]): void
  (event: 'update:grepKeywordFilter', value: string): void
  (event: 'update:grepTimeRange', value?: [Dayjs, Dayjs]): void
  (event: 'update:indexColumnSettings', value: TableColumnSetting[]): void
  (event: 'update:indexTimeRange', value: RuntimeLogTimeRangeValue): void
  (event: 'update:keywordFilter', value: string): void
  (event: 'update:levelFilter', value: RuntimeLogLevelFilter): void
  (event: 'update:traceIdFilter', value: string): void
  (event: 'update:viewMode', value: RuntimeLogViewMode): void
}>()
</script>
