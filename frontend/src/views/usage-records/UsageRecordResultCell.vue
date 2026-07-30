<template>
  <span class="usage-result-cell">
    <a-tooltip v-if="failureDetail" placement="topLeft">
      <template #title>
        <div class="usage-failure-detail">{{ failureDetail }}</div>
      </template>
      <InfoCircleOutlined class="usage-failure-info" aria-label="查看错误详情" />
    </a-tooltip>
    <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { InfoCircleOutlined } from '@ant-design/icons-vue'

import type { UsageRecordListItem } from '@/types/domain'
import { usageRecordFailureAttributionText } from './usageRecordFormatters'

const props = defineProps<{
  record: UsageRecordListItem
}>()

const failureAttribution = computed(() => usageRecordFailureAttributionText(props.record))
const failureDetail = computed(() => {
  if (props.record.success) return undefined
  const reason = props.record.failureReason?.trim()
  if (reason) return reason
  return failureAttribution.value?.replace(/^归因：/u, '').trim()
})
</script>

<style scoped>
.usage-result-cell {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  min-width: 0;
}

.usage-failure-info {
  color: #667085;
  cursor: help;
  font-size: 14px;
}

.usage-failure-detail {
  max-width: 420px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
