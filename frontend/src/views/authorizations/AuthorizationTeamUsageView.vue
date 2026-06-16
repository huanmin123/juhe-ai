<template>
  <div class="authorization-usage-page">
    <a-card class="page-card authorization-usage-header-card">
      <ResponsiveListToolbar
        :show-search="false"
        filter-title="筛选团队消耗"
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

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" />

    <a-card class="page-card authorization-usage-table-card" :loading="initialLoading">
      <div class="authorization-usage-table-head">
        <h3>团队消耗明细</h3>
      </div>
      <ResponsiveDataList
        class="authorization-usage-responsive-list"
        table-class="page-table authorization-usage-table"
        :columns="columns"
        :data-source="teamRows"
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
        @mobile-refresh="refreshMobileTeamRows"
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
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { useRemoteAuthorizationPrincipalOptions } from '@/composables/useRemoteAuthorizationPrincipalOptions'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList, type ResponsivePagedListLoadOptions } from '@/composables/useResponsivePagedList'
import type { AuthorizationTeamUsageOverview, AuthorizationTeamUsageRow, SystemTeamPrincipalSummary } from '@/types/domain'
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

const router = useRouter()
const authorizationUsagePageSize = 20
const overview = ref<AuthorizationTeamUsageOverview>()
let optionsLoaded = false
let optionsLoading: Promise<void> | undefined
let usageRequestSeq = 0

const filters = reactive<AuthorizationTeamUsageFilters>(defaultAuthorizationTeamUsageFilters())
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
  handleDropdown: handleResourceOwnerOptionsDropdown,
  handleSearch: handleResourceOwnerOptionsSearch,
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
  syncDateRangeFromResponse
} = useAuthorizationUsageDateRange({ onChange: reloadFromFirstPage })
const {
  items: teamRows,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileTeamRows,
  refreshMobile: refreshMobileTeamRows,
  resetPagination
} = useResponsivePagedList<AuthorizationTeamUsageRow, { forceOptions?: boolean }>({
  pageSize: authorizationUsagePageSize,
  showTotal: createAuthorizationUsageShowTotal('团队消耗'),
  fetchPage: fetchTeamUsagePage,
  onError: (error) => {
    console.error(error)
    message.error('加载团队消耗明细失败')
  }
})
const resourceTypeOptions = authorizationResourceTypeOptions
const detailActions = authorizationTeamUsageDetailActions
const columns = authorizationTeamUsageColumns

const initialLoading = computed(() => loading.value && !overview.value)
const activeFilterCount = computed(() => countAuthorizationTeamUsageActiveFilters(filters, {
  dateRangeExplicit: dateRangeExplicit.value,
  resourceGroupDisabled: resourceGroupDisabled.value,
  selectedResourceOwnerSystemAccountId: selectedResourceOwnerSystemAccountId.value
}))
const advancedFilterCount = computed(() => countAuthorizationUsageAdvancedFilters(filters, resourceGroupDisabled.value))
const summaryCards = computed(() => buildAuthorizationTeamUsageSummaryCards({
  hasMore: overview.value?.hasMore,
  rangeLabel: rangeLabel.value,
  summary: overview.value?.summary,
  teamCount: overview.value?.teamCount
}))

async function loadOptions(options: { force?: boolean } = {}) {
  if (options.force) {
    resetResourceOwnerOptionsSearch()
    resetTeamOptionsSearch()
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
  const [teamResult, ownerResult, resourceResult] = await Promise.allSettled([
    loadTeamOptions(),
    loadResourceOwnerOptions(),
    loadAuthorizableResourceOptions()
  ])
  if (teamResult.status === 'rejected') {
    console.error(teamResult.reason)
    message.error('加载被授权团队失败')
  }
  if (ownerResult.status === 'rejected') {
    console.error(ownerResult.reason)
    message.error('加载资源归属用户失败')
  }
  if (resourceResult.status === 'rejected') {
    console.error(resourceResult.reason)
    message.error('加载资源选项失败')
  }
}

async function fetchTeamUsagePage(loadPageOptions: ResponsivePagedListLoadOptions & { forceOptions?: boolean }, pageState: { current: number; pageSize: number }) {
  const requestSeq = ++usageRequestSeq
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
  reloadFromFirstPage({ forceOptions: true })
}

onMounted(loadData)
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
