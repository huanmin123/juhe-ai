<template>
  <a-card class="page-card usage-stats-page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="filters.keyword"
      search-placeholder="搜索AI账户..."
      filter-title="筛选用量"
      :active-filter-count="activeFilterCount"
      :refresh-loading="loading"
      @search="applyFilters"
      @reset="resetFilters"
      @refresh="loadData"
    >
      <template #inline-filters>
        <a-select v-model:value="filters.type" class="toolbar-select responsive-list-inline-filter" :options="typeOptions" @change="applyFilters" />
        <SystemPrincipalSelect v-if="isManagementView" v-model:value="filters.systemAccountId" :accounts="systemAccounts" :active-only="false" include-all class="toolbar-select responsive-list-inline-filter" @change="handleSystemAccountFilterChange" />
      </template>
      <template #filters>
        <label class="mobile-filter-field">
          <span>账户类型</span>
          <a-select v-model:value="filters.type" :options="typeOptions" @change="applyFilters" />
        </label>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect v-model:value="filters.systemAccountId" :accounts="systemAccounts" :active-only="false" include-all @change="handleSystemAccountFilterChange" />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      class="usage-stats-responsive-list"
      table-class="usage-stats-table"
      :columns="columns"
      :data-source="rows"
      :mobile-data-source="rows"
      row-key="id"
      :loading="loading"
      :scroll-x="tableScrollX"
      :table-scroll-y="tableScrollY"
      :pagination="tablePagination"
      mobile-pagination
      pull-refresh-enabled
      :mobile-has-more="mobileHasMore"
      :loading-more="mobileLoadingMore"
      :refreshing="mobileRefreshing"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileRows"
      @mobile-refresh="refreshMobileRows"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无账户用量统计，等待后台聚合后会显示结果。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="usage-account-cell">
            <span class="usage-account-name-row">
              <span class="usage-account-name">{{ record.name }}</span>
              <a-tag v-if="record.accessType === 'authorized'" color="blue">来自授权</a-tag>
            </span>
          </div>
        </template>
        <template v-else-if="column.key === 'type'">
          <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ record.systemAccountName || record.systemAccountId || '-' }}</span>
        </template>
        <template v-else-if="isWindowColumn(column.key)">
          <UsageStatCell :usage="record.usageByWindow[column.key]" />
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-button v-if="record.authorizationUsageAvailable" type="link" size="small" @click="openAuthorizationUsage(record)">授权用量</a-button>
          <span v-else class="muted-cell">-</span>
        </template>
      </template>
      <template #card="{ record }">
        <article class="usage-mobile-card">
          <div class="usage-mobile-head">
            <div>
              <div class="usage-mobile-title">{{ record.name }}</div>
              <div class="usage-mobile-subtitle">
                <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
                <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
                <a-tag v-if="record.accessType === 'authorized'" color="blue">来自授权</a-tag>
              </div>
            </div>
            <a-button v-if="record.authorizationUsageAvailable" size="small" @click="openAuthorizationUsage(record)">授权用量</a-button>
          </div>
          <div class="usage-mobile-grid">
            <div v-for="window in compactWindows" :key="window.key" class="usage-mobile-metric">
              <span>{{ window.label }}</span>
              <strong>{{ formatUsageBrief(record.usageByWindow[window.key]) }}</strong>
            </div>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <AuthorizationUsageModal
      v-model:open="authorizationUsageOpen"
      :loading="authorizationUsageLoading"
      :overview="authorizationUsageOverview"
      @close="handleAuthorizationUsageClosed"
    />
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import type {
  AccountAuthorizationUsageOverview,
  AccountType,
  AccountUsageStatsOverview,
  AccountUsageStatsRow,
  ProviderDefinition,
  SystemAccountSummary,
  UsageStatsWindowKey
} from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import AuthorizationUsageModal from './AuthorizationUsageModal.vue'
import UsageStatCell from './UsageStatCell.vue'
import { defaultUsageWindows, displayWindowKeys, formatUsageBrief, isUsageWindowColumn } from './usageStatsFormatters'

interface UsageStatsFilters {
  keyword: string
  type: 'all' | AccountType
  systemAccountId: string
}

type UsageStatsPageState = {
  filters: UsageStatsFilters
  pagination: { current: number; pageSize: number }
}

const FALLBACK_PROVIDER: ProviderDefinition = {
  id: 'openai',
  code: 'openai',
  name: 'OpenAI',
  enabled: true,
  baseUrl: 'https://api.openai.com/v1',
  accountTypes: ['oauth', 'api_key'],
  capabilities: ['models', 'responses', 'stream', 'passthrough']
}

const loading = ref(false)
const mobileLoadingMore = ref(false)
const mobileRefreshing = ref(false)
const overview = ref<AccountUsageStatsOverview>()
const authorizationUsageOpen = ref(false)
const authorizationUsageLoading = ref(false)
const authorizationUsageOverview = ref<AccountAuthorizationUsageOverview>()
const authorizationUsageAccountId = ref<string>()
const systemAccounts = ref<SystemAccountSummary[]>([])
const providers = ref<ProviderDefinition[]>([])
const routeAuthorizationUsageHandled = ref(false)
const pageSize = 20
const defaultUsageStatsPageState = (): UsageStatsPageState => ({
  filters: { keyword: '', type: 'all', systemAccountId: allSystemAccountsValue },
  pagination: { current: 1, pageSize }
})
const pageStateCache = usePageStateCache<UsageStatsPageState>(undefined, defaultUsageStatsPageState)
const initialPageState = pageStateCache.read()
const filters = reactive<UsageStatsFilters>({ ...initialPageState.filters })
const pagination = reactive({ current: initialPageState.pagination.current, pageSize: initialPageState.pagination.pageSize, total: 0 })
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const route = useRoute()
const router = useRouter()

const typeOptions = [
  { label: '全部类型', value: 'all' },
  { label: 'OAuth', value: 'oauth' },
  { label: 'API Key', value: 'api_key' }
]

const availableProviders = computed(() => providers.value.length ? providers.value : [FALLBACK_PROVIDER])
const windows = computed(() => overview.value?.windows ?? defaultUsageWindows())
const compactWindows = computed(() => windows.value.filter((window) => displayWindowKeys.includes(window.key)))
const rows = computed(() => overview.value?.rows ?? [])

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: 'AI账户名称', dataIndex: 'name', key: 'name', width: 230 },
    { title: '账户类型', dataIndex: 'type', key: 'type', width: 120 },
    { title: '供应商', dataIndex: 'providerCode', key: 'providerCode', width: 110 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 170 })
  }
  for (const window of compactWindows.value) {
    baseColumns.push({ title: window.label, key: window.key, width: 180 })
  }
  baseColumns.push({ title: '操作', key: 'actions', width: 120, fixed: 'right' })
  return baseColumns
})

const tableScrollX = computed(() => isManagementView.value ? 1670 : 1500)
const tableScrollY = computed(() => 'calc(100dvh - 286px)')
const mobileHasMore = computed(() => rows.value.length < pagination.total)
const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  hideOnSinglePage: true,
  showSizeChanger: false,
  showTotal: (total: number) => `共 ${total} 个账户`
}))
const activeFilterCount = computed(() => [
  filters.keyword.trim(),
  filters.type !== 'all',
  isManagementView.value && filters.systemAccountId !== allSystemAccountsValue
].filter(Boolean).length)

async function loadData(options: { append?: boolean; quiet?: boolean } = {}) {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const systemAccountId = isManagementView.value ? scopedSystemAccountId(filters.systemAccountId) : undefined
    const [usageOverview, providerList, systemAccountList] = await Promise.all([
      isManagementView.value ? api.stats.accountUsage(accountUsageParams(systemAccountId)) : api.myStats.accountUsage(accountUsageParams()),
      isManagementView.value ? api.providers.list() : Promise.resolve([] as ProviderDefinition[]),
      isManagementView.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
    ])
    overview.value = {
      ...usageOverview,
      rows: options.append ? [...rows.value, ...usageOverview.rows] : usageOverview.rows
    }
    pagination.current = usageOverview.page
    pagination.pageSize = usageOverview.pageSize
    pagination.total = usageOverview.total
    providers.value = providerList.length ? providerList : [FALLBACK_PROVIDER]
    systemAccounts.value = systemAccountList
    await openAuthorizationUsageFromRoute()
  } catch (error) {
    console.error(error)
    message.error('用量统计加载失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
  }
}

async function openAuthorizationUsageFromRoute() {
  if (routeAuthorizationUsageHandled.value) return
  if (route.query.action !== 'authorization-usage' || typeof route.query.accountId !== 'string') return
  const systemAccountId = typeof route.query.systemAccountId === 'string' ? route.query.systemAccountId : undefined
  if (isManagementView.value && systemAccountId && filters.systemAccountId !== systemAccountId) {
    filters.systemAccountId = systemAccountId
  }
  routeAuthorizationUsageHandled.value = true
  authorizationUsageAccountId.value = route.query.accountId
  authorizationUsageOpen.value = true
  authorizationUsageOverview.value = undefined
  await reloadAuthorizationUsage()
}

function applyFilters() {
  pagination.current = 1
  void loadData()
}

function resetFilters() {
  const defaults = defaultUsageStatsPageState()
  Object.assign(filters, defaults.filters)
  pagination.current = defaults.pagination.current
  pagination.pageSize = defaults.pagination.pageSize
  pageStateCache.clear()
  void loadData()
}

function handleSystemAccountFilterChange() {
  pagination.current = 1
  void loadData()
}

function handleTableChange(paginationInfo: unknown) {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const current = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(current) && current > 0 ? current : 1
  pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : pageSize
  void loadData()
}

async function loadMoreMobileRows() {
  if (!mobileHasMore.value || mobileLoadingMore.value) return
  mobileLoadingMore.value = true
  pagination.current += 1
  try {
    await loadData({ append: true, quiet: true })
  } finally {
    mobileLoadingMore.value = false
  }
}

async function refreshMobileRows() {
  mobileRefreshing.value = true
  pagination.current = 1
  try {
    await loadData()
  } finally {
    mobileRefreshing.value = false
  }
}

function accountUsageParams(systemAccountId?: string) {
  return {
    systemAccountId,
    page: pagination.current,
    pageSize: pagination.pageSize,
    keyword: filters.keyword.trim() || undefined,
    type: filters.type === 'all' ? undefined : filters.type
  }
}

async function openAuthorizationUsage(row: AccountUsageStatsRow, keepRouteQuery = true) {
  authorizationUsageAccountId.value = row.id
  authorizationUsageOpen.value = true
  authorizationUsageOverview.value = undefined
  await reloadAuthorizationUsage()
  if (keepRouteQuery) {
    await router.replace({ path: route.path, query: { ...route.query, accountId: row.id, action: 'authorization-usage' } })
  }
}

async function reloadAuthorizationUsage() {
  if (!authorizationUsageAccountId.value) return
  authorizationUsageLoading.value = true
  try {
    authorizationUsageOverview.value = isManagementView.value
      ? await api.stats.accountAuthorizationUsage(authorizationUsageAccountId.value, { systemAccountId: scopedSystemAccountId(filters.systemAccountId) })
      : await api.myStats.accountAuthorizationUsage(authorizationUsageAccountId.value)
  } catch (error) {
    console.error(error)
    message.error('授权用量加载失败')
  } finally {
    authorizationUsageLoading.value = false
  }
}

async function handleAuthorizationUsageClosed() {
  authorizationUsageOpen.value = false
  authorizationUsageAccountId.value = undefined
  const { accountId: _accountId, action: _action, ...query } = route.query
  if (_accountId || _action) {
    await router.replace({ path: route.path, query })
  }
}

function isWindowColumn(value: unknown): value is UsageStatsWindowKey {
  return isUsageWindowColumn(value)
}

function accountTypeText(type: AccountType) {
  if (type === 'oauth') return 'OAuth'
  if (type === 'api_key') return 'API Key'
  return type || '-'
}

function providerName(providerCode?: string) {
  if (!providerCode) return '未知供应商'
  return availableProviders.value.find((provider) => provider.code === providerCode)?.name ?? providerCode
}

function snapshotPageState(): UsageStatsPageState {
  return {
    filters: { ...filters },
    pagination: { current: pagination.current, pageSize: pagination.pageSize }
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

onMounted(loadData)
</script>

<style scoped>
.usage-stats-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.usage-account-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 6px;
}

.usage-account-name-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.usage-account-name {
  display: inline-block;
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 400;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-mobile-card {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 14px;
  border: 1px solid #e8edf5;
  border-radius: 14px;
  background: #fff;
}

.usage-mobile-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
}

.usage-mobile-title {
  color: #0f172a;
  font-weight: 700;
}

.usage-mobile-subtitle {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 4px;
  color: #64748b;
  font-size: 12px;
}

.usage-mobile-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.usage-mobile-metric {
  min-width: 0;
  padding: 10px;
  border: 1px solid #eef2f7;
  border-radius: 10px;
  background: #f8fafc;
}

.usage-mobile-metric span {
  display: block;
  color: #64748b;
  font-size: 12px;
}

.usage-mobile-metric strong {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  color: #0f172a;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 768px) {
  .usage-mobile-grid {
    grid-template-columns: 1fr;
  }
}
</style>
