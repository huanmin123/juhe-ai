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

    <a-modal v-model:open="modelModalOpen" :title="modelModalTitle" width="1180px" wrap-class-name="model-price-modal-wrap" :footer="null" @cancel="resetModelModal">
      <div class="model-modal-content">
        <div class="model-toolbar">
          <a-input-search v-model:value="modelKeyword" allow-clear placeholder="搜索模型名称、用途或接口协议" class="model-search" />
          <a-space wrap>
            <a-button type="primary" @click="openCreateCustomModel">新增模型</a-button>
            <a-tag color="blue">{{ filteredModels.length }} / {{ currentCategoryModels.length }} 个模型</a-tag>
            <a-tag color="purple">Token：USD / 1M；图片：USD / 张</a-tag>
          </a-space>
        </div>
        <a-tabs v-model:activeKey="selectedModelCategory" class="model-tabs" size="small">
          <a-tab-pane v-for="tab in modelCategoryTabs" :key="tab.key" :tab="tab.label" />
        </a-tabs>
        <ResponsiveDataList
          class="model-table"
          table-class="model-table"
          size="small"
          :columns="modelColumns"
          :data-source="filteredModels"
          row-key="model"
          :loading="modelLoading"
          :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
          :scroll-x="1500"
          :lock-body-scroll="false"
        >
          <template #emptyText>
            <a-empty class="page-empty-card" description="这个供应商暂未配置模型价格。" />
          </template>
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'model'">
              <a-space size="small">
                <span class="mono-cell">{{ record.model }}</span>
                <a-tag v-if="record.displayName">{{ record.displayName }}</a-tag>
                <a-tag v-if="record.shutdownDate" color="orange">将停用 {{ record.shutdownDate }}</a-tag>
              </a-space>
            </template>
            <template v-else-if="column.key === 'scope'">
              <a-tag :color="modelScopeColor(record.scope)">{{ formatModelScope(record.scope) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'status'">
              <a-tag :color="modelStatusColor(record.status)">{{ formatModelStatus(record.status) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'visibility'">
              <a-tag :color="record.visibility === 'mapping_target_only' ? 'orange' : 'green'">{{ formatModelVisibility(record.visibility) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'releaseDate'">
              <span>{{ record.releaseDate || '-' }}</span>
            </template>
            <template v-else-if="column.key === 'category'">
              <a-tag>{{ formatModelCategory(record) }}</a-tag>
            </template>
            <template v-else-if="column.key === 'protocols'">
              <a-space wrap size="small">
                <a-tag v-for="protocol in record.supportedApiProtocols" :key="protocol" :color="getApiProtocolTagColor(protocol)">{{ formatApiProtocol(protocol) }}</a-tag>
                <span v-if="!record.supportedApiProtocols?.length" class="muted-text">-</span>
              </a-space>
            </template>
            <template v-else-if="column.key === 'prices'">
              <div class="price-cell">
                <span v-if="record.pricingModel">计价 {{ record.pricingModel }}</span>
                <template v-else>
                  <span>输入 {{ formatPrice(record.inputUsdPer1M) }}</span>
                  <span>输出 {{ formatPrice(record.outputUsdPer1M) }}</span>
                  <span>缓存读 {{ formatPrice(record.cachedInputUsdPer1M) }}</span>
                </template>
              </div>
            </template>
            <template v-else-if="column.key === 'cacheWrite'">
              <div class="price-cell">
                <span>写入 {{ formatPrice(record.cacheWriteUsdPer1M) }}</span>
                <span>1h {{ formatPrice(record.cacheWrite1hUsdPer1M) }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'imageTokenPrice'">
              <div class="price-cell">
                <span>输入 {{ formatPrice(record.imageInputUsdPer1M) }}</span>
                <span>输出 {{ formatPrice(record.imageOutputUsdPer1M) }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'audioTokenPrice'">
              <div class="price-cell">
                <span>输入 {{ formatPrice(record.audioInputUsdPer1M) }}</span>
                <span>输出 {{ formatPrice(record.audioOutputUsdPer1M) }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'imageUnitPrice'">
              <span>{{ formatUnitPrice(record.outputUsdPerImage) }}</span>
            </template>
            <template v-else-if="column.key === 'context'">
              <div class="price-cell">
                <span>输入 {{ formatModelInputTokens(record) }}</span>
                <span>输出 {{ formatTokens(record.maxOutputTokens) }}</span>
              </div>
            </template>
            <template v-else-if="column.key === 'actions'">
              <RowActions :actions="modelRowActions(record)" @action-click="handleModelAction($event, record)" />
            </template>
          </template>
          <template #card="{ record }">
            <article class="model-mobile-card">
              <div class="model-mobile-card-head">
                <strong class="mono-cell">{{ record.model }}</strong>
                <a-space size="small" wrap>
                  <a-tag>{{ formatModelCategory(record) }}</a-tag>
                  <a-tag :color="modelStatusColor(record.status)">{{ formatModelStatus(record.status) }}</a-tag>
                </a-space>
              </div>
              <div class="model-mobile-card-grid">
                <span>来源</span>
                <strong>{{ formatModelScope(record.scope) }}</strong>
                <span>可见性</span>
                <strong>{{ formatModelVisibility(record.visibility) }}</strong>
                <span>发布时间</span>
                <strong>{{ record.releaseDate || '-' }}</strong>
                <span>接口协议</span>
                <strong>{{ (record.supportedApiProtocols ?? []).map(formatApiProtocol).join(' / ') || '-' }}</strong>
                <span>价格</span>
                <strong>{{ formatModelPriceSummary(record) }}</strong>
                <span>上下文</span>
                <strong>{{ formatModelInputTokens(record) }} / {{ formatTokens(record.maxOutputTokens) }}</strong>
              </div>
              <RowActions v-if="modelRowActions(record).length" variant="button" :actions="modelRowActions(record)" @action-click="handleModelAction($event, record)" />
            </article>
          </template>
        </ResponsiveDataList>
      </div>
    </a-modal>

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
  formatApiProtocol,
  formatCapabilitiesSummary,
  formatModelCategory,
  formatModelInputTokens,
  formatModelPriceSummary,
  formatModelScope,
  formatModelStatus,
  formatModelVisibility,
  formatPrice,
  formatProviderCapability,
  formatTokens,
  formatUnitPrice,
  getApiProtocolTagColor,
  getModelCategory,
  modelModeOptions,
  modelScopeColor,
  modelStatusColor,
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
.provider-table :deep(.ant-empty),
.model-table :deep(.ant-empty) {
  margin: 12px 0;
}

.provider-table :deep(.ant-table-cell),
.model-table :deep(.ant-table-cell) {
  white-space: nowrap;
}

:global(.model-price-modal-wrap .ant-modal) {
  top: 30px;
  max-width: calc(100vw - 60px);
  padding-bottom: 30px;
}

:global(.model-price-modal-wrap .ant-modal-body) {
  max-height: none;
  overflow: hidden;
}

.model-modal-content {
  display: flex;
  height: calc(100dvh - 156px);
  min-height: 0;
  flex-direction: column;
}

.model-toolbar {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.model-tabs {
  flex: 0 0 auto;
  margin-bottom: 8px;
}

.model-tabs :deep(.ant-tabs-content-holder) {
  display: none;
}

.model-tabs :deep(.ant-tabs-nav) {
  margin-bottom: 0;
}

.model-search {
  width: 320px;
}

.model-table {
  min-height: 0;
  flex: 1 1 auto;
}

.price-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  line-height: 1.5;
}

.model-mobile-card {
  display: grid;
  gap: 10px;
  padding: 12px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
}

.model-mobile-card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
}

.model-mobile-card-grid {
  display: grid;
  grid-template-columns: minmax(76px, auto) minmax(0, 1fr);
  gap: 6px 10px;
  color: #64748b;
  font-size: 12px;
}

.model-mobile-card-grid strong {
  min-width: 0;
  overflow: hidden;
  color: #0f172a;
  text-overflow: ellipsis;
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
  :global(.model-price-modal-wrap .ant-modal) {
    top: 12px;
    max-width: calc(100vw - 24px);
    padding-bottom: 12px;
  }

  .model-modal-content {
    height: calc(100dvh - 140px);
  }

  .model-toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .model-search {
    width: 100%;
  }

  .custom-model-grid {
    grid-template-columns: 1fr;
  }
}
</style>
