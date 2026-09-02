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
      @refresh="refreshData"
    >
      <template #inline-filters>
        <a-select v-model:value="rangeHours" class="ai-health-range-select" :options="rangeOptions" @change="changeRange" />
      </template>
      <template #actions>
        <div class="ai-health-legend" aria-label="状态图例">
          <span><i class="success" />可用</span>
          <span><i class="failure" />不可用</span>
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
              <span class="success-text">可用 {{ account.successHours }}</span>
              <span class="failure-text">不可用 {{ account.failureHours }}</span>
              <span class="unknown-text">无记录 {{ account.unknownHours }}</span>
            </div>

            <AiHealthStatusBar :account-name="account.name" :hours="account.hours" @select="openHourDetail(account, $event)" />
            <div class="ai-health-range-labels">
              <span>{{ formatHour(account.hours[0]?.statHour) }}</span>
              <span>{{ formatHour(account.hours[account.hours.length - 1]?.statHour) }}</span>
            </div>
          </article>
        </div>
        <a-empty v-else-if="!loading" class="page-empty-card" description="没有匹配的 AI 账户" />
      </a-spin>
    </div>

    <div v-if="pagination.current > 1 || hasMore" class="ai-health-pagination">
      <span>第 {{ pagination.current }} 页</span>
      <a-space>
        <a-button :disabled="loading || pagination.current <= 1" @click="changePage(pagination.current - 1)">上一页</a-button>
        <a-button :disabled="loading || !hasMore" @click="changePage(pagination.current + 1)">下一页</a-button>
      </a-space>
    </div>

    <a-drawer :open="detailOpen" title="检查详情" width="420px" @update:open="setDetailOpen">
      <a-alert v-if="detailError" class="ai-health-detail-error" type="error" show-icon :message="detailError" />
      <a-spin :spinning="detailLoading">
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
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'

import { api } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import { authState } from '@/composables/useAuth'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { AccountStatus, AiHealthAccountRow, AiHealthHourDetail, AiHealthHourPoint, AiHealthHourStatus } from '@/types/domain'
import AiHealthStatusBar from './AiHealthStatusBar.vue'
import { createAiHealthRequestCoordinator, isAiHealthCanceledRequest } from './aiHealthRequestCoordinator'

// Keep the first viewport responsive by limiting the initial account batch.
const pageSize = 10
const keyword = ref('')
const rangeHours = ref(7 * 24)
const contentRef = ref<HTMLElement>()
const selectedDetail = ref<{ accountId: string; accountName: string; point: AiHealthHourDetail }>()
const detailOpen = ref(false)
const detailLoading = ref(false)
const detailError = ref('')
const requestCoordinator = createAiHealthRequestCoordinator()
let initialVisibleLoadStarted = false
let initialVisibleLoadCompleted = false
let initialVisibleLoadGeneration = 0
let pageActive = true
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const rangeOptions = [
  { label: '最近一天', value: 24 },
  { label: '最近 7 天', value: 7 * 24 },
  { label: '最近 14 天', value: 14 * 24 },
  { label: '最近 31 天', value: 31 * 24 }
]
const hasActiveFilters = computed(() => Boolean(keyword.value.trim()) || rangeHours.value !== 7 * 24)

const { hasMore, items: accounts, loading, pagination, handleTableChange, invalidatePendingLoads, loadData, resetPagination } = useResponsivePagedList<AiHealthAccountRow>({
  pageSize,
  showTotal: (_total, _range, context) => context ? `当前页 ${context.currentPageCount} 个账户` : '',
  fetchPage: async (_options, page) => {
    const token = requestCoordinator.beginList()
    const request = isManagementView.value ? api.stats.aiHealth : api.myStats.aiHealth
    try {
      const result = await request({
        hours: rangeHours.value,
        keyword: keyword.value.trim() || undefined,
        page: page.current,
        pageSize: page.pageSize,
        systemAccountId: scopedSystemAccountId()
      }, { signal: token.signal })
      if (!token.isCurrent()) return supersededPage(page.current, page.pageSize)
      return {
        ...result,
        total: knownPageLowerBound(result.page, result.pageSize, result.items.length, result.hasMore)
      }
    } catch (error) {
      if (isAiHealthCanceledRequest(error) || !token.isCurrent()) {
        return supersededPage(page.current, page.pageSize)
      }
      throw error
    }
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
  closeDetail()
  scrollContentToTop()
  resetPagination()
  hasMore.value = false
  void loadData()
}

function resetFilters(): void {
  closeDetail()
  keyword.value = ''
  rangeHours.value = 7 * 24
  scrollContentToTop()
  resetPagination()
  hasMore.value = false
  void loadData()
}

function changeRange(): void {
  closeDetail()
  scrollContentToTop()
  resetPagination()
  hasMore.value = false
  void loadData()
}

function changePage(page: number): void {
  closeDetail()
  scrollContentToTop()
  handleTableChange({ current: page, pageSize: pagination.pageSize })
}

function scrollContentToTop(): void {
  contentRef.value?.scrollTo({ top: 0 })
}

function refreshData(): void {
  closeDetail()
  if (pageActive && document.visibilityState === 'visible') void loadData()
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
  if (status === 'quality_isolated') return '质量隔离'
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

async function openHourDetail(account: AiHealthAccountRow, point: AiHealthHourPoint): Promise<void> {
  requestCoordinator.cancelDetail()
  detailOpen.value = true
  detailLoading.value = false
  detailError.value = ''
  selectedDetail.value = { accountId: account.id, accountName: account.name, point }
  if (point.status === 'unknown') return

  const token = requestCoordinator.beginDetail()
  detailLoading.value = true
  try {
    const request = isManagementView.value ? api.stats.aiHealthHourDetail : api.myStats.aiHealthHourDetail
    const detail = await request({
      accountId: account.id,
      statHour: point.statHour,
      systemAccountId: scopedSystemAccountId()
    }, { signal: token.signal })
    if (!token.isCurrent() || !selectedDetailMatches(account.id, point.statHour)) return
    selectedDetail.value = { accountId: account.id, accountName: account.name, point: detail }
  } catch (error) {
    if (isAiHealthCanceledRequest(error) || !token.isCurrent() || !selectedDetailMatches(account.id, point.statHour)) return
    detailError.value = extractApiErrorMessage(error, '加载检查详情失败')
  } finally {
    if (token.isCurrent() && selectedDetailMatches(account.id, point.statHour)) detailLoading.value = false
  }
}

function setDetailOpen(open: boolean): void {
  if (open) detailOpen.value = true
  else closeDetail()
}

function closeDetail(): void {
  requestCoordinator.cancelDetail()
  detailOpen.value = false
  detailLoading.value = false
  detailError.value = ''
  selectedDetail.value = undefined
}

function selectedDetailMatches(accountId: string, statHour: string): boolean {
  return detailOpen.value
    && selectedDetail.value?.accountId === accountId
    && selectedDetail.value.point.statHour === statHour
}

function detailReason(point: AiHealthHourDetail): string {
  if (point.status === 'unknown') return '该小时没有检查记录'
  if (point.status === 'success') return '-'
  return point.errorMessage || point.errorCode || '检查未成功，未返回具体原因'
}

function supersededPage(page: number, currentPageSize: number) {
  return { items: [], total: 0, hasMore: false, page, pageSize: currentPageSize, superseded: true }
}

function knownPageLowerBound(page: number, currentPageSize: number, itemCount: number, pageHasMore: boolean): number {
  return (page - 1) * currentPageSize + itemCount + (pageHasMore ? 1 : 0)
}

function handleVisibilityChange(): void {
  if (document.visibilityState === 'hidden') {
    invalidateInitialVisibleLoad()
    requestCoordinator.cancelList()
    requestCoordinator.cancelDetail()
    invalidatePendingLoads()
    return
  }
  loadInitialVisiblePage()
}

function loadInitialVisiblePage(): void {
  if (
    initialVisibleLoadStarted
    || initialVisibleLoadCompleted
    || accounts.value.length > 0
    || !pageActive
    || document.visibilityState !== 'visible'
  ) return
  initialVisibleLoadStarted = true
  const generation = initialVisibleLoadGeneration
  void loadData().finally(() => {
    if (generation !== initialVisibleLoadGeneration) return
    initialVisibleLoadStarted = false
    if (pageActive && document.visibilityState === 'visible') initialVisibleLoadCompleted = true
  })
}

function invalidateInitialVisibleLoad(): void {
  initialVisibleLoadGeneration += 1
  initialVisibleLoadStarted = false
}

onMounted(() => {
  pageActive = true
  document.addEventListener('visibilitychange', handleVisibilityChange)
  loadInitialVisiblePage()
})

onDeactivated(() => {
  pageActive = false
  invalidateInitialVisibleLoad()
  requestCoordinator.cancelList()
  requestCoordinator.cancelDetail()
  invalidatePendingLoads()
})

onActivated(() => {
  pageActive = true
  loadInitialVisiblePage()
})

watch(() => authState.revision.value, () => {
  invalidateInitialVisibleLoad()
  requestCoordinator.cancelList()
  invalidatePendingLoads()
  closeDetail()
  accounts.value = []
  pagination.current = 1
  pagination.total = 0
  hasMore.value = false
})

onBeforeUnmount(() => {
  pageActive = false
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  requestCoordinator.dispose()
  invalidatePendingLoads()
})
</script>

<style scoped>
.ai-health-range-select { width: 148px; }
.ai-health-legend { display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap; gap: 14px; color: #64748b; font-size: 13px; white-space: nowrap; }
.ai-health-legend span { display: inline-flex; align-items: center; gap: 6px; }
.ai-health-legend i { width: 7px; height: 18px; border-radius: 2px; }
.ai-health-legend .success { background: #10b981; }
.ai-health-legend .failure { background: #ef4444; }
.ai-health-legend .unknown { background: #d7dde5; }
.ai-health-content { min-height: 0; flex: 1 1 auto; overflow-y: auto; overscroll-behavior: contain; padding-right: 4px; scrollbar-gutter: stable; display: flex; position: relative; flex-direction: column; }
.ai-health-content :deep(.ant-spin-nested-loading), .ai-health-content :deep(.ant-spin-container) { display: flex; flex: 1 1 auto; min-height: 0; flex-direction: column; }
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
.ai-health-detail-error { margin-bottom: 12px; }
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
