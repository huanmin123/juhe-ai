<template>
  <a-modal
    v-model:open="modalOpen"
    :title="editingId ? '编辑 API Key' : '新建 API Key'"
    width="760px"
    :confirm-loading="apiKeySaving"
    :ok-button-props="{ type: 'primary', disabled: apiKeySaving }"
    @cancel="clearRouteStrategyOptionsSearchTimer"
    @ok="saveApiKey"
  >
    <a-alert v-if="!editingId && isManagementView && targetSystemAccountLabel" class="modal-alert" type="info" show-icon :message="`当前创建目标：${targetSystemAccountLabel}`" />
    <a-form layout="vertical" class="modal-form">
      <a-form-item label="名称" required :tooltip="editingNameLocked ? '默认 API Key 和 AI 对话 API Key 的名称不可修改。' : undefined">
        <a-input v-model:value="form.name" :disabled="editingNameLocked" placeholder="请输入 API Key 名称" />
      </a-form-item>
      <a-form-item label="策略路由" required tooltip="API Key 只绑定策略路由；分组、模型和供应商调度规则在策略路由中维护。">
        <RouteStrategySelect
          v-model:value="form.routeStrategyId"
          v-model:selected-strategy="form.routeStrategy"
          :disabled="editingIsDefault"
          :filter-option="false"
          :loading="routeStrategyOptionsLoading"
          :route-strategies="visibleRouteStrategyOptions"
          disable-inactive
          placeholder="选择策略路由"
          @dropdown-visible-change="handleRouteStrategyDropdown"
          @search="handleRouteStrategySearch"
          @update:value="markRouteStrategyTouched"
        />
      </a-form-item>
      <a-row :gutter="12">
        <a-col :span="12">
          <a-form-item label="运行状态" tooltip="API Key 只有一个运行状态；配置时间计划后，保存和计划边界都会直接更新该状态。">
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
      <div class="modal-section-title">美元额度</div>
      <RequestQuotaFields :model="form.quotaLimits" />
      <div class="modal-section-title">时间计划</div>
      <TimeScheduleSection :form="{ availabilitySchedule: form.availabilitySchedule }" label="API Key 时间计划" :bordered="false" />
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import type { Dayjs } from 'dayjs'

import type { useScopedApiKeysApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { getCachedUserReferenceData, loadUserReferenceData } from '@/composables/useUserReferenceData'
import RouteStrategySelect from '@/components/RouteStrategySelect.vue'
import { useSubmitAction } from '@/composables/useSubmitAction'
import { loadRouteStrategyOptionsResource } from '@/composables/useRouteStrategyOptionsResource'
import { message } from '@/lib/antd'
import { extractApiErrorMessage } from '@/shared/apiError'
import { formatServerDateTimeInput, parseStrictDatePickerValue } from '@/shared/formatters'
import type { RouteStrategySelection } from '@/shared/routeStrategyLabelCache'
import type {
  ApiKeyAvailabilitySchedule,
  ApiKeyMutationResult,
  ApiKeyQuotaLimits,
  ApiKeySummary,
  RouteStrategyOptionSummary
} from '@/types/domain'
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
import {
  buildApiKeyMutationPatch,
  hasApiKeyMutationChanges,
  type ApiKeyEditableSnapshot
} from './apiKeyMutation'
import type { ApiKeyScopeParams } from './apiKeyScope'
import { apiKeyStatusOptions as statusOptions } from './apiKeyTableConfig'

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
  (event: 'updated', result: ApiKeyMutationResult): void
}>()

const modalOpen = ref(false)
const editingId = ref<string>()
const editingIsDefault = ref(false)
const editingNameLocked = ref(false)
const editingSystemAccountId = ref<string>()
const { submitAction, submittingRef } = useSubmitAction('api-keys')
const apiKeySaving = submittingRef('api_keys.save')
const routeStrategyOptionsRaw = ref<RouteStrategyOptionSummary[]>([])
const routeStrategyOptionsLoading = ref(false)
const routeStrategyOptionsScopeKey = ref('')
const routeStrategyTouched = ref(false)
let routeStrategyOptionsRequestToken = 0
let routeStrategyOptionsLoadingKey: string | undefined
let routeStrategyOptionsLoadingPromise: Promise<void> | undefined
let routeStrategyOptionsSearchTimer: ReturnType<typeof window.setTimeout> | undefined
let editingBaseline: ApiKeyEditableSnapshot | undefined
let editingRevision: string | undefined

const form = reactive({
  name: '',
  routeStrategyId: '',
  routeStrategy: undefined as RouteStrategySelection | undefined,
  status: 'active' as 'active' | 'disabled',
  expiresAt: undefined as Dayjs | undefined,
  description: '',
  quotaLimits: createQuotaLimitForm(),
  availabilitySchedule: createApiKeyTimeScheduleForm()
})
const visibleRouteStrategyOptions = computed(() => (
  routeStrategyOptionsScopeKey.value === currentRouteStrategyOptionsScopeKey()
    ? routeStrategyOptionsRaw.value
    : []
))

function openCreate() {
  if (props.isManagementView && !props.scopeParams?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  const defaultStrategy = cachedDefaultRouteStrategy()
  editingId.value = undefined
  editingIsDefault.value = false
  editingNameLocked.value = false
  editingSystemAccountId.value = undefined
  editingRevision = undefined
  routeStrategyTouched.value = false
  Object.assign(form, {
    name: '',
    routeStrategyId: defaultStrategy?.id ?? '',
    routeStrategy: defaultStrategy,
    status: 'active',
    expiresAt: undefined,
    description: '',
    quotaLimits: createQuotaLimitForm(),
    availabilitySchedule: createApiKeyTimeScheduleForm()
  })
  editingBaseline = undefined
  modalOpen.value = true
  if (!defaultStrategy) prewarmCreateDefaultRouteStrategy()
}

function openEdit(apiKey: ApiKeySummary) {
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
  editingIsDefault.value = apiKey.isDefault === true && apiKey.purpose !== 'chat'
  editingNameLocked.value = apiKey.isDefault === true || apiKey.purpose === 'chat'
  editingSystemAccountId.value = editScopeParams?.systemAccountId
  editingRevision = apiKey.revision
  routeStrategyTouched.value = false
  Object.assign(form, {
    name: apiKey.name,
    routeStrategyId: apiKey.routeStrategyId,
    routeStrategy: apiKeyRouteStrategySelection(apiKey),
    status: apiKey.status,
    expiresAt,
    description: apiKey.description ?? '',
    quotaLimits,
    availabilitySchedule
  })
  editingBaseline = currentApiKeyEditableSnapshot(buildTimeSchedulePayload(form.availabilitySchedule))
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
  if (editingId.value && !form.routeStrategyId.trim()) {
    message.warning('请选择策略路由')
    return
  }
  if (!editingId.value && routeStrategyTouched.value && !form.routeStrategyId.trim()) {
    message.warning('请选择策略路由')
    return
  }
  try {
    const availabilitySchedule = availabilitySchedulePayload()
    if (availabilitySchedule === false) return
    const targetId = editingId.value
    const snapshot = currentApiKeyEditableSnapshot(availabilitySchedule)
    if (targetId) {
      if (!editingBaseline || !editingRevision) {
        message.error('API Key 编辑基线缺失，请关闭弹窗后重试')
        return
      }
      const patch = buildApiKeyMutationPatch(editingBaseline, snapshot)
      if (!hasApiKeyMutationChanges(patch)) {
        message.info('没有需要保存的修改')
        return
      }
      const result = await props.apiKeysApi.update(targetId, {
        expectedRevision: editingRevision,
        ...patch
      }, apiKeyOperationScopeParams())
      emit('updated', result)
      message.success('API Key 已更新')
    } else {
      const result = await props.apiKeysApi.create({
        name: snapshot.name,
        ...(routeStrategyTouched.value && snapshot.routeStrategyId
          ? { routeStrategyId: snapshot.routeStrategyId }
          : {}),
        status: snapshot.status,
        expiresAt: snapshot.expiresAt ?? undefined,
        description: snapshot.description,
        quotaLimits: snapshot.quotaLimits,
        availabilitySchedule: snapshot.availabilitySchedule
      }, props.scopeParams)
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

function currentApiKeyEditableSnapshot(
  availabilitySchedule: ApiKeyAvailabilitySchedule | null
): ApiKeyEditableSnapshot {
  return {
    name: form.name.trim(),
    routeStrategyId: form.routeStrategyId.trim() || undefined,
    status: form.status,
    expiresAt: formatServerDateTimeInput(form.expiresAt),
    description: form.description,
    quotaLimits: quotaLimitsPayload(),
    availabilitySchedule
  }
}

function cachedDefaultRouteStrategy(): RouteStrategySelection | undefined {
  const reference = getCachedUserReferenceData({
    viewScope: props.isManagementView ? 'admin' : 'self',
    systemAccountId: props.scopeParams?.systemAccountId
  })?.preferredDefaultRouteStrategy
  if (!reference || reference.status !== 'active') return undefined
  return {
    ...reference,
    isDefault: true
  }
}

function prewarmCreateDefaultRouteStrategy(): void {
  const params = {
    viewScope: props.isManagementView ? 'admin' as const : 'self' as const,
    systemAccountId: props.scopeParams?.systemAccountId
  }
  void loadUserReferenceData(params).then((referenceData) => {
    const reference = referenceData?.preferredDefaultRouteStrategy
    if (
      !reference
      || reference.status !== 'active'
      || !modalOpen.value
      || editingId.value
      || routeStrategyTouched.value
      || form.routeStrategyId
    ) {
      return
    }
    form.routeStrategyId = reference.id
    form.routeStrategy = { ...reference, isDefault: true }
  }).catch(() => undefined)
}

async function loadRouteStrategyOptions(keyword = '') {
  const operationScopeParams = apiKeyOperationScopeParams()
  if (props.isManagementView && !operationScopeParams?.systemAccountId) {
    routeStrategyOptionsRequestToken += 1
    routeStrategyOptionsLoadingKey = undefined
    routeStrategyOptionsLoadingPromise = undefined
    routeStrategyOptionsRaw.value = []
    routeStrategyOptionsLoading.value = false
    return
  }
  const requestKeyword = keyword.trim() || undefined
  const requestScopeKey = routeStrategyOptionsCatalogScopeKey(operationScopeParams?.systemAccountId)
  const requestKey = routeStrategyOptionsRequestKey(operationScopeParams?.systemAccountId, requestKeyword)
  if (routeStrategyOptionsLoadingKey === requestKey && routeStrategyOptionsLoadingPromise) {
    return routeStrategyOptionsLoadingPromise
  }
  const requestToken = ++routeStrategyOptionsRequestToken
  routeStrategyOptionsLoading.value = true
  routeStrategyOptionsLoadingKey = requestKey
  routeStrategyOptionsLoadingPromise = (async () => {
    try {
      await loadRouteStrategyOptionsResource({
        api: props.routeStrategiesApi,
        apply: (nextOptions) => {
          routeStrategyOptionsRaw.value = nextOptions
          routeStrategyOptionsScopeKey.value = requestScopeKey
        },
        isCurrent: () => requestToken === routeStrategyOptionsRequestToken
          && requestScopeKey === currentRouteStrategyOptionsScopeKey(),
        isManagementView: props.isManagementView,
        keyword: requestKeyword,
        systemAccountId: operationScopeParams?.systemAccountId
      })
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
    routeStrategyOptionsCatalogScopeKey(systemAccountId),
    keyword ?? ''
  ])
}

function currentRouteStrategyOptionsScopeKey(): string {
  return routeStrategyOptionsCatalogScopeKey(apiKeyOperationScopeParams()?.systemAccountId)
}

function routeStrategyOptionsCatalogScopeKey(systemAccountId: string | undefined): string {
  return props.isManagementView ? `management:${systemAccountId ?? ''}` : 'self'
}

function handleRouteStrategyDropdown(open: boolean) {
  if (open && !visibleRouteStrategyOptions.value.length) void loadRouteStrategyOptions()
}

function markRouteStrategyTouched(): void {
  routeStrategyTouched.value = true
}

function handleRouteStrategySearch(value: string) {
  clearRouteStrategyOptionsSearchTimer()
  routeStrategyOptionsSearchTimer = window.setTimeout(() => {
    routeStrategyOptionsSearchTimer = undefined
    void loadRouteStrategyOptions(value)
  }, 250)
}

function clearRouteStrategyOptionsSearchTimer() {
  if (routeStrategyOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(routeStrategyOptionsSearchTimer)
    routeStrategyOptionsSearchTimer = undefined
  }
}

function apiKeyRouteStrategySelection(apiKey: ApiKeySummary): RouteStrategySelection | undefined {
  const id = apiKey.routeStrategyId.trim()
  const name = apiKey.routeStrategyName?.trim()
  if (!id || !name) return undefined
  return {
    id,
    name,
    mode: apiKey.routeStrategyMode,
    status: apiKey.routeStrategyStatus,
    systemAccountName: apiKey.systemAccountName
  }
}

onBeforeUnmount(() => {
  clearRouteStrategyOptionsSearchTimer()
})

defineExpose({ openCreate, openEdit })
</script>

<style scoped>
.modal-alert {
  margin-bottom: 12px;
}

.modal-form {
  max-height: 72vh;
  overflow-y: auto;
  overflow-x: hidden;
  padding-right: 4px;
}

.modal-section-title {
  margin: 18px 0 10px;
  color: #0f172a;
  font-size: 14px;
  font-weight: 700;
}

.full-width-control {
  width: 100%;
}
</style>
