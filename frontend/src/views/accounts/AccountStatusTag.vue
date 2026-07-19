<template>
  <div class="status-cell">
    <a-tooltip v-if="tooltipLines.length" placement="topLeft">
      <template #title>
        <div class="status-tooltip">
          <div v-for="line in tooltipLines" :key="line" class="status-tooltip-line">
            <span>{{ line }}</span>
            <a-button
              v-if="line.startsWith('traceId：')"
              class="trace-copy-button"
              type="text"
              size="small"
              aria-label="复制 traceId"
              title="复制 traceId"
              @click.stop="copyTraceId"
            >
              <CopyOutlined />
            </a-button>
          </div>
        </div>
      </template>
      <span class="status-tag-group">
        <StatusTag class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
        <StatusTag v-if="account.superPriorityEnabled" class="status-tag priority-tag" :color="dispatchFlagActive ? 'gold' : 'default'" :label="dispatchFlagActive ? '超级优先' : '超级优先暂停'" />
        <StatusTag v-if="account.fallbackEnabled" class="status-tag priority-tag" :color="dispatchFlagActive ? 'purple' : 'default'" :label="dispatchFlagActive ? '降级备用' : '备用暂停'" />
      </span>
    </a-tooltip>
    <span v-else class="status-tag-group">
      <StatusTag class="status-tag" :color="accountStatusColor(account)" :label="accountStatusText(account)" />
      <a-tooltip v-if="account.superPriorityEnabled" :title="superPriorityTooltip">
        <StatusTag class="status-tag priority-tag" :color="dispatchFlagActive ? 'gold' : 'default'" :label="dispatchFlagActive ? '超级优先' : '超级优先暂停'" />
      </a-tooltip>
      <a-tooltip v-if="account.fallbackEnabled" :title="fallbackTooltip">
        <StatusTag class="status-tag priority-tag" :color="dispatchFlagActive ? 'purple' : 'default'" :label="dispatchFlagActive ? '降级备用' : '备用暂停'" />
      </a-tooltip>
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { message } from 'ant-design-vue'
import { CopyOutlined } from '@ant-design/icons-vue'

import StatusTag from '@/components/StatusTag.vue'
import type { AccountSummary } from '@/types/domain'
import { accountStatusColor, accountStatusText, accountStatusTooltipLines } from './accountFormatters'
import { accountStatusTooltipTraceId } from './accountStatusPresentation'

const props = defineProps<{
  account: AccountSummary
}>()

const dispatchFlagActive = computed(() => props.account.effectiveAvailability?.available ?? (props.account.status === 'active' && props.account.schedulable))
const superPriorityTooltip = computed(() => dispatchFlagActive.value
  ? '超级优先：下次调度优先使用此账户'
  : '超级优先已保留；账户恢复正常并参与调度后自动生效'
)
const fallbackTooltip = computed(() => dispatchFlagActive.value
  ? '降级备用：仅在同分组其他可用账户都不可用时使用'
  : '降级备用已保留；账户恢复正常并参与调度后自动生效'
)

const tooltipLines = computed(() => {
  return accountStatusTooltipLines(props.account)
})

async function copyTraceId(): Promise<void> {
  const traceId = accountStatusTooltipTraceId(props.account)
  if (!traceId) return
  try {
    await navigator.clipboard.writeText(traceId)
    message.success('traceId 已复制')
  } catch {
    message.error('复制 traceId 失败')
  }
}
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

.status-tooltip-line {
  display: flex;
  align-items: flex-start;
  gap: 4px;
  overflow-wrap: anywhere;
}

.trace-copy-button {
  flex: none;
  color: inherit;
  padding: 0 2px;
  height: 22px;
}
</style>
