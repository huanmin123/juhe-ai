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
      @refresh="loadData"
    >
      <template #inline-filters>
        <a-range-picker
          v-model:value="lastUsedDateRange"
          :allow-clear="false"
          :disabled="loading"
          :disabled-date="disabledDate"
          class="toolbar-select ip-stats-range responsive-list-inline-filter"
          format="YYYY-MM-DD"
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
          <a-form-item label="最后使用日期">
            <a-range-picker
              v-model:value="lastUsedDateRange"
              :allow-clear="false"
              :disabled="loading"
              :disabled-date="disabledDate"
              class="drawer-range-picker"
              format="YYYY-MM-DD"
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

    <ResponsiveDataList
      class="ip-stats-list"
      table-class="page-table ip-stats-table"
      :columns="columns"
      :data-source="rows"
      row-key="ipHash"
      :loading="loading"
      :pagination="tablePagination"
      :pagination-summary="false"
      :scroll-x="1770"
      @change="handleTableChange"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" :description="emptyDescription" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'ip'">
          <span class="mono-cell">{{ record.aggregateIpKey }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'requestCount'">
          <span class="number-cell">{{ formatInteger(record.rangeUsage.requestCount) }}</span>
        </template>
        <template v-else-if="column.key === 'totalTokens'">
          <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</span>
        </template>
        <template v-else-if="column.key === 'inputTokens'">
          <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.inputTokens) }}</span>
        </template>
        <template v-else-if="column.key === 'outputTokens'">
          <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.outputTokens) }}</span>
        </template>
        <template v-else-if="column.key === 'cacheReadTokens'">
          <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.cacheReadTokens) }}</span>
        </template>
        <template v-else-if="column.key === 'cacheRate'">
          <span class="number-cell">{{ formatPercent(cacheReadRate(record.rangeUsage)) }}</span>
        </template>
        <template v-else-if="column.key === 'cacheCost'">
          <span class="number-cell">{{ formatCost(record.rangeUsage.cacheReadCost) }}</span>
        </template>
        <template v-else-if="column.key === 'cost'">
          <span class="number-cell">{{ formatCost(record.rangeUsage.totalCost) }}</span>
        </template>
        <template v-else-if="column.key === 'errorRate'">
          <a-tag :color="record.rangeUsage.errorRate > 0.05 ? 'red' : 'green'">
            {{ formatPercent(record.rangeUsage.errorRate * 100) }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'activeDays'">
          {{ formatInteger(record.rangeUsage.activeDays) }}
        </template>
        <template v-else-if="column.key === 'averageFirstTokenMs'">
          <span class="number-cell">{{ formatDuration(record.rangeUsage.averageFirstTokenMs) }}</span>
        </template>
        <template v-else-if="column.key === 'averageDurationMs'">
          <span class="number-cell">{{ formatDuration(record.rangeUsage.averageDurationMs) }}</span>
        </template>
        <template v-else-if="column.key === 'maxDurationMs'">
          <span class="number-cell">{{ formatDuration(record.rangeUsage.maxDurationMs) }}</span>
        </template>
        <template v-else-if="column.key === 'lastUsedAt'">
          <span :class="clientIpLastUsedAt(record) ? 'name-cell' : 'muted-cell'">{{ formatDateTime(clientIpLastUsedAt(record)) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="ipRowActions(record)" @action-click="handleRowAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="ip-mobile-card">
          <div class="ip-mobile-head">
            <div class="mono-cell">{{ record.aggregateIpKey }}</div>
            <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
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
              <span>输入 Token</span>
              <strong>{{ formatCompactInteger(record.rangeUsage.inputTokens) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>输出 Token</span>
              <strong>{{ formatCompactInteger(record.rangeUsage.outputTokens) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>缓存 Token</span>
              <strong>{{ formatCompactInteger(record.rangeUsage.cacheReadTokens) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>缓存率</span>
              <strong>{{ formatPercent(cacheReadRate(record.rangeUsage)) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>缓存成本</span>
              <strong>{{ formatCost(record.rangeUsage.cacheReadCost) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>成本</span>
              <strong>{{ formatCost(record.rangeUsage.totalCost) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>失败率</span>
              <strong>{{ formatPercent(record.rangeUsage.errorRate * 100) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>活跃天数</span>
              <strong>{{ formatInteger(record.rangeUsage.activeDays) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>平均首 Token</span>
              <strong>{{ formatDuration(record.rangeUsage.averageFirstTokenMs) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>平均总耗时</span>
              <strong>{{ formatDuration(record.rangeUsage.averageDurationMs) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最大总耗时</span>
              <strong>{{ formatDuration(record.rangeUsage.maxDurationMs) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>最后使用</span>
              <strong>{{ formatDateTime(clientIpLastUsedAt(record)) }}</strong>
            </div>
          </div>
          <div class="ip-mobile-actions">
            <a-button v-if="record.status === 'blacklisted'" size="small" @click="openPolicyModal(record, 'unblock')">解封</a-button>
            <a-button v-else size="small" danger @click="openPolicyModal(record, 'blacklist')">封禁</a-button>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

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
        <a-form-item v-if="policyAction !== 'unblock'" label="封禁原因">
          <a-textarea v-model:value="policyForm.reason" :rows="3" :maxlength="500" show-count />
        </a-form-item>
        <a-form-item v-if="policyAction !== 'unblock'" label="封禁时长">
          <a-segmented
            v-model:value="policyForm.durationMode"
            :options="policyDurationOptions"
            block
            @change="handlePolicyDurationModeChange"
          />
        </a-form-item>
        <a-form-item v-if="policyAction !== 'unblock' && policyForm.durationMode === 'minutes'" label="分钟数">
          <a-input-number
            v-model:value="policyForm.durationValue"
            class="policy-duration-input"
            :min="1"
            :max="525600"
            :precision="0"
            addon-after="分钟"
          />
        </a-form-item>
        <a-form-item v-if="policyAction !== 'unblock' && policyForm.durationMode === 'days'" label="天数">
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
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'

import { api, type ClientIpStatsListParams, type SortDirection } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import type { ClientIpStatsRow, ClientIpStatsSortField, ClientIpStatus, ClientIpUsageSummary } from '@/types/domain'
import { formatCompactInteger, formatCost, formatDuration, formatInteger, formatPercent } from '@/views/stats/statsFormatters'

type TableSortOrder = 'ascend' | 'descend' | null
type PolicyAction = 'blacklist' | 'unblock'
type PolicyDurationMode = 'permanent' | 'minutes' | 'days'

const columns = [
  { title: 'IP', key: 'ip', width: 180, fixed: 'left', align: 'left' },
  { title: '状态', key: 'status', width: 110, align: 'left' },
  { title: '请求', key: 'requestCount', width: 120, align: 'left', sorter: true },
  { title: 'Token', key: 'totalTokens', width: 120, align: 'left', sorter: true },
  { title: '输入 Token', key: 'inputTokens', width: 120, align: 'left' },
  { title: '输出 Token', key: 'outputTokens', width: 120, align: 'left' },
  { title: '缓存 Token', key: 'cacheReadTokens', width: 120, align: 'left' },
  { title: '缓存率', key: 'cacheRate', width: 100, align: 'left' },
  { title: '缓存成本', key: 'cacheCost', width: 120, align: 'left' },
  { title: '成本', key: 'cost', width: 130, align: 'left', sorter: true },
  { title: '失败率', key: 'errorRate', width: 110, align: 'left', sorter: true },
  { title: '活跃天数', key: 'activeDays', width: 120, align: 'left', sorter: true },
  { title: '平均首 Token', key: 'averageFirstTokenMs', width: 130, align: 'left' },
  { title: '平均总耗时', key: 'averageDurationMs', width: 130, align: 'left' },
  { title: '最大总耗时', key: 'maxDurationMs', width: 130, align: 'left' },
  { title: '最后使用', key: 'lastUsedAt', width: 180, align: 'left', sorter: true },
  { title: '操作', key: 'actions', fixed: 'right', align: 'left' }
]

const statusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '正常', value: 'normal' },
  { label: '已封禁', value: 'blacklisted' }
]

const policyDurationOptions = [
  { label: '永久', value: 'permanent' },
  { label: '分钟', value: 'minutes' },
  { label: '天', value: 'days' }
]

const loading = ref(false)
const keyword = ref('')
const statusFilter = ref<ClientIpStatus>('all')
const lastUsedDateRange = ref<[Dayjs, Dayjs]>(defaultLastUsedDateRange())
const rows = ref<ClientIpStatsRow[]>([])
const paginationUpperBound = ref(0)
const rangeReady = ref(true)
const pagination = reactive({ current: 1, pageSize: 20 })
const sortState = ref<{ field: ClientIpStatsSortField; order: TableSortOrder }>({ field: 'requestCount', order: 'descend' })
const policyModalOpen = ref(false)
const policySubmitting = ref(false)
const policyTarget = ref<ClientIpStatsRow>()
const policyAction = ref<PolicyAction>('blacklist')
const policyForm = reactive<{ reason?: string; durationMode: PolicyDurationMode; durationValue?: number | null }>({
  durationMode: 'permanent'
})

const activeFilterCount = computed(() => {
  let count = 0
  if (keyword.value.trim()) count += 1
  if (statusFilter.value !== 'all') count += 1
  if (!isDefaultLastUsedDateRange(lastUsedDateRange.value)) count += 1
  return count
})

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: paginationUpperBound.value,
  showSizeChanger: true
}))

const emptyDescription = computed(() => rangeReady.value ? '当前最后使用日期范围下没有 IP 统计数据。' : '当前近 7 天用量窗口尚未完成预聚合，请稍后刷新。')

const policyModalTitle = computed(() => {
  if (policyAction.value === 'blacklist') return '封禁 IP'
  return '解除封禁'
})

onMounted(() => {
  void loadData()
})

async function loadData(): Promise<void> {
  loading.value = true
  try {
    const result = await api.ipStats.list(buildListParams())
    rows.value = result.items
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    paginationUpperBound.value = result.pageUpperBound
    rangeReady.value = result.rangeReady
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载 IP 统计失败'))
  } finally {
    loading.value = false
  }
}

function buildListParams(): ClientIpStatsListParams {
  const usageRange = defaultUsageDateRange()
  return {
    page: pagination.current,
    pageSize: pagination.pageSize,
    keyword: keyword.value.trim() || undefined,
    status: statusFilter.value,
    startDate: formatDateKey(usageRange[0]),
    endDate: formatDateKey(usageRange[1]),
    lastUsedStartDate: formatDateKey(lastUsedDateRange.value[0]),
    lastUsedEndDate: formatDateKey(lastUsedDateRange.value[1]),
    sortField: sortState.value.field,
    sortOrder: tableSortOrderToApi(sortState.value.order)
  }
}

function applyFilters(): void {
  pagination.current = 1
  void loadData()
}

function resetFilters(): void {
  keyword.value = ''
  statusFilter.value = 'all'
  lastUsedDateRange.value = defaultLastUsedDateRange()
  pagination.current = 1
  sortState.value = { field: 'requestCount', order: 'descend' }
  void loadData()
}

function ipRowActions(record: ClientIpStatsRow): RowActionItem[] {
  if (record.status === 'blacklisted') {
    return [{ key: 'unblock', label: '解封', icon: 'restore', tone: 'success' }]
  }
  return [{ key: 'blacklist', label: '封禁', icon: 'disable', tone: 'danger' }]
}

function handleRowAction(key: string, record: ClientIpStatsRow): void {
  const action = key as PolicyAction
  if (action === 'blacklist' || action === 'unblock') {
    openPolicyModal(record, action)
  }
}

async function handleTableChange(paginationInfo: unknown, _filters: unknown, sorter: unknown): Promise<void> {
  updatePaginationFromTable(paginationInfo)
  sortState.value = normalizeTableSorter(sorter) ?? { field: 'requestCount', order: 'descend' }
  await loadData()
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
  policySubmitting.value = true
  try {
    if (policyAction.value === 'blacklist') {
      const payload = policyPayload()
      if (!payload) return
      await api.ipStats.blacklist(policyTarget.value.ipHash, payload)
      message.success('已封禁 IP')
    } else {
      await api.ipStats.unblock(policyTarget.value.ipHash, {})
      message.success('已解除封禁')
    }
    policyModalOpen.value = false
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

function cacheReadRate(usage?: ClientIpUsageSummary): number {
  const inputTokens = usage?.inputTokens ?? 0
  if (inputTokens <= 0) return 0
  return ((usage?.cacheReadTokens ?? 0) / inputTokens) * 100
}

function clientIpLastUsedAt(record: ClientIpStatsRow): string | undefined {
  return record.lastSeenAt ?? record.rangeUsage.lastUsedAt
}

function tableSortOrderToApi(order: TableSortOrder): SortDirection | undefined {
  if (order === 'ascend') return 'asc'
  if (order === 'descend') return 'desc'
  return undefined
}

function statusText(status: ClientIpStatus): string {
  if (status === 'blacklisted') return '已封禁'
  if (status === 'normal') return '正常'
  return '全部'
}

function statusColor(status: ClientIpStatus): string {
  if (status === 'blacklisted') return 'red'
  if (status === 'normal') return 'green'
  return 'default'
}

function disabledDate(current: Dayjs): boolean {
  const today = dayjs()
  return current.isAfter(today, 'day') || current.isBefore(today.subtract(30, 'day'), 'day')
}

function formatDateKey(value: Dayjs): string {
  return value.format('YYYY-MM-DD')
}

function defaultLastUsedDateRange(): [Dayjs, Dayjs] {
  return [dayjs().subtract(6, 'day'), dayjs()]
}

function defaultUsageDateRange(): [Dayjs, Dayjs] {
  return defaultLastUsedDateRange()
}

function isDefaultLastUsedDateRange(range: [Dayjs, Dayjs]): boolean {
  const defaultRange = defaultLastUsedDateRange()
  return range[0].isSame(defaultRange[0], 'day') && range[1].isSame(defaultRange[1], 'day')
}
</script>

<style scoped>
.ip-stats-range {
  width: 260px;
}

.ip-stats-status {
  width: 130px;
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

.metric-cell {
  display: flex;
  width: 100%;
  flex-direction: column;
  gap: 2px;
}

.metric-cell-extra,
.mobile-list-meta-item small {
  color: #8c8c8c;
  font-size: 12px;
  line-height: 1.4;
}

.policy-duration-input,
.drawer-range-picker {
  width: 100%;
}

.ip-mobile-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ip-mobile-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.ip-mobile-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

@media (max-width: 768px) {
  .ip-stats-range,
  .ip-stats-status {
    width: 100%;
  }
}
</style>
