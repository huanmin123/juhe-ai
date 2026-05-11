<template>
  <div class="ai-performance-page">
    <a-card class="page-card ai-performance-header-card">
      <div class="page-toolbar ai-performance-toolbar">
        <a-space class="ai-performance-filters" :size="12" wrap>
          <a-segmented v-model:value="selectedWindow" class="ai-performance-window-segmented" :options="windowOptions" :disabled="loading" @change="handleWindowChange" />
          <a-select
            v-model:value="selectedAccountIds"
            class="ai-performance-account-select"
            mode="multiple"
            allow-clear
            show-search
            :max-tag-count="3"
            :options="accountOptions"
            :loading="accountsLoading"
            :disabled="loading"
            :filter-option="false"
            placeholder="临时指定账户"
            @change="handleAccountChange"
            @search="handleAccountSearch"
            @dropdown-visible-change="handleAccountDropdownVisibleChange"
          />
        </a-space>
        <div class="page-toolbar-actions">
          <a-button :disabled="!selectedAccountIds.length || loading" @click="resetSelectedAccounts">重置指定</a-button>
          <a-button :loading="loading" @click="loadPerformance">
            <template #icon>
              <ReloadOutlined />
            </template>
            刷新
          </a-button>
        </div>
      </div>
      <div class="ai-performance-tip">
        默认显示最近 7 天活跃度最高的前 10 个自有 AI 账户；临时指定仅本次查看生效。
      </div>
    </a-card>

    <StatsSummaryCards :cards="summaryCards" :loading="initialLoading" />

    <a-row :gutter="[16, 16]" class="ai-performance-section">
      <a-col :xs="24">
        <StatsChartCard :title="`首token 耗时监控图（${currentWindowLabel}）`" :loading="initialLoading" :has-data="hasFirstTokenData" :empty-description="firstTokenEmptyDescription">
          <div ref="firstTokenChartRef" class="chart-panel" />
        </StatsChartCard>
      </a-col>
      <a-col :xs="24">
        <StatsChartCard :title="`总耗时 监控图（${currentWindowLabel}）`" :loading="initialLoading" :has-data="hasDurationData" :empty-description="durationEmptyDescription">
          <div ref="durationChartRef" class="chart-panel" />
        </StatsChartCard>
      </a-col>
    </a-row>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import type { Ref, ShallowRef } from 'vue'
import { ReloadOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import { init, type ECharts } from '@/lib/echarts'
import type { AccountStatus, AiPerformanceAccountOption, AiPerformanceOverview, AiPerformanceWindowKey } from '@/types/domain'
import StatsChartCard from '@/views/stats/StatsChartCard.vue'
import StatsSummaryCards from '@/views/stats/StatsSummaryCards.vue'
import { formatDuration, formatInteger, formatSeconds } from '@/views/stats/statsFormatters'
import { buildAiPerformanceOption } from './aiPerformanceChartOptions'

const windowOptions: Array<{ label: string; value: AiPerformanceWindowKey }> = [
  { label: '近一天', value: 'last1d' },
  { label: '近三天', value: 'last3d' },
  { label: '近一周', value: 'last7d' }
]

const selectedWindow = ref<AiPerformanceWindowKey>('last1d')
const selectedAccountIds = ref<string[]>([])
const overview = ref<AiPerformanceOverview>()
const accounts = ref<AiPerformanceAccountOption[]>([])
const loading = ref(false)
const accountsLoading = ref(false)
const accountSearchKeyword = ref('')
let accountSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let accountSearchSeq = 0

const firstTokenChartRef = ref<HTMLDivElement>()
const durationChartRef = ref<HTMLDivElement>()
const firstTokenChart = shallowRef<ECharts>()
const durationChart = shallowRef<ECharts>()

const hasOverview = computed(() => Boolean(overview.value))
const initialLoading = computed(() => loading.value && !hasOverview.value)
const currentWindowLabel = computed(() => overview.value?.window.label ?? windowOptions.find((item) => item.value === selectedWindow.value)?.label ?? '近一天')
const hasAccounts = computed(() => (overview.value?.accounts.length ?? 0) > 0)
const hasFirstTokenData = computed(() => (overview.value?.hourlySeries ?? []).some((series) => series.points.some((point) => point.averageFirstTokenMs !== undefined)))
const hasDurationData = computed(() => (overview.value?.hourlySeries ?? []).some((series) => series.points.some((point) => point.averageDurationMs !== undefined)))
const firstTokenEmptyDescription = computed(() => hasAccounts.value ? `${currentWindowLabel.value}暂无首 token 样本` : '最近 7 天暂无活跃 AI 账户')
const durationEmptyDescription = computed(() => hasAccounts.value ? `${currentWindowLabel.value}暂无总耗时样本` : '最近 7 天暂无活跃 AI 账户')

const accountOptions = computed(() => accounts.value
  .map((account) => ({
    label: accountOptionLabel(account),
    value: account.id
  })))

const summaryCards = computed(() => {
  const summary = overview.value?.summary
  return [
    { key: 'accounts', label: '展示账户', value: formatInteger(overview.value?.accounts.length), extra: `默认 ${formatInteger(overview.value?.defaultAccounts.length)} / 指定 ${formatInteger(overview.value?.selectedAccounts.length)}` },
    { key: 'requests', label: `${currentWindowLabel.value}请求`, value: formatInteger(summary?.requestCount), extra: `统计滞后 ${formatSeconds(overview.value?.statsLagSeconds)}` },
    { key: 'firstToken', label: '平均首 token', value: formatDuration(summary?.averageFirstTokenMs), extra: `样本 ${formatInteger(summary?.firstTokenCount)}` },
    { key: 'duration', label: '平均总耗时', value: formatDuration(summary?.averageDurationMs), extra: `样本 ${formatInteger(summary?.durationCount)}` }
  ]
})

async function loadPerformance() {
  loading.value = true
  try {
    overview.value = await api.myStats.aiPerformance({
      window: selectedWindow.value,
      accountIds: selectedAccountIds.value
    })
  } catch (error) {
    console.error(error)
    message.error('AI性能监控数据加载失败')
  } finally {
    loading.value = false
    renderCharts()
  }
}

async function loadAccounts() {
  const requestSeq = ++accountSearchSeq
  accountsLoading.value = true
  try {
    const keyword = accountSearchKeyword.value.trim()
    const nextAccounts = await api.myStats.aiPerformanceAccounts({
      keyword,
      accountIds: selectedAccountIds.value,
      limit: 30
    })
    if (requestSeq !== accountSearchSeq) return
    accounts.value = nextAccounts
  } catch (error) {
    console.error(error)
    message.error('AI账户列表加载失败')
  } finally {
    if (requestSeq === accountSearchSeq) {
      accountsLoading.value = false
    }
  }
}

function handleWindowChange(value: string | number) {
  selectedWindow.value = value as AiPerformanceWindowKey
  void loadPerformance()
}

function handleAccountChange(value: unknown) {
  const ids = Array.isArray(value) ? value.map((item) => String(item)) : []
  if (ids.length > 20) {
    selectedAccountIds.value = ids.slice(0, 20)
    message.warning('临时指定账户最多选择 20 个')
    void loadAccounts()
  } else {
    selectedAccountIds.value = ids
    void loadAccounts()
  }
  void loadPerformance()
}

function handleAccountSearch(value: string) {
  accountSearchKeyword.value = value
  if (accountSearchTimer) window.clearTimeout(accountSearchTimer)
  accountSearchTimer = window.setTimeout(() => {
    void loadAccounts()
  }, 250)
}

function handleAccountDropdownVisibleChange(open: boolean) {
  if (open) {
    void loadAccounts()
  }
}

function resetSelectedAccounts() {
  selectedAccountIds.value = []
  void loadAccounts()
  void loadPerformance()
}

function renderCharts() {
  void nextTick(() => {
    renderFirstTokenChart()
    renderDurationChart()
    resizeCharts()
  })
}

function renderFirstTokenChart() {
  if (!overview.value || !hasFirstTokenData.value) {
    disposeChart(firstTokenChart)
    return
  }
  const chart = ensureChart(firstTokenChartRef, firstTokenChart)
  if (!chart) return
  chart.setOption(buildAiPerformanceOption(overview.value, 'firstToken'), { notMerge: true })
}

function renderDurationChart() {
  if (!overview.value || !hasDurationData.value) {
    disposeChart(durationChart)
    return
  }
  const chart = ensureChart(durationChartRef, durationChart)
  if (!chart) return
  chart.setOption(buildAiPerformanceOption(overview.value, 'duration'), { notMerge: true })
}

function ensureChart(elementRef: Ref<HTMLDivElement | undefined>, chartRef: ShallowRef<ECharts | undefined>) {
  const element = elementRef.value
  if (!element) return undefined
  if (!chartRef.value || chartRef.value.isDisposed()) {
    chartRef.value = init(element)
  }
  return chartRef.value
}

function disposeChart(chartRef: ShallowRef<ECharts | undefined>) {
  if (chartRef.value && !chartRef.value.isDisposed()) {
    chartRef.value.dispose()
  }
  chartRef.value = undefined
}

function resizeCharts() {
  for (const chart of [firstTokenChart.value, durationChart.value]) {
    if (chart && !chart.isDisposed()) chart.resize()
  }
}

function accountOptionLabel(account: AiPerformanceAccountOption) {
  const statusText = account.status === 'active' ? '' : `（${accountStatusText(account.status)}）`
  return `${account.name}${statusText} · 近7天 ${formatInteger(account.requestCountLast7d)} 次`
}

function accountStatusText(status: AccountStatus) {
  const labels: Record<AccountStatus, string> = {
    active: '正常',
    disabled: '已停用',
    error: '异常',
    rate_limited: '限流',
    temporary_unavailable: '临时不可用'
  }
  return labels[status] ?? status
}

onMounted(() => {
  window.addEventListener('resize', resizeCharts)
  void loadAccounts()
  void loadPerformance()
})

onBeforeUnmount(() => {
  window.removeEventListener('resize', resizeCharts)
  if (accountSearchTimer) window.clearTimeout(accountSearchTimer)
  disposeChart(firstTokenChart)
  disposeChart(durationChart)
})
</script>

<style scoped>
.ai-performance-page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.ai-performance-header-card :deep(.ant-card-body) {
  padding: 16px 18px;
}

.ai-performance-toolbar {
  margin: 0;
}

.ai-performance-filters {
  flex: 1 1 520px;
  min-width: 0;
}

.ai-performance-window-segmented {
  width: max-content;
  max-width: 100%;
}

.ai-performance-account-select {
  width: min(520px, 100%);
}

.ai-performance-tip {
  margin-top: 10px;
  color: #64748b;
  font-size: 13px;
}

.ai-performance-section {
  margin-top: 0;
}

.chart-panel {
  width: 100%;
  height: 360px;
}

@media (max-width: 768px) {
  .ai-performance-window-segmented,
  .ai-performance-account-select {
    width: 100%;
    min-width: 0;
  }

  .chart-panel {
    height: 300px;
  }
}
</style>
