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

    <ExternalIntegrationSourceList
      :actions="sourceActions"
      :built-in-source-short-description="builtInSourceShortDescription"
      :data-source="rows"
      :format-token-preview="formatTokenPreview"
      :loading="loading"
      :pagination="tablePagination"
      :primary-token="primaryToken"
      :scope-label="scopeLabel"
      :source-notes="sourceNotes"
      :token-copying-key="tokenCopyingKey"
      :token-copy-key="tokenCopyKey"
      :token-display-title="tokenDisplayTitle"
      @action-click="handleSourceAction"
      @change="handleTableChange"
      @copy-token="copyTokenPreview"
    />

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
import { BookOutlined } from '@ant-design/icons-vue'

import { api, type ExternalIntegrationSourceListParams } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { RowActionItem } from '@/components/rowActions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type {
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary
} from '@/types/domain'
import ExternalSourceApiDocsModal from './ExternalSourceApiDocsModal.vue'
import ExternalSourceCreatedTokenModal from './ExternalSourceCreatedTokenModal.vue'
import ExternalSourceFormModal from './ExternalSourceFormModal.vue'
import ExternalIntegrationSourceList from './ExternalIntegrationSourceList.vue'
import { resolvePublicApiBaseUrl } from './externalSourceApiDocs'
import {
  buildSourcePayload,
  createDefaultRateLimit,
  createEmptySourceForm,
  createSourceFormFromRecord,
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
    ? { key: 'disable', label: '停用', icon: 'disable', tone: 'danger' as const, confirmTitle: `确认停用来源授权 ${record.name}？停用后该来源的公开接口请求会被拒绝。`, confirmOkText: '停用' }
    : { key: 'enable', label: '启用', icon: 'enable', tone: 'success' as const, confirmTitle: `确认启用来源授权 ${record.name}？`, confirmOkText: '启用' }
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

</style>
