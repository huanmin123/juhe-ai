<template>
  <div class="usage-summary-tags" :class="{ 'usage-summary-tags-compact': compact }">
    <a-tag class="usage-summary-tag">{{ formatRequestCountTag(usage?.requestCount) }}</a-tag>
    <a-tag class="usage-summary-tag">{{ formatCompactUsageAmount(usage?.totalTokens) }}</a-tag>
    <a-tag class="usage-summary-tag">{{ formatUsd(usage?.totalCost) }}</a-tag>
    <a-tag v-if="showCacheRate && cacheRate != null" class="usage-summary-tag" :color="cacheRateTone(cacheRate)">{{ cacheRateTagText(cacheRate) }}</a-tag>
  </div>
</template>

<script setup lang="ts">
import { formatCompactUsageAmount, formatRequestCountTag, formatUsd } from '@/shared/formatters'

import { cacheRateTagText, cacheRateTone } from './usageCacheRateTag'

defineProps<{
  compact?: boolean
  usage?: {
    requestCount?: number
    totalCost?: number
    totalTokens?: number
    cacheWriteTokens?: number
    cacheWrite1hTokens?: number
    thinkingTokens?: number
    inputImageTokens?: number
    outputImageTokens?: number
  }
  showCacheRate?: boolean
  cacheRate?: number | null
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
  color: var(--juhe-fg);
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
