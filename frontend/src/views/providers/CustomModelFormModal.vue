<template>
  <a-modal
    :open="open"
    :title="customModelModalTitle"
    width="820px"
    :confirm-loading="saving"
    @ok="saveCustomModel"
    @cancel="handleCancel"
  >
    <a-form layout="vertical" class="custom-model-form">
      <div class="custom-model-grid">
        <a-divider class="custom-model-grid-wide" orientation="left">基本信息</a-divider>
        <a-form-item v-if="isManagementView && !editingBuiltInModel" label="作用域" class="custom-model-grid-wide" extra="个人模型只对所选用户可见；全局模型对所有用户可见。">
          <a-radio-group v-model:value="customModelForm.scope" :disabled="customModelEditing" button-style="solid" @change="handleCustomModelScopeChange">
            <a-radio-button value="personal">{{ selectedModelOwnerLabel }}个人模型</a-radio-button>
            <a-radio-button value="global">全局模型</a-radio-button>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="模型 ID" required extra="必须与上游接口要求的 model 完全一致，创建后不可修改。">
          <a-input v-model:value="customModelForm.model" :disabled="customModelEditing" placeholder="例如 gpt-5.5-pro" />
        </a-form-item>
        <a-form-item label="状态" extra="启用后进入可用模型目录；草稿和停用状态不会参与正常调用。">
          <a-select v-model:value="customModelForm.status" :options="customModelStatusOptions" />
        </a-form-item>
        <a-form-item v-if="!customModelEditing" label="配置模板" extra="复制同一供应商已有模型的用途、协议、能力、容量和价格；模型 ID 不会被复制。">
          <a-select
            v-model:value="customModelForm.configurationTemplateId"
            show-search
            option-filter-prop="label"
            :options="configurationTemplateOptions"
            placeholder="选择当前供应商的已有模型"
            @change="handleConfigurationTemplateChange"
          />
        </a-form-item>
        <a-form-item label="用途" extra="决定模型所在目录分类、默认接口协议和可填写的计费字段。">
          <a-select v-model:value="customModelForm.mode" :options="customModelModeOptions" @change="handleCustomModelModeChange" />
        </a-form-item>
        <a-divider class="custom-model-grid-wide" orientation="left">接口与能力</a-divider>
        <a-form-item label="接口协议" class="custom-model-grid-wide" extra="只选择上游模型真实支持的接口；该配置会影响账号、映射和路由的可选范围。">
          <a-select
            v-model:value="customModelForm.supportedApiProtocols"
            mode="multiple"
            :options="customModelApiProtocolOptions"
            placeholder="不确定时可留空"
          />
        </a-form-item>
        <template v-if="!editingBuiltInModel">
          <a-form-item label="输入模态" class="custom-model-grid-wide" extra="声明模型可接收的输入类型；留空表示未声明。">
            <a-select
              v-model:value="customModelForm.inputModalities"
              mode="multiple"
              :options="modelModalityOptions"
              placeholder="未声明时可留空"
            />
          </a-form-item>
          <a-form-item label="输出模态" class="custom-model-grid-wide" extra="声明模型可产出的输出类型；留空表示未声明。">
            <a-select
              v-model:value="customModelForm.outputModalities"
              mode="multiple"
              :options="modelModalityOptions"
              placeholder="未声明时可留空"
            />
          </a-form-item>
          <a-form-item
            v-for="protocol in customModelForm.supportedApiProtocols"
            :key="protocol"
            class="custom-model-grid-wide"
            :label="`工具矩阵 · ${formatApiProtocol(protocol)}`"
            extra="声明该接口协议下可用的模型工具（如函数调用、联网搜索）；留空表示未声明。"
          >
            <a-select
              v-model:value="customModelForm.supportedToolsByProtocol[protocol]"
              mode="multiple"
              :options="modelToolOptions"
              placeholder="未声明时可留空"
            />
          </a-form-item>
        </template>
        <template v-if="showCustomModelRequestCapabilities">
          <a-form-item v-if="showCustomModelServiceTiers" label="服务等级" class="custom-model-grid-wide" extra="只选择供应商真实支持且已配置独立价格的请求档位；不同供应商的可选值不强行统一。">
            <a-select
              v-model:value="customModelForm.supportedServiceTiers"
              :mode="customModelCapabilitySelectMode"
              :options="customModelServiceTierOptions"
              placeholder="未选择时表示不支持账户级服务等级覆盖"
              @change="handleCustomModelServiceTiersChange"
            />
          </a-form-item>
          <a-form-item v-if="showCustomModelReasoningEfforts" label="思考能力" class="custom-model-grid-wide" extra="只填写上游支持的思考强度值；关闭思考不属于级别，因此不显示 none。">
            <a-select
              v-model:value="customModelForm.supportedReasoningEfforts"
              :mode="customModelCapabilitySelectMode"
              :options="customModelReasoningEffortOptions"
              placeholder="仅选择上游真实支持的思考强度"
              @change="normalizeCustomModelRequestCapabilities(customModelForm)"
            />
          </a-form-item>
          <a-form-item v-if="editingBuiltInModel && targetProvider?.code !== 'gpt'" label="默认思考级别" class="custom-model-grid-wide">
            <a-select
              v-model:value="customModelForm.defaultReasoningEffort"
              allow-clear
              :options="customModelDefaultReasoningEffortOptions"
              :disabled="!customModelForm.supportedReasoningEfforts.length"
              placeholder="从已支持的思考级别中选择"
            />
          </a-form-item>
        </template>
        <a-divider class="custom-model-grid-wide" orientation="left">生命周期与容量</a-divider>
        <a-form-item label="发布时间" extra="模型首次公开可用的日期，用于模型目录排序。">
          <a-date-picker v-model:value="customModelForm.releaseDate" value-format="YYYY-MM-DD" placeholder="选择发布日期" style="width: 100%" />
        </a-form-item>
        <a-form-item label="停用日期" extra="供应商计划停止提供该模型的日期；没有明确日期时留空。">
          <a-date-picker v-model:value="customModelForm.shutdownDate" value-format="YYYY-MM-DD" placeholder="选择停用日期" style="width: 100%" />
        </a-form-item>
        <a-form-item label="上下文" extra="一次请求可使用的输入与输出 Token 总上限。">
          <a-input-number v-model:value="customModelForm.contextWindowTokens" :min="0" style="width: 100%" />
        </a-form-item>
        <a-form-item label="最大输入" extra="单次请求允许的最大输入 Token；供应商未单独公布时留空。">
          <a-input-number v-model:value="customModelForm.maxInputTokens" :min="0" style="width: 100%" />
        </a-form-item>
        <a-form-item label="最大输出" extra="单次请求允许生成的最大输出 Token。">
          <a-input-number v-model:value="customModelForm.maxOutputTokens" :min="0" style="width: 100%" />
        </a-form-item>
        <template v-if="canManageModelPrices">
          <a-divider class="custom-model-grid-wide" orientation="left">{{ customModelPriceSectionTitle }}</a-divider>
          <template v-if="customModelPricingCategory === 'text'">
            <a-form-item v-for="field in customModelDirectPriceFields" :key="field.key" :label="field.label" :extra="field.description">
              <a-input-number v-model:value="customModelForm[field.key]" :min="0" style="width: 100%" />
            </a-form-item>
            <template v-for="tier in customModelForm.supportedServiceTiers" :key="tier">
              <a-divider class="custom-model-grid-wide" orientation="left">{{ formatModelServiceTier(tier) }} · USD / 1M Token</a-divider>
              <a-form-item v-for="field in customModelTierPriceFieldOptions" :key="`${tier}-${field.key}`" :label="field.label" :extra="field.description">
                <a-input-number v-model:value="customModelForm.serviceTierPrices[tier][field.key]" :min="0" style="width: 100%" />
              </a-form-item>
            </template>
          </template>
          <template v-else>
            <a-form-item v-for="field in customModelDirectPriceFields" :key="field.key" :label="field.label" :extra="field.description">
              <a-input-number v-model:value="customModelForm[field.key]" :min="0" style="width: 100%" />
            </a-form-item>
          </template>
        </template>
      </div>
    </a-form>
  </a-modal>
</template>

<script lang="ts">
// CustomModelFormModal：供应商自定义/内置模型创建与编辑表单（自 ProvidersView 抽取，
// 模型广场页与后续挂载方共用）。纯逻辑域在 ./customProviderModelForm，本文件只承
// 接 UI、editing 状态与 api 调用；普通 script 块导出表单的薄层组装纯函数与契约类型。
// 注：vitest 未接入 @vitejs/plugin-vue，.vue 导出无法在单测导入；本块函数按
// UsageSummaryTags.test.ts 惯例由 customModelFormModal.test.ts 做源码级锚点断言。
import type { PrincipalSelection } from '@/shared/principalLabelCache'
import type {
  CustomProviderModelScope,
  ProviderModelMutationResult,
  ProviderModelPricing,
  ProviderModelsParams,
  ProviderModelUpsertPayload,
  ProviderProtocolProfileDefinition,
  SystemAccountPrincipalSummary
} from '@/types/domain'
import { buildCustomModelMutationPatch, hasCustomModelMutationChanges } from './customProviderModelForm'

/** 表单目标供应商：最小契约 { code, name, enabled }；传入完整 ProviderDefinition 的
 * 协议派生字段（protocolCode/protocolProfiles/defaultProtocolProfileId）时，目录缺行
 * 场景下的默认接口协议 fallback 保留 profile 派生语义，否则退化为通用默认。 */
export interface CustomModelFormTargetProvider {
  code: string
  name: string
  enabled: boolean
  protocolCode?: string
  protocolProfiles?: ProviderProtocolProfileDefinition[]
  defaultProtocolProfileId?: string
}

/** saved 事件负载：created 携带创建结果行；updated 携带按 props.models 就地合并变更后
 * 的完整行（父级替换同 id 行即可）；deleted 携带被删行。created/deleted 后父级重载，
 * updated 后父级就地更新（模型广场页与供应商目录退役设计 §4.4 刷新语义）。 */
export interface ProviderModelFormSavedPayload {
  kind: 'created' | 'updated' | 'deleted'
  result: ProviderModelPricing
}

/** 编辑入口门控：custom 行需有 id；built_in 行仅管理面可编辑（write_handlers.go admin-only 的前端镜像）。 */
export function canEditProviderModelRecord(record: ProviderModelPricing, isManagementView: boolean): boolean {
  if (!record.id) return false
  if (record.scope === 'built_in' && !isManagementView) return false
  return true
}

/** 创建请求 query：管理面 personal 行 pin 归属用户；global 与用户面不传（后端 pin 本人）。
 * scope 缺失按 personal 同路径处理（与原 ProvidersView `payload.scope === 'global'` 判定一致）。 */
export function providerModelOperationQueryParams(
  isManagementView: boolean,
  scope: CustomProviderModelScope | undefined,
  systemAccountId: string
): Pick<ProviderModelsParams, 'systemAccountId' | 'viewScope'> | undefined {
  if (!isManagementView || scope === 'global') return undefined
  const trimmed = systemAccountId.trim()
  return trimmed ? { viewScope: 'admin', systemAccountId: trimmed } : { viewScope: 'admin' }
}

export interface ProviderModelEditPatch {
  expectedUpdatedAt: string
  patch: Partial<ProviderModelUpsertPayload>
}

/** 编辑差量 PATCH：基线与当前表单载荷比对出仅变更字段，并携带乐观锁版本；无变更返回 undefined。 */
export function buildProviderModelEditPatch(
  baseline: Partial<ProviderModelUpsertPayload>,
  current: Partial<ProviderModelUpsertPayload>,
  expectedUpdatedAt: string
): ProviderModelEditPatch | undefined {
  const patch = buildCustomModelMutationPatch(baseline, current)
  if (!hasCustomModelMutationChanges(patch)) return undefined
  return { expectedUpdatedAt, patch }
}

/** 编辑后就地合并：patch 的 null 归一为 undefined 后覆盖基线行，再以服务端结果回写
 * status/updatedAt（ProvidersView applyProviderModelMutationResult 的单行语义）。 */
export function mergeProviderModelMutationResult(
  models: ProviderModelPricing[],
  result: ProviderModelMutationResult,
  patch: Partial<ProviderModelUpsertPayload>
): ProviderModelPricing | undefined {
  const normalizedPatch = Object.fromEntries(Object.entries(patch).map(([key, value]) => [
    key,
    value === null ? undefined : value
  ]))
  const current = models.find((item) => item.id === result.id)
  if (!current) return undefined
  return { ...current, ...normalizedPatch, status: result.status, updatedAt: result.updatedAt } as ProviderModelPricing
}

/** 上游错误信息透出：优先取响应体 message（409 绑定 guard 等业务错误原文），否则用 fallback。 */
export function extractModelErrorMessage(error: unknown, fallback: string): string {
  if (typeof error !== 'object' || error === null) return fallback
  const response = (error as { response?: { data?: { message?: unknown } } }).response
  const messageText = response?.data?.message
  return typeof messageText === 'string' && messageText.trim() ? messageText.trim() : fallback
}
</script>

<script setup lang="ts">
import { Modal } from 'ant-design-vue'
import { computed, reactive, ref, watch } from 'vue'

import { api } from '@/api/client'
import { authState } from '@/composables/useAuth'
import { message } from '@/lib/antd'
import { principalLabelForId } from '@/shared/principalLabelCache'
import type { ProviderDefinition, ProviderModelApiProtocol, ProviderModelMode } from '@/types/domain'
import {
  applyConfigurationTemplateToCustomModelForm,
  availableCustomModelModeOptions,
  availableCustomModelStatusOptions,
  buildCustomModelCapabilityOptions,
  buildCustomModelPayload as buildCustomModelUpsertPayload,
  canManageModelPricesForView,
  clearCustomModelGptCapabilities,
  clearCustomModelPricesOutsideCategory,
  createCustomModelFormFromPricing,
  emptyCustomModelForm,
  normalizeCustomModelRequestCapabilities,
  reconcileCustomModelServiceTierPrices,
  type CustomModelForm
} from './customProviderModelForm'
import {
  apiProtocolOptions,
  categoryFromModeOrModel,
  customModelPriceFields,
  customModelTierPriceFields,
  defaultProtocolsForProviderModelCategory,
  formatApiProtocol,
  formatModelServiceTier,
  getModelCategory,
  modelModalityOptions,
  modelToolOptions,
  type ModelCategoryKey
} from './providerModelFormatters'
import { buildConfigurationTemplateOptions } from './providerModelTableState'

const props = defineProps<{
  open: boolean
  /** 目标供应商；openCreate 的入参优先于此声明式镜像（watch 同步）。 */
  provider: CustomModelFormTargetProvider | null
  /** 该供应商当前全量目录行：配置模板 options、能力并集与编辑基线来源。 */
  models: ProviderModelPricing[]
  isManagementView: boolean
  /** 广场 providers 摘要携带的默认检查模型；表单当前未消费，与卡片/抽屉徽标同源透传。 */
  defaultHealthCheckModel?: string
  /** 管理面归属选项与当前归属筛选：personal 创建的归属 pin 与「{归属}个人模型」文案来源。 */
  systemAccounts?: SystemAccountPrincipalSummary[]
  systemAccountFilter?: string
  systemAccountFilterSelection?: PrincipalSelection
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  saved: [payload: ProviderModelFormSavedPayload]
  cancel: []
}>()

const saving = ref(false)
const deleting = ref(false)
const targetProvider = ref<CustomModelFormTargetProvider | null>(props.provider)
const editingCustomModelId = ref<string>()
const editingCustomModelProviderCode = ref<string>()
const editingModelScope = ref<ProviderModelPricing['scope']>()
const editingOriginalStatus = ref<ProviderModelPricing['status']>()
const editingCustomModelBaseline = ref<Partial<ProviderModelUpsertPayload>>()
const editingExpectedUpdatedAt = ref<string>()

const customModelForm = reactive<CustomModelForm>({ ...emptyCustomModelForm })

watch(() => props.provider, (value) => {
  targetProvider.value = value
})
watch(() => props.open, (open) => {
  if (!open) resetCustomModelForm()
})
// 能力声明矩阵键必须 ⊆ 当前接口协议（后端校验同款约束）：协议变化（含配置模板
// 改写协议）时清除不在集合内的矩阵键；键内工具数组不随协议变化强改。
watch(() => [...customModelForm.supportedApiProtocols], (protocols) => {
  const allowed = new Set<string>(protocols)
  for (const protocol of Object.keys(customModelForm.supportedToolsByProtocol)) {
    if (!allowed.has(protocol)) delete customModelForm.supportedToolsByProtocol[protocol]
  }
})

const customModelEditing = computed(() => Boolean(editingCustomModelId.value))
const editingBuiltInModel = computed(() => editingModelScope.value === 'built_in')
const canManageModelPrices = computed(() => canManageModelPricesForView(props.isManagementView, authState.isAdmin.value))
const systemAccountFilterValue = computed(() => (props.systemAccountFilter ?? '').trim())

const customModelModalTitle = computed(() => editingBuiltInModel.value ? '编辑内置模型' : customModelEditing.value ? '编辑自定义模型' : '新增自定义模型')
const customModelPricingCategory = computed<ModelCategoryKey>(() => categoryFromModeOrModel(customModelForm.mode, customModelForm.model))
const customModelTargetProviderCode = computed(() => editingCustomModelProviderCode.value ?? targetProvider.value?.code ?? '')
const customModelDirectPriceFields = computed(() => customModelPriceFields(customModelTargetProviderCode.value, customModelPricingCategory.value))
const customModelTierPriceFieldOptions = computed(() => customModelTierPriceFields(customModelTargetProviderCode.value))
const customModelPriceSectionTitle = computed(() => {
  if (customModelPricingCategory.value === 'image') return '图像计费 · USD'
  if (customModelPricingCategory.value === 'audio') return '音频计费 · USD'
  return 'Token 计费 · USD / 1M Token'
})
const customModelCategoryRecords = computed(() => props.models.filter((item) => getModelCategory(item) === customModelPricingCategory.value))
const customModelCapabilityOptions = computed(() => buildCustomModelCapabilityOptions(
  targetProvider.value?.code ?? '',
  customModelCategoryRecords.value.flatMap((item) => item.supportedServiceTiers ?? []),
  customModelCategoryRecords.value.flatMap((item) => item.supportedReasoningEfforts ?? [])
))
const customModelServiceTierOptions = computed(() => customModelCapabilityOptions.value.serviceTiers)
const customModelReasoningEffortOptions = computed(() => customModelCapabilityOptions.value.reasoningEfforts)
const customModelCapabilitySelectMode = computed(() => customModelTargetProviderCode.value === 'gpt' ? 'multiple' : 'tags')
const customModelDefaultReasoningEffortOptions = computed(() => customModelReasoningEffortOptions.value.filter((option) => (
  customModelForm.supportedReasoningEfforts.includes(option.value)
)))
const showCustomModelServiceTiers = computed(() => customModelPricingCategory.value === 'text' && customModelServiceTierOptions.value.length > 0)
const showCustomModelReasoningEfforts = computed(() => customModelPricingCategory.value === 'text' && customModelReasoningEffortOptions.value.length > 0)
const showCustomModelRequestCapabilities = computed(() => showCustomModelServiceTiers.value || showCustomModelReasoningEfforts.value)
const customModelStatusOptions = computed(() => {
  const options = availableCustomModelStatusOptions(canManageModelPrices.value, editingOriginalStatus.value)
  return editingBuiltInModel.value ? options.filter((option) => option.value !== 'draft') : options
})
const customModelApiProtocolOptions = computed(() => {
  const supported = new Set(customModelCategoryRecords.value.flatMap((item) => item.supportedApiProtocols ?? []))
  for (const protocol of defaultProtocolsForCurrentProviderCategory(customModelPricingCategory.value)) supported.add(protocol)
  return apiProtocolOptions.filter((option) => supported.has(option.value))
})
const customModelModeOptions = computed(() => availableCustomModelModeOptions(
  targetProvider.value?.code ?? '',
  props.models
))
const selectedModelOwnerLabel = computed(() => {
  if (!props.isManagementView) return ''
  const systemAccountId = systemAccountFilterValue.value
  if (!systemAccountId) return '所选用户'
  if (props.systemAccountFilterSelection?.id === systemAccountId && props.systemAccountFilterSelection.name) {
    return props.systemAccountFilterSelection.name
  }
  const account = (props.systemAccounts ?? []).find((item) => item.id === systemAccountId)
  return account?.displayName || principalLabelForId('system_account', systemAccountId) || '所选用户'
})

const configurationTemplateOptions = computed(() => buildConfigurationTemplateOptions(
  props.models,
  customModelForm.model,
  customModelPricingCategory.value,
  customModelForm.scope
))

/** 公开入口：父级持 ref 调用打开创建/编辑表单；打开本身经 update:open 契约化。 */
defineExpose({
  openCreate,
  openEdit,
  deleteCustomModel
})

function openCreate(
  provider: CustomModelFormTargetProvider,
  options: { scope?: CustomProviderModelScope; mode?: ProviderModelMode } = {}
): void {
  resetCustomModelForm()
  targetProvider.value = provider
  customModelForm.scope = options.scope ?? 'personal'
  customModelForm.status = 'active'
  customModelForm.mode = options.mode ?? 'text'
  customModelForm.supportedApiProtocols = defaultProtocolsForCurrentProviderCategory(customModelForm.mode)
  applyDefaultConfigurationTemplate()
  emit('update:open', true)
}

function openEdit(record: ProviderModelPricing): void {
  if (!canEditProviderModelRecord(record, props.isManagementView)) return
  if (!record.updatedAt) {
    message.error('模型编辑版本缺失，请刷新模型目录后重试')
    return
  }
  resetCustomModelForm()
  editingCustomModelId.value = record.id
  editingCustomModelProviderCode.value = record.providerCode
  editingModelScope.value = record.scope
  editingOriginalStatus.value = record.status ?? 'active'
  Object.assign(customModelForm, createCustomModelFormFromPricing(record, props.models))
  ensureServiceTierPriceRows()
  const baseline = record.scope === 'built_in' ? buildBuiltInModelPayload() : buildCurrentCustomModelPayload()
  if (!baseline) return
  editingCustomModelBaseline.value = structuredClone(baseline)
  editingExpectedUpdatedAt.value = record.updatedAt
  emit('update:open', true)
}

async function saveCustomModel() {
  const provider = targetProvider.value
  if (!provider) return
  const targetProviderCode = editingCustomModelId.value
    ? editingCustomModelProviderCode.value ?? provider.code
    : provider.code
  saving.value = true
  try {
    if (editingCustomModelId.value) {
      const payload = editingBuiltInModel.value ? buildBuiltInModelPayload() : buildCurrentCustomModelPayload()
      if (!payload) return
      if (!editingCustomModelBaseline.value || !editingExpectedUpdatedAt.value) {
        message.error('模型编辑基线缺失，请关闭弹窗后重试')
        return
      }
      const editPatch = buildProviderModelEditPatch(editingCustomModelBaseline.value, payload, editingExpectedUpdatedAt.value)
      if (!editPatch) {
        message.info('没有需要保存的修改')
        return
      }
      const result = await api.providers.updateModel(targetProviderCode, editingCustomModelId.value, {
        expectedUpdatedAt: editPatch.expectedUpdatedAt,
        ...editPatch.patch
      })
      const merged = mergeProviderModelMutationResult(props.models, result, editPatch.patch)
      if (merged) emit('saved', { kind: 'updated', result: merged })
      message.success(editingBuiltInModel.value ? '内置模型已更新' : '自定义模型已更新')
    } else {
      const payload = buildCurrentCustomModelPayload()
      if (!payload) return
      const result = await api.providers.createModel(targetProviderCode, payload, providerModelOperationQueryParams(
        props.isManagementView,
        payload.scope,
        props.systemAccountFilter ?? ''
      ))
      emit('saved', { kind: 'created', result })
      message.success('自定义模型已创建')
    }
    emit('update:open', false)
    resetCustomModelForm()
  } catch (error) {
    console.error(error)
    message.error(extractModelErrorMessage(error, '自定义模型保存失败'))
  } finally {
    saving.value = false
  }
}

/** 删除入口（自含确认，文案对齐原目录弹窗 delete action；409 绑定 guard 错误原文透出）。
 * 挂载方行操作不要再叠加 confirmTitle，避免双重确认。 */
function deleteCustomModel(record: ProviderModelPricing): void {
  if (deleting.value) return
  const id = record.id
  if (!id) return
  Modal.confirm({
    title: `确认删除模型 ${record.model}？已绑定 AI 账户时需要先从账户支持模型或模型映射中移除。`,
    okText: '删除',
    okButtonProps: { danger: true },
    onOk: async () => {
      deleting.value = true
      try {
        await api.providers.deleteModel(record.providerCode, id)
        message.success('自定义模型已删除')
        emit('saved', { kind: 'deleted', result: record })
      } catch (error) {
        console.error(error)
        message.error(extractModelErrorMessage(error, '自定义模型删除失败'))
      } finally {
        deleting.value = false
      }
    }
  })
}

function handleCancel(): void {
  resetCustomModelForm()
  emit('update:open', false)
  emit('cancel')
}

function resetCustomModelForm() {
  editingCustomModelId.value = undefined
  editingCustomModelProviderCode.value = undefined
  editingModelScope.value = undefined
  editingOriginalStatus.value = undefined
  editingCustomModelBaseline.value = undefined
  editingExpectedUpdatedAt.value = undefined
  Object.assign(customModelForm, {
    ...emptyCustomModelForm,
    supportedApiProtocols: [...emptyCustomModelForm.supportedApiProtocols],
    supportedToolsByProtocol: {},
    inputModalities: [...emptyCustomModelForm.inputModalities],
    outputModalities: [...emptyCustomModelForm.outputModalities],
    supportedServiceTiers: [...emptyCustomModelForm.supportedServiceTiers],
    supportedReasoningEfforts: [...emptyCustomModelForm.supportedReasoningEfforts],
    serviceTierPrices: {}
  })
}

function buildCurrentCustomModelPayload(): ProviderModelUpsertPayload | undefined {
  const payload = buildCustomModelUpsertPayload(customModelForm, customModelPricingCategory.value, {
    includeRequestCapabilities: true,
    includePrices: canManageModelPrices.value,
    includeDefaultReasoningEffort: false
  })
  if (!payload) {
    message.warning('请填写模型 ID')
    return undefined
  }
  if (props.isManagementView && payload.scope === 'personal' && !systemAccountFilterValue.value) {
    message.warning('请先选择模型归属用户')
    return undefined
  }
  return payload
}

function buildBuiltInModelPayload(): Partial<ProviderModelUpsertPayload> | undefined {
  const payload = buildCustomModelUpsertPayload(customModelForm, customModelPricingCategory.value, {
    includeRequestCapabilities: true,
    includePrices: true,
    includeDefaultReasoningEffort: true
  })
  if (!payload) return undefined
  // built_in 行编辑不提交能力声明三键（契约仅 custom 行可写）；表单虽回填三键，
  // 但能力声明编辑区对 built_in 隐藏，剥离后 diff 也不会携带这些键。
  const {
    model: _model,
    scope: _scope,
    configurationTemplateId: _configurationTemplateId,
    supportedToolsByProtocol: _supportedToolsByProtocol,
    inputModalities: _inputModalities,
    outputModalities: _outputModalities,
    ...configuration
  } = payload
  return configuration
}

function ensureServiceTierPriceRows(): void {
  for (const tier of customModelForm.supportedServiceTiers) {
    customModelForm.serviceTierPrices[tier] ??= {}
  }
}

function handleCustomModelServiceTiersChange(): void {
  reconcileCustomModelServiceTierPrices(customModelForm)
}

function handleCustomModelModeChange() {
  const category = customModelPricingCategory.value
  customModelForm.supportedApiProtocols = defaultProtocolsForCurrentProviderCategory(category)
  customModelForm.configurationTemplateId = undefined
  clearCustomModelPricesOutsideCategory(customModelForm, category)
  if (!showCustomModelRequestCapabilities.value) {
    clearCustomModelGptCapabilities(customModelForm)
  }
  applyDefaultConfigurationTemplate()
}

function handleCustomModelScopeChange(): void {
  const selected = customModelForm.configurationTemplateId
  if (selected && configurationTemplateOptions.value.some((item) => item.value === selected)) return
  customModelForm.configurationTemplateId = undefined
  applyDefaultConfigurationTemplate()
}

function handleConfigurationTemplateChange(id?: string) {
  applyConfigurationTemplateToCustomModelForm(customModelForm, props.models, id)
  ensureServiceTierPriceRows()
}

function applyDefaultConfigurationTemplate(): void {
  const id = configurationTemplateOptions.value[0]?.value
  if (id) handleConfigurationTemplateChange(id)
}

function defaultProtocolsForCurrentProviderCategory(category: ModelCategoryKey): ProviderModelApiProtocol[] {
  const catalogProtocols = props.models
    .filter((item) => getModelCategory(item) === category)
    .flatMap((item) => item.supportedApiProtocols ?? [])
  if (catalogProtocols.length) return [...new Set(catalogProtocols)]
  // 目录缺行 fallback：目标供应商只携最小契约字段时，协议派生退化为通用默认；
  // 传入完整 ProviderDefinition 字段则保留原 profile 派生语义。
  const provider = targetProvider.value
  const providerDefinition = provider
    ? {
      code: provider.code,
      name: provider.name,
      enabled: provider.enabled,
      protocolCode: provider.protocolCode ?? '',
      protocolProfiles: provider.protocolProfiles ?? [],
      defaultProtocolProfileId: provider.defaultProtocolProfileId ?? ''
    } as ProviderDefinition
    : undefined
  return defaultProtocolsForProviderModelCategory(providerDefinition, category)
}
</script>

<style scoped>
.custom-model-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0 16px;
}

.custom-model-grid-wide {
  grid-column: 1 / -1;
}

@media (max-width: 768px) {
  .custom-model-grid {
    grid-template-columns: 1fr;
  }
}
</style>
