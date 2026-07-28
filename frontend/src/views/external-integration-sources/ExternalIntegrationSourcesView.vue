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
      :scope-options="availableScopeOptions"
      :scope-options-loading="scopeOptionsLoading"
      @add-rate-limit="addRateLimit"
      @remove-rate-limit="removeRateLimit"
      @save="saveSource"
      @scope-options-dropdown-visible-change="handleScopeOptionsDropdown"
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
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { BookOutlined } from '@ant-design/icons-vue'

import { api, type ExternalIntegrationSourceListParams } from '@/api/client'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { RowActionItem } from '@/components/rowActions'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { sanitizePaginationState, stringOrFallback, stringUnionOrFallback, type PagePaginationState } from '@/shared/pageStateSanitizers'
import type {
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceListItem,
  ExternalIntegrationSourceStatus
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
  DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS,
  type ExternalSourceForm
} from './externalSourceFormModel'
import { useExternalSourceTokenActions } from './useExternalSourceTokenActions'

interface ExternalIntegrationSourcesPageState {
  keyword: string
  pagination: PagePaginationState
  statusFilter: ExternalIntegrationSourceStatus | 'all'
}

const pageSize = 20
const pageStateCache = usePageStateCache<ExternalIntegrationSourcesPageState>(undefined, defaultExternalSourcesPageState, {
  sanitize: sanitizeExternalSourcesPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const loading = ref(false)
const keyword = ref(initialPageState.keyword)
const statusFilter = ref<ExternalIntegrationSourceStatus | 'all'>(initialPageState.statusFilter)
let listRequestId = 0
const rows = ref<ExternalIntegrationSourceListItem[]>([])
const paginationUpperBound = ref(0)
const pagination = reactive({ ...initialPageState.pagination })
const scopeOptions = ref<ExternalIntegrationScopeOption[]>([...DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS])
const scopeOptionsLoading = ref(false)
const scopeOptionsLoaded = ref(false)
let scopeOptionsRequestId = 0
let activeScopeOptionsRequest: Promise<void> | undefined

const apiDocsOpen = ref(false)
const publicApiBaseUrl = computed(() => resolvePublicApiBaseUrl())

const sourceModalOpen = ref(false)
const sourceSaving = ref(false)
const editingSourceId = ref<string>()
const sourceForm = reactive<ExternalSourceForm>(createEmptySourceForm())
const availableScopeOptions = computed(() => {
  const optionsByValue = new Map(scopeOptions.value.map((option) => [option.value, option]))
  for (const value of sourceForm.scopes) {
    if (!optionsByValue.has(value)) optionsByValue.set(value, { value, label: value })
  }
  return [...optionsByValue.values()]
})

const builtInSourceDescription = '已授权全部公开资源维护接口；复制完整 Token 调用 /__aipublic__ 接口时只返回 Mock 数据，可用于对接请求头、参数和响应解析。'
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
  void loadData()
})

function loadScopes(): Promise<void> {
  if (scopeOptionsLoaded.value) return Promise.resolve()
  if (activeScopeOptionsRequest) return activeScopeOptionsRequest
  const requestId = ++scopeOptionsRequestId
  scopeOptionsLoading.value = true
  const request = (async () => {
    try {
      const nextOptions = await api.externalIntegrationSources.scopes()
      if (requestId !== scopeOptionsRequestId) return
      scopeOptions.value = nextOptions.length ? nextOptions : [...DEFAULT_EXTERNAL_INTEGRATION_SCOPE_OPTIONS]
      scopeOptionsLoaded.value = true
    } catch (error) {
      if (requestId !== scopeOptionsRequestId) return
      console.error(error)
      message.error(extractApiErrorMessage(error, '加载接口资源授权选项失败，请重试'))
    } finally {
      if (requestId === scopeOptionsRequestId) {
        scopeOptionsLoading.value = false
        activeScopeOptionsRequest = undefined
      }
    }
  })()
  activeScopeOptionsRequest = request
  return request
}

function handleScopeOptionsDropdown(open: boolean): void {
  if (open) void loadScopes()
}

onBeforeUnmount(() => {
  listRequestId += 1
  scopeOptionsRequestId += 1
  activeScopeOptionsRequest = undefined
})

function openApiDocs(): void {
  apiDocsOpen.value = true
}

async function loadData(): Promise<void> {
  const requestId = ++listRequestId
  const params = buildListParams()
  const requestSignature = JSON.stringify(params)
  loading.value = true
  try {
    const result = await api.externalIntegrationSources.list(params)
    if (!isCurrentListRequest(requestId, requestSignature)) return
    rows.value = result.items
    pagination.current = result.page
    pagination.pageSize = result.pageSize
    paginationUpperBound.value = result.pageUpperBound
  } catch (error) {
    if (!isCurrentListRequest(requestId, requestSignature)) return
    message.error(extractApiErrorMessage(error, '加载公开接口授权失败'))
  } finally {
    if (requestId === listRequestId) loading.value = false
  }
}

function isCurrentListRequest(requestId: number, signature: string): boolean {
  return requestId === listRequestId && signature === JSON.stringify(buildListParams())
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
  const defaults = defaultExternalSourcesPageState()
  keyword.value = defaults.keyword
  statusFilter.value = defaults.statusFilter
  pagination.current = defaults.pagination.current
  pagination.pageSize = defaults.pagination.pageSize
  pageStateCache.clear()
  void loadData()
}

function handleTableChange(nextPagination: unknown): void {
  const paginationInfo = (nextPagination && typeof nextPagination === 'object' ? nextPagination : {}) as { current?: number; pageSize?: number }
  pagination.current = paginationInfo.current ?? 1
  pagination.pageSize = paginationInfo.pageSize ?? pageSize
  void loadData()
}

function defaultExternalSourcesPageState(): ExternalIntegrationSourcesPageState {
  return {
    keyword: '',
    pagination: { current: 1, pageSize },
    statusFilter: 'all'
  }
}

function sanitizeExternalSourcesPageState(value: unknown, fallback: ExternalIntegrationSourcesPageState): ExternalIntegrationSourcesPageState {
  const source = value && typeof value === 'object' ? value as Partial<ExternalIntegrationSourcesPageState> : {}
  return {
    keyword: stringOrFallback(source.keyword, fallback.keyword),
    pagination: sanitizePaginationState(source.pagination, fallback.pagination),
    statusFilter: stringUnionOrFallback(source.statusFilter, ['all', 'active', 'disabled'], fallback.statusFilter)
  }
}

function snapshotPageState(): ExternalIntegrationSourcesPageState {
  return {
    keyword: keyword.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    statusFilter: statusFilter.value
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })

function openCreateSource(): void {
  editingSourceId.value = undefined
  clearCreatedToken()
  Object.assign(sourceForm, createEmptySourceForm())
  sourceModalOpen.value = true
}

function openEditSource(record: ExternalIntegrationSourceListItem): void {
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

function sourceActions(record: ExternalIntegrationSourceListItem): RowActionItem[] {
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

function handleSourceAction(key: string, record: ExternalIntegrationSourceListItem): void {
  if (key === 'edit') {
    void openEditSource(record)
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

async function updateSourceStatus(record: ExternalIntegrationSourceListItem, status: ExternalIntegrationSourceStatus): Promise<void> {
  try {
    await api.externalIntegrationSources.update(record.id, { status })
    message.success(status === 'active' ? '来源授权已启用' : '来源授权已停用')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '更新来源授权状态失败'))
  }
}

async function deleteSource(record: ExternalIntegrationSourceListItem): Promise<void> {
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

function sourceNotes(record: ExternalIntegrationSourceListItem): string {
  if (record.isBuiltIn) {
    return builtInSourceDescription
  }
  return record.notes?.trim() || '无备注'
}

function scopeLabel(scope: string): string {
  return availableScopeOptions.value.find((item) => item.value === scope)?.label ?? scope
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
