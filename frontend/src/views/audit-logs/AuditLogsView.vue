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
      <template #advanced-filters>
        <a-form layout="vertical" class="advanced-filter-form">
          <a-form-item label="结果">
            <a-select v-model:value="outcomeFilter" :options="outcomeOptions" @change="applyFilters" />
          </a-form-item>
          <a-form-item label="来源">
            <a-select v-model:value="trafficSourceFilter" :options="trafficSourceOptions" @change="applyFilters" />
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
              @change="applyFilters"
              @dropdown-visible-change="handleSystemAccountOptionsDropdown"
              @search="handleSystemAccountOptionsSearch"
            />
          </a-form-item>
          <a-form-item label="AI账户">
            <AccountSelect
              v-model:value="accountIdFilter"
              v-model:selected-account="accountSelection"
              :accounts="accountOptions"
              :filter-option="false"
              :loading="accountOptionsLoading"
              allow-clear
              placeholder="选择 AI账户"
              @change="applyFilters"
              @dropdown-visible-change="handleAccountOptionsDropdown"
              @search="handleAccountOptionsSearch"
            />
          </a-form-item>
          <a-form-item label="接口路径">
            <a-input v-model:value="pathFilter" allow-clear placeholder="/v1/responses" @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="状态码">
            <a-input v-model:value="statusCodeFilter" allow-clear placeholder="401 / 503" @press-enter="applyFilters" />
          </a-form-item>
        </a-form>
      </template>
      <template #actions>
        <a-button
          v-if="!fullBodyCaptureEnabled"
          :disabled="!runtime"
          :loading="fullBodyCaptureUpdating"
          @click="openFullBodyCaptureModal"
        >
          <template #icon><SettingOutlined /></template>
          开启临时捕获
        </a-button>
        <a-button v-else danger :loading="fullBodyCaptureUpdating" @click="disableFullBodyCapture">
          <template #icon><PoweroffOutlined /></template>
          关闭
        </a-button>
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
          <a-form-item label="AI账户">
            <AccountSelect
              v-model:value="accountIdFilter"
              v-model:selected-account="accountSelection"
              :accounts="accountOptions"
              :filter-option="false"
              :loading="accountOptionsLoading"
              allow-clear
              placeholder="选择 AI账户"
              @change="applyFilters"
              @dropdown-visible-change="handleAccountOptionsDropdown"
              @search="handleAccountOptionsSearch"
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

    <a-modal
      v-model:open="fullBodyCaptureModalOpen"
      title="临时全量捕获"
      ok-text="保存配置"
      cancel-text="取消"
      :confirm-loading="fullBodyCaptureUpdating"
      @ok="submitFullBodyCaptureConfig"
    >
      <a-form layout="vertical" class="full-capture-form">
        <a-alert
          type="warning"
          show-icon
          message="临时捕获会放大审计写入，建议只选择一个 AI 账户并设置较短有效期。"
        />
        <a-form-item label="捕获范围" required>
          <a-radio-group v-model:value="fullBodyCaptureForm.scope" button-style="solid">
            <a-radio-button value="account">指定 AI 账户</a-radio-button>
            <a-radio-button value="global">全局</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-if="fullBodyCaptureForm.scope === 'account'" label="AI 账户" required>
          <AccountSelect
            v-model:value="fullBodyCaptureForm.accountId"
            v-model:selected-account="fullBodyCaptureAccountSelection"
            :accounts="accountOptions"
            :filter-option="false"
            :loading="accountOptionsLoading"
            placeholder="选择要观察的 AI 账户"
            @dropdown-visible-change="handleAccountOptionsDropdown"
            @search="handleAccountOptionsSearch"
          />
        </a-form-item>
        <a-form-item label="普通成功请求">
          <a-switch v-model:checked="fullBodyCaptureForm.includeSuccess" checked-children="捕获 200" un-checked-children="按采样" />
          <div class="form-help">开启后命中范围内的普通成功请求也会写入原始审计，不再只按 10% 成功采样。</div>
        </a-form-item>
        <a-form-item label="有效期" required>
          <a-input-number v-model:value="fullBodyCaptureForm.durationMinutes" :min="1" :max="1440" :precision="0" addon-after="分钟" />
          <div class="form-help">到期后自动关闭；最大 1440 分钟。单请求仍受 64MB 活跃捕获硬上限约束。</div>
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer v-model:open="detailOpen" width="min(980px, 96vw)" title="审计详情" :body-style="{ padding: '18px' }">
      <a-spin :spinning="detailLoading">
        <template v-if="detail">
          <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
            <a-descriptions-item label="traceId">{{ detail.traceId }}</a-descriptions-item>
            <a-descriptions-item label="结果">{{ outcomeText(detail.auditOutcome) }}</a-descriptions-item>
            <a-descriptions-item label="来源">{{ trafficSourceText(detail.trafficSource) }}</a-descriptions-item>
            <a-descriptions-item label="接口">{{ detail.method }} {{ detail.path }}</a-descriptions-item>
            <a-descriptions-item label="状态码">{{ detail.finalStatusCode ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="AI账户">{{ displayName(detail.accountName, detail.accountId) }}</a-descriptions-item>
            <a-descriptions-item label="API Key">{{ displayName(detail.apiKeyName, detail.apiKeyId) }}</a-descriptions-item>
            <a-descriptions-item label="分组">{{ displayAuditGroupName(detail.groupName, detail.groupId) }}</a-descriptions-item>
            <a-descriptions-item label="系统账户">{{ displayName(detail.systemAccountName, detail.systemAccountId) }}</a-descriptions-item>
            <a-descriptions-item label="耗时">{{ formatDuration(detail.durationMs) }}</a-descriptions-item>
            <a-descriptions-item label="采样" :span="2">{{ detail.sampleReason }} / {{ detail.sampleBucket }}</a-descriptions-item>
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
                      <span>AI账户</span>
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
import { ArrowRightOutlined, CopyOutlined, PoweroffOutlined, SettingOutlined } from '@ant-design/icons-vue'
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type { AccountOptionSummary, AuditFullBodyCaptureScope, AuditLogDetail, AuditLogPayloadDetail, AuditLogSummary, AuditOutcome, AuditLogRuntime, AuditTrafficSource } from '@/types/domain'
import AccountSelect from '@/components/AccountSelect.vue'
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
import { accountSelectionForId, rememberAccountSelection, type AccountSelection } from '@/shared/accountLabelCache'
import { rememberGroupLabel } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { serverDateTimeTimestamp } from '@/shared/formatters'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
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
const fullBodyCaptureUpdating = ref(false)
const fullBodyCaptureModalOpen = ref(false)
const fullBodyCaptureAccountSelection = ref<AccountSelection | undefined>()
const fullBodyCaptureForm = ref<{
  scope: AuditFullBodyCaptureScope
  accountId: string
  includeSuccess: boolean
  durationMinutes: number
}>({
  scope: 'account',
  accountId: '',
  includeSuccess: true,
  durationMinutes: 15
})
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
const accountOptions = ref<AccountOptionSummary[]>([])
const accountOptionsLoading = ref(false)
const accountOptionsKeyword = ref('')
const accountOptionsCache = createShortLivedQueryCache<AccountOptionSummary[]>({ ttlMs: 10_000 })
let accountOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let accountOptionsRequestSeq = 0
let accountOptionsLoadingKey: string | undefined
let accountOptionsLoadingPromise: Promise<void> | undefined
let auditRuntimeRequestSeq = 0

function handleAccountOptionsSearch(value: string): void {
  accountOptionsKeyword.value = value
  clearAccountOptionsSearchTimer()
  accountOptionsSearchTimer = window.setTimeout(() => {
    accountOptionsSearchTimer = undefined
    void loadAccountOptions(accountOptionsKeyword.value)
  }, 250)
}

function handleAccountOptionsDropdown(open: boolean): void {
  if (open) {
    void loadAccountOptions()
  }
}

function resetAccountOptionsSearch(): void {
  accountOptionsKeyword.value = ''
  clearAccountOptionsSearchTimer()
}

function clearAccountOptionsSearchTimer(): void {
  if (accountOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(accountOptionsSearchTimer)
    accountOptionsSearchTimer = undefined
  }
}

const pageSize = 100
type AuditLogsPageState = {
  accountIdFilter: string
  accountSelection?: AccountSelection
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
  accountIdFilter: '',
  accountSelection: undefined,
  outcomeFilter: 'all',
  pagination: { current: 1, pageSize },
  pathFilter: '',
  statusCodeFilter: '',
  systemAccountFilter: allSystemAccountsValue,
  systemAccountSelection: undefined,
  traceIdFilter: '',
  trafficSourceFilter: 'all'
})
const pageStateCache = usePageStateCache<AuditLogsPageState>(undefined, defaultAuditLogsPageState, { version: 7 })
const initialPageState = pageStateCache.read()
const route = useRoute()
const router = useRouter()
const initialTraceId = routeTraceId()
const effectiveInitialPageState: AuditLogsPageState = initialTraceId
  ? { ...defaultAuditLogsPageState(), traceIdFilter: initialTraceId }
  : initialPageState

const traceIdFilter = ref(effectiveInitialPageState.traceIdFilter)
const accountIdFilter = ref(effectiveInitialPageState.accountIdFilter)
const accountSelection = ref<AccountSelection | undefined>(effectiveInitialPageState.accountSelection)
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
      resetAccountOptionsSearch()
    }
    const listResult = await fetchRecords(pageState)
    void refreshAuditRuntimeQuietly()
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
  { label: 'AI账户测试', value: 'manual_account_test' },
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
  if (accountIdFilter.value) count += 1
  if (outcomeFilter.value !== 'all') count += 1
  if (systemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (pathFilter.value.trim()) count += 1
  if (statusCodeFilter.value.trim()) count += 1
  if (trafficSourceFilter.value !== 'all') count += 1
  return count
})
const advancedFilterCount = computed(() => {
  let count = 0
  if (outcomeFilter.value !== 'all') count += 1
  if (systemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (accountIdFilter.value) count += 1
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
const fullBodyCaptureEnabled = computed(() => runtime.value?.settings.fullBodyCaptureEnabled ?? false)
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

async function refreshAuditRuntimeQuietly(): Promise<void> {
  const requestSeq = ++auditRuntimeRequestSeq
  try {
    const runtimeInfo = await api.auditLogs.runtime()
    if (requestSeq !== auditRuntimeRequestSeq) return
    runtime.value = runtimeInfo
  } catch (error) {
    if (requestSeq !== auditRuntimeRequestSeq) return
    console.error(error)
  }
}

function cancelAuditRuntimeRequest(): void {
  auditRuntimeRequestSeq += 1
}

watch(records, rememberAuditRecordGroupLabels, { immediate: true })
watch(detail, (nextDetail) => {
  rememberGroupLabel(nextDetail?.groupId, nextDetail?.groupName)
})

function applyFilters(): void {
  clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

function openFullBodyCaptureModal(): void {
  if (!runtime.value || fullBodyCaptureUpdating.value) return
  const config = runtime.value.settings.fullBodyCapture
  const fallbackAccountId = config.accountId || accountIdFilter.value || ''
  fullBodyCaptureForm.value = {
    scope: config.scope === 'global' ? 'global' : 'account',
    accountId: fallbackAccountId,
    includeSuccess: config.enabled ? config.includeSuccess : true,
    durationMinutes: remainingDurationMinutes(config.expiresAt) ?? 15
  }
  fullBodyCaptureAccountSelection.value = accountSelectionForId(fallbackAccountId) ?? accountSelection.value
  if (fallbackAccountId) {
    void ensureAccountOptionById(fallbackAccountId)
  }
  fullBodyCaptureModalOpen.value = true
}

async function submitFullBodyCaptureConfig(): Promise<void> {
  if (!runtime.value || fullBodyCaptureUpdating.value) return
  const form = fullBodyCaptureForm.value
  if (form.scope === 'account' && !form.accountId) {
    message.warning('请选择要定向捕获的 AI 账户')
    return
  }
  if (!Number.isInteger(form.durationMinutes) || form.durationMinutes < 1 || form.durationMinutes > 1440) {
    message.warning('请填写有效期')
    return
  }
  await updateFullBodyCapture({
    enabled: true,
    scope: form.scope,
    accountId: form.scope === 'account' ? form.accountId : undefined,
    includeSuccess: form.includeSuccess,
    durationMinutes: form.durationMinutes
  }, '临时全量捕获配置已保存')
  fullBodyCaptureModalOpen.value = false
}

async function disableFullBodyCapture(): Promise<void> {
  await updateFullBodyCapture({ enabled: false }, '临时全量捕获已关闭')
}

async function updateFullBodyCapture(
  payload: Parameters<typeof api.auditLogs.updateFullBodyCapture>[0],
  successMessage: string
): Promise<void> {
  if (!runtime.value || fullBodyCaptureUpdating.value) return
  const previousRuntime = runtime.value
  if (payload.enabled === false) {
    runtime.value = {
      ...previousRuntime,
      settings: {
        ...previousRuntime.settings,
        fullBodyCaptureEnabled: false,
        fullBodyCapture: {
          ...previousRuntime.settings.fullBodyCapture,
          enabled: false
        }
      }
    }
  }
  fullBodyCaptureUpdating.value = true
  try {
    const result = await api.auditLogs.updateFullBodyCapture(payload)
    if (runtime.value) {
      runtime.value = {
        ...runtime.value,
        settings: result.settings
      }
    }
    message.success(successMessage)
  } catch (error) {
    runtime.value = previousRuntime
    console.error(error)
    message.error('保存临时全量捕获配置失败')
  } finally {
    fullBodyCaptureUpdating.value = false
  }
}

function applyPageState(state: AuditLogsPageState): void {
  traceIdFilter.value = state.traceIdFilter
  accountIdFilter.value = state.accountIdFilter
  accountSelection.value = state.accountSelection
  outcomeFilter.value = state.outcomeFilter
  pathFilter.value = state.pathFilter
  statusCodeFilter.value = state.statusCodeFilter
  systemAccountFilter.value = state.systemAccountFilter
  systemAccountSelection.value = state.systemAccountSelection
  trafficSourceFilter.value = state.trafficSourceFilter
  pagination.current = state.pagination.current
  pagination.pageSize = state.pagination.pageSize
  resetSystemAccountOptionsSearch()
  resetAccountOptionsSearch()
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
  accountIdFilter.value = defaults.accountIdFilter
  accountSelection.value = defaults.accountSelection
  resetAccountOptionsSearch()
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
    accountId: accountIdFilter.value || undefined,
    outcome: outcomeFilter.value,
    path: pathFilter.value || undefined,
    statusCode: normalizedStatusCode(statusCodeFilter.value),
    systemAccountId,
    trafficSource: trafficSourceFilter.value === 'all' ? undefined : trafficSourceFilter.value
  })
}

async function loadAccountOptions(keyword = accountOptionsKeyword.value, force = false): Promise<void> {
  accountOptionsKeyword.value = keyword
  const requestKeyword = keyword.trim() || undefined
  const selectedIds = [accountIdFilter.value, fullBodyCaptureForm.value.accountId].filter(Boolean)
  const requestKey = JSON.stringify([requestKeyword ?? '', selectedIds])
  if (!force && accountOptionsLoadingKey === requestKey && accountOptionsLoadingPromise) {
    return accountOptionsLoadingPromise
  }
  const requestSeq = ++accountOptionsRequestSeq
  if (!force) {
    const cachedOptions = accountOptionsCache.get(requestKey)
    if (cachedOptions) {
      accountOptionsLoadingKey = undefined
      accountOptionsLoadingPromise = undefined
      accountOptionsLoading.value = false
      accountOptions.value = cachedOptions
      syncSelectedAccountFromOptions(cachedOptions)
      return
    }
  }
  accountOptionsLoading.value = true
  accountOptionsLoadingKey = requestKey
  accountOptionsLoadingPromise = (async () => {
    try {
      let nextOptions = await api.accounts.options({ keyword: requestKeyword, limit: 50 })
      nextOptions = await ensureSelectedAccountOption(nextOptions)
      accountOptionsCache.set(requestKey, nextOptions)
      if (requestSeq !== accountOptionsRequestSeq) return
      accountOptions.value = nextOptions
      syncSelectedAccountFromOptions(nextOptions)
    } catch (error) {
      if (requestSeq !== accountOptionsRequestSeq) return
      console.error(error)
      message.error('AI账户筛选项加载失败')
    } finally {
      if (accountOptionsLoadingKey === requestKey) {
        accountOptionsLoadingKey = undefined
        accountOptionsLoadingPromise = undefined
      }
      if (requestSeq === accountOptionsRequestSeq) {
        accountOptionsLoading.value = false
      }
    }
  })()
  return accountOptionsLoadingPromise
}

async function ensureSelectedAccountOption(options: AccountOptionSummary[]): Promise<AccountOptionSummary[]> {
  const selectedIds = [accountIdFilter.value, fullBodyCaptureForm.value.accountId].filter(Boolean)
  const missingIds = selectedIds.filter((id) => !options.some((account) => account.id === id))
  if (!missingIds.length) return options
  try {
    const selectedOptions = await api.accounts.options({ ids: missingIds, limit: 50 })
    return mergeOptionsById(selectedOptions, options)
  } catch {
    return options
  }
}

async function ensureAccountOptionById(accountId: string): Promise<void> {
  if (!accountId || accountOptions.value.some((account) => account.id === accountId)) return
  try {
    const selectedOptions = await api.accounts.options({ ids: [accountId], limit: 50 })
    accountOptions.value = mergeOptionsById(selectedOptions, accountOptions.value)
  } catch {
  }
}

function syncSelectedAccountFromOptions(options: AccountOptionSummary[]): void {
  if (!accountIdFilter.value || accountSelection.value) return
  accountSelection.value = accountSelectionForId(accountIdFilter.value, options)
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const seen = new Set<string>()
  const output: T[] = []
  for (const item of [...leading, ...trailing]) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    output.push(item)
  }
  return output
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

function remainingDurationMinutes(expiresAt?: string): number | undefined {
  if (!expiresAt) return undefined
  const timestamp = serverDateTimeTimestamp(expiresAt)
  if (timestamp === undefined) return undefined
  const remainingMs = timestamp - Date.now()
  if (!Number.isFinite(remainingMs) || remainingMs <= 0) return undefined
  return Math.min(Math.max(Math.ceil(remainingMs / 60_000), 1), 1440)
}

function snapshotPageState(): AuditLogsPageState {
  return {
    accountIdFilter: accountIdFilter.value,
    accountSelection: accountSelection.value,
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
watch(accountSelection, (selection) => rememberAccountSelection(selection), { deep: true, immediate: true })
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
onBeforeUnmount(() => {
  clearAccountOptionsSearchTimer()
  cancelAuditRuntimeRequest()
})
onDeactivated(() => {
  clearAccountOptionsSearchTimer()
  cancelAuditRuntimeRequest()
  closeTransientDetails()
})
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

.full-capture-form {
  display: grid;
  gap: 14px;
}

.full-capture-form :deep(.ant-alert) {
  margin-bottom: 2px;
}

.full-capture-form :deep(.ant-input-number) {
  width: 180px;
}

.form-help {
  margin-top: 6px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
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

@media (max-width: 900px) {
  .audit-full-capture-switch {
    width: 100%;
    justify-content: center;
  }
}

</style>
