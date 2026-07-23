<template>
  <template v-if="viewMode === 'index'">
    <RuntimeLogDataList
      table-class="page-table runtime-log-table"
      :columns="indexColumns"
      :records="records"
      :loading="loading"
      :pagination="pagination"
      empty-description="最近 3 天暂无匹配运行日志。可先用 traceId、级别或事件缩小范围。"
      mobile-pagination
      :mobile-has-more="mobileHasMore"
      :loading-more="mobileLoadingMore"
      :refreshing="loading"
      @change="$emit('change', $event)"
      @detail="$emit('index-detail', $event)"
      @mobile-load-more="$emit('mobile-load-more')"
      @mobile-refresh="$emit('index-mobile-refresh')"
      @trace="$emit('trace', $event)"
    />
  </template>

  <template v-else>
    <RuntimeLogDataList
      table-class="page-table grep-table"
      :columns="grepColumns"
      :records="grepRecords"
      :loading="loading"
      :empty-description="grepKeywordFilter.trim() ? '没有匹配的日志行。' : '输入任意关键字后搜索文件日志。'"
      action-label="查看"
      message-mode="grep"
      :refreshing="loading"
      @detail="$emit('grep-detail', $event)"
      @mobile-refresh="$emit('grep-mobile-refresh')"
      @trace="$emit('trace', $event)"
    />
  </template>
</template>

<script setup lang="ts">
import type { RuntimeLogGrepItem, RuntimeLogGrepResult, RuntimeLogSummary } from '@/types/domain'

import RuntimeLogDataList from './RuntimeLogDataList.vue'

type RuntimeLogListRecord = RuntimeLogSummary | RuntimeLogGrepItem
type RuntimeLogViewMode = 'index' | 'grep'

defineProps<{
  grepColumns: Array<Record<string, unknown>>
  grepKeywordFilter: string
  grepRecords: RuntimeLogGrepItem[]
  grepResult?: RuntimeLogGrepResult
  indexColumns: Array<Record<string, unknown>>
  loading: boolean
  mobileHasMore: boolean
  mobileLoadingMore: boolean
  pagination?: false | Record<string, unknown>
  records: RuntimeLogSummary[]
  viewMode: RuntimeLogViewMode
}>()

defineEmits<{
  (event: 'change', paginationInfo: unknown): void
  (event: 'grep-detail', record: RuntimeLogListRecord): void
  (event: 'grep-mobile-refresh'): void
  (event: 'index-detail', record: RuntimeLogListRecord): void
  (event: 'index-mobile-refresh'): void
  (event: 'mobile-load-more'): void
  (event: 'trace', traceId?: string): void
}>()
</script>

