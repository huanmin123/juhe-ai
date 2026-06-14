<template>
  <a-card class="page-card responsive-page-card">
    <template v-if="viewMode === 'index'">
      <ResponsiveListToolbar
        v-model:keyword="traceIdFilter"
        search-placeholder="搜索 traceId"
        filter-title="日志筛选"
        :active-filter-count="activeFilterCount"
        :advanced-filter-count="advancedFilterCount"
        :refresh-loading="loading"
        @refresh="refreshIndexLogs"
        @reset="resetFilters"
        @search="applyIndexFilters"
      >
        <template #inline-filters>
          <a-select v-model:value="levelFilter" class="toolbar-select log-level-filter responsive-list-inline-filter" :options="levelOptions" @change="applyIndexFilters" />
          <a-select
            v-model:value="eventFilter"
            allow-clear
            show-search
            class="toolbar-select runtime-event-filter responsive-list-inline-filter"
            placeholder="事件"
            :options="eventOptions"
            :filter-option="filterEventOption"
            @change="applyIndexFilters"
          />
        </template>
        <template #advanced-filters>
          <a-form layout="vertical" class="advanced-filter-form">
            <a-form-item label="关键字">
              <a-input v-model:value="keywordFilter" allow-clear placeholder="模糊匹配消息列" @press-enter="applyIndexFilters" />
            </a-form-item>
            <a-form-item label="索引时间范围">
              <a-range-picker
                v-model:value="indexTimeRange"
                allow-clear
                show-time
                class="drawer-range-picker"
                :disabled-date="disabledIndexDate"
                :placeholder="['索引开始时间', '索引结束时间']"
                @change="handleIndexRangeChange"
              />
            </a-form-item>
          </a-form>
        </template>
        <template #actions>
          <TableColumnManager
            :columns="runtimeLogColumns"
            :settings="indexColumnSettings"
            :required-keys="['message']"
            @reset="resetIndexColumnSettings"
            @update:settings="updateIndexColumnSettings"
          />
          <a-segmented v-model:value="viewMode" class="log-mode-segmented" :options="viewModeOptions" @change="handleModeChange" />
        </template>
        <template #filters>
          <a-form layout="vertical">
            <a-form-item label="级别">
              <a-select v-model:value="levelFilter" :options="levelOptions" />
            </a-form-item>
            <a-form-item label="事件">
              <a-select v-model:value="eventFilter" allow-clear show-search :options="eventOptions" :filter-option="filterEventOption" placeholder="选择或输入事件" />
            </a-form-item>
            <a-form-item label="关键字">
              <a-input v-model:value="keywordFilter" allow-clear placeholder="模糊匹配消息列" />
            </a-form-item>
            <a-form-item label="索引时间范围">
              <a-range-picker
                v-model:value="indexTimeRange"
                allow-clear
                show-time
                class="drawer-range-picker"
                :disabled-date="disabledIndexDate"
                :placeholder="['索引开始时间', '索引结束时间']"
                @change="handleIndexRangeChange"
              />
            </a-form-item>
          </a-form>
        </template>
      </ResponsiveListToolbar>

      <RuntimeLogStatusAlerts
        :runtime-logs-alert-visible="runtimeLogsAlertVisible"
        :runtime-logs-alert-description="runtimeLogsAlertDescription"
        :queue-health-alert-visible="queueHealthAlertVisible"
        :queue-health-alert-description="queueHealthAlertDescription"
      />

      <RuntimeLogDataList
        table-class="page-table runtime-log-table"
        :columns="indexManagedColumns"
        :records="records"
        :loading="loading"
        :pagination="tablePagination"
        empty-description="最近 3 天暂无匹配运行日志。可先用 traceId、级别或事件缩小范围。"
        mobile-pagination
        :mobile-has-more="mobileHasMore"
        :loading-more="mobileLoadingMore"
        :refreshing="loading"
        @change="handleTableChange"
        @detail="openRuntimeLogDetail"
        @mobile-load-more="loadMoreMobileRecords"
        @mobile-refresh="refreshMobileRecords"
        @trace="searchTrace"
      />
    </template>

    <template v-else>
      <ResponsiveListToolbar
        v-model:keyword="grepKeywordFilter"
        search-placeholder="后端 rg 搜索任意关键字，空格分隔表示同时命中"
        filter-title="grep 文件范围"
        :active-filter-count="grepActiveFilterCount"
        :refresh-loading="loading"
        @refresh="searchGrepLogs"
        @reset="resetGrepSearch"
        @search="searchGrepLogs"
      >
        <template #inline-filters>
          <a-range-picker
            v-model:value="grepTimeRange"
            :allow-clear="false"
            class="toolbar-select grep-time-range responsive-list-inline-filter"
            show-time
            :title="grepRangeLimitText"
            :disabled-date="disabledGrepDate"
            :placeholder="['文件开始时间', '文件结束时间']"
            @change="handleGrepRangeChange"
          />
        </template>
        <template #actions>
          <TableColumnManager
            :columns="runtimeLogColumns"
            :settings="grepColumnSettings"
            :required-keys="['message']"
            @reset="resetGrepColumnSettings"
            @update:settings="updateGrepColumnSettings"
          />
          <a-segmented v-model:value="viewMode" class="log-mode-segmented" :options="viewModeOptions" @change="handleModeChange" />
        </template>
        <template #filters>
          <a-form layout="vertical">
            <a-form-item label="文件时间范围">
              <a-range-picker
                v-model:value="grepTimeRange"
                :allow-clear="false"
                show-time
                class="drawer-range-picker"
                :title="grepRangeLimitText"
                :disabled-date="disabledGrepDate"
                :placeholder="['文件开始时间', '文件结束时间']"
                @change="handleGrepRangeChange"
              />
            </a-form-item>
          </a-form>
        </template>
      </ResponsiveListToolbar>

      <RuntimeLogStatusAlerts
        :runtime-logs-alert-visible="runtimeLogsAlertVisible"
        :runtime-logs-alert-description="runtimeLogsAlertDescription"
        :queue-health-alert-visible="queueHealthAlertVisible"
        :queue-health-alert-description="queueHealthAlertDescription"
      />

      <a-alert
        v-if="grepResult?.message"
        :type="grepResult.available === false || grepResult.truncated ? 'warning' : 'info'"
        show-icon
        :message="grepResult.message"
        class="grep-alert"
      />

      <RuntimeLogDataList
        table-class="page-table grep-table"
        :columns="grepManagedColumns"
        :records="grepRecords"
        :loading="loading"
        :empty-description="grepKeywordFilter.trim() ? '没有匹配的日志行。' : '输入任意关键字后搜索文件日志。'"
        action-label="查看"
        message-mode="grep"
        :refreshing="loading"
        @detail="openRuntimeGrepDetail"
        @mobile-refresh="searchGrepLogs"
        @trace="searchTrace"
      />
    </template>

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
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import dayjs, { type Dayjs } from 'dayjs'
import { useRoute, useRouter } from 'vue-router'

import { api } from '@/api/client'
import type { RuntimeLogFacets, RuntimeLogGrepItem, RuntimeLogGrepResult, RuntimeLogLevel, RuntimeLogSummary } from '@/types/domain'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { copyTextToClipboard } from '@/shared/clipboard'
import { splitGrepKeywords } from './runtimeLogFormatters'
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
  defaultGrepRange as defaultRuntimeLogGrepRange,
  isDefaultGrepRange as isDefaultRuntimeLogGrepRange,
  isGrepDateDisabled,
  isIndexDateDisabled,
  normalizeGrepRange as normalizeRuntimeLogGrepRange,
  normalizeOptionalTimeRange,
  parseOptionalTimeRange,
  parseStoredGrepRangeWithoutRuntime,
  type RuntimeLogTimeRangeValue
} from './runtimeLogTimeRanges'
import RuntimeLogDataList from './RuntimeLogDataList.vue'
import RuntimeLogDetailDrawer from './RuntimeLogDetailDrawer.vue'
import RuntimeLogStatusAlerts from './RuntimeLogStatusAlerts.vue'
import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'

type RuntimeLogViewMode = 'index' | 'grep'
type RuntimeLogListRecord = RuntimeLogSummary | RuntimeLogGrepItem
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
const initialPageState = pageStateCache.read()
const route = useRoute()
const router = useRouter()
const initialTraceId = routeTraceId()
const effectiveInitialPageState: RuntimeLogsPageState = initialTraceId
  ? { ...defaultRuntimeLogsPageState(), traceIdFilter: initialTraceId }
  : initialPageState

const grepRecords = ref<RuntimeLogGrepItem[]>([])
const grepResult = ref<RuntimeLogGrepResult>()
const facets = ref<RuntimeLogFacets>()
const grepTimeRange = ref<[Dayjs, Dayjs] | undefined>(parseStoredGrepRangeWithoutRuntime(effectiveInitialPageState.grepTimeRange))
const indexTimeRange = ref<RuntimeLogTimeRangeValue>(parseOptionalTimeRange(effectiveInitialPageState.indexTimeRange))
const selectedLog = ref<RuntimeLogSummary>()
const selectedGrepItem = ref<RuntimeLogGrepItem>()
const detailOpen = ref(false)
const grepDetailOpen = ref(false)
let detailRequestId = 0
let grepSearchRequestId = 0
let facetsRequestSeq = 0
const viewMode = ref<RuntimeLogViewMode>(effectiveInitialPageState.viewMode === 'grep' ? 'grep' : 'index')
let skipNextRouteTraceRestore = false

const traceIdFilter = ref(effectiveInitialPageState.traceIdFilter)
const grepKeywordFilter = ref(effectiveInitialPageState.grepKeywordFilter)
const levelFilter = ref<RuntimeLogLevel | 'all'>(effectiveInitialPageState.levelFilter)
const eventFilter = ref<string | undefined>(effectiveInitialPageState.eventFilter)
const keywordFilter = ref(effectiveInitialPageState.keywordFilter)
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
} = useResponsivePagedList<RuntimeLogSummary, { refreshFacets?: boolean }>({
  pageSize,
  initialPagination: effectiveInitialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条运行日志，还有更多`
    : `共 ${total} 条运行日志`,
  fetchPage: async (options, pageState) => {
    const traceId = traceIdFilter.value.trim()
    const range = normalizeOptionalTimeRange(indexTimeRange.value)
    void loadRuntimeLogFacets(options.refreshFacets === true)
    return await api.runtimeLogs.list({
      page: pageState.current,
      pageSize: pageState.pageSize,
      traceId: traceId || undefined,
      level: levelFilter.value,
      event: eventFilter.value || undefined,
      keyword: keywordFilter.value || undefined,
      startAt: range?.[0].toISOString(),
      endAt: range?.[1].toISOString()
    })
  },
  onError: (error) => {
    console.error(error)
    message.error('加载运行日志失败')
  }
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
const grepActiveFilterCount = computed(() => isDefaultGrepRange() ? 0 : 1)

function defaultGrepRange(): [Dayjs, Dayjs] {
  return defaultRuntimeLogGrepRange(grepRuntime.value)
}

function normalizeGrepRange(value?: [Dayjs, Dayjs]): [Dayjs, Dayjs] {
  return normalizeRuntimeLogGrepRange(value, grepRuntime.value)
}

function ensureGrepTimeRange(): [Dayjs, Dayjs] {
  const normalized = grepTimeRange.value ? normalizeGrepRange(grepTimeRange.value) : defaultGrepRange()
  grepTimeRange.value = normalized
  return normalized
}

function isDefaultGrepRange(): boolean {
  return isDefaultRuntimeLogGrepRange(grepTimeRange.value, grepRuntime.value)
}

function disabledGrepDate(current: Dayjs): boolean {
  return isGrepDateDisabled(current, grepRuntime.value)
}

function disabledIndexDate(current: Dayjs): boolean {
  return isIndexDateDisabled(current, facets.value)
}

function handleIndexRangeChange(): void {
  indexTimeRange.value = normalizeOptionalTimeRange(indexTimeRange.value)
  applyIndexFilters()
}

function handleGrepRangeChange(): void {
  grepTimeRange.value = ensureGrepTimeRange()
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
  grepKeywordFilter.value = state.grepKeywordFilter
  grepTimeRange.value = parseStoredGrepRangeWithoutRuntime(state.grepTimeRange)
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

function resetGrepSearch(): void {
  clearRouteTraceIdForManualState()
  grepSearchRequestId += 1
  loading.value = false
  grepKeywordFilter.value = ''
  grepTimeRange.value = defaultGrepRange()
  grepRecords.value = []
  grepResult.value = undefined
  if (!routeTraceId()) {
    pageStateCache.scheduleWrite(snapshotPageState)
  }
}

function filterEventOption(input: string, option?: { label?: string; rawEvent?: string; value?: string }): boolean {
  return filterRuntimeLogEventOption(input, option)
}

function refreshIndexLogs(): void {
  void loadData({ refreshFacets: true })
}

async function loadRuntimeLogFacets(force = false): Promise<void> {
  if (facets.value && !force) return
  const requestSeq = ++facetsRequestSeq
  try {
    const nextFacets = await api.runtimeLogs.facets()
    if (requestSeq !== facetsRequestSeq) return
    facets.value = nextFacets
  } catch (error) {
    if (requestSeq !== facetsRequestSeq) return
    console.error(error)
    message.error('加载运行日志筛选项失败')
  }
}

function cancelRuntimeLogFacetsRequest(): void {
  facetsRequestSeq += 1
}

async function searchGrepLogs(): Promise<void> {
  clearRouteTraceIdForManualState()
  const requestId = ++grepSearchRequestId
  const keywords = splitGrepKeywords(grepKeywordFilter.value)
  if (!keywords.length) {
    loading.value = false
    grepRecords.value = []
    grepResult.value = undefined
    message.warning('请输入要搜索的关键字')
    return
  }

  const range = ensureGrepTimeRange()
  loading.value = true
  try {
    const result = await api.runtimeLogs.grep({
      keywords: keywords.join(' '),
      startAt: range[0].toISOString(),
      endAt: range[1].toISOString(),
      limit: 100
    })
    if (requestId !== grepSearchRequestId) return
    grepResult.value = result
    grepTimeRange.value = normalizeGrepRange([dayjs(result.startAt), dayjs(result.endAt)])
    grepRecords.value = result.items
    if (!result.available) {
      message.warning(result.message || 'grep 模式不可用')
    }
  } catch (error) {
    if (requestId !== grepSearchRequestId) return
    console.error(error)
    message.error('grep 搜索失败')
  } finally {
    if (requestId === grepSearchRequestId) {
      loading.value = false
    }
  }
}

async function openDetail(record: RuntimeLogSummary): Promise<void> {
  const requestId = detailRequestId + 1
  detailRequestId = requestId
  selectedLog.value = record
  detailOpen.value = true
  try {
    const detail = await api.runtimeLogs.detail(record.id)
    if (detailRequestId === requestId) {
      selectedLog.value = detail
    }
  } catch (error) {
    console.error(error)
    message.error('加载运行日志详情失败')
  }
}

function openGrepDetail(record: RuntimeLogGrepItem): void {
  selectedGrepItem.value = record
  grepDetailOpen.value = true
}

function openRuntimeLogDetail(record: RuntimeLogListRecord): void {
  void openDetail(record as RuntimeLogSummary)
}

function openRuntimeGrepDetail(record: RuntimeLogListRecord): void {
  openGrepDetail(record as RuntimeLogGrepItem)
}

function closeTransientDetails(): void {
  detailRequestId += 1
  detailOpen.value = false
  grepDetailOpen.value = false
  selectedLog.value = undefined
  selectedGrepItem.value = undefined
}

function searchTrace(traceId?: string): void {
  const text = traceId?.trim()
  if (!text) return
  clearRouteTraceIdForManualState()
  viewMode.value = 'index'
  traceIdFilter.value = text
  resetPagination()
  void loadData()
}

function copyDetailText(value: string, successMessage?: string): void {
  void copyTextToClipboard(value, successMessage)
}

function applyRouteTraceId(traceId: string): void {
  pageStateCache.flushPendingWrite()
  applyPageState({ ...defaultRuntimeLogsPageState(), traceIdFilter: traceId })
  resetPagination()
  void loadData()
}

function restorePageStateAfterRouteTraceCleared(): void {
  applyPageState(pageStateCache.read())
  loadCurrentRuntimeLogState({ refreshFacets: true })
}

function routeTraceId(): string | undefined {
  return trimmedRouteQueryValue(route.query.traceId)
}

function clearRouteTraceIdForManualState(): void {
  if (!routeTraceId()) return
  skipNextRouteTraceRestore = true
  void removeRouteTraceIdQuery(router, route).catch((error) => {
    skipNextRouteTraceRestore = false
    console.error(error)
  })
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

watch(snapshotPageState, () => {
  if (routeTraceId()) {
    pageStateCache.cancelPendingWrite()
    return
  }
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })
watch(
  () => route.query.traceId,
  () => {
    const traceId = routeTraceId()
    if (!traceId) {
      if (skipNextRouteTraceRestore) {
        skipNextRouteTraceRestore = false
        pageStateCache.scheduleWrite(snapshotPageState)
        return
      }
      restorePageStateAfterRouteTraceCleared()
      return
    }
    if (traceId === traceIdFilter.value.trim()) return
    applyRouteTraceId(traceId)
  }
)

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

<style scoped>
.log-mode-segmented {
  flex: none;
}

.runtime-event-filter {
  width: 210px;
}

.log-level-filter {
  width: 108px;
}

.log-keyword-filter {
  width: 240px;
}

.index-time-range {
  width: 360px;
}

.grep-time-range {
  width: 380px;
}

.drawer-range-picker {
  width: 100%;
}

.advanced-filter-form :deep(.ant-input),
.advanced-filter-form :deep(.ant-picker) {
  width: 100%;
}

.grep-alert {
  margin-bottom: 14px;
}

</style>
