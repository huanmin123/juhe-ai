<template>
  <span v-if="!record.success" class="result-cell">
    <a-popover trigger="hover" placement="right" overlay-class-name="usage-error-popover">
      <template #content>
        <div class="usage-error-message">{{ errorText(record) }}</div>
      </template>
      <InfoCircleOutlined class="usage-error-icon" />
    </a-popover>
    <a-tag color="red">失败</a-tag>
  </span>
  <span v-else class="result-cell">
    <a-popover v-if="codexGuardStatus" trigger="hover" placement="right">
      <template #content>
        <div class="usage-guard-message">{{ codexGuardStatus.detail }}</div>
      </template>
      <InfoCircleOutlined class="usage-guard-icon" />
    </a-popover>
    <a-tag :color="codexGuardStatus ? 'gold' : 'green'">{{ codexGuardStatus?.label ?? '成功' }}</a-tag>
  </span>
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import type { UsageRecordSummary } from '@/types/domain'
import { errorText, usageRecordCodexGuardStatus } from './usageRecordFormatters'

const props = defineProps<{
  record: UsageRecordSummary
}>()
const codexGuardStatus = computed(() => usageRecordCodexGuardStatus(props.record))
</script>

<style scoped>
.result-cell {
  display: inline-flex;
  gap: 6px;
  align-items: center;
}

.usage-error-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 13px;
}

.usage-error-icon:hover {
  color: #dc2626;
}

.usage-error-message {
  max-width: 460px;
  max-height: 180px;
  overflow: auto;
  color: #fca5a5;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

.usage-guard-icon {
  color: #d97706;
  cursor: help;
  font-size: 13px;
}

.usage-guard-message {
  max-width: 460px;
  color: #78350f;
  font-size: 12px;
  line-height: 1.6;
}

:global(.usage-error-popover .ant-popover-inner) {
  background: #0f172a;
  border-radius: 8px;
  box-shadow: 0 10px 24px rgb(15 23 42 / 24%);
}

:global(.usage-error-popover .ant-popover-arrow::before) {
  background: #0f172a;
}
</style>
