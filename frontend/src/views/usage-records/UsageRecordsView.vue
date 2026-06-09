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

    <ResponsiveDataList
      table-class="page-table usage-table"
      :columns="managedColumns"
      :data-source="filteredRecords"
      :mobile-data-source="mobileRecords"
      row-key="id"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="tablePagination"
      :scroll-x="isManagementView ? 2460 : 2280"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前条件下没有使用记录。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'traceId'">
          <div class="trace-id-cell">
            <span class="trace-id-text">{{ record.traceId }}</span>
            <span class="trace-id-actions">
              <a-tooltip title="复制 traceId">
                <a-button size="small" type="text" @click.stop="copyTraceId(record.traceId)">
                  <template #icon><copy-outlined /></template>
                </a-button>
              </a-tooltip>
              <a-tooltip v-if="isManagementView" title="查看运行日志">
                <a-button size="small" type="text" @click.stop="openTraceTarget(record.traceId, 'runtime')">
                  <template #icon><search-outlined /></template>
                </a-button>
              </a-tooltip>
              <a-tooltip v-if="isManagementView" title="查看审计日志">
                <a-button size="small" type="text" @click.stop="openTraceTarget(record.traceId, 'audit')">
                  <template #icon><file-search-outlined /></template>
                </a-button>
              </a-tooltip>
            </span>
          </div>
        </template>
        <template v-else-if="column.key === 'apiKey'">
          <span :class="record.apiKeyName ? 'name-cell' : 'muted-cell'">{{ displayName(record.apiKeyName, record.apiKeyId) }}</span>
        </template>
        <template v-else-if="column.key === 'group'">
          <span :class="record.groupName ? 'name-cell' : 'muted-cell'">{{ displayUsageRecordGroupName(record.groupName, record.groupId) }}</span>
        </template>
        <template v-else-if="column.key === 'account'">
          <span :class="record.accountName || record.accountId ? 'name-cell' : 'muted-cell'">{{ accountDisplayText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">
            {{ usageRecordSystemAccountText(record) }}
          </span>
        </template>
        <template v-else-if="column.key === 'clientIp'">
          <span :class="record.clientIp ? 'ip-cell' : 'muted-cell'">{{ record.clientIp ?? '-' }}</span>
        </template>
        <template v-else-if="column.key === 'endpoint'">
          <span :class="record.endpoint ? 'endpoint-cell' : 'muted-cell'">{{ formatEndpoint(record.endpoint) }}</span>
        </template>
        <template v-else-if="column.key === 'model'">
          <span v-if="record.model" class="model-cell">
            <a-tag color="blue">{{ record.model }}</a-tag>
            <a-tag v-if="record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
          </span>
          <span v-else class="muted-cell">-</span>
        </template>
        <template v-else-if="column.key === 'stream'">
          <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'statusCode'">
          <a-tag :color="statusCodeColor(record)">{{ statusCodeText(record) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'success'">
          <UsageRecordResultCell :record="record" />
        </template>
        <template v-else-if="column.key === 'trafficSource'">
          <a-tag :color="trafficSourceColor(record)">{{ trafficSourceText(record) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'tokens'">
          <div class="token-cell">
            <span>输入 {{ formatTokens(record.inputTokens) }}</span>
            <span>输出 {{ formatTokens(record.outputTokens) }}</span>
            <span>缓存 {{ formatTokens(record.cacheReadTokens) }}</span>
            <span v-if="(record.inputImageTokens ?? 0) + (record.outputImageTokens ?? 0) > 0">
              图片 {{ formatTokens((record.inputImageTokens ?? 0) + (record.outputImageTokens ?? 0)) }}
            </span>
          </div>
        </template>
        <template v-else-if="column.key === 'cost'">
          <UsageRecordCostCell :record="record" />
        </template>
        <template v-else-if="column.key === 'firstTokenMs'">
          <span>{{ formatDuration(record.firstTokenMs) }}</span>
        </template>
        <template v-else-if="column.key === 'durationMs'">
          <span>{{ formatDuration(record.durationMs) }}</span>
        </template>
        <template v-else-if="column.key === 'createdAt'">
          <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
        </template>
      </template>
      <template #card="{ record }">
        <UsageRecordMobileCard
          :is-management-view="isManagementView"
          :record="record"
          @copy-trace-id="copyTraceId"
          @open-audit-logs="openTraceTarget(record.traceId, 'audit')"
          @open-runtime-logs="openTraceTarget(record.traceId, 'runtime')"
        />
      </template>
    </ResponsiveDataList>
  </a-card>
</template>

<script setup lang="ts">
import { CopyOutlined, FileSearchOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import type { Dayjs } from 'dayjs'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { api, type UsageRecordListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedGroupsApi, useScopedUsageRecordsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatDateKey, normalizeDayjsDateRange, parseDateKey } from '@/shared/dateRange'
import { rememberGroupLabel, rememberGroupLabels, rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { removeRouteTraceIdQuery, trimmedRouteQueryValue } from '@/shared/routeQuery'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import {
  localSelectStorageKey,
  readLocalSelectOptionWindow,
  removeLocalSelectOptionWindowValues,
  removeLocalSelectPreferenceValues,
  writeLocalSelectOptionWindow
} from '@/shared/selectLocalPreferenceCache'
import type { GroupOptionSummary, ProviderModelOption, UsageRecordSummary, UsageRecordTrafficSource } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import UsageRecordCostCell from './UsageRecordCostCell.vue'
import UsageRecordMobileCard from './UsageRecordMobileCard.vue'
import UsageRecordResultCell from './UsageRecordResultCell.vue'
import UsageRecordsFilterToolbar from './UsageRecordsFilterToolbar.vue'
import {
  accountDisplayText,
  displayName,
  displayUsageRecordGroupName,
  formatDateTime,
  formatDuration,
  formatEndpoint,
  formatTokens,
  statusCodeColor,
  statusCodeText,
  trafficSourceColor,
  trafficSourceText,
  usageRecordSystemAccountText
} from './usageRecordFormatters'

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
const groups = ref<GroupOptionSummary[]>([])
const groupOptionsLoading = ref(false)
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
let groupOptionsRequestId = 0
let groupOptionsLoadingKey: string | undefined
let groupOptionsLoadingPromise: Promise<void> | undefined
let groupOptionsKeyword = ''
let groupOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let modelOptionsLoaded = false
let modelOptionsLoadingPromise: Promise<void> | undefined
let skipNextRouteTraceRestore = false
const groupOptionsCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: 10_000 })
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
    message.error('加载使用记录失败')
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
    { title: 'Tokens', key: 'tokens', width: 150 },
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

function selectedGroupSelection(id: string | undefined): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = groups.value.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  if (groupFilterSelection.value?.id === normalizedId) return groupFilterSelection.value
  return undefined
}

async function loadGroupOptions(keyword = groupOptionsKeyword, force = false): Promise<void> {
  groupOptionsKeyword = keyword
  const systemAccountId = isManagementView.value ? scopedSystemAccountId(systemAccountFilter.value) : undefined
  const requestKeyword = normalizeOptionKeyword(keyword)
  const requestKey = JSON.stringify([
    isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self',
    requestKeyword ?? '',
    groupFilter.value ?? ''
  ])
  if (!force && groupOptionsLoadingKey === requestKey && groupOptionsLoadingPromise) {
    return groupOptionsLoadingPromise
  }
  const requestId = ++groupOptionsRequestId
  const optionWindowKey = groupOptionWindowKey(systemAccountId, requestKeyword)
  const localWindowGroups = !force ? readLocalSelectOptionWindow<GroupOptionSummary>(optionWindowKey) : undefined
  if (localWindowGroups?.length) {
    groupOptionsLoading.value = false
    rememberGroupLabels(localWindowGroups)
    syncSelectedGroupSelection(localWindowGroups)
    groups.value = localWindowGroups
  }
  if (!force) {
    const cachedGroups = groupOptionsCache.get(requestKey)
    if (cachedGroups) {
      groupOptionsLoadingKey = undefined
      groupOptionsLoadingPromise = undefined
      groupOptionsLoading.value = false
      rememberGroupLabels(cachedGroups)
      syncSelectedGroupSelection(cachedGroups)
      writeLocalSelectOptionWindow(optionWindowKey, cachedGroups)
      groups.value = cachedGroups
      return
    }
  }
  groupOptionsLoading.value = !localWindowGroups?.length
  groupOptionsLoadingKey = requestKey
  groupOptionsLoadingPromise = (async () => {
    try {
      let nextGroups = await groupsApi.options({ systemAccountId, keyword: requestKeyword, limit: 50 })
      nextGroups = await ensureSelectedGroupOptions(nextGroups, systemAccountId, optionWindowKey)
      rememberGroupLabels(nextGroups)
      syncSelectedGroupSelection(nextGroups)
      groupOptionsCache.set(requestKey, nextGroups)
      writeLocalSelectOptionWindow(optionWindowKey, nextGroups)
      if (requestId !== groupOptionsRequestId) return
      groups.value = nextGroups
    } catch (error) {
      if (requestId !== groupOptionsRequestId) return
      console.error(error)
      message.error('加载分组选项失败')
    } finally {
      if (groupOptionsLoadingKey === requestKey) {
        groupOptionsLoadingKey = undefined
        groupOptionsLoadingPromise = undefined
      }
      if (requestId === groupOptionsRequestId) {
        groupOptionsLoading.value = false
      }
    }
  })()
  return groupOptionsLoadingPromise
}

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

function handleGroupOptionsDropdown(open: boolean): void {
  if (open) {
    void loadGroupOptions()
  }
}

function handleGroupOptionsSearch(value: string): void {
  groupOptionsKeyword = value
  clearGroupOptionsSearchTimer()
  groupOptionsSearchTimer = window.setTimeout(() => {
    groupOptionsSearchTimer = undefined
    void loadGroupOptions(groupOptionsKeyword)
  }, 250)
}

function resetGroupOptionsSearch(): void {
  groupOptionsKeyword = ''
  clearGroupOptionsSearchTimer()
}

function clearGroupOptionsSearchTimer(): void {
  if (groupOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(groupOptionsSearchTimer)
    groupOptionsSearchTimer = undefined
  }
}

async function ensureSelectedGroupOptions(nextGroups: GroupOptionSummary[], systemAccountId: string | undefined, optionWindowKey: string): Promise<GroupOptionSummary[]> {
  const selectedIds = [groupFilter.value].filter((id): id is string => Boolean(id))
  const missingIds = [...new Set(selectedIds)].filter((id) => !nextGroups.some((group) => group.id === id))
  if (!missingIds.length) return nextGroups
  const selectedGroups = await Promise.all(missingIds.map(async (id) => {
    try {
      return await groupsApi.options({ systemAccountId, ids: [id], limit: 1 })
    } catch {
      return []
    }
  }))
  const foundIds = new Set(selectedGroups.flat().map((group) => group.id))
  handleMissingGroupOptions(missingIds.filter((id) => !foundIds.has(id)), optionWindowKey)
  return mergeOptionsById(selectedGroups.flat(), nextGroups)
}

function handleMissingGroupOptions(ids: string[], optionWindowKey: string): void {
  const missingIds = [...new Set(ids.map((id) => id.trim()).filter(Boolean))]
  if (!missingIds.length) return
  removeLocalSelectOptionWindowValues(optionWindowKey, missingIds)
  removeLocalSelectPreferenceValues('groups', missingIds)
  if (groupFilter.value && missingIds.includes(groupFilter.value)) {
    groupFilterSelection.value = undefined
    resetPagination()
    void loadData({ forceOptions: true })
  }
  message.warning('已移除不存在或无权访问的分组，请重新选择')
}

function groupOptionWindowKey(systemAccountId: string | undefined, requestKeyword: string | undefined): string {
  return localSelectStorageKey([
    'group-options',
    isManagementView.value ? 'management' : 'self',
    systemAccountId ?? 'all',
    'usage-records',
    requestKeyword ?? ''
  ])
}

function syncSelectedGroupSelection(nextGroups = groups.value): void {
  if (!groupFilter.value) return
  groupFilterSelection.value = selectedGroupFromOptions(groupFilter.value, nextGroups, groupFilterSelection.value)
}

function selectedGroupFromOptions(id: string | undefined, nextGroups: GroupOptionSummary[], fallback?: GroupSelection): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = nextGroups.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  return fallback?.id === normalizedId ? fallback : undefined
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

function normalizeOptionKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
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

<style scoped>
.usage-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.usage-table :deep(.ant-empty) {
  margin: 12px 0;
}

.token-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  color: #475569;
  font-size: 12px;
  line-height: 1.3;
}

.model-cell {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 260px;
  vertical-align: bottom;
}

.trace-id-cell {
  display: inline-flex;
  align-items: center;
  max-width: 290px;
  gap: 4px;
  vertical-align: bottom;
}

.trace-id-actions {
  display: inline-flex;
  flex: none;
  gap: 2px;
}

.trace-id-text {
  min-width: 0;
  overflow: hidden;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.name-cell {
  display: inline-block;
  max-width: 160px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.ip-cell {
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.endpoint-cell {
  display: inline-block;
  max-width: 140px;
  overflow: hidden;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

</style>




