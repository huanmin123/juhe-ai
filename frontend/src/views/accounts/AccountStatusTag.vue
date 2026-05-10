<template>
  <div class="status-cell">
    <a-tooltip v-if="tooltipLines.length" placement="topLeft">
      <template #title>
        <div class="status-tooltip">
          <div v-for="line in tooltipLines" :key="line">{{ line }}</div>
        </div>
      </template>
      <span class="status-tag-group">
        <StatusTag class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
        <StatusTag v-if="account.superPriorityEnabled" class="status-tag priority-tag" color="gold" label="超级优先" />
        <StatusTag v-if="account.fallbackEnabled" class="status-tag priority-tag" color="purple" label="降级备用" />
      </span>
    </a-tooltip>
    <span v-else class="status-tag-group">
      <StatusTag class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
      <a-tooltip v-if="account.superPriorityEnabled" title="超级优先：下次调度优先使用此账户">
        <StatusTag class="status-tag priority-tag" color="gold" label="超级优先" />
      </a-tooltip>
      <a-tooltip v-if="account.fallbackEnabled" title="降级备用：仅在同分组其他可用账户都不可用时使用">
        <StatusTag class="status-tag priority-tag" color="purple" label="降级备用" />
      </a-tooltip>
    </span>
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
  if (props.account.superPriorityEnabled) {
    lines.push('超级优先：下次调度优先使用此账户')
  }
  if (props.account.fallbackEnabled) {
    lines.push('降级备用：仅在同分组其他可用账户都不可用时使用')
  }
  return lines
})
</script>

<style scoped>
.status-cell {
  display: inline-flex;
  align-items: center;
}

.status-tag-group {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  max-width: 100%;
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
