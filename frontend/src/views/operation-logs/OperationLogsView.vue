<template>
  <a-card class="page-card responsive-page-card">
    <OperationLogFilterToolbar
      v-model:action-filter="actionFilter"
      :actor-system-account-filter="actorSystemAccountFilter"
      v-model:actor-system-account-selection="actorSystemAccountSelection"
      :affected-system-account-filter="affectedSystemAccountFilter"
      v-model:affected-system-account-selection="affectedSystemAccountSelection"
      v-model:created-at-range="createdAtRange"
      v-model:module-filter="moduleFilter"
      :operation-scope-system-account-filter="operationScopeSystemAccountFilter"
      v-model:operation-scope-system-account-selection="operationScopeSystemAccountSelection"
      v-model:resource-id-filter="resourceIdFilter"
      v-model:resource-type-filter="resourceTypeFilter"
      v-model:summary-keyword="summaryKeywordFilter"
      v-model:trace-id-filter="traceIdFilter"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :actor-system-account-options-loading="actorSystemAccountOptionsLoading"
      :actor-system-accounts="actorSystemAccounts"
      :affected-system-account-options-loading="affectedSystemAccountOptionsLoading"
      :affected-system-accounts="affectedSystemAccounts"
      :is-management-view="isManagementView"
      :loading="loading"
      :operation-scope-system-account-options-loading="operationScopeSystemAccountOptionsLoading"
      :operation-scope-system-accounts="operationScopeSystemAccounts"
      @actor-dropdown-visible-change="handleActorSystemAccountOptionsDropdown"
      @actor-search="handleActorSystemAccountOptionsSearch"
      @affected-dropdown-visible-change="handleAffectedSystemAccountOptionsDropdown"
      @affected-search="handleAffectedSystemAccountOptionsSearch"
      @operation-scope-dropdown-visible-change="handleOperationScopeSystemAccountOptionsDropdown"
      @operation-scope-search="handleOperationScopeSystemAccountOptionsSearch"
      @refresh="refreshRecords"
      @reset="resetFilters"
      @search="applyFilters"
      @update:actor-system-account-filter="updateActorSystemAccountFilter"
      @update:affected-system-account-filter="updateAffectedSystemAccountFilter"
      @update:operation-scope-system-account-filter="updateOperationScopeSystemAccountFilter"
    >
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['summary']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
      </template>
    </OperationLogFilterToolbar>

    <OperationLogList
      :columns="managedColumns"
      :is-management-view="isManagementView"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="tablePagination"
      :records="records"
      @change="handleTableChange"
      @detail="openDetail"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    />

    <OperationLogDetailDrawer
      v-model:open="detailOpen"
      :detail="detail"
      :is-management-view="isManagementView"
      :loading="detailLoading"
    />
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedOperationLogsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { rememberPrincipalSelection } from '@/shared/principalLabelCache'
import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'
import { loadEntityDetailCached } from '@/shared/entityDetailCache'
import type { OperationLogDetail, OperationLogListItem } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import OperationLogDetailDrawer from './OperationLogDetailDrawer.vue'
import OperationLogFilterToolbar from './OperationLogFilterToolbar.vue'
import OperationLogList from './OperationLogList.vue'
import { operationLogFilterCounts, operationLogListParams } from './operationLogFilters'
import {
  applyOperationLogPageState,
  createOperationLogPageStateRefs,
  operationLogFilterValuesFromRefs,
  snapshotOperationLogPageState
} from './operationLogPageStateModel'
import { operationLogTableColumns } from './operationLogTableConfig'
import { useOperationLogSystemAccountOptions } from './useOperationLogSystemAccountOptions'
import {
  defaultOperationLogsPageState,
  operationLogPageStateForTrace,
  type OperationLogsPageState
} from './operationLogPageState'

const pageSize = 20
const { isManagementView } = useScopedMenuView()
const defaultPageState = () => defaultOperationLogsPageState(pageSize, isManagementView.value)
const pageStateCache = usePageStateCache<OperationLogsPageState>(undefined, defaultPageState, { version: 5 })
const initialPageState = pageStateCache.read()
const operationLogsApi = useScopedOperationLogsApi(isManagementView)
const route = useRoute()
const router = useRouter()
const initialTraceId = routeTraceId()
const effectiveInitialPageState: OperationLogsPageState = initialTraceId
  ? operationLogPageStateForTrace(pageSize, isManagementView.value, initialTraceId)
  : initialPageState

const detailLoading = ref(false)
const detail = ref<OperationLogDetail>()
const detailOpen = ref(false)
let detailRequestId = 0
let skipNextRouteTraceRestore = false
const pageStateRefs = createOperationLogPageStateRefs(effectiveInitialPageState)
const {
  actionFilter,
  actorSystemAccountFilter,
  actorSystemAccountSelection,
  affectedSystemAccountFilter,
  affectedSystemAccountSelection,
  createdAtRange,
  resourceIdFilter,
  resourceTypeFilter,
  summaryKeywordFilter,
  moduleFilter,
  operationScopeSystemAccountFilter,
  operationScopeSystemAccountSelection,
  traceIdFilter
} = pageStateRefs
const operationLogSystemAccountOptions = useOperationLogSystemAccountOptions({
  actorSystemAccountFilter,
  affectedSystemAccountFilter,
  isManagementView,
  operationScopeSystemAccountFilter
})
const {
  actor: {
    handleDropdown: handleActorSystemAccountOptionsDropdown,
    handleSearch: handleActorSystemAccountOptionsSearch,
    loading: actorSystemAccountOptionsLoading,
    resetSearch: resetActorSystemAccountOptionsSearch,
    systemAccounts: actorSystemAccounts
  },
  affected: {
    handleDropdown: handleAffectedSystemAccountOptionsDropdown,
    handleSearch: handleAffectedSystemAccountOptionsSearch,
    loading: affectedSystemAccountOptionsLoading,
    resetSearch: resetAffectedSystemAccountOptionsSearch,
    systemAccounts: affectedSystemAccounts
  },
  operationScope: {
    handleDropdown: handleOperationScopeSystemAccountOptionsDropdown,
    handleSearch: handleOperationScopeSystemAccountOptionsSearch,
    loading: operationScopeSystemAccountOptionsLoading,
    resetSearch: resetOperationScopeSystemAccountOptionsSearch,
    systemAccounts: operationScopeSystemAccounts
  },
  resetAllSearches: resetSystemAccountOptionSearches
} = operationLogSystemAccountOptions
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
} = useResponsivePagedList<OperationLogListItem, { forceOptions?: boolean }>({
  pageSize,
  initialPagination: effectiveInitialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条操作日志，还有更多`
    : `共 ${total} 条操作日志`,
  fetchPage: async (options, pageState) => {
    if (options.forceOptions === true) {
      resetSystemAccountOptionSearches()
    }
    return fetchRecords(pageState)
  },
  requestSignature: (_options, pageState) => [
    isManagementView.value ? 'management' : 'self',
    operationLogRequestParams(pageState)
  ],
  onError: (error) => {
    console.error(error)
    message.error('加载操作日志失败')
  }
})

const currentFilterValues = computed(() => operationLogFilterValuesFromRefs(pageStateRefs))
const filterCounts = computed(() => operationLogFilterCounts(currentFilterValues.value, isManagementView.value))
const activeFilterCount = computed(() => filterCounts.value.active)
const advancedFilterCount = computed(() => filterCounts.value.advanced)
const rawColumns = computed(() => operationLogTableColumns(isManagementView.value))
const columnStorageKey = computed(() => (isManagementView.value ? 'operation-logs:management' : 'operation-logs:self'))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['summary'],
  minVisible: 1
})
function applyFilters(): void {
  clearRouteTraceIdForManualState()
  resetPagination()
  void loadData()
}

function applyPageState(state: OperationLogsPageState): void {
  applyOperationLogPageState(pageStateRefs, pagination, state)
  resetSystemAccountOptionSearches()
}

function applyRouteTraceId(traceId: string): void {
  pageStateCache.flushPendingWrite()
  applyPageState(operationLogPageStateForTrace(pageSize, isManagementView.value, traceId))
  resetPagination()
  void loadData()
}

function restorePageStateAfterRouteTraceCleared(): void {
  applyPageState(pageStateCache.read())
  void loadData({ forceOptions: true })
}

function refreshRecords(): void {
  resetPagination()
  void loadData({ forceOptions: true })
}

function resetFilters(): void {
  clearRouteTraceIdForManualState()
  const defaults = defaultPageState()
  applyPageState(defaults)
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

function updateActorSystemAccountFilter(value: string): void {
  actorSystemAccountFilter.value = value
  if (value === allSystemAccountsValue) {
    actorSystemAccountSelection.value = undefined
  }
  resetActorSystemAccountOptionsSearch()
}

function updateAffectedSystemAccountFilter(value: string): void {
  affectedSystemAccountFilter.value = value
  if (value === allSystemAccountsValue) {
    affectedSystemAccountSelection.value = undefined
  }
  resetAffectedSystemAccountOptionsSearch()
}

function updateOperationScopeSystemAccountFilter(value: string): void {
  operationScopeSystemAccountFilter.value = value
  if (value === allSystemAccountsValue) {
    operationScopeSystemAccountSelection.value = undefined
  }
  resetOperationScopeSystemAccountOptionsSearch()
}

async function fetchRecords(pageState: { current: number; pageSize: number }) {
  return operationLogsApi.list(operationLogRequestParams(pageState))
}

function operationLogRequestParams(pageState: { current: number; pageSize: number }) {
  return operationLogListParams(currentFilterValues.value, pageState, isManagementView.value)
}

async function openDetail(record: OperationLogListItem): Promise<void> {
  const requestId = detailRequestId + 1
  detailRequestId = requestId
  detailOpen.value = true
  detailLoading.value = true
  try {
    const nextDetail = await loadEntityDetailCached({
      id: record.id,
      load: () => operationLogsApi.detail(record.id),
      namespace: 'operation-log-detail',
      scope: isManagementView.value ? 'management' : 'self'
    })
    if (requestId === detailRequestId) {
      detail.value = nextDetail
    }
  } catch (error) {
    console.error(error)
    message.error('加载操作日志详情失败')
  } finally {
    if (requestId === detailRequestId) {
      detailLoading.value = false
    }
  }
}

function closeTransientDetails(): void {
  detailRequestId += 1
  detailOpen.value = false
  detailLoading.value = false
  detail.value = undefined
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

function snapshotPageState(): OperationLogsPageState {
  return snapshotOperationLogPageState(pageStateRefs, pagination)
}

watch(snapshotPageState, () => {
  if (routeTraceId()) {
    pageStateCache.cancelPendingWrite()
    return
  }
  pageStateCache.scheduleWrite(snapshotPageState)
}, { deep: true })
watch(actorSystemAccountSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(affectedSystemAccountSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
watch(operationScopeSystemAccountSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })
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
.module-filter {
  width: 132px;
}

.action-filter {
  width: 126px;
}

.created-at-range {
  width: 360px;
}

.trace-filter {
  width: 190px;
}

.account-filter {
  width: 220px;
}

</style>
