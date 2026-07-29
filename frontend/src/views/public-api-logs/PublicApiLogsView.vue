<template>
  <a-card class="page-card responsive-page-card">
    <PublicApiLogFilterToolbar
      v-model:trace-id-filter="traceIdFilter"
      v-model:result-filter="resultFilter"
      v-model:source-ref-id-filter="sourceRefIdFilter"
      v-model:path-filter="pathFilter"
      v-model:status-code-filter="statusCodeFilter"
      v-model:client-ip-filter="clientIpFilter"
      v-model:time-range="timeRange"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :loading="loading"
      :result-options="publicApiLogResultOptions"
      @refresh="refreshRecords"
      @reset="resetFilters"
      @search="applyFilters"
    />

    <PublicApiLogList
      :columns="publicApiLogColumns"
      :records="records"
      :loading="loading"
      :mobile-has-more="mobileHasMore"
      :mobile-loading-more="mobileLoadingMore"
      :pagination="tablePagination"
      @change="handleTableChange"
      @detail="openDetail"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    />

    <PublicApiLogDetailDrawer
      v-model:open="detailOpen"
      :detail="detail"
      :loading="detailLoading"
      @close="closeDetail"
    />
  </a-card>
</template>

<script setup lang="ts">
import { computed, onDeactivated, ref, watch } from 'vue'
import axios from 'axios'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type { PublicApiLogListItem, PublicApiLogRenderedDetail, PublicApiLogResultFilter } from '@/types/domain'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { extractApiErrorMessage } from '@/shared/apiError'
import PublicApiLogDetailDrawer from './PublicApiLogDetailDrawer.vue'
import PublicApiLogFilterToolbar from './PublicApiLogFilterToolbar.vue'
import PublicApiLogList from './PublicApiLogList.vue'
import { mergePublicApiLogDetail } from './publicApiLogDetail'
import { mergePublicApiLogListItems } from './publicApiLogPageWindow'
import {
  normalizePublicApiLogStatusCode,
  normalizePublicApiLogTimeRange,
  parseStoredPublicApiLogTimeRange,
  type PublicApiLogTimeRangeValue
} from './publicApiLogFormatters'
import { publicApiLogColumns, publicApiLogResultOptions } from './publicApiLogOptions'

type PublicApiLogsPageState = {
  clientIpFilter: string
  pathFilter: string
  pagination: { current: number; pageSize: number }
  resultFilter: PublicApiLogResultFilter
  sourceRefIdFilter: string
  statusCodeFilter: string
  timeRange?: [string, string]
  traceIdFilter: string
}

const pageSize = 50
const defaultPageState = (): PublicApiLogsPageState => ({
  clientIpFilter: '',
  pathFilter: '',
  pagination: { current: 1, pageSize },
  resultFilter: 'all',
  sourceRefIdFilter: '',
  statusCodeFilter: '',
  timeRange: undefined,
  traceIdFilter: ''
})
const pageStateCache = usePageStateCache<PublicApiLogsPageState>(undefined, defaultPageState, { version: 2 })
const initialState = pageStateCache.read()

const traceIdFilter = ref(initialState.traceIdFilter)
const sourceRefIdFilter = ref(initialState.sourceRefIdFilter)
const pathFilter = ref(initialState.pathFilter)
const statusCodeFilter = ref(initialState.statusCodeFilter)
const clientIpFilter = ref(initialState.clientIpFilter)
const resultFilter = ref<PublicApiLogResultFilter>(initialState.resultFilter)
const timeRange = ref<PublicApiLogTimeRangeValue>(parseStoredPublicApiLogTimeRange(initialState.timeRange))
const detailOpen = ref(false)
const detailLoading = ref(false)
const detail = ref<PublicApiLogRenderedDetail>()
let detailRequestId = 0

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
} = useResponsivePagedList<PublicApiLogListItem>({
  pageSize,
  mergeItems: mergePublicApiLogListItems,
  initialPagination: initialState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条公开接口日志，还有更多`
    : `共 ${total} 条公开接口日志`,
  fetchPage: async (_options, pageState) => {
    const range = normalizePublicApiLogTimeRange(timeRange.value)
    return await api.publicApiLogs.list({
      page: pageState.current,
      pageSize: pageState.pageSize,
      traceId: traceIdFilter.value.trim() || undefined,
      sourceRefId: sourceRefIdFilter.value.trim() || undefined,
      path: pathFilter.value.trim() || undefined,
      statusCode: normalizedStatusCodeFilter.value,
      clientIp: clientIpFilter.value.trim() || undefined,
      result: resultFilter.value,
      startAt: range?.[0].toISOString(),
      endAt: range?.[1].toISOString()
    })
  },
  onError: (error) => {
    console.error(error)
    message.error('加载公开接口日志失败')
  }
})

const activeFilterCount = computed(() => {
  let count = 0
  if (traceIdFilter.value.trim()) count += 1
  if (resultFilter.value !== 'all') count += 1
  if (sourceRefIdFilter.value.trim()) count += 1
  if (pathFilter.value.trim()) count += 1
  if (normalizedStatusCodeFilter.value !== undefined) count += 1
  if (clientIpFilter.value.trim()) count += 1
  if (normalizePublicApiLogTimeRange(timeRange.value)) count += 1
  return count
})
const advancedFilterCount = computed(() => activeFilterCount.value - (traceIdFilter.value.trim() ? 1 : 0) - (resultFilter.value !== 'all' ? 1 : 0))
const normalizedStatusCodeFilter = computed(() => normalizePublicApiLogStatusCode(statusCodeFilter.value))
const hasInvalidStatusCodeFilter = computed(() => statusCodeFilter.value.trim() !== '' && normalizedStatusCodeFilter.value === undefined)

function applyFilters(): void {
  if (!validateStatusCodeFilter()) return
  resetPagination()
  void loadData()
}

function refreshRecords(): void {
  if (!validateStatusCodeFilter()) return
  resetPagination()
  void loadData()
}

function resetFilters(): void {
  const defaults = defaultPageState()
  traceIdFilter.value = defaults.traceIdFilter
  sourceRefIdFilter.value = defaults.sourceRefIdFilter
  pathFilter.value = defaults.pathFilter
  statusCodeFilter.value = defaults.statusCodeFilter
  clientIpFilter.value = defaults.clientIpFilter
  resultFilter.value = defaults.resultFilter
  timeRange.value = parseStoredPublicApiLogTimeRange(defaults.timeRange)
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

async function openDetail(record: PublicApiLogListItem): Promise<void> {
  const requestId = detailRequestId + 1
  detailRequestId = requestId
  detailOpen.value = true
  detailLoading.value = true
  detail.value = undefined
  try {
    const supplement = await api.publicApiLogs.detail(record.id)
    if (requestId === detailRequestId) {
      detail.value = mergePublicApiLogDetail(record, supplement)
    }
  } catch (error) {
    if (requestId !== detailRequestId) return
    console.error(error)
    if (isNotFoundError(error)) {
      closeDetail()
      message.warning('公开接口日志不存在或已被清理，已刷新列表')
      void loadData()
      return
    }
    message.error(extractApiErrorMessage(error, '加载公开接口日志详情失败'))
  } finally {
    if (requestId === detailRequestId) {
      detailLoading.value = false
    }
  }
}

function closeDetail(): void {
  detailRequestId += 1
  detailOpen.value = false
  detailLoading.value = false
  detail.value = undefined
}

function snapshotPageState(): PublicApiLogsPageState {
  const range = normalizePublicApiLogTimeRange(timeRange.value)
  return {
    clientIpFilter: clientIpFilter.value,
    pathFilter: pathFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    resultFilter: resultFilter.value,
    sourceRefIdFilter: sourceRefIdFilter.value,
    statusCodeFilter: statusCodeFilter.value,
    timeRange: range ? [range[0].toISOString(), range[1].toISOString()] : undefined,
    traceIdFilter: traceIdFilter.value
  }
}

function validateStatusCodeFilter(): boolean {
  if (!hasInvalidStatusCodeFilter.value) return true
  message.warning('状态码须为 100-599 的整数')
  return false
}

function isNotFoundError(error: unknown): boolean {
  return axios.isAxiosError(error) && error.response?.status === 404
}

watch(snapshotPageState, () => {
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })

void loadData()
onDeactivated(closeDetail)
</script>
