<template>
  <div class="authorization-usage-page">
    <a-card class="page-card authorization-usage-header-card">
      <ResponsiveListToolbar
        :show-search="false"
        filter-title="筛选用户消耗"
        :active-filter-count="activeFilterCount"
        :advanced-filter-count="advancedFilterCount"
        :refresh-loading="loading"
        @reset="resetFilters"
        @refresh="loadData"
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
            :accounts="resourceOwnerUsers"
            :active-only="false"
            :filter-option="false"
            :loading="resourceOwnerUserOptionsLoading"
            include-all
            all-label="全部资源归属用户"
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选资源归属用户"
            @change="handleResourceOwnerChange"
            @dropdown-visible-change="handleResourceOwnerUserOptionsDropdown"
            @search="handleResourceOwnerUserOptionsSearch"
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
            placeholder="筛选所属团队"
            scope="team"
            @change="handleTeamChange"
            @dropdown-visible-change="handleTeamOptionsDropdown"
            @search="handleTeamOptionsSearch"
          />
          <SystemPrincipalSelect
            v-model:value="filters.granteeSystemAccountId"
            v-model:selected-principal="filters.granteeSystemAccount"
            :accounts="granteeUsers"
            :active-only="false"
            :filter-option="false"
            :loading="granteeUserOptionsLoading"
            allow-clear
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选被授权用户"
            @change="handleUserChange"
            @dropdown-visible-change="handleGranteeUserOptionsDropdown"
            @search="handleGranteeUserOptionsSearch"
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
              type-label="授权内容"
              resource-label="授权资源"
              resource-placeholder="筛选授权资源"
              empty-type-placeholder="先选择授权内容"
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
            <span>所属团队</span>
            <SystemPrincipalSelect
              v-model:value="filters.teamId"
              v-model:selected-principal="filters.team"
              :teams="teams"
              :active-only="false"
              :filter-option="false"
              :loading="teamOptionsLoading"
              allow-clear
              scope="team"
              placeholder="筛选所属团队"
              @change="handleTeamChange"
              @dropdown-visible-change="handleTeamOptionsDropdown"
              @search="handleTeamOptionsSearch"
            />
          </label>
          <label class="mobile-filter-field">
            <span>被授权用户</span>
            <SystemPrincipalSelect
              v-model:value="filters.granteeSystemAccountId"
              v-model:selected-principal="filters.granteeSystemAccount"
              :accounts="granteeUsers"
              :active-only="false"
              :filter-option="false"
              :loading="granteeUserOptionsLoading"
              allow-clear
              placeholder="筛选被授权用户"
              @change="handleUserChange"
              @dropdown-visible-change="handleGranteeUserOptionsDropdown"
              @search="handleGranteeUserOptionsSearch"
            />
          </label>
          <AuthorizationUsageResourceFilterFields
            v-model:resource-type="filters.resourceType"
            v-model:resource-id="filters.resourceId"
            v-model:resource-account="filters.resourceAccount"
            v-model:resource-group="filters.resourceGroup"
            variant="mobile"
            type-label="授权内容"
            resource-label="授权资源"
            resource-placeholder="筛选授权资源"
            empty-type-placeholder="先选择授权内容"
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
                  :accounts="resourceOwnerUsers"
                  :active-only="false"
                  :filter-option="false"
                  :loading="resourceOwnerUserOptionsLoading"
                  include-all
                  all-label="全部资源归属用户"
                  placeholder="筛选资源归属用户"
                  @change="handleResourceOwnerChange"
                  @dropdown-visible-change="handleResourceOwnerUserOptionsDropdown"
                  @search="handleResourceOwnerUserOptionsSearch"
                />
              </label>
            </template>
          </AuthorizationUsageResourceFilterFields>
        </template>
      </ResponsiveListToolbar>
    </a-card>

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" />

    <a-card class="page-card authorization-usage-table-card" :loading="initialLoading">
      <div class="authorization-usage-table-head">
        <h3>用户消耗明细</h3>
      </div>
      <ResponsiveDataList
        class="authorization-usage-responsive-list"
        table-class="page-table authorization-usage-table"
        :columns="columns"
        :data-source="userRows"
        row-key="id"
        :loading="loading"
        :pagination="tablePagination"
        :scroll-x="1440"
        mobile-pagination
        :mobile-has-more="mobileHasMore"
        :loading-more="mobileLoadingMore"
        pull-refresh-enabled
        :refreshing="loading"
        @change="handleTableChange"
        @mobile-load-more="loadMoreMobileUserRows"
        @mobile-refresh="refreshMobileUserRows"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" description="当前筛选范围暂无用户授权消耗。" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'user'">
            <div class="authorization-usage-user-cell">
              <span class="authorization-usage-name">{{ record.userName }}</span>
              <span v-if="record.username && record.username !== record.userName" class="authorization-usage-subtext">{{ record.username }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'teams'">
            <span class="authorization-usage-name">{{ teamDisplayName(record) }}</span>
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
          <template v-else-if="column.key === 'usage'">
            <UsageSummaryTags :usage="record.usage" />
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            {{ formatDateTime(record.lastUsedAt) }}
          </template>
        </template>
        <template #card="{ record }">
          <article class="mobile-list-card">
            <div class="mobile-list-card-head">
              <div>
                <div class="mobile-list-card-title">{{ record.userName }}</div>
                <div v-if="record.username && record.username !== record.userName" class="authorization-usage-subtext">{{ record.username }}</div>
              </div>
            </div>
            <div class="mobile-list-meta-grid">
              <div class="mobile-list-meta-item mobile-list-meta-wide">
                <span>范围消耗</span>
                <strong><UsageSummaryTags :usage="record.usage" /></strong>
              </div>
              <div class="mobile-list-meta-item">
                <span>所属团队</span>
                <strong>{{ teamDisplayName(record) }}</strong>
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
          </article>
        </template>
      </ResponsiveDataList>
    </a-card>
  </div>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { useRemoteAuthorizationPrincipalOptions } from '@/composables/useRemoteAuthorizationPrincipalOptions'
import { useResponsivePagedList, type ResponsivePagedListLoadOptions } from '@/composables/useResponsivePagedList'
import { isDateKey } from '@/shared/dateRange'
import type { AuthorizationUserUsageOverview, AuthorizationUserUsageRow, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatDateTime } from './authorizationFormatters'
import {
  buildAuthorizationUserUsageSummaryCards,
  createAuthorizationUsageShowTotal,
  resourceDisplayName,
  resourceTypeTag,
  teamDisplayName
} from './authorizationUsageDisplay'
import {
  countAuthorizationUsageAdvancedFilters,
  countAuthorizationUserUsageActiveFilters,
  defaultAuthorizationUserUsageFilters,
  type AuthorizationUserUsageFilters
} from './authorizationUsageFilters'
import AuthorizationUsageResourceFilterFields from './AuthorizationUsageResourceFilterFields.vue'
import { authorizationUserUsageColumns } from './authorizationUsageTableConfig'
import { authorizationResourceTypeOptions } from './authorizationTableColumns'
import { useAuthorizationUsageDateRange } from './useAuthorizationUsageDateRange'
import { useAuthorizationUsageResourceFilters } from './useAuthorizationUsageResourceFilters'

const route = useRoute()
const authorizationUsagePageSize = 20
const overview = ref<AuthorizationUserUsageOverview>()
let optionsLoaded = false
let optionsLoading: Promise<void> | undefined
let usageRequestSeq = 0

const filters = reactive<AuthorizationUserUsageFilters>(defaultAuthorizationUserUsageFilters())
const {
  isManagementView,
  selectedResourceOwnerSystemAccountId,
  resourceGroupDisabled,
  resourceOptions,
  resourceOptionsLoading,
  handleResourceOptionsDropdown,
  handleResourceOptionsSearch,
  loadAuthorizableResourceOptions,
  resetResourceId,
  resetResourceOptionsSearch
} = useAuthorizationUsageResourceFilters(filters)
const {
  handleDropdown: handleTeamOptionsDropdown,
  handleSearch: handleTeamOptionsSearch,
  load: loadTeamOptions,
  loading: teamOptionsLoading,
  options: teams,
  resetSearch: resetTeamOptionsSearch
} = useRemoteAuthorizationPrincipalOptions<SystemTeamPrincipalSummary>({
  errorMessage: '加载授权团队失败',
  isManagementView: () => isManagementView.value,
  kind: 'team',
  selectedIds: () => [filters.teamId]
})
const {
  handleDropdown: handleGranteeUserOptionsDropdown,
  handleSearch: handleGranteeUserOptionsSearch,
  load: loadGranteeUserOptions,
  loading: granteeUserOptionsLoading,
  options: granteeUsers,
  resetSearch: resetGranteeUserOptionsSearch
} = useRemoteAuthorizationPrincipalOptions<SystemAccountPrincipalSummary>({
  errorMessage: '加载被授权用户列表失败',
  isManagementView: () => isManagementView.value,
  kind: 'account',
  selectedIds: () => [filters.granteeSystemAccountId]
})
const {
  handleDropdown: handleResourceOwnerUserOptionsDropdown,
  handleSearch: handleResourceOwnerUserOptionsSearch,
  load: loadResourceOwnerUserOptions,
  loading: resourceOwnerUserOptionsLoading,
  options: resourceOwnerUsers,
  resetSearch: resetResourceOwnerUserOptionsSearch
} = useRemoteAuthorizationPrincipalOptions<SystemAccountPrincipalSummary>({
  enabled: () => isManagementView.value,
  errorMessage: '加载资源归属用户列表失败',
  isManagementView: () => true,
  kind: 'account',
  selectedIds: () => [filters.resourceOwnerSystemAccountId]
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
} = useAuthorizationUsageDateRange({ onChange: reloadFromFirstPage })
const {
  items: userRows,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileUserRows,
  refreshMobile: refreshMobileUserRows,
  resetPagination
} = useResponsivePagedList<AuthorizationUserUsageRow, { forceOptions?: boolean }>({
  pageSize: authorizationUsagePageSize,
  showTotal: createAuthorizationUsageShowTotal('用户消耗'),
  fetchPage: fetchUserUsagePage,
  onError: (error) => {
    console.error(error)
    message.error('加载用户消耗明细失败')
  }
})
const resourceTypeOptions = authorizationResourceTypeOptions
const columns = authorizationUserUsageColumns

const initialLoading = computed(() => loading.value && !overview.value)
const activeFilterCount = computed(() => countAuthorizationUserUsageActiveFilters(filters, {
  dateRangeExplicit: dateRangeExplicit.value,
  resourceGroupDisabled: resourceGroupDisabled.value,
  selectedResourceOwnerSystemAccountId: selectedResourceOwnerSystemAccountId.value
}))
const advancedFilterCount = computed(() => countAuthorizationUsageAdvancedFilters(filters, resourceGroupDisabled.value))
const summaryCards = computed(() => buildAuthorizationUserUsageSummaryCards({
  hasMore: overview.value?.hasMore,
  rangeLabel: rangeLabel.value,
  summary: overview.value?.summary,
  userCount: overview.value?.userCount
}))

async function loadOptions(options: { force?: boolean } = {}) {
  if (options.force) {
    resetTeamOptionsSearch()
    resetGranteeUserOptionsSearch()
    resetResourceOwnerUserOptionsSearch()
    resetResourceOptionsSearch()
  }
  if (optionsLoaded && !options.force) return
  if (optionsLoading && !options.force) return optionsLoading
  optionsLoading = loadOptionsNow()
  try {
    await optionsLoading
    optionsLoaded = true
  } finally {
    optionsLoading = undefined
  }
}

async function loadOptionsNow() {
  const [teamResult, granteeUserResult, resourceOwnerUserResult, resourceResult] = await Promise.allSettled([
    loadTeamOptions(),
    loadGranteeUserOptions(),
    loadResourceOwnerUserOptions(),
    loadAuthorizableResourceOptions()
  ])
  if (teamResult.status === 'rejected') {
    console.error(teamResult.reason)
    message.error('加载授权团队失败')
  }
  if (granteeUserResult.status === 'rejected') {
    console.error(granteeUserResult.reason)
    message.error('加载被授权用户失败')
  }
  if (resourceOwnerUserResult.status === 'rejected') {
    console.error(resourceOwnerUserResult.reason)
    message.error('加载资源归属用户失败')
  }
  if (resourceResult.status === 'rejected') {
    console.error(resourceResult.reason)
    message.error('加载授权资源选项失败')
  }
}

async function fetchUserUsagePage(loadPageOptions: ResponsivePagedListLoadOptions & { forceOptions?: boolean }, pageState: { current: number; pageSize: number }) {
  const requestSeq = ++usageRequestSeq
  const ownerSystemAccountId = selectedResourceOwnerSystemAccountId.value
  const rangeParams = selectedRangeParams()
  const params = {
    systemAccountId: ownerSystemAccountId,
    resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
    resourceId: filters.resourceType === 'all' || resourceGroupDisabled.value ? undefined : filters.resourceId,
    teamId: filters.teamId,
    granteeSystemAccountId: filters.granteeSystemAccountId,
    page: pageState.current,
    pageSize: pageState.pageSize,
    ...rangeParams
  }
  if (loadPageOptions.forceOptions === true) {
    resetTeamOptionsSearch()
    resetGranteeUserOptionsSearch()
    resetResourceOwnerUserOptionsSearch()
    resetResourceOptionsSearch()
  }
  const usageOverview = isManagementView.value ? await api.authorizations.userUsage(params) : await api.myAuthorizations.userUsage(params)
  if (requestSeq === usageRequestSeq) {
    overview.value = usageOverview
    syncDateRangeFromResponse(usageOverview.range)
  }
  return {
    items: usageOverview.rows,
    page: usageOverview.page,
    pageSize: usageOverview.pageSize,
    total: usageOverview.total,
    hasMore: usageOverview.hasMore
  }
}

function reloadFromFirstPage(options: { forceOptions?: boolean } = {}) {
  resetPagination()
  void loadData(options)
}

function handleTeamChange() {
  resetTeamOptionsSearch()
  reloadFromFirstPage()
}

function handleUserChange() {
  resetGranteeUserOptionsSearch()
  reloadFromFirstPage()
}

function handleResourceChange() {
  resetResourceOptionsSearch()
  reloadFromFirstPage()
}

function handleResourceTypeChange() {
  resetResourceId()
  resetResourceOptionsSearch()
  reloadFromFirstPage({ forceOptions: true })
}

function handleResourceOwnerChange() {
  resetResourceId()
  resetResourceOwnerUserOptionsSearch()
  resetResourceOptionsSearch()
  reloadFromFirstPage({ forceOptions: true })
}

function resetFilters() {
  Object.assign(filters, defaultAuthorizationUserUsageFilters())
  resetTeamOptionsSearch()
  resetGranteeUserOptionsSearch()
  resetResourceOwnerUserOptionsSearch()
  resetResourceOptionsSearch()
  resetDateRange()
  reloadFromFirstPage({ forceOptions: true })
}

function applyRouteFilters() {
  if (!hasRouteFilters()) return
  const teamId = singleQueryValue(route.query.teamId)
  const granteeSystemAccountId = singleQueryValue(route.query.granteeSystemAccountId)
  const resourceOwnerSystemAccountId = singleQueryValue(route.query.resourceOwnerSystemAccountId)
  const resourceId = singleQueryValue(route.query.resourceId)
  const resourceType = route.query.resourceType === 'account' || route.query.resourceType === 'group' ? route.query.resourceType : undefined
  const startDate = singleQueryValue(route.query.startDate)
  const endDate = singleQueryValue(route.query.endDate)
  Object.assign(filters, defaultAuthorizationUserUsageFilters())
  resetDateRange()
  filters.teamId = teamId
  filters.granteeSystemAccountId = granteeSystemAccountId
  if (isManagementView.value && resourceOwnerSystemAccountId) {
    filters.resourceOwnerSystemAccountId = resourceOwnerSystemAccountId
  }
  if (resourceType) {
    filters.resourceType = resourceType
    filters.resourceId = resourceId
  }
  if (isDateKey(startDate) || isDateKey(endDate)) {
    setExplicitDateRange({ startDate, endDate })
  }
  resetTeamOptionsSearch()
  resetGranteeUserOptionsSearch()
  resetResourceOwnerUserOptionsSearch()
  resetResourceOptionsSearch()
}

function hasRouteFilters(): boolean {
  return Boolean(
    singleQueryValue(route.query.teamId)
    || singleQueryValue(route.query.granteeSystemAccountId)
    || singleQueryValue(route.query.resourceOwnerSystemAccountId)
    || singleQueryValue(route.query.resourceId)
    || singleQueryValue(route.query.startDate)
    || singleQueryValue(route.query.endDate)
    || route.query.resourceType === 'account'
    || route.query.resourceType === 'group'
  )
}

function singleQueryValue(value: unknown): string | undefined {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : undefined
  return typeof value === 'string' ? value : undefined
}

onMounted(() => {
  applyRouteFilters()
  reloadFromFirstPage()
})

watch(() => route.fullPath, () => {
  if (route.path !== '/authorization-user-usage' && route.path !== '/my-authorization-user-usage') return
  if (!hasRouteFilters()) return
  applyRouteFilters()
  reloadFromFirstPage()
})
</script>

<style scoped>
.authorization-usage-page {
  display: flex;
  height: calc(100dvh - 154px);
  min-height: 0;
  flex-direction: column;
  gap: 16px;
}

.authorization-usage-header-card,
.authorization-usage-page :deep(.stats-summary-grid) {
  flex: 0 0 auto;
}

.authorization-usage-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.authorization-usage-range {
  width: 260px;
}

.authorization-usage-select {
  min-width: 180px;
}

.authorization-usage-resource {
  min-width: 220px;
}

.authorization-usage-table-card {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.authorization-usage-table-card :deep(.ant-card-body) {
  display: flex;
  min-height: 0;
  flex: 1 1 auto;
  flex-direction: column;
}

.authorization-usage-table-head {
  display: flex;
  flex: 0 0 auto;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.authorization-usage-responsive-list {
  min-height: 0;
  flex: 1 1 auto;
}

.authorization-usage-table-head h3 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.authorization-usage-user-cell {
  display: grid;
  gap: 3px;
  min-width: 0;
}

.authorization-usage-resource-cell {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  gap: 8px;
  min-width: 0;
}

.authorization-usage-resource-cell :deep(.ant-tag) {
  flex: 0 0 auto;
  margin-inline-end: 0;
}

.authorization-usage-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 400;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.authorization-usage-subtext {
  min-width: 0;
  overflow: hidden;
  color: #64748b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.authorization-usage-number {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
}

.authorization-usage-table :deep(.ant-table-thead > tr > th),
.authorization-usage-table :deep(.ant-table-cell) {
  font-weight: 400;
  white-space: nowrap;
}

.authorization-usage-page :deep(.mobile-list-card-title),
.authorization-usage-page :deep(.mobile-list-meta-item strong) {
  font-weight: 400;
}

.authorization-usage-table :deep(.responsive-data-list-flex-column) {
  min-width: 260px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.advanced-filter-form :deep(.ant-select) {
  width: 100%;
}

@media (max-width: 900px) {
  .authorization-usage-page {
    height: auto;
    min-height: calc(100dvh - 122px);
  }
}
</style>
