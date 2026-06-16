<template>
  <div class="usage-cell">
    <UsageSummaryTags :usage="account.todayUsage" />
    <div v-if="bars.length" class="oauth-usage-bars">
      <div v-for="bar in bars" :key="bar.key" class="oauth-usage-row">
        <span class="oauth-usage-label">{{ bar.label }}</span>
        <a-progress class="oauth-usage-progress" size="small" :percent="bar.percent" :stroke-color="bar.color" :show-info="false" />
        <span class="oauth-usage-percent" :class="bar.tone">{{ bar.displayPercent }}</span>
        <span class="oauth-usage-reset">{{ bar.resetText }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import type { AccountSummary } from '@/types/domain'
import { oauthUsageBars } from './accountUsageFormatters'

const props = defineProps<{
  account: AccountSummary
}>()

const bars = computed(() => oauthUsageBars(props.account))
</script>

<style scoped>
.usage-cell {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 5px;
  line-height: 1.4;
  white-space: normal;
}

.oauth-usage-bars {
  display: flex;
  flex-direction: column;
  gap: 3px;
  width: min(220px, 100%);
  min-width: 150px;
}

.oauth-usage-row {
  display: grid;
  grid-template-columns: 24px minmax(0, 1fr) 36px 44px;
  align-items: center;
  column-gap: 4px;
}

.oauth-usage-label {
  display: inline-flex;
  justify-content: center;
  border-radius: 999px;
  background: #eef2ff;
  color: #4338ca;
  font-size: 11px;
  font-weight: 600;
}

.oauth-usage-progress {
  line-height: 1;
  min-width: 0;
}

.oauth-usage-percent {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-align: right;
}

.oauth-usage-percent.normal {
  color: #475569;
}

.oauth-usage-percent.warning {
  color: #d97706;
}

.oauth-usage-percent.danger {
  color: #dc2626;
}

.oauth-usage-reset {
  color: #64748b;
  font-size: 12px;
  white-space: nowrap;
}
</style>
