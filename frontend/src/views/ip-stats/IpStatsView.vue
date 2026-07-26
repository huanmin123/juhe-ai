<template>
  <a-card class="page-card responsive-page-card ip-stats-page-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索 IP"
      filter-title="IP 管理筛选"
      :active-filter-count="activeFilterCount"
      :refresh-loading="loading"
      @search="applyFilters"
      @reset="resetFilters"
      @refresh="() => loadData({ force: true })"
    >
      <template #inline-filters>
        <a-segmented
          v-model:value="usageWindow"
          :disabled="loading"
          :options="usageWindowOptions"
          class="ip-stats-usage-window responsive-list-inline-filter"
          @change="applyFilters"
        />
        <a-select
          v-model:value="statusFilter"
          class="toolbar-select ip-stats-status responsive-list-inline-filter"
          :disabled="loading"
          :options="statusOptions"
          @change="applyFilters"
        />
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="统计范围">
            <a-segmented
              v-model:value="usageWindow"
              :disabled="loading"
              :options="usageWindowOptions"
              block
              @change="applyFilters"
            />
          </a-form-item>
          <a-form-item label="状态">
            <a-select
              v-model:value="statusFilter"
              :disabled="loading"
              :options="statusOptions"
              @change="applyFilters"
            />
          </a-form-item>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <a-alert
      v-if="!rangeReady"
      class="ip-stats-range-alert"
      type="warning"
      show-icon
      :message="`${currentUsageWindowLabel}用量窗口尚未完成预聚合，请稍后刷新。`"
    />

    <IpStatsList
      :empty-description="emptyDescription"
      :loading="loading"
      :rows="rows"
      :table-pagination="tablePagination"
      @change="handleTableChange"
      @detail="openDetailDrawer"
      @policy-action="handlePolicyAction"
    />

    <a-modal
      v-model:open="policyModalOpen"
      :title="policyModalTitle"
      ok-text="提交"
      cancel-text="取消"
      :confirm-loading="policySubmitting"
      @ok="submitPolicy"
    >
      <a-form layout="vertical">
        <a-form-item label="IP">
          <a-input :value="policyTarget?.aggregateIpKey" disabled />
        </a-form-item>
        <a-form-item v-if="policyAction === 'blacklist'" :label="policyReasonLabel">
          <a-textarea v-model:value="policyForm.reason" :rows="3" :maxlength="500" show-count />
        </a-form-item>
        <a-form-item v-if="policyAction === 'blacklist'" label="封禁时长">
          <a-segmented
            v-model:value="policyForm.durationMode"
            :options="policyDurationOptions"
            block
            @change="handlePolicyDurationModeChange"
          />
        </a-form-item>
        <a-form-item v-if="policyAction === 'blacklist' && policyForm.durationMode === 'minutes'" label="分钟数">
          <a-input-number
            v-model:value="policyForm.durationValue"
            class="policy-duration-input"
            :min="1"
            :max="525600"
            :precision="0"
            addon-after="分钟"
          />
        </a-form-item>
        <a-form-item v-if="policyAction === 'blacklist' && policyForm.durationMode === 'days'" label="天数">
          <a-input-number
            v-model:value="policyForm.durationValue"
            class="policy-duration-input"
            :min="1"
            :max="3650"
            :precision="0"
            addon-after="天"
          />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-drawer
      v-model:open="detailDrawerOpen"
      class="ip-detail-drawer"
      :title="detailDrawerTitle"
      :width="1040"
      destroy-on-close
    >
      <a-descriptions v-if="detailTarget" class="ip-detail-summary" size="small" bordered :column="{ xs: 1, sm: 2 }">
        <a-descriptions-item label="IP">
          <span class="mono-cell">{{ detailTarget.aggregateIpKey }}</span>
        </a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag :color="statusColor(detailTarget.status)">{{ statusText(detailTarget.status) }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="统计范围">{{ currentUsageWindowLabel }}</a-descriptions-item>
        <a-descriptions-item label="最近使用">{{ formatDateTime(detailTarget.lastSeenAt || detailTarget.rangeUsage.lastUsedAt) }}</a-descriptions-item>
      </a-descriptions>

      <ResponsiveDataList
        class="ip-detail-account-table"
        table-class="page-table ip-detail-account-table-inner"
        :columns="detailColumns"
        :data-source="detailRows"
        row-key="accountId"
        :loading="detailLoading"
        :pagination="detailTablePagination"
        :pagination-summary="false"
        :scroll-x="1220"
        size="small"
        :lock-body-scroll="false"
        :mobile-breakpoint="760"
        @change="handleDetailTableChange"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" :description="detailEmptyDescription" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'account'">
            <div class="ip-detail-account">
              <a-tooltip v-if="record.accountName" :title="record.accountName">
                <span class="name-cell ip-detail-account-name">{{ record.accountName }}</span>
              </a-tooltip>
              <span v-else class="muted-cell">未匹配到账户名称</span>
            </div>
          </template>
          <template v-else-if="column.key === 'accountOwner'">
            <a-tooltip v-if="record.accountOwnerSystemAccountName" :title="record.accountOwnerSystemAccountName">
              <span class="name-cell ip-detail-owner-name">{{ record.accountOwnerSystemAccountName }}</span>
            </a-tooltip>
            <span v-else class="muted-cell">未匹配到用户</span>
          </template>
          <template v-else-if="column.key === 'requestCount'">
            <span class="number-cell">{{ formatInteger(record.rangeUsage.requestCount) }}</span>
          </template>
          <template v-else-if="column.key === 'totalTokens'">
            <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</span>
          </template>
          <template v-else-if="column.key === 'cost'">
            <span class="number-cell">{{ formatCost(record.rangeUsage.totalCost) }}</span>
          </template>
          <template v-else-if="column.key === 'errorRate'">
            <a-tag :color="record.rangeUsage.errorRate > 0.05 ? 'red' : 'green'">
              {{ formatPercent(record.rangeUsage.errorRate * 100) }}
            </a-tag>
          </template>
          <template v-else-if="column.key === 'averageFirstTokenMs'">
            <span class="number-cell">{{ formatDuration(record.rangeUsage.averageFirstTokenMs) }}</span>
          </template>
          <template v-else-if="column.key === 'averageDurationMs'">
            <span class="number-cell">{{ formatDuration(record.rangeUsage.averageDurationMs) }}</span>
          </template>
          <template v-else-if="column.key === 'lastUsedAt'">
            <span :class="record.rangeUsage.lastUsedAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.rangeUsage.lastUsedAt) }}</span>
          </template>
        </template>
        <template #card="{ record }">
          <article class="ip-detail-mobile-card">
            <div class="ip-detail-mobile-head">
              <div class="ip-detail-mobile-title">
                <a-tooltip v-if="record.accountName" :title="record.accountName">
                  <span class="name-cell ip-detail-account-name">{{ record.accountName }}</span>
                </a-tooltip>
                <span v-else class="muted-cell">未匹配到账户名称</span>
                <span :class="record.accountOwnerSystemAccountName ? 'ip-detail-owner-text' : 'muted-cell'">
                  用户：{{ record.accountOwnerSystemAccountName || '未匹配到用户' }}
                </span>
              </div>
              <a-tag :color="record.rangeUsage.errorRate > 0.05 ? 'red' : 'green'">
                {{ formatPercent(record.rangeUsage.errorRate * 100) }}
              </a-tag>
            </div>
            <div class="mobile-list-meta-grid">
              <div class="mobile-list-meta-item">
                <span>请求</span>
                <strong>{{ formatInteger(record.rangeUsage.requestCount) }}</strong>
              </div>
              <div class="mobile-list-meta-item">
                <span>Token</span>
                <strong>{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</strong>
              </div>
              <div class="mobile-list-meta-item">
                <span>成本</span>
                <strong>{{ formatCost(record.rangeUsage.totalCost) }}</strong>
              </div>
              <div class="mobile-list-meta-item">
                <span>平均首 Token</span>
                <strong>{{ formatDuration(record.rangeUsage.averageFirstTokenMs) }}</strong>
              </div>
              <div class="mobile-list-meta-item">
                <span>平均总耗时</span>
                <strong>{{ formatDuration(record.rangeUsage.averageDurationMs) }}</strong>
              </div>
              <div class="mobile-list-meta-item mobile-list-meta-wide">
                <span>最后使用</span>
                <strong>{{ formatDateTime(record.rangeUsage.lastUsedAt) }}</strong>
              </div>
            </div>
          </article>
        </template>
      </ResponsiveDataList>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'

import { api, type ClientIpStatsDetailParams, type ClientIpStatsListParams, type SortDirection } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { didUsageStatsWindowLoadFail, useUsageStatsWindow } from '@/composables/useUsageStatsWindow'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import { sanitizePaginationState, stringOrFallback, stringUnionOrFallback, type PagePaginationState } from '@/shared/pageStateSanitizers'
import type { ClientIpAccountUsageRow, ClientIpStatsRow, ClientIpStatsSortField, ClientIpStatus } from '@/types/domain'
import { formatCompactInteger, formatCost, formatDuration, formatInteger, formatPercent } from '@/views/stats/statsFormatters'

import IpStatsList from './IpStatsList.vue'
import { statusColor, statusText, type IpStatsPolicyAction } from './ipStatsDisplay'

type TableSortOrder = 'ascend' | 'descend' | null
type PolicyAction = IpStatsPolicyAction
type PolicyDurationMode = 'permanent' | 'minutes' | 'days'
type UsageWindow = 'today' | 'recent7d' | 'recent1m'

interface IpStatsPageState {
  keyword: string
  pagination: PagePaginationState
  sortState: {
    field: ClientIpStatsSortField
    order: TableSortOrder
  }
  statusFilter: ClientIpStatus
  usageWindow: UsageWindow
}

const usageWindowOptions = [
  { label: '今天', value: 'today' },
  { label: '近 7 天', value: 'recent7d' },
  { label: '近1月', value: 'recent1m' }
]

const { usageStatsWindowEndDate, loadUsageStatsWindow } = useUsageStatsWindow()

const statusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '正常', value: 'normal' },
  { label: '白名单', value: 'allowlisted' },
  { label: '已封禁', value: 'blacklisted' }
]

const policyDurationOptions = [
  { label: '永久', value: 'permanent' },
  { label: '分钟', value: 'minutes' },
  { label: '天', value: 'days' }
]

const detailColumns = [
  { title: 'AI 账户', key: 'account', width: 180, fixed: 'left', align: 'left' },
  { title: '用户名称', key: 'accountOwner', width: 150, align: 'left' },
  { title: '请求', key: 'requestCount', width: 100, align: 'left', sorter: true },
  { title: 'Token', key: 'totalTokens', width: 120, align: 'left', sorter: true },
  { title: '成本', key: 'cost', width: 120, align: 'left', sorter: true },
  { title: '失败率', key: 'errorRate', width: 110, align: 'left', sorter: true },
  { title: '平均首 Token', key: 'averageFirstTokenMs', width: 130, align: 'left' },
  { title: '平均总耗时', key: 'averageDurationMs', width: 130, align: 'left' },
  { title: '最后使用', key: 'lastUsedAt', width: 180, align: 'left', sorter: true }
]

const ipStatsPageSize = 20
const pageStateCache = usePageStateCache<IpStatsPageState>(undefined, defaultIpStatsPageState, {
  sanitize: sanitizeIpStatsPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const loading = ref(false)
let listRequestSeq = 0
const keyword = ref(initialPageState.keyword)
const statusFilter = ref<ClientIpStatus>(initialPageState.statusFilter)
const usageWindow = ref<UsageWindow>(initialPageState.usageWindow)
const rows = ref<ClientIpStatsRow[]>([])
const paginationUpperBound = ref(0)
const rangeReady = ref(true)
const pagination = reactive({ ...initialPageState.pagination })
const sortState = ref<{ field: ClientIpStatsSortField; order: TableSortOrder }>({ ...initialPageState.sortState })
const policyModalOpen = ref(false)
const policySubmitting = ref(false)
const policyTarget = ref<ClientIpStatsRow>()
const policyAction = ref<PolicyAction>('blacklist')
const policyForm = reactive<{ reason?: string; durationMode: PolicyDurationMode; durationValue?: number | null }>({
  durationMode: 'permanent'
})
const detailDrawerOpen = ref(false)
const detailLoading = ref(false)
const detailTarget = ref<ClientIpStatsRow>()
const detailRows = ref<ClientIpAccountUsageRow[]>([])
const detailRangeReady = ref(true)
const detailPaginationUpperBound = ref(0)
const detailPagination = reactive({ current: 1, pageSize: 20 })
const detailSortState = ref<{ field: ClientIpStatsSortField; order: TableSortOrder }>({ field: 'requestCount', order: 'descend' })

const activeFilterCount = computed(() => {
  let count = 0
  if (keyword.value.trim()) count += 1
  if (usageWindow.value !== 'recent7d') count += 1
  if (statusFilter.value !== 'all') count += 1
  return count
})

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: paginationUpperBound.value,
  showSizeChanger: true
}))

const detailTablePagination = computed(() => ({
  current: detailPagination.current,
  pageSize: detailPagination.pageSize,
  total: detailPaginationUpperBound.value,
  showSizeChanger: true
}))

const currentUsageWindowLabel = computed(() => usageWindowOptions.find((option) => option.value === usageWindow.value)?.label ?? '当前范围')
const emptyDescription = '当前筛选下没有 IP。'
const detailDrawerTitle = computed(() => detailTarget.value ? `IP 详情：${detailTarget.value.aggregateIpKey}` : 'IP 详情')
const detailEmptyDescription = computed(() => detailRangeReady.value ? '当前统计范围内没有关联账号。' : `${currentUsageWindowLabel.value}用量窗口尚未完成预聚合，请稍后刷新。`)

const policyModalTitle = computed(() => '封禁 IP')
const policyReasonLabel = computed(() => '封禁原因')

onMounted(() => {
  void loadData()
})

async function loadData(options: { force?: boolean } = {}): Promise<void> {
  const requestSeq = ++listRequestSeq
  loading.value = true
  try {
    await loadUsageStatsWindow({
      force: options.force === true,
      viewScope: 'admin'
    })
    if (didUsageStatsWindowLoadFail('admin')) throw new Error('统计窗口加载失败')
    if (requestSeq !== listRequestSeq) return
    const result = await api.ipStats.list(buildListParams())
    if (requestSeq !== listRequestSeq) return
    rows.value = result.items
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    paginationUpperBound.value = result.pageUpperBound
    rangeReady.value = result.rangeReady
  } catch (error) {
    if (requestSeq !== listRequestSeq) return
    message.error(extractApiErrorMessage(error, '加载 IP 统计失败'))
  } finally {
    if (requestSeq === listRequestSeq) loading.value = false
  }
}

function buildListParams(): ClientIpStatsListParams {
  const usageRange = usageWindowDateRange(usageWindow.value)
  return {
    page: pagination.current,
    pageSize: pagination.pageSize,
    keyword: keyword.value.trim() || undefined,
    status: statusFilter.value,
    startDate: formatDateKey(usageRange[0]),
    endDate: formatDateKey(usageRange[1]),
    sortField: sortState.value.field,
    sortOrder: tableSortOrderToApi(sortState.value.order)
  }
}

function applyFilters(): void {
  pagination.current = 1
  void loadData()
}

function resetFilters(): void {
  const defaults = defaultIpStatsPageState()
  keyword.value = defaults.keyword
  usageWindow.value = defaults.usageWindow
  statusFilter.value = defaults.statusFilter
  pagination.current = defaults.pagination.current
  pagination.pageSize = defaults.pagination.pageSize
  sortState.value = { ...defaults.sortState }
  pageStateCache.clear()
  void loadData()
}

function defaultIpStatsPageState(): IpStatsPageState {
  return {
    keyword: '',
    pagination: { current: 1, pageSize: ipStatsPageSize },
    sortState: { field: 'requestCount', order: 'descend' },
    statusFilter: 'all',
    usageWindow: 'recent7d'
  }
}

function sanitizeIpStatsPageState(value: unknown, fallback: IpStatsPageState): IpStatsPageState {
  const source = value && typeof value === 'object' ? value as Partial<IpStatsPageState> : {}
  const sourceSortState = source.sortState && typeof source.sortState === 'object'
    ? source.sortState as Partial<IpStatsPageState['sortState']>
    : {}
  return {
    keyword: stringOrFallback(source.keyword, fallback.keyword),
    pagination: sanitizePaginationState(source.pagination, fallback.pagination),
    sortState: {
      field: stringUnionOrFallback(sourceSortState.field, ['requestCount', 'successCount', 'errorCount', 'errorRate', 'totalTokens', 'totalCost', 'activeDays', 'lastUsedAt'], fallback.sortState.field),
      order: sourceSortState.order === 'ascend' || sourceSortState.order === 'descend' || sourceSortState.order === null
        ? sourceSortState.order
        : fallback.sortState.order
    },
    statusFilter: stringUnionOrFallback(source.statusFilter, ['all', 'normal', 'blacklisted', 'allowlisted'], fallback.statusFilter),
    usageWindow: stringUnionOrFallback(source.usageWindow, ['today', 'recent7d', 'recent1m'], fallback.usageWindow)
  }
}

function snapshotPageState(): IpStatsPageState {
  return {
    keyword: keyword.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    sortState: { ...sortState.value },
    statusFilter: statusFilter.value,
    usageWindow: usageWindow.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

async function handleTableChange(paginationInfo: unknown, _filters: unknown, sorter: unknown): Promise<void> {
  updatePaginationFromTable(paginationInfo)
  sortState.value = normalizeTableSorter(sorter) ?? { field: 'requestCount', order: 'descend' }
  await loadData()
}

function openDetailDrawer(record: ClientIpStatsRow): void {
  detailTarget.value = record
  detailRows.value = []
  detailRangeReady.value = true
  detailPaginationUpperBound.value = 0
  detailPagination.current = 1
  detailSortState.value = { field: 'requestCount', order: 'descend' }
  detailDrawerOpen.value = true
  void loadDetailData()
}

async function loadDetailData(): Promise<void> {
  const target = detailTarget.value
  if (!target) return
  const targetIpHash = target.ipHash
  detailLoading.value = true
  try {
    const result = await api.ipStats.detail(targetIpHash, buildDetailParams())
    if (detailTarget.value?.ipHash !== targetIpHash) return
    detailRows.value = result.items
    detailPagination.current = result.page
    detailPagination.pageSize = result.pageSize
    detailPaginationUpperBound.value = result.pageUpperBound
    detailRangeReady.value = result.rangeReady
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载 IP 详情失败'))
  } finally {
    if (detailTarget.value?.ipHash === targetIpHash) {
      detailLoading.value = false
    }
  }
}

function buildDetailParams(): ClientIpStatsDetailParams {
  const usageRange = usageWindowDateRange(usageWindow.value)
  return {
    page: detailPagination.current,
    pageSize: detailPagination.pageSize,
    startDate: formatDateKey(usageRange[0]),
    endDate: formatDateKey(usageRange[1]),
    sortField: detailSortState.value.field,
    sortOrder: tableSortOrderToApi(detailSortState.value.order)
  }
}

async function handleDetailTableChange(paginationInfo: unknown, _filters: unknown, sorter: unknown): Promise<void> {
  updateDetailPaginationFromTable(paginationInfo)
  detailSortState.value = normalizeTableSorter(sorter) ?? { field: 'requestCount', order: 'descend' }
  await loadDetailData()
}

function handlePolicyAction(record: ClientIpStatsRow, action: PolicyAction): void {
  if (action === 'blacklist') {
    openPolicyModal(record, action)
    return
  }
  void submitPolicyAction(record, action)
}

function openPolicyModal(record: ClientIpStatsRow, action: PolicyAction): void {
  policyTarget.value = record
  policyAction.value = action
  policyForm.reason = undefined
  policyForm.durationMode = 'permanent'
  policyForm.durationValue = undefined
  policyModalOpen.value = true
}

async function submitPolicy(): Promise<void> {
  if (!policyTarget.value) return
  await submitPolicyAction(policyTarget.value, policyAction.value, true)
}

async function submitPolicyAction(record: ClientIpStatsRow, action: PolicyAction, closeModal = false): Promise<void> {
  policySubmitting.value = true
  try {
    if (action === 'blacklist') {
      const payload = policyPayload()
      if (!payload) return
      await api.ipStats.blacklist(record.ipHash, payload)
      message.success('已封禁 IP')
    } else if (action === 'allowlist') {
      await api.ipStats.allowlist(record.ipHash, {})
      message.success('已加入白名单')
    } else if (action === 'unallowlist') {
      await api.ipStats.unallowlist(record.ipHash, {})
      message.success('已移出白名单')
    } else {
      await api.ipStats.unblock(record.ipHash, {})
      message.success('已解除封禁')
    }
    if (closeModal) {
      policyModalOpen.value = false
    }
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '提交 IP 策略失败'))
  } finally {
    policySubmitting.value = false
  }
}

function policyPayload(): { reason?: string; durationMinutes?: number; durationDays?: number } | undefined {
  const reason = policyForm.reason?.trim() || undefined
  if (policyForm.durationMode === 'permanent') {
    return { reason }
  }
  const durationValue = normalizeDurationValue(policyForm.durationValue)
  if (!durationValue) {
    message.warning('请输入封禁时长')
    return undefined
  }
  if (policyForm.durationMode === 'minutes') {
    return { reason, durationMinutes: durationValue }
  }
  return { reason, durationDays: durationValue }
}

function handlePolicyDurationModeChange(value: string | number): void {
  const mode = value as PolicyDurationMode
  policyForm.durationMode = mode
  policyForm.durationValue = defaultPolicyDurationValue(mode)
}

function defaultPolicyDurationValue(mode: PolicyDurationMode): number | undefined {
  if (mode === 'minutes') return 60
  if (mode === 'days') return 1
  return undefined
}

function normalizeDurationValue(value: unknown): number | undefined {
  const numericValue = Number(value)
  if (!Number.isFinite(numericValue) || numericValue < 1) return undefined
  return Math.trunc(numericValue)
}

function updatePaginationFromTable(paginationInfo: unknown): void {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const current = Number(next.current)
  const pageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(current) && current > 0 ? current : 1
  pagination.pageSize = Number.isFinite(pageSize) && pageSize > 0 ? pageSize : pagination.pageSize
}

function updateDetailPaginationFromTable(paginationInfo: unknown): void {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const current = Number(next.current)
  const pageSize = Number(next.pageSize)
  detailPagination.current = Number.isFinite(current) && current > 0 ? current : 1
  detailPagination.pageSize = Number.isFinite(pageSize) && pageSize > 0 ? pageSize : detailPagination.pageSize
}

function normalizeTableSorter(sorter: unknown): { field: ClientIpStatsSortField; order: TableSortOrder } | undefined {
  const item = Array.isArray(sorter) ? sorter[0] : sorter
  if (!item || typeof item !== 'object') return undefined
  const record = item as Record<string, unknown>
  const field = sortFieldFromColumn(record.columnKey ?? record.field)
  const order = record.order === 'ascend' || record.order === 'descend' ? record.order : null
  return field && order ? { field, order } : undefined
}

function sortFieldFromColumn(value: unknown): ClientIpStatsSortField | undefined {
  if (value === 'requestCount' || value === 'errorRate' || value === 'activeDays' || value === 'lastUsedAt') return value
  if (value === 'totalTokens') return 'totalTokens'
  if (value === 'cost') return 'totalCost'
  return undefined
}

function tableSortOrderToApi(order: TableSortOrder): SortDirection | undefined {
  if (order === 'ascend') return 'asc'
  if (order === 'descend') return 'desc'
  return undefined
}

function formatDateKey(value: Dayjs): string {
  return value.format('YYYY-MM-DD')
}

function usageWindowDateRange(value: UsageWindow): [Dayjs, Dayjs] {
  const today = (usageStatsWindowEndDate.value?.isValid() ? usageStatsWindowEndDate.value : dayjs()).startOf('day')
  if (value === 'today') return [today, today]
  if (value === 'recent1m') return [today.subtract(30, 'day'), today]
  return [today.subtract(6, 'day'), today]
}
</script>

<style scoped>
.ip-stats-usage-window {
  width: max-content;
  max-width: 100%;
  flex: none;
}

.ip-stats-status {
  width: 130px;
}

.ip-stats-range-alert {
  margin-bottom: 12px;
}

.policy-duration-input {
  width: 100%;
}

.mono-cell {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  word-break: break-all;
}

.muted-cell {
  color: #8c8c8c;
  font-size: 12px;
}

.name-cell {
  color: #1f2937;
}

.number-cell {
  font-variant-numeric: tabular-nums;
}

.ip-detail-summary {
  margin-bottom: 16px;
}

.ip-detail-account-table {
  margin-top: 4px;
}

.ip-detail-account {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.ip-detail-account-name,
.ip-detail-owner-name {
  display: block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ip-detail-mobile-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 14px;
  border: 1px solid #f0f0f0;
  border-radius: 8px;
  background: #fff;
}

.ip-detail-mobile-head {
  display: flex;
  min-width: 0;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.ip-detail-mobile-title {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.ip-detail-owner-text {
  color: #6b7280;
  font-size: 12px;
  line-height: 1.5;
}

@media (max-width: 768px) {
  .ip-stats-usage-window,
  .ip-stats-status {
    width: 100%;
  }
}
</style>
