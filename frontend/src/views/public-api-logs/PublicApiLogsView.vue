<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="traceIdFilter"
      search-placeholder="搜索 traceId"
      filter-title="公开接口筛选"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :refresh-loading="loading"
      @refresh="refreshRecords"
      @reset="resetFilters"
      @search="applyFilters"
    >
      <template #inline-filters>
        <a-select v-model:value="resultFilter" class="toolbar-select result-filter responsive-list-inline-filter" :options="resultOptions" @change="applyFilters" />
      </template>
      <template #advanced-filters>
        <a-form layout="vertical" class="advanced-filter-form">
          <a-form-item label="来源系统 ID">
            <a-input v-model:value="sourceRefIdFilter" allow-clear placeholder="extsrc_xxx" @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="接口路径">
            <a-input v-model:value="pathFilter" allow-clear placeholder="/__aipublic__/access/info" @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="状态码">
            <a-input v-model:value="statusCodeFilter" allow-clear placeholder="200 / 401 / 500" @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="客户端 IP">
            <a-input v-model:value="clientIpFilter" allow-clear placeholder="203.0.113." @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="调用时间范围">
            <a-range-picker
              v-model:value="timeRange"
              allow-clear
              show-time
              class="drawer-range-picker"
              :placeholder="['开始时间', '结束时间']"
              @change="applyFilters"
            />
          </a-form-item>
        </a-form>
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="结果">
            <a-select v-model:value="resultFilter" :options="resultOptions" />
          </a-form-item>
          <a-form-item label="来源系统 ID">
            <a-input v-model:value="sourceRefIdFilter" allow-clear placeholder="extsrc_xxx" />
          </a-form-item>
          <a-form-item label="接口路径">
            <a-input v-model:value="pathFilter" allow-clear placeholder="/__aipublic__/access/info" />
          </a-form-item>
          <a-form-item label="状态码">
            <a-input v-model:value="statusCodeFilter" allow-clear placeholder="200 / 401 / 500" />
          </a-form-item>
          <a-form-item label="客户端 IP">
            <a-input v-model:value="clientIpFilter" allow-clear placeholder="203.0.113." />
          </a-form-item>
          <a-form-item label="调用时间范围">
            <a-range-picker
              v-model:value="timeRange"
              allow-clear
              show-time
              class="drawer-range-picker"
              :placeholder="['开始时间', '结束时间']"
            />
          </a-form-item>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table public-api-log-table"
      :columns="columns"
      :data-source="records"
      row-key="id"
      :loading="loading"
      :pagination="tablePagination"
      mobile-pagination
      :mobile-has-more="mobileHasMore"
      :loading-more="mobileLoadingMore"
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无公开接口日志" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'createdAt'">
          <span class="mono-cell muted-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template v-else-if="column.key === 'source'">
          <div class="source-cell">
            <span class="source-name-text">{{ record.sourceName || '未认证来源' }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'path'">
          <span class="path-cell">{{ record.method }} {{ record.path }}</span>
        </template>
        <template v-else-if="column.key === 'result'">
          <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'statusCode'">
          <a-tag :color="statusColor(record.statusCode)">{{ record.statusCode ?? '-' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'duration'">
          {{ formatDuration(record.durationMs) }}
        </template>
        <template v-else-if="column.key === 'traceId'">
          <a-tooltip :title="record.traceId || '-'">
            <span class="hash-cell">{{ preview(record.traceId) }}</span>
          </a-tooltip>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="detailActions" @action-click="openDetail(record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="log-mobile-card">
          <div class="log-mobile-card-head">
            <div>
              <strong>{{ record.sourceName || '未认证来源' }}</strong>
              <span>{{ record.method }} {{ record.path }}</span>
            </div>
            <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
          </div>
          <div class="log-mobile-card-grid">
            <span>时间</span>
            <strong>{{ formatDateTime(record.createdAt) }}</strong>
            <span>状态码</span>
            <strong>{{ record.statusCode ?? '-' }}</strong>
            <span>耗时</span>
            <strong>{{ formatDuration(record.durationMs) }}</strong>
            <span>客户端 IP</span>
            <strong>{{ record.clientIp || '-' }}</strong>
            <span>traceId</span>
            <strong class="hash-cell">{{ preview(record.traceId) }}</strong>
          </div>
          <RowActions :actions="detailActions" variant="button" @action-click="openDetail(record)" />
        </article>
      </template>
    </ResponsiveDataList>

    <a-drawer v-model:open="detailOpen" width="min(960px, 96vw)" title="公开接口日志详情" :body-style="{ padding: '18px' }" @close="closeDetail">
      <a-spin :spinning="detailLoading">
        <template v-if="detail">
          <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
            <a-descriptions-item label="调用时间">{{ formatDateTime(detail.createdAt) }}</a-descriptions-item>
            <a-descriptions-item label="结果">
              <a-tag :color="detail.success ? 'green' : 'red'">{{ detail.success ? '成功' : '失败' }}</a-tag>
            </a-descriptions-item>
            <a-descriptions-item label="来源系统">{{ detail.sourceName || '-' }}</a-descriptions-item>
            <a-descriptions-item label="测试 token">{{ detail.isTestToken ? '是' : '否' }}</a-descriptions-item>
            <a-descriptions-item label="token">{{ detail.tokenName || '-' }} / {{ detail.tokenPrefix || '-' }}</a-descriptions-item>
            <a-descriptions-item label="接口">{{ detail.method }} {{ detail.path }}</a-descriptions-item>
            <a-descriptions-item label="状态码">{{ detail.statusCode ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="耗时">{{ formatDuration(detail.durationMs) }}</a-descriptions-item>
            <a-descriptions-item label="客户端 IP">{{ detail.clientIp || '-' }}</a-descriptions-item>
            <a-descriptions-item label="traceId">{{ detail.traceId || '-' }}</a-descriptions-item>
            <a-descriptions-item label="User-Agent" :span="2">{{ detail.userAgent || '-' }}</a-descriptions-item>
            <a-descriptions-item label="错误" :span="2">{{ detail.errorMessage || detail.errorCode || '-' }}</a-descriptions-item>
          </a-descriptions>

          <a-tabs>
            <a-tab-pane key="request" tab="请求数据">
              <ReadonlyCodeViewer content-type="application/json" :text="prettyJson(detail.requestData)" title="请求摘要" />
            </a-tab-pane>
            <a-tab-pane key="response" tab="响应数据">
              <ReadonlyCodeViewer content-type="application/json" :text="prettyJson(detail.responseData)" title="响应摘要" />
            </a-tab-pane>
          </a-tabs>
        </template>
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onDeactivated, ref, watch } from 'vue'
import dayjs, { type Dayjs } from 'dayjs'
import axios from 'axios'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type { PublicApiLogDetail, PublicApiLogResultFilter, PublicApiLogSummary } from '@/types/domain'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import ReadonlyCodeViewer from '@/components/ReadonlyCodeViewer.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'

type TimeRangeValue = [Dayjs | null | undefined, Dayjs | null | undefined] | null | undefined
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

const pageSize = 100
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
const pageStateCache = usePageStateCache<PublicApiLogsPageState>(undefined, defaultPageState, { version: 1 })
const initialState = pageStateCache.read()

const traceIdFilter = ref(initialState.traceIdFilter)
const sourceRefIdFilter = ref(initialState.sourceRefIdFilter)
const pathFilter = ref(initialState.pathFilter)
const statusCodeFilter = ref(initialState.statusCodeFilter)
const clientIpFilter = ref(initialState.clientIpFilter)
const resultFilter = ref<PublicApiLogResultFilter>(initialState.resultFilter)
const timeRange = ref<TimeRangeValue>(parseStoredTimeRange(initialState.timeRange))
const detailOpen = ref(false)
const detailLoading = ref(false)
const detail = ref<PublicApiLogDetail>()
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
} = useResponsivePagedList<PublicApiLogSummary>({
  pageSize,
  initialPagination: initialState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条公开接口日志，还有更多`
    : `共 ${total} 条公开接口日志`,
  fetchPage: async (_options, pageState) => {
    const range = normalizeTimeRange(timeRange.value)
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

const resultOptions = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '失败', value: 'failed' }
]
const columns = [
  { title: '调用时间', key: 'createdAt', width: 180 },
  { title: '来源系统', key: 'source', minWidth: 180 },
  { title: '接口', key: 'path', minWidth: 260, responsiveFlex: true },
  { title: '结果', key: 'result', width: 92 },
  { title: '状态码', key: 'statusCode', width: 92 },
  { title: '耗时', key: 'duration', width: 100 },
  { title: '客户端 IP', dataIndex: 'clientIp', key: 'clientIp', width: 140 },
  { title: 'traceId', key: 'traceId', width: 150 },
  { title: '操作', key: 'actions', fixed: 'right', actionCount: 1 }
]
const detailActions: RowActionItem[] = [{ key: 'detail', label: '详情', icon: 'detail', tone: 'info' }]

const activeFilterCount = computed(() => {
  let count = 0
  if (traceIdFilter.value.trim()) count += 1
  if (resultFilter.value !== 'all') count += 1
  if (sourceRefIdFilter.value.trim()) count += 1
  if (pathFilter.value.trim()) count += 1
  if (normalizedStatusCodeFilter.value !== undefined) count += 1
  if (clientIpFilter.value.trim()) count += 1
  if (normalizeTimeRange(timeRange.value)) count += 1
  return count
})
const advancedFilterCount = computed(() => activeFilterCount.value - (traceIdFilter.value.trim() ? 1 : 0) - (resultFilter.value !== 'all' ? 1 : 0))
const normalizedStatusCodeFilter = computed(() => normalizedStatusCode(statusCodeFilter.value))
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
  timeRange.value = parseStoredTimeRange(defaults.timeRange)
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

async function openDetail(record: PublicApiLogSummary): Promise<void> {
  const requestId = detailRequestId + 1
  detailRequestId = requestId
  detailOpen.value = true
  detailLoading.value = true
  detail.value = undefined
  try {
    const nextDetail = await api.publicApiLogs.detail(record.id)
    if (requestId === detailRequestId) {
      detail.value = nextDetail
    }
  } catch (error) {
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
  const range = normalizeTimeRange(timeRange.value)
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

function parseStoredTimeRange(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return normalizeTimeRange(start.isValid() && end.isValid() ? [start, end] : undefined)
}

function normalizeTimeRange(value: TimeRangeValue): [Dayjs, Dayjs] | undefined {
  const start = value?.[0]
  const end = value?.[1]
  if (!start?.isValid() || !end?.isValid()) return undefined
  return start.isAfter(end) ? [end, start] : [start, end]
}

function normalizedStatusCode(value: string): number | undefined {
  const number = Number(value.trim())
  return Number.isInteger(number) && number >= 100 && number <= 599 ? number : undefined
}

function validateStatusCodeFilter(): boolean {
  if (!hasInvalidStatusCodeFilter.value) return true
  message.warning('状态码须为 100-599 的整数')
  return false
}

function isNotFoundError(error: unknown): boolean {
  return axios.isAxiosError(error) && error.response?.status === 404
}

function formatDuration(value?: number): string {
  if (value === undefined || value === null) return '-'
  if (value < 1000) return `${value} ms`
  return `${(value / 1000).toFixed(2)} s`
}

function statusColor(value?: number): string {
  if (!value) return 'default'
  if (value >= 200 && value < 300) return 'green'
  if (value >= 400 && value < 500) return 'orange'
  if (value >= 500) return 'red'
  return 'blue'
}

function preview(value?: string): string {
  if (!value) return '-'
  return value.length > 18 ? `${value.slice(0, 8)}...${value.slice(-6)}` : value
}

function prettyJson(value: unknown): string {
  try {
    return JSON.stringify(value ?? {}, null, 2)
  } catch {
    return '{}'
  }
}

watch(snapshotPageState, () => {
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })

void loadData()
onDeactivated(closeDetail)
</script>

<style scoped>
.result-filter {
  width: 112px;
}

.drawer-range-picker {
  width: 100%;
}

.advanced-filter-form :deep(.ant-input),
.advanced-filter-form :deep(.ant-picker) {
  width: 100%;
}

.source-cell {
  display: grid;
  gap: 2px;
}

.source-name-text {
  overflow: hidden;
  color: #0f172a;
  font-weight: 400;
  text-overflow: ellipsis;
}

.mono-cell,
.hash-cell,
.path-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.path-cell {
  display: block;
  overflow-wrap: anywhere;
}

.hash-cell {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.log-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.log-mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.log-mobile-card-head div {
  display: grid;
  min-width: 0;
  gap: 3px;
}

.log-mobile-card-head strong,
.log-mobile-card-head span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}

.log-mobile-card-head span {
  color: #64748b;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.log-mobile-card-grid {
  display: grid;
  grid-template-columns: minmax(86px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: #64748b;
  font-size: 12px;
}

.log-mobile-card-grid strong {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}
</style>
