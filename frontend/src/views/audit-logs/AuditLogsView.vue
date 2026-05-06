<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="traceIdFilter"
      search-placeholder="搜索 traceId"
      filter-title="审计筛选"
      :active-filter-count="activeFilterCount"
      :refresh-loading="loading"
      @refresh="loadData"
      @reset="resetFilters"
      @search="applyFilters"
    >
      <template #inline-filters>
        <a-select v-model:value="outcomeFilter" class="toolbar-select audit-outcome-filter responsive-list-inline-filter" :options="outcomeOptions" @change="applyFilters" />
        <a-input v-model:value="pathFilter" allow-clear class="toolbar-select audit-path-filter responsive-list-inline-filter" placeholder="接口路径" @press-enter="applyFilters" />
        <a-input v-model:value="statusCodeFilter" allow-clear class="toolbar-select audit-status-filter responsive-list-inline-filter" placeholder="状态码" @press-enter="applyFilters" />
        <a-input v-model:value="modelFilter" allow-clear class="toolbar-select audit-model-filter responsive-list-inline-filter" placeholder="模型" @press-enter="applyFilters" />
        <a-input v-model:value="clientIpFilter" allow-clear class="toolbar-select audit-client-ip-filter responsive-list-inline-filter" placeholder="客户端 IP" @press-enter="applyFilters" />
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="结果">
            <a-select v-model:value="outcomeFilter" :options="outcomeOptions" />
          </a-form-item>
          <a-form-item label="接口路径">
            <a-input v-model:value="pathFilter" allow-clear placeholder="/v1/responses" />
          </a-form-item>
          <a-form-item label="状态码">
            <a-input v-model:value="statusCodeFilter" allow-clear placeholder="401 / 503" />
          </a-form-item>
          <a-form-item label="模型">
            <a-input v-model:value="modelFilter" allow-clear placeholder="模型名称" />
          </a-form-item>
          <a-form-item label="客户端 IP">
            <a-input v-model:value="clientIpFilter" allow-clear placeholder="IP 地址" />
          </a-form-item>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <AuditLogList
      :records="records"
      :loading="loading"
      :pagination="tablePagination"
      :mobile-has-more="mobileHasMore"
      :loading-more="mobileLoadingMore"
      @change="handleTableChange"
      @detail="openDetail"
      @mobile-load-more="loadMoreMobileRecords"
      @mobile-refresh="refreshMobileRecords"
    />

    <a-drawer v-model:open="detailOpen" width="min(980px, 96vw)" title="审计详情" :body-style="{ padding: '18px' }">
      <a-spin :spinning="detailLoading">
        <template v-if="detail">
          <a-alert
            type="warning"
            show-icon
            message="原始审计日志包含完整凭据、请求头、请求体和响应内容，仅用于管理员排障。"
            class="detail-warning"
          />
          <a-descriptions bordered size="small" :column="2" class="detail-descriptions">
            <a-descriptions-item label="traceId">{{ detail.traceId }}</a-descriptions-item>
            <a-descriptions-item label="结果">{{ outcomeText(detail.auditOutcome) }}</a-descriptions-item>
            <a-descriptions-item label="接口">{{ detail.method }} {{ detail.path }}</a-descriptions-item>
            <a-descriptions-item label="状态码">{{ detail.finalStatusCode ?? '-' }}</a-descriptions-item>
            <a-descriptions-item label="账号">{{ displayName(detail.accountName, detail.accountId) }}</a-descriptions-item>
            <a-descriptions-item label="API Key">{{ displayName(detail.apiKeyName, detail.apiKeyId) }}</a-descriptions-item>
            <a-descriptions-item label="分组">{{ displayName(detail.groupName, detail.groupId) }}</a-descriptions-item>
            <a-descriptions-item label="系统账户">{{ displayName(detail.systemAccountName, detail.systemAccountId) }}</a-descriptions-item>
            <a-descriptions-item label="耗时">{{ formatDuration(detail.durationMs) }}</a-descriptions-item>
            <a-descriptions-item label="采样">{{ detail.sampleReason }} / {{ detail.sampleBucket }}</a-descriptions-item>
            <a-descriptions-item label="错误" :span="2">{{ detail.errorMessage ?? '-' }}</a-descriptions-item>
          </a-descriptions>

          <a-tabs>
            <a-tab-pane key="attempts" tab="上游尝试">
              <a-table size="small" :pagination="false" :columns="attemptColumns" :data-source="detail.attempts" row-key="id">
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'success'">
                    <a-tag :color="record.success ? 'green' : 'red'">{{ record.success ? '成功' : '失败' }}</a-tag>
                  </template>
                  <template v-else-if="column.key === 'account'">
                    {{ displayName(record.accountName, record.accountId) }}
                  </template>
                  <template v-else-if="column.key === 'duration'">
                    {{ formatDuration(record.durationMs) }}
                  </template>
                  <template v-else-if="column.key === 'url'">
                    <span class="url-cell">{{ record.upstreamUrl }}</span>
                  </template>
                </template>
              </a-table>
            </a-tab-pane>
            <a-tab-pane key="payloads" tab="原始内容">
              <a-table size="small" :pagination="false" :columns="payloadColumns" :data-source="detail.payloads" row-key="id">
                <template #bodyCell="{ column, record }">
                  <template v-if="column.key === 'partType'">
                    <a-tag>{{ payloadPartText(record.partType) }}</a-tag>
                  </template>
                  <template v-else-if="column.key === 'size'">
                    {{ formatBytes(record.sizeBytes) }}
                  </template>
                  <template v-else-if="column.key === 'actions'">
                    <a-button type="link" size="small" :loading="payloadLoadingId === record.id" @click="loadPayload(record.id)">查看原文</a-button>
                  </template>
                </template>
              </a-table>
              <div v-if="selectedPayload" class="payload-viewer">
                <div class="payload-viewer-head">
                  <strong>{{ payloadPartText(selectedPayload.partType) }}</strong>
                  <span>{{ formatBytes(selectedPayload.sizeBytes) }}</span>
                </div>
                <a-tabs>
                  <a-tab-pane key="headers" tab="Headers">
                    <pre class="raw-block">{{ prettyJson(selectedPayload.headers ?? {}) }}</pre>
                  </a-tab-pane>
                  <a-tab-pane key="body" tab="Body">
                    <pre class="raw-block">{{ selectedPayload.bodyText ?? selectedPayload.bodyBase64 ?? '' }}</pre>
                  </a-tab-pane>
                </a-tabs>
              </div>
            </a-tab-pane>
          </a-tabs>
        </template>
      </a-spin>
    </a-drawer>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from '@/lib/antd'

import { api } from '@/api/client'
import type { AuditLogDetail, AuditLogPayloadDetail, AuditLogSummary, AuditOutcome, AuditLogRuntime } from '@/types/domain'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import AuditLogList from './AuditLogList.vue'
import {
  displayName,
  formatBytes,
  formatDateTime,
  formatDuration,
  normalizedStatusCode,
  outcomeText,
  payloadPartText,
  prettyJson
} from './auditLogFormatters'
import {
  auditAttemptColumns,
  auditOutcomeOptions,
  auditPayloadColumns
} from './auditLogTableColumns'

const loading = ref(false)
const detailLoading = ref(false)
const payloadLoadingId = ref('')
const mobileLoadingMore = ref(false)
const records = ref<AuditLogSummary[]>([])
const runtime = ref<AuditLogRuntime>()
const detail = ref<AuditLogDetail>()
const selectedPayload = ref<AuditLogPayloadDetail>()
const detailOpen = ref(false)

const traceIdFilter = ref('')
const outcomeFilter = ref<AuditOutcome | 'all'>('all')
const pathFilter = ref('')
const statusCodeFilter = ref('')
const modelFilter = ref('')
const clientIpFilter = ref('')
const pageSize = 100
const pagination = reactive({ current: 1, pageSize, total: 0 })

const outcomeOptions = auditOutcomeOptions
const attemptColumns = auditAttemptColumns
const payloadColumns = auditPayloadColumns

const activeFilterCount = computed(() => {
  let count = 0
  if (outcomeFilter.value !== 'all') count += 1
  if (pathFilter.value.trim()) count += 1
  if (statusCodeFilter.value.trim()) count += 1
  if (modelFilter.value.trim()) count += 1
  if (clientIpFilter.value.trim()) count += 1
  return count
})

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  hideOnSinglePage: true,
  showSizeChanger: false,
  showTotal: (total: number) => `共 ${total} 条审计日志`
}))

const mobileHasMore = computed(() => records.value.length < pagination.total)

function applyFilters(): void {
  pagination.current = 1
  void loadData()
}

function resetFilters(): void {
  traceIdFilter.value = ''
  outcomeFilter.value = 'all'
  pathFilter.value = ''
  statusCodeFilter.value = ''
  modelFilter.value = ''
  clientIpFilter.value = ''
  pagination.current = 1
  void loadData()
}

async function loadData(options: { append?: boolean; quiet?: boolean } = {}): Promise<void> {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const [listResult, runtimeInfo] = await Promise.all([
      api.auditLogs.list({
        page: pagination.current,
        pageSize: pagination.pageSize,
        traceId: traceIdFilter.value || undefined,
        outcome: outcomeFilter.value,
        path: pathFilter.value || undefined,
        statusCode: normalizedStatusCode(statusCodeFilter.value),
        model: modelFilter.value || undefined,
        clientIp: clientIpFilter.value || undefined
      }),
      api.auditLogs.runtime()
    ])
    pagination.current = listResult.page
    pagination.pageSize = listResult.pageSize
    pagination.total = listResult.total
    records.value = options.append ? [...records.value, ...listResult.items] : listResult.items
    runtime.value = runtimeInfo
  } catch (error) {
    console.error(error)
    message.error('加载审计日志失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
  }
}

function handleTableChange(paginationInfo: unknown): void {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
  pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : pageSize
  void loadData()
}

async function loadMoreMobileRecords(): Promise<void> {
  if (!mobileHasMore.value || mobileLoadingMore.value) return
  mobileLoadingMore.value = true
  pagination.current += 1
  try {
    await loadData({ append: true, quiet: true })
  } finally {
    mobileLoadingMore.value = false
  }
}

async function refreshMobileRecords(): Promise<void> {
  pagination.current = 1
  await loadData()
}

async function openDetail(record: AuditLogSummary): Promise<void> {
  detailOpen.value = true
  detailLoading.value = true
  selectedPayload.value = undefined
  try {
    detail.value = await api.auditLogs.detail(record.id)
  } catch (error) {
    console.error(error)
    message.error('加载审计详情失败')
  } finally {
    detailLoading.value = false
  }
}

async function loadPayload(payloadId: string): Promise<void> {
  if (!detail.value) return
  payloadLoadingId.value = payloadId
  try {
    selectedPayload.value = await api.auditLogs.payload(detail.value.id, payloadId)
  } catch (error) {
    console.error(error)
    message.error('加载原始内容失败')
  } finally {
    payloadLoadingId.value = ''
  }
}

onMounted(loadData)
</script>

<style scoped>
.audit-outcome-filter {
  width: 132px;
}

.audit-path-filter {
  width: 220px;
}

.audit-status-filter {
  width: 108px;
}

.audit-model-filter {
  width: 180px;
}

.audit-client-ip-filter {
  width: 150px;
}

.url-cell,
.mono-cell {
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
}

.url-cell {
  display: inline-block;
  max-width: 320px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}

.detail-warning {
  margin-bottom: 14px;
}

.detail-descriptions {
  margin-bottom: 16px;
}

.payload-viewer {
  margin-top: 16px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  overflow: hidden;
}

.payload-viewer-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  background: #f8fafc;
  border-bottom: 1px solid #e2e8f0;
}

.raw-block {
  max-height: 420px;
  margin: 0;
  padding: 12px;
  overflow: auto;
  color: #0f172a;
  background: #f8fafc;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>
