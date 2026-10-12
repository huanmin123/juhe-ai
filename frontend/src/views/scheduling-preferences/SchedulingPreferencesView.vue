<template>
  <a-card class="page-card scheduling-preferences-page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索调度偏好"
      filter-title="筛选调度偏好"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="0"
      :refresh-loading="loading"
      @reset="resetFilters"
      @refresh="refreshSchedulingPreferences"
      @search="applyFilters"
    >
      <template #inline-filters>
        <SystemPrincipalSelect
          v-if="isManagementView"
          v-model:value="systemAccountFilter"
          :accounts="systemAccounts"
          :active-only="false"
          :filter-option="false"
          :loading="systemAccountOptionsLoading"
          v-model:selected-principal="systemAccountFilterSelection"
          include-all
          class="toolbar-select responsive-list-inline-filter"
          @change="handleSystemAccountFilterChange"
          @dropdown-visible-change="handleSystemAccountOptionsDropdown"
          @search="handleSystemAccountOptionsSearch"
        />
        <a-select
          v-model:value="chatModeFilter"
          class="toolbar-select responsive-list-inline-filter"
          :options="chatModeFilterOptions"
          @change="applyFilters"
        />
        <a-select
          v-model:value="statusFilter"
          class="toolbar-select responsive-list-inline-filter"
          :options="statusFilterOptions"
          @change="applyFilters"
        />
      </template>
      <template #actions>
        <a-button type="primary" @click="openCreate">
          <template #icon><PlusOutlined /></template>
          新建调度偏好
        </a-button>
      </template>
      <template #filters>
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
        <label class="mobile-filter-field">
          <span>对话形态</span>
          <a-select v-model:value="chatModeFilter" :options="chatModeFilterOptions" />
        </label>
        <label class="mobile-filter-field">
          <span>状态</span>
          <a-select v-model:value="statusFilter" :options="statusFilterOptions" />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table scheduling-preference-table"
      :columns="columns"
      :data-source="items"
      row-key="id"
      :loading="loading"
      :pagination="pagination"
      :scroll-x="isManagementView ? 1180 : 1000"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-refresh="loadSchedulingPreferences"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无调度偏好。创建后可在 API Key 中绑定。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <span class="scheduling-preference-name-text">{{ record.name }}</span>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ schedulingPreferenceSystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'chatMode'">
          <a-tag :color="schedulingPreferenceChatModeColor(record.chatMode)">{{ schedulingPreferenceChatModeText(record.chatMode) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'builtIn'">
          <a-tag v-if="record.builtIn" color="gold">内置</a-tag>
          <span v-else class="muted-cell">-</span>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="schedulingPreferenceStatusColor(record.enabled)">{{ schedulingPreferenceStatusText(record.enabled) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'boundApiKeyCount'">
          <a-tag>{{ formatNumber(record.boundApiKeyCount) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'updatedAt'">
          <span class="muted-cell">{{ formatDateTime(record.updatedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <span v-if="record.builtIn" class="muted-cell">内置只读</span>
          <RowActions v-else :actions="schedulingPreferenceActions(record)" @action-click="handleSchedulingPreferenceAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">
              <div class="mobile-list-card-name-row">
                <span>{{ record.name }}</span>
                <a-tag v-if="record.builtIn" color="gold">内置</a-tag>
              </div>
            </div>
            <div class="mobile-list-card-tags">
              <a-tag :color="schedulingPreferenceChatModeColor(record.chatMode)">{{ schedulingPreferenceChatModeText(record.chatMode) }}</a-tag>
              <a-tag :color="schedulingPreferenceStatusColor(record.enabled)">{{ schedulingPreferenceStatusText(record.enabled) }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>系统账户</span>
              <strong>{{ schedulingPreferenceSystemAccountText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>绑定 API Key</span>
              <strong>{{ formatNumber(record.boundApiKeyCount) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>更新时间</span>
              <strong>{{ formatDateTime(record.updatedAt) }}</strong>
            </div>
          </div>
          <div v-if="!record.builtIn" class="mobile-list-card-actions">
            <RowActions
              variant="button"
              :actions="schedulingPreferenceActions(record)"
              @action-click="handleSchedulingPreferenceAction($event, record)"
            />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑调度偏好' : '新建调度偏好'" width="720px" :confirm-loading="saving" destroy-on-close @ok="saveSchedulingPreference">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-form layout="vertical" class="scheduling-preference-modal-form">
        <a-form-item label="名称" required tooltip="用于在 API Key 绑定调度偏好时识别这套对话调度方式。">
          <a-input v-model:value="form.name" placeholder="请输入调度偏好名称" />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="对话形态" required tooltip="成本优先保持当前账号缓存和会话粘黏；速度优先先观察首字慢样本，确认账号近期变慢后再优先切换到更快账号。作用域为当前请求所落分组内的账号调度。">
              <a-segmented v-model:value="form.chatMode" block :options="chatModeOptions" />
            </a-form-item>
          </a-col>
          <a-col v-if="editingId" :span="12">
            <a-form-item label="状态" required tooltip="停用后，绑定保留，期间回落系统默认（对话·成本优先）。">
              <a-select v-model:value="form.enabled" :options="enabledOptions" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-alert
          v-if="editingId && !form.enabled"
          class="modal-alert"
          type="warning"
          show-icon
          :message="`停用后绑定保留，期间回落系统默认（对话·成本优先）。当前有 ${formatNumber(editingBoundApiKeyCount)} 个 API Key 绑定此偏好。`"
        />
        <template v-if="form.chatMode === 'speed_first'">
          <div class="modal-section-title">
            <span>速度优先参数</span>
            <a-tooltip title="只作用于速度优先的可重放文本；图像和其他副作用请求永久排除，不记录慢样本也不自动切号。">
              <InfoCircleOutlined class="scheduling-preference-field-help-icon" />
            </a-tooltip>
          </div>
          <div class="scheduling-config-grid">
            <a-form-item label="首字截止" required tooltip="只作用于速度优先的可重放文本；超过该首字时长的样本计为慢样本。默认 30000 毫秒，范围 10000-60000 毫秒。">
              <a-input-number
                v-model:value="form.speedFirst.firstByteDeadlineMs"
                :min="schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.min"
                :max="schedulingPreferenceSpeedFirstBounds.firstByteDeadlineMs.max"
                :step="1000"
                :precision="0"
                addon-after="毫秒"
              />
            </a-form-item>
            <a-form-item label="慢速触发次数" required tooltip="窗口期内达到这个慢速次数后，账号进入速度降级。">
              <a-input-number
                v-model:value="form.speedFirst.slowTriggerCount"
                :min="schedulingPreferenceSpeedFirstBounds.slowTriggerCount.min"
                :max="schedulingPreferenceSpeedFirstBounds.slowTriggerCount.max"
                addon-after="次"
              />
            </a-form-item>
            <a-form-item label="慢速窗口期" required tooltip="统计连续慢速触发的时间窗口。">
              <a-input-number
                v-model:value="form.speedFirst.slowWindowSeconds"
                :min="schedulingPreferenceSpeedFirstBounds.slowWindowSeconds.min"
                :max="schedulingPreferenceSpeedFirstBounds.slowWindowSeconds.max"
                addon-after="秒"
              />
            </a-form-item>
            <a-form-item label="恢复成功次数" required tooltip="后台探针连续满足首字阈值达到该次数后，账号恢复正常调度。">
              <a-input-number
                v-model:value="form.speedFirst.recoverySuccessCount"
                :min="schedulingPreferenceSpeedFirstBounds.recoverySuccessCount.min"
                :max="schedulingPreferenceSpeedFirstBounds.recoverySuccessCount.max"
                addon-after="次"
              />
            </a-form-item>
            <a-form-item label="探针间隔" required tooltip="账号速度降级后，后台探针的最小执行间隔。">
              <a-input-number
                v-model:value="form.speedFirst.probeIntervalSeconds"
                :min="schedulingPreferenceSpeedFirstBounds.probeIntervalSeconds.min"
                :max="schedulingPreferenceSpeedFirstBounds.probeIntervalSeconds.max"
                addon-after="秒"
              />
            </a-form-item>
            <a-form-item label="降级租约时间" required tooltip="确认慢或探针失败后刷新这段租约；持续慢会继续后置，连续达标后恢复。">
              <a-input-number
                v-model:value="form.speedFirst.degradedTtlSeconds"
                :min="schedulingPreferenceSpeedFirstBounds.degradedTtlSeconds.min"
                :max="schedulingPreferenceSpeedFirstBounds.degradedTtlSeconds.max"
                addon-after="秒"
              />
            </a-form-item>
            <a-form-item label="单请求切号次数" required tooltip="同一个请求在账号已确认首字慢后，最多隐藏切换账号的次数。">
              <a-input-number
                v-model:value="form.speedFirst.maxFirstByteRetriesPerRequest"
                :min="schedulingPreferenceSpeedFirstBounds.maxFirstByteRetriesPerRequest.min"
                :max="schedulingPreferenceSpeedFirstBounds.maxFirstByteRetriesPerRequest.max"
                addon-after="次"
              />
            </a-form-item>
            <a-form-item label="请求总时间截止" required tooltip="总耗时超过该值的请求计为总时间慢样本；估算输入 ≥10 万 token 的大请求自动按压缩档兜底。默认 120 秒，范围 60-270 秒。">
              <a-input-number
                v-model:value="form.speedFirst.totalTimeDeadlineSeconds"
                :min="schedulingPreferenceSpeedFirstBounds.totalTimeDeadlineSeconds.min"
                :max="schedulingPreferenceSpeedFirstBounds.totalTimeDeadlineSeconds.max"
                addon-after="秒"
              />
            </a-form-item>
            <a-form-item label="压缩总时间截止" required tooltip="压缩与大输入请求的总时长兜底阈值。默认 300 秒，范围 300-900 秒。">
              <a-input-number
                v-model:value="form.speedFirst.compactionTotalTimeDeadlineSeconds"
                :min="schedulingPreferenceSpeedFirstBounds.compactionTotalTimeDeadlineSeconds.min"
                :max="schedulingPreferenceSpeedFirstBounds.compactionTotalTimeDeadlineSeconds.max"
                addon-after="秒"
              />
            </a-form-item>
          </div>
        </template>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { InfoCircleOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { TablePaginationConfig } from 'ant-design-vue'

import type { SchedulingPreferenceCreatePayload } from '@/api/domains/schedulingPreferences'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { useScopedSchedulingPreferencesApi } from '@/composables/useScopedDomainApi'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import {
  buildSchedulingPreferenceFormPayload,
  buildSchedulingPreferenceMutationPatch,
  defaultSpeedFirstParamsForm,
  extractSchedulingPreferenceReferenceCount,
  hasSchedulingPreferenceMutationChanges,
  mergeSchedulingPreferenceMutationResult,
  schedulingPreferenceSpeedFirstBounds,
  speedFirstParamsFormFromConfig,
  type SchedulingPreferenceFormPayload,
  type SpeedFirstParamsForm
} from './schedulingPreferenceForm'
import type {
  SchedulingPreferenceChatMode,
  SchedulingPreferenceMutationResult,
  SchedulingPreferenceSummary
} from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

interface SchedulingPreferencesPageState {
  keyword: string
  chatModeFilter: SchedulingPreferenceChatMode | 'all'
  pagination: {
    current: number
    pageSize: number
  }
  statusFilter: 'enabled' | 'disabled' | 'all'
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

const schedulingPreferencesPageSize = 50
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const schedulingPreferencesApi = useScopedSchedulingPreferencesApi(isManagementView)
const pageStateCache = usePageStateCache<SchedulingPreferencesPageState>(undefined, defaultSchedulingPreferencesPageState, {
  sanitize: sanitizeSchedulingPreferencesPageState,
  version: 1
})
const initialPageState = pageStateCache.read()
const keyword = ref(initialPageState.keyword)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const chatModeFilter = ref<SchedulingPreferenceChatMode | 'all'>(initialPageState.chatModeFilter)
const statusFilter = ref<'enabled' | 'disabled' | 'all'>(initialPageState.statusFilter)
const loading = ref(false)
const saving = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const editingBuiltIn = ref(false)
const editingSystemAccountId = ref<string>()
const editingExpectedUpdatedAt = ref<string>()
const editingBoundApiKeyCount = ref(0)
let editingBaseline: SchedulingPreferenceFormPayload | undefined
let listRequestGeneration = 0
const items = ref<SchedulingPreferenceSummary[]>([])
const total = ref(0)
const page = ref(initialPageState.pagination.current)
const pageSize = ref(initialPageState.pagination.pageSize)

const scopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})

const {
  handleDropdown: handleSystemAccountOptionsDropdown,
  handleSearch: handleSystemAccountOptionsSearch,
  loading: systemAccountOptionsLoading,
  resetSearch: resetSystemAccountOptionsSearch,
  systemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  onMissingSelectedIds: handleMissingSystemAccountFilter,
  selectedIds: () => [systemAccountFilter.value]
})

const form = reactive({
  name: '',
  chatMode: 'cost_first' as SchedulingPreferenceChatMode,
  enabled: true,
  speedFirst: defaultSpeedFirstParamsForm() as SpeedFirstParamsForm
})

const chatModeOptions: Array<{ label: string; value: SchedulingPreferenceChatMode }> = [
  { label: '成本优先', value: 'cost_first' },
  { label: '速度优先', value: 'speed_first' }
]

const enabledOptions: Array<{ label: string; value: boolean }> = [
  { label: '启用', value: true },
  { label: '停用', value: false }
]

const chatModeFilterOptions = [
  { label: '全部形态', value: 'all' },
  ...chatModeOptions
]

const statusFilterOptions = [
  { label: '全部状态', value: 'all' },
  { label: '启用', value: 'enabled' },
  { label: '停用', value: 'disabled' }
]

const columns = computed<Array<Record<string, unknown>>>(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', key: 'name', width: 240 },
    { title: '对话形态', key: 'chatMode', width: 130 },
    { title: '内置', key: 'builtIn', width: 90 },
    { title: '状态', key: 'status', width: 100 },
    { title: '绑定 API Key', key: 'boundApiKeyCount', width: 130 },
    { title: '更新时间', dataIndex: 'updatedAt', key: 'updatedAt', width: 180 },
    { title: '操作', key: 'actions', width: 104, fixed: 'right' }
  ]
  return isManagementView.value
    ? [{ title: '系统账户', key: 'systemAccount', width: 180 }, ...baseColumns]
    : baseColumns
})

const activeFilterCount = computed(() => [
  keyword.value.trim(),
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue,
  chatModeFilter.value !== 'all',
  statusFilter.value !== 'all'
].filter(Boolean).length)

const pagination = computed<TablePaginationConfig>(() => ({
  current: page.value,
  pageSize: pageSize.value,
  total: total.value,
  showSizeChanger: true,
  showTotal: (value) => `共 ${formatNumber(value)} 条`
}))

const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = scopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (systemAccountFilterSelection.value?.kind === 'system_account' && systemAccountFilterSelection.value.id === systemAccountId) {
    return systemAccountFilterSelection.value.name
  }
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName
    || principalLabelForId('system_account', systemAccountId)
    || ''
})

watch(modalOpen, (open) => {
  if (open) return
  editingBaseline = undefined
})
watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onMounted(() => {
  void loadSchedulingPreferences()
})

onBeforeUnmount(() => {
  listRequestGeneration += 1
})

async function loadSchedulingPreferences(): Promise<boolean> {
  const requestGeneration = ++listRequestGeneration
  const requestScopeParams = scopeParams.value
  const scopeKey = listScopeKey()
  const listParams = {
    page: page.value,
    pageSize: pageSize.value,
    keyword: keyword.value.trim() || undefined,
    chatMode: chatModeFilter.value,
    status: statusFilter.value,
    systemAccountId: requestScopeParams?.systemAccountId
  }
  loading.value = true
  try {
    const result = await schedulingPreferencesApi.list(listParams)
    if (requestGeneration !== listRequestGeneration || scopeKey !== listScopeKey()) return false
    if (result.page > 1 && result.items.length === 0 && result.hasMore === false) {
      page.value -= 1
      return await loadSchedulingPreferences()
    }
    items.value = result.items
    total.value = result.total
    return true
  } catch (error) {
    if (requestGeneration === listRequestGeneration) {
      message.error(extractApiErrorMessage(error, '调度偏好加载失败'))
    }
    return false
  } finally {
    if (requestGeneration === listRequestGeneration) loading.value = false
  }
}

function listScopeKey(): string {
  return isManagementView.value
    ? `management:${scopeParams.value?.systemAccountId ?? '*'}`
    : 'self'
}

function handleTableChange(...args: unknown[]) {
  const nextPagination = args[0] as TablePaginationConfig
  page.value = nextPagination.current ?? 1
  pageSize.value = nextPagination.pageSize ?? schedulingPreferencesPageSize
  void loadSchedulingPreferences()
}

function applyFilters() {
  page.value = 1
  void loadSchedulingPreferences()
}

function refreshSchedulingPreferences() {
  resetSystemAccountOptionsSearch()
  void loadSchedulingPreferences()
}

function resetFilters() {
  const defaults = defaultSchedulingPreferencesPageState()
  keyword.value = defaults.keyword
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountFilterSelection.value = defaults.systemAccountFilterSelection
  chatModeFilter.value = defaults.chatModeFilter
  statusFilter.value = defaults.statusFilter
  resetSchedulingPreferenceListForScopeChange()
  resetSystemAccountOptionsSearch()
  page.value = defaults.pagination.current
  pageSize.value = defaults.pagination.pageSize
  pageStateCache.clear()
  void loadSchedulingPreferences()
}

function handleSystemAccountFilterChange() {
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetSchedulingPreferenceListForScopeChange()
  resetSystemAccountOptionsSearch()
  page.value = 1
  void loadSchedulingPreferences()
}

function handleMissingSystemAccountFilter(ids: string[]): void {
  if (systemAccountFilter.value === allSystemAccountsValue || !ids.includes(systemAccountFilter.value)) return
  systemAccountFilter.value = allSystemAccountsValue
  systemAccountFilterSelection.value = undefined
  resetSchedulingPreferenceListForScopeChange()
  page.value = 1
  void loadSchedulingPreferences()
}

function resetSchedulingPreferenceListForScopeChange(): void {
  listRequestGeneration += 1
  loading.value = false
  items.value = []
  total.value = 0
}

function defaultSchedulingPreferencesPageState(): SchedulingPreferencesPageState {
  return {
    keyword: '',
    chatModeFilter: 'all',
    pagination: { current: 1, pageSize: schedulingPreferencesPageSize },
    statusFilter: 'all',
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined
  }
}

function sanitizeSchedulingPreferencesPageState(value: unknown, fallback: SchedulingPreferencesPageState): SchedulingPreferencesPageState {
  if (!value || typeof value !== 'object') return fallback
  const source = value as Partial<SchedulingPreferencesPageState>
  const pagination = source.pagination && typeof source.pagination === 'object'
    ? source.pagination as Partial<SchedulingPreferencesPageState['pagination']>
    : {}
  return {
    keyword: typeof source.keyword === 'string' ? source.keyword : fallback.keyword,
    chatModeFilter: isChatModeFilter(source.chatModeFilter) ? source.chatModeFilter : fallback.chatModeFilter,
    pagination: {
      current: sanitizePositiveInteger(pagination.current, fallback.pagination.current),
      pageSize: sanitizePositiveInteger(pagination.pageSize, fallback.pagination.pageSize, 200)
    },
    statusFilter: isStatusFilter(source.statusFilter) ? source.statusFilter : fallback.statusFilter,
    systemAccountFilter: typeof source.systemAccountFilter === 'string' && source.systemAccountFilter.trim()
      ? source.systemAccountFilter
      : fallback.systemAccountFilter,
    systemAccountFilterSelection: sanitizeSystemAccountSelection(source.systemAccountFilterSelection)
  }
}

function isChatModeFilter(value: unknown): value is SchedulingPreferenceChatMode | 'all' {
  return value === 'all' || value === 'cost_first' || value === 'speed_first'
}

function isStatusFilter(value: unknown): value is 'enabled' | 'disabled' | 'all' {
  return value === 'all' || value === 'enabled' || value === 'disabled'
}

function sanitizePositiveInteger(value: unknown, fallback: number, max = Number.MAX_SAFE_INTEGER): number {
  const numeric = Number(value)
  return Number.isInteger(numeric) && numeric > 0 && numeric <= max ? numeric : fallback
}

function sanitizeSystemAccountSelection(value: unknown): PrincipalSelection | undefined {
  if (!value || typeof value !== 'object') return undefined
  const selection = value as Partial<PrincipalSelection>
  const id = typeof selection.id === 'string' ? selection.id.trim() : ''
  const name = typeof selection.name === 'string' ? selection.name.trim() : ''
  if (!id || !name || selection.kind !== 'system_account') return undefined
  return { id, name, kind: 'system_account' }
}

function snapshotPageState(): SchedulingPreferencesPageState {
  return {
    keyword: keyword.value,
    chatModeFilter: chatModeFilter.value,
    pagination: {
      current: page.value,
      pageSize: pageSize.value
    },
    statusFilter: statusFilter.value,
    systemAccountFilter: systemAccountFilter.value,
    systemAccountFilterSelection: systemAccountFilterSelection.value
  }
}

function openCreate() {
  if (isManagementView.value && !scopeParams.value?.systemAccountId) {
    message.warning('请先选择目标系统账户，再创建调度偏好')
    return
  }
  editingId.value = undefined
  editingBuiltIn.value = false
  editingSystemAccountId.value = undefined
  editingExpectedUpdatedAt.value = undefined
  editingBoundApiKeyCount.value = 0
  editingBaseline = undefined
  form.name = ''
  form.chatMode = 'cost_first'
  form.enabled = true
  form.speedFirst = defaultSpeedFirstParamsForm()
  modalOpen.value = true
}

function openEdit(record: SchedulingPreferenceSummary) {
  if (record.builtIn) {
    message.warning('系统内置调度偏好不可修改或删除')
    return
  }
  if (isManagementView.value && !record.systemAccountId?.trim()) {
    message.warning('无法确定调度偏好归属系统账户，请刷新后重试')
    return
  }
  editingId.value = record.id
  editingBuiltIn.value = record.builtIn
  editingSystemAccountId.value = record.systemAccountId
  editingExpectedUpdatedAt.value = record.updatedAt
  editingBoundApiKeyCount.value = record.boundApiKeyCount
  form.name = record.name
  form.chatMode = record.chatMode
  form.enabled = record.enabled
  form.speedFirst = record.chatMode === 'speed_first'
    ? speedFirstParamsFormFromConfig(record.chatSpeedFirstConfig)
    : defaultSpeedFirstParamsForm()
  editingBaseline = buildSchedulingPreferenceFormPayload(form)
  modalOpen.value = true
}

async function saveSchedulingPreference() {
  const name = form.name.trim()
  if (!name) {
    message.warning('请输入调度偏好名称')
    return
  }
  const payload = buildSchedulingPreferenceFormPayload(form)
  const operationScopeParams = schedulingPreferenceOperationScopeParams()
  if (isManagementView.value && !operationScopeParams?.systemAccountId) {
    message.warning('请先选择目标系统账户')
    return
  }
  saving.value = true
  try {
    if (editingId.value) {
      if (!editingBaseline) {
        message.error('调度偏好编辑基线缺失，请关闭弹窗后重试')
        return
      }
      if (!editingExpectedUpdatedAt.value) {
        message.error('调度偏好编辑版本缺失，请关闭弹窗后重试')
        return
      }
      const patch = buildSchedulingPreferenceMutationPatch(editingBaseline, payload)
      if (!hasSchedulingPreferenceMutationChanges(patch)) {
        message.info('没有需要保存的修改')
        return
      }
      const result = await schedulingPreferencesApi.update(editingId.value, {
        ...patch,
        expectedUpdatedAt: editingExpectedUpdatedAt.value
      }, operationScopeParams)
      applySchedulingPreferenceMutationResult(result)
      message.success('调度偏好已更新')
      modalOpen.value = false
    } else {
      const created = await schedulingPreferencesApi.create(
        buildSchedulingPreferenceCreatePayload(payload),
        operationScopeParams as { systemAccountId: string }
      )
      applyCreatedSchedulingPreferenceListItem(created, operationScopeParams?.systemAccountId)
      message.success('调度偏好已创建')
      modalOpen.value = false
    }
  } catch (error) {
    message.error(extractApiErrorMessage(error, '调度偏好保存失败'))
  } finally {
    saving.value = false
  }
}

function buildSchedulingPreferenceCreatePayload(payload: SchedulingPreferenceFormPayload): SchedulingPreferenceCreatePayload {
  const createPayload: SchedulingPreferenceCreatePayload = {
    name: payload.name,
    chatMode: payload.chatMode
  }
  if (payload.chatSpeedFirstConfig) createPayload.chatSpeedFirstConfig = payload.chatSpeedFirstConfig
  return createPayload
}

function applySchedulingPreferenceMutationResult(result: SchedulingPreferenceMutationResult): void {
  const index = items.value.findIndex((item) => item.id === result.id)
  if (index < 0) return
  const updated = mergeSchedulingPreferenceMutationResult(items.value[index]!, result)
  if (!matchesCurrentFilters(updated)) {
    items.value.splice(index, 1)
    total.value = Math.max(0, total.value - 1)
    return
  }
  items.value[index] = updated
}

function applyCreatedSchedulingPreferenceListItem(created: SchedulingPreferenceSummary, selectedSystemAccountId?: string): void {
  if (isManagementView.value && selectedSystemAccountId && created.systemAccountId && created.systemAccountId !== selectedSystemAccountId) return
  if (!matchesCurrentFilters(created)) return
  total.value += 1
  if (page.value !== 1) return
  items.value = [created, ...items.value].slice(0, pageSize.value)
}

function matchesCurrentFilters(record: SchedulingPreferenceSummary): boolean {
  const namePrefix = keyword.value.trim()
  return (!namePrefix || record.name.startsWith(namePrefix))
    && (chatModeFilter.value === 'all' || record.chatMode === chatModeFilter.value)
    && (statusFilter.value === 'all'
      || (statusFilter.value === 'enabled' ? record.enabled : !record.enabled))
}

async function deleteSchedulingPreference(record: SchedulingPreferenceSummary) {
  const operationScopeParams = schedulingPreferenceOperationScopeParams(record)
  if (isManagementView.value && !operationScopeParams?.systemAccountId) {
    message.warning('无法确定调度偏好归属系统账户，请刷新后重试')
    return
  }
  try {
    await schedulingPreferencesApi.delete(record.id, operationScopeParams)
    const index = items.value.findIndex((item) => item.id === record.id)
    if (index >= 0) {
      items.value.splice(index, 1)
      total.value = Math.max(0, total.value - 1)
    }
    message.success('调度偏好已删除')
  } catch (error) {
    const referenceCount = extractSchedulingPreferenceReferenceCount(error)
    if (referenceCount !== undefined) {
      message.warning(`调度偏好仍被 ${referenceCount} 个 API Key 绑定，请先调整绑定后再删除`)
      return
    }
    message.error(extractApiErrorMessage(error, '调度偏好删除失败'))
  }
}

function schedulingPreferenceOperationScopeParams(record?: Pick<SchedulingPreferenceSummary, 'systemAccountId'>): { systemAccountId: string } | undefined {
  const systemAccountId = record?.systemAccountId?.trim()
    || editingSystemAccountId.value?.trim()
    || scopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

function schedulingPreferenceActions(record: SchedulingPreferenceSummary): RowActionItem[] {
  if (record.builtIn) return []
  const actions: RowActionItem[] = [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' }
  ]
  actions.push({
    key: 'delete',
    label: '删除',
    icon: 'delete',
    tone: 'danger',
    confirmTitle: record.boundApiKeyCount > 0
      ? `确认删除调度偏好「${record.name}」？该偏好正被 ${record.boundApiKeyCount} 个 API Key 绑定，需先调整绑定。`
      : `确认删除调度偏好「${record.name}」？`,
    confirmOkText: '删除'
  })
  return actions
}

function handleSchedulingPreferenceAction(key: string, record: SchedulingPreferenceSummary) {
  if (key === 'edit') {
    openEdit(record)
    return
  }
  if (key === 'delete') {
    void deleteSchedulingPreference(record)
  }
}

function schedulingPreferenceSystemAccountText(record: SchedulingPreferenceSummary): string {
  return record.systemAccountName || record.systemAccountId || '-'
}

function schedulingPreferenceChatModeText(chatMode: SchedulingPreferenceChatMode): string {
  return chatMode === 'speed_first' ? '速度优先' : '成本优先'
}

function schedulingPreferenceChatModeColor(chatMode: SchedulingPreferenceChatMode): string {
  return chatMode === 'speed_first' ? 'blue' : 'green'
}

function schedulingPreferenceStatusText(enabled: boolean): string {
  return enabled ? '启用' : '停用'
}

function schedulingPreferenceStatusColor(enabled: boolean): string {
  return enabled ? 'green' : 'default'
}
</script>

<style scoped>
.scheduling-preferences-page-card {
  border: 1px solid var(--juhe-border);
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(34, 40, 43, 0.04);
}

.modal-alert {
  margin-bottom: 12px;
}

.toolbar-select {
  min-width: 180px;
}

.mobile-filter-field {
  display: grid;
  gap: 8px;
  color: var(--juhe-fg-soft);
  font-size: 13px;
  font-weight: 600;
}

:deep(.scheduling-preference-table .ant-table-cell) {
  white-space: nowrap;
}

:deep(.scheduling-preference-table .ant-empty) {
  margin: 12px 0;
}

.scheduling-preference-name-text {
  overflow: hidden;
  color: var(--juhe-fg);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mobile-list-card-title,
.mobile-list-card-name-row {
  font-weight: 400;
}

.scheduling-preference-modal-form {
  max-height: 72vh;
  overflow-x: hidden;
  overflow-y: auto;
  padding-right: 4px;
}

.modal-section-title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 18px 0 10px;
  color: var(--juhe-fg);
  font-size: 14px;
  font-weight: 700;
}

.scheduling-preference-field-help-icon {
  flex: none;
  color: var(--juhe-faint);
  cursor: help;
  font-size: 14px;
}

.scheduling-preference-field-help-icon:hover {
  color: var(--juhe-accent);
}

.scheduling-config-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 12px;
}

.scheduling-config-grid :deep(.ant-input-number),
.scheduling-config-grid :deep(.ant-segmented) {
  width: 100%;
}

@media (max-width: 720px) {
  .scheduling-config-grid {
    grid-template-columns: 1fr;
  }
}
</style>
