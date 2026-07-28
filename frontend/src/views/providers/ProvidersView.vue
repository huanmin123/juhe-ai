<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadProviders(true)" />
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
      :loading="modelModalLoading"
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
      @system-account-change="handleModelSystemAccountChange"
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
          <a-divider class="custom-model-grid-wide" orientation="left">基本信息</a-divider>
          <a-form-item v-if="!customModelEditing" label="配置模板" class="custom-model-grid-wide" extra="复制同一供应商已有模型的用途、协议、能力、容量和价格；模型 ID 不会被复制。">
            <a-select
              v-model:value="customModelForm.configurationTemplateId"
              show-search
              option-filter-prop="label"
              :options="configurationTemplateOptions"
              placeholder="选择当前供应商的已有模型"
              @change="handleConfigurationTemplateChange"
            />
          </a-form-item>
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
            <a-form-item v-if="editingBuiltInModel && activeProvider?.code !== 'gpt'" label="默认思考级别" class="custom-model-grid-wide">
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
            <a-input v-model:value="customModelForm.releaseDate" placeholder="YYYY-MM-DD" />
          </a-form-item>
          <a-form-item label="停用日期" extra="供应商计划停止提供该模型的日期；没有明确日期时留空。">
            <a-input v-model:value="customModelForm.shutdownDate" placeholder="YYYY-MM-DD" />
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
                <a-input-number v-model:value="customModelForm[field.key]" :min="0" :precision="8" style="width: 100%" />
              </a-form-item>
              <template v-for="tier in customModelForm.supportedServiceTiers" :key="tier">
                <a-divider class="custom-model-grid-wide" orientation="left">{{ formatModelServiceTier(tier) }} · USD / 1M Token</a-divider>
                <a-form-item v-for="field in customModelTierPriceFieldOptions" :key="`${tier}-${field.key}`" :label="field.label" :extra="field.description">
                  <a-input-number v-model:value="customModelForm.serviceTierPrices[tier][field.key]" :min="0" :precision="8" style="width: 100%" />
                </a-form-item>
              </template>
            </template>
            <template v-else>
              <a-form-item v-for="field in customModelDirectPriceFields" :key="field.key" :label="field.label" :extra="field.description">
                <a-input-number v-model:value="customModelForm[field.key]" :min="0" :precision="8" style="width: 100%" />
              </a-form-item>
            </template>
          </template>
        </div>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from '@/lib/antd'
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import RowActions from '@/components/RowActions.vue'
import type { RowActionItem } from '@/components/rowActions'
import { authState } from '@/composables/useAuth'
import { useRemoteSystemAccountOptions } from '@/composables/useRemoteSystemAccountOptions'
import { loadProviderOptionsResource } from '@/composables/useProviderOptionsResource'
import { loadProviderModelCatalogResource } from '@/composables/useProviderModelCatalogResource'
import { principalLabelForId, type PrincipalSelection } from '@/shared/principalLabelCache'
import type { ProviderDefinition, ProviderModelMutationResult, ProviderModelPricing, ProviderModelsParams, ProviderModelUpsertPayload } from '@/types/domain'
import ProviderModelCatalogModal from './ProviderModelCatalogModal.vue'
import {
  applyConfigurationTemplateToCustomModelForm,
  availableCustomModelModeOptions,
  availableCustomModelStatusOptions,
  buildCustomModelCapabilityOptions,
  buildCustomModelMutationPatch,
  buildCustomModelPayload as buildCustomModelUpsertPayload,
  canManageModelPricesForView,
  clearCustomModelGptCapabilities,
  clearCustomModelPricesOutsideCategory,
  createCustomModelFormFromPricing,
  emptyCustomModelForm,
  hasCustomModelMutationChanges,
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
  findFirstModelCategory,
  formatCapabilitiesSummary,
  formatModelServiceTier,
  formatProviderCapability,
  getModelCategory,
  modelModeOptions,
  visibleProviderCapabilities,
  type ModelCategoryKey
} from './providerModelFormatters'
import {
  buildModelCategoryTabs,
  buildConfigurationTemplateOptions,
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
const modelModalLoading = computed(() => modelLoading.value)
const customModelSaving = ref(false)
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const modelKeyword = ref('')
const modelLoadError = ref('')
const selectedModelCategory = ref<ModelCategoryKey>('text')
const modelModalOpen = ref(false)
const customModelModalOpen = ref(false)
const activeProvider = ref<ProviderDefinition | null>(null)
const activeProviderScopedDefaultHealthCheckModel = ref<string>()
const modelSystemAccountFilter = ref('')
const modelSystemAccountFilterSelection = ref<PrincipalSelection>()
const editingCustomModelId = ref<string>()
const editingCustomModelProviderCode = ref<string>()
const editingModelScope = ref<ProviderModelPricing['scope']>()
const editingOriginalStatus = ref<ProviderModelPricing['status']>()
const editingCustomModelBaseline = ref<Partial<ProviderModelUpsertPayload>>()
const editingExpectedUpdatedAt = ref<string>()
let modelRequestSequence = 0
let providerListRequestSequence = 0
let pageActive = true
let pageWasDeactivated = false

const isManagementView = computed(() => route.meta.viewScope === 'admin')
const canManageModelPrices = computed(() => canManageModelPricesForView(isManagementView.value, authState.isAdmin.value))
const customModelEditing = computed(() => Boolean(editingCustomModelId.value))
const editingBuiltInModel = computed(() => editingModelScope.value === 'built_in')

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
  selectedIds: () => [modelSystemAccountFilter.value],
  onMissingSelectedIds: (ids) => {
    if (!ids.includes(modelSystemAccountFilter.value)) return
    const fallback = currentUserPrincipalSelection()
    modelSystemAccountFilter.value = fallback?.id ?? ''
    modelSystemAccountFilterSelection.value = fallback
    invalidateActiveProviderDetail()
    if (modelModalOpen.value && activeProvider.value) void reloadActiveProviderModels(true)
  }
})

const currentCategoryModels = computed(() => {
  const category = selectedModelCategory.value
  return providerModels.value.filter((item) => getModelCategory(item) === category)
})

const modelColumns = computed(() => buildProviderModelColumns(selectedModelCategory.value, currentCategoryModels.value))

const modelModalTitle = computed(() => activeProvider.value ? `${activeProvider.value.name} 模型目录` : '模型目录')
const activeProviderDefaultHealthCheckModel = computed(() => {
  if (activeProviderScopedDefaultHealthCheckModel.value !== undefined) {
    return activeProviderScopedDefaultHealthCheckModel.value
  }
  const selectedOwnerId = modelSystemAccountFilter.value.trim()
  const viewerId = authState.currentUser.value?.id ?? ''
  return !isManagementView.value || (selectedOwnerId && selectedOwnerId === viewerId)
    ? activeProvider.value?.defaultHealthCheckModel ?? ''
    : ''
})
const customModelModalTitle = computed(() => editingBuiltInModel.value ? '编辑内置模型' : customModelEditing.value ? '编辑自定义模型' : '新增自定义模型')
const customModelPricingCategory = computed<ModelCategoryKey>(() => categoryFromModeOrModel(customModelForm.mode, customModelForm.model))
const customModelTargetProviderCode = computed(() => editingCustomModelProviderCode.value ?? activeProvider.value?.code ?? '')
const customModelDirectPriceFields = computed(() => customModelPriceFields(customModelTargetProviderCode.value, customModelPricingCategory.value))
const customModelTierPriceFieldOptions = computed(() => customModelTierPriceFields(customModelTargetProviderCode.value))
const customModelPriceSectionTitle = computed(() => {
  if (customModelPricingCategory.value === 'image') return '图像计费 · USD'
  return 'Token 计费 · USD / 1M Token'
})
const customModelCategoryRecords = computed(() => providerModels.value.filter((item) => getModelCategory(item) === customModelPricingCategory.value))
const customModelCapabilityOptions = computed(() => buildCustomModelCapabilityOptions(
  activeProvider.value?.code ?? '',
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
  activeProvider.value?.code ?? '',
  providerModels.value
))
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

const configurationTemplateOptions = computed(() => buildConfigurationTemplateOptions(
  providerModels.value,
  customModelForm.model,
  customModelPricingCategory.value,
  customModelForm.scope
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

async function loadProviders(force = false) {
  const requestSequence = ++providerListRequestSequence
  const requestSignature = providerListRequestSignature()
  loading.value = true
  try {
    const providerResult = await loadProviderOptionsResource({
      force,
      includeDisabled: isManagementView.value,
      includeDefinitions: !isManagementView.value,
      listItemsOnly: true,
      viewScope: isManagementView.value ? 'admin' : 'self',
      isManagementView: isManagementView.value,
      isCurrent: () => isCurrentProviderListRequest(requestSequence, requestSignature)
    })
    if (!isCurrentProviderListRequest(requestSequence, requestSignature)) return
    providers.value = providerResult.data
  } catch (error) {
    if (!isCurrentProviderListRequest(requestSequence, requestSignature)) return
    console.error(error)
    message.error('加载供应商失败')
  } finally {
    if (requestSequence === providerListRequestSequence) loading.value = false
  }
}

function providerListRequestSignature(): string {
  const viewer = authState.currentUser.value
  return JSON.stringify([
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    isManagementView.value ? 'admin' : 'self'
  ])
}

function isCurrentProviderListRequest(sequence: number, signature: string): boolean {
  return pageActive
    && sequence === providerListRequestSequence
    && signature === providerListRequestSignature()
}

async function openModelModal(provider: ProviderDefinition) {
  invalidateActiveProviderDetail()
  activeProvider.value = provider
  modelModalOpen.value = true
  modelKeyword.value = ''
  selectedModelCategory.value = 'text'
  ensureModelSystemAccountFilter()
  await reloadActiveProviderModels()
}

function handleProviderAction(key: string, provider: ProviderDefinition) {
  if (key === 'models') {
    void openModelModal(provider)
  }
}

function resetModelModal() {
  modelRequestSequence += 1
  invalidateActiveProviderDetail()
  modelLoading.value = false
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
  customModelForm.status = 'active'
  customModelForm.mode = selectedModelCategory.value
  customModelForm.supportedApiProtocols = defaultProtocolsForCurrentProviderCategory(selectedModelCategory.value)
  applyDefaultConfigurationTemplate()
  customModelModalOpen.value = true
}

function openEditCustomModel(record: ProviderModelPricing) {
  if (!record.id || (record.scope === 'built_in' && !isManagementView.value)) return
  if (!record.updatedAt) {
    message.error('模型编辑版本缺失，请刷新模型目录后重试')
    return
  }
  resetCustomModelForm()
  editingCustomModelId.value = record.id
  editingCustomModelProviderCode.value = record.providerCode
  editingModelScope.value = record.scope
  editingOriginalStatus.value = record.status ?? 'active'
  Object.assign(customModelForm, createCustomModelFormFromPricing(record, providerModels.value))
  ensureServiceTierPriceRows()
  const baseline = record.scope === 'built_in' ? buildBuiltInModelPayload() : buildCurrentCustomModelPayload()
  if (!baseline) return
  editingCustomModelBaseline.value = structuredClone(baseline)
  editingExpectedUpdatedAt.value = record.updatedAt
  customModelModalOpen.value = true
}

async function saveCustomModel() {
  if (!activeProvider.value) return
  const wasEditing = Boolean(editingCustomModelId.value)
  const targetProviderCode = editingCustomModelId.value
    ? editingCustomModelProviderCode.value ?? activeProvider.value.code
    : activeProvider.value.code
  customModelSaving.value = true
  try {
    if (editingCustomModelId.value) {
      const payload = editingBuiltInModel.value ? buildBuiltInModelPayload() : buildCurrentCustomModelPayload()
      if (!payload) return
      if (!editingCustomModelBaseline.value || !editingExpectedUpdatedAt.value) {
        message.error('模型编辑基线缺失，请关闭弹窗后重试')
        return
      }
      const patch = buildCustomModelMutationPatch(editingCustomModelBaseline.value, payload)
      if (!hasCustomModelMutationChanges(patch)) {
        message.info('没有需要保存的修改')
        return
      }
      const result = await api.providers.updateModel(targetProviderCode, editingCustomModelId.value, {
        expectedUpdatedAt: editingExpectedUpdatedAt.value,
        ...patch
      })
      applyProviderModelMutationResult(result, patch)
      message.success(editingBuiltInModel.value ? '内置模型已更新' : '自定义模型已更新')
    } else {
      const payload = buildCurrentCustomModelPayload()
      if (!payload) return
      await api.providers.createModel(targetProviderCode, payload, modelOperationQueryParams(payload))
      message.success('自定义模型已创建')
    }
    customModelModalOpen.value = false
    resetCustomModelForm()
    if (!wasEditing) await reloadActiveProviderModels(true)
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
    message.success('自定义模型已删除')
    await reloadActiveProviderModels(true)
  } catch (error) {
    console.error(error)
    message.error(extractModelErrorMessage(error, '自定义模型删除失败'))
  } finally {
    modelLoading.value = false
  }
}

async function reloadActiveProviderModels(force = false) {
  const provider = activeProvider.value
  if (!provider) return
  ensureModelSystemAccountFilter()
  const requestSequence = ++modelRequestSequence
  const requestSignature = modelRequestSignature(provider.code)
  const modelQuery = modelCatalogQueryParams()
  providerModels.value = []
  modelLoading.value = true
  modelLoadError.value = ''
  try {
    const modelResult = await loadProviderModelCatalogResource({
      force,
      isManagementView: isManagementView.value,
      providerCode: provider.code,
      query: modelQuery
    })
    if (!isCurrentModelRequest(requestSequence, requestSignature, provider.code)) return
    applyProviderModelResult(modelResult, requestSequence, provider.code)
  } catch (error) {
    if (!isCurrentModelRequest(requestSequence, requestSignature, provider.code)) return
    console.error(error)
    modelLoadError.value = extractModelErrorMessage(error, '加载模型价格失败')
    message.error(modelLoadError.value)
  } finally {
    if (requestSequence === modelRequestSequence) modelLoading.value = false
  }
}

function modelRequestSignature(providerCode: string): string {
  const viewer = authState.currentUser.value
  const query = modelProviderQueryParams()
  return JSON.stringify([
    authState.revision.value,
    viewer?.id ?? 'anonymous',
    viewer?.role ?? 'anonymous',
    query.viewScope ?? 'self',
    query.systemAccountId ?? '',
    providerCode
  ])
}

function isCurrentModelRequest(sequence: number, signature: string, providerCode: string): boolean {
  return pageActive
    && sequence === modelRequestSequence
    && signature === modelRequestSignature(providerCode)
    && activeProvider.value?.code === providerCode
}

function applyProviderModelResult(models: ProviderModelPricing[], requestSequence: number, providerCode: string): void {
  if (requestSequence !== modelRequestSequence || activeProvider.value?.code !== providerCode) return
  providerModels.value = models
  selectedModelCategory.value = findFirstModelCategory(models)
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
    supportedServiceTiers: [...emptyCustomModelForm.supportedServiceTiers],
    supportedReasoningEfforts: [...emptyCustomModelForm.supportedReasoningEfforts],
    serviceTierPrices: {}
  })
}

function ensureModelSystemAccountFilter(): void {
  if (!isManagementView.value || modelSystemAccountFilter.value.trim()) return
  const currentUser = currentUserPrincipalSelection()
  modelSystemAccountFilter.value = currentUser?.id ?? ''
  modelSystemAccountFilterSelection.value = currentUser
}

function currentUserPrincipalSelection(): PrincipalSelection | undefined {
  const user = authState.currentUser.value
  if (!user?.id || !user.displayName?.trim()) return undefined
  return { id: user.id, name: user.displayName.trim(), kind: 'system_account' }
}

function handleModelSystemAccountChange(): void {
  invalidateActiveProviderDetail()
  void reloadActiveProviderModels(true)
}

function invalidateActiveProviderDetail(): void {
  activeProviderScopedDefaultHealthCheckModel.value = undefined
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
  if (isManagementView.value && payload.scope === 'personal' && !modelSystemAccountFilter.value.trim()) {
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
  const {
    model: _model,
    scope: _scope,
    configurationTemplateId: _configurationTemplateId,
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
  applyConfigurationTemplateToCustomModelForm(customModelForm, providerModels.value, id)
  ensureServiceTierPriceRows()
}

function applyDefaultConfigurationTemplate(): void {
  const id = configurationTemplateOptions.value[0]?.value
  if (id) handleConfigurationTemplateChange(id)
}

function defaultProtocolsForCurrentProviderCategory(category: ModelCategoryKey) {
  const catalogProtocols = providerModels.value
    .filter((item) => getModelCategory(item) === category)
    .flatMap((item) => item.supportedApiProtocols ?? [])
  return catalogProtocols.length
    ? [...new Set(catalogProtocols)]
    : defaultProtocolsForProviderModelCategory(activeProvider.value ?? undefined, category)
}

function applyProviderModelMutationResult(
  result: ProviderModelMutationResult,
  patch: Partial<ProviderModelUpsertPayload>
): void {
  const normalizedPatch = Object.fromEntries(Object.entries(patch).map(([key, value]) => [
    key,
    value === null ? undefined : value
  ]))
  providerModels.value = providerModels.value.map((item) => item.id === result.id
    ? { ...item, ...normalizedPatch, status: result.status, updatedAt: result.updatedAt } as ProviderModelPricing
    : item)
  if (result.defaultHealthCheckModelCleared && isActiveProviderDefaultHealthCheckModel(result.model)) {
    applyProviderDefaultHealthCheckModel(result.providerCode, '')
  }
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

function modelCatalogQueryParams(): ProviderModelsParams {
  return {
    ...modelProviderQueryParams(),
    includeInactive: true,
    includeUnpriced: true
  }
}

function modelProviderQueryParams(): Pick<ProviderModelsParams, 'systemAccountId' | 'viewScope'> {
  if (!isManagementView.value) return { viewScope: 'self' }
  const systemAccountId = modelSystemAccountFilter.value.trim()
  return systemAccountId ? { systemAccountId, viewScope: 'admin' } : { viewScope: 'admin' }
}

function modelOperationQueryParams(payload: ProviderModelUpsertPayload): Pick<ProviderModelsParams, 'systemAccountId'> | undefined {
  if (!isManagementView.value || payload.scope === 'global') return undefined
  return modelProviderQueryParams()
}

function modelRowActions(record: ProviderModelPricing): RowActionItem[] {
  if (modelLoading.value) return []
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
  actions.push({ key: 'edit', label: record.scope === 'built_in' ? '编辑配置' : '编辑', icon: 'edit', tone: 'info' })
  if (record.scope !== 'built_in') {
    actions.push({ key: 'delete', label: '删除', icon: 'delete', danger: true, confirmTitle: `确认删除模型 ${record.model}？已绑定 AI 账户时需要先从账户支持模型或模型映射中移除。`, confirmOkText: '删除' })
  }
  return actions
}

function handleModelAction(key: string, record: ProviderModelPricing) {
  if (modelLoading.value) return
  if (key === 'set-default-health-check-model') {
    void setDefaultHealthCheckModel(record)
    return
  }
  if (key === 'edit') {
    void openEditCustomModel(record)
    return
  }
  if (key === 'delete') {
    void deleteCustomModel(record)
  }
}

function canMutateModel(record: ProviderModelPricing): boolean {
  if (!record.id) return false
  if (record.scope === 'built_in') return isManagementView.value
  if (isManagementView.value) return true
  return record.systemAccountId === authState.currentUser.value?.id
}

function isActiveProviderDefaultHealthCheckModel(model: string): boolean {
  const current = activeProviderDefaultHealthCheckModel.value.trim()
  return Boolean(current && model.trim() === current)
}

function applyProviderDefaultHealthCheckModel(providerCode: string, defaultHealthCheckModel: string) {
  activeProviderScopedDefaultHealthCheckModel.value = defaultHealthCheckModel
  const selectedOwnerId = modelSystemAccountFilter.value.trim()
  const viewerId = authState.currentUser.value?.id ?? ''
  if (!isManagementView.value || (selectedOwnerId && selectedOwnerId === viewerId)) {
    providers.value = providers.value.map((provider) => (
      provider.code === providerCode
        ? providerWithDefaultHealthCheckModel(provider, defaultHealthCheckModel)
        : provider
    ))
    if (activeProvider.value?.code === providerCode) {
      activeProvider.value = providerWithDefaultHealthCheckModel(activeProvider.value, defaultHealthCheckModel)
    }
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

function invalidateProviderPageRequests(): void {
  pageActive = false
  providerListRequestSequence += 1
  modelRequestSequence += 1
  providers.value = []
  resetModelModal()
  customModelModalOpen.value = false
}

onMounted(loadProviders)
onDeactivated(() => {
  pageWasDeactivated = true
  invalidateProviderPageRequests()
})
onBeforeUnmount(invalidateProviderPageRequests)
onActivated(() => {
  pageActive = true
  if (!pageWasDeactivated) return
  pageWasDeactivated = false
  void loadProviders(true)
})
watch(() => authState.revision.value, () => {
  providerListRequestSequence += 1
  modelRequestSequence += 1
  resetModelModal()
  providers.value = []
  modelSystemAccountFilter.value = ''
  modelSystemAccountFilterSelection.value = undefined
  customModelModalOpen.value = false
  if (pageActive) void loadProviders(true)
  else pageWasDeactivated = true
})
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

.custom-model-switch-row {
  display: flex;
  min-height: 32px;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.custom-model-switch-label {
  color: rgba(0, 0, 0, 0.65);
}

@media (max-width: 768px) {
  .custom-model-grid {
    grid-template-columns: 1fr;
  }
}
</style>
