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

    <a-modal
      v-model:open="sourceModalOpen"
      :title="editingSourceId ? '编辑来源授权' : '新增授权'"
      width="760px"
      :confirm-loading="sourceSaving"
      ok-text="保存"
      cancel-text="取消"
      :ok-button-props="{ disabled: sourceSaving }"
      @ok="saveSource"
    >
      <a-form layout="vertical">
        <a-form-item label="授权名称" required>
          <a-input v-model:value="sourceForm.name" placeholder="例如 公益站生产授权" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="sourceForm.status" :options="sourceStatusOptions" />
        </a-form-item>
        <a-form-item label="接口资源授权">
          <a-select v-model:value="sourceForm.scopes" mode="multiple" :options="scopeOptions" placeholder="选择允许调用的公开接口" />
        </a-form-item>
        <a-form-item label="到期时间">
          <a-date-picker v-model:value="sourceForm.expiresAt" class="full-control" show-time allow-clear />
        </a-form-item>
        <a-form-item label="限频规则">
          <div class="rate-limit-list">
            <div v-for="(rule, index) in sourceForm.rateLimits" :key="index" class="rate-limit-row">
              <a-input-number v-model:value="rule.windowSeconds" :min="1" :max="86400" :precision="0" addon-after="秒内" />
              <a-input-number v-model:value="rule.maxRequests" :min="1" :max="100000" :precision="0" addon-after="次" />
              <a-button danger @click="removeRateLimit(index)">删除</a-button>
            </div>
            <a-button @click="addRateLimit">新增限频规则</a-button>
            <span v-if="!sourceForm.rateLimits.length" class="muted-cell">默认不限制。</span>
          </div>
        </a-form-item>
        <a-form-item label="备注">
          <a-textarea v-model:value="sourceForm.notes" :rows="3" :maxlength="500" show-count />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal
      v-model:open="createdTokenOpen"
      title="来源授权已创建"
      width="600px"
      :footer="null"
      :mask-closable="false"
      @cancel="closeCreatedTokenModal"
    >
      <div class="created-token-guide">
        <a-alert
          class="created-token-alert"
          type="success"
          show-icon
          message="生产 Token 已生成"
          description="请复制后保存到外部系统后端；后续可在列表按权限复制完整 Token，不要放进前端包或公开文档。"
        />
        <div class="created-token-guide-section">
          <span class="created-token-step-title">1. 复制 Base URL</span>
          <div class="created-token-copy-row">
            <span class="created-token-label">Base URL</span>
            <code class="created-token-value">{{ publicApiBaseUrl }}</code>
            <a-button type="text" size="small" @click="copyPublicApiBaseUrl">
              <template #icon><copy-outlined /></template>
              复制
            </a-button>
          </div>
        </div>
        <div class="created-token-guide-section">
          <span class="created-token-step-title">2. 保存生产 Token</span>
          <a-input-group compact class="created-token-input">
            <a-input :value="createdTokenPlain" readonly />
            <a-button type="primary" @click="copyCreatedToken">复制</a-button>
          </a-input-group>
        </div>
        <div class="created-token-guide-section">
          <span class="created-token-step-title">3. 配置请求头</span>
          <pre class="created-token-code">{{ createdTokenAuthHeader }}</pre>
          <a-button size="small" @click="copyCreatedTokenAuthHeader">
            <template #icon><copy-outlined /></template>
            复制认证头
          </a-button>
        </div>
        <div class="created-token-actions">
          <a-button @click="openApiDocs">查看接入文档</a-button>
          <a-button type="primary" @click="closeCreatedTokenModal">我已保存</a-button>
        </div>
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import type { Dayjs } from 'dayjs'
import { BookOutlined, CopyOutlined } from '@ant-design/icons-vue'

import { api, type ExternalIntegrationSourceListParams } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatDateTime, formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type {
  ExternalIntegrationRateLimitRule,
  ExternalIntegrationScopeOption,
  ExternalIntegrationSourceStatus,
  ExternalIntegrationSourceSummary,
  ExternalIntegrationSourceTokenSummary
} from '@/types/domain'
import ExternalSourceApiDocsModal from './ExternalSourceApiDocsModal.vue'
import { resolvePublicApiBaseUrl } from './externalSourceApiDocs'

const pageSize = 20
const sourceAuthDemoScope = 'external_integrations:source_auth_demo:read'
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
const createdTokenOpen = ref(false)
const sourceSaving = ref(false)
const generatingTokenSourceId = ref('')
const editingSourceId = ref<string>()
const sourceForm = reactive<{
  name: string
  status: ExternalIntegrationSourceStatus
  scopes: string[]
  rateLimits: ExternalIntegrationRateLimitRule[]
  expiresAt: Dayjs | null
  notes: string
}>({
  name: '',
  status: 'active',
  scopes: [],
  rateLimits: [],
  expiresAt: null,
  notes: ''
})

const createdTokenPlain = ref('')
const tokenCopyingKey = ref('')
const createdTokenAuthHeader = computed(() => `Authorization: Bearer ${createdTokenPlain.value || '<source_token>'}`)
const builtInSourceShortDescription = '系统内置联调用来源'
const builtInSourceDescription = '系统内置联调用来源，已授权全部公开接口；复制完整 Token 调用 /__aipublic__ 接口时只返回 Mock 数据，可用于对接请求头、参数和响应解析。'

const statusOptions = [
  { label: '全部状态', value: 'all' },
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const sourceStatusOptions = [
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
  createdTokenPlain.value = ''
  createdTokenOpen.value = false
  Object.assign(sourceForm, {
    name: '',
    status: 'active',
    scopes: defaultCreateSourceScopes(),
    rateLimits: [],
    expiresAt: null,
    notes: ''
  })
  sourceModalOpen.value = true
}

function defaultCreateSourceScopes(): string[] {
  return scopeOptions.value.some((item) => item.value === sourceAuthDemoScope)
    ? [sourceAuthDemoScope]
    : []
}

function openEditSource(record: ExternalIntegrationSourceSummary): void {
  let rateLimits: ExternalIntegrationRateLimitRule[]
  let expiresAt: Dayjs | null
  try {
    rateLimits = normalizeRateLimits(record.rateLimits)
    expiresAt = parseStrictDatePickerValue(record.expiresAt, '来源授权过期时间') ?? null
  } catch (error) {
    message.error(extractApiErrorMessage(error, '来源授权数据异常，请清理后再编辑'))
    return
  }
  editingSourceId.value = record.id
  createdTokenPlain.value = ''
  createdTokenOpen.value = false
  Object.assign(sourceForm, {
    name: record.name,
    status: record.status,
    scopes: [...record.scopes],
    rateLimits,
    expiresAt,
    notes: record.notes ?? ''
  })
  sourceModalOpen.value = true
}

async function saveSource(): Promise<void> {
  if (!sourceForm.name.trim()) {
    message.error('请填写来源名称')
    return
  }
  sourceSaving.value = true
  try {
    const payload = {
      name: sourceForm.name.trim(),
      status: sourceForm.status,
      scopes: [...sourceForm.scopes],
      rateLimits: normalizeRateLimits(sourceForm.rateLimits),
      expiresAt: formatServerDateTimeInput(sourceForm.expiresAt),
      notes: sourceForm.notes.trim() || null
    }
    if (editingSourceId.value) {
      await api.externalIntegrationSources.update(editingSourceId.value, payload)
      message.success('来源授权已更新')
      sourceModalOpen.value = false
    } else {
      const result = await api.externalIntegrationSources.create(payload)
      createdTokenPlain.value = result.token.token
      sourceModalOpen.value = false
      createdTokenOpen.value = true
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
  sourceForm.rateLimits.push({ windowSeconds: 60, maxRequests: 10 })
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

async function resetBuiltInTestToken(): Promise<void> {
  try {
    const result = await api.externalIntegrationSources.resetBuiltInTestToken()
    await copyTextToClipboard(result.token.token, '内置测试 Token 已重置并复制')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '重置内置测试 Token 失败'))
  }
}

async function generateSourceToken(record: ExternalIntegrationSourceSummary): Promise<void> {
  if (record.isBuiltIn || primaryToken(record) || generatingTokenSourceId.value) return
  generatingTokenSourceId.value = record.id
  try {
    const result = await api.externalIntegrationSources.createToken(record.id, {
      name: `${record.name} 生产 Token`,
      status: 'active',
      scopes: [...record.scopes],
      expiresAt: record.expiresAt ?? null
    })
    createdTokenPlain.value = result.token.token
    createdTokenOpen.value = true
    message.success('生产 Token 已生成')
    await loadData()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '生成生产 Token 失败'))
  } finally {
    if (generatingTokenSourceId.value === record.id) {
      generatingTokenSourceId.value = ''
    }
  }
}

function copyCreatedToken(): void {
  void copyTextToClipboard(createdTokenPlain.value, 'Token 已复制')
}

function copyCreatedTokenAuthHeader(): void {
  void copyTextToClipboard(createdTokenAuthHeader.value, '认证头已复制')
}

function copyPublicApiBaseUrl(): void {
  void copyTextToClipboard(publicApiBaseUrl.value, 'Base URL 已复制')
}

function closeCreatedTokenModal(): void {
  createdTokenOpen.value = false
  createdTokenPlain.value = ''
}

async function copyTokenPreview(record: ExternalIntegrationSourceSummary): Promise<void> {
  const token = primaryToken(record)
  if (!token || tokenCopyingKey.value) return
  const copyingKey = tokenCopyKey(record)
  tokenCopyingKey.value = copyingKey
  try {
    const result = await api.externalIntegrationSources.tokenSecret(record.id, token.id)
    await copyTextToClipboard(result.token, '完整 Token 已复制')
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '复制完整 Token 失败'))
  } finally {
    if (tokenCopyingKey.value === copyingKey) {
      tokenCopyingKey.value = ''
    }
  }
}

function normalizeRateLimits(rules: ExternalIntegrationRateLimitRule[]): ExternalIntegrationRateLimitRule[] {
  return rules.map((rule, index) => ({
    windowSeconds: normalizeRateLimitInteger(rule.windowSeconds, 1, 86400, `第 ${index + 1} 条限频窗口`),
    maxRequests: normalizeRateLimitInteger(rule.maxRequests, 1, 100000, `第 ${index + 1} 条限频次数`)
  }))
}

function normalizeRateLimitInteger(value: unknown, min: number, max: number, label: string): number {
  if (typeof value !== 'number' || !Number.isInteger(value)) {
    throw new Error(`${label}必须是整数`)
  }
  if (value < min || value > max) {
    throw new Error(`${label}必须在 ${min} 到 ${max} 之间`)
  }
  return value
}

function formatRateLimits(rules: ExternalIntegrationRateLimitRule[]): string {
  return rules.length ? rules.map((rule) => `${rule.windowSeconds}s/${rule.maxRequests}次`).join('，') : '不限制'
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

function formatTokenPreview(token: ExternalIntegrationSourceTokenSummary | undefined): string {
  if (!token) return '未生成'
  return maskSecretPreview('', token.tokenPrefix, token.tokenSuffix)
}

function tokenDisplayTitle(token: ExternalIntegrationSourceTokenSummary | undefined): string {
  return token ? '列表仅显示 Token 标识，点击复制按钮复制完整 Token' : '未生成'
}

function maskSecretPreview(value: string | undefined, prefix?: string, suffix?: string): string {
  if (value) {
    return value.length > 16 ? `${value.slice(0, 8)}...${value.slice(-8)}` : value
  }
  const head = prefix?.slice(0, 8) ?? ''
  const tail = suffix?.slice(-8) ?? ''
  if (head && tail) return `${head}...${tail}`
  if (head) return `${head}...`
  return '未生成'
}

function primaryToken(record: ExternalIntegrationSourceSummary): ExternalIntegrationSourceTokenSummary | undefined {
  return record.tokens[0]
}

function tokenCopyKey(record: ExternalIntegrationSourceSummary): string {
  const token = primaryToken(record)
  return token ? `${record.id}:${token.id}` : ''
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

.full-control {
  width: 100%;
}

.rate-limit-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rate-limit-row {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) minmax(120px, 1fr) auto;
  gap: 8px;
}

.modal-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.created-token-alert {
  margin-bottom: 0;
}

.created-token-guide {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.created-token-guide-section {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 10px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 12px;
  background: #fbfdff;
}

.created-token-step-title {
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
}

.created-token-copy-row {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
}

.created-token-label {
  flex: none;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.created-token-value {
  min-width: 0;
  flex: 1;
  padding: 4px 10px;
  overflow: hidden;
  color: #0f766e;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
  border-radius: 6px;
  background: #ecfeff;
}

.created-token-input {
  display: flex;
  margin-top: 2px;
}

.created-token-input :deep(.ant-input) {
  flex: 1;
  font-family: ui-monospace, SFMono-Regular, Consolas, 'Liberation Mono', Menlo, monospace;
}

.created-token-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.created-token-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
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

@media (max-width: 720px) {
  .rate-limit-row {
    grid-template-columns: 1fr;
  }

  .modal-toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .created-token-copy-row,
  .created-token-actions {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
