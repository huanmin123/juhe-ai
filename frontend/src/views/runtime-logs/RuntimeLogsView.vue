<template>
  <a-card class="page-card responsive-page-card">
    <template v-if="viewMode === 'index'">
      <ResponsiveListToolbar
        v-model:keyword="traceIdFilter"
        search-placeholder="搜索 traceId"
        filter-title="日志筛选"
        :active-filter-count="activeFilterCount"
        :refresh-loading="loading"
        @refresh="loadData"
        @reset="resetFilters"
        @search="applyIndexFilters"
      >
        <template #inline-filters>
          <a-select v-model:value="levelFilter" class="toolbar-select log-level-filter responsive-list-inline-filter" :options="levelOptions" @change="applyIndexFilters" />
          <a-select
            v-model:value="eventFilter"
            allow-clear
            show-search
            class="toolbar-select runtime-event-filter responsive-list-inline-filter"
            placeholder="事件"
            :options="eventOptions"
            @change="applyIndexFilters"
          />
          <a-input v-model:value="keywordFilter" allow-clear class="toolbar-select log-keyword-filter responsive-list-inline-filter" placeholder="关键字" @press-enter="applyIndexFilters" />
        </template>
        <template #actions>
          <a-segmented v-model:value="viewMode" class="log-mode-segmented" :options="viewModeOptions" @change="handleModeChange" />
        </template>
        <template #filters>
          <a-form layout="vertical">
            <a-form-item label="级别">
              <a-select v-model:value="levelFilter" :options="levelOptions" />
            </a-form-item>
            <a-form-item label="事件">
              <a-select v-model:value="eventFilter" allow-clear show-search :options="eventOptions" placeholder="选择或输入事件" />
            </a-form-item>
            <a-form-item label="关键字">
              <a-input v-model:value="keywordFilter" allow-clear placeholder="错误摘要或日志内容" />
            </a-form-item>
            <a-form-item label="时间范围">
              <a-range-picker v-model:value="timeRangeFilter" show-time class="runtime-range-picker" />
            </a-form-item>
          </a-form>
        </template>
      </ResponsiveListToolbar>

      <RuntimeLogDataList
        table-class="page-table runtime-log-table"
        :records="records"
        :loading="loading"
        :pagination="tablePagination"
        empty-description="最近 3 天暂无匹配运行日志。可先用 traceId、级别或事件缩小范围。"
        mobile-pagination
        :mobile-has-more="mobileHasMore"
        :loading-more="mobileLoadingMore"
        :refreshing="loading"
        @change="handleTableChange"
        @detail="openRuntimeLogDetail"
        @mobile-load-more="loadMoreMobileRecords"
        @mobile-refresh="refreshMobileRecords"
        @trace="searchTrace"
      />
    </template>

    <template v-else>
      <ResponsiveListToolbar
        v-model:keyword="grepKeywordFilter"
        search-placeholder="输入关键字，空格分隔多个关键字"
        :active-filter-count="0"
        :refresh-loading="loading"
        :show-filters="false"
        @refresh="searchGrepLogs"
        @reset="resetGrepSearch"
        @search="searchGrepLogs"
      >
        <template #actions>
          <a-segmented v-model:value="viewMode" class="log-mode-segmented" :options="viewModeOptions" @change="handleModeChange" />
          <a-button type="primary" :loading="loading" @click="searchGrepLogs">
            <template #icon>
              <SearchOutlined />
            </template>
            搜索
          </a-button>
        </template>
      </ResponsiveListToolbar>

      <a-alert
        v-if="grepResult?.message"
        :type="grepResult.available === false || grepResult.truncated ? 'warning' : 'info'"
        show-icon
        :message="grepResult.message"
        class="grep-alert"
      >
        <template v-if="grepResult.installSteps?.length" #description>
          <ul class="install-list">
            <li v-for="step in grepResult.installSteps" :key="step">{{ step }}</li>
          </ul>
        </template>
      </a-alert>

      <RuntimeLogDataList
        table-class="page-table grep-table"
        :records="grepRecords"
        :loading="loading"
        :empty-description="grepKeywordFilter.trim() ? '没有匹配的日志行。' : '输入关键字后搜索文件日志。'"
        action-label="查看"
        message-mode="grep"
        :refreshing="loading"
        @detail="openRuntimeGrepDetail"
        @mobile-refresh="searchGrepLogs"
        @trace="searchTrace"
      />
    </template>

    <a-drawer v-model:open="detailOpen" width="min(920px, 96vw)" title="运行日志详情" :body-style="{ padding: '18px' }">
      <template v-if="selectedLog">
        <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
          <a-descriptions-item label="时间">{{ formatDateTime(selectedLog.time) }}</a-descriptions-item>
          <a-descriptions-item label="级别">{{ levelText(selectedLog.level) }}</a-descriptions-item>
          <a-descriptions-item label="traceId" :span="2">{{ selectedLog.traceId ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="事件">{{ selectedLog.event ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="消息" :span="2">{{ selectedLog.errorMessage || selectedLog.message || '-' }}</a-descriptions-item>
        </a-descriptions>
        <pre class="raw-block">{{ prettyRawJson(selectedLog.rawJson) }}</pre>
      </template>
    </a-drawer>

    <a-drawer v-model:open="grepDetailOpen" width="min(920px, 96vw)" title="grep 匹配行" :body-style="{ padding: '18px' }">
      <template v-if="selectedGrepItem">
        <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
          <a-descriptions-item label="时间">{{ formatDateTime(selectedGrepItem.time) }}</a-descriptions-item>
          <a-descriptions-item label="级别">{{ levelText(selectedGrepItem.level) }}</a-descriptions-item>
          <a-descriptions-item label="traceId" :span="2">{{ selectedGrepItem.traceId ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="事件">{{ selectedGrepItem.event ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="消息">{{ selectedGrepItem.errorMessage || selectedGrepItem.message || '-' }}</a-descriptions-item>
          <a-descriptions-item label="文件">{{ selectedGrepItem.fileName || selectedGrepItem.file }}</a-descriptions-item>
          <a-descriptions-item label="位置">{{ grepLinePositionText(selectedGrepItem) }}</a-descriptions-item>
          <a-descriptions-item label="完整路径" :span="2">{{ selectedGrepItem.file }}</a-descriptions-item>
        </a-descriptions>
        <pre class="raw-block">{{ prettyRawJson(selectedGrepItem.rawJson || selectedGrepItem.line) }}</pre>
      </template>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
import dayjs from 'dayjs'
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref } from 'vue'
import { SearchOutlined } from '@ant-design/icons-vue'

import { api } from '@/api/client'
import type { RuntimeLogFacets, RuntimeLogGrepItem, RuntimeLogGrepResult, RuntimeLogLevel, RuntimeLogSummary } from '@/types/domain'
import { formatDateTime } from '@/shared/formatters'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import {
  grepLinePositionText,
  levelText,
  prettyRawJson,
  splitGrepKeywords
} from './runtimeLogFormatters'
import {
  runtimeLogLevelOptions,
  runtimeLogViewModeOptions
} from './runtimeLogTableColumns'
import RuntimeLogDataList from './RuntimeLogDataList.vue'

type RuntimeLogViewMode = 'index' | 'grep'
type RuntimeLogListRecord = RuntimeLogSummary | RuntimeLogGrepItem

const loading = ref(false)
const mobileLoadingMore = ref(false)
const records = ref<RuntimeLogSummary[]>([])
const grepRecords = ref<RuntimeLogGrepItem[]>([])
const grepResult = ref<RuntimeLogGrepResult>()
const facets = ref<RuntimeLogFacets>()
const selectedLog = ref<RuntimeLogSummary>()
const selectedGrepItem = ref<RuntimeLogGrepItem>()
const detailOpen = ref(false)
const grepDetailOpen = ref(false)
const viewMode = ref<RuntimeLogViewMode>('index')

const traceIdFilter = ref('')
const grepKeywordFilter = ref('')
const levelFilter = ref<RuntimeLogLevel | 'all'>('all')
const eventFilter = ref<string | undefined>()
const keywordFilter = ref('')
const timeRangeFilter = ref<[Dayjs, Dayjs] | undefined>([dayjs().subtract(24, 'hour'), dayjs()])
const pageSize = 100
const pagination = reactive({ current: 1, pageSize, total: 0 })

const viewModeOptions = runtimeLogViewModeOptions
const levelOptions = runtimeLogLevelOptions

const eventOptions = computed(() => (facets.value?.events ?? []).map((event) => ({ label: event, value: event })))
const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  hideOnSinglePage: true,
  showSizeChanger: false,
  showTotal: (total: number) => `共 ${total} 条运行日志`
}))
const mobileHasMore = computed(() => records.value.length < pagination.total)

const activeFilterCount = computed(() => {
  let count = 0
  if (levelFilter.value !== 'all') count += 1
  if (eventFilter.value) count += 1
  if (keywordFilter.value.trim()) count += 1
  if (timeRangeFilter.value?.length) count += 1
  return count
})

function handleModeChange(value: string | number): void {
  const nextMode: RuntimeLogViewMode = value === 'grep' ? 'grep' : 'index'
  viewMode.value = nextMode
  if (nextMode === 'index') {
    if (!records.value.length) {
      void loadData()
    }
    return
  }
  if (grepKeywordFilter.value.trim()) {
    void searchGrepLogs()
  }
}

function applyIndexFilters(): void {
  pagination.current = 1
  void loadData()
}

function resetFilters(): void {
  traceIdFilter.value = ''
  levelFilter.value = 'all'
  eventFilter.value = undefined
  keywordFilter.value = ''
  timeRangeFilter.value = [dayjs().subtract(24, 'hour'), dayjs()]
  pagination.current = 1
  void loadData()
}

function resetGrepSearch(): void {
  grepKeywordFilter.value = ''
  grepRecords.value = []
  grepResult.value = undefined
}

async function loadData(options: { append?: boolean; quiet?: boolean } = {}): Promise<void> {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const [result, nextFacets] = await Promise.all([
      api.runtimeLogs.list({
        page: pagination.current,
        pageSize: pagination.pageSize,
        traceId: traceIdFilter.value || undefined,
        level: levelFilter.value,
        event: eventFilter.value || undefined,
        keyword: keywordFilter.value || undefined,
        startedAt: timeRangeFilter.value?.[0]?.toISOString(),
        endedAt: timeRangeFilter.value?.[1]?.toISOString()
      }),
      api.runtimeLogs.facets()
    ])
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    pagination.total = result.total
    records.value = options.append ? [...records.value, ...result.items] : result.items
    facets.value = nextFacets
  } catch (error) {
    console.error(error)
    message.error('加载运行日志失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
  }
}

function handleTableChange(paginationInfo: unknown): void {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
  pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : pageSize
  void loadData()
}

async function loadMoreMobileRecords(): Promise<void> {
  if (!mobileHasMore.value || mobileLoadingMore.value) return
  mobileLoadingMore.value = true
  pagination.current += 1
  try {
    await loadData({ append: true, quiet: true })
  } finally {
    mobileLoadingMore.value = false
  }
}

async function refreshMobileRecords(): Promise<void> {
  pagination.current = 1
  await loadData()
}

async function searchGrepLogs(): Promise<void> {
  const keywords = splitGrepKeywords(grepKeywordFilter.value)
  if (!keywords.length) {
    grepRecords.value = []
    grepResult.value = undefined
    message.warning('请输入 grep 关键字')
    return
  }

  loading.value = true
  try {
    const result = await api.runtimeLogs.grep({
      keywords: keywords.join(' '),
      limit: 100
    })
    grepResult.value = result
    grepRecords.value = result.items
    if (!result.available) {
      message.warning(result.message || 'grep 模式不可用')
    }
  } catch (error) {
    console.error(error)
    message.error('grep 搜索失败')
  } finally {
    loading.value = false
  }
}

function openDetail(record: RuntimeLogSummary): void {
  selectedLog.value = record
  detailOpen.value = true
}

function openGrepDetail(record: RuntimeLogGrepItem): void {
  selectedGrepItem.value = record
  grepDetailOpen.value = true
}

function openRuntimeLogDetail(record: RuntimeLogListRecord): void {
  openDetail(record as RuntimeLogSummary)
}

function openRuntimeGrepDetail(record: RuntimeLogListRecord): void {
  openGrepDetail(record as RuntimeLogGrepItem)
}

function searchTrace(traceId?: string): void {
  if (!traceId) return
  viewMode.value = 'index'
  traceIdFilter.value = traceId
  pagination.current = 1
  void loadData()
}

onMounted(loadData)
</script>

<style scoped>
.log-mode-segmented {
  flex: none;
}

.runtime-event-filter {
  width: 210px;
}

.log-level-filter {
  width: 108px;
}

.log-keyword-filter {
  width: 240px;
}

.runtime-range-picker {
  width: 100%;
}

.grep-alert {
  margin-bottom: 14px;
}

.install-list {
  margin: 6px 0 0;
  padding-left: 18px;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.raw-block {
  max-height: 520px;
  margin: 0;
  padding: 12px;
  overflow: auto;
  color: #0f172a;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
