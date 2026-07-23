<template>
  <article class="mobile-list-card">
    <div class="mobile-list-card-head">
      <div class="mobile-list-card-title">{{ accountDisplayText(record) }}</div>
      <div class="mobile-list-card-tags">
        <a-tag v-if="record.model" color="blue">{{ record.model }}</a-tag>
        <a-tag v-if="record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
        <a-tag v-if="usageRecordServiceTierText(record)" color="gold">{{ usageRecordServiceTierText(record) }}</a-tag>
        <a-tag v-if="usageRecordReasoningEffortText(record)" color="cyan">思考 {{ usageRecordReasoningEffortText(record) }}</a-tag>
        <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
        <a-tag :color="trafficSourceColor(record)">{{ trafficSourceText(record) }}</a-tag>
        <a-tag v-if="!record.success" color="red">失败</a-tag>
        <a-tag v-else :title="usageRecordCodexGuardStatus(record)?.detail" :color="usageRecordCodexGuardStatus(record) ? 'gold' : 'green'">
          {{ usageRecordCodexGuardStatus(record)?.label ?? '成功' }}
        </a-tag>
        <a-tag v-if="typeof record.statusCode === 'number'" :color="statusCodeColor(record)">状态码 {{ statusCodeText(record) }}</a-tag>
      </div>
    </div>
    <div class="mobile-list-meta-grid">
      <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
        <span>系统账户</span>
        <strong>{{ usageRecordSystemAccountText(record) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>接口</span>
        <strong class="mono-cell">{{ formatEndpoint(record.endpoint) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>请求来源</span>
        <strong>{{ trafficSourceText(record) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>成本</span>
        <strong>{{ formatCost(usageRecordDisplayCostUsd(record)) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>Tokens</span>
        <strong>{{ formatRecordTokens(record) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>延迟</span>
        <strong class="latency-summary">
          <span v-for="part in usageRecordLatencyParts(record)" :key="part">{{ part }}</span>
        </strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>时间</span>
        <strong>{{ formatDateTime(record.createdAt) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>API Key</span>
        <strong>{{ displayName(record.apiKeyName, record.apiKeyId) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>分组</span>
        <strong>{{ displayUsageRecordGroupName(record.groupName, record.groupId) }}</strong>
      </div>
      <div class="mobile-list-meta-item">
        <span>IP</span>
        <strong class="mono-cell">{{ record.clientIp ?? '-' }}</strong>
      </div>
      <div class="mobile-list-meta-item mobile-list-meta-wide">
        <span>traceId</span>
        <strong class="mono-cell">{{ record.traceId }}</strong>
      </div>
    </div>
    <div class="mobile-list-card-actions">
      <RowActions variant="button" :actions="traceActions" :more-actions="traceMoreActions" @action-click="handleTraceAction" />
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'

import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { UsageRecordSummary } from '@/types/domain'
import {
  accountDisplayText,
  displayName,
  displayUsageRecordGroupName,
  formatCost,
  formatDateTime,
  formatEndpoint,
  formatRecordTokens,
  statusCodeColor,
  statusCodeText,
  trafficSourceColor,
  trafficSourceText,
  usageRecordLatencyParts,
  usageRecordDisplayCostUsd,
  usageRecordReasoningEffortText,
  usageRecordServiceTierText,
  usageRecordSystemAccountText,
  usageRecordCodexGuardStatus
} from './usageRecordFormatters'

const props = defineProps<{
  isManagementView: boolean
  record: UsageRecordSummary
}>()

const emit = defineEmits<{
  (event: 'copyTraceId', traceId: string): void
  (event: 'openDetail'): void
  (event: 'openAuditLogs'): void
  (event: 'openRuntimeLogs'): void
}>()

const traceActions = computed<RowActionItem[]>(() => [
  { key: 'open-detail', label: '查看详情', icon: 'detail', tone: 'info' },
  { key: 'copy-trace-id', label: '复制 traceId', icon: 'copy', tone: 'primary' }
])
const traceMoreActions = computed<RowActionItem[]>(() => {
  const actions: RowActionItem[] = []
  if (props.isManagementView) {
    actions.push({ key: 'open-runtime-logs', label: '运行日志', icon: 'detail', tone: 'info' })
    actions.push({ key: 'open-audit-logs', label: '审计日志', icon: 'detail', tone: 'info' })
  }
  return actions
})

function handleTraceAction(key: string): void {
  if (key === 'open-detail') {
    emit('openDetail')
    return
  }
  if (key === 'copy-trace-id') {
    emit('copyTraceId', props.record.traceId)
    return
  }
  if (key === 'open-runtime-logs') {
    emit('openRuntimeLogs')
    return
  }
  if (key === 'open-audit-logs') {
    emit('openAuditLogs')
  }
}
</script>

<style scoped>
.latency-summary {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
</style>
