<template>
  <a-card class="page-card responsive-page-card">
    <template v-if="viewMode === 'index'">
      <ResponsiveListToolbar
        v-model:keyword="traceIdFilter"
        search-placeholder="搜索 traceId"
        filter-title="日志筛选"
        :active-filter-count="activeFilterCount"
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
          <a-input v-model:value="keywordFilter" allow-clear class="toolbar-select log-keyword-filter responsive-list-inline-filter" placeholder="消息关键字" @press-enter="applyIndexFilters" />
          <a-range-picker
            v-model:value="indexTimeRange"
            allow-clear
            class="toolbar-select index-time-range responsive-list-inline-filter"
            show-time
            :disabled-date="disabledIndexDate"
            :placeholder="['索引开始时间', '索引结束时间']"
            @change="handleIndexRangeChange"
          />
        </template>
        <template #actions>
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

      <RuntimeAvailabilityAlert
        :visible="runtimeLogsAlertVisible"
        message="日志运行态暂时不可观测"
        :description="runtimeLogsAlertDescription"
      />

      <RuntimeLogDataList
        table-class="page-table runtime-log-table"
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

      <RuntimeAvailabilityAlert
        :visible="runtimeLogsAlertVisible"
        message="日志运行态暂时不可观测"
        :description="runtimeLogsAlertDescription"
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

    <a-drawer v-model:open="detailOpen" width="min(920px, 96vw)" title="运行日志详情" :body-style="{ padding: '18px' }">
      <template v-if="selectedLog">
        <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
          <a-descriptions-item label="时间">{{ formatDateTime(selectedLog.time) }}</a-descriptions-item>
          <a-descriptions-item label="级别">{{ levelText(selectedLog.level) }}</a-descriptions-item>
          <a-descriptions-item label="traceId" :span="2">{{ selectedLog.traceId ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="事件">{{ eventText(selectedLog.event) }}</a-descriptions-item>
          <a-descriptions-item v-if="selectedLog.event" label="事件原值">{{ selectedLog.event }}</a-descriptions-item>
          <a-descriptions-item label="消息" :span="2">{{ runtimeLogMessageText(selectedLog) }}</a-descriptions-item>
        </a-descriptions>
        <pre class="raw-block">{{ prettyRawJson(selectedLog.rawJson) }}</pre>
      </template>
    </a-drawer>

    <a-drawer v-model:open="grepDetailOpen" width="min(920px, 96vw)" title="grep 匹配行" :body-style="{ padding: '18px' }">
      <template v-if="selectedGrepItem">
        <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
          <a-descriptions-item label="时间">{{ formatDateTime(selectedGrepItem.time) }}</a-descriptions-item>
          <a-descriptions-item label="级别">{{ levelText(selectedGrepItem.level) }}</a-descriptions-item>
          <a-descriptions-item label="traceId" :span="2">{{ selectedGrepItem.traceId ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="事件">{{ eventText(selectedGrepItem.event) }}</a-descriptions-item>
          <a-descriptions-item v-if="selectedGrepItem.event" label="事件原值">{{ selectedGrepItem.event }}</a-descriptions-item>
          <a-descriptions-item label="消息">{{ runtimeLogMessageText(selectedGrepItem) }}</a-descriptions-item>
          <a-descriptions-item label="文件">{{ selectedGrepItem.fileName || selectedGrepItem.file }}</a-descriptions-item>
          <a-descriptions-item label="位置">{{ grepLinePositionText(selectedGrepItem) }}</a-descriptions-item>
          <a-descriptions-item label="完整路径" :span="2">{{ selectedGrepItem.file }}</a-descriptions-item>
        </a-descriptions>
        <pre class="raw-block">{{ prettyRawJson(selectedGrepItem.rawJson || selectedGrepItem.line) }}</pre>
      </template>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onDeactivated, onMounted, ref, watch } from 'vue'
import dayjs, { type Dayjs } from 'dayjs'
import { useRoute, useRouter } from 'vue-router'

import { api } from '@/api/client'
import type { RuntimeLogFacets, RuntimeLogGrepItem, RuntimeLogGrepResult, RuntimeLogLevel, RuntimeLogSummary } from '@/types/domain'
import { formatDateTime } from '@/shared/formatters'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RuntimeAvailabilityAlert from '@/components/RuntimeAvailabilityAlert.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import {
  eventText,
  grepLinePositionText,
  levelText,
  prettyRawJson,
  runtimeLogMessageText,
  splitGrepKeywords
} from './runtimeLogFormatters'
import {
  runtimeLogLevelOptions,
  runtimeLogViewModeOptions
} from './runtimeLogTableColumns'
import RuntimeLogDataList from './RuntimeLogDataList.vue'
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
type RuntimeLogTimeRangeValue = [Dayjs | null | undefined, Dayjs | null | undefined] | null | undefined
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
    const facetsRequest = options.refreshFacets === true || !facets.value
      ? api.runtimeLogs.facets()
      : Promise.resolve(facets.value)
    const [result, nextFacets] = await Promise.all([
      api.runtimeLogs.list({
        page: pageState.current,
        pageSize: pageState.pageSize,
        traceId: traceId || undefined,
        level: levelFilter.value,
        event: eventFilter.value || undefined,
        keyword: keywordFilter.value || undefined,
        startAt: range?.[0].toISOString(),
        endAt: range?.[1].toISOString()
      }),
      facetsRequest
    ])
    if (nextFacets) {
      facets.value = nextFacets
    }
    return result
  },
  onError: (error) => {
    console.error(error)
    message.error('加载运行日志失败')
  }
})

const viewModeOptions = runtimeLogViewModeOptions
const levelOptions = runtimeLogLevelOptions

const eventOptions = computed(() => (facets.value?.events ?? []).map((event) => ({ label: eventText(event), value: event, rawEvent: event })))
const grepRuntime = computed(() => facets.value?.grep)
const grepRangeLimitText = computed(() => {
  const runtime = grepRuntime.value
  if (!runtime) return '按文件时间筛选，默认最近 3 天，单次最多 7 天'
  return `按文件时间筛选，默认最近 ${runtime.defaultRangeDays} 天，单次最多 ${runtime.maxRangeDays} 天`
})
const runtimeLogsAlertVisible = computed(() => Boolean(facets.value && (
  !facets.value.runtimeAvailable
  || !facets.value.workerSnapshotAvailable
  || !facets.value.runtimeLogIndexQueueAvailable
  || !facets.value.dbService.statusAvailable
  || !facets.value.dbService.stateAvailable
  || !facets.value.gatewayAccountSideEffectsAvailable
)))
const runtimeLogsAlertDescription = computed(() => {
  const info = facets.value
  if (!info) return ''
  const reasons: string[] = []
  if (!info.runtimeAvailable) {
    reasons.push('服务运行态不可用')
  } else {
    if (!info.workerSnapshotAvailable) reasons.push('后台进程快照不可用')
    if (!info.runtimeLogIndexQueueAvailable) reasons.push('运行日志索引队列不可用')
    if (!info.gatewayAccountSideEffectsAvailable) reasons.push('网关账户副作用状态不可用')
  }
  if (!info.dbService.statusAvailable) {
    reasons.push('本地数据库服务状态不可用')
  } else if (!info.dbService.stateAvailable) {
    reasons.push('本地数据库服务父进程状态不可用')
  }
  return `${reasons.join('；') || '运行态状态未知'}。`
})

const activeFilterCount = computed(() => {
  let count = 0
  if (traceIdFilter.value.trim()) count += 1
  if (levelFilter.value !== 'all') count += 1
  if (eventFilter.value) count += 1
  if (keywordFilter.value.trim()) count += 1
  if (normalizeOptionalTimeRange(indexTimeRange.value)) count += 1
  return count
})
const grepActiveFilterCount = computed(() => isDefaultGrepRange() ? 0 : 1)

function parseStoredGrepRangeWithoutRuntime(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return start.isValid() && end.isValid() ? [start, end] : undefined
}

function defaultGrepRange(): [Dayjs, Dayjs] {
  const runtime = grepRuntime.value
  const end = dayjs(runtime?.defaultEndAt ?? new Date())
  const start = dayjs(runtime?.defaultStartAt ?? end.subtract(3, 'day'))
  return normalizeGrepRange([start, end])
}

function normalizeGrepRange(value?: [Dayjs, Dayjs]): [Dayjs, Dayjs] {
  const runtime = grepRuntime.value
  const now = dayjs()
  const earliest = runtime?.earliestFileTime ? dayjs(runtime.earliestFileTime) : now.subtract(runtime?.fileRetentionDays ?? 30, 'day')
  const maxRangeDays = runtime?.maxRangeDays ?? 7
  let end = value?.[1]?.isValid() ? value[1] : now
  if (end.isAfter(now)) end = now
  if (end.isBefore(earliest)) end = earliest

  let start = value?.[0]?.isValid() ? value[0] : end.subtract(runtime?.defaultRangeDays ?? 3, 'day')
  if (start.isBefore(earliest)) start = earliest
  if (start.isAfter(end)) start = end.subtract(runtime?.defaultRangeDays ?? 3, 'day')
  if (end.diff(start, 'millisecond') > maxRangeDays * 24 * 60 * 60 * 1000) {
    start = end.subtract(maxRangeDays, 'day')
  }
  if (start.isBefore(earliest)) start = earliest
  return [start, end]
}

function ensureGrepTimeRange(): [Dayjs, Dayjs] {
  const normalized = grepTimeRange.value ? normalizeGrepRange(grepTimeRange.value) : defaultGrepRange()
  grepTimeRange.value = normalized
  return normalized
}

function isDefaultGrepRange(): boolean {
  const range = grepTimeRange.value
  if (!range) return true
  const defaults = defaultGrepRange()
  return Math.abs(range[0].diff(defaults[0], 'minute')) <= 1
    && Math.abs(range[1].diff(defaults[1], 'minute')) <= 1
}

function disabledGrepDate(current: Dayjs): boolean {
  const runtime = grepRuntime.value
  const earliest = runtime?.earliestFileTime ? dayjs(runtime.earliestFileTime).startOf('day') : dayjs().subtract(runtime?.fileRetentionDays ?? 30, 'day').startOf('day')
  return current.isBefore(earliest, 'day') || current.isAfter(dayjs(), 'day')
}

function disabledIndexDate(current: Dayjs): boolean {
  const earliest = facets.value?.earliestIndexedAt ? dayjs(facets.value.earliestIndexedAt).startOf('day') : dayjs().subtract(facets.value?.retentionDays ?? 3, 'day').startOf('day')
  return current.isBefore(earliest, 'day') || current.isAfter(dayjs(), 'day')
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
  const keyword = input.trim().toLowerCase()
  if (!keyword) return true
  return [option?.label, option?.rawEvent, option?.value].some((item) => String(item ?? '').toLowerCase().includes(keyword))
}

function refreshIndexLogs(): void {
  void loadData({ refreshFacets: true })
}

async function loadRuntimeLogFacets(): Promise<void> {
  if (facets.value) return
  try {
    facets.value = await api.runtimeLogs.facets()
  } catch (error) {
    console.error(error)
    message.error('加载运行日志筛选项失败')
  }
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

function parseOptionalTimeRange(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return normalizeOptionalTimeRange(start.isValid() && end.isValid() ? [start, end] : undefined)
}

function normalizeOptionalTimeRange(value: RuntimeLogTimeRangeValue): [Dayjs, Dayjs] | undefined {
  const start = value?.[0]
  const end = value?.[1]
  if (!start?.isValid() || !end?.isValid()) return undefined
  return start.isAfter(end) ? [end, start] : [start, end]
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

onDeactivated(closeTransientDetails)
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

.grep-alert {
  margin-bottom: 14px;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.raw-block {
  max-height: 520px;
  margin: 0;
  padding: 12px;
  overflow: auto;
  color: #0f172a;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
