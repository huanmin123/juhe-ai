<template>
  <article class="mobile-list-card">
    <div class="mobile-list-card-head">
      <div class="mobile-list-card-title">{{ accountDisplayText(record) }}</div>
      <div class="mobile-list-card-tags">
        <a-tag v-if="record.model" color="blue">{{ record.model }}</a-tag>
        <a-tag v-if="record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
        <a-tag v-if="record.upstreamModelMismatch && record.upstreamResponseModel" color="red">上游响应 {{ record.upstreamResponseModel }}</a-tag>
        <a-tag v-if="usageRecordServiceTierText(record)" color="gold">{{ usageRecordServiceTierText(record) }}</a-tag>
        <a-tag v-if="usageRecordReasoningEffortText(record)" color="cyan">思考 {{ usageRecordReasoningEffortText(record) }}</a-tag>
        <a-tag :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
        <a-tag :color="trafficSourceColor(record)">{{ trafficSourceText(record) }}</a-tag>
        <UsageRecordResultCell :record="record" />
        <a-tag v-if="typeof record.statusCode === 'number'" :color="statusCodeColor(record)">{{ statusCodeText(record) }}</a-tag>
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
        <strong class="mobile-trace-id mono-cell">
          <span>{{ record.traceId }}</span>
          <a-tooltip title="复制 traceId">
            <a-button size="small" type="text" @click="emit('copyTraceId', record.traceId)">
              <template #icon><copy-outlined /></template>
            </a-button>
          </a-tooltip>
        </strong>
      </div>
    </div>
  </article>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'

import type { UsageRecordListItem } from '@/types/domain'
import UsageRecordResultCell from './UsageRecordResultCell.vue'
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
  usageRecordSystemAccountText
} from './usageRecordFormatters'

const props = defineProps<{
  isManagementView: boolean
  record: UsageRecordListItem
}>()

const emit = defineEmits<{
  (event: 'copyTraceId', traceId: string): void
}>()

</script>

<style scoped>
.latency-summary {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.mobile-trace-id {
  display: inline-flex;
  align-items: center;
  min-width: 0;
  gap: 4px;
}

.mobile-trace-id > span {
  min-width: 0;
  overflow-wrap: anywhere;
}

.mobile-list-card-tags :deep(.ant-tag) {
  max-width: 100%;
  overflow-wrap: anywhere;
  white-space: normal;
}

</style>
