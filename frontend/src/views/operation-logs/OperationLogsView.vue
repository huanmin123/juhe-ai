<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="summaryKeywordFilter"
      search-placeholder="搜索操作摘要"
      filter-title="操作日志筛选"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="advancedFilterCount"
      :refresh-loading="loading"
      @refresh="refreshRecords"
      @reset="resetFilters"
      @search="applyFilters"
    >
      <template #advanced-filters>
        <a-form layout="vertical" class="advanced-filter-form">
          <a-form-item label="模块">
            <a-select v-model:value="moduleFilter" :options="moduleOptions" @change="applyFilters" />
          </a-form-item>
          <a-form-item label="动作">
            <a-select v-model:value="actionFilter" :options="actionOptions" @change="applyFilters" />
          </a-form-item>
          <a-form-item label="资源类型">
            <a-select v-model:value="resourceTypeFilter" :options="resourceTypeOptions" @change="applyFilters" />
          </a-form-item>
          <a-form-item label="资源 ID">
            <a-input v-model:value="resourceIdFilter" allow-clear placeholder="输入资源 ID 精确筛选" @press-enter="applyFilters" />
          </a-form-item>
          <a-form-item label="时间范围">
            <a-range-picker
              v-model:value="createdAtRange"
              allow-clear
              class="drawer-range-picker"
              format="YYYY-MM-DD HH:mm"
              show-time
              :placeholder="['开始时间', '结束时间']"
              @change="handleCreatedAtRangeChange"
            />
          </a-form-item>
          <a-form-item label="traceId">
            <a-input v-model:value="traceIdFilter" allow-clear placeholder="输入 traceId" @press-enter="applyFilters" />
          </a-form-item>
          <template v-if="isManagementView">
            <a-form-item label="用户操作人">
              <SystemPrincipalSelect
                v-model:value="actorSystemAccountFilter"
                v-model:selected-principal="actorSystemAccountSelection"
                :accounts="actorSystemAccounts"
                :active-only="false"
                :filter-option="false"
                :loading="actorSystemAccountOptionsLoading"
                include-all
                all-label="全部操作人"
                placeholder="筛选用户操作人"
                @change="handleActorSystemAccountChange"
                @dropdown-visible-change="handleActorSystemAccountOptionsDropdown"
                @search="handleActorSystemAccountOptionsSearch"
              />
            </a-form-item>
            <a-form-item label="影响用户">
              <SystemPrincipalSelect
                v-model:value="affectedSystemAccountFilter"
                v-model:selected-principal="affectedSystemAccountSelection"
                :accounts="affectedSystemAccounts"
                :active-only="false"
                :filter-option="false"
                :loading="affectedSystemAccountOptionsLoading"
                include-all
                all-label="全部用户"
                placeholder="筛选影响用户"
                @change="handleAffectedSystemAccountChange"
                @dropdown-visible-change="handleAffectedSystemAccountOptionsDropdown"
                @search="handleAffectedSystemAccountOptionsSearch"
              />
            </a-form-item>
            <a-form-item label="业务归属">
              <SystemPrincipalSelect
                v-model:value="operationScopeSystemAccountFilter"
                v-model:selected-principal="operationScopeSystemAccountSelection"
                :accounts="operationScopeSystemAccounts"
                :active-only="false"
                :filter-option="false"
                :loading="operationScopeSystemAccountOptionsLoading"
                include-all
                all-label="全部用户"
                placeholder="筛选业务归属"
                @change="handleOperationScopeSystemAccountChange"
                @dropdown-visible-change="handleOperationScopeSystemAccountOptionsDropdown"
                @search="handleOperationScopeSystemAccountOptionsSearch"
              />
            </a-form-item>
          </template>
        </a-form>
      </template>
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['summary']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="模块">
            <a-select v-model:value="moduleFilter" :options="moduleOptions" />
          </a-form-item>
          <a-form-item label="动作">
            <a-select v-model:value="actionFilter" :options="actionOptions" />
          </a-form-item>
          <a-form-item label="资源类型">
            <a-select v-model:value="resourceTypeFilter" :options="resourceTypeOptions" />
          </a-form-item>
          <a-form-item label="资源 ID">
            <a-input v-model:value="resourceIdFilter" allow-clear placeholder="输入资源 ID 精确筛选" />
          </a-form-item>
          <a-form-item label="时间范围">
            <a-range-picker
              v-model:value="createdAtRange"
              allow-clear
              class="drawer-range-picker"
              format="YYYY-MM-DD HH:mm"
              show-time
              :placeholder="['开始时间', '结束时间']"
              @change="handleCreatedAtRangeChange"
            />
          </a-form-item>
          <a-form-item label="traceId">
            <a-input v-model:value="traceIdFilter" allow-clear placeholder="输入 traceId" />
          </a-form-item>
          <template v-if="isManagementView">
            <a-form-item label="用户操作人">
              <SystemPrincipalSelect
                v-model:value="actorSystemAccountFilter"
                v-model:selected-principal="actorSystemAccountSelection"
                :accounts="actorSystemAccounts"
                :active-only="false"
                :filter-option="false"
                :loading="actorSystemAccountOptionsLoading"
                include-all
                all-label="全部操作人"
                placeholder="筛选用户操作人"
                @change="handleActorSystemAccountChange"
                @dropdown-visible-change="handleActorSystemAccountOptionsDropdown"
                @search="handleActorSystemAccountOptionsSearch"
              />
            </a-form-item>
            <a-form-item label="影响用户">
              <SystemPrincipalSelect
                v-model:value="affectedSystemAccountFilter"
                v-model:selected-principal="affectedSystemAccountSelection"
                :accounts="affectedSystemAccounts"
                :active-only="false"
                :filter-option="false"
                :loading="affectedSystemAccountOptionsLoading"
                include-all
                all-label="全部用户"
                placeholder="筛选影响用户"
                @change="handleAffectedSystemAccountChange"
                @dropdown-visible-change="handleAffectedSystemAccountOptionsDropdown"
                @search="handleAffectedSystemAccountOptionsSearch"
              />
            </a-form-item>
            <a-form-item label="业务归属">
              <SystemPrincipalSelect
                v-model:value="operationScopeSystemAccountFilter"
                v-model:selected-principal="operationScopeSystemAccountSelection"
                :accounts="operationScopeSystemAccounts"
                :active-only="false"
                :filter-option="false"
                :loading="operationScopeSystemAccountOptionsLoading"
                include-all
                all-label="全部用户"
                placeholder="筛选业务归属"
                @change="handleOperationScopeSystemAccountChange"
                @dropdown-visible-change="handleOperationScopeSystemAccountOptionsDropdown"
                @search="handleOperationScopeSystemAccountOptionsSearch"
              />
            </a-form-item>
          </template>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table operation-log-table"
      :columns="managedColumns"
      :data-source="records"
      :mobile-data-source="records"
      row-key="id"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="tablePagination"
      :scroll-x="isManagementView ? 1280 : 1060"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前条件下没有操作日志。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'summary'">
          <div class="summary-cell">
            <span>{{ record.summary }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'module'">
          <a-tag>{{ moduleText(record.module) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'actor'">
          <span class="name-cell">{{ actorText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'scope'">
          <span :class="record.operationScopeSystemAccountName ? 'name-cell' : 'muted-cell'">
            {{ displayName(record.operationScopeSystemAccountName) }}
          </span>
        </template>
        <template v-else-if="column.key === 'traceId'">
          <span :class="record.traceId ? 'mono-cell' : 'muted-cell'">{{ record.traceId ?? '-' }}</span>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="detailActions" @action-click="openDetail(record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="operation-log-mobile-card">
          <div class="mobile-card-head">
            <span>{{ moduleText(record.module) }}</span>
            <a-tag :color="actionColor(record.action)">{{ actionText(record.action) }}</a-tag>
          </div>
          <div class="mobile-card-meta">
            <span>{{ actorText(record) }}</span>
            <span>{{ formatDateTime(record.createdAt) }}</span>
          </div>
          <div class="mobile-card-summary">{{ record.summary }}</div>
          <div class="mobile-card-actions">
            <RowActions variant="button" :actions="detailActions" @action-click="openDetail(record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-drawer v-model:open="detailOpen" width="min(920px, 96vw)" title="操作日志详情" :body-style="{ padding: '18px' }">
      <a-spin :spinning="detailLoading">
        <template v-if="detail">
          <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
            <a-descriptions-item label="时间">{{ formatDateTime(detail.createdAt) }}</a-descriptions-item>
            <a-descriptions-item label="动作">{{ moduleText(detail.module) }} / {{ actionText(detail.action) }}</a-descriptions-item>
            <a-descriptions-item label="操作标识">{{ detail.operationKey }}</a-descriptions-item>
            <a-descriptions-item label="操作人">{{ actorText(detail) }}</a-descriptions-item>
            <a-descriptions-item label="业务归属">{{ displayName(detail.operationScopeSystemAccountName, detail.operationScopeSystemAccountId) }}</a-descriptions-item>
            <a-descriptions-item label="资源">{{ resourceText(detail) }}</a-descriptions-item>
            <a-descriptions-item label="可见范围" :span="detail.method || detail.path || detail.clientIp ? 1 : 2">{{ visibilityText(detail.visibilityScope) }}</a-descriptions-item>
            <a-descriptions-item v-if="detail.method || detail.path" label="请求">{{ requestText(detail) }}</a-descriptions-item>
            <a-descriptions-item v-if="detail.clientIp" label="客户端 IP" :span="detail.method || detail.path ? 2 : 1">{{ detail.clientIp }}</a-descriptions-item>
            <a-descriptions-item label="traceId" :span="2">{{ detail.traceId ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="摘要" :span="2">{{ detail.summary }}</a-descriptions-item>
          </a-descriptions>

          <a-tabs>
            <a-tab-pane key="changes" tab="变更内容">
              <ResponsiveDataList size="small" :pagination="false" :columns="changeColumns" :data-source="detail.changes" row-key="field" :table-scroll-enabled="false" :lock-body-scroll="false">
                <template #emptyText>
                  <a-empty description="没有字段级变更摘要。" />
                </template>
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'field'">
                    <span class="mono-cell">{{ record.field }}</span>
                  </template>
                  <template v-else-if="column.key === 'before'">
                    <span :class="record.sensitive ? 'muted-cell' : ''">{{ valueText(record.before) }}</span>
                  </template>
                  <template v-else-if="column.key === 'after'">
                    <span :class="record.sensitive ? 'muted-cell' : ''">{{ valueText(record.after) }}</span>
                  </template>
                </template>
                <template #card="{ record }">
                  <article class="detail-table-card">
                    <strong class="mono-cell">{{ record.field }}</strong>
                    <span>名称：{{ record.label }}</span>
                    <span>变更前：{{ valueText(record.before) }}</span>
                    <span>变更后：{{ valueText(record.after) }}</span>
                  </article>
                </template>
              </ResponsiveDataList>
            </a-tab-pane>
            <a-tab-pane key="targets" tab="影响对象">
              <ResponsiveDataList size="small" :pagination="false" :columns="targetColumns" :data-source="detail.targets" row-key="id" :table-scroll-enabled="false" :lock-body-scroll="false">
                <template #emptyText>
                  <a-empty description="没有额外影响对象。" />
                </template>
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'target'">{{ displayName(record.targetName, record.targetId) }}</template>
                  <template v-else-if="column.key === 'type'"><a-tag>{{ resourceTypeText(record.targetType) }}</a-tag></template>
                  <template v-else-if="column.key === 'owner'">{{ displayName(record.targetOwnerSystemAccountName, record.targetOwnerSystemAccountId) }}</template>
                  <template v-else-if="column.key === 'relation'">{{ relationText(record.relation) }}</template>
                </template>
                <template #card="{ record }">
                  <article class="detail-table-card">
                    <strong>{{ displayName(record.targetName, record.targetId) }}</strong>
                    <span>类型：{{ resourceTypeText(record.targetType) }}</span>
                    <span>归属用户：{{ displayName(record.targetOwnerSystemAccountName, record.targetOwnerSystemAccountId) }}</span>
                    <span>关系：{{ relationText(record.relation) }}</span>
                  </article>
                </template>
              </ResponsiveDataList>
            </a-tab-pane>
            <a-tab-pane v-if="isManagementView" key="viewers" tab="可见用户">
              <ResponsiveDataList size="small" :pagination="false" :columns="viewerColumns" :data-source="detail.viewers" row-key="systemAccountId" :table-scroll-enabled="false" :lock-body-scroll="false">
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'user'">{{ displayName(record.systemAccountName, record.systemAccountId) }}</template>
                  <template v-else-if="column.key === 'reason'">{{ visibilityReasonText(record.visibilityReason) }}</template>
                  <template v-else-if="column.key === 'level'">{{ record.detailLevel === 'summary' ? '摘要' : '完整' }}</template>
                </template>
                <template #card="{ record }">
                  <article class="detail-table-card">
                    <strong>{{ displayName(record.systemAccountName, record.systemAccountId) }}</strong>
                    <span>可见原因：{{ visibilityReasonText(record.visibilityReason) }}</span>
                    <span>详情级别：{{ record.detailLevel === 'summary' ? '摘要' : '完整' }}</span>
                  </article>
                </template>
              </ResponsiveDataList>
            </a-tab-pane>
          </a-tabs>
        </template>
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import dayjs, { type Dayjs } from 'dayjs'
import { computed, onDeactivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import type { OperationLogListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedOperationLogsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { formatDateTime } from '@/shared/formatters'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'
import type { OperationLogChange, OperationLogDetail, OperationLogSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import { actorText, displayName, requestText, resourceText, valueText } from './operationLogDisplay'
import { actionColor, actionText, moduleText, relationText, resourceTypeText, visibilityReasonText, visibilityText } from './operationLogLabels'
import { actionOptions, changeColumns, detailActions, moduleOptions, resourceTypeOptions, targetColumns, viewerColumns } from './operationLogOptions'

type OperationLogsPageState = {
  actionFilter: string
  actorSystemAccountFilter: string
  actorSystemAccountSelection?: PrincipalSelection
  affectedSystemAccountFilter: string
  affectedSystemAccountSelection?: PrincipalSelection
  createdAtRange?: [string, string]
  resourceIdFilter: string
  resourceTypeFilter: string
  summaryKeywordFilter: string
  moduleFilter: string
  operationScopeSystemAccountFilter: string
  operationScopeSystemAccountSelection?: PrincipalSelection
  pagination: { current: number; pageSize: number }
  traceIdFilter: string
}
type CreatedAtRangeValue = [Dayjs | null | undefined, Dayjs | null | undefined] | null | undefined

const pageSize = 20
const defaultOperationLogsPageState = (): OperationLogsPageState => ({
  actionFilter: 'all',
  actorSystemAccountFilter: allSystemAccountsValue,
  actorSystemAccountSelection: undefined,
  affectedSystemAccountFilter: allSystemAccountsValue,
  affectedSystemAccountSelection: undefined,
  createdAtRange: undefined,
  resourceIdFilter: '',
  resourceTypeFilter: 'all',
  summaryKeywordFilter: '',
  moduleFilter: 'all',
  operationScopeSystemAccountFilter: allSystemAccountsValue,
  operationScopeSystemAccountSelection: undefined,
  pagination: { current: 1, pageSize },
  traceIdFilter: ''
})

const pageStateCache = usePageStateCache<OperationLogsPageState>(undefined, defaultOperationLogsPageState, { version: 4 })
const initialPageState = pageStateCache.read()
const { isManagementView } = useScopedMenuView()
const operationLogsApi = useScopedOperationLogsApi(isManagementView)
const route = useRoute()
const router = useRouter()
const initialTraceId = routeTraceId()
const effectiveInitialPageState: OperationLogsPageState = initialTraceId
  ? { ...defaultOperationLogsPageState(), traceIdFilter: initialTraceId }
  : initialPageState

const detailLoading = ref(false)
const detail = ref<OperationLogDetail>()
const detailOpen = ref(false)
let detailRequestId = 0
let skipNextRouteTraceRestore = false
const summaryKeywordFilter = ref(effectiveInitialPageState.summaryKeywordFilter)
const moduleFilter = ref(effectiveInitialPageState.moduleFilter)
const actionFilter = ref(effectiveInitialPageState.actionFilter)
const resourceTypeFilter = ref(effectiveInitialPageState.resourceTypeFilter)
const resourceIdFilter = ref(effectiveInitialPageState.resourceIdFilter)
const createdAtRange = ref<CreatedAtRangeValue>(parseCreatedAtRange(effectiveInitialPageState.createdAtRange))
const traceIdFilter = ref(effectiveInitialPageState.traceIdFilter)
const actorSystemAccountFilter = ref(effectiveInitialPageState.actorSystemAccountFilter)
const actorSystemAccountSelection = ref<PrincipalSelection | undefined>(effectiveInitialPageState.actorSystemAccountSelection)
const affectedSystemAccountFilter = ref(effectiveInitialPageState.affectedSystemAccountFilter)
const affectedSystemAccountSelection = ref<PrincipalSelection | undefined>(effectiveInitialPageState.affectedSystemAccountSelection)
const operationScopeSystemAccountFilter = ref(effectiveInitialPageState.operationScopeSystemAccountFilter)
const operationScopeSystemAccountSelection = ref<PrincipalSelection | undefined>(effectiveInitialPageState.operationScopeSystemAccountSelection)
const {
  handleDropdown: handleActorSystemAccountOptionsDropdown,
  handleSearch: handleActorSystemAccountOptionsSearch,
  loading: actorSystemAccountOptionsLoading,
  resetSearch: resetActorSystemAccountOptionsSearch,
  systemAccounts: actorSystemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [actorSystemAccountFilter.value]
})
const {
  handleDropdown: handleAffectedSystemAccountOptionsDropdown,
  handleSearch: handleAffectedSystemAccountOptionsSearch,
  loading: affectedSystemAccountOptionsLoading,
  resetSearch: resetAffectedSystemAccountOptionsSearch,
  systemAccounts: affectedSystemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [affectedSystemAccountFilter.value]
})
const {
  handleDropdown: handleOperationScopeSystemAccountOptionsDropdown,
  handleSearch: handleOperationScopeSystemAccountOptionsSearch,
  loading: operationScopeSystemAccountOptionsLoading,
  resetSearch: resetOperationScopeSystemAccountOptionsSearch,
  systemAccounts: operationScopeSystemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [operationScopeSystemAccountFilter.value]
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
} = useResponsivePagedList<OperationLogSummary, { forceOptions?: boolean }>({
  pageSize,
  initialPagination: effectiveInitialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 条操作日志，还有更多`
    : `共 ${total} 条操作日志`,
  fetchPage: async (options, pageState) => {
    if (options.forceOptions === true) {
      resetActorSystemAccountOptionsSearch()
      resetAffectedSystemAccountOptionsSearch()
      resetOperationScopeSystemAccountOptionsSearch()
    }
    return fetchRecords(pageState)
  },
  onError: (error) => {
    console.error(error)
    message.error('加载操作日志失败')
  }
})

const activeFilterCount = computed(() => {
  let count = 0
  if (summaryKeywordFilter.value.trim()) count += 1
  if (moduleFilter.value !== 'all') count += 1
  if (actionFilter.value !== 'all') count += 1
  if (resourceTypeFilter.value !== 'all') count += 1
  if (resourceIdFilter.value.trim()) count += 1
  if (normalizeCreatedAtRange(createdAtRange.value)) count += 1
  if (traceIdFilter.value.trim()) count += 1
  if (isManagementView.value && actorSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (isManagementView.value && affectedSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (isManagementView.value && operationScopeSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  return count
})
const advancedFilterCount = computed(() => {
  let count = 0
  if (moduleFilter.value !== 'all') count += 1
  if (actionFilter.value !== 'all') count += 1
  if (resourceTypeFilter.value !== 'all') count += 1
  if (resourceIdFilter.value.trim()) count += 1
  if (normalizeCreatedAtRange(createdAtRange.value)) count += 1
  if (traceIdFilter.value.trim()) count += 1
  if (isManagementView.value && actorSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (isManagementView.value && affectedSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  if (isManagementView.value && operationScopeSystemAccountFilter.value !== allSystemAccountsValue) count += 1
  return count
})
const rawColumns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '模块', key: 'module', width: 120 },
    { title: '动作', key: 'action', width: 110 },
    { title: '操作人', key: 'actor', width: 170 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '业务归属', key: 'scope', width: 170 })
  }
  baseColumns.push(
    { title: '摘要', key: 'summary', width: 300, responsiveFlex: true },
    { title: 'traceId', key: 'traceId', width: 190 },
    { title: '时间', key: 'createdAt', width: 180 },
    { title: '操作', key: 'actions', fixed: 'right' }
  )
  return baseColumns
})
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
  summaryKeywordFilter.value = state.summaryKeywordFilter
  moduleFilter.value = state.moduleFilter
  actionFilter.value = state.actionFilter
  resourceTypeFilter.value = state.resourceTypeFilter
  resourceIdFilter.value = state.resourceIdFilter
  createdAtRange.value = parseCreatedAtRange(state.createdAtRange)
  traceIdFilter.value = state.traceIdFilter
  actorSystemAccountFilter.value = state.actorSystemAccountFilter
  actorSystemAccountSelection.value = state.actorSystemAccountSelection
  affectedSystemAccountFilter.value = state.affectedSystemAccountFilter
  affectedSystemAccountSelection.value = state.affectedSystemAccountSelection
  operationScopeSystemAccountFilter.value = state.operationScopeSystemAccountFilter
  operationScopeSystemAccountSelection.value = state.operationScopeSystemAccountSelection
  pagination.current = state.pagination.current
  pagination.pageSize = state.pagination.pageSize
  resetActorSystemAccountOptionsSearch()
  resetAffectedSystemAccountOptionsSearch()
  resetOperationScopeSystemAccountOptionsSearch()
}

function applyRouteTraceId(traceId: string): void {
  pageStateCache.flushPendingWrite()
  applyPageState({ ...defaultOperationLogsPageState(), traceIdFilter: traceId })
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
  const defaults = defaultOperationLogsPageState()
  summaryKeywordFilter.value = defaults.summaryKeywordFilter
  moduleFilter.value = defaults.moduleFilter
  actionFilter.value = defaults.actionFilter
  resourceTypeFilter.value = defaults.resourceTypeFilter
  resourceIdFilter.value = defaults.resourceIdFilter
  createdAtRange.value = parseCreatedAtRange(defaults.createdAtRange)
  traceIdFilter.value = defaults.traceIdFilter
  actorSystemAccountFilter.value = defaults.actorSystemAccountFilter
  actorSystemAccountSelection.value = defaults.actorSystemAccountSelection
  affectedSystemAccountFilter.value = defaults.affectedSystemAccountFilter
  affectedSystemAccountSelection.value = defaults.affectedSystemAccountSelection
  operationScopeSystemAccountFilter.value = defaults.operationScopeSystemAccountFilter
  operationScopeSystemAccountSelection.value = defaults.operationScopeSystemAccountSelection
  resetActorSystemAccountOptionsSearch()
  resetAffectedSystemAccountOptionsSearch()
  resetOperationScopeSystemAccountOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData()
}

function handleActorSystemAccountChange(): void {
  if (actorSystemAccountFilter.value === allSystemAccountsValue) {
    actorSystemAccountSelection.value = undefined
  }
  resetActorSystemAccountOptionsSearch()
  applyFilters()
}

function handleAffectedSystemAccountChange(): void {
  if (affectedSystemAccountFilter.value === allSystemAccountsValue) {
    affectedSystemAccountSelection.value = undefined
  }
  resetAffectedSystemAccountOptionsSearch()
  applyFilters()
}

function handleOperationScopeSystemAccountChange(): void {
  if (operationScopeSystemAccountFilter.value === allSystemAccountsValue) {
    operationScopeSystemAccountSelection.value = undefined
  }
  resetOperationScopeSystemAccountOptionsSearch()
  applyFilters()
}

function handleCreatedAtRangeChange(): void {
  createdAtRange.value = normalizeCreatedAtRange(createdAtRange.value)
  applyFilters()
}

async function fetchRecords(pageState: { current: number; pageSize: number }) {
  const range = normalizeCreatedAtRange(createdAtRange.value)
  const params: OperationLogListParams = {
    page: pageState.current,
    pageSize: pageState.pageSize,
    summaryKeyword: summaryKeywordFilter.value.trim() || undefined,
    module: moduleFilter.value === 'all' ? undefined : moduleFilter.value,
    action: actionFilter.value === 'all' ? undefined : actionFilter.value,
    resourceType: resourceTypeFilter.value === 'all' ? undefined : resourceTypeFilter.value,
    resourceId: resourceIdFilter.value.trim() || undefined,
    startAt: range?.[0].toISOString(),
    endAt: range?.[1].toISOString(),
    traceId: traceIdFilter.value.trim() || undefined,
    actorSystemAccountId: adminAccountFilter(actorSystemAccountFilter.value),
    affectedSystemAccountId: adminAccountFilter(affectedSystemAccountFilter.value),
    operationScopeSystemAccountId: adminAccountFilter(operationScopeSystemAccountFilter.value)
  }
  return operationLogsApi.list(params)
}

async function openDetail(record: OperationLogSummary): Promise<void> {
  const requestId = detailRequestId + 1
  detailRequestId = requestId
  detailOpen.value = true
  detailLoading.value = true
  try {
    const nextDetail = await operationLogsApi.detail(record.id)
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

function adminAccountFilter(value: string): string | undefined {
  return isManagementView.value && value !== allSystemAccountsValue ? value : undefined
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
  const range = normalizeCreatedAtRange(createdAtRange.value)
  return {
    actionFilter: actionFilter.value,
    actorSystemAccountFilter: actorSystemAccountFilter.value,
    actorSystemAccountSelection: actorSystemAccountSelection.value,
    affectedSystemAccountFilter: affectedSystemAccountFilter.value,
    affectedSystemAccountSelection: affectedSystemAccountSelection.value,
    createdAtRange: range ? [range[0].toISOString(), range[1].toISOString()] : undefined,
    resourceIdFilter: resourceIdFilter.value,
    resourceTypeFilter: resourceTypeFilter.value,
    summaryKeywordFilter: summaryKeywordFilter.value,
    moduleFilter: moduleFilter.value,
    operationScopeSystemAccountFilter: operationScopeSystemAccountFilter.value,
    operationScopeSystemAccountSelection: operationScopeSystemAccountSelection.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    traceIdFilter: traceIdFilter.value
  }
}

function parseCreatedAtRange(value?: [string, string]): [Dayjs, Dayjs] | undefined {
  if (!value) return undefined
  const start = dayjs(value[0])
  const end = dayjs(value[1])
  return normalizeCreatedAtRange(start.isValid() && end.isValid() ? [start, end] : undefined)
}

function normalizeCreatedAtRange(value: CreatedAtRangeValue): [Dayjs, Dayjs] | undefined {
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

.drawer-range-picker {
  width: 100%;
}

.advanced-filter-form :deep(.ant-select),
.advanced-filter-form :deep(.ant-input) {
  width: 100%;
}

.operation-log-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.summary-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 3px;
}

.summary-cell span {
  max-width: 280px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}

.muted-cell {
  color: #0f172a;
  font-size: 12px;
}

.name-cell {
  display: inline-block;
  max-width: 190px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.mono-cell {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.detail-table-card {
  display: grid;
  gap: 6px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
  color: #64748b;
  font-size: 12px;
}

.detail-table-card strong {
  color: #0f172a;
  font-size: 13px;
}

.operation-log-mobile-card {
  display: grid;
  width: 100%;
  gap: 10px;
  padding: 12px;
  text-align: left;
  background: #ffffff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(15 23 42 / 4%);
}

.mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}

.mobile-card-head > span {
  min-width: 0;
  color: #0f172a;
  line-height: 1.35;
}

.mobile-card-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 12px;
  color: #0f172a;
  font-size: 12px;
}

.mobile-card-summary {
  color: #0f172a;
  font-size: 13px;
  line-height: 1.4;
}

.mobile-card-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
