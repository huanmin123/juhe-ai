<template>
  <a-modal
    v-model:open="modalOpen"
    :title="editingId ? '编辑 API Key' : '新建 API Key'"
    width="760px"
    :confirm-loading="apiKeySaving"
    :ok-button-props="{ type: 'primary', disabled: apiKeySaving }"
    @ok="saveApiKey"
  >
    <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
    <a-form layout="vertical" class="modal-form">
      <a-form-item label="名称" required>
        <a-input v-model:value="form.name" placeholder="请输入 API Key 名称" />
      </a-form-item>
      <a-form-item label="策略路由" required tooltip="API Key 只绑定策略路由；分组、模型和供应商调度规则在策略路由中维护。">
        <a-select
          v-model:value="form.routeStrategyId"
          show-search
          :filter-option="false"
          :loading="routeStrategyOptionsLoading"
          :options="routeStrategyOptions"
          placeholder="选择策略路由"
          @dropdown-visible-change="handleRouteStrategyDropdown"
          @search="handleRouteStrategySearch"
        />
      </a-form-item>
      <a-row :gutter="12">
        <a-col :span="12">
          <a-form-item label="状态">
            <a-select v-model:value="form.status" :options="statusOptions" />
          </a-form-item>
        </a-col>
        <a-col :span="12">
          <a-form-item label="过期时间">
            <a-date-picker v-model:value="form.expiresAt" show-time allow-clear class="full-width-control" />
          </a-form-item>
        </a-col>
      </a-row>
      <a-form-item label="说明">
        <a-textarea v-model:value="form.description" :rows="2" placeholder="可选" />
      </a-form-item>
      <a-divider orientation="left">美元额度</a-divider>
      <RequestQuotaFields :model="form.quotaLimits" />
      <a-divider orientation="left">时间计划</a-divider>
      <TimeScheduleSection :form="{ availabilitySchedule: form.availabilitySchedule }" label="API Key 时间计划" />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import type { Dayjs } from 'dayjs'

import type { useScopedApiKeysApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type { ApiKeyAvailabilitySchedule, ApiKeyQuotaLimits, ApiKeySummary, RouteStrategyOptionSummary } from '@/types/domain'
import RequestQuotaFields from '@/views/shared/RequestQuotaFields.vue'
import { createQuotaLimitForm, quotaLimitsPayload as buildQuotaLimitsPayload } from '@/views/shared/requestQuotaForm'
import {
  buildTimeSchedulePayload,
  validateTimeScheduleForm
} from '@/views/shared/timeSchedule'
import TimeScheduleSection from '@/views/shared/TimeScheduleSection.vue'
import {
  createApiKeyTimeScheduleForm,
  type ApiKeyAvailabilityScheduleForm
} from './apiKeyFormModel'
import { apiKeyStatusOptions as statusOptions } from './apiKeyTableConfig'
import type { ApiKeyScopeParams } from './useApiKeyGroupOptions'

type ScopedApiKeysApi = ReturnType<typeof useScopedApiKeysApi>
type ScopedRouteStrategiesApi = ReturnType<typeof useScopedRouteStrategiesApi>

interface CreatedKeyPayload {
  key: string
  title: string
  message: string
}

const props = defineProps<{
  apiKeysApi: Pick<ScopedApiKeysApi, 'create' | 'update'>
  routeStrategiesApi: Pick<ScopedRouteStrategiesApi, 'options'>
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
const routeStrategyOptionsRaw = ref<RouteStrategyOptionSummary[]>([])
const routeStrategyOptionsLoading = ref(false)

const form = reactive({
  name: '',
  routeStrategyId: '',
  status: 'active' as 'active' | 'disabled',
  expiresAt: undefined as Dayjs | undefined,
  description: '',
  quotaLimits: createQuotaLimitForm(),
  availabilitySchedule: createApiKeyTimeScheduleForm()
})

const routeStrategyOptions = computed(() => routeStrategyOptionsRaw.value.map((strategy) => ({
  label: `${strategy.name}（${routeStrategyModeText(strategy.mode)}）`,
  value: strategy.id,
  disabled: strategy.status !== 'active'
})))

async function openCreate() {
  if (props.isManagementView && !props.scopeParams?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  editingId.value = undefined
  editingSystemAccountId.value = undefined
  Object.assign(form, {
    name: '',
    routeStrategyId: '',
    status: 'active',
    expiresAt: undefined,
    description: '',
    quotaLimits: createQuotaLimitForm(),
    availabilitySchedule: createApiKeyTimeScheduleForm()
  })
  await loadRouteStrategyOptions()
  if (routeStrategyOptionsRaw.value.length === 1) {
    form.routeStrategyId = routeStrategyOptionsRaw.value[0].id
  }
  modalOpen.value = true
}

async function openEdit(apiKey: ApiKeySummary) {
  const editScopeParams = apiKeyOperationScopeParams(apiKey)
  if (props.isManagementView && !editScopeParams?.systemAccountId) {
    message.warning('无法确定 API Key 归属系统账户，请刷新后重试')
    return
  }
  let quotaLimits: ReturnType<typeof createQuotaLimitForm>
  let expiresAt: Dayjs | undefined
  let availabilitySchedule: ApiKeyAvailabilityScheduleForm
  try {
    quotaLimits = createQuotaLimitForm(apiKey.quotaLimits)
    expiresAt = parseStrictDatePickerValue(apiKey.expiresAt, 'API Key 过期时间')
    availabilitySchedule = createApiKeyTimeScheduleForm(apiKey.availabilitySchedule)
  } catch (error) {
    message.error(extractApiErrorMessage(error, 'API Key 数据结构异常，请清理后再编辑'))
    return
  }
  editingId.value = apiKey.id
  editingSystemAccountId.value = editScopeParams?.systemAccountId
  Object.assign(form, {
    name: apiKey.name,
    routeStrategyId: apiKey.routeStrategyId,
    status: apiKey.status,
    expiresAt,
    description: apiKey.description ?? '',
    quotaLimits,
    availabilitySchedule
  })
  await loadRouteStrategyOptions('', [apiKey.routeStrategyId])
  modalOpen.value = true
}

function apiKeyOperationScopeParams(apiKey?: Pick<ApiKeySummary, 'systemAccountId'>): ApiKeyScopeParams {
  const systemAccountId = apiKey?.systemAccountId?.trim()
    || editingSystemAccountId.value
    || props.scopeParams?.systemAccountId
  return systemAccountId ? { systemAccountId } : undefined
}

const saveApiKey = submitAction('api_keys.save', async () => {
  if (!form.name.trim()) {
    message.warning('请填写名称')
    return
  }
  if (!form.routeStrategyId.trim()) {
    message.warning('请选择策略路由')
    return
  }
  try {
    const availabilitySchedule = availabilitySchedulePayload()
    if (availabilitySchedule === false) return
    const targetId = editingId.value
    const expiresAt = formatServerDateTimeInput(form.expiresAt)
    const payload = {
      name: form.name.trim(),
      routeStrategyId: form.routeStrategyId,
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
    message.error(extractApiErrorMessage(error, '保存 API Key 失败'))
  }
})

function quotaLimitsPayload(): ApiKeyQuotaLimits {
  return buildQuotaLimitsPayload(form.quotaLimits)
}

function availabilitySchedulePayload(): ApiKeyAvailabilitySchedule | null | false {
  const validationMessage = validateTimeScheduleForm(form.availabilitySchedule)
  if (validationMessage) {
    message.warning(validationMessage)
    return false
  }
  return buildTimeSchedulePayload(form.availabilitySchedule)
}

async function loadRouteStrategyOptions(keyword = '', ids?: string[]) {
  routeStrategyOptionsLoading.value = true
  try {
    routeStrategyOptionsRaw.value = await props.routeStrategiesApi.options({
      keyword: keyword.trim() || undefined,
      ids,
      limit: 100,
      systemAccountId: apiKeyOperationScopeParams()?.systemAccountId
    })
  } catch (error) {
    message.error(extractApiErrorMessage(error, '策略路由选项加载失败'))
  } finally {
    routeStrategyOptionsLoading.value = false
  }
}

function handleRouteStrategyDropdown(open: boolean) {
  if (open && !routeStrategyOptionsRaw.value.length) void loadRouteStrategyOptions()
}

function handleRouteStrategySearch(value: string) {
  void loadRouteStrategyOptions(value)
}

function routeStrategyModeText(mode: string | undefined): string {
  if (mode === 'hybrid_smart') return '混合智能路由'
  if (mode === 'weighted') return '权重调度路由'
  if (mode === 'round_robin') return '轮询路由'
  if (mode === 'failover') return '故障回退路由'
  return '普通路由'
}

defineExpose({ openCreate, openEdit })
</script>

<style scoped>
.modal-alert {
  margin-bottom: 12px;
}

.modal-form {
  max-height: 72vh;
  overflow-y: auto;
  padding-right: 4px;
}

.full-width-control {
  width: 100%;
}
</style>
