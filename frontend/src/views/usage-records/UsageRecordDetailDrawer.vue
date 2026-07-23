<template>
  <a-drawer :open="open" title="使用记录详情" width="min(920px, 96vw)" @update:open="emit('update:open', $event)">
    <a-spin :spinning="loading">
      <template v-if="detail">
        <a-descriptions bordered size="small" :column="2">
          <a-descriptions-item label="时间">{{ formatDateTime(detail.createdAt) }}</a-descriptions-item>
          <a-descriptions-item label="traceId"><span class="mono-cell">{{ detail.traceId }}</span></a-descriptions-item>
          <a-descriptions-item label="模型">{{ detail.model ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="接口">{{ detail.endpoint ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="结果">{{ detail.success ? '成功' : '失败' }} / {{ detail.statusCode ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="错误">{{ detail.errorMessage ?? '-' }}</a-descriptions-item>
        </a-descriptions>
        <a-tabs>
          <a-tab-pane key="request" tab="请求快照"><pre class="snapshot">{{ json(detail.requestSnapshot) }}</pre></a-tab-pane>
          <a-tab-pane key="response" tab="响应快照"><pre class="snapshot">{{ json(detail.responseSnapshot) }}</pre></a-tab-pane>
        </a-tabs>
      </template>
      <a-empty v-else-if="!loading" description="未找到使用记录详情。" />
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import type { UsageRecordSummary } from '@/types/domain'
import { formatDateTime } from '@/shared/formatters'

defineProps<{ open: boolean; loading: boolean; detail?: UsageRecordSummary }>()
const emit = defineEmits<{ (event: 'update:open', open: boolean): void }>()

function json(value: unknown): string {
  return value === undefined ? '-' : JSON.stringify(value, null, 2)
}
</script>

<style scoped>
.snapshot { max-height: 55vh; overflow: auto; padding: 12px; background: #f8fafc; white-space: pre-wrap; word-break: break-word; }
</style>
