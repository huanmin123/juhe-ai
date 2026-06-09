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
import type {
  CustomProviderModelScope,
  ProviderDefinition,
  ProviderModelApiProtocol,
  ProviderModelMode,
  ProviderModelPricing,
  ProviderModelStatus,
  ProviderModelUpsertPayload,
  ProviderModelVisibility
} from '@/types/domain'

const modelCategoryOrder = ['text', 'image', 'audio'] as const
type ModelCategoryKey = typeof modelCategoryOrder[number]
type DirectPriceFieldKey =
  | 'inputUsdPer1M'
  | 'outputUsdPer1M'
  | 'cachedInputUsdPer1M'
  | 'cacheWriteUsdPer1M'
  | 'imageInputUsdPer1M'
  | 'imageOutputUsdPer1M'
  | 'audioInputUsdPer1M'
  | 'audioOutputUsdPer1M'
  | 'outputUsdPerImage'

interface CustomModelForm {
  id?: string
  model: string
  scope: CustomProviderModelScope
  status: ProviderModelStatus
  visibility: ProviderModelVisibility
  displayName?: string
  mode: ProviderModelMode
  supportedApiProtocols: ProviderModelApiProtocol[]
  pricingTemplateModel?: string
  releaseDate?: string
  shutdownDate?: string
  contextWindowTokens?: number
  maxOutputTokens?: number
  inputUsdPer1M?: number
  outputUsdPer1M?: number
  cachedInputUsdPer1M?: number
  cacheWriteUsdPer1M?: number
  imageInputUsdPer1M?: number
  imageOutputUsdPer1M?: number
  audioInputUsdPer1M?: number
  audioOutputUsdPer1M?: number
  outputUsdPerImage?: number
}

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

const modelCategoryLabels: Record<ModelCategoryKey, string> = {
  text: '对话 / 编码',
  image: '图像',
  audio: '音频'
}

const apiProtocolLabels: Record<string, string> = {
  chat_completions: 'Chat Completions',
  responses: 'Responses',
  completions: 'Completions',
  images: 'Images API',
  audio: 'Audio API',
  realtime: 'Realtime API'
}

const hiddenProviderCapabilities = new Set(['models', 'passthrough', 'stream'])

const providerCapabilityLabels: Record<string, string> = {
  responses: 'Responses',
  chat: 'Chat',
  chat_completions: 'Chat'
}

const providerCapabilityOrder = ['responses', 'chat'] as const

const managementProviderColumns = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 160 },
  { title: '状态', key: 'status', width: 90 },
  { title: '账户类型', key: 'accountTypes', width: 180 },
  { title: '接口能力', key: 'capabilities', width: 280 },
  { title: '默认 Base URL', dataIndex: 'baseUrl', key: 'baseUrl', width: 240 },
  { title: '默认测试模型', dataIndex: 'defaultTestModel', key: 'defaultTestModel', width: 160 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

const selfProviderColumns = [
  { title: '模型目录', dataIndex: 'name', key: 'name', width: 180 },
  { title: '状态', key: 'status', width: 90 },
  { title: '接口能力', key: 'capabilities', width: 260 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 260 },
  { title: '操作', key: 'actions', fixed: 'right' }
]

const providerColumns = computed(() => isManagementView.value ? managementProviderColumns : selfProviderColumns)
const providerScrollX = computed(() => isManagementView.value ? 1320 : 850)
const providerEmptyDescription = computed(() => isManagementView.value
  ? '当前内置 OpenAI 兼容与 GPT 供应商，后续新供应商会在这里扩展。'
  : '当前没有可用模型目录。'
)
const providerActions = computed<RowActionItem[]>(() => [
  { key: 'models', label: isManagementView.value ? '模型目录' : '查看模型', icon: 'detail', tone: 'info' }
])

const baseModelColumns = [
  { title: '模型', key: 'model', width: 260 },
  { title: '范围', key: 'scope', width: 100 },
  { title: '状态', key: 'status', width: 90 },
  { title: '可见性', key: 'visibility', width: 130 },
  { title: '发布时间', key: 'releaseDate', width: 120 },
  { title: '用途', key: 'category', width: 120 },
  { title: '接口协议', key: 'protocols', width: 230 },
  { title: '计费', key: 'prices', width: 230 },
  { title: '缓存写入', key: 'cacheWrite', width: 180 },
  { title: '图片 token 价格', key: 'imageTokenPrice', width: 180 },
  { title: '音频 token 价格', key: 'audioTokenPrice', width: 180 },
  { title: '每张价格', key: 'imageUnitPrice', width: 130 },
  { title: '上下文', key: 'context', width: 180 },
  { title: '操作', key: 'actions', width: 86, fixed: 'right' }
]

const emptyCustomModelForm: CustomModelForm = {
  model: '',
  scope: 'personal',
  status: 'active',
  visibility: 'public',
  mode: 'text',
  supportedApiProtocols: ['responses', 'chat_completions']
}

const customModelForm = reactive<CustomModelForm>({ ...emptyCustomModelForm })

const modelStatusOptions = [
  { label: '启用', value: 'active' },
  { label: '草稿', value: 'draft' },
  { label: '停用', value: 'disabled' }
]

const modelVisibilityOptions = [
  { label: '公开目录', value: 'public' },
  { label: '仅映射目标', value: 'mapping_target_only' }
]

const modelModeOptions = [
  { label: '对话 / 编码', value: 'text' },
  { label: '图像', value: 'image' },
  { label: '音频', value: 'audio' }
]

const apiProtocolOptions = Object.entries(apiProtocolLabels).map(([value, label]) => ({ value, label }))

const directPriceFieldKeys: DirectPriceFieldKey[] = [
  'inputUsdPer1M',
  'outputUsdPer1M',
  'cachedInputUsdPer1M',
  'cacheWriteUsdPer1M',
  'imageInputUsdPer1M',
  'imageOutputUsdPer1M',
  'audioInputUsdPer1M',
  'audioOutputUsdPer1M',
  'outputUsdPerImage'
]

const directPriceFieldsByCategory: Record<ModelCategoryKey, DirectPriceFieldKey[]> = {
  text: ['inputUsdPer1M', 'outputUsdPer1M', 'cachedInputUsdPer1M', 'cacheWriteUsdPer1M'],
  image: ['imageInputUsdPer1M', 'imageOutputUsdPer1M', 'outputUsdPerImage'],
  audio: ['audioInputUsdPer1M', 'audioOutputUsdPer1M']
}

const currentCategoryModels = computed(() => {
  const category = selectedModelCategory.value
  return providerModels.value.filter((item) => getModelCategory(item) === category)
})

const modelColumns = computed(() => {
  const rows = currentCategoryModels.value
  const visibleKeys = new Set(['model', 'scope', 'status', 'visibility', 'releaseDate', 'category', 'protocols', 'actions'])

  if (selectedModelCategory.value === 'text') {
    visibleKeys.add('prices')
    visibleKeys.add('cacheWrite')
  }
  if (selectedModelCategory.value === 'image') {
    visibleKeys.add('imageTokenPrice')
    visibleKeys.add('imageUnitPrice')
  }
  if (selectedModelCategory.value === 'audio') {
    visibleKeys.add('audioTokenPrice')
  }
  if (rows.some((item) => hasAnyNumber(item.maxInputTokens, item.contextWindowTokens, item.maxOutputTokens))) {
    visibleKeys.add('context')
  }

  return baseModelColumns.filter((column) => visibleKeys.has(column.key))
})

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

const pricingTemplateOptions = computed(() => providerModels.value
  .filter((item) => item.model.toLowerCase() !== customModelForm.model.trim().toLowerCase())
  .filter((item) => getModelCategory(item) === customModelPricingCategory.value)
  .filter((item) => (item.status ?? 'active') === 'active')
  .filter((item) => !item.pricingModel && hasDirectModelPrice(item))
  .map((item) => ({
    value: item.model,
    label: `${item.model}${item.scope === 'built_in' ? '（内置）' : item.scope === 'global' ? '（全局）' : '（个人）'}`
  })))

const modelCategoryTabs = computed(() => {
  const counts = new Map<ModelCategoryKey, number>()
  for (const key of modelCategoryOrder) {
    counts.set(key, 0)
  }

  for (const item of providerModels.value) {
    const key = getModelCategory(item)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }

  return modelCategoryOrder
    .filter((key) => (counts.get(key) ?? 0) > 0)
    .map((key) => ({
      key,
      label: `${modelCategoryLabels[key]} (${counts.get(key) ?? 0})`
    }))
})

const filteredModels = computed(() => {
  const keyword = modelKeyword.value.trim().toLowerCase()
  return currentCategoryModels.value.filter((item) => {
    const keywordMatches = !keyword
      || item.model.toLowerCase().includes(keyword)
      || formatModelCategory(item).toLowerCase().includes(keyword)
      || (item.supportedApiProtocols ?? []).some((protocol) => formatApiProtocol(protocol).toLowerCase().includes(keyword))
    return keywordMatches
  })
})

function hasAnyNumber(...values: Array<number | undefined>) {
  return values.some((value) => typeof value === 'number')
}

function hasDirectModelPrice(item: ProviderModelPricing) {
  return hasAnyNumber(
    item.inputUsdPer1M,
    item.outputUsdPer1M,
    item.cachedInputUsdPer1M,
    item.cacheWriteUsdPer1M,
    item.imageInputUsdPer1M,
    item.imageOutputUsdPer1M,
    item.audioInputUsdPer1M,
    item.audioOutputUsdPer1M,
    item.outputUsdPerImage
  )
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
  customModelForm.id = record.id
  customModelForm.model = record.model
  customModelForm.scope = record.scope === 'global' ? 'global' : 'personal'
  customModelForm.status = record.status ?? 'active'
  customModelForm.visibility = record.visibility ?? 'public'
  customModelForm.displayName = record.displayName
  customModelForm.mode = categoryFromModeOrModel(record.mode, record.model)
  customModelForm.supportedApiProtocols = [...(record.supportedApiProtocols ?? [])]
  customModelForm.releaseDate = record.releaseDate
  customModelForm.shutdownDate = record.shutdownDate
  customModelForm.contextWindowTokens = record.contextWindowTokens
  customModelForm.maxOutputTokens = record.maxOutputTokens
  customModelForm.inputUsdPer1M = record.inputUsdPer1M
  customModelForm.outputUsdPer1M = record.outputUsdPer1M
  customModelForm.cachedInputUsdPer1M = record.cachedInputUsdPer1M
  customModelForm.cacheWriteUsdPer1M = record.cacheWriteUsdPer1M
  customModelForm.imageInputUsdPer1M = record.imageInputUsdPer1M
  customModelForm.imageOutputUsdPer1M = record.imageOutputUsdPer1M
  customModelForm.audioInputUsdPer1M = record.audioInputUsdPer1M
  customModelForm.audioOutputUsdPer1M = record.audioOutputUsdPer1M
  customModelForm.outputUsdPerImage = record.outputUsdPerImage
  clearCustomModelPricesOutsideCategory(customModelPricingCategory.value)
  if (record.pricingModel) {
    applyPricingTemplate(record.pricingModel)
  }
  customModelModalOpen.value = true
}

async function saveCustomModel() {
  if (!activeProvider.value) return
  const payload = buildCustomModelPayload()
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

function buildCustomModelPayload(): ProviderModelUpsertPayload | undefined {
  const model = customModelForm.model.trim()
  if (!model) {
    message.warning('请填写模型 ID')
    return undefined
  }
  return {
    model,
    scope: customModelForm.scope,
    status: customModelForm.status,
    visibility: customModelForm.visibility,
    displayName: trimToNull(customModelForm.displayName),
    mode: customModelForm.mode,
    supportedApiProtocols: [...customModelForm.supportedApiProtocols],
    pricingModel: null,
    releaseDate: trimToNull(customModelForm.releaseDate),
    shutdownDate: trimToNull(customModelForm.shutdownDate),
    contextWindowTokens: numberToNull(customModelForm.contextWindowTokens),
    maxOutputTokens: numberToNull(customModelForm.maxOutputTokens),
    ...buildCustomModelDirectPricePayload(customModelPricingCategory.value)
  }
}

function handleCustomModelModeChange() {
  const category = customModelPricingCategory.value
  customModelForm.supportedApiProtocols = defaultProtocolsForModelCategory(category)
  customModelForm.pricingTemplateModel = undefined
  clearCustomModelPricesOutsideCategory(category)
}

function handlePricingTemplateChange(value?: string) {
  applyPricingTemplate(value)
}

function buildCustomModelDirectPricePayload(category: ModelCategoryKey) {
  const visibleFields = new Set(directPriceFieldsByCategory[category])
  const payload: Partial<Record<DirectPriceFieldKey, number | null>> = {}
  for (const field of directPriceFieldKeys) {
    payload[field] = !visibleFields.has(field)
      ? null
      : numberToNull(customModelForm[field])
  }
  return payload
}

function applyPricingTemplate(model?: string) {
  const templateModel = trimToUndefined(model)
  if (!templateModel) return
  const template = findProviderModelByName(templateModel)
  if (!template) return
  const category = customModelPricingCategory.value
  if (getModelCategory(template) !== category) return
  const visibleFields = new Set(directPriceFieldsByCategory[category])
  for (const field of directPriceFieldKeys) {
    customModelForm[field] = visibleFields.has(field) ? template[field] : undefined
  }
}

function findProviderModelByName(model: string): ProviderModelPricing | undefined {
  const normalized = model.trim().toLowerCase()
  return providerModels.value.find((item) => item.model.trim().toLowerCase() === normalized)
}

function clearCustomModelPricesOutsideCategory(category: ModelCategoryKey) {
  const visibleFields = new Set(directPriceFieldsByCategory[category])
  for (const field of directPriceFieldKeys) {
    if (!visibleFields.has(field)) {
      customModelForm[field] = undefined
    }
  }
}

function defaultProtocolsForModelCategory(category: ModelCategoryKey): ProviderModelApiProtocol[] {
  if (category === 'image') return ['images']
  if (category === 'audio') return ['audio']
  return ['responses', 'chat_completions']
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

function findFirstModelCategory(models: ProviderModelPricing[]): ModelCategoryKey {
  for (const key of modelCategoryOrder) {
    if (models.some((item) => getModelCategory(item) === key)) {
      return key
    }
  }
  return 'text'
}

function getModelCategory(item: ProviderModelPricing): ModelCategoryKey {
  return categoryFromModeOrModel(item.mode, item.model)
}

function categoryFromModeOrModel(modeValue: string | undefined, modelValue: string): ModelCategoryKey {
  const model = modelValue.toLowerCase()
  const mode = (modeValue ?? '').trim().toLowerCase()

  if (isModelCategoryKey(mode)) {
    return mode
  }

  if (mode === 'image_generation' || model.startsWith('gpt-image') || model.startsWith('dall-e')) {
    return 'image'
  }

  if (
    mode === 'audio_speech'
    || mode === 'audio_transcription'
    || model.includes('audio')
    || model.includes('realtime')
    || model.includes('transcribe')
    || model.includes('tts')
    || model.includes('whisper')
  ) {
    return 'audio'
  }

  if (
    mode === 'chat'
    || mode === 'responses'
    || mode === 'completion'
    || model.includes('codex')
    || model.startsWith('gpt-')
    || model.startsWith('o')
  ) {
    return 'text'
  }

  return 'text'
}

function isModelCategoryKey(value: string): value is ModelCategoryKey {
  return (modelCategoryOrder as readonly string[]).includes(value)
}

function formatModelCategory(item: ProviderModelPricing) {
  return modelCategoryLabels[getModelCategory(item)]
}

function formatModelScope(scope?: string) {
  if (scope === 'built_in') return '内置'
  if (scope === 'global') return '全局'
  if (scope === 'personal') return '个人'
  return '-'
}

function modelScopeColor(scope?: string) {
  if (scope === 'built_in') return 'blue'
  if (scope === 'global') return 'purple'
  if (scope === 'personal') return 'green'
  return 'default'
}

function formatModelStatus(status?: string) {
  if (status === 'active') return '启用'
  if (status === 'draft') return '草稿'
  if (status === 'disabled') return '停用'
  return '-'
}

function modelStatusColor(status?: string) {
  if (status === 'active') return 'green'
  if (status === 'draft') return 'gold'
  if (status === 'disabled') return 'default'
  return 'default'
}

function formatModelVisibility(visibility?: string) {
  if (visibility === 'public') return '公开目录'
  if (visibility === 'mapping_target_only') return '仅映射目标'
  return '-'
}

function formatApiProtocol(protocol?: string) {
  return apiProtocolLabels[protocol ?? ''] ?? protocol ?? '-'
}

function getApiProtocolTagColor(protocol?: string) {
  switch (protocol) {
    case 'chat_completions':
      return 'blue'
    case 'responses':
      return 'purple'
    case 'images':
      return 'cyan'
    case 'audio':
      return 'green'
    case 'realtime':
      return 'orange'
    default:
      return 'default'
  }
}

function visibleProviderCapabilities(capabilities: string[]) {
  const normalized = new Set<string>()
  for (const capability of capabilities) {
    if (capability === 'chat_completions' || capability === 'passthrough') {
      normalized.add('chat')
      continue
    }
    if (!hiddenProviderCapabilities.has(capability)) {
      normalized.add(capability)
    }
  }
  return [...normalized].sort((left, right) => {
    const leftIndex = providerCapabilityOrder.indexOf(left as typeof providerCapabilityOrder[number])
    const rightIndex = providerCapabilityOrder.indexOf(right as typeof providerCapabilityOrder[number])
    if (leftIndex !== -1 || rightIndex !== -1) {
      return (leftIndex === -1 ? providerCapabilityOrder.length : leftIndex) - (rightIndex === -1 ? providerCapabilityOrder.length : rightIndex)
    }
    return left.localeCompare(right)
  })
}

function formatProviderCapability(capability: string) {
  return providerCapabilityLabels[capability] ?? capability
}

function formatCapabilitiesSummary(capabilities: string[]) {
  const visibleCapabilities = visibleProviderCapabilities(capabilities)
  return visibleCapabilities.length ? visibleCapabilities.map(formatProviderCapability).join(' / ') : '-'
}

function formatPrice(value?: number) {
  return typeof value === 'number' ? `$${trimNumber(value)}` : '-'
}

function formatUnitPrice(value?: number) {
  return typeof value === 'number' ? `$${trimNumber(value)}` : '-'
}

function formatModelPriceSummary(item: ProviderModelPricing) {
  if (item.pricingModel) return `计价 ${item.pricingModel}`
  const category = getModelCategory(item)
  if (category === 'image') {
    return [
      `图片输入 ${formatPrice(item.imageInputUsdPer1M)}`,
      `图片输出 ${formatPrice(item.imageOutputUsdPer1M)}`,
      `每张 ${formatUnitPrice(item.outputUsdPerImage)}`
    ].join(' / ')
  }
  if (category === 'audio') {
    return [
      `音频输入 ${formatPrice(item.audioInputUsdPer1M)}`,
      `音频输出 ${formatPrice(item.audioOutputUsdPer1M)}`
    ].join(' / ')
  }
  return [
    `输入 ${formatPrice(item.inputUsdPer1M)}`,
    `输出 ${formatPrice(item.outputUsdPer1M)}`,
    `缓存读 ${formatPrice(item.cachedInputUsdPer1M)}`
  ].join(' / ')
}

function formatTokens(value?: number) {
  if (typeof value !== 'number') return '-'
  if (value >= 1_000_000) return `${trimNumber(value / 1_000_000)}M`
  if (value >= 1_000) return `${trimNumber(value / 1_000)}K`
  return String(value)
}

function formatModelInputTokens(item: ProviderModelPricing) {
  return formatTokens(item.maxInputTokens ?? item.contextWindowTokens)
}

function trimNumber(value: number) {
  return Number(value.toFixed(8)).toString()
}

function trimToUndefined(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value.trim() : undefined
}

function trimToNull(value: unknown): string | null {
  return trimToUndefined(value) ?? null
}

function numberToNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
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
