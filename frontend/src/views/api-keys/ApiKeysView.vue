<template>
  <a-card class="page-card api-keys-page-card responsive-page-card">
    <ResponsiveListToolbar v-model:keyword="keywordFilter" search-placeholder="搜索 API Key..." filter-title="筛选 API Key" :active-filter-count="activeFilterCount" :refresh-loading="loading" @reset="resetFilters" @refresh="refreshApiKeys" @search="applyFilters">
      <template #inline-filters>
        <a-select v-model:value="statusFilter" class="toolbar-select responsive-list-inline-filter" :options="listStatusOptions" @change="applyFilters" />
        <a-select v-model:value="groupFilter" allow-clear class="toolbar-select responsive-list-inline-filter" :options="groupFilterOptions" placeholder="全部分组" @change="applyFilters" />
        <SystemPrincipalSelect v-if="isManagementView" v-model:value="systemAccountFilter" :accounts="systemAccounts" :active-only="false" include-all class="toolbar-select responsive-list-inline-filter" @change="handleSystemAccountFilterChange" />
      </template>
      <template #actions>
        <a-button @click="helpOpen = true">
          <template #icon><question-circle-outlined /></template>
          接入帮助
        </a-button>
        <a-button type="primary" @click="openCreate">新建 API Key</a-button>
      </template>
      <template #filters>
        <label class="mobile-filter-field">
          <span>状态</span>
          <a-select v-model:value="statusFilter" :options="listStatusOptions" />
        </label>
        <label class="mobile-filter-field">
          <span>分组</span>
          <a-select v-model:value="groupFilter" allow-clear :options="groupFilterOptions" placeholder="全部分组" />
        </label>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect v-model:value="systemAccountFilter" :accounts="systemAccounts" :active-only="false" include-all @change="handleSystemAccountFilterChange" />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table api-keys-table" :columns="columns" :data-source="filteredApiKeys" :mobile-data-source="mobileApiKeys" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="isManagementView ? 1800 : 1620" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileApiKeys" @mobile-refresh="refreshMobileApiKeys">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有 API Key。先新建一个并绑定分组；接入说明可点击右上角帮助查看。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <StatusTag :color="record.status === 'active' ? 'green' : 'default'" :label="record.status === 'active' ? '启用' : '停用'" />
        </template>
        <template v-else-if="column.key === 'usage'">
          <div class="api-key-usage-cell">
            <a-tag class="api-key-usage-tag">{{ `${record.usage.requestCount}req` }}</a-tag>
            <a-tag class="api-key-usage-tag">{{ formatUsageAmount(record.usage.totalTokens) }}</a-tag>
            <a-tag class="api-key-usage-tag">{{ formatCost(record.usage.totalCost) }}</a-tag>
          </div>
        </template>
        <template v-else-if="column.key === 'key'">
          <div class="key-preview-cell">
            <span class="key-preview" :title="record.key || '密钥明文仅创建时展示，请重新创建密钥'">{{ formatKeyPreview(record.key) }}</span>
            <a-button class="key-copy-button" type="text" size="small" :disabled="!record.key" @click="copyText(record.key)">
              <template #icon><copy-outlined /></template>
            </a-button>
          </div>
        </template>
        <template v-else-if="column.key === 'group'">
          <a-tag color="purple">{{ groupName(record.groupId) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ apiKeySystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'quotaLimits'">
          <span>{{ quotaLimitSummaryText(record.quotaLimits) }}</span>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <a-space class="row-actions" :size="8">
            <a-button type="link" size="small" @click="openEdit(record)">编辑</a-button>
            <a-popconfirm title="确认删除这个 API Key？相关使用记录、审计日志和统计缓存会一起删除。" @confirm="removeApiKey(record.id)">
              <a-button type="link" size="small" danger>删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <StatusTag :color="record.status === 'active' ? 'green' : 'default'" :label="record.status === 'active' ? '启用' : '停用'" />
              <a-tag color="purple">{{ groupName(record.groupId) }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>API Key</span>
              <strong>{{ formatKeyPreview(record.key) }}</strong>
            </div>
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>系统账户</span>
              <strong>{{ apiKeySystemAccountText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>过期时间</span>
              <strong>{{ formatDateTime(record.expiresAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>累计用量</span>
              <strong>{{ formatUsageSummary(record.usage) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>美元额度</span>
              <strong>{{ quotaLimitSummaryText(record.quotaLimits) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions two-actions">
            <a-button type="primary" @click="openEdit(record)">编辑</a-button>
            <a-popconfirm title="确认删除这个 API Key？相关使用记录、审计日志和统计缓存会一起删除。" @confirm="removeApiKey(record.id)">
              <a-button danger>删除</a-button>
            </a-popconfirm>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="helpOpen" title="API Key 接入帮助" width="560px" :footer="null">
      <div class="gateway-help-content">
        <div class="gateway-help-section">
          <span class="gateway-step-title">1. 复制 Base URL</span>
          <div class="gateway-url-row">
            <span class="gateway-url-label">Base URL</span>
            <span class="gateway-url-value">{{ gatewayBaseUrl }}</span>
            <a-button class="gateway-copy-button" type="text" size="small" @click="copyGatewayBaseUrl">
              <template #icon><copy-outlined /></template>
              复制
            </a-button>
          </div>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-step-title">2. 复制本页 API Key</span>
        </div>
        <div class="gateway-help-section">
          <span class="gateway-step-title">3. 填到客户端</span>
          <pre class="gateway-code">{{ gatewayClientExample }}</pre>
        </div>
      </div>
    </a-modal>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑 API Key' : '新建 API Key'" width="640px" :ok-button-props="{ type: 'primary' }" @ok="saveApiKey">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-alert class="modal-alert" message="系统会自动生成完整密钥，创建后直接复制保存即可。" type="info" show-icon />
      <a-form layout="vertical" class="modal-form">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" />
        </a-form-item>
        <a-form-item label="绑定分组" required>
          <a-select v-model:value="form.groupId" :options="groupOptions" placeholder="选择分组" />
        </a-form-item>
        <a-form-item label="状态">
          <a-select v-model:value="form.status" :options="statusOptions" />
        </a-form-item>
        <a-form-item label="过期时间">
          <a-date-picker v-model:value="form.expiresAt" show-time allow-clear style="width: 100%" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="3" placeholder="可选，填写用途或接入方说明" />
        </a-form-item>
        <RequestQuotaFields :model="form.quotaLimits" />
      </a-form>
    </a-modal>

    <a-modal v-model:open="createdKeyOpen" title="API Key 已创建" width="640px" :footer="null">
      <a-alert message="复制下方 API Key 和 Base URL，填到客户端即可。" type="info" show-icon />
      <div class="created-key-base-url">
        <span class="created-key-label">Base URL</span>
        <span class="created-key-value">{{ gatewayBaseUrl }}</span>
        <a-button type="link" size="small" @click="copyGatewayBaseUrl">复制</a-button>
      </div>
      <a-input-group compact class="created-key">
        <a-input :value="createdKey" readonly style="width: calc(100% - 88px)" />
        <a-button type="primary" @click="copyCreatedKey">复制</a-button>
      </a-input-group>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
import { CopyOutlined, QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import StatusTag from '@/components/StatusTag.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { formatCompactUsageAmount, formatDateTime, formatNumber, formatServerDateTimeInput, formatUsd } from '@/shared/formatters'
import type { AccountUsageSummary, ApiKeyQuotaLimits, ApiKeySummary, GroupSummary, SystemAccountSummary } from '@/types/domain'
import { allSystemAccountsValue, systemAccountDisplayText } from '@/utils/systemAccountFilter'
import RequestQuotaFields from '@/views/shared/RequestQuotaFields.vue'
import { quotaLimitSummaryText } from '@/views/shared/requestQuotaFormatters'
import { createQuotaLimitForm, quotaLimitsPayload as buildQuotaLimitsPayload } from '@/views/shared/requestQuotaForm'

const loading = ref(false)
const modalOpen = ref(false)
const createdKeyOpen = ref(false)
const helpOpen = ref(false)
const editingId = ref<string>()
const createdKey = ref('')
const keywordFilter = ref('')
const statusFilter = ref<'all' | 'active' | 'disabled'>('all')
const groupFilter = ref<string | undefined>()
const mobileLoadingMore = ref(false)
const apiKeys = ref<ApiKeySummary[]>([])
const groups = ref<GroupSummary[]>([])
const systemAccounts = ref<SystemAccountSummary[]>([])
const systemAccountFilter = ref(allSystemAccountsValue)
const pageSize = 50
const pagination = reactive({ current: 1, pageSize, total: 0 })
const form = reactive({
  name: '',
  groupId: '',
  status: 'active' as 'active' | 'disabled',
  expiresAt: undefined as Dayjs | undefined,
  description: '',
  quotaLimits: createQuotaLimitForm()
})
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()

const columns = computed(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
    { title: '密钥', key: 'key', width: 180 }
  ]
  if (isManagementView.value) {
    baseColumns.push({ title: '系统账户', key: 'systemAccount', width: 180 })
  }
  baseColumns.push(
    { title: '绑定分组', key: 'group', width: 220 },
    { title: '状态', key: 'status', width: 100 },
    { title: '累计用量', key: 'usage', width: 190 },
    { title: '美元额度', key: 'quotaLimits', width: 220 },
    { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 180 },
    { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
    { title: '操作', key: 'actions', width: 110, fixed: 'right' }
  )
  return baseColumns
})

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]
const listStatusOptions = [
  { label: '全部状态', value: 'all' },
  ...statusOptions
]

const groupOptions = computed(() => groups.value.map((group) => ({ label: groupOptionLabel(group), value: group.id })))
const groupFilterOptions = computed(() => groups.value.map((group) => ({ label: groupOptionLabel(group), value: group.id })))
const filteredApiKeys = computed(() => apiKeys.value)
const mobileApiKeys = computed(() => apiKeys.value)
const mobileHasMore = computed(() => apiKeys.value.length < pagination.total)
const tablePagination = computed(() => ({
  current: pagination.current,
  pageSize: pagination.pageSize,
  total: pagination.total,
  hideOnSinglePage: true,
  showSizeChanger: false,
  showTotal: (total: number) => `共 ${total} 个 API Key`
}))
const apiKeyScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const activeFilterCount = computed(() => [
  keywordFilter.value.trim(),
  statusFilter.value !== 'all',
  groupFilter.value,
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue
].filter(Boolean).length)
const gatewayBaseUrl = computed(() => normalizeGatewayBaseUrl((import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined) || inferGatewayBaseUrl()))
const gatewayClientExample = computed(() => [`Base URL：${gatewayBaseUrl.value}`, 'API Key：填本页复制的密钥'].join('\n'))
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = apiKeyScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName || systemAccounts.value.find((account) => account.id === systemAccountId)?.username || systemAccountId
})

function groupName(groupId: string) {
  const group = groups.value.find((item) => item.id === groupId)
  return group ? groupOptionLabel(group) : groupId
}

function groupOptionLabel(group: GroupSummary) {
  if (group.accessType !== 'authorized') return group.name
  return `${group.name}（来自 ${group.ownerSystemAccountName || '其他用户'} 授权）`
}

function formatKeyPreview(value?: string) {
  if (!value) return '未回填'
  if (value.length <= 14) return value
  return `${value.slice(0, 6)}...${value.slice(-4)}`
}

async function loadData() {
  await fetchData()
}

async function fetchData(options: { append?: boolean; quiet?: boolean } = {}) {
  if (!options.quiet) {
    loading.value = true
  }
  try {
    const systemAccountId = isManagementView.value ? apiKeyScopeParams.value?.systemAccountId : undefined
    const [keyList, groupList, systemAccountList] = await Promise.all([
      isManagementView.value ? api.apiKeys.list(apiKeyListParams(systemAccountId)) : api.myApiKeys.list(apiKeyListParams()),
      isManagementView.value ? api.groups.list({ systemAccountId }) : api.myGroups.list(),
      isManagementView.value ? api.systemAccounts.list() : Promise.resolve([] as SystemAccountSummary[])
    ])
    pagination.current = keyList.page
    pagination.pageSize = keyList.pageSize
    pagination.total = keyList.total
    apiKeys.value = options.append ? [...apiKeys.value, ...keyList.items] : keyList.items
    groups.value = groupList
    systemAccounts.value = systemAccountList
  } catch (error) {
    console.error(error)
    message.error('加载 API Key 失败')
  } finally {
    if (!options.quiet) {
      loading.value = false
    }
  }
}

function resetFilters() {
  keywordFilter.value = ''
  statusFilter.value = 'all'
  groupFilter.value = undefined
  systemAccountFilter.value = allSystemAccountsValue
  resetPagination()
  void fetchData()
}

function applyFilters() {
  resetPagination()
  void fetchData()
}

function refreshApiKeys() {
  resetPagination()
  void fetchData()
}

function handleSystemAccountFilterChange() {
  groupFilter.value = undefined
  resetPagination()
  void fetchData()
}

function handleTableChange(paginationInfo: unknown) {
  if (!paginationInfo || typeof paginationInfo !== 'object') return
  const next = paginationInfo as { current?: unknown; pageSize?: unknown }
  const nextCurrent = Number(next.current)
  const nextPageSize = Number(next.pageSize)
  pagination.current = Number.isFinite(nextCurrent) && nextCurrent > 0 ? nextCurrent : 1
  pagination.pageSize = Number.isFinite(nextPageSize) && nextPageSize > 0 ? nextPageSize : pageSize
  void fetchData()
}

async function loadMoreMobileApiKeys() {
  if (!mobileHasMore.value || mobileLoadingMore.value) return
  mobileLoadingMore.value = true
  pagination.current += 1
  try {
    await fetchData({ append: true, quiet: true })
  } finally {
    mobileLoadingMore.value = false
  }
}

async function refreshMobileApiKeys() {
  resetPagination()
  await fetchData()
}

function resetPagination() {
  pagination.current = 1
}

function apiKeyListParams(systemAccountId?: string) {
  return {
    systemAccountId,
    page: pagination.current,
    pageSize: pagination.pageSize,
    keyword: keywordFilter.value.trim() || undefined,
    status: statusFilter.value,
    groupId: groupFilter.value
  }
}

function apiKeySystemAccountText(apiKey: ApiKeySummary) {
  return systemAccountDisplayText(apiKey)
}

function formatUsageSummary(usage: AccountUsageSummary): string {
  return `${formatNumber(usage.requestCount)}req / ${formatUsageAmount(usage.totalTokens)} / ${formatCost(usage.totalCost)}`
}

function formatUsageAmount(value?: number): string {
  return formatCompactUsageAmount(value)
}

function formatCost(value?: number): string {
  return formatUsd(value)
}

function openCreate() {
  if (isManagementView.value && !apiKeyScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  editingId.value = undefined
  Object.assign(form, { name: '', groupId: groups.value[0]?.id ?? '', status: 'active', expiresAt: undefined, description: '', quotaLimits: createQuotaLimitForm() })
  modalOpen.value = true
}

function openEdit(apiKey: ApiKeySummary) {
  editingId.value = apiKey.id
  Object.assign(form, { name: apiKey.name, groupId: apiKey.groupId, status: apiKey.status, expiresAt: undefined, description: apiKey.description ?? '', quotaLimits: createQuotaLimitForm(apiKey.quotaLimits) })
  modalOpen.value = true
}

async function saveApiKey() {
  if (!form.name.trim()) {
    message.warning('请填写名称')
    return
  }
  if (!form.groupId) {
    message.warning('请选择绑定分组')
    return
  }
  const payload = {
    name: form.name,
    groupId: form.groupId,
    status: form.status,
    expiresAt: formatServerDateTimeInput(form.expiresAt) ?? undefined,
    description: form.description,
    quotaLimits: quotaLimitsPayload()
  }
  try {
    if (editingId.value) {
      if (isManagementView.value) {
        await api.apiKeys.update(editingId.value, payload, apiKeyScopeParams.value)
      } else {
        await api.myApiKeys.update(editingId.value, payload)
      }
      message.success('API Key 已更新')
    } else {
      const result = isManagementView.value
        ? await api.apiKeys.create(payload, apiKeyScopeParams.value)
        : await api.myApiKeys.create(payload)
      createdKey.value = result.key
      createdKeyOpen.value = true
      message.success('API Key 已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('保存 API Key 失败')
  }
}

function quotaLimitsPayload(): ApiKeyQuotaLimits {
  return buildQuotaLimitsPayload(form.quotaLimits)
}

async function copyText(value: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  message.success('已复制')
}

async function copyGatewayBaseUrl() {
  await copyText(gatewayBaseUrl.value)
}

async function copyCreatedKey() {
  await copyText(createdKey.value)
}

function inferGatewayBaseUrl() {
  if (typeof window === 'undefined') return 'http://127.0.0.1:3000/v1'
  if (import.meta.env.DEV) {
    return `${window.location.protocol}//${window.location.hostname}:3000/v1`
  }
  return `${window.location.origin}/v1`
}

function normalizeGatewayBaseUrl(value: string) {
  const trimmed = value.trim().replace(/\/+$/, '')
  return trimmed.endsWith('/v1') ? trimmed : `${trimmed}/v1`
}

async function removeApiKey(id: string) {
  try {
    if (isManagementView.value) {
      await api.apiKeys.delete(id, apiKeyScopeParams.value)
    } else {
      await api.myApiKeys.delete(id)
    }
    message.success('API Key 已删除')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error('删除 API Key 失败')
  }
}

onMounted(loadData)
</script>

<style scoped>
.api-keys-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
}

.toolbar-select {
  min-width: 180px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

.gateway-help-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.gateway-help-section {
  padding: 14px;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  background: #fbfdff;
}

.gateway-step-title {
  color: #0f172a;
  font-size: 15px;
  font-weight: 700;
}

.gateway-url-row,
.created-key-base-url {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.gateway-url-label,
.created-key-label {
  flex: none;
  color: #64748b;
  font-size: 12px;
  font-weight: 600;
}

.gateway-url-value,
.created-key-value {
  min-width: 0;
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

.gateway-copy-button {
  flex: none;
}

.gateway-code {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  color: #334155;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  line-height: 1.6;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.8);
}

.modal-alert {
  margin-bottom: 16px;
}

.modal-form {
  margin-top: 16px;
}

.created-key {
  margin-top: 16px;
}

.created-key-base-url {
  margin-top: 16px;
}

.api-keys-table :deep(.ant-empty) {
  margin: 12px 0;
}

.api-keys-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.key-preview-cell {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

.key-preview {
  display: inline-flex;
  align-items: center;
  max-width: 120px;
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

.key-copy-button {
  color: #94a3b8;
}

.key-copy-button:hover:not(:disabled) {
  color: #1677ff;
  background: #eff6ff;
}

.api-key-usage-cell {
  display: inline-grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 4px;
  width: 160px;
}

.api-key-usage-tag {
  min-width: 0;
  margin-inline-end: 0;
  padding-inline: 5px;
  color: #0f172a;
  font-family: Consolas, 'Courier New', monospace;
  font-size: 12px;
  text-align: center;
  white-space: nowrap;
}

</style>
