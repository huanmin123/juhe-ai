<template>
  <a-modal
    v-model:open="modalOpen"
    :title="editingId ? '编辑 API Key' : '新建 API Key'"
    width="640px"
    :confirm-loading="apiKeySaving"
    :ok-button-props="{ type: 'primary', disabled: apiKeySaving }"
    @ok="saveApiKey"
  >
    <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
    <a-form layout="vertical" class="modal-form">
      <a-form-item label="名称" required>
        <a-input v-model:value="form.name" />
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
            help-message="时间计划开启后，只在开始时间启用一次，在结束时间关闭一次；边界之后的手动启停不会被持续覆盖。"
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
import { useSubmitAction } from '@/composables/useSubmitAction'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import type { GroupSelection } from '@/shared/groupLabelCache'
import { formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type { ApiKeyAvailabilitySchedule, ApiKeyGroupRouteStrategy, ApiKeyQuotaLimits, ApiKeySummary } from '@/types/domain'
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
const form = reactive({
  name: '',
  groupRouteStrategy: 'priority_failover' as ApiKeyGroupRouteStrategy,
  groupBindings: [] as ApiKeyGroupBindingFormRow[],
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
  onGroupFilterCleared: () => {
    groupFilterSelection.value = undefined
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

async function openCreate() {
  if (props.isManagementView && !props.scopeParams?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  editingId.value = undefined
  editingSystemAccountId.value = undefined
  resetGroupOptionsSearch()
  await loadGroupOptions('', true, {
    systemAccountId: props.scopeParams?.systemAccountId,
    selectedIds: []
  })
  const defaultGroup = groups.value.find((group) => group.enabled && group.isDefault)
  if (!defaultGroup) {
    message.warning('请先创建并启用默认分组，再创建 API Key')
    return
  }
  Object.assign(form, {
    name: '',
    groupRouteStrategy: 'priority_failover',
    groupBindings: [createGroupBindingRow(defaultGroup)],
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
    groupRouteStrategy: apiKey.groupRouteStrategy,
    groupBindings: bindings,
    status: apiKey.status,
    expiresAt,
    description: apiKey.description ?? '',
    quotaLimits,
    availabilitySchedule
  })
  resetGroupOptionsSearch()
  await loadGroupOptions('', true, {
    systemAccountId: editScopeParams?.systemAccountId,
    selectedIds: formGroupBindingIds.value
  })
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
    const availabilitySchedule = availabilitySchedulePayload()
    if (availabilitySchedule === false) {
      return
    }
    const targetId = editingId.value
    const expiresAt = formatServerDateTimeInput(form.expiresAt)
    const payload = {
      name: form.name,
      groupRouteStrategy: form.groupRouteStrategy,
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
        message: '复制下方 API Key 和 Base URL；统计、会话亲和和缓存按本地 API Key 与分组保持连续。'
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
}
</style>
