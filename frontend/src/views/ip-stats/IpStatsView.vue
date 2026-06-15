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

    <IpStatsList
      :empty-description="emptyDescription"
      :loading="loading"
      :rows="rows"
      :table-pagination="tablePagination"
      @change="handleTableChange"
      @policy-action="openPolicyModal"
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
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { ClientIpStatsRow, ClientIpStatsSortField, ClientIpStatus } from '@/types/domain'

import IpStatsList from './IpStatsList.vue'
import type { IpStatsPolicyAction } from './ipStatsDisplay'

type TableSortOrder = 'ascend' | 'descend' | null
type PolicyAction = IpStatsPolicyAction
type PolicyDurationMode = 'permanent' | 'minutes' | 'days'
type UsageWindow = 'today' | 'recent7d' | 'recent31d'

const usageWindowOptions = [
  { label: '今天', value: 'today' },
  { label: '近 7 天', value: 'recent7d' },
  { label: '近 31 天', value: 'recent31d' }
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
const usageWindow = ref<UsageWindow>('recent7d')
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

const currentUsageWindowLabel = computed(() => usageWindowOptions.find((option) => option.value === usageWindow.value)?.label ?? '当前范围')
const emptyDescription = computed(() => rangeReady.value ? '当前筛选下没有 IP 统计数据。' : `${currentUsageWindowLabel.value}用量窗口尚未完成预聚合，请稍后刷新。`)

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
  keyword.value = ''
  usageWindow.value = 'recent7d'
  statusFilter.value = 'all'
  pagination.current = 1
  sortState.value = { field: 'requestCount', order: 'descend' }
  void loadData()
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

function tableSortOrderToApi(order: TableSortOrder): SortDirection | undefined {
  if (order === 'ascend') return 'asc'
  if (order === 'descend') return 'desc'
  return undefined
}

function formatDateKey(value: Dayjs): string {
  return value.format('YYYY-MM-DD')
}

function usageWindowDateRange(value: UsageWindow): [Dayjs, Dayjs] {
  const today = dayjs().startOf('day')
  if (value === 'today') return [today, today]
  if (value === 'recent31d') return [today.subtract(30, 'day'), today]
  return [today.subtract(6, 'day'), today]
}
</script>

<style scoped>
.ip-stats-usage-window {
  min-width: 236px;
}

.ip-stats-status {
  width: 130px;
}

.policy-duration-input {
  width: 100%;
}

@media (max-width: 768px) {
  .ip-stats-usage-window,
  .ip-stats-status {
    width: 100%;
  }
}
</style>
