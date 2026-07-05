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
        <RouteStrategySelect
          v-model:value="routeStrategyFilter"
          v-model:selected-strategy="routeStrategyFilterSelection"
          allow-clear
          class="toolbar-select responsive-list-inline-filter"
          :filter-option="false"
          :loading="routeStrategyOptionsLoading"
          :route-strategies="routeStrategyOptionsRaw"
          :show-system-account-label="isManagementView"
          placeholder="策略路由"
          @change="handleRouteStrategyFilterChange"
          @dropdown-visible-change="handleRouteStrategyOptionsDropdown"
          @search="handleRouteStrategyOptionsSearch"
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
          <span>策略路由</span>
          <RouteStrategySelect
            v-model:value="routeStrategyFilter"
            v-model:selected-strategy="routeStrategyFilterSelection"
            allow-clear
            :filter-option="false"
            :loading="routeStrategyOptionsLoading"
            :route-strategies="routeStrategyOptionsRaw"
            :show-system-account-label="isManagementView"
            placeholder="策略路由"
            @change="handleRouteStrategyFilterChange"
            @dropdown-visible-change="handleRouteStrategyOptionsDropdown"
            @search="handleRouteStrategyOptionsSearch"
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
      :route-strategies-api="routeStrategiesApi"
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
      :gateway-base-url="gatewayBaseUrl"
      :message="createdKeyModalMessage"
      :minimal-http-request-example="createdKeyMinimalHttpRequestExample"
      :minimal-http-request-platform-label="createdKeyMinimalHttpRequestPlatformLabel"
      :title="createdKeyModalTitle"
      @copy-api-key="copyCreatedKey"
      @copy-gateway-base-url="copyGatewayBaseUrl"
      @copy-minimal-http-request="copyCreatedMinimalHttpRequest"
    />
  </a-card>
</template>

<script setup lang="ts">
import { QuestionCircleOutlined } from '@ant-design/icons-vue'
import { message } from '@/lib/antd'
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

import TableColumnManager from '@/components/TableColumnManager.vue'
import { useTableColumnSettings } from '@/components/tableColumnSettings'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RouteStrategySelect from '@/components/RouteStrategySelect.vue'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useResponsivePagedList } from '@/composables/useResponsivePagedList'
import { useScopedApiKeysApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { extractApiErrorMessage } from '@/shared/apiError'
import { copyTextToClipboard } from '@/shared/clipboard'
import { formatNumber } from '@/shared/formatters'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import { routeStrategySelectionFromOption } from '@/shared/routeStrategyLabelCache'
import { createShortLivedQueryCache } from '@/shared/shortLivedQueryCache'
import type { ApiKeySummary, RouteStrategyOptionSummary } from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'
import { defaultApiKeysPageState, type ApiKeyRouteStrategyFilterSelection, type ApiKeysPageState } from './apiKeyPageState'
import {
  apiKeyColumnStorageKey,
  apiKeyListStatusOptions as listStatusOptions,
  buildApiKeyTableColumns
} from './apiKeyTableConfig'
import type { ApiKeyScopeParams } from './apiKeyScope'
import ApiKeyCreatedSecretModal from './ApiKeyCreatedSecretModal.vue'
import ApiKeyEditModal from './ApiKeyEditModal.vue'
import ApiKeyHelpModal from './ApiKeyHelpModal.vue'
import ApiKeyResponsiveList from './ApiKeyResponsiveList.vue'
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
const routeStrategyFilterSelection = ref<ApiKeyRouteStrategyFilterSelection | undefined>(initialPageState.routeStrategyFilter)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const apiKeyOptionsLoaded = ref(false)
const apiKeyOptionsScopeKey = ref('')
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const apiKeysApi = useScopedApiKeysApi(isManagementView)
const routeStrategiesApi = useScopedRouteStrategiesApi(isManagementView)
const routeStrategyOptionsRaw = ref<RouteStrategyOptionSummary[]>([])
const routeStrategyOptionsLoading = ref(false)
const routeStrategyOptionsCache = createShortLivedQueryCache<RouteStrategyOptionSummary[]>({ ttlMs: 10_000 })
let routeStrategyOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let routeStrategyOptionsKeyword = ''
let routeStrategyOptionsRequestToken = 0
let routeStrategyOptionsLoadingKey: string | undefined
let routeStrategyOptionsLoadingPromise: Promise<void> | undefined
const apiKeyScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
const routeStrategyFilter = computed({
  get: () => routeStrategyFilterSelection.value?.id,
  set: (id: string | undefined) => {
    routeStrategyFilterSelection.value = selectedRouteStrategySelection(id)
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
    routeStrategyFilterSelection.value = undefined
    resetSystemAccountOptionsSearch()
    resetRouteStrategyOptionsSearch()
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
  requestSignature: (_options, pageState) => {
    const systemAccountId = isManagementView.value ? apiKeyScopeParams.value?.systemAccountId : undefined
    return [
      isManagementView.value ? 'management' : 'self',
      apiKeyListParams(systemAccountId, pageState)
    ]
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
const activeFilterCount = computed(() => [
  keywordFilter.value.trim(),
  statusFilter.value !== 'all',
  routeStrategyFilter.value,
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue
].filter(Boolean).length)
const advancedFilterCount = computed(() => 0)
const gatewayBaseUrl = computed(() => normalizeGatewayBaseUrl((import.meta.env.VITE_JUHE_AI_GATEWAY_BASE_URL as string | undefined) || inferGatewayBaseUrl()))
const gatewayClientExample = computed(() => [`Base URL：${gatewayBaseUrl.value}`, 'API Key：填复制到的完整密钥'].join('\n'))
const clientHttpRequestPlatform = computed(inferClientHttpRequestPlatform)
const createdKeyMinimalHttpRequestPlatformLabel = computed(() => clientHttpRequestPlatformLabel(clientHttpRequestPlatform.value))
const createdKeyMinimalHttpRequestExample = computed(() => buildMinimalHttpRequestExample({
  apiKey: createdKey.value,
  platform: clientHttpRequestPlatform.value,
  url: gatewayV1EndpointUrl('/models')
}))
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

  await loadRouteStrategyOptions('', force)
  apiKeyOptionsLoaded.value = true
  apiKeyOptionsScopeKey.value = scopeKey
}

async function loadRouteStrategyOptions(keyword = routeStrategyOptionsKeyword, force = false): Promise<void> {
  routeStrategyOptionsKeyword = keyword
  const requestKeyword = keyword.trim() || undefined
  const systemAccountId = apiKeyScopeParams.value?.systemAccountId
  const requestKey = routeStrategyOptionsRequestKey(systemAccountId, requestKeyword)
  if (!force && routeStrategyOptionsLoadingKey === requestKey && routeStrategyOptionsLoadingPromise) {
    return routeStrategyOptionsLoadingPromise
  }
  const requestToken = ++routeStrategyOptionsRequestToken
  if (!force) {
    const cachedOptions = routeStrategyOptionsCache.get(requestKey)
    if (cachedOptions) {
      routeStrategyOptionsLoadingKey = undefined
      routeStrategyOptionsLoadingPromise = undefined
      routeStrategyOptionsRaw.value = cachedOptions
      routeStrategyOptionsLoading.value = false
      return
    }
  }
  routeStrategyOptionsLoading.value = true
  routeStrategyOptionsLoadingKey = requestKey
  routeStrategyOptionsLoadingPromise = (async () => {
    try {
      const nextOptions = await routeStrategiesApi.options({
        keyword: requestKeyword,
        limit: 50,
        activeOnly: false,
        systemAccountId
      })
      routeStrategyOptionsCache.set(requestKey, nextOptions)
      if (requestToken !== routeStrategyOptionsRequestToken) return
      routeStrategyOptionsRaw.value = nextOptions
      if (force) {
        routeStrategyFilterSelection.value = selectedRouteStrategySelection(routeStrategyFilterSelection.value?.id)
      }
    } catch (error) {
      if (requestToken !== routeStrategyOptionsRequestToken) return
      message.error(extractApiErrorMessage(error, '策略路由选项加载失败'))
    } finally {
      if (routeStrategyOptionsLoadingKey === requestKey) {
        routeStrategyOptionsLoadingKey = undefined
        routeStrategyOptionsLoadingPromise = undefined
      }
      if (requestToken === routeStrategyOptionsRequestToken) {
        routeStrategyOptionsLoading.value = false
      }
    }
  })()
  return routeStrategyOptionsLoadingPromise
}

function routeStrategyOptionsRequestKey(systemAccountId: string | undefined, keyword: string | undefined): string {
  return JSON.stringify([
    isManagementView.value ? `management:${systemAccountId ?? 'all'}` : 'self',
    keyword ?? ''
  ])
}

function handleRouteStrategyOptionsDropdown(open: boolean) {
  if (open && !routeStrategyOptionsRaw.value.length) void loadRouteStrategyOptions()
}

function handleRouteStrategyOptionsSearch(value: string) {
  routeStrategyOptionsKeyword = value
  clearRouteStrategyOptionsSearchTimer()
  routeStrategyOptionsSearchTimer = window.setTimeout(() => {
    routeStrategyOptionsSearchTimer = undefined
    void loadRouteStrategyOptions(routeStrategyOptionsKeyword)
  }, 250)
}

function resetRouteStrategyOptionsSearch() {
  routeStrategyOptionsKeyword = ''
  routeStrategyOptionsRequestToken += 1
  routeStrategyOptionsLoadingKey = undefined
  routeStrategyOptionsLoadingPromise = undefined
  routeStrategyOptionsLoading.value = false
  clearRouteStrategyOptionsSearchTimer()
}

function clearRouteStrategyOptionsSearchTimer() {
  if (routeStrategyOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(routeStrategyOptionsSearchTimer)
    routeStrategyOptionsSearchTimer = undefined
  }
}

function selectedRouteStrategySelection(id: string | undefined): ApiKeyRouteStrategyFilterSelection | undefined {
  const normalizedId = id?.trim()
  if (!normalizedId) return undefined
  const strategy = routeStrategyOptionsRaw.value.find((item) => item.id === normalizedId)
  if (strategy) return routeStrategySelectionFromOption(strategy)
  return routeStrategyFilterSelection.value?.id === normalizedId ? routeStrategyFilterSelection.value : undefined
}

function resetFilters() {
  const defaults = defaultApiKeysPageState(pageSize)
  keywordFilter.value = defaults.keywordFilter
  statusFilter.value = defaults.statusFilter
  routeStrategyFilterSelection.value = defaults.routeStrategyFilter
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountFilterSelection.value = defaults.systemAccountFilterSelection
  resetSystemAccountOptionsSearch()
  resetRouteStrategyOptionsSearch()
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
  resetRouteStrategyOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

function handleSystemAccountFilterChange() {
  routeStrategyFilterSelection.value = undefined
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetSystemAccountOptionsSearch()
  resetRouteStrategyOptionsSearch()
  resetPagination()
  void loadData({ forceOptions: true })
}

async function refreshMobileApiKeys() {
  resetSystemAccountOptionsSearch()
  resetRouteStrategyOptionsSearch()
  resetPagination()
  await loadData({ forceOptions: true })
}

function handleRouteStrategyFilterChange() {
  resetRouteStrategyOptionsSearch()
  applyFilters()
}

function snapshotPageState(): ApiKeysPageState {
  return {
    keywordFilter: keywordFilter.value,
    pagination: { current: pagination.current, pageSize: pagination.pageSize },
    routeStrategyFilter: routeStrategyFilterSelection.value,
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
    routeStrategyId: routeStrategyFilter.value
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

async function copyCreatedMinimalHttpRequest() {
  await copyText(createdKeyMinimalHttpRequestExample.value)
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

function gatewayV1EndpointUrl(endpointPath: string) {
  const normalizedEndpointPath = endpointPath.startsWith('/') ? endpointPath : `/${endpointPath}`
  const normalizedBaseUrl = gatewayBaseUrl.value.replace(/\/+$/, '')
  const basePath = gatewayBasePathname(normalizedBaseUrl)
  const versionPrefix = basePath.endsWith('/v1') ? '' : '/v1'
  return `${normalizedBaseUrl}${versionPrefix}${normalizedEndpointPath}`
}

function gatewayBasePathname(value: string) {
  try {
    return new URL(value).pathname.replace(/\/+$/, '')
  } catch {
    return value.replace(/\/+$/, '')
  }
}

type ClientHttpRequestPlatform = 'windows' | 'macos' | 'linux' | 'curl'

interface NavigatorWithUserAgentData extends Navigator {
  userAgentData?: {
    platform?: string
  }
}

function inferClientHttpRequestPlatform(): ClientHttpRequestPlatform {
  if (typeof navigator === 'undefined') return 'curl'
  const clientNavigator = navigator as NavigatorWithUserAgentData
  const platformText = [
    clientNavigator.userAgentData?.platform,
    clientNavigator.platform,
    clientNavigator.userAgent
  ].filter(Boolean).join(' ').toLowerCase()

  if (platformText.includes('win')) return 'windows'
  if (platformText.includes('mac')) return 'macos'
  if (platformText.includes('linux') || platformText.includes('x11')) return 'linux'
  return 'curl'
}

function clientHttpRequestPlatformLabel(platform: ClientHttpRequestPlatform) {
  if (platform === 'windows') return 'Windows PowerShell'
  if (platform === 'macos') return 'macOS 终端'
  if (platform === 'linux') return 'Linux 终端'
  return '通用 curl'
}

function buildMinimalHttpRequestExample(input: {
  apiKey: string
  platform: ClientHttpRequestPlatform
  url: string
}) {
  if (input.platform === 'windows') {
    return `Invoke-RestMethod -Uri "${input.url}" -Headers @{ Authorization = "Bearer ${input.apiKey}" }`
  }
  return `curl -sS "${input.url}" -H "Authorization: Bearer ${input.apiKey}"`
}

watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(apiKeys, () => rememberPrincipalSelection(systemAccountFilterSelection.value), { immediate: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onBeforeUnmount(clearRouteStrategyOptionsSearchTimer)

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
