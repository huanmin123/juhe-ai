<template>
  <div class="usage-summary-tags" :class="{ 'usage-summary-tags-compact': compact }">
    <a-tag class="usage-summary-tag">{{ `${formatInteger(usage?.requestCount)}req` }}</a-tag>
    <a-tag class="usage-summary-tag">{{ formatCompactUsageAmount(usage?.totalTokens) }}</a-tag>
    <a-tag class="usage-summary-tag">{{ formatUsd(usage?.totalCost) }}</a-tag>
  </div>
</template>

<script setup lang="ts">
import { formatCompactUsageAmount, formatInteger, formatUsd } from '@/shared/formatters'

defineProps<{
  compact?: boolean
  usage?: {
    requestCount?: number
    totalCost?: number
    totalTokens?: number
  }
}>()
</script>

<style scoped>
.usage-summary-tags {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px;
  min-width: 0;
  max-width: 100%;
  white-space: normal;
}

.usage-summary-tag {
  display: inline-flex;
  justify-content: center;
  max-width: 100%;
  margin-inline-end: 0;
  padding-inline: 6px;
  overflow: hidden;
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-summary-tags-compact {
  display: inline-grid;
  grid-template-columns: 1fr;
  align-items: stretch;
  width: max-content;
}

.usage-summary-tags-compact .usage-summary-tag {
  width: 100%;
}
</style>
