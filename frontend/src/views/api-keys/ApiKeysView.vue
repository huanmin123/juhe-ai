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
          show-provider-label
          placeholder="绑定分组"
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
          <span>绑定分组</span>
          <GroupSelect
            v-model:value="groupFilter"
            v-model:selected-group="groupFilterSelection"
            allow-clear
            :disabled="groupFilterDisabled"
            :filter-option="false"
            :groups="groups"
            :loading="groupOptionsLoading"
            show-provider-label
            placeholder="绑定分组"
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

    <ApiKeyResponsiveList
      :columns="managedColumns"
      :data-source="filteredApiKeys"
      :mobile-data-source="mobileApiKeys"
      :loading="loading"
      :loading-more="mobileLoadingMore"
      :mobile-has-more="mobileHasMore"
      :pagination="tablePagination"
      :is-management-view="isManagementView"
      :key-copying-id="keyCopyingId"
      :primary-actions="apiKeyPrimaryActions"
      :more-actions="apiKeyMoreActions"
      @action-click="handleApiKeyAction"
      @change="handleTableChange"
      @copy-key="copyKeyPreview"
      @mobile-load-more="loadMoreMobileApiKeys"
      @mobile-refresh="refreshMobileApiKeys"
    />

    <ApiKeyHelpModal
      v-model:open="helpOpen"
      :gateway-base-url="gatewayBaseUrl"
      :gateway-client-example="gatewayClientExample"
      @copy-base-url="copyGatewayBaseUrl"
    />

    <ApiKeyEditModal
      ref="apiKeyEditModalRef"
      :api-keys-api="apiKeysApi"
      :groups-api="groupsApi"
      :is-management-view="isManagementView"
      :scope-params="apiKeyScopeParams"
      :target-system-account-label="targetSystemAccountLabel"
      @created="showCreatedKey"
      @reload="handleApiKeyModalReload"
      @updated="handleApiKeyUpdated"
    />

    <ApiKeyCreatedSecretModal
      v-model:open="createdKeyOpen"
      :api-key="createdKey"
      :client-config-example="createdKeyClientConfigExample"
      :gateway-base-url="gatewayBaseUrl"
      :message="createdKeyModalMessage"
      :title="createdKeyModalTitle"
      @copy-api-key="copyCreatedKey"
      @copy-client-config="copyCreatedClientConfig"
      @copy-gateway-base-url="copyGatewayBaseUrl"
    />
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import GroupSelect from '@/components/GroupSelect.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedApiKeysApi, useScopedGroupsApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatNumber } from '@/shared/formatters'
import { rememberGroupLabel, rememberGroupSelection, type GroupSelection } from '@/shared/groupLabelCache'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { ApiKeySummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import {
  apiKeyGroupBindings,
} from './apiKeyFormatters'
import { defaultApiKeysPageState, type ApiKeysPageState } from './apiKeyPageState'
import {
  apiKeyColumnStorageKey,
  apiKeyListStatusOptions as listStatusOptions,
  buildApiKeyTableColumns
} from './apiKeyTableConfig'
import ApiKeyCreatedSecretModal from './ApiKeyCreatedSecretModal.vue'
import ApiKeyEditModal from './ApiKeyEditModal.vue'
import ApiKeyHelpModal from './ApiKeyHelpModal.vue'
import ApiKeyResponsiveList from './ApiKeyResponsiveList.vue'
import { useApiKeyGroupOptions, type ApiKeyScopeParams } from './useApiKeyGroupOptions'
import { useApiKeyRowActions } from './useApiKeyRowActions'

const createdKeyOpen = ref(false)
const helpOpen = ref(false)
const createdKey = ref('')
const createdKeyModalTitle = ref('API Key 已创建')
const createdKeyModalMessage = ref('按下面 3 步完成客户端接入；完整密钥只在此处直接展示，请先复制保存。')
const apiKeyEditModalRef = ref<InstanceType<typeof ApiKeyEditModal>>()
const pageSize = 50
const pageStateCache = usePageStateCache<ApiKeysPageState>(undefined, () => defaultApiKeysPageState(pageSize))
const initialPageState = pageStateCache.read()
const keywordFilter = ref(initialPageState.keywordFilter)
const statusFilter = ref(initialPageState.statusFilter)
const groupFilterSelection = ref<GroupSelection | undefined>(initialPageState.groupFilter)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const apiKeyOptionsLoaded = ref(false)
const apiKeyOptionsScopeKey = ref('')
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const apiKeysApi = useScopedApiKeysApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const apiKeyScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const emptyApiKeyGroupBindings = () => []
const emptyApiKeyGroupBindingIds = computed<string[]>(() => [])
const {
  clearGroupOptionsSearchTimer,
  groups,
  groupOptionsLoading,
  handleGroupOptionsDropdown,
  handleGroupOptionsSearch,
  resetGroupOptionsSearch,
  selectedGroupSelection,
  syncSelectedGroupSelections
} = useApiKeyGroupOptions({
  groupsApi,
  isManagementView,
  isFormContext: () => false,
  listScopeParams: apiKeyScopeParams,
  formScopeParams: apiKeyScopeParams,
  groupFilterSelection,
  formGroupBindings: emptyApiKeyGroupBindings,
  formGroupBindingIds: emptyApiKeyGroupBindingIds,
  onGroupFilterCleared: () => {
    resetPagination()
    void loadData({ forceOptions: true })
  }
})
const groupFilter = computed({
  get: () => groupFilterSelection.value?.id,
  set: (id: string | undefined) => {
    groupFilterSelection.value = selectedGroupSelection(id)
  }
})
const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  onMissingSelectedIds: (ids) => {
    if (!ids.includes(systemAccountFilter.value)) return
    systemAccountFilter.value = allSystemAccountsValue
    systemAccountFilterSelection.value = undefined
    groupFilterSelection.value = undefined
    resetSystemAccountOptionsSearch()
    resetGroupOptionsSearch()
    resetPagination()
    void loadData({ forceOptions: true })
  },
  selectedIds: () => [systemAccountFilter.value]
})
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
  removeItems: removeApiKeyItems,
  resetPagination,
  updateItems: updateApiKeyItems
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
    message.error(extractApiErrorMessage(error, '加载 API Key 失败'))
  }
})
const {
  keyCopyingId,
  apiKeyMoreActions,
  apiKeyPrimaryActions,
  copyKeyPreview,
  handleApiKeyAction
} = useApiKeyRowActions({
  apiKeysApi,
  operationScopeParams: apiKeyOperationScopeParams,
  updateItems: updateApiKeyItems,
  removeItems: removeApiKeyItems,
  reload: () => loadData({ quiet: true }),
  openEdit,
  showCreatedKey
})

const rawColumns = computed(() => buildApiKeyTableColumns(isManagementView.value))
const columnStorageKey = computed(() => apiKeyColumnStorageKey(isManagementView.value))
const {
  managedColumns,
  columnSettings,
  updateColumnSettings,
  resetColumnSettings
} = useTableColumnSettings(columnStorageKey, rawColumns, {
  requiredKeys: ['name'],
  minVisible: 1
})

const filteredApiKeys = computed(() => apiKeys.value)
const mobileApiKeys = computed(() => apiKeys.value)
const groupFilterDisabled = computed(() => false)
const activeFilterCount = computed(() => [
  keywordFilter.value.trim(),
  statusFilter.value !== 'all',
  groupFilter.value,
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue
].filter(Boolean).length)
const advancedFilterCount = computed(() => 0)
const gatewayBaseUrl = computed(() => normalizeGatewayBaseUrl((import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined) || inferGatewayBaseUrl()))
const gatewayClientExample = computed(() => [`Base URL：${gatewayBaseUrl.value}`, 'API Key：填复制到的完整密钥'].join('\n'))
const createdKeyClientConfigExample = computed(() => [
  `Base URL：${gatewayBaseUrl.value}`,
  'API Key：填入上方复制的完整密钥',
  '常用验证：拉取模型列表或发送一次最小对话请求'
].join('\n'))
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

async function loadApiKeyOptions(systemAccountId: string | undefined, force = false): Promise<void> {
  const scopeKey = isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self'
  if (!force && apiKeyOptionsLoaded.value && apiKeyOptionsScopeKey.value === scopeKey) {
    return
  }

  apiKeyOptionsLoaded.value = true
  apiKeyOptionsScopeKey.value = scopeKey
}

function resetFilters() {
  const defaults = defaultApiKeysPageState(pageSize)
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
    groupId: groupFilter.value
  }
}

function openCreate() {
  void apiKeyEditModalRef.value?.openCreate()
}

function openEdit(apiKey: ApiKeySummary) {
  void apiKeyEditModalRef.value?.openEdit(apiKey)
}

function apiKeyOperationScopeParams(apiKey?: Pick<ApiKeySummary, 'systemAccountId'>): ApiKeyScopeParams {
  const systemAccountId = apiKey?.systemAccountId?.trim()
    || apiKeyScopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

function showCreatedKey(payload: { key: string; title: string; message: string }) {
  createdKey.value = payload.key
  createdKeyModalTitle.value = payload.title
  createdKeyModalMessage.value = payload.message
  createdKeyOpen.value = true
}

function handleApiKeyUpdated(apiKey: ApiKeySummary) {
  updateApiKeyItems((item) => item.id === apiKey.id, () => apiKey)
}

function handleApiKeyModalReload(options?: { quiet?: boolean }) {
  void loadData(options)
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

async function copyCreatedClientConfig() {
  await copyText(createdKeyClientConfigExample.value)
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

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(apiKeys, (items) => {
  for (const item of items) {
    for (const binding of apiKeyGroupBindings(item)) {
      rememberGroupLabel(binding.groupId, binding.groupName)
    }
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

</style>
