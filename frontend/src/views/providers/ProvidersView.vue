<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadProviders" />
    <ResponsiveDataList table-class="page-table provider-table" :columns="providerColumns" :data-source="providers" row-key="code" :loading="loading" :scroll-x="providerScrollX" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadProviders">
      <template #emptyText>
        <a-empty class="page-empty-card" :description="providerEmptyDescription" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
        </template>
        <template v-else-if="column.key === 'accountTypes'">
          <a-space wrap>
            <a-tag v-for="type in record.accountTypes" :key="type" color="processing">{{ type }}</a-tag>
          </a-space>
        </template>
        <template v-else-if="column.key === 'capabilities'">
          <a-space wrap>
            <a-tag v-for="capability in visibleProviderCapabilities(record.capabilities)" :key="capability" color="blue">{{ formatProviderCapability(capability) }}</a-tag>
            <span v-if="!visibleProviderCapabilities(record.capabilities).length" class="muted-text">-</span>
          </a-space>
        </template>
        <template v-else-if="column.key === 'description'">
          <span>{{ record.description || '-' }}</span>
        </template>
        <template v-else-if="column.key === 'baseUrl'">
          <span class="mono-cell">{{ record.baseUrl }}</span>
        </template>
        <template v-else-if="column.key === 'defaultHealthCheckModel'">
          <span class="mono-cell">{{ record.defaultHealthCheckModel }}</span>
        </template>
        <template v-else-if="column.key === 'defaultSupportedModels'">
          <a-space wrap>
            <a-tag v-for="model in visibleDefaultSupportedModels(record)" :key="model" class="mono-cell">{{ model }}</a-tag>
            <a-tag v-if="hiddenDefaultSupportedModelCount(record) > 0">+{{ hiddenDefaultSupportedModelCount(record) }}</a-tag>
            <span v-if="!record.defaultSupportedModels?.length" class="muted-text">-</span>
          </a-space>
        </template>
        <template v-else-if="column.key === 'actions'">
          <RowActions :actions="providerActions" @action-click="handleProviderAction($event, record)" />
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>账户类型</span>
              <strong>{{ record.accountTypes.join(' / ') }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>接口能力</span>
              <strong>{{ formatCapabilitiesSummary(record.capabilities) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认 Base URL</span>
              <strong class="mono-cell">{{ record.baseUrl }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认检查模型</span>
              <strong class="mono-cell">{{ record.defaultHealthCheckModel }}</strong>
            </div>
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认支持模型</span>
              <strong class="mono-cell">{{ formatDefaultSupportedModels(record) }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions">
            <RowActions variant="button" :actions="providerActions" @action-click="handleProviderAction($event, record)" />
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <ProviderModelCatalogModal
      v-model:keyword="modelKeyword"
      v-model:open="modelModalOpen"
      v-model:selected-category="selectedModelCategory"
      :category-tabs="modelCategoryTabs"
      :columns="modelColumns"
      :current-category-count="currentCategoryModels.length"
      :default-health-check-model="activeProviderDefaultHealthCheckModel"
      :is-management-view="isManagementView"
      :load-error="modelLoadError"
      :loading="modelLoading"
      :models="filteredModels"
      :row-actions="modelRowActions"
      :system-account-filter="modelSystemAccountFilter"
      :system-account-filter-selection="modelSystemAccountFilterSelection"
      :system-accounts="modelSystemAccounts"
      :system-accounts-loading="modelSystemAccountOptionsLoading"
      :title="modelModalTitle"
      @cancel="resetModelModal"
      @create="openCreateCustomModel"
      @model-action="handleModelAction"
      @system-account-change="reloadActiveProviderModels"
      @system-account-dropdown="handleModelSystemAccountOptionsDropdown"
      @system-account-search="handleModelSystemAccountOptionsSearch"
      @update:system-account-filter="modelSystemAccountFilter = $event"
      @update:system-account-filter-selection="modelSystemAccountFilterSelection = $event"
    />

    <a-modal
      v-model:open="customModelModalOpen"
      :title="customModelModalTitle"
      width="820px"
      :confirm-loading="customModelSaving"
      @ok="saveCustomModel"
      @cancel="resetCustomModelForm"
    >
      <a-form layout="vertical" class="custom-model-form">
        <div class="custom-model-grid">
          <a-form-item v-if="isManagementView" label="作用域" class="custom-model-grid-wide">
            <a-radio-group v-model:value="customModelForm.scope" :disabled="customModelEditing" button-style="solid">
              <a-radio-button value="personal">{{ selectedModelOwnerLabel }}个人模型</a-radio-button>
              <a-radio-button value="global">全局模型</a-radio-button>
            </a-radio-group>
          </a-form-item>
          <a-form-item label="模型 ID" required>
            <a-input v-model:value="customModelForm.model" :disabled="customModelEditing" placeholder="例如 gpt-5.5-pro" />
          </a-form-item>
          <a-form-item label="状态">
            <a-select v-model:value="customModelForm.status" :options="modelStatusOptions" />
          </a-form-item>
          <a-form-item label="用途">
            <a-select v-model:value="customModelForm.mode" :options="modelModeOptions" @change="handleCustomModelModeChange" />
          </a-form-item>
          <a-form-item label="接口协议" class="custom-model-grid-wide">
            <a-select
              v-model:value="customModelForm.supportedApiProtocols"
              mode="multiple"
              :options="apiProtocolOptions"
              placeholder="不确定时可留空"
            />
          </a-form-item>
          <template v-if="showCustomModelRequestCapabilities">
            <a-form-item label="服务等级" class="custom-model-grid-wide">
              <a-select
                v-model:value="customModelForm.supportedServiceTiers"
                mode="tags"
                placeholder="未选择时表示不支持账户级服务等级覆盖"
              />
            </a-form-item>
            <a-form-item label="思考级别" class="custom-model-grid-wide">
              <a-select
                v-model:value="customModelForm.supportedReasoningEfforts"
                mode="tags"
                placeholder="仅配置上游 wire 支持的思考级别"
                @change="handleCustomModelReasoningEffortsChange"
              />
            </a-form-item>
            <a-form-item label="默认思考级别" class="custom-model-grid-wide">
              <a-select
                v-model:value="customModelForm.defaultReasoningEffort"
                allow-clear
                :disabled="!customModelDefaultReasoningOptions.length"
                :options="customModelDefaultReasoningOptions"
                placeholder="从已支持的思考级别中选择"
              />
            </a-form-item>
          </template>
          <a-form-item label="价格模板" class="custom-model-grid-wide">
            <a-select
              v-model:value="customModelForm.pricingTemplateModel"
              allow-clear
              show-search
              option-filter-prop="label"
              :options="pricingTemplateOptions"
              placeholder="选择后仅回填价格，不保存为计价引用"
              @change="handlePricingTemplateChange"
            />
          </a-form-item>
          <a-form-item label="发布时间">
            <a-input v-model:value="customModelForm.releaseDate" placeholder="YYYY-MM-DD" />
          </a-form-item>
          <a-form-item label="停用时间">
            <a-input v-model:value="customModelForm.shutdownDate" placeholder="YYYY-MM-DD" />
          </a-form-item>
          <a-form-item label="上下文 token">
            <a-input-number v-model:value="customModelForm.contextWindowTokens" :min="0" style="width: 100%" />
          </a-form-item>
          <a-form-item label="最大输出 token">
            <a-input-number v-model:value="customModelForm.maxOutputTokens" :min="0" style="width: 100%" />
          </a-form-item>
          <template v-if="customModelPricingCategory === 'text'">
            <a-form-item label="输入价格">
              <a-input-number v-model:value="customModelForm.inputUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
            <a-form-item label="输出价格">
              <a-input-number v-model:value="customModelForm.outputUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
            <a-form-item label="缓存读取价格">
              <a-input-number v-model:value="customModelForm.cachedInputUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
            <a-form-item label="缓存写入价格">
              <a-input-number v-model:value="customModelForm.cacheWriteUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
          </template>
          <template v-else-if="customModelPricingCategory === 'image'">
            <a-form-item label="图片输入价格">
              <a-input-number v-model:value="customModelForm.imageInputUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
            <a-form-item label="图片输出价格">
              <a-input-number v-model:value="customModelForm.imageOutputUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
            <a-form-item label="每张图片价格">
              <a-input-number v-model:value="customModelForm.outputUsdPerImage" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
          </template>
          <template v-else-if="customModelPricingCategory === 'audio'">
            <a-form-item label="音频输入价格">
              <a-input-number v-model:value="customModelForm.audioInputUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
            <a-form-item label="音频输出价格">
              <a-input-number v-model:value="customModelForm.audioOutputUsdPer1M" :min="0" :precision="8" style="width: 100%" />
            </a-form-item>
          </template>
        </div>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { authState } from '@/composables/useAuth'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { principalLabelForId, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { ProviderDefinition, ProviderModelPricing, ProviderModelsParams, ProviderModelUpsertPayload } from '@/types/domain'
import { invalidateAccountProviderModelOptionsCache } from '@/views/accounts/useAccountProviderModelOptions'
import ProviderModelCatalogModal from './ProviderModelCatalogModal.vue'
import {
  applyPricingTemplateToCustomModelForm,
  buildCustomModelPayload as buildCustomModelUpsertPayload,
  clearCustomModelGptCapabilities,
  clearCustomModelPricesOutsideCategory,
  createCustomModelFormFromPricing,
  emptyCustomModelForm,
  normalizeCustomModelDefaultReasoningEffort,
  type CustomModelForm
} from './customProviderModelForm'
import {
  apiProtocolOptions,
  categoryFromModeOrModel,
  defaultProtocolsForProviderModelCategory,
  findFirstModelCategory,
  formatCapabilitiesSummary,
  formatModelReasoningEffort,
  formatProviderCapability,
  getModelCategory,
  modelModeOptions,
  modelStatusOptions,
  visibleProviderCapabilities,
  type ModelCategoryKey
} from './providerModelFormatters'
import {
  buildModelCategoryTabs,
  buildPricingTemplateOptions,
  buildProviderModelColumns,
  filterProviderModelsByKeyword
} from './providerModelTableState'
import {
  providerActionsForScope,
  providerColumnsForScope,
  providerEmptyDescriptionForScope,
  providerScrollXForScope
} from './providerTableConfig'

const route = useRoute()
const loading = ref(false)
const modelLoading = ref(false)
const customModelSaving = ref(false)
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const modelKeyword = ref('')
const modelLoadError = ref('')
const selectedModelCategory = ref<ModelCategoryKey>('text')
const modelModalOpen = ref(false)
const customModelModalOpen = ref(false)
const activeProvider = ref<ProviderDefinition | null>(null)
const modelSystemAccountFilter = ref('')
const modelSystemAccountFilterSelection = ref<PrincipalSelection>()
const editingCustomModelId = ref<string>()
const editingCustomModelProviderCode = ref<string>()

const isManagementView = computed(() => route.meta.viewScope === 'admin')
const customModelEditing = computed(() => Boolean(editingCustomModelId.value))

const providerColumns = computed(() => providerColumnsForScope(isManagementView.value))
const providerScrollX = computed(() => providerScrollXForScope(isManagementView.value))
const providerEmptyDescription = computed(() => providerEmptyDescriptionForScope(isManagementView.value))
const providerActions = computed<RowActionItem[]>(() => providerActionsForScope(isManagementView.value))

const customModelForm = reactive<CustomModelForm>({ ...emptyCustomModelForm })

const {
  handleDropdown: handleModelSystemAccountOptionsDropdown,
  handleSearch: handleModelSystemAccountOptionsSearch,
  loading: modelSystemAccountOptionsLoading,
  systemAccounts: modelSystemAccounts
} = useRemoteSystemAccountOptions({
  enabled: () => isManagementView.value,
  errorMessage: '加载模型归属用户失败',
  localCacheKeyParts: () => ['providers', 'model-catalog'],
  selectedIds: () => [modelSystemAccountFilter.value],
  onMissingSelectedIds: (ids) => {
    if (!ids.includes(modelSystemAccountFilter.value)) return
    modelSystemAccountFilter.value = currentUserSystemAccountId()
    modelSystemAccountFilterSelection.value = undefined
  }
})

const currentCategoryModels = computed(() => {
  const category = selectedModelCategory.value
  return providerModels.value.filter((item) => getModelCategory(item) === category)
})

const modelColumns = computed(() => buildProviderModelColumns(selectedModelCategory.value, currentCategoryModels.value))

const modelModalTitle = computed(() => activeProvider.value ? `${activeProvider.value.name} 模型目录` : '模型目录')
const activeProviderDefaultHealthCheckModel = computed(() => activeProvider.value?.defaultHealthCheckModel ?? '')
const customModelModalTitle = computed(() => customModelEditing.value ? '编辑自定义模型' : '新增自定义模型')
const customModelPricingCategory = computed<ModelCategoryKey>(() => categoryFromModeOrModel(customModelForm.mode, customModelForm.model))
const showCustomModelRequestCapabilities = computed(() => customModelPricingCategory.value === 'text')
const customModelDefaultReasoningOptions = computed(() => customModelForm.supportedReasoningEfforts.map((value) => ({
  value,
  label: formatModelReasoningEffort(value)
})))
const selectedModelOwnerLabel = computed(() => {
  if (!isManagementView.value) return ''
  const systemAccountId = modelSystemAccountFilter.value.trim()
  if (!systemAccountId) return '所选用户'
  if (modelSystemAccountFilterSelection.value?.id === systemAccountId && modelSystemAccountFilterSelection.value.name) {
    return modelSystemAccountFilterSelection.value.name
  }
  const account = modelSystemAccounts.value.find((item) => item.id === systemAccountId)
  return account?.displayName || principalLabelForId('system_account', systemAccountId) || '所选用户'
})

const pricingTemplateOptions = computed(() => buildPricingTemplateOptions(
  providerModels.value,
  customModelForm.model,
  customModelPricingCategory.value
))

const modelCategoryTabs = computed(() => buildModelCategoryTabs(providerModels.value))

const filteredModels = computed(() => filterProviderModelsByKeyword(currentCategoryModels.value, modelKeyword.value))

function visibleDefaultSupportedModels(provider: ProviderDefinition): string[] {
  return (provider.defaultSupportedModels ?? []).slice(0, 5)
}

function hiddenDefaultSupportedModelCount(provider: ProviderDefinition): number {
  return Math.max(0, (provider.defaultSupportedModels?.length ?? 0) - 5)
}

function formatDefaultSupportedModels(provider: ProviderDefinition): string {
  const visible = visibleDefaultSupportedModels(provider)
  if (!visible.length) return '-'
  const hiddenCount = hiddenDefaultSupportedModelCount(provider)
  return hiddenCount > 0 ? `${visible.join(' / ')} / +${hiddenCount}` : visible.join(' / ')
}

async function loadProviders() {
  loading.value = true
  try {
    providers.value = isManagementView.value ? await api.providers.list() : await api.providers.options()
  } catch (error) {
    console.error(error)
    message.error('加载供应商失败')
  } finally {
    loading.value = false
  }
}

async function openModelModal(provider: ProviderDefinition) {
  activeProvider.value = provider
  modelModalOpen.value = true
  modelKeyword.value = ''
  selectedModelCategory.value = 'text'
  ensureModelSystemAccountFilter()
  if (isManagementView.value) {
    void handleModelSystemAccountOptionsDropdown(true)
  }
  await reloadActiveProviderModels()
}

function handleProviderAction(key: string, provider: ProviderDefinition) {
  if (key === 'models') {
    void openModelModal(provider)
  }
}

function resetModelModal() {
  activeProvider.value = null
  modelKeyword.value = ''
  modelLoadError.value = ''
  selectedModelCategory.value = 'text'
  providerModels.value = []
  resetCustomModelForm()
}

function openCreateCustomModel() {
  if (!activeProvider.value) return
  ensureModelSystemAccountFilter()
  resetCustomModelForm()
  customModelForm.scope = 'personal'
  customModelForm.mode = selectedModelCategory.value
  customModelForm.supportedApiProtocols = defaultProtocolsForProviderModelCategory(activeProvider.value, selectedModelCategory.value)
  customModelModalOpen.value = true
}

function openEditCustomModel(record: ProviderModelPricing) {
  if (!record.id || record.scope === 'built_in') return
  resetCustomModelForm()
  editingCustomModelId.value = record.id
  editingCustomModelProviderCode.value = record.providerCode
  Object.assign(customModelForm, createCustomModelFormFromPricing(record, providerModels.value))
  customModelModalOpen.value = true
}

async function saveCustomModel() {
  if (!activeProvider.value) return
  const payload = buildCurrentCustomModelPayload()
  if (!payload) return
  const targetProviderCode = editingCustomModelId.value
    ? editingCustomModelProviderCode.value ?? activeProvider.value.code
    : activeProvider.value.code
  customModelSaving.value = true
  try {
    if (editingCustomModelId.value) {
      await api.providers.updateModel(targetProviderCode, editingCustomModelId.value, payload)
      message.success('自定义模型已更新')
    } else {
      await api.providers.createModel(targetProviderCode, payload, modelOperationQueryParams(payload))
      message.success('自定义模型已创建')
    }
    invalidateAccountProviderModelOptionsCache()
    customModelModalOpen.value = false
    resetCustomModelForm()
    await reloadActiveProviderModels()
  } catch (error) {
    console.error(error)
    message.error(extractModelErrorMessage(error, '自定义模型保存失败'))
  } finally {
    customModelSaving.value = false
  }
}

async function deleteCustomModel(record: ProviderModelPricing) {
  if (!activeProvider.value || !record.id) return
  modelLoading.value = true
  try {
    await api.providers.deleteModel(record.providerCode, record.id)
    invalidateAccountProviderModelOptionsCache()
    message.success('自定义模型已删除')
    await reloadActiveProviderModels()
  } catch (error) {
    console.error(error)
    message.error(extractModelErrorMessage(error, '自定义模型删除失败'))
  } finally {
    modelLoading.value = false
  }
}

async function reloadActiveProviderModels() {
  const provider = activeProvider.value
  if (!provider) return
  ensureModelSystemAccountFilter()
  modelLoading.value = true
  modelLoadError.value = ''
  try {
    const scopedProviders = isManagementView.value
      ? await api.providers.list(modelProviderQueryParams())
      : await api.providers.options()
    const scopedProvider = scopedProviders.find((item) => item.code === provider.code)
    if (scopedProvider) {
      activeProvider.value = scopedProvider
    }
    providerModels.value = await api.providers.models(provider.code, modelCatalogQueryParams())
    selectedModelCategory.value = findFirstModelCategory(providerModels.value)
  } catch (error) {
    console.error(error)
    modelLoadError.value = extractModelErrorMessage(error, '加载模型价格失败')
    message.error(modelLoadError.value)
  } finally {
    modelLoading.value = false
  }
}

function resetCustomModelForm() {
  editingCustomModelId.value = undefined
  editingCustomModelProviderCode.value = undefined
  Object.assign(customModelForm, {
    ...emptyCustomModelForm,
    supportedApiProtocols: [...emptyCustomModelForm.supportedApiProtocols],
    supportedServiceTiers: [...emptyCustomModelForm.supportedServiceTiers],
    supportedReasoningEfforts: [...emptyCustomModelForm.supportedReasoningEfforts]
  })
}

function ensureModelSystemAccountFilter(): void {
  if (!isManagementView.value || modelSystemAccountFilter.value.trim()) return
  modelSystemAccountFilter.value = currentUserSystemAccountId()
}

function currentUserSystemAccountId(): string {
  return authState.currentUser.value?.id ?? ''
}

function buildCurrentCustomModelPayload(): ProviderModelUpsertPayload | undefined {
  const payload = buildCustomModelUpsertPayload(customModelForm, customModelPricingCategory.value, {
    includeRequestCapabilities: true
  })
  if (!payload) {
    message.warning('请填写模型 ID')
    return undefined
  }
  if (isManagementView.value && payload.scope === 'personal' && !modelSystemAccountFilter.value.trim()) {
    message.warning('请先选择模型归属用户')
    return undefined
  }
  return payload
}

function handleCustomModelModeChange() {
  const category = customModelPricingCategory.value
  customModelForm.supportedApiProtocols = defaultProtocolsForProviderModelCategory(activeProvider.value ?? undefined, category)
  customModelForm.pricingTemplateModel = undefined
  clearCustomModelPricesOutsideCategory(customModelForm, category)
  if (!showCustomModelRequestCapabilities.value) {
    clearCustomModelGptCapabilities(customModelForm)
  }
}

function handleCustomModelReasoningEffortsChange() {
  normalizeCustomModelDefaultReasoningEffort(customModelForm)
}

async function setDefaultHealthCheckModel(record: ProviderModelPricing) {
  const provider = activeProvider.value
  if (!provider) return
  ensureModelSystemAccountFilter()
  modelLoading.value = true
  try {
    const result = await api.providers.setDefaultHealthCheckModel(provider.code, record.model, modelProviderQueryParams())
    applyProviderDefaultHealthCheckModel(provider.code, result.defaultHealthCheckModel)
    message.success(`默认检查模型已设置为 ${result.defaultHealthCheckModel}`)
  } catch (error) {
    console.error(error)
    message.error(extractModelErrorMessage(error, '默认检查模型设置失败'))
  } finally {
    modelLoading.value = false
  }
}

function handlePricingTemplateChange(value?: string) {
  applyPricingTemplateToCustomModelForm(customModelForm, providerModels.value, value)
}

function modelCatalogQueryParams(): ProviderModelsParams {
  return {
    ...modelProviderQueryParams(),
    includeInactive: true,
    includeUnpriced: true
  }
}

function modelProviderQueryParams(): Pick<ProviderModelsParams, 'systemAccountId'> | undefined {
  if (!isManagementView.value) return undefined
  const systemAccountId = modelSystemAccountFilter.value.trim()
  return systemAccountId ? { systemAccountId } : undefined
}

function modelOperationQueryParams(payload: ProviderModelUpsertPayload): Pick<ProviderModelsParams, 'systemAccountId'> | undefined {
  if (!isManagementView.value || payload.scope === 'global') return undefined
  return modelProviderQueryParams()
}

function modelRowActions(record: ProviderModelPricing): RowActionItem[] {
  const actions: RowActionItem[] = []
  const isDefault = isActiveProviderDefaultHealthCheckModel(record.model)
  if (getModelCategory(record) === 'text') {
    const unavailableForSystemDefault = isManagementView.value && record.scope === 'personal'
    actions.push({
      key: 'set-default-health-check-model',
      label: isDefault ? '默认检查' : '设为默认检查',
      icon: 'test',
      tone: isDefault ? 'success' : 'info',
      disabled: isDefault || unavailableForSystemDefault || (record.status ?? 'active') !== 'active'
    })
  }
  if (!canMutateModel(record)) return actions
  actions.push(
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'info' },
    { key: 'delete', label: '删除', icon: 'delete', danger: true, confirmTitle: `确认删除模型 ${record.model}？已绑定 AI 账户时需要先从账户支持模型或模型映射中移除。`, confirmOkText: '删除' }
  )
  return actions
}

function handleModelAction(key: string, record: ProviderModelPricing) {
  if (key === 'set-default-health-check-model') {
    void setDefaultHealthCheckModel(record)
    return
  }
  if (key === 'edit') {
    openEditCustomModel(record)
    return
  }
  if (key === 'delete') {
    void deleteCustomModel(record)
  }
}

function canMutateModel(record: ProviderModelPricing): boolean {
  if (!record.id || record.scope === 'built_in') return false
  if (isManagementView.value) return true
  return record.systemAccountId === authState.currentUser.value?.id
}

function isActiveProviderDefaultHealthCheckModel(model: string): boolean {
  const current = activeProviderDefaultHealthCheckModel.value.trim()
  return Boolean(current && model.trim() === current)
}

function applyProviderDefaultHealthCheckModel(providerCode: string, defaultHealthCheckModel: string) {
  providers.value = providers.value.map((provider) => (
    provider.code === providerCode
      ? providerWithDefaultHealthCheckModel(provider, defaultHealthCheckModel)
      : provider
  ))
  if (activeProvider.value?.code === providerCode) {
    activeProvider.value = providerWithDefaultHealthCheckModel(activeProvider.value, defaultHealthCheckModel)
  }
}

function providerWithDefaultHealthCheckModel(provider: ProviderDefinition, defaultHealthCheckModel: string): ProviderDefinition {
  return {
    ...provider,
    defaultHealthCheckModel,
    systemDefaultHealthCheckModel: isManagementView.value
      ? defaultHealthCheckModel
      : provider.systemDefaultHealthCheckModel
  }
}

function extractModelErrorMessage(error: unknown, fallback: string) {
  if (typeof error !== 'object' || error === null) return fallback
  const response = (error as { response?: { data?: { message?: unknown } } }).response
  const messageText = response?.data?.message
  return typeof messageText === 'string' && messageText.trim() ? messageText.trim() : fallback
}

onMounted(loadProviders)
</script>

<style scoped>
.provider-table :deep(.ant-empty) {
  margin: 12px 0;
}

.provider-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

.muted-text {
  color: rgba(0, 0, 0, 0.45);
}

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
