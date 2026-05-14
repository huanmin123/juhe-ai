<template>
  <div class="authorization-usage-page">
    <a-card class="page-card authorization-usage-header-card">
      <ResponsiveListToolbar
        :show-search="false"
        filter-title="筛选用户消耗"
        :active-filter-count="activeFilterCount"
        :refresh-loading="loading"
        @reset="resetFilters"
        @refresh="loadData"
      >
        <template #inline-filters>
          <a-date-picker
            v-model:value="usageMonthValue"
            :allow-clear="false"
            :disabled="loading"
            class="authorization-usage-range responsive-list-inline-filter"
            format="YYYY年M月"
            picker="month"
            @change="handleUsageMonthChange"
          />
          <SystemPrincipalSelect
            v-model:value="filters.teamId"
            :teams="teams"
            :active-only="false"
            allow-clear
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选授权团队"
            scope="team"
            @change="loadData"
          />
          <SystemPrincipalSelect
            v-model:value="filters.granteeSystemAccountId"
            :accounts="users"
            :active-only="false"
            allow-clear
            class="authorization-usage-select responsive-list-inline-filter"
            placeholder="筛选被授权用户"
            @change="loadData"
          />
          <a-select v-model:value="filters.resourceType" class="authorization-usage-select responsive-list-inline-filter" :options="resourceTypeOptions" @change="handleResourceTypeChange" />
          <a-select
            v-model:value="filters.resourceId"
            show-search
            allow-clear
            option-filter-prop="label"
            class="authorization-usage-resource responsive-list-inline-filter"
            :options="resourceOptions"
            :disabled="filters.resourceType === 'all'"
            :placeholder="filters.resourceType === 'all' ? '先选择授权内容' : '筛选授权资源'"
            @change="loadData"
          />
        </template>
        <template #filters>
          <label class="mobile-filter-field">
            <span>用量月份</span>
            <a-date-picker
              v-model:value="usageMonthValue"
              :allow-clear="false"
              :disabled="loading"
              format="YYYY年M月"
              picker="month"
              @change="handleUsageMonthChange"
            />
          </label>
          <label class="mobile-filter-field">
            <span>授权团队</span>
            <SystemPrincipalSelect v-model:value="filters.teamId" :teams="teams" :active-only="false" allow-clear scope="team" placeholder="筛选授权团队" @change="loadData" />
          </label>
          <label class="mobile-filter-field">
            <span>被授权用户</span>
            <SystemPrincipalSelect v-model:value="filters.granteeSystemAccountId" :accounts="users" :active-only="false" allow-clear placeholder="筛选被授权用户" @change="loadData" />
          </label>
          <label class="mobile-filter-field">
            <span>授权内容</span>
            <a-select v-model:value="filters.resourceType" :options="resourceTypeOptions" @change="handleResourceTypeChange" />
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
              @change="loadData"
            />
          </label>
        </template>
      </ResponsiveListToolbar>
    </a-card>

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" />

    <a-card class="page-card authorization-usage-table-card" :loading="initialLoading">
      <div class="authorization-usage-table-head">
        <h3>用户消耗明细</h3>
      </div>
      <ResponsiveDataList
        table-class="page-table authorization-usage-table"
        :columns="columns"
        :data-source="userRows"
        row-key="id"
        :loading="loading"
        :pagination="false"
        :scroll-x="1120"
        pull-refresh-enabled
        :refreshing="loading"
        @mobile-refresh="loadData"
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
          <template v-else-if="column.key === 'sources'">
            <div class="authorization-usage-tags">
              <a-tag v-for="label in record.sourceLabels.slice(0, 3)" :key="label" color="blue">{{ label }}</a-tag>
              <a-tag v-if="record.sourceLabels.length > 3">+{{ record.sourceLabels.length - 3 }}</a-tag>
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
            <div class="authorization-usage-tags">
              <a-tag v-for="label in record.sourceLabels.slice(0, 3)" :key="label" color="blue">{{ label }}</a-tag>
              <a-tag v-if="record.sourceLabels.length > 3">+{{ record.sourceLabels.length - 3 }}</a-tag>
            </div>
            <div class="mobile-list-meta-grid">
              <div class="mobile-list-meta-item mobile-list-meta-wide">
                <span>月度消耗</span>
                <strong><UsageSummaryTags :usage="record.usage" /></strong>
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
import dayjs, { type Dayjs } from 'dayjs'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import type { AccountSummary, AuthorizationUserUsageOverview, AuthorizationUserUsageRow, GroupSummary, SystemAccountPrincipalSummary, SystemTeamSummary } from '@/types/domain'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import {
  emptyUsageSummary,
  formatCost,
  formatDateTime,
  formatNumber,
  formatUsageAmount
} from './authorizationFormatters'
import { authorizationResourceTypeOptions, type AuthorizationFilterResourceType } from './authorizationTableColumns'

type UserUsageFilters = {
  teamId?: string
  granteeSystemAccountId?: string
  resourceType: AuthorizationFilterResourceType
  resourceId?: string
  statMonth: string
}
const route = useRoute()
const { isManagementView } = useScopedMenuView()
const loading = ref(false)
const overview = ref<AuthorizationUserUsageOverview>()
const teams = ref<SystemTeamSummary[]>([])
const users = ref<SystemAccountPrincipalSummary[]>([])
const accounts = ref<AccountSummary[]>([])
const groups = ref<GroupSummary[]>([])

const filters = reactive<UserUsageFilters>(defaultFilters())
const resourceTypeOptions = authorizationResourceTypeOptions
const columns = [
  { title: '被授权用户', key: 'user', width: 230 },
  { title: '授权来源', key: 'sources', width: 240 },
  { title: '月度消耗', key: 'usage', width: 220 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 }
]

const initialLoading = computed(() => loading.value && !overview.value)
const defaultMonth = computed(() => defaultUsageMonth())
const activeFilterCount = computed(() => {
  let count = 0
  if (filters.teamId) count += 1
  if (filters.granteeSystemAccountId) count += 1
  if (filters.resourceType !== 'all') count += 1
  if (filters.resourceId) count += 1
  if (filters.statMonth !== defaultMonth.value) count += 1
  return count
})
const resourceOptions = computed(() => {
  if (filters.resourceType === 'all') return []
  if (filters.resourceType === 'account') {
    return ownAuthorizableAccounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return ownAuthorizableGroups.value.map((group) => ({ label: group.name, value: group.id }))
})
const ownAuthorizableAccounts = computed(() => accounts.value.filter((account) => account.permissions?.canAuthorize !== false))
const ownAuthorizableGroups = computed(() => groups.value.filter((group) => group.permissions?.canAuthorize !== false))
const userRows = computed<AuthorizationUserUsageRow[]>(() => overview.value?.rows ?? [])
const totalUsage = computed(() => overview.value?.summary ?? emptyUsageSummary())
const rangeLabel = computed(() => formatMonthLabel(filters.statMonth))
const summaryCards = computed(() => [
  { key: 'users', label: '被授权用户', value: formatNumber(overview.value?.userCount ?? 0), extra: `月份 ${rangeLabel.value}` },
  { key: 'requests', label: '月度请求', value: formatNumber(totalUsage.value.requestCount), extra: `最后使用 ${formatDateTime(totalUsage.value.lastUsedAt)}` },
  { key: 'tokens', label: 'Token 消耗', value: formatUsageAmount(totalUsage.value.totalTokens), extra: `输入 ${formatUsageAmount(totalUsage.value.inputTokens)}` },
  { key: 'cost', label: '成本', value: formatCost(totalUsage.value.totalCost), extra: `最后使用 ${formatDateTime(totalUsage.value.lastUsedAt)}` }
])
const usageMonthValue = computed<Dayjs>({
  get() {
    return dayjs(`${filters.statMonth}-01`).startOf('month')
  },
  set(value) {
    filters.statMonth = normalizeUsageMonth(value)
  }
})

async function loadOptions() {
  const [teamResult, userResult, accountResult, groupResult] = await Promise.allSettled([
    isManagementView.value ? api.systemTeams.list() : api.myTeams.list(),
    isManagementView.value ? api.systemAccounts.list() : api.myAuthorizationOptions.granteeAccounts(),
    isManagementView.value ? api.accounts.list({ limit: 500 }) : api.myAccounts.list({ limit: 500 }),
    isManagementView.value ? api.groups.list() : api.myGroups.list()
  ])
  if (teamResult.status === 'fulfilled') {
    teams.value = teamResult.value
  } else {
    console.error(teamResult.reason)
    message.error('加载授权团队失败')
  }
  if (userResult.status === 'fulfilled') {
    users.value = userResult.value
  } else {
    console.error(userResult.reason)
    message.error('加载系统账户列表失败')
  }
  if (accountResult.status === 'fulfilled') {
    accounts.value = accountResult.value.items
  } else {
    console.error(accountResult.reason)
    message.error('加载 AI 账户失败')
  }
  if (groupResult.status === 'fulfilled') {
    groups.value = groupResult.value
  } else {
    console.error(groupResult.reason)
    message.error('加载分组失败')
  }
}

async function loadData() {
  loading.value = true
  try {
    const params = {
      resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
      resourceId: filters.resourceType === 'all' ? undefined : filters.resourceId,
      teamId: filters.teamId,
      granteeSystemAccountId: filters.granteeSystemAccountId,
      statMonth: filters.statMonth
    }
    const [usageOverview] = await Promise.all([
      isManagementView.value ? api.authorizations.userUsage(params) : api.myAuthorizations.userUsage(params),
      loadOptions()
    ])
    overview.value = usageOverview
  } catch (error) {
    console.error(error)
    message.error('加载用户消耗明细失败')
  } finally {
    loading.value = false
  }
}

function handleResourceTypeChange() {
  filters.resourceId = undefined
  void loadData()
}

function resetFilters() {
  Object.assign(filters, defaultFilters())
  void loadData()
}

function applyRouteFilters() {
  const teamId = singleQueryValue(route.query.teamId)
  const granteeSystemAccountId = singleQueryValue(route.query.granteeSystemAccountId)
  const resourceId = singleQueryValue(route.query.resourceId)
  const resourceType = route.query.resourceType === 'account' || route.query.resourceType === 'group' ? route.query.resourceType : undefined
  const statMonth = singleQueryValue(route.query.statMonth)
  if (teamId) filters.teamId = teamId
  if (granteeSystemAccountId) filters.granteeSystemAccountId = granteeSystemAccountId
  if (resourceType) filters.resourceType = resourceType
  if (resourceType && resourceId) filters.resourceId = resourceId
  if (isMonthKey(statMonth)) {
    usageMonthValue.value = dayjs(`${statMonth}-01`).startOf('month')
  }
}

function singleQueryValue(value: unknown): string | undefined {
  if (Array.isArray(value)) return typeof value[0] === 'string' ? value[0] : undefined
  return typeof value === 'string' ? value : undefined
}

function handleUsageMonthChange() {
  usageMonthValue.value = usageMonthValue.value
  void loadData()
}

function normalizeUsageMonth(value: Dayjs): string {
  const today = dayjs().startOf('month')
  const month = value && value.isValid() ? value.startOf('month') : today
  return month.isAfter(today, 'month') ? today.format('YYYY-MM') : month.format('YYYY-MM')
}

function defaultUsageMonth(): string {
  return dayjs().format('YYYY-MM')
}

function defaultFilters(): UserUsageFilters {
  return {
    resourceType: 'all',
    resourceId: undefined,
    teamId: undefined,
    granteeSystemAccountId: undefined,
    statMonth: defaultUsageMonth()
  }
}

function isMonthKey(value?: string): value is string {
  return Boolean(value && /^\d{4}-\d{2}$/.test(value))
}

function formatMonthLabel(value: string): string {
  const parsed = dayjs(value)
  return parsed.isValid() ? parsed.format('YYYY年M月') : value
}

onMounted(() => {
  applyRouteFilters()
  void loadData()
})
</script>

<style scoped>
.authorization-usage-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.authorization-usage-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.authorization-usage-range {
  width: 250px;
}

.authorization-usage-select {
  min-width: 180px;
}

.authorization-usage-resource {
  min-width: 220px;
}

.authorization-usage-table-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.authorization-usage-table-head {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
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

.authorization-usage-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
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

.authorization-usage-tags {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
}

.authorization-usage-tags :deep(.ant-tag) {
  margin-inline-end: 0;
}

.authorization-usage-number {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
}

.authorization-usage-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}
</style>
