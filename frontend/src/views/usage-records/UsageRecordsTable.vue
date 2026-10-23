<template>
  <ResponsiveDataList
    table-class="page-table usage-table"
    :columns="columns"
    :data-source="records"
    :mobile-data-source="mobileRecords"
    row-key="id"
    :loading="loading"
    :loading-more="loadingMore"
    :mobile-has-more="mobileHasMore"
    :pagination="pagination"
    :scroll-x="isManagementView ? 2588 : 2408"
    mobile-pagination
    pull-refresh-enabled
    :refreshing="loading"
    @change="handleTableChange"
    @mobile-load-more="$emit('mobile-load-more')"
    @mobile-refresh="$emit('mobile-refresh')"
  >
    <template #emptyText>
      <a-empty class="page-empty-card" :description="emptyDescription" />
    </template>
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'traceId'">
        <div class="trace-id-cell">
          <span class="trace-id-text">{{ record.traceId }}</span>
          <span class="trace-id-actions">
            <a-tooltip title="复制 traceId">
              <a-button size="small" type="text" @click.stop="$emit('copy-trace-id', record.traceId)">
                <template #icon><copy-outlined /></template>
              </a-button>
            </a-tooltip>
          </span>
        </div>
      </template>
      <template v-else-if="column.key === 'apiKey'">
        <span :class="record.apiKeyName ? 'name-cell' : 'muted-cell'">{{ displayName(record.apiKeyName, record.apiKeyId) }}</span>
      </template>
      <template v-else-if="column.key === 'group'">
        <span :class="record.groupName ? 'name-cell' : 'muted-cell'">{{ displayUsageRecordGroupName(record.groupName, record.groupId) }}</span>
        <span v-if="record.routeGroupId && record.routeGroupId !== record.groupId" class="muted-cell"> · 路由组 {{ displayUsageRecordGroupName(record.routeGroupName, record.routeGroupId) }}</span>
      </template>
      <template v-else-if="column.key === 'account'">
        <span :class="record.accountName || record.accountId ? 'name-cell' : 'muted-cell'">{{ accountDisplayText(record) }}</span>
      </template>
      <template v-else-if="column.key === 'systemAccount'">
        <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">
          {{ usageRecordSystemAccountText(record) }}
        </span>
      </template>
      <template v-else-if="column.key === 'clientIp'">
        <span :class="record.clientIp ? 'ip-cell' : 'muted-cell'">{{ record.clientIp ?? '-' }}</span>
      </template>
      <template v-else-if="column.key === 'endpoint'">
        <a-tooltip v-if="record.endpoint" :title="formatEndpoint(record.endpoint)" placement="topLeft">
          <span class="endpoint-cell">{{ formatEndpoint(record.endpoint) }}</span>
        </a-tooltip>
        <span v-else class="muted-cell">-</span>
      </template>
      <template v-else-if="column.key === 'model'">
        <span v-if="record.model || record.upstreamModelMismatch || usageRecordServiceTierText(record) || usageRecordReasoningEffortText(record)" class="model-cell">
          <a-tag v-if="record.model" color="blue"><ProviderIcon v-if="record.providerCode" :provider-code="record.providerCode" :name="providerDisplayName(record.providerCode)" :size="13" /> {{ record.model }}</a-tag>
          <a-tag v-if="record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
          <a-tag v-if="record.upstreamModelMismatch && record.upstreamResponseModel" color="red">上游响应 {{ record.upstreamResponseModel }}</a-tag>
          <a-tag v-if="usageRecordServiceTierText(record)" color="gold">{{ usageRecordServiceTierText(record) }}</a-tag>
          <a-tag v-if="usageRecordReasoningEffortText(record)" color="cyan">思考 {{ usageRecordReasoningEffortText(record) }}</a-tag>
        </span>
        <span v-else class="muted-cell">-</span>
      </template>
      <template v-else-if="column.key === 'stream'">
        <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
      </template>
      <template v-else-if="column.key === 'status'">
        <span class="status-cell">
          <UsageRecordResultCell :record="record" />
          <a-tag v-if="typeof record.statusCode === 'number'" :color="statusCodeColor(record)">{{ statusCodeText(record) }}</a-tag>
        </span>
      </template>
      <template v-else-if="column.key === 'success'">
        <UsageRecordResultCell :record="record" />
      </template>
      <template v-else-if="column.key === 'trafficSource'">
        <a-tag :color="trafficSourceColor(record)">{{ trafficSourceText(record) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'tokens'">
        <div v-if="usageRecordHasMediaMetering(record)" class="token-cell">
          <span v-for="part in usageRecordMediaParts(record)" :key="part">{{ part }}</span>
        </div>
        <div v-else class="token-cell">
          <span class="token-item">
            <svg class="token-icon token-icon-input" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M19 14l-7 7m0 0l-7-7m7 7V3" /></svg>
            {{ formatTokens(record.inputTokens) }}
          </span>
          <span class="token-item">
            <svg class="token-icon token-icon-output" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M5 10l7-7m0 0l7 7m-7-7v18" /></svg>
            {{ formatTokens(record.outputTokens) }}
          </span>
          <span v-if="(record.cacheReadTokens ?? 0) > 0" class="token-item">
            <svg class="token-icon token-icon-cache" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" d="M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4" /></svg>
            {{ formatCacheTokens(record.cacheReadTokens) }}
          </span>
        </div>
      </template>
      <template v-else-if="column.key === 'cost'">
        <UsageRecordCostCell :record="record" />
      </template>
      <template v-else-if="column.key === 'latency'">
        <div class="latency-cell" :style="usageRecordLatencyBarVars(record, usageRecordIsCompaction(record))">
          <span class="latency-bar" aria-hidden="true"></span>
          <div class="latency-rows">
            <span class="latency-label">首字</span>
            <span v-if="record.firstTokenMs == null || usageRecordIsCompaction(record)" class="latency-value latency-value-neutral">{{ formatDuration(record.firstTokenMs) }}</span>
            <span v-else class="latency-value" :class="latencySeverityClass(firstTokenSeverity(record.firstTokenMs))">{{ formatDuration(record.firstTokenMs) }}</span>
            <span class="latency-label">总耗时</span>
            <span class="latency-value" :class="latencySeverityClass(durationSeverity(record.durationMs, usageRecordIsCompaction(record)))">{{ formatDuration(record.durationMs) }}</span>
            <span class="latency-label" title="输出 Token ÷ 完整耗时（含首字等待）">TPS</span>
            <span class="latency-value latency-tps">{{ formatUsageOutputTps(record) }}</span>
          </div>
        </div>
      </template>
      <template v-else-if="column.key === 'createdAt'">
        <span class="muted-cell">{{ formatDateTime(record.createdAt) }}</span>
      </template>
    </template>
    <template #card="{ record }">
      <UsageRecordMobileCard
        :is-management-view="isManagementView"
        :record="record"
        @copy-trace-id="$emit('copy-trace-id', $event)"
      />
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'

import ProviderIcon from '@/components/ProviderIcon.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { providerDisplayName } from '@/shared/providerDisplay'
import type { UsageRecordListItem } from '@/types/domain'
import UsageRecordCostCell from './UsageRecordCostCell.vue'
import UsageRecordMobileCard from './UsageRecordMobileCard.vue'
import UsageRecordResultCell from './UsageRecordResultCell.vue'
import { durationSeverity, firstTokenSeverity, usageRecordLatencyBarVars, type LatencySeverity } from './usageRecordLatencyHealth'
import {
  accountDisplayText,
  displayName,
  displayUsageRecordGroupName,
  formatCacheTokens,
  formatDateTime,
  formatDuration,
  formatEndpoint,
  formatTokens,
  formatUsageOutputTps,
  statusCodeColor,
  statusCodeText,
  trafficSourceColor,
  trafficSourceText,
  usageRecordHasMediaMetering,
  usageRecordIsCompaction,
  usageRecordMediaParts,
  usageRecordReasoningEffortText,
  usageRecordServiceTierText,
  usageRecordSystemAccountText
} from './usageRecordFormatters'

function latencySeverityClass(severity: LatencySeverity): string {
  return `latency-sev-${severity}`
}

withDefaults(defineProps<{
  columns: Array<Record<string, any>>
  isManagementView: boolean
  loading: boolean
  loadingMore: boolean
  mobileHasMore: boolean
  mobileRecords: UsageRecordListItem[]
  pagination: Record<string, any> | false
  records: UsageRecordListItem[]
  emptyDescription?: string
}>(), {
  emptyDescription: '当前条件下没有使用记录。'
})

const emit = defineEmits<{
  (event: 'change', ...args: unknown[]): void
  (event: 'copy-trace-id', traceId: string): void
  (event: 'mobile-load-more'): void
  (event: 'mobile-refresh'): void
}>()

function handleTableChange(...args: unknown[]): void {
  emit('change', ...args)
}
</script>

<style scoped>
.usage-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.usage-table :deep(.ant-empty) {
  margin: 12px 0;
}

.token-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  color: var(--juhe-fg);
  font-size: 12px;
  line-height: 1.3;
}

.token-item {
  display: flex;
  align-items: center;
  gap: 4px;
  font-weight: 500;
}

.token-icon {
  width: 14px;
  height: 14px;
  flex: none;
}

.token-icon-input {
  color: var(--juhe-ok-strong);
}

.token-icon-output {
  color: var(--juhe-violet-strong);
}

.token-icon-cache {
  color: var(--juhe-accent-strong);
}

.model-cell {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 260px;
  vertical-align: bottom;
}

.model-cell :deep(.ant-tag) {
  max-width: 100%;
  overflow-wrap: anywhere;
  white-space: normal;
}

.status-cell {
  display: inline-flex;
  align-items: flex-start;
  gap: 4px;
  white-space: normal;
}

.latency-cell {
  display: flex;
  align-items: stretch;
  gap: 6px;
  font-size: 12px;
  line-height: 1.35;
}

.latency-bar {
  width: 4px;
  flex: none;
  border-radius: 999px;
  background: linear-gradient(to bottom, var(--latency-bar-from) 0 40%, var(--latency-bar-to) 60% 100%);
}

.latency-rows {
  display: grid;
  grid-template-columns: max-content max-content;
  column-gap: 8px;
  row-gap: 2px;
  align-items: baseline;
}

.latency-label {
  color: var(--juhe-muted);
}

.latency-value {
  color: var(--juhe-fg);
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}

.latency-value-neutral {
  color: var(--juhe-fg-soft);
  font-weight: 400;
}

.latency-tps {
  color: var(--juhe-fg-soft);
}

.latency-sev-good {
  color: var(--juhe-latency-good-text);
}

.latency-sev-warn {
  color: var(--juhe-latency-warn-text);
}

.latency-sev-slow {
  color: var(--juhe-latency-slow-text);
}

.latency-sev-critical {
  color: var(--juhe-latency-critical-text);
}

.trace-id-cell {
  display: inline-flex;
  align-items: center;
  max-width: 290px;
  gap: 4px;
  vertical-align: bottom;
}

.trace-id-actions {
  display: inline-flex;
  flex: none;
  gap: 2px;
}

.trace-id-text {
  min-width: 0;
  overflow: hidden;
  color: var(--juhe-fg-soft);
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.name-cell {
  display: inline-block;
  max-width: 160px;
  overflow: hidden;
  color: var(--juhe-fg);
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.ip-cell {
  color: var(--juhe-fg-soft);
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.endpoint-cell {
  display: inline-block;
  max-width: 140px;
  overflow: hidden;
  color: var(--juhe-fg-soft);
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}
</style>
