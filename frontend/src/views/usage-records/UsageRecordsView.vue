<template>
  <a-card class="page-card responsive-page-card">
    <UsageRecordsFilterToolbar
      v-model:keyword="accountNameFilter"
      v-model:date-range="dateRangeFilter"
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
      @reset="resetFilters"
      @refresh="refreshRecords"
      @search="applyFilters"
      @system-account-change="handleSystemAccountFilterChange"
      @system-account-dropdown="handleSystemAccountOptionsDropdown"
      @system-account-search="handleSystemAccountOptionsSearch"
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
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { api, type UsageRecordListParams } from '@/api/client'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedGroupsApi, useScopedUsageRecordsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatDateKey, normalizeDayjsDateRange, parseDateKey } from '@/shared/dateRange'
import { rememberGroupLabel, rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'
import type { ProviderModelOption, UsageRecordSummary, UsageRecordTrafficSource } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import UsageRecordsFilterToolbar from './UsageRecordsFilterToolbar.vue'
import UsageRecordsTable from './UsageRecordsTable.vue'
import { useUsageRecordGroupOptions } from './useUsageRecordGroupOptions'

type UsageRecordSortField = NonNullable<UsageRecordListParams['sortBy']>
type TableSortOrder = 'ascend' | 'descend' | null
type TraceTarget = 'audit' | 'runtime'
type UsageRecordsPageState = {
  accountNameFilter: string
  clientIpFilter: string
  dateRangeFilter?: [string, string]
  groupFilter?: GroupSelection
  modelFilter: string
  pagination: { current: number; pageSize: number }
  resultFilter: 'all' | 'success' | 'failed'
  sortState: { field: UsageRecordSortField; order: TableSortOrder }
  statusCodeFilter: string
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
  traceIdFilter: string
  trafficSourceFilter: UsageRecordTrafficSource | 'all'
}

const pageSize = 20
const defaultUsageRecordsPageState = (): UsageRecordsPageState => ({
  accountNameFilter: '',
  clientIpFilter: '',
  dateRangeFilter: undefined,
  groupFilter: undefined,
  modelFilter: '',
  pagination: { current: 1, pageSize },
  resultFilter: 'all',
  sortState: { field: 'createdAt', order: 'descend' },
  statusCodeFilter: '',
  systemAccountFilter: allSystemAccountsValue,
  systemAccountFilterSelection: undefined,
  traceIdFilter: '',
  trafficSourceFilter: 'all'
})
const pageStateCache = usePageStateCache<UsageRecordsPageState>(undefined, defaultUsageRecordsPageState, { version: 8 })
const route = useRoute()
const initialRouteTraceId = routeTraceId()
const cachedInitialPageState = pageStateCache.read()
const initialPageState = initialRouteTraceId
  ? { ...defaultUsageRecordsPageState(), traceIdFilter: initialRouteTraceId }
  : cachedInitialPageState

const accountNameFilter = ref(initialPageState.accountNameFilter)
const clientIpFilter = ref(initialPageState.clientIpFilter ?? '')
const dateRangeFilter = ref<[Dayjs, Dayjs] | undefined>(parseDateRange(initialPageState.dateRangeFilter))
const groupFilterSelection = ref<GroupSelection | undefined>(initialPageState.groupFilter)
const modelFilter = ref(initialPageState.modelFilter ?? '')
const resultFilter = ref<'all' | 'success' | 'failed'>(initialPageState.resultFilter)
const statusCodeFilter = ref<string>(initialPageState.statusCodeFilter)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const traceIdFilter = ref(initialPageState.traceIdFilter ?? '')
const trafficSourceFilter = ref<UsageRecordTrafficSource | 'all'>(initialPageState.trafficSourceFilter)
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const usageRecordsApi = useScopedUsageRecordsApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const router = useRouter()
const sortState = ref<{ field: UsageRecordSortField; order: TableSortOrder }>(initialPageState.sortState)
const groupFilter = computed({
  get: () => groupFilterSelection.value?.id,
  set: (id: string | undefined) => {
    groupFilterSelection.value = selectedGroupSelection(id)
  }
})
const groupFilterDisabled = computed(() => false)
const modelOptions = ref<ProviderModelOption[]>([])
const modelOptionsLoading = ref(false)
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
let modelOptionsLoaded = false
let modelOptionsLoadingPromise: Promise<void> | undefined
let skipNextRouteTraceRestore = false
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
  refreshMobile: refreshMobileRecords,
  resetPagination
} = useResponsivePagedList<UsageRecordSummary, { forceOptions?: boolean }>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条使用记录，还有更多`
    : `共 ${total} 条使用记录`,
  fetchPage: async (options, pageState) => {
    if (options.forceOptions === true) {
      resetSystemAccountOptionsSearch()
      resetGroupOptionsSearch()
    }
    const [result] = await Promise.all([
      fetchRecords(pageState),
      loadModelOptions(options.forceOptions === true)
    ])
    return result
  },
  onError: (error) => {
    console.error(error)
    message.error(extractApiErrorMessage(error, '加载使用记录失败'))
  }
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
  { label: '恢复探活', value: 'cooldown_retest' }
] satisfies Array<{ label: string; value: UsageRecordTrafficSource | 'all' }>

const activeFilterCount = computed(() => {
  let count = 0
  if (accountNameFilter.value.trim()) count += 1
  if (clientIpFilter.value.trim()) count += 1
  if (dateRangeFilter.value) count += 1
  if (groupFilter.value) count += 1
  if (modelFilter.value.trim()) count += 1
  if (resultFilter.value !== 'all') count += 1
  if (statusCodeFilter.value) count += 1
  if (systemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (traceIdFilter.value.trim()) count += 1
  if (trafficSourceFilter.value !== 'all') count += 1
  return count
})
const advancedFilterCount = computed(() => {
  let count = 0
  if (dateRangeFilter.value) count += 1
  if (resultFilter.value !== 'all') count += 1
  if (systemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (groupFilter.value) count += 1
  if (clientIpFilter.value.trim()) count += 1
  if (modelFilter.value.trim()) count += 1
  if (statusCodeFilter.value) count += 1
  if (traceIdFilter.value.trim()) count += 1
  if (trafficSourceFilter.value !== 'all') count += 1
  return count
})

const filteredRecords = computed(() => records.value)
const mobileRecords = computed(() => records.value)

const rawColumns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: 'AI账户名称', dataIndex: 'accountName', key: 'account', width: 170, fixed: 'left' }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '接口', dataIndex: 'endpoint', key: 'endpoint', width: 150 },
    { title: '模型', dataIndex: 'model', key: 'model', width: 170 },
    { title: '类型', key: 'stream', width: 90 },
    { title: '状态', key: 'success', width: 90 },
    { title: '状态码', dataIndex: 'statusCode', key: 'statusCode', width: 110 },
    { title: '请求来源', key: 'trafficSource', width: 110 },
    { title: 'Token 用量', key: 'tokens', width: 150 },
    { title: '成本', key: 'cost', width: 110, sorter: true, sortOrder: columnSortOrder('costUsd') },
    { title: '首 token', dataIndex: 'firstTokenMs', key: 'firstTokenMs', width: 100, sorter: true, sortOrder: columnSortOrder('firstTokenMs') },
    { title: '总耗时', dataIndex: 'durationMs', key: 'durationMs', width: 100, sorter: true, sortOrder: columnSortOrder('durationMs') },
    { title: '时间', dataIndex: 'createdAt', key: 'createdAt', width: 180, sorter: true, sortOrder: columnSortOrder('createdAt') },
    { title: 'API Key', dataIndex: 'apiKeyName', key: 'apiKey', width: 170 },
    { title: '分组', dataIndex: 'groupName', key: 'group', width: 150 },
    { title: 'IP', dataIndex: 'clientIp', key: 'clientIp', width: 130 },
    { title: 'traceId', dataIndex: 'traceId', key: 'traceId', width: 300 }
  )
  return baseColumns
})
const columnStorageKey = computed(() => (isManagementView.value ? 'usage-records:management' : 'usage-records:self'))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['account'],
  minVisible: 1
})

async function loadModelOptions(force = false): Promise<void> {
  if (!force && (modelOptionsLoaded || modelOptionsLoadingPromise)) {
    return modelOptionsLoadingPromise
  }
  modelOptionsLoading.value = true
  modelOptionsLoadingPromise = (async () => {
    try {
      modelOptions.value = await api.providers.modelOptions()
      modelOptionsLoaded = true
    } catch (error) {
      console.error(error)
      modelOptionsLoaded = true
      message.warning('加载模型筛选选项失败')
    } finally {
      modelOptionsLoading.value = false
      modelOptionsLoadingPromise = undefined
    }
  })()
  return modelOptionsLoadingPromise
}

function resetFilters(): void {
  clearRouteTraceIdForManualState()
  const defaults = defaultUsageRecordsPageState()
  accountNameFilter.value = defaults.accountNameFilter
  clientIpFilter.value = defaults.clientIpFilter
  dateRangeFilter.value = parseDateRange(defaults.dateRangeFilter)
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
  const normalized = normalizeTableSorter(sorter)
  sortState.value = normalized ?? { field: 'createdAt', order: 'descend' }
  await loadData()
}

function columnSortOrder(field: UsageRecordSortField): TableSortOrder {
  return sortState.value.field === field ? sortState.value.order : null
}

function normalizeTableSorter(sorter: unknown): { field: UsageRecordSortField; order: TableSortOrder } | undefined {
  const item = Array.isArray(sorter) ? sorter[0] : sorter
  if (!item || typeof item !== 'object') return undefined
  const record = item as Record<string, unknown>
  const field = sortFieldFromColumn(record.columnKey ?? record.field)
  const order = record.order === 'ascend' || record.order === 'descend' ? record.order : null
  return field && order ? { field, order } : undefined
}

function sortFieldFromColumn(value: unknown): UsageRecordSortField | undefined {
  if (value === 'cost') return 'costUsd'
  if (value === 'costUsd' || value === 'firstTokenMs' || value === 'durationMs' || value === 'createdAt') return value
  return undefined
}

function updatePaginationFromTable(paginationInfo: unknown): void {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
  pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : pageSize
}

function applyFilters(): void {
  clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

function refreshRecords(): void {
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
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
  dateRangeFilter.value = parseDateRange(state.dateRangeFilter)
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
}

function handleGroupFilterChange(): void {
  resetGroupOptionsSearch()
  applyFilters()
}

function handleSystemAccountFilterChange(): void {
  groupFilterSelection.value = undefined
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

async function fetchRecords(pageState: { current: number; pageSize: number }) {
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(systemAccountFilter.value) : undefined
  const sortOrder = sortState.value.order === 'ascend' ? 'asc' : 'desc'
  const dateRange = dateRangeParam(dateRangeFilter.value)
  const params: UsageRecordListParams = {
    page: pageState.current,
    pageSize: pageState.pageSize,
    accountKeyword: accountNameFilter.value.trim() || undefined,
    clientIp: clientIpFilter.value.trim() || undefined,
    startDate: dateRange?.[0],
    endDate: dateRange?.[1],
    groupId: groupFilter.value,
    model: modelFilter.value.trim() || undefined,
    result: resultFilter.value,
    statusCode: normalizedStatusCode(statusCodeFilter.value),
    systemAccountId,
    traceId: traceIdFilter.value.trim() || undefined,
    trafficSource: trafficSourceFilter.value === 'all' ? undefined : trafficSourceFilter.value,
    sortBy: sortState.value.field,
    sortOrder
  }
  return usageRecordsApi.list(params)
}

function normalizedStatusCode(value: string): number | undefined {
  const text = value.trim()
  if (!text) return undefined
  const statusCode = Number(text)
  return Number.isInteger(statusCode) && statusCode >= 100 && statusCode <= 599 ? statusCode : undefined
}

function dateRangeParam(value?: [Dayjs, Dayjs]): [string, string] | undefined {
  const normalized = normalizeDayjsDateRange(value)
  return normalized ? [formatDateKey(normalized[0]), formatDateKey(normalized[1])] : undefined
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
    dateRangeFilter: dateRangeParam(dateRangeFilter.value),
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

function parseDateRange(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = parseDateKey(value[0])
  const end = parseDateKey(value[1])
  return start && end ? normalizeDayjsDateRange([start, end]) : undefined
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

onBeforeUnmount(clearGroupOptionsSearchTimer)

onMounted(loadData)
</script>

