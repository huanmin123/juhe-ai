<template>
  <div class="authorization-usage-page">
    <a-card class="page-card authorization-usage-header-card">
      <ResponsiveListToolbar
        :show-search="false"
        filter-title="筛选团队消耗"
        :active-filter-count="activeFilterCount"
        :refresh-loading="loading"
        @reset="resetFilters"
        @refresh="loadData"
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
            v-model:value="filters.teamId"
            :teams="teams"
            :active-only="false"
            allow-clear
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选授权团队"
            scope="team"
            @change="reloadFromFirstPage"
          />
          <a-select v-model:value="filters.resourceType" class="authorization-usage-select responsive-list-inline-filter" :options="resourceTypeOptions" @change="handleResourceTypeChange" />
          <SystemPrincipalSelect
            v-if="isManagementView"
            v-model:value="filters.resourceOwnerSystemAccountId"
            :accounts="resourceOwners"
            :active-only="false"
            include-all
            all-label="全部资源归属用户"
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选资源归属用户"
            @change="handleResourceOwnerChange"
          />
          <a-select
            v-model:value="filters.resourceId"
            show-search
            allow-clear
            option-filter-prop="label"
            class="authorization-usage-resource responsive-list-inline-filter"
            :options="resourceOptions"
            :disabled="filters.resourceType === 'all'"
            :placeholder="filters.resourceType === 'all' ? '先选择授权内容' : '筛选授权资源'"
            @change="reloadFromFirstPage"
          />
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
            <span>授权团队</span>
            <SystemPrincipalSelect v-model:value="filters.teamId" :teams="teams" :active-only="false" allow-clear scope="team" placeholder="筛选授权团队" @change="reloadFromFirstPage" />
          </label>
          <label class="mobile-filter-field">
            <span>授权内容</span>
            <a-select v-model:value="filters.resourceType" :options="resourceTypeOptions" @change="handleResourceTypeChange" />
          </label>
          <label v-if="isManagementView" class="mobile-filter-field">
            <span>资源归属用户</span>
            <SystemPrincipalSelect
              v-model:value="filters.resourceOwnerSystemAccountId"
              :accounts="resourceOwners"
              :active-only="false"
              include-all
              all-label="全部资源归属用户"
              placeholder="筛选资源归属用户"
              @change="handleResourceOwnerChange"
            />
          </label>
          <label class="mobile-filter-field">
            <span>授权资源</span>
            <a-select
              v-model:value="filters.resourceId"
              show-search
              allow-clear
              option-filter-prop="label"
              :options="resourceOptions"
              :disabled="filters.resourceType === 'all'"
              :placeholder="filters.resourceType === 'all' ? '先选择授权内容' : '筛选授权资源'"
              @change="reloadFromFirstPage"
            />
          </label>
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
          <a-empty class="page-empty-card" description="当前筛选范围暂无团队授权消耗。" />
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
              <span class="authorization-usage-name">{{ record.accountOwnerSystemAccountName || record.accountOwnerSystemAccountId || '-' }}</span>
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
                <strong>{{ record.accountOwnerSystemAccountName || record.accountOwnerSystemAccountId || '-' }}</strong>
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
import type { RowActionItem } from '@/components/rowActions'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { useResponsivePagedList, type ResponsivePagedListLoadOptions } from '@/composables/useResponsivePagedList'
import type { AuthorizationResourceType, AuthorizationTeamUsageOverview, AuthorizationTeamUsageRow, SystemAccountPrincipalSummary, SystemTeamPrincipalSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import {
  emptyUsageSummary,
  formatCost,
  formatDateTime,
  formatNumber,
  formatUsageAmount
} from './authorizationFormatters'
import { authorizationResourceTypeOptions, type AuthorizationFilterResourceType } from './authorizationTableColumns'
import { useAuthorizationUsageDateRange } from './useAuthorizationUsageDateRange'
import { useAuthorizationUsageResourceFilters } from './useAuthorizationUsageResourceFilters'

type TeamUsageFilters = {
  teamId?: string
  resourceOwnerSystemAccountId: string
  resourceType: AuthorizationFilterResourceType
  resourceId?: string
}
const router = useRouter()
const authorizationUsagePageSize = 20
const overview = ref<AuthorizationTeamUsageOverview>()
const teams = ref<SystemTeamPrincipalSummary[]>([])
const resourceOwners = ref<SystemAccountPrincipalSummary[]>([])
let optionsLoaded = false
let optionsLoading: Promise<void> | undefined
let usageRequestSeq = 0

const filters = reactive<TeamUsageFilters>(defaultFilters())
const {
  isManagementView,
  selectedResourceOwnerSystemAccountId,
  resourceOptions,
  loadAuthorizableResourceOptions,
  resetResourceId
} = useAuthorizationUsageResourceFilters(filters)
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
  showTotal: (total, _range, context) => {
    const loaded = context ? (context.current - 1) * context.pageSize + context.currentPageCount : total
    return context?.hasMore ? `已加载到第 ${formatNumber(loaded)} 条团队消耗，还有更多` : `共 ${formatNumber(total)} 条团队消耗`
  },
  fetchPage: fetchTeamUsagePage,
  onError: (error) => {
    console.error(error)
    message.error('加载团队消耗明细失败')
  }
})
const resourceTypeOptions = authorizationResourceTypeOptions
const detailActions: RowActionItem[] = [
  { key: 'users', label: '查询用户明细', icon: 'detail', tone: 'info' }
]
const columns = [
  { title: '资源名称', key: 'account', width: 220 },
  { title: '资源归属人', key: 'accountOwner', width: 180 },
  { title: '被授权团队', key: 'team', width: 240 },
  { title: '范围消耗', key: 'usage', width: 220 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 },
  { title: '操作', key: 'actions', width: 96, fixed: 'right' }
]

const initialLoading = computed(() => loading.value && !overview.value)
const activeFilterCount = computed(() => {
  let count = 0
  if (filters.teamId) count += 1
  if (selectedResourceOwnerSystemAccountId.value) count += 1
  if (filters.resourceType !== 'all') count += 1
  if (filters.resourceId) count += 1
  if (dateRangeExplicit.value) count += 1
  return count
})
const totalUsage = computed(() => overview.value?.summary ?? emptyUsageSummary())
const summaryCards = computed(() => [
  { key: 'teams', label: '授权团队', value: formatNumber(overview.value?.teamCount ?? 0), extra: `范围 ${rangeLabel.value}` },
  { key: 'requests', label: '范围请求', value: formatNumber(totalUsage.value.requestCount), extra: `最后使用 ${formatDateTime(totalUsage.value.lastUsedAt)}` },
  { key: 'tokens', label: 'Token 消耗', value: formatUsageAmount(totalUsage.value.totalTokens), extra: `输入 ${formatUsageAmount(totalUsage.value.inputTokens)}` },
  { key: 'cost', label: '成本', value: formatCost(totalUsage.value.totalCost), extra: `最后使用 ${formatDateTime(totalUsage.value.lastUsedAt)}` }
])

async function loadOptions(options: { force?: boolean } = {}) {
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
    isManagementView.value ? api.authorizationOptions.granteeTeams() : api.myAuthorizationOptions.granteeTeams(),
    isManagementView.value ? api.systemAccounts.options() : Promise.resolve([]),
    loadAuthorizableResourceOptions()
  ])
  if (teamResult.status === 'fulfilled') {
    teams.value = teamResult.value
  } else {
    console.error(teamResult.reason)
    message.error('加载授权团队失败')
  }
  if (ownerResult.status === 'fulfilled') {
    resourceOwners.value = ownerResult.value
  } else {
    console.error(ownerResult.reason)
    message.error('加载资源归属用户失败')
  }
  if (resourceResult.status === 'rejected') {
    console.error(resourceResult.reason)
    message.error('加载授权资源选项失败')
  }
}

async function fetchTeamUsagePage(loadPageOptions: ResponsivePagedListLoadOptions & { forceOptions?: boolean }, pageState: { current: number; pageSize: number }) {
  const requestSeq = ++usageRequestSeq
  const ownerSystemAccountId = selectedResourceOwnerSystemAccountId.value
  const rangeParams = selectedRangeParams()
  const params = {
    systemAccountId: ownerSystemAccountId,
    resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
    resourceId: filters.resourceType === 'all' ? undefined : filters.resourceId,
    teamId: filters.teamId,
    page: pageState.current,
    pageSize: pageState.pageSize,
    ...rangeParams
  }
  const [usageOverview] = await Promise.all([
    isManagementView.value ? api.authorizations.teamUsage(params) : api.myAuthorizations.teamUsage(params),
    loadOptions({ force: loadPageOptions.forceOptions === true })
  ])
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

function reloadFromFirstPage() {
  resetPagination()
  void loadData()
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

function resourceTypeTag(resourceType: AuthorizationResourceType) {
  return resourceType === 'group'
    ? { text: '分组', color: 'purple' }
    : { text: 'AI账户', color: 'blue' }
}

function resourceDisplayName(row: AuthorizationTeamUsageRow): string {
  return row.resourceName || row.resourceId || row.accountName || row.accountId || '-'
}

function handleResourceTypeChange() {
  resetResourceId()
  reloadFromFirstPage()
}

function handleResourceOwnerChange() {
  resetResourceId()
  reloadFromFirstPage()
}

function resetFilters() {
  Object.assign(filters, defaultFilters())
  resetDateRange()
  reloadFromFirstPage()
}

function defaultFilters(): TeamUsageFilters {
  return {
    resourceOwnerSystemAccountId: allSystemAccountsValue,
    resourceType: 'all',
    resourceId: undefined,
    teamId: undefined
  }
}

onMounted(loadData)
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

.authorization-usage-name-cell {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  gap: 8px;
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

@media (max-width: 900px) {
  .authorization-usage-page {
    height: auto;
    min-height: calc(100dvh - 122px);
  }
}
</style>
