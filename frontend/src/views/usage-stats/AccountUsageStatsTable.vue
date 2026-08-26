<template>
  <a-card class="page-card usage-stats-table-card" :loading="initialLoading">
    <div class="usage-stats-table-head">
      <div>
        <h3>账户统计明细</h3>
        <p>账户类型仅作运行态参考；统计、会话亲和和缓存按本地 API Key 与分组连续。</p>
      </div>
    </div>
    <ResponsiveDataList
      class="usage-stats-responsive-list"
      table-class="usage-stats-table"
      :columns="columns"
      :data-source="rows"
      :mobile-data-source="rows"
      row-key="id"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="pagination"
      :scroll-x="scrollX"
      :table-scroll-enabled="false"
      :lock-body-scroll="false"
      :mobile-pagination="!hasSelectedTrendAccounts"
      pull-refresh-enabled
      :refreshing="loading"
      @change="(...args) => emit('change', ...args)"
      @mobile-load-more="emit('mobile-load-more')"
      @mobile-refresh="emit('mobile-refresh')"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" :description="emptyDescription" />
      </template>
      <template #bodyCell="{ column, record, index }">
        <template v-if="column.key === 'rank'">
          <span class="usage-rank">{{ Number(index ?? 0) + 1 }}</span>
        </template>
        <template v-else-if="column.key === 'name'">
          <div class="usage-account-cell">
            <span class="usage-account-name-row">
              <span class="usage-account-name">{{ record.name }}</span>
              <a-tag v-if="record.accessType === 'authorized'" color="blue">{{ authorizationAccountTagText(record) }}</a-tag>
            </span>
            <span class="usage-account-meta">{{ statusText(record.status) }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'type'">
          <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'providerCode'">
          <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ record.systemAccountName || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'requests'">
          <span class="usage-number">{{ formatInteger(record.rangeUsage.requestCount) }}</span>
        </template>
        <template v-else-if="column.key === 'tokens'">
          <span class="usage-number">{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</span>
        </template>
        <template v-else-if="column.key === 'cacheRate'">
          <span class="usage-number">{{ formatPercent(cacheReadRate(record.rangeUsage, record.providerCode)) }}</span>
        </template>
        <template v-else-if="column.key === 'cacheCost'">
          <span class="usage-number">{{ formatCost(record.rangeUsage.cacheReadCost) }}</span>
        </template>
        <template v-else-if="column.key === 'cost'">
          <span class="usage-number">{{ formatCost(record.rangeUsage.totalCost) }}</span>
        </template>
        <template v-else-if="column.key === 'lastUsedAt'">
          <span :class="record.rangeUsage.lastUsedAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.rangeUsage.lastUsedAt) }}</span>
        </template>
      </template>
      <template #card="{ record, index }">
        <article class="usage-mobile-card">
          <div class="usage-mobile-head">
            <div>
              <div class="usage-mobile-title">
                <span class="usage-rank">{{ index + 1 }}</span>
                <span>{{ record.name }}</span>
              </div>
              <div class="usage-mobile-subtitle">
                <a-tag color="processing">{{ accountTypeText(record.type) }}</a-tag>
                <a-tag color="geekblue">{{ providerName(record.providerCode) }}</a-tag>
                <a-tag :color="statusColor(record.status)">{{ statusText(record.status) }}</a-tag>
                <a-tag v-if="record.accessType === 'authorized'" color="blue">{{ authorizationAccountTagText(record) }}</a-tag>
              </div>
            </div>
          </div>
          <div class="usage-mobile-grid">
            <div class="usage-mobile-metric">
              <span>请求</span>
              <strong>{{ formatInteger(record.rangeUsage.requestCount) }}</strong>
            </div>
            <div class="usage-mobile-metric">
              <span>Token</span>
              <strong>{{ formatCompactInteger(record.rangeUsage.totalTokens) }}</strong>
            </div>
            <div class="usage-mobile-metric">
              <span>缓存读占比</span>
              <strong>{{ formatPercent(cacheReadRate(record.rangeUsage, record.providerCode)) }}</strong>
            </div>
            <div class="usage-mobile-metric">
              <span>缓存成本</span>
              <strong>{{ formatCost(record.rangeUsage.cacheReadCost) }}</strong>
            </div>
            <div class="usage-mobile-metric">
              <span>成本</span>
              <strong>{{ formatCost(record.rangeUsage.totalCost) }}</strong>
            </div>
            <div class="usage-mobile-metric">
              <span>最后使用</span>
              <strong>{{ formatDateTime(record.rangeUsage.lastUsedAt) }}</strong>
            </div>
          </div>
        </article>
      </template>
    </ResponsiveDataList>
  </a-card>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { formatDateTime } from '@/shared/formatters'
import type { AccountUsageStatsRow, AccountUsageSummary } from '@/types/domain'
import { accountTypeText } from '@/views/accounts/accountBasicFormatters'
import { statusColor, statusText } from '@/views/accounts/accountFormatters'
import { formatCompactInteger, formatCost, formatInteger, formatPercent } from '@/views/stats/statsFormatters'

type TablePagination = false | Record<string, unknown>

defineProps<{
  columns: Array<Record<string, unknown>>
  emptyDescription: string
  hasSelectedTrendAccounts: boolean
  initialLoading: boolean
  loading: boolean
  mobileHasMore: boolean
  mobileLoadingMore: boolean
  pagination: TablePagination
  rows: AccountUsageStatsRow[]
  scrollX: number
  authorizationAccountTagText: (account: Pick<AccountUsageStatsRow, 'ownerSystemAccountName'>) => string
  cacheReadRate: (summary?: AccountUsageSummary, providerCode?: string) => number
  providerName: (providerCode?: string) => string
}>()

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()
</script>

<style scoped>
.usage-stats-table-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.usage-stats-table-head {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}

.usage-stats-table-head h3 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.usage-stats-table-head p {
  margin: 4px 0 0;
  color: #64748b;
  font-size: 12px;
  line-height: 1.6;
}

.usage-account-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
}

.usage-account-name-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.usage-account-name {
  display: inline-block;
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  font-weight: 500;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.usage-account-meta {
  color: #64748b;
  font-size: 12px;
}

.usage-rank {
  display: inline-flex;
  min-width: 22px;
  height: 22px;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: #f1f5f9;
  color: #475569;
  font-size: 12px;
  font-weight: 700;
}

.usage-number {
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
}

.usage-mobile-card {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 14px;
  border: 1px solid #e8edf5;
  border-radius: 8px;
  background: #fff;
}

.usage-mobile-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
}

.usage-mobile-title {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: #0f172a;
  font-weight: 700;
}

.usage-mobile-subtitle {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-top: 6px;
  color: #64748b;
  font-size: 12px;
}

.usage-mobile-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.usage-mobile-metric {
  min-width: 0;
  padding: 10px;
  border: 1px solid #eef2f7;
  border-radius: 8px;
  background: #f8fafc;
}

.usage-mobile-metric span {
  display: block;
  color: #64748b;
  font-size: 12px;
}

.usage-mobile-metric strong {
  display: block;
  margin-top: 4px;
  overflow: hidden;
  color: #0f172a;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 768px) {
  .usage-mobile-grid {
    grid-template-columns: 1fr;
  }
}
</style>
