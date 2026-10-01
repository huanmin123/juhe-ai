<template>
  <article class="mobile-list-card">
    <div class="mobile-list-card-head">
      <div class="mobile-list-card-title">{{ accountDisplayText(record) }}</div>
      <div class="mobile-list-card-tags">
        <a-tag v-if="record.model" color="blue">{{ record.model }}</a-tag>
        <a-tag v-if="expanded && record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
        <a-tag v-if="record.upstreamModelMismatch && record.upstreamResponseModel" color="red">上游响应 {{ record.upstreamResponseModel }}</a-tag>
        <a-tag v-if="expanded && usageRecordServiceTierText(record)" color="gold">{{ usageRecordServiceTierText(record) }}</a-tag>
        <a-tag v-if="expanded && usageRecordReasoningEffortText(record)" color="cyan">思考 {{ usageRecordReasoningEffortText(record) }}</a-tag>
        <a-tag v-if="expanded" :color="record.stream ? 'purple' : 'default'">{{ record.stream ? '流式' : '非流式' }}</a-tag>
        <a-tag v-if="expanded" :color="trafficSourceColor(record)">{{ trafficSourceText(record) }}</a-tag>
        <UsageRecordResultCell :record="record" />
        <a-tag v-if="typeof record.statusCode === 'number'" :color="statusCodeColor(record)">{{ statusCodeText(record) }}</a-tag>
      </div>
    </div>
    <div class="mobile-list-meta-grid">
      <div class="mobile-list-meta-item">
        <span>接口</span>
        <strong class="mono-cell">{{ formatEndpoint(record.endpoint) }}</strong>
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
        <span>时间</span>
        <strong>{{ formatDateTime(record.createdAt) }}</strong>
      </div>
      <template v-if="expanded">
        <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
          <span>系统账户</span>
          <strong>{{ usageRecordSystemAccountText(record) }}</strong>
        </div>
        <div class="mobile-list-meta-item">
          <span>请求来源</span>
          <strong>{{ trafficSourceText(record) }}</strong>
        </div>
        <div class="mobile-list-meta-item">
          <span>延迟</span>
          <strong class="latency-summary">
            <span v-for="part in usageRecordLatencyParts(record)" :key="part">{{ part }}</span>
          </strong>
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
      </template>
    </div>
    <button type="button" class="usage-record-mobile-more" @click="expanded = !expanded">
      {{ expanded ? '收起' : '更多字段' }}
      <caret-down-outlined class="usage-record-mobile-more-icon" :class="{ open: expanded }" />
    </button>
  </article>
</template>

<script setup lang="ts">
import { CaretDownOutlined, CopyOutlined } from '@ant-design/icons-vue'
import { ref } from 'vue'

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

// 手机端卡片默认只保留高频判读字段（模型/结果/接口/成本/Tokens/时间），
// 排障向的次要字段（上游映射、服务等级、来源、延迟细分、Key、分组、IP、traceId）
// 收进「更多字段」展开——对齐响应式列表规范 §5 的信息分层要求。
defineProps<{
  isManagementView: boolean
  record: UsageRecordListItem
}>()

const emit = defineEmits<{
  (event: 'copyTraceId', traceId: string): void
}>()

const expanded = ref(false)

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

.usage-record-mobile-more {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  width: 100%;
  min-height: 32px;
  padding: 4px 8px;
  border: 0;
  border-radius: var(--juhe-radius-sm);
  background: transparent;
  color: var(--juhe-muted);
  font-size: 12px;
  cursor: pointer;
}

.usage-record-mobile-more-icon {
  font-size: 10px;
  transition: transform 0.18s ease;
}

.usage-record-mobile-more-icon.open {
  transform: rotate(180deg);
}

</style>
