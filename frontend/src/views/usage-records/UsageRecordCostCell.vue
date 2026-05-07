<template>
  <span class="cost-cell-wrap">
    <span class="cost-cell">{{ formatCost(record.costUsd) }}</span>
    <a-popover v-if="record.costBreakdown" trigger="hover" placement="right" overlay-class-name="cost-popover">
      <template #content>
        <div class="cost-detail-panel">
          <div class="cost-detail-title">成本明细</div>
          <div class="cost-detail-row">
            <span>输入成本</span>
            <span class="cost-detail-value">{{ formatCost(record.costBreakdown.inputCostUsd) }}</span>
          </div>
          <div class="cost-detail-row">
            <span>输出成本</span>
            <span class="cost-detail-value">{{ formatCost(record.costBreakdown.outputCostUsd) }}</span>
          </div>
          <div class="cost-detail-row">
            <span>输入单价</span>
            <span class="cost-detail-value">{{ formatUnitPrice(record.costBreakdown.inputUsdPer1M) }}</span>
          </div>
          <div class="cost-detail-row">
            <span>输出单价</span>
            <span class="cost-detail-value">{{ formatUnitPrice(record.costBreakdown.outputUsdPer1M) }}</span>
          </div>
          <div class="cost-detail-row">
            <span>缓存读取成本</span>
            <span class="cost-detail-value">{{ formatCost(record.costBreakdown.cacheReadCostUsd) }}</span>
          </div>
          <div v-if="record.costBreakdown.inputImageCostUsd !== undefined" class="cost-detail-row">
            <span>图片输入成本</span>
            <span class="cost-detail-value">{{ formatCost(record.costBreakdown.inputImageCostUsd) }}</span>
          </div>
          <div v-if="record.costBreakdown.outputImageCostUsd !== undefined" class="cost-detail-row">
            <span>图片输出成本</span>
            <span class="cost-detail-value">{{ formatCost(record.costBreakdown.outputImageCostUsd) }}</span>
          </div>
          <div v-if="record.costBreakdown.inputImageUsdPer1M !== undefined" class="cost-detail-row">
            <span>图片输入单价</span>
            <span class="cost-detail-value">{{ formatUnitPrice(record.costBreakdown.inputImageUsdPer1M) }}</span>
          </div>
          <div v-if="record.costBreakdown.outputImageUsdPer1M !== undefined" class="cost-detail-row">
            <span>图片输出单价</span>
            <span class="cost-detail-value">{{ formatUnitPrice(record.costBreakdown.outputImageUsdPer1M) }}</span>
          </div>
          <div class="cost-detail-row">
            <span>账户计费</span>
            <span class="cost-detail-value">{{ formatCost(record.costBreakdown.accountChargeUsd) }}</span>
          </div>
          <div class="cost-detail-row">
            <span>倍率</span>
            <span class="cost-detail-value">{{ record.costBreakdown.multiplier }}x</span>
          </div>
        </div>
      </template>
      <InfoCircleOutlined class="cost-detail-icon" />
    </a-popover>
  </span>
</template>

<script setup lang="ts">
import { InfoCircleOutlined } from '@ant-design/icons-vue'

import type { UsageRecordSummary } from '@/types/domain'
import { formatCost, formatUnitPrice } from './usageRecordFormatters'

defineProps<{
  record: UsageRecordSummary
}>()
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

.cost-detail-row {
  display: flex;
  justify-content: space-between;
  gap: 18px;
  line-height: 1.8;
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
