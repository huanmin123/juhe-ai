<template>
  <a-drawer
    :open="open"
    width="min(980px, 96vw)"
    title="审计详情"
    :body-style="{ padding: '18px' }"
    @update:open="emit('update:open', $event)"
  >
    <a-spin :spinning="loading">
      <template v-if="detail">
        <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
          <a-descriptions-item label="traceId">{{ detail.traceId }}</a-descriptions-item>
          <a-descriptions-item label="结果">{{ outcomeText(detail.auditOutcome) }}</a-descriptions-item>
          <a-descriptions-item label="来源">{{ trafficSourceText(detail.trafficSource) }}</a-descriptions-item>
          <a-descriptions-item label="接口">{{ detail.method }} {{ detail.path }}</a-descriptions-item>
          <a-descriptions-item label="状态码">{{ detail.finalStatusCode ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="AI账户">{{ displayName(detail.accountName, detail.accountId) }}</a-descriptions-item>
          <a-descriptions-item label="API Key">{{ displayName(detail.apiKeyName, detail.apiKeyId) }}</a-descriptions-item>
          <a-descriptions-item label="分组">{{ displayAuditGroupName(detail.groupName, detail.groupId) }}</a-descriptions-item>
          <a-descriptions-item label="系统账户">{{ displayName(detail.systemAccountName, detail.systemAccountId) }}</a-descriptions-item>
          <a-descriptions-item label="耗时">{{ formatDuration(detail.durationMs) }}</a-descriptions-item>
          <a-descriptions-item label="采样" :span="2">{{ detail.sampleReason }} / {{ detail.sampleBucket }}</a-descriptions-item>
          <a-descriptions-item label="错误" :span="2">{{ detail.errorMessage ?? '-' }}</a-descriptions-item>
        </a-descriptions>

        <a-tabs>
          <a-tab-pane key="attempts" tab="上游尝试">
            <ResponsiveDataList
              table-class="audit-detail-table"
              size="small"
              :columns="attemptColumns"
              :data-source="detail.attempts"
              row-key="id"
              :pagination="false"
              :table-scroll-enabled="false"
              :adaptive-column-width="false"
              :mobile-breakpoint="1024"
              :lock-body-scroll="false"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'success'">
                  <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
                </template>
                <template v-else-if="column.key === 'account'">
                  <span class="attempt-account-cell">{{ displayName(record.accountName, record.accountId) }}</span>
                </template>
                <template v-else-if="column.key === 'startedAt'">
                  <span class="detail-time-cell muted-cell">{{ formatDateTime(record.startedAt) }}</span>
                </template>
                <template v-else-if="column.key === 'duration'">
                  {{ formatDuration(record.durationMs) }}
                </template>
                <template v-else-if="column.key === 'url'">
                  <span class="url-cell">{{ record.upstreamUrl || '-' }}</span>
                </template>
                <template v-else-if="column.key === 'error'">
                  <span class="error-cell">{{ record.errorMessage || '-' }}</span>
                </template>
              </template>
              <template #card="{ record }">
                <article class="payload-mobile-card">
                  <div class="payload-mobile-card-head">
                    <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
                    <span>{{ formatDuration(record.durationMs) }}</span>
                  </div>
                  <div class="payload-mobile-card-grid">
                    <span>序号</span>
                    <strong>{{ record.attemptIndex }}</strong>
                    <span>AI账户</span>
                    <strong>{{ displayName(record.accountName, record.accountId) }}</strong>
                    <span>状态码</span>
                    <strong>{{ record.upstreamStatusCode ?? '-' }}</strong>
                    <span>时间</span>
                    <strong>{{ formatDateTime(record.startedAt) }}</strong>
                    <span>上游 URL</span>
                    <strong>{{ record.upstreamUrl }}</strong>
                  </div>
                </article>
              </template>
            </ResponsiveDataList>
          </a-tab-pane>
          <a-tab-pane key="payloads" tab="原始内容">
            <ResponsiveDataList
              table-class="audit-payload-table"
              size="small"
              :columns="payloadColumns"
              :data-source="detail.payloads"
              row-key="id"
              :pagination="false"
              :table-scroll-enabled="false"
              :adaptive-column-width="false"
              :mobile-breakpoint="1024"
              :lock-body-scroll="false"
            >
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'partType'">
                  <a-tag>{{ payloadPartText(record.partType) }}</a-tag>
                </template>
                <template v-else-if="column.key === 'size'">
                  {{ formatBytes(record.sizeBytes) }}
                </template>
                <template v-else-if="column.key === 'captureStatus'">
                  <a-tooltip :title="payloadCaptureStatusDescription(record)">
                    <a-tag>{{ captureStatusText(record.captureStatus) }}</a-tag>
                  </a-tooltip>
                </template>
                <template v-else-if="column.key === 'createdAt'">
                  <span class="detail-time-cell muted-cell">{{ formatDateTime(record.createdAt) }}</span>
                </template>
                <template v-else-if="column.key === 'headersSha256'">
                  <a-tooltip :title="record.headersSha256 || payloadHeadersHashMissingText(record)">
                    <span class="hash-cell">{{ formatHashPreview(record.headersSha256) }}</span>
                  </a-tooltip>
                </template>
                <template v-else-if="column.key === 'bodySha256'">
                  <a-tooltip :title="record.bodySha256 || payloadBodyHashMissingText(record)">
                    <span class="hash-cell">{{ formatHashPreview(record.bodySha256) }}</span>
                  </a-tooltip>
                </template>
                <template v-else-if="column.key === 'actions'">
                  <RowActions :actions="payloadActions(record)" @action-click="emit('load-payload', record.id)" />
                </template>
              </template>
              <template #card="{ record }">
                <article class="payload-mobile-card">
                  <div class="payload-mobile-card-head">
                    <a-tag>{{ payloadPartText(record.partType) }}</a-tag>
                    <span>{{ formatBytes(record.sizeBytes) }}</span>
                  </div>
                  <div class="payload-mobile-card-grid">
                    <span>序号</span>
                    <strong>{{ record.sequenceIndex }}</strong>
                    <span>类型</span>
                    <strong>{{ record.contentType || '-' }}</strong>
                    <span>状态</span>
                    <strong>{{ captureStatusText(record.captureStatus) }}</strong>
                    <span>Headers</span>
                    <strong>{{ record.hasHeaders ? '已保存' : '未保存' }}</strong>
                    <span>Body</span>
                    <strong>{{ record.hasBody ? '已保存' : '未保存' }}</strong>
                    <span>时间</span>
                    <strong>{{ formatDateTime(record.createdAt) }}</strong>
                    <span>Headers SHA256</span>
                    <strong class="hash-cell">{{ formatHashPreview(record.headersSha256) }}</strong>
                    <span>Body SHA256</span>
                    <strong class="hash-cell">{{ formatHashPreview(record.bodySha256) }}</strong>
                  </div>
                  <RowActions :actions="payloadActions(record)" variant="button" @action-click="emit('load-payload', record.id)" />
                </article>
              </template>
            </ResponsiveDataList>
            <div v-if="selectedPayload" class="payload-viewer">
              <div class="payload-viewer-toolbar">
                <div class="payload-viewer-main">
                  <strong>{{ payloadPartText(selectedPayload.partType) }}</strong>
                  <span>{{ formatBytes(selectedPayload.sizeBytes) }}</span>
                  <a-tag class="payload-state-tag" :color="payloadStorageStatusColor(selectedPayload.headersStorageStatus)">
                    {{ payloadStorageStatusText('Headers', selectedPayload.hasHeaders, selectedPayload.headersStorageStatus) }}
                  </a-tag>
                  <a-tag class="payload-state-tag" :color="payloadStorageStatusColor(selectedPayload.bodyStorageStatus)">
                    {{ payloadStorageStatusText('Body', selectedPayload.hasBody, selectedPayload.bodyStorageStatus) }}
                  </a-tag>
                  <a-tabs v-model:activeKey="payloadContentTab" class="payload-content-tabs" size="small">
                    <a-tab-pane key="headers" tab="Headers" />
                    <a-tab-pane key="body" tab="Body" />
                  </a-tabs>
                </div>
                <div class="payload-viewer-actions">
                  <a-tooltip title="搜索当前内容">
                    <a-button size="small" :disabled="!selectedPayloadCurrentText" @click="openSelectedPayloadSearch">
                      <template #icon><SearchOutlined /></template>
                    </a-button>
                  </a-tooltip>
                  <a-tooltip title="复制当前内容">
                    <a-button size="small" :disabled="!selectedPayloadCurrentText" @click="copySelectedPayloadText">
                      <template #icon><CopyOutlined /></template>
                    </a-button>
                  </a-tooltip>
                </div>
              </div>
              <ReadonlyCodeViewer
                v-if="selectedPayloadCurrentText"
                ref="payloadCodeViewer"
                attached-toolbar
                :content-type="selectedPayloadViewerContentType"
                :show-toolbar="false"
                :text="selectedPayloadCurrentText"
              />
              <a-empty v-else class="payload-empty" :description="selectedPayloadEmptyText" />
            </div>
          </a-tab-pane>
        </a-tabs>
      </template>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { CopyOutlined, SearchOutlined } from '@ant-design/icons-vue'
import { computed, ref, watch } from 'vue'

import ReadonlyCodeViewer from '@/components/ReadonlyCodeViewer.vue'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import type { AuditLogDetail, AuditLogPayloadDetail } from '@/types/domain'
import {
  captureStatusText,
  displayAuditGroupName,
  displayName,
  formatBytes,
  formatDateTime,
  formatDuration,
  formatHashPreview,
  outcomeText,
  payloadPartText,
  prettyJson,
  trafficSourceText
} from './auditLogFormatters'
import { auditAttemptColumns, auditPayloadColumns } from './auditLogTableColumns'
import {
  payloadBodyHashMissingText,
  payloadBodyUnavailableText,
  payloadCaptureStatusDescription,
  payloadHeadersHashMissingText,
  payloadStorageStatusColor,
  payloadStorageStatusText,
  type AuditPayloadRow
} from './auditPayloadDetails'

const props = defineProps<{
  detail?: AuditLogDetail
  loading: boolean
  open: boolean
  payloadLoadingId: string
  selectedPayload?: AuditLogPayloadDetail
}>()

const emit = defineEmits<{
  (event: 'load-payload', payloadId: string): void
  (event: 'update:open', value: boolean): void
}>()

const attemptColumns = auditAttemptColumns
const payloadColumns = auditPayloadColumns
const payloadContentTab = ref<'headers' | 'body'>('body')
const payloadCodeViewer = ref<{
  copyDisplayText: () => Promise<void>
  openSearch: () => Promise<void>
}>()

const selectedPayloadCurrentText = computed(() => {
  const payload = props.selectedPayload
  if (!payload) return ''
  if (payloadContentTab.value === 'headers') {
    if (!payload.hasHeaders || !payload.headers) return ''
    return prettyJson(payload.headers)
  }
  return payload.bodyText ?? payload.bodyBase64 ?? ''
})
const selectedPayloadViewerContentType = computed(() => payloadContentTab.value === 'headers'
  ? 'application/json'
  : props.selectedPayload?.contentType)
const selectedPayloadEmptyText = computed(() => {
  const payload = props.selectedPayload
  if (!payload) return '未选择原始内容'
  if (payloadContentTab.value === 'headers') {
    if (payload.headersStorageStatus === 'file_missing') {
      return 'Headers 文件缺失：数据库仍有 blob 引用，但 data/audit/blobs 下没有对应文件。'
    }
    if (payload.headersStorageStatus === 'metadata_missing') {
      return 'Headers 元数据缺失：payload 引用了不存在的 blob 记录。'
    }
    return payload.hasHeaders
      ? 'Headers 已保存，但当前窗口未读到内容。'
      : 'Headers 未保存或该部分没有 Headers。'
  }
  if (payload.bodyStorageStatus === 'file_missing') {
    return '正文文件缺失：数据库仍有 blob 引用，但 data/audit/blobs 下没有对应文件。'
  }
  if (payload.bodyStorageStatus === 'metadata_missing') {
    return '正文元数据缺失：payload 引用了不存在的 blob 记录。'
  }
  if (!payload.hasBody) {
    return payloadBodyUnavailableText(payload)
  }
  if (payload.bodyTotalBytes > 0 && payload.bodyBytesReturned === 0) {
    return '正文文件暂时不可读取，或当前窗口没有返回内容。'
  }
  return '正文为空。'
})

watch(
  () => props.selectedPayload?.id,
  () => {
    const payload = props.selectedPayload
    payloadContentTab.value = payload?.hasBody ? 'body' : 'headers'
  }
)

async function copySelectedPayloadText(): Promise<void> {
  await payloadCodeViewer.value?.copyDisplayText()
}

async function openSelectedPayloadSearch(): Promise<void> {
  await payloadCodeViewer.value?.openSearch()
}

function payloadActions(record: AuditPayloadRow): RowActionItem[] {
  const hasReadablePayload = record.hasHeaders || record.hasBody
  return [
    {
      key: 'payload',
      label: record.hasBody ? '查看原文' : record.hasHeaders ? '查看 Headers' : '无原文',
      icon: 'detail',
      tone: 'info',
      disabled: !hasReadablePayload || props.payloadLoadingId === record.id
    }
  ]
}
</script>

<style scoped>
.attempt-account-cell,
.error-cell,
.url-cell,
.detail-time-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

:deep(.audit-detail-table .ant-table-cell) {
  vertical-align: top;
  white-space: normal;
}

.attempt-account-cell,
.detail-time-cell,
.error-cell,
.url-cell {
  display: block;
  overflow: visible;
  overflow-wrap: anywhere;
  white-space: normal;
  word-break: break-word;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.payload-viewer {
  margin-top: 16px;
}

.payload-viewer-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 42px;
  padding: 0 10px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-bottom: 0;
  border-radius: 8px 8px 0 0;
}

.payload-viewer-main {
  display: flex;
  align-items: center;
  min-width: 0;
  flex: 1 1 auto;
  gap: 12px;
  color: #64748b;
  font-size: 12px;
  white-space: nowrap;
}

.payload-viewer-main strong {
  flex: 0 0 auto;
  color: #0f172a;
  font-size: 13px;
}

.payload-viewer-main span {
  overflow: hidden;
  text-overflow: ellipsis;
}

.payload-content-tabs {
  flex: 0 0 auto;
}

.payload-content-tabs :deep(.ant-tabs-nav) {
  margin-bottom: 0;
}

.payload-content-tabs :deep(.ant-tabs-nav-operations) {
  display: none !important;
}

.payload-content-tabs :deep(.ant-tabs-content-holder) {
  display: none;
}

.payload-state-tag {
  flex: 0 0 auto;
  margin-inline-end: 0;
}

.payload-viewer-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex: 0 0 auto;
  gap: 6px;
}

.hash-cell {
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.payload-empty {
  padding: 28px 12px;
  border: 1px dashed #cbd5e1;
  border-radius: 8px;
  background: #f8fafc;
}

.payload-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.payload-mobile-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.payload-mobile-card-grid {
  display: grid;
  grid-template-columns: minmax(88px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: #64748b;
  font-size: 12px;
}

.payload-mobile-card-grid strong {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
}
</style>
