<template>
  <div class="usage-cell">
    <UsageSummaryTags :usage="account.todayUsage" />
    <div v-if="account.balanceQueryEnabled" class="balance-row">
      <a-popover
        v-if="isMultiKey && balanceDisplay.visible"
        v-model:open="detailsOpen"
        trigger="click"
        placement="bottomLeft"
        overlay-class-name="account-balance-details-popover"
        @open-change="handleDetailsOpenChange"
      >
        <template #content>
          <div class="balance-details-content" role="dialog" aria-label="API Key 余额明细">
            <div class="balance-details-header">
              <span>余额明细</span>
              <span v-if="detailKeyCount" class="balance-details-count">{{ detailKeyCount }} 个 Key</span>
            </div>
            <div v-if="balanceDetailsLoading" class="balance-details-state">加载中…</div>
            <div v-else-if="balanceDetailsError" class="balance-details-state balance-details-error">
              {{ balanceDetailsError }}
            </div>
            <div v-else-if="resolvedDetails?.keyBalances?.length" class="balance-details-list">
              <div v-for="item in resolvedDetails.keyBalances" :key="item.keyFingerprint" class="balance-details-item">
                <span class="balance-details-key-wrap">
                  <span class="balance-details-key" :title="item.maskedKey">{{ item.maskedKey }}</span>
                  <span class="balance-details-updated">{{ keyBalanceUpdatedText(item) }}</span>
                </span>
                <span class="balance-details-item-value" :class="`balance-${keyBalanceTone(item)}`">{{ keyBalanceText(item) }}</span>
              </div>
            </div>
            <div v-else class="balance-details-state">暂时没有可用的余额明细</div>
            <div v-if="resolvedDetails && resolvedDetails.queriedKeyCount < resolvedDetails.keyCount" class="balance-details-hint">
              已查询 {{ resolvedDetails.queriedKeyCount }}/{{ resolvedDetails.keyCount }} 个 Key
            </div>
          </div>
        </template>
        <button
          type="button"
          class="balance-text balance-details-trigger"
          :aria-label="balanceAriaLabel"
          aria-haspopup="dialog"
          :aria-expanded="detailsOpen"
        >
          <span v-if="balanceDisplay.tone !== 'failed'" class="balance-label">{{ balanceLabel }}</span>
          <span class="balance-value" :class="`balance-${balanceDisplay.tone}`">{{ balanceDisplay.text }}</span>
        </button>
      </a-popover>
      <a-tooltip v-else-if="balanceDisplay.visible" :title="balanceDisplay.tooltip">
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
import { computed, ref, watch } from 'vue'

import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import type { AccountBalanceDetails, AccountBalanceKeySnapshot, AccountListItem } from '@/types/domain'
import { canManuallyRefreshAccountBalance, formatAccountBalance } from './accountBalanceQuery'

const props = defineProps<{
  account: AccountListItem
  refreshing?: boolean
  balanceDetails?: AccountBalanceDetails
  balanceDetailsLoading?: boolean
  balanceDetailsError?: string
  loadBalanceDetails?: (accountId: string) => Promise<AccountBalanceDetails>
}>()

const emit = defineEmits<{
  (event: 'refresh-balance', accountId: string): void
  (event: 'balance-details-request', accountId: string): void
}>()

const balanceDisplay = computed(() => formatAccountBalance(props.account.balanceSnapshot, props.account))
const canRefresh = computed(() => canManuallyRefreshAccountBalance(props.account))
const isMultiKey = computed(() => (
  (props.account.balanceSnapshot?.keyCount ?? 0) > 1
  || (props.account.apiKeyRuntime?.total ?? 0) > 1
))
const balanceLabel = computed(() => {
  if (!isMultiKey.value) return '剩余：'
  if (props.account.balanceSnapshot?.aggregation === 'shared') return '共享余额：'
  if (props.account.balanceSnapshot?.aggregation === 'sum') return '总剩余：'
  return '余额：'
})
const balanceAriaLabel = computed(() => `${balanceLabel.value}${balanceDisplay.value.text}，点击查看各 Key 余额`)
const detailsOpen = ref(false)
// 明细由 AccountsView 统一加载；此组件只负责在打开时发出一次请求事件。
const resolvedDetails = computed(() => props.balanceDetails)
const detailKeyCount = computed(() => resolvedDetails.value?.keyCount ?? props.account.balanceSnapshot?.keyCount ?? props.account.apiKeyRuntime?.total)

watch(() => props.account.id, () => {
  detailsOpen.value = false
})

function handleDetailsOpenChange(open: boolean): void {
  detailsOpen.value = open
  if (!open) return
  emit('balance-details-request', props.account.id)
}

function keyBalanceText(item: AccountBalanceKeySnapshot): string {
  return formatAccountBalance(item, props.account).text
}

function keyBalanceTone(item: AccountBalanceKeySnapshot): ReturnType<typeof formatAccountBalance>['tone'] {
  return formatAccountBalance(item, props.account).tone
}

function keyBalanceUpdatedText(item: AccountBalanceKeySnapshot): string {
  const timestamp = item.lastSuccessAt ?? item.lastAttemptAt
  if (!timestamp) return '待查询'
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) return '时间未知'
  return `更新于 ${date.toLocaleString('zh-CN', { hour12: false })}`
}
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

.balance-details-trigger {
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font: inherit;
}

.balance-details-trigger:hover .balance-value,
.balance-details-trigger:focus-visible .balance-value {
  text-decoration: underline;
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

.balance-details-content {
  width: 280px;
  height: 320px;
  max-width: calc(100vw - 32px);
  box-sizing: border-box;
  overflow: hidden;
}

.balance-details-header {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding-bottom: 8px;
  color: #0f172a;
  font-size: 13px;
  font-weight: 600;
}

.balance-details-count,
.balance-details-hint {
  color: #64748b;
  font-size: 12px;
  font-weight: 400;
}

.balance-details-list {
  height: 260px;
  max-height: none;
  overflow-y: auto;
  overscroll-behavior: contain;
}

.balance-details-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  min-height: 32px;
  border-top: 1px solid #f1f5f9;
}

.balance-details-key {
  min-width: 0;
  overflow: hidden;
  color: #475569;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.balance-details-key-wrap {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.balance-details-updated {
  color: #94a3b8;
  font-size: 11px;
  line-height: 1.2;
}

.balance-details-item-value {
  flex: none;
  font-size: 12px;
  font-weight: 600;
}

.balance-details-state {
  padding: 14px 0;
  color: #64748b;
  font-size: 12px;
  text-align: center;
}

.balance-details-error { color: #dc2626; }
.balance-details-hint { padding-top: 8px; }

</style>
