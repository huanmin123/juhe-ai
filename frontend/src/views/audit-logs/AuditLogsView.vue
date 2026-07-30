<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="toolbarKeyword"
      :search-placeholder="toolbarSearchPlaceholder"
      :filter-title="toolbarFilterTitle"
      :active-filter-count="toolbarActiveFilterCount"
      :advanced-filter-count="toolbarAdvancedFilterCount"
      :show-filters="viewMode === 'list'"
      :refresh-loading="currentLoading"
      @refresh="refreshCurrentMode"
      @reset="resetCurrentMode"
      @search="applyCurrentMode"
    >
      <template #advanced-filters>
        <AuditLogFilterForm
          v-model:outcome="outcomeFilter"
          v-model:traffic-source="trafficSourceFilter"
          v-model:system-account="systemAccountFilter"
          v-model:system-account-selection="systemAccountSelection"
          v-model:account-id="accountIdFilter"
          v-model:account-selection="accountSelection"
          v-model:session-id="sessionIdFilter"
          v-model:path="pathFilter"
          mode="advanced"
          :visible="viewMode === 'list'"
          :outcome-options="outcomeOptions"
          :traffic-source-options="trafficSourceOptions"
          :system-accounts="systemAccounts"
          :system-account-options-loading="systemAccountOptionsLoading"
          :account-options="accountOptions"
          :account-options-loading="accountOptionsLoading"
          @apply="applyFilters"
          @system-account-dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @system-account-search="handleSystemAccountOptionsSearch"
          @account-dropdown-visible-change="handleAccountOptionsDropdown"
          @account-search="handleAccountOptionsSearch"
        />
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
        <AuditLogFilterForm
          v-model:outcome="outcomeFilter"
          v-model:traffic-source="trafficSourceFilter"
          v-model:system-account="systemAccountFilter"
          v-model:system-account-selection="systemAccountSelection"
          v-model:account-id="accountIdFilter"
          v-model:account-selection="accountSelection"
          v-model:session-id="sessionIdFilter"
          v-model:path="pathFilter"
          mode="mobile"
          :visible="viewMode === 'list'"
          :outcome-options="outcomeOptions"
          :traffic-source-options="trafficSourceOptions"
          :system-accounts="systemAccounts"
          :system-account-options-loading="systemAccountOptionsLoading"
          :account-options="accountOptions"
          :account-options-loading="accountOptionsLoading"
          @apply="applyFilters"
          @system-account-dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @system-account-search="handleSystemAccountOptionsSearch"
          @account-dropdown-visible-change="handleAccountOptionsDropdown"
          @account-search="handleAccountOptionsSearch"
        />
      </template>
    </ResponsiveListToolbar>

    <AuditLogList
      :columns="managedColumns"
      :records="currentRecords"
      :loading="currentLoading"
      :pagination="currentTablePagination"
      :mobile-has-more="currentMobileHasMore"
      :mobile-pagination="viewMode === 'list'"
      :loading-more="currentMobileLoadingMore"
      :empty-description="auditEmptyDescription"
      @change="handleCurrentTableChange"
      @detail="openDetail"
      @filter-session="filterSession"
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
  AuditLogListItem,
  AuditOutcome,
  AuditTrafficSource
} from '@/types/domain'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { rememberAccountSelection, type AccountSelection } from '@/shared/accountLabelCache'
import { rememberGroupLabel } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { allSystemAccountsValue, selectedSystemAccountId } from '@/utils/systemAccountFilter'
import AuditLogList from './AuditLogList.vue'
import {
  displayAuditGroupName,
  displayName,
  formatDateTime,
  statusColor,
  trafficSourceText
} from './auditLogFormatters'
import {
  auditLogFilterCounts,
  auditLogListParams
} from './auditLogFilters'
import {
  auditLogColumns,
  auditOutcomeOptions
} from './auditLogTableColumns'
import AuditLogDetailDrawer from './AuditLogDetailDrawer.vue'
import AuditLogFilterForm from './AuditLogFilterForm.vue'
import { useAuditLogAccountOptions } from './useAuditLogAccountOptions'
import { useAuditLogDetailPayload } from './useAuditLogDetailPayload'
import { useAuditLogHotSearchState } from './useAuditLogHotSearchState'
import { useAuditLogModeBridge } from './useAuditLogModeBridge'
import { auditLogEmptyDescription } from './auditLogRetentionText'
import {
  auditLogRouteTraceId,
  useAuditLogTraceRoute
} from './useAuditLogTraceRoute'

type AuditLogViewMode = 'list' | 'search'

const {
  closeTransientDetails,
  detail,
  detailLoading,
  detailOpen,
  loadPayload,
  openDetail,
  payloadLoadingId,
  selectedPayload
} = useAuditLogDetailPayload()
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  selectedIds: () => [systemAccountFilter.value]
})
const auditEmptyDescription = auditLogEmptyDescription()

const pageSize = 100
type AuditLogsPageState = {
  accountIdFilter: string
  accountSelection?: AccountSelection
  hotSearchKeywordFilter: string
  outcomeFilter: AuditOutcome | 'all'
  pagination: { current: number; pageSize: number }
  pathFilter: string
  sessionIdFilter: string
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
  sessionIdFilter: '',
  systemAccountFilter: allSystemAccountsValue,
  systemAccountSelection: undefined,
  traceIdFilter: '',
  trafficSourceFilter: 'all',
  viewMode: 'list'
})
const pageStateCache = usePageStateCache<AuditLogsPageState>(undefined, defaultAuditLogsPageState, { version: 11 })
const initialPageState = pageStateCache.read()
const route = useRoute()
const router = useRouter()
const initialTraceId = auditLogRouteTraceId(route)
const effectiveInitialPageState: AuditLogsPageState = initialTraceId
  ? { ...defaultAuditLogsPageState(), traceIdFilter: initialTraceId }
  : initialPageState

const traceIdFilter = ref(effectiveInitialPageState.traceIdFilter)
const accountIdFilter = ref(effectiveInitialPageState.accountIdFilter)
const accountSelection = ref<AccountSelection | undefined>(effectiveInitialPageState.accountSelection)
const outcomeFilter = ref<AuditOutcome | 'all'>(effectiveInitialPageState.outcomeFilter)
const pathFilter = ref(effectiveInitialPageState.pathFilter)
const sessionIdFilter = ref(effectiveInitialPageState.sessionIdFilter)
const systemAccountFilter = ref(effectiveInitialPageState.systemAccountFilter)
const systemAccountSelection = ref<PrincipalSelection | undefined>(effectiveInitialPageState.systemAccountSelection)
const trafficSourceFilter = ref<AuditTrafficSource | 'all'>(effectiveInitialPageState.trafficSourceFilter)
const viewMode = ref<AuditLogViewMode>(effectiveInitialPageState.viewMode === 'search' ? 'search' : 'list')
const {
  clearSearchTimer: clearAccountOptionsSearchTimer,
  handleDropdown: handleAccountOptionsDropdown,
  handleSearch: handleAccountOptionsSearch,
  load: loadAccountOptions,
  loading: accountOptionsLoading,
  options: accountOptions,
  resetSearch: resetAccountOptionsSearch
} = useAuditLogAccountOptions({
  accountSelection,
  selectedSystemAccountId: () => selectedSystemAccountId(systemAccountFilter.value, true),
  selectedAccountId: () => accountIdFilter.value
})
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
} = useResponsivePagedList<AuditLogListItem, { forceOptions?: boolean }>({
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
    return await fetchRecords(pageState)
  },
  requestSignature: (_options, pageState) => auditLogRequestParams(pageState),
  onError: (error) => {
    console.error(error)
    message.error('加载审计日志失败')
  }
})
const {
  cancelHotSearchRequest,
  hotSearchActiveFilterCount,
  hotSearchKeywordFilter,
  hotSearchLoading,
  hotSearchRecords,
  hotSearchResult,
  hotSearchTablePagination,
  resetHotSearch,
  searchHotAuditLogs
} = useAuditLogHotSearchState({
  initialKeyword: effectiveInitialPageState.hotSearchKeywordFilter,
  pageSize: () => pagination.pageSize
})

const outcomeOptions = auditOutcomeOptions as Array<{ label: string; value: AuditOutcome | 'all' }>
const viewModeOptions = [
  { label: '审计列表', value: 'list' },
  { label: '最近内容搜索', value: 'search' }
]
const trafficSourceOptions = [
  { label: '全部来源', value: 'all' },
  { label: '网关请求', value: 'gateway' },
  { label: 'AI账户测试', value: 'manual_account_test' },
  { label: '健康检查', value: 'account_health_check' },
  { label: '快速恢复检测', value: 'runtime_recovery_probe' },
  { label: '冷却账户复测', value: 'cooldown_retest' },
  { label: '混合路由选型', value: 'hybrid_scoring' },
  { label: '回答质量复核', value: 'hybrid_quality_scoring' }
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
const currentFilterValues = computed(() => ({
  accountIdFilter: accountIdFilter.value,
  outcomeFilter: outcomeFilter.value,
  pathFilter: pathFilter.value,
  sessionIdFilter: sessionIdFilter.value,
  systemAccountFilter: systemAccountFilter.value,
  traceIdFilter: traceIdFilter.value,
  trafficSourceFilter: trafficSourceFilter.value
}))
const filterCounts = computed(() => auditLogFilterCounts(currentFilterValues.value))
const activeFilterCount = computed(() => filterCounts.value.active)
const advancedFilterCount = computed(() => filterCounts.value.advanced)
watch(records, rememberAuditRecordGroupLabels, { immediate: true })
watch(hotSearchRecords, rememberAuditRecordGroupLabels, { immediate: true })
watch(detail, (nextDetail) => {
  rememberGroupLabel(nextDetail?.groupId, nextDetail?.groupName)
})
watch(systemAccountFilter, () => {
  accountIdFilter.value = ''
  accountSelection.value = undefined
  resetAccountOptionsSearch()
})
watch(traceIdFilter, (value) => {
  if (value.trim()) clearFiltersForDirectTraceLookup()
}, { immediate: true, flush: 'sync' })

function applyFilters(): void {
  clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

function filterSession(sessionId: string): void {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return
  clearRouteTraceIdForManualState()
  applyPageState({ ...defaultAuditLogsPageState(), sessionIdFilter: normalizedSessionId })
  resetPagination()
  void loadData()
}

function clearFiltersForDirectTraceLookup(): void {
  const defaults = defaultAuditLogsPageState()
  accountIdFilter.value = defaults.accountIdFilter
  accountSelection.value = defaults.accountSelection
  outcomeFilter.value = defaults.outcomeFilter
  pathFilter.value = defaults.pathFilter
  sessionIdFilter.value = defaults.sessionIdFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountSelection.value = defaults.systemAccountSelection
  trafficSourceFilter.value = defaults.trafficSourceFilter
  resetSystemAccountOptionsSearch()
  resetAccountOptionsSearch()
}

function applyPageState(state: AuditLogsPageState): void {
  traceIdFilter.value = state.traceIdFilter
  hotSearchKeywordFilter.value = state.hotSearchKeywordFilter
  systemAccountFilter.value = state.systemAccountFilter
  systemAccountSelection.value = state.systemAccountSelection
  accountIdFilter.value = state.accountIdFilter
  accountSelection.value = state.accountSelection
  outcomeFilter.value = state.outcomeFilter
  pathFilter.value = state.pathFilter
  sessionIdFilter.value = state.sessionIdFilter
  trafficSourceFilter.value = state.trafficSourceFilter
  viewMode.value = state.viewMode === 'search' ? 'search' : 'list'
  pagination.current = state.pagination.current
  pagination.pageSize = state.pagination.pageSize
  if (traceIdFilter.value.trim()) clearFiltersForDirectTraceLookup()
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
  sessionIdFilter.value = defaults.sessionIdFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountSelection.value = defaults.systemAccountSelection
  trafficSourceFilter.value = defaults.trafficSourceFilter
  resetSystemAccountOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

function fetchRecords(pageState: { current: number; pageSize: number }) {
  return api.auditLogs.list(auditLogRequestParams(pageState))
}

function auditLogRequestParams(pageState: { current: number; pageSize: number }) {
  return auditLogListParams(currentFilterValues.value, pageState)
}

function rememberAuditRecordGroupLabels(items: AuditLogListItem[]): void {
  for (const item of items) {
    rememberGroupLabel(item.groupId, item.groupName)
  }
}

function snapshotPageState(): AuditLogsPageState {
  return {
    accountIdFilter: accountIdFilter.value,
    accountSelection: accountSelection.value,
    hotSearchKeywordFilter: hotSearchKeywordFilter.value,
    outcomeFilter: outcomeFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    pathFilter: pathFilter.value,
    sessionIdFilter: sessionIdFilter.value,
    systemAccountFilter: systemAccountFilter.value,
    systemAccountSelection: systemAccountSelection.value,
    traceIdFilter: traceIdFilter.value,
    trafficSourceFilter: trafficSourceFilter.value,
    viewMode: viewMode.value
  }
}

const traceRoute = useAuditLogTraceRoute({
  applyRouteTraceId,
  currentTraceId: () => traceIdFilter.value,
  onManualRouteTraceCleared: () => pageStateCache.scheduleWrite(snapshotPageState),
  restoreAfterRouteTraceCleared: restorePageStateAfterRouteTraceCleared,
  route,
  router
})
const { clearRouteTraceIdForManualState, routeTraceId } = traceRoute
const {
  applyCurrentMode,
  currentLoading,
  currentMobileHasMore,
  currentMobileLoadingMore,
  currentRecords,
  currentTablePagination,
  handleCurrentTableChange,
  handleViewModeChange,
  loadMoreCurrentMobileRecords,
  refreshCurrentMobileRecords,
  refreshCurrentMode,
  resetCurrentMode,
  toolbarActiveFilterCount,
  toolbarAdvancedFilterCount,
  toolbarFilterTitle,
  toolbarKeyword,
  toolbarSearchPlaceholder
} = useAuditLogModeBridge({
  activeFilterCount,
  advancedFilterCount,
  applyFilters,
  clearRouteTraceIdForManualState,
  handleTableChange,
  hotSearchActiveFilterCount,
  hotSearchKeywordFilter,
  hotSearchLoading,
  hotSearchRecords,
  hotSearchResult,
  hotSearchTablePagination,
  loadData,
  loading,
  loadMoreMobileRecords,
  mobileHasMore,
  mobileLoadingMore,
  records,
  refreshMobileRecords,
  resetFilters,
  resetHotSearch,
  searchHotAuditLogs,
  tablePagination,
  traceIdFilter,
  viewMode
})

watch(snapshotPageState, () => {
  if (routeTraceId()) {
    pageStateCache.cancelPendingWrite()
    return
  }
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })
watch(accountSelection, (selection) => rememberAccountSelection(selection), { deep: true, immediate: true })
watch(systemAccountSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

function loadInitialModeData(): void {
  if (viewMode.value === 'search') {
    void searchHotAuditLogs()
    return
  }
  void loadData()
}

onMounted(() => {
  loadInitialModeData()
})
onBeforeUnmount(() => {
  traceRoute.stop()
  clearAccountOptionsSearchTimer()
  cancelHotSearchRequest()
})
onDeactivated(() => {
  clearAccountOptionsSearchTimer()
  cancelHotSearchRequest()
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

.form-help {
  margin-top: 6px;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
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
