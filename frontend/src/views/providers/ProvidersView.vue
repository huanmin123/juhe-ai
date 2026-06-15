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
        <template v-else-if="column.key === 'defaultTestModel'">
          <span class="mono-cell">{{ record.defaultTestModel }}</span>
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
            <div v-if="isManagementView" class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认测试模型</span>
              <strong class="mono-cell">{{ record.defaultTestModel }}</strong>
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
      :loading="modelLoading"
      :models="filteredModels"
      :row-actions="modelRowActions"
      :title="modelModalTitle"
      @cancel="resetModelModal"
      @create="openCreateCustomModel"
      @model-action="handleModelAction"
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
          <a-form-item label="模型 ID" required>
            <a-input v-model:value="customModelForm.model" placeholder="例如 gpt-5.5-pro" />
          </a-form-item>
          <a-form-item label="显示名称">
            <a-input v-model:value="customModelForm.displayName" allow-clear />
          </a-form-item>
          <a-form-item label="范围">
            <a-select v-model:value="customModelForm.scope" :disabled="customModelEditing" :options="modelScopeOptions" />
          </a-form-item>
          <a-form-item label="状态">
            <a-select v-model:value="customModelForm.status" :options="modelStatusOptions" />
          </a-form-item>
          <a-form-item label="可见性">
            <a-select v-model:value="customModelForm.visibility" :options="modelVisibilityOptions" />
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
import type { ProviderDefinition, ProviderModelPricing, ProviderModelUpsertPayload } from '@/types/domain'
import ProviderModelCatalogModal from './ProviderModelCatalogModal.vue'
import {
  applyPricingTemplateToCustomModelForm,
  buildCustomModelPayload as buildCustomModelUpsertPayload,
  clearCustomModelPricesOutsideCategory,
  createCustomModelFormFromPricing,
  emptyCustomModelForm,
  type CustomModelForm
} from './customProviderModelForm'
import {
  apiProtocolOptions,
  categoryFromModeOrModel,
  defaultProtocolsForModelCategory,
  findFirstModelCategory,
  formatCapabilitiesSummary,
  formatProviderCapability,
  getModelCategory,
  modelModeOptions,
  modelStatusOptions,
  modelVisibilityOptions,
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
const selectedModelCategory = ref<ModelCategoryKey>('text')
const modelModalOpen = ref(false)
const customModelModalOpen = ref(false)
const activeProvider = ref<ProviderDefinition | null>(null)
const editingCustomModelId = ref<string>()

const isManagementView = computed(() => route.meta.viewScope === 'admin')
const canCreateGlobalModel = computed(() => authState.isAdmin.value && isManagementView.value)
const customModelEditing = computed(() => Boolean(editingCustomModelId.value))

const providerColumns = computed(() => providerColumnsForScope(isManagementView.value))
const providerScrollX = computed(() => providerScrollXForScope(isManagementView.value))
const providerEmptyDescription = computed(() => providerEmptyDescriptionForScope(isManagementView.value))
const providerActions = computed<RowActionItem[]>(() => providerActionsForScope(isManagementView.value))

const customModelForm = reactive<CustomModelForm>({ ...emptyCustomModelForm })

const currentCategoryModels = computed(() => {
  const category = selectedModelCategory.value
  return providerModels.value.filter((item) => getModelCategory(item) === category)
})

const modelColumns = computed(() => buildProviderModelColumns(selectedModelCategory.value, currentCategoryModels.value))

const modelModalTitle = computed(() => activeProvider.value ? `${activeProvider.value.name} 模型目录` : '模型目录')
const customModelModalTitle = computed(() => customModelEditing.value ? '编辑自定义模型' : '新增自定义模型')
const customModelPricingCategory = computed<ModelCategoryKey>(() => categoryFromModeOrModel(customModelForm.mode, customModelForm.model))

const modelScopeOptions = computed(() => {
  const options = [{ label: '个人模型', value: 'personal' }]
  if (canCreateGlobalModel.value) {
    options.unshift({ label: '全局模型', value: 'global' })
  }
  return options
})

const pricingTemplateOptions = computed(() => buildPricingTemplateOptions(
  providerModels.value,
  customModelForm.model,
  customModelPricingCategory.value
))

const modelCategoryTabs = computed(() => buildModelCategoryTabs(providerModels.value))

const filteredModels = computed(() => filterProviderModelsByKeyword(currentCategoryModels.value, modelKeyword.value))

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
  modelLoading.value = true
  try {
    providerModels.value = await api.providers.models(provider.code, {
      includeMappingTargets: true,
      includeInactive: true,
      includeUnpriced: true
    })
    selectedModelCategory.value = findFirstModelCategory(providerModels.value)
  } catch (error) {
    console.error(error)
    providerModels.value = []
    message.error('加载模型价格失败')
  } finally {
    modelLoading.value = false
  }
}

function handleProviderAction(key: string, provider: ProviderDefinition) {
  if (key === 'models') {
    void openModelModal(provider)
  }
}

function resetModelModal() {
  activeProvider.value = null
  modelKeyword.value = ''
  selectedModelCategory.value = 'text'
  providerModels.value = []
  resetCustomModelForm()
}

function openCreateCustomModel() {
  if (!activeProvider.value) return
  resetCustomModelForm()
  customModelForm.scope = canCreateGlobalModel.value ? 'global' : 'personal'
  customModelForm.mode = selectedModelCategory.value
  customModelForm.supportedApiProtocols = defaultProtocolsForModelCategory(selectedModelCategory.value)
  customModelModalOpen.value = true
}

function openEditCustomModel(record: ProviderModelPricing) {
  if (!record.id || record.scope === 'built_in') return
  resetCustomModelForm()
  editingCustomModelId.value = record.id
  Object.assign(customModelForm, createCustomModelFormFromPricing(record, providerModels.value))
  customModelModalOpen.value = true
}

async function saveCustomModel() {
  if (!activeProvider.value) return
  const payload = buildCurrentCustomModelPayload()
  if (!payload) return
  customModelSaving.value = true
  try {
    if (editingCustomModelId.value) {
      await api.providers.updateModel(activeProvider.value.code, editingCustomModelId.value, payload)
      message.success('自定义模型已更新')
    } else {
      await api.providers.createModel(activeProvider.value.code, payload)
      message.success('自定义模型已创建')
    }
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
    await api.providers.deleteModel(activeProvider.value.code, record.id)
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
  providerModels.value = await api.providers.models(provider.code, {
    includeMappingTargets: true,
    includeInactive: true,
    includeUnpriced: true
  })
  selectedModelCategory.value = findFirstModelCategory(providerModels.value)
}

function resetCustomModelForm() {
  editingCustomModelId.value = undefined
  Object.assign(customModelForm, {
    ...emptyCustomModelForm,
    supportedApiProtocols: [...emptyCustomModelForm.supportedApiProtocols]
  })
}

function buildCurrentCustomModelPayload(): ProviderModelUpsertPayload | undefined {
  const payload = buildCustomModelUpsertPayload(customModelForm, customModelPricingCategory.value)
  if (!payload) {
    message.warning('请填写模型 ID')
    return undefined
  }
  return payload
}

function handleCustomModelModeChange() {
  const category = customModelPricingCategory.value
  customModelForm.supportedApiProtocols = defaultProtocolsForModelCategory(category)
  customModelForm.pricingTemplateModel = undefined
  clearCustomModelPricesOutsideCategory(customModelForm, category)
}

function handlePricingTemplateChange(value?: string) {
  applyPricingTemplateToCustomModelForm(customModelForm, providerModels.value, value)
}

function modelRowActions(record: ProviderModelPricing): RowActionItem[] {
  if (!canMutateModel(record)) return []
  return [
    { key: 'edit', label: '编辑', icon: 'edit', tone: 'info' },
    { key: 'delete', label: '删除', icon: 'delete', danger: true, confirmTitle: `确认删除模型 ${record.model}？`, confirmOkText: '删除' }
  ]
}

function handleModelAction(key: string, record: ProviderModelPricing) {
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
  if (activeProvider.value && record.providerCode !== activeProvider.value.code) return false
  if (record.scope === 'global') return canCreateGlobalModel.value
  return record.systemAccountId === authState.currentUser.value?.id
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
