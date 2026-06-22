<template>
  <ResponsiveDataList
    v-if="rows.length > 0"
    table-class="process-event-loop-table"
    card-class="process-event-loop-mobile-list"
    :columns="processEventLoopColumns"
    :data-source="rows"
    :mobile-data-source="rows"
    row-key="processRole"
    size="small"
    :pagination="false"
    :scroll-x="920"
    :table-scroll-enabled="false"
    :lock-body-scroll="false"
    :adaptive-column-width="false"
  >
    <template #bodyCell="{ column, record }">
      <template v-if="column.key === 'processRole'">
        <a-tag>{{ processRoleLabel(record.processRole) }}</a-tag>
      </template>
      <template v-else-if="column.key === 'processPid'">
        {{ record.processPid ?? '-' }}
      </template>
      <template v-else-if="column.key === 'processRss'">
        {{ record.latestSampleAvailable ? formatBytesMiB(record.latestProcessRssBytes) : '未知' }}
      </template>
      <template v-else-if="column.key === 'processHeap'">
        {{ record.latestSampleAvailable ? `${formatBytesMiB(record.latestProcessHeapUsedBytes)} / ${formatBytesMiB(record.latestProcessHeapTotalBytes)}` : '未知' }}
      </template>
      <template v-else-if="column.key === 'latestLag'">
        {{ record.latestSampleAvailable ? formatJobDuration(record.latestEventLoopLagMs ?? undefined) : '未知' }}
      </template>
      <template v-else-if="column.key === 'peakLag'">
        {{ record.peakSampleAvailable ? formatJobDuration(record.peakEventLoopLagMs ?? undefined) : '未知' }}
      </template>
      <template v-else-if="column.key === 'sampledAt'">
        {{ record.latestSampleAvailable ? formatDateTime(record.latestSampledAt ?? undefined) : '-' }}
      </template>
      <template v-else-if="column.key === 'status'">
        <a-tag :color="record.latestSampleAvailable ? 'success' : 'warning'">
          {{ record.latestSampleAvailable ? '正常' : '缺样本' }}
        </a-tag>
      </template>
    </template>
    <template #card="{ record }">
      <article class="process-event-loop-card" :class="{ unavailable: !record.latestSampleAvailable }">
        <div class="process-event-loop-card-head">
          <strong>{{ processRoleLabel(record.processRole) }}</strong>
          <a-tag :color="record.latestSampleAvailable ? 'success' : 'warning'">
            {{ record.latestSampleAvailable ? '正常' : '缺样本' }}
          </a-tag>
        </div>
        <div class="mobile-list-meta-grid">
          <div class="mobile-list-meta-item">
            <span>PID</span>
            <strong>{{ record.processPid ?? '-' }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>RSS</span>
            <strong>{{ record.latestSampleAvailable ? formatBytesMiB(record.latestProcessRssBytes) : '未知' }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>Heap</span>
            <strong>{{ record.latestSampleAvailable ? `${formatBytesMiB(record.latestProcessHeapUsedBytes)} / ${formatBytesMiB(record.latestProcessHeapTotalBytes)}` : '未知' }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>最新延迟</span>
            <strong>{{ record.latestSampleAvailable ? formatJobDuration(record.latestEventLoopLagMs ?? undefined) : '未知' }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>24小时峰值</span>
            <strong>{{ record.peakSampleAvailable ? formatJobDuration(record.peakEventLoopLagMs ?? undefined) : '未知' }}</strong>
          </div>
          <div class="mobile-list-meta-item">
            <span>采样时间</span>
            <strong>{{ record.latestSampleAvailable ? formatDateTime(record.latestSampledAt ?? undefined) : '-' }}</strong>
          </div>
        </div>
      </article>
    </template>
  </ResponsiveDataList>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { formatDateTime } from '@/shared/formatters'
import { processRoleLabel } from './statsChartOptions'
import { formatBytesMiB, formatDuration } from './statsFormatters'
import { processEventLoopColumns, type ProcessEventLoopRow } from './statsProcessEventLoop'

defineProps<{
  rows: ProcessEventLoopRow[]
}>()

function formatJobDuration(value?: number) {
  return value === undefined ? '-' : formatDuration(value)
}
</script>

<style scoped>
:deep(.process-event-loop-table) {
  margin-top: 12px;
}

:deep(.process-event-loop-table .ant-table-tbody > tr > td) {
  vertical-align: middle;
}

.process-event-loop-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.process-event-loop-card.unavailable {
  border-color: #ffd591;
  background: #fff7e6;
}

.process-event-loop-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.process-event-loop-card-head strong {
  min-width: 0;
  color: #0f172a;
  font-weight: 600;
  overflow-wrap: anywhere;
}

@media (max-width: 768px) {
  :deep(.process-event-loop-table) {
    margin-top: 10px;
  }
}
</style>
