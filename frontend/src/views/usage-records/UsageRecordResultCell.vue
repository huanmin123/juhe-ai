<template>
  <span class="usage-result-cell">
    <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
    <span v-if="record.failureReason" class="usage-failure-reason">{{ record.failureReason }}</span>
    <span v-if="failureAttribution" class="usage-failure-attribution">{{ failureAttribution }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import type { UsageRecordListItem } from '@/types/domain'
import { usageRecordFailureAttributionText } from './usageRecordFormatters'

const props = defineProps<{
  record: UsageRecordListItem
}>()

const failureAttribution = computed(() => usageRecordFailureAttributionText(props.record))
</script>

<style scoped>
.usage-result-cell {
  display: inline-flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 3px;
  min-width: 0;
}

.usage-failure-reason,
.usage-failure-attribution {
  max-width: 280px;
  color: #b42318;
  font-size: 12px;
  line-height: 1.35;
  overflow-wrap: anywhere;
  white-space: normal;
}

.usage-failure-attribution {
  color: #667085;
}
</style>
