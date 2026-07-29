<template>
  <span class="cost-cell">{{ formatCost(record.costUsd) }}</span>
</template>

<script setup lang="ts">
import type { UsageRecordListItem } from '@/types/domain'
import { formatCost } from './usageRecordFormatters'

defineProps<{
  record: UsageRecordListItem
}>()
</script>

<style scoped>
.cost-cell {
  color: #059669;
  font-family: Consolas, 'Courier New', monospace;
}

</style>
