<template>
  <a-drawer
    :open="open"
    :title="record ? `使用记录：${accountDisplayText(record)}` : '使用记录详情'"
    width="720"
    placement="right"
    @close="$emit('close')"
    @update:open="handleOpenUpdate"
  >
    <template v-if="record" #extra>
      <a-space>
        <a-button size="small" @click="copyTraceId">
          <template #icon><copy-outlined /></template>
          Trace ID
        </a-button>
        <a-button size="small" @click="copyDiagnosticSummary">
          <template #icon><copy-outlined /></template>
          排障摘要
        </a-button>
      </a-space>
    </template>
    <a-empty v-if="!record" description="请选择一条使用记录" />
    <a-spin v-else :spinning="loading" tip="正在加载快照详情">
      <div class="usage-record-detail">
      <section class="detail-section">
        <h4>请求概览</h4>
        <div class="detail-grid">
          <div v-if="isManagementView" class="detail-item">
            <span>系统账户</span>
            <strong>{{ usageRecordSystemAccountText(record) }}</strong>
          </div>
          <div class="detail-item">
            <span>Trace ID</span>
            <strong class="mono-text">{{ record.traceId }}</strong>
          </div>
          <div class="detail-item">
            <span>AI 账户</span>
            <strong>{{ accountDisplayText(record) }}</strong>
          </div>
          <div class="detail-item">
            <span>接口</span>
            <strong class="mono-text">{{ formatEndpoint(record.endpoint) }}</strong>
          </div>
          <div class="detail-item">
            <span>模型</span>
            <strong>{{ record.model || '-' }}</strong>
          </div>
          <div class="detail-item">
            <span>状态</span>
            <strong>{{ statusCodeText(record) }} / {{ record.success ? '成功' : '失败' }}</strong>
          </div>
          <div class="detail-item">
            <span>请求类型</span>
            <strong>{{ record.stream ? '流式' : '非流式' }}</strong>
          </div>
          <div class="detail-item">
            <span>客户端 IP</span>
            <strong class="mono-text">{{ record.clientIp || '-' }}</strong>
          </div>
          <div class="detail-item">
            <span>时间</span>
            <strong>{{ formatDateTime(record.createdAt) }}</strong>
          </div>
        </div>
      </section>

      <section class="detail-section">
        <h4>用量与耗时</h4>
        <div class="detail-grid">
          <div class="detail-item">
            <span>Tokens</span>
            <strong>{{ formatRecordTokens(record) }}</strong>
          </div>
          <div class="detail-item">
            <span>成本</span>
            <strong>{{ formatCost(record.costUsd) }}</strong>
          </div>
          <div class="detail-item">
            <span>首 token</span>
            <strong>{{ formatDuration(record.firstTokenMs) }}</strong>
          </div>
          <div class="detail-item">
            <span>总耗时</span>
            <strong>{{ formatDuration(record.durationMs) }}</strong>
          </div>
          <div class="detail-item">
            <span>API Key</span>
            <strong>{{ displayName(record.apiKeyName, record.apiKeyId) }}</strong>
          </div>
          <div class="detail-item">
            <span>分组</span>
            <strong>{{ displayName(record.groupName, record.groupId) }}</strong>
          </div>
        </div>
      </section>

      <section v-if="!record.success || record.errorMessage || record.errorCode" class="detail-section">
        <h4>错误信息</h4>
        <div class="error-panel">
          <span v-if="record.errorCode" class="mono-text">{{ record.errorCode }}</span>
          <strong>{{ errorText(record) }}</strong>
        </div>
      </section>

      <section class="detail-section">
        <div class="detail-section-head">
          <h4>请求快照</h4>
          <a-button size="small" type="link" :disabled="loading" @click="copySnapshot('request')">复制</a-button>
        </div>
        <pre class="snapshot-block">{{ formatSnapshot(record.requestSnapshot) }}</pre>
      </section>

      <section class="detail-section">
        <div class="detail-section-head">
          <h4>响应快照</h4>
          <a-button size="small" type="link" :disabled="loading" @click="copySnapshot('response')">复制</a-button>
        </div>
        <pre class="snapshot-block">{{ formatSnapshot(record.responseSnapshot) }}</pre>
      </section>
      </div>
    </a-spin>
  </a-drawer>
</template>

<script setup lang="ts">
import { CopyOutlined } from '@ant-design/icons-vue'

import { message } from '@/lib/antd'
import type { UsageRecordSummary } from '@/types/domain'
import {
  accountDisplayText,
  displayName,
  errorText,
  formatCost,
  formatDateTime,
  formatDuration,
  formatEndpoint,
  formatRecordTokens,
  statusCodeText,
  usageRecordSystemAccountText
} from './usageRecordFormatters'

const props = defineProps<{
  isManagementView: boolean
  loading?: boolean
  open: boolean
  record?: UsageRecordSummary
}>()

const emit = defineEmits<{
  (event: 'close'): void
  (event: 'update:open', value: boolean): void
}>()

function handleOpenUpdate(value: boolean) {
  emit('update:open', value)
}

function formatSnapshot(value?: Record<string, unknown>): string {
  if (!value || !Object.keys(value).length) return '暂无快照'
  return JSON.stringify(value, null, 2)
}

async function copyText(value: string, successText = '已复制'): Promise<void> {
  if (!value) return
  if (!navigator.clipboard?.writeText) {
    message.error('当前浏览器不支持自动复制，请手动选择内容复制')
    return
  }
  try {
    await navigator.clipboard.writeText(value)
    message.success(successText)
  } catch (error) {
    console.error(error)
    message.error('复制失败，请手动选择内容复制')
  }
}

async function copyTraceId(): Promise<void> {
  if (!props.record?.traceId) return
  await copyText(props.record.traceId, 'Trace ID 已复制')
}

async function copyDiagnosticSummary(): Promise<void> {
  const record = props.record
  if (!record) return
  await copyText(buildDiagnosticSummary(record), '排障摘要已复制')
}

async function copySnapshot(type: 'request' | 'response'): Promise<void> {
  const record = props.record
  if (!record) return
  const value = type === 'request' ? record.requestSnapshot : record.responseSnapshot
  await copyText(formatSnapshot(value), type === 'request' ? '请求快照已复制' : '响应快照已复制')
}

function buildDiagnosticSummary(record: UsageRecordSummary): string {
  return [
    `Trace ID: ${record.traceId}`,
    `系统账户: ${props.isManagementView ? usageRecordSystemAccountText(record) : '-'}`,
    `AI 账户: ${accountDisplayText(record)}`,
    `接口: ${formatEndpoint(record.endpoint)}`,
    `模型: ${record.model || '-'}`,
    `状态: ${statusCodeText(record)} / ${record.success ? '成功' : '失败'}`,
    `请求类型: ${record.stream ? '流式' : '非流式'}`,
    `API Key: ${displayName(record.apiKeyName, record.apiKeyId)}`,
    `分组: ${displayName(record.groupName, record.groupId)}`,
    `Tokens: ${formatRecordTokens(record)}`,
    `成本: ${formatCost(record.costUsd)}`,
    `首 token: ${formatDuration(record.firstTokenMs)}`,
    `总耗时: ${formatDuration(record.durationMs)}`,
    `客户端 IP: ${record.clientIp || '-'}`,
    `时间: ${formatDateTime(record.createdAt)}`,
    `错误码: ${record.errorCode || '-'}`,
    `错误信息: ${record.errorMessage || '-'}`
  ].join('\n')
}
</script>

<style scoped>
.usage-record-detail {
  display: grid;
  gap: 18px;
}

.detail-section {
  display: grid;
  gap: 10px;
}

.detail-section h4 {
  margin: 0;
  color: #0f172a;
  font-size: 15px;
  font-weight: 700;
}

.detail-section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.detail-section-head :deep(.ant-btn) {
  padding-right: 0;
  padding-left: 0;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

.detail-item {
  display: grid;
  gap: 4px;
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid #edf1f7;
  border-radius: 8px;
  background: #f8fafc;
}

.detail-item span {
  color: #64748b;
  font-size: 12px;
}

.detail-item strong {
  min-width: 0;
  overflow-wrap: anywhere;
  color: #0f172a;
  font-size: 13px;
  font-weight: 600;
}

.mono-text {
  font-family: Consolas, 'Courier New', monospace;
}

.error-panel {
  display: grid;
  gap: 6px;
  padding: 12px;
  border: 1px solid #fecaca;
  border-radius: 8px;
  background: #fef2f2;
  color: #991b1b;
}

.snapshot-block {
  max-height: 280px;
  margin: 0;
  overflow: auto;
  padding: 12px;
  border: 1px solid #edf1f7;
  border-radius: 8px;
  background: #0f172a;
  color: #e2e8f0;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

@media (max-width: 720px) {
  .detail-grid {
    grid-template-columns: 1fr;
  }
}
</style>
