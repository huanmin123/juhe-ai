<template>
  <div class="usage-cell">
    <UsageSummaryTags :usage="account.todayUsage" />
    <div v-if="account.balanceQueryEnabled" class="balance-row">
      <a-tooltip v-if="balanceDisplay.visible" :title="balanceDisplay.tooltip">
        <span class="balance-text">
          <span v-if="balanceDisplay.tone !== 'failed'" class="balance-label">剩余：</span>
          <span class="balance-value" :class="`balance-${balanceDisplay.tone}`">{{ balanceDisplay.text }}</span>
        </span>
      </a-tooltip>
      <a-tooltip :title="canRefresh ? '刷新上游余额' : '授权账户不能刷新来源账户余额'">
        <ReloadOutlined
          class="balance-refresh-icon"
          :class="{ spinning: refreshing || balanceDisplay.refreshing, disabled: !canRefresh }"
          @click="canRefresh && !refreshing && $emit('refresh-balance', account.id)"
        />
      </a-tooltip>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ReloadOutlined } from '@ant-design/icons-vue'
import { computed } from 'vue'

import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import type { AccountListItem } from '@/types/domain'
import { canManuallyRefreshAccountBalance, formatAccountBalance } from './accountBalanceQuery'

const props = defineProps<{
  account: AccountListItem
  refreshing?: boolean
}>()

defineEmits<{ (event: 'refresh-balance', accountId: string): void }>()

const balanceDisplay = computed(() => formatAccountBalance(props.account.balanceSnapshot))
const canRefresh = computed(() => canManuallyRefreshAccountBalance(props.account))
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

.balance-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-height: 20px;
}

.balance-text {
  font-size: 12px;
  white-space: nowrap;
}

.balance-label {
  color: #64748b;
}

.balance-failed { color: #dc2626; }
.balance-fresh { color: #15803d; }
.balance-pending,
.balance-refreshing,
.balance-unsupported { color: #64748b; }

.balance-refresh-icon {
  color: #1677ff;
  cursor: pointer;
  font-size: 11px;
}

.balance-refresh-icon:hover { color: #0958d9; }

.balance-refresh-icon.disabled {
  color: #b8b8b8;
  cursor: not-allowed;
}

.balance-refresh-icon.spinning {
  animation: balance-spin 0.8s linear infinite;
  pointer-events: none;
}

@keyframes balance-spin {
  to { transform: rotate(360deg); }
}

</style>
