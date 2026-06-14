<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="toolbarKeyword"
      :search-placeholder="toolbarSearchPlaceholder"
      :filter-title="toolbarFilterTitle"
      :active-filter-count="toolbarActiveFilterCount"
      :advanced-filter-count="toolbarAdvancedFilterCount"
      :refresh-loading="currentLoading"
      @refresh="refreshCurrentMode"
      @reset="resetCurrentMode"
      @search="applyCurrentMode"
    >
      <template #advanced-filters>
        <a-form v-if="viewMode === 'list'" layout="vertical" class="advanced-filter-form">
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
        <TableColumnManager
          :columns="auditLogColumns"
          :settings="columnSettings"
          :required-keys="['traceId']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
        <a-segmented v-model:value="viewMode" class="audit-mode-segmented" :options="viewModeOptions" @change="handleViewModeChange" />
      </template>
      <template #filters>
        <a-form v-if="viewMode === 'list'" layout="vertical">
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
      message="审计队列需要关注"
      :description="auditRuntimeAlertDescription"
    />

    <a-alert
      v-if="viewMode === 'search' && hotSearchResult?.message"
      :type="hotSearchResult.available === false || hotSearchResult.truncated ? 'warning' : 'info'"
      show-icon
      :message="hotSearchResult.message"
      class="audit-search-alert"
    />

    <AuditLogList
      :columns="managedColumns"
      :records="currentRecords"
      :loading="currentLoading"
      :pagination="currentTablePagination"
      :mobile-has-more="currentMobileHasMore"
      :loading-more="currentMobileLoadingMore"
      @change="handleCurrentTableChange"
      @detail="openDetail"
      @mobile-load-more="loadMoreCurrentMobileRecords"
      @mobile-refresh="refreshCurrentMobileRecords"
    />

    <AuditLogDetailDrawer
      v-model:open="detailOpen"
      :detail="detail"
      :loading="detailLoading"
      :payload-loading-id="payloadLoadingId"
      :selected-payload="selectedPayload"
      @load-payload="loadPayload"
    />
  </a-card>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type {
  AccountOptionSummary,
  AuditLogDetail,
  AuditLogHotSearchResult,
  AuditLogPayloadDetail,
  AuditLogRuntime,
  AuditLogSummary,
  AuditOutcome,
  AuditTrafficSource
} from '@/types/domain'
import AccountSelect from '@/components/AccountSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RuntimeAvailabilityAlert from '@/components/RuntimeAvailabilityAlert.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'
import { accountSelectionForId, rememberAccountSelection, type AccountSelection } from '@/shared/accountLabelCache'
import { rememberGroupLabel } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { allSystemAccountsValue, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import AuditLogList from './AuditLogList.vue'
import {
  displayAuditGroupName,
  displayName,
  formatDateTime,
  normalizedStatusCode,
  statusColor,
  trafficSourceText
} from './auditLogFormatters'
import {
  auditLogColumns,
  auditOutcomeOptions
} from './auditLogTableColumns'
import {
  finalizeMergedPayloadBody,
  mergeAuditPayloadWindow
} from './auditPayloadDetails'
import AuditLogDetailDrawer from './AuditLogDetailDrawer.vue'

type AuditLogViewMode = 'list' | 'search'

const detailLoading = ref(false)
const payloadLoadingId = ref('')
const runtime = ref<AuditLogRuntime>()
const hotSearchResult = ref<AuditLogHotSearchResult>()
const hotSearchRecords = ref<AuditLogSummary[]>([])
const hotSearchLoading = ref(false)
const detail = ref<AuditLogDetail>()
const selectedPayload = ref<AuditLogPayloadDetail>()
const detailOpen = ref(false)
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
let hotSearchRequestSeq = 0

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
const auditPayloadFullReadWindowBytes = 768 * 1024
type AuditLogsPageState = {
  accountIdFilter: string
  accountSelection?: AccountSelection
  hotSearchKeywordFilter: string
  outcomeFilter: AuditOutcome | 'all'
  pagination: { current: number; pageSize: number }
  pathFilter: string
  statusCodeFilter: string
  systemAccountFilter: string
  systemAccountSelection?: PrincipalSelection
  traceIdFilter: string
  trafficSourceFilter: AuditTrafficSource | 'all'
  viewMode: AuditLogViewMode
}
const defaultAuditLogsPageState = (): AuditLogsPageState => ({
  accountIdFilter: '',
  accountSelection: undefined,
  hotSearchKeywordFilter: '',
  outcomeFilter: 'all',
  pagination: { current: 1, pageSize },
  pathFilter: '',
  statusCodeFilter: '',
  systemAccountFilter: allSystemAccountsValue,
  systemAccountSelection: undefined,
  traceIdFilter: '',
  trafficSourceFilter: 'all',
  viewMode: 'list'
})
const pageStateCache = usePageStateCache<AuditLogsPageState>(undefined, defaultAuditLogsPageState, { version: 8 })
const initialPageState = pageStateCache.read()
const route = useRoute()
const router = useRouter()
const initialTraceId = routeTraceId()
const effectiveInitialPageState: AuditLogsPageState = initialTraceId
  ? { ...defaultAuditLogsPageState(), traceIdFilter: initialTraceId }
  : initialPageState

const traceIdFilter = ref(effectiveInitialPageState.traceIdFilter)
const hotSearchKeywordFilter = ref(effectiveInitialPageState.hotSearchKeywordFilter)
const accountIdFilter = ref(effectiveInitialPageState.accountIdFilter)
const accountSelection = ref<AccountSelection | undefined>(effectiveInitialPageState.accountSelection)
const outcomeFilter = ref<AuditOutcome | 'all'>(effectiveInitialPageState.outcomeFilter)
const pathFilter = ref(effectiveInitialPageState.pathFilter)
const statusCodeFilter = ref(effectiveInitialPageState.statusCodeFilter)
const systemAccountFilter = ref(effectiveInitialPageState.systemAccountFilter)
const systemAccountSelection = ref<PrincipalSelection | undefined>(effectiveInitialPageState.systemAccountSelection)
const trafficSourceFilter = ref<AuditTrafficSource | 'all'>(effectiveInitialPageState.trafficSourceFilter)
const viewMode = ref<AuditLogViewMode>(effectiveInitialPageState.viewMode === 'search' ? 'search' : 'list')
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
const viewModeOptions = [
  { label: '审计列表', value: 'list' },
  { label: '最近内容搜索', value: 'search' }
]
const trafficSourceOptions = [
  { label: '全部来源', value: 'all' },
  { label: '网关请求', value: 'gateway' },
  { label: 'AI账户测试', value: 'manual_account_test' },
  { label: '恢复探活', value: 'cooldown_retest' }
] satisfies Array<{ label: string; value: AuditTrafficSource | 'all' }>
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
const hotSearchActiveFilterCount = computed(() => normalizeHotSearchKeywordInput(hotSearchKeywordFilter.value) ? 1 : 0)
const toolbarKeyword = computed({
  get: () => viewMode.value === 'search' ? hotSearchKeywordFilter.value : traceIdFilter.value,
  set: (value: string) => {
    if (viewMode.value === 'search') {
      hotSearchKeywordFilter.value = value
    } else {
      traceIdFilter.value = value
    }
  }
})
const toolbarSearchPlaceholder = computed(() => viewMode.value === 'search'
  ? '搜索最近1小时审计原始内容'
  : '搜索 traceId')
const toolbarFilterTitle = computed(() => viewMode.value === 'search' ? '最近内容搜索' : '审计筛选')
const toolbarActiveFilterCount = computed(() => viewMode.value === 'search' ? hotSearchActiveFilterCount.value : activeFilterCount.value)
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
const toolbarAdvancedFilterCount = computed(() => viewMode.value === 'search' ? 0 : advancedFilterCount.value)
const currentRecords = computed(() => viewMode.value === 'search' ? hotSearchRecords.value : records.value)
const currentLoading = computed(() => viewMode.value === 'search' ? hotSearchLoading.value : loading.value)
const currentMobileHasMore = computed(() => viewMode.value === 'search' ? false : mobileHasMore.value)
const currentMobileLoadingMore = computed(() => viewMode.value === 'search' ? false : mobileLoadingMore.value)
const hotSearchTablePagination = computed(() => {
  const hasMore = hotSearchResult.value?.hasMore === true
  const count = hotSearchRecords.value.length
  return {
    current: 1,
    pageSize: pagination.pageSize,
    total: hasMore ? count + 1 : count,
    showSizeChanger: false,
    showTotal: () => hasMore
      ? `已显示前 ${count} 条匹配审计，还有更多`
      : `共 ${count} 条匹配审计`
  }
})
const currentTablePagination = computed(() => viewMode.value === 'search' ? hotSearchTablePagination.value : tablePagination.value)
const auditRuntimeRiskReasons = computed(() => {
  const info = runtime.value
  if (!info) return []
  const reasons: string[] = []
  if (info.flushLastError) reasons.push(`最近写入失败：${info.flushLastError}`)
  if (positiveRuntimeCount(info.droppedSuccessCount)) reasons.push(`成功审计丢弃 ${info.droppedSuccessCount} 条`)
  if (positiveRuntimeCount(info.droppedFailureCount)) reasons.push(`失败审计丢弃 ${info.droppedFailureCount} 条`)
  if (positiveRuntimeCount(info.droppedOverflowCount)) reasons.push(`队列溢出丢弃 ${info.droppedOverflowCount} 条`)
  if (positiveRuntimeCount(info.droppedOversizeCount)) reasons.push(`超限审计丢弃 ${info.droppedOversizeCount} 条`)
  return reasons
})
const auditRuntimeAlertVisible = computed(() => auditRuntimeRiskReasons.value.length > 0)
const auditRuntimeAlertDescription = computed(() => {
  const info = runtime.value
  if (!info) return ''
  const reasons = auditRuntimeRiskReasons.value
  const workerText = info.worker.available
    ? `后台进程${runtimeReadyText(info.worker.ready)}`
    : '后台进程状态不可用'
  return `${reasons.join('；')}。${workerText}。`
})
let skipNextRouteTraceRestore = false
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
watch(hotSearchRecords, rememberAuditRecordGroupLabels, { immediate: true })
watch(detail, (nextDetail) => {
  rememberGroupLabel(nextDetail?.groupId, nextDetail?.groupName)
})

function applyCurrentMode(): void {
  if (viewMode.value === 'search') {
    clearRouteTraceIdForManualState()
    void searchHotAuditLogs()
    return
  }
  applyFilters()
}

function refreshCurrentMode(): void {
  if (viewMode.value === 'search') {
    void searchHotAuditLogs()
    void refreshAuditRuntimeQuietly()
    return
  }
  refreshRecords()
}

function resetCurrentMode(): void {
  if (viewMode.value === 'search') {
    clearRouteTraceIdForManualState()
    hotSearchKeywordFilter.value = ''
    hotSearchRecords.value = []
    hotSearchResult.value = undefined
    return
  }
  resetFilters()
}

function handleViewModeChange(): void {
  if (viewMode.value === 'search') {
    clearRouteTraceIdForManualState()
    if (hotSearchKeywordFilter.value.trim() && !hotSearchResult.value) {
      void searchHotAuditLogs()
    }
    return
  }
  void loadData({ forceOptions: true })
}

function handleCurrentTableChange(paginationInfo: unknown): void {
  if (viewMode.value === 'search') return
  handleTableChange(paginationInfo)
}

function loadMoreCurrentMobileRecords(): void {
  if (viewMode.value === 'search') return
  loadMoreMobileRecords()
}

function refreshCurrentMobileRecords(): void {
  if (viewMode.value === 'search') {
    void searchHotAuditLogs()
    return
  }
  refreshMobileRecords()
}

function applyFilters(): void {
  clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

async function searchHotAuditLogs(): Promise<void> {
  const keyword = normalizeHotSearchKeywordInput(hotSearchKeywordFilter.value)
  if (hotSearchKeywordFilter.value !== keyword) {
    hotSearchKeywordFilter.value = keyword
  }
  const requestId = ++hotSearchRequestSeq
  if (!keyword) {
    hotSearchRecords.value = []
    hotSearchResult.value = undefined
    return
  }
  hotSearchLoading.value = true
  try {
    const result = await api.auditLogs.searchHot({
      keywords: keyword,
      limit: pagination.pageSize
    })
    if (requestId !== hotSearchRequestSeq) return
    hotSearchResult.value = result
    hotSearchRecords.value = result.items
    void refreshAuditRuntimeQuietly()
  } catch (error) {
    if (requestId !== hotSearchRequestSeq) return
    console.error(error)
    message.error('搜索最近审计内容失败')
  } finally {
    if (requestId === hotSearchRequestSeq) {
      hotSearchLoading.value = false
    }
  }
}

function normalizeHotSearchKeywordInput(value: string): string {
  return value.trim()
}

function applyPageState(state: AuditLogsPageState): void {
  traceIdFilter.value = state.traceIdFilter
  hotSearchKeywordFilter.value = state.hotSearchKeywordFilter
  accountIdFilter.value = state.accountIdFilter
  accountSelection.value = state.accountSelection
  outcomeFilter.value = state.outcomeFilter
  pathFilter.value = state.pathFilter
  statusCodeFilter.value = state.statusCodeFilter
  systemAccountFilter.value = state.systemAccountFilter
  systemAccountSelection.value = state.systemAccountSelection
  trafficSourceFilter.value = state.trafficSourceFilter
  viewMode.value = state.viewMode === 'search' ? 'search' : 'list'
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
  if (viewMode.value === 'search') {
    void searchHotAuditLogs()
  } else {
    void loadData({ forceOptions: true })
  }
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
  const selectedIds = [accountIdFilter.value].filter(Boolean)
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
  const selectedIds = [accountIdFilter.value].filter(Boolean)
  const missingIds = selectedIds.filter((id) => !options.some((account) => account.id === id))
  if (!missingIds.length) return options
  try {
    const selectedOptions = await api.accounts.options({ ids: missingIds, limit: 50 })
    return mergeOptionsById(selectedOptions, options)
  } catch {
    return options
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
  selectedPayload.value = undefined
  try {
    const nextPayload = await loadCompletePayload(payloadId, requestId)
    if (!nextPayload) return
    if (requestId === payloadRequestId) {
      selectedPayload.value = nextPayload
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

async function loadCompletePayload(payloadId: string, requestId: number): Promise<AuditLogPayloadDetail | undefined> {
  if (!detail.value) return undefined
  const auditLogId = detail.value.id
  let mergedPayload = await api.auditLogs.payload(auditLogId, payloadId, {
    offset: 0,
    limit: auditPayloadFullReadWindowBytes
  })
  if (requestId !== payloadRequestId) return undefined
  while (mergedPayload.bodyTruncated && mergedPayload.bodyNextOffset !== undefined) {
    const requestedOffset = mergedPayload.bodyNextOffset
    const nextPayload = await api.auditLogs.payload(auditLogId, payloadId, {
      offset: requestedOffset,
      limit: auditPayloadFullReadWindowBytes
    })
    if (requestId !== payloadRequestId) return undefined
    if (nextPayload.bodyBytesReturned <= 0) break
    mergedPayload = mergeAuditPayloadWindow(mergedPayload, nextPayload)
    if (
      nextPayload.bodyTruncated
      && nextPayload.bodyNextOffset !== undefined
      && nextPayload.bodyNextOffset <= requestedOffset
    ) {
      break
    }
  }
  return finalizeMergedPayloadBody(mergedPayload)
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

function runtimeReadyText(value: boolean | null): string {
  if (value === true) return '已就绪'
  if (value === false) return '未就绪'
  return '状态未知'
}

function positiveRuntimeCount(value: number | null): boolean {
  return typeof value === 'number' && value > 0
}

function snapshotPageState(): AuditLogsPageState {
  return {
    accountIdFilter: accountIdFilter.value,
    accountSelection: accountSelection.value,
    hotSearchKeywordFilter: hotSearchKeywordFilter.value,
    outcomeFilter: outcomeFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    pathFilter: pathFilter.value,
    statusCodeFilter: statusCodeFilter.value,
    systemAccountFilter: systemAccountFilter.value,
    systemAccountSelection: systemAccountSelection.value,
    traceIdFilter: traceIdFilter.value,
    trafficSourceFilter: trafficSourceFilter.value,
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

function loadInitialModeData(): void {
  if (viewMode.value === 'search') {
    void searchHotAuditLogs()
    void refreshAuditRuntimeQuietly()
    return
  }
  void loadData()
}

onMounted(loadInitialModeData)
onBeforeUnmount(() => {
  clearAccountOptionsSearchTimer()
  cancelAuditRuntimeRequest()
  hotSearchRequestSeq += 1
})
onDeactivated(() => {
  clearAccountOptionsSearchTimer()
  cancelAuditRuntimeRequest()
  hotSearchRequestSeq += 1
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

.audit-mode-segmented {
  white-space: nowrap;
}

.audit-search-alert {
  margin-bottom: 12px;
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

.mono-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

@media (max-width: 900px) {
  .audit-full-capture-switch {
    width: 100%;
    justify-content: center;
  }
}

</style>
