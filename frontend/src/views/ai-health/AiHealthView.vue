<template>
  <a-card class="page-card responsive-page-card ai-health-page-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索账户名"
      :show-filters="false"
      :show-reset="hasActiveFilters"
      :refresh-loading="loading"
      :mobile-action-count="1"
      @search="searchAccounts"
      @reset="resetFilters"
      @refresh="loadData"
    >
      <template #inline-filters>
        <a-select v-model:value="rangeHours" class="ai-health-range-select" :options="rangeOptions" @change="changeRange" />
      </template>
      <template #actions>
        <div class="ai-health-legend" aria-label="状态图例">
          <span><i class="success" />检查成功</span>
          <span><i class="failure" />检查失败</span>
          <span><i class="unknown" />无记录</span>
        </div>
      </template>
    </ResponsiveListToolbar>

    <div ref="contentRef" class="ai-health-content">
      <a-spin :spinning="loading">
        <div v-if="accounts.length" class="ai-health-list">
          <article v-for="account in accounts" :key="account.id" class="ai-health-account">
            <header class="ai-health-account-header">
              <div class="ai-health-account-title">
                <strong>{{ account.name }}</strong>
                <a-tag>{{ providerDisplayName(account.providerCode) }}</a-tag>
                <a-tag :color="accountStatusColor(account.status)">{{ accountStatusLabel(account.status) }}</a-tag>
                <a-tag :color="healthStatusColor(account.latestStatus)">{{ healthStatusLabel(account.latestStatus) }}</a-tag>
              </div>
              <div class="ai-health-account-rate">
                <strong>{{ formatHealthRate(account.healthRate) }}</strong>
                <span>检查成功率</span>
              </div>
            </header>

            <div class="ai-health-account-meta">
              <span v-if="isManagementView && account.systemAccountName">所属用户：{{ account.systemAccountName }}</span>
              <span>最近独立检查：{{ formatDateTime(account.lastHealthCheckAt) }}</span>
              <span v-if="account.lastHealthSuccessAt">最近健康成功信号：{{ formatDateTime(account.lastHealthSuccessAt) }}</span>
              <span>下次独立检查：{{ formatDateTime(account.nextHealthCheckAt) }}</span>
              <span class="success-text">检查成功 {{ account.successHours }}</span>
              <span class="failure-text">检查失败 {{ account.failureHours }}</span>
              <span class="unknown-text">无记录 {{ account.unknownHours }}</span>
            </div>

            <AiHealthStatusBar :account-name="account.name" :hours="account.hours" @select="openHourDetail(account.name, $event)" />
            <div class="ai-health-range-labels">
              <span>{{ formatHour(account.hours[0]?.statHour) }}</span>
              <span>{{ formatHour(account.hours[account.hours.length - 1]?.statHour) }}</span>
            </div>
          </article>
        </div>
        <a-empty v-else-if="!loading" class="page-empty-card" description="没有匹配的 AI 账户" />
      </a-spin>
    </div>

    <div v-if="pagination.total > pagination.pageSize" class="ai-health-pagination">
      <span>共 {{ pagination.total }} 个账户</span>
      <a-pagination
        :current="pagination.current"
        :page-size="pagination.pageSize"
        :total="pagination.total"
        :show-size-changer="false"
        show-less-items
        @change="changePage"
      />
    </div>

    <a-drawer v-model:open="detailOpen" title="检查详情" width="420px">
      <a-descriptions v-if="selectedDetail" bordered size="small" :column="1">
        <a-descriptions-item label="账户">{{ selectedDetail.accountName }}</a-descriptions-item>
        <a-descriptions-item label="状态">
          <a-tag :color="healthStatusColor(selectedDetail.point.status)">{{ pointStatusLabel(selectedDetail.point.status) }}</a-tag>
        </a-descriptions-item>
        <a-descriptions-item label="统计小时">{{ formatHour(selectedDetail.point.statHour) }}</a-descriptions-item>
        <a-descriptions-item label="检查时间">{{ formatDateTime(selectedDetail.point.lastObservedAt) }}</a-descriptions-item>
        <a-descriptions-item label="HTTP 状态">{{ selectedDetail.point.statusCode ?? '-' }}</a-descriptions-item>
        <a-descriptions-item label="错误码">{{ selectedDetail.point.errorCode || '-' }}</a-descriptions-item>
        <a-descriptions-item label="原因">{{ detailReason(selectedDetail.point) }}</a-descriptions-item>
      </a-descriptions>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountStatus, AiHealthAccountRow, AiHealthHourPoint, AiHealthHourStatus } from '@/types/domain'
import AiHealthStatusBar from './AiHealthStatusBar.vue'

const pageSize = 20
const keyword = ref('')
const rangeHours = ref(7 * 24)
const contentRef = ref<HTMLElement>()
const selectedDetail = ref<{ accountName: string; point: AiHealthHourPoint }>()
const detailOpen = computed({
  get: () => Boolean(selectedDetail.value),
  set: (open: boolean) => {
    if (!open) selectedDetail.value = undefined
  }
})
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const rangeOptions = [
  { label: '最近一天', value: 24 },
  { label: '最近 7 天', value: 7 * 24 },
  { label: '最近 14 天', value: 14 * 24 },
  { label: '最近 31 天', value: 31 * 24 }
]
const hasActiveFilters = computed(() => Boolean(keyword.value.trim()) || rangeHours.value !== 7 * 24)

const { items: accounts, loading, pagination, handleTableChange, loadData, resetPagination } = useResponsivePagedList<AiHealthAccountRow>({
  pageSize,
  showTotal: (total) => `共 ${total} 个账户`,
  fetchPage: async (_options, page) => {
    const request = isManagementView.value ? api.stats.aiHealth : api.myStats.aiHealth
    const result = await request({
      hours: rangeHours.value,
      keyword: keyword.value.trim() || undefined,
      page: page.current,
      pageSize: page.pageSize,
      systemAccountId: scopedSystemAccountId()
    })
    return result
  },
  onError: (error) => message.error(extractApiErrorMessage(error, '加载 AI 健康监控失败')),
  requestSignature: () => ({
    scope: isManagementView.value ? 'admin' : 'self',
    systemAccountId: scopedSystemAccountId(),
    hours: rangeHours.value,
    keyword: keyword.value.trim()
  })
})

function searchAccounts(): void {
  scrollContentToTop()
  resetPagination()
  void loadData()
}

function resetFilters(): void {
  keyword.value = ''
  rangeHours.value = 7 * 24
  scrollContentToTop()
  resetPagination()
  void loadData()
}

function changeRange(): void {
  scrollContentToTop()
  resetPagination()
  void loadData()
}

function changePage(page: number): void {
  scrollContentToTop()
  handleTableChange({ current: page, pageSize: pagination.pageSize })
}

function scrollContentToTop(): void {
  contentRef.value?.scrollTo({ top: 0 })
}

function healthStatusLabel(status: AiHealthHourStatus): string {
  if (status === 'success') return '最近检查成功'
  if (status === 'failure') return '最近检查失败'
  return '暂无检查'
}

function pointStatusLabel(status: AiHealthHourStatus): string {
  if (status === 'success') return '检查成功'
  if (status === 'failure') return '检查失败'
  return '无记录'
}

function healthStatusColor(status: AiHealthHourStatus): string {
  if (status === 'success') return 'green'
  if (status === 'failure') return 'red'
  return 'default'
}

function accountStatusLabel(status: AccountStatus): string {
  if (status === 'active') return '已启用'
  if (status === 'pending_test') return '待检查'
  if (status === 'disabled') return '已停用'
  if (status === 'error') return '异常'
  if (status === 'rate_limited') return '限流中'
  return '临时不可用'
}

function accountStatusColor(status: AccountStatus): string {
  if (status === 'active') return 'green'
  if (status === 'pending_test') return 'blue'
  if (status === 'disabled') return 'default'
  if (status === 'rate_limited') return 'orange'
  return 'red'
}

function formatHealthRate(value?: number): string {
  return typeof value === 'number' ? `${value.toFixed(value % 1 === 0 ? 0 : 1)}%` : '--'
}

function formatHour(value?: string): string {
  return value ? value.replace('T', ' ') : '-'
}

function openHourDetail(accountName: string, point: AiHealthHourPoint): void {
  selectedDetail.value = { accountName, point }
}

function detailReason(point: AiHealthHourPoint): string {
  if (point.status === 'unknown') return '该小时没有检查记录'
  if (point.status === 'success') return '-'
  return point.errorMessage || point.errorCode || '检查未成功，未返回具体原因'
}

onMounted(() => void loadData())
</script>

<style scoped>
.ai-health-range-select { width: 148px; }
.ai-health-legend { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 14px; color: #64748b; font-size: 13px; white-space: nowrap; }
.ai-health-legend span { display: inline-flex; align-items: center; gap: 6px; }
.ai-health-legend i { width: 7px; height: 18px; border-radius: 2px; }
.ai-health-legend .success { background: #10b981; }
.ai-health-legend .failure { background: #ef4444; }
.ai-health-legend .unknown { background: #d7dde5; }
.ai-health-content { min-height: 0; flex: 1 1 auto; overflow-y: auto; overscroll-behavior: contain; padding-right: 4px; scrollbar-gutter: stable; }
.ai-health-list { display: grid; gap: 12px; }
.ai-health-account { min-width: 0; padding: 16px; border: 1px solid #e8edf3; border-radius: 8px; background: #fff; }
.ai-health-account-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 18px; }
.ai-health-account-title { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; min-width: 0; }
.ai-health-account-title strong { max-width: min(480px, 55vw); overflow: hidden; color: #172033; font-size: 16px; text-overflow: ellipsis; white-space: nowrap; }
.ai-health-account-rate { display: grid; flex: 0 0 auto; justify-items: end; }
.ai-health-account-rate strong { color: #172033; font-size: 20px; line-height: 1.1; }
.ai-health-account-rate span { margin-top: 3px; color: #94a3b8; font-size: 12px; }
.ai-health-account-meta { display: flex; flex-wrap: wrap; gap: 6px 18px; margin: 10px 0 9px; color: #64748b; font-size: 13px; }
.success-text { color: #059669; }
.failure-text { color: #dc2626; }
.unknown-text { color: #94a3b8; }
.ai-health-range-labels { display: flex; justify-content: space-between; margin-top: 3px; color: #94a3b8; font-size: 11px; }
.ai-health-pagination { display: flex; align-items: center; justify-content: space-between; flex: 0 0 auto; gap: 16px; margin-top: 14px; padding-top: 14px; color: #64748b; border-top: 1px solid #edf1f7; }

@media (max-width: 720px) {
  .ai-health-account { padding: 13px; }
  .ai-health-account-header { display: block; }
  .ai-health-account-title strong { width: 100%; max-width: 100%; }
  .ai-health-account-rate { display: flex; align-items: baseline; justify-content: flex-start; gap: 8px; margin-top: 10px; }
  .ai-health-account-rate span { margin-top: 0; }
  .ai-health-pagination { align-items: flex-start; flex-direction: column; }
}
</style>
