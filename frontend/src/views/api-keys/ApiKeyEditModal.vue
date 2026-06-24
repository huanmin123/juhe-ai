<template>
  <a-modal
    v-model:open="modalOpen"
    :title="editingId ? '编辑 API Key' : '新建 API Key'"
    width="860px"
    :confirm-loading="apiKeySaving"
    :ok-button-props="{ type: 'primary', disabled: apiKeySaving }"
    @ok="saveApiKey"
  >
    <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
    <a-form layout="vertical" class="modal-form">
      <a-form-item label="名称" required>
        <a-input v-model:value="form.name" />
      </a-form-item>
      <a-form-item label="入口路由模式" tooltip="普通路由按客户端请求的 model 选择号池；混合智能路由会先调用评分模型，再按等级改写为目标模型。评分请求会单独计入当前 API Key 用量。">
        <a-segmented v-model:value="form.routeMode" :options="routeModeOptions" block />
      </a-form-item>
      <a-form-item label="分组路由策略">
        <a-segmented v-model:value="form.groupRouteStrategy" :options="groupRouteStrategyOptions" block />
      </a-form-item>
      <a-form-item label="绑定分组路由" required>
        <div class="api-key-group-bindings-field">
          <div v-for="(binding, index) in form.groupBindings" :key="binding.key" class="api-key-group-binding-row" :class="{ 'api-key-group-binding-row-weighted': form.groupRouteStrategy === 'weighted_round_robin' }">
            <span class="binding-priority">{{ groupBindingPriorityText(index) }}</span>
            <GroupSelect
              v-model:value="binding.groupId"
              v-model:selected-group="binding.group"
              class="binding-group-select"
              :disabled="formGroupSelectDisabled"
              :filter-option="false"
              :groups="groupOptionsForBinding(index)"
              :loading="groupOptionsLoading"
              show-provider-label
              :placeholder="formGroupSelectDisabled ? '请先选择系统账户' : '输入分组名称搜索'"
              :selected-ids="formGroupBindingIds"
              :selected-groups="formGroupBindingSelections"
              :hidden-option-values="hiddenGroupBindingIds(index)"
              @change="handleGroupBindingChange(index)"
              @dropdown-visible-change="handleFormGroupOptionsDropdown"
              @search="handleFormGroupOptionsSearch"
            />
            <a-input-number
              v-if="form.groupRouteStrategy === 'weighted_round_robin'"
              v-model:value="binding.weight"
              class="binding-weight-input"
              :min="1"
              :max="100"
            />
            <a-select v-model:value="binding.status" class="binding-status-select" :options="bindingStatusOptions" />
            <div class="binding-row-actions">
              <a-tooltip title="上移">
                <a-button type="text" size="small" :disabled="index === 0" @click="moveGroupBinding(index, -1)">
                  <template #icon><up-outlined /></template>
                </a-button>
              </a-tooltip>
              <a-tooltip title="下移">
                <a-button type="text" size="small" :disabled="index === form.groupBindings.length - 1" @click="moveGroupBinding(index, 1)">
                  <template #icon><down-outlined /></template>
                </a-button>
              </a-tooltip>
              <a-popconfirm title="确认移除这个分组绑定？" ok-text="移除" cancel-text="取消" :disabled="form.groupBindings.length <= 1" @confirm="removeGroupBinding(index)">
                <a-tooltip title="移除">
                  <a-button type="text" size="small" danger :disabled="form.groupBindings.length <= 1">
                    <template #icon><delete-outlined /></template>
                  </a-button>
                </a-tooltip>
              </a-popconfirm>
            </div>
          </div>
          <a-button type="dashed" block :disabled="!canAddGroupBinding" :title="addGroupBindingDisabledReason" @click="addGroupBinding">
            <template #icon><plus-outlined /></template>
            添加分组
          </a-button>
        </div>
      </a-form-item>
      <template v-if="form.routeMode === 'hybrid'">
        <div class="hybrid-config-grid">
          <a-form-item label="评分模型" required tooltip="评分模型只从模型目录选择；运行时仍从当前 API Key 绑定分组池里选择能承接该模型的账号，不配置独立评分分组。">
            <a-select
              v-model:value="form.hybridRoutingConfig.scoringModel"
              show-search
              :filter-option="filterHybridModelOption"
              :loading="providerModelOptionsLoading"
              :options="hybridModelSelectOptions"
              placeholder="选择评分模型"
              @dropdown-visible-change="handleHybridModelDropdownVisibleChange"
            />
          </a-form-item>
          <a-form-item label="质量偏好">
            <a-select v-model:value="form.hybridRoutingConfig.qualityPreference" :options="hybridQualityPreferenceOptions" />
          </a-form-item>
          <a-form-item label="评分超时" tooltip="评分请求超过该时间会进入评分不可用兜底，不再阻断客户端主请求。">
            <a-input-number v-model:value="form.hybridRoutingConfig.scoringTimeoutMs" :min="1000" :max="60000" :step="1000" addon-after="ms" />
          </a-form-item>
          <a-form-item label="评分不可用兜底上限" tooltip="评分模型不可用时，系统只在该等级以内从低到高寻找可用目标模型；默认最高到 5，避免直接打到高成本档。">
            <a-input-number v-model:value="form.hybridRoutingConfig.scoringFallbackMaxLevel" :min="2" :max="5" />
          </a-form-item>
          <a-form-item label="评分缓存 TTL" tooltip="只缓存完全相同请求指纹的成功评分结果；缓存命中只省评分调用，目标模型请求仍会正常执行。">
            <a-input-number v-model:value="form.hybridRoutingConfig.scoringCacheTtlSeconds" :min="1" :max="3600" addon-after="秒" />
          </a-form-item>
          <a-form-item label="缓存亲和 TTL" tooltip="同一会话短时间内尽量保持模型稳定，减少频繁跨模型导致的上游缓存损失。">
            <a-input-number v-model:value="form.hybridRoutingConfig.affinityTtlSeconds" :min="1" :max="86400" addon-after="秒" />
          </a-form-item>
          <a-form-item label="切换最小等级差" tooltip="本次评分和上次评分差距小于该值时，优先沿用上次目标模型。">
            <a-input-number v-model:value="form.hybridRoutingConfig.switchMinLevelDelta" :min="0" :max="9" />
          </a-form-item>
          <a-form-item label="低分降级确认次数" tooltip="从高档降到低档前需要连续低分确认，避免一次波动破坏会话缓存。">
            <a-input-number v-model:value="form.hybridRoutingConfig.downgradeConsecutiveLowCount" :min="1" :max="20" />
          </a-form-item>
        </div>
        <a-form-item>
          <a-checkbox v-model:checked="form.hybridRoutingConfig.qualityInspection.enabled">
            <a-tooltip title="质量评分是额外模型调用，只检查上游 200 响应内容是否可交付；非 200、超时和连接失败仍走原有切号与重试。质量评分不可用默认放行原 200。">
              <span>启用 200 响应质量评分</span>
            </a-tooltip>
          </a-checkbox>
        </a-form-item>
        <div v-if="form.hybridRoutingConfig.qualityInspection.enabled" class="hybrid-config-grid">
          <a-form-item label="质量评分模型" required tooltip="质量评分模型同样从当前 API Key 绑定分组池里选账号；不可用时按下方处理方式决定放行或报错。">
            <a-select
              v-model:value="form.hybridRoutingConfig.qualityInspection.scoringModel"
              show-search
              :filter-option="filterHybridModelOption"
              :loading="providerModelOptionsLoading"
              :options="hybridModelSelectOptions"
              placeholder="选择质量评分模型"
              @dropdown-visible-change="handleHybridModelDropdownVisibleChange"
            />
          </a-form-item>
          <a-form-item label="触发模式">
            <a-select v-model:value="form.hybridRoutingConfig.qualityInspection.triggerMode" :options="hybridQualityInspectionTriggerOptions" />
          </a-form-item>
          <a-form-item label="最高复审等级">
            <a-input-number v-model:value="form.hybridRoutingConfig.qualityInspection.maxTriggerLevel" :min="1" :max="10" />
          </a-form-item>
          <a-form-item label="最大重试次数">
            <a-input-number v-model:value="form.hybridRoutingConfig.qualityInspection.maxRetries" :min="0" :max="2" />
          </a-form-item>
          <a-form-item label="失败动作">
            <a-select v-model:value="form.hybridRoutingConfig.qualityInspection.failureAction" :options="hybridQualityInspectionFailureOptions" />
          </a-form-item>
          <a-form-item label="不可用处理" tooltip="质量评分器不可用不等于响应质量不合格。默认放行原始 200；严格质量场景可改为返回错误。">
            <a-select v-model:value="form.hybridRoutingConfig.qualityInspection.unavailableAction" :options="hybridQualityInspectionUnavailableOptions" />
          </a-form-item>
        </div>
        <a-form-item label="等级模型区间" required tooltip="等级区间按从小到大连续配置，起始等级自动接上上一段；例如上一段是 1-4，下一段只能从 5 开始。最多 5 个区间，必须完整覆盖 1-10 且至少配置 2 个不同目标模型。">
          <div class="hybrid-level-routes-field">
            <div v-for="(route, index) in form.hybridRoutingConfig.levelRoutes" :key="index" class="hybrid-level-route-row">
              <a-input-number v-model:value="route.minLevel" :min="1" :max="10" disabled />
              <span class="hybrid-level-separator">至</span>
              <a-input-number
                v-model:value="route.maxLevel"
                :min="hybridRouteMinMaxLevel(index)"
                :max="hybridRouteMaxMaxLevel(index)"
                :disabled="index === form.hybridRoutingConfig.levelRoutes.length - 1"
                @change="handleHybridRouteMaxLevelChange(index)"
              />
              <a-select
                v-model:value="route.targetModel"
                class="hybrid-target-model-select"
                show-search
                :filter-option="filterHybridModelOption"
                :loading="providerModelOptionsLoading"
                :options="hybridModelSelectOptions"
                placeholder="目标模型"
                @dropdown-visible-change="handleHybridModelDropdownVisibleChange"
              />
              <a-popconfirm title="确认移除这个等级区间？" ok-text="移除" cancel-text="取消" :disabled="form.hybridRoutingConfig.levelRoutes.length <= 1" @confirm="removeHybridLevelRoute(index)">
                <a-tooltip title="移除">
                  <a-button type="text" size="small" danger :disabled="form.hybridRoutingConfig.levelRoutes.length <= 1">
                    <template #icon><delete-outlined /></template>
                  </a-button>
                </a-tooltip>
              </a-popconfirm>
            </div>
            <a-button type="dashed" block :disabled="!canAddHybridLevelRoute" :title="addHybridLevelRouteDisabledReason" @click="addHybridLevelRoute">
              <template #icon><plus-outlined /></template>
              添加等级区间
            </a-button>
          </div>
        </a-form-item>
      </template>
      <a-form-item label="状态">
        <a-select v-model:value="form.status" :options="statusOptions" />
      </a-form-item>
      <a-form-item class="api-key-schedule-form-item">
        <div class="api-key-schedule-field">
          <TimeScheduleSection
            :form="form"
            :bordered="false"
            label="时间计划"
            row-key-prefix="api_key_schedule_window"
            help-message="时间计划开启后，保存时按当前时间初始化；之后只在开始和结束边界切换，手动提前启用或提前关闭会保留到下一次计划边界。"
          />
        </div>
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
</template>

<script setup lang="ts">
import type { Dayjs } from 'dayjs'
import { DeleteOutlined, DownOutlined, PlusOutlined, UpOutlined } from '@ant-design/icons-vue'
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'

import GroupSelect from '@/components/GroupSelect.vue'
import type { useScopedApiKeysApi, useScopedGroupsApi } from '@/composables/useScopedDomainApi'
import { useProviderModelSelectOptions } from '@/composables/useProviderModelSelectOptions'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { GroupSelection } from '@/shared/groupLabelCache'
import { formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type { ApiKeyAvailabilitySchedule, ApiKeyGroupRouteStrategy, ApiKeyHybridRoutingConfig, ApiKeyQuotaLimits, ApiKeyRouteMode, ApiKeySummary } from '@/types/domain'
import RequestQuotaFields from '@/views/shared/RequestQuotaFields.vue'
import { createQuotaLimitForm, quotaLimitsPayload as buildQuotaLimitsPayload } from '@/views/shared/requestQuotaForm'
import {
  buildTimeSchedulePayload,
  validateTimeScheduleForm
} from '@/views/shared/timeSchedule'
import TimeScheduleSection from '@/views/shared/TimeScheduleSection.vue'
import {
  createApiKeyTimeScheduleForm,
  type ApiKeyAvailabilityScheduleForm,
  type ApiKeyGroupBindingFormRow
} from './apiKeyFormModel'
import {
  apiKeyBindingStatusOptions as bindingStatusOptions,
  apiKeyGroupRouteStrategyOptions as groupRouteStrategyOptions,
  apiKeyRouteModeOptions as routeModeOptions,
  apiKeyStatusOptions as statusOptions
} from './apiKeyTableConfig'
import { useApiKeyGroupBindings } from './useApiKeyGroupBindings'
import { useApiKeyGroupOptions, type ApiKeyScopeParams } from './useApiKeyGroupOptions'

type ScopedApiKeysApi = ReturnType<typeof useScopedApiKeysApi>
type ScopedGroupsApi = ReturnType<typeof useScopedGroupsApi>

interface CreatedKeyPayload {
  key: string
  title: string
  message: string
}

type ApiKeyHybridRoutingConfigForm = ApiKeyHybridRoutingConfig & {
  qualityInspection: NonNullable<ApiKeyHybridRoutingConfig['qualityInspection']>
}

const props = defineProps<{
  apiKeysApi: Pick<ScopedApiKeysApi, 'create' | 'update'>
  groupsApi: Pick<ScopedGroupsApi, 'options'>
  isManagementView: boolean
  scopeParams?: ApiKeyScopeParams
  targetSystemAccountLabel?: string
}>()

const emit = defineEmits<{
  (event: 'created', payload: CreatedKeyPayload): void
  (event: 'reload', options?: { quiet?: boolean }): void
  (event: 'updated', apiKey: ApiKeySummary): void
}>()

const modalOpen = ref(false)
const editingId = ref<string>()
const editingSystemAccountId = ref<string>()
const { submitAction, submittingRef } = useSubmitAction('api-keys')
const apiKeySaving = submittingRef('api_keys.save')
const hybridLevelRouteMaxCount = 5
const hybridMinLevel = 1
const hybridMaxLevel = 10
const defaultHybridLevelRoutes: ApiKeyHybridRoutingConfig['levelRoutes'] = [
  { minLevel: 1, maxLevel: 2, targetModel: 'gpt-5.4-mini', enabled: true },
  { minLevel: 3, maxLevel: 4, targetModel: 'gpt-5.4-mini', enabled: true },
  { minLevel: 5, maxLevel: 6, targetModel: 'glm-5.2', enabled: true },
  { minLevel: 7, maxLevel: 8, targetModel: 'gpt-5.5', enabled: true },
  { minLevel: 9, maxLevel: 10, targetModel: 'claude-opus-4-8', enabled: true }
]
const hybridQualityPreferenceOptions = [
  { label: '省钱优先', value: 'cost_first' },
  { label: '均衡', value: 'balanced' },
  { label: '质量优先', value: 'quality_first' }
] satisfies Array<{ label: string; value: ApiKeyHybridRoutingConfig['qualityPreference'] }>
const hybridQualityInspectionTriggerOptions = [
  { label: '按风险触发', value: 'risk_based' },
  { label: '质量优先触发', value: 'quality_first_only' },
  { label: '所有混合请求', value: 'always_for_hybrid' }
] satisfies Array<{ label: string; value: NonNullable<ApiKeyHybridRoutingConfig['qualityInspection']>['triggerMode'] }>
const hybridQualityInspectionFailureOptions = [
  { label: '先修复再升档', value: 'repair_then_upgrade' },
  { label: '升级下一档', value: 'upgrade_next_level' },
  { label: '同模型重试', value: 'retry_same_model' },
  { label: '返回错误', value: 'return_error' }
] satisfies Array<{ label: string; value: NonNullable<ApiKeyHybridRoutingConfig['qualityInspection']>['failureAction'] }>
const hybridQualityInspectionUnavailableOptions = [
  { label: '放行原 200', value: 'pass_through' },
  { label: '返回错误', value: 'return_error' }
] satisfies Array<{ label: string; value: NonNullable<ApiKeyHybridRoutingConfig['qualityInspection']>['unavailableAction'] }>
const form = reactive({
  name: '',
  routeMode: 'normal' as ApiKeyRouteMode,
  groupRouteStrategy: 'priority_failover' as ApiKeyGroupRouteStrategy,
  groupBindings: [] as ApiKeyGroupBindingFormRow[],
  hybridRoutingConfig: createHybridRoutingConfigForm(),
  status: 'active' as 'active' | 'disabled',
  expiresAt: undefined as Dayjs | undefined,
  description: '',
  quotaLimits: createQuotaLimitForm(),
  availabilitySchedule: createApiKeyTimeScheduleForm()
})
const groupFilterSelection = ref<GroupSelection | undefined>()
const isManagementViewRef = computed(() => props.isManagementView)
const apiKeyFormScopeParams = computed<ApiKeyScopeParams>(() => {
  const systemAccountId = editingSystemAccountId.value || props.scopeParams?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
})
const groupBindingIdsForOptions = computed(() => form.groupBindings.map((binding) => binding.groupId).filter(Boolean))
const formGroupSelectDisabled = computed(() => props.isManagementView && !apiKeyFormScopeParams.value?.systemAccountId)
const {
  clearGroupOptionsSearchTimer,
  groups,
  groupOptionsLoading,
  handleFormGroupOptionsDropdown,
  handleFormGroupOptionsSearch,
  loadGroupOptions,
  resetGroupOptionsSearch
} = useApiKeyGroupOptions({
  groupsApi: props.groupsApi,
  isManagementView: isManagementViewRef,
  isFormContext: () => true,
  listScopeParams: apiKeyFormScopeParams,
  formScopeParams: apiKeyFormScopeParams,
  groupFilterSelection,
  formGroupBindings: () => form.groupBindings,
  formGroupBindingIds: groupBindingIdsForOptions,
  allowMixedProviderProtocolProfiles: () => true,
  onGroupFilterCleared: () => {
    groupFilterSelection.value = undefined
  }
})
const {
  filterModelOption: filterHybridModelOption,
  hasModel: hasHybridModel,
  loadFailed: providerModelOptionsLoadFailed,
  loading: providerModelOptionsLoading,
  loadModelOptions: loadProviderModelOptions,
  selectOptions: hybridModelSelectOptions
} = useProviderModelSelectOptions({
  scopeParams: apiKeyFormScopeParams,
  onLoadError: () => {
    message.warning('加载模型选项失败')
  }
})
const {
  addGroupBinding,
  addGroupBindingDisabledReason,
  canAddGroupBinding,
  createGroupBindingRow,
  existingGroupBindingRows,
  formGroupBindingIds,
  formGroupBindingSelections,
  groupBindingPriorityText,
  groupOptionsForBinding,
  handleGroupBindingChange,
  hiddenGroupBindingIds,
  moveGroupBinding,
  removeGroupBinding,
  validateGroupBindingsPayload
} = useApiKeyGroupBindings({
  form,
  groups,
  formGroupSelectDisabled
})
const canAddHybridLevelRoute = computed(() => {
  const routes = form.hybridRoutingConfig.levelRoutes
  return routes.length < hybridLevelRouteMaxCount
    && routes.some((route) => Number(route.maxLevel) > Number(route.minLevel))
})
const addHybridLevelRouteDisabledReason = computed(() => {
  if (form.hybridRoutingConfig.levelRoutes.length >= hybridLevelRouteMaxCount) {
    return `最多只能配置 ${hybridLevelRouteMaxCount} 个等级区间`
  }
  if (!canAddHybridLevelRoute.value) {
    return '当前没有可继续拆分的等级区间'
  }
  return undefined
})

function handleHybridModelDropdownVisibleChange(open: boolean): void {
  if (open) void loadProviderModelOptions()
}

function validateExistingHybridModel(model: string, label: string): boolean {
  if (providerModelOptionsLoadFailed.value) {
    message.warning('模型目录加载失败，暂时不能保存混合路由配置')
    return false
  }
  if (!hybridModelSelectOptions.value.length) {
    message.warning('模型目录为空，请先维护供应商模型目录')
    return false
  }
  if (!hasHybridModel(model)) {
    message.warning(`${label}不存在于模型目录：${model}`)
    return false
  }
  return true
}

async function openCreate() {
  if (props.isManagementView && !props.scopeParams?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  editingId.value = undefined
  editingSystemAccountId.value = undefined
  resetGroupOptionsSearch()
  await loadGroupOptions('', false, {
    systemAccountId: props.scopeParams?.systemAccountId,
    selectedIds: []
  }, {
    useLocalWindow: false
  })
  const defaultGroup = groups.value.find((group) => group.enabled && group.isDefault)
  if (!defaultGroup) {
    message.warning('请先创建并启用默认分组，再创建 API Key')
    return
  }
  Object.assign(form, {
    name: '',
    routeMode: 'normal',
    groupRouteStrategy: 'priority_failover',
    groupBindings: [createGroupBindingRow(defaultGroup)],
    hybridRoutingConfig: createHybridRoutingConfigForm(),
    status: 'active',
    expiresAt: undefined,
    description: '',
    quotaLimits: createQuotaLimitForm(),
    availabilitySchedule: createApiKeyTimeScheduleForm()
  })
  modalOpen.value = true
}

async function openEdit(apiKey: ApiKeySummary) {
  const editScopeParams = apiKeyOperationScopeParams(apiKey)
  if (props.isManagementView && !editScopeParams?.systemAccountId) {
    message.warning('无法确定 API Key 归属系统账户，请刷新后重试')
    return
  }
  let bindings: ApiKeyGroupBindingFormRow[]
  let quotaLimits: ReturnType<typeof createQuotaLimitForm>
  let expiresAt: Dayjs | undefined
  let availabilitySchedule: ApiKeyAvailabilityScheduleForm
  try {
    bindings = existingGroupBindingRows(apiKey)
    quotaLimits = createQuotaLimitForm(apiKey.quotaLimits)
    expiresAt = parseStrictDatePickerValue(apiKey.expiresAt, 'API Key 过期时间')
    availabilitySchedule = createApiKeyTimeScheduleForm(apiKey.availabilitySchedule)
  } catch (error) {
    message.error(extractApiErrorMessage(error, 'API Key 数据结构异常，请清理后再编辑'))
    return
  }
  if (!bindings.length) {
    message.error('API Key 分组绑定数据异常，请刷新后重试')
    return
  }
  editingId.value = apiKey.id
  editingSystemAccountId.value = editScopeParams?.systemAccountId
  Object.assign(form, {
    name: apiKey.name,
    routeMode: apiKey.routeMode,
    groupRouteStrategy: apiKey.groupRouteStrategy,
    groupBindings: bindings,
    hybridRoutingConfig: createHybridRoutingConfigForm(apiKey.hybridRoutingConfig),
    status: apiKey.status,
    expiresAt,
    description: apiKey.description ?? '',
    quotaLimits,
    availabilitySchedule
  })
  resetGroupOptionsSearch()
  await loadGroupOptions('', false, {
    systemAccountId: editScopeParams?.systemAccountId,
    selectedIds: formGroupBindingIds.value
  }, {
    useLocalWindow: false
  })
  if (form.routeMode === 'hybrid') {
    void loadProviderModelOptions()
  }
  modalOpen.value = true
}

function apiKeyOperationScopeParams(apiKey?: Pick<ApiKeySummary, 'systemAccountId'>): ApiKeyScopeParams {
  const systemAccountId = apiKey?.systemAccountId?.trim()
    || apiKeyFormScopeParams.value?.systemAccountId
    || props.scopeParams?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

const saveApiKey = submitAction('api_keys.save', async () => {
  if (!form.name.trim()) {
    message.warning('请填写名称')
    return
  }
  try {
    const groupBindings = validateGroupBindingsPayload()
    if (!groupBindings) return
    if (form.routeMode === 'hybrid') {
      await loadProviderModelOptions()
    }
    const hybridRoutingConfig = hybridRoutingConfigPayload()
    if (hybridRoutingConfig === false) return
    const availabilitySchedule = availabilitySchedulePayload()
    if (availabilitySchedule === false) {
      return
    }
    const targetId = editingId.value
    const expiresAt = formatServerDateTimeInput(form.expiresAt)
    const payload = {
      name: form.name,
      routeMode: form.routeMode,
      groupRouteStrategy: form.groupRouteStrategy,
      hybridRoutingConfig,
      groupBindings,
      status: form.status,
      expiresAt: targetId ? expiresAt : expiresAt ?? undefined,
      description: form.description,
      quotaLimits: quotaLimitsPayload(),
      availabilitySchedule
    }
    if (targetId) {
      const updated = await props.apiKeysApi.update(targetId, payload, apiKeyOperationScopeParams())
      emit('updated', updated)
      message.success('API Key 已更新')
      emit('reload', { quiet: true })
    } else {
      const result = await props.apiKeysApi.create(payload, props.scopeParams)
      emit('created', {
        key: result.key,
        title: 'API Key 已创建',
        message: '按下面 3 步完成客户端接入；完整密钥只在此处直接展示，请先复制保存。'
      })
      message.success('API Key 已创建')
      emit('reload')
    }
    modalOpen.value = false
  } catch (error) {
    console.error(error)
    message.error(extractApiErrorMessage(error, '保存 API Key 失败'))
  }
})

function quotaLimitsPayload(): ApiKeyQuotaLimits {
  return buildQuotaLimitsPayload(form.quotaLimits)
}

function createHybridRoutingConfigForm(input: Partial<ApiKeyHybridRoutingConfig> = {}): ApiKeyHybridRoutingConfigForm {
  const qualityInspection = input.qualityInspection
  const levelRoutes = normalizeHybridLevelRouteFormRows(input.levelRoutes?.length ? input.levelRoutes : defaultHybridLevelRoutes)
  return {
    scoringModel: input.scoringModel ?? 'gpt-5.4-mini',
    scoringContextMode: 'full_request',
    qualityPreference: input.qualityPreference ?? 'balanced',
    scoringTimeoutMs: input.scoringTimeoutMs ?? 15000,
    scoringFallbackMaxLevel: input.scoringFallbackMaxLevel ?? 5,
    scoringCacheEnabled: true,
    scoringCacheTtlSeconds: input.scoringCacheTtlSeconds ?? 300,
    cacheAffinityEnabled: true,
    affinityTtlSeconds: input.affinityTtlSeconds ?? 900,
    switchMinLevelDelta: input.switchMinLevelDelta ?? 2,
    downgradeConsecutiveLowCount: input.downgradeConsecutiveLowCount ?? 2,
    levelRoutes,
    qualityInspection: {
      enabled: qualityInspection?.enabled ?? true,
      scoringModel: qualityInspection?.scoringModel ?? input.scoringModel ?? 'gpt-5.4-mini',
      triggerMode: qualityInspection?.triggerMode ?? 'risk_based',
      maxTriggerLevel: qualityInspection?.maxTriggerLevel ?? 6,
      maxRetries: qualityInspection?.maxRetries ?? 2,
      failureAction: qualityInspection?.failureAction ?? 'repair_then_upgrade',
      unavailableAction: qualityInspection?.unavailableAction ?? 'pass_through'
    }
  }
}

function hybridRoutingConfigPayload(): ApiKeyHybridRoutingConfig | undefined | false {
  if (form.routeMode !== 'hybrid') return undefined
  const scoringModel = form.hybridRoutingConfig.scoringModel.trim()
  if (!scoringModel) {
    message.warning('请选择评分模型')
    return false
  }
  if (!validateExistingHybridModel(scoringModel, '评分模型')) {
    return false
  }
  const scoringTimeoutMs = normalizeIntegerField(form.hybridRoutingConfig.scoringTimeoutMs, 1000, 60000, '评分超时')
  const scoringFallbackMaxLevel = normalizeIntegerField(form.hybridRoutingConfig.scoringFallbackMaxLevel, 2, 5, '评分不可用兜底上限')
  const scoringCacheTtlSeconds = normalizeIntegerField(form.hybridRoutingConfig.scoringCacheTtlSeconds, 1, 3600, '评分缓存 TTL')
  const affinityTtlSeconds = normalizeIntegerField(form.hybridRoutingConfig.affinityTtlSeconds, 1, 86400, '缓存亲和 TTL')
  const switchMinLevelDelta = normalizeIntegerField(form.hybridRoutingConfig.switchMinLevelDelta, 0, 9, '切换最小等级差')
  const downgradeConsecutiveLowCount = normalizeIntegerField(form.hybridRoutingConfig.downgradeConsecutiveLowCount, 1, 20, '低分降级确认次数')
  if (
    scoringTimeoutMs === false
    || scoringFallbackMaxLevel === false
    || scoringCacheTtlSeconds === false
    || affinityTtlSeconds === false
    || switchMinLevelDelta === false
    || downgradeConsecutiveLowCount === false
  ) {
    return false
  }
  const levelRoutes = normalizedHybridLevelRoutes()
  if (levelRoutes === false) return false
  const qualityInspection = normalizedHybridQualityInspection()
  if (qualityInspection === false) return false
  return {
    scoringModel,
    scoringContextMode: 'full_request',
    qualityPreference: form.hybridRoutingConfig.qualityPreference,
    scoringTimeoutMs,
    scoringFallbackMaxLevel,
    scoringCacheEnabled: true,
    scoringCacheTtlSeconds,
    cacheAffinityEnabled: true,
    affinityTtlSeconds,
    switchMinLevelDelta,
    downgradeConsecutiveLowCount,
    levelRoutes,
    qualityInspection
  }
}

function normalizedHybridQualityInspection(): ApiKeyHybridRoutingConfig['qualityInspection'] | false {
  const qualityInspection = form.hybridRoutingConfig.qualityInspection
  if (!qualityInspection.enabled) {
    return {
      ...qualityInspection,
      enabled: false,
      scoringModel: qualityInspection.scoringModel.trim(),
      unavailableAction: qualityInspection.unavailableAction
    }
  }
  const scoringModel = qualityInspection.scoringModel.trim()
  if (!scoringModel) {
    message.warning('请选择质量评分模型')
    return false
  }
  if (!validateExistingHybridModel(scoringModel, '质量评分模型')) {
    return false
  }
  const maxTriggerLevel = normalizeIntegerField(qualityInspection.maxTriggerLevel, 1, 10, '质量评分最高复审等级')
  const maxRetries = normalizeIntegerField(qualityInspection.maxRetries, 0, 2, '质量评分最大重试次数')
  if (maxTriggerLevel === false || maxRetries === false) return false
  return {
    enabled: true,
    scoringModel,
    triggerMode: qualityInspection.triggerMode,
    maxTriggerLevel,
    maxRetries,
    failureAction: qualityInspection.failureAction,
    unavailableAction: qualityInspection.unavailableAction
  }
}

function normalizedHybridLevelRoutes(): ApiKeyHybridRoutingConfig['levelRoutes'] | false {
  normalizeHybridLevelRouteSequence()
  if (form.hybridRoutingConfig.levelRoutes.length > hybridLevelRouteMaxCount) {
    message.warning(`混合路由最多只能配置 ${hybridLevelRouteMaxCount} 个等级区间`)
    return false
  }
  const normalized = form.hybridRoutingConfig.levelRoutes.map((route, index) => {
    const minLevel = normalizeIntegerField(route.minLevel, 1, 10, `第 ${index + 1} 个区间起始等级`)
    const maxLevel = normalizeIntegerField(route.maxLevel, 1, 10, `第 ${index + 1} 个区间结束等级`)
    if (minLevel === false || maxLevel === false) return false
    if (minLevel > maxLevel) {
      message.warning(`第 ${index + 1} 个等级区间起始等级不能大于结束等级`)
      return false
    }
    const targetModel = route.targetModel.trim()
    if (!targetModel) {
      message.warning(`第 ${index + 1} 个启用区间必须选择目标模型`)
      return false
    }
    if (!validateExistingHybridModel(targetModel, `第 ${index + 1} 个等级区间目标模型`)) {
      return false
    }
    return {
      minLevel,
      maxLevel,
      targetModel,
      enabled: true
    }
  })
  if (normalized.some((route) => route === false)) return false
  const routes = normalized as ApiKeyHybridRoutingConfig['levelRoutes']
  const targetModelKeys = new Set(routes.map((route) => route.targetModel.trim().toLowerCase()).filter(Boolean))
  if (targetModelKeys.size < 2) {
    message.warning('混合路由至少需要配置 2 个不同的目标模型')
    return false
  }
  const firstRoute = routes[0]
  if (!firstRoute || firstRoute.minLevel !== 1 || firstRoute.maxLevel < 2 || firstRoute.maxLevel > 5) {
    message.warning('最低档必须从等级 1 开始，并覆盖 1-2 到 1-5 之间的范围')
    return false
  }
  let expectedMinLevel = hybridMinLevel
  for (let index = 0; index < routes.length; index += 1) {
    const route = routes[index]
    if (route.minLevel !== expectedMinLevel) {
      message.warning(`第 ${index + 1} 个等级区间必须从 ${expectedMinLevel} 开始`)
      return false
    }
    if (route.maxLevel < route.minLevel) {
      message.warning(`第 ${index + 1} 个等级区间起始等级不能大于结束等级`)
      return false
    }
    expectedMinLevel = route.maxLevel + 1
  }
  if (expectedMinLevel !== hybridMaxLevel + 1) {
    message.warning('等级模型区间必须连续覆盖 1-10')
    return false
  }
  return routes
}

function normalizeIntegerField(value: unknown, min: number, max: number, label: string): number | false {
  if (typeof value !== 'number' || !Number.isInteger(value) || value < min || value > max) {
    message.warning(`${label}必须是 ${min}-${max} 之间的整数`)
    return false
  }
  return value
}

function addHybridLevelRoute() {
  normalizeHybridLevelRouteSequence()
  if (!canAddHybridLevelRoute.value) {
    const reason = addHybridLevelRouteDisabledReason.value
    if (reason) message.warning(reason)
    return
  }
  const routes = form.hybridRoutingConfig.levelRoutes
  let splitIndex = 0
  for (let index = 1; index < routes.length; index += 1) {
    if ((routes[index].maxLevel - routes[index].minLevel) > (routes[splitIndex].maxLevel - routes[splitIndex].minLevel)) {
      splitIndex = index
    }
  }
  const route = routes[splitIndex]
  const originalMaxLevel = route.maxLevel
  const splitMaxLevel = Math.floor((route.minLevel + route.maxLevel) / 2)
  route.maxLevel = splitMaxLevel
  routes.splice(splitIndex + 1, 0, {
    minLevel: splitMaxLevel + 1,
    maxLevel: originalMaxLevel,
    targetModel: '',
    enabled: true
  })
  normalizeHybridLevelRouteSequence()
}

function removeHybridLevelRoute(index: number) {
  if (form.hybridRoutingConfig.levelRoutes.length <= 1) return
  form.hybridRoutingConfig.levelRoutes.splice(index, 1)
  normalizeHybridLevelRouteSequence()
}

function handleHybridRouteMaxLevelChange(index: number): void {
  normalizeHybridLevelRouteSequence(index)
}

function hybridRouteMinMaxLevel(index: number): number {
  return form.hybridRoutingConfig.levelRoutes[index]?.minLevel ?? hybridMinLevel
}

function hybridRouteMaxMaxLevel(index: number): number {
  const remainingRoutes = form.hybridRoutingConfig.levelRoutes.length - index - 1
  const maxLevel = hybridMaxLevel - remainingRoutes
  return index === 0 ? Math.min(5, maxLevel) : maxLevel
}

function normalizeHybridLevelRouteFormRows(routes: ApiKeyHybridRoutingConfig['levelRoutes']): ApiKeyHybridRoutingConfig['levelRoutes'] {
  const enabledRoutes = routes
    .filter((route) => route.enabled !== false)
    .slice(0, hybridLevelRouteMaxCount)
    .sort((left, right) => left.minLevel - right.minLevel || left.maxLevel - right.maxLevel)
    .map((route) => ({
      minLevel: route.minLevel,
      maxLevel: route.maxLevel,
      targetModel: route.targetModel,
      enabled: true
    }))
  const normalizedRoutes = enabledRoutes.length ? enabledRoutes : defaultHybridLevelRoutes.map((route) => ({ ...route }))
  normalizeHybridLevelRouteRows(normalizedRoutes)
  return normalizedRoutes
}

function normalizeHybridLevelRouteSequence(changedIndex = 0): void {
  normalizeHybridLevelRouteRows(form.hybridRoutingConfig.levelRoutes, changedIndex)
}

function normalizeHybridLevelRouteRows(routes: ApiKeyHybridRoutingConfig['levelRoutes'], changedIndex = 0): void {
  if (!routes.length) {
    routes.push({ minLevel: hybridMinLevel, maxLevel: hybridMaxLevel, targetModel: '', enabled: true })
  }
  if (routes.length > hybridLevelRouteMaxCount) {
    routes.splice(hybridLevelRouteMaxCount)
  }
  for (let index = 0; index < routes.length; index += 1) {
    const route = routes[index]
    const minLevel = index === 0 ? hybridMinLevel : routes[index - 1].maxLevel + 1
    const maxLevelCeiling = hybridRouteMaxLevelForRows(routes, index)
    route.enabled = true
    route.minLevel = minLevel
    route.maxLevel = clampInteger(route.maxLevel, minLevel, maxLevelCeiling)
    if (index === routes.length - 1) {
      route.maxLevel = hybridMaxLevel
    }
  }
  for (let index = Math.max(0, changedIndex + 1); index < routes.length; index += 1) {
    const route = routes[index]
    route.minLevel = routes[index - 1].maxLevel + 1
    route.maxLevel = clampInteger(route.maxLevel, route.minLevel, hybridRouteMaxLevelForRows(routes, index))
    if (index === routes.length - 1) {
      route.maxLevel = hybridMaxLevel
    }
  }
}

function hybridRouteMaxLevelForRows(routes: ApiKeyHybridRoutingConfig['levelRoutes'], index: number): number {
  const remainingRoutes = routes.length - index - 1
  const maxLevel = hybridMaxLevel - remainingRoutes
  return index === 0 ? Math.min(5, maxLevel) : maxLevel
}

function clampInteger(value: unknown, min: number, max: number): number {
  const numeric = typeof value === 'number' && Number.isFinite(value) ? Math.trunc(value) : min
  return Math.min(max, Math.max(min, numeric))
}

function availabilitySchedulePayload(): ApiKeyAvailabilitySchedule | null | false {
  const scheduleValidation = validateTimeScheduleForm(form.availabilitySchedule)
  if (scheduleValidation) {
    message.warning(scheduleValidation)
    return false
  }
  return buildTimeSchedulePayload<ApiKeyAvailabilitySchedule>(form.availabilitySchedule)
}

watch(modalOpen, (open) => {
  if (open) return
  editingId.value = undefined
  editingSystemAccountId.value = undefined
})

watch(() => form.routeMode, (routeMode) => {
  if (routeMode !== 'hybrid') return
  void loadProviderModelOptions()
})

onBeforeUnmount(clearGroupOptionsSearchTimer)

defineExpose({
  openCreate,
  openEdit
})
</script>

<style scoped>
.modal-alert {
  margin-bottom: 16px;
}

.modal-form {
  margin-top: 16px;
}

.api-key-group-bindings-field {
  display: grid;
  gap: 10px;
}

.api-key-group-binding-row {
  display: grid;
  grid-template-columns: 64px minmax(0, 1fr) 96px auto;
  gap: 8px;
  align-items: start;
}

.api-key-group-binding-row-weighted {
  grid-template-columns: 64px minmax(0, 1fr) 84px 96px auto;
}

.binding-priority {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 32px;
  min-width: 0;
  color: #475569;
  font-size: 12px;
  font-weight: 700;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #f8fafc;
}

.binding-group-select,
.binding-weight-input,
.binding-status-select {
  min-width: 0;
}

.binding-weight-input {
  width: 100%;
}

.binding-row-actions {
  display: inline-flex;
  align-items: center;
  gap: 2px;
}

.api-key-schedule-field {
  display: grid;
  gap: 10px;
}

.hybrid-config-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px 12px;
}

.hybrid-config-grid :deep(.ant-input-number-group-wrapper),
.hybrid-config-grid :deep(.ant-input-number),
.hybrid-config-grid :deep(.ant-input),
.hybrid-config-grid :deep(.ant-select) {
  width: 100%;
}

.hybrid-level-routes-field {
  display: grid;
  gap: 8px;
}

.hybrid-level-route-row {
  display: grid;
  grid-template-columns: 72px 24px 72px minmax(160px, 1fr) 32px;
  gap: 8px;
  align-items: center;
}

.hybrid-level-route-row :deep(.ant-input-number),
.hybrid-target-model-select {
  width: 100%;
  min-width: 0;
}

.hybrid-level-separator {
  color: #64748b;
  font-size: 12px;
  text-align: center;
}

@media (max-width: 640px) {
  .api-key-group-binding-row {
    grid-template-columns: 64px minmax(0, 1fr);
  }

  .binding-weight-input,
  .binding-status-select,
  .binding-row-actions {
    grid-column: 2;
  }

  .binding-row-actions {
    justify-content: flex-start;
  }

  .hybrid-config-grid,
  .hybrid-level-route-row {
    grid-template-columns: 1fr;
  }

  .hybrid-level-separator {
    text-align: left;
  }
}
</style>
