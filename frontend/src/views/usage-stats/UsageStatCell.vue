<template>
  <div class="usage-stat-cell" :class="{ 'usage-stat-cell-compact': compact }">
    <a-tag class="usage-summary-tag">{{ `${formatInteger(usage?.requestCount)}req` }}</a-tag>
    <a-tag class="usage-summary-tag">{{ formatCompactUsageAmount(usage?.totalTokens) }}</a-tag>
    <a-tag class="usage-summary-tag">{{ formatUsd(usage?.totalCost) }}</a-tag>
  </div>
</template>

<script setup lang="ts">
import { formatCompactUsageAmount, formatInteger, formatUsd } from '@/shared/formatters'
import type { AccountUsageSummary } from '@/types/domain'

defineProps<{
  compact?: boolean
  usage?: AccountUsageSummary
}>()
</script>

<style scoped>
.usage-stat-cell {
  display: inline-grid;
  grid-template-columns: repeat(3, 1fr);
  align-items: center;
  gap: 4px;
  width: 150px;
  min-width: 0;
}

.usage-summary-tag {
  min-width: 0;
  margin-inline-end: 0;
  padding-inline: 5px;
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-align: center;
  white-space: nowrap;
}

.usage-stat-cell-compact {
  display: flex;
  width: 58px;
  flex-direction: column;
  align-items: stretch;
  gap: 3px;
}

.usage-stat-cell-compact .usage-summary-tag {
  width: 100%;
  line-height: 20px;
}
</style>
