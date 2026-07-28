<template>
  <a-modal
    v-model:open="modalOpen"
    :title="editingId ? '编辑 API Key' : '新建 API Key'"
    width="760px"
    :confirm-loading="apiKeySaving"
    :ok-button-props="{ type: 'primary', disabled: apiKeySaving }"
    @after-close="handleModalAfterClose"
    @cancel="handleModalCancel"
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
import { computed, onBeforeUnmount, reactive, ref, shallowRef, watch } from 'vue'
import type { Dayjs } from 'dayjs'

import type { useScopedApiKeysApi, useScopedRouteStrategiesApi } from '@/composables/useScopedDomainApi'
import { getCachedUserReferenceData } from '@/composables/useUserReferenceData'
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

interface ApiKeyEditModalSession {
  generation: number
  isManagementView: boolean
  parentScopeKey: string
  routeStrategyOptionsScopeKey: string
  systemAccountId?: string
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
let modalSessionGeneration = 0
const activeModalSession = shallowRef<ApiKeyEditModalSession>()

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
  activeModalSession.value
  && modalOpen.value
  && routeStrategyOptionsScopeKey.value === activeModalSession.value.routeStrategyOptionsScopeKey
    ? routeStrategyOptionsRaw.value
    : []
))

function openCreate() {
  const createScopeParams = normalizedScopeParams(props.scopeParams)
  if (props.isManagementView && !createScopeParams?.systemAccountId) {
    message.warning('请先在右侧选择目标系统账户，再创建 API Key')
    return
  }
  const defaultStrategy = cachedDefaultRouteStrategy(createScopeParams)
  editingId.value = undefined
  editingIsDefault.value = false
  editingNameLocked.value = false
  editingRevision = undefined
  routeStrategyTouched.value = false
  beginModalSession(createScopeParams)
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
  editingRevision = apiKey.revision
  routeStrategyTouched.value = false
  beginModalSession(editScopeParams)
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
    || props.scopeParams?.systemAccountId?.trim()
  return systemAccountId ? { systemAccountId } : undefined
}

const saveApiKey = submitAction('api_keys.save', async () => {
  const modalSession = activeModalSession.value
  if (!modalSession || !isCurrentModalSession(modalSession)) {
    message.error('API Key 弹窗作用域已变化，请重新打开后再保存')
    return
  }
  const operationScopeParams = modalSessionScopeParams(modalSession)
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
      }, operationScopeParams)
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
      }, operationScopeParams)
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

function cachedDefaultRouteStrategy(scopeParams: ApiKeyScopeParams | undefined): RouteStrategySelection | undefined {
  const reference = getCachedUserReferenceData({
    viewScope: props.isManagementView ? 'admin' : 'self',
    systemAccountId: scopeParams?.systemAccountId
  })?.preferredDefaultRouteStrategy
  if (!reference || reference.status !== 'active') return undefined
  return {
    ...reference,
    isDefault: true
  }
}

async function loadRouteStrategyOptions(keyword = '') {
  const modalSession = activeModalSession.value
  if (!modalSession || !isCurrentModalSession(modalSession)) return
  const operationScopeParams = modalSessionScopeParams(modalSession)
  if (modalSession.isManagementView && !operationScopeParams?.systemAccountId) return
  const requestKeyword = keyword.trim() || undefined
  const requestScopeKey = modalSession.routeStrategyOptionsScopeKey
  const requestKey = routeStrategyOptionsRequestKey(modalSession, requestKeyword)
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
          && isCurrentModalSession(modalSession)
          && requestScopeKey === modalSession.routeStrategyOptionsScopeKey,
        isManagementView: modalSession.isManagementView,
        keyword: requestKeyword,
        systemAccountId: operationScopeParams?.systemAccountId
      })
    } catch (error) {
      if (requestToken !== routeStrategyOptionsRequestToken || !isCurrentModalSession(modalSession)) return
      message.error(extractApiErrorMessage(error, '策略路由选项加载失败'))
    } finally {
      if (requestToken === routeStrategyOptionsRequestToken && routeStrategyOptionsLoadingKey === requestKey) {
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

function routeStrategyOptionsRequestKey(modalSession: ApiKeyEditModalSession, keyword: string | undefined): string {
  return JSON.stringify([
    modalSession.routeStrategyOptionsScopeKey,
    keyword ?? ''
  ])
}

function routeStrategyOptionsCatalogScopeKey(isManagementView: boolean, systemAccountId: string | undefined): string {
  return isManagementView ? `management:${systemAccountId ?? ''}` : 'self'
}

function handleRouteStrategyDropdown(open: boolean) {
  if (open && !visibleRouteStrategyOptions.value.length) void loadRouteStrategyOptions()
}

function markRouteStrategyTouched(): void {
  routeStrategyTouched.value = true
}

function handleRouteStrategySearch(value: string) {
  const modalSession = activeModalSession.value
  if (!modalSession || !isCurrentModalSession(modalSession)) return
  clearRouteStrategyOptionsSearchTimer()
  routeStrategyOptionsSearchTimer = window.setTimeout(() => {
    routeStrategyOptionsSearchTimer = undefined
    if (!isCurrentModalSession(modalSession)) return
    void loadRouteStrategyOptions(value)
  }, 250)
}

function clearRouteStrategyOptionsSearchTimer() {
  if (routeStrategyOptionsSearchTimer && typeof window !== 'undefined') {
    window.clearTimeout(routeStrategyOptionsSearchTimer)
    routeStrategyOptionsSearchTimer = undefined
  }
}

function beginModalSession(scopeParams: ApiKeyScopeParams | undefined): void {
  invalidateModalSession({ clearOptions: true })
  const systemAccountId = scopeParams?.systemAccountId?.trim() || undefined
  activeModalSession.value = {
    generation: ++modalSessionGeneration,
    isManagementView: props.isManagementView,
    parentScopeKey: currentParentScopeKey(),
    routeStrategyOptionsScopeKey: routeStrategyOptionsCatalogScopeKey(props.isManagementView, systemAccountId),
    systemAccountId
  }
}

function invalidateModalSession(options: { clearOptions?: boolean } = {}): void {
  modalSessionGeneration += 1
  activeModalSession.value = undefined
  routeStrategyOptionsRequestToken += 1
  routeStrategyOptionsLoadingKey = undefined
  routeStrategyOptionsLoadingPromise = undefined
  routeStrategyOptionsLoading.value = false
  clearRouteStrategyOptionsSearchTimer()
  if (options.clearOptions) {
    routeStrategyOptionsRaw.value = []
    routeStrategyOptionsScopeKey.value = ''
  }
}

function isCurrentModalSession(modalSession: ApiKeyEditModalSession): boolean {
  return modalOpen.value
    && activeModalSession.value === modalSession
    && modalSession.generation === modalSessionGeneration
    && modalSession.parentScopeKey === currentParentScopeKey()
}

function modalSessionScopeParams(modalSession: ApiKeyEditModalSession): ApiKeyScopeParams | undefined {
  return modalSession.systemAccountId ? { systemAccountId: modalSession.systemAccountId } : undefined
}

function normalizedScopeParams(scopeParams: ApiKeyScopeParams | undefined): ApiKeyScopeParams | undefined {
  const systemAccountId = scopeParams?.systemAccountId?.trim()
  return systemAccountId ? { systemAccountId } : undefined
}

function currentParentScopeKey(): string {
  return routeStrategyOptionsCatalogScopeKey(props.isManagementView, props.scopeParams?.systemAccountId?.trim())
}

function handleModalCancel(): void {
  invalidateModalSession({ clearOptions: true })
}

function handleModalAfterClose(): void {
  if (!modalOpen.value) invalidateModalSession({ clearOptions: true })
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

watch(currentParentScopeKey, (nextScopeKey, previousScopeKey) => {
  if (nextScopeKey === previousScopeKey) return
  invalidateModalSession({ clearOptions: true })
  modalOpen.value = false
})

onBeforeUnmount(() => invalidateModalSession({ clearOptions: true }))

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
