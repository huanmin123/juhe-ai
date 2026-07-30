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
        <span v-if="record.model || usageRecordServiceTierText(record) || usageRecordReasoningEffortText(record)" class="model-cell">
          <a-tag v-if="record.model" color="blue">{{ record.model }}</a-tag>
          <a-tag v-if="record.modelMappingApplied && record.upstreamModel" color="orange">上游 {{ record.upstreamModel }}</a-tag>
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
        <div class="token-cell">
          <span v-for="part in usageRecordTokenParts(record)" :key="part">{{ part }}</span>
        </div>
      </template>
      <template v-else-if="column.key === 'cost'">
        <UsageRecordCostCell :record="record" />
      </template>
      <template v-else-if="column.key === 'latency'">
        <div class="latency-cell">
          <span v-for="part in usageRecordLatencyParts(record)" :key="part">{{ part }}</span>
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

import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import type { UsageRecordListItem } from '@/types/domain'
import UsageRecordCostCell from './UsageRecordCostCell.vue'
import UsageRecordMobileCard from './UsageRecordMobileCard.vue'
import UsageRecordResultCell from './UsageRecordResultCell.vue'
import {
  accountDisplayText,
  displayName,
  displayUsageRecordGroupName,
  formatDateTime,
  formatEndpoint,
  statusCodeColor,
  statusCodeText,
  trafficSourceColor,
  trafficSourceText,
  usageRecordLatencyParts,
  usageRecordReasoningEffortText,
  usageRecordServiceTierText,
  usageRecordTokenParts,
  usageRecordSystemAccountText
} from './usageRecordFormatters'

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
  color: #475569;
  font-size: 12px;
  line-height: 1.3;
}

.model-cell {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
  max-width: 260px;
  vertical-align: bottom;
}

.status-cell {
  display: inline-flex;
  align-items: flex-start;
  gap: 4px;
  white-space: normal;
}

.latency-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  color: #475569;
  font-size: 12px;
  line-height: 1.3;
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
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.name-cell {
  display: inline-block;
  max-width: 160px;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.ip-cell {
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.endpoint-cell {
  display: inline-block;
  max-width: 140px;
  overflow: hidden;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}
</style>
