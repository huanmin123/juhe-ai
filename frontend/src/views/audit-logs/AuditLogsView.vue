<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="traceIdFilter"
      search-placeholder="搜索 traceId"
      filter-title="审计筛选"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :refresh-loading="loading"
      @refresh="refreshRecords"
      @reset="resetFilters"
      @search="applyFilters"
    >
      <template #inline-filters>
        <a-select v-model:value="outcomeFilter" class="toolbar-select audit-outcome-filter responsive-list-inline-filter" :options="outcomeOptions" @change="applyFilters" />
        <a-select v-model:value="trafficSourceFilter" class="toolbar-select audit-source-filter responsive-list-inline-filter" :options="trafficSourceOptions" @change="applyFilters" />
        <SystemPrincipalSelect
          v-model:value="systemAccountFilter"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          v-model:selected-principal="systemAccountSelection"
          include-all
          all-label="全部用户"
          class="toolbar-select audit-user-filter responsive-list-inline-filter"
          placeholder="筛选用户"
          @change="applyFilters"
          @dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @search="handleSystemAccountOptionsSearch"
        />
      </template>
      <template #advanced-filters>
        <a-form layout="vertical" class="advanced-filter-form">
          <a-form-item label="接口路径">
            <a-input v-model:value="pathFilter" allow-clear placeholder="/v1/responses" @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="状态码">
            <a-input v-model:value="statusCodeFilter" allow-clear placeholder="401 / 503" @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="来源">
            <a-select v-model:value="trafficSourceFilter" :options="trafficSourceOptions" @change="applyFilters" />
          </a-form-item>
        </a-form>
      </template>
      <template #actions>
        <TableColumnManager
          :columns="auditLogColumns"
          :settings="columnSettings"
          :required-keys="['traceId']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="结果">
            <a-select v-model:value="outcomeFilter" :options="outcomeOptions" />
          </a-form-item>
          <a-form-item label="来源">
            <a-select v-model:value="trafficSourceFilter" :options="trafficSourceOptions" />
          </a-form-item>
          <a-form-item label="用户">
            <SystemPrincipalSelect
              v-model:value="systemAccountFilter"
              :accounts="systemAccounts"
              :active-only="false"
              :filter-option="false"
              :loading="systemAccountOptionsLoading"
              v-model:selected-principal="systemAccountSelection"
              include-all
              all-label="全部用户"
              placeholder="筛选用户"
              @dropdown-visible-change="handleSystemAccountOptionsDropdown"
              @search="handleSystemAccountOptionsSearch"
            />
          </a-form-item>
          <a-form-item label="接口路径">
            <a-input v-model:value="pathFilter" allow-clear placeholder="/v1/responses" />
          </a-form-item>
          <a-form-item label="状态码">
            <a-input v-model:value="statusCodeFilter" allow-clear placeholder="401 / 503" />
          </a-form-item>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <RuntimeAvailabilityAlert
      :visible="auditRuntimeAlertVisible"
      message="审计运行态暂时不可观测"
      :description="auditRuntimeAlertDescription"
    />

    <AuditLogList
      :columns="managedColumns"
      :records="records"
      :loading="loading"
      :pagination="tablePagination"
      :mobile-has-more="mobileHasMore"
      :loading-more="mobileLoadingMore"
      @change="handleTableChange"
      @detail="openDetail"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    />

    <a-drawer v-model:open="detailOpen" width="min(980px, 96vw)" title="审计详情" :body-style="{ padding: '18px' }">
      <a-spin :spinning="detailLoading">
        <template v-if="detail">
          <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
            <a-descriptions-item label="traceId">{{ detail.traceId }}</a-descriptions-item>
            <a-descriptions-item label="结果">{{ outcomeText(detail.auditOutcome) }}</a-descriptions-item>
            <a-descriptions-item label="来源">{{ trafficSourceText(detail.trafficSource) }}</a-descriptions-item>
            <a-descriptions-item label="接口">{{ detail.method }} {{ detail.path }}</a-descriptions-item>
            <a-descriptions-item label="状态码">{{ detail.finalStatusCode ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="账号">{{ displayName(detail.accountName, detail.accountId) }}</a-descriptions-item>
            <a-descriptions-item label="API Key">{{ displayName(detail.apiKeyName, detail.apiKeyId) }}</a-descriptions-item>
            <a-descriptions-item label="分组">{{ displayAuditGroupName(detail.groupName, detail.groupId) }}</a-descriptions-item>
            <a-descriptions-item label="系统账户">{{ displayName(detail.systemAccountName, detail.systemAccountId) }}</a-descriptions-item>
            <a-descriptions-item label="耗时">{{ formatDuration(detail.durationMs) }}</a-descriptions-item>
            <a-descriptions-item label="采样">{{ detail.sampleReason }} / {{ detail.sampleBucket }}</a-descriptions-item>
            <a-descriptions-item label="错误" :span="2">{{ detail.errorMessage ?? '-' }}</a-descriptions-item>
          </a-descriptions>

          <a-tabs>
            <a-tab-pane key="attempts" tab="上游尝试">
              <ResponsiveDataList
                table-class="audit-detail-table"
                size="small"
                :columns="attemptColumns"
                :data-source="detail.attempts"
                row-key="id"
                :pagination="false"
                :table-scroll-enabled="false"
                :adaptive-column-width="false"
                :mobile-breakpoint="1024"
                :lock-body-scroll="false"
              >
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'success'">
                    <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
                  </template>
                  <template v-else-if="column.key === 'account'">
                    <span class="attempt-account-cell">{{ displayName(record.accountName, record.accountId) }}</span>
                  </template>
                  <template v-else-if="column.key === 'startedAt'">
                    <span class="detail-time-cell muted-cell">{{ formatDateTime(record.startedAt) }}</span>
                  </template>
                  <template v-else-if="column.key === 'duration'">
                    {{ formatDuration(record.durationMs) }}
                  </template>
                  <template v-else-if="column.key === 'url'">
                    <span class="url-cell">{{ record.upstreamUrl || '-' }}</span>
                  </template>
                  <template v-else-if="column.key === 'error'">
                    <span class="error-cell">{{ record.errorMessage || '-' }}</span>
                  </template>
                </template>
                <template #card="{ record }">
                  <article class="payload-mobile-card">
                    <div class="payload-mobile-card-head">
                      <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
                      <span>{{ formatDuration(record.durationMs) }}</span>
                    </div>
                    <div class="payload-mobile-card-grid">
                      <span>序号</span>
                      <strong>{{ record.attemptIndex }}</strong>
                      <span>账号</span>
                      <strong>{{ displayName(record.accountName, record.accountId) }}</strong>
                      <span>状态码</span>
                      <strong>{{ record.upstreamStatusCode ?? '-' }}</strong>
                      <span>时间</span>
                      <strong>{{ formatDateTime(record.startedAt) }}</strong>
                      <span>上游 URL</span>
                      <strong>{{ record.upstreamUrl }}</strong>
                    </div>
                  </article>
                </template>
              </ResponsiveDataList>
            </a-tab-pane>
            <a-tab-pane key="payloads" tab="原始内容">
              <ResponsiveDataList
                table-class="audit-payload-table"
                size="small"
                :columns="payloadColumns"
                :data-source="detail.payloads"
                row-key="id"
                :pagination="false"
                :table-scroll-enabled="false"
                :adaptive-column-width="false"
                :mobile-breakpoint="1024"
                :lock-body-scroll="false"
              >
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'partType'">
                    <a-tag>{{ payloadPartText(record.partType) }}</a-tag>
                  </template>
                  <template v-else-if="column.key === 'size'">
                    {{ formatBytes(record.sizeBytes) }}
                  </template>
                  <template v-else-if="column.key === 'captureStatus'">
                    <a-tag>{{ captureStatusText(record.captureStatus) }}</a-tag>
                  </template>
                  <template v-else-if="column.key === 'createdAt'">
                    <span class="detail-time-cell muted-cell">{{ formatDateTime(record.createdAt) }}</span>
                  </template>
                  <template v-else-if="column.key === 'headersSha256'">
                    <a-tooltip :title="record.headersSha256 || '-'">
                      <span class="hash-cell">{{ formatHashPreview(record.headersSha256) }}</span>
                    </a-tooltip>
                  </template>
                  <template v-else-if="column.key === 'bodySha256'">
                    <a-tooltip :title="record.bodySha256 || '-'">
                      <span class="hash-cell">{{ formatHashPreview(record.bodySha256) }}</span>
                    </a-tooltip>
                  </template>
                  <template v-else-if="column.key === 'actions'">
                    <RowActions :actions="payloadActions(record.id)" @action-click="loadPayload(record.id)" />
                  </template>
                </template>
                <template #card="{ record }">
                  <article class="payload-mobile-card">
                    <div class="payload-mobile-card-head">
                      <a-tag>{{ payloadPartText(record.partType) }}</a-tag>
                      <span>{{ formatBytes(record.sizeBytes) }}</span>
                    </div>
                    <div class="payload-mobile-card-grid">
                      <span>序号</span>
                      <strong>{{ record.sequenceIndex }}</strong>
                      <span>类型</span>
                      <strong>{{ record.contentType || '-' }}</strong>
                      <span>状态</span>
                      <strong>{{ captureStatusText(record.captureStatus) }}</strong>
                      <span>时间</span>
                      <strong>{{ formatDateTime(record.createdAt) }}</strong>
                      <span>Headers SHA256</span>
                      <strong class="hash-cell">{{ formatHashPreview(record.headersSha256) }}</strong>
                      <span>Body SHA256</span>
                      <strong class="hash-cell">{{ formatHashPreview(record.bodySha256) }}</strong>
                    </div>
                    <RowActions :actions="payloadActions(record.id)" variant="button" @action-click="loadPayload(record.id)" />
                  </article>
                </template>
              </ResponsiveDataList>
              <div v-if="selectedPayload" class="payload-viewer">
                <div class="payload-viewer-toolbar">
                  <div class="payload-viewer-main">
                    <strong>{{ payloadPartText(selectedPayload.partType) }}</strong>
                    <span>{{ formatBytes(selectedPayload.sizeBytes) }}</span>
                    <span v-if="payloadContentTab === 'body' && selectedPayloadBodyWindowText" class="payload-window-range">{{ selectedPayloadBodyWindowText }}</span>
                    <a-tabs v-model:activeKey="payloadContentTab" class="payload-content-tabs" size="small">
                      <a-tab-pane key="headers" tab="Headers" />
                      <a-tab-pane key="body" tab="Body" />
                    </a-tabs>
                  </div>
                  <div class="payload-viewer-actions">
                    <a-tooltip v-if="selectedPayloadCanLoadMore" title="读取下一段正文">
                      <a-button size="small" :loading="payloadLoadingId === selectedPayload.id" @click="loadNextPayloadWindow">
                        <template #icon><arrow-right-outlined /></template>
                      </a-button>
                    </a-tooltip>
                    <a-tooltip title="复制当前内容">
                      <a-button size="small" :disabled="!selectedPayloadCurrentText" @click="copySelectedPayloadText">
                        <template #icon><copy-outlined /></template>
                      </a-button>
                    </a-tooltip>
                  </div>
                </div>
                <ReadonlyCodeViewer
                  ref="payloadCodeViewer"
                  attached-toolbar
                  :content-type="selectedPayloadViewerContentType"
                  :show-toolbar="false"
                  :text="selectedPayloadCurrentText"
                />
              </div>
            </a-tab-pane>
          </a-tabs>
        </template>
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { ArrowRightOutlined, CopyOutlined } from '@ant-design/icons-vue'
import { computed, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type { AuditLogDetail, AuditLogPayloadDetail, AuditLogSummary, AuditOutcome, AuditLogRuntime, AuditTrafficSource } from '@/types/domain'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import ReadonlyCodeViewer from '@/components/ReadonlyCodeViewer.vue'
import RowActions from '@/components/RowActions.vue'
import RuntimeAvailabilityAlert from '@/components/RuntimeAvailabilityAlert.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import type { RowActionItem } from '@/components/rowActions'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'
import { rememberGroupLabel } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { allSystemAccountsValue, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import AuditLogList from './AuditLogList.vue'
import {
  captureStatusText,
  displayAuditGroupName,
  displayName,
  formatBytes,
  formatDateTime,
  formatDuration,
  formatHashPreview,
  normalizedStatusCode,
  outcomeText,
  payloadPartText,
  prettyJson,
  statusColor,
  trafficSourceText
} from './auditLogFormatters'
import {
  auditAttemptColumns,
  auditLogColumns,
  auditOutcomeOptions,
  auditPayloadColumns
} from './auditLogTableColumns'

const detailLoading = ref(false)
const payloadLoadingId = ref('')
const runtime = ref<AuditLogRuntime>()
const detail = ref<AuditLogDetail>()
const selectedPayload = ref<AuditLogPayloadDetail>()
const detailOpen = ref(false)
const payloadContentTab = ref<'headers' | 'body'>('body')
const payloadCodeViewer = ref<{ copyDisplayText: () => Promise<void> }>()
let detailRequestId = 0
let payloadRequestId = 0
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  selectedIds: () => [systemAccountFilter.value]
})

const pageSize = 100
type AuditLogsPageState = {
  outcomeFilter: AuditOutcome | 'all'
  pagination: { current: number; pageSize: number }
  pathFilter: string
  statusCodeFilter: string
  systemAccountFilter: string
  systemAccountSelection?: PrincipalSelection
  traceIdFilter: string
  trafficSourceFilter: AuditTrafficSource | 'all'
}
const defaultAuditLogsPageState = (): AuditLogsPageState => ({
  outcomeFilter: 'all',
  pagination: { current: 1, pageSize },
  pathFilter: '',
  statusCodeFilter: '',
  systemAccountFilter: allSystemAccountsValue,
  systemAccountSelection: undefined,
  traceIdFilter: '',
  trafficSourceFilter: 'all'
})
const pageStateCache = usePageStateCache<AuditLogsPageState>(undefined, defaultAuditLogsPageState, { version: 5 })
const initialPageState = pageStateCache.read()
const route = useRoute()
const router = useRouter()
const initialTraceId = routeTraceId()
const effectiveInitialPageState: AuditLogsPageState = initialTraceId
  ? { ...defaultAuditLogsPageState(), traceIdFilter: initialTraceId }
  : initialPageState

const traceIdFilter = ref(effectiveInitialPageState.traceIdFilter)
const outcomeFilter = ref<AuditOutcome | 'all'>(effectiveInitialPageState.outcomeFilter)
const pathFilter = ref(effectiveInitialPageState.pathFilter)
const statusCodeFilter = ref(effectiveInitialPageState.statusCodeFilter)
const systemAccountFilter = ref(effectiveInitialPageState.systemAccountFilter)
const systemAccountSelection = ref<PrincipalSelection | undefined>(effectiveInitialPageState.systemAccountSelection)
const trafficSourceFilter = ref<AuditTrafficSource | 'all'>(effectiveInitialPageState.trafficSourceFilter)
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
} = useResponsivePagedList<AuditLogSummary, { forceOptions?: boolean }>({
  pageSize,
  initialPagination: effectiveInitialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条审计日志，还有更多`
    : `共 ${total} 条审计日志`,
  fetchPage: async (options, pageState) => {
    if (options.forceOptions === true) {
      resetSystemAccountOptionsSearch()
    }
    const [listResult, runtimeInfo] = await Promise.all([
      fetchRecords(pageState),
      api.auditLogs.runtime()
    ])
    runtime.value = runtimeInfo
    return listResult
  },
  onError: (error) => {
    console.error(error)
    message.error('加载审计日志失败')
  }
})

const outcomeOptions = auditOutcomeOptions
const trafficSourceOptions = [
  { label: '全部来源', value: 'all' },
  { label: '网关请求', value: 'gateway' },
  { label: '账号测试', value: 'manual_account_test' },
  { label: '恢复探活', value: 'cooldown_retest' }
] satisfies Array<{ label: string; value: AuditTrafficSource | 'all' }>
const attemptColumns = auditAttemptColumns
const payloadColumns = auditPayloadColumns
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings('audit-logs', auditLogColumns, {
  requiredKeys: ['traceId'],
  minVisible: 1
})
const activeFilterCount = computed(() => {
  let count = 0
  if (traceIdFilter.value.trim()) count += 1
  if (outcomeFilter.value !== 'all') count += 1
  if (systemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (pathFilter.value.trim()) count += 1
  if (statusCodeFilter.value.trim()) count += 1
  if (trafficSourceFilter.value !== 'all') count += 1
  return count
})
const advancedFilterCount = computed(() => {
  let count = 0
  if (pathFilter.value.trim()) count += 1
  if (statusCodeFilter.value.trim()) count += 1
  if (trafficSourceFilter.value !== 'all') count += 1
  return count
})
const auditRuntimeAlertVisible = computed(() => Boolean(runtime.value && (
  !runtime.value.runtimeAvailable
  || !runtime.value.workerSnapshotAvailable
  || !runtime.value.auditLogQueueAvailable
  || !runtime.value.activeCaptureAvailable
)))
const auditRuntimeAlertDescription = computed(() => {
  const info = runtime.value
  if (!info) return ''
  const reasons: string[] = []
  if (!info.runtimeAvailable) {
    reasons.push('服务运行态不可用')
  } else {
    if (!info.workerSnapshotAvailable) reasons.push('后台进程快照不可用')
    if (!info.auditLogQueueAvailable) reasons.push('审计队列状态不可用')
    if (!info.activeCaptureAvailable) reasons.push('活跃捕获计数不可用')
  }
  const workerText = info.worker.available
    ? `后台进程${runtimeReadyText(info.worker.ready)}`
    : '后台进程状态不可用'
  return `${reasons.join('；') || '运行态状态未知'}。${workerText}。`
})
let skipNextRouteTraceRestore = false

const selectedPayloadCurrentText = computed(() => {
  if (!selectedPayload.value) return ''
  return payloadContentTab.value === 'headers'
    ? prettyJson(selectedPayload.value.headers ?? {})
    : selectedPayload.value.bodyText ?? selectedPayload.value.bodyBase64 ?? ''
})
const selectedPayloadViewerContentType = computed(() => payloadContentTab.value === 'headers'
  ? 'application/json'
  : selectedPayload.value?.contentType)
const selectedPayloadBodyWindowText = computed(() => {
  const payload = selectedPayload.value
  if (!payload || payload.bodyTotalBytes <= 0) return ''
  const start = payload.bodyOffset
  const end = Math.min(payload.bodyTotalBytes, payload.bodyOffset + payload.bodyBytesReturned)
  return `${formatBytes(start)} - ${formatBytes(end)} / ${formatBytes(payload.bodyTotalBytes)}`
})
const selectedPayloadCanLoadMore = computed(() => Boolean(
  selectedPayload.value
  && payloadContentTab.value === 'body'
  && selectedPayload.value.bodyTruncated
  && selectedPayload.value.bodyNextOffset !== undefined
))

watch(records, rememberAuditRecordGroupLabels, { immediate: true })
watch(detail, (nextDetail) => {
  rememberGroupLabel(nextDetail?.groupId, nextDetail?.groupName)
})

function applyFilters(): void {
  clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

function applyPageState(state: AuditLogsPageState): void {
  traceIdFilter.value = state.traceIdFilter
  outcomeFilter.value = state.outcomeFilter
  pathFilter.value = state.pathFilter
  statusCodeFilter.value = state.statusCodeFilter
  systemAccountFilter.value = state.systemAccountFilter
  systemAccountSelection.value = state.systemAccountSelection
  trafficSourceFilter.value = state.trafficSourceFilter
  pagination.current = state.pagination.current
  pagination.pageSize = state.pagination.pageSize
  resetSystemAccountOptionsSearch()
}

function applyRouteTraceId(traceId: string): void {
  pageStateCache.flushPendingWrite()
  applyPageState({ ...defaultAuditLogsPageState(), traceIdFilter: traceId })
  resetPagination()
  void loadData()
}

function restorePageStateAfterRouteTraceCleared(): void {
  applyPageState(pageStateCache.read())
  void loadData({ forceOptions: true })
}

function refreshRecords(): void {
  void loadData({ forceOptions: true })
}

function resetFilters(): void {
  clearRouteTraceIdForManualState()
  const defaults = defaultAuditLogsPageState()
  traceIdFilter.value = defaults.traceIdFilter
  outcomeFilter.value = defaults.outcomeFilter
  pathFilter.value = defaults.pathFilter
  statusCodeFilter.value = defaults.statusCodeFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountSelection.value = defaults.systemAccountSelection
  trafficSourceFilter.value = defaults.trafficSourceFilter
  resetSystemAccountOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

function fetchRecords(pageState: { current: number; pageSize: number }) {
  const systemAccountId = selectedSystemAccountId(systemAccountFilter.value, true)
  return api.auditLogs.list({
    page: pageState.current,
    pageSize: pageState.pageSize,
    traceId: traceIdFilter.value.trim() || undefined,
    outcome: outcomeFilter.value,
    path: pathFilter.value || undefined,
    statusCode: normalizedStatusCode(statusCodeFilter.value),
    systemAccountId,
    trafficSource: trafficSourceFilter.value === 'all' ? undefined : trafficSourceFilter.value
  })
}

function rememberAuditRecordGroupLabels(items: AuditLogSummary[]): void {
  for (const item of items) {
    rememberGroupLabel(item.groupId, item.groupName)
  }
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

async function openDetail(record: AuditLogSummary): Promise<void> {
  const requestId = detailRequestId + 1
  detailRequestId = requestId
  detailOpen.value = true
  detailLoading.value = true
  selectedPayload.value = undefined
  try {
    const nextDetail = await api.auditLogs.detail(record.id)
    if (requestId === detailRequestId) {
      detail.value = nextDetail
    }
  } catch (error) {
    console.error(error)
    message.error('加载审计详情失败')
  } finally {
    if (requestId === detailRequestId) {
      detailLoading.value = false
    }
  }
}

async function loadPayload(payloadId: string): Promise<void> {
  if (!detail.value) return
  const requestId = payloadRequestId + 1
  payloadRequestId = requestId
  payloadLoadingId.value = payloadId
  try {
    const nextPayload = await api.auditLogs.payload(detail.value.id, payloadId)
    if (requestId === payloadRequestId) {
      selectedPayload.value = nextPayload
      payloadContentTab.value = 'body'
    }
  } catch (error) {
    console.error(error)
    message.error('加载原始内容失败')
  } finally {
    if (requestId === payloadRequestId) {
      payloadLoadingId.value = ''
    }
  }
}

async function loadNextPayloadWindow(): Promise<void> {
  if (!detail.value || !selectedPayload.value?.bodyTruncated || selectedPayload.value.bodyNextOffset === undefined) return
  const payloadId = selectedPayload.value.id
  const requestId = payloadRequestId + 1
  payloadRequestId = requestId
  payloadLoadingId.value = payloadId
  try {
    const nextPayload = await api.auditLogs.payload(detail.value.id, payloadId, {
      offset: selectedPayload.value.bodyNextOffset,
      limit: selectedPayload.value.bodyLimit
    })
    if (requestId === payloadRequestId) {
      selectedPayload.value = nextPayload
      payloadContentTab.value = 'body'
    }
  } catch (error) {
    console.error(error)
    message.error('加载下一段正文失败')
  } finally {
    if (requestId === payloadRequestId) {
      payloadLoadingId.value = ''
    }
  }
}

async function copySelectedPayloadText(): Promise<void> {
  await payloadCodeViewer.value?.copyDisplayText()
}

function closeTransientDetails(): void {
  detailRequestId += 1
  payloadRequestId += 1
  detailOpen.value = false
  detailLoading.value = false
  payloadLoadingId.value = ''
  detail.value = undefined
  selectedPayload.value = undefined
}

function payloadActions(payloadId: string): RowActionItem[] {
  return [
    {
      key: 'payload',
      label: '查看原文',
      icon: 'detail',
      tone: 'info',
      disabled: payloadLoadingId.value === payloadId
    }
  ]
}

function runtimeReadyText(value: boolean | null): string {
  if (value === true) return '已就绪'
  if (value === false) return '未就绪'
  return '状态未知'
}

function snapshotPageState(): AuditLogsPageState {
  return {
    outcomeFilter: outcomeFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    pathFilter: pathFilter.value,
    statusCodeFilter: statusCodeFilter.value,
    systemAccountFilter: systemAccountFilter.value,
    systemAccountSelection: systemAccountSelection.value,
    traceIdFilter: traceIdFilter.value,
    trafficSourceFilter: trafficSourceFilter.value
  }
}

watch(snapshotPageState, () => {
  if (routeTraceId()) {
    pageStateCache.cancelPendingWrite()
    return
  }
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })
watch(systemAccountSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
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

onMounted(loadData)
onDeactivated(closeTransientDetails)
</script>

<style scoped>
.audit-outcome-filter {
  width: 132px;
}

.audit-path-filter {
  width: 220px;
}

.audit-status-filter {
  width: 108px;
}

.audit-user-filter {
  width: 190px;
}

.advanced-filter-form :deep(.ant-input) {
  width: 100%;
}

.attempt-account-cell,
.error-cell,
.url-cell,
.mono-cell,
.detail-time-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

:deep(.audit-detail-table .ant-table-cell) {
  vertical-align: top;
  white-space: normal;
}

.attempt-account-cell,
.detail-time-cell,
.error-cell,
.url-cell {
  display: block;
  overflow: visible;
  overflow-wrap: anywhere;
  white-space: normal;
  word-break: break-word;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.payload-viewer {
  margin-top: 16px;
}

.payload-viewer-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 42px;
  padding: 0 10px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-bottom: 0;
  border-radius: 8px 8px 0 0;
}

.payload-viewer-main {
  display: flex;
  align-items: center;
  min-width: 0;
  flex: 1 1 auto;
  gap: 12px;
  color: #64748b;
  font-size: 12px;
  white-space: nowrap;
}

.payload-viewer-main strong {
  flex: 0 0 auto;
  color: #0f172a;
  font-size: 13px;
}

.payload-viewer-main span {
  overflow: hidden;
  text-overflow: ellipsis;
}

.payload-content-tabs {
  flex: 0 0 auto;
}

.payload-content-tabs :deep(.ant-tabs-nav) {
  margin-bottom: 0;
}

.payload-content-tabs :deep(.ant-tabs-nav-operations) {
  display: none !important;
}

.payload-content-tabs :deep(.ant-tabs-content-holder) {
  display: none;
}

.payload-window-range {
  max-width: 180px;
  color: #475569;
}

.payload-viewer-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex: 0 0 auto;
  gap: 6px;
}

.hash-cell {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.payload-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.payload-mobile-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.payload-mobile-card-grid {
  display: grid;
  grid-template-columns: minmax(88px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: #64748b;
  font-size: 12px;
}

.payload-mobile-card-grid strong {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}

</style>
