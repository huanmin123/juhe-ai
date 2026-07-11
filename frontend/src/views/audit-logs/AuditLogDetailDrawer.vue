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
          <a-descriptions-item label="模型">
            <span v-if="detail.model" class="model-cell">
              <a-tag color="blue">{{ detail.model }}</a-tag>
              <a-tag v-if="detail.modelMappingApplied && detail.upstreamModel" color="orange">上游 {{ detail.upstreamModel }}</a-tag>
            </span>
            <span v-else>-</span>
          </a-descriptions-item>
          <a-descriptions-item label="状态码">{{ detail.finalStatusCode ?? '-' }}</a-descriptions-item>
          <a-descriptions-item label="AI账户">{{ displayName(detail.accountName, detail.accountId) }}</a-descriptions-item>
          <a-descriptions-item label="API Key">{{ displayName(detail.apiKeyName, detail.apiKeyId) }}</a-descriptions-item>
          <a-descriptions-item label="分组">{{ displayAuditGroupName(detail.groupName, detail.groupId) }}</a-descriptions-item>
          <a-descriptions-item label="系统账户">{{ displayName(detail.systemAccountName, detail.systemAccountId) }}</a-descriptions-item>
          <a-descriptions-item label="耗时">{{ formatDuration(detail.durationMs) }}</a-descriptions-item>
          <a-descriptions-item label="采样" :span="2">{{ detail.sampleReason }} / {{ detail.sampleBucket }}</a-descriptions-item>
          <a-descriptions-item label="错误" :span="2">{{ detail.errorMessage ?? '-' }}</a-descriptions-item>
        </a-descriptions>

        <section class="request-chain-section">
          <div class="request-chain-heading">
            <strong>请求链路</strong>
            <span>共 {{ requestChainRows.length }} 个步骤</span>
          </div>
          <a-alert
            v-if="requestChainHasOnlyMetadata"
            class="request-chain-alert"
            type="warning"
            show-icon
            message="当前记录没有保存客户端请求、上游请求、上游响应或返回客户端的原文。"
          />
          <ResponsiveDataList
            table-class="audit-detail-table"
            size="small"
            :columns="requestChainColumns"
            :data-source="requestChainRows"
            row-key="id"
            :pagination="false"
            :table-scroll-enabled="false"
            :adaptive-column-width="false"
            :mobile-breakpoint="1024"
            :lock-body-scroll="false"
          >
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'step'">
                <div class="chain-step-cell">
                  <a-tag>{{ record.phaseText }}</a-tag>
                  <span>{{ record.title }}</span>
                </div>
              </template>
              <template v-else-if="column.key === 'account'">
                <span class="attempt-account-cell">{{ displayName(record.accountName, record.accountId) }}</span>
              </template>
              <template v-else-if="column.key === 'status'">
                <a-tag :color="record.success === undefined ? 'default' : record.success ? 'green' : 'red'">{{ record.statusText }}</a-tag>
              </template>
              <template v-else-if="column.key === 'timeMetrics'">
                <span class="detail-time-cell">{{ record.time ? formatDateTime(record.time) : '-' }}</span>
                <span class="chain-secondary-text">耗时 {{ formatDuration(record.durationMs) }}</span>
              </template>
              <template v-else-if="column.key === 'data'">
                <span>{{ record.sizeBytes === undefined ? '-' : formatBytes(record.sizeBytes) }}</span>
                <a-tooltip v-if="record.payload" :title="payloadCaptureStatusDescription(record.payload)">
                  <span class="chain-secondary-text">{{ captureStatusText(record.captureStatus) }}</span>
                </a-tooltip>
                <span v-else class="chain-secondary-text">未捕获</span>
              </template>
              <template v-else-if="column.key === 'target'">
                <span :class="record.errorMessage ? 'error-cell' : 'url-cell'">{{ record.url || '-' }}</span>
              </template>
              <template v-else-if="column.key === 'actions'">
                <RowActions :actions="requestChainActions(record)" @action-click="handleRequestChainAction(record)" />
              </template>
            </template>
            <template #card="{ record }">
              <article class="payload-mobile-card">
                <div class="payload-mobile-card-head">
                  <a-tag>{{ record.phaseText }}</a-tag>
                  <a-tag :color="record.success === undefined ? 'default' : record.success ? 'green' : 'red'">{{ record.statusText }}</a-tag>
                </div>
                <strong class="payload-mobile-title">{{ record.title }}</strong>
                <div class="payload-mobile-card-grid">
                  <span>AI账户</span>
                  <strong>{{ displayName(record.accountName, record.accountId) }}</strong>
                  <span>时间</span>
                  <strong>{{ record.time ? formatDateTime(record.time) : '-' }}</strong>
                  <span>耗时</span>
                  <strong>{{ formatDuration(record.durationMs) }}</strong>
                  <span>数据</span>
                  <strong>{{ record.sizeBytes === undefined ? '-' : formatBytes(record.sizeBytes) }} · {{ record.payload ? captureStatusText(record.captureStatus) : '未捕获' }}</strong>
                  <span>目标 / 错误</span>
                  <strong :class="record.errorMessage ? 'error-cell' : 'url-cell'">{{ record.url || '-' }}</strong>
                </div>
                <RowActions :actions="requestChainActions(record)" variant="button" @action-click="handleRequestChainAction(record)" />
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
        </section>
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
import type {
  AuditLogAttemptSummary,
  AuditLogDetail,
  AuditLogPayloadDetail,
  AuditLogPayloadSummary,
  AuditPayloadPartType
} from '@/types/domain'
import {
  captureStatusText,
  displayAuditGroupName,
  displayName,
  formatBytes,
  formatDateTime,
  formatDuration,
  outcomeText,
  payloadPartText,
  prettyJson,
  trafficSourceText
} from './auditLogFormatters'
import {
  payloadBodyUnavailableText,
  payloadCaptureStatusDescription,
  payloadStorageStatusColor,
  payloadStorageStatusText
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

interface RequestChainRow {
  id: string
  sequenceText: string
  phaseText: string
  title: string
  partType: AuditPayloadPartType
  accountId?: string
  accountName?: string
  success?: boolean
  statusText: string
  time?: string
  durationMs?: number
  sizeBytes?: number
  captureStatus?: string
  url?: string
  errorMessage?: string
  payload?: AuditLogPayloadSummary
}

const requestChainColumns = [
  { title: '#', dataIndex: 'sequenceText', width: 48 },
  { title: '步骤', key: 'step', width: 170 },
  { title: 'AI账户', key: 'account', width: 130 },
  { title: '状态', key: 'status', width: 82 },
  { title: '时间 / 耗时', key: 'timeMetrics', width: 148 },
  { title: '数据', key: 'data', width: 92 },
  { title: '目标 / 错误', key: 'target', width: 240 },
  { title: '详情', key: 'actions', width: 64 }
]

const payloadContentTab = ref<'headers' | 'body'>('body')
const payloadCodeViewer = ref<{
  copyDisplayText: () => Promise<void>
  openSearch: () => Promise<void>
}>()

const requestChainHasOnlyMetadata = computed(() => {
  const payloads = props.detail?.payloads ?? []
  return payloads.length === 0 || payloads.every((payload) => payload.partType === 'gateway_metadata')
})

const requestChainRows = computed<RequestChainRow[]>(() => {
  const detail = props.detail
  if (!detail) return []

  const payloads = [...detail.payloads].sort((left, right) => left.sequenceIndex - right.sequenceIndex)
  const attemptById = new Map(detail.attempts.map((attempt) => [attempt.id, attempt]))
  const payloadsByAttemptPart = new Map<string, AuditLogPayloadSummary[]>()
  const payloadsByPart = new Map<AuditPayloadPartType, AuditLogPayloadSummary[]>()

  for (const payload of payloads) {
    if (payload.partType === 'gateway_metadata') continue
    if (payload.attemptId) {
      const key = attemptPartKey(payload.attemptId, payload.partType)
      const bucket = payloadsByAttemptPart.get(key) ?? []
      bucket.push(payload)
      payloadsByAttemptPart.set(key, bucket)
      continue
    }
    const bucket = payloadsByPart.get(payload.partType) ?? []
    bucket.push(payload)
    payloadsByPart.set(payload.partType, bucket)
  }

  const usedPayloadIds = new Set<string>()
  const rows: RequestChainRow[] = []
  const nextSequence = () => String(rows.length + 1)
  const takePayload = (partType: AuditPayloadPartType, attemptId?: string): AuditLogPayloadSummary | undefined => {
    const bucket = attemptId
      ? payloadsByAttemptPart.get(attemptPartKey(attemptId, partType))
      : payloadsByPart.get(partType)
    const payload = bucket?.shift()
    if (payload) usedPayloadIds.add(payload.id)
    return payload
  }

  const clientPayload = takePayload('client_request')
  rows.push(createClientRequestRow(detail, clientPayload, nextSequence()))

  const attempts = [...detail.attempts].sort((left, right) => left.attemptIndex - right.attemptIndex)
  for (const attempt of attempts) {
    const upstreamRequest = takePayload('upstream_request', attempt.id)
    rows.push(createUpstreamRequestRow(attempt, upstreamRequest, nextSequence()))

    const upstreamResponse = takePayload('upstream_response', attempt.id)
    rows.push(createUpstreamResponseRow(attempt, upstreamResponse, nextSequence()))
  }

  const gatewayPayload = takePayload(detail.success ? 'gateway_response' : 'gateway_error')
    ?? takePayload('gateway_response')
    ?? takePayload('gateway_error')
  rows.push(createGatewayResultRow(detail, gatewayPayload, nextSequence()))

  for (const payload of payloads) {
    if (payload.partType === 'gateway_metadata' || usedPayloadIds.has(payload.id)) continue
    rows.push(createPayloadOnlyRow(detail, payload, attemptById.get(payload.attemptId ?? ''), nextSequence()))
  }

  return rows
})

function attemptPartKey(attemptId: string, partType: AuditPayloadPartType): string {
  return `${attemptId}:${partType}`
}

function createClientRequestRow(
  detail: AuditLogDetail,
  payload: AuditLogPayloadSummary | undefined,
  sequenceText: string
): RequestChainRow {
  const target = auditDetailPath(detail)
  return {
    id: payload?.id ?? `client-request:${detail.id}`,
    sequenceText,
    phaseText: payloadPartText('client_request'),
    title: `${detail.method} ${target}`,
    partType: 'client_request',
    statusText: '收到请求',
    time: payload?.createdAt ?? detail.startedAt,
    sizeBytes: payload?.sizeBytes,
    captureStatus: payload?.captureStatus,
    url: target,
    payload
  }
}

function createUpstreamRequestRow(
  attempt: AuditLogAttemptSummary,
  payload: AuditLogPayloadSummary | undefined,
  sequenceText: string
): RequestChainRow {
  return {
    id: payload?.id ?? `upstream-request:${attempt.id}`,
    sequenceText,
    phaseText: payloadPartText('upstream_request'),
    title: `第 ${attempt.attemptIndex} 次上游请求`,
    partType: 'upstream_request',
    accountId: attempt.accountId,
    accountName: attempt.accountName,
    statusText: '已发起',
    time: payload?.createdAt ?? attempt.startedAt,
    sizeBytes: payload?.sizeBytes,
    captureStatus: payload?.captureStatus,
    url: attempt.upstreamUrl,
    payload
  }
}

function createUpstreamResponseRow(
  attempt: AuditLogAttemptSummary,
  payload: AuditLogPayloadSummary | undefined,
  sequenceText: string
): RequestChainRow {
  return {
    id: payload?.id ?? `upstream-response:${attempt.id}`,
    sequenceText,
    phaseText: payloadPartText('upstream_response'),
    title: `第 ${attempt.attemptIndex} 次上游响应`,
    partType: 'upstream_response',
    accountId: attempt.accountId,
    accountName: attempt.accountName,
    success: attempt.success,
    statusText: upstreamAttemptStatusText(attempt),
    time: payload?.createdAt ?? attempt.endedAt ?? attempt.startedAt,
    durationMs: attempt.durationMs,
    sizeBytes: payload?.sizeBytes,
    captureStatus: payload?.captureStatus,
    url: attempt.errorMessage || attempt.upstreamUrl,
    errorMessage: attempt.errorMessage,
    payload
  }
}

function createGatewayResultRow(
  detail: AuditLogDetail,
  payload: AuditLogPayloadSummary | undefined,
  sequenceText: string
): RequestChainRow {
  const partType = payload?.partType ?? (detail.success ? 'gateway_response' : 'gateway_error')
  return {
    id: payload?.id ?? `gateway-result:${detail.id}`,
    sequenceText,
    phaseText: payloadPartText(partType),
    title: detail.success ? '返回客户端' : '网关错误',
    partType,
    accountId: detail.accountId,
    accountName: detail.accountName,
    success: detail.success,
    statusText: gatewayStatusText(detail),
    time: payload?.createdAt ?? detail.endedAt,
    durationMs: detail.durationMs,
    sizeBytes: payload?.sizeBytes,
    captureStatus: payload?.captureStatus,
    url: detail.errorMessage || auditDetailPath(detail),
    errorMessage: detail.errorMessage,
    payload
  }
}

function createPayloadOnlyRow(
  detail: AuditLogDetail,
  payload: AuditLogPayloadSummary,
  attempt: AuditLogAttemptSummary | undefined,
  sequenceText: string
): RequestChainRow {
  return {
    id: payload.id,
    sequenceText,
    phaseText: payloadPartText(payload.partType),
    title: payloadPartText(payload.partType),
    partType: payload.partType,
    accountId: attempt?.accountId ?? detail.accountId,
    accountName: attempt?.accountName ?? detail.accountName,
    success: payload.partType === 'upstream_response' ? attempt?.success : undefined,
    statusText: payloadOnlyStatusText(payload, attempt),
    time: payload.createdAt,
    durationMs: attempt?.durationMs,
    sizeBytes: payload.sizeBytes,
    captureStatus: payload.captureStatus,
    url: attempt?.errorMessage || attempt?.upstreamUrl || detail.errorMessage || auditDetailPath(detail),
    errorMessage: attempt?.errorMessage ?? detail.errorMessage,
    payload
  }
}

function upstreamAttemptStatusText(attempt: AuditLogAttemptSummary): string {
  if (attempt.upstreamStatusCode !== undefined) return String(attempt.upstreamStatusCode)
  return attempt.success ? '成功' : '失败'
}

function gatewayStatusText(detail: AuditLogDetail): string {
  if (detail.finalStatusCode !== undefined) return String(detail.finalStatusCode)
  return outcomeText(detail.auditOutcome)
}

function payloadOnlyStatusText(
  payload: AuditLogPayloadSummary,
  attempt: AuditLogAttemptSummary | undefined
): string {
  if (payload.partType === 'upstream_response' && attempt) return upstreamAttemptStatusText(attempt)
  if (payload.partType === 'gateway_response') return '返回'
  if (payload.partType === 'gateway_error') return '错误'
  return '已捕获'
}

function auditDetailPath(detail: AuditLogDetail): string {
  return detail.queryString ? `${detail.path}?${detail.queryString}` : detail.path
}

function readablePayload(record?: AuditLogPayloadSummary): boolean {
  return Boolean(record && (record.hasHeaders || record.hasBody))
}

function payloadActionLabel(record: AuditLogPayloadSummary): string {
  if (!record.hasBody && record.hasHeaders) return '查看 Headers'
  if (record.partType === 'upstream_response' || record.partType === 'gateway_response') return '查看原始响应'
  if (record.partType === 'gateway_error') return '查看原始错误'
  return '查看原始请求'
}

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
  if (!payload) return '未选择原始请求'
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

function requestChainActions(record: RequestChainRow): RowActionItem[] {
  return [
    {
      key: 'payload',
      label: record.payload ? payloadActionLabel(record.payload) : '未捕获',
      icon: 'detail',
      tone: 'info',
      disabled: !readablePayload(record.payload) || props.payloadLoadingId === record.payload?.id
    }
  ]
}

function handleRequestChainAction(record: RequestChainRow): void {
  if (!readablePayload(record.payload) || !record.payload) return
  emit('load-payload', record.payload.id)
}

</script>

<style scoped>
.error-cell,
.url-cell,
.detail-time-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.model-cell {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 4px;
}

.request-chain-alert {
  margin-bottom: 12px;
}

.request-chain-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}

.request-chain-heading strong {
  color: #0f172a;
  font-size: 14px;
}

.request-chain-heading span,
.chain-secondary-text {
  color: #64748b;
  font-size: 12px;
}

.chain-secondary-text {
  display: block;
  margin-top: 3px;
}

.chain-step-cell {
  display: grid;
  min-width: 0;
  gap: 4px;
}

.chain-step-cell .ant-tag {
  width: fit-content;
  margin-inline-end: 0;
}

.chain-step-cell span {
  min-width: 0;
  overflow-wrap: anywhere;
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

.payload-mobile-title {
  min-width: 0;
  overflow-wrap: anywhere;
  color: #0f172a;
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
