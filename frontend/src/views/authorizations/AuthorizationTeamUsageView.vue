<template>
  <div class="authorization-usage-page">
    <a-card class="page-card authorization-usage-header-card">
      <ResponsiveListToolbar
        :show-search="false"
        filter-title="筛选团队消耗"
        :active-filter-count="activeFilterCount"
        :advanced-filter-count="advancedFilterCount"
        :refresh-loading="loading || summaryLoading"
        @reset="resetFilters"
        @refresh="refreshUsage"
        @search="loadData"
      >
        <template #inline-filters>
          <a-range-picker
            v-model:value="dateRange"
            :allow-clear="false"
            :disabled="loading"
            :disabled-date="disabledDate"
            class="authorization-usage-range responsive-list-inline-filter"
            format="YYYY-MM-DD"
            @calendar-change="handleCalendarChange"
            @change="handleDateRangeChange"
            @open-change="handleDateRangeOpenChange"
          />
          <SystemPrincipalSelect
            v-if="isManagementView"
            v-model:value="filters.resourceOwnerSystemAccountId"
            v-model:selected-principal="filters.resourceOwnerSystemAccount"
            :accounts="resourceOwners"
            :active-only="false"
            :filter-option="false"
            :loading="resourceOwnerOptionsLoading"
            include-all
            all-label="全部资源归属用户"
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选资源归属用户"
            @change="handleResourceOwnerChange"
            @dropdown-visible-change="handleResourceOwnerOptionsDropdown"
            @search="handleResourceOwnerOptionsSearch"
          />
          <SystemPrincipalSelect
            v-model:value="filters.teamId"
            v-model:selected-principal="filters.team"
            :teams="teams"
            :active-only="false"
            :filter-option="false"
            :loading="teamOptionsLoading"
            allow-clear
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选被授权团队"
            scope="team"
            @change="handleTeamChange"
            @dropdown-visible-change="handleTeamOptionsDropdown"
            @search="handleTeamOptionsSearch"
          />
        </template>
        <template #advanced-filters>
          <a-form layout="vertical" class="advanced-filter-form">
            <AuthorizationUsageResourceFilterFields
              v-model:resource-type="filters.resourceType"
              v-model:resource-id="filters.resourceId"
              v-model:resource-account="filters.resourceAccount"
              v-model:resource-group="filters.resourceGroup"
              variant="advanced"
              type-label="资源类型"
              resource-label="资源名称"
              resource-placeholder="筛选资源"
              empty-type-placeholder="先选择资源类型"
              :resource-type-options="resourceTypeOptions"
              :resource-options="resourceOptions"
              :resource-options-loading="resourceOptionsLoading"
              :resource-group-disabled="resourceGroupDisabled"
              @resource-type-change="handleResourceTypeChange"
              @resource-change="handleResourceChange"
              @resource-options-dropdown="handleResourceOptionsDropdown"
              @resource-options-search="handleResourceOptionsSearch"
            />
          </a-form>
        </template>
        <template #filters>
          <label class="mobile-filter-field">
            <span>用量日期</span>
            <a-range-picker
              v-model:value="dateRange"
              :allow-clear="false"
              :disabled="loading"
              :disabled-date="disabledDate"
              format="YYYY-MM-DD"
              @calendar-change="handleCalendarChange"
              @change="handleDateRangeChange"
              @open-change="handleDateRangeOpenChange"
            />
          </label>
          <label class="mobile-filter-field">
            <span>被授权团队</span>
            <SystemPrincipalSelect
              v-model:value="filters.teamId"
              v-model:selected-principal="filters.team"
              :teams="teams"
              :active-only="false"
              :filter-option="false"
              :loading="teamOptionsLoading"
              allow-clear
              scope="team"
              placeholder="筛选被授权团队"
              @change="handleTeamChange"
              @dropdown-visible-change="handleTeamOptionsDropdown"
              @search="handleTeamOptionsSearch"
            />
          </label>
          <AuthorizationUsageResourceFilterFields
            v-model:resource-type="filters.resourceType"
            v-model:resource-id="filters.resourceId"
            v-model:resource-account="filters.resourceAccount"
            v-model:resource-group="filters.resourceGroup"
            variant="mobile"
            type-label="资源类型"
            resource-label="资源名称"
            resource-placeholder="筛选资源"
            empty-type-placeholder="先选择资源类型"
            :resource-type-options="resourceTypeOptions"
            :resource-options="resourceOptions"
            :resource-options-loading="resourceOptionsLoading"
            :resource-group-disabled="resourceGroupDisabled"
            @resource-type-change="handleResourceTypeChange"
            @resource-change="handleResourceChange"
            @resource-options-dropdown="handleResourceOptionsDropdown"
            @resource-options-search="handleResourceOptionsSearch"
          >
            <template #between>
              <label v-if="isManagementView" class="mobile-filter-field">
                <span>资源归属用户</span>
                <SystemPrincipalSelect
                  v-model:value="filters.resourceOwnerSystemAccountId"
                  v-model:selected-principal="filters.resourceOwnerSystemAccount"
                  :accounts="resourceOwners"
                  :active-only="false"
                  :filter-option="false"
                  :loading="resourceOwnerOptionsLoading"
                  include-all
                  all-label="全部资源归属用户"
                  placeholder="筛选资源归属用户"
                  @change="handleResourceOwnerChange"
                  @dropdown-visible-change="handleResourceOwnerOptionsDropdown"
                  @search="handleResourceOwnerOptionsSearch"
                />
              </label>
            </template>
          </AuthorizationUsageResourceFilterFields>
        </template>
      </ResponsiveListToolbar>
    </a-card>

    <StatsSummaryCards :cards="summaryCards" :loading="summaryLoading" />

    <a-card class="page-card authorization-usage-table-card" :loading="rowsInitialLoading">
      <div class="authorization-usage-table-head">
        <h3>团队消耗明细</h3>
      </div>
      <ResponsiveDataList
        class="authorization-usage-responsive-list"
        table-class="page-table authorization-usage-table"
        :columns="columns"
        :data-source="visibleTeamRows"
        row-key="id"
        :loading="loading"
        :pagination="tablePagination"
        :scroll-x="1360"
        mobile-pagination
        :mobile-has-more="mobileHasMore"
        :loading-more="mobileLoadingMore"
        pull-refresh-enabled
        :refreshing="loading"
        @change="handleTableChange"
        @mobile-load-more="loadMoreMobileTeamRows"
        @mobile-refresh="refreshMobileUsage"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" description="当前筛选范围暂无团队消耗。" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'team'">
            <div class="authorization-usage-name-cell">
              <span class="authorization-usage-name">{{ record.teamName }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'usage'">
            <UsageSummaryTags :usage="record.usage" />
          </template>
          <template v-else-if="column.key === 'account'">
            <div class="authorization-usage-resource-cell">
              <span class="authorization-usage-name">{{ resourceDisplayName(record) }}</span>
              <a-tag v-if="record.resourceType" :color="resourceTypeTag(record.resourceType).color">{{ resourceTypeTag(record.resourceType).text }}</a-tag>
            </div>
          </template>
          <template v-else-if="column.key === 'accountOwner'">
            <div class="authorization-usage-user-cell">
              <span class="authorization-usage-name">{{ record.accountOwnerSystemAccountName || '-' }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            {{ formatDateTime(record.lastUsedAt) }}
          </template>
          <template v-else-if="column.key === 'actions'">
            <RowActions :actions="detailActions" @action-click="handleTeamAction($event, record)" />
          </template>
        </template>
        <template #card="{ record }">
          <article class="mobile-list-card">
            <div class="mobile-list-card-head">
              <div class="mobile-list-card-title">{{ record.teamName }}</div>
            </div>
            <div class="mobile-list-meta-grid">
              <div class="mobile-list-meta-item mobile-list-meta-wide">
                <span>范围消耗</span>
                <strong><UsageSummaryTags :usage="record.usage" /></strong>
              </div>
              <div class="mobile-list-meta-item">
                <span>资源名称</span>
                <strong class="authorization-usage-resource-cell">
                  <span>{{ resourceDisplayName(record) }}</span>
                  <a-tag v-if="record.resourceType" :color="resourceTypeTag(record.resourceType).color">{{ resourceTypeTag(record.resourceType).text }}</a-tag>
                </strong>
              </div>
              <div class="mobile-list-meta-item">
                <span>资源归属人</span>
                <strong>{{ record.accountOwnerSystemAccountName || '-' }}</strong>
              </div>
              <div class="mobile-list-meta-item mobile-list-meta-wide">
                <span>最后使用</span>
                <strong>{{ formatDateTime(record.lastUsedAt) }}</strong>
              </div>
            </div>
            <RowActions variant="button" :actions="detailActions" @action-click="handleTeamAction($event, record)" />
          </article>
        </template>
      </ResponsiveDataList>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { authState } from '@/composables/useAuth'
import { useRemoteAuthorizationPrincipalOptions } from '@/composables/useRemoteAuthorizationPrincipalOptions'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList, type ResponsivePagedListLoadOptions } from '@/composables/useResponsivePagedList'
import { sanitizePaginationState, type PagePaginationState } from '@/shared/pageStateSanitizers'
import type { AuthorizationTeamUsageRowsResult, AuthorizationTeamUsageRow, AuthorizationTeamUsageSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatDateTime } from './authorizationFormatters'
import {
  buildAuthorizationTeamUsageSummaryCards,
  createAuthorizationUsageShowTotal,
  resourceDisplayName,
  resourceTypeTag
} from './authorizationUsageDisplay'
import {
  countAuthorizationTeamUsageActiveFilters,
  countAuthorizationUsageAdvancedFilters,
  defaultAuthorizationTeamUsageFilters,
  type AuthorizationTeamUsageFilters
} from './authorizationUsageFilters'
import AuthorizationUsageResourceFilterFields from './AuthorizationUsageResourceFilterFields.vue'
import { authorizationTeamUsageColumns, authorizationTeamUsageDetailActions } from './authorizationUsageTableConfig'
import { authorizationResourceTypeOptions } from './authorizationTableColumns'
import { useAuthorizationUsageDateRange } from './useAuthorizationUsageDateRange'
import { useAuthorizationUsageResourceFilters } from './useAuthorizationUsageResourceFilters'
import { buildAuthorizationUsageSignature, createAuthorizationUsageRequestGate } from './authorizationUsageRequestGate'

interface AuthorizationTeamUsagePageState {
  dateRange: {
    explicit: boolean
    startDate?: string
    endDate?: string
  }
  filters: AuthorizationTeamUsageFilters
  pagination: PagePaginationState
}

const router = useRouter()
const authorizationUsagePageSize = 20
const pageStateCache = usePageStateCache<AuthorizationTeamUsagePageState>(undefined, defaultAuthorizationTeamUsagePageState, {
  sanitize: sanitizeAuthorizationTeamUsagePageState,
  version: 2
})
const initialPageState = pageStateCache.read()
const overview = ref<AuthorizationTeamUsageRowsResult>()
const usageSummary = ref<AuthorizationTeamUsageSummary>()
const summaryLoading = ref(false)
const summaryError = ref('')
const rowsError = ref('')
const summaryResolvedSignature = ref('')
const rowsResolvedSignature = ref('')
const requestGate = createAuthorizationUsageRequestGate()
let pageActive = true
const requestEpoch = ref(0)

const filters = reactive<AuthorizationTeamUsageFilters>({ ...defaultAuthorizationTeamUsageFilters(), ...initialPageState.filters })
const {
  isManagementView,
  selectedResourceOwnerSystemAccountId,
  resourceGroupDisabled,
  resourceOptions,
  resourceOptionsLoading,
  handleResourceOptionsDropdown,
  handleResourceOptionsSearch,
  invalidate: invalidateResourceOptions,
  resetResourceId,
  resetResourceOptionsSearch
} = useAuthorizationUsageResourceFilters(filters)
const {
  handleDropdown: handleResourceOwnerOptionsDropdown,
  handleSearch: handleResourceOwnerOptionsSearch,
  invalidate: invalidateResourceOwnerOptions,
  load: loadResourceOwnerOptions,
  loading: resourceOwnerOptionsLoading,
  resetSearch: resetResourceOwnerOptionsSearch,
  systemAccounts: resourceOwners
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  errorMessage: '加载资源归属用户失败',
  selectedIds: () => [filters.resourceOwnerSystemAccountId]
})
const {
  handleDropdown: handleTeamOptionsDropdown,
  handleSearch: handleTeamOptionsSearch,
  invalidate: invalidateTeamOptions,
  load: loadTeamOptions,
  loading: teamOptionsLoading,
  options: teams,
  resetSearch: resetTeamOptionsSearch
} = useRemoteAuthorizationPrincipalOptions<SystemTeamPrincipalSummary>({
  errorMessage: '加载被授权团队失败',
  isManagementView: () => isManagementView.value,
  kind: 'team',
  selectedIds: () => [filters.teamId]
})
const {
  dateRange,
  dateRangeExplicit,
  displayRange,
  rangeLabel,
  handleDateRangeChange,
  selectedRangeParams,
  handleCalendarChange,
  handleDateRangeOpenChange,
  disabledDate,
  resetDateRange,
  setExplicitDateRange,
  syncDateRangeFromResponse
} = useAuthorizationUsageDateRange({
  viewScope: isManagementView.value ? 'admin' : 'self',
  onChange: reloadFromFirstPage
})
if (initialPageState.dateRange.explicit) {
  setExplicitDateRange({
    startDate: initialPageState.dateRange.startDate,
    endDate: initialPageState.dateRange.endDate
  })
}
const {
  items: teamRows,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  invalidatePendingLoads,
  loadData,
  loadMoreMobile: loadMoreMobileTeamRows,
  resetPagination
} = useResponsivePagedList<AuthorizationTeamUsageRow, { forceOptions?: boolean }>({
  pageSize: authorizationUsagePageSize,
  initialPagination: initialPageState.pagination,
  showTotal: createAuthorizationUsageShowTotal('团队消耗'),
  fetchPage: fetchTeamUsagePage,
  onError: (error) => {
    console.error(error)
    rowsError.value = '加载团队消耗明细失败'
  },
  requestSignature: () => [currentUsageSignature(), requestEpoch.value]
})
const resourceTypeOptions = authorizationResourceTypeOptions
const detailActions = authorizationTeamUsageDetailActions
const columns = authorizationTeamUsageColumns

const rowsInitialLoading = computed(() => loading.value && !overview.value)
const activeFilterCount = computed(() => countAuthorizationTeamUsageActiveFilters(filters, {
  dateRangeExplicit: dateRangeExplicit.value,
  resourceGroupDisabled: resourceGroupDisabled.value,
  selectedResourceOwnerSystemAccountId: selectedResourceOwnerSystemAccountId.value
}))
const advancedFilterCount = computed(() => countAuthorizationUsageAdvancedFilters(filters, resourceGroupDisabled.value))
const visibleTeamRows = computed(() => rowsResolvedSignature.value === currentUsageSignature() ? teamRows.value : [])
const summaryCards = computed(() => buildAuthorizationTeamUsageSummaryCards({
  hasMore: rowsResolvedSignature.value === currentUsageSignature() ? overview.value?.hasMore : undefined,
  loadedCount: visibleTeamRows.value.length,
  rangeLabel: rangeLabel.value,
  summary: summaryResolvedSignature.value === currentUsageSignature() ? usageSummary.value?.summary : undefined
}))

async function fetchTeamUsagePage(loadPageOptions: ResponsivePagedListLoadOptions & { forceOptions?: boolean }, pageState: { current: number; pageSize: number }) {
  const signature = currentUsageSignature()
  const requestToken = requestGate.begin('rows', signature)
  rowsError.value = ''
  const ownerSystemAccountId = selectedResourceOwnerSystemAccountId.value
  const rangeParams = selectedRangeParams()
  const params = {
    systemAccountId: ownerSystemAccountId,
    resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
    resourceId: filters.resourceType === 'all' || resourceGroupDisabled.value ? undefined : filters.resourceId,
    teamId: filters.teamId,
    page: pageState.current,
    pageSize: pageState.pageSize,
    ...rangeParams
  }
  if (loadPageOptions.forceOptions === true) {
    resetTeamOptionsSearch()
    resetResourceOwnerOptionsSearch()
    resetResourceOptionsSearch()
  }
  const usageOverview = isManagementView.value ? await api.authorizations.teamUsage(params) : await api.myAuthorizations.teamUsage(params)
  if (!requestGate.isCurrent(requestToken, currentUsageSignature())) return { ...usageOverview, items: [], superseded: true }
  if (!requestGate.acceptRange(requestToken, currentUsageSignature(), usageOverview.range)) throw new Error('团队明细与汇总统计范围不一致')
  overview.value = usageOverview
  rowsResolvedSignature.value = signature
  syncDateRangeFromResponse(usageOverview.range)
  return {
    items: usageOverview.rows,
    page: usageOverview.page,
    pageSize: usageOverview.pageSize,
    total: usageOverview.total,
    hasMore: usageOverview.hasMore
  }
}

async function loadUsageSummary() {
  const signature = currentUsageSignature()
  const requestToken = requestGate.begin('summary', signature)
  if (summaryResolvedSignature.value !== signature) usageSummary.value = undefined
  summaryError.value = ''
  summaryLoading.value = true
  try {
    const params = {
      systemAccountId: selectedResourceOwnerSystemAccountId.value,
      resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
      resourceId: filters.resourceType === 'all' || resourceGroupDisabled.value ? undefined : filters.resourceId,
      teamId: filters.teamId,
      ...selectedRangeParams()
    }
    const result = isManagementView.value ? await api.authorizations.teamUsageSummary(params) : await api.myAuthorizations.teamUsageSummary(params)
    if (!requestGate.isCurrent(requestToken, currentUsageSignature())) return
    if (!requestGate.acceptRange(requestToken, currentUsageSignature(), result.range)) throw new Error('团队明细与汇总统计范围不一致')
    usageSummary.value = result
    summaryResolvedSignature.value = signature
  } catch (error) {
    if (requestGate.isCurrent(requestToken, currentUsageSignature())) {
      console.error(error)
      summaryError.value = '加载团队消耗汇总失败'
    }
  } finally {
    if (requestGate.isCurrent(requestToken, currentUsageSignature())) summaryLoading.value = false
  }
}

function currentUsageSignature(): string {
  const range = selectedRangeParams()
  const user = authState.currentUser.value
  return buildAuthorizationUsageSignature({
    kind: 'team',
    scope: isManagementView.value ? 'admin' : 'self',
    authRevision: authState.revision.value,
    viewerId: user?.id,
    viewerRole: user?.role,
    ownerSystemAccountId: isManagementView.value ? selectedResourceOwnerSystemAccountId.value : user?.id,
    startDate: range.startDate,
    endDate: range.endDate,
    resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
    resourceId: filters.resourceType === 'all' || resourceGroupDisabled.value ? undefined : filters.resourceId,
    teamId: filters.teamId
  })
}

function reloadFromFirstPage(options: { forceOptions?: boolean } = {}) {
  resetPagination()
  if (!pageActive) return
  requestGate.beginBatch(currentUsageSignature())
  void loadUsageSummary()
  void loadData(options)
}

function refreshUsage() {
  requestGate.beginBatch(currentUsageSignature())
  void loadUsageSummary()
  void loadData()
}

function refreshMobileUsage() {
  resetPagination()
  refreshUsage()
}

function handleTeamChange() {
  resetTeamOptionsSearch()
  reloadFromFirstPage()
}

function handleResourceChange() {
  resetResourceOptionsSearch()
  reloadFromFirstPage()
}

function handleTeamAction(key: string, row: AuthorizationTeamUsageRow) {
  if (key !== 'users') return
  const [startDate, endDate] = displayRange.value
  void router.push({
    path: isManagementView.value ? '/authorization-user-usage' : '/my-authorization-user-usage',
    query: {
      teamId: row.teamId,
      startDate,
      endDate,
      resourceType: row.resourceType,
      resourceId: row.resourceId,
      resourceOwnerSystemAccountId: row.accountOwnerSystemAccountId ?? selectedResourceOwnerSystemAccountId.value
    }
  })
}

function handleResourceTypeChange() {
  resetResourceId()
  resetResourceOptionsSearch()
  reloadFromFirstPage({ forceOptions: true })
}

function handleResourceOwnerChange() {
  resetResourceId()
  resetResourceOwnerOptionsSearch()
  resetResourceOptionsSearch()
  reloadFromFirstPage({ forceOptions: true })
}

function resetFilters() {
  Object.assign(filters, defaultAuthorizationTeamUsageFilters())
  resetResourceOwnerOptionsSearch()
  resetTeamOptionsSearch()
  resetResourceOptionsSearch()
  resetDateRange()
  pageStateCache.clear()
  reloadFromFirstPage({ forceOptions: true })
}

function defaultAuthorizationTeamUsagePageState(): AuthorizationTeamUsagePageState {
  return {
    dateRange: { explicit: false },
    filters: defaultAuthorizationTeamUsageFilters(),
    pagination: { current: 1, pageSize: authorizationUsagePageSize }
  }
}

function sanitizeAuthorizationTeamUsagePageState(value: unknown, fallback: AuthorizationTeamUsagePageState): AuthorizationTeamUsagePageState {
  const source = value && typeof value === 'object' ? value as Partial<AuthorizationTeamUsagePageState> : {}
  const dateRange = source.dateRange && typeof source.dateRange === 'object'
    ? source.dateRange as Partial<AuthorizationTeamUsagePageState['dateRange']>
    : {}
  return {
    dateRange: {
      explicit: dateRange.explicit === true,
      startDate: typeof dateRange.startDate === 'string' ? dateRange.startDate : undefined,
      endDate: typeof dateRange.endDate === 'string' ? dateRange.endDate : undefined
    },
    filters: { ...fallback.filters, ...(source.filters && typeof source.filters === 'object' ? source.filters : {}) },
    pagination: sanitizePaginationState(source.pagination, fallback.pagination)
  }
}

function snapshotPageState(): AuthorizationTeamUsagePageState {
  const [startDate, endDate] = displayRange.value
  return {
    dateRange: {
      explicit: dateRangeExplicit.value,
      startDate: dateRangeExplicit.value ? startDate : undefined,
      endDate: dateRangeExplicit.value ? endDate : undefined
    },
    filters: { ...filters },
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(() => authState.revision.value, () => {
  requestEpoch.value += 1
  requestGate.deactivate()
  invalidatePendingLoads()
  invalidateResourceOwnerOptions()
  invalidateTeamOptions()
  invalidateResourceOptions()
  Object.assign(filters, defaultAuthorizationTeamUsageFilters())
  resourceOwners.value = []
  teams.value = []
  resetResourceOwnerOptionsSearch()
  resetTeamOptionsSearch()
  resetResourceOptionsSearch()
  if (pageActive) requestGate.activate()
  overview.value = undefined
  usageSummary.value = undefined
  summaryLoading.value = false
  summaryError.value = ''
  rowsError.value = ''
  summaryResolvedSignature.value = ''
  rowsResolvedSignature.value = ''
  teamRows.value = []
  resetPagination()
})

onMounted(() => {
  requestGate.beginBatch(currentUsageSignature())
  void loadUsageSummary()
  void loadData()
})
onDeactivated(() => {
  pageActive = false
  requestGate.deactivate()
  summaryLoading.value = false
})
onActivated(() => {
  pageActive = true
  requestGate.activate()
})
onBeforeUnmount(() => requestGate.deactivate())
</script>

<style scoped src="./authorization-usage.css"></style>

<style scoped>
.authorization-usage-name-cell {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  gap: 8px;
}
</style>
