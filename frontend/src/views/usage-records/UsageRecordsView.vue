<template>
  <a-card class="page-card responsive-page-card">
    <UsageRecordsFilterToolbar
      v-model:keyword="accountNameFilter"
      :date-range="dateRangeFilter"
      v-model:group-id="groupFilter"
      v-model:group-selection="groupFilterSelection"
      v-model:client-ip="clientIpFilter"
      v-model:model="modelFilter"
      v-model:result="resultFilter"
      v-model:status-code="statusCodeFilter"
      v-model:system-account-id="systemAccountFilter"
      v-model:system-account-selection="systemAccountFilterSelection"
      v-model:trace-id="traceIdFilter"
      v-model:traffic-source="trafficSourceFilter"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :business-filters-disabled="businessFiltersDisabled"
      :group-disabled="groupFilterDisabled"
      :group-options="groups"
      :group-options-loading="groupOptionsLoading"
      :is-management-view="isManagementView"
      :model-options="modelOptions"
      :models-loading="modelOptionsLoading"
      :refresh-loading="loading"
      :result-options="resultOptions"
      :system-accounts="systemAccounts"
      :system-accounts-loading="systemAccountOptionsLoading"
      :traffic-source-options="trafficSourceOptions"
      @group-change="handleGroupFilterChange"
      @group-dropdown="handleGroupOptionsDropdown"
      @group-search="handleGroupOptionsSearch"
      @model-options-dropdown="handleModelOptionsDropdown"
      @model-options-search="handleModelOptionsSearch"
      @reset="resetFilters"
      @refresh="refreshRecords"
      @search="applyFilters"
      @system-account-change="handleSystemAccountFilterChange"
      @system-account-dropdown="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
      @update:date-range="handleDateRangeUpdate"
    >
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['account']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
      </template>
    </UsageRecordsFilterToolbar>

    <UsageRecordsTable
      :columns="managedColumns"
      :is-management-view="isManagementView"
      :loading="loading"
      :mobile-has-more="mobileHasMore"
      :mobile-records="mobileRecords"
      :loading-more="mobileLoadingMore"
      :pagination="tablePagination"
      :records="filteredRecords"
      empty-description="当前条件下没有使用记录。"
      @change="handleTableChange"
      @copy-trace-id="copyTraceId"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
      @open-trace-target="openTraceTarget"
    />
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import type { Dayjs } from 'dayjs'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedGroupsApi, useScopedUsageRecordsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { rememberGroupLabel, rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { trimmedRouteQueryValue } from '@/shared/routeQuery'
import type { UsageRecordSummary, UsageRecordTrafficSource } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import UsageRecordsFilterToolbar from './UsageRecordsFilterToolbar.vue'
import UsageRecordsTable from './UsageRecordsTable.vue'
import { usageRecordActiveFilterCount, usageRecordAdvancedFilterCount, usageRecordListParams } from './usageRecordFilters'
import {
  defaultUsageRecordTrafficSourceFilter,
  defaultUsageRecordsPageState,
  parseUsageRecordDateRange,
  usageRecordDateRangeParam,
  usageRecordsPageSize,
  type UsageRecordSortField,
  type UsageRecordsPageState,
  type UsageRecordTableSortOrder
} from './usageRecordPageState'
import {
  normalizeUsageRecordTableSorter,
  usageRecordColumnStorageKey,
  usageRecordPaginationFromTable,
  usageRecordTableColumns
} from './usageRecordTableConfig'
import { useUsageRecordGroupOptions } from './useUsageRecordGroupOptions'
import { useUsageRecordModelOptions } from './useUsageRecordModelOptions'
import { useUsageRecordTraceRoute } from './useUsageRecordTraceRoute'

type TraceTarget = 'audit' | 'runtime'
const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
const route = useRoute()
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const defaultPageState = () => defaultUsageRecordsPageState(defaultUsageRecordTrafficSourceFilter(route.meta.viewScope === 'admin'))
const pageStateCache = usePageStateCache<UsageRecordsPageState>(undefined, defaultPageState, { version: 11 })
const initialRouteTraceId = trimmedRouteQueryValue(route.query.traceId)
const cachedInitialPageState = pageStateCache.read()
const initialPageState = initialRouteTraceId
  ? { ...defaultPageState(), traceIdFilter: initialRouteTraceId }
  : cachedInitialPageState

const accountNameFilter = ref(initialPageState.accountNameFilter)
const clientIpFilter = ref(initialPageState.clientIpFilter ?? '')
const dateRangeFilter = ref<[Dayjs, Dayjs] | undefined>(parseUsageRecordDateRange(initialPageState.dateRangeFilter))
const dateMode = ref(initialPageState.dateMode)
const deploymentTimezone = ref(browserTimezone)
const autoDateRolloverTimer = ref<ReturnType<typeof setTimeout>>()
const autoDateLifecycleActive = ref(false)
const groupFilterSelection = ref<GroupSelection | undefined>(initialPageState.groupFilter)
const modelFilter = ref(initialPageState.modelFilter ?? '')
const resultFilter = ref<'all' | 'success' | 'failed'>(initialPageState.resultFilter)
const statusCodeFilter = ref<string>(initialPageState.statusCodeFilter)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const traceIdFilter = ref(initialPageState.traceIdFilter ?? '')
const trafficSourceFilter = ref<UsageRecordTrafficSource | 'all'>(initialPageState.trafficSourceFilter)
const usageRecordsApi = useScopedUsageRecordsApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const router = useRouter()
const sortState = ref<{ field: UsageRecordSortField; order: UsageRecordTableSortOrder }>(initialPageState.sortState)
const groupFilter = computed({
  get: () => groupFilterSelection.value?.id,
  set: (id: string | undefined) => {
    groupFilterSelection.value = selectedGroupSelection(id)
  }
})
const groupFilterDisabled = computed(() => isManagementView.value && !scopedSystemAccountId(systemAccountFilter.value))
const businessFiltersDisabled = computed(() => isManagementView.value && !scopedSystemAccountId(systemAccountFilter.value))
if (businessFiltersDisabled.value) {
  restoreAllSystemAccountsAutoFilters()
}
const modelOptionsScopeParams = computed(() => {
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(systemAccountFilter.value) : undefined
  return systemAccountId ? { systemAccountId } : undefined
})
const {
  handleDropdown: handleModelOptionsDropdown,
  handleSearch: handleModelOptionsSearch,
  modelOptions,
  modelOptionsLoading,
  resetModelOptions
} = useUsageRecordModelOptions({
  scopeParams: modelOptionsScopeParams,
  selectedModel: computed(() => modelFilter.value)
})
const { loadUsageStatsWindow } = useUsageStatsWindow()
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  onMissingSelectedIds: (ids) => {
    if (!ids.includes(systemAccountFilter.value)) return
    systemAccountFilter.value = allSystemAccountsValue
    systemAccountFilterSelection.value = undefined
    groupFilterSelection.value = undefined
    resetSystemAccountOptionsSearch()
    resetGroupOptionsSearch()
    resetPagination()
    void loadData({ forceOptions: true })
  },
  selectedIds: () => [systemAccountFilter.value]
})
const {
  clearSearchTimer: clearGroupOptionsSearchTimer,
  groups,
  handleDropdown: handleGroupOptionsDropdown,
  handleSearch: handleGroupOptionsSearch,
  load: loadGroupOptions,
  loading: groupOptionsLoading,
  resetSearch: resetGroupOptionsSearch,
  selectedGroupSelection,
  syncSelectedGroupSelection
} = useUsageRecordGroupOptions({
  groupFilterSelection,
  groupsApi,
  isManagementView,
  onSelectedGroupMissing: () => {
    resetPagination()
    void loadData({ forceOptions: true })
  },
  selectedGroupId: () => groupFilter.value,
  systemAccountId: () => scopedSystemAccountId(systemAccountFilter.value)
})
const {
  items: records,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  loadData,
  loadMoreMobile: loadMoreMobileRecords,
  refreshMobile: refreshMobileRecordsList,
  resetPagination
} = useResponsivePagedList<UsageRecordSummary, { forceOptions?: boolean }>({
  pageSize: usageRecordsPageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条使用记录，还有更多`
    : `共 ${total} 条使用记录`,
  fetchPage: async (options, pageState) => {
    if (options.forceOptions === true) {
      resetSystemAccountOptionsSearch()
      resetGroupOptionsSearch()
      resetModelOptions()
    }
    return await fetchRecords(pageState)
  },
  requestSignature: (_options, pageState) => [
    isManagementView.value ? 'management' : 'self',
    usageRecordRequestParams(pageState)
  ],
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载使用记录失败'))
  },
})

const resultOptions = [
  { label: '全部结果', value: 'all' },
  { label: '成功', value: 'success' },
  { label: '失败', value: 'failed' }
] satisfies Array<{ label: string; value: 'all' | 'success' | 'failed' }>
const trafficSourceOptions = [
  { label: '全部来源', value: 'all' },
  { label: '网关请求', value: 'gateway' },
  { label: '账号测试', value: 'manual_account_test' },
  { label: '健康检查', value: 'account_health_check' },
  { label: '运行态恢复探针', value: 'runtime_recovery_probe' },
  { label: '恢复探活', value: 'cooldown_retest' },
  { label: '混合评分', value: 'hybrid_scoring' },
  { label: '混合质量评分', value: 'hybrid_quality_scoring' }
] satisfies Array<{ label: string; value: UsageRecordTrafficSource | 'all' }>

const activeFilterCount = computed(() => {
  return usageRecordActiveFilterCount(filterCountInput())
})
const advancedFilterCount = computed(() => {
  return usageRecordAdvancedFilterCount(filterCountInput())
})

function filterCountInput() {
  return {
    accountName: accountNameFilter.value,
    clientIp: clientIpFilter.value,
    dateRangeSelected: dateMode.value === 'manual' && Boolean(dateRangeFilter.value),
    groupId: groupFilter.value,
    model: modelFilter.value,
    result: resultFilter.value,
    statusCode: statusCodeFilter.value,
    systemAccountId: systemAccountFilter.value,
    allSystemAccountsValue,
    traceId: traceIdFilter.value,
    trafficSource: trafficSourceFilter.value
  }
}

const filteredRecords = computed(() => records.value)
const mobileRecords = computed(() => records.value)
const rawColumns = computed(() => {
  return usageRecordTableColumns({
    isManagementView: isManagementView.value,
    columnSortOrder
  })
})
const columnStorageKey = computed(() => usageRecordColumnStorageKey(isManagementView.value))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['account'],
  minVisible: 1
})
const traceRoute = useUsageRecordTraceRoute({
  applyRouteTraceId,
  currentTraceId: () => traceIdFilter.value,
  onManualRouteTraceCleared: () => pageStateCache.scheduleWrite(snapshotPageState),
  restoreAfterRouteTraceCleared: restorePageStateAfterRouteTraceCleared,
  route,
  router
})

function resetFilters(): void {
  traceRoute.clearRouteTraceIdForManualState()
  const defaults = defaultPageState()
  accountNameFilter.value = defaults.accountNameFilter
  clientIpFilter.value = defaults.clientIpFilter
  dateRangeFilter.value = autoTodayDateRange()
  dateMode.value = defaults.dateMode
  groupFilterSelection.value = defaults.groupFilter
  modelFilter.value = defaults.modelFilter
  resultFilter.value = defaults.resultFilter
  statusCodeFilter.value = defaults.statusCodeFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountFilterSelection.value = defaults.systemAccountFilterSelection
  traceIdFilter.value = defaults.traceIdFilter
  trafficSourceFilter.value = defaults.trafficSourceFilter
  sortState.value = defaults.sortState
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData({ forceOptions: true })
}

async function handleTableChange(paginationInfo: unknown, _filters: unknown, sorter: unknown): Promise<void> {
  updatePaginationFromTable(paginationInfo)
  const normalized = normalizeUsageRecordTableSorter(sorter)
  sortState.value = businessFiltersDisabled.value
    ? { field: 'createdAt', order: 'descend' }
    : normalized ?? { field: 'createdAt', order: 'descend' }
  await loadData()
}

function columnSortOrder(field: UsageRecordSortField): UsageRecordTableSortOrder {
  return sortState.value.field === field ? sortState.value.order : null
}

function updatePaginationFromTable(paginationInfo: unknown): void {
  const next = usageRecordPaginationFromTable(paginationInfo, usageRecordsPageSize)
  if (!next) return
  pagination.current = next.current
  pagination.pageSize = next.pageSize
}

function applyFilters(): void {
  traceRoute.clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

function handleDateRangeUpdate(value?: [Dayjs, Dayjs]): void {
  dateRangeFilter.value = value
  dateMode.value = 'manual'
}

function refreshRecords(): void {
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

async function refreshMobileRecords(): Promise<void> {
  await refreshMobileRecordsList({ forceOptions: true })
}

function applyRouteTraceId(traceId: string): void {
  pageStateCache.flushPendingWrite()
  traceIdFilter.value = traceId
  resetPagination()
  void loadData()
}

function restorePageStateAfterRouteTraceCleared(): void {
  applyPageState(pageStateCache.read())
  void loadData({ forceOptions: true })
}

function applyPageState(state: UsageRecordsPageState): void {
  accountNameFilter.value = state.accountNameFilter
  clientIpFilter.value = state.clientIpFilter
  dateRangeFilter.value = parseUsageRecordDateRange(state.dateRangeFilter)
  dateMode.value = state.dateMode
  groupFilterSelection.value = state.groupFilter
  modelFilter.value = state.modelFilter
  resultFilter.value = state.resultFilter
  statusCodeFilter.value = state.statusCodeFilter
  systemAccountFilter.value = state.systemAccountFilter
  systemAccountFilterSelection.value = state.systemAccountFilterSelection
  traceIdFilter.value = state.traceIdFilter
  trafficSourceFilter.value = state.trafficSourceFilter
  sortState.value = state.sortState
  pagination.current = state.pagination.current
  pagination.pageSize = state.pagination.pageSize
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  if (businessFiltersDisabled.value) {
    restoreAllSystemAccountsAutoFilters()
  }
}

function handleGroupFilterChange(): void {
  resetGroupOptionsSearch()
  applyFilters()
}

function handleSystemAccountFilterChange(): void {
  groupFilterSelection.value = undefined
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
    restoreAllSystemAccountsAutoFilters()
  }
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

function restoreAllSystemAccountsAutoFilters(): void {
  accountNameFilter.value = ''
  clientIpFilter.value = ''
  dateRangeFilter.value = autoTodayDateRange()
  dateMode.value = 'auto'
  groupFilterSelection.value = undefined
  modelFilter.value = ''
  resultFilter.value = 'all'
  statusCodeFilter.value = ''
  traceIdFilter.value = ''
  trafficSourceFilter.value = 'all'
  sortState.value = { field: 'createdAt', order: 'descend' }
}

async function fetchRecords(pageState: { current: number; pageSize: number }) {
  return await usageRecordsApi.list(usageRecordRequestParams(pageState))
}

function usageRecordRequestParams(pageState: { current: number; pageSize: number }) {
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(systemAccountFilter.value) : undefined
  return usageRecordListParams({
    page: pageState.current,
    pageSize: pageState.pageSize,
    accountName: accountNameFilter.value,
    clientIp: clientIpFilter.value,
    dateRange: dateMode.value === 'manual' ? usageRecordDateRangeParam(dateRangeFilter.value) : undefined,
    groupId: groupFilter.value,
    model: modelFilter.value,
    result: resultFilter.value,
    statusCode: statusCodeFilter.value,
    systemAccountId,
    traceId: traceIdFilter.value,
    trafficSource: trafficSourceFilter.value,
    sortBy: sortState.value.field,
    sortOrder: sortState.value.order === 'ascend' ? 'asc' : 'desc'
  })
}

async function copyTraceId(traceId?: string): Promise<void> {
  await copyTextToClipboard(traceId ?? '', 'traceId 已复制')
}

function openTraceTarget(traceId: string | undefined, target: TraceTarget): void {
  const text = traceId?.trim()
  if (!text) return
  void router.push({
    path: traceTargetPath(target),
    query: { traceId: text }
  })
}

function traceTargetPath(target: TraceTarget): string {
  if (target === 'runtime') return '/runtime-logs'
  return '/audit-logs'
}

function snapshotPageState(): UsageRecordsPageState {
  return {
    accountNameFilter: accountNameFilter.value,
    clientIpFilter: clientIpFilter.value,
    dateRangeFilter: usageRecordDateRangeParam(dateRangeFilter.value),
    dateMode: dateMode.value,
    groupFilter: groupFilterSelection.value,
    modelFilter: modelFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    resultFilter: resultFilter.value,
    sortState: sortState.value,
    statusCodeFilter: statusCodeFilter.value,
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value,
    traceIdFilter: traceIdFilter.value,
    trafficSourceFilter: trafficSourceFilter.value
  }
}

function refreshAutoDateAfterRollover(): void {
  if (!autoDateLifecycleActive.value || dateMode.value !== 'auto') return
  const nextRange = autoTodayDateRange()
  const current = usageRecordDateRangeParam(dateRangeFilter.value)
  const next = usageRecordDateRangeParam(nextRange)
  if (current?.[0] === next?.[0] && current?.[1] === next?.[1]) return
  dateRangeFilter.value = nextRange
  resetPagination()
  void loadData()
}

function scheduleAutoDateRollover(): void {
  clearAutoDateRolloverTimer()
  if (!autoDateLifecycleActive.value || dateMode.value !== 'auto') return
  const now = new Date()
  autoDateRolloverTimer.value = setTimeout(() => {
    refreshAutoDateAfterRollover()
    scheduleAutoDateRollover()
  }, millisecondsUntilNextDeploymentDay(now) + 100)
}

function autoTodayDateRange(): [Dayjs, Dayjs] | undefined {
  const today = deploymentDateKey(new Date())
  return parseUsageRecordDateRange([today, today])
}

function deploymentDateKey(value: Date): string {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: deploymentTimezone.value,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit'
  }).formatToParts(value)
  const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((item) => item.type === type)?.value ?? ''
  return `${part('year')}-${part('month')}-${part('day')}`
}

function millisecondsUntilNextDeploymentDay(now: Date): number {
  const currentKey = deploymentDateKey(now)
  let low = now.getTime()
  let high = low + 30 * 60 * 60 * 1000
  while (high - low > 1000) {
    const middle = low + Math.floor((high - low) / 2)
    if (deploymentDateKey(new Date(middle)) === currentKey) {
      low = middle
    } else {
      high = middle
    }
  }
  return Math.max(1, high - now.getTime())
}

async function loadDeploymentTimezone(): Promise<void> {
  if (!isManagementView.value) return
  try {
    const usageWindow = await loadUsageStatsWindow({ viewScope: 'admin' })
    deploymentTimezone.value = usageWindow.timezone
    refreshAutoDateAfterRollover()
    scheduleAutoDateRollover()
  } catch {
    // The list API still applies the deployment timezone when auto mode omits dates.
  }
}

function clearAutoDateRolloverTimer(): void {
  if (autoDateRolloverTimer.value === undefined) return
  clearTimeout(autoDateRolloverTimer.value)
  autoDateRolloverTimer.value = undefined
}

function handleVisibilityChange(): void {
  if (document.visibilityState !== 'visible') return
  refreshAutoDateAfterRollover()
  scheduleAutoDateRollover()
}

function handleWindowFocus(): void {
  refreshAutoDateAfterRollover()
  scheduleAutoDateRollover()
}

function activateAutoDateLifecycle(): void {
  if (autoDateLifecycleActive.value) return
  autoDateLifecycleActive.value = true
  document.addEventListener('visibilitychange', handleVisibilityChange)
  window.addEventListener('focus', handleWindowFocus)
  refreshAutoDateAfterRollover()
  scheduleAutoDateRollover()
}

function deactivateAutoDateLifecycle(): void {
  if (autoDateLifecycleActive.value) {
    autoDateLifecycleActive.value = false
    document.removeEventListener('visibilitychange', handleVisibilityChange)
    window.removeEventListener('focus', handleWindowFocus)
  }
  clearAutoDateRolloverTimer()
}

watch(snapshotPageState, () => {
  if (traceRoute.routeTraceId()) {
    pageStateCache.cancelPendingWrite()
    return
  }
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })
watch(groupFilterDisabled, (disabled) => {
  if (!disabled) return
  groupFilterSelection.value = undefined
  groups.value = []
}, { immediate: true })
watch(records, (items) => {
  for (const item of items) {
    rememberGroupLabel(item.groupId, item.groupName)
  }
  rememberGroupSelection(groupFilterSelection.value)
  rememberPrincipalSelection(systemAccountFilterSelection.value)
  syncSelectedGroupSelection()
}, { immediate: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(dateMode, scheduleAutoDateRollover)

onBeforeUnmount(() => {
  clearGroupOptionsSearchTimer()
  deactivateAutoDateLifecycle()
  traceRoute.stop()
})

onActivated(activateAutoDateLifecycle)
onDeactivated(deactivateAutoDateLifecycle)

onMounted(() => {
  activateAutoDateLifecycle()
  void loadDeploymentTimezone()
  void loadData()
})
</script>
