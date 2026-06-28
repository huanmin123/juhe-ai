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
          <a-tag :color="routeStrategyModeColor(record.mode)">{{ routeStrategyModeText(record.mode) }}</a-tag>
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
            <span v-if="!record.groupBindings.length" class="muted-cell">未绑定</span>
          </div>
        </template>
        <template v-else-if="column.key === 'apiKeyCount'">
          <a-tag>{{ formatNumber(record.apiKeyCount ?? 0) }}</a-tag>
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
              <a-tag :color="routeStrategyModeColor(record.mode)">{{ routeStrategyModeText(record.mode) }}</a-tag>
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
              <strong>{{ formatNumber(record.apiKeyCount ?? 0) }}</strong>
            </div>
            <div class="mobile-list-meta-item">
              <span>更新时间</span>
              <strong>{{ formatDateTime(record.updatedAt) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
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
      <a-form layout="vertical" class="route-strategy-modal-form">
        <a-form-item label="名称" required>
          <a-input v-model:value="form.name" placeholder="请输入策略路由名称" />
        </a-form-item>
        <a-form-item label="说明">
          <a-textarea v-model:value="form.description" :rows="2" placeholder="可选" />
        </a-form-item>
        <a-row :gutter="12">
          <a-col :span="12">
            <a-form-item label="路由模式" required>
              <a-select v-model:value="form.mode" :options="modeOptions" />
            </a-form-item>
          </a-col>
          <a-col :span="12">
            <a-form-item label="状态" required>
              <a-select v-model:value="form.status" :options="statusOptions" />
            </a-form-item>
          </a-col>
        </a-row>

        <div class="modal-section-title">分组绑定</div>
        <div class="route-strategy-binding-list">
          <div class="route-strategy-binding-header" :style="bindingGridStyle">
            <span v-for="column in bindingColumns" :key="column.key">{{ column.label }}</span>
          </div>
          <div v-for="(binding, index) in form.groupBindings" :key="binding.key" class="route-strategy-binding-row" :style="bindingGridStyle">
            <a-select
              v-model:value="binding.groupId"
              show-search
              :filter-option="false"
              :loading="groupOptionsLoading"
              :options="groupOptions"
              placeholder="选择分组"
              @dropdown-visible-change="handleGroupOptionsDropdown"
              @search="handleGroupOptionsSearch"
            />
            <a-input-number v-if="bindingShowsPriority" v-model:value="binding.priority" :min="1" :max="100000" :placeholder="bindingPriorityPlaceholder" />
            <a-input-number v-if="bindingShowsWeight" v-model:value="binding.weight" :min="1" :max="100" placeholder="权重" />
            <a-select v-model:value="binding.status" :options="statusOptions" />
            <a-button type="text" danger :disabled="form.groupBindings.length <= 1" @click="removeBinding(index)">
              <template #icon><DeleteOutlined /></template>
            </a-button>
          </div>
        </div>
        <a-button type="dashed" block :disabled="bindingAddDisabled" @click="addBinding">
          <template #icon><PlusOutlined /></template>
          添加分组
        </a-button>

        <template v-if="form.mode === 'hybrid_smart'">
          <div class="modal-section-title">混合智能配置</div>
          <div class="hybrid-config-grid">
            <a-form-item label="评分模型" required>
              <a-select
                v-model:value="form.hybrid.scoringModel"
                show-search
                allow-clear
                :filter-option="filterModelOption"
                :loading="modelOptionsLoading"
                :options="modelSelectOptions"
                placeholder="选择评分模型"
                @dropdown-visible-change="handleModelOptionsDropdown"
              />
            </a-form-item>
            <a-form-item label="质量偏好">
              <a-segmented v-model:value="form.hybrid.qualityPreference" block :options="qualityPreferenceOptions" />
            </a-form-item>
            <a-form-item label="评分超时">
              <a-input-number v-model:value="form.hybrid.scoringTimeoutMs" :min="1000" :max="60000" addon-after="ms" />
            </a-form-item>
            <a-form-item label="评分失败兜底最高等级">
              <a-input-number v-model:value="form.hybrid.scoringFallbackMaxLevel" :min="2" :max="5" />
            </a-form-item>
          </div>

          <div class="modal-section-title">等级模型</div>
          <div class="hybrid-level-route-list">
            <div class="hybrid-level-route-header">
              <span>等级范围</span>
              <span>目标模型</span>
              <span></span>
            </div>
            <div v-for="(route, index) in form.hybrid.levelRoutes" :key="route.key" class="hybrid-level-route-row">
              <div class="hybrid-level-range">
                <a-input-number v-model:value="route.minLevel" :min="1" :max="10" disabled />
                <span>-</span>
                <a-input-number
                  v-model:value="route.maxLevel"
                  :min="hybridRouteMinMaxLevel(index)"
                  :max="hybridRouteMaxMaxLevel(index)"
                  :disabled="index === form.hybrid.levelRoutes.length - 1"
                  @change="normalizeHybridLevelRouteRanges"
                />
              </div>
              <a-select
                v-model:value="route.targetModel"
                show-search
                allow-clear
                :filter-option="filterModelOption"
                :loading="modelOptionsLoading"
                :options="modelSelectOptions"
                placeholder="选择目标模型"
                @dropdown-visible-change="handleModelOptionsDropdown"
              />
              <a-button type="text" danger :disabled="form.hybrid.levelRoutes.length <= 2" @click="removeHybridLevelRoute(index)">
                <template #icon><DeleteOutlined /></template>
              </a-button>
            </div>
          </div>
          <a-button type="dashed" block :disabled="form.hybrid.levelRoutes.length >= 5" @click="addHybridLevelRoute">
            <template #icon><PlusOutlined /></template>
            添加等级
          </a-button>

          <div class="modal-section-title">质量检查</div>
          <a-form-item label="启用质量检查">
            <a-switch v-model:checked="form.hybrid.qualityInspection.enabled" checked-children="启用" un-checked-children="停用" />
          </a-form-item>
          <div class="hybrid-config-grid">
            <a-form-item label="质量评分模型">
              <a-select
                v-model:value="form.hybrid.qualityInspection.scoringModel"
                show-search
                allow-clear
                :disabled="!form.hybrid.qualityInspection.enabled"
                :filter-option="filterModelOption"
                :loading="modelOptionsLoading"
                :options="modelSelectOptions"
                placeholder="默认使用评分模型"
                @dropdown-visible-change="handleModelOptionsDropdown"
              />
            </a-form-item>
            <a-form-item label="触发模式">
              <a-select v-model:value="form.hybrid.qualityInspection.triggerMode" :disabled="!form.hybrid.qualityInspection.enabled" :options="qualityInspectionTriggerOptions" />
            </a-form-item>
            <a-form-item label="最高触发等级">
              <a-input-number v-model:value="form.hybrid.qualityInspection.maxTriggerLevel" :disabled="!form.hybrid.qualityInspection.enabled" :min="1" :max="10" />
            </a-form-item>
            <a-form-item label="最多重试">
              <a-input-number v-model:value="form.hybrid.qualityInspection.maxRetries" :disabled="!form.hybrid.qualityInspection.enabled" :min="0" :max="2" />
            </a-form-item>
            <a-form-item label="失败处理">
              <a-select v-model:value="form.hybrid.qualityInspection.failureAction" :disabled="!form.hybrid.qualityInspection.enabled" :options="qualityInspectionFailureActionOptions" />
            </a-form-item>
            <a-form-item label="检查不可用处理">
              <a-select v-model:value="form.hybrid.qualityInspection.unavailableAction" :disabled="!form.hybrid.qualityInspection.enabled" :options="qualityInspectionUnavailableActionOptions" />
            </a-form-item>
          </div>

          <div class="modal-section-title">缓存与切换</div>
          <div class="hybrid-config-grid">
            <a-form-item label="评分缓存 TTL">
              <a-input-number v-model:value="form.hybrid.scoringCacheTtlSeconds" :min="1" :max="3600" addon-after="秒" />
            </a-form-item>
            <a-form-item label="模型亲和 TTL">
              <a-input-number v-model:value="form.hybrid.affinityTtlSeconds" :min="1" :max="86400" addon-after="秒" />
            </a-form-item>
            <a-form-item label="切换等级差">
              <a-input-number v-model:value="form.hybrid.switchMinLevelDelta" :min="0" :max="9" />
            </a-form-item>
            <a-form-item label="降级确认次数">
              <a-input-number v-model:value="form.hybrid.downgradeConsecutiveLowCount" :min="1" :max="20" />
            </a-form-item>
          </div>
        </template>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons-vue'
import { computed, onMounted, reactive, ref, watch } from 'vue'
import type { TablePaginationConfig } from 'ant-design-vue'

import type { RouteStrategyMutationPayload } from '@/api/domains/routeStrategies'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { filterModelOption, useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'
import { useScopedGroupsApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { useScopedMenuView } from '@/composables/useScopedMenuView'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatDateTime, formatNumber } from '@/shared/formatters'
import type {
  ApiKeyHybridLevelRoute,
  ApiKeyHybridQualityInspectionFailureAction,
  ApiKeyHybridQualityInspectionTriggerMode,
  ApiKeyHybridQualityInspectionUnavailableAction,
  ApiKeyHybridQualityPreference,
  ApiKeyHybridRoutingConfig,
  GroupOptionSummary,
  RouteStrategyGroupBindingSummary,
  RouteStrategyMode,
  RouteStrategyStatus,
  RouteStrategySummary
} from '@/types/domain'

interface BindingFormRow {
  key: string
  groupId: string
  priority: number
  weight: number
  status: 'active' | 'disabled'
}

interface HybridLevelRouteFormRow extends ApiKeyHybridLevelRoute {
  key: string
}

interface HybridQualityInspectionForm {
  enabled: boolean
  scoringModel: string
  triggerMode: ApiKeyHybridQualityInspectionTriggerMode
  maxTriggerLevel: number
  maxRetries: number
  failureAction: ApiKeyHybridQualityInspectionFailureAction
  unavailableAction: ApiKeyHybridQualityInspectionUnavailableAction
}

interface HybridRoutingForm {
  scoringModel: string
  qualityPreference: ApiKeyHybridQualityPreference
  scoringTimeoutMs: number
  scoringFallbackMaxLevel: number
  scoringCacheTtlSeconds: number
  affinityTtlSeconds: number
  switchMinLevelDelta: number
  downgradeConsecutiveLowCount: number
  levelRoutes: HybridLevelRouteFormRow[]
  qualityInspection: HybridQualityInspectionForm
}

const { isManagementView, scopedSystemAccountId } = useScopedMenuView()
const routeStrategiesApi = useScopedRouteStrategiesApi(isManagementView)
const groupsApi = useScopedGroupsApi(isManagementView)
const modelOptionsScopeParams = computed(() => {
  const systemAccountId = scopedSystemAccountId()
  return systemAccountId ? { systemAccountId } : undefined
})
const {
  loading: modelOptionsLoading,
  loadModelOptions,
  selectOptions: modelSelectOptions
} = useProviderModelSelectOptions({
  scopeParams: modelOptionsScopeParams,
  onLoadError: (error) => message.warning(extractApiErrorMessage(error, '模型选项加载失败'))
})
const keyword = ref('')
const statusFilter = ref<RouteStrategyStatus | 'all'>('all')
const modeFilter = ref<RouteStrategyMode | 'all'>('all')
const loading = ref(false)
const saving = ref(false)
const modalOpen = ref(false)
const editingId = ref<string>()
const items = ref<RouteStrategySummary[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const groupOptionsRaw = ref<GroupOptionSummary[]>([])
const groupOptionsLoading = ref(false)

const form = reactive({
  name: '',
  description: '',
  mode: 'normal' as RouteStrategyMode,
  status: 'active' as RouteStrategyStatus,
  groupBindings: [] as BindingFormRow[],
  hybrid: defaultHybridRoutingForm()
})

const modeOptions: Array<{ label: string; value: RouteStrategyMode }> = [
  { label: '普通路由', value: 'normal' },
  { label: '混合智能路由', value: 'hybrid_smart' },
  { label: '权重调度路由', value: 'weighted' },
  { label: '故障回退路由', value: 'failover' },
  { label: '轮询路由', value: 'round_robin' }
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

const qualityPreferenceOptions = [
  { label: '成本优先', value: 'cost_first' },
  { label: '均衡', value: 'balanced' },
  { label: '质量优先', value: 'quality_first' }
]

const qualityInspectionTriggerOptions = [
  { label: '质量优先时触发', value: 'quality_first_only' },
  { label: '风险场景触发', value: 'risk_based' },
  { label: '混合路由总是触发', value: 'always_for_hybrid' }
]

const qualityInspectionFailureActionOptions = [
  { label: '修复后升级', value: 'repair_then_upgrade' },
  { label: '升级下一档', value: 'upgrade_next_level' },
  { label: '重试当前模型', value: 'retry_same_model' },
  { label: '直接返回错误', value: 'return_error' }
]

const qualityInspectionUnavailableActionOptions = [
  { label: '放行响应', value: 'pass_through' },
  { label: '返回错误', value: 'return_error' }
]

const columns = computed<Array<Record<string, unknown>>>(() => {
  const baseColumns: Array<Record<string, unknown>> = [
    { title: '名称', key: 'name', width: 260 },
    { title: '模式', key: 'mode', width: 150 },
    { title: '状态', key: 'status', width: 100 },
    { title: '绑定分组', key: 'groups', width: 320 },
    { title: 'API Key', dataIndex: 'apiKeyCount', key: 'apiKeyCount', width: 100 },
    { title: '更新时间', dataIndex: 'updatedAt', key: 'updatedAt', width: 180 },
    { title: '操作', key: 'actions', width: 96, fixed: 'right' }
  ]
  return isManagementView.value
    ? [{ title: '系统账户', key: 'systemAccount', width: 180 }, ...baseColumns]
    : baseColumns
})

const activeFilterCount = computed(() => [
  keyword.value.trim(),
  statusFilter.value !== 'all',
  modeFilter.value !== 'all'
].filter(Boolean).length)

const bindingAddDisabled = computed(() => form.mode === 'normal' && form.groupBindings.length >= 1)
const bindingShowsPriority = computed(() => form.mode === 'failover' || form.mode === 'round_robin' || form.mode === 'hybrid_smart')
const bindingShowsWeight = computed(() => form.mode === 'weighted')
const bindingPriorityPlaceholder = computed(() => form.mode === 'round_robin' ? '顺序' : '优先级')
const bindingColumns = computed(() => [
  { key: 'group', label: '分组' },
  ...(bindingShowsPriority.value ? [{ key: 'priority', label: bindingPriorityPlaceholder.value }] : []),
  ...(bindingShowsWeight.value ? [{ key: 'weight', label: '权重' }] : []),
  { key: 'status', label: '状态' },
  { key: 'actions', label: '' }
])
const bindingGridStyle = computed(() => {
  const tracks = ['minmax(0, 1fr)']
  if (bindingShowsPriority.value) tracks.push('minmax(76px, 92px)')
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

watch(() => form.mode, (mode) => {
  if (mode === 'normal' && form.groupBindings.length > 1) {
    form.groupBindings = [form.groupBindings[0] ?? createBindingRow()]
  }
  normalizeBindingRowsForMode()
  if (mode === 'hybrid_smart') {
    normalizeHybridLevelRouteRanges()
    void loadModelOptions()
  }
})

onMounted(() => {
  void loadRouteStrategies()
  void loadGroupOptions()
})

async function loadRouteStrategies() {
  loading.value = true
  try {
    const result = await routeStrategiesApi.list({
      page: page.value,
      pageSize: pageSize.value,
      keyword: keyword.value.trim() || undefined,
      mode: modeFilter.value,
      status: statusFilter.value,
      systemAccountId: scopedSystemAccountId()
    })
    items.value = result.items
    total.value = result.total
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由加载失败'))
  } finally {
    loading.value = false
  }
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
  void loadRouteStrategies()
}

function resetFilters() {
  keyword.value = ''
  statusFilter.value = 'all'
  modeFilter.value = 'all'
  page.value = 1
  void loadRouteStrategies()
}

function openCreate() {
  editingId.value = undefined
  form.name = ''
  form.description = ''
  form.mode = 'normal'
  form.status = 'active'
  form.groupBindings = [createBindingRow()]
  form.hybrid = defaultHybridRoutingForm()
  modalOpen.value = true
}

function openEdit(record: RouteStrategySummary) {
  editingId.value = record.id
  form.name = record.name
  form.description = record.description ?? ''
  form.mode = record.mode
  form.status = record.status
  form.groupBindings = record.groupBindings.length
    ? record.groupBindings.map((binding) => createBindingRow(binding.groupId, binding.priority, binding.weight, binding.status))
    : [createBindingRow()]
  form.hybrid = hybridRoutingFormFromConfig(record.hybridRoutingConfig)
  normalizeBindingRowsForMode()
  if (record.mode === 'hybrid_smart') normalizeHybridLevelRouteRanges()
  modalOpen.value = true
  if (record.mode === 'hybrid_smart') void loadModelOptions()
}

async function saveRouteStrategy() {
  const name = form.name.trim()
  if (!name) {
    message.warning('请输入策略路由名称')
    return
  }
  const groupBindings = form.groupBindings.map((binding) => ({
    groupId: binding.groupId.trim(),
    priority: binding.priority,
    weight: binding.weight,
    status: binding.status
  }))
  if (!groupBindings.every((binding) => binding.groupId)) {
    message.warning('请选择分组')
    return
  }
  if (!validateGroupBindingsForMode(groupBindings)) return
  saving.value = true
  try {
    const payload: RouteStrategyMutationPayload = {
      name,
      description: form.description.trim() || null,
      mode: form.mode,
      status: form.status,
      groupBindings
    }
    if (form.mode === 'hybrid_smart') {
      const hybridRoutingConfig = buildHybridRoutingConfigPayload()
      if (hybridRoutingConfig === false) return
      payload.hybridRoutingConfig = hybridRoutingConfig
    } else {
      payload.hybridRoutingConfig = null
    }
    if (editingId.value) {
      await routeStrategiesApi.update(editingId.value, payload, { systemAccountId: scopedSystemAccountId() })
      message.success('策略路由已更新')
    } else {
      await routeStrategiesApi.create(payload, { systemAccountId: scopedSystemAccountId() })
      message.success('策略路由已创建')
    }
    modalOpen.value = false
    await loadRouteStrategies()
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由保存失败'))
  } finally {
    saving.value = false
  }
}

async function deleteRouteStrategy(record: RouteStrategySummary) {
  if (record.isDefault) {
    message.warning('默认路由不能删除')
    return
  }
  try {
    await routeStrategiesApi.delete(record.id, { systemAccountId: scopedSystemAccountId() })
    message.success('策略路由已删除')
    await loadRouteStrategies()
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
  if (form.groupBindings.length <= 1) return
  form.groupBindings.splice(index, 1)
  normalizeBindingRowsForMode()
}

function createBindingRow(groupId = '', priority = form.groupBindings.length + 1, weight = 1, status: 'active' | 'disabled' = 'active'): BindingFormRow {
  return {
    key: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    groupId,
    priority,
    weight,
    status
  }
}

async function loadGroupOptions(keywordInput = '') {
  groupOptionsLoading.value = true
  try {
    groupOptionsRaw.value = await groupsApi.options({
      keyword: keywordInput.trim() || undefined,
      limit: 100,
      manageableOnly: true,
      systemAccountId: scopedSystemAccountId()
    })
  } catch (error) {
    message.error(extractApiErrorMessage(error, '分组选项加载失败'))
  } finally {
    groupOptionsLoading.value = false
  }
}

function handleGroupOptionsDropdown(open: boolean) {
  if (open && !groupOptionsRaw.value.length) void loadGroupOptions()
}

function handleGroupOptionsSearch(value: string) {
  void loadGroupOptions(value)
}

function handleModelOptionsDropdown(open: boolean) {
  if (open) void loadModelOptions()
}

function normalizeBindingRowsForMode() {
  form.groupBindings.forEach((binding, index) => {
    binding.priority = form.mode === 'normal' || form.mode === 'weighted' ? 1 : index + 1
    binding.weight = form.mode === 'weighted' ? Math.max(1, Math.min(100, Number(binding.weight) || 1)) : 1
  })
}

function validateGroupBindingsForMode(groupBindings: Array<{ groupId: string; priority: number; weight: number; status: 'active' | 'disabled' }>): boolean {
  const activeBindings = groupBindings.filter((binding) => binding.status === 'active')
  if (form.mode === 'normal' && (groupBindings.length !== 1 || activeBindings.length !== 1)) {
    message.warning('普通路由只能绑定一个启用分组')
    return false
  }
  if ((form.mode === 'weighted' || form.mode === 'failover' || form.mode === 'round_robin') && activeBindings.length < 2) {
    message.warning(`${routeStrategyModeText(form.mode)}至少需要两个启用分组`)
    return false
  }
  if (form.mode === 'hybrid_smart' && activeBindings.length < 1) {
    message.warning('混合智能路由至少需要一个启用分组')
    return false
  }
  if (form.mode === 'failover') {
    const priorities = new Set(activeBindings.map((binding) => binding.priority))
    if (priorities.size !== activeBindings.length) {
      message.warning('故障回退路由的启用分组优先级不能重复')
      return false
    }
  }
  return true
}

function routeStrategyActions(record: RouteStrategySummary): RowActionItem[] {
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

function handleRouteStrategyAction(key: string, record: RouteStrategySummary) {
  if (key === 'edit') {
    openEdit(record)
    return
  }
  if (key === 'delete') {
    void deleteRouteStrategy(record)
  }
}

function visibleGroupBindings(record: RouteStrategySummary): RouteStrategyGroupBindingSummary[] {
  return record.groupBindings.slice(0, 3)
}

function hiddenGroupBindingCount(record: RouteStrategySummary): number {
  return Math.max(0, record.groupBindings.length - 3)
}

function routeStrategyGroupSummary(record: RouteStrategySummary): string {
  if (!record.groupBindings.length) return '未绑定'
  const visibleNames = visibleGroupBindings(record).map(routeStrategyGroupLabel).join('、')
  const hiddenCount = hiddenGroupBindingCount(record)
  return hiddenCount > 0 ? `${visibleNames} 等 ${record.groupBindings.length} 个分组` : visibleNames
}

function routeStrategyGroupLabel(binding: RouteStrategyGroupBindingSummary): string {
  return binding.groupName || binding.groupId
}

function routeStrategyGroupTagColor(binding: RouteStrategyGroupBindingSummary): string {
  return binding.status === 'active' && binding.groupEnabled ? 'blue' : 'default'
}

function routeStrategySystemAccountText(record: RouteStrategySummary): string {
  return record.systemAccountName || record.systemAccountId || '-'
}

function routeStrategyModeText(mode: RouteStrategyMode): string {
  return modeOptions.find((item) => item.value === mode)?.label ?? mode
}

function routeStrategyModeColor(mode: RouteStrategyMode): string {
  if (mode === 'hybrid_smart') return 'cyan'
  if (mode === 'weighted') return 'purple'
  if (mode === 'round_robin') return 'blue'
  if (mode === 'failover') return 'orange'
  return 'default'
}

function routeStrategyStatusText(status: RouteStrategyStatus): string {
  return status === 'active' ? '启用' : '停用'
}

function routeStrategyStatusColor(status: RouteStrategyStatus): string {
  return status === 'active' ? 'green' : 'default'
}

function defaultHybridRoutingForm(): HybridRoutingForm {
  return {
    scoringModel: '',
    qualityPreference: 'balanced',
    scoringTimeoutMs: 15000,
    scoringFallbackMaxLevel: 5,
    scoringCacheTtlSeconds: 300,
    affinityTtlSeconds: 900,
    switchMinLevelDelta: 2,
    downgradeConsecutiveLowCount: 2,
    levelRoutes: [
      createHybridLevelRoute(1, 5, ''),
      createHybridLevelRoute(6, 10, '')
    ],
    qualityInspection: defaultHybridQualityInspectionForm()
  }
}

function defaultHybridQualityInspectionForm(scoringModel = ''): HybridQualityInspectionForm {
  return {
    enabled: true,
    scoringModel,
    triggerMode: 'risk_based',
    maxTriggerLevel: 6,
    maxRetries: 2,
    failureAction: 'repair_then_upgrade',
    unavailableAction: 'pass_through'
  }
}

function hybridRoutingFormFromConfig(config?: ApiKeyHybridRoutingConfig): HybridRoutingForm {
  const fallback = defaultHybridRoutingForm()
  if (!config) return fallback
  return {
    scoringModel: config.scoringModel ?? '',
    qualityPreference: config.qualityPreference ?? fallback.qualityPreference,
    scoringTimeoutMs: config.scoringTimeoutMs ?? fallback.scoringTimeoutMs,
    scoringFallbackMaxLevel: config.scoringFallbackMaxLevel ?? fallback.scoringFallbackMaxLevel,
    scoringCacheTtlSeconds: config.scoringCacheTtlSeconds ?? fallback.scoringCacheTtlSeconds,
    affinityTtlSeconds: config.affinityTtlSeconds ?? fallback.affinityTtlSeconds,
    switchMinLevelDelta: config.switchMinLevelDelta ?? fallback.switchMinLevelDelta,
    downgradeConsecutiveLowCount: config.downgradeConsecutiveLowCount ?? fallback.downgradeConsecutiveLowCount,
    levelRoutes: config.levelRoutes?.length
      ? config.levelRoutes.map((route) => createHybridLevelRoute(route.minLevel, route.maxLevel, route.targetModel, route.enabled))
      : fallback.levelRoutes,
    qualityInspection: config.qualityInspection
      ? {
          enabled: config.qualityInspection.enabled,
          scoringModel: config.qualityInspection.scoringModel ?? config.scoringModel ?? '',
          triggerMode: config.qualityInspection.triggerMode,
          maxTriggerLevel: config.qualityInspection.maxTriggerLevel,
          maxRetries: config.qualityInspection.maxRetries,
          failureAction: config.qualityInspection.failureAction,
          unavailableAction: config.qualityInspection.unavailableAction
        }
      : defaultHybridQualityInspectionForm(config.scoringModel)
  }
}

function createHybridLevelRoute(minLevel: number, maxLevel: number, targetModel: string, enabled = true): HybridLevelRouteFormRow {
  return {
    key: `${Date.now()}-${Math.random().toString(16).slice(2)}`,
    minLevel,
    maxLevel,
    targetModel,
    enabled
  }
}

function normalizeHybridLevelRouteRanges() {
  let minLevel = 1
  form.hybrid.levelRoutes.forEach((route, index) => {
    const remaining = form.hybrid.levelRoutes.length - index - 1
    const minMaxLevel = index === 0 ? Math.max(2, minLevel) : minLevel
    const maxMaxLevel = index === 0
      ? Math.min(5, 10 - remaining)
      : 10 - remaining
    route.minLevel = minLevel
    route.maxLevel = index === form.hybrid.levelRoutes.length - 1
      ? 10
      : boundedInteger(route.maxLevel, minMaxLevel, maxMaxLevel)
    route.enabled = true
    minLevel = route.maxLevel + 1
  })
}

function hybridRouteMinMaxLevel(index: number): number {
  const route = form.hybrid.levelRoutes[index]
  if (!route) return 1
  return index === 0 ? 2 : route.minLevel
}

function hybridRouteMaxMaxLevel(index: number): number {
  const remaining = form.hybrid.levelRoutes.length - index - 1
  return index === 0 ? Math.min(5, 10 - remaining) : 10 - remaining
}

function addHybridLevelRoute() {
  if (form.hybrid.levelRoutes.length >= 5) return
  normalizeHybridLevelRouteRanges()
  const lastRoute = form.hybrid.levelRoutes[form.hybrid.levelRoutes.length - 1]
  if (!lastRoute || lastRoute.minLevel >= 10) return
  const nextMaxLevel = lastRoute.maxLevel
  lastRoute.maxLevel = Math.max(lastRoute.minLevel, nextMaxLevel - 1)
  form.hybrid.levelRoutes.push(createHybridLevelRoute(lastRoute.maxLevel + 1, nextMaxLevel, ''))
  normalizeHybridLevelRouteRanges()
}

function removeHybridLevelRoute(index: number) {
  if (form.hybrid.levelRoutes.length <= 2) return
  form.hybrid.levelRoutes.splice(index, 1)
  normalizeHybridLevelRouteRanges()
}

function buildHybridRoutingConfigPayload(): ApiKeyHybridRoutingConfig | false {
  normalizeHybridLevelRouteRanges()
  const scoringModel = form.hybrid.scoringModel.trim()
  if (!scoringModel) {
    message.warning('请选择混合智能路由评分模型')
    return false
  }
  const levelRoutes = form.hybrid.levelRoutes.map((route) => ({
    minLevel: route.minLevel,
    maxLevel: route.maxLevel,
    targetModel: route.targetModel.trim(),
    enabled: true
  }))
  if (!levelRoutes.every((route) => route.targetModel)) {
    message.warning('请选择每个等级范围的目标模型')
    return false
  }
  const distinctModels = new Set(levelRoutes.map((route) => route.targetModel.toLowerCase()))
  if (distinctModels.size < 2) {
    message.warning('混合智能路由至少需要两个不同目标模型')
    return false
  }
  const qualityInspection = form.hybrid.qualityInspection
  return {
    scoringModel,
    scoringContextMode: 'full_request',
    qualityPreference: form.hybrid.qualityPreference,
    scoringTimeoutMs: boundedInteger(form.hybrid.scoringTimeoutMs, 1000, 60000),
    scoringFallbackMaxLevel: boundedInteger(form.hybrid.scoringFallbackMaxLevel, 2, 5),
    scoringCacheEnabled: true,
    scoringCacheTtlSeconds: boundedInteger(form.hybrid.scoringCacheTtlSeconds, 1, 3600),
    cacheAffinityEnabled: true,
    affinityTtlSeconds: boundedInteger(form.hybrid.affinityTtlSeconds, 1, 86400),
    switchMinLevelDelta: boundedInteger(form.hybrid.switchMinLevelDelta, 0, 9),
    downgradeConsecutiveLowCount: boundedInteger(form.hybrid.downgradeConsecutiveLowCount, 1, 20),
    levelRoutes,
    qualityInspection: {
      enabled: qualityInspection.enabled,
      scoringModel: qualityInspection.scoringModel.trim() || scoringModel,
      triggerMode: qualityInspection.triggerMode,
      maxTriggerLevel: boundedInteger(qualityInspection.maxTriggerLevel, 1, 10),
      maxRetries: boundedInteger(qualityInspection.maxRetries, 0, 2),
      failureAction: qualityInspection.failureAction,
      unavailableAction: qualityInspection.unavailableAction
    }
  }
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

.route-strategy-name-text {
  overflow: hidden;
  color: #0f172a;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  margin: 18px 0 10px;
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
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

.route-strategy-binding-header {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.route-strategy-binding-row > * {
  min-width: 0;
}

.route-strategy-binding-row :deep(.ant-input-number) {
  width: 100%;
}

.hybrid-config-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 12px;
}

.hybrid-config-grid :deep(.ant-input-number),
.hybrid-config-grid :deep(.ant-select),
.hybrid-config-grid :deep(.ant-segmented) {
  width: 100%;
}

.hybrid-level-route-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 8px;
}

.hybrid-level-route-header,
.hybrid-level-route-row {
  display: grid;
  grid-template-columns: minmax(132px, 156px) minmax(0, 1fr) 32px;
  gap: 8px;
  align-items: center;
}

.hybrid-level-route-header {
  color: #64748b;
  font-size: 12px;
  line-height: 18px;
}

.hybrid-level-range {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 12px minmax(0, 1fr);
  gap: 6px;
  align-items: center;
}

.hybrid-level-range :deep(.ant-input-number),
.hybrid-level-route-row :deep(.ant-select) {
  width: 100%;
}

@media (max-width: 720px) {
  .route-strategy-binding-header {
    display: none;
  }

  .route-strategy-binding-row,
  .hybrid-config-grid,
  .hybrid-level-route-row {
    grid-template-columns: 1fr;
  }

  .hybrid-level-route-header {
    display: none;
  }
}
</style>
