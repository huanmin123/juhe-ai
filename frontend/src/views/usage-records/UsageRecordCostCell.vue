<template>
  <span class="cost-cell-wrap">
    <span class="cost-cell">{{ formatCost(displayCostUsd) }}</span>
    <a-popover v-if="costBreakdown" trigger="hover" placement="right" overlay-class-name="cost-popover">
      <template #content>
        <div class="cost-detail-panel">
          <div class="cost-detail-title">{{ detailTitle }}</div>
          <template v-if="tokenDetailRows.length">
            <div class="cost-detail-section-title">Token 明细</div>
            <div v-for="row in tokenDetailRows" :key="row.key" class="cost-detail-row">
              <span>{{ row.label }}</span>
              <span class="cost-detail-value">{{ row.value }}</span>
            </div>
          </template>
          <div v-if="tokenDetailRows.length && costAmountRows.length" class="cost-detail-divider"></div>
          <div v-for="row in costAmountRows" :key="row.key" class="cost-detail-row">
            <span>{{ row.label }}</span>
            <span class="cost-detail-value">{{ row.value }}</span>
          </div>
          <div v-if="unitPriceRows.length" class="cost-detail-divider"></div>
          <div v-for="row in unitPriceRows" :key="row.key" class="cost-detail-row">
            <span>{{ row.label }}</span>
            <span class="cost-detail-value">{{ row.value }}</span>
          </div>
        </div>
      </template>
      <InfoCircleOutlined class="cost-detail-icon" />
    </a-popover>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { InfoCircleOutlined } from '@ant-design/icons-vue'

import type { UsageRecordSummary } from '@/types/domain'
import {
  usageRecordCostAmountRows,
  usageRecordCostDetailTitle,
  usageRecordCostPriceRows,
  usageRecordCostTokenRows
} from './usageRecordCostDetails'
import { formatCost } from './usageRecordFormatters'

const props = defineProps<{
  record: UsageRecordSummary
}>()

const costBreakdown = computed(() => props.record.costBreakdown)
const displayCostUsd = computed(() => props.record.costUsd ?? costBreakdown.value?.accountChargeUsd)
const detailTitle = computed(() => usageRecordCostDetailTitle(props.record))
const tokenDetailRows = computed(() => usageRecordCostTokenRows(props.record))
const costAmountRows = computed(() => usageRecordCostAmountRows(props.record))
const unitPriceRows = computed(() => usageRecordCostPriceRows(props.record))
</script>

<style scoped>
.cost-cell {
  color: #059669;
  font-family: Consolas, 'Courier New', monospace;
}

.cost-cell-wrap {
  display: inline-flex;
  gap: 6px;
  align-items: center;
}

.cost-detail-icon {
  color: #94a3b8;
  cursor: help;
  font-size: 13px;
}

.cost-detail-icon:hover {
  color: #2563eb;
}

.cost-detail-panel {
  min-width: 190px;
  color: #e2e8f0;
  font-size: 12px;
}

.cost-detail-title {
  margin-bottom: 6px;
  color: #f8fafc;
}

.cost-detail-section-title {
  margin: 6px 0 2px;
  color: #94a3b8;
}

.cost-detail-row {
  display: flex;
  justify-content: space-between;
  gap: 18px;
  line-height: 1.8;
}

.cost-detail-divider {
  height: 1px;
  margin: 6px 0;
  background: rgb(148 163 184 / 22%);
}

.cost-detail-value {
  color: #60a5fa;
  font-family: Consolas, 'Courier New', monospace;
}

:global(.cost-popover .ant-popover-inner) {
  background: #0f172a;
  border-radius: 8px;
  box-shadow: 0 10px 24px rgb(15 23 42 / 24%);
}

:global(.cost-popover .ant-popover-arrow::before) {
  background: #0f172a;
}
</style>
