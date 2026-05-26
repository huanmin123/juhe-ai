<template>
  <a-card class="page-card api-keys-page-card responsive-page-card">
    <ResponsiveListToolbar v-model:keyword="keywordFilter" search-placeholder="API Key 名称" filter-title="筛选 API Key" :active-filter-count="activeFilterCount" :advanced-filter-count="advancedFilterCount" :refresh-loading="loading" @reset="resetFilters" @refresh="refreshApiKeys" @search="applyFilters">
      <template #inline-filters>
        <a-select v-model:value="statusFilter" class="toolbar-select responsive-list-inline-filter" :options="listStatusOptions" @change="applyFilters" />
        <SystemPrincipalSelect
          v-if="isManagementView"
          v-model:value="systemAccountFilter"
          v-model:selected-principal="systemAccountFilterSelection"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          include-all
          class="toolbar-select responsive-list-inline-filter"
          @change="handleSystemAccountFilterChange"
          @dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @search="handleSystemAccountOptionsSearch"
        />
        <GroupSelect
          v-model:value="groupFilter"
          v-model:selected-group="groupFilterSelection"
          allow-clear
          class="toolbar-select responsive-list-inline-filter"
          :disabled="groupFilterDisabled"
          :filter-option="false"
          :groups="groups"
          :loading="groupOptionsLoading"
          :placeholder="groupFilterDisabled ? '请先选择系统账户' : '全部分组'"
          @change="handleGroupFilterChange"
          @dropdown-visible-change="handleGroupOptionsDropdown"
          @search="handleGroupOptionsSearch"
        />
      </template>
      <template #actions>
        <TableColumnManager
          :columns="rawColumns"
          :settings="columnSettings"
          :required-keys="['name']"
          @reset="resetColumnSettings"
          @update:settings="updateColumnSettings"
        />
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
          <GroupSelect
            v-model:value="groupFilter"
            v-model:selected-group="groupFilterSelection"
            allow-clear
            :disabled="groupFilterDisabled"
            :filter-option="false"
            :groups="groups"
            :loading="groupOptionsLoading"
            :placeholder="groupFilterDisabled ? '请先选择系统账户' : '全部分组'"
            @change="handleGroupFilterChange"
            @dropdown-visible-change="handleGroupOptionsDropdown"
            @search="handleGroupOptionsSearch"
          />
        </label>
        <label v-if="isManagementView" class="mobile-filter-field">
          <span>系统账户</span>
          <SystemPrincipalSelect
            v-model:value="systemAccountFilter"
            :accounts="systemAccounts"
            :active-only="false"
            :filter-option="false"
            :loading="systemAccountOptionsLoading"
            v-model:selected-principal="systemAccountFilterSelection"
            include-all
            @change="handleSystemAccountFilterChange"
            @dropdown-visible-change="handleSystemAccountOptionsDropdown"
            @search="handleSystemAccountOptionsSearch"
          />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList table-class="page-table api-keys-table" :columns="managedColumns" :data-source="filteredApiKeys" :mobile-data-source="mobileApiKeys" row-key="id" :loading="loading" :loading-more="mobileLoadingMore" :mobile-has-more="mobileHasMore" :pagination="tablePagination" :scroll-x="isManagementView ? 1800 : 1620" mobile-pagination pull-refresh-enabled :refreshing="loading" @change="handleTableChange" @mobile-load-more="loadMoreMobileApiKeys" @mobile-refresh="refreshMobileApiKeys">
      <template #emptyText>
        <a-empty class="page-empty-card" description="还没有 API Key。先新建一个并绑定分组；接入说明可点击右上角帮助查看。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <StatusTag :color="record.status === 'active' ? 'green' : 'default'" :label="record.status === 'active' ? '启用' : '停用'" />
        </template>
        <template v-else-if="column.key === 'usage'">
          <UsageSummaryTags :usage="record.usage" />
        </template>
        <template v-else-if="column.key === 'key'">
          <div class="key-preview-cell">
            <span class="key-preview" :title="keyDisplayTitle(record)">{{ formatKeyPreview(record) }}</span>
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
          <RowActions :actions="apiKeyActions(record)" @action-click="handleApiKeyAction($event, record)" />
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
              <strong>{{ formatKeyPreview(record) }}</strong>
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
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="apiKeyActions(record)" @action-click="handleApiKeyAction($event, record)" />
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
        <a-alert class="gateway-help-note" type="info" show-icon message="Responses 是连续会话入口；/chat/completions 需要兼容上游。统计、会话亲和和缓存不按 OAuth / API Key 类型拆分。" />
      </div>
    </a-modal>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑 API Key' : '新建 API Key'" width="640px" :confirm-loading="apiKeySaving" :ok-button-props="{ type: 'primary', disabled: apiKeySaving }" @ok="saveApiKey">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-alert class="modal-alert" message="系统会自动生成完整密钥，创建后可在列表继续复制。" type="info" show-icon />
      <a-form layout="vertical" class="modal-form">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" />
        </a-form-item>
        <a-form-item label="绑定分组" required>
          <GroupSelect
            v-model:value="form.groupId"
            v-model:selected-group="form.group"
            :disabled="groupFilterDisabled"
            :filter-option="false"
            :groups="groups"
            :loading="groupOptionsLoading"
            :placeholder="groupFilterDisabled ? '请先选择系统账户' : '输入分组名称搜索'"
            @dropdown-visible-change="handleGroupOptionsDropdown"
            @search="handleGroupOptionsSearch"
          />
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
      <a-alert message="复制下方 API Key 和 Base URL；统计、会话亲和和缓存按本地 API Key 与分组保持连续。" type="info" show-icon />
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
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import StatusTag from '@/components/StatusTag.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import UsageSummaryTags from '@/components/UsageSummaryTags.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedApiKeysApi, useScopedGroupsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatCompactUsageAmount, formatDateTime, formatNumber, formatServerDateTimeInput, formatUsd } from '@/shared/formatters'
import { displayGroupName, rememberGroupLabel, rememberGroupLabels, rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { AccountUsageSummary, ApiKeyQuotaLimits, ApiKeySummary, GroupOptionSummary } from '@/types/domain'
import { allSystemAccountsValue, systemAccountDisplayText } from '@/utils/systemAccountFilter'
import RequestQuotaFields from '@/views/shared/RequestQuotaFields.vue'
import { quotaLimitSummaryText } from '@/views/shared/requestQuotaFormatters'
import { createQuotaLimitForm, quotaLimitsPayload as buildQuotaLimitsPayload } from '@/views/shared/requestQuotaForm'

const modalOpen = ref(false)
const createdKeyOpen = ref(false)
const helpOpen = ref(false)
const editingId = ref<string>()
const createdKey = ref('')
const statusUpdatingId = ref('')
const { submitAction, submittingRef } = useSubmitAction('api-keys')
const apiKeySaving = submittingRef('api_keys.save')
const pageSize = 50
type ApiKeysPageState = {
  groupFilter?: GroupSelection
  keywordFilter: string
  pagination: { current: number; pageSize: number }
  statusFilter: 'all' | 'active' | 'disabled'
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}
const defaultApiKeysPageState = (): ApiKeysPageState => ({
  groupFilter: undefined,
  keywordFilter: '',
  pagination: { current: 1, pageSize },
  statusFilter: 'all',
  systemAccountFilter: allSystemAccountsValue,
  systemAccountFilterSelection: undefined
})
const pageStateCache = usePageStateCache<ApiKeysPageState>(undefined, defaultApiKeysPageState)
const initialPageState = pageStateCache.read()
const keywordFilter = ref(initialPageState.keywordFilter)
const statusFilter = ref<'all' | 'active' | 'disabled'>(initialPageState.statusFilter)
const groupFilterSelection = ref<GroupSelection | undefined>(initialPageState.groupFilter)
const groupFilter = computed({
  get: () => groupFilterSelection.value?.id,
  set: (id: string | undefined) => {
    groupFilterSelection.value = selectedGroupSelection(id)
  }
})
const groups = ref<GroupOptionSummary[]>([])
const groupOptionsLoading = ref(false)
const apiKeyOptionsLoaded = ref(false)
const apiKeyOptionsScopeKey = ref('')
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const form = reactive({
  name: '',
  groupId: '',
  group: undefined as GroupSelection | undefined,
  status: 'active' as 'active' | 'disabled',
  expiresAt: undefined as Dayjs | undefined,
  description: '',
  quotaLimits: createQuotaLimitForm()
})
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const apiKeysApi = useScopedApiKeysApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  selectedIds: () => [systemAccountFilter.value]
})
let groupOptionsRequestId = 0
let groupOptionsLoadingKey: string | undefined
let groupOptionsLoadingPromise: Promise<void> | undefined
let groupOptionsKeyword = ''
let groupOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
const groupOptionsCache = createShortLivedQueryCache<GroupOptionSummary[]>({ ttlMs: 10_000 })
const {
  items: apiKeys,
  loading,
  mobileHasMore,
  mobileLoadingMore,
  pagination,
  tablePagination,
  handleTableChange,
  loadData,
  loadMoreMobile: loadMoreMobileApiKeys,
  resetPagination
} = useResponsivePagedList<ApiKeySummary, { forceOptions?: boolean }>({
  pageSize,
  initialPagination: initialPageState.pagination,
  showTotal: (total, range, context) => context?.hasMore
    ? `已加载到第 ${range?.[1] ?? total - 1} 个 API Key，还有更多`
    : `共 ${formatNumber(total)} 个 API Key`,
  fetchPage: async (options, pageState) => {
    const systemAccountId = isManagementView.value ? apiKeyScopeParams.value?.systemAccountId : undefined
    const [keyList] = await Promise.all([
      apiKeysApi.list(apiKeyListParams(systemAccountId, pageState)),
      loadApiKeyOptions(systemAccountId, options.forceOptions === true)
    ])
    return keyList
  },
  onError: (error) => {
    console.error(error)
    message.error('加载 API Key 失败')
  }
})

const rawColumns = computed(() => {
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
    { title: '累计用量', key: 'usage', width: 180 },
    { title: '美元额度', key: 'quotaLimits', width: 220 },
    { title: '过期时间', dataIndex: 'expiresAt', key: 'expiresAt', width: 180 },
    { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
    { title: '操作', key: 'actions', width: 90, fixed: 'right' }
  )
  return baseColumns
})
const columnStorageKey = computed(() => (isManagementView.value ? 'api-keys:management' : 'api-keys:self'))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['name'],
  minVisible: 1
})

const statusOptions = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]
const listStatusOptions = [
  { label: '全部状态', value: 'all' },
  ...statusOptions
]

const filteredApiKeys = computed(() => apiKeys.value)
const mobileApiKeys = computed(() => apiKeys.value)
const apiKeyScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const groupFilterDisabled = computed(() => isManagementView.value && !apiKeyScopeParams.value?.systemAccountId)
const activeFilterCount = computed(() => [
  keywordFilter.value.trim(),
  statusFilter.value !== 'all',
  !groupFilterDisabled.value && groupFilter.value,
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue
].filter(Boolean).length)
const advancedFilterCount = computed(() => 0)
const gatewayBaseUrl = computed(() => normalizeGatewayBaseUrl((import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined) || inferGatewayBaseUrl()))
const gatewayClientExample = computed(() => [`Base URL：${gatewayBaseUrl.value}`, 'API Key：填本页复制的密钥'].join('\n'))
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = apiKeyScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (systemAccountFilterSelection.value?.kind === 'system_account' && systemAccountFilterSelection.value.id === systemAccountId) {
    return systemAccountFilterSelection.value.name
  }
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName
    || principalLabelForId('system_account', systemAccountId)
    || ''
})

function groupName(groupId: string) {
  const apiKey = apiKeys.value.find((item) => item.groupId === groupId)
  return displayGroupName(apiKey?.groupName, groupId)
}

function selectedGroupSelection(id: string | undefined): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = groups.value.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  if (groupFilterSelection.value?.id === normalizedId) return groupFilterSelection.value
  if (form.group?.id === normalizedId) return form.group
  return undefined
}

function formatKeyPreview(apiKey: Pick<ApiKeySummary, 'key' | 'keyPrefix'>) {
  const value = apiKey.key
  if (!value) return apiKey.keyPrefix ? `${apiKey.keyPrefix}...` : '密钥不可还原'
  if (value.length <= 14) return value
  return `${value.slice(0, 6)}...${value.slice(-4)}`
}

function keyDisplayTitle(apiKey: Pick<ApiKeySummary, 'key' | 'keyPrefix'>) {
  if (apiKey.key) return apiKey.key
  return apiKey.keyPrefix ? `${apiKey.keyPrefix}...` : '密钥不可还原'
}

function apiKeyActions(apiKey: ApiKeySummary): RowActionItem[] {
  const updating = statusUpdatingId.value === apiKey.id
  const statusAction: RowActionItem = apiKey.status === 'active'
    ? {
        key: 'disable',
        label: '停用',
        icon: 'disable',
        tone: 'warning',
        disabled: updating,
        confirmTitle: '确认停用这个 API Key？停用后后续请求会立即被拒绝。',
        confirmOkText: '停用'
      }
    : {
        key: 'enable',
        label: '启用',
        icon: 'enable',
        tone: 'success',
        disabled: updating
      }
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary', disabled: updating },
    statusAction,
    {
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      disabled: updating,
      confirmTitle: '确认删除这个 API Key？删除后会立即失效，关联历史记录和统计将由后台分批清理。',
      confirmOkText: '删除'
    }
  ]
}

async function loadApiKeyOptions(systemAccountId: string | undefined, force = false): Promise<void> {
  const scopeKey = isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self'
  if (!force && apiKeyOptionsLoaded.value && apiKeyOptionsScopeKey.value === scopeKey) {
    return
  }

  apiKeyOptionsLoaded.value = true
  apiKeyOptionsScopeKey.value = scopeKey
}

async function loadGroupOptions(keyword = groupOptionsKeyword, force = false): Promise<void> {
  groupOptionsKeyword = keyword
  const systemAccountId = isManagementView.value ? apiKeyScopeParams.value?.systemAccountId : undefined
  if (isManagementView.value && !systemAccountId) {
    groups.value = []
    groupOptionsLoading.value = false
    groupOptionsLoadingKey = undefined
    groupOptionsLoadingPromise = undefined
    return
  }
  const requestKeyword = normalizeOptionKeyword(keyword)
  const requestKey = JSON.stringify([isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self', requestKeyword ?? '', groupFilter.value ?? '', form.groupId ?? ''])
  if (groupOptionsLoadingKey === requestKey && groupOptionsLoadingPromise) {
    return groupOptionsLoadingPromise
  }
  const requestId = ++groupOptionsRequestId
  if (!force) {
    const cachedGroups = groupOptionsCache.get(requestKey)
    if (cachedGroups) {
      groupOptionsLoadingKey = undefined
      groupOptionsLoadingPromise = undefined
      groupOptionsLoading.value = false
      rememberGroupLabels(cachedGroups)
      syncSelectedGroupSelections(cachedGroups)
      groups.value = cachedGroups
      return
    }
  }
  groupOptionsLoading.value = true
  groupOptionsLoadingKey = requestKey
  groupOptionsLoadingPromise = (async () => {
    try {
      let nextGroups = await groupsApi.options({ systemAccountId, keyword: requestKeyword, limit: 50, manageableOnly: true, preferDefault: true })
      nextGroups = await ensureSelectedGroupOptions(nextGroups, systemAccountId)
      rememberGroupLabels(nextGroups)
      syncSelectedGroupSelections(nextGroups)
      groupOptionsCache.set(requestKey, nextGroups)
      if (requestId !== groupOptionsRequestId) return
      groups.value = nextGroups
    } catch (error) {
      if (requestId !== groupOptionsRequestId) return
      console.error(error)
      message.error('加载分组选项失败')
    } finally {
      if (groupOptionsLoadingKey === requestKey) {
        groupOptionsLoadingKey = undefined
        groupOptionsLoadingPromise = undefined
      }
      if (requestId === groupOptionsRequestId) {
        groupOptionsLoading.value = false
      }
    }
  })()
  return groupOptionsLoadingPromise
}

function handleGroupOptionsDropdown(open: boolean): void {
  if (open) {
    void loadGroupOptions()
  }
}

function handleGroupOptionsSearch(value: string): void {
  groupOptionsKeyword = value
  clearGroupOptionsSearchTimer()
  groupOptionsSearchTimer = window.setTimeout(() => {
    groupOptionsSearchTimer = undefined
    void loadGroupOptions(groupOptionsKeyword)
  }, 250)
}

function resetGroupOptionsSearch(): void {
  groupOptionsKeyword = ''
  clearGroupOptionsSearchTimer()
}

function clearGroupOptionsSearchTimer(): void {
  if (groupOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(groupOptionsSearchTimer)
    groupOptionsSearchTimer = undefined
  }
}

async function ensureSelectedGroupOptions(nextGroups: GroupOptionSummary[], systemAccountId: string | undefined): Promise<GroupOptionSummary[]> {
  const selectedIds = [groupFilter.value, form.groupId].filter((id): id is string => Boolean(id))
  const missingIds = [...new Set(selectedIds)].filter((id) => !nextGroups.some((group) => group.id === id))
  if (!missingIds.length) return nextGroups
  const selectedGroups = await Promise.all(missingIds.map(async (id) => {
    try {
      return await groupsApi.options({ systemAccountId, ids: [id], limit: 1, manageableOnly: true, preferDefault: true })
    } catch {
      return []
    }
  }))
  return mergeOptionsById(selectedGroups.flat(), nextGroups)
}

function syncSelectedGroupSelections(nextGroups = groups.value): void {
  if (groupFilterDisabled.value) {
    groupFilterSelection.value = undefined
    return
  }
  if (groupFilter.value) {
    groupFilterSelection.value = selectedGroupFromOptions(groupFilter.value, nextGroups, groupFilterSelection.value)
  }
  if (form.groupId) {
    form.group = selectedGroupFromOptions(form.groupId, nextGroups, form.group)
  }
}

function selectedGroupFromOptions(id: string | undefined, nextGroups: GroupOptionSummary[], fallback?: GroupSelection): GroupSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const group = nextGroups.find((item) => item.id === normalizedId)
  if (group) return { id: group.id, name: group.name }
  return fallback?.id === normalizedId ? fallback : undefined
}

function mergeOptionsById<T extends { id: string }>(leading: T[], trailing: T[]): T[] {
  const merged = new Map<string, T>()
  for (const item of [...leading, ...trailing]) {
    merged.set(item.id, item)
  }
  return [...merged.values()]
}

function normalizeOptionKeyword(value?: string): string | undefined {
  const keyword = value?.trim()
  return keyword ? keyword : undefined
}

function resetFilters() {
  const defaults = defaultApiKeysPageState()
  keywordFilter.value = defaults.keywordFilter
  statusFilter.value = defaults.statusFilter
  groupFilterSelection.value = defaults.groupFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountFilterSelection.value = defaults.systemAccountFilterSelection
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  pageStateCache.clear()
  void loadData({ forceOptions: true })
}

function applyFilters() {
  resetPagination()
  void loadData()
}

function refreshApiKeys() {
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

function handleSystemAccountFilterChange() {
  groupFilterSelection.value = undefined
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

async function refreshMobileApiKeys() {
  resetSystemAccountOptionsSearch()
  resetGroupOptionsSearch()
  resetPagination()
  await loadData({ forceOptions: true })
}

function handleGroupFilterChange() {
  resetGroupOptionsSearch()
  applyFilters()
}

function snapshotPageState(): ApiKeysPageState {
  return {
    groupFilter: groupFilterSelection.value,
    keywordFilter: keywordFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    statusFilter: statusFilter.value,
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value
  }
}

function apiKeyListParams(systemAccountId: string | undefined, pageState: { current: number; pageSize: number }) {
  return {
    systemAccountId,
    page: pageState.current,
    pageSize: pageState.pageSize,
    keyword: keywordFilter.value.trim() || undefined,
    status: statusFilter.value,
    groupId: isManagementView.value && !systemAccountId ? undefined : groupFilter.value
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

async function openCreate() {
  if (isManagementView.value && !apiKeyScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  resetGroupOptionsSearch()
  await loadGroupOptions()
  editingId.value = undefined
  const defaultGroup = groups.value[0]
  Object.assign(form, { name: '', groupId: defaultGroup?.id ?? '', group: defaultGroup ? { id: defaultGroup.id, name: defaultGroup.name } : undefined, status: 'active', expiresAt: undefined, description: '', quotaLimits: createQuotaLimitForm() })
  modalOpen.value = true
}

async function openEdit(apiKey: ApiKeySummary) {
  editingId.value = apiKey.id
  rememberGroupLabel(apiKey.groupId, apiKey.groupName)
  Object.assign(form, { name: apiKey.name, groupId: apiKey.groupId, group: apiKey.groupName ? { id: apiKey.groupId, name: apiKey.groupName } : undefined, status: apiKey.status, expiresAt: undefined, description: apiKey.description ?? '', quotaLimits: createQuotaLimitForm(apiKey.quotaLimits) })
  resetGroupOptionsSearch()
  await loadGroupOptions()
  modalOpen.value = true
}

function handleApiKeyAction(key: string, apiKey: ApiKeySummary) {
  if (key === 'edit') {
    void openEdit(apiKey)
    return
  }
  if (key === 'enable' || key === 'disable') {
    void updateApiKeyStatus(apiKey, key === 'enable' ? 'active' : 'disabled')
    return
  }
  if (key === 'delete') {
    void removeApiKey(apiKey.id)
  }
}

async function updateApiKeyStatus(apiKey: ApiKeySummary, status: 'active' | 'disabled') {
  statusUpdatingId.value = apiKey.id
  try {
    await apiKeysApi.update(apiKey.id, { status }, apiKeyScopeParams.value)
    message.success(status === 'active' ? 'API Key 已启用' : 'API Key 已停用')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, status === 'active' ? '启用 API Key 失败' : '停用 API Key 失败'))
  } finally {
    if (statusUpdatingId.value === apiKey.id) {
      statusUpdatingId.value = ''
    }
  }
}

const saveApiKey = submitAction('api_keys.save', async () => {
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
      await apiKeysApi.update(editingId.value, payload, apiKeyScopeParams.value)
      message.success('API Key 已更新')
    } else {
      const result = await apiKeysApi.create(payload, apiKeyScopeParams.value)
      createdKey.value = result.key
      createdKeyOpen.value = true
      message.success('API Key 已创建')
    }
    modalOpen.value = false
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存 API Key 失败'))
  }
})

function quotaLimitsPayload(): ApiKeyQuotaLimits {
  return buildQuotaLimitsPayload(form.quotaLimits)
}

async function copyText(value: string) {
  await copyTextToClipboard(value)
}

async function copyGatewayBaseUrl() {
  await copyText(gatewayBaseUrl.value)
}

async function copyCreatedKey() {
  await copyText(createdKey.value)
}

function inferGatewayBaseUrl() {
  if (typeof window === 'undefined') return 'http://127.0.0.1:3000'
  if (import.meta.env.DEV) {
    return `${window.location.protocol}//${window.location.hostname}:3000`
  }
  return window.location.origin
}

function normalizeGatewayBaseUrl(value: string) {
  const trimmed = value.trim().replace(/\/+$/, '')
  return trimmed || inferGatewayBaseUrl()
}

async function removeApiKey(id: string) {
  try {
    await apiKeysApi.delete(id, apiKeyScopeParams.value)
    message.success('API Key 已删除，关联记录将后台清理')
    await loadData()
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '删除 API Key 失败'))
  }
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(groupFilterDisabled, (disabled) => {
  if (!disabled) return
  groupFilterSelection.value = undefined
  groups.value = []
}, { immediate: true })
watch(apiKeys, (items) => {
  for (const item of items) {
    rememberGroupLabel(item.groupId, item.groupName)
  }
  rememberGroupSelection(groupFilterSelection.value)
  rememberPrincipalSelection(systemAccountFilterSelection.value)
  syncSelectedGroupSelections()
}, { immediate: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onBeforeUnmount(clearGroupOptionsSearchTimer)

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

.advanced-filter-form :deep(.ant-select) {
  width: 100%;
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

.gateway-help-note {
  border-radius: 8px;
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

</style>
