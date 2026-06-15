<template>
  <ResponsiveDataList
    class="ip-stats-list"
    table-class="page-table ip-stats-table"
    :columns="ipStatsColumns"
    :data-source="rows"
    row-key="ipHash"
    :loading="loading"
    :pagination="tablePagination"
    :pagination-summary="false"
    :scroll-x="1770"
    @change="emitTableChange"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" :description="emptyDescription" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'ip'">
        <span class="mono-cell">{{ record.aggregateIpKey }}</span>
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'requestCount'">
        <span class="number-cell">{{ formatInteger(record.rangeUsage.requestCount) }}</span>
      </template>
      <template v-else-if="column.key === 'totalTokens'">
        <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</span>
      </template>
      <template v-else-if="column.key === 'inputTokens'">
        <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.inputTokens) }}</span>
      </template>
      <template v-else-if="column.key === 'outputTokens'">
        <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.outputTokens) }}</span>
      </template>
      <template v-else-if="column.key === 'cacheReadTokens'">
        <span class="number-cell">{{ formatCompactInteger(record.rangeUsage.cacheReadTokens) }}</span>
      </template>
      <template v-else-if="column.key === 'cacheRate'">
        <span class="number-cell">{{ formatPercent(cacheReadRate(record.rangeUsage)) }}</span>
      </template>
      <template v-else-if="column.key === 'cacheCost'">
        <span class="number-cell">{{ formatCost(record.rangeUsage.cacheReadCost) }}</span>
      </template>
      <template v-else-if="column.key === 'cost'">
        <span class="number-cell">{{ formatCost(record.rangeUsage.totalCost) }}</span>
      </template>
      <template v-else-if="column.key === 'errorRate'">
        <a-tag :color="record.rangeUsage.errorRate > 0.05 ? 'red' : 'green'">
          {{ formatPercent(record.rangeUsage.errorRate * 100) }}
        </a-tag>
      </template>
      <template v-else-if="column.key === 'activeDays'">
        {{ formatInteger(record.rangeUsage.activeDays) }}
      </template>
      <template v-else-if="column.key === 'averageFirstTokenMs'">
        <span class="number-cell">{{ formatDuration(record.rangeUsage.averageFirstTokenMs) }}</span>
      </template>
      <template v-else-if="column.key === 'averageDurationMs'">
        <span class="number-cell">{{ formatDuration(record.rangeUsage.averageDurationMs) }}</span>
      </template>
      <template v-else-if="column.key === 'maxDurationMs'">
        <span class="number-cell">{{ formatDuration(record.rangeUsage.maxDurationMs) }}</span>
      </template>
      <template v-else-if="column.key === 'lastUsedAt'">
        <span :class="clientIpLastUsedAt(record) ? 'name-cell' : 'muted-cell'">{{ formatDateTime(clientIpLastUsedAt(record)) }}</span>
      </template>
      <template v-else-if="column.key === 'actions'">
        <RowActions :actions="ipRowActions(record)" @action-click="handleRowAction($event, record)" />
      </template>
    </template>
    <template #card="{ record }">
      <article class="ip-mobile-card">
        <div class="ip-mobile-head">
          <div class="mono-cell">{{ record.aggregateIpKey }}</div>
          <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item">
            <span>请求</span>
            <strong>{{ formatInteger(record.rangeUsage.requestCount) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>Token</span>
            <strong>{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>输入 Token</span>
            <strong>{{ formatCompactInteger(record.rangeUsage.inputTokens) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>输出 Token</span>
            <strong>{{ formatCompactInteger(record.rangeUsage.outputTokens) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>缓存 Token</span>
            <strong>{{ formatCompactInteger(record.rangeUsage.cacheReadTokens) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>缓存率</span>
            <strong>{{ formatPercent(cacheReadRate(record.rangeUsage)) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>缓存成本</span>
            <strong>{{ formatCost(record.rangeUsage.cacheReadCost) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>成本</span>
            <strong>{{ formatCost(record.rangeUsage.totalCost) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>失败率</span>
            <strong>{{ formatPercent(record.rangeUsage.errorRate * 100) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>活跃天数</span>
            <strong>{{ formatInteger(record.rangeUsage.activeDays) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>平均首 Token</span>
            <strong>{{ formatDuration(record.rangeUsage.averageFirstTokenMs) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>平均总耗时</span>
            <strong>{{ formatDuration(record.rangeUsage.averageDurationMs) }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>最大总耗时</span>
            <strong>{{ formatDuration(record.rangeUsage.maxDurationMs) }}</strong>
          </div>
          <div class="mobile-list-meta-item mobile-list-meta-wide">
            <span>最后使用</span>
            <strong>{{ formatDateTime(clientIpLastUsedAt(record)) }}</strong>
          </div>
        </div>
        <div class="ip-mobile-actions">
          <a-button v-if="record.status === 'blacklisted'" size="small" @click="emitPolicyAction(record, 'unblock')">解封</a-button>
          <a-button v-else size="small" danger @click="emitPolicyAction(record, 'blacklist')">封禁</a-button>
        </div>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { ResponsiveDataListTablePagination } from '@/components/responsiveDataListTableLayout'
import RowActions from '@/components/RowActions.vue'
import { formatDateTime } from '@/shared/formatters'
import type { ClientIpStatsRow } from '@/types/domain'
import { formatCompactInteger, formatCost, formatDuration, formatInteger, formatPercent } from '@/views/stats/statsFormatters'

import {
  cacheReadRate,
  clientIpLastUsedAt,
  ipRowActions,
  ipStatsColumns,
  statusColor,
  statusText,
  type IpStatsPolicyAction
} from './ipStatsDisplay'

defineProps<{
  rows: ClientIpStatsRow[]
  loading: boolean
  tablePagination: ResponsiveDataListTablePagination
  emptyDescription: string
}>()

const emit = defineEmits<{
  change: [paginationInfo: unknown, filters: unknown, sorter: unknown]
  'policy-action': [record: ClientIpStatsRow, action: IpStatsPolicyAction]
}>()

function emitTableChange(...args: unknown[]): void {
  emit('change', args[0], args[1], args[2])
}

function handleRowAction(key: string, record: ClientIpStatsRow): void {
  if (key === 'blacklist' || key === 'unblock') {
    emitPolicyAction(record, key)
  }
}

function emitPolicyAction(record: ClientIpStatsRow, action: IpStatsPolicyAction): void {
  emit('policy-action', record, action)
}
</script>

<style scoped>
.mono-cell {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  word-break: break-all;
}

.muted-cell {
  color: #8c8c8c;
  font-size: 12px;
}

.name-cell {
  color: #1f2937;
}

.number-cell {
  font-variant-numeric: tabular-nums;
}

.ip-mobile-card {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ip-mobile-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.ip-mobile-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
</style>
