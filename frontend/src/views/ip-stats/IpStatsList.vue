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
    :scroll-x="1790"
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
            <span>缓存读占比</span>
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
          <a-button size="small" @click="emitDetail(record)">详情</a-button>
          <a-popconfirm
            v-if="record.status === 'blacklisted'"
            title="确认解除这个 IP 的封禁？"
            ok-text="解封"
            cancel-text="取消"
            @confirm="emitPolicyAction(record, 'unblock')"
          >
            <a-button size="small">解封</a-button>
          </a-popconfirm>
          <a-popconfirm
            v-else-if="record.status === 'allowlisted'"
            title="确认将这个 IP 移出白名单？"
            ok-text="移出"
            cancel-text="取消"
            @confirm="emitPolicyAction(record, 'unallowlist')"
          >
            <a-button size="small">移出白名单</a-button>
          </a-popconfirm>
          <a-popconfirm
            v-else
            title="确认将这个 IP 加入白名单？"
            ok-text="加白"
            cancel-text="取消"
            @confirm="emitPolicyAction(record, 'allowlist')"
          >
            <a-button size="small">加白</a-button>
          </a-popconfirm>
          <a-popconfirm
            v-if="record.status === 'normal'"
            title="确认封禁这个 IP？封禁后该 IP 的公开请求会被拒绝。"
            ok-text="封禁"
            cancel-text="取消"
            @confirm="emitPolicyAction(record, 'blacklist')"
          >
            <a-button size="small" danger>封禁</a-button>
          </a-popconfirm>
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
  type IpStatsPolicyAction,
  type IpStatsRowAction
} from './ipStatsDisplay'

defineProps<{
  rows: ClientIpStatsRow[]
  loading: boolean
  tablePagination: ResponsiveDataListTablePagination
  emptyDescription: string
}>()

const emit = defineEmits<{
  change: [paginationInfo: unknown, filters: unknown, sorter: unknown]
  detail: [record: ClientIpStatsRow]
  'policy-action': [record: ClientIpStatsRow, action: IpStatsPolicyAction]
}>()

function emitTableChange(...args: unknown[]): void {
  emit('change', args[0], args[1], args[2])
}

function handleRowAction(key: IpStatsRowAction | string, record: ClientIpStatsRow): void {
  if (key === 'detail') {
    emitDetail(record)
    return
  }
  if (key === 'blacklist' || key === 'unblock' || key === 'allowlist' || key === 'unallowlist') {
    emitPolicyAction(record, key)
  }
}

function emitDetail(record: ClientIpStatsRow): void {
  emit('detail', record)
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
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.ip-mobile-actions :deep(.ant-btn) {
  width: 100%;
  min-height: 36px;
  white-space: normal;
}
</style>
