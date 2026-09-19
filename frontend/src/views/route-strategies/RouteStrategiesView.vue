<template>
  <a-card class="page-card route-strategies-page-card responsive-page-card">
    <ResponsiveListToolbar
      v-model:keyword="keyword"
      search-placeholder="搜索策略路由"
      filter-title="筛选策略路由"
      :active-filter-count="activeFilterCount"
      :advanced-filter-count="0"
      :refresh-loading="loading"
      @reset="resetFilters"
      @refresh="refreshRouteStrategies"
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
          v-model:value="statusFilter"
          class="toolbar-select responsive-list-inline-filter"
          :options="statusFilterOptions"
          @change="applyFilters"
        />
        <a-select
          v-model:value="modeFilter"
          class="toolbar-select responsive-list-inline-filter"
          :options="modeFilterOptions"
          @change="applyFilters"
        />
      </template>
      <template #actions>
        <a-button type="primary" @click="openCreate">
          <template #icon><PlusOutlined /></template>
          新建策略路由
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
          <span>状态</span>
          <a-select v-model:value="statusFilter" :options="statusFilterOptions" />
        </label>
        <label class="mobile-filter-field">
          <span>路由模式</span>
          <a-select v-model:value="modeFilter" :options="modeFilterOptions" />
        </label>
      </template>
    </ResponsiveListToolbar>

    <ResponsiveDataList
      table-class="page-table route-strategy-table"
      :columns="columns"
      :data-source="items"
      row-key="id"
      :loading="loading"
      :pagination="pagination"
      :scroll-x="isManagementView ? 1260 : 1080"
      mobile-pagination
      pull-refresh-enabled
      :refreshing="loading"
      @change="handleTableChange"
      @mobile-refresh="loadRouteStrategies"
    >
      <template #emptyText>
        <a-empty class="page-empty-card" description="暂无策略路由。创建后可在 API Key 中绑定。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'name'">
          <div class="route-strategy-name-cell">
            <div class="route-strategy-name-line">
              <span class="route-strategy-name-text">{{ record.name }}</span>
              <a-tag v-if="record.isDefault" color="gold">默认</a-tag>
            </div>
            <span v-if="record.description" class="route-strategy-description">{{ record.description }}</span>
          </div>
        </template>
        <template v-else-if="column.key === 'systemAccount'">
          <span :class="record.systemAccountName ? 'name-cell' : 'muted-cell'">{{ routeStrategySystemAccountText(record) }}</span>
        </template>
        <template v-else-if="column.key === 'mode'">
          <div class="route-strategy-mode-cell">
            <a-tag :color="routeStrategyModeColor(record.mode)">{{ routeStrategyModeDisplayText(record) }}</a-tag>
            <span v-if="speedFirstLatencyStatusText(record)" class="speed-first-latency-status" :class="speedFirstLatencyStatusClass(record)">
              {{ speedFirstLatencyStatusText(record) }}
            </span>
            <a-button
              v-if="isSpeedFirstRouteStrategy(record)"
              type="link"
              size="small"
              class="speed-first-runtime-link"
              @click="openSpeedFirstRuntime(record)"
            >
              查看速度状态
            </a-button>
          </div>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag :color="routeStrategyStatusColor(record.status)">{{ routeStrategyStatusText(record.status) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'groups'">
          <div class="route-strategy-groups">
            <a-tag
              v-for="binding in visibleGroupBindings(record)"
              :key="binding.id"
              :color="routeStrategyGroupTagColor(binding)"
            >
              {{ routeStrategyGroupLabel(binding) }}
            </a-tag>
            <a-tag v-if="hiddenGroupBindingCount(record) > 0" color="default">+{{ hiddenGroupBindingCount(record) }}</a-tag>
            <span v-if="!routeStrategyBindingCount(record)" class="muted-cell">未绑定</span>
          </div>
        </template>
        <template v-else-if="column.key === 'apiKeyCount'">
          <a-tag>{{ formatNumber(routeStrategyApiKeyCount(record)) }}</a-tag>
        </template>
        <template v-else-if="column.key === 'updatedAt'">
          <span class="muted-cell">{{ formatDateTime(record.updatedAt) }}</span>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="routeStrategyActions(record)" @action-click="handleRouteStrategyAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">
              <div class="mobile-list-card-name-row">
                <span>{{ record.name }}</span>
                <a-tag v-if="record.isDefault" color="gold">默认</a-tag>
              </div>
            </div>
            <div class="mobile-list-card-tags">
              <a-tag :color="routeStrategyModeColor(record.mode)">{{ routeStrategyModeDisplayText(record) }}</a-tag>
              <a-tag :color="routeStrategyStatusColor(record.status)">{{ routeStrategyStatusText(record.status) }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>系统账户</span>
              <strong>{{ routeStrategySystemAccountText(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>分组</span>
              <strong>{{ routeStrategyGroupSummary(record) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>API Key</span>
              <strong>{{ formatNumber(routeStrategyApiKeyCount(record)) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>更新时间</span>
              <strong>{{ formatDateTime(record.updatedAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
            <div v-if="isSpeedFirstRouteStrategy(record)" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>速度状态</span>
              <strong :class="speedFirstLatencyStatusClass(record)">{{ speedFirstLatencyStatusText(record) }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <a-button
              v-if="isSpeedFirstRouteStrategy(record)"
              size="small"
              class="speed-first-runtime-mobile-link"
              @click="openSpeedFirstRuntime(record)"
            >
              查看速度状态
            </a-button>
            <RowActions
              variant="button"
              :actions="routeStrategyActions(record)"
              @action-click="handleRouteStrategyAction($event, record)"
            />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modalOpen" :title="editingId ? '编辑策略路由' : '新建策略路由'" width="760px" :confirm-loading="saving" destroy-on-close @ok="saveRouteStrategy">
      <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
      <a-form layout="vertical" class="route-strategy-modal-form">
        <a-form-item label="名称" required :tooltip="editingIsDefault ? '默认策略路由的名称不可修改。' : '用于在 API Key 中识别这套路由策略，建议写清楚业务场景或使用方。'">
          <a-input v-model:value="form.name" :disabled="editingIsDefault" placeholder="请输入策略路由名称" />
        </a-form-item>
        <a-form-item label="说明" tooltip="补充这套路由策略的用途、适用范围或注意事项，只用于后台识别。">
          <a-textarea v-model:value="form.description" :rows="2" placeholder="可选" />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="路由模式" required tooltip="决定请求如何在绑定分组之间选择：普通、权重、故障回退、轮询或合并。">
              <a-select v-model:value="form.mode" :options="modeOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="状态" required tooltip="停用后，这套路由策略不会再参与 API Key 请求调度。">
              <a-select v-model:value="form.status" :options="statusOptions" />
            </a-form-item>
          </a-col>
        </a-row>

        <template v-if="schedulingPreferenceSupported">
          <div class="modal-section-title">
            <span>调度偏好</span>
            <a-tooltip :title="normalRoutingConfigTooltip">
              <InfoCircleOutlined class="route-strategy-field-help-icon" />
            </a-tooltip>
          </div>
          <div class="scheduling-config-grid">
            <a-form-item label="调度偏好" tooltip="成本优先保持当前账号缓存和会话粘黏；速度优先先观察首字慢样本，确认账号近期变慢后再优先切换到更快账号。作用域为当前请求所落分组内的账号调度。">
              <a-segmented v-model:value="form.normal.schedulingPreference" block :options="normalSchedulingPreferenceOptions" />
            </a-form-item>
            <a-form-item v-if="form.normal.schedulingPreference === 'speed_first'" label="首字截止" required tooltip="只作用于速度优先的可重放文本；图像和其他副作用请求永久排除，不记录慢样本也不自动切号。">
              <a-input-number v-model:value="form.normal.firstByteDeadlineSeconds" :min="10" :max="60" :precision="0" addon-after="秒" />
            </a-form-item>
          </div>
          <div v-if="form.normal.schedulingPreference === 'speed_first'" class="scheduling-config-grid">
            <a-form-item label="慢速触发次数" required tooltip="窗口期内达到这个慢速次数后，账号进入速度降级。">
              <a-input-number v-model:value="form.normal.speedFirstConfig.slowTriggerCount" :min="2" :max="10" addon-after="次" />
            </a-form-item>
            <a-form-item label="慢速窗口期" required tooltip="统计连续慢速触发的时间窗口。">
              <a-input-number v-model:value="form.normal.speedFirstConfig.slowWindowSeconds" :min="60" :max="600" addon-after="秒" />
            </a-form-item>
            <a-form-item label="恢复成功次数" required tooltip="后台探针连续满足首字阈值达到该次数后，账号恢复正常调度。">
              <a-input-number v-model:value="form.normal.speedFirstConfig.recoverySuccessCount" :min="3" :max="10" addon-after="次" />
            </a-form-item>
            <a-form-item label="探针间隔" required tooltip="账号速度降级后，后台探针的最小执行间隔。">
              <a-input-number v-model:value="form.normal.speedFirstConfig.probeIntervalSeconds" :min="10" :max="300" addon-after="秒" />
            </a-form-item>
            <a-form-item label="降级租约时间" required tooltip="确认慢或探针失败后刷新这段租约；持续慢会继续后置，连续达标后恢复。">
              <a-input-number v-model:value="form.normal.speedFirstConfig.degradedTtlSeconds" :min="60" :max="3600" addon-after="秒" />
            </a-form-item>
            <a-form-item label="单请求切号次数" required tooltip="同一个请求在账号已确认首字慢后，最多隐藏切换账号的次数。">
              <a-input-number
                v-model:value="form.normal.speedFirstConfig.maxFirstByteRetriesPerRequest"
                :min="1"
                :max="3"
                addon-after="次"
              />
            </a-form-item>
          </div>
        </template>

        <div class="modal-section-title">
          <span>分组绑定</span>
          <a-tooltip :title="bindingSectionTooltip">
            <InfoCircleOutlined class="route-strategy-field-help-icon" />
          </a-tooltip>
        </div>
        <div class="route-strategy-binding-list">
          <div class="route-strategy-binding-header" :style="bindingGridStyle">
            <span v-for="column in bindingColumns" :key="column.key" class="route-strategy-binding-header-cell">
              <span>{{ column.label }}</span>
              <a-tooltip v-if="column.tooltip" :title="column.tooltip">
                <InfoCircleOutlined class="route-strategy-field-help-icon" />
              </a-tooltip>
            </span>
          </div>
          <div
            v-for="(binding, index) in form.groupBindings"
            :key="binding.key"
            class="route-strategy-binding-row"
            :class="{
              'is-dragging': bindingDragSourceIndex === index,
              'is-drag-over': bindingDragOverIndex === index
            }"
            :style="bindingGridStyle"
            @dragenter.prevent="handleBindingDragEnter(index)"
            @dragover="handleBindingDragOver(index, $event)"
            @drop="handleBindingDrop(index, $event)"
          >
            <div v-if="bindingShowsDragColumn" class="route-strategy-binding-drag-cell">
              <button
                v-if="bindingRowDragEnabled(index)"
                type="button"
                class="route-strategy-binding-drag-handle"
                :draggable="bindingRowDragEnabled(index)"
                :aria-label="`拖动调整第 ${index + 1} 个分组顺序`"
                @dragstart="handleBindingDragStart(index, $event)"
                @dragend="handleBindingDragEnd"
                @keydown.up.prevent="moveBindingForMode(index, index - 1)"
                @keydown.down.prevent="moveBindingForMode(index, index + 1)"
              >
                <HolderOutlined />
              </button>
              <span v-else class="route-strategy-binding-drag-placeholder"></span>
            </div>
            <GroupSelect
              v-model:value="binding.groupId"
              v-model:selected-group="binding.group"
              :filter-option="false"
              :loading="groupOptionsLoading"
              :options="groupOptions"
              :placeholder="bindingGroupPlaceholder(index)"
              @dropdown-visible-change="handleGroupOptionsDropdown"
              @search="handleGroupOptionsSearch"
            />
            <div v-if="bindingShowsRole" class="route-strategy-binding-role">
              <a-tag :color="bindingRoleColor(index)">{{ bindingRoleText(index) }}</a-tag>
            </div>
            <a-input-number
              v-if="bindingShowsWeight"
              v-model:value="binding.weight"
              :min="1"
              :max="bindingWeightMax(index)"
              placeholder="权重"
              @change="handleBindingWeightChange(index)"
            />
            <a-select v-model:value="binding.status" :options="statusOptions" />
            <a-button type="text" danger :disabled="bindingRemoveDisabled" @click="removeBinding(index)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </div>
        </div>
        <a-button type="dashed" block :disabled="bindingAddDisabled" @click="addBinding">
          <template #icon><PlusOutlined /></template>
          {{ bindingAddButtonText }}
        </a-button>
      </a-form>
    </a-modal>

    <SpeedFirstRuntimeDrawer
      v-model:open="speedFirstRuntimeDrawerOpen"
      :strategy-name="speedFirstRuntimeTarget?.name || '策略路由'"
      :loading="speedFirstRuntimeLoading"
      :runtime="speedFirstRuntime"
      :error="speedFirstRuntimeError"
      @refresh="refreshSpeedFirstRuntime"
    />
  </a-card>
</template>

<script setup lang="ts">
import { DeleteOutlined, HolderOutlined, InfoCircleOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import type { TablePaginationConfig } from 'ant-design-vue'

import type { RouteStrategyMutationPayload } from '@/api/domains/routeStrategies'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import GroupSelect from '@/components/GroupSelect.vue'
import type { RowActionItem } from '@/components/rowActions'
import SystemPrincipalSelect from '@/components/SystemPrincipalSelect.vue'
import { loadGroupOptionsResource } from '@/composables/useGroupOptionsResource'
import {
  useProviderModelSelectOptions,
  type ProviderModelSelectOption
} from '@/composables/useProviderModelSelectOptions'
import { usePageStateCache } from '@/composables/usePageStateCache'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { useScopedGroupsApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { invalidateUserReferenceData } from '@/composables/useUserReferenceData'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import type { GroupSelection } from '@/shared/groupLabelCache'
import { principalLabelForId, rememberPrincipalSelection, type PrincipalSelection } from '@/shared/principalLabelCache'
import SpeedFirstRuntimeDrawer from './SpeedFirstRuntimeDrawer.vue'
import { buildRouteStrategyMutationPatch, hasRouteStrategyMutationChanges, mergeRouteStrategyMutationResult } from './routeStrategyMutation'
import type {
  RouteStrategyGroupOption,
  RouteStrategyEditBasicDetail,
  RouteStrategyNormalRoutingConfig,
  RouteStrategyNormalSchedulingPreference,
  RouteStrategySpeedFirstConfig,
  RouteStrategySpeedFirstLatencyRuntime,
  RouteStrategyGroupBindingPreview,
  RouteStrategyGroupBindingSummary,
  RouteStrategyListItem,
  RouteStrategyMutationResult,
  RouteStrategyMode,
  RouteStrategyStatus,
  RouteStrategySummary
} from '@/types/domain'
import { allSystemAccountsValue } from '@/utils/systemAccountFilter'

interface BindingFormRow {
  key: string
  groupId: string
  group?: GroupSelection
  priority: number
  weight: number
  status: 'active' | 'disabled'
}

interface BindingColumn {
  key: string
  label: string
  tooltip?: string
}

interface RouteStrategiesPageState {
  keyword: string
  modeFilter: RouteStrategyMode | 'all'
  pagination: {
    current: number
    pageSize: number
  }
  statusFilter: RouteStrategyStatus | 'all'
  systemAccountFilter: string
  systemAccountFilterSelection?: PrincipalSelection
}

interface NormalRoutingForm {
  schedulingPreference: RouteStrategyNormalSchedulingPreference
  firstByteDeadlineSeconds: number
  speedFirstConfig: SpeedFirstConfigForm
}

interface SpeedFirstConfigForm extends RouteStrategySpeedFirstConfig {}

const routeStrategiesPageSize = 20
const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const routeStrategiesApi = useScopedRouteStrategiesApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const pageStateCache = usePageStateCache<RouteStrategiesPageState>(undefined, defaultRouteStrategiesPageState, {
  sanitize: sanitizeRouteStrategiesPageState,
  version: 2
})
const initialPageState = pageStateCache.read()
const keyword = ref(initialPageState.keyword)
const systemAccountFilter = ref(initialPageState.systemAccountFilter)
const systemAccountFilterSelection = ref<PrincipalSelection | undefined>(initialPageState.systemAccountFilterSelection)
const statusFilter = ref<RouteStrategyStatus | 'all'>(initialPageState.statusFilter)
const modeFilter = ref<RouteStrategyMode | 'all'>(initialPageState.modeFilter)
const loading = ref(false)
const saving = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const editingIsDefault = ref(false)
const editingSystemAccountId = ref<string>()
const editingExpectedUpdatedAt = ref<string>()
let editingBaseline: RouteStrategyMutationPayload | undefined
let editDetailRequestToken = 0
const speedFirstRuntimeDrawerOpen = ref(false)
const speedFirstRuntimeLoading = ref(false)
const speedFirstRuntimeTarget = ref<RouteStrategyListItem>()
const speedFirstRuntime = ref<RouteStrategySpeedFirstLatencyRuntime>()
const speedFirstRuntimeError = ref('')
let speedFirstRuntimeRequestToken = 0
let speedFirstRuntimeAbortController: AbortController | undefined
const bindingDragSourceIndex = ref<number | null>(null)
const bindingDragOverIndex = ref<number | null>(null)
const items = ref<RouteStrategyListItem[]>([])
const total = ref(0)
const page = ref(initialPageState.pagination.current)
const pageSize = ref(initialPageState.pagination.pageSize)
const groupOptionsRaw = ref<RouteStrategyGroupOption[]>([])
const groupOptionsLoading = ref(false)
const groupOptionsLoaded = ref(false)
let groupOptionsRequestToken = 0
let groupOptionsLoadingKey: string | undefined
let groupOptionsLoadingPromise: Promise<void> | undefined
let groupOptionsSearchTimer: number | undefined
let modelOptionsSearchTimer: number | undefined

const routeStrategyScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId(systemAccountFilter.value)
  return systemAccountId ? { systemAccountId } : undefined
})
let routeStrategyListRequestGeneration = 0
const modelOptionsScopeParams = computed(() => routeStrategyOperationScopeParams())
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
  description: '',
  mode: 'normal' as RouteStrategyMode,
  status: 'active' as RouteStrategyStatus,
  groupBindings: [] as BindingFormRow[],
  normal: defaultNormalRoutingForm()
})
const modelProviderCodes = computed(() => {
  const selectedGroupIds = new Set(form.groupBindings.map((binding) => binding.groupId).filter(Boolean))
  return [...new Set(groupOptionsRaw.value
    .filter((group) => selectedGroupIds.has(group.id))
    .map((group) => group.providerCode?.trim() ?? '')
    .filter(Boolean))]
})
// 混合智能路由移除后，表单内暂无可选模型；模型候选机制保留，按绑定分组供应商作用域按需加载。
const selectedModelIds = computed<string[]>(() => [])
const {
  loading: modelOptionsLoading,
  loadModelOptions,
  resetModelOptions,
  selectOptions: loadedModelSelectOptions
} = useProviderModelSelectOptions({
  scopeParams: modelOptionsScopeParams,
  providerCodes: modelProviderCodes,
  selectedIds: selectedModelIds,
  onLoadError: (error) => message.warning(extractApiErrorMessage(error, '模型选项加载失败'))
})
const modelSelectOptions = computed<ProviderModelSelectOption[]>(() => {
  const optionsByValue = new Map<string, ProviderModelSelectOption>()
  for (const value of selectedModelIds.value) {
    optionsByValue.set(value, {
      label: value,
      value,
      providerCodes: modelProviderCodes.value,
      supportedApiProtocols: []
    })
  }
  for (const option of loadedModelSelectOptions.value) optionsByValue.set(option.value, option)
  return [...optionsByValue.values()]
})

const modeOptions: Array<{ label: string; value: RouteStrategyMode }> = [
  { label: '普通路由', value: 'normal' },
  { label: '权重调度路由', value: 'weighted' },
  { label: '故障回退路由', value: 'failover' },
  { label: '轮询路由', value: 'round_robin' },
  { label: '合并路由', value: 'merge' }
]

const statusOptions: Array<{ label: string; value: RouteStrategyStatus }> = [
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' }
]

const statusFilterOptions = [
  { label: '全部状态', value: 'all' },
  ...statusOptions
]

const modeFilterOptions = [
  { label: '全部模式', value: 'all' },
  ...modeOptions
]

const normalSchedulingPreferenceOptions = [
  { label: '成本优先', value: 'cost_first' },
  { label: '速度优先', value: 'speed_first' }
]

// 调度偏好（历史命名 normalRoutingConfig）支持的路由模式。
const schedulingPreferenceSupportedModes: ReadonlyArray<RouteStrategyMode> = ['normal', 'weighted', 'failover', 'round_robin', 'merge']

const normalRoutingConfigTooltip = '首字软截止只作用于速度优先的可重放文本，并用于累计慢样本、临时降级和探针恢复。成本优先、图像及其他副作用请求不创建该截止。'

const columns = computed<Array<Record<string, unknown>>>(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', key: 'name', width: 260 },
    { title: '模式', key: 'mode', width: 150 },
    { title: '状态', key: 'status', width: 100 },
    { title: '绑定分组', key: 'groups', width: 320 },
    { title: 'API Key', key: 'apiKeyCount', width: 100 },
    { title: '更新时间', dataIndex: 'updatedAt', key: 'updatedAt', width: 180 },
    { title: '操作', key: 'actions', width: 96, fixed: 'right' }
  ]
  return isManagementView.value
    ? [{ title: '系统账户', key: 'systemAccount', width: 180 }, ...baseColumns]
    : baseColumns
})

const activeFilterCount = computed(() => [
  keyword.value.trim(),
  isManagementView.value && systemAccountFilter.value !== allSystemAccountsValue,
  statusFilter.value !== 'all',
  modeFilter.value !== 'all'
].filter(Boolean).length)

const weightedBindingTotal = computed(() => totalBindingWeight(form.groupBindings))
const bindingAddDisabled = computed(() => {
  if (form.mode === 'normal' && form.groupBindings.length >= 1) return true
  if (form.mode === 'weighted' && weightedBindingTotal.value >= 100) return true
  return false
})
const bindingRemoveDisabled = computed(() => form.groupBindings.length <= minimumBindingRowsForMode(form.mode))
const bindingShowsDragHandle = computed(() => form.mode === 'failover' || form.mode === 'round_robin' || form.mode === 'merge')
const bindingShowsDragColumn = computed(() => {
  if (!bindingShowsDragHandle.value) return false
  if (form.mode === 'round_robin' || form.mode === 'merge') return form.groupBindings.length > 1
  if (form.mode === 'failover') return form.groupBindings.length > 1
  return false
})
const bindingShowsRole = computed(() => form.mode === 'failover')
const bindingOrderUsesPosition = computed(() => form.mode === 'failover' || form.mode === 'round_robin' || form.mode === 'merge')
const bindingShowsWeight = computed(() => form.mode === 'weighted')
const schedulingPreferenceSupported = computed(() => schedulingPreferenceSupportedModes.includes(form.mode))
const bindingAddButtonText = computed(() => form.mode === 'failover' && form.groupBindings.length >= 1 ? '添加备用分组' : '添加分组')
const bindingSectionTooltip = computed(() => {
  if (form.mode === 'normal') return '普通路由只绑定一个分组，请求会直接进入这个分组的账号池。'
  if (form.mode === 'weighted') return '权重调度按分组权重比例分配流量，所有分组权重总和不能超过 100。'
  if (form.mode === 'failover') return '故障回退按当前顺序将第一行作为主用分组，后续为备用分组；所有行都可拖拽，备用拖到第一行即可晋升主用。主用恢复后继续优先使用主用。'
  if (form.mode === 'round_robin') return '轮询路由按当前分组顺序依次调度，可通过拖拽改变轮询顺序。'
  if (form.mode === 'merge') return '合并路由把所有绑定分组的账号合并成一个大池统一调度，不区分分组；拖拽顺序仅决定调度平局时的基础先后。'
  return ''
})
const bindingColumns = computed<BindingColumn[]>(() => [
  ...(bindingShowsDragColumn.value ? [{ key: 'drag', label: '' }] : []),
  { key: 'group', label: '分组', tooltip: '选择这套路由策略可以使用的账号分组，实际请求会进入分组内的 AI 账户池。' },
  ...(bindingShowsRole.value ? [{ key: 'role', label: '主备', tooltip: '第一行是主用分组，后续行为备用分组；所有行都可拖拽，备用分组拖到第一行即可晋升主用。' }] : []),
  ...(bindingShowsWeight.value ? [{ key: 'weight', label: '权重', tooltip: '权重越高命中比例越高；所有分组权重总和不能超过 100。' }] : []),
  { key: 'status', label: '状态', tooltip: '停用某一行后，这个分组不会参与当前策略调度。' },
  { key: 'actions', label: '' }
])
const bindingGridStyle = computed(() => {
  const tracks = ['minmax(0, 1fr)']
  if (bindingShowsDragColumn.value) tracks.unshift('32px')
  if (bindingShowsRole.value) tracks.push('minmax(64px, 76px)')
  if (bindingShowsWeight.value) tracks.push('minmax(76px, 92px)')
  tracks.push('minmax(88px, 96px)', '32px')
  return { gridTemplateColumns: tracks.join(' ') }
})

const pagination = computed<TablePaginationConfig>(() => ({
  current: page.value,
  pageSize: pageSize.value,
  total: total.value,
  showSizeChanger: true,
  showTotal: (value) => `共 ${formatNumber(value)} 条`
}))

const groupOptions = computed(() => groupOptionsRaw.value.map((group) => ({
  label: group.name,
  value: group.id,
  disabled: group.enabled === false
})))
const targetSystemAccountLabel = computed(() => {
  if (!isManagementView.value) return undefined
  const systemAccountId = routeStrategyScopeParams.value?.systemAccountId
  if (!systemAccountId) return '请选择系统账户后再创建'
  if (systemAccountFilterSelection.value?.kind === 'system_account' && systemAccountFilterSelection.value.id === systemAccountId) {
    return systemAccountFilterSelection.value.name
  }
  return systemAccounts.value.find((account) => account.id === systemAccountId)?.displayName
    || principalLabelForId('system_account', systemAccountId)
    || ''
})

watch(() => form.mode, (mode) => {
  clearModelOptionsSearchTimer()
  if (mode === 'normal' && form.groupBindings.length > 1) {
    form.groupBindings = [form.groupBindings[0] ?? createBindingRow()]
  }
  ensureMinimumBindingRowsForMode()
  normalizeBindingRowsForMode()
})
watch(() => modelProviderCodes.value.join('\u0000'), () => {
  clearModelOptionsSearchTimer()
  resetModelOptions()
})
watch(modalOpen, (open) => {
  if (open) return
  editingBaseline = undefined
  clearGroupOptionsSearchTimer()
  clearModelOptionsSearchTimer()
  resetModelOptions()
})
watch(speedFirstRuntimeDrawerOpen, (open) => {
  if (!open) invalidateSpeedFirstRuntimeRequest()
})
watch(snapshotPageState, () => pageStateCache.scheduleWrite(snapshotPageState), { deep: true })
watch(systemAccountFilterSelection, (selection) => rememberPrincipalSelection(selection), { deep: true, immediate: true })

onMounted(() => {
  void loadRouteStrategies()
})

onBeforeUnmount(() => {
  routeStrategyListRequestGeneration += 1
  invalidateEditDetailRequest()
  invalidateSpeedFirstRuntimeRequest()
  clearGroupOptionsSearchTimer()
  clearModelOptionsSearchTimer()
  resetModelOptions()
})

async function loadRouteStrategies(): Promise<boolean> {
  const requestGeneration = ++routeStrategyListRequestGeneration
  const scopeParams = routeStrategyScopeParams.value
  const scopeKey = routeStrategyListScopeKey()
  const listParams = {
    page: page.value,
    pageSize: pageSize.value,
    keyword: keyword.value.trim() || undefined,
    mode: modeFilter.value,
    status: statusFilter.value,
    systemAccountId: scopeParams?.systemAccountId
  }
  loading.value = true
  try {
    const result = await routeStrategiesApi.list(listParams)
    if (requestGeneration !== routeStrategyListRequestGeneration || scopeKey !== routeStrategyListScopeKey()) return false
    if (result.page > 1 && result.items.length === 0 && result.hasMore === false) {
      page.value -= 1
      return await loadRouteStrategies()
    }
    items.value = result.items
    total.value = result.total
    return true
  } catch (error) {
    if (requestGeneration === routeStrategyListRequestGeneration) {
      message.error(extractApiErrorMessage(error, '策略路由加载失败'))
    }
    return false
  } finally {
    if (requestGeneration === routeStrategyListRequestGeneration) loading.value = false
  }
}

function routeStrategyListScopeKey(): string {
  return isManagementView.value
    ? `management:${routeStrategyScopeParams.value?.systemAccountId ?? '*'}`
    : 'self'
}

function handleTableChange(...args: unknown[]) {
  const nextPagination = args[0] as TablePaginationConfig
  page.value = nextPagination.current ?? 1
  pageSize.value = nextPagination.pageSize ?? 20
  void loadRouteStrategies()
}

function applyFilters() {
  page.value = 1
  void loadRouteStrategies()
}

function refreshRouteStrategies() {
  resetSystemAccountOptionsSearch()
  void loadRouteStrategies()
}

function resetSpeedFirstRuntimeForScopeChange(): void {
  invalidateSpeedFirstRuntimeRequest()
  speedFirstRuntimeDrawerOpen.value = false
  speedFirstRuntimeTarget.value = undefined
  speedFirstRuntime.value = undefined
  speedFirstRuntimeError.value = ''
}

function resetFilters() {
  invalidateEditDetailRequest()
  resetSpeedFirstRuntimeForScopeChange()
  const defaults = defaultRouteStrategiesPageState()
  keyword.value = defaults.keyword
  systemAccountFilter.value = defaults.systemAccountFilter
  systemAccountFilterSelection.value = defaults.systemAccountFilterSelection
  statusFilter.value = defaults.statusFilter
  modeFilter.value = defaults.modeFilter
  resetRouteStrategyListForScopeChange()
  resetGroupOptions()
  resetRouteModelOptions()
  resetSystemAccountOptionsSearch()
  page.value = defaults.pagination.current
  pageSize.value = defaults.pagination.pageSize
  pageStateCache.clear()
  void loadRouteStrategies()
}

function handleSystemAccountFilterChange() {
  invalidateEditDetailRequest()
  resetSpeedFirstRuntimeForScopeChange()
  if (systemAccountFilter.value === allSystemAccountsValue) {
    systemAccountFilterSelection.value = undefined
  }
  resetRouteStrategyListForScopeChange()
  resetGroupOptions()
  resetRouteModelOptions()
  resetSystemAccountOptionsSearch()
  page.value = 1
  void loadRouteStrategies()
}

function handleMissingSystemAccountFilter(ids: string[]): void {
  if (systemAccountFilter.value === allSystemAccountsValue || !ids.includes(systemAccountFilter.value)) return
  invalidateEditDetailRequest()
  resetSpeedFirstRuntimeForScopeChange()
  systemAccountFilter.value = allSystemAccountsValue
  systemAccountFilterSelection.value = undefined
  resetRouteStrategyListForScopeChange()
  resetGroupOptions()
  resetRouteModelOptions()
  page.value = 1
  void loadRouteStrategies()
}

function resetRouteStrategyListForScopeChange(): void {
  routeStrategyListRequestGeneration += 1
  loading.value = false
  items.value = []
  total.value = 0
}

function defaultRouteStrategiesPageState(): RouteStrategiesPageState {
  return {
    keyword: '',
    modeFilter: 'all',
    pagination: { current: 1, pageSize: routeStrategiesPageSize },
    statusFilter: 'all',
    systemAccountFilter: allSystemAccountsValue,
    systemAccountFilterSelection: undefined
  }
}

function sanitizeRouteStrategiesPageState(value: unknown, fallback: RouteStrategiesPageState): RouteStrategiesPageState {
  if (!value || typeof value !== 'object') return fallback
  const source = value as Partial<RouteStrategiesPageState>
  const pagination = source.pagination && typeof source.pagination === 'object'
    ? source.pagination as Partial<RouteStrategiesPageState['pagination']>
    : {}
  return {
    keyword: typeof source.keyword === 'string' ? source.keyword : fallback.keyword,
    modeFilter: isRouteStrategyModeFilter(source.modeFilter) ? source.modeFilter : fallback.modeFilter,
    pagination: {
      current: sanitizePositiveInteger(pagination.current, fallback.pagination.current),
      pageSize: sanitizePositiveInteger(pagination.pageSize, fallback.pagination.pageSize, 200)
    },
    statusFilter: isRouteStrategyStatusFilter(source.statusFilter) ? source.statusFilter : fallback.statusFilter,
    systemAccountFilter: typeof source.systemAccountFilter === 'string' && source.systemAccountFilter.trim()
      ? source.systemAccountFilter
      : fallback.systemAccountFilter,
    systemAccountFilterSelection: sanitizeSystemAccountSelection(source.systemAccountFilterSelection)
  }
}

function isRouteStrategyModeFilter(value: unknown): value is RouteStrategyMode | 'all' {
  return value === 'all'
    || value === 'normal'
    || value === 'weighted'
    || value === 'failover'
    || value === 'round_robin'
    || value === 'merge'
}

function isRouteStrategyStatusFilter(value: unknown): value is RouteStrategyStatus | 'all' {
  return value === 'all' || value === 'active' || value === 'disabled'
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

function snapshotPageState(): RouteStrategiesPageState {
  return {
    keyword: keyword.value,
    modeFilter: modeFilter.value,
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
  editDetailRequestToken += 1
  if (isManagementView.value && !routeStrategyScopeParams.value?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建策略路由')
    return
  }
  editingId.value = undefined
  editingIsDefault.value = false
  editingSystemAccountId.value = undefined
  editingExpectedUpdatedAt.value = undefined
  editingBaseline = undefined
  form.name = ''
  form.description = ''
  form.mode = 'normal'
  form.status = 'active'
  form.groupBindings = [createBindingRow()]
  form.normal = defaultNormalRoutingForm()
  resetGroupOptions()
  resetRouteModelOptions()
  modalOpen.value = true
}

async function openEdit(record: RouteStrategyListItem) {
  if (isManagementView.value && !record.systemAccountId?.trim()) {
    message.warning('无法确定策略路由归属系统账户，请刷新后重试')
    return
  }
  const operationScopeParams = routeStrategyOperationScopeParams(record)
  const requestToken = editDetailRequestToken + 1
  editDetailRequestToken = requestToken
  const requestSignature = editDetailRequestSignature(record.id, operationScopeParams?.systemAccountId)
  try {
    const detail = await routeStrategiesApi.editBasicDetail(record.id, operationScopeParams)
    if (!isCurrentEditDetailRequest(requestToken, requestSignature, record.id, operationScopeParams?.systemAccountId)) return
    fillEditForm(detail, record.systemAccountId)
  } catch (error) {
    if (!isCurrentEditDetailRequest(requestToken, requestSignature, record.id, operationScopeParams?.systemAccountId)) return
    message.error(extractApiErrorMessage(error, '策略路由详情加载失败'))
  }
}

function invalidateEditDetailRequest(): void {
  editDetailRequestToken += 1
}

function editDetailRequestSignature(recordId: string, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return JSON.stringify([
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    routeStrategyListScopeKey(),
    systemAccountId ?? '',
    recordId
  ])
}

function isCurrentEditDetailRequest(token: number, signature: string, recordId: string, systemAccountId?: string): boolean {
  return token === editDetailRequestToken
    && signature === editDetailRequestSignature(recordId, systemAccountId)
}

function fillEditForm(record: RouteStrategyEditBasicDetail, fallbackSystemAccountId?: string) {
  editingId.value = record.id
  editingIsDefault.value = record.isDefault
  editingSystemAccountId.value = record.systemAccountId ?? fallbackSystemAccountId
  editingExpectedUpdatedAt.value = record.updatedAt
  form.name = record.name
  form.description = record.description ?? ''
  form.mode = record.mode
  form.status = record.status
  form.groupBindings = record.groupBindings.length
    ? record.groupBindings.map((binding) => createBindingRow(binding.groupId, binding.priority, binding.weight, binding.status, binding.groupName))
    : [createBindingRow()]
  form.normal = normalRoutingFormFromConfig(record.normalRoutingConfig)
  normalizeBindingRowsForMode()
  resetGroupOptions()
  resetRouteModelOptions()
  groupOptionsRaw.value = selectedGroupOptionsFromBindings(record.groupBindings)
  const baseline = buildRouteStrategyFormPayload(false)
  editingBaseline = baseline === false ? undefined : baseline
  modalOpen.value = true
}

async function saveRouteStrategy() {
  const name = form.name.trim()
  if (!name) {
    message.warning('请输入策略路由名称')
    return
  }
  const groupBindings = form.groupBindings.map((binding, index) => ({
    groupId: binding.groupId.trim(),
    priority: bindingOrderUsesPosition.value ? index + 1 : binding.priority,
    weight: binding.weight,
    status: binding.status
  }))
  if (!groupBindings.every((binding) => binding.groupId)) {
    message.warning('请选择分组')
    return
  }
  if (!validateGroupBindingsForMode(groupBindings)) return
  const completePayload = buildRouteStrategyFormPayload()
  if (completePayload === false) return
  const payload = editingId.value
    ? (editingBaseline ? buildRouteStrategyMutationPatch(editingBaseline, completePayload) : undefined)
    : completePayload
  if (!payload) {
    message.error('策略路由编辑基线缺失，请关闭弹窗后重试')
    return
  }
  if (editingId.value && !hasRouteStrategyMutationChanges(payload)) {
    message.info('没有需要保存的修改')
    return
  }
  saving.value = true
  try {
    const operationScopeParams = routeStrategyOperationScopeParams()
    if (isManagementView.value && !operationScopeParams?.systemAccountId) {
      message.warning('请先选择目标系统账户')
      return
    }
    if (editingId.value) {
      if (!editingExpectedUpdatedAt.value) {
        message.error('策略路由编辑版本缺失，请关闭弹窗后重试')
        return
      }
      const result = await routeStrategiesApi.update(editingId.value, {
        ...payload,
        expectedUpdatedAt: editingExpectedUpdatedAt.value
      }, operationScopeParams)
      applyRouteStrategyMutationResult(result)
      if (editingIsDefault.value) {
        invalidateUserReferenceData({
          viewScope: isManagementView.value ? 'admin' : 'self',
          systemAccountId: operationScopeParams?.systemAccountId
        })
      }
      message.success('策略路由已更新')
      modalOpen.value = false
    } else {
      const created = await routeStrategiesApi.create(payload, operationScopeParams)
      applyCreatedRouteStrategyListItem(created, operationScopeParams?.systemAccountId)
      message.success('策略路由已创建')
      modalOpen.value = false
    }
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由保存失败'))
  } finally {
    saving.value = false
  }
}

function applyRouteStrategyMutationResult(result: RouteStrategyMutationResult): void {
  const index = items.value.findIndex((item) => item.id === result.id)
  if (index < 0) return
  const updated = mergeRouteStrategyMutationResult(items.value[index]!, result)
  if (!routeStrategyMatchesCurrentFilters(updated)) {
    items.value.splice(index, 1)
    total.value = Math.max(0, total.value - 1)
    return
  }
  items.value[index] = updated
}

function applyCreatedRouteStrategyListItem(result: RouteStrategyListItem, selectedSystemAccountId?: string): void {
  const created = isManagementView.value && result.systemAccountId && !result.systemAccountName
    ? {
        ...result,
        systemAccountName: systemAccountFilterSelection.value?.id === result.systemAccountId
          ? systemAccountFilterSelection.value.name
          : principalLabelForId('system_account', result.systemAccountId) ?? undefined
      }
    : result
  if (isManagementView.value && selectedSystemAccountId && created.systemAccountId !== selectedSystemAccountId) return
  if (!routeStrategyMatchesCurrentFilters(created)) return
  total.value += 1
  if (page.value !== 1) return
  items.value = [created, ...items.value].slice(0, pageSize.value)
}

function routeStrategyMatchesCurrentFilters(record: RouteStrategyListItem): boolean {
  const namePrefix = keyword.value.trim()
  return (!namePrefix || record.name.startsWith(namePrefix))
    && (modeFilter.value === 'all' || record.mode === modeFilter.value)
    && (statusFilter.value === 'all' || record.status === statusFilter.value)
}

function buildRouteStrategyFormPayload(reportValidation = true): RouteStrategyMutationPayload | false {
  const payload: RouteStrategyMutationPayload = {
    name: form.name.trim(),
    description: form.description.trim() || null,
    mode: form.mode,
    status: form.status,
    groupBindings: form.groupBindings.map((binding, index) => ({
      groupId: binding.groupId.trim(),
      priority: bindingOrderUsesPosition.value ? index + 1 : binding.priority,
      weight: binding.weight,
      status: binding.status
    }))
  }
  if (form.mode === 'normal') {
    payload.normalRoutingConfig = buildNormalRoutingConfigPayload()
  } else {
    // weighted/failover/round_robin/merge 与 normal 共用调度偏好配置（历史命名 normalRoutingConfig）。
    payload.normalRoutingConfig = buildNormalRoutingConfigPayload()
  }
  return payload
}

async function deleteRouteStrategy(record: RouteStrategyListItem) {
  if (record.isDefault) {
    message.warning('默认路由不能删除')
    return
  }
  try {
    const operationScopeParams = routeStrategyOperationScopeParams(record)
    if (isManagementView.value && !operationScopeParams?.systemAccountId) {
      message.warning('无法确定策略路由归属系统账户，请刷新后重试')
      return
    }
    await routeStrategiesApi.delete(record.id, operationScopeParams)
    const index = items.value.findIndex((item) => item.id === record.id)
    if (index >= 0) {
      items.value.splice(index, 1)
      total.value = Math.max(0, total.value - 1)
    }
    message.success('策略路由已删除')
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由删除失败'))
  }
}

function addBinding() {
  if (bindingAddDisabled.value) return
  form.groupBindings.push(createBindingRow('', form.groupBindings.length + 1, 1, 'active'))
  normalizeBindingRowsForMode()
}

function removeBinding(index: number) {
  if (bindingRemoveDisabled.value) return
  form.groupBindings.splice(index, 1)
  normalizeBindingRowsForMode()
}

function bindingWeightMax(index: number): number {
  const otherWeightTotal = form.groupBindings.reduce((sum, binding, bindingIndex) => {
    return bindingIndex === index ? sum : sum + normalizeBindingWeightValue(binding.weight)
  }, 0)
  return Math.max(1, 100 - otherWeightTotal)
}

function handleBindingWeightChange(index: number) {
  const binding = form.groupBindings[index]
  if (!binding || form.mode !== 'weighted') return
  binding.weight = boundedInteger(binding.weight, 1, bindingWeightMax(index))
}

function moveBinding(fromIndex: number, toIndex: number) {
  if (fromIndex === toIndex || fromIndex < 0 || toIndex < 0 || fromIndex >= form.groupBindings.length || toIndex >= form.groupBindings.length) return
  const [binding] = form.groupBindings.splice(fromIndex, 1)
  if (!binding) return
  form.groupBindings.splice(toIndex, 0, binding)
  normalizeBindingRowsForMode()
}

function moveBindingForMode(fromIndex: number, toIndex: number) {
  if (!bindingRowDragEnabled(fromIndex)) return
  const normalizedToIndex = bindingDropTargetIndex(toIndex)
  moveBinding(fromIndex, normalizedToIndex)
}

function bindingRowDragEnabled(index: number): boolean {
  if (form.mode === 'round_robin' || form.mode === 'merge') return form.groupBindings.length > 1
  if (form.mode === 'failover') return index >= 0 && index < form.groupBindings.length && form.groupBindings.length > 1
  return false
}

function bindingDropTargetIndex(index: number): number {
  return Math.min(form.groupBindings.length - 1, Math.max(0, index))
}

function handleBindingDragStart(index: number, event: DragEvent) {
  if (!bindingRowDragEnabled(index)) {
    event.preventDefault()
    return
  }
  bindingDragSourceIndex.value = index
  bindingDragOverIndex.value = index
  event.dataTransfer?.setData('text/plain', String(index))
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
  }
}

function handleBindingDragEnter(index: number) {
  if (bindingDragSourceIndex.value === null) return
  bindingDragOverIndex.value = bindingDropTargetIndex(index)
}

function handleBindingDragOver(index: number, event: DragEvent) {
  if (bindingDragSourceIndex.value === null) return
  event.preventDefault()
  bindingDragOverIndex.value = bindingDropTargetIndex(index)
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = 'move'
  }
}

function handleBindingDrop(index: number, event: DragEvent) {
  event.preventDefault()
  const sourceIndex = bindingDragSourceIndex.value
  handleBindingDragEnd()
  if (sourceIndex === null) return
  moveBindingForMode(sourceIndex, index)
}

function handleBindingDragEnd() {
  bindingDragSourceIndex.value = null
  bindingDragOverIndex.value = null
}

function bindingGroupPlaceholder(index: number): string {
  if (form.mode !== 'failover') return '选择分组'
  return index === 0 ? '选择主用分组' : '选择备用分组'
}

function bindingRoleText(index: number): string {
  return index === 0 ? '主用' : `备用 ${index}`
}

function bindingRoleColor(index: number): string {
  return index === 0 ? 'blue' : 'orange'
}

function createBindingRow(
  groupId = '',
  priority = form.groupBindings.length + 1,
  weight = 1,
  status: 'active' | 'disabled' = 'active',
  groupName?: string
): BindingFormRow {
  return {
    key: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    groupId,
    group: groupId && groupName?.trim() ? { id: groupId, name: groupName.trim() } : undefined,
    priority,
    weight,
    status
  }
}

function selectedGroupOptionsFromBindings(bindings: RouteStrategyGroupBindingSummary[]): RouteStrategyGroupOption[] {
  const selectedGroups = new Map<string, RouteStrategyGroupOption>()
  for (const binding of bindings) {
    const id = binding.groupId.trim()
    if (!id) continue
    selectedGroups.set(id, {
      id,
      name: binding.groupName?.trim() || id,
      providerCode: binding.providerCode?.trim() || '',
      enabled: binding.groupEnabled
    })
  }
  return [...selectedGroups.values()]
}

function routeStrategyOperationScopeParams(record?: Pick<RouteStrategyListItem | RouteStrategySummary | RouteStrategyEditBasicDetail, 'systemAccountId'>): { systemAccountId: string } | undefined {
  const systemAccountId = record?.systemAccountId?.trim()
    || editingSystemAccountId.value?.trim()
    || routeStrategyScopeParams.value?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

function resetGroupOptions() {
  clearGroupOptionsSearchTimer()
  groupOptionsRequestToken += 1
  groupOptionsLoadingKey = undefined
  groupOptionsLoadingPromise = undefined
  groupOptionsRaw.value = []
  groupOptionsLoaded.value = false
  groupOptionsLoading.value = false
}

async function loadGroupOptions(keywordInput = '', selectedIds: string[] = []) {
  const operationScopeParams = routeStrategyOperationScopeParams()
  if (isManagementView.value && !operationScopeParams?.systemAccountId) {
    groupOptionsRequestToken += 1
    groupOptionsLoadingKey = undefined
    groupOptionsLoadingPromise = undefined
    groupOptionsRaw.value = []
    groupOptionsLoaded.value = false
    groupOptionsLoading.value = false
    return
  }
  const keyword = keywordInput.trim() || undefined
  const normalizedSelectedIds = [...new Set(selectedIds.map((id) => id.trim()).filter(Boolean))]
  const requestKey = groupOptionsRequestKey(operationScopeParams?.systemAccountId, keyword, normalizedSelectedIds)
  if (groupOptionsLoadingKey === requestKey && groupOptionsLoadingPromise) {
    return groupOptionsLoadingPromise
  }
  const requestToken = ++groupOptionsRequestToken
  groupOptionsLoading.value = true
  groupOptionsLoadingKey = requestKey
  groupOptionsLoadingPromise = (async () => {
    try {
      await loadGroupOptionsResource({
        api: groupsApi,
        isManagementView: isManagementView.value,
        systemAccountId: operationScopeParams?.systemAccountId,
        keyword,
        selectedIds: normalizedSelectedIds,
        selectedOptions: groupOptionsRaw.value,
        isCurrent: () => requestToken === groupOptionsRequestToken,
        apply: (groups) => {
          const currentSelectedIds = new Set(form.groupBindings.map((binding) => binding.groupId.trim()).filter(Boolean))
          const merged = new Map(groups.map((group) => [group.id, group]))
          for (const group of groupOptionsRaw.value) {
            if (currentSelectedIds.has(group.id) && !merged.has(group.id)) merged.set(group.id, group)
          }
          groupOptionsRaw.value = [...merged.values()]
          groupOptionsLoaded.value = !keyword
        }
      })
    } catch (error) {
      if (requestToken !== groupOptionsRequestToken) return
      message.error(extractApiErrorMessage(error, '分组选项加载失败'))
    } finally {
      if (groupOptionsLoadingKey === requestKey) {
        groupOptionsLoadingKey = undefined
        groupOptionsLoadingPromise = undefined
      }
      if (requestToken === groupOptionsRequestToken) {
        groupOptionsLoading.value = false
      }
    }
  })()
  return groupOptionsLoadingPromise
}

function groupOptionsRequestKey(systemAccountId: string | undefined, keyword: string | undefined, selectedIds: string[]): string {
  return JSON.stringify([
    isManagementView.value ? `management:${systemAccountId ?? ''}` : 'self',
    keyword ?? '',
    selectedIds
  ])
}

function handleGroupOptionsDropdown(open: boolean) {
  if (open && !groupOptionsLoaded.value) {
    void loadGroupOptions('', form.groupBindings.map((binding) => binding.groupId))
  }
}

function handleGroupOptionsSearch(value: string) {
  clearGroupOptionsSearchTimer()
  groupOptionsSearchTimer = window.setTimeout(() => {
    groupOptionsSearchTimer = undefined
    void loadGroupOptions(value, form.groupBindings.map((binding) => binding.groupId))
  }, 250)
}

function clearGroupOptionsSearchTimer() {
  if (groupOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(groupOptionsSearchTimer)
    groupOptionsSearchTimer = undefined
  }
}

function handleModelOptionsDropdown(open: boolean) {
  if (open && modelProviderCodes.value.length) void loadModelOptions()
}

function handleModelOptionsSearch(value: string): void {
  clearModelOptionsSearchTimer()
  modelOptionsSearchTimer = window.setTimeout(() => {
    modelOptionsSearchTimer = undefined
    if (modelProviderCodes.value.length) void loadModelOptions({ keyword: value, selectedIds: selectedModelIds.value })
  }, 250)
}

function clearModelOptionsSearchTimer(): void {
  if (!modelOptionsSearchTimer || typeof window === 'undefined') return
  window.clearTimeout(modelOptionsSearchTimer)
  modelOptionsSearchTimer = undefined
}

function resetRouteModelOptions(): void {
  clearModelOptionsSearchTimer()
  resetModelOptions()
}

function normalizeBindingRowsForMode() {
  ensureMinimumBindingRowsForMode()
  form.groupBindings.forEach((binding, index) => {
    binding.priority = form.mode === 'normal' || form.mode === 'weighted' ? 1 : index + 1
    binding.weight = form.mode === 'weighted' ? Math.max(1, Math.min(100, Number(binding.weight) || 1)) : 1
  })
  normalizeWeightedBindingWeightsForTotal()
}

function ensureMinimumBindingRowsForMode() {
  while (form.groupBindings.length < minimumBindingRowsForMode(form.mode)) {
    form.groupBindings.push(createBindingRow('', form.groupBindings.length + 1, 1, 'active'))
  }
}

function minimumBindingRowsForMode(mode: RouteStrategyMode): number {
  return mode === 'weighted' || mode === 'round_robin' || mode === 'failover' || mode === 'merge' ? 2 : 1
}

function validateGroupBindingsForMode(groupBindings: Array<{ groupId: string; priority: number; weight: number; status: 'active' | 'disabled' }>): boolean {
  const activeBindings = groupBindings.filter((binding) => binding.status === 'active')
  if (form.mode === 'normal' && (groupBindings.length !== 1 || activeBindings.length !== 1)) {
    message.warning('普通路由只能绑定一个启用分组')
    return false
  }
  if (form.mode === 'failover') {
    if (groupBindings.length < 2) {
      message.warning('故障回退路由需要一个主用分组和至少一个备用分组')
      return false
    }
    if (groupBindings[0]?.status !== 'active') {
      message.warning('故障回退路由的主用分组必须启用')
      return false
    }
    if (!groupBindings.slice(1).some((binding) => binding.status === 'active')) {
      message.warning('故障回退路由至少需要一个启用备用分组')
      return false
    }
  }
  if ((form.mode === 'weighted' || form.mode === 'round_robin' || form.mode === 'merge') && activeBindings.length < 2) {
    message.warning(`${routeStrategyModeText(form.mode)}至少需要两个启用分组`)
    return false
  }
  if (form.mode === 'weighted' && totalBindingWeight(groupBindings) > 100) {
    message.warning('权重调度路由的分组权重总和不能超过 100')
    return false
  }
  return true
}

function normalizeWeightedBindingWeightsForTotal() {
  if (form.mode !== 'weighted') return
  let remainingWeight = 100
  form.groupBindings.forEach((binding, index) => {
    const remainingRows = form.groupBindings.length - index - 1
    const maxWeight = Math.max(1, remainingWeight - remainingRows)
    binding.weight = boundedInteger(binding.weight, 1, maxWeight)
    remainingWeight -= binding.weight
  })
}

function totalBindingWeight(bindings: Array<{ weight: number }>): number {
  return bindings.reduce((sum, binding) => sum + normalizeBindingWeightValue(binding.weight), 0)
}

function normalizeBindingWeightValue(value: unknown): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  return Number.isInteger(numeric) && numeric >= 1 ? numeric : 1
}

function routeStrategyActions(record: RouteStrategyListItem): RowActionItem[] {
  const actions: RowActionItem[] = [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'primary' }
  ]
  if (!record.isDefault) {
    actions.push({
      key: 'delete',
      label: '删除',
      icon: 'delete',
      tone: 'danger',
      confirmTitle: `确认删除策略路由「${record.name}」？`,
      confirmOkText: '删除'
    })
  }
  return actions
}

function handleRouteStrategyAction(key: string, record: RouteStrategyListItem) {
  if (key === 'edit') {
    void openEdit(record)
    return
  }
  if (key === 'delete') {
    void deleteRouteStrategy(record)
  }
}

function visibleGroupBindings(record: RouteStrategyListItem): RouteStrategyGroupBindingPreview[] {
  return record.groupBindingPreview
}

function hiddenGroupBindingCount(record: RouteStrategyListItem): number {
  return Math.max(0, record.bindingCount - record.groupBindingPreview.length)
}

function routeStrategyGroupSummary(record: RouteStrategyListItem): string {
  if (!record.bindingCount) return '未绑定'
  const visibleNames = visibleGroupBindings(record).map(routeStrategyGroupLabel).join('、')
  const hiddenCount = hiddenGroupBindingCount(record)
  return hiddenCount > 0 ? `${visibleNames} 等 ${record.bindingCount} 个分组` : visibleNames
}

function routeStrategyBindingCount(record: RouteStrategyListItem): number {
  return record.bindingCount
}

function routeStrategyApiKeyCount(record: RouteStrategyListItem): number {
  return record.apiKeyCount
}

function routeStrategyGroupLabel(binding: RouteStrategyGroupBindingPreview | RouteStrategyGroupBindingSummary): string {
  return binding.groupName || binding.groupId
}

function routeStrategyGroupTagColor(binding: RouteStrategyGroupBindingPreview | RouteStrategyGroupBindingSummary): string {
  return binding.status === 'active' && binding.groupEnabled ? 'blue' : 'default'
}

function routeStrategySystemAccountText(record: RouteStrategyListItem | RouteStrategySummary): string {
  return record.systemAccountName || record.systemAccountId || '-'
}

function routeStrategyModeText(mode: RouteStrategyMode): string {
  return modeOptions.find((item) => item.value === mode)?.label ?? mode
}

function routeStrategyModeDisplayText(record: RouteStrategyListItem | RouteStrategySummary): string {
  const base = routeStrategyModeText(record.mode)
  if (!schedulingPreferenceSupportedModes.includes(record.mode)) return base
  const preference = record.normalRoutingConfig?.schedulingPreference ?? 'cost_first'
  return `${base} / ${preference === 'speed_first' ? '速度优先' : '成本优先'}`
}

function isSpeedFirstRouteStrategy(record: Pick<RouteStrategyListItem | RouteStrategySummary, 'mode' | 'normalRoutingConfig'>): boolean {
  return schedulingPreferenceSupportedModes.includes(record.mode) && record.normalRoutingConfig?.schedulingPreference === 'speed_first'
}

function speedFirstLatencyStatusText(record: RouteStrategyListItem): string {
  if (!isSpeedFirstRouteStrategy(record)) return ''
  const runtime = record.speedFirstLatencyRuntime
  if (!runtime || !runtime.runtimeAvailable) return '速度状态暂不可用'
  return runtime.degradedCount > 0 ? `速度降级 ${runtime.degradedCount} 个账号` : '速度正常'
}

function speedFirstLatencyStatusClass(record: RouteStrategyListItem): string {
  if (!isSpeedFirstRouteStrategy(record)) return ''
  const runtime = record.speedFirstLatencyRuntime
  if (!runtime || !runtime.runtimeAvailable) return 'speed-first-latency-status-unavailable'
  return runtime.degradedCount > 0 ? 'speed-first-latency-status-degraded' : 'speed-first-latency-status-ok'
}

function openSpeedFirstRuntime(record: RouteStrategyListItem): void {
  if (!isSpeedFirstRouteStrategy(record)) return
  speedFirstRuntimeTarget.value = record
  speedFirstRuntime.value = undefined
  speedFirstRuntimeError.value = ''
  speedFirstRuntimeDrawerOpen.value = true
  void loadSpeedFirstRuntime(record)
}

function refreshSpeedFirstRuntime(): void {
  const target = speedFirstRuntimeTarget.value
  if (!target || !speedFirstRuntimeDrawerOpen.value) return
  void loadSpeedFirstRuntime(target)
}

async function loadSpeedFirstRuntime(record: RouteStrategyListItem): Promise<void> {
  const requestToken = ++speedFirstRuntimeRequestToken
  speedFirstRuntimeAbortController?.abort()
  const controller = new AbortController()
  speedFirstRuntimeAbortController = controller
  const operationScopeParams = routeStrategyOperationScopeParams(record)
  const requestSignature = speedFirstRuntimeRequestSignature(record.id, operationScopeParams?.systemAccountId)
  speedFirstRuntimeLoading.value = true
  try {
    const result = await routeStrategiesApi.speedFirstRuntime(record.id, operationScopeParams, { signal: controller.signal })
    if (!isCurrentSpeedFirstRuntimeRequest(requestToken, requestSignature, record.id, operationScopeParams?.systemAccountId)) return
    speedFirstRuntime.value = result
    speedFirstRuntimeError.value = ''
  } catch (error) {
    if (!isCurrentSpeedFirstRuntimeRequest(requestToken, requestSignature, record.id, operationScopeParams?.systemAccountId)) return
    speedFirstRuntime.value = undefined
    speedFirstRuntimeError.value = extractApiErrorMessage(error, '速度状态加载失败')
  } finally {
    if (requestToken === speedFirstRuntimeRequestToken) {
      speedFirstRuntimeLoading.value = false
      if (speedFirstRuntimeAbortController === controller) speedFirstRuntimeAbortController = undefined
    }
  }
}

function invalidateSpeedFirstRuntimeRequest(): void {
  speedFirstRuntimeRequestToken += 1
  speedFirstRuntimeAbortController?.abort()
  speedFirstRuntimeAbortController = undefined
  speedFirstRuntimeLoading.value = false
}

function speedFirstRuntimeRequestSignature(recordId: string, systemAccountId?: string): string {
  const viewer = authState.currentUser.value
  return JSON.stringify([
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    routeStrategyListScopeKey(),
    systemAccountId ?? '',
    recordId
  ])
}

function isCurrentSpeedFirstRuntimeRequest(token: number, signature: string, recordId: string, systemAccountId?: string): boolean {
  return token === speedFirstRuntimeRequestToken
    && speedFirstRuntimeDrawerOpen.value
    && speedFirstRuntimeTarget.value?.id === recordId
    && signature === speedFirstRuntimeRequestSignature(recordId, systemAccountId)
}

function routeStrategyModeColor(mode: RouteStrategyMode): string {
  if (mode === 'weighted') return 'purple'
  if (mode === 'round_robin') return 'blue'
  if (mode === 'failover') return 'orange'
  if (mode === 'merge') return 'lime'
  return 'default'
}

function routeStrategyStatusText(status: RouteStrategyStatus): string {
  return status === 'active' ? '启用' : '停用'
}

function routeStrategyStatusColor(status: RouteStrategyStatus): string {
  return status === 'active' ? 'green' : 'default'
}

function defaultNormalRoutingForm(): NormalRoutingForm {
  return {
    schedulingPreference: 'cost_first',
    firstByteDeadlineSeconds: 30,
    speedFirstConfig: defaultSpeedFirstConfigForm()
  }
}

function defaultSpeedFirstConfigForm(): SpeedFirstConfigForm {
  return {
    slowTriggerCount: 3,
    slowWindowSeconds: 120,
    recoverySuccessCount: 3,
    probeIntervalSeconds: 30,
    degradedTtlSeconds: 300,
    maxFirstByteRetriesPerRequest: 2
  }
}

function normalRoutingFormFromConfig(config?: RouteStrategyNormalRoutingConfig): NormalRoutingForm {
  const fallback = defaultNormalRoutingForm()
  if (!config) return fallback
  const speedFirstConfig = speedFirstConfigFormFromConfig(
    config.schedulingPreference === 'speed_first' ? config.speedFirstConfig : undefined
  )
  return {
    schedulingPreference: config.schedulingPreference ?? fallback.schedulingPreference,
    firstByteDeadlineSeconds: config.schedulingPreference === 'speed_first'
      ? millisecondsToSeconds(config.firstByteDeadlineMs, fallback.firstByteDeadlineSeconds)
      : fallback.firstByteDeadlineSeconds,
    speedFirstConfig
  }
}

function speedFirstConfigFormFromConfig(config?: RouteStrategySpeedFirstConfig): SpeedFirstConfigForm {
  const fallback = defaultSpeedFirstConfigForm()
  return {
    slowTriggerCount: config?.slowTriggerCount ?? fallback.slowTriggerCount,
    slowWindowSeconds: config?.slowWindowSeconds ?? fallback.slowWindowSeconds,
    recoverySuccessCount: config?.recoverySuccessCount ?? fallback.recoverySuccessCount,
    probeIntervalSeconds: config?.probeIntervalSeconds ?? fallback.probeIntervalSeconds,
    degradedTtlSeconds: config?.degradedTtlSeconds ?? fallback.degradedTtlSeconds,
    maxFirstByteRetriesPerRequest: config?.maxFirstByteRetriesPerRequest ?? fallback.maxFirstByteRetriesPerRequest
  }
}

function buildNormalRoutingConfigPayload(): RouteStrategyNormalRoutingConfig {
  if (form.normal.schedulingPreference !== 'speed_first') {
    return { schedulingPreference: 'cost_first' }
  }
  const firstByteDeadlineMs = secondsToMilliseconds(form.normal.firstByteDeadlineSeconds)
  const speedFirstConfig = form.normal.speedFirstConfig
  return {
    schedulingPreference: 'speed_first',
    firstByteDeadlineMs,
    speedFirstConfig: {
      slowTriggerCount: boundedInteger(speedFirstConfig.slowTriggerCount, 2, 10),
      slowWindowSeconds: boundedInteger(speedFirstConfig.slowWindowSeconds, 60, 600),
      recoverySuccessCount: boundedInteger(speedFirstConfig.recoverySuccessCount, 3, 10),
      probeIntervalSeconds: boundedInteger(speedFirstConfig.probeIntervalSeconds, 10, 300),
      degradedTtlSeconds: boundedInteger(speedFirstConfig.degradedTtlSeconds, 60, 3600),
      maxFirstByteRetriesPerRequest: boundedInteger(speedFirstConfig.maxFirstByteRetriesPerRequest, 1, 3)
    }
  }
}

function millisecondsToSeconds(value: unknown, fallback: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  if (!Number.isFinite(numeric) || numeric <= 0) return fallback
  return boundedInteger(Math.ceil(numeric / 1000), 10, 60)
}

function secondsToMilliseconds(value: unknown): number {
  return boundedInteger(value, 10, 60) * 1000
}

function boundedInteger(value: unknown, min: number, max: number): number {
  const numeric = typeof value === 'number' ? value : Number(value)
  const integer = Number.isInteger(numeric) ? numeric : min
  return Math.min(max, Math.max(min, integer))
}
</script>

<style scoped>
.route-strategies-page-card {
  border: 1px solid #e8edf5;
  border-radius: 16px;
  box-shadow: 0 10px 28px rgba(15, 23, 42, 0.04);
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
  color: #334155;
  font-size: 13px;
  font-weight: 600;
}

:deep(.route-strategy-table .ant-table-cell) {
  white-space: nowrap;
}

:deep(.route-strategy-table .ant-empty) {
  margin: 12px 0;
}

.route-strategy-name-cell {
  display: grid;
  min-width: 0;
  gap: 4px;
}

.route-strategy-name-line,
.mobile-list-card-name-row {
  display: flex;
  align-items: center;
  min-width: 0;
  gap: 6px;
}

.route-strategy-mode-cell {
  display: grid;
  justify-items: start;
  gap: 4px;
}

.route-strategy-mode-cell :deep(.ant-tag) {
  margin-inline-end: 0;
}

.speed-first-latency-status {
  font-size: 12px;
  line-height: 18px;
}

.speed-first-latency-status-degraded {
  color: #d97706;
}

.speed-first-latency-status-ok {
  color: #16a34a;
}

.speed-first-latency-status-unavailable {
  color: #64748b;
}

.speed-first-runtime-link,
.speed-first-runtime-mobile-link {
  padding: 0;
  font-size: 12px;
}

.route-strategy-name-text {
  overflow: hidden;
  color: #0f172a;
  font-weight: 400;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mobile-list-card-title,
.mobile-list-card-name-row {
  font-weight: 400;
}

.route-strategy-description {
  overflow: hidden;
  color: rgba(0, 0, 0, 0.45);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.route-strategy-groups {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  min-width: 0;
}

.route-strategy-modal-form {
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
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
}

.route-strategy-field-help-icon {
  flex: none;
  color: #94a3b8;
  cursor: help;
  font-size: 14px;
}

.route-strategy-field-help-icon:hover {
  color: #1677ff;
}

.route-strategy-binding-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 8px;
}

.route-strategy-binding-header,
.route-strategy-binding-row {
  display: grid;
  gap: 8px;
  align-items: center;
}

.route-strategy-binding-row.is-dragging {
  opacity: 0.56;
}

.route-strategy-binding-row.is-drag-over {
  border-radius: 6px;
  outline: 1px dashed #1677ff;
  outline-offset: 3px;
}

.route-strategy-binding-header {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.route-strategy-binding-header-cell {
  display: inline-flex;
  min-width: 0;
  align-items: center;
  gap: 4px;
}

.route-strategy-binding-header-cell > span:first-child {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.route-strategy-binding-row > * {
  min-width: 0;
}

.route-strategy-binding-drag-cell,
.route-strategy-binding-drag-placeholder {
  display: inline-flex;
  width: 32px;
  height: 32px;
  align-items: center;
  justify-content: center;
}

.route-strategy-binding-drag-handle {
  display: inline-flex;
  width: 32px;
  height: 32px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: #64748b;
  cursor: grab;
}

.route-strategy-binding-drag-handle:active {
  cursor: grabbing;
}

.route-strategy-binding-drag-handle:hover,
.route-strategy-binding-drag-handle:focus-visible {
  background: #f1f5f9;
  color: #1677ff;
  outline: none;
}

.route-strategy-binding-role {
  display: flex;
  align-items: center;
}

.route-strategy-binding-role :deep(.ant-tag) {
  margin-inline-end: 0;
}

.route-strategy-binding-row :deep(.ant-input-number) {
  width: 100%;
}

.scheduling-config-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 12px;
}

.scheduling-config-grid :deep(.ant-input-number),
.scheduling-config-grid :deep(.ant-select),
.scheduling-config-grid :deep(.ant-segmented) {
  width: 100%;
}

@media (max-width: 720px) {
  .route-strategy-binding-header {
    display: none;
  }

  .route-strategy-binding-row,
  .scheduling-config-grid {
    grid-template-columns: 1fr;
  }
}
</style>
