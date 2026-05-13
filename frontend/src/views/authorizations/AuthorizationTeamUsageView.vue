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
            <span>用量时间</span>
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
        <h3>团队消耗明细</h3>
      </div>
      <ResponsiveDataList
        table-class="page-table authorization-usage-table"
        :columns="columns"
        :data-source="teamRows"
        row-key="id"
        :loading="loading"
        :pagination="false"
        :scroll-x="980"
        pull-refresh-enabled
        :refreshing="loading"
        @mobile-refresh="loadData"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" description="当前筛选范围暂无团队授权消耗。" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'team'">
            <div class="authorization-usage-name-cell">
              <span class="authorization-usage-name">{{ record.teamName }}</span>
              <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
            </div>
          </template>
          <template v-else-if="column.key === 'usage'">
            <UsageSummaryTags :usage="record.usage" />
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
              <div class="mobile-list-card-tags">
                <a-tag :color="record.status === 'active' ? 'green' : 'default'">{{ record.status === 'active' ? '启用' : '停用' }}</a-tag>
              </div>
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
            <RowActions variant="button" :actions="detailActions" @action-click="handleTeamAction($event, record)" />
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
import { useRouter } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import type { AccountSummary, AuthorizationTeamUsageOverview, AuthorizationTeamUsageRow, GroupSummary, SystemTeamSummary } from '@/types/domain'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import {
  emptyUsageSummary,
  formatCost,
  formatDateTime,
  formatNumber,
  formatUsageAmount
} from './authorizationFormatters'
import { authorizationResourceTypeOptions, type AuthorizationFilterResourceType } from './authorizationTableColumns'

type TeamUsageFilters = {
  teamId?: string
  resourceType: AuthorizationFilterResourceType
  resourceId?: string
  statMonth: string
}
const router = useRouter()
const loading = ref(false)
const overview = ref<AuthorizationTeamUsageOverview>()
const teams = ref<SystemTeamSummary[]>([])
const accounts = ref<AccountSummary[]>([])
const groups = ref<GroupSummary[]>([])

const filters = reactive<TeamUsageFilters>(defaultFilters())
const resourceTypeOptions = authorizationResourceTypeOptions
const detailActions: RowActionItem[] = [
  { key: 'users', label: '查询用户明细', icon: 'detail', tone: 'info' }
]
const columns = [
  { title: '授权团队', key: 'team', width: 240 },
  { title: '月度消耗', key: 'usage', width: 220 },
  { title: '最后使用', key: 'lastUsedAt', width: 180 },
  { title: '操作', key: 'actions', width: 96, fixed: 'right' }
]

const initialLoading = computed(() => loading.value && !overview.value)
const defaultMonth = computed(() => defaultUsageMonth())
const activeFilterCount = computed(() => {
  let count = 0
  if (filters.teamId) count += 1
  if (filters.resourceType !== 'all') count += 1
  if (filters.resourceId) count += 1
  if (filters.statMonth !== defaultMonth.value) count += 1
  return count
})
const resourceOptions = computed(() => {
  if (filters.resourceType === 'all') return []
  if (filters.resourceType === 'account') {
    return accounts.value.map((account) => ({ label: account.name, value: account.id }))
  }
  return groups.value.map((group) => ({ label: group.name, value: group.id }))
})
const teamRows = computed<AuthorizationTeamUsageRow[]>(() => overview.value?.rows ?? [])
const totalUsage = computed(() => overview.value?.summary ?? emptyUsageSummary())
const rangeLabel = computed(() => formatMonthLabel(filters.statMonth))
const summaryCards = computed(() => [
  { key: 'teams', label: '授权团队', value: formatNumber(overview.value?.teamCount ?? 0), extra: `月份 ${rangeLabel.value}` },
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
  const [teamResult, accountResult, groupResult] = await Promise.allSettled([
    api.systemTeams.list(),
    api.accounts.list({ limit: 500 }),
    api.groups.list()
  ])
  if (teamResult.status === 'fulfilled') {
    teams.value = teamResult.value
  } else {
    console.error(teamResult.reason)
    message.error('加载授权团队失败')
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
    const [usageOverview] = await Promise.all([
      api.authorizations.teamUsage({
        resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
        resourceId: filters.resourceType === 'all' ? undefined : filters.resourceId,
        teamId: filters.teamId,
        statMonth: filters.statMonth
      }),
      loadOptions()
    ])
    overview.value = usageOverview
  } catch (error) {
    console.error(error)
    message.error('加载团队消耗明细失败')
  } finally {
    loading.value = false
  }
}

function handleTeamAction(key: string, row: AuthorizationTeamUsageRow) {
  if (key !== 'users') return
  void router.push({
    path: '/authorization-user-usage',
    query: {
      teamId: row.id,
      statMonth: filters.statMonth,
      resourceType: filters.resourceType === 'all' ? undefined : filters.resourceType,
      resourceId: filters.resourceType === 'all' ? undefined : filters.resourceId
    }
  })
}

function handleResourceTypeChange() {
  filters.resourceId = undefined
  void loadData()
}

function resetFilters() {
  Object.assign(filters, defaultFilters())
  void loadData()
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

function defaultFilters(): TeamUsageFilters {
  return {
    resourceType: 'all',
    resourceId: undefined,
    teamId: undefined,
    statMonth: defaultUsageMonth()
  }
}

function formatMonthLabel(value: string): string {
  const parsed = dayjs(value)
  return parsed.isValid() ? parsed.format('YYYY年M月') : value
}

onMounted(loadData)
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

.authorization-usage-name-cell {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  gap: 8px;
}

.authorization-usage-name {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
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
