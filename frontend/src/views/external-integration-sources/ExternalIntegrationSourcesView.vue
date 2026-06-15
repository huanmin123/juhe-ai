<template>
  <a-card class="page-card responsive-page-card external-source-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索来源名称"
      filter-title="公开接口授权筛选"
      :active-filter-count="activeFilterCount"
      :refresh-loading="loading"
      @search="applyFilters"
      @reset="resetFilters"
      @refresh="loadData"
    >
      <template #inline-filters>
        <a-select
          v-model:value="statusFilter"
          class="toolbar-select external-source-status responsive-list-inline-filter"
          :disabled="loading"
          :options="statusOptions"
          @change="applyFilters"
        />
      </template>
      <template #actions>
        <a-button @click="openApiDocs">
          <template #icon><book-outlined /></template>
          接入文档
        </a-button>
        <a-button type="primary" @click="openCreateSource">新增授权</a-button>
      </template>
      <template #filters>
        <a-form layout="vertical">
          <a-form-item label="状态">
            <a-select v-model:value="statusFilter" :disabled="loading" :options="statusOptions" @change="applyFilters" />
          </a-form-item>
        </a-form>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table external-source-table"
      :columns="columns"
      :data-source="rows"
      row-key="id"
      :loading="loading"
      :pagination="tablePagination"
      :scroll-x="1620"
      @change="handleTableChange"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无公开接口来源授权。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'source'">
          <div class="source-name-cell">
            <div class="source-name-line">
              <strong>{{ record.name }}</strong>
              <a-tag v-if="record.isBuiltIn" color="blue">内置</a-tag>
              <a-tag v-if="record.isBuiltIn" color="orange">Mock 数据</a-tag>
            </div>
            <span v-if="record.isBuiltIn" class="source-description">{{ builtInSourceShortDescription }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="sourceStatusColor(record.status)">{{ sourceStatusText(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'tokens'">
          <div class="token-preview-cell">
            <span class="token-preview" :title="tokenDisplayTitle(primaryToken(record))">{{ formatTokenPreview(primaryToken(record)) }}</span>
            <a-tooltip title="复制完整 Token">
              <span class="token-copy-button-wrap">
                <a-button
                  class="token-copy-button"
                  type="text"
                  size="small"
                  :loading="Boolean(tokenCopyingKey) && tokenCopyingKey === tokenCopyKey(record)"
                  :disabled="!primaryToken(record) || (Boolean(tokenCopyingKey) && tokenCopyingKey !== tokenCopyKey(record))"
                  @click="copyTokenPreview(record)"
                >
                  <template #icon><copy-outlined /></template>
                </a-button>
              </span>
            </a-tooltip>
          </div>
        </template>
        <template v-else-if="column.key === 'scopes'">
          <div class="scope-tag-line">
            <a-tag v-if="record.isBuiltIn" color="blue">全部</a-tag>
            <template v-else>
              <a-tag v-for="scope in record.scopes" :key="scope">{{ scopeLabel(scope) }}</a-tag>
            </template>
            <span v-if="!record.isBuiltIn && !record.scopes.length" class="muted-cell">未授权</span>
          </div>
        </template>
        <template v-else-if="column.key === 'rateLimits'">
          <span>{{ formatRateLimits(record.rateLimits) }}</span>
        </template>
        <template v-else-if="column.key === 'notes'">
          <span class="source-note-cell" :title="sourceNotes(record)">{{ sourceNotes(record) }}</span>
        </template>
        <template v-else-if="column.key === 'expiresAt'">
          <span :class="record.expiresAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.expiresAt) }}</span>
        </template>
        <template v-else-if="column.key === 'lastUsedAt'">
          <span :class="record.lastUsedAt ? 'name-cell' : 'muted-cell'">{{ formatDateTime(record.lastUsedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="sourceActions(record)" @action-click="handleSourceAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div>
              <div class="mobile-list-card-title">
                {{ record.name }}
                <a-tag v-if="record.isBuiltIn" color="blue">内置</a-tag>
                <a-tag v-if="record.isBuiltIn" color="orange">Mock 数据</a-tag>
              </div>
            </div>
            <a-tag :color="sourceStatusColor(record.status)">{{ sourceStatusText(record.status) }}</a-tag>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item">
              <span>Token</span>
              <strong>{{ formatTokenPreview(primaryToken(record)) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>限频</span>
              <strong>{{ formatRateLimits(record.rateLimits) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>到期</span>
              <strong>{{ formatDateTime(record.expiresAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>最近调用</span>
              <strong>{{ formatDateTime(record.lastUsedAt) }}</strong>
            </div>
          </div>
          <div class="mobile-list-note">
            <span>备注</span>
            <strong>{{ sourceNotes(record) }}</strong>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="sourceActions(record)" @action-click="handleSourceAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <ExternalSourceApiDocsModal v-model:open="apiDocsOpen" />

    <ExternalSourceFormModal
      v-model:open="sourceModalOpen"
      :editing="Boolean(editingSourceId)"
      :form="sourceForm"
      :saving="sourceSaving"
      :scope-options="scopeOptions"
      @add-rate-limit="addRateLimit"
      @remove-rate-limit="removeRateLimit"
      @save="saveSource"
    />

    <ExternalSourceCreatedTokenModal
      :open="createdTokenOpen"
      :public-api-base-url="publicApiBaseUrl"
      :token="createdTokenPlain"
      @close="closeCreatedTokenModal"
      @open-docs="openApiDocs"
    />
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { BookOutlined, CopyOutlined } from '@ant-design/icons-vue'

import { api, type ExternalIntegrationSourceListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime } from '@/shared/formatters'
import type {
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary
} from '@/types/domain'
import ExternalSourceApiDocsModal from './ExternalSourceApiDocsModal.vue'
import ExternalSourceCreatedTokenModal from './ExternalSourceCreatedTokenModal.vue'
import ExternalSourceFormModal from './ExternalSourceFormModal.vue'
import { resolvePublicApiBaseUrl } from './externalSourceApiDocs'
import {
  buildSourcePayload,
  createDefaultRateLimit,
  createEmptySourceForm,
  createSourceFormFromRecord,
  formatRateLimits,
  type ExternalSourceForm
} from './externalSourceFormModel'
import { useExternalSourceTokenActions } from './useExternalSourceTokenActions'

const pageSize = 20
const loading = ref(false)
const keyword = ref('')
const statusFilter = ref<ExternalIntegrationSourceStatus | 'all'>('all')
const rows = ref<ExternalIntegrationSourceSummary[]>([])
const paginationUpperBound = ref(0)
const pagination = reactive({ current: 1, pageSize })
const scopeOptions = ref<ExternalIntegrationScopeOption[]>([])

const apiDocsOpen = ref(false)
const publicApiBaseUrl = computed(() => resolvePublicApiBaseUrl())

const sourceModalOpen = ref(false)
const sourceSaving = ref(false)
const editingSourceId = ref<string>()
const sourceForm = reactive<ExternalSourceForm>(createEmptySourceForm())

const builtInSourceShortDescription = '系统内置联调用来源'
const builtInSourceDescription = '系统内置联调用来源，已授权全部公开接口；复制完整 Token 调用 /__aipublic__ 接口时只返回 Mock 数据，可用于对接请求头、参数和响应解析。'
const {
  createdTokenOpen,
  createdTokenPlain,
  generatingTokenSourceId,
  tokenCopyingKey,
  showCreatedToken,
  clearCreatedToken,
  closeCreatedTokenModal,
  resetBuiltInTestToken,
  generateSourceToken,
  copyTokenPreview,
  formatTokenPreview,
  tokenDisplayTitle,
  primaryToken,
  tokenCopyKey
} = useExternalSourceTokenActions({
  reload: loadData
})

const statusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const columns = [
  { title: '来源授权', key: 'source', width: 180, fixed: 'left', align: 'left' },
  { title: '状态', key: 'status', width: 100, align: 'left' },
  { title: 'Token', key: 'tokens', width: 220, align: 'left' },
  { title: '接口资源授权', key: 'scopes', width: 300, className: 'scope-column', align: 'left' },
  { title: '备注', key: 'notes', width: 260, align: 'left' },
  { title: '限频', key: 'rateLimits', width: 180, align: 'left' },
  { title: '到期时间', key: 'expiresAt', width: 180, align: 'left' },
  { title: '最近调用', key: 'lastUsedAt', width: 180, align: 'left' },
  { title: '操作', key: 'actions', width: 120, fixed: 'right', align: 'left' }
]

const activeFilterCount = computed(() => {
  let count = 0
  if (keyword.value.trim()) count += 1
  if (statusFilter.value !== 'all') count += 1
  return count
})

const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: paginationUpperBound.value,
  showSizeChanger: true
}))

onMounted(() => {
  void loadScopes()
  void loadData()
})

async function loadScopes(): Promise<void> {
  try {
    scopeOptions.value = await api.externalIntegrationSources.scopes()
  } catch {
    scopeOptions.value = [
      { value: 'external_integrations:source_auth_demo:read', label: 'GET 来源鉴权 Demo' },
      { value: 'juhe_ai_public:ip_usage:read', label: 'GET IP 维度消费聚合' },
      { value: 'juhe_ai_public:account_usage:read', label: 'GET 账号维度实际消耗聚合' }
    ]
  }
}

function openApiDocs(): void {
  apiDocsOpen.value = true
}

async function loadData(): Promise<void> {
  loading.value = true
  try {
    const result = await api.externalIntegrationSources.list(buildListParams())
    rows.value = result.items
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    paginationUpperBound.value = result.pageUpperBound
  } catch (error) {
    message.error(extractApiErrorMessage(error, '加载公开接口授权失败'))
  } finally {
    loading.value = false
  }
}

function buildListParams(): ExternalIntegrationSourceListParams {
  return {
    page: pagination.current,
    pageSize: pagination.pageSize,
    keyword: keyword.value.trim() || undefined,
    status: statusFilter.value
  }
}

function applyFilters(): void {
  pagination.current = 1
  void loadData()
}

function resetFilters(): void {
  keyword.value = ''
  statusFilter.value = 'all'
  applyFilters()
}

function handleTableChange(nextPagination: unknown): void {
  const paginationInfo = (nextPagination && typeof nextPagination === 'object' ? nextPagination : {}) as { current?: number; pageSize?: number }
  pagination.current = paginationInfo.current ?? 1
  pagination.pageSize = paginationInfo.pageSize ?? pageSize
  void loadData()
}

function openCreateSource(): void {
  editingSourceId.value = undefined
  clearCreatedToken()
  Object.assign(sourceForm, createEmptySourceForm(scopeOptions.value))
  sourceModalOpen.value = true
}

function openEditSource(record: ExternalIntegrationSourceSummary): void {
  let nextForm: ExternalSourceForm
  try {
    nextForm = createSourceFormFromRecord(record)
  } catch (error) {
    message.error(extractApiErrorMessage(error, '来源授权数据异常，请清理后再编辑'))
    return
  }
  editingSourceId.value = record.id
  clearCreatedToken()
  Object.assign(sourceForm, nextForm)
  sourceModalOpen.value = true
}

async function saveSource(): Promise<void> {
  if (!sourceForm.name.trim()) {
    message.error('请填写来源名称')
    return
  }
  sourceSaving.value = true
  try {
    const payload = buildSourcePayload(sourceForm)
    if (editingSourceId.value) {
      await api.externalIntegrationSources.update(editingSourceId.value, payload)
      message.success('来源授权已更新')
      sourceModalOpen.value = false
    } else {
      const result = await api.externalIntegrationSources.create(payload)
      sourceModalOpen.value = false
      showCreatedToken(result.token.token)
      message.success('来源授权已创建')
    }
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '保存来源授权失败'))
  } finally {
    sourceSaving.value = false
  }
}

function addRateLimit(): void {
  sourceForm.rateLimits.push(createDefaultRateLimit())
}

function removeRateLimit(index: number): void {
  sourceForm.rateLimits.splice(index, 1)
}

function sourceActions(record: ExternalIntegrationSourceSummary): RowActionItem[] {
  const statusAction: RowActionItem = record.status === 'active'
    ? { key: 'disable', label: '停用', icon: 'disable', tone: 'danger' as const }
    : { key: 'enable', label: '启用', icon: 'enable', tone: 'success' as const }
  if (record.isBuiltIn) {
    return [
      statusAction,
      {
        key: 'resetToken',
        label: '重置',
        icon: 'reset',
        tone: 'warning' as const,
        confirmTitle: '确认重置内置测试 Token？旧 Token 会立即失效。',
        confirmOkText: '重置'
      }
    ]
  }
  const generateTokenAction: RowActionItem | undefined = primaryToken(record)
    ? undefined
    : {
        key: 'generateToken',
        label: '生成 Token',
        icon: 'password',
        tone: 'info' as const,
        disabled: Boolean(generatingTokenSourceId.value) && generatingTokenSourceId.value !== record.id,
        confirmTitle: `确认给来源授权“${record.name}”生成新的生产 Token？`,
        confirmOkText: '生成'
      }
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' },
    ...(generateTokenAction ? [generateTokenAction] : []),
    statusAction,
    { key: 'delete', label: '删除', icon: 'delete', tone: 'danger', confirmTitle: `确认删除来源授权 ${record.name}？`, confirmOkText: '删除' }
  ]
}

function handleSourceAction(key: string, record: ExternalIntegrationSourceSummary): void {
  if (key === 'edit') {
    openEditSource(record)
    return
  }
  if (key === 'enable' || key === 'disable') {
    void updateSourceStatus(record, key === 'enable' ? 'active' : 'disabled')
    return
  }
  if (key === 'delete') {
    void deleteSource(record)
    return
  }
  if (key === 'generateToken') {
    void generateSourceToken(record)
    return
  }
  if (key === 'resetToken') {
    void resetBuiltInTestToken()
  }
}

async function updateSourceStatus(record: ExternalIntegrationSourceSummary, status: ExternalIntegrationSourceStatus): Promise<void> {
  try {
    await api.externalIntegrationSources.update(record.id, { status })
    message.success(status === 'active' ? '来源授权已启用' : '来源授权已停用')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '更新来源授权状态失败'))
  }
}

async function deleteSource(record: ExternalIntegrationSourceSummary): Promise<void> {
  try {
    await api.externalIntegrationSources.delete(record.id)
    if (rows.value.length <= 1 && pagination.current > 1) {
      pagination.current -= 1
    }
    message.success('来源授权已删除')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '删除来源授权失败'))
  }
}

function sourceNotes(record: ExternalIntegrationSourceSummary): string {
  if (record.isBuiltIn) {
    return builtInSourceDescription
  }
  return record.notes?.trim() || '无备注'
}

function sourceStatusText(status: ExternalIntegrationSourceStatus): string {
  return status === 'active' ? '启用' : '停用'
}

function sourceStatusColor(status: ExternalIntegrationSourceStatus): string {
  return status === 'active' ? 'green' : 'red'
}

function scopeLabel(scope: string): string {
  return scopeOptions.value.find((item) => item.value === scope)?.label ?? scope
}

</script>

<style scoped>
.external-source-card :deep(.ant-card-body) {
  min-width: 0;
}

.external-source-status {
  width: 132px;
}

.source-name-cell {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.source-name-line {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}

.source-name-line strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.source-name-cell span {
  color: #64748b;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 12px;
}

.source-name-cell .source-description {
  font-family: inherit;
}

.tag-line {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.scope-tag-line {
  display: flex;
  width: 300px;
  max-width: 100%;
  flex-wrap: wrap;
  gap: 4px;
  white-space: normal;
}

.scope-tag-line :deep(.ant-tag) {
  max-width: 100%;
  margin-inline-end: 0;
  overflow-wrap: anywhere;
  white-space: normal;
}

.source-note-cell {
  display: -webkit-box;
  max-width: 260px;
  overflow: hidden;
  color: #475569;
  line-height: 1.5;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.mobile-list-note {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 4px;
  border-top: 1px solid #f1f5f9;
  padding-top: 10px;
}

.mobile-list-note span {
  color: #64748b;
  font-size: 12px;
}

.mobile-list-note strong {
  color: #334155;
  font-size: 13px;
  font-weight: 500;
  line-height: 1.5;
  overflow-wrap: anywhere;
}

.external-source-table :deep(.scope-column) {
  max-width: 300px;
  white-space: normal;
}

.link-button {
  border: 0;
  padding: 0;
  background: transparent;
  color: #1677ff;
  cursor: pointer;
}

.token-preview-cell {
  display: flex;
  align-items: center;
  width: 100%;
  min-width: 0;
  gap: 8px;
}

.token-preview {
  display: inline-flex;
  align-items: center;
  max-width: calc(100% - 32px);
  box-sizing: border-box;
  padding: 3px 8px;
  overflow: hidden;
  color: #008b8b;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 18px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 4px;
  background: #eefafa;
}

.token-copy-button {
  color: #64748b;
}

.token-copy-button-wrap {
  flex: none;
}

.token-copy-button:hover:not(:disabled) {
  color: #1677ff;
  background: #eff6ff;
}

</style>
