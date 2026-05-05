<template>
  <div class="status-cell">
    <a-tooltip v-if="tooltipLines.length" placement="topLeft">
      <template #title>
        <div class="status-tooltip">
          <div v-for="line in tooltipLines" :key="line">{{ line }}</div>
        </div>
      </template>
      <StatusTag class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
    </a-tooltip>
    <StatusTag v-else class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import StatusTag from '@/components/StatusTag.vue'
import type { AccountSummary } from '@/types/domain'
import { accountStatusColor, accountStatusText, accountStatusTooltipLines } from './accountFormatters'

const props = defineProps<{
  account: AccountSummary
}>()

const tooltipLines = computed(() => accountStatusTooltipLines(props.account))
</script>

<style scoped>
.status-cell {
  display: inline-flex;
  align-items: center;
}

.status-tag {
  width: max-content;
  max-width: 100%;
  margin-inline-end: 0;
  white-space: nowrap;
}

.status-tooltip {
  max-width: 320px;
  line-height: 1.7;
  white-space: pre-wrap;
}
</style>
