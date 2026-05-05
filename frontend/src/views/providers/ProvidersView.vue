<template>
  <a-card class="page-card responsive-page-card">
    <ResponsiveListToolbar :show-search="false" :show-reset="false" :refresh-loading="loading" @refresh="loadProviders" />
    <ResponsiveDataList table-class="page-table provider-table" :columns="columns" :data-source="providers" row-key="code" :loading="loading" :scroll-x="1200" pull-refresh-enabled :refreshing="loading" @mobile-refresh="loadProviders">
      <template #emptyText>
        <a-empty class="page-empty-card" description="当前仅内置 OpenAI 供应商，后续新供应商会在这里扩展。" />
      </template>
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'code'">
          <span class="mono-cell">{{ record.code }}</span>
        </template>
        <template v-else-if="column.key === 'status'">
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
        <template v-else-if="column.key === 'actions'">
          <a-button type="link" size="small" @click="openModelModal(record)">查看模型</a-button>
        </template>
      </template>
      <template #card="{ record }">
        <article class="mobile-list-card">
          <div class="mobile-list-card-head">
            <div class="mobile-list-card-title">{{ record.name }}</div>
            <div class="mobile-list-card-tags">
              <a-tag class="mono-cell">{{ record.code }}</a-tag>
              <a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag>
            </div>
          </div>
          <div class="mobile-list-meta-grid">
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>账户类型</span>
              <strong>{{ record.accountTypes.join(' / ') }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>能力</span>
              <strong>{{ formatCapabilitiesSummary(record.capabilities) }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>说明</span>
              <strong>{{ record.description || '-' }}</strong>
            </div>
            <div class="mobile-list-meta-item mobile-list-meta-wide">
              <span>默认 Base URL</span>
              <strong class="mono-cell">{{ record.baseUrl }}</strong>
            </div>
          </div>
          <div class="mobile-list-card-actions single-action">
            <a-button type="primary" @click="openModelModal(record)">查看模型</a-button>
          </div>
        </article>
      </template>
    </ResponsiveDataList>

    <a-modal v-model:open="modelModalOpen" :title="modelModalTitle" width="1180px" :footer="null" @cancel="resetModelModal">
      <div class="model-toolbar">
        <a-input-search v-model:value="modelKeyword" allow-clear placeholder="搜索模型名称或类型" class="model-search" />
        <a-space wrap>
          <a-tag color="blue">{{ filteredModels.length }} / {{ providerModels.length }} 个模型</a-tag>
          <a-tag color="purple">价格单位：USD / 1M tokens</a-tag>
        </a-space>
      </div>
      <a-tabs v-model:activeKey="selectedModelType" class="model-tabs" size="small">
        <a-tab-pane v-for="tab in modelTypeTabs" :key="tab.key" :tab="tab.label" />
      </a-tabs>
      <a-table
        class="model-table"
        size="small"
        :columns="modelColumns"
        :data-source="filteredModels"
        row-key="model"
        :loading="modelLoading"
        :pagination="{ pageSize: 20, hideOnSinglePage: true, showSizeChanger: false }"
        :scroll="{ x: 1500 }"
      >
        <template #emptyText>
          <a-empty class="page-empty-card" description="这个供应商暂未配置模型价格。" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'model'">
            <span class="mono-cell">{{ record.model }}</span>
          </template>
          <template v-else-if="column.key === 'releaseDate'">
            <span>{{ record.releaseDate || '-' }}</span>
          </template>
          <template v-else-if="column.key === 'mode'">
            <a-tag>{{ formatModelMode(record.mode) }}</a-tag>
          </template>
          <template v-else-if="column.key === 'prices'">
            <div class="price-cell">
              <span>输入 {{ formatPrice(record.inputUsdPer1M) }}</span>
              <span>输出 {{ formatPrice(record.outputUsdPer1M) }}</span>
              <span>缓存读 {{ formatPrice(record.cachedInputUsdPer1M) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'cacheWrite'">
            <div class="price-cell">
              <span>写入 {{ formatPrice(record.cacheWriteUsdPer1M) }}</span>
              <span>1h {{ formatPrice(record.cacheWrite1hUsdPer1M) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'imageTokenPrice'">
            <span>{{ formatPrice(record.imageOutputUsdPer1M) }}</span>
          </template>
          <template v-else-if="column.key === 'imageUnitPrice'">
            <span>{{ formatUnitPrice(record.outputUsdPerImage) }}</span>
          </template>
          <template v-else-if="column.key === 'context'">
            <div class="price-cell">
              <span>输入 {{ formatTokens(record.maxInputTokens) }}</span>
              <span>输出 {{ formatTokens(record.maxOutputTokens) }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'features'">
            <a-space wrap>
              <a-tag v-if="record.supportsPromptCaching" color="green">缓存</a-tag>
              <a-tag v-if="record.supportsServiceTier" color="gold">service tier</a-tag>
              <span v-if="!record.supportsPromptCaching && !record.supportsServiceTier" class="muted-text">-</span>
            </a-space>
          </template>
        </template>
      </a-table>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { message } from 'ant-design-vue'
import { computed, onMounted, ref } from 'vue'

import { api } from '@/api/client'
import ResponsiveDataList from '@/components/ResponsiveDataList.vue'
import ResponsiveListToolbar from '@/components/ResponsiveListToolbar.vue'
import type { ProviderDefinition, ProviderModelPricing } from '@/types/domain'

const modelTypeOrder = ['chat', 'responses', 'image_generation', 'audio_speech', 'audio_transcription', 'other'] as const
type ModelTypeKey = typeof modelTypeOrder[number]

const loading = ref(false)
const modelLoading = ref(false)
const providers = ref<ProviderDefinition[]>([])
const providerModels = ref<ProviderModelPricing[]>([])
const modelKeyword = ref('')
const selectedModelType = ref<ModelTypeKey>('chat')
const modelModalOpen = ref(false)
const activeProvider = ref<ProviderDefinition | null>(null)

const modelTypeLabels: Record<ModelTypeKey, string> = {
  chat: '对话',
  responses: 'Responses',
  image_generation: '图像',
  audio_speech: '语音合成',
  audio_transcription: '语音转写',
  other: '其他'
}

const hiddenProviderCapabilities = new Set(['passthrough'])

const providerCapabilityLabels: Record<string, string> = {
  models: '模型',
  responses: 'Responses',
  stream: '流式'
}

const columns = [
  { title: '编码', dataIndex: 'code', key: 'code', width: 120 },
  { title: '名称', dataIndex: 'name', key: 'name', width: 160 },
  { title: '状态', key: 'status', width: 90 },
  { title: '账户类型', key: 'accountTypes', width: 180 },
  { title: '能力', key: 'capabilities', width: 360 },
  { title: '默认 Base URL', dataIndex: 'baseUrl', key: 'baseUrl', width: 240 },
  { title: '说明', dataIndex: 'description', key: 'description', width: 200 },
  { title: '操作', key: 'actions', fixed: 'right', width: 120 }
]

const baseModelColumns = [
  { title: '模型', key: 'model', width: 260 },
  { title: '版本日期', key: 'releaseDate', width: 120 },
  { title: '类型', key: 'mode', width: 110 },
  { title: 'Token 价格', key: 'prices', width: 230 },
  { title: '缓存写入', key: 'cacheWrite', width: 180 },
  { title: '图片 token 价格', key: 'imageTokenPrice', width: 170 },
  { title: '每张价格', key: 'imageUnitPrice', width: 130 },
  { title: '上下文', key: 'context', width: 180 },
  { title: '能力', key: 'features', width: 180 }
]

const currentTabModels = computed(() => {
  const selectedType = selectedModelType.value
  return providerModels.value.filter((item) => getModelTypeKey(item.mode) === selectedType)
})

const modelColumns = computed(() => {
  const rows = currentTabModels.value
  const visibleKeys = new Set(['model', 'releaseDate', 'mode', 'features'])

  if (rows.some((item) => hasAnyNumber(item.inputUsdPer1M, item.outputUsdPer1M, item.cachedInputUsdPer1M))) {
    visibleKeys.add('prices')
  }
  if (rows.some((item) => hasAnyNumber(item.cacheWriteUsdPer1M, item.cacheWrite1hUsdPer1M))) {
    visibleKeys.add('cacheWrite')
  }
  if (rows.some((item) => typeof item.imageOutputUsdPer1M === 'number')) {
    visibleKeys.add('imageTokenPrice')
  }
  if (rows.some((item) => typeof item.outputUsdPerImage === 'number')) {
    visibleKeys.add('imageUnitPrice')
  }
  if (rows.some((item) => hasAnyNumber(item.maxInputTokens, item.maxOutputTokens))) {
    visibleKeys.add('context')
  }

  return baseModelColumns.filter((column) => visibleKeys.has(column.key))
})

const modelModalTitle = computed(() => activeProvider.value ? `${activeProvider.value.name} 模型价格` : '模型价格')

const modelTypeTabs = computed(() => {
  const counts = new Map<ModelTypeKey, number>()
  for (const key of modelTypeOrder) {
    counts.set(key, 0)
  }

  for (const item of providerModels.value) {
    const key = getModelTypeKey(item.mode)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }

  return modelTypeOrder
    .filter((key) => (counts.get(key) ?? 0) > 0)
    .map((key) => ({
      key,
      label: `${modelTypeLabels[key]} (${counts.get(key) ?? 0})`
    }))
})

const filteredModels = computed(() => {
  const keyword = modelKeyword.value.trim().toLowerCase()
  return currentTabModels.value.filter((item) => {
    const keywordMatches = !keyword
      || item.model.toLowerCase().includes(keyword)
      || formatModelMode(item.mode).toLowerCase().includes(keyword)
    return keywordMatches
  })
})

function hasAnyNumber(...values: Array<number | undefined>) {
  return values.some((value) => typeof value === 'number')
}

async function loadProviders() {
  loading.value = true
  try {
    providers.value = await api.providers.list()
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
  selectedModelType.value = 'chat'
  modelLoading.value = true
  try {
    providerModels.value = await api.providers.models(provider.code)
    selectedModelType.value = findFirstModelType(providerModels.value)
  } catch (error) {
    console.error(error)
    providerModels.value = []
    message.error('加载模型价格失败')
  } finally {
    modelLoading.value = false
  }
}

function resetModelModal() {
  activeProvider.value = null
  modelKeyword.value = ''
  selectedModelType.value = 'chat'
  providerModels.value = []
}

function findFirstModelType(models: ProviderModelPricing[]): ModelTypeKey {
  for (const key of modelTypeOrder) {
    if (models.some((item) => getModelTypeKey(item.mode) === key)) {
      return key
    }
  }
  return 'other'
}

function getModelTypeKey(mode?: string): ModelTypeKey {
  switch ((mode ?? '').trim()) {
    case 'chat':
    case 'responses':
    case 'image_generation':
    case 'audio_speech':
    case 'audio_transcription':
      return mode as ModelTypeKey
    default:
      return 'other'
  }
}

function formatModelMode(mode?: string) {
  return modelTypeLabels[getModelTypeKey(mode)]
}

function visibleProviderCapabilities(capabilities: string[]) {
  return capabilities.filter((capability) => !hiddenProviderCapabilities.has(capability))
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

function formatTokens(value?: number) {
  if (typeof value !== 'number') return '-'
  if (value >= 1_000_000) return `${trimNumber(value / 1_000_000)}M`
  if (value >= 1_000) return `${trimNumber(value / 1_000)}K`
  return String(value)
}

function trimNumber(value: number) {
  return Number(value.toFixed(8)).toString()
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

.model-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.model-tabs {
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

.price-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  line-height: 1.5;
}

.muted-text {
  color: rgba(0, 0, 0, 0.45);
}

@media (max-width: 768px) {
  .model-toolbar {
    align-items: stretch;
    flex-direction: column;
  }

  .model-search {
    width: 100%;
  }
}
</style>
