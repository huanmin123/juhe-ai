<template>
  <a-modal
    v-model:open="open"
    title="代理质量检测报告"
    width="680px"
    :footer="null"
    class="proxy-test-modal"
  >
    <template v-if="selectedProxy && !report">
      <section class="proxy-test-start">
        <h3>{{ selectedProxy.name }}</h3>
        <div class="proxy-report-meta">
          <span>检测目标: 供应商默认地址</span>
          <span>当前延迟: {{ formatLatency(selectedProxy.latencyMs) }}</span>
          <span>当前状态: {{ testStatusText(selectedProxy.testStatus) }}</span>
          <span>最近检测: {{ formatDateTime(selectedProxy.lastTestedAt) }}</span>
        </div>
      </section>

      <div class="proxy-report-footer">
        <a-space>
          <a-button :disabled="Boolean(testingProxyId)" @click="open = false">关闭</a-button>
          <a-button type="primary" :loading="testingProxyId === selectedProxy.id" @click="emit('run-test')">开始测试</a-button>
        </a-space>
      </div>
    </template>

    <template v-else-if="report">
      <section class="proxy-report-summary">
        <div class="proxy-report-main">
          <h3>{{ report.proxyName }}</h3>
          <p>通过 {{ report.passedCount }} 项，告警 {{ report.warningCount }} 项，失败 {{ report.failedCount }} 项</p>
          <div class="proxy-report-meta">
            <span>检测目标: 供应商默认地址</span>
            <span>出口 IP: {{ report.outboundIp || '-' }}</span>
            <span>出口地区: {{ report.outboundRegion || '-' }}</span>
            <span>基础延迟: {{ formatLatency(report.baseLatencyMs) }}</span>
            <span>检测时间: {{ formatDateTime(report.testedAt) }}</span>
          </div>
        </div>
        <div class="proxy-score">
          <strong>{{ report.score }}</strong>
          <span>等级 {{ report.grade }}</span>
        </div>
      </section>

      <ResponsiveDataList
        size="small"
        table-class="proxy-report-table"
        :columns="proxyReportColumns"
        :data-source="report.items"
        :pagination="false"
        row-key="name"
        :table-scroll-enabled="false"
        :lock-body-scroll="false"
      >
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'status'">
            <a-tag :color="testItemStatusColor(record.status)">{{ testItemStatusText(record.status) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'httpStatus'">
            <span>{{ record.httpStatus ?? '-' }}</span>
          </template>
          <template v-else-if="column.key === 'latencyMs'">
            <span>{{ formatLatency(record.latencyMs) }}</span>
          </template>
          <template v-else-if="column.key === 'message'">
            <span>{{ record.message }}</span>
          </template>
        </template>
        <template #card="{ record }">
          <article class="proxy-report-card">
            <div class="proxy-report-card-head">
              <strong>{{ record.name }}</strong>
              <a-tag :color="testItemStatusColor(record.status)">{{ testItemStatusText(record.status) }}</a-tag>
            </div>
            <div class="proxy-report-card-grid">
              <span>HTTP</span>
              <strong>{{ record.httpStatus ?? '-' }}</strong>
              <span>延迟</span>
              <strong>{{ formatLatency(record.latencyMs) }}</strong>
              <span>说明</span>
              <strong>{{ record.message }}</strong>
            </div>
          </article>
        </template>
      </ResponsiveDataList>

      <div class="proxy-report-footer">
        <a-space>
          <a-button :disabled="Boolean(testingProxyId)" @click="open = false">关闭</a-button>
          <a-button type="primary" :loading="testingProxyId === report.proxyId" @click="emit('run-test')">重新测试</a-button>
        </a-space>
      </div>
    </template>
  </a-modal>
</template>

<script setup lang="ts">
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import { formatDateTime } from '@/shared/formatters'
import type { ProxyProfileSummary, ProxyTestReport } from '@/types/domain'
import {
  formatLatency,
  proxyReportColumns,
  testItemStatusColor,
  testItemStatusText,
  testStatusText
} from './proxyDisplay'

const open = defineModel<boolean>('open', { required: true })

defineProps<{
  report?: ProxyTestReport
  selectedProxy?: ProxyProfileSummary
  testingProxyId?: string
}>()

const emit = defineEmits<{
  'run-test': []
}>()
</script>

<style scoped>
.proxy-report-summary {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 20px;
  padding: 22px;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  background: #f8fafc;
}

.proxy-report-main {
  min-width: 0;
}

.proxy-report-main h3,
.proxy-test-start h3 {
  margin: 0 0 8px;
  color: #0f172a;
  font-size: 16px;
  font-weight: 700;
}

.proxy-report-main p {
  margin: 0 0 14px;
  color: #475569;
  font-size: 14px;
}

.proxy-report-meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px 28px;
  color: #475569;
  font-size: 13px;
}

.proxy-report-meta span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.proxy-score {
  display: flex;
  flex: 0 0 auto;
  flex-direction: column;
  align-items: center;
  min-width: 74px;
  color: #0f172a;
}

.proxy-score strong {
  font-size: 32px;
  line-height: 1;
}

.proxy-score span {
  margin-top: 6px;
  color: #64748b;
  font-size: 13px;
}

.proxy-report-table {
  margin-top: 8px;
}

.proxy-report-table :deep(.ant-table-cell) {
  white-space: normal;
}

.proxy-report-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.proxy-report-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.proxy-report-card-grid {
  display: grid;
  grid-template-columns: minmax(52px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: #64748b;
  font-size: 12px;
}

.proxy-report-card-grid strong {
  min-width: 0;
  color: #0f172a;
  font-weight: 400;
}

.proxy-report-footer {
  display: flex;
  justify-content: flex-end;
  margin-top: 20px;
}

@media (max-width: 640px) {
  .proxy-report-summary {
    flex-direction: column;
  }

  .proxy-report-meta {
    grid-template-columns: 1fr;
  }
}
</style>
