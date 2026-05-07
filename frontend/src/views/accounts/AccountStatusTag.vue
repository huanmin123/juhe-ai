<template>
  <div class="status-cell">
    <a-tooltip v-if="tooltipLines.length" placement="topLeft">
      <template #title>
        <div class="status-tooltip">
          <div v-for="line in tooltipLines" :key="line">{{ line }}</div>
        </div>
      </template>
      <StatusTag class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
      <StatusTag v-if="account.superPriorityEnabled" class="status-tag priority-tag" color="gold" label="优先" />
    </a-tooltip>
    <template v-else>
      <StatusTag class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
      <a-tooltip v-if="account.superPriorityEnabled" title="超级优先：下次调度优先使用此账户">
        <StatusTag class="status-tag priority-tag" color="gold" label="优先" />
      </a-tooltip>
    </template>
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

const tooltipLines = computed(() => {
  const lines = accountStatusTooltipLines(props.account)
  return props.account.superPriorityEnabled
    ? [...lines, '超级优先：下次调度优先使用此账户']
    : lines
})
</script>

<style scoped>
.status-cell {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.status-tag {
  width: max-content;
  max-width: 100%;
  margin-inline-end: 0;
  white-space: nowrap;
}

.priority-tag {
  flex: none;
}

.status-tooltip {
  max-width: 320px;
  line-height: 1.7;
  white-space: pre-wrap;
}
</style>
